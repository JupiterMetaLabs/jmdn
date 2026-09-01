package Security

// M2b activation flag tests. The user's own warning: M0 makes `period`
// chain-derived, but not tamper-proof - that requires M2b (fold the six
// consensus fields into the block hash) to actually be LIVE, not just built.
// These tests pin the flag's two required properties: off is a true no-op
// (byte-identical to pre-M2b behavior), and on actually closes the tamper
// gap M0 alone leaves open.

import (
	"testing"

	"gossipnode/config"
)

// TestM2bFlagDefaultsOff pins the rollout safety property directly: without
// anyone setting JMDN_M2B_HASH, M2bHashEnabled must be false, so a fresh
// process behaves exactly as it did before M2b existed.
func TestM2bFlagDefaultsOff(t *testing.T) {
	if M2bHashEnabled {
		t.Fatal("M2bHashEnabled must default to false - flipping it is a coordinated rollout, never a default")
	}
}

// TestCheckBlockHash_FlagOff_LegacyBehaviorUnchanged proves the flag being
// off is a true no-op: mutating a consensus field must NOT affect
// CheckBlockHash's verdict, exactly as before M2b was wired in. This is the
// regression guard for "landing M2b broke behavior for everyone who hasn't
// opted in yet."
func TestCheckBlockHash_FlagOff_LegacyBehaviorUnchanged(t *testing.T) {
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

// TestCheckBlockHash_IgnoresM2bFlag pins the post-directive invariant: BlockHash
// is the orchestrator's transactions-only identity and CheckBlockHash validates
// it that way REGARDLESS of M2bHashEnabled. M2b no longer folds the consensus
// fields into BlockHash (that broke every tx-only validator and the vote — see
// Block/consensus_fields.go). The tamper-evidence M2b used to provide now lives
// in a SEPARATE ConsensusHash that the v4 committee vote signs
// (config.ZKBlock.ConsensusHash, verified by messaging.checkConsensusBinding),
// so BlockHash does not move when a consensus field changes and CheckBlockHash
// must NOT reject on a Period rewrite.
func TestCheckBlockHash_IgnoresM2bFlag(t *testing.T) {
	M2bHashEnabled = true
	defer func() { M2bHashEnabled = false }()

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
		t.Fatalf("flag on: tx-only BlockHash must still pass (M2b no longer rebinds BlockHash), got ok=%v err=%v", ok, err)
	}

	// A consensus-field rewrite does NOT change the tx-only BlockHash, so
	// CheckBlockHash is unaffected — that binding is now ConsensusHash's job.
	block.Period = 99
	if ok, err := CheckBlockHash(block); err != nil || !ok {
		t.Fatalf("flag on: a Period rewrite must NOT affect the tx-only BlockHash check (ConsensusHash covers it now), got ok=%v err=%v", ok, err)
	}
}
