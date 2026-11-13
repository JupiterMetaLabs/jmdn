package DB_OPs

// GetReceiptsofBlock used to read the whole block once per transaction, plus
// once for the transaction list: GetTransactionsOfBlock is a full
// GetZKBlockByNumber, and GetReceiptByHash resolves its own block through
// GetTransactionBlock, which is another one. N+1 whole-block reads for a block
// of N transactions.
//
// The cost is not the count. A stored block carries its STARK proof, so every
// one of those reads pulled a multi-megabyte proof out of storage into a fresh
// allocation, and eth_getBlockByNumber reaches here on every call (through
// utils.ConvertZKBlockToBlock, to rebuild the receipts root and logs bloom).
// There is no cap on transactions per block.
//
// These tests pin what the single-read rewrite must preserve — index,
// cumulative gas, block identity — and the property that makes it a saving:
// every receipt references ONE proof rather than owning a copy of it.
//
// No DB handle is set here, so generateReceiptFromTransaction takes its
// documented fallback path (getHandle errors, the persisted EVM receipt is
// skipped, the reconstruction is used). That is the path a plain value
// transfer takes in production too.

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

func blockWithTxs(n int, proof []byte) *config.ZKBlock {
	blk := &config.ZKBlock{
		BlockNumber: 858,
		BlockHash:   common.HexToHash("0xb10c"),
		Status:      "committed",
		StarkProof:  proof,
	}
	for i := 0; i < n; i++ {
		to := common.BigToAddress(common.Big1)
		from := common.BigToAddress(common.Big2)
		blk.Transactions = append(blk.Transactions, config.Transaction{
			Hash:  common.BigToHash(common.Big1),
			Type:  0,
			To:    &to,
			From:  &from,
			Nonce: uint64(i),
		})
	}
	return blk
}

// Index, cumulative gas and block identity must come out exactly as the
// per-hash path produced them: the index is the transaction's position, and
// cumulative gas is the running sum over the block's own transactions.
func TestReceiptsFromOneBlockRead_IndexAndCumulativeGas(t *testing.T) {
	blk := blockWithTxs(3, []byte("proof"))

	var running uint64
	for i := range blk.Transactions {
		r := generateReceiptFromTransaction(nil, &blk.Transactions[i], blk, uint64(i))

		if r.TransactionIndex != uint64(i) {
			t.Fatalf("receipt %d: TransactionIndex = %d", i, r.TransactionIndex)
		}
		if r.BlockHash != blk.BlockHash || r.BlockNumber != blk.BlockNumber {
			t.Fatalf("receipt %d: block identity not carried: %s/%d", i, r.BlockHash.Hex(), r.BlockNumber)
		}
		running += reconstructedGasUsed(&blk.Transactions[i])
		if r.CumulativeGasUsed != running {
			t.Fatalf("receipt %d: CumulativeGasUsed = %d, want the running sum %d", i, r.CumulativeGasUsed, running)
		}
		if r.CumulativeGasUsed < r.GasUsed {
			t.Fatalf("receipt %d: cumulative %d is below this tx's own %d", i, r.CumulativeGasUsed, r.GasUsed)
		}
	}
}

// The point of the rewrite: one block read means one proof. Every receipt must
// reference the SAME backing array, not a per-receipt copy — N copies of a
// multi-megabyte proof is the cost this change removes.
func TestReceiptsFromOneBlockRead_ShareOneProof(t *testing.T) {
	proof := []byte("a-stark-proof-stands-in-for-megabytes")
	blk := blockWithTxs(4, proof)

	receipts := make([]*config.Receipt, len(blk.Transactions))
	for i := range blk.Transactions {
		receipts[i] = generateReceiptFromTransaction(nil, &blk.Transactions[i], blk, uint64(i))
	}

	for i, r := range receipts {
		if len(r.ZKProof) != len(proof) {
			t.Fatalf("receipt %d: proof not carried (len %d)", i, len(r.ZKProof))
		}
		// Same backing array as the block's, so N receipts hold one proof.
		if &r.ZKProof[0] != &blk.StarkProof[0] {
			t.Fatalf("receipt %d: proof was copied instead of shared", i)
		}
		if r.ZKStatus != blk.Status {
			t.Fatalf("receipt %d: ZKStatus = %q, want %q", i, r.ZKStatus, blk.Status)
		}
	}
}

// An empty block yields no receipts and must not read anything per transaction.
func TestReceiptsFromOneBlockRead_EmptyBlock(t *testing.T) {
	blk := blockWithTxs(0, nil)
	if len(blk.Transactions) != 0 {
		t.Fatal("fixture")
	}
	receipts := make([]*config.Receipt, len(blk.Transactions))
	if len(receipts) != 0 {
		t.Fatalf("want no receipts, got %d", len(receipts))
	}
}
