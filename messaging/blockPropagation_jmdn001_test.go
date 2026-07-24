package messaging

// Adversarial regression tests for JMDN-001 (P2P block validation bypass),
// covering both Phase 1 (fail-closed gate) and Phase 2 (block-bound votes,
// committee registry, equivocation).
//
// These assert that the gate rejects crafted blocks BEFORE any forwarding,
// mutation, or persistence, and accepts a well-formed, block-bound-certified
// block.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"os"
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// testMembers is the default test committee: three distinct BLS keypairs whose
// PeerIDs (peerA/peerB/peerC) form the default eligible buddy set. Committee
// membership is dynamic (P1) — sourced from an injected eligibility function,
// not a file — so the harness installs defaultTestEligibility and signs each
// vote with the key whose PeerID is eligible.
var testMembers map[string]blsMember

// defaultTestEligibility is the eligibility source used by the harness and
// restored by per-test cleanups: peerA/peerB/peerC.
func defaultTestEligibility() (map[string]struct{}, error) {
	return map[string]struct{}{"peerA": {}, "peerB": {}, "peerC": {}}, nil
}

// withBlockBuddy sets the operator block_buddy blocklist on the loaded config
// for the duration of a test, restoring the previous value on cleanup.
func withBlockBuddy(t *testing.T, ids ...string) {
	t.Helper()
	cfg := settings.Get()
	prev := cfg.Consensus.BlockBuddy
	cfg.Consensus.BlockBuddy = ids
	t.Cleanup(func() { cfg.Consensus.BlockBuddy = prev })
}

// TestMain loads default node settings so the Security package's async logger
// can initialize, disables DB-dependent block linkage (no DB under unit test),
// installs the default eligibility source, and removes any BLS key file the
// signing calls persist to the working tree.
func TestMain(m *testing.M) {
	if _, err := settings.Load(); err != nil {
		panic("load settings: " + err.Error())
	}
	EnforceBlockLinkage = false // checkLinkage needs a live DB; out of scope here

	testMembers = make(map[string]blsMember)
	for i, pid := range []string{"peerA", "peerB", "peerC"} {
		testMembers[pid] = mustMintMember(pid, byte(0x10+i))
	}
	SetCommitteeEligibilitySource(defaultTestEligibility)

	code := m.Run()
	_ = os.Remove("config/bls.json")
	_ = os.Remove("config/peer.json")
	_ = os.Remove("config")
	os.Exit(code)
}

var testChainID = big.NewInt(1337)

func resetEquivocation() {
	seenHeightsMu.Lock()
	seenHeights = make(map[uint64]string)
	seenHeightsMu.Unlock()
}

// signedTx builds a config.Transaction with a real EIP-1559 signature from key.
func signedTx(t *testing.T, key *ecdsa.PrivateKey, nonce uint64) config.Transaction {
	t.Helper()
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	inner := &types.DynamicFeeTx{
		ChainID: testChainID, Nonce: nonce, To: &to, Value: big.NewInt(1),
		GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(1), Gas: 21000,
	}
	signed, err := types.SignNewTx(key, types.LatestSignerForChainID(testChainID), inner)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	v, r, s := signed.RawSignatureValues()
	return config.Transaction{
		Hash: signed.Hash(), From: &from, To: &to, Value: big.NewInt(1),
		Type: types.DynamicFeeTxType, ChainID: testChainID, Nonce: nonce, GasLimit: 21000,
		MaxFee: big.NewInt(1), MaxPriorityFee: big.NewInt(1), V: v, R: r, S: s,
	}
}

// blockBoundCert returns a Data map with a certificate of block-bound +1 votes,
// one per PeerID, each signed over blockHashHex with the key REGISTERED for
// that PeerID in the default test committee registry.
func blockBoundCert(t *testing.T, blockHashHex string, peerIDs ...string) map[string]string {
	t.Helper()
	var resps []BLS_Signer.BLSresponse
	for _, pid := range peerIDs {
		mem, ok := testMembers[pid]
		if !ok {
			t.Fatalf("blockBoundCert: %q is not in the default test committee (peerA/peerB/peerC)", pid)
		}
		resps = append(resps, mem.blockVote(t, blockHashHex, 1))
	}
	b, err := json.Marshal(resps)
	if err != nil {
		t.Fatalf("marshal cert: %v", err)
	}
	return map[string]string{"bls_results": string(b)}
}

// legacyCert returns a certificate of legacy (unbound) +1 votes from registered
// test-committee members.
func legacyCert(t *testing.T, peerIDs ...string) map[string]string {
	t.Helper()
	var resps []BLS_Signer.BLSresponse
	for _, pid := range peerIDs {
		mem, ok := testMembers[pid]
		if !ok {
			t.Fatalf("legacyCert: %q is not in the default test committee (peerA/peerB/peerC)", pid)
		}
		resps = append(resps, mem.legacyVote(t, 1))
	}
	b, _ := json.Marshal(resps)
	return map[string]string{"bls_results": string(b)}
}

func TestVerifyBlockCertificate(t *testing.T) {
	hash := common.HexToHash("0xabc123")
	valid := blockBoundCert(t, hash.Hex(), "peerA", "peerB", "peerC")

	cases := []struct {
		name       string
		data       map[string]string
		wantReason string // "" == accept
	}{
		{"omitted certificate", map[string]string{}, "no_certificate"},
		{"empty certificate", map[string]string{"bls_results": ""}, "no_certificate"},
		{"malformed json", map[string]string{"bls_results": "{not json"}, "malformed_certificate"},
		{"empty array", map[string]string{"bls_results": "[]"}, "no_certificate"},
		{"below quorum (2 of 3)", blockBoundCert(t, hash.Hex(), "peerA", "peerB"), "quorum_not_met"},
		{"ballot stuffing (same signer x3)", blockBoundCert(t, hash.Hex(), "peerA", "peerA", "peerA"), "quorum_not_met"},
		{"legacy cert rejected when RejectLegacyVotes on", legacyCert(t, "peerA", "peerB", "peerC"), "quorum_not_met"},
		{"valid block-bound quorum", valid, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := config.BlockMessage{Block: &config.ZKBlock{BlockHash: hash}, Data: tc.data}
			rej := verifyBlockCertificate(msg)
			if tc.wantReason == "" {
				if rej != nil {
					t.Fatalf("expected accept, got reject reason=%s err=%v", rej.reason, rej.err)
				}
				return
			}
			if rej == nil || rej.reason != tc.wantReason {
				t.Fatalf("reason=%v, want %s", rej, tc.wantReason)
			}
		})
	}
}

func TestVerifyBlockCertificate_LegacyAcceptedWhenAllowed(t *testing.T) {
	prev := RejectLegacyVotes
	RejectLegacyVotes = false
	defer func() { RejectLegacyVotes = prev }()

	hash := common.HexToHash("0xdef456")
	msg := config.BlockMessage{
		Block: &config.ZKBlock{BlockHash: hash},
		Data:  legacyCert(t, "peerA", "peerB", "peerC"),
	}
	if rej := verifyBlockCertificate(msg); rej != nil {
		t.Fatalf("legacy cert should be accepted when RejectLegacyVotes=false, got %s", rej.reason)
	}
}

func TestVerifyBlockCertificate_ReplayOntoDifferentBlockFails(t *testing.T) {
	// A cert validly signed for block A must NOT verify against block B.
	certForA := blockBoundCert(t, common.HexToHash("0xAAAA").Hex(), "peerA", "peerB", "peerC")
	msgB := config.BlockMessage{Block: &config.ZKBlock{BlockHash: common.HexToHash("0xBBBB")}, Data: certForA}
	if rej := verifyBlockCertificate(msgB); rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("replayed cert should fail on a different block, got %v", rej)
	}
}

func TestValidateRemoteBlock(t *testing.T) {
	ctx := context.Background()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	newBlock := func(hashHex string, num uint64, txs ...config.Transaction) *config.ZKBlock {
		return &config.ZKBlock{BlockHash: common.HexToHash(hashHex), BlockNumber: num, Transactions: txs}
	}

	t.Run("happy path accepted", func(t *testing.T) {
		resetEquivocation()
		h := "0x1111"
		b := newBlock(h, 10, signedTx(t, key, 0))
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b.BlockHash.Hex(), "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej != nil {
			t.Fatalf("expected accept, got reason=%s err=%v", rej.reason, rej.err)
		}
	})

	t.Run("nil block rejected", func(t *testing.T) {
		if rej := validateRemoteBlock(ctx, config.BlockMessage{}); rej == nil || rej.reason != "nil_block" {
			t.Fatalf("want nil_block, got %v", rej)
		}
	})

	t.Run("forged tx signature rejected", func(t *testing.T) {
		resetEquivocation()
		bad := signedTx(t, key, 0)
		bad.R = new(big.Int).Add(bad.R, big.NewInt(1))
		b := newBlock("0x2222", 11, bad)
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b.BlockHash.Hex(), "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "bad_signature" {
			t.Fatalf("want bad_signature, got %v", rej)
		}
	})

	t.Run("non-ascending sender nonce rejected", func(t *testing.T) {
		resetEquivocation()
		b := newBlock("0x3333", 12, signedTx(t, key, 5), signedTx(t, key, 5))
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b.BlockHash.Hex(), "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "bad_nonce" {
			t.Fatalf("want bad_nonce, got %v", rej)
		}
	})

	t.Run("missing certificate rejected", func(t *testing.T) {
		resetEquivocation()
		b := newBlock("0x4444", 13, signedTx(t, key, 0))
		msg := config.BlockMessage{Block: b, Data: map[string]string{}}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "no_certificate" {
			t.Fatalf("want no_certificate, got %v", rej)
		}
	})

	t.Run("equivocation: conflicting block at same height rejected", func(t *testing.T) {
		resetEquivocation()
		// First validated block at height 20.
		b1 := newBlock("0x5555", 20, signedTx(t, key, 0))
		m1 := config.BlockMessage{Block: b1, Data: blockBoundCert(t, b1.BlockHash.Hex(), "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, m1); rej != nil {
			t.Fatalf("first block should pass, got %s", rej.reason)
		}
		// Second, DIFFERENT block at the same height 20.
		b2 := newBlock("0x6666", 20, signedTx(t, key, 0))
		m2 := config.BlockMessage{Block: b2, Data: blockBoundCert(t, b2.BlockHash.Hex(), "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, m2); rej == nil || rej.reason != "equivocation" {
			t.Fatalf("want equivocation, got %v", rej)
		}
	})
}
