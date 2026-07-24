package Security

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func legacyTx(nonce uint64, val int64) config.Transaction {
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	return config.Transaction{
		Type:     0, // legacy
		Nonce:    nonce,
		To:       &to,
		Value:    big.NewInt(val),
		GasPrice: big.NewInt(1),
		GasLimit: 21000,
		V:        big.NewInt(0),
		R:        big.NewInt(1),
		S:        big.NewInt(1),
	}
}

// RecomputeBlockHashFromContents/CheckBlockHash must bind the block hash to the
// transaction CONTENTS: a block can only claim a hash that its transactions
// actually produce, and changing any tx content invalidates it.
func TestCheckBlockHash_BindsToContents(t *testing.T) {
	txs := []config.Transaction{legacyTx(0, 1), legacyTx(1, 2)}
	want := RecomputeBlockHashFromContents(txs)

	// A block claiming the recomputed hash passes.
	blk := &config.ZKBlock{BlockHash: want, Transactions: txs}
	if ok, err := CheckBlockHash(blk); err != nil || !ok {
		t.Fatalf("matching block hash must pass: ok=%v err=%v", ok, err)
	}

	// Changing a transaction's contents changes the hash → rejected.
	tampered := &config.ZKBlock{
		BlockHash:    want,
		Transactions: []config.Transaction{legacyTx(0, 1), legacyTx(1, 999)},
	}
	if ok, _ := CheckBlockHash(tampered); ok {
		t.Fatal("changed tx contents must invalidate the block hash")
	}

	// A block claiming an unrelated hash (e.g. copied from another block) is
	// rejected — a borrowed hash cannot pass at the block level.
	forged := &config.ZKBlock{BlockHash: common.HexToHash("0xdeadbeef"), Transactions: txs}
	if ok, _ := CheckBlockHash(forged); ok {
		t.Fatal("block claiming a hash not derived from its contents must be rejected")
	}

	// nil block fails closed.
	if ok, err := CheckBlockHash(nil); ok || err == nil {
		t.Fatal("nil block must fail closed")
	}
}
