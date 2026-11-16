package DB_OPs

// Security review finding #6 and the two follow-up notes on it.
//
// #6: cumulativeGasUsed is this transaction's gas plus every earlier
// transaction's in the block. The builder summed reconstructedGasUsed for all
// of them — exact for a value transfer between accounts, but only the
// intrinsic FLOOR for anything that runs EVM code. Only the requested
// transaction's own GasUsed was replaced with the persisted value, so a receipt
// could report a cumulative total BELOW its own gasUsed.
//
// PP-f24b51: the first fix skipped the lookup for transactions with no
// calldata. Apply classifies by whether the recipient HOLDS CODE
// (evmexec.Executor.IsContractTx), so a no-calldata transfer into a contract's
// receive() does have a persisted receipt — and the inversion survived for it.
//
// PP-ab2798: resolving the whole block to answer one receipt request did work
// proportional to the block rather than to the index asked for.

import (
	"math/big"
	"testing"

	"gossipnode/DB_OPs/store"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func contractTx(n int64, data []byte) config.Transaction {
	to := common.BigToAddress(common.Big1)
	from := common.BigToAddress(common.Big2)
	return config.Transaction{Hash: common.BigToHash(big.NewInt(n)), To: &to, From: &from, Data: data}
}

// transferTx has a recipient and NO calldata. Whether it runs code depends on
// whether that recipient holds any — which this layer cannot see.
func transferTx(n int64) config.Transaction {
	to := common.BigToAddress(common.Big1)
	from := common.BigToAddress(common.Big2)
	return config.Transaction{Hash: common.BigToHash(big.NewInt(n)), To: &to, From: &from}
}

// countingLookup answers from a fixture and records what was asked for.
func countingLookup(real map[string]uint64, asked *[]string) func(string) *store.ContractReceipt {
	return func(h string) *store.ContractReceipt {
		*asked = append(*asked, h)
		g, ok := real[h]
		if !ok {
			return nil
		}
		return &store.ContractReceipt{Found: true, Status: 1, GasUsed: g}
	}
}

// An earlier contract call's REAL gas must be in the total, not its floor.
func TestResolveReceipts_UsesRealGasForContractCalls(t *testing.T) {
	blk := &config.ZKBlock{Transactions: []config.Transaction{
		contractTx(1, []byte{1, 2, 3}),
		transferTx(2),
		contractTx(3, []byte{4}),
	}}
	real := map[string]uint64{
		blk.Transactions[0].Hash.Hex(): 500_000,
		blk.Transactions[2].Hash.Hex(): 300_000,
	}
	var asked []string

	crs := resolveReceipts(blk, 2, countingLookup(real, &asked))
	used := gasUsedFrom(blk, crs)

	if used[0] != 500_000 || used[2] != 300_000 {
		t.Fatalf("contract calls must report persisted gas, got %v", used)
	}
	if want := reconstructedGasUsed(&blk.Transactions[1]); used[1] != want {
		t.Fatalf("a transaction with no receipt keeps the reconstruction %d, got %d", want, used[1])
	}
	if got, want := cumulativeGasUpTo(used, 2), uint64(500_000)+used[1]+uint64(300_000); got != want {
		t.Fatalf("cumulative at index 2 = %d, want %d", got, want)
	}
}

// PP-f24b51. A no-calldata transfer INTO A CONTRACT runs its receive() and has
// a real receipt. It must be looked up like any other transaction, or its real
// gas lands in the receipt while the floor lands in the cumulative sum.
func TestResolveReceipts_NoCalldataTransferToContractIsLookedUp(t *testing.T) {
	blk := &config.ZKBlock{Transactions: []config.Transaction{transferTx(1), contractTx(2, []byte{9})}}
	real := map[string]uint64{blk.Transactions[0].Hash.Hex(): 40_000}
	var asked []string

	crs := resolveReceipts(blk, 1, countingLookup(real, &asked))
	used := gasUsedFrom(blk, crs)

	if len(asked) != 2 {
		t.Fatalf("every transaction must be looked up, got %d: %v", len(asked), asked)
	}
	if used[0] != 40_000 {
		t.Fatalf("receive() gas must reach the sum: used[0] = %d, want 40000", used[0])
	}

	// The inversion, stated as the invariant it violated.
	r := generateReceiptFromTransaction(&blk.Transactions[0], blk, 0, cumulativeGasUpTo(used, 0), crs[0])
	if r.CumulativeGasUsed < r.GasUsed {
		t.Fatalf("cumulative %d is below this tx's own %d", r.CumulativeGasUsed, r.GasUsed)
	}
	if r.GasUsed != 40_000 {
		t.Fatalf("receipt gasUsed = %d, want the persisted 40000", r.GasUsed)
	}
}

// PP-ab2798. One receipt request must resolve up to its index and no further.
func TestResolveReceipts_StopsAtTheRequestedIndex(t *testing.T) {
	txs := make([]config.Transaction, 10)
	for i := range txs {
		txs[i] = contractTx(int64(i+1), []byte{byte(i)})
	}
	blk := &config.ZKBlock{Transactions: txs}

	var asked []string
	crs := resolveReceipts(blk, 0, countingLookup(map[string]uint64{}, &asked))

	if len(asked) != 1 {
		t.Fatalf("a receipt for index 0 of a 10-tx block must cost 1 lookup, got %d", len(asked))
	}
	if len(crs) != 1 {
		t.Fatalf("resolved %d entries, want 1", len(crs))
	}
}

// An index past the end is clamped, not a panic; an empty block resolves to
// nothing.
func TestResolveReceipts_BoundsAreSafe(t *testing.T) {
	blk := &config.ZKBlock{Transactions: []config.Transaction{transferTx(1)}}
	var asked []string
	if got := len(resolveReceipts(blk, 99, countingLookup(nil, &asked))); got != 1 {
		t.Fatalf("out-of-range upTo must clamp to the block, got %d entries", got)
	}
	if got := resolveReceipts(&config.ZKBlock{}, 0, countingLookup(nil, &asked)); got != nil {
		t.Fatalf("an empty block resolves to nothing, got %v", got)
	}
	if got := len(resolveReceipts(blk, 0, nil)); got != 1 {
		t.Fatalf("a nil lookup still yields one nil entry, got %d", got)
	}
	if got := receiptAt(nil, 3); got != nil {
		t.Fatalf("receiptAt past the end must be nil, got %v", got)
	}
}

// The defect as reported: estimates understate the total and invert the
// invariant; real gas restores both.
func TestCumulativeGas_EstimateOnlyUnderstatesAndInverts(t *testing.T) {
	blk := &config.ZKBlock{Transactions: []config.Transaction{
		contractTx(1, []byte{1, 2, 3}),
		contractTx(2, []byte{4, 5, 6}),
	}}
	real := map[string]uint64{
		blk.Transactions[0].Hash.Hex(): 900_000,
		blk.Transactions[1].Hash.Hex(): 800_000,
	}
	var asked []string

	estimateOnly := cumulativeGasUpTo(gasUsedFrom(blk, resolveReceipts(blk, 1, nil)), 1)
	withRealGas := cumulativeGasUpTo(gasUsedFrom(blk, resolveReceipts(blk, 1, countingLookup(real, &asked))), 1)

	if estimateOnly >= withRealGas {
		t.Fatalf("estimate-only total %d should understate the real %d", estimateOnly, withRealGas)
	}
	ownGasUsed := real[blk.Transactions[1].Hash.Hex()]
	if estimateOnly >= ownGasUsed {
		t.Fatalf("fixture too weak: estimate-only cumulative %d must fall below this tx's own %d",
			estimateOnly, ownGasUsed)
	}
	if withRealGas < ownGasUsed {
		t.Fatalf("cumulative %d must never be below this tx's own gas", withRealGas)
	}
}
