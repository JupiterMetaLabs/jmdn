package Sequencer

// Concurrent Start/CancelSealer coverage for D-34 item 4's eviction work.
// The map itself was already provably safe under sync.Mutex by construction
// (every vdfSealers access takes vdfSealersMu, with no read/insert window
// released between check and write) -- these tests are the regression guard
// for that invariant under `go test -race`, and for the single-object-per-
// epoch property sealerFor/CancelSealer's placeholder-planting depend on.

import (
	"sync"
	"testing"

	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/randao"
)

// TestConcurrentStartAndCancelSealer_NoRace hammers Start and CancelSealer
// for the SAME epoch from many goroutines at once. The zero-value pipeline
// makes any launched evaluation fail fast (see
// TestOnEpochFinalised_StartedSealer_FailsClosedOnZeroValuePipeline), so this
// stays fast under -race instead of running a real ~20-minute evaluation.
func TestConcurrentStartAndCancelSealer_NoRace(t *testing.T) {
	resetVDFWiringState(t)
	p := &beacon.Pipeline{}
	const epoch = 42

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s := sealerFor(epoch, p)
			s.Start(epoch, randao.Seed{})
		}()
		go func() {
			defer wg.Done()
			CancelSealer(epoch)
		}()
	}
	wg.Wait()

	if !SealerCancelledForTest(epoch) {
		t.Fatal("epoch was never marked cancelled despite 50 concurrent CancelSealer calls racing " +
			"against 50 concurrent Start calls")
	}
}

// TestConcurrentSealerForAndCancelSealer_MapMissRace stresses the specific
// window this fix depends on: sealerFor's fresh-sealer-insert and
// CancelSealer's placeholder-insert both target the SAME map-miss state for
// one epoch. If both ever "won" that race, two distinct VDFSealer objects
// would exist for one epoch -- silently breaking the per-epoch singleton
// sealerFor's own doc comment guarantees (Start must be called at most once
// per instance).
func TestConcurrentSealerForAndCancelSealer_MapMissRace(t *testing.T) {
	resetVDFWiringState(t)
	p := &beacon.Pipeline{}
	const epoch = 99

	var wg sync.WaitGroup
	results := make([]*VDFSealer, 20)
	for i := 0; i < 20; i++ {
		idx := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			results[idx] = sealerFor(epoch, p)
		}()
		go func() {
			defer wg.Done()
			CancelSealer(epoch)
		}()
	}
	wg.Wait()

	first := results[0]
	for i, s := range results {
		if s != first {
			t.Fatalf("sealerFor(%d) returned a different object at index %d than index 0 -- two "+
				"distinct VDFSealers exist for the same epoch, meaning sealerFor and CancelSealer's "+
				"placeholder-plant raced past each other", epoch, i)
		}
	}
}
