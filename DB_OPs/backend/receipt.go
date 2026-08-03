package backend

// MODULE: DB_OPs/backend/receipt.go
// PURPOSE: Implement store.ReceiptStore — generate receipts on-the-fly from tx+block via ThebeReader.
// CORE DATA STRUCTURES: config.Receipt built from TransactionRecord + BlockRecord; no persistent state.
// TO MODIFY BEHAVIOR: edit generateReceiptFromRecords for different gas/log computation
// DO NOT: import ImmuDB, PooledConnection, or dualdb packages
// EXTENSION POINT: persist receipts → implement a ReceiptWriter and inject via New()

import (
	"context"
	"fmt"
	"strings"

	"gossipnode/config"
	"gossipnode/config/utils"

	"github.com/ethereum/go-ethereum/common"
)

// GetReceipt generates a receipt on-the-fly by fetching the transaction and its containing block.
// Mirrors DB_OPs.GetReceiptByHash logic using ThebeReader instead of ImmuDB connections.
// Time: O(n) where n = number of transactions in the block (for cumulative gas computation).
func (b *thebeBackend) GetReceipt(ctx context.Context, txHash string) (*config.Receipt, error) {
	// Normalize hash — ensure 0x prefix
	normalizedHash := txHash
	if !strings.HasPrefix(strings.ToLower(txHash), "0x") {
		normalizedHash = "0x" + txHash
	}

	// Fetch transaction
	txRec, err := b.r.GetTransaction(ctx, normalizedHash)
	if err != nil {
		// Check contract receipt table for known-failed tx (status=0)
		cRec, cErr := b.r.GetContractReceipt(ctx, normalizedHash)
		if cErr == nil && cRec != nil && cRec.Status == 0 {
			return nil, nil // failed tx → null receipt (mirrors Facade_Receipts.go pattern)
		}
		return nil, fmt.Errorf("backend.GetReceipt(%s): transaction not found: %w", txHash, err)
	}

	// Fetch the containing block
	blockRec, err := b.r.GetBlock(ctx, txRec.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetReceipt(%s): get block %d: %w", txHash, txRec.BlockNumber, err)
	}

	// Fetch all transactions in block for cumulative gas calculation
	blockTxs, err := b.r.GetTransactionsByBlock(ctx, txRec.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetReceipt(%s): get block txs: %w", txHash, err)
	}

	// Compute cumulative gas used up to and including this transaction.
	// Uses GasLimit as proxy (mirrors Facade_Receipts.go — actual gas_used not stored separately).
	// Time: O(txIndex) — bounded by block tx count.
	var cumulativeGasUsed uint64
	txIndexInt := int(txRec.TxIndex)
	for i := 0; i <= txIndexInt && i < len(blockTxs); i++ {
		// GasLimit stored as string in TransactionRecord
		// parseUint64OrZero is a local helper to avoid panicking on empty/invalid values
		cumulativeGasUsed += parseUint64OrZero(blockTxs[i].GasLimit)
	}

	// Build minimal log entry mirroring Facade_Receipts.go pattern
	logs := []config.Log{}
	if txRec.FromAddr != "" {
		log := config.Log{
			BlockNumber: blockRec.BlockNumber,
			BlockHash:   common.HexToHash(blockRec.BlockHash),
			TxHash:      common.HexToHash(txRec.TxHash),
			TxIndex:     uint64(txRec.TxIndex),
			LogIndex:    uint64(txRec.TxIndex),
			Data:        []byte{0},
			Topics:      []common.Hash{},
			Removed:     false,
			Address:     common.HexToAddress(txRec.FromAddr),
		}
		logs = append(logs, log)
	}

	logsBloom := utils.GenerateLogsBloom(logs)

	return &config.Receipt{
		TxHash:            common.HexToHash(txRec.TxHash),
		BlockHash:         common.HexToHash(blockRec.BlockHash),
		BlockNumber:       blockRec.BlockNumber,
		TransactionIndex:  uint64(txRec.TxIndex),
		Status:            1,
		Type:              uint8(txRec.Type),
		GasUsed:           parseUint64OrZero(txRec.GasLimit),
		CumulativeGasUsed: cumulativeGasUsed,
		ContractAddress:   nil,
		Logs:              logs,
		LogsBloom:         logsBloom,
		ZKProof:           nil,
		ZKStatus:          "",
	}, nil
}

// parseUint64OrZero parses a decimal string to uint64; returns 0 on any error.
func parseUint64OrZero(s string) uint64 {
	if s == "" {
		return 0
	}
	var n uint64
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
