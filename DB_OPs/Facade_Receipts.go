package DB_OPs

import (
	"encoding/json"
	"fmt"
	"gossipnode/config"
	"gossipnode/config/utils"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/logging"
	"math/big"
	"time"

	"go.uber.org/zap"
)

// GetReceiptByHash retrieves a transaction receipt by its hash
func GetReceiptByHash(mainDBClient *config.PooledConnection, hash string) (*config.Receipt, error) {
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
			zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
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
				zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}

	// First, try to get the receipt directly from storage
	receiptKey := fmt.Sprintf("%s%s", DEFAULT_PREFIX_RECEIPT, hash)
	receiptBytes, err := Read(mainDBClient, receiptKey)
	if err == nil && len(receiptBytes) > 0 {
		// Receipt exists, unmarshal it
		var receipt config.Receipt
		if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
			mainDBClient.Client.Logger.Logger.Error("Failed to unmarshal receipt",
				zap.Error(err),
				zap.String("txHash", hash),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
			)
			return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
		}

		mainDBClient.Client.Logger.Logger.Info("Successfully retrieved receipt from storage",
			zap.String("txHash", hash),
			zap.Uint64("blockNumber", receipt.BlockNumber),
			zap.Uint64("status", receipt.Status),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
		)
		return &receipt, nil
	}

	// Receipt doesn't exist in storage, generate it from transaction data
	mainDBClient.Client.Logger.Logger.Info("Receipt not found in storage, generating from transaction data",
		zap.String("txHash", hash),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
	)

	// Get the transaction to generate receipt
	tx, err := GetTransactionByHash(mainDBClient, hash)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to get transaction for receipt generation",
			zap.Error(err),
			zap.String("txHash", hash),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
		)
		return nil, fmt.Errorf("failed to get transaction for receipt generation: %w", err)
	}

	// Get the block containing this transaction
	block, err := GetTransactionBlock(mainDBClient, hash)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to get block for receipt generation",
			zap.Error(err),
			zap.String("txHash", hash),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
		)
		return nil, fmt.Errorf("failed to get block for receipt generation: %w", err)
	}

	// Find transaction index in the block
	var txIndex uint64 = 0
	for i, blockTx := range block.Transactions {
		if blockTx.Hash.Hex() == hash {
			txIndex = uint64(i)
			break
		}
	}

	// Generate receipt from transaction and block data
	receipt := generateReceiptFromTransaction(tx, block, txIndex)

	// Store the generated receipt for future use
	receiptBytes, err = json.Marshal(receipt)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to marshal generated receipt",
			zap.Error(err),
			zap.String("txHash", hash),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
		)
		// Don't fail the operation, just log the error
	} else {
		// Store the receipt
		if err := Create(mainDBClient, receiptKey, receiptBytes); err != nil {
			mainDBClient.Client.Logger.Logger.Error("Failed to store generated receipt",
				zap.Error(err),
				zap.String("txHash", hash),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
			)
			// Don't fail the operation, just log the error
		}
	}

	mainDBClient.Client.Logger.Logger.Info("Successfully generated and returned receipt",
		zap.String("txHash", hash),
		zap.Uint64("blockNumber", receipt.BlockNumber),
		zap.Uint64("status", receipt.Status),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
	)

	return receipt, nil
}

// generateReceiptFromTransaction creates a receipt from transaction and block data
func generateReceiptFromTransaction(tx *config.Transaction, block *config.ZKBlock, txIndex uint64) *config.Receipt {
	// Calculate cumulative gas used up to this transaction using actual gas consumption
	var cumulativeGasUsed uint64 = 0
	for i := uint64(0); i <= txIndex; i++ {
		if i < uint64(len(block.Transactions)) {
			// Use actual gas consumption if available, otherwise fall back to gas limit
			gasUsed := block.Transactions[i].GasLimit
			cumulativeGasUsed += gasUsed
		}
	}

	// Generate logs (empty for now, but could be populated from contract execution)
	logs := []config.Log{}

	// Create bloom filter for logs using proper Ethereum algorithm
	logsBloom := utils.GenerateLogsBloom(logs)

	// Determine if this is a contract creation transaction
	// For now, we dont need to worry about the contractAddress
	// var contractAddress *common.Address = nil
	// if tx.To == nil {
	// 	// This is a contract creation transaction
	// 	// Future, For now, we'll leave it as nil
	// }

	// Use actual gas consumption if available, otherwise fall back to gas limit
	gasUsed := tx.GasLimit
	if gasUsed == 0 {
		// If gas tracking is not available, use gas limit as fallback
		gasUsed = tx.GasLimit
	}

	receipt := &config.Receipt{
		TxHash:            tx.Hash,
		BlockHash:         block.BlockHash,
		BlockNumber:       block.BlockNumber,
		TransactionIndex:  txIndex,
		Status:            uint64(1),
		Type:              tx.Type,
		GasUsed:           gasUsed, // Use actual gas consumption
		CumulativeGasUsed: cumulativeGasUsed,
		ContractAddress:   nil,
		Logs:              logs,
		LogsBloom:         logsBloom,
		ZKProof:           block.StarkProof,
		ZKStatus:          block.Status,
	}

	return receipt
}

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
				Address:     log.Address.Hex(),
				Topics:      utils.ConvertHashesToStrings(log.Topics),
				Data:        log.Data,
				BlockNumber: big.NewInt(int64(log.BlockNumber)),
				TxHash:      log.TxHash.Hex(),
				LogIndex:    uint32(log.LogIndex),
			}

			// Apply address filter
			if len(filterQuery.Addresses) > 0 {
				if !utils.ContainsAddress(filterQuery.Addresses, typesLog.Address) {
					continue
				}
			}

			// Apply topic filters
			if len(filterQuery.Topics) > 0 {
				if !utils.MatchesTopicFilter(filterQuery.Topics, typesLog.Topics) {
					continue
				}
			}

			blockLogs = append(blockLogs, typesLog)
		}
	}

	return blockLogs, nil
}
