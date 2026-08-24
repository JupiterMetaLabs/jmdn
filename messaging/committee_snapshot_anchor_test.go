package messaging

// Tests for docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md items 1 and 8: freezing
// the NEXT epoch's eligible-validator snapshot with a lookahead (so every
// node has it cached before it's needed), and stamping its hash on-chain so
// a rejoining node can verify a snapshot body against something that
// traveled with the chain instead of trusting whoever served it.

import (
	"testing"
)

func resetSnapshotAnchorState() {
	frozenSnapshotHashMu.Lock()
	frozenSnapshotHash = make(map[uint64][32]byte)
	frozenSnapshotHashMu.Unlock()
}

func TestMaybeFreezeUpcomingSnapshot_Disabled_NeverFreezes(t *testing.T) {
	resetSnapshotAnchorState()
	old := CommitteeSnapshotAnchorEnabled
	CommitteeSnapshotAnchorEnabled = false
	defer func() { CommitteeSnapshotAnchorEnabled = old }()

	// Slot deep enough into epoch 0 that, if enabled, epoch 1 would already
	// be frozen (freeze slot = 1*N - SnapshotFreezeLookahead).
	maybeFreezeUpcomingSnapshot(N - SnapshotFreezeLookahead)

	if _, ok := frozenSnapshotHashFor(1); ok {
		t.Fatal("expected no freeze to occur while CommitteeSnapshotAnchorEnabled is false")
	}
}

func TestMaybeFreezeUpcomingSnapshot_BeforeLookahead_DoesNotFreezeYet(t *testing.T) {
	resetSnapshotAnchorState()
	old := CommitteeSnapshotAnchorEnabled
	CommitteeSnapshotAnchorEnabled = true
	defer func() { CommitteeSnapshotAnchorEnabled = old }()

	// One slot before the freeze point for epoch 1.
	maybeFreezeUpcomingSnapshot(N - SnapshotFreezeLookahead - 1)

	if _, ok := frozenSnapshotHashFor(1); ok {
		t.Fatal("expected epoch 1 to remain unfrozen before its lookahead slot")
	}
}

func TestMaybeFreezeUpcomingSnapshot_AtLookahead_Freezes(t *testing.T) {
	resetSnapshotAnchorState()
	old := CommitteeSnapshotAnchorEnabled
	CommitteeSnapshotAnchorEnabled = true
	defer func() { CommitteeSnapshotAnchorEnabled = old }()

	maybeFreezeUpcomingSnapshot(N - SnapshotFreezeLookahead)

	h1, ok := frozenSnapshotHashFor(1)
	if !ok {
		t.Fatal("expected epoch 1 to be frozen exactly at its lookahead slot")
	}
	var zero [32]byte
	if h1 == zero {
		t.Fatal("frozen hash must not be the zero value for a non-empty eligible set")
	}
}

func TestMaybeFreezeUpcomingSnapshot_IsIdempotent(t *testing.T) {
	resetSnapshotAnchorState()
	old := CommitteeSnapshotAnchorEnabled
	CommitteeSnapshotAnchorEnabled = true
	defer func() { CommitteeSnapshotAnchorEnabled = old }()

	maybeFreezeUpcomingSnapshot(N - SnapshotFreezeLookahead)
	h1, _ := frozenSnapshotHashFor(1)

	// Calling again at a LATER slot within the same epoch must not re-derive
	// (and potentially change) the frozen value - that would defeat the
	// entire point of freezing.
	maybeFreezeUpcomingSnapshot(N - SnapshotFreezeLookahead + 5)
	h1Again, ok := frozenSnapshotHashFor(1)
	if !ok || h1Again != h1 {
		t.Fatalf("expected the frozen hash for epoch 1 to stay fixed once set: got %x, want %x", h1Again, h1)
	}
}

func TestMaybeFreezeUpcomingSnapshot_FreezesFutureEpochNotCurrent(t *testing.T) {
	resetSnapshotAnchorState()
	old := CommitteeSnapshotAnchorEnabled
	CommitteeSnapshotAnchorEnabled = true
	defer func() { CommitteeSnapshotAnchorEnabled = old }()

	maybeFreezeUpcomingSnapshot(N - SnapshotFreezeLookahead)

	if _, ok := frozenSnapshotHashFor(0); ok {
		t.Fatal("epoch 0 (the CURRENT epoch at this slot) should not be frozen by this call - only the upcoming epoch 1 is")
	}
}
