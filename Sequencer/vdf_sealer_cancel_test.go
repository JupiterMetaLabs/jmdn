package Sequencer

// Tests for sealer cancellation — the "first valid proof wins" saving.
//
// The property under test is that adopting a peer's proof for epoch E stops
// this node's own evaluation for E. Before this, a node that adopted a proof
// in milliseconds still burned a core for the remainder of a ~T_vdf run.

import (
	"testing"

	"github.com/JupiterMetaLabs/avc/vdf"
)

func TestCancelSealerIsSafeWhenNoSealerExists(t *testing.T) {
	// A peer proof can arrive for an epoch this node never started sealing —
	// a restarted or late node is exactly that case.
	CancelSealer(987654)
}

func TestCancelSealerMarksTheEpochCancelled(t *testing.T) {
	const epoch = 4242
	SeedSealResultForTest(epoch, SealResult{ForEpoch: epoch, Proof: vdf.Proof{T: 7}})
	t.Cleanup(func() { ClearSealerForTest(epoch) })

	if SealerCancelledForTest(epoch) {
		t.Fatal("a freshly seeded sealer must not report cancelled")
	}
	CancelSealer(epoch)
	if !SealerCancelledForTest(epoch) {
		t.Fatal("CancelSealer must mark the epoch cancelled")
	}
}

// TestCancelSealerIsIdempotent — duplicate proofs for one epoch are the norm,
// arriving once per peer that gossips the boundary block.
func TestCancelSealerIsIdempotent(t *testing.T) {
	const epoch = 4243
	SeedSealResultForTest(epoch, SealResult{ForEpoch: epoch})
	t.Cleanup(func() { ClearSealerForTest(epoch) })

	for i := 0; i < 5; i++ {
		CancelSealer(epoch)
	}
	if !SealerCancelledForTest(epoch) {
		t.Fatal("repeated cancellation must remain cancelled")
	}
}

// TestCancellationDoesNotDestroyAnAlreadyLatchedResult covers the race the
// design has to survive: a local evaluation that COMPLETED just before a peer
// proof was adopted. The completed result must still be readable — cancelling
// afterwards must not turn a valid proof into "not ready".
func TestCancellationDoesNotDestroyAnAlreadyLatchedResult(t *testing.T) {
	const epoch = 4244
	want := vdf.Proof{T: 99, Group: "g"}
	SeedSealResultForTest(epoch, SealResult{ForEpoch: epoch, Proof: want})
	t.Cleanup(func() { ClearSealerForTest(epoch) })

	first, ok := SealerResultFor(epoch)
	if !ok || first.Proof.T != want.T {
		t.Fatalf("precondition: expected a ready result, got ok=%v %+v", ok, first)
	}

	CancelSealer(epoch)

	again, ok := SealerResultFor(epoch)
	if !ok {
		t.Fatal("a result that had already completed and latched must survive a later " +
			"cancellation — otherwise a node that finished first would lose its own proof " +
			"the moment a peer's copy arrived")
	}
	if again.Proof.T != want.T {
		t.Fatalf("latched result changed after cancellation: %+v", again)
	}
}
