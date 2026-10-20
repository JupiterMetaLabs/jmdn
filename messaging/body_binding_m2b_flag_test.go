package messaging

// checkBodyBinding binds BlockHash to transactions only, exactly as
// Security.CheckBlockHash does. Consensus-field binding is checkConsensusBinding's
// job (ConsensusHash). There is no flag choosing between formulas.

import (
	"testing"

	"gossipnode/Security"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// TestCheckBodyBinding_TxOnlyIdentity pins the invariant: checkBodyBinding binds
// BlockHash to transactions only and does NOT fold the consensus fields into
// BlockHash. Consensus-field tamper-evidence lives in the SEPARATE ConsensusHash
// the v4 vote signs (checkConsensusBinding), so a Period rewrite must NOT be
// rejected here — that is checkConsensusBinding's job, not this tx-only check.
func TestCheckBodyBinding_TxOnlyIdentity(t *testing.T) {
	txs := []config.Transaction{txWithHash(common.HexToHash("0x01"))}
	b := &config.ZKBlock{BlockHash: RecomputeBlockHashFromTxs(txs), Transactions: txs, Slot: 10, Period: 1, SeedEpoch: 5, VotingSnapshotEpoch: 5}

	if rej := checkBodyBinding(b); rej != nil {
		t.Fatalf("matching tx-only BlockHash must pass, got rejection: %+v", rej)
	}

	b.Period = 99 // consensus-field rewrite does not change the tx-only BlockHash
	if rej := checkBodyBinding(b); rej != nil {
		t.Fatalf("a Period rewrite must NOT affect the tx-only body check (ConsensusHash covers it), got rejection: %+v", rej)
	}
}

// TestCheckConsensusBinding_ClosesTheTamperGap is where the consensus-field
// tamper-evidence moved: a block carrying a ConsensusHash that does not match
// its recomputed consensus fields (e.g. a relay rewrote Period after the hash
// was signed) must be rejected.
func TestCheckConsensusBinding_ClosesTheTamperGap(t *testing.T) {
	txs := []config.Transaction{txWithHash(common.HexToHash("0x01"))}
	b := &config.ZKBlock{Transactions: txs, Slot: 10, Period: 1, SeedEpoch: 5, VotingSnapshotEpoch: 5}
	b.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(b)

	if rej := checkConsensusBinding(b); rej != nil {
		t.Fatalf("correctly-bound ConsensusHash should pass, got rejection: %+v", rej)
	}

	b.Period = 99 // relay rewrites Period after ConsensusHash was signed
	if rej := checkConsensusBinding(b); rej == nil {
		t.Fatal("a rewritten Period must be rejected by checkConsensusBinding (ConsensusHash no longer matches)")
	}

	// D-28: a zero ConsensusHash is now rejected unconditionally, including
	// at Period 0 — the leniency this used to have at Period 0 was the exact
	// bypass that let two equivocating forks each omit ConsensusHash and
	// collide on the same (zero) dedup/equivocation key. No honest block can
	// look like this: the proposer always sets ConsensusHash unconditionally
	// (Block/consensus_fields.go).
	b2 := &config.ZKBlock{Transactions: txs, Period: 0}
	if rej := checkConsensusBinding(b2); rej == nil || rej.reason != "consensus_hash_missing" {
		t.Fatalf("zero ConsensusHash must be rejected as consensus_hash_missing even at Period 0, got %+v", rej)
	}

	// Fix 3: once timeout certificates can advance Period, a zero
	// ConsensusHash with Period > 0 is the tamper gap itself - a relay could
	// zero the hash and rewrite Period to pick a different committee. No
	// honest block can look like this: the proposer always sets ConsensusHash
	// (Block/consensus_fields.go), and no block has ever had Period > 0.
	b3 := &config.ZKBlock{Transactions: txs, Period: 7}
	if rej := checkConsensusBinding(b3); rej == nil || rej.reason != "consensus_hash_missing" {
		t.Fatalf("zero ConsensusHash with Period > 0 must be rejected as consensus_hash_missing, got %+v", rej)
	}
}
