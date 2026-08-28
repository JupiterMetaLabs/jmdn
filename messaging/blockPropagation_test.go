package messaging

// Regression tests for peer-to-peer block validation, covering the fail-closed
// gate as well as block-bound votes, committee registry, and equivocation.
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
	"strings"
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
// membership is dynamic — sourced from an injected eligibility function,
// not a file — so the harness installs defaultTestEligibility and signs each
// vote with the key whose PeerID is eligible.
var testMembers map[string]blsMember

// defaultCommitteePeerIDs is the default test committee: a fixed 4-member set
// (NOT tied to production MaxMainPeers, which is 7). The Byzantine quorum is
// ceil(2n/3): for n=4 that is 3, so the existing 3-vote assertions (peerA/B/C)
// remain exactly a quorum while a 2-vote cert stays below it. Tests that need a
// different committee size set their own source (useEligible/useEligibleBound).
var defaultCommitteePeerIDs = []string{"peerA", "peerB", "peerC", "peerD"}

// defaultTestEligibility is the eligibility source used by the harness and
// restored by per-test cleanups.
func defaultTestEligibility(_ uint64, _ bool) (map[string]string, error) {
	// Bind each default committee peer_id to its minted member key, so the
	// harness exercises the key binding (votes are signed with these same keys).
	set := make(map[string]string, len(defaultCommitteePeerIDs))
	for _, p := range defaultCommitteePeerIDs {
		if m, ok := testMembers[p]; ok {
			set[p] = m.pubHex
		} else {
			set[p] = ""
		}
	}
	return set, nil
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
	for i, pid := range defaultCommitteePeerIDs {
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
// one per PeerID, signed over the block's hash AND at the block's BlockNumber
// (v3). verifyBlockCertificate verifies each vote at msg.Block.BlockNumber, so
// the cert must be signed at that height — passing the block keeps the two in
// lock-step for blocks at any height.
func blockBoundCert(t *testing.T, block *config.ZKBlock, peerIDs ...string) map[string]string {
	t.Helper()
	return blockBoundCertH(t, block.BlockHash.Hex(), block.BlockNumber, peerIDs...)
}

// blockBoundCertH signs the certificate over an explicit hash and height, for
// tests that verify at height 0 or over a hash with no backing block value.
func blockBoundCertH(t *testing.T, blockHashHex string, height uint64, peerIDs ...string) map[string]string {
	t.Helper()
	var resps []BLS_Signer.BLSresponse
	for _, pid := range peerIDs {
		mem, ok := testMembers[pid]
		if !ok {
			t.Fatalf("blockBoundCert: %q is not in the default test committee (peerA/peerB/peerC)", pid)
		}
		resps = append(resps, mem.blockVoteAt(t, blockHashHex, 1, height))
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
	valid := blockBoundCertH(t, hash.Hex(), 0, "peerA", "peerB", "peerC")

	cases := []struct {
		name       string
		data       map[string]string
		wantReason string // "" == accept
	}{
		{"omitted certificate", map[string]string{}, "no_certificate"},
		{"empty certificate", map[string]string{"bls_results": ""}, "no_certificate"},
		{"malformed json", map[string]string{"bls_results": "{not json"}, "malformed_certificate"},
		{"empty array", map[string]string{"bls_results": "[]"}, "no_certificate"},
		{"below quorum (2 of 3)", blockBoundCertH(t, hash.Hex(), 0, "peerA", "peerB"), "quorum_not_met"},
		{"ballot stuffing (same signer x3)", blockBoundCertH(t, hash.Hex(), 0, "peerA", "peerA", "peerA"), "quorum_not_met"},
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
	certForA := blockBoundCertH(t, common.HexToHash("0xAAAA").Hex(), 0, "peerA", "peerB", "peerC")
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

	// newBlock builds a block whose BlockHash is the canonical hash of its txs
	// (body binding is on by default, so an arbitrary hash would be rejected
	// as body_mismatch). The hashHint arg is ignored, kept for readability.
	newBlock := func(_ string, num uint64, txs ...config.Transaction) *config.ZKBlock {
		return &config.ZKBlock{
			BlockHash:    RecomputeBlockHashFromTxs(txs),
			TxnsRoot:     RecomputeTxnsRoot(txs),
			BlockNumber:  num,
			Transactions: txs,
		}
	}

	t.Run("happy path accepted", func(t *testing.T) {
		resetEquivocation()
		h := "0x1111"
		b := newBlock(h, 10, signedTx(t, key, 0))
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej != nil {
			t.Fatalf("expected accept, got reason=%s err=%v", rej.reason, rej.err)
		}
	})

	t.Run("nil block rejected", func(t *testing.T) {
		if rej := validateRemoteBlock(ctx, config.BlockMessage{}); rej == nil || rej.reason != "nil_block" {
			t.Fatalf("want nil_block, got %v", rej)
		}
	})

	t.Run("invalid tx signature rejected", func(t *testing.T) {
		resetEquivocation()
		bad := signedTx(t, key, 0)
		bad.R = new(big.Int).Add(bad.R, big.NewInt(1))
		b := newBlock("0x2222", 11, bad)
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "bad_signature" {
			t.Fatalf("want bad_signature, got %v", rej)
		}
	})

	t.Run("non-ascending sender nonce rejected", func(t *testing.T) {
		resetEquivocation()
		b := newBlock("0x3333", 12, signedTx(t, key, 5), signedTx(t, key, 5))
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}
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
		// A second signer so the two blocks have genuinely different bodies
		// (hence different canonical hashes) — otherwise body binding, not
		// equivocation, is what differs.
		key2, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("genkey2: %v", err)
		}
		// First validated block at height 20.
		b1 := newBlock("", 20, signedTx(t, key, 0))
		m1 := config.BlockMessage{Block: b1, Data: blockBoundCert(t, b1, "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, m1); rej != nil {
			t.Fatalf("first block should pass, got %s", rej.reason)
		}
		// Second, DIFFERENT block (different tx set) at the same height 20.
		b2 := newBlock("", 20, signedTx(t, key2, 0))
		if b2.BlockHash == b1.BlockHash {
			t.Fatal("test setup: b1 and b2 must have different hashes")
		}
		m2 := config.BlockMessage{Block: b2, Data: blockBoundCert(t, b2, "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, m2); rej == nil || rej.reason != "equivocation" {
			t.Fatalf("want equivocation, got %v", rej)
		}
	})

	// A certified hash reused over a SUBSTITUTED body (a different, validly
	// signed tx set) must be rejected by body binding BEFORE the certificate is
	// honored. This is the core body-binding case.
	t.Run("body substitution under certified hash rejected", func(t *testing.T) {
		resetEquivocation()
		key2, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("genkey2: %v", err)
		}
		// Honest block + certificate over its canonical hash.
		honest := newBlock("", 30, signedTx(t, key, 0))
		certHash := honest.BlockHash
		// The block keeps the certified hash but swaps in a different, validly
		// signed body.
		swapped := &config.ZKBlock{
			BlockHash:    certHash, // reused certified hash
			BlockNumber:  30,
			Transactions: []config.Transaction{signedTx(t, key2, 0)},
		}
		msg := config.BlockMessage{Block: swapped, Data: blockBoundCert(t, swapped, "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "body_mismatch" {
			t.Fatalf("want body_mismatch for substituted body, got %v", rej)
		}
	})

	// tx.Hash is a remote-supplied wire field and body binding hashes OVER it. A
	// crafted transaction can set tx.Hash so RecomputeBlockHashFromTxs reproduces
	// a certified BlockHash and then reuse that block's real certificate. The
	// receive path MUST verify tx.Hash against contents and reject the mismatch
	// BEFORE body binding.
	t.Run("tx.Hash not matching contents rejected", func(t *testing.T) {
		resetEquivocation()
		otherTx := signedTx(t, key, 0) // valid contents and signature
		otherTx.Hash = common.HexToHash("0x00000000000000000000000000000000000000000000000000000000deadbeef")
		txs := []config.Transaction{otherTx}
		// Body hashes computed over the mismatched tx.Hash (as the generator
		// formula does), so body binding alone would pass.
		b := &config.ZKBlock{
			BlockHash:    RecomputeBlockHashFromTxs(txs),
			TxnsRoot:     RecomputeTxnsRoot(txs),
			BlockNumber:  33,
			Transactions: txs,
		}
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "tx_hash_mismatch" {
			t.Fatalf("want tx_hash_mismatch for mismatched tx.Hash, got %v", rej)
		}
	})

	// A mismatched TxnsRoot alone (body binding second axis) is rejected.
	t.Run("txnsroot mismatch rejected", func(t *testing.T) {
		resetEquivocation()
		b := newBlock("", 31, signedTx(t, key, 0))
		b.TxnsRoot = "0x" + strings.Repeat("c", 64) // wrong root
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "txnsroot_mismatch" {
			t.Fatalf("want txnsroot_mismatch, got %v", rej)
		}
	})

	// KNOWN GAP (pinned): the generator's BlockHash does NOT cover
	// StarkProof/Commitment, so swapping the proof field while keeping the
	// certified hash is NOT detected by body binding today. This test PINS that
	// accepted limitation so it is visible. Closing it requires a generator
	// hash-scheme change; when that lands, flip this to expect a rejection.
	t.Run("PROOF GAP: swapped StarkProof under same hash currently accepted", func(t *testing.T) {
		resetEquivocation()
		b := newBlock("", 32, signedTx(t, key, 0))
		b.StarkProof = []byte("swapped-proof")
		b.Commitment = []uint32{1, 2, 3}
		msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}
		if rej := validateRemoteBlock(ctx, msg); rej != nil {
			t.Fatalf("proof fields are not in BlockHash today, so a swap is expected to PASS body binding until the generator hashes them; got reject %s. If proof binding was added, update this test to expect rejection.", rej.reason)
		}
	})
}

// FeeRecipients is not committed to by the canonical block hash and is not
// credited by the catch-up (FastsyncV2) apply path, so a block carrying it would
// diverge silently between live and catch-up nodes. validateRemoteBlock must
// refuse such a block fail-closed until it is hash-bound and catch-up-threaded.
// The block is otherwise well-formed (canonical hash + quorum
// cert), so the ONLY reason to reject is the FeeRecipients guard — proving it
// fires ahead of the passing signature/certificate checks.
func TestValidateRemoteBlock_FeeRecipientsRejected(t *testing.T) {
	resetEquivocation()
	ctx := context.Background()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	txs := []config.Transaction{signedTx(t, key, 0)}
	b := &config.ZKBlock{
		BlockHash:     RecomputeBlockHashFromTxs(txs),
		TxnsRoot:      RecomputeTxnsRoot(txs),
		BlockNumber:   70,
		Transactions:  txs,
		FeeRecipients: []config.FeeRecipient{{Addr: common.HexToAddress("0x01"), Weight: 1}},
	}
	msg := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}
	if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "feerecipients_unsupported" {
		t.Fatalf("want feerecipients_unsupported, got %v", rej)
	}
}
