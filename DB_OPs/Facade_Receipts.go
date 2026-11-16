package DB_OPs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gossipnode/DB_OPs/store"
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

		// cumulativeGasUsed needs every EARLIER transaction's real gas, so the
		// block's receipts are resolved once, up to this index and no further.
		crs := blockReceipts(mainDBClient, block, txIndex)
		cumulative := cumulativeGasUpTo(gasUsedFrom(block, crs), txIndex)
		receipt := generateReceiptFromTransaction(tx, block, txIndex, cumulative, receiptAt(crs, txIndex))

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

// blockReceipts resolves the persisted EVM receipt for blk.Transactions[0..upTo].
// Entry i is nil when that transaction has none.
//
// WHY ONE PASS, AND WHY BOUNDED. cumulativeGasUsed is a prefix sum, so the
// receipt at index i needs every EARLIER transaction's real gas. Resolving
// those inside generateReceiptFromTransaction would cost i keyed reads per
// receipt and N²/2 for a whole block, undoing the single block read this file
// exists to introduce. Resolving the WHOLE block for a single receipt request
// is the opposite waste: entries above upTo are never summed, so an
// unauthenticated eth_getTransactionReceipt for the first transaction of a
// busy block would do work proportional to the whole block. upTo bounds it.
//
// The resolved receipts are handed to generateReceiptFromTransaction so a
// transaction's own gasUsed and the cumulative total come from the SAME read:
// a second, independently classified lookup is how they fell out of step.
//
// Best effort: a handle or read failure leaves a nil entry and the caller falls
// back to the reconstruction rather than failing the RPC.
func blockReceipts(mainDBClient *config.PooledConnection, blk *config.ZKBlock, upTo uint64) []*store.ContractReceipt {
	h, herr := getHandle(mainDBClient)
	if herr != nil {
		return resolveReceipts(blk, upTo, nil)
	}
	return resolveReceipts(blk, upTo, func(txHash string) *store.ContractReceipt {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		cr, cerr := h.GetContractReceipt(rctx, txHash)
		if cerr != nil || cr == nil || !cr.Found {
			return nil
		}
		return cr
	})
}

// resolveReceipts is blockReceipts with the lookup injected, so the bounds and
// the fallbacks are testable without a database. lookup may be nil, meaning no
// receipts are available.
//
// Every transaction is looked up. There is no "this one cannot have a receipt"
// shortcut: apply decides that by whether the recipient HOLDS CODE
// (SmartContract/evmexec/executor.go IsContractTx), not by calldata, so a
// no-calldata value transfer into a contract's receive() has a real receipt
// with real gas. Skipping it put the 21,000 floor in the cumulative sum while
// the receipt itself reported the true gas — the very inversion this code is
// here to remove. A transaction that genuinely has none costs one keyed miss.
func resolveReceipts(blk *config.ZKBlock, upTo uint64, lookup func(txHash string) *store.ContractReceipt) []*store.ContractReceipt {
	n := uint64(len(blk.Transactions))
	if upTo >= n {
		if n == 0 {
			return nil
		}
		upTo = n - 1
	}
	out := make([]*store.ContractReceipt, upTo+1)
	if lookup == nil {
		return out
	}
	for i := uint64(0); i <= upTo; i++ {
		out[i] = lookup(blk.Transactions[i].Hash.Hex())
	}
	return out
}

// gasUsedFrom returns the gas actually used by each transaction covered by crs:
// the persisted receipt's value where there is one, the intrinsic
// reconstruction otherwise.
func gasUsedFrom(blk *config.ZKBlock, crs []*store.ContractReceipt) []uint64 {
	used := make([]uint64, len(crs))
	for i := range crs {
		if crs[i] != nil {
			used[i] = crs[i].GasUsed
			continue
		}
		used[i] = reconstructedGasUsed(&blk.Transactions[i])
	}
	return used
}

// receiptAt returns crs[i] or nil when i is beyond what was resolved.
func receiptAt(crs []*store.ContractReceipt, i uint64) *store.ContractReceipt {
	if i >= uint64(len(crs)) {
		return nil
	}
	return crs[i]
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
// The connection is gone from this signature on purpose: the function no longer
// reads anything. Its one read is now the caller's, shared across the block.
func generateReceiptFromTransaction(tx *config.Transaction, block *config.ZKBlock, txIndex uint64, cumulativeGasUsed uint64, cr *store.ContractReceipt) *config.Receipt {

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
	// ACTUAL revert status, true gas, contract address, and logs. It is resolved by
	// the caller, in the same pass that built cumulativeGasUsed, so this
	// transaction's gasUsed and the running total can never disagree. nil means
	// none was found and the reconstruction above stands.
	if cr != nil {
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
	if len(blk.Transactions) == 0 {
		return []*config.Receipt{}
	}
	receipts := make([]*config.Receipt, len(blk.Transactions))
	crs := blockReceipts(mainDBClient, blk, uint64(len(blk.Transactions)-1))
	used := gasUsedFrom(blk, crs)
	var cumulative uint64
	for i := range blk.Transactions {
		cumulative += used[i]
		receipts[i] = generateReceiptFromTransaction(&blk.Transactions[i], blk, uint64(i), cumulative, receiptAt(crs, uint64(i)))
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
