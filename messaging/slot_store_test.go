package messaging

// Tests for the slot rule (slot == block number) and M3 (Architecture
// §3.1/§9.1, EpochForSlot): "epoch boundary exactly at E*N; test slots
// E*N-1 and E*N".

import "testing"

func TestSlotStoreStartsAtZero(t *testing.T) {
	s := NewSlotStore()
	if got := s.Current(); got != 0 {
		t.Fatalf("fresh SlotStore should start at slot 0, got %d", got)
	}
}

// Committing a height moves the store to that height's slot - the height.
func TestAdvanceOnCommit_SlotIsTheHeight(t *testing.T) {
	s := NewSlotStore()
	if slot, advanced := s.AdvanceOnCommit(0); !advanced || slot != 0 {
		t.Fatalf("genesis commit: advanced=%v slot=%d, want true,0", advanced, slot)
	}
	if slot, advanced := s.AdvanceOnCommit(1); !advanced || slot != 1 {
		t.Fatalf("height 1: advanced=%v slot=%d, want true,1", advanced, slot)
	}
}

// A node that catches up jumps straight to the synced tip; nothing in between
// has to be replayed.
func TestAdvanceOnCommit_CatchUpJumpsToTheTip(t *testing.T) {
	s := NewSlotStore()
	s.AdvanceOnCommit(19_900)
	if slot, advanced := s.AdvanceOnCommit(20_000); !advanced || slot != 20_000 {
		t.Fatalf("after catching up to 20000: advanced=%v slot=%d, want true,20000", advanced, slot)
	}
}

// A node's own broadcast block echoing back through gossip (or any other
// double delivery) must not move the store.
func TestAdvanceOnCommit_DuplicateHeightIsNoOp(t *testing.T) {
	s := NewSlotStore()
	s.AdvanceOnCommit(5)
	if slot, advanced := s.AdvanceOnCommit(5); advanced || slot != 5 {
		t.Fatalf("duplicate height must be a no-op, got advanced=%v slot=%d", advanced, slot)
	}
}

// An older height arriving after a newer one - a sync batch and a live commit
// racing - never regresses the store.
func TestAdvanceOnCommit_NeverGoesBackwards(t *testing.T) {
	s := NewSlotStore()
	s.AdvanceOnCommit(10)
	if slot, advanced := s.AdvanceOnCommit(3); advanced || slot != 10 {
		t.Fatalf("an older height must never regress the store, got advanced=%v slot=%d", advanced, slot)
	}
}

// TestEpochForSlot_BoundaryExactlyAtEN is the literal §9.1 acceptance
// criterion for M3: "epoch boundary exactly at E*N; test slots E*N-1 and E*N."
func TestEpochForSlot_BoundaryExactlyAtEN(t *testing.T) {
	cases := []struct {
		slot uint64
		want uint64
	}{
		{0, 0},
		{N - 1, 0}, // slot 49 -> still epoch 0
		{N, 1},     // slot 50 -> epoch 1, the boundary
		{N + 1, 1},
		{2*N - 1, 1}, // slot 99 -> still epoch 1
		{2 * N, 2},   // slot 100 -> epoch 2
	}
	for _, c := range cases {
		if got := EpochForSlot(c.slot); got != c.want {
			t.Fatalf("EpochForSlot(%d) = %d, want %d", c.slot, got, c.want)
		}
	}
}

// The slot a block carries is its height - independent of the committed store
// and of how many rounds failed at that height.
func TestLiveSlotFor_IsTheHeight(t *testing.T) {
	savedSlots, savedPeriods := DefaultSlotStore, DefaultProposerPeriods
	DefaultSlotStore, DefaultProposerPeriods = NewSlotStore(), NewProposerPeriods()
	t.Cleanup(func() { DefaultSlotStore, DefaultProposerPeriods = savedSlots, savedPeriods })

	DefaultSlotStore.AdvanceOnCommit(1)
	if got := LiveSlotFor(2); got != 2 {
		t.Fatalf("LiveSlotFor(2) = %d, want 2", got)
	}
	DefaultProposerPeriods.AdvanceAfterFailure(2, 7)
	DefaultProposerPeriods.AdvanceAfterFailure(2, 7)
	if got := LiveSlotFor(2); got != 2 {
		t.Fatalf("after two failed rounds LiveSlotFor(2) = %d, want 2 (the retry shares the slot)", got)
	}
}

// A height's epoch boundary block sits exactly at slot E*N, whatever retries
// happened before it, so the boundary block is always height E*N.
func TestLiveSlotFor_EpochBoundaryIsHeightEN(t *testing.T) {
	for _, e := range []uint64{1, 2, 400} {
		h := EpochBoundarySlot(e)
		if got := EpochForSlot(LiveSlotFor(h)); got != e {
			t.Fatalf("height %d: epoch %d, want %d", h, got, e)
		}
		if got := EpochForSlot(LiveSlotFor(h - 1)); got != e-1 {
			t.Fatalf("height %d: epoch %d, want %d", h-1, got, e-1)
		}
	}
}
