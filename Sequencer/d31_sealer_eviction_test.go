package Sequencer

// D-31 (second half): vdfSealers had no eviction anywhere outside tests — the
// only delete(vdfSealers, ...) in production code was ClearSealerForTest,
// so the map grew by one entry per epoch for the life of the process. These
// tests pin the new evictOldSealersLocked bound, reusing resetVDFWiringState
// (vdf_seal_wiring_test.go) so they run against a fresh, isolated map rather
// than the package's shared global one.

import (
	"strconv"
	"testing"

	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/committee"
)

func TestSealerFor_EvictsEpochsOlderThanTheRetentionWindow(t *testing.T) {
	resetVDFWiringState(t)
	p := &beacon.Pipeline{}

	// Advance well past the default retention floor (committee.MinRetainedEpochs).
	const highestEpoch = uint64(committee.MinRetainedEpochs) + 50
	for e := uint64(1); e <= highestEpoch; e++ {
		sealerFor(e, p)
	}

	vdfSealersMu.Lock()
	_, oldStillPresent := vdfSealers[1]
	_, newestPresent := vdfSealers[highestEpoch]
	n := len(vdfSealers)
	vdfSealersMu.Unlock()

	if oldStillPresent {
		t.Fatalf("epoch 1's sealer survived %d later epochs — vdfSealers must be bounded, "+
			"not grow forever (this is the D-31 unbounded-map defect)", highestEpoch-1)
	}
	if !newestPresent {
		t.Fatal("the most recently touched epoch's sealer must never be evicted")
	}
	// retain+1 entries max (retain epochs back from newest, inclusive of newest).
	if want := int(committee.MinRetainedEpochs) + 1; n > want {
		t.Fatalf("vdfSealers holds %d entries after sealing %d sequential epochs, want at most %d — "+
			"the map is not bounded by the configured retention window", n, highestEpoch, want)
	}
}

func TestSealerFor_RetentionWindowTracksTheConfiguredEnvVar(t *testing.T) {
	resetVDFWiringState(t)
	p := &beacon.Pipeline{}

	const widened = uint64(committee.MinRetainedEpochs) + 20
	t.Setenv("JMDN_AVC_BEACON_RETAIN_EPOCHS", strconv.FormatUint(widened, 10))

	// Seed epoch 1, then advance to a point that the OLD (unwidened) window
	// would have evicted it at, but the widened one must not.
	sealerFor(1, p)
	oldWindowEvictionPoint := uint64(committee.MinRetainedEpochs) + 2
	sealerFor(oldWindowEvictionPoint, p)

	vdfSealersMu.Lock()
	_, stillPresent := vdfSealers[1]
	vdfSealersMu.Unlock()

	if !stillPresent {
		t.Fatalf("epoch 1's sealer was evicted at newest=%d under a configured retain=%d — "+
			"JMDN_AVC_BEACON_RETAIN_EPOCHS must actually widen this map's own retention window, "+
			"the same way it widens the mix store's (D-47)", oldWindowEvictionPoint, widened)
	}

	// And it must still evict once genuinely past the widened window.
	farNewest := 1 + widened + 10
	sealerFor(farNewest, p)
	vdfSealersMu.Lock()
	_, stillPresent = vdfSealers[1]
	vdfSealersMu.Unlock()
	if stillPresent {
		t.Fatalf("epoch 1's sealer survived at newest=%d with configured retain=%d — eviction "+
			"must still bound the map, just at the configured window", farNewest, widened)
	}
}

func TestCancelSealer_PlacedPlaceholderIsAlsoSubjectToEviction(t *testing.T) {
	resetVDFWiringState(t)
	p := &beacon.Pipeline{}

	// A placeholder planted by CancelSealer (D-31, first half) must not be a
	// backdoor around the retention bound — it goes through the same
	// evictOldSealersLocked call sealerFor uses.
	CancelSealer(1)
	const highestEpoch = uint64(committee.MinRetainedEpochs) + 50
	for e := uint64(2); e <= highestEpoch; e++ {
		sealerFor(e, p)
	}

	vdfSealersMu.Lock()
	_, placeholderStillPresent := vdfSealers[1]
	vdfSealersMu.Unlock()

	if placeholderStillPresent {
		t.Fatal("a CancelSealer-planted placeholder survived well past the retention window — " +
			"it must be evicted like any other sealer entry, not held onto forever")
	}
}
