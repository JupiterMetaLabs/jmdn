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
	hashes             map[uint64]common.Hash
	single, batch      int
	rowsAsked, rangeRd int
}

func (h *blockReadHandle) GetBlock(_ context.Context, n uint64) (*thebegateway.BlockRecord, error) {
	h.single++
	hash, ok := h.hashes[n]
	if !ok {
		return nil, errors.New("block not found")
	}
	return &thebegateway.BlockRecord{BlockNumber: n, BlockHash: hash.Hex()}, nil
}

func (h *blockReadHandle) GetBlockHashes(_ context.Context, numbers []uint64) (map[uint64]string, error) {
	h.batch++
	h.rowsAsked += len(numbers)
	out := map[uint64]string{}
	for _, n := range numbers {
		if hash, ok := h.hashes[n]; ok {
			out[n] = hash.Hex()
		}
	}
	return out, nil
}

func (h *blockReadHandle) BulkGetBlocks(context.Context, uint64, uint64) ([]*thebegateway.BlockRecord, error) {
	h.rangeRd++
	return nil, errors.New("a range read must not be used to check log blocks")
}

// logsAtHeights returns one log in each of n heights spaced step apart.
func logsAtHeights(n int, step uint64, hashes map[uint64]common.Hash) []*ethtypes.Log {
	logs := make([]*ethtypes.Log, 0, n)
	for i := 0; i < n; i++ {
		h := 100 + step*uint64(i)
		hash := common.BigToHash(new(big.Int).SetUint64(h + 1))
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

// The block check reads exactly the heights in the result: one cached read
// each for a few, batched reads of those heights otherwise. It never reads
// the span between them, however far apart they are (the address path of
// eth_getLogs can return heights from genesis to the tip).
func TestGetLogs_StoredBlockReadsAreTheResultHeights(t *testing.T) {
	for _, tc := range []struct {
		heights       int
		step          uint64
		single, batch int
	}{
		{perHeightBlockReads, 2, perHeightBlockReads, 0},
		{perHeightBlockReads + 1, 1_000_000, 0, 1},
		{2500, 50_000, 0, 3},
	} {
		hashes := map[uint64]common.Hash{}
		logs := logsAtHeights(tc.heights, tc.step, hashes)
		h := &blockReadHandle{fixedLogHandle: fixedLogHandle{logs: logs}, hashes: hashes}
		withBlockReadHandle(t, h)
		got, err := GetLogs(context.Background(), nil, Types.FilterQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.heights || h.single != tc.single || h.batch != tc.batch || h.rangeRd != 0 {
			t.Fatalf("%d heights: %d logs, %d single, %d batched, %d range reads; want %d, %d, %d, 0",
				tc.heights, len(got), h.single, h.batch, h.rangeRd, tc.heights, tc.single, tc.batch)
		}
		if tc.batch > 0 && h.rowsAsked != tc.heights {
			t.Fatalf("%d heights: asked for %d rows; want exactly the result's heights", tc.heights, h.rowsAsked)
		}
	}
}

// A cancelled request reads no blocks at all.
func TestGetLogs_CancelledRequestReadsNoBlocks(t *testing.T) {
	hashes := map[uint64]common.Hash{}
	h := &blockReadHandle{fixedLogHandle: fixedLogHandle{logs: logsAtHeights(40, 3, hashes)}, hashes: hashes}
	withBlockReadHandle(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GetLogs(ctx, nil, Types.FilterQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if h.single+h.batch+h.rangeRd != 0 {
		t.Fatalf("a cancelled request made %d block reads", h.single+h.batch+h.rangeRd)
	}
}
