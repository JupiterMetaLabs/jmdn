package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
	"gossipnode/config/utils"
	"gossipnode/gETH/Facade/Service/Types"
)

// GetLogs retrieves logs based on filter criteria
func GetLogs(mainDBClient *config.PooledConnection, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get connection if not provided
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - GetLogs", err)
		}
		shouldReturnConnection = true
	}

	// Return connection to pool when done
	if shouldReturnConnection {
		defer PutMainDBConnection(mainDBClient)
	}

	var allLogs []Types.Log

	// Determine block range
	fromBlock := uint64(0)
	toBlock := uint64(0)

	if filterQuery.FromBlock != nil {
		fromBlock = filterQuery.FromBlock.Uint64()
	}

	if filterQuery.ToBlock != nil {
		toBlock = filterQuery.ToBlock.Uint64()
	} else {
		// If ToBlock is not specified, get the latest block number
		latestBlock, err := GetLatestBlockNumber(mainDBClient)
		if err != nil {
			return nil, fmt.Errorf("failed to get latest block number: %w", err)
		}
		toBlock = latestBlock
	}

	// Iterate through blocks in the specified range
	for blockNum := fromBlock; blockNum <= toBlock; blockNum++ {
		block, err := GetZKBlockByNumber(mainDBClient, blockNum)
		if err != nil {
			// Log error but continue with other blocks
			continue
		}

		// Get logs from all transactions in this block
		blockLogs, err := GetLogsFromBlock(mainDBClient, block, filterQuery)
		if err != nil {
			// Log error but continue with other blocks
			continue
		}

		allLogs = append(allLogs, blockLogs...)
	}

	return allLogs, nil
}

// getLogsFromBlock extracts logs from a specific block based on filter criteria
func GetLogsFromBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	var blockLogs []Types.Log

	// Iterate through all transactions in the block
	for _, tx := range block.Transactions {
		// Get receipt for this transaction
		receipt, err := GetReceiptByHash(mainDBClient, tx.Hash.Hex())
		if err != nil {
			// If receipt doesn't exist, skip this transaction
			continue
		}

		// Convert config.Log to Types.Log and apply filters
		for _, log := range receipt.Logs {
			// Convert to Types.Log format
			typesLog := Types.Log{
				Address:     log.Address.Bytes(),
				Topics:      utils.ConvertHashesToByteArrays(log.Topics),
				Data:        log.Data,
				BlockNumber: log.BlockNumber,
				TxHash:      log.TxHash.Bytes(),
				LogIndex:    log.LogIndex,
			}

			// Apply address filter
			if len(filterQuery.Addresses) > 0 {
				if !utils.ContainsAddress(filterQuery.Addresses, string(typesLog.Address)) {
					continue
				}
			}

			// Apply topic filters
			if len(filterQuery.Topics) > 0 {
				if !utils.MatchesTopicFilter(filterQuery.Topics, utils.ConvertHashesToStrings(log.Topics)) {
					continue
				}
			}

			blockLogs = append(blockLogs, typesLog)
		}
	}

	return blockLogs, nil
}
