package DB_OPs

import (
	"context"
	"fmt"
	"gossipnode/config"
	"gossipnode/config/utils"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// GetReceiptByHash retrieves a transaction receipt by its hash
func GetReceiptByHash(mainDBClient *config.PooledConnection, hash string) (*config.Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

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

		return receipt, nil
	}

	// Transaction not found - SECOND: Check KV for in-flight processing flag ("-1" sentinel).
	// SetTxProcessing writes this flag when a tx enters the mempool/processing queue.
	// If the flag is present, the tx is still being processed → return null receipt (not an error).
	if h, hErr := getHandle(mainDBClient); hErr == nil {
		if processing, _ := h.IsTxProcessing(ctx, normalizedHash); processing {
			return nil, nil
		}
	}

	// THIRD: Transaction not found and tx_processing is not -1 (or doesn't exist)

	// Return error that will be formatted as "transaction not found" in JSON-RPC
	return nil, fmt.Errorf("transaction not found")
}

// generateReceiptFromTransaction creates a receipt from transaction and block data
func generateReceiptFromTransaction(mainDBClient *config.PooledConnection, tx *config.Transaction, block *config.ZKBlock, txIndex uint64) *config.Receipt {
	// Calculate cumulative gas used up to this transaction using actual gas consumption
	var cumulativeGasUsed uint64 = 0
	for i := uint64(0); i <= txIndex; i++ {
		if i < uint64(len(block.Transactions)) {
			// Use actual gas consumption if available, otherwise fall back to gas limit
			gasUsed := block.Transactions[i].GasLimit
			cumulativeGasUsed += gasUsed
		}
	}

	// Generate logs directly when function is called
	// Logs are generated with metadata populated from block/transaction data
	logs := []config.Log{}

	// Create a log entry with all required fields populated
	if tx.From != nil {
		log := config.Log{
			BlockNumber: block.BlockNumber,
			BlockHash:   block.BlockHash,
			TxHash:      tx.Hash,
			TxIndex:     txIndex,
			LogIndex:    txIndex,
			Data:        []byte{0},
			Topics:      []common.Hash{},
			Removed:     false,
			Address:     *tx.From,
		}
		logs = append(logs, log)
	}

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
	receiptRoot, err := utils.GenerateReceiptRoot(receipts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate receipt root: %w", err)
	}
	return receiptRoot, nil
}

func GetReceiptsofBlock(mainDBClient *config.PooledConnection, blockNumber uint64) ([]*config.Receipt, error) {
	// Get Transactions of block and then get receipts for each transaction
	transactions, err := GetTransactionsOfBlock(mainDBClient, blockNumber)
	if err != nil {

		return nil, fmt.Errorf("failed to get transactions of block: %w", err)
	}

	receipts := make([]*config.Receipt, len(transactions))
	for i, tx := range transactions {
		receipt, err := GetReceiptByHash(mainDBClient, tx.Hash.Hex())
		if err != nil {

			return nil, fmt.Errorf("failed to get receipt by hash: %w", err)
		}
		receipts[i] = receipt
	}

	return receipts, nil
}
