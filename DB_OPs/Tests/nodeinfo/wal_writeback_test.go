// Verifies the WAL replay write paths land in ThebeDB: PoTS WAL hydration
// (FastsyncV2.dumpPoTSWALToDB) and crash recovery replay both write through
// BlockInfo.NewHeadersWriter().WriteHeaders() and NewDataWriter().WriteData().
// These tests prove those writers hit the ThebeDB handle (StoreBlock et al.),
// so every WAL event type that carries DB writes reaches ThebeDB:
//
//	HeaderSync → WriteHeaders → handle.StoreBlock
//	DataSync   → WriteData    → handle.StoreBlock (+ transactions)
//	PoTS       → replayed via the two paths above
//	MerkleSync / PriorSync → state only, no DB write (by design)
package nodeinfo_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	blockpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"

	"gossipnode/DB_OPs"
	NodeInfo "gossipnode/DB_OPs/Nodeinfo"
	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// captureHandle records StoreBlock calls; other ThebeHandle methods fall
// through to the nil embedded interface (panic if unexpectedly called),
// except the ones the write path legitimately touches.
type captureHandle struct {
	store.ThebeHandle
	mu     sync.Mutex
	blocks []uint64
	txs    int
	syncKV map[string][]byte
}

// Sync-state KV — the data writer's marker-advance tail (latest_block) reads
// and writes these after a successful batch.
func (c *captureHandle) GetSyncKV(key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.syncKV[key], nil // nil when absent, per the interface contract
}

func (c *captureHandle) PutSyncKV(key string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.syncKV == nil {
		c.syncKV = make(map[string][]byte)
	}
	c.syncKV[key] = append([]byte(nil), value...)
	return nil
}

func (c *captureHandle) StoreBlock(_ context.Context, b *config.ZKBlock) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocks = append(c.blocks, b.BlockNumber)
	return nil
}

func (c *captureHandle) StoreZKBlock(_ context.Context, _ *config.ZKBlock) error { return nil }

// GetBlock simulates "block not found" so WriteData builds a fresh ZKBlock.
func (c *captureHandle) GetBlock(_ context.Context, n uint64) (*thebegateway.BlockRecord, error) {
	return nil, fmt.Errorf("block %d not found", n)
}

func (c *captureHandle) StoreTransaction(_ context.Context, _ *config.Transaction, _ uint64, _ int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.txs++
	return nil
}

func (c *captureHandle) RefreshAccountTxStats(_ context.Context, _ string) error { return nil }

func TestWALReplayHeadersWriteThroughToThebeDB(t *testing.T) {
	h := &captureHandle{}
	DB_OPs.SetGlobalHandle(h)
	defer DB_OPs.SetGlobalHandle(nil)

	w := NodeInfo.NewSyncStruct().NewHeadersWriter()
	headers := []*blockpb.Header{
		{BlockNumber: 7, Timestamp: 1},
		{BlockNumber: 8, Timestamp: 2},
	}
	if err := w.WriteHeaders(headers); err != nil {
		t.Fatalf("WriteHeaders: %v", err)
	}
	if len(h.blocks) != 2 || h.blocks[0] != 7 || h.blocks[1] != 8 {
		t.Fatalf("expected blocks [7 8] stored in ThebeDB, got %v", h.blocks)
	}
}

func TestWALReplayDataWritesThroughToThebeDB(t *testing.T) {
	h := &captureHandle{}
	DB_OPs.SetGlobalHandle(h)
	defer DB_OPs.SetGlobalHandle(nil)

	w := NodeInfo.NewSyncStruct().NewDataWriter()
	data := []*blockpb.NonHeaders{
		{BlockNumber: 9},
	}
	if err := w.WriteData(data); err != nil {
		t.Fatalf("WriteData: %v", err)
	}
	if len(h.blocks) != 1 || h.blocks[0] != 9 {
		t.Fatalf("expected block [9] stored in ThebeDB, got %v", h.blocks)
	}
}
