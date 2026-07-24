package BLS_Verifier

import (
	"encoding/hex"
	"testing"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// P4: votes are domain-separated by network chain id. These tests pin the core
// property — a committee vote signed for chain A must NOT verify as a valid vote
// on chain B (fork / testnet↔mainnet replay) — and document the exact residual
// exposure of the staged v1→v2 rollout.

const (
	chainA = uint64(8000800)
	chainB = uint64(7000700)
	// A representative 32-byte block hash hex (lower-case, as normalized).
	blkHash = "0x1111111111111111111111111111111111111111111111111111111111111111"
)

// TestVoteDomain_CrossChainReplayRejected is the P4 hot spot: a v2 signature
// captured on chain A cannot be replayed as a valid vote on chain B, and this
// holds even while legacy v1 acceptance is ON (the rollout flag must not open a
// cross-chain hole).
func TestVoteDomain_CrossChainReplayRejected(t *testing.T) {
	defer restoreV1Flag(AcceptV1BlockBoundVotes)
	AcceptV1BlockBoundVotes = true // worst case for the attacker's benefit

	respA, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, blkHash)
	if err != nil || !ok {
		t.Fatalf("sign v2 on chainA: ok=%v err=%v", ok, err)
	}

	// Same chain → verifies.
	if err := VerifyForBlock(respA, chainA, blkHash, 1); err != nil {
		t.Fatalf("v2 vote should verify on its own chain A: %v", err)
	}

	// Different chain → MUST be rejected (this is the whole point of P4).
	if err := VerifyForBlock(respA, chainB, blkHash, 1); err == nil {
		t.Fatalf("SECURITY: chainA vote verified on chainB — cross-chain replay not closed")
	}
}

// TestVoteDomain_V2RoundTripAndFieldBinding confirms the v2 domain also still
// binds the block hash and the vote value (regression guard for D3 properties).
func TestVoteDomain_V2RoundTripAndFieldBinding(t *testing.T) {
	defer restoreV1Flag(AcceptV1BlockBoundVotes)
	AcceptV1BlockBoundVotes = false // pure v2

	resp, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, blkHash)
	if err != nil || !ok {
		t.Fatalf("sign v2: ok=%v err=%v", ok, err)
	}

	if err := VerifyForBlock(resp, chainA, blkHash, 1); err != nil {
		t.Fatalf("v2 round-trip should pass: %v", err)
	}
	// Wrong block hash → reject.
	otherHash := "0x2222222222222222222222222222222222222222222222222222222222222222"
	if err := VerifyForBlock(resp, chainA, otherHash, 1); err == nil {
		t.Fatalf("SECURITY: vote for %s verified against %s (block hash not bound)", blkHash, otherHash)
	}
	// Wrong vote value → reject.
	if err := VerifyForBlock(resp, chainA, blkHash, -1); err == nil {
		t.Fatalf("SECURITY: +1 signature verified as -1 (vote value not bound)")
	}
}

// TestVoteDomain_V1RolloutGap documents the ONLY cross-chain exposure that
// exists during rollout: a genuine legacy v1 signature (block-bound but
// chain-agnostic) is accepted on any chain while AcceptV1BlockBoundVotes is on,
// and is rejected on every chain once the flag is turned off. This is the
// operator's lever to close the migration window.
func TestVoteDomain_V1RolloutGap(t *testing.T) {
	defer restoreV1Flag(AcceptV1BlockBoundVotes)

	// Build a real v1 signature with a fresh keypair.
	priv, pub, err := blssign.GenerateBLSKeyPair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	v1msg, err := canonicalVoteMessageV1(blkHash, 1)
	if err != nil {
		t.Fatalf("v1 canonical: %v", err)
	}
	sig, err := blssign.BLSSign(priv, v1msg)
	if err != nil {
		t.Fatalf("v1 sign: %v", err)
	}
	resp := *BLS_Signer.NewBLSresponseBuilder(nil).
		SetSignature(hex.EncodeToString(sig)).
		SetAgree(true).
		SetPubKey(hex.EncodeToString(pub)).
		Build()

	// Flag ON: v1 accepted regardless of chain id (documents the rollout gap).
	AcceptV1BlockBoundVotes = true
	if err := VerifyForBlock(resp, chainA, blkHash, 1); err != nil {
		t.Fatalf("v1 vote should be accepted on chainA during rollout: %v", err)
	}
	if err := VerifyForBlock(resp, chainB, blkHash, 1); err != nil {
		t.Fatalf("v1 vote should be accepted on chainB during rollout: %v", err)
	}

	// Flag OFF: v1 rejected everywhere — rollout complete, hole closed.
	AcceptV1BlockBoundVotes = false
	if err := VerifyForBlock(resp, chainA, blkHash, 1); err == nil {
		t.Fatalf("SECURITY: v1 vote still accepted after AcceptV1BlockBoundVotes disabled")
	}
}

// TestVoteDomain_SignerVerifierShareOneFormat guards against drift: the bytes
// the verifier builds MUST equal what the signer signs, since both derive from
// BLS_Signer.CanonicalVoteMessage.
func TestVoteDomain_SignerVerifierShareOneFormat(t *testing.T) {
	signerBytes, err := BLS_Signer.CanonicalVoteMessage(chainA, blkHash, 1)
	if err != nil {
		t.Fatalf("signer canonical: %v", err)
	}
	verifierBytes, err := CanonicalBlockVoteMessage(chainA, blkHash, 1)
	if err != nil {
		t.Fatalf("verifier canonical: %v", err)
	}
	if string(signerBytes) != string(verifierBytes) {
		t.Fatalf("signer/verifier format drift:\n signer=%q\n verifier=%q", signerBytes, verifierBytes)
	}
}

// TestVoteDomain_InvalidInputsRejected: empty bindings and invalid vote values
// are rejected by the canonical builder.
func TestVoteDomain_InvalidInputsRejected(t *testing.T) {
	if _, err := BLS_Signer.CanonicalVoteMessage(chainA, "  ", 1); err == nil {
		t.Fatalf("empty bindings should be rejected")
	}
	if _, err := BLS_Signer.CanonicalVoteMessage(chainA, blkHash, 0); err == nil {
		t.Fatalf("vote value 0 should be rejected")
	}
}

func restoreV1Flag(v bool) { AcceptV1BlockBoundVotes = v }
