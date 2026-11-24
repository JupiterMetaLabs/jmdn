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

// Committee votes bind the network chain id, the block HEIGHT, the block hash
// AND the block's ConsensusHash. Older formats that didn't bind the
// ConsensusHash (or chain/height at all) are no longer accepted. These tests
// pin cross-chain, cross-height, cross-consensus-hash, field-binding and
// downgrade-rejection.

const (
	chainA = uint64(8000800)
	chainB = uint64(7000700)
	// A representative 32-byte block hash hex (lower-case, as normalized).
	blkHash = "0x1111111111111111111111111111111111111111111111111111111111111111"
	// A representative 32-byte consensus hash hex, distinct from blkHash.
	consHash  = "0x3333333333333333333333333333333333333333333333333333333333333333"
	consHashB = "0x4444444444444444444444444444444444444444444444444444444444444444"
)

// TestVoteDomain_CrossChainReplayRejected: a vote signed on chain A must NOT
// verify on chain B (fork / testnet↔mainnet domain separation).
func TestVoteDomain_CrossChainReplayRejected(t *testing.T) {
	respA, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, 100, blkHash, consHash)
	if err != nil || !ok {
		t.Fatalf("sign on chainA: ok=%v err=%v", ok, err)
	}
	if err := VerifyForBlock(respA, chainA, 100, blkHash, consHash, 1); err != nil {
		t.Fatalf("vote should verify on its own chain A: %v", err)
	}
	if err := VerifyForBlock(respA, chainB, 100, blkHash, consHash, 1); err == nil {
		t.Fatalf("SECURITY: chainA vote verified on chainB — cross-chain replay not closed")
	}
}

// TestVoteDomain_HeightBinding: a vote for one height must NOT verify at
// another height, and a different chain is still rejected.
func TestVoteDomain_HeightBinding(t *testing.T) {
	resp, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, 100, blkHash, consHash)
	if err != nil || !ok {
		t.Fatalf("sign: ok=%v err=%v", ok, err)
	}
	if err := VerifyForBlock(resp, chainA, 100, blkHash, consHash, 1); err != nil {
		t.Fatalf("vote should verify at its own height: %v", err)
	}
	if err := VerifyForBlock(resp, chainA, 200, blkHash, consHash, 1); err == nil {
		t.Fatalf("vote for height 100 verified at height 200 — height not bound")
	}
	if err := VerifyForBlock(resp, chainB, 100, blkHash, consHash, 1); err == nil {
		t.Fatalf("SECURITY: vote verified on a different chain")
	}
}

// TestVoteDomain_FieldBinding: the vote domain binds the block hash, the
// ConsensusHash and the vote value.
func TestVoteDomain_FieldBinding(t *testing.T) {
	resp, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, 100, blkHash, consHash)
	if err != nil || !ok {
		t.Fatalf("sign: ok=%v err=%v", ok, err)
	}
	otherHash := "0x2222222222222222222222222222222222222222222222222222222222222222"
	if err := VerifyForBlock(resp, chainA, 100, otherHash, consHash, 1); err == nil {
		t.Fatalf("SECURITY: vote for %s verified against %s (block hash not bound)", blkHash, otherHash)
	}
	if err := VerifyForBlock(resp, chainA, 100, blkHash, consHashB, 1); err == nil {
		t.Fatalf("SECURITY regression — a signature over consensus hash %s verified against a different consensus hash %s", consHash, consHashB)
	}
	if err := VerifyForBlock(resp, chainA, 100, blkHash, consHash, -1); err == nil {
		t.Fatalf("SECURITY: +1 signature verified as -1 (vote value not bound)")
	}
}

// TestVoteDomain_DowngradeRejected: a genuine BLS signature over an OLDER
// canonical format — block-only, chain-but-no-height, or height-bound but with
// no ConsensusHash at all — MUST NOT verify under the current, single accepted
// format. The verifier now builds only one message shape, so any
// differently-shaped signature fails.
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
	if err := VerifyForBlock(v1, chainA, 100, blkHash, consHash, 1); err == nil {
		t.Fatalf("a v1 signature was accepted under the current format")
	}
	// v2: "zkvote:v2:chain=8000800:<blockhash>:<vote>" (chain, no height).
	v2 := build(BLS_Signer.BlockBoundVotePrefix + "v2:chain=8000800:" + blkHash + ":1")
	if err := VerifyForBlock(v2, chainA, 100, blkHash, consHash, 1); err == nil {
		t.Fatalf("a v2 signature was accepted under the current format")
	}
	// Old v3: "zkvote:v3:chain=<id>:h=<height>:<blockhash>:<vote>" — height-bound
	// but with no "ch=<consensushash>" segment at all. This is the exact
	// regression this format change closes: a signature that never committed to the consensus fields must
	// not verify, no matter what ConsensusHash the verifier is asked to check it
	// against.
	hashOnly := build(BLS_Signer.BlockBoundVotePrefix + "v3:chain=8000800:h=100:" + blkHash + ":1")
	if err := VerifyForBlock(hashOnly, chainA, 100, blkHash, consHash, 1); err == nil {
		t.Fatalf("SECURITY: a hash-only signature with no ConsensusHash binding was accepted")
	}
}

// TestVoteDomain_InvalidInputsRejected: empty bindings, empty consensus hash
// and invalid vote values are rejected by the canonical builder.
func TestVoteDomain_InvalidInputsRejected(t *testing.T) {
	if _, err := BLS_Signer.CanonicalVoteMessageV3(chainA, 100, "  ", consHash, 1); err == nil {
		t.Fatalf("empty bindings should be rejected")
	}
	if _, err := BLS_Signer.CanonicalVoteMessageV3(chainA, 100, blkHash, "  ", 1); err == nil {
		t.Fatalf("empty consensus hash should be rejected")
	}
	if _, err := BLS_Signer.CanonicalVoteMessageV3(chainA, 100, blkHash, consHash, 0); err == nil {
		t.Fatalf("vote value 0 should be rejected")
	}
	if _, _, err := BLS_Signer.SignMessageForBlock(1, chainA, 100, blkHash, ""); err == nil {
		t.Fatalf("SignMessageForBlock should reject an empty consensus hash")
	}
}
