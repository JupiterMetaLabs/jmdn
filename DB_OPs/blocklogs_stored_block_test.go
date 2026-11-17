package DB_OPs

// Logs are indexed while a block is applied, before it is stored, and a
// rollback does not remove them. eth_getLogs must only return logs of the
// block actually stored at each height.

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/gETH/Facade/Service/Types"
)

type fixedLogHandle struct {
	store.ThebeHandle
	logs []*ethtypes.Log
}

func (h *fixedLogHandle) GetLogs(context.Context, store.LogFilter) ([]*ethtypes.Log, error) {
	out := make([]*ethtypes.Log, len(h.logs))
	copy(out, h.logs)
	return out, nil
}

func withLogsAndStoredBlocks(t *testing.T, logs []*ethtypes.Log, stored map[uint64]common.Hash, readErr error) {
	t.Helper()
	globalThebeHandleMu.RLock()
	prev := globalThebeHandle
	globalThebeHandleMu.RUnlock()
	SetGlobalHandle(&fixedLogHandle{logs: logs})
	prevFn := storedBlockHashesFn
	storedBlockHashesFn = func(_ context.Context, heights []uint64) (map[uint64]common.Hash, error) {
		if readErr != nil {
			return nil, readErr
		}
		out := map[uint64]common.Hash{}
		for _, n := range heights {
			if h, ok := stored[n]; ok {
				out[n] = h
			}
		}
		return out, nil
	}
	t.Cleanup(func() {
		SetGlobalHandle(prev)
		storedBlockHashesFn = prevFn
	})
}

func TestGetLogs_OnlyLogsOfStoredBlocks(t *testing.T) {
	canonical5 := common.HexToHash("0x05")
	canonical6 := common.HexToHash("0x06")
	competing6 := common.HexToHash("0x66")
	rolledBack7 := common.HexToHash("0x07")

	logs := []*ethtypes.Log{
		{BlockNumber: 5, BlockHash: canonical5, Index: 0},
		{BlockNumber: 6, BlockHash: competing6, Index: 0}, // a different block once applied at 6
		{BlockNumber: 6, BlockHash: canonical6, Index: 1},
		{BlockNumber: 7, BlockHash: rolledBack7, Index: 0}, // applied, then rolled back: nothing stored at 7
	}
	withLogsAndStoredBlocks(t, logs, map[uint64]common.Hash{5: canonical5, 6: canonical6}, nil)

	got, err := GetLogs(context.Background(), nil, Types.FilterQuery{})
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d logs, want the 2 of stored blocks: %+v", len(got), got)
	}
	for _, l := range got {
		h := common.BytesToHash(l.BlockHash)
		if h != canonical5 && h != canonical6 {
			t.Fatalf("served a log of block %s at height %d, which is not the stored block", h.Hex(), l.BlockNumber)
		}
	}
}

func TestGetLogs_BlockReadErrorFailsTheQuery(t *testing.T) {
	logs := []*ethtypes.Log{{BlockNumber: 5, BlockHash: common.HexToHash("0x05")}}
	withLogsAndStoredBlocks(t, logs, nil, errors.New("db down"))
	if _, err := GetLogs(context.Background(), nil, Types.FilterQuery{}); err == nil {
		t.Fatal("a block read error must fail the query, not serve unchecked logs")
	}
}

// blockReadHandle serves stored blocks and counts the reads made.
type blockReadHandle struct {
	fixedLogHandle
	hashes       map[uint64]common.Hash
	single, bulk int
}

func (h *blockReadHandle) GetBlock(_ context.Context, n uint64) (*thebegateway.BlockRecord, error) {
	h.single++
	hash, ok := h.hashes[n]
	if !ok {
		return nil, errors.New("block not found")
	}
	return &thebegateway.BlockRecord{BlockNumber: n, BlockHash: hash.Hex()}, nil
}

func (h *blockReadHandle) BulkGetBlocks(_ context.Context, from, to uint64) ([]*thebegateway.BlockRecord, error) {
	h.bulk++
	var out []*thebegateway.BlockRecord
	for n := from; n <= to; n++ {
		if hash, ok := h.hashes[n]; ok {
			out = append(out, &thebegateway.BlockRecord{BlockNumber: n, BlockHash: hash.Hex()})
		}
	}
	return out, nil
}

func logsAtHeights(n int, hashes map[uint64]common.Hash) []*ethtypes.Log {
	logs := make([]*ethtypes.Log, 0, n)
	for i := 0; i < n; i++ {
		h := uint64(100 + 2*i)
		hash := common.BigToHash(new(big.Int).SetUint64(h))
		hashes[h] = hash
		logs = append(logs, &ethtypes.Log{BlockNumber: h, BlockHash: hash})
	}
	return logs
}

func withBlockReadHandle(t *testing.T, h *blockReadHandle) {
	t.Helper()
	globalThebeHandleMu.RLock()
	prev := globalThebeHandle
	globalThebeHandleMu.RUnlock()
	SetGlobalHandle(h)
	t.Cleanup(func() { SetGlobalHandle(prev) })
}

// Few heights: one cached read each. Many heights: one range read in total,
// never one round trip per height.
func TestGetLogs_StoredBlockReadsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		heights      int
		single, bulk int
	}{
		{perHeightBlockReads, perHeightBlockReads, 0},
		{perHeightBlockReads + 1, 0, 1},
		{1000, 0, 1},
	} {
		hashes := map[uint64]common.Hash{}
		logs := logsAtHeights(tc.heights, hashes)
		h := &blockReadHandle{fixedLogHandle: fixedLogHandle{logs: logs}, hashes: hashes}
		withBlockReadHandle(t, h)
		got, err := GetLogs(context.Background(), nil, Types.FilterQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.heights || h.single != tc.single || h.bulk != tc.bulk {
			t.Fatalf("%d heights: %d logs, %d single reads, %d range reads; want %d, %d, %d",
				tc.heights, len(got), h.single, h.bulk, tc.heights, tc.single, tc.bulk)
		}
	}
}

// A cancelled request reads no blocks at all.
func TestGetLogs_CancelledRequestReadsNoBlocks(t *testing.T) {
	hashes := map[uint64]common.Hash{}
	h := &blockReadHandle{fixedLogHandle: fixedLogHandle{logs: logsAtHeights(10, hashes)}, hashes: hashes}
	withBlockReadHandle(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GetLogs(ctx, nil, Types.FilterQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if h.single+h.bulk != 0 {
		t.Fatalf("a cancelled request made %d block reads", h.single+h.bulk)
	}
}
