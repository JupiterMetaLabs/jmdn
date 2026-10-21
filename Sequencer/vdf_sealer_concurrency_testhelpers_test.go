//go:build sequencertest

package Sequencer

// D-49: TestConcurrentStartAndCancelSealer_NoRace, extracted from
// vdf_sealer_concurrency_test.go because it depends on SealerCancelledForTest,
// which now lives behind the `sequencertest` build tag
// (vdf_seal_wiring_testhelpers.go). Run with
// `go test -tags sequencertest ./Sequencer/...`.

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
