package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs/logstore"
	"gossipnode/DB_OPs/store"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/metrics"
)

// GetLogs retrieves event logs matching the given filter query via ThebeDB SQL.
// PooledConnection may be nil — getHandle falls back to the global ThebeDB handle.
func GetLogs(mainDBClient *config.PooledConnection, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
