package DB_OPs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
	"gossipnode/config/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// WriteContractReceipt persists a full contract receipt via the process-wide
// ThebeDB handle's gateway 2PC path (→ SQL contract_receipts) — the same
// synchronous path WriteTransaction uses, so it works with or without a projector.
// Called from the apply path (applyContractTx), NOT the decoupled executor.
func WriteContractReceipt(conn *config.PooledConnection, rec *thebegateway.ContractReceiptRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("WriteContractReceipt: %w", err)
	}
	return h.WriteContractReceipt(ctx, rec)
}

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

// reconstructedGasUsed is the FALLBACK gas-used value used only when a tx has no
// persisted EVM receipt (a plain value transfer). generateReceiptFromTransaction
// prefers the actual persisted GasUsed via the handle's GetContractReceipt.
//
// A plain value transfer (recipient present, no calldata) costs exactly 21000 and
// always succeeds once included, so it is EXACT. For a contract create/call this
// is only the intrinsic FLOOR (never tx.GasLimit, which massively over-reports):
//   - creation: 21000 + 32000 (CREATE) = 53000, plus the EIP-3860 initcode word
//     cost (2 gas per 32-byte word);
//   - call: 21000;
//   - plus the EIP-2028 calldata cost (16/non-zero byte, 4/zero byte).
func reconstructedGasUsed(tx *config.Transaction) uint64 {
	var gas uint64
	if tx.To == nil {
		gas = 53000
		words := (uint64(len(tx.Data)) + 31) / 32
		gas += 2 * words // EIP-3860 initcode word cost
	} else {
		gas = 21000
	}
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

	// Reconstruction defaults — correct for plain value transfers, overridden below
	// by the persisted EVM receipt for contract txs. No synthetic logs.
	logs := []config.Log{}
	gasUsed := reconstructedGasUsed(tx)
	status := uint64(1)

	// Contract-creation address is DETERMINISTIC: crypto.CreateAddress(sender, nonce),
	// the same address the EVM and EnrichBlockAccountNonces derive. Set it for a
	// creation tx (To == nil, From != nil); leave nil for calls/transfers.
	var contractAddress *common.Address
	if tx.To == nil && tx.From != nil {
		ca := crypto.CreateAddress(*tx.From, tx.Nonce)
		contractAddress = &ca
	}

	// Persisted EVM receipt (written at apply time) is authoritative: it carries the
	// ACTUAL revert status, true gas, contract address, and logs. Override the
	// reconstruction with it when present; a plain transfer has none (Found=false)
	// and keeps the reconstruction above. Best-effort read — a lookup failure falls
	// back to the reconstruction rather than failing the RPC.
	if h, herr := getHandle(mainDBClient); herr == nil {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		if cr, cerr := h.GetContractReceipt(rctx, tx.Hash.Hex()); cerr == nil && cr != nil && cr.Found {
			status = cr.Status
			gasUsed = cr.GasUsed
			if cr.ContractAddress != nil {
				contractAddress = cr.ContractAddress
			}
			if len(cr.Logs) > 0 {
				logs = cr.Logs
			}
		}
		rcancel()
	}

	logsBloom := utils.GenerateLogsBloom(logs)

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

// __DEAD_CODE_AUDIT_PUBLIC__
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
