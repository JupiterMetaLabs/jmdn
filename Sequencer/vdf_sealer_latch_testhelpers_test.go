//go:build sequencertest

package Sequencer

// D-49: TestSealerResultForIsRepeatable, extracted from
// vdf_sealer_latch_test.go because it depends on SeedSealResultForTest,
// which now lives behind the `sequencertest` build tag
// (vdf_seal_wiring_testhelpers.go). Run with
// `go test -tags sequencertest ./Sequencer/...`.

import (
	"testing"

	"github.com/JupiterMetaLabs/avc/vdf"
)

// SealerResultFor is the real caller-facing entry point; prove the latch
// survives the map lookup path that Block.attachAVCConsensusFields uses.
func TestSealerResultForIsRepeatable(t *testing.T) {
	const epoch = 4242
	SeedSealResultForTest(epoch, SealResult{ForEpoch: epoch, Proof: vdf.Proof{T: 99}})

	for i := 1; i <= 3; i++ {
		r, ok := SealerResultFor(epoch)
		if !ok {
			t.Fatalf("SealerResultFor call #%d reported not-ready — this is the "+
				"ErrVDFProofNotReady-forever path on a re-proposed boundary block", i)
		}
		if r.Proof.T != 99 {
			t.Fatalf("call #%d: got T=%d, want 99", i, r.Proof.T)
		}
	}
}
