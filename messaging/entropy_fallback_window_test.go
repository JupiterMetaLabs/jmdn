package messaging

// Tests for entropy_fallback_window.go — the count-based fallback signer
// collection (Architecture §4.2a as amended 2026-08-24).
//
// The most important tests here are the three-outcome ones: FallbackSeedForEpoch
// must distinguish "not ready yet, keep waiting" from "deadline exceeded, give
// up" from "here is the seed" — collapsing any two of those into one outcome
// is what made the fallback path unable to ever succeed before this amendment.

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"
)

func resetAggSigStore(t *testing.T) {
	t.Helper()
	saved := defaultAggSigStore
	defaultAggSigStore = &aggSigStore{sigs: make(map[uint64][]byte)}
	t.Cleanup(func() { defaultAggSigStore = saved })
}

func testAggSig(b byte) []byte {
	s := make([]byte, randao.AggSigLen)
	for i := range s {
		s[i] = b
	}
	return s
}

func mustRecordAggSig(t *testing.T, slot uint64, sig []byte) {
	t.Helper()
	if err := RecordAggSigForFallback(slot, sig); err != nil {
		t.Fatalf("RecordAggSigForFallback(%d): %v", slot, err)
	}
}

func testCollectionBounds(t *testing.T, epoch uint64) (start, deadline uint64) {
	t.Helper()
	start, deadline, err := randao.FallbackCollectionBounds(epoch, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		t.Fatalf("FallbackCollectionBounds: %v", err)
	}
	return start, deadline
}

// The compiled-in parameters must actually form a usable collection range.
// Getting this wrong is a silent liveness bug, so it is asserted rather than
// assumed.
func TestValidateFallbackWindowParams_AdoptedValuesAreUsable(t *testing.T) {
	if err := ValidateFallbackWindowParams(); err != nil {
		t.Fatalf("N=%d/K=%d/B=%d/MaxOffset=%d is not usable: %v", N, RevealCutoffK, FallbackFoldBufferB, FallbackFoldMaxSlotOffset, err)
	}
}

// B's ceiling, derived from §7.2's own liveness rule at the adopted
// N=50, K=3, s_min=60s, T_vdf=1200s: B <= N - K - 2*T_vdf/s_min = 7.
// If someone raises B past that, a fallback epoch loses its VDF runway — the
// exact defect the narrowed collection range was introduced to fix.
func TestFallbackFoldBufferB_WithinTheDerivedCeiling(t *testing.T) {
	const (
		sMinSeconds = 60
		tVDFSeconds = 1200
	)
	ceiling := N - RevealCutoffK - 2*tVDFSeconds/sMinSeconds
	if FallbackFoldBufferB > uint64(ceiling) {
		t.Fatalf("B=%d exceeds the derived ceiling %d (N-K-2*T_vdf/s_min) — a fallback epoch would have "+
			"less VDF runway than §7.2 requires", FallbackFoldBufferB, ceiling)
	}
	if FallbackFoldBufferB == 0 {
		t.Fatal("B must be at least 1, or there is nothing to fold")
	}
}

// The same ceiling applies to the deadline itself — MaxOffset is bounded by
// the identical liveness rule, since it is the range B's signers must be
// found inside.
func TestFallbackFoldMaxSlotOffset_WithinTheDerivedCeilingAndAtLeastB(t *testing.T) {
	const (
		sMinSeconds = 60
		tVDFSeconds = 1200
	)
	ceiling := N - RevealCutoffK - 2*tVDFSeconds/sMinSeconds
	if FallbackFoldMaxSlotOffset > uint64(ceiling) {
		t.Fatalf("MaxOffset=%d exceeds the derived ceiling %d — a fallback epoch could lose its VDF runway",
			FallbackFoldMaxSlotOffset, ceiling)
	}
	if FallbackFoldMaxSlotOffset < FallbackFoldBufferB {
		t.Fatalf("MaxOffset=%d is smaller than B=%d — even zero timeouts could never collect enough signers before the deadline",
			FallbackFoldMaxSlotOffset, FallbackFoldBufferB)
	}
}

// The collection range must start at the cutoff, not at the epoch boundary —
// that is what keeps the reveal/withhold decision blind.
func TestFallbackCollectionBounds_UsesTheCutoffAsItsStart(t *testing.T) {
	start, deadline := testCollectionBounds(t, 9)
	if start != cutoffSlotFor(9) {
		t.Fatalf("range starts at %d, want the cutoff slot %d", start, cutoffSlotFor(9))
	}
	if deadline >= 10*N {
		t.Fatalf("range ends at %d, at or past epoch 9's end %d — no runway left", deadline, 10*N)
	}
}

// --- FallbackSeedForEpoch: the three outcomes -------------------------------

// Fewer than B signers, deadline not reached: keep waiting, do not fail
// permanently and do not fabricate a seed.
func TestFallbackSeedForEpoch_NotYetReadyBeforeDeadline(t *testing.T) {
	resetAggSigStore(t)
	start, _ := testCollectionBounds(t, 9)
	mustRecordAggSig(t, start, testAggSig(0x01))
	mustRecordAggSig(t, start+1, testAggSig(0x02)) // only 2 of 5

	_, err := FallbackSeedForEpoch(9, start+1)
	if !errors.Is(err, ErrFallbackNotYetReady) {
		t.Fatalf("error = %v, want ErrFallbackNotYetReady", err)
	}
}

// The exact worked example from the design discussion: signers land at
// offsets 0,1,3,5,6 from the cutoff (offsets 2 and 4 behaved as if they
// timed out). This must succeed — it is the scenario the fixed-width window
// could never handle.
func TestFallbackSeedForEpoch_ReadyOnceFiveCollectedWithGaps(t *testing.T) {
	resetAggSigStore(t)
	start, _ := testCollectionBounds(t, 9)
	offsets := []uint64{0, 1, 3, 5, 6}
	for i, off := range offsets {
		mustRecordAggSig(t, start+off, testAggSig(byte(i+1)))
	}

	seed, err := FallbackSeedForEpoch(9, start+offsets[len(offsets)-1])
	if err != nil {
		t.Fatalf("FallbackSeedForEpoch: %v", err)
	}
	if seed == (randao.Seed{}) {
		t.Fatal("returned the zero seed")
	}
}

// Past the deadline with too few signers: fail closed, distinguishably from
// "not yet ready", so the caller knows to stop retrying.
func TestFallbackSeedForEpoch_DeadlineExceededWithTooFewSigners(t *testing.T) {
	resetAggSigStore(t)
	start, deadline := testCollectionBounds(t, 9)
	mustRecordAggSig(t, start, testAggSig(0x01)) // only 1 of 5

	_, err := FallbackSeedForEpoch(9, deadline)
	if !errors.Is(err, ErrFallbackDeadlineExceeded) {
		t.Fatalf("error = %v, want ErrFallbackDeadlineExceeded", err)
	}
}

// Zero signers at the cutoff slot itself is exactly the instant the old
// single-shot design called this function at — it must be "not yet ready",
// never an immediate hard failure, or the fallback path can never succeed.
func TestFallbackSeedForEpoch_ZeroSignersAtCutoffIsNotYetReadyNotAFailure(t *testing.T) {
	resetAggSigStore(t)
	start, deadline := testCollectionBounds(t, 9)
	if start >= deadline {
		t.Fatal("test setup: start must be before deadline")
	}

	_, err := FallbackSeedForEpoch(9, start)
	if !errors.Is(err, ErrFallbackNotYetReady) {
		t.Fatalf("error = %v, want ErrFallbackNotYetReady — this is the exact bug being fixed", err)
	}
	if errors.Is(err, ErrFallbackDeadlineExceeded) {
		t.Fatal("zero signers at the cutoff must not be treated as a deadline failure")
	}
}

// And it must actually depend on the aggregates, not just on epoch/chain.
func TestFallbackSeedForEpoch_DependsOnTheAggregates(t *testing.T) {
	resetAggSigStore(t)
	start, deadline := testCollectionBounds(t, 9)
	for i := uint64(0); i < FallbackFoldBufferB; i++ {
		mustRecordAggSig(t, start+i, testAggSig(byte(i)))
	}
	seed, err := FallbackSeedForEpoch(9, deadline)
	if err != nil {
		t.Fatalf("FallbackSeedForEpoch: %v", err)
	}

	resetAggSigStore(t)
	for i := uint64(0); i < FallbackFoldBufferB; i++ {
		mustRecordAggSig(t, start+i, testAggSig(byte(i)+100))
	}
	other, err := FallbackSeedForEpoch(9, deadline)
	if err != nil {
		t.Fatalf("FallbackSeedForEpoch (second set): %v", err)
	}
	if other == seed {
		t.Fatal("a different set of aggregates produced the same seed — the fold is not reading them, " +
			"which would make the seed precomputable")
	}
}

func TestRecordAggSigForFallback_RejectsMalformed(t *testing.T) {
	resetAggSigStore(t)

	if err := RecordAggSigForFallback(10, nil); err == nil {
		t.Fatal("empty aggregate must be rejected at record time, not stored to fail later at finalisation")
	}
	if err := RecordAggSigForFallback(10, make([]byte, randao.AggSigLen-1)); err == nil {
		t.Fatal("short aggregate must be rejected")
	}
	if err := RecordAggSigForFallback(10, make([]byte, randao.AggSigLen+1)); err == nil {
		t.Fatal("over-long aggregate must be rejected")
	}
	if len(defaultAggSigStore.sigs) != 0 {
		t.Fatal("a rejected aggregate must not be stored")
	}
}

// The store must not grow without bound across epochs.
func TestPruneAggSigsBelow_DropsOnlyOlderSlots(t *testing.T) {
	resetAggSigStore(t)

	for slot := uint64(0); slot < 20; slot++ {
		mustRecordAggSig(t, slot, testAggSig(byte(slot)))
	}
	pruneAggSigsBelow(10)

	defaultAggSigStore.mu.Lock()
	defer defaultAggSigStore.mu.Unlock()
	for slot := uint64(0); slot < 10; slot++ {
		if _, ok := defaultAggSigStore.sigs[slot]; ok {
			t.Fatalf("slot %d survived pruning below 10", slot)
		}
	}
	for slot := uint64(10); slot < 20; slot++ {
		if _, ok := defaultAggSigStore.sigs[slot]; !ok {
			t.Fatalf("slot %d was pruned but is at or above the watermark", slot)
		}
	}
}
