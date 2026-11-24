package Sequencer

// Eviction must release a running evaluation, not just forget about it.
//
// evictOldSealersLocked used to `delete(vdfSealers, e)` with no Cancel. The map
// entry is the ONLY reference to that sealer's context cancel func, so a still
// running evaluation was left with nothing able to stop it — and by then
// nothing could read its result either (sealerFor refuses to resurrect below
// evictedBelow; SealerResultFor reports not-ready once the entry is gone), so
// the work was unreachable as well as unstoppable.
//
// None of the five pre-existing sealer tests asserted termination — no
// goleak, no NumGoroutine delta — which is why this one exists.

import (
	"testing"
)

// The direct assertion: an evicted sealer must come back Cancelled.
//
// Deliberately checks the sealer object rather than a goroutine count. A
// NumGoroutine delta would be flaky under -race and in a package that starts
// background workers, and it would not distinguish "this eviction cancelled
// it" from "the evaluation happened to finish". Cancelled() is the property
// eviction is actually responsible for.
func TestEvictOldSealersCancelsBeforeDropping(t *testing.T) {
	resetVDFWiringState(t)

	retain := configuredBeaconRetainEpochs()

	// A sealer far enough back that the next touch evicts it.
	victim := &VDFSealer{resultCh: make(chan SealResult, 1)}
	const victimEpoch = 1
	vdfSealers[victimEpoch] = victim

	if victim.Cancelled() {
		t.Fatal("precondition: a fresh sealer must not already be cancelled")
	}

	// Touch an epoch far enough ahead to push victimEpoch below the cutoff.
	vdfSealersMu.Lock()
	evictOldSealersLocked(victimEpoch + retain + 5)
	_, stillPresent := vdfSealers[victimEpoch]
	vdfSealersMu.Unlock()

	if stillPresent {
		t.Fatalf("victim epoch %d should have been evicted (retain=%d)", victimEpoch, retain)
	}
	if !victim.Cancelled() {
		t.Fatal("evicted sealer was dropped WITHOUT being cancelled — a running " +
			"evaluation would have no remaining handle able to stop it (D-73)")
	}
}

// Eviction must not disturb sealers inside the retention window: cancelling
// one of those would abort an evaluation whose result is still reachable
// through SealerResultFor.
func TestEvictOldSealersLeavesRetainedSealersRunning(t *testing.T) {
	resetVDFWiringState(t)

	retain := configuredBeaconRetainEpochs()
	newest := retain + 10

	keep := &VDFSealer{resultCh: make(chan SealResult, 1)}
	vdfSealers[newest] = keep

	vdfSealersMu.Lock()
	evictOldSealersLocked(newest)
	_, kept := vdfSealers[newest]
	vdfSealersMu.Unlock()

	if !kept {
		t.Fatalf("epoch %d is the newest and must not be evicted", newest)
	}
	if keep.Cancelled() {
		t.Fatal("a retained sealer was cancelled — its result is still reachable " +
			"via SealerResultFor, so cancelling it discards work someone may read")
	}
}
