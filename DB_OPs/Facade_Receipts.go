package DB_OPs

import (
	"encoding/json"
	"fmt"
	"gossipnode/config"
	"gossipnode/config/utils"
	"gossipnode/logging"
	"strings"
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
			zap.Time(logging.Created_at, time.Now().UTC()),
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
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}

	// Normalize hash - ensure it has 0x prefix (keys are stored with 0x prefix)
	normalizedHash := hash
	if !strings.HasPrefix(strings.ToLower(hash), "0x") {
		normalizedHash = "0x" + hash
	}

	// First, check if transaction is currently processing
	processingKey := fmt.Sprintf("tx_processing:%s", normalizedHash)
	processing, err := Exists(mainDBClient, processingKey)
	if err == nil && processing {
		mainDBClient.Client.Logger.Logger.Info("Transaction is currently processing, receipt not available yet",
			zap.String("txHash", normalizedHash),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
		)
		// Return nil to indicate receipt not available yet (transaction still processing)
		return nil, ErrNotFound
	}

	// Try to get the receipt directly from storage
	receiptKey := fmt.Sprintf("%s%s", DEFAULT_PREFIX_RECEIPT, normalizedHash)
	receiptBytes, err := Read(mainDBClient, receiptKey)
	if err == nil && len(receiptBytes) > 0 {
		// Receipt exists, unmarshal it
		var receipt config.Receipt
		if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
			mainDBClient.Client.Logger.Logger.Error("Failed to unmarshal receipt",
				zap.Error(err),
				zap.String("txHash", normalizedHash),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
			)
			return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
		}

		mainDBClient.Client.Logger.Logger.Info("Successfully retrieved receipt from storage",
			zap.String("txHash", normalizedHash),
			zap.Uint64("blockNumber", receipt.BlockNumber),
			zap.Uint64("status", receipt.Status),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
		)
		return &receipt, nil
	}

	// Receipt doesn't exist in storage, generate it from transaction data
	mainDBClient.Client.Logger.Logger.Info("Receipt not found in storage, generating from transaction data",
		zap.String("txHash", normalizedHash),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
	)

	// Get the transaction to generate receipt
	tx, err := GetTransactionByHash(mainDBClient, normalizedHash)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to get transaction for receipt generation",
			zap.Error(err),
			zap.String("txHash", normalizedHash),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
		)
		return nil, fmt.Errorf("failed to get transaction for receipt generation: %w", err)
	}

	// Get the block containing this transaction
	block, err := GetTransactionBlock(mainDBClient, normalizedHash)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to get block for receipt generation",
			zap.Error(err),
			zap.String("txHash", normalizedHash),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
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
		if blockTx.Hash.Hex() == normalizedHash {
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
			zap.String("txHash", normalizedHash),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
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
				zap.String("txHash", normalizedHash),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetReceiptByHash"),
			)
			// Don't fail the operation, just log the error
		}
	}

	mainDBClient.Client.Logger.Logger.Info("Successfully generated and returned receipt",
		zap.String("txHash", normalizedHash),
		zap.Uint64("blockNumber", receipt.BlockNumber),
		zap.Uint64("status", receipt.Status),
		zap.Time(logging.Created_at, time.Now().UTC()),
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

func MakeReceiptRoot(mainDBClient *config.PooledConnection, receipts []*config.Receipt) ([]byte, error) {
	var err error
	var shouldReturnConnection bool = false

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.MakeReceiptRoot"),
		)
	}

	receiptRoot, err := utils.GenerateReceiptRoot(receipts)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to generate receipt root",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.MakeReceiptRoot"),
		)
		return nil, fmt.Errorf("failed to generate receipt root: %w", err)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.MakeReceiptRoot"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}

	mainDBClient.Client.Logger.Logger.Info("Successfully generated receipt root",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, "ImmuDB.log"),
		zap.String(logging.Topic, "ImmuDB_ImmuClient"),
		zap.String(logging.Loki_url, logging.GetLokiURL()),
		zap.String(logging.Function, "DB_OPs.MakeReceiptRoot"),
	)

	return receiptRoot, nil

}

func GetReceiptsofBlock(mainDBClient *config.PooledConnection, blockNumber uint64) ([]*config.Receipt, error) {
	var err error
	var shouldReturnConnection bool = false

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetReceiptsofBlock"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.GetReceiptsofBlock"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}

	// Get Transactions of block and then get receipts for each transaction
	transactions, err := GetTransactionsOfBlock(mainDBClient, blockNumber)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to get transactions of block",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetReceiptsofBlock"),
		)
		return nil, fmt.Errorf("failed to get transactions of block: %w", err)
	}

	receipts := make([]*config.Receipt, len(transactions))
	for i, tx := range transactions {
		receipt, err := GetReceiptByHash(mainDBClient, tx.Hash.Hex())
		if err != nil {
			mainDBClient.Client.Logger.Logger.Error("Failed to get receipt by hash",
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.GetReceiptsofBlock"),
			)
			return nil, fmt.Errorf("failed to get receipt by hash: %w", err)
		}
		receipts[i] = receipt
	}

	mainDBClient.Client.Logger.Logger.Info("Successfully retrieved receipts of block",
		zap.Uint64("blockNumber", blockNumber),
		zap.Int("receiptCount", len(receipts)),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, "ImmuDB.log"),
		zap.String(logging.Topic, "ImmuDB_ImmuClient"),
		zap.String(logging.Loki_url, logging.GetLokiURL()),
		zap.String(logging.Function, "DB_OPs.GetReceiptsofBlock"),
	)
	return receipts, nil
}
