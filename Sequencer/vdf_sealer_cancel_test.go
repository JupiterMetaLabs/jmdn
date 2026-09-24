package Sequencer

// Tests for sealer cancellation — the "first valid proof wins" saving.
//
// The property under test is that adopting a peer's proof for epoch E stops
// this node's own evaluation for E. Before this, a node that adopted a proof
// in milliseconds still burned a core for the remainder of a ~T_vdf run.

import (
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/JupiterMetaLabs/avc/vdf"
)

// D-31: this used to be a bare smoke test (call it, assert nothing) — that
// let the actual behavior drift stale under it, since CancelSealer's old
// "no sealer? just return" was a SILENT no-op that dropped the cancellation
// entirely. It has since been fixed to plant a pre-cancelled placeholder
// instead (see CancelSealer's doc comment in vdf_seal_wiring.go for the race
// this closes: a peer's proof for forEpoch can be adopted before THIS node's
// own onEpochFinalised fires for it). This test now pins that behavior.
func TestCancelSealerIsSafeWhenNoSealerExists(t *testing.T) {
	const epoch = 987654
	t.Cleanup(func() { ClearSealerForTest(epoch) })

	if SealerCancelledForTest(epoch) {
		t.Fatal("precondition: epoch must start with no sealer registered")
	}

	CancelSealer(epoch)

	if !SealerCancelledForTest(epoch) {
		t.Fatal("CancelSealer on an epoch with no existing sealer must plant a pre-cancelled " +
			"placeholder, not silently no-op — otherwise a LATER onEpochFinalised call for the " +
			"same epoch creates a fresh, non-cancelled sealer and launches a full ~T_vdf " +
			"evaluation for an epoch a peer's proof already resolved")
	}
}

// TestSealerFor_ReturnsCancelSealersPlaceholder_NotAFreshSealer proves the
// other half of the same fix: sealerFor must return the SAME pre-cancelled
// object CancelSealer planted, and Start on it must refuse to launch.
func TestSealerFor_ReturnsCancelSealersPlaceholder_NotAFreshSealer(t *testing.T) {
	const epoch = 987655 // distinct from the epoch above — tests may run in any order
	t.Cleanup(func() { ClearSealerForTest(epoch) })

	CancelSealer(epoch) // plants the placeholder; epoch has no sealer yet

	s := sealerFor(epoch, nil) // pipeline is never touched by a cancelled Start, so nil is safe
	if !s.Cancelled() {
		t.Fatal("sealerFor must return the SAME pre-cancelled sealer CancelSealer planted for " +
			"this epoch, not construct a fresh (non-cancelled) one")
	}

	s.Start(epoch, randao.Seed{})

	// A launched Start assigns s.cancel; a refused one (the cancelled branch)
	// never reaches that line. This is a synchronous, deterministic check of
	// exactly the guard added for D-31 — it does not depend on any goroutine
	// actually running or racing.
	if _, ready := s.Result(); ready {
		t.Fatal("Start on a pre-cancelled sealer must never produce a result")
	}
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
