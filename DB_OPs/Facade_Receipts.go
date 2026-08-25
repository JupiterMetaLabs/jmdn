package DB_OPs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gossipnode/config"
	"gossipnode/config/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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

// reconstructedGasUsed returns the best gas-used value available WITHOUT the
// persisted EVM receipt. A plain value transfer (recipient present, no calldata)
// costs exactly the 21000 intrinsic and always succeeds once included, so it is
// EXACT. A contract create/call executes additional opcode gas that is only in
// the persisted EVM receipt — so we return the EIP-2028 intrinsic FLOOR
// (21000 + calldata), never tx.GasLimit (which massively over-reports).
//
// TODO(receipt-persistence): the actual GasUsed and Status for contract txs live
// in the per-tx EVM receipt written at apply time (contractDB.WriteReceipt →
// thebegateway.GetContractReceipt). Expose that reader on the store handle and
// read it here so a reverted contract tx reports status 0 and its true gas.
func reconstructedGasUsed(tx *config.Transaction) uint64 {
	gas := uint64(21000) // intrinsic base (EIP-2028, non-creation)
	for _, b := range tx.Data {
		if b == 0 {
			gas += 4
		} else {
			gas += 16
		}
	}
	return gas
}

// generateReceiptFromTransaction creates a receipt from transaction and block data.
func generateReceiptFromTransaction(mainDBClient *config.PooledConnection, tx *config.Transaction, block *config.ZKBlock, txIndex uint64) *config.Receipt {
	// Cumulative gas = sum of per-tx reconstructed gas up to and including this one
	// (NOT GasLimit).
	var cumulativeGasUsed uint64
	for i := uint64(0); i <= txIndex; i++ {
		if i < uint64(len(block.Transactions)) {
			cumulativeGasUsed += reconstructedGasUsed(&block.Transactions[i])
		}
	}

	// Plain ETH transfers and most non-contract calls emit no logs.
	// Do not fabricate synthetic log entries — return an empty slice so
	// dApps that parse receipt logs see correct data.
	logs := []config.Log{}
	logsBloom := utils.GenerateLogsBloom(logs)

	gasUsed := reconstructedGasUsed(tx)

	// Contract-creation address is DETERMINISTIC: crypto.CreateAddress(sender, nonce),
	// the same address the EVM and EnrichBlockAccountNonces derive. Set it for a
	// creation tx (To == nil, From != nil); leave nil for calls/transfers.
	var contractAddress *common.Address
	if tx.To == nil && tx.From != nil {
		ca := crypto.CreateAddress(*tx.From, tx.Nonce)
		contractAddress = &ca
	}

	// Status: plain transfers always succeed once included. A reverted contract tx
	// should be status 0, but that outcome is only in the persisted EVM receipt —
	// see the reconstructedGasUsed TODO.
	status := uint64(1)

	receipt := &config.Receipt{
		TxHash:            tx.Hash,
		BlockHash:         block.BlockHash,
		BlockNumber:       block.BlockNumber,
		TransactionIndex:  txIndex,
		Status:            status,
		Type:              tx.Type,
		GasUsed:           gasUsed,
		CumulativeGasUsed: cumulativeGasUsed,
		ContractAddress:   contractAddress,
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
