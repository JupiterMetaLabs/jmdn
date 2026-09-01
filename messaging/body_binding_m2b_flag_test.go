package messaging

// checkBodyBinding must read the SAME Security.M2bHashEnabled flag
// Security.CheckBlockHash reads, so the two hash-validation call sites in the
// receive path can never disagree about which formula is live.

import (
	"testing"

	"gossipnode/Security"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func TestCheckBodyBinding_FlagOff_LegacyBehaviorUnchanged(t *testing.T) {
	txs := []config.Transaction{txWithHash(common.HexToHash("0x01"))}
	b := &config.ZKBlock{BlockHash: RecomputeBlockHashFromTxs(txs), Transactions: txs, Period: 1}

	if rej := checkBodyBinding(b); rej != nil {
		t.Fatalf("flag off: matching legacy hash should pass, got rejection: %+v", rej)
	}

	b.Period = 2 // legacy formula never covers this field
	if rej := checkBodyBinding(b); rej != nil {
		t.Fatalf("flag off: mutating Period must not affect checkBodyBinding, got rejection: %+v", rej)
	}
}

// TestCheckBodyBinding_IgnoresM2bFlag pins the post-directive invariant:
// checkBodyBinding binds BlockHash to transactions only and does NOT fold the
// consensus fields into BlockHash regardless of M2bHashEnabled. Consensus-field
// tamper-evidence now lives in the SEPARATE ConsensusHash the v4 vote signs
// (checkConsensusBinding), so a Period rewrite must NOT be rejected here — that
// is checkConsensusBinding's job, not this tx-only body check.
func TestCheckBodyBinding_IgnoresM2bFlag(t *testing.T) {
	Security.M2bHashEnabled = true
	defer func() { Security.M2bHashEnabled = false }()

	txs := []config.Transaction{txWithHash(common.HexToHash("0x01"))}
	b := &config.ZKBlock{BlockHash: RecomputeBlockHashFromTxs(txs), Transactions: txs, Slot: 10, Period: 1, SeedEpoch: 5, VotingSnapshotEpoch: 5}

	if rej := checkBodyBinding(b); rej != nil {
		t.Fatalf("flag on: tx-only BlockHash must still pass (M2b no longer rebinds BlockHash), got rejection: %+v", rej)
	}

	b.Period = 99 // consensus-field rewrite does not change the tx-only BlockHash
	if rej := checkBodyBinding(b); rej != nil {
		t.Fatalf("flag on: a Period rewrite must NOT affect the tx-only body check (ConsensusHash covers it now), got rejection: %+v", rej)
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

	// A block with no ConsensusHash (pre-v4) is skipped, not rejected.
	b2 := &config.ZKBlock{Transactions: txs, Period: 7}
	if rej := checkConsensusBinding(b2); rej != nil {
		t.Fatalf("zero ConsensusHash must be skipped (rollout leniency), got rejection: %+v", rej)
	}
}
