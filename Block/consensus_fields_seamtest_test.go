//go:build sequencertest

package Block

// D-49: these two tests were extracted from consensus_fields_test.go because
// they depend on Sequencer.SeedSealResultForTest/ClearSealerForTest, which
// now live behind the `sequencertest` build tag
// (Sequencer/vdf_seal_wiring_testhelpers.go), to keep those seams out of the
// release binary. Both helpers — resetSlotAndPeriodStores and
// landOnBoundarySlot — stay in consensus_fields_test.go (an untagged file in
// the same package), which is fine: build tags only gate the file they're
// declared in, not cross-file visibility within a package.
//
// Run with `go test -tags sequencertest ./Sequencer/... ./Block/...`.

import (
	"errors"
	"math/big"
	"testing"

	"gossipnode/Sequencer"
	"gossipnode/config"
	"gossipnode/messaging"

	"github.com/JupiterMetaLabs/avc/vdf"
)

func TestAttachAVCConsensusFields_BoundarySlot_AttachesProofWhenReady(t *testing.T) {
	resetSlotAndPeriodStores(t)
	landOnBoundarySlot(t)
	t.Cleanup(Sequencer.SetVDFPipelineInstalledForTest(true))

	want := vdf.Proof{Y: big.NewInt(7), Pi: big.NewInt(11), T: 1234, Group: "test-group"}
	Sequencer.SeedSealResultForTest(1, Sequencer.SealResult{ForEpoch: 1, Proof: want})
	t.Cleanup(func() { Sequencer.ClearSealerForTest(1) })

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

func TestAttachAVCConsensusFields_BoundarySlot_FailsClosedWhenSealingErrored(t *testing.T) {
	resetSlotAndPeriodStores(t)
	landOnBoundarySlot(t)
	t.Cleanup(Sequencer.SetVDFPipelineInstalledForTest(true))

	sealErr := errors.New("simulated VDF evaluation failure")
	Sequencer.SeedSealResultForTest(1, Sequencer.SealResult{ForEpoch: 1, Err: sealErr})
	t.Cleanup(func() { Sequencer.ClearSealerForTest(1) })

	block := &config.ZKBlock{BlockNumber: 1}
	err := attachAVCConsensusFields(block)
	if !errors.Is(err, sealErr) {
		t.Fatalf("attachAVCConsensusFields error = %v, want errors.Is(..., sealErr)", err)
	}
	if block.VdfProof != nil {
		t.Fatalf("VdfProof = %x on sealing failure, want nil", block.VdfProof)
	}
}
