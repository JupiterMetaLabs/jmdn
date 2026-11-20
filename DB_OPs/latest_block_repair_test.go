package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"

	"github.com/ethereum/go-ethereum/common"
)

// tipChainHandle is a store.ThebeHandle with a sync-state KV, stored blocks
// and their transactions. Only the methods the data-complete tip read uses
// are implemented.
type tipChainHandle struct {
	store.ThebeHandle
	mu        sync.Mutex
	kv        map[string][]byte
	blocks    map[uint64]*thebegateway.BlockRecord
	txs       map[uint64][]*thebegateway.TransactionRecord
	failPutKV map[string]bool
}

func newTipChainHandle() *tipChainHandle {
	return &tipChainHandle{
		kv:        map[string][]byte{},
		blocks:    map[uint64]*thebegateway.BlockRecord{},
		txs:       map[uint64][]*thebegateway.TransactionRecord{},
		failPutKV: map[string]bool{},
	}
}

func (h *tipChainHandle) GetSyncKV(key string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.kv[key], nil
}

func (h *tipChainHandle) PutSyncKV(key string, value []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failPutKV[key] {
		return errors.New("sync kv write failed")
	}
	h.kv[key] = append([]byte(nil), value...)
	return nil
}

func (h *tipChainHandle) GetBlock(_ context.Context, n uint64) (*thebegateway.BlockRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if b, ok := h.blocks[n]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("block %d: not found", n)
}

func (h *tipChainHandle) GetTransactionsByBlock(_ context.Context, n uint64) ([]*thebegateway.TransactionRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.txs[n], nil
}

func txHashFor(n uint64, i int) common.Hash {
	return common.BytesToHash([]byte(fmt.Sprintf("tx-%d-%d", n, i)))
}

func blockHashFor(n uint64) string {
	return common.BytesToHash([]byte(fmt.Sprintf("block-%d", n))).Hex()
}

// storeBlock stores block n (txCount transactions, parent n-1) and, when
// applied, writes an applied tx_processed marker for each transaction.
func (h *tipChainHandle) storeBlock(n uint64, txCount int, applied bool) {
	hashes := make([]common.Hash, txCount)
	recs := make([]*thebegateway.TransactionRecord, txCount)
	for i := 0; i < txCount; i++ {
		hashes[i] = txHashFor(n, i)
		recs[i] = &thebegateway.TransactionRecord{TxHash: hashes[i].Hex(), BlockNumber: n, TxIndex: int16(i)}
		if applied {
			h.kv[TxProcessedKey(hashes[i].Hex())] = []byte("1")
		}
	}
	// Returned out of order: the root must follow tx_index, not read order.
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	h.blocks[n] = &thebegateway.BlockRecord{
		BlockNumber: n,
		BlockHash:   blockHashFor(n),
		ParentHash:  blockHashFor(n - 1),
		TxsRoot:     TxnsRootOfHashes(hashes),
	}
	h.txs[n] = recs
}

func (h *tipChainHandle) setMarker(n uint64) {
	h.kv[LatestBlockMarkerKey] = []byte(strconv.FormatUint(n, 10))
}

func (h *tipChainHandle) marker() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return string(h.kv[LatestBlockMarkerKey])
}

func installTipChain(t *testing.T) *tipChainHandle {
	t.Helper()
	h := newTipChainHandle()
	SetGlobalHandle(h)
	t.Cleanup(func() {
		SetGlobalHandle(nil)
		SetTipRepairHook(nil)
	})
	return h
}

// TestDataCompleteTip_StoredBlockAboveFailedMarker reproduces the block 21829
// stall: block N is stored and applied, but the latest_block and
// applied-anchor writes after the store both failed, so the marker reads N-1.
// The data-complete tip must be N (so the ingress guard refuses a second
// proposal at N and /api/latest-block reports N), and the marker is written.
func TestDataCompleteTip_StoredBlockAboveFailedMarker(t *testing.T) {
	h := installTipChain(t)
	h.storeBlock(100, 3, true)
	h.storeBlock(101, 5, true)
	h.setMarker(100)

	var from, to uint64
	SetTipRepairHook(func(f, tt uint64, err error) {
		from, to = f, tt
		if err != nil {
			t.Errorf("record error: %v", err)
		}
	})

	tip, err := GetLatestDataCompleteBlock()
	if err != nil {
		t.Fatal(err)
	}
	if tip != 101 {
		t.Fatalf("data-complete tip = %d, want 101", tip)
	}
	if got := h.marker(); got != "101" {
		t.Fatalf("latest_block marker = %q, want 101", got)
	}
	if from != 100 || to != 101 {
		t.Fatalf("repair hook = %d→%d, want 100→101", from, to)
	}
}

// TestDataCompleteTip_RefusesUnprovenBlocks: a block above the marker is not
// counted unless it links to the tip, holds all its transactions, and every
// transaction is marked applied. An empty block is never counted.
func TestDataCompleteTip_RefusesUnprovenBlocks(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *tipChainHandle)
	}{
		{"not applied (store failed, markers revoked)", func(h *tipChainHandle) {
			h.storeBlock(101, 3, false)
		}},
		{"one marker revoked", func(h *tipChainHandle) {
			h.storeBlock(101, 3, true)
			h.kv[TxProcessedKey(txHashFor(101, 1).Hex())] = []byte(strconv.FormatInt(MarkerRevoked, 10))
		}},
		{"header-only row (no transactions)", func(h *tipChainHandle) {
			h.storeBlock(101, 3, true)
			h.txs[101] = nil
		}},
		{"partly stored transactions", func(h *tipChainHandle) {
			h.storeBlock(101, 3, true)
			h.txs[101] = h.txs[101][:2]
		}},
		{"does not link to the tip", func(h *tipChainHandle) {
			h.storeBlock(101, 3, true)
			h.blocks[101].ParentHash = blockHashFor(999)
		}},
		{"empty block", func(h *tipChainHandle) {
			h.storeBlock(101, 0, true)
		}},
		{"no TxnsRoot", func(h *tipChainHandle) {
			h.storeBlock(101, 3, true)
			h.blocks[101].TxsRoot = ""
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := installTipChain(t)
			h.storeBlock(100, 3, true)
			h.setMarker(100)
			c.setup(h)
			tip, err := GetLatestDataCompleteBlock()
			if err != nil {
				t.Fatal(err)
			}
			if tip != 100 {
				t.Fatalf("data-complete tip = %d, want 100", tip)
			}
			if got := h.marker(); got != "100" {
				t.Fatalf("marker = %q, want unchanged 100", got)
			}
		})
	}
}

// TestDataCompleteTip_RepairsEvenWhenTheWriteFails: if the marker still
// cannot be written, the read still reports the stored, applied block, so the
// next proposal is for N+1, and the hook reports the write error.
func TestDataCompleteTip_RepairsEvenWhenTheWriteFails(t *testing.T) {
	h := installTipChain(t)
	h.storeBlock(100, 3, true)
	h.storeBlock(101, 2, true)
	h.setMarker(100)
	h.failPutKV[LatestBlockMarkerKey] = true

	var recordErr error
	SetTipRepairHook(func(_, _ uint64, err error) { recordErr = err })

	tip, err := GetLatestDataCompleteBlock()
	if err != nil {
		t.Fatal(err)
	}
	if tip != 101 {
		t.Fatalf("data-complete tip = %d, want 101", tip)
	}
	if recordErr == nil {
		t.Fatal("hook got no error for the failed marker write")
	}
}

// TestDataCompleteTip_WalkIsBounded: several lagging blocks are repaired in
// one read, up to tipRepairMaxSteps.
func TestDataCompleteTip_WalkIsBounded(t *testing.T) {
	h := installTipChain(t)
	for n := uint64(100); n <= 100+tipRepairMaxSteps+3; n++ {
		h.storeBlock(n, 1, true)
	}
	h.setMarker(100)

	tip, err := GetLatestDataCompleteBlock()
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(100 + tipRepairMaxSteps); tip != want {
		t.Fatalf("data-complete tip = %d, want %d", tip, want)
	}
	tip, _ = GetLatestDataCompleteBlock()
	if want := uint64(100 + tipRepairMaxSteps + 3); tip != want {
		t.Fatalf("second read = %d, want %d", tip, want)
	}
}

// TestDataCompleteTip_FreshNode: an unset marker is not walked.
func TestDataCompleteTip_FreshNode(t *testing.T) {
	h := installTipChain(t)
	h.storeBlock(1, 1, true)
	tip, err := GetLatestDataCompleteBlock()
	if err != nil {
		t.Fatal(err)
	}
	if tip != 0 {
		t.Fatalf("data-complete tip = %d, want 0", tip)
	}
}
