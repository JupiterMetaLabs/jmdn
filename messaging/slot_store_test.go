package messaging

// Tests for M0.1 (Architecture §7.1, monotonic slot counter) and M3
// (Architecture §3.1/§9.1, EpochForSlot). Required acceptance criteria per
// §9.1's build table:
//  - M0.1: "slot advances on commit AND on timeout; never skips backwards"
//  - M3:   "epoch boundary exactly at E*N; test slots E*N-1 and E*N"

import "testing"

func TestSlotStoreStartsAtZero(t *testing.T) {
	s := NewSlotStore()
	if got := s.Current(); got != 0 {
		t.Fatalf("fresh SlotStore should start at slot 0, got %d", got)
	}
}

// TestAdvanceOnCommit_NoTimeouts is the simple case: a height that committed
// on its first try (period 0) advances slot by exactly 1 - one unit for the
// commit, zero for timeouts.
func TestAdvanceOnCommit_NoTimeouts(t *testing.T) {
	s := NewSlotStore()
	newSlot, advanced := s.AdvanceOnCommit(0, 0)
	if !advanced || newSlot != 1 {
		t.Fatalf("expected advanced=true slot=1, got advanced=%v slot=%d", advanced, newSlot)
	}
}

// TestAdvanceOnCommit_FoldsInTimeoutHistory is the case the whole design
// exists for: a height that burned 2 certified timeouts before finally
// committing must advance slot by 3 (2 timeouts + 1 commit), not by 1 - a
// stalled-then-recovered round must not be invisible to the epoch clock.
func TestAdvanceOnCommit_FoldsInTimeoutHistory(t *testing.T) {
	s := NewSlotStore()
	newSlot, advanced := s.AdvanceOnCommit(0, 2)
	if !advanced || newSlot != 3 {
		t.Fatalf("expected advanced=true slot=3 (period 2 + 1 commit), got advanced=%v slot=%d", advanced, newSlot)
	}
}

// TestAdvanceOnCommit_SequentialHeights confirms slot accumulates correctly
// across multiple real heights, not just one in isolation.
func TestAdvanceOnCommit_SequentialHeights(t *testing.T) {
	s := NewSlotStore()
	s.AdvanceOnCommit(0, 0) // slot -> 1
	s.AdvanceOnCommit(1, 1) // slot -> 1 + 2 = 3
	newSlot, advanced := s.AdvanceOnCommit(2, 0)
	if !advanced || newSlot != 4 {
		t.Fatalf("expected advanced=true slot=4 after heights [0,1,2], got advanced=%v slot=%d", advanced, newSlot)
	}
}

// TestAdvanceOnCommit_DuplicateHeightIsNoOp is required: a node's own
// broadcast block echoing back through its own gossip receive path (or any
// other double-delivery) must not double-advance the counter.
func TestAdvanceOnCommit_DuplicateHeightIsNoOp(t *testing.T) {
	s := NewSlotStore()
	s.AdvanceOnCommit(5, 1) // slot -> 2

	newSlot, advanced := s.AdvanceOnCommit(5, 1) // same height redelivered
	if advanced || newSlot != 2 {
		t.Fatalf("duplicate height must be a no-op, got advanced=%v slot=%d", advanced, newSlot)
	}
}

// TestAdvanceOnCommit_NeverSkipsBackwards is the §9.1 acceptance criterion,
// stated directly: an out-of-order (older) height arriving after a newer one
// must never regress the counter.
func TestAdvanceOnCommit_NeverSkipsBackwards(t *testing.T) {
	s := NewSlotStore()
	s.AdvanceOnCommit(10, 0) // slot -> 1, lastCommittedHeight -> 10

	newSlot, advanced := s.AdvanceOnCommit(3, 5) // stale/out-of-order height 3
	if advanced || newSlot != 1 {
		t.Fatalf("an older height must never regress or re-advance slot, got advanced=%v slot=%d", advanced, newSlot)
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
		{N - 1, 0},  // slot 49 -> still epoch 0
		{N, 1},      // slot 50 -> epoch 1, the boundary
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

// TestLiveSlotFor_FreshHeightAfterCleanCommits is the simple case: after N
// heights committed with no timeouts, the NEXT height's live slot is exactly
// one more than the last committed slot.
func TestLiveSlotFor_FreshHeightAfterCleanCommits(t *testing.T) {
	saved, savedPeriod := DefaultSlotStore, DefaultPeriodStore
	DefaultSlotStore = NewSlotStore()
	DefaultPeriodStore = NewPeriodStore()
	t.Cleanup(func() { DefaultSlotStore = saved; DefaultPeriodStore = savedPeriod })

	DefaultSlotStore.AdvanceOnCommit(0, 0) // slot -> 1
	DefaultSlotStore.AdvanceOnCommit(1, 0) // slot -> 2

	if got := LiveSlotFor(2); got != 3 {
		t.Fatalf("LiveSlotFor(2) = %d, want 3 (committed slot 2, no pending timeouts)", got)
	}
}

// TestLiveSlotFor_AccountsForPendingUncommittedTimeouts is the property this
// whole helper exists for: a height with already-accepted, not-yet-committed
// TimeoutCertificates must have its live slot reflect them immediately - the
// exact case a naive `DefaultSlotStore.Current()+1` would get wrong.
func TestLiveSlotFor_AccountsForPendingUncommittedTimeouts(t *testing.T) {
	saved, savedPeriod := DefaultSlotStore, DefaultPeriodStore
	DefaultSlotStore = NewSlotStore()
	DefaultPeriodStore = NewPeriodStore()
	t.Cleanup(func() { DefaultSlotStore = saved; DefaultPeriodStore = savedPeriod })

	DefaultSlotStore.AdvanceOnCommit(0, 0) // slot -> 1; next height is 1

	kps := newKeypairs(t, 4)
	cert := buildCertificate(t, kps, 1, 2) // height 1 has already timed out twice
	if _, accepted, err := DefaultPeriodStore.AcceptTimeoutCertificate(*cert, len(kps), pubKeyMap(kps)); err != nil || !accepted {
		t.Fatalf("accept: accepted=%v err=%v", accepted, err)
	}

	// committed slot (1) + pending period (2) + 1 (this new attempt) = 4.
	// A naive Current()+1 would wrongly say 2, silently losing the two
	// timeouts height 1 already burned.
	if got := LiveSlotFor(1); got != 4 {
		t.Fatalf("LiveSlotFor(1) = %d, want 4 (1 committed-slot + 2 pending periods + 1)", got)
	}
}

// TestCurrentEpoch_ReflectsInFlightTimeoutsBeforeCommit is the property that
// motivated CurrentEpoch existing at all: a height that has already timed
// out enough times to cross an epoch boundary must report the NEW epoch
// immediately, without waiting for that height to actually commit.
func TestCurrentEpoch_ReflectsInFlightTimeoutsBeforeCommit(t *testing.T) {
	saved, savedPeriod := DefaultSlotStore, DefaultPeriodStore
	DefaultSlotStore = NewSlotStore()
	DefaultPeriodStore = NewPeriodStore()
	t.Cleanup(func() { DefaultSlotStore = saved; DefaultPeriodStore = savedPeriod })

	// Commit 49 heights cleanly (no timeouts) so committed slot sits at 49,
	// one short of the epoch-1 boundary.
	for h := uint64(0); h < N-1; h++ {
		DefaultSlotStore.AdvanceOnCommit(h, 0)
	}
	if got := CurrentEpoch(N - 1); got != 0 {
		t.Fatalf("before any pending timeout, expected epoch 0, got %d", got)
	}

	// Height N-1 (the next, not-yet-committed height) has already timed out
	// once - a live TimeoutCertificate was accepted for it, tracked by
	// DefaultPeriodStore, with no commit yet.
	kps := newKeypairs(t, 4)
	cert := buildCertificate(t, kps, N-1, 1)
	if _, accepted, err := DefaultPeriodStore.AcceptTimeoutCertificate(*cert, len(kps), pubKeyMap(kps)); err != nil || !accepted {
		t.Fatalf("accept timeout cert: accepted=%v err=%v", accepted, err)
	}

	// Committed slot (49) + pending period (1) = 50 = the epoch-1 boundary,
	// even though nothing has committed at height N-1 yet.
	if got := CurrentEpoch(N - 1); got != 1 {
		t.Fatalf("a pending, uncommitted timeout crossing the boundary should already report epoch 1, got %d", got)
	}
}
