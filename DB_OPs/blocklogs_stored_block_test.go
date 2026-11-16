package DB_OPs

// Logs are indexed while a block is applied, before it is stored, and a
// rollback does not remove them. eth_getLogs must only return logs of the
// block actually stored at each height.

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs/store"
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
	prevFn := storedBlockHashFn
	storedBlockHashFn = func(height uint64) (common.Hash, bool, error) {
		if readErr != nil {
			return common.Hash{}, false, readErr
		}
		h, ok := stored[height]
		return h, ok, nil
	}
	t.Cleanup(func() {
		SetGlobalHandle(prev)
		storedBlockHashFn = prevFn
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
