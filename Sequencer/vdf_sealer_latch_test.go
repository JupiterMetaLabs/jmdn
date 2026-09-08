package Sequencer

// Regression test for the single-shot VDFSealer.Result defect (2026-09-03).
//
// Before the latch, the first Result() call consumed the channel and every
// later call reported not-ready forever — so a re-proposed epoch-boundary
// block could never attach its VDF proof, and the node could not recover
// without a restart (which loses the sealer map too).

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/vdf"
)

func TestVDFSealerResultIsRepeatable(t *testing.T) {
	want := SealResult{ForEpoch: 7, Proof: vdf.Proof{T: 1234, Group: "rsa-2048-frc"}}

	s := &VDFSealer{resultCh: make(chan SealResult, 1)}
	s.resultCh <- want

	first, ok := s.Result()
	if !ok {
		t.Fatalf("first Result() must be ready")
	}
	if first.ForEpoch != want.ForEpoch || first.Proof.T != want.Proof.T {
		t.Fatalf("first Result() = %+v, want %+v", first, want)
	}

	// This is the assertion that failed before the fix.
	for i := 2; i <= 5; i++ {
		again, ok := s.Result()
		if !ok {
			t.Fatalf("Result() call #%d reported not-ready after a successful first read — "+
				"a re-proposed boundary block can never attach its proof", i)
		}
		if again.ForEpoch != want.ForEpoch || again.Proof.T != want.Proof.T ||
			again.Proof.Group != want.Proof.Group {
			t.Fatalf("Result() call #%d = %+v, want %+v", i, again, want)
		}
	}
}

func TestVDFSealerNotReadyStaysNotReady(t *testing.T) {
	s := &VDFSealer{resultCh: make(chan SealResult, 1)}
	for i := 1; i <= 3; i++ {
		if _, ok := s.Result(); ok {
			t.Fatalf("call #%d: empty sealer must report not-ready (fail closed)", i)
		}
	}
}

// A failed evaluation must latch too — the caller has to see the SAME error on
// a retry rather than a not-ready that looks like "still working".
func TestVDFSealerLatchesFailure(t *testing.T) {
	boom := errors.New("vdf: evaluation exploded")
	s := &VDFSealer{resultCh: make(chan SealResult, 1)}
	s.resultCh <- SealResult{ForEpoch: 3, Err: boom}

	for i := 1; i <= 3; i++ {
		r, ok := s.Result()
		if !ok {
			t.Fatalf("call #%d: a failed result is still a result, must report ready", i)
		}
		if !errors.Is(r.Err, boom) {
			t.Fatalf("call #%d: want the latched error, got %v", i, r.Err)
		}
	}
}

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
