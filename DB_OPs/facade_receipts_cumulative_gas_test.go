package DB_OPs

// Security review finding #6. cumulativeGasUsed is defined as the gas used by
// this transaction plus every transaction before it in the block. The receipt
// builder summed reconstructedGasUsed for all of them — the intrinsic FLOOR for
// a contract call (21,000 plus calldata), never the gas a contract actually
// burned. Only the requested transaction's own GasUsed was replaced with the
// persisted value, so a receipt could report a cumulative total BELOW its own
// gasUsed, and every wallet or indexer that derives per-transaction gas by
// differencing consecutive cumulative totals got wrong numbers.

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// contractTx is a call with calldata; transferTx is a plain value transfer.
func contractTx(n int64, data []byte) config.Transaction {
	to := common.BigToAddress(common.Big1)
	from := common.BigToAddress(common.Big2)
	return config.Transaction{Hash: common.BigToHash(big.NewInt(n)), To: &to, From: &from, Data: data}
}

func transferTx(n int64) config.Transaction {
	to := common.BigToAddress(common.Big1)
	from := common.BigToAddress(common.Big2)
	return config.Transaction{Hash: common.BigToHash(big.NewInt(n)), To: &to, From: &from}
}

// An earlier contract call's REAL gas must be in the total, not its floor.
func TestGasUsedOfBlock_UsesRealGasForContractCalls(t *testing.T) {
	blk := &config.ZKBlock{Transactions: []config.Transaction{
		contractTx(1, []byte{1, 2, 3}),
		transferTx(2),
		contractTx(3, []byte{4}),
	}}

	real := map[string]uint64{
		blk.Transactions[0].Hash.Hex(): 500_000,
		blk.Transactions[2].Hash.Hex(): 300_000,
	}
	var lookups []string
	used := gasUsedOfBlock(blk, func(h string) (uint64, bool) {
		lookups = append(lookups, h)
		g, ok := real[h]
		return g, ok
	})

	if used[0] != 500_000 || used[2] != 300_000 {
		t.Fatalf("contract calls must report persisted gas, got %v", used)
	}
	if want := reconstructedGasUsed(&blk.Transactions[1]); used[1] != want {
		t.Fatalf("plain transfer must keep the exact reconstruction %d, got %d", want, used[1])
	}

	// A plain transfer has no persisted receipt; looking it up is a wasted read.
	if len(lookups) != 2 {
		t.Fatalf("want 2 lookups (contract txs only), got %d: %v", len(lookups), lookups)
	}

	// The cumulative total is the prefix sum of the REAL values.
	if got, want := cumulativeGasUpTo(used, 2), uint64(500_000)+used[1]+uint64(300_000); got != want {
		t.Fatalf("cumulative at index 2 = %d, want %d", got, want)
	}
}

// The defect as reported: with estimates only, the total for a block whose
// earlier transactions are contract calls is far below the truth, and the last
// receipt's cumulative can fall below its own gasUsed.
func TestCumulativeGas_EstimateOnlyUnderstatesAndInverts(t *testing.T) {
	blk := &config.ZKBlock{Transactions: []config.Transaction{
		contractTx(1, []byte{1, 2, 3}),
		contractTx(2, []byte{4, 5, 6}),
	}}
	real := map[string]uint64{
		blk.Transactions[0].Hash.Hex(): 900_000,
		blk.Transactions[1].Hash.Hex(): 800_000,
	}
	lookup := func(h string) (uint64, bool) { g, ok := real[h]; return g, ok }

	estimateOnly := cumulativeGasUpTo(gasUsedOfBlock(blk, nil), 1)
	withRealGas := cumulativeGasUpTo(gasUsedOfBlock(blk, lookup), 1)

	if estimateOnly >= withRealGas {
		t.Fatalf("estimate-only total %d should understate the real %d", estimateOnly, withRealGas)
	}
	// The inversion wallets actually hit: own gasUsed above the cumulative total.
	if ownGasUsed := real[blk.Transactions[1].Hash.Hex()]; estimateOnly >= ownGasUsed {
		t.Fatalf("fixture too weak: estimate-only cumulative %d must fall below this tx's own %d",
			estimateOnly, ownGasUsed)
	}
	if withRealGas < real[blk.Transactions[1].Hash.Hex()] {
		t.Fatalf("cumulative %d must never be below this tx's own gas", withRealGas)
	}
}

// Classification: only a creation or a call with calldata can have a receipt.
func TestHasPersistedReceipt(t *testing.T) {
	create := contractTx(1, []byte{1})
	create.To = nil
	for _, c := range []struct {
		name string
		tx   config.Transaction
		want bool
	}{
		{"plain transfer", transferTx(1), false},
		{"call with calldata", contractTx(2, []byte{1}), true},
		{"creation", create, true},
	} {
		if got := hasPersistedReceipt(&c.tx); got != c.want {
			t.Fatalf("%s: hasPersistedReceipt = %v, want %v", c.name, got, c.want)
		}
	}
}
