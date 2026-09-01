package BLS_Verifier

import (
	"encoding/hex"
	"os"
	"testing"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// Tests sign with an ephemeral keypair (no provisioned config/bls.json);
// production loads-only and fails closed (getBLSKeypair).
func init() { os.Setenv("JMDN_BLS_AUTOGEN", "1") }

// Committee votes are v3-only: the signed bytes bind the network chain id, the
// block HEIGHT and the block hash. v1 (block-only) and v2 (chain, no height) are
// no longer accepted. These tests pin cross-chain,
// cross-height, field-binding and downgrade-rejection.

const (
	chainA = uint64(8000800)
	chainB = uint64(7000700)
	// A representative 32-byte block hash hex (lower-case, as normalized).
	blkHash = "0x1111111111111111111111111111111111111111111111111111111111111111"
)

// TestVoteDomain_CrossChainReplayRejected: a v3 vote signed on chain A must NOT
// verify on chain B (fork / testnet↔mainnet domain separation).
func TestVoteDomain_CrossChainReplayRejected(t *testing.T) {
	respA, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, 100, blkHash, "")
	if err != nil || !ok {
		t.Fatalf("sign v3 on chainA: ok=%v err=%v", ok, err)
	}
	if err := VerifyForBlock(respA, chainA, 100, blkHash, "", 1); err != nil {
		t.Fatalf("v3 vote should verify on its own chain A: %v", err)
	}
	if err := VerifyForBlock(respA, chainB, 100, blkHash, "", 1); err == nil {
		t.Fatalf("SECURITY: chainA vote verified on chainB — cross-chain replay not closed")
	}
}

// TestVoteDomain_V3HeightBinding: a v3 vote for one height must NOT verify at
// another height, and a different chain is still rejected.
func TestVoteDomain_V3HeightBinding(t *testing.T) {
	resp, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, 100, blkHash, "")
	if err != nil || !ok {
		t.Fatalf("sign v3: ok=%v err=%v", ok, err)
	}
	if err := VerifyForBlock(resp, chainA, 100, blkHash, "", 1); err != nil {
		t.Fatalf("v3 vote should verify at its own height: %v", err)
	}
	if err := VerifyForBlock(resp, chainA, 200, blkHash, "", 1); err == nil {
		t.Fatalf("v3 vote for height 100 verified at height 200 — height not bound")
	}
	if err := VerifyForBlock(resp, chainB, 100, blkHash, "", 1); err == nil {
		t.Fatalf("SECURITY: v3 vote verified on a different chain")
	}
}

// TestVoteDomain_V3FieldBinding: the v3 domain binds the block hash and the vote
// value.
func TestVoteDomain_V3FieldBinding(t *testing.T) {
	resp, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, 100, blkHash, "")
	if err != nil || !ok {
		t.Fatalf("sign v3: ok=%v err=%v", ok, err)
	}
	otherHash := "0x2222222222222222222222222222222222222222222222222222222222222222"
	if err := VerifyForBlock(resp, chainA, 100, otherHash, "", 1); err == nil {
		t.Fatalf("SECURITY: vote for %s verified against %s (block hash not bound)", blkHash, otherHash)
	}
	if err := VerifyForBlock(resp, chainA, 100, blkHash, "", -1); err == nil {
		t.Fatalf("SECURITY: +1 signature verified as -1 (vote value not bound)")
	}
}

// TestVoteDomain_DowngradeRejected: a genuine BLS signature over the OLD v1
// (block-only) or v2 (chain, no height) canonical bytes MUST NOT verify under
// v3-only. The verifier now builds only v3 bytes, so any non-v3 signature fails.
func TestVoteDomain_DowngradeRejected(t *testing.T) {
	priv, pub, err := blssign.GenerateBLSKeyPair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	build := func(msg string) BLS_Signer.BLSresponse {
		sig, err := blssign.BLSSign(priv, []byte(msg))
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return *BLS_Signer.NewBLSresponseBuilder(nil).
			SetSignature(hex.EncodeToString(sig)).
			SetAgree(true).
			SetPubKey(hex.EncodeToString(pub)).
			Build()
	}

	// v1: "zkvote:<blockhash>:<vote>" (block-only, no chain, no height).
	v1 := build(BLS_Signer.BlockBoundVotePrefix + blkHash + ":1")
	if err := VerifyForBlock(v1, chainA, 100, blkHash, "", 1); err == nil {
		t.Fatalf("a v1 signature was accepted under v3-only")
	}
	// v2: "zkvote:v2:chain=8000800:<blockhash>:<vote>" (chain, no height).
	v2 := build(BLS_Signer.BlockBoundVotePrefix + "v2:chain=8000800:" + blkHash + ":1")
	if err := VerifyForBlock(v2, chainA, 100, blkHash, "", 1); err == nil {
		t.Fatalf("a v2 signature was accepted under v3-only")
	}
}

// TestVoteDomain_InvalidInputsRejected: empty bindings and invalid vote values
// are rejected by the v3 canonical builder.
func TestVoteDomain_InvalidInputsRejected(t *testing.T) {
	if _, err := BLS_Signer.CanonicalVoteMessageV3(chainA, 100, "  ", 1); err == nil {
		t.Fatalf("empty bindings should be rejected")
	}
	if _, err := BLS_Signer.CanonicalVoteMessageV3(chainA, 100, blkHash, 0); err == nil {
		t.Fatalf("vote value 0 should be rejected")
	}
}
