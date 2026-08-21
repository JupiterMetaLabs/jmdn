package Sequencer

import (
	"testing"
	"time"

	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/randao"
)

// resetVDFWiringState isolates each test from global sealer/pipeline state.
func resetVDFWiringState(t *testing.T) {
	t.Helper()

	vdfPipelineMu.Lock()
	savedPipeline := vdfPipeline
	vdfPipeline = nil
	vdfPipelineMu.Unlock()

	vdfSealersMu.Lock()
	savedSealers := vdfSealers
	vdfSealers = make(map[uint64]*VDFSealer)
	vdfSealersMu.Unlock()

	t.Cleanup(func() {
		vdfPipelineMu.Lock()
		vdfPipeline = savedPipeline
		vdfPipelineMu.Unlock()

		vdfSealersMu.Lock()
		vdfSealers = savedSealers
		vdfSealersMu.Unlock()
	})
}

func TestSealerFor_SameEpoch_ReturnsSameInstance(t *testing.T) {
	resetVDFWiringState(t)

	p := &beacon.Pipeline{} // opaque non-nil pointer; never dereferenced by sealerFor itself
	a := sealerFor(5, p)
	b := sealerFor(5, p)
	if a != b {
		t.Fatalf("sealerFor(5, ...) returned different instances across calls")
	}
}

func TestSealerFor_DifferentEpochs_DifferentInstances(t *testing.T) {
	resetVDFWiringState(t)

	p := &beacon.Pipeline{}
	a := sealerFor(5, p)
	b := sealerFor(6, p)
	if a == b {
		t.Fatalf("sealerFor(5, ...) and sealerFor(6, ...) returned the SAME instance")
	}
}

func TestOnEpochFinalised_NoPipelineInstalled_CreatesNoSealer(t *testing.T) {
	resetVDFWiringState(t)

	onEpochFinalised(3, randao.Seed{0x01})

	vdfSealersMu.Lock()
	n := len(vdfSealers)
	vdfSealersMu.Unlock()
	if n != 0 {
		t.Fatalf("no pipeline installed: expected zero sealers created, got %d", n)
	}
}

func TestOnEpochFinalised_WithPipeline_SealsForClosedEpochPlusOne(t *testing.T) {
	resetVDFWiringState(t)
	SetVDFPipeline(&beacon.Pipeline{})

	onEpochFinalised(3, randao.Seed{0x02})

	vdfSealersMu.Lock()
	_, gotWrongEpoch := vdfSealers[3]
	_, gotRightEpoch := vdfSealers[4]
	vdfSealersMu.Unlock()

	if gotWrongEpoch {
		t.Fatalf("a sealer was created for the CLOSED epoch (3) — must be closedEpoch+1 (4), per beacon.Pipeline.Seal's documented one-epoch lag")
	}
	if !gotRightEpoch {
		t.Fatalf("no sealer was created for forEpoch=4 (closedEpoch+1)")
	}
}

func TestOnEpochFinalised_StartedSealer_FailsClosedOnZeroValuePipeline(t *testing.T) {
	// &beacon.Pipeline{} has a nil vdf.Group. vdf.Eval already guards nil
	// groups and returns an error rather than panicking or fabricating
	// output — this proves the wiring's failure mode, when Stage F hasn't
	// actually supplied a real group yet, is a clean error, not a silent
	// "success" with meaningless entropy.
	resetVDFWiringState(t)
	SetVDFPipeline(&beacon.Pipeline{})

	onEpochFinalised(7, randao.Seed{0x03})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if res, ok := SealerResultFor(8); ok {
			if res.Err == nil {
				t.Fatalf("expected an error result from a zero-value (nil-group) pipeline, got a clean success")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("sealing goroutine never produced a result within 2s")
}

// InstallEpochFinalisedHook itself is a one-line call to
// messaging.SetEpochFinalisedHook(onEpochFinalised). Both halves of that
// composition are already covered directly: onEpochFinalised's behavior by
// the tests above, and SetEpochFinalisedHook/notifyEpochFinalised generically
// by messaging's own entropy_finalise_test.go (TestSetEpochFinalisedHook_*).
// messaging exposes no way to trigger its hook from outside the package
// (by design — notifyEpochFinalised is only ever called from
// maybeFinaliseCompletedEpochs), so re-verifying the wiring here would need
// either a private-API leak or a full committee/beacon fixture — not worth
// either for a one-line call.
