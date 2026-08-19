package Block

// Tests for attachAVCConsensusFields (consensus_fields.go) — the producer-
// side half of M2b (Architecture §8) plus VDF-Implementation-Handoff.md §6's
// corrected attachment point.

import (
	"testing"

	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/messaging"

	"github.com/ethereum/go-ethereum/common"
)

// resetSlotAndPeriodStores swaps in fresh package-level defaults for the
// duration of a test and restores the originals afterward — same pattern
// used throughout messaging's own tests (e.g.
// TestRoundContextForBlockReadsPeriodStore) for the same reason: these are
// process-wide singletons other tests may also depend on.
func resetSlotAndPeriodStores(t *testing.T) {
	t.Helper()
	savedSlot, savedPeriod := messaging.DefaultSlotStore, messaging.DefaultPeriodStore
	messaging.DefaultSlotStore = messaging.NewSlotStore()
	messaging.DefaultPeriodStore = messaging.NewPeriodStore()
	t.Cleanup(func() {
		messaging.DefaultSlotStore = savedSlot
		messaging.DefaultPeriodStore = savedPeriod
	})
}

func TestAttachAVCConsensusFields_SetsSlotAndPeriodFromLiveStores(t *testing.T) {
	resetSlotAndPeriodStores(t)

	// Two heights already committed cleanly -> committed slot sits at 2.
	messaging.DefaultSlotStore.AdvanceOnCommit(0, 0)
	messaging.DefaultSlotStore.AdvanceOnCommit(1, 0)

	block := &config.ZKBlock{BlockNumber: 2}
	attachAVCConsensusFields(block)

	// LiveSlotFor(2) = committed(2) + pendingPeriod(0) + 1 = 3.
	if block.Slot != 3 {
		t.Fatalf("block.Slot = %d, want 3 (LiveSlotFor(2))", block.Slot)
	}
	if block.Period != 0 {
		t.Fatalf("block.Period = %d, want 0 (no timeout certs for height 2)", block.Period)
	}
}

func TestAttachAVCConsensusFields_FlagOff_DoesNotTouchBlockHash(t *testing.T) {
	resetSlotAndPeriodStores(t)
	saved := Security.M2bHashEnabled
	Security.M2bHashEnabled = false
	t.Cleanup(func() { Security.M2bHashEnabled = saved })

	original := common.HexToHash("0xdeadbeef")
	block := &config.ZKBlock{BlockNumber: 0, BlockHash: original}
	attachAVCConsensusFields(block)

	if block.BlockHash != original {
		t.Fatalf("flag off: BlockHash changed from %s to %s, must be untouched", original.Hex(), block.BlockHash.Hex())
	}
}

func TestAttachAVCConsensusFields_FlagOn_RecomputesBlockHashToMatchIndependentCall(t *testing.T) {
	resetSlotAndPeriodStores(t)
	saved := Security.M2bHashEnabled
	Security.M2bHashEnabled = true
	t.Cleanup(func() { Security.M2bHashEnabled = saved })

	block := &config.ZKBlock{
		BlockNumber: 0,
		BlockHash:   common.HexToHash("0xdeadbeef"), // whatever the caller supplied — must be overwritten
	}
	attachAVCConsensusFields(block)

	// Independently recompute — not by re-checking the function's own output
	// against itself, but by calling the same underlying function a second
	// time on the now-mutated block and confirming it's stable (idempotent)
	// and non-trivial (not still the pre-existing placeholder hash).
	want := Security.RecomputeBlockHashWithConsensusFields(block)
	if block.BlockHash != want {
		t.Fatalf("flag on: block.BlockHash = %s, want %s (RecomputeBlockHashWithConsensusFields)", block.BlockHash.Hex(), want.Hex())
	}
	if block.BlockHash == common.HexToHash("0xdeadbeef") {
		t.Fatal("flag on: BlockHash was not overwritten from the placeholder value")
	}
}
