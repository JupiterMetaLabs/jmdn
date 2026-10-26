package Block

// Tests for attachAVCConsensusFields (consensus_fields.go) — the producer-
// side half of M2b (Architecture §8) plus VDF-Implementation-Handoff.md §6's
// corrected attachment point.

import (
	"errors"
	"testing"

	"gossipnode/Security"
	"gossipnode/Sequencer"
	"gossipnode/config"
	"gossipnode/messaging"

	"github.com/ethereum/go-ethereum/common"
)

// resetSlotAndPeriodStores swaps in fresh package-level defaults for the
// slot store and the proposer's period counter for the duration of a test and
// restores the originals afterward: these are process-wide singletons other
// tests may also depend on.
func resetSlotAndPeriodStores(t *testing.T) {
	t.Helper()
	savedSlot, savedPeriods := messaging.DefaultSlotStore, messaging.DefaultProposerPeriods
	messaging.DefaultSlotStore = messaging.NewSlotStore()
	messaging.DefaultProposerPeriods = messaging.NewProposerPeriods()
	t.Cleanup(func() {
		messaging.DefaultSlotStore = savedSlot
		messaging.DefaultProposerPeriods = savedPeriods
	})

	// attachAVCConsensusFields now fails closed (docs/COMMITTEE-SNAPSHOT-
	// FREEZE-TODO.md item 8) unless messaging.SlotStoreReady() is true. These
	// tests are exercising the field-assignment logic itself, not the
	// recovery gate — TestAttachAVCConsensusFields_FailsClosedWhenSlotStoreNotRecovered
	// below covers the gate directly, deliberately WITHOUT this helper.
	savedReady := messaging.SlotStoreReady()
	messaging.MarkSlotStoreReady()
	t.Cleanup(func() {
		if !savedReady {
			messaging.ResetSlotStoreReadyForTest()
		}
	})
}

func TestAttachAVCConsensusFields_SetsSlotAndPeriodFromLiveStores(t *testing.T) {
	resetSlotAndPeriodStores(t)

	// Heights 0 and 1 committed.
	messaging.DefaultSlotStore.AdvanceOnCommit(0)
	messaging.DefaultSlotStore.AdvanceOnCommit(1)

	block := &config.ZKBlock{BlockNumber: 2}
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}

	// Slot = block number.
	if block.Slot != 2 {
		t.Fatalf("block.Slot = %d, want 2 (slot = block number)", block.Slot)
	}
	if block.Period != 0 {
		t.Fatalf("block.Period = %d, want 0 (no failed round at height 2)", block.Period)
	}
}

// R3: after two failed rounds at a height, the retry is stamped Period 2 and
// keeps the height's slot - every attempt at a height shares slot = height.
func TestAttachAVCConsensusFields_RetryAfterFailedRoundsUsesNextPeriod(t *testing.T) {
	resetSlotAndPeriodStores(t)
	messaging.DefaultSlotStore.AdvanceOnCommit(0)
	messaging.DefaultSlotStore.AdvanceOnCommit(1)
	messaging.DefaultProposerPeriods.AdvanceAfterFailure(2, 7)
	messaging.DefaultProposerPeriods.AdvanceAfterFailure(2, 7)

	block := &config.ZKBlock{BlockNumber: 2}
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}
	if block.Period != 2 {
		t.Fatalf("block.Period = %d, want 2 (two failed rounds)", block.Period)
	}
	if block.Slot != 2 {
		t.Fatalf("block.Slot = %d, want 2 (the retry shares the height's slot)", block.Slot)
	}
}

func TestAttachAVCConsensusFields_DoesNotTouchBlockHash(t *testing.T) {
	resetSlotAndPeriodStores(t)

	original := common.HexToHash("0xdeadbeef")
	block := &config.ZKBlock{BlockNumber: 0, BlockHash: original}
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}

	if block.BlockHash != original {
		t.Fatalf("flag off: BlockHash changed from %s to %s, must be untouched", original.Hex(), block.BlockHash.Hex())
	}
}

// TestAttachAVCConsensusFields_SetsConsensusHashAndLeavesBlockHash locks in the
// post-M2b contract: attach NEVER mutates the orchestrator-submitted, tx-only
// BlockHash. The six consensus fields are made tamper-evident through the
// SEPARATE ConsensusHash field (the value the committee's v4 vote signs), which
// attach sets to RecomputeBlockHashWithConsensusFields(block). Replaces the old
// FlagOn test that asserted the removed BlockHash-rebinding behavior.
func TestAttachAVCConsensusFields_SetsConsensusHashAndLeavesBlockHash(t *testing.T) {
	resetSlotAndPeriodStores(t)

	original := common.HexToHash("0xdeadbeef")
	block := &config.ZKBlock{
		BlockNumber: 0,
		BlockHash:   original, // orchestrator's tx-only identity — must be untouched
	}
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}

	// BlockHash is the orchestrator-submitted tx-only identity: never overwritten.
	if block.BlockHash != original {
		t.Fatalf("BlockHash changed from %s to %s, must be untouched", original.Hex(), block.BlockHash.Hex())
	}

	// ConsensusHash is set to the independent consensus-fields recompute, is
	// stable (idempotent — the recompute does not fold ConsensusHash itself), and
	// is actually populated (non-zero).
	want := Security.RecomputeBlockHashWithConsensusFields(block)
	if block.ConsensusHash != want {
		t.Fatalf("ConsensusHash = %s, want %s (RecomputeBlockHashWithConsensusFields)", block.ConsensusHash.Hex(), want.Hex())
	}
	if (block.ConsensusHash == common.Hash{}) {
		t.Fatal("ConsensusHash was not populated (still zero)")
	}
}

// TestAttachAVCConsensusFields_FailsClosedWhenSlotStoreNotRecovered is the
// propose-side half of the slot-restart fail-closed fix
// (docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8): a node that has not
// recovered its slot/epoch clock from committed history must refuse to
// PROPOSE, exactly as consensus_sync_gate.go's consensusVoteReady refuses to
// VOTE. Deliberately does NOT use resetSlotAndPeriodStores's helper (that
// helper marks the store ready specifically so the OTHER tests in this file
// can test field-assignment logic in isolation from this gate).
func TestAttachAVCConsensusFields_FailsClosedWhenSlotStoreNotRecovered(t *testing.T) {
	savedSlot := messaging.DefaultSlotStore
	messaging.DefaultSlotStore = messaging.NewSlotStore()
	messaging.ResetSlotStoreReadyForTest()
	savedEnforce := messaging.EnforceSlotRecoveryGate
	messaging.EnforceSlotRecoveryGate = true
	t.Cleanup(func() {
		messaging.DefaultSlotStore = savedSlot
		messaging.EnforceSlotRecoveryGate = savedEnforce
	})

	block := &config.ZKBlock{BlockNumber: 7, Slot: 999, Period: 999}
	err := attachAVCConsensusFields(block)
	if err == nil {
		t.Fatal("expected attachAVCConsensusFields to refuse when SlotStoreReady() is false, got nil error")
	}
	// The refusal must happen BEFORE any field is touched — a caller that
	// ignores the error must not find a half-mutated block that looks valid.
	if block.Slot != 999 || block.Period != 999 {
		t.Fatalf("attachAVCConsensusFields mutated Slot/Period despite refusing: got Slot=%d Period=%d", block.Slot, block.Period)
	}

	// Once recovery completes, the same call must succeed.
	messaging.MarkSlotStoreReady()
	t.Cleanup(messaging.ResetSlotStoreReadyForTest)
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error after SlotStoreReady: %v", err)
	}
}

// --- VDF proof attachment (VDF-Implementation-Handoff.md §6) ---

// boundaryHeight is the first block of epoch 1: with slot = block number, the
// epoch-boundary block is simply height messaging.EpochBoundarySlot(1).
var boundaryHeight = messaging.EpochBoundarySlot(1)

// landOnBoundarySlot commits the height just below the boundary, so the next
// block is the boundary block. Must be called after resetSlotAndPeriodStores.
func landOnBoundarySlot(t *testing.T) {
	t.Helper()
	messaging.DefaultSlotStore.AdvanceOnCommit(boundaryHeight - 1)
}

func TestAttachAVCConsensusFields_NonBoundarySlot_LeavesVdfProofZero(t *testing.T) {
	resetSlotAndPeriodStores(t)

	// Same setup as TestAttachAVCConsensusFields_SetsSlotAndPeriodFromLiveStores:
	// BlockNumber=2 -> slot 2, not a multiple of messaging.N (50).
	messaging.DefaultSlotStore.AdvanceOnCommit(0)
	messaging.DefaultSlotStore.AdvanceOnCommit(1)

	block := &config.ZKBlock{BlockNumber: 2}
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}
	if block.Slot == messaging.EpochBoundarySlot(messaging.EpochForSlot(block.Slot)) {
		t.Fatalf("test setup error: slot %d is a boundary slot, expected non-boundary", block.Slot)
	}
	if block.VdfProof != nil {
		t.Fatalf("non-boundary block: VdfProof = %x, want nil (Sequencer.SealerResultFor must not even be consulted off-boundary)", block.VdfProof)
	}
	if block.SeedEpoch != 0 {
		t.Fatalf("non-boundary block: SeedEpoch = %d, want 0", block.SeedEpoch)
	}
}

// TestAttachAVCConsensusFields_BoundarySlot_AttachesProofWhenReady moved to
// consensus_fields_seamtest_test.go (D-49): it depends on
// Sequencer.SeedSealResultForTest/ClearSealerForTest, which now live behind
// the `sequencertest` build tag. Run with
// `go test -tags sequencertest ./Sequencer/... ./Block/...`.

func TestAttachAVCConsensusFields_BoundarySlot_FailsClosedWhenProofNotReady(t *testing.T) {
	resetSlotAndPeriodStores(t)
	landOnBoundarySlot(t)
	t.Cleanup(Sequencer.SetVDFPipelineInstalledForTest(true))
	// Deliberately do not seed a result for epoch 1 — SealerResultFor must
	// report ok=false, and attachAVCConsensusFields must fail closed rather
	// than propose with a missing entropy value for its own boundary slot.

	block := &config.ZKBlock{BlockNumber: boundaryHeight}
	err := attachAVCConsensusFields(block)
	if !errors.Is(err, Sequencer.ErrVDFProofNotReady) {
		t.Fatalf("attachAVCConsensusFields error = %v, want errors.Is(..., Sequencer.ErrVDFProofNotReady)", err)
	}
	// Scoped to the VDF-related fields specifically: unlike the SlotStore
	// gate (which returns before touching ANY field), this check runs after
	// Slot/Period/RandaoReveals/etc. are already set, so only VdfProof/
	// SeedEpoch are expected to stay untouched on this failure path.
	if block.VdfProof != nil {
		t.Fatalf("VdfProof = %x on failure, want nil", block.VdfProof)
	}
	if block.SeedEpoch != 0 {
		t.Fatalf("SeedEpoch = %d on failure, want 0", block.SeedEpoch)
	}
}

// TestAttachAVCConsensusFields_BoundarySlot_FailsClosedWhenSealingErrored
// moved to consensus_fields_seamtest_test.go (D-49): same reason as above.

func TestAttachAVCConsensusFields_BoundarySlot_Stage1_DoesNotRequireProof(t *testing.T) {
	resetSlotAndPeriodStores(t)
	landOnBoundarySlot(t)
	t.Cleanup(Sequencer.SetVDFPipelineInstalledForTest(false))

	block := &config.ZKBlock{BlockNumber: boundaryHeight}
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("Stage 1 at a boundary slot must not require a VDF proof: %v", err)
	}
	if block.Slot != messaging.EpochBoundarySlot(1) {
		t.Fatalf("test setup: slot = %d, want %d", block.Slot, messaging.EpochBoundarySlot(1))
	}
	if block.VdfProof != nil || block.SeedEpoch != 0 || block.VdfParamsDigest != "" {
		t.Fatal("Stage 1 boundary block must carry no VDF fields")
	}
}
