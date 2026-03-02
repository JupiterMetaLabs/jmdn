package explorer

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"jmdn/DB_OPs"

	"github.com/JupiterMetaLabs/ion"
	"go.opentelemetry.io/otel/attribute"
)

var (
	// lastBlockNumber stores the last seen block number
	lastBlockNumber uint64
	blockMutex      sync.RWMutex
)

func ConvertStringToUint64(str string) (uint64, error) {
	return strconv.ParseUint(str, 10, 64)
}

// StartBlockPoller starts a background loop that polls for new blocks.
// It stops when ctx is cancelled.
func StartBlockPoller(ctx context.Context, DBclient *ImmuDBServer, pollInterval time.Duration) {
	// Create span for block poller initialization
	spanCtx, span := DBclient.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(ctx, "ExplorerAPI.StartBlockPoller")
	defer span.End()

	span.SetAttributes(
		attribute.String("poll_interval", pollInterval.String()),
	)

	// Initialize with the current latest block
	if lastBlockNumber == 0 {
		latest, err := DB_OPs.GetLatestBlockNumber(&DBclient.defaultdb)
		if err == nil {
			blockMutex.Lock()
			lastBlockNumber = latest
			blockMutex.Unlock()
			span.SetAttributes(attribute.Int64("initial_block_number", int64(latest)))
		} else {
			span.RecordError(err)
			DBclient.defaultdb.Client.Logger.Error(spanCtx, "Failed to get initial latest block number",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ExplorerAPI.StartBlockPoller"))
		}
	}

	DBclient.defaultdb.Client.Logger.Info(spanCtx, "Block poller started",
		ion.String("poll_interval", pollInterval.String()),
		ion.Uint64("last_block_number", lastBlockNumber),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.StartBlockPoller"))

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			DBclient.defaultdb.Client.Logger.Info(spanCtx, "Block poller stopped",
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ExplorerAPI.StartBlockPoller"))
			return
		case <-ticker.C:
			checkForNewBlocks(DBclient)
		}
	}
}

func checkForNewBlocks(DBclient *ImmuDBServer) {
	// Create span for checking new blocks
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spanCtx, span := DBclient.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(loggerCtx, "ExplorerAPI.checkForNewBlocks")
	defer span.End()

	startTime := time.Now().UTC()

	// Get the latest block number from the database
	latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(&DBclient.defaultdb)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		DBclient.defaultdb.Client.Logger.Error(spanCtx, "Failed to get latest block number",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.checkForNewBlocks"))
		return
	}

	blockMutex.RLock()
	currentBlockNumber := lastBlockNumber
	blockMutex.RUnlock()

	span.SetAttributes(
		attribute.Int64("current_block_number", int64(currentBlockNumber)),
		attribute.Int64("latest_block_number", int64(latestBlockNumber)),
	)

	// If no new blocks, do nothing
	if latestBlockNumber <= currentBlockNumber {
		span.SetAttributes(attribute.String("status", "no_new_blocks"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return
	}

	// New blocks found, fetch and notify
	blocksToFetch := latestBlockNumber - currentBlockNumber
	span.SetAttributes(
		attribute.String("status", "new_blocks_detected"),
		attribute.Int64("blocks_to_fetch", int64(blocksToFetch)),
	)

	DBclient.defaultdb.Client.Logger.Info(spanCtx, "New blocks detected, fetching blocks",
		ion.Uint64("current_block_number", currentBlockNumber),
		ion.Uint64("latest_block_number", latestBlockNumber),
		ion.Uint64("blocks_to_fetch", blocksToFetch),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.checkForNewBlocks"))

	// Fetch missing blocks
	var missingBlocks []interface{}
	successCount := 0
	errorCount := 0

	for i := currentBlockNumber + 1; i <= latestBlockNumber; i++ {
		block, err := DB_OPs.GetZKBlockByNumber(&DBclient.defaultdb, i)
		if err != nil {
			errorCount++
			span.RecordError(err)
			DBclient.defaultdb.Client.Logger.Error(spanCtx, "Failed to fetch block",
				err,
				ion.Uint64("block_number", i),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ExplorerAPI.checkForNewBlocks"))
			continue
		}
		missingBlocks = append(missingBlocks, block)
		successCount++

		// Update last processed block number
		blockMutex.Lock()
		lastBlockNumber = i
		blockMutex.Unlock()
	}

	span.SetAttributes(
		attribute.Int("blocks_fetched", successCount),
		attribute.Int("blocks_errors", errorCount),
		attribute.Int("total_blocks", len(missingBlocks)),
	)

	// Notify WebSocket clients if we have new blocks
	if len(missingBlocks) > 0 {
		data, err := json.Marshal(missingBlocks)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "marshal_error"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			DBclient.defaultdb.Client.Logger.Error(spanCtx, "Failed to marshal blocks",
				err,
				ion.Int("blocks_count", len(missingBlocks)),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ExplorerAPI.checkForNewBlocks"))
			return
		}

		sendEventToClients(StreamEvent{
			EventType: "new_blocks",
			Data:      string(data),
		})

		span.SetAttributes(
			attribute.String("status", "success"),
			attribute.Int("blocks_notified", len(missingBlocks)),
		)

		DBclient.defaultdb.Client.Logger.Info(spanCtx, "New blocks notified to WebSocket clients",
			ion.Int("blocks_count", len(missingBlocks)),
			ion.Uint64("from_block", currentBlockNumber+1),
			ion.Uint64("to_block", latestBlockNumber),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.checkForNewBlocks"))
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))
}

// GetLastBlockNumber returns the last processed block number
func GetLastBlockNumber() uint64 {
	blockMutex.RLock()
	defer blockMutex.RUnlock()
	return lastBlockNumber
}
