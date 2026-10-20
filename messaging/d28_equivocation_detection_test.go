package messaging

// D-28 (equivocation-detection half) regression tests.
//
// The certificate-replay half of D-28 was closed by binding BlockNumber and
// PrevHash into ConsensusHash (Security/consensus_fields_hash.go). This file
// pins the OTHER half: checkEquivocation and getBlockDedupID were still keyed
// on BlockHash — transactions-only, so two blocks on DIFFERENT forks (different
// PrevHash) carrying the IDENTICAL transaction set at the SAME height produced
// the SAME BlockHash. The second block was silently dropped as a "duplicate" by
// getBlockDedupID before it ever reached checkEquivocation, and even if it had
// reached checkEquivocation, that function would have seen a matching hash and
// not flagged it either.
//
// The fix re-keys both onto ConsensusHash, which DOES cover PrevHash/BlockNumber,
// and hardens checkConsensusBinding to reject (not skip) a missing ConsensusHash
// — without that second change, an attacker reproduces the exact same collision
// by simply omitting ConsensusHash on both forks.

import (
	"context"
	"testing"

	"gossipnode/Security"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// forkPair builds two ZKBlocks at the same height with IDENTICAL transactions
// (so BlockHash is identical for both — the exact condition that used to
// collide) but DIFFERENT PrevHash (so they are genuinely different forks).
// Each gets a correctly-bound ConsensusHash for its own PrevHash.
func forkPair(t *testing.T, num uint64, txs ...config.Transaction) (b1, b2 *config.ZKBlock) {
	t.Helper()

	b1 = &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     RecomputeTxnsRoot(txs),
		BlockNumber:  num,
		PrevHash:     common.HexToHash("0xaaaa"),
		Transactions: txs,
	}
	b1.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(b1)

	b2 = &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     RecomputeTxnsRoot(txs),
		BlockNumber:  num,
		PrevHash:     common.HexToHash("0xbbbb"),
		Transactions: txs,
	}
	b2.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(b2)

	if b1.BlockHash != b2.BlockHash {
		t.Fatal("test setup: b1 and b2 must share an identical BlockHash — that collision " +
			"is the exact bug this file exists to catch")
	}
	if b1.ConsensusHash == b2.ConsensusHash {
		t.Fatal("test setup: b1 and b2 must have DIFFERENT ConsensusHash (different PrevHash) " +
			"— otherwise this test can't tell the fix from the bug")
	}
	return b1, b2
}

// TestEquivocationDetectsForksSharingIdenticalTransactions is the core D-28
// regression: two different forks (different PrevHash) with byte-identical
// transactions at the same height must be caught as equivocation, not silently
// treated as "the same block".
func TestEquivocationDetectsForksSharingIdenticalTransactions(t *testing.T) {
	ctx := context.Background()
	resetEquivocation()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	b1, b2 := forkPair(t, 90, signedTx(t, key, 0))

	m1 := config.BlockMessage{Block: b1, Data: blockBoundCert(t, b1, "peerA", "peerB", "peerC")}
	if rej := validateRemoteBlock(ctx, m1); rej != nil {
		t.Fatalf("first fork should be accepted, got %s: %v", rej.reason, rej.err)
	}

	m2 := config.BlockMessage{Block: b2, Data: blockBoundCert(t, b2, "peerA", "peerB", "peerC")}
	rej := validateRemoteBlock(ctx, m2)
	if rej == nil {
		t.Fatal("a second fork sharing the first fork's transactions at the same height was " +
			"ACCEPTED — before this fix, checkEquivocation was keyed on BlockHash (transactions-" +
			"only), so this exact case went undetected (D-28)")
	}
	if rej.reason != "equivocation" {
		t.Fatalf("want reason %q, got %q (err: %v) — a rejection for a different reason means "+
			"something upstream of checkEquivocation caught this by accident, not the fix itself",
			"equivocation", rej.reason, rej.err)
	}
}

// TestGetBlockDedupID_DiffersAcrossForksWithSameTransactions pins the other
// half of the bug: even if checkEquivocation were fixed alone, the dedup layer
// ran BEFORE it with the same colliding key, dropping the second fork as a
// "duplicate" before validation — let alone equivocation detection — ever ran.
func TestGetBlockDedupID_DiffersAcrossForksWithSameTransactions(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	b1, b2 := forkPair(t, 91, signedTx(t, key, 0))

	id1 := getBlockDedupID(config.BlockMessage{Type: "zkblock", Block: b1})
	id2 := getBlockDedupID(config.BlockMessage{Type: "zkblock", Block: b2})

	if id1 == id2 {
		t.Fatalf("getBlockDedupID returned the SAME id (%q) for two different forks sharing "+
			"identical transactions — the second would be dropped as a duplicate before it ever "+
			"reaches validation or equivocation detection (D-28)", id1)
	}
}

// TestCheckConsensusBinding_RejectsZeroConsensusHash pins the bypass-closing
// half: re-keying checkEquivocation/getBlockDedupID onto ConsensusHash means
// nothing unless a block is actually required to carry one. See the updated
// TestCheckConsensusBinding_ClosesTheTamperGap in body_binding_m2b_flag_test.go
// for the same assertion in its original home.
func TestCheckConsensusBinding_RejectsZeroConsensusHash(t *testing.T) {
	b := &config.ZKBlock{
		BlockHash:   RecomputeBlockHashFromTxs(nil),
		BlockNumber: 92,
	}
	rej := checkConsensusBinding(b)
	if rej == nil {
		t.Fatal("a block with a zero ConsensusHash was accepted — this is the fallback an " +
			"attacker uses to reproduce the fork-collision bug even after checkEquivocation " +
			"and getBlockDedupID are re-keyed onto ConsensusHash (D-28)")
	}
	if rej.reason != "consensus_hash_missing" {
		t.Fatalf("want reason %q, got %q (err: %v)", "consensus_hash_missing", rej.reason, rej.err)
	}
}
