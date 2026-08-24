package messaging

// Tests for the restart-recovery gap named in this file's own header comment
// ("does NOT survive a process restart correctly") and tracked in
// docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8: a freshly-constructed
// SlotStore starts at slot 0 regardless of chain history. SeedFromCommittedTip
// lets startup code fix that from the last committed block's own Slot value
// (which already has every historical period fold baked in - see
// AdvanceOnCommit's doc comment), instead of the counter silently resetting.

import "testing"

func TestSeedFromCommittedTip_FreshStoreAdoptsTipSlot(t *testing.T) {
	s := NewSlotStore()
	ok := s.SeedFromCommittedTip(123, 45)
	if !ok {
		t.Fatal("expected SeedFromCommittedTip to succeed on a fresh store")
	}
	if got := s.Current(); got != 123 {
		t.Fatalf("expected Current()=123 after seeding, got %d", got)
	}
}

// TestSeedFromCommittedTip_RefusesOnceLive guards the exact failure mode a
// careless startup call would cause: calling this AFTER the live commit hooks
// have already started advancing the counter must be a no-op, not a
// clobber - otherwise a late/duplicate seed call could silently rewind the
// counter mid-operation, which is exactly the "double-advance/regress"
// hazard AdvanceOnCommit's own no-op-on-stale-height rule already guards
// against for its own path.
func TestSeedFromCommittedTip_RefusesOnceLive(t *testing.T) {
	s := NewSlotStore()
	if _, advanced := s.AdvanceOnCommit(5, 0); !advanced {
		t.Fatal("setup: expected the live commit to advance the fresh store")
	}
	liveSlot := s.Current()

	ok := s.SeedFromCommittedTip(999, 5000)
	if ok {
		t.Fatal("expected SeedFromCommittedTip to refuse once the store is already live")
	}
	if got := s.Current(); got != liveSlot {
		t.Fatalf("SeedFromCommittedTip must not alter the counter once live: got %d, want unchanged %d", got, liveSlot)
	}
}

// TestSeedFromCommittedTip_SubsequentCommitsAdvanceCorrectly is the actual
// point of the feature: after seeding from a synced tip, the NEXT real commit
// must advance from the seeded value, not from zero.
func TestSeedFromCommittedTip_SubsequentCommitsAdvanceCorrectly(t *testing.T) {
	s := NewSlotStore()
	if ok := s.SeedFromCommittedTip(500, 200); !ok {
		t.Fatal("setup: seed should succeed on a fresh store")
	}

	newSlot, advanced := s.AdvanceOnCommit(201, 0)
	if !advanced {
		t.Fatal("expected the next height (201) to advance past the seeded tip (height 200)")
	}
	if newSlot != 501 {
		t.Fatalf("expected slot 501 (seeded 500 + period 0 + 1), got %d", newSlot)
	}
}

// TestSeedFromCommittedTip_DuplicateOrStaleHeightIsNoOp mirrors
// AdvanceOnCommit_DuplicateHeightIsNoOp's guarantee: seeding twice, or
// seeding then trying to advance a height at-or-before the seeded tip, must
// never regress the counter.
func TestSeedFromCommittedTip_DuplicateOrStaleHeightIsNoOp(t *testing.T) {
	s := NewSlotStore()
	if ok := s.SeedFromCommittedTip(500, 200); !ok {
		t.Fatal("setup: first seed should succeed")
	}
	if ok := s.SeedFromCommittedTip(999, 999); ok {
		t.Fatal("a second SeedFromCommittedTip call must refuse - the store is already seeded")
	}
	if got := s.Current(); got != 500 {
		t.Fatalf("second seed attempt must not alter the counter, got %d", got)
	}

	if _, advanced := s.AdvanceOnCommit(200, 0); advanced {
		t.Fatal("a height at the seeded tip must be a no-op, not a second advance")
	}
	if _, advanced := s.AdvanceOnCommit(150, 0); advanced {
		t.Fatal("a height before the seeded tip must be a no-op")
	}
}
