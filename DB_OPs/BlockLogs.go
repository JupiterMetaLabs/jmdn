package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs/logstore"
	"gossipnode/DB_OPs/store"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/metrics"
)

// logQueryTimeout caps one eth_getLogs scan. It is a ceiling on top of the
// caller's context, never a replacement for it: the scan stops at whichever
// comes first.
const logQueryTimeout = 10 * time.Second

// GetLogs retrieves event logs matching the given filter query via ThebeDB SQL.
// PooledConnection may be nil — getHandle falls back to the global ThebeDB handle.
//
// ctx is the CALLER'S context and must be honoured: this is the heaviest RPC
// element, and it used to build its own deadline from context.Background(). A
// scan therefore outlived the request that asked for it — the batch deadline
// (gETH/Facade/rpc/http_server.go) stopped DISPATCHING at 25s but an element
// already running kept scanning for its full 10s, so a batch could reach ~35s
// against a 30s WriteTimeout and the client lost the response it had paid for.
// A client disconnect did not stop an in-flight scan either. A nil ctx is
// treated as context.Background() so a caller that has none is unchanged.
func GetLogs(ctx context.Context, mainDBClient *config.PooledConnection, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, logQueryTimeout)
	defer cancel()

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetLogs: %w", err)
	}

	filter := filterQueryToStoreFilter(filterQuery)
	ethLogs, err := h.GetLogs(ctx, filter)
	if err != nil {
		countLogQueryRefusal(err)
		return nil, fmt.Errorf("GetLogs: %w", err)
	}

	ethLogs, err = keepLogsOfStoredBlocks(ctx, ethLogs)
	if err != nil {
		return nil, fmt.Errorf("GetLogs: %w", err)
	}

	result := make([]Types.Log, 0, len(ethLogs))
	for _, l := range ethLogs {
		if l == nil {
			continue
		}
		topics := make([][]byte, len(l.Topics))
		for i, t := range l.Topics {
			h := t // copy
			topics[i] = h[:]
		}
		result = append(result, Types.Log{
			Address:     l.Address.Bytes(),
			Topics:      topics,
			Data:        l.Data,
			BlockNumber: l.BlockNumber,
			BlockHash:   l.BlockHash.Bytes(),
			TxHash:      l.TxHash.Bytes(),
			TxIndex:     uint64(l.TxIndex),
			LogIndex:    uint64(l.Index),
			Removed:     l.Removed,
		})
	}
	return result, nil
}

// countLogQueryRefusal records which eth_getLogs cost bound refused a query, so
// the bounds can be tuned from evidence instead of guesswork. Only the three
// bounds are counted; a genuine backend failure is not a refusal and is left to
// the error path and the logs.
//
// The reason is one of a fixed set, never anything the caller supplied, so the
// metric's cardinality cannot be driven from the public RPC port.
func countLogQueryRefusal(err error) {
	var reason string
	switch {
	case errors.Is(err, logstore.ErrQueryUnbounded):
		reason = "logs_span"
	case errors.Is(err, logstore.ErrTooManyResults):
		reason = "logs_results"
	case errors.Is(err, logstore.ErrScanBudgetExceeded):
		reason = "logs_scan"
	default:
		return
	}
	metrics.RPCQueryRefusedCounter.WithLabelValues("eth_getLogs", reason).Inc()
}

// filterQueryToStoreFilter converts a gETH FilterQuery to a store.LogFilter.
func filterQueryToStoreFilter(q Types.FilterQuery) store.LogFilter {
	var from, to uint64
	if q.FromBlock != nil {
		from = q.FromBlock.Uint64()
	}
	if q.ToBlock != nil {
		to = q.ToBlock.Uint64()
	}

	addrs := make([]common.Address, 0, len(q.Addresses))
	for _, a := range q.Addresses {
		addrs = append(addrs, common.HexToAddress(a))
	}

	topics := make([][]common.Hash, len(q.Topics))
	for i, row := range q.Topics {
		hashes := make([]common.Hash, len(row))
		for j, t := range row {
			hashes[j] = common.HexToHash(t)
		}
		topics[i] = hashes
	}

	return store.LogFilter{
		FromBlock: from,
		ToBlock:   to,
		Addresses: addrs,
		Topics:    topics,
	}
}

// storedBlockHashFn returns the BlockHash of the block stored at height, or
// found=false when no block is stored there. A seam for tests.
var storedBlockHashFn = func(height uint64) (hash common.Hash, found bool, err error) {
	blk, err := GetZKBlockHeaderByNumber(height)
	if err != nil {
		if IsNotFound(err) {
			return common.Hash{}, false, nil
		}
		return common.Hash{}, false, err
	}
	if blk == nil {
		return common.Hash{}, false, nil
	}
	return blk.BlockHash, true, nil
}

// perHeightBlockReads is the most distinct heights keepLogsOfStoredBlocks
// reads one at a time (each through the block cache). Past it, the hashes of
// exactly those heights are read in batches of blockHashChunk.
const (
	perHeightBlockReads = 32
	blockHashChunk      = 1000
)

// storedBlockHashesFn returns the BlockHash stored at each of heights (sorted
// ascending, distinct). A height with no stored block is absent from the map.
// It honours ctx: the reads run inside the caller's deadline. A seam for tests.
var storedBlockHashesFn = func(ctx context.Context, heights []uint64) (map[uint64]common.Hash, error) {
	out := make(map[uint64]common.Hash, len(heights))
	if len(heights) == 0 {
		return out, nil
	}
	h, err := getHandle(nil)
	if err != nil {
		return nil, err
	}
	if len(heights) <= perHeightBlockReads {
		for _, n := range heights {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rec, err := h.GetBlock(ctx, n)
			if err != nil {
				if IsNotFound(err) {
					continue
				}
				return nil, err
			}
			if rec == nil {
				continue
			}
			blk, err := blockRecordToZKBlock(rec)
			if err != nil {
				return nil, err
			}
			out[n] = blk.BlockHash
		}
		return out, nil
	}
	// One read of exactly these heights (WHERE block_number = ANY), in chunks.
	// Not a range read: on the address path the heights can lie anywhere
	// between genesis and the tip, and a range over them would read every
	// block in between. The rows read equal the heights asked for.
	for start := 0; start < len(heights); start += blockHashChunk {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+blockHashChunk, len(heights))
		hashes, err := h.GetBlockHashes(ctx, heights[start:end])
		if err != nil {
			return nil, err
		}
		for n, hx := range hashes {
			out[n] = common.HexToHash(hx)
		}
	}
	return out, nil
}

// keepLogsOfStoredBlocks drops every log whose block is not the block stored
// at its height.
//
// Logs are indexed while a block is APPLIED (contract_apply.go), before the
// block itself is stored, and nothing removes them if the block is then
// rolled back or never stored. Without this filter eth_getLogs would serve
// events of a block the chain does not contain - the case a bridge relayer
// must never act on. Matching both height and BlockHash also drops logs left
// by a competing block at a height whose stored block is a different one.
//
// The reads run under the caller's ctx (the same deadline and cancellation as
// the log scan): per height through the block cache for a few heights, else
// one batched read of exactly those heights. Either way the rows read are the
// heights in the result, never the span between them. A read error fails the
// query rather than guessing.
func keepLogsOfStoredBlocks(ctx context.Context, logs []*ethtypes.Log) ([]*ethtypes.Log, error) {
	if len(logs) == 0 {
		return logs, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	distinct := make(map[uint64]struct{})
	for _, l := range logs {
		if l != nil {
			distinct[l.BlockNumber] = struct{}{}
		}
	}
	heights := make([]uint64, 0, len(distinct))
	for n := range distinct {
		heights = append(heights, n)
	}
	slices.Sort(heights)
	stored, err := storedBlockHashesFn(ctx, heights)
	if err != nil {
		return nil, fmt.Errorf("check the blocks of %d logs: %w", len(logs), err)
	}
	out := logs[:0]
	for _, l := range logs {
		if l == nil {
			continue
		}
		if h, ok := stored[l.BlockNumber]; ok && h == l.BlockHash {
			out = append(out, l)
		}
	}
	return out, nil
}

// StoredBlockHash returns the BlockHash of the block stored at height, or
// found=false when none is stored there. A missing block is not an error.
func StoredBlockHash(height uint64) (hash common.Hash, found bool, err error) {
	return storedBlockHashFn(height)
}
