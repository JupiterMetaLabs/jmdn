package DB_OPs

import (
	"fmt"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/config/utils"
	"gossipnode/logging"
	"time"

	"go.uber.org/zap"
)

// GetLogs retrieves logs based on filter criteria
func GetLogs(mainDBClient *config.PooledConnection, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	var err error
	var shouldReturnConnection bool = false

	// Get connection if not provided
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetLogs"),
		)
	}

	// Return connection to pool when done
	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.GetLogs"),
			)
			PutMainDBConnection(mainDBClient)
		}()
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
			mainDBClient.Client.Logger.Logger.Error("Failed to get latest block number",
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.GetLogs"),
			)
			return nil, fmt.Errorf("failed to get latest block number: %w", err)
		}
		toBlock = latestBlock
	}

	// Iterate through blocks in the specified range
	for blockNum := fromBlock; blockNum <= toBlock; blockNum++ {
		block, err := GetZKBlockByNumber(mainDBClient, blockNum)
		if err != nil {
			// Log error but continue with other blocks
			mainDBClient.Client.Logger.Logger.Warn("Failed to get block for log filtering",
				zap.Error(err),
				zap.Uint64("blockNumber", blockNum),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.GetLogs"),
			)
			continue
		}

		// Get logs from all transactions in this block
		blockLogs, err := GetLogsFromBlock(mainDBClient, block, filterQuery)
		if err != nil {
			// Log error but continue with other blocks
			mainDBClient.Client.Logger.Logger.Warn("Failed to get logs from block",
				zap.Error(err),
				zap.Uint64("blockNumber", blockNum),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.GetLogs"),
			)
			continue
		}

		allLogs = append(allLogs, blockLogs...)
	}

	mainDBClient.Client.Logger.Logger.Info("Successfully retrieved logs",
		zap.Uint64("fromBlock", fromBlock),
		zap.Uint64("toBlock", toBlock),
		zap.Int("logCount", len(allLogs)),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, "ImmuDB.log"),
		zap.String(logging.Topic, "ImmuDB_ImmuClient"),
		zap.String(logging.Loki_url, logging.GetLokiURL()),
		zap.String(logging.Function, "DB_OPs.GetLogs"),
	)

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