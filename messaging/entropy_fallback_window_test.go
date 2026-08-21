package messaging

// Tests for entropy_fallback_window.go — the [K, K+B) fallback fold window
// (Architecture §4.2a as amended 2026-08-20).
//
// The most important test here is the one asserting that this path currently
// FAILS. Blocker B1 (aggSig is never persisted on a block) means no node can
// compute this seed today, and the correct behaviour is a visible refusal, not
// a substituted value. If someone later wires the collector, that test is what
// tells them the blocker is genuinely cleared rather than papered over.

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

// The compiled-in parameters must actually form a usable window. Getting this
// wrong is a silent liveness bug, so it is asserted rather than assumed.
func TestValidateFallbackWindowParams_AdoptedValuesAreUsable(t *testing.T) {
	if err := ValidateFallbackWindowParams(); err != nil {
		t.Fatalf("N=%d/K=%d/B=%d is not a usable window: %v", N, RevealCutoffK, FallbackFoldBufferB, err)
	}
}

// B's ceiling, derived from §7.2's own liveness rule at the adopted
// N=50, K=3, s_min=60s, T_vdf=1200s:  B <= N - K - 2*T_vdf/s_min = 7.
// If someone raises B past that, a fallback epoch loses its VDF runway — the
// exact defect the narrowed window was introduced to fix.
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

// The window must start at the cutoff, not at the epoch boundary — that is
// what keeps the reveal/withhold decision blind.
func TestFallbackWindow_UsesTheCutoffAsItsStart(t *testing.T) {
	start, end, err := randao.FallbackWindow(9, N, RevealCutoffK, FallbackFoldBufferB)
	if err != nil {
		t.Fatalf("FallbackWindow: %v", err)
	}
	if start != cutoffSlotFor(9) {
		t.Fatalf("window starts at %d, want the cutoff slot %d", start, cutoffSlotFor(9))
	}
	if end >= 10*N {
		t.Fatalf("window ends at %d, at or past epoch 9's end %d — no runway left", end, 10*N)
	}
}

// BLOCKER B1, asserted. Nothing populates the store in production, so this must
// refuse rather than produce a seed.
func TestFallbackSeedForEpoch_FailsClosedWhileAggSigIsNotPersisted(t *testing.T) {
	resetAggSigStore(t)

	_, err := FallbackSeedForEpoch(9)
	if err == nil {
		t.Fatal("FallbackSeedForEpoch returned a seed with no recorded aggregates — while blocker B1 stands " +
			"this path must fail closed, never substitute a value")
	}
	if !errors.Is(err, ErrAggSigUnavailable) {
		t.Fatalf("error = %v, want ErrAggSigUnavailable so the cause is unambiguous", err)
	}
}

// A partial window is still a refusal: folding one would hand whoever caused
// the gap a choice among window subsets.
func TestFallbackSeedForEpoch_PartialWindowStillFailsClosed(t *testing.T) {
	resetAggSigStore(t)
	start, end, err := randao.FallbackWindow(9, N, RevealCutoffK, FallbackFoldBufferB)
	if err != nil {
		t.Fatalf("FallbackWindow: %v", err)
	}

	// Record every slot except the last.
	for slot := start; slot < end-1; slot++ {
		if err := RecordAggSigForFallback(slot, testAggSig(byte(slot))); err != nil {
			t.Fatalf("RecordAggSigForFallback(%d): %v", slot, err)
		}
	}

	if _, err := FallbackSeedForEpoch(9); !errors.Is(err, ErrAggSigUnavailable) {
		t.Fatalf("error = %v, want ErrAggSigUnavailable for a window missing one slot", err)
	}
}

// The forward-looking test: once B1 lands and the collector is fed, the seed
// must actually compute. This proves the wiring is complete apart from its
// input.
func TestFallbackSeedForEpoch_CompleteWindowProducesSeed(t *testing.T) {
	resetAggSigStore(t)
	start, end, err := randao.FallbackWindow(9, N, RevealCutoffK, FallbackFoldBufferB)
	if err != nil {
		t.Fatalf("FallbackWindow: %v", err)
	}
	for slot := start; slot < end; slot++ {
		if err := RecordAggSigForFallback(slot, testAggSig(byte(slot))); err != nil {
			t.Fatalf("RecordAggSigForFallback(%d): %v", slot, err)
		}
	}

	seed, err := FallbackSeedForEpoch(9)
	if err != nil {
		t.Fatalf("FallbackSeedForEpoch: %v", err)
	}
	if seed == (randao.Seed{}) {
		t.Fatal("seed is all zero")
	}

	// And it must depend on the aggregates, not just on epoch/chain.
	resetAggSigStore(t)
	for slot := start; slot < end; slot++ {
		if err := RecordAggSigForFallback(slot, testAggSig(byte(slot)+100)); err != nil {
			t.Fatalf("RecordAggSigForFallback(%d): %v", slot, err)
		}
	}
	other, err := FallbackSeedForEpoch(9)
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
		if err := RecordAggSigForFallback(slot, testAggSig(byte(slot))); err != nil {
			t.Fatalf("RecordAggSigForFallback(%d): %v", slot, err)
		}
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
