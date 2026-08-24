package messaging

// Tests for entropy_finalise.go after the 2026-08-24 two-phase rewrite.
//
// decideEpoch (mixed-vs-fallback decision at the cutoff) and
// resolvePendingFallbacks (per-block retry of pending fallback epochs) are
// what actually decide (a) which outcome an epoch gets, and (b) WHEN a
// fallback epoch finalises. Both are tested directly against the real
// pendingFallback/finaliseTrackMu state, reset between tests, so the pending
// lifecycle can be exercised without a live committee or beacon driving real
// blocks through the whole pipeline.

import (
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"

	"gossipnode/config"
)

func seedTag(b byte) randao.Seed {
	var out randao.Seed
	for i := range out {
		out[i] = b
	}
	return out
}

// resetFinaliseTracking isolates each test's view of the package-level
// decided-epoch watermark and pending-fallback set, which decideEpoch and
// resolvePendingFallbacks both read and mutate directly.
func resetFinaliseTracking(t *testing.T) {
	t.Helper()
	finaliseTrackMu.Lock()
	savedDecided, savedHave, savedPending := lastDecidedEpoch, haveDecidedAny, pendingFallback
	lastDecidedEpoch, haveDecidedAny = 0, false
	pendingFallback = make(map[uint64]struct{})
	finaliseTrackMu.Unlock()
	t.Cleanup(func() {
		finaliseTrackMu.Lock()
		lastDecidedEpoch, haveDecidedAny, pendingFallback = savedDecided, savedHave, savedPending
		finaliseTrackMu.Unlock()
	})
}

// --- decideEpoch: the cutoff-slot decision -----------------------------------

// A fallback outcome at the cutoff must NOT try to resolve a seed
// immediately — that is the exact bug (finalisation firing when the
// collection range holds nothing) this rewrite fixes. It must instead become
// pending.
func TestDecideEpoch_FallbackOutcomeBecomesPendingNotImmediatelyResolved(t *testing.T) {
	resetFinaliseTracking(t)
	resetEntropyAccumulatorStore(t)
	wireEligibilityWithPeers(t, []string{"peer-a", "peer-b"}) // 2 expected, none revealed -> fallback
	withBeaconEntropy(t, map[uint64][]byte{20: fakeEntropy(0x11, 32)})

	var notified bool
	SetEpochFinalisedHook(func(uint64, randao.Seed) { notified = true })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	decideEpoch(20, &config.ZKBlock{Slot: cutoffSlotFor(20), BlockNumber: 1})

	finaliseTrackMu.Lock()
	_, isPending := pendingFallback[20]
	finaliseTrackMu.Unlock()

	if !isPending {
		t.Fatal("epoch 20 had a fallback outcome but was not marked pending")
	}
	if notified {
		t.Fatal("epoch 20 must not finalise yet — no signers exist at the cutoff slot")
	}
}

// A mixed outcome must still finalise immediately, exactly as the single-shot
// design did — this rewrite must not have slowed down the common case.
func TestDecideEpoch_MixedOutcomeFinalisesImmediately(t *testing.T) {
	resetFinaliseTracking(t)
	resetEntropyAccumulatorStore(t)
	resetRevealInbox(t)
	priv, id := newTestIdentity(t)
	withNodeIdentity(t, priv, id)
	wireEligibilityWithPeers(t, []string{id})
	withBeaconEntropy(t, map[uint64][]byte{21: fakeEntropy(0x22, 32)})

	declared := RevealsForBlock(uint64(21) * N)
	if len(declared) != 1 {
		t.Fatalf("test setup: expected 1 declared reveal, got %d", len(declared))
	}
	acc, err := entropyAccumulatorFor(21)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor: %v", err)
	}
	foldBlockDeclaredReveals(&config.ZKBlock{BlockNumber: 1, Slot: uint64(21) * N, RandaoReveals: declared})
	if !acc.Complete() {
		t.Fatal("test setup: accumulator should be complete with the sole expected member revealed")
	}

	var gotEpoch uint64
	var called int
	SetEpochFinalisedHook(func(e uint64, _ randao.Seed) { called++; gotEpoch = e })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	decideEpoch(21, &config.ZKBlock{Slot: cutoffSlotFor(21), BlockNumber: 2})

	if called != 1 || gotEpoch != 21 {
		t.Fatalf("hook called %d time(s) for epoch %d, want exactly once for epoch 21", called, gotEpoch)
	}
	finaliseTrackMu.Lock()
	_, isPending := pendingFallback[21]
	finaliseTrackMu.Unlock()
	if isPending {
		t.Fatal("a mixed outcome must never enter pendingFallback")
	}
}

// randao.Fallback() is §4.2a's RESOLVED-AS-BROKEN formula. No path may ever
// let it reach notifyEpochFinalised — decideEpoch discards it by construction
// (it never reads res.Seed on the fallback branch), asserted here by checking
// the pending epoch's eventual seed (once resolved) is never that formula.
func TestResolvePendingFallbacks_NeverReturnsTheBrokenFormula(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)
	const chainID, epoch = 7000700, 22
	broken := randao.Fallback(chainID, randao.Seed{}, epoch)

	finaliseTrackMu.Lock()
	pendingFallback[epoch] = struct{}{}
	finaliseTrackMu.Unlock()

	start, _ := testCollectionBounds(t, epoch)
	for i := uint64(0); i < FallbackFoldBufferB; i++ {
		mustRecordAggSig(t, start+i, testAggSig(byte(i+1)))
	}

	var gotSeed randao.Seed
	SetEpochFinalisedHook(func(_ uint64, s randao.Seed) { gotSeed = s })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	resolvePendingFallbacks(&config.ZKBlock{Slot: start + FallbackFoldBufferB - 1, BlockNumber: 3})

	if gotSeed == broken {
		t.Fatal("the aggregate path returned randao.Fallback()'s output — the offline-precomputable formula must never reach the beacon")
	}
}

// --- resolvePendingFallbacks: the ordering that used to be impossible -------

// Once B signers have been collected, resolvePendingFallbacks must seal the
// epoch on a LATER block — this is the scenario that was previously
// impossible under any parameterisation, since the old code only ever tried
// at the cutoff instant.
func TestResolvePendingFallbacks_SealsOnceEnoughSignersArrive(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)

	finaliseTrackMu.Lock()
	pendingFallback[7] = struct{}{}
	finaliseTrackMu.Unlock()

	start, _ := testCollectionBounds(t, 7)
	for i := uint64(0); i < FallbackFoldBufferB; i++ {
		mustRecordAggSig(t, start+i, testAggSig(byte(i+1)))
	}

	var gotEpoch uint64
	var gotSeed randao.Seed
	SetEpochFinalisedHook(func(e uint64, s randao.Seed) { gotEpoch, gotSeed = e, s })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	resolvePendingFallbacks(&config.ZKBlock{Slot: start + FallbackFoldBufferB - 1, BlockNumber: 2})

	if gotEpoch != 7 {
		t.Fatalf("hook fired for epoch %d, want 7", gotEpoch)
	}
	if gotSeed == (randao.Seed{}) {
		t.Fatal("hook fired with the zero seed")
	}
	finaliseTrackMu.Lock()
	_, stillPending := pendingFallback[7]
	finaliseTrackMu.Unlock()
	if stillPending {
		t.Fatal("epoch 7 sealed but was left in the pending set")
	}
}

// The exact worked example from the design discussion: signers land at
// offsets 0,1,3,5,6 from the cutoff (2 and 4 timed out) — must still seal.
func TestResolvePendingFallbacks_SealsWithGapsFromTimeouts(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)

	finaliseTrackMu.Lock()
	pendingFallback[8] = struct{}{}
	finaliseTrackMu.Unlock()

	start, _ := testCollectionBounds(t, 8)
	offsets := []uint64{0, 1, 3, 5, 6}
	for i, off := range offsets {
		mustRecordAggSig(t, start+off, testAggSig(byte(i+1)))
	}

	var notified bool
	SetEpochFinalisedHook(func(uint64, randao.Seed) { notified = true })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	resolvePendingFallbacks(&config.ZKBlock{Slot: start + offsets[len(offsets)-1], BlockNumber: 4})

	if !notified {
		t.Fatal("epoch 8 should have sealed with 5 signers collected despite 2 gapped slots")
	}
	finaliseTrackMu.Lock()
	_, stillPending := pendingFallback[8]
	finaliseTrackMu.Unlock()
	if stillPending {
		t.Fatal("epoch 8 sealed but was left in the pending set")
	}
}

// Not enough signers yet, deadline not reached: stay pending, do not
// finalise and do not drop the epoch.
func TestResolvePendingFallbacks_StaysPendingBeforeDeadline(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)

	finaliseTrackMu.Lock()
	pendingFallback[9] = struct{}{}
	finaliseTrackMu.Unlock()

	start, _ := testCollectionBounds(t, 9)
	mustRecordAggSig(t, start, testAggSig(0x01)) // only 1 of 5

	var notified bool
	SetEpochFinalisedHook(func(uint64, randao.Seed) { notified = true })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	resolvePendingFallbacks(&config.ZKBlock{Slot: start + 1, BlockNumber: 2})

	if notified {
		t.Fatal("epoch 9 must not finalise yet — only 1 of 5 signers collected")
	}
	finaliseTrackMu.Lock()
	_, stillPending := pendingFallback[9]
	finaliseTrackMu.Unlock()
	if !stillPending {
		t.Fatal("epoch 9 was dropped from pending before its deadline — it would never be retried again")
	}
}

// Past the deadline with too few signers: the epoch must be dropped from
// pending and never retried, with no seed produced.
func TestResolvePendingFallbacks_DropsEpochAtDeadlineWithoutASeed(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)

	finaliseTrackMu.Lock()
	pendingFallback[3] = struct{}{}
	finaliseTrackMu.Unlock()

	_, deadline := testCollectionBounds(t, 3)

	var notified bool
	SetEpochFinalisedHook(func(uint64, randao.Seed) { notified = true })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	resolvePendingFallbacks(&config.ZKBlock{Slot: deadline, BlockNumber: 5})

	if notified {
		t.Fatal("epoch 3 must not finalise — it never collected enough signers")
	}
	finaliseTrackMu.Lock()
	_, stillPending := pendingFallback[3]
	finaliseTrackMu.Unlock()
	if stillPending {
		t.Fatal("epoch 3 exceeded its deadline but was left in the pending set — it would be retried forever")
	}
}

// Multiple epochs pending at once must be resolved independently — one
// sealing must not affect another still waiting.
func TestResolvePendingFallbacks_ResolvesMultipleEpochsIndependently(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)

	finaliseTrackMu.Lock()
	pendingFallback[10] = struct{}{}
	pendingFallback[11] = struct{}{}
	finaliseTrackMu.Unlock()

	start10, _ := testCollectionBounds(t, 10)
	for i := uint64(0); i < FallbackFoldBufferB; i++ {
		mustRecordAggSig(t, start10+i, testAggSig(byte(i+1)))
	}
	start11, _ := testCollectionBounds(t, 11)
	mustRecordAggSig(t, start11, testAggSig(0x01)) // only 1 of 5 for epoch 11

	sealed := map[uint64]bool{}
	SetEpochFinalisedHook(func(e uint64, _ randao.Seed) { sealed[e] = true })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	// Use whichever slot is safely inside both epochs' still-open ranges.
	resolvePendingFallbacks(&config.ZKBlock{Slot: start10 + FallbackFoldBufferB - 1, BlockNumber: 6})

	if !sealed[10] {
		t.Fatal("epoch 10 had 5 signers and should have sealed")
	}
	if sealed[11] {
		t.Fatal("epoch 11 had only 1 signer and must not have sealed")
	}
	finaliseTrackMu.Lock()
	_, tenPending := pendingFallback[10]
	_, elevenPending := pendingFallback[11]
	finaliseTrackMu.Unlock()
	if tenPending {
		t.Fatal("epoch 10 sealed but was left pending")
	}
	if !elevenPending {
		t.Fatal("epoch 11 has not resolved yet and must remain pending")
	}
}

// --- epochsWithClosedRevealWindow: the cutoff-slot trigger ------------------
// (unchanged by the two-phase rewrite — decideEpoch and resolvePendingFallbacks
// consume the same trigger the single-shot finaliseEpoch used to.)

// The M4-1 fix, stated as a test: epoch E finalises at slot E*N+K, while E is
// still running — NOT at slot (E+1)*N when E rolls over.
func TestEpochsWithClosedRevealWindow_FiresAtCutoffNotAtRollover(t *testing.T) {
	cutoff := cutoffSlotFor(0) // 0*N + K == K

	if got := epochsWithClosedRevealWindow(cutoff-1, 0, false); len(got) != 0 {
		t.Fatalf("at slot %d (one before the cutoff) got %v, want nothing — the reveal window is still open", cutoff-1, got)
	}
	got := epochsWithClosedRevealWindow(cutoff, 0, false)
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("at the cutoff slot %d got %v, want [0]", cutoff, got)
	}

	// And it must fire strictly inside the epoch, leaving runway.
	if cutoff >= N {
		t.Fatalf("cutoff slot %d is not inside epoch 0 (N=%d) — there would be no VDF runway", cutoff, N)
	}
	runway := N - cutoff
	if runway < N/2 {
		t.Fatalf("runway is only %d of %d slots; the cutoff trigger exists to keep this large", runway, N)
	}
}

func TestEpochsWithClosedRevealWindow_NothingBeforeTheFirstCutoff(t *testing.T) {
	for slot := uint64(0); slot < RevealCutoffK; slot++ {
		if got := epochsWithClosedRevealWindow(slot, 0, false); len(got) != 0 {
			t.Fatalf("slot %d: got %v, want nothing", slot, got)
		}
	}
}

func TestEpochsWithClosedRevealWindow_ResumesAfterLastFinalised(t *testing.T) {
	// Slot is inside epoch 6, past epoch 6's cutoff; epochs 0..3 are done.
	got := epochsWithClosedRevealWindow(cutoffSlotFor(6), 3, true)
	want := []uint64{4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestEpochsWithClosedRevealWindow_SameEpochAfterCutoff_ReturnsEmpty(t *testing.T) {
	// Already finalised epoch 3; still inside epoch 3, past its cutoff.
	if got := epochsWithClosedRevealWindow(3*N+RevealCutoffK+5, 3, true); len(got) != 0 {
		t.Fatalf("got %v, want nothing — epoch 3 is already finalised and epoch 4's cutoff has not been reached", got)
	}
}

func TestEpochsWithClosedRevealWindow_NodeFallsBehind_ReturnsAllInOrder(t *testing.T) {
	got := epochsWithClosedRevealWindow(cutoffSlotFor(10), 2, true)
	want := []uint64{3, 4, 5, 6, 7, 8, 9, 10}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v (index %d), want %v", got, i, want)
		}
	}
}

// A block landing anywhere in the reveal-closed tail must not re-finalise.
func TestEpochsWithClosedRevealWindow_IdempotentAcrossTheEpochTail(t *testing.T) {
	for slot := cutoffSlotFor(4); slot < 5*N; slot++ {
		if got := epochsWithClosedRevealWindow(slot, 4, true); len(got) != 0 {
			t.Fatalf("slot %d: got %v, want nothing — epoch 4 already finalised", slot, got)
		}
	}
}

// --- Stage D -> E seam ------------------------------------------------------

func TestNotifyEpochFinalised_NilHook_NoPanic(t *testing.T) {
	SetEpochFinalisedHook(nil)
	notifyEpochFinalised(3, randao.Seed{})
}

func TestSetEpochFinalisedHook_NotifiesRegisteredHook(t *testing.T) {
	var gotEpoch uint64
	var gotSeed randao.Seed
	var called int

	SetEpochFinalisedHook(func(epoch uint64, seed randao.Seed) {
		called++
		gotEpoch, gotSeed = epoch, seed
	})
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	want := seedTag(0x5A)
	notifyEpochFinalised(11, want)

	if called != 1 {
		t.Fatalf("hook called %d times, want 1", called)
	}
	if gotEpoch != 11 || gotSeed != want {
		t.Fatalf("hook got (%d, %s), want (11, %s)", gotEpoch, gotSeed, want)
	}
}
