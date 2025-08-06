package explorer

import (
	"encoding/json"
	"gossipnode/config"
	"gossipnode/DB_OPs"
	"strconv"
	"sync"
	"time"
)

var (
	// lastBlockNumber stores the last seen block number
	lastBlockNumber uint64
	blockMutex      sync.RWMutex
)

func ConvertStringToUint64(str string) (uint64, error) {
	return strconv.ParseUint(str, 10, 64)
}

// StartBlockPoller starts a background goroutine that polls for new blocks
func StartBlockPoller(DBclient *ImmuDBServer, pollInterval time.Duration) {
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

	for range ticker.C {
		checkForNewBlocks(DBclient)
	}
}

func checkForNewBlocks(DBclient *ImmuDBServer) {
	// Get the latest block number from the database
	latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(&DBclient.defaultdb)
	if err != nil {
		config.Error(DBclient.defaultdb.Logger, "Failed to get latest block number: %v", err)
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
	config.Info(DBclient.defaultdb.Logger, "New blocks detected, fetching blocks from %d to %d", 
		currentBlockNumber, latestBlockNumber)

	// Fetch missing blocks
	var missingBlocks []interface{}
	for i := currentBlockNumber + 1; i <= latestBlockNumber; i++ {
		block, err := DB_OPs.GetZKBlockByNumber(&DBclient.defaultdb, i)
		if err != nil {
			config.Error(DBclient.defaultdb.Logger, "Failed to fetch block %d: %v", i, err)
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
			config.Error(DBclient.defaultdb.Logger, "Failed to marshal blocks: %v", err)
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