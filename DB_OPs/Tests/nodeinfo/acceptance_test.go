// Migration acceptance criteria (STEP 8) that are verifiable without live
// infrastructure:
//
//	#3 GetTransactionsForAccountInRange returns an empty slice (NOT an error)
//	   for accounts with no transactions in range.
//	#4 l1_tx_hash / l1_block_number survive a write-read round-trip
//	   (plumbing level: record written == record read back).
//	#5 GetAccountsByNonces returns correct rows for a mix of existing and
//	   non-existing nonces (missing nonces silently omitted).
package nodeinfo_test

import (
	"context"
	"fmt"
	"testing"

	"gossipnode/DB_OPs"
	NodeInfo "gossipnode/DB_OPs/Nodeinfo"
	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
)

// acceptHandle backs the three acceptance tests.
type acceptHandle struct {
	store.ThebeHandle
	accounts map[uint64]*store.Account                 // nonce → account
	txs      []*thebegateway.TransactionRecord         // range-query result
	l1       map[uint64]*thebegateway.L1FinalityRecord // L2 block → finality
}

func (h *acceptHandle) GetTransactionsByAddressInRange(_ context.Context, _ string, from, to uint64) ([]*thebegateway.TransactionRecord, error) {
	var out []*thebegateway.TransactionRecord
	for _, t := range h.txs {
		if t.BlockNumber >= from && t.BlockNumber <= to {
			out = append(out, t)
		}
	}
	return out, nil // nil slice for empty result — mirrors SQL rows loop
}

func (h *acceptHandle) GetAccountsByNonces(_ context.Context, nonces []uint64) ([]*store.Account, error) {
	var out []*store.Account
	for _, n := range nonces {
		if a, ok := h.accounts[n]; ok {
			out = append(out, a) // missing nonces silently omitted
		}
	}
	return out, nil
}

func (h *acceptHandle) StoreL1Finality(_ context.Context, rec *thebegateway.L1FinalityRecord) error {
	for _, bn := range rec.BlockNumbers {
		h.l1[bn] = rec
	}
	return nil
}

func (h *acceptHandle) GetL1FinalityForBlock(_ context.Context, bn uint64) (*thebegateway.L1FinalityRecord, error) {
	rec, ok := h.l1[bn]
	if !ok {
		return nil, fmt.Errorf("GetL1FinalityForBlock(%d): sql: no rows in result set", bn)
	}
	return rec, nil
}

// #3 — empty range must be an empty slice, not an error.
func TestAcceptance3_EmptyRangeIsEmptySliceNotError(t *testing.T) {
	h := &acceptHandle{txs: nil, l1: map[uint64]*thebegateway.L1FinalityRecord{}}
	DB_OPs.SetGlobalHandle(h)
	defer DB_OPs.SetGlobalHandle(nil)

	am := NodeInfo.NewSyncStruct().NewAccountManager()
	txs, err := am.GetTransactionsForAccountInRange("0x1111111111111111111111111111111111111111", 10, 20)
	if err != nil {
		t.Fatalf("expected nil error for empty range, got: %v", err)
	}
	if txs == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(txs) != 0 {
		t.Fatalf("expected 0 txs, got %d", len(txs))
	}
}

// #4 — L1 commit range survives write → read.
func TestAcceptance4_L1CommitRoundTrip(t *testing.T) {
	h := &acceptHandle{l1: map[uint64]*thebegateway.L1FinalityRecord{}}
	DB_OPs.SetGlobalHandle(h)
	defer DB_OPs.SetGlobalHandle(nil)

	const l1Hash = "0xabc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abcd"
	const l1Block = uint64(19_000_001)

	if err := DB_OPs.StoreL1CommitRange(l1Hash, l1Block, 100, 105); err != nil {
		t.Fatalf("StoreL1CommitRange: %v", err)
	}

	// every block in range resolves to the commit
	for bn := uint64(100); bn <= 105; bn++ {
		gotHash, gotBlock, err := DB_OPs.GetL1CommitForBlock(bn)
		if err != nil {
			t.Fatalf("GetL1CommitForBlock(%d): %v", bn, err)
		}
		if gotHash != l1Hash || gotBlock != l1Block {
			t.Fatalf("block %d: got (%s, %d), want (%s, %d)", bn, gotHash, gotBlock, l1Hash, l1Block)
		}
	}

	// uncommitted block → ("", 0, nil), not an error
	gotHash, gotBlock, err := DB_OPs.GetL1CommitForBlock(999)
	if err != nil {
		t.Fatalf("uncommitted block should not error, got: %v", err)
	}
	if gotHash != "" || gotBlock != 0 {
		t.Fatalf("uncommitted block: got (%q, %d), want empty", gotHash, gotBlock)
	}
}

// #5 — GetAccountsByNonces with a mix of existing and non-existing nonces.
func TestAcceptance5_GetAccountsByNoncesMixed(t *testing.T) {
	h := &acceptHandle{
		accounts: map[uint64]*store.Account{
			7:  {Nonce: 7, Balance: "700"},
			42: {Nonce: 42, Balance: "4200"},
		},
		l1: map[uint64]*thebegateway.L1FinalityRecord{},
	}
	DB_OPs.SetGlobalHandle(h)
	defer DB_OPs.SetGlobalHandle(nil)

	it := NodeInfo.NewSyncStruct().NewAccountManager().NewAccountNonceIterator(100)
	defer it.Close()

	got, err := it.GetAccountsByNonces([]uint64{7, 13, 42, 99}) // 13, 99 don't exist
	if err != nil {
		t.Fatalf("GetAccountsByNonces: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 accounts (missing silently omitted), got %d", len(got))
	}
	found := map[uint64]bool{}
	for _, a := range got {
		found[a.Nonce] = true
	}
	if !found[7] || !found[42] {
		t.Fatalf("expected nonces 7 and 42, got %+v", found)
	}
}
