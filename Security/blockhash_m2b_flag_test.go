package Security

// CheckBlockHash validates the transactions-only BlockHash. Consensus-field
// binding is ConsensusHash's job (messaging.checkConsensusBinding); there is no
// flag choosing between formulas (the former JMDN_M2B_HASH switch was removed).

import (
	"testing"

	"gossipnode/config"
)

// TestCheckBlockHash_LegacyBehaviorUnchanged proves mutating a consensus field
// does NOT affect CheckBlockHash's verdict: BlockHash is transactions-only.
func TestCheckBlockHash_LegacyBehaviorUnchanged(t *testing.T) {
	txs := []config.Transaction{legacyTx(0, 1)}
	block := &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromContents(txs),
		Transactions: txs,
		Period:       1,
	}
	if ok, err := CheckBlockHash(block); err != nil || !ok {
		t.Fatalf("flag off: legacy-hash block should pass, got ok=%v err=%v", ok, err)
	}

	block.Period = 2 // mutate the consensus field the legacy hash never covered
	if ok, err := CheckBlockHash(block); err != nil || !ok {
		t.Fatalf("flag off: mutating Period must NOT affect the legacy check, got ok=%v err=%v", ok, err)
	}
}

// TestCheckBlockHash_TxOnlyIdentity pins the invariant: BlockHash is the
// orchestrator's transactions-only identity and CheckBlockHash validates it that
// way. The consensus fields are NOT folded into BlockHash (that broke every
// tx-only validator and the vote — see Block/consensus_fields.go); their
// tamper-evidence lives in the SEPARATE ConsensusHash the v4 committee vote signs
// (config.ZKBlock.ConsensusHash, verified by messaging.checkConsensusBinding), so
// CheckBlockHash must NOT reject on a Period rewrite.
func TestCheckBlockHash_TxOnlyIdentity(t *testing.T) {
	txs := []config.Transaction{legacyTx(0, 1)}
	block := &config.ZKBlock{
		BlockHash:           RecomputeBlockHashFromContents(txs), // tx-only identity
		Transactions:        txs,
		Slot:                10,
		Period:              1,
		SeedEpoch:           5,
		VotingSnapshotEpoch: 5,
	}
	if ok, err := CheckBlockHash(block); err != nil || !ok {
		t.Fatalf("tx-only BlockHash must pass, got ok=%v err=%v", ok, err)
	}

	// A consensus-field rewrite does NOT change the tx-only BlockHash, so
	// CheckBlockHash is unaffected — that binding is now ConsensusHash's job.
	block.Period = 99
	if ok, err := CheckBlockHash(block); err != nil || !ok {
		t.Fatalf("a Period rewrite must NOT affect the tx-only BlockHash check (ConsensusHash covers it), got ok=%v err=%v", ok, err)
	}
}
