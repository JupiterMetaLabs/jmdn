package DB_OPs

// Unit tests for stampTxTimestamps (explorer tx-timestamp fix). TransactionRecord has
// no time column, so txRecordToConfig leaves the containing block NUMBER in
// Transaction.Timestamp as a placeholder; client-facing read paths call
// stampTxTimestamps to replace it with the block's epoch-seconds timestamp. These
// tests run without a database: a fake handle implements only GetBlock, the one
// method stampTxTimestamps calls.

import (
	"context"
	"errors"
	"testing"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// fakeBlockHandle embeds store.ThebeHandle (nil) and overrides GetBlock only. Any
// other method would panic, which would itself flag an unexpected dependency.
type fakeBlockHandle struct {
	store.ThebeHandle
	blocks map[uint64]*thebegateway.BlockRecord
	errs   map[uint64]error
	calls  map[uint64]int
}

func (f *fakeBlockHandle) GetBlock(_ context.Context, n uint64) (*thebegateway.BlockRecord, error) {
	f.calls[n]++
	if err := f.errs[n]; err != nil {
		return nil, err
	}
	return f.blocks[n], nil
}

func newFakeBlockHandle() *fakeBlockHandle {
	return &fakeBlockHandle{
		blocks: map[uint64]*thebegateway.BlockRecord{},
		errs:   map[uint64]error{},
		calls:  map[uint64]int{},
	}
}

// recsAndTxs builds parallel record/tx slices the way the read paths do, so the
// placeholder comes from the real txRecordToConfig.
func recsAndTxs(blockNumbers ...uint64) ([]*thebegateway.TransactionRecord, []*config.Transaction) {
	recs := make([]*thebegateway.TransactionRecord, len(blockNumbers))
	txs := make([]*config.Transaction, len(blockNumbers))
	for i, n := range blockNumbers {
		recs[i] = &thebegateway.TransactionRecord{BlockNumber: n}
		txs[i] = txRecordToConfig(recs[i])
	}
	return recs, txs
}

func TestStampTxTimestamps_StampsBlockTimeInEpochSeconds(t *testing.T) {
	h := newFakeBlockHandle()
	t10 := time.Date(2026, 9, 30, 11, 54, 23, 0, time.UTC)
	t11 := t10.Add(5 * time.Second)
	h.blocks[10] = &thebegateway.BlockRecord{BlockNumber: 10, Timestamp: t10}
	h.blocks[11] = &thebegateway.BlockRecord{BlockNumber: 11, Timestamp: t11}

	recs, txs := recsAndTxs(10, 10, 11)
	if txs[0].Timestamp != 10 {
		t.Fatalf("precondition: placeholder should be the block number, got %d", txs[0].Timestamp)
	}

	stampTxTimestamps(context.Background(), h, recs, txs)

	want := []uint64{uint64(t10.Unix()), uint64(t10.Unix()), uint64(t11.Unix())}
	for i, w := range want {
		if txs[i].Timestamp != w {
			t.Fatalf("tx %d Timestamp = %d, want %d (block epoch seconds)", i, txs[i].Timestamp, w)
		}
	}
}

func TestStampTxTimestamps_OneHeaderReadPerDistinctBlock(t *testing.T) {
	h := newFakeBlockHandle()
	ts := time.Unix(1_780_000_000, 0)
	for _, n := range []uint64{20, 21} {
		h.blocks[n] = &thebegateway.BlockRecord{BlockNumber: n, Timestamp: ts}
	}
	recs, txs := recsAndTxs(20, 20, 20, 21, 21)

	stampTxTimestamps(context.Background(), h, recs, txs)

	if h.calls[20] != 1 || h.calls[21] != 1 {
		t.Fatalf("GetBlock calls = %v, want exactly 1 per distinct block", h.calls)
	}
}

func TestStampTxTimestamps_KeepsPlaceholderOnErrorOrZeroTime(t *testing.T) {
	h := newFakeBlockHandle()
	h.errs[30] = errors.New("read failed")
	h.blocks[31] = &thebegateway.BlockRecord{BlockNumber: 31} // zero Timestamp
	// block 32: no record at all (nil, nil)

	recs, txs := recsAndTxs(30, 31, 32)
	stampTxTimestamps(context.Background(), h, recs, txs)

	for i, n := range []uint64{30, 31, 32} {
		if txs[i].Timestamp != n {
			t.Fatalf("block %d: Timestamp = %d, want placeholder %d (never regress to 0)", n, txs[i].Timestamp, n)
		}
	}

	// A failed read is cached for the block, not retried per tx.
	h2 := newFakeBlockHandle()
	h2.errs[30] = errors.New("read failed")
	recs2, txs2 := recsAndTxs(30, 30)
	stampTxTimestamps(context.Background(), h2, recs2, txs2)
	if h2.calls[30] != 1 {
		t.Fatalf("failed block read retried %d times, want 1", h2.calls[30])
	}
}

func TestStampTxTimestamps_NilAndMismatchedInputsAreSafe(t *testing.T) {
	// nil handle: no-op, no panic.
	recs, txs := recsAndTxs(40)
	stampTxTimestamps(context.Background(), nil, recs, txs)
	if txs[0].Timestamp != 40 {
		t.Fatalf("nil handle changed Timestamp to %d", txs[0].Timestamp)
	}

	h := newFakeBlockHandle()
	h.blocks[41] = &thebegateway.BlockRecord{BlockNumber: 41, Timestamp: time.Unix(1_780_000_000, 0)}
	// nil record, nil tx, and more records than txs must all be skipped safely.
	recs = []*thebegateway.TransactionRecord{nil, {BlockNumber: 41}, {BlockNumber: 41}}
	txs = []*config.Transaction{{Timestamp: 1}, nil}
	stampTxTimestamps(context.Background(), h, recs, txs)
	if txs[0].Timestamp != 1 {
		t.Fatalf("tx paired with a nil record was modified: %d", txs[0].Timestamp)
	}
}
