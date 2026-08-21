package messaging

// Tests for entropy_finalise.go after the 2026-08-20 rewrite.
//
// The two pure helpers pulled out of the production path (resolveFallbackSeed,
// epochsWithClosedRevealWindow) are what actually decide (a) which fallback
// formula an epoch gets, and (b) WHEN an epoch finalises. Both are tested
// directly, with no live committee, beacon, or aggregate store — the same
// reason they take their inputs as parameters rather than reading globals.

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"
)

func seedTag(b byte) randao.Seed {
	var out randao.Seed
	for i := range out {
		out[i] = b
	}
	return out
}

var errNoAggSig = errors.New("no aggsig")

func aggSigOK(seed randao.Seed) func(uint64) (randao.Seed, error) {
	return func(uint64) (randao.Seed, error) { return seed, nil }
}

func aggSigFails() func(uint64) (randao.Seed, error) {
	return func(uint64) (randao.Seed, error) { return randao.Seed{}, errNoAggSig }
}

// --- resolveFallbackSeed: the ordering that decides seed security -----------

// §4.2a's formula is the only formula — there is nothing else to fall
// through to.
func TestResolveFallbackSeed_UsesAggSigWindow(t *testing.T) {
	want := seedTag(0x42)
	got, source, err := resolveFallbackSeed(9, aggSigOK(want))
	if err != nil {
		t.Fatalf("resolveFallbackSeed: %v", err)
	}
	if source != fallbackSourceAggSig {
		t.Fatalf("source = %q, want %q", source, fallbackSourceAggSig)
	}
	if got != want {
		t.Fatal("returned a seed other than the aggregate fold's")
	}
}

// The headline behaviour: with §4.2a unavailable there is NO second option, so
// the epoch produces no seed at all. An earlier version fell through to a
// grindable interim formula here; that formula has since been deleted outright,
// and this test is what stops one being reintroduced.
func TestResolveFallbackSeed_FailsClosedWithNoAggSigWindow(t *testing.T) {
	_, _, err := resolveFallbackSeed(9, aggSigFails())
	if err == nil {
		t.Fatal("with no aggregate window, finalisation must fail closed — there must be no weaker fallback to fall through to")
	}
	if !errors.Is(err, errNoAggSig) {
		t.Fatalf("error = %v, want it to wrap the aggregate-window failure so the real cause is visible", err)
	}
}

// randao.Fallback() is §4.2a's RESOLVED-AS-BROKEN formula. No path may return
// it, whichever branch is taken.
func TestResolveFallbackSeed_NeverReturnsTheBrokenFormula(t *testing.T) {
	const chainID, epoch = 7000700, 9
	broken := randao.Fallback(chainID, randao.Seed{}, epoch)

	agg, _, err := resolveFallbackSeed(epoch, aggSigOK(seedTag(0x99)))
	if err != nil {
		t.Fatalf("aggsig path: %v", err)
	}
	if agg == broken {
		t.Fatal("the aggregate path returned randao.Fallback()'s output — the offline-precomputable formula must never reach the beacon")
	}
}

// --- epochsWithClosedRevealWindow: the cutoff-slot trigger ------------------

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
