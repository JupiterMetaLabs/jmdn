package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gossipnode/config"
	"gossipnode/config/utils"

	"github.com/JupiterMetaLabs/ion"
)

// GetReceiptByHash retrieves a transaction receipt by its hash
func GetReceiptByHash(mainDBClient *config.PooledConnection, hash string) (*config.Receipt, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get connection if not provided
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", "ImmuDB.log"),
			ion.String("topic", "ImmuDB_ImmuClient"),
			ion.String("function", "DB_OPs.GetReceiptByHash"))
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
				ion.String("function", "DB_OPs.GetReceiptByHash"))
			PutMainDBConnection(mainDBClient)
		}()
	}

	// Normalize hash - ensure it has 0x prefix (keys are stored with 0x prefix)
	normalizedHash := hash
	if !strings.HasPrefix(strings.ToLower(hash), "0x") {
		normalizedHash = "0x" + hash
	}

	// FIRST: Check if transaction exists (similar to TxByHash pattern)
	// Get the transaction to verify it exists
	tx, err := GetTransactionByHash(mainDBClient, normalizedHash)
	if err == nil && tx != nil {
		// Transaction found - get the block and generate receipt
		block, err := GetTransactionBlock(ctx, mainDBClient, normalizedHash)
		if err != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Error(loggerCtx, "Failed to get block for receipt generation",
				err,
				ion.String("txHash", normalizedHash),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.GetReceiptByHash"))
			return nil, fmt.Errorf("failed to get block for receipt generation: %w", err)
		}

		// Find transaction index in the block
		var txIndex uint64 = 0
		for i, blockTx := range block.Transactions {
			if blockTx.Hash.Hex() == normalizedHash {
				txIndex = uint64(i)
				break
			}
		}

		// Generate receipt from transaction and block data
		receipt := generateReceiptFromTransaction(mainDBClient, tx, block, txIndex)

		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully generated and returned receipt",
			ion.String("txHash", normalizedHash),
			ion.Uint64("blockNumber", receipt.BlockNumber),
			ion.Uint64("status", receipt.Status),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetReceiptByHash"))

		return receipt, nil
	}

	// Transaction not found - SECOND: Check if tx_processing = -1
	processingKey := fmt.Sprintf("tx_processing:%s", normalizedHash)
	processing, err := Exists(mainDBClient, processingKey)
	if err == nil && processing {
		// Read the value to check if it's -1
		processingValueBytes, readErr := Read(mainDBClient, processingKey)
		if readErr == nil && len(processingValueBytes) > 0 {
			var processingValue int64
			if jsonErr := json.Unmarshal(processingValueBytes, &processingValue); jsonErr == nil {
				if processingValue == -1 {
					loggerCtx, cancel := context.WithCancel(context.Background())
					defer cancel()
					mainDBClient.Client.Logger.Info(loggerCtx, "Transaction processing status is -1, returning null result",
						ion.String("txHash", normalizedHash),
						ion.Int64("processingValue", processingValue),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "DB_OPs.GetReceiptByHash"))
					// Return nil receipt to indicate result should be null
					return nil, nil
				}
			}
		}
	}

	// THIRD: Transaction not found and tx_processing is not -1 (or doesn't exist)
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mainDBClient.Client.Logger.Error(loggerCtx, "Transaction not found",
		fmt.Errorf("transaction not found"),
		ion.String("txHash", normalizedHash),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", "ImmuDB.log"),
		ion.String("topic", "ImmuDB_ImmuClient"),
		ion.String("function", "DB_OPs.GetReceiptByHash"))
	// Return error that will be formatted as "transaction not found" in JSON-RPC
	return nil, fmt.Errorf("transaction not found")
}

// generateReceiptFromTransaction creates a receipt from transaction and block data
func generateReceiptFromTransaction(mainDBClient *config.PooledConnection, tx *config.Transaction, block *config.ZKBlock, txIndex uint64) *config.Receipt {
	// gasForTx returns the actual gas consumed by a transaction, falling back
	// to GasLimit when GasUsed is not yet populated (pre-EIP-1559 blocks).
	gasForTx := func(t config.Transaction) uint64 {
		if t.GasUsed > 0 {
			return t.GasUsed
		}
		return t.GasLimit
	}

	// Cumulative gas used = sum of gas for all txns up to and including this one.
	var cumulativeGasUsed uint64
	for i := uint64(0); i <= txIndex; i++ {
		if i < uint64(len(block.Transactions)) {
			cumulativeGasUsed += gasForTx(block.Transactions[i])
		}
	}

	// Plain ETH transfers and most non-contract calls emit no logs.
	// Do not fabricate synthetic log entries — return an empty slice so
	// dApps that parse receipt logs see correct data.
	logs := []config.Log{}
	logsBloom := utils.GenerateLogsBloom(logs)

	// Actual gas used by this transaction.
	gasUsed := gasForTx(*tx)

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

func MakeReceiptRoot(mainDBClient *config.PooledConnection, receipts []*config.Receipt) ([]byte, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", "ImmuDB.log"),
			ion.String("topic", "ImmuDB_ImmuClient"),
			ion.String("function", "DB_OPs.MakeReceiptRoot"))
	}

	receiptRoot, err := utils.GenerateReceiptRoot(receipts)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to generate receipt root",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", "ImmuDB.log"),
			ion.String("topic", "ImmuDB_ImmuClient"),
			ion.String("function", "DB_OPs.MakeReceiptRoot"))
		return nil, fmt.Errorf("failed to generate receipt root: %w", err)
	}

	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.MakeReceiptRoot"))
			PutMainDBConnection(mainDBClient)
		}()
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully generated receipt root",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", "ImmuDB.log"),
		ion.String("topic", "ImmuDB_ImmuClient"),
		ion.String("function", "DB_OPs.MakeReceiptRoot"))

	return receiptRoot, nil

}

func GetReceiptsofBlock(mainDBClient *config.PooledConnection, blockNumber uint64) ([]*config.Receipt, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", "ImmuDB.log"),
			ion.String("topic", "ImmuDB_ImmuClient"),
			ion.String("function", "DB_OPs.GetReceiptsofBlock"))
	}

	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.GetReceiptsofBlock"))
			PutMainDBConnection(mainDBClient)
		}()
	}

	// Get Transactions of block and then get receipts for each transaction
	transactions, err := GetTransactionsOfBlock(mainDBClient, blockNumber)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to get transactions of block",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", "ImmuDB.log"),
			ion.String("topic", "ImmuDB_ImmuClient"),
			ion.String("function", "DB_OPs.GetReceiptsofBlock"))
		return nil, fmt.Errorf("failed to get transactions of block: %w", err)
	}

	receipts := make([]*config.Receipt, len(transactions))
	for i, tx := range transactions {
		receipt, err := GetReceiptByHash(mainDBClient, tx.Hash.Hex())
		if err != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Error(loggerCtx, "Failed to get receipt by hash",
				err,
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.GetReceiptsofBlock"))
			return nil, fmt.Errorf("failed to get receipt by hash: %w", err)
		}
		receipts[i] = receipt
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully retrieved receipts of block",
		ion.Uint64("blockNumber", blockNumber),
		ion.Int("receiptCount", len(receipts)),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", "ImmuDB.log"),
		ion.String("topic", "ImmuDB_ImmuClient"),
		ion.String("function", "DB_OPs.GetReceiptsofBlock"))
	return receipts, nil
}
