package DB_OPs

// GetBlocksRange is header-only by design (BulkGetBlocks reads the blocks table
// alone). The txindex catch-up used it and indexed zero transactions for every
// caught-up block (explorer total 303/522 vs 4961 on testnet). These tests pin
// GetBlocksRangeWithTransactions: it returns each block's transactions and
// fails closed on a transaction read error.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

type fakeRangeHandle struct {
	store.ThebeHandle
	txs    map[uint64][]*thebegateway.TransactionRecord
	txErrs map[uint64]error
}

func (f *fakeRangeHandle) BulkGetBlocks(_ context.Context, from, to uint64) ([]*thebegateway.BlockRecord, error) {
	var out []*thebegateway.BlockRecord
	for n := from; n <= to; n++ {
		out = append(out, &thebegateway.BlockRecord{BlockNumber: n, Timestamp: time.Unix(1_780_000_000+int64(n), 0)})
	}
	return out, nil
}

func (f *fakeRangeHandle) GetTransactionsByBlock(_ context.Context, n uint64) ([]*thebegateway.TransactionRecord, error) {
	if err := f.txErrs[n]; err != nil {
		return nil, err
	}
	return f.txs[n], nil
}

func txRec(n uint64, i int) *thebegateway.TransactionRecord {
	to := "0x00000000000000000000000000000000000000aa"
	return &thebegateway.TransactionRecord{TxHash: fmt.Sprintf("0x%064x", n*100+uint64(i)), BlockNumber: n, TxIndex: int16(i),
		FromAddr: "0x00000000000000000000000000000000000000bb", ToAddr: &to, Nonce: "0"}
}

func TestGetBlocksRangeWithTransactions_AttachesTransactions(t *testing.T) {
	f := &fakeRangeHandle{txs: map[uint64][]*thebegateway.TransactionRecord{
		10: {txRec(10, 0)}, 11: {txRec(11, 0), txRec(11, 1)}, 12: {txRec(12, 0)},
	}}
	conn := &config.PooledConnection{Handle: f}

	headers, err := GetBlocksRange(conn, 10, 12)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range headers {
		if len(b.Transactions) != 0 {
			t.Fatalf("GetBlocksRange is expected to stay header-only; block %d has %d txs", b.BlockNumber, len(b.Transactions))
		}
	}

	blocks, err := GetBlocksRangeWithTransactions(conn, 10, 12)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint64]int{10: 1, 11: 2, 12: 1}
	for _, b := range blocks {
		if len(b.Transactions) != want[b.BlockNumber] {
			t.Fatalf("block %d: %d txs, want %d", b.BlockNumber, len(b.Transactions), want[b.BlockNumber])
		}
		if b.Transactions[0].Timestamp != uint64(b.Timestamp) || b.Transactions[0].From == nil || b.Transactions[0].To == nil {
			t.Fatalf("block %d: tx not converted fully: %+v", b.BlockNumber, b.Transactions[0])
		}
	}
}

func TestGetBlocksRangeWithTransactions_FailsClosedOnTxReadError(t *testing.T) {
	f := &fakeRangeHandle{txs: map[uint64][]*thebegateway.TransactionRecord{10: {txRec(10, 0)}},
		txErrs: map[uint64]error{11: errors.New("connection reset")}}
	if _, err := GetBlocksRangeWithTransactions(&config.PooledConnection{Handle: f}, 10, 12); err == nil {
		t.Fatal("a transaction read error must fail the call, not return a block that looks empty")
	}
}
