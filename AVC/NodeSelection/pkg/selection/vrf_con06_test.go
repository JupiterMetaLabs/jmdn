package selection

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/yahoo/coname/vrf"
)

// CON-06 defect 2: committee selection must be REPRODUCIBLE for identical inputs.
// The pre-existing "determinism" test only logs both results (and comments that
// they "may differ"); this one ASSERTS the selected order is identical across
// calls, which fails without the regions-sort fix (map iteration is randomized).
func TestCON06_SelectMultipleBuddies_DeterministicOrder(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	sel, err := NewVRFSelector(&VRFConfig{NetworkSalt: []byte("test-salt"), PrivateKey: priv})
	if err != nil {
		t.Fatalf("selector: %v", err)
	}
	vs := sel.(*VRFSelector)
	nodes := createTestNodes(100, 5)
	ctx := context.Background()

	first, err := vs.SelectMultipleBuddies(ctx, "node-orchestrator", nodes, 13)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// Run several more times; every run must match the first exactly.
	for r := 0; r < 8; r++ {
		got, err := vs.SelectMultipleBuddies(ctx, "node-orchestrator", nodes, 13)
		if err != nil {
			t.Fatalf("run %d: %v", r, err)
		}
		if len(got) != len(first) {
			t.Fatalf("run %d: length %d != %d", r, len(got), len(first))
		}
		for i := range got {
			if got[i].Node.PeerId != first[i].Node.PeerId {
				t.Fatalf("run %d position %d: %s != %s (selection not reproducible)",
					r, i, got[i].Node.PeerId, first[i].Node.PeerId)
			}
		}
	}
}

// CON-06 defect 1: the round-bound message must bind round, node, salt, and be
// domain-tagged so it can never collide with the legacy nodeID:salt message.
func TestCON06_BuildVRFRoundMessage_Binds(t *testing.T) {
	salt := []byte("salt")
	base := BuildVRFRoundMessage("nodeA", salt, 100)

	if !bytes.Contains(base, []byte(VRFRoundDomain)) {
		t.Fatal("round message is not domain-tagged")
	}
	// Legacy format is "nodeA:salt"; the round message must differ from it.
	if bytes.Equal(base, []byte("nodeA:salt")) {
		t.Fatal("round message collides with the legacy message format")
	}
	cases := map[string][]byte{
		"round": BuildVRFRoundMessage("nodeA", salt, 101),
		"node":  BuildVRFRoundMessage("nodeB", salt, 100),
		"salt":  BuildVRFRoundMessage("nodeA", []byte("other"), 100),
	}
	for name, m := range cases {
		if bytes.Equal(m, base) {
			t.Fatalf("changing %s did not change the round message", name)
		}
	}
	// Deterministic: same inputs -> same bytes.
	if !bytes.Equal(base, BuildVRFRoundMessage("nodeA", salt, 100)) {
		t.Fatal("round message is not deterministic")
	}
}

// CON-06 defect 3: a VRF proof must actually be verifiable. Prove over a message,
// verify it, then confirm every tamper is rejected (fail-closed).
func TestCON06_VerifyVRFProof_AcceptRejectTamper(t *testing.T) {
	pk, sk, err := vrf.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("vrf keygen: %v", err)
	}
	msg := BuildVRFRoundMessage("node-orchestrator", []byte("salt"), 42)
	vrfHash, proof := vrf.Prove(msg, sk)

	if !VerifyVRFProof(ed25519.PublicKey(pk), msg, vrfHash, proof) {
		t.Fatal("valid VRF proof was rejected")
	}

	// Tampered message (attacker-chosen round) must fail.
	badMsg := BuildVRFRoundMessage("node-orchestrator", []byte("salt"), 43)
	if VerifyVRFProof(ed25519.PublicKey(pk), badMsg, vrfHash, proof) {
		t.Fatal("proof verified against a different message (round not bound)")
	}
	// Tampered proof bytes must fail.
	badProof := append([]byte(nil), proof...)
	badProof[0] ^= 0xff
	if VerifyVRFProof(ed25519.PublicKey(pk), msg, vrfHash, badProof) {
		t.Fatal("tampered proof verified")
	}
	// Tampered vrf hash must fail.
	badHash := append([]byte(nil), vrfHash...)
	badHash[0] ^= 0xff
	if VerifyVRFProof(ed25519.PublicKey(pk), msg, badHash, proof) {
		t.Fatal("tampered vrf hash verified")
	}
	// Wrong public key must fail.
	otherPub, _, _ := vrf.GenerateKey(rand.Reader)
	if VerifyVRFProof(ed25519.PublicKey(otherPub), msg, vrfHash, proof) {
		t.Fatal("proof verified under the wrong public key")
	}
	// Degenerate inputs are rejected without panicking.
	if VerifyVRFProof(nil, msg, vrfHash, proof) {
		t.Fatal("nil public key accepted")
	}
	if VerifyVRFProof(ed25519.PublicKey(pk), msg, nil, proof) {
		t.Fatal("empty vrf hash accepted")
	}
}
