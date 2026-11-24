package messaging

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"

	"gossipnode/config"
)

// D-54 regression, jmdn-side (the avc PoC cannot reach this call path). A
// boundary block whose declared VDF identity differs from this node's is
// rejected on adoption, with the cause named — not left to a nameless
// vdf.Verify failure.
func TestVerifyAndAcceptVDFProof_RejectsIdentityMismatch(t *testing.T) {
	SetVDFProofAcceptor(func(uint64, randao.Seed, []byte) error { return nil })
	t.Cleanup(func() { SetVDFProofAcceptor(nil) })
	SetLocalVDFIdentity("aaaaaaaa")
	t.Cleanup(func() { SetLocalVDFIdentity("") })

	blk := &config.ZKBlock{
		VdfProof:        []byte{1, 2, 3},
		VdfParamsDigest: "bbbbbbbb", // != local
		SeedEpoch:       1,
	}
	if err := VerifyAndAcceptVDFProof(blk); !errors.Is(err, ErrVDFIdentityMismatch) {
		t.Fatalf("want ErrVDFIdentityMismatch, got %v", err)
	}
}

// A matching identity clears CHECK 0 and the function fails later (the boundary
// check), proving the identity gate does not fire on a match.
func TestVerifyAndAcceptVDFProof_IdentityMatchClearsCheck0(t *testing.T) {
	SetVDFProofAcceptor(func(uint64, randao.Seed, []byte) error { return nil })
	t.Cleanup(func() { SetVDFProofAcceptor(nil) })
	SetLocalVDFIdentity("cccccccc")
	t.Cleanup(func() { SetLocalVDFIdentity("") })

	blk := &config.ZKBlock{
		VdfProof:        []byte{1, 2, 3},
		VdfParamsDigest: "cccccccc", // == local
		SeedEpoch:       1,
		Slot:            1 << 40, // deliberately not the boundary slot
	}
	if err := VerifyAndAcceptVDFProof(blk); errors.Is(err, ErrVDFIdentityMismatch) {
		t.Fatalf("identity matched but still got mismatch: %v", err)
	}
}

// Additive rollout: an empty identity on either side (Stage-1 node, or a
// pre-upgrade proposer) must NOT trip the gate.
func TestVerifyAndAcceptVDFProof_IdentityGateIsAdditive(t *testing.T) {
	SetVDFProofAcceptor(func(uint64, randao.Seed, []byte) error { return nil })
	t.Cleanup(func() { SetVDFProofAcceptor(nil) })

	// local set, block empty (pre-upgrade proposer)
	SetLocalVDFIdentity("dddddddd")
	t.Cleanup(func() { SetLocalVDFIdentity("") })
	blk := &config.ZKBlock{VdfProof: []byte{1}, VdfParamsDigest: "", SeedEpoch: 1, Slot: 1 << 40}
	if err := VerifyAndAcceptVDFProof(blk); errors.Is(err, ErrVDFIdentityMismatch) {
		t.Fatalf("empty block identity should skip the gate, got %v", err)
	}

	// local empty (Stage-1), block carries one
	SetLocalVDFIdentity("")
	blk2 := &config.ZKBlock{VdfProof: []byte{1}, VdfParamsDigest: "eeee", SeedEpoch: 1, Slot: 1 << 40}
	if err := VerifyAndAcceptVDFProof(blk2); errors.Is(err, ErrVDFIdentityMismatch) {
		t.Fatalf("empty local identity should skip the gate, got %v", err)
	}
}
