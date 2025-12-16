package explorer

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/logging"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
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
	// Initialize with the current latest block
	if lastBlockNumber == 0 {
		latest, err := DB_OPs.GetLatestBlockNumber(&DBclient.defaultdb)
		if err == nil {
			blockMutex.Lock()
			lastBlockNumber = latest
			blockMutex.Unlock()
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkForNewBlocks(DBclient)
		}
	}
}

func checkForNewBlocks(DBclient *ImmuDBServer) {
	// Get the latest block number from the database
	latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(&DBclient.defaultdb)
	if err != nil {
		DBclient.defaultdb.Client.Logger.Logger.Error("Failed to get latest block number: %v",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Explorer.checkForNewBlocks"),
		)
		return
	}

	blockMutex.RLock()
	currentBlockNumber := lastBlockNumber
	blockMutex.RUnlock()

	// If no new blocks, do nothing
	if latestBlockNumber <= currentBlockNumber {
		return
	}

	// New blocks found, fetch and notify
	DBclient.defaultdb.Client.Logger.Logger.Info("New blocks detected, fetching blocks from %d to %d",
		zap.String("currentBlockNumber", fmt.Sprintf("%d", currentBlockNumber)),
		zap.String("latestBlockNumber", fmt.Sprintf("%d", latestBlockNumber)),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "Explorer.checkForNewBlocks"),
	)

	// Fetch missing blocks
	var missingBlocks []interface{}
	for i := currentBlockNumber + 1; i <= latestBlockNumber; i++ {
		block, err := DB_OPs.GetZKBlockByNumber(&DBclient.defaultdb, i)
		if err != nil {
			DBclient.defaultdb.Client.Logger.Logger.Error("Failed to fetch block %d: %v",
				zap.Error(err),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "Explorer.checkForNewBlocks"),
			)
			continue
		}
		missingBlocks = append(missingBlocks, block)

		// Update last processed block number
		blockMutex.Lock()
		lastBlockNumber = i
		blockMutex.Unlock()
	}

	// Notify WebSocket clients if we have new blocks
	if len(missingBlocks) > 0 {
		data, err := json.Marshal(missingBlocks)
		if err != nil {
			DBclient.defaultdb.Client.Logger.Logger.Error("Failed to marshal blocks: %v",
				zap.Error(err),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "Explorer.checkForNewBlocks"),
			)
			return
		}

		sendEventToClients(StreamEvent{
			EventType: "new_blocks",
			Data:      string(data),
		})
	}
}

// GetLastBlockNumber returns the last processed block number
func GetLastBlockNumber() uint64 {
	blockMutex.RLock()
	defer blockMutex.RUnlock()
	return lastBlockNumber
}
