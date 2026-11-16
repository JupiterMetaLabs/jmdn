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

		// Generate receipt from transaction and block data. cumulativeGasUsed needs
		// every earlier transaction's REAL gas, so it is computed from the block.
		cumulative := cumulativeGasUpTo(blockGasUsed(mainDBClient, block), txIndex)
		receipt := generateReceiptFromTransaction(mainDBClient, tx, block, txIndex, cumulative)

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

// hasPersistedReceipt reports whether a transaction can have a persisted EVM
// receipt. A plain value transfer (recipient present, no calldata) never runs
// EVM code, so apply writes no receipt for it and reconstructedGasUsed is EXACT
// — the lookup would always miss. Skipping those keeps blockGasUsed at one read
// per CONTRACT transaction rather than one per transaction.
func hasPersistedReceipt(tx *config.Transaction) bool {
	return tx.To == nil || len(tx.Data) > 0
}

// blockGasUsed returns the gas actually used by every transaction in blk: the
// persisted EVM receipt's value where one exists, the intrinsic reconstruction
// otherwise.
//
// WHY A WHOLE-BLOCK PASS. cumulativeGasUsed is a prefix sum, so the receipt at
// index i needs every EARLIER transaction's real gas, not an estimate of it.
// Reading those inside generateReceiptFromTransaction would cost i keyed reads
// per receipt and N²/2 for a block — undoing the single-block read this file
// just gained. One pass per block is at most one read per contract transaction,
// shared by every receipt built from that block.
//
// Best effort, like the per-receipt lookup: a handle or read failure leaves the
// reconstruction in place rather than failing the RPC.
func blockGasUsed(mainDBClient *config.PooledConnection, blk *config.ZKBlock) []uint64 {
	h, herr := getHandle(mainDBClient)
	if herr != nil {
		// No handle: every entry is the reconstruction, which is what the whole
		// block would fall back to anyway.
		return gasUsedOfBlock(blk, nil)
	}
	return gasUsedOfBlock(blk, func(txHash string) (uint64, bool) {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		cr, cerr := h.GetContractReceipt(rctx, txHash)
		if cerr != nil || cr == nil || !cr.Found {
			return 0, false
		}
		return cr.GasUsed, true
	})
}

// gasUsedOfBlock is blockGasUsed with the receipt lookup injected, so the
// prefix-sum behaviour can be tested without a database. realGas may be nil,
// meaning "no receipts available"; it is called only for transactions that can
// have one.
func gasUsedOfBlock(blk *config.ZKBlock, realGas func(txHash string) (uint64, bool)) []uint64 {
	used := make([]uint64, len(blk.Transactions))
	for i := range blk.Transactions {
		tx := &blk.Transactions[i]
		used[i] = reconstructedGasUsed(tx)
		if realGas == nil || !hasPersistedReceipt(tx) {
			continue
		}
		if g, ok := realGas(tx.Hash.Hex()); ok {
			used[i] = g
		}
	}
	return used
}

// cumulativeGasUpTo sums used[0..idx] — the prefix sum a receipt reports as
// cumulativeGasUsed.
func cumulativeGasUpTo(used []uint64, idx uint64) uint64 {
	var total uint64
	for i := uint64(0); i <= idx && i < uint64(len(used)); i++ {
		total += used[i]
	}
	return total
}

// generateReceiptFromTransaction creates a receipt from transaction and block data.
//
// cumulativeGasUsed is supplied by the caller because it is a property of the
// BLOCK, not of this transaction: see blockGasUsed for why computing it here
// would be quadratic.
func generateReceiptFromTransaction(mainDBClient *config.PooledConnection, tx *config.Transaction, block *config.ZKBlock, txIndex uint64, cumulativeGasUsed uint64) *config.Receipt {

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
				// Receipts persisted before the apply path stamped block/tx context
				// carry zeroed blockHash/txHash/logIndex. Fill them here so old
				// blocks read the same as new ones; logIndex falls back to the
				// receipt-local position (block-wide index is unknown for them).
				for i := range logs {
					if logs[i].TxHash == (common.Hash{}) {
						logs[i].TxHash = tx.Hash
						logs[i].BlockHash = block.BlockHash
						logs[i].BlockNumber = block.BlockNumber
						logs[i].TxIndex = txIndex
						logs[i].LogIndex = uint64(i)
					}
				}
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

// ReceiptsOfStoredBlock builds every receipt in a block from a block the caller
// ALREADY HOLDS, reading nothing.
//
// blk must be a full block (GetZKBlockByNumber / GetZKBlockByHash /
// ReadZKBlockByNumber), not a header-only read: receipts come from
// blk.Transactions, so a header-only block yields none.
//
// The index is each transaction's position in the block, which is exactly what
// the old per-hash path searched the block to recover, and every receipt shares
// the block's one proof slice instead of owning a copy.
//
// The per-transaction GetContractReceipt lookup inside
// generateReceiptFromTransaction is unchanged: it is a small keyed read, and
// batching it needs a new handle method.
func ReceiptsOfStoredBlock(mainDBClient *config.PooledConnection, blk *config.ZKBlock) []*config.Receipt {
	if blk == nil {
		return nil
	}
	receipts := make([]*config.Receipt, len(blk.Transactions))
	used := blockGasUsed(mainDBClient, blk)
	var cumulative uint64
	for i := range blk.Transactions {
		cumulative += used[i]
		receipts[i] = generateReceiptFromTransaction(mainDBClient, &blk.Transactions[i], blk, uint64(i), cumulative)
	}
	return receipts
}

// GetReceiptsofBlock returns every receipt in a block, reading the block ONCE.
//
// It used to call GetTransactionsOfBlock (itself a full GetZKBlockByNumber) and
// then GetReceiptByHash per transaction — and GetReceiptByHash resolves its own
// block through GetTransactionBlock, which is another full GetZKBlockByNumber.
// A block with N transactions therefore performed N+1 whole-block reads.
//
// The cost that matters is not the N+1 count: a stored block carries its STARK
// proof (config.ZKBlock.StarkProof, the zk_proofs row), so every one of those
// reads pulled a multi-megabyte proof out of storage and into a fresh
// allocation. eth_getBlockByNumber / eth_getBlockByHash reach here through
// utils.ConvertZKBlockToBlock to rebuild the receipts root and logs bloom, so a
// single RPC call on a large block moved N copies of the same proof. There is
// no cap on transactions per block, so N is unbounded.
//
// A caller that already holds the block should use ReceiptsOfStoredBlock and
// read nothing at all.
func GetReceiptsofBlock(mainDBClient *config.PooledConnection, blockNumber uint64) ([]*config.Receipt, error) {
	block, err := GetZKBlockByNumber(mainDBClient, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d for receipts: %w", blockNumber, err)
	}
	return ReceiptsOfStoredBlock(mainDBClient, block), nil
}
