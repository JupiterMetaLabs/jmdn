package Block

// Tests for attachAVCConsensusFields (consensus_fields.go) — the producer-
// side half of M2b (Architecture §8) plus VDF-Implementation-Handoff.md §6's
// corrected attachment point.

import (
	"errors"
	"math/big"
	"testing"

	"gossipnode/Security"
	"gossipnode/Sequencer"
	"gossipnode/config"
	"gossipnode/messaging"

	"github.com/JupiterMetaLabs/avc/vdf"
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

	// Two heights already committed cleanly -> committed slot sits at 2.
	messaging.DefaultSlotStore.AdvanceOnCommit(0, 0)
	messaging.DefaultSlotStore.AdvanceOnCommit(1, 0)

	block := &config.ZKBlock{BlockNumber: 2}
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}

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
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}

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
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}

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

// TestAttachAVCConsensusFields_FailsClosedWhenSlotStoreNotRecovered is the
// propose-side half of the slot-restart fail-closed fix
// (docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8): a node that has not
// recovered its slot/epoch clock from committed history must refuse to
// PROPOSE, exactly as consensus_sync_gate.go's consensusVoteReady refuses to
// VOTE. Deliberately does NOT use resetSlotAndPeriodStores's helper (that
// helper marks the store ready specifically so the OTHER tests in this file
// can test field-assignment logic in isolation from this gate).
func TestAttachAVCConsensusFields_FailsClosedWhenSlotStoreNotRecovered(t *testing.T) {
	savedSlot, savedPeriod := messaging.DefaultSlotStore, messaging.DefaultPeriodStore
	messaging.DefaultSlotStore = messaging.NewSlotStore()
	messaging.DefaultPeriodStore = messaging.NewPeriodStore()
	messaging.ResetSlotStoreReadyForTest()
	savedEnforce := messaging.EnforceSlotRecoveryGate
	messaging.EnforceSlotRecoveryGate = true
	t.Cleanup(func() {
		messaging.DefaultSlotStore = savedSlot
		messaging.DefaultPeriodStore = savedPeriod
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

// landOnBoundarySlot advances DefaultSlotStore directly to slot 49 (via a
// single AdvanceOnCommit(0, 48) — the advance amount is period+1, so this
// is equivalent to 49 ordinary commits without looping) so that
// LiveSlotFor(1) = 49 + PeriodFor(1) + 1 = 50, exactly
// messaging.EpochBoundarySlot(1). Must be called after resetSlotAndPeriodStores.
func landOnBoundarySlot(t *testing.T) {
	t.Helper()
	messaging.DefaultSlotStore.AdvanceOnCommit(0, 48)
}

func TestAttachAVCConsensusFields_NonBoundarySlot_LeavesVdfProofZero(t *testing.T) {
	resetSlotAndPeriodStores(t)

	// Same setup as TestAttachAVCConsensusFields_SetsSlotAndPeriodFromLiveStores:
	// two commits then BlockNumber=2 -> slot 3, not a multiple of messaging.N (50).
	messaging.DefaultSlotStore.AdvanceOnCommit(0, 0)
	messaging.DefaultSlotStore.AdvanceOnCommit(1, 0)

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

func TestAttachAVCConsensusFields_BoundarySlot_AttachesProofWhenReady(t *testing.T) {
	resetSlotAndPeriodStores(t)
	landOnBoundarySlot(t)

	want := vdf.Proof{Y: big.NewInt(7), Pi: big.NewInt(11), T: 1234, Group: "test-group"}
	Sequencer.SeedSealResultForTest(1, Sequencer.SealResult{ForEpoch: 1, Proof: want})

	block := &config.ZKBlock{BlockNumber: 1}
	if err := attachAVCConsensusFields(block); err != nil {
		t.Fatalf("attachAVCConsensusFields: unexpected error: %v", err)
	}
	if block.Slot != messaging.EpochBoundarySlot(1) {
		t.Fatalf("test setup error: block.Slot = %d, want %d (epoch 1's boundary)", block.Slot, messaging.EpochBoundarySlot(1))
	}
	if block.SeedEpoch != 1 {
		t.Fatalf("block.SeedEpoch = %d, want 1", block.SeedEpoch)
	}
	if len(block.VdfProof) == 0 {
		t.Fatal("boundary block: VdfProof is empty, want the seeded proof's encoding")
	}
	var got vdf.Proof
	if err := got.UnmarshalBinary(block.VdfProof); err != nil {
		t.Fatalf("block.VdfProof does not round-trip via vdf.Proof.UnmarshalBinary: %v", err)
	}
	if got.T != want.T || got.Group != want.Group || got.Y.Cmp(want.Y) != 0 || got.Pi.Cmp(want.Pi) != 0 {
		t.Fatalf("round-tripped proof = %+v, want %+v", got, want)
	}
}

func TestAttachAVCConsensusFields_BoundarySlot_FailsClosedWhenProofNotReady(t *testing.T) {
	resetSlotAndPeriodStores(t)
	landOnBoundarySlot(t)
	// Deliberately do not seed a result for epoch 1 — SealerResultFor must
	// report ok=false, and attachAVCConsensusFields must fail closed rather
	// than propose with a missing entropy value for its own boundary slot.

	block := &config.ZKBlock{BlockNumber: 1}
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

func TestAttachAVCConsensusFields_BoundarySlot_FailsClosedWhenSealingErrored(t *testing.T) {
	resetSlotAndPeriodStores(t)
	landOnBoundarySlot(t)

	sealErr := errors.New("simulated VDF evaluation failure")
	Sequencer.SeedSealResultForTest(1, Sequencer.SealResult{ForEpoch: 1, Err: sealErr})

	block := &config.ZKBlock{BlockNumber: 1}
	err := attachAVCConsensusFields(block)
	if !errors.Is(err, sealErr) {
		t.Fatalf("attachAVCConsensusFields error = %v, want errors.Is(..., sealErr)", err)
	}
	if block.VdfProof != nil {
		t.Fatalf("VdfProof = %x on sealing failure, want nil", block.VdfProof)
	}
}
