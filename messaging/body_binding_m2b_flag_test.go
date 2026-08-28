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

func TestCheckBodyBinding_FlagOn_ClosesTheTamperGap(t *testing.T) {
	Security.M2bHashEnabled = true
	defer func() { Security.M2bHashEnabled = false }()

	txs := []config.Transaction{txWithHash(common.HexToHash("0x01"))}
	b := &config.ZKBlock{Transactions: txs, Slot: 10, Period: 1, SeedEpoch: 5, VotingSnapshotEpoch: 5}
	b.BlockHash = Security.RecomputeBlockHashWithConsensusFields(b)

	if rej := checkBodyBinding(b); rej != nil {
		t.Fatalf("flag on: correctly-hashed M2b block should pass, got rejection: %+v", rej)
	}

	b.Period = 99
	if rej := checkBodyBinding(b); rej == nil {
		t.Fatal("flag on: a block with a rewritten Period must be rejected by checkBodyBinding")
	}
}
