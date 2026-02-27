package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
	"gossipnode/config/utils"
	"gossipnode/gETH/Facade/Service/Types"

	"github.com/JupiterMetaLabs/ion"
)

// GetLogs retrieves logs based on filter criteria
func GetLogs(mainDBClient *config.PooledConnection, filterQuery Types.FilterQuery) ([]Types.Log, error) {

	// DEFINE NEW GLOBAL REPO USAGE:
	if repo, ok := GlobalRepo.(interface {
		GetLogs(context.Context, Types.FilterQuery) ([]Types.Log, error)
	}); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		logs, err := repo.GetLogs(ctx, filterQuery)
		if err == nil {
			return logs, nil
		}
		// If custom repo fails, fall through to legacy logic
	}

	var err error
	var shouldReturnConnection bool = false

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
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", "ImmuDB.log"),
			ion.String("topic", "ImmuDB_ImmuClient"),
			ion.String("function", "DB_OPs.GetLogs"))
	}

	// Return connection to pool when done
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.GetLogs"))
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
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Error(loggerCtx, "Failed to get latest block number",
				err,
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.GetLogs"))
			return nil, fmt.Errorf("failed to get latest block number: %w", err)
		}
		toBlock = latestBlock
	}

	// Iterate through blocks in the specified range
	for blockNum := fromBlock; blockNum <= toBlock; blockNum++ {
		block, err := GetZKBlockByNumber(mainDBClient, blockNum)
		if err != nil {
			// Log error but continue with other blocks
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Warn(loggerCtx, "Failed to get block for log filtering",
				ion.String("error", err.Error()),
				ion.Uint64("blockNumber", blockNum),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.GetLogs"))
			continue
		}

		// Get logs from all transactions in this block
		blockLogs, err := GetLogsFromBlock(mainDBClient, block, filterQuery)
		if err != nil {
			// Log error but continue with other blocks
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Warn(loggerCtx, "Failed to get logs from block",
				ion.String("error", err.Error()),
				ion.Uint64("blockNumber", blockNum),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.GetLogs"))
			continue
		}

		allLogs = append(allLogs, blockLogs...)
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully retrieved logs",
		ion.Uint64("fromBlock", fromBlock),
		ion.Uint64("toBlock", toBlock),
		ion.Int("logCount", len(allLogs)),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", "ImmuDB.log"),
		ion.String("topic", "ImmuDB_ImmuClient"),
		ion.String("function", "DB_OPs.GetLogs"))

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
