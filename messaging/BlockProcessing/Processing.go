package BlockProcessing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"go.opentelemetry.io/otel/attribute"
)

const (
	LOG_FILE = ""
	TOPIC    = "BlockProcessing"
)

// Global map to track processed transactions during block processing
var (
	processedTxs      = make(map[string]bool)
	processedTxsMutex sync.Mutex
	txProcessingLocks = make(map[string]*sync.Mutex)
	txLocksGuard      = sync.Mutex{}

	// Configurable defaults that can be adjusted as needed
	DefaultGasLimit       = int64(21000)
	DefaultGasPrice       = int64(1000000000) // 1 Gwei
	CreateMissingAccounts = true              // Set to false to disable automatic DID creation
)

// ClearProcessedTransactions clears the processed transactions map
// This should be called at the start of processing a new block
func ClearProcessedTransactions() {
	processedTxsMutex.Lock()
	defer processedTxsMutex.Unlock()
	processedTxs = make(map[string]bool)
}

// getTransactionLock gets or creates a mutex for a specific transaction
func getTransactionLock(txHash string) *sync.Mutex {
	txLocksGuard.Lock()
	defer txLocksGuard.Unlock()

	if _, exists := txProcessingLocks[txHash]; !exists {
		txProcessingLocks[txHash] = &sync.Mutex{}
	}
	return txProcessingLocks[txHash]
}

// cleanupTransactionLock removes a transaction lock when no longer needed
func cleanupTransactionLock(txHash string) {
	txLocksGuard.Lock()
	defer txLocksGuard.Unlock()

	delete(txProcessingLocks, txHash)
}

// ProcessBlockTransactions processes all transactions in a block atomically
// If any transaction fails, all are rolled back
func ProcessBlockTransactions(logger_ctx context.Context, block *config.ZKBlock, accountsClient *config.PooledConnection) error {
	// Record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("BlockProcessing").Start(logger_ctx, "BlockProcessing.ProcessBlockTransactions")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.Int64("block_number", int64(block.BlockNumber)),
		attribute.String("block_hash", block.BlockHash.Hex()),
		attribute.Int("transaction_count", len(block.Transactions)),
	)

	// Check if block was already processed
	blockKey := fmt.Sprintf("block_processed:%s", block.BlockHash.Hex())
	processed, err := DB_OPs.Exists(accountsClient, blockKey)
	if err == nil && processed {
		span.SetAttributes(attribute.String("status", "already_processed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Info(span_ctx, "Block already processed, skipping",
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Int64("block_number", int64(block.BlockNumber)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
		)
		return nil
	}

	ClearProcessedTransactions()

	// Store original balances to enable rollback - CRITICAL for atomicity
	originalBalances := make(map[common.Address]string)
	affectedAccounts := make(map[common.Address]bool)

	// First, collect all affected DIDs from the block
	for _, tx := range block.Transactions {
		affectedAccounts[*tx.From] = true
		affectedAccounts[*tx.To] = true
	}
	affectedAccounts[*block.CoinbaseAddr] = true
	affectedAccounts[*block.ZKVMAddr] = true

	span.SetAttributes(attribute.Int("affected_accounts", len(affectedAccounts)))

	// Fetch and store original balances BEFORE any processing
	for accounts := range affectedAccounts {
		doc, err := DB_OPs.GetAccount(accountsClient, accounts)
		if err == nil {
			originalBalances[accounts] = doc.Balance
		} else {
			// DID doesn't exist yet, so original balance is 0
			originalBalances[accounts] = "0"
		}
	}

	// Sort transactions by nonce if available to ensure proper ordering
	sortedTxs := sortTransactionsByNonce(block.Transactions)
	span.SetAttributes(attribute.Int("sorted_transactions", len(sortedTxs)))

	logger().NamedLogger.Info(span_ctx, "Starting block processing",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.Int("transaction_count", len(sortedTxs)),
		ion.Int("affected_accounts", len(affectedAccounts)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
	)

	// Track successfully processed transactions for atomic commit
	successfullyProcessedTxs := make([]string, 0, len(sortedTxs))

	// Process all transactions - if ANY fails, rollback ALL
	for i, tx := range sortedTxs {
		// Check if this transaction was already processed within this block
		processedTxsMutex.Lock()
		if processedTxs[tx.Hash.Hex()] {
			logger().NamedLogger.Warn(span_ctx, "Duplicate transaction in block, skipping",
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.Int("tx_index", i),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)
			processedTxsMutex.Unlock()
			continue
		}
		processedTxs[tx.Hash.Hex()] = true
		processedTxsMutex.Unlock()

		// Check if this transaction was already processed in a previous block
		txKey := fmt.Sprintf("tx_processed:%s", tx.Hash)
		alreadyProcessed, err := DB_OPs.Exists(accountsClient, txKey)
		if err == nil && alreadyProcessed {
			logger().NamedLogger.Warn(span_ctx, "Transaction already processed in previous block, skipping",
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.Int("tx_index", i),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)
			continue
		}

		// Process the transaction with span context
		Process_err := processTransaction(span_ctx, tx, *block.CoinbaseAddr, *block.ZKVMAddr, accountsClient)
		if Process_err != nil {
			// ATOMICITY: If any transaction fails, roll back ALL affected accounts
			span.RecordError(Process_err)
			span.SetAttributes(attribute.String("status", "failed"), attribute.String("failed_tx_hash", tx.Hash.Hex()), attribute.Int("failed_tx_index", i))

			logger().NamedLogger.Error(span_ctx, "Transaction failed, rolling back entire block",
				Process_err,
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.Int("tx_index", i),
				ion.Int("total_transactions", len(sortedTxs)),
				ion.Int("successful_before_failure", len(successfullyProcessedTxs)),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)

			// Rollback all balances to original state
			rollbackError := rollbackBalances(span_ctx, originalBalances, accountsClient)
			if rollbackError != nil {
				span.RecordError(rollbackError)
				logger().NamedLogger.Error(span_ctx, "Failed to rollback balances after transaction failure",
					rollbackError,
					ion.String("tx_hash", tx.Hash.Hex()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
				)
				// Still return the original error as it's more critical
			}

			// Clean up processing markers for all transactions processed so far
			for _, txHash := range successfullyProcessedTxs {
				cleanupProcessingMarkers(span_ctx, accountsClient, txHash)
			}
			cleanupProcessingMarkers(span_ctx, accountsClient, tx.Hash.Hex())

			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return fmt.Errorf("block processing failed at transaction %d/%d (hash: %s): %w", i+1, len(sortedTxs), tx.Hash.Hex(), Process_err)
		}

		// Track successfully processed transaction
		successfullyProcessedTxs = append(successfullyProcessedTxs, tx.Hash.Hex())
	}

	// ATOMICITY: Use Immudb's atomic transaction to mark all operations at once
	// This reduces N database calls to 1 atomic transaction, improving performance
	// If any operation fails, Immudb automatically rolls back the entire transaction
	if len(successfullyProcessedTxs) > 0 {
		// Use Immudb's atomic transaction API to batch all marking operations
		err := DB_OPs.Transaction(accountsClient.Client, func(tx *config.ImmuTransaction) error {
			// Mark all successfully processed transactions
			for _, txHash := range successfullyProcessedTxs {
				txKey := fmt.Sprintf("tx_processed:%s", txHash)
				if err := DB_OPs.Set(tx, txKey, time.Now().UTC().Unix()); err != nil {
					return fmt.Errorf("failed to add transaction marker for %s: %w", txHash, err)
				}

				// Clean up processing markers (set to -1 to mark as cleaned)
				processingKey := fmt.Sprintf("tx_processing:%s", txHash)
				if err := DB_OPs.Set(tx, processingKey, int64(-1)); err != nil {
					return fmt.Errorf("failed to add cleanup marker for %s: %w", txHash, err)
				}
			}

			// Mark the block as processed - this is the final operation in the transaction
			if err := DB_OPs.Set(tx, blockKey, time.Now().UTC().Unix()); err != nil {
				return fmt.Errorf("failed to add block marker: %w", err)
			}

			return nil
		})

		if err != nil {
			// Transaction failed - Immudb automatically rolled back all operations
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "atomic_marking_failed"))
			logger().NamedLogger.Error(span_ctx, "Failed to atomically mark transactions and block, rolling back balances",
				err,
				ion.Int("transaction_count", len(successfullyProcessedTxs)),
				ion.String("block_hash", block.BlockHash.Hex()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)
			// Rollback balances since transaction marking failed
			rollbackBalances(span_ctx, originalBalances, accountsClient)
			// Clean up processing markers (they weren't committed due to transaction failure)
			for _, txHash := range successfullyProcessedTxs {
				cleanupProcessingMarkers(span_ctx, accountsClient, txHash)
			}
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return fmt.Errorf("failed to atomically mark transactions and block: %w", err)
		}

		span.SetAttributes(attribute.Int("atomically_marked_transactions", len(successfullyProcessedTxs)))
		logger().NamedLogger.Info(span_ctx, "Atomically marked all transactions and block as processed",
			ion.Int("transaction_count", len(successfullyProcessedTxs)),
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Int64("block_number", int64(block.BlockNumber)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
		)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
		attribute.Int("processed_transactions", len(successfullyProcessedTxs)),
	)
	logger().NamedLogger.Info(span_ctx, "Block processed successfully",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.Int("processed_transactions", len(successfullyProcessedTxs)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
	)

	return nil
}

// sortTransactionsByNonce sorts transactions by their nonce value if available
func sortTransactionsByNonce(txs []config.Transaction) []config.Transaction {
	// Create a copy to avoid modifying the original
	sortedTxs := make([]config.Transaction, len(txs))
	copy(sortedTxs, txs)

	// Group transactions by sender address
	txsBySender := make(map[common.Address][]config.Transaction)
	for _, tx := range sortedTxs {
		txsBySender[*tx.From] = append(txsBySender[*tx.From], tx)
	}

	// Sort each sender's transactions by nonce
	for sender, senderTxs := range txsBySender {
		sort.Slice(senderTxs, func(i, j int) bool {
			// If nonce is missing, maintain original order
			if senderTxs[i].Nonce == 0 || senderTxs[j].Nonce == 0 {
				return i < j
			}

			// Compare nonces directly as uint64
			return senderTxs[i].Nonce < senderTxs[j].Nonce
		})

		txsBySender[sender] = senderTxs
	}

	// Rebuild the sorted transaction list
	result := []config.Transaction{}
	for _, senderTxs := range txsBySender {
		result = append(result, senderTxs...)
	}

	return result
}

// cleanupProcessingMarkers removes temporary processing markers
func cleanupProcessingMarkers(span_ctx context.Context, accountsClient *config.PooledConnection, txHash string) {
	processingKey := fmt.Sprintf("tx_processing:%s", txHash)
	if exists, _ := DB_OPs.Exists(accountsClient, processingKey); exists {
		if err := DB_OPs.Create(accountsClient, processingKey, int64(-1)); err != nil {
			logger().NamedLogger.Warn(span_ctx, "Failed to clean up processing marker",
				ion.String("tx_hash", txHash),
				ion.String("error", err.Error()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.cleanupProcessingMarkers"),
			)
		}
	}

	// Also clean up the transaction lock
	cleanupTransactionLock(txHash)
}

// rollbackBalances restores original balances for all affected DIDs
func rollbackBalances(span_ctx context.Context, originalBalances map[common.Address]string, accountsClient *config.PooledConnection) error {
	rollbackSpanCtx, rollbackSpan := logger().NamedLogger.Tracer("BlockProcessing").Start(span_ctx, "BlockProcessing.rollbackBalances")
	defer rollbackSpan.End()

	rollbackStartTime := time.Now().UTC()
	rollbackSpan.SetAttributes(attribute.Int("accounts_to_rollback", len(originalBalances)))

	rollbackCount := 0
	for did, balance := range originalBalances {
		if err := DB_OPs.UpdateAccountBalance(accountsClient, did, balance); err != nil {
			rollbackSpan.RecordError(err)
			rollbackSpan.SetAttributes(attribute.String("status", "partial_failure"), attribute.String("failed_account", did.Hex()))
			logger().NamedLogger.Error(rollbackSpanCtx, "Failed to restore balance during rollback",
				err,
				ion.String("account", did.Hex()),
				ion.String("original_balance", balance),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.rollbackBalances"),
			)
			return fmt.Errorf("failed to restore balance for %s: %w", did, err)
		}
		rollbackCount++
		logger().NamedLogger.Debug(rollbackSpanCtx, "Rolled back balance to original value",
			ion.String("account", did.Hex()),
			ion.String("balance", balance),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.rollbackBalances"),
		)
	}

	duration := time.Since(rollbackStartTime).Seconds()
	rollbackSpan.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
		attribute.Int("rolled_back_accounts", rollbackCount),
	)
	logger().NamedLogger.Info(rollbackSpanCtx, "Rollback completed successfully",
		ion.Int("rolled_back_accounts", rollbackCount),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.rollbackBalances"),
	)

	return nil
}

// ProcessTransaction handles a single transaction's balance updates
func processTransaction(span_ctx context.Context, tx config.Transaction, coinbaseAddr common.Address, zkvmAddr common.Address, accountsClient *config.PooledConnection) error {
	// Record trace span and close it
	txSpanCtx, txSpan := logger().NamedLogger.Tracer("BlockProcessing").Start(span_ctx, "BlockProcessing.processTransaction")
	defer txSpan.End()

	txStartTime := time.Now().UTC()
	txSpan.SetAttributes(
		attribute.String("tx_hash", tx.Hash.Hex()),
		attribute.String("from", tx.From.Hex()),
		attribute.String("to", tx.To.Hex()),
		attribute.String("coinbase", coinbaseAddr.Hex()),
		attribute.String("zkvm", zkvmAddr.Hex()),
	)

	// First check the connection
	if accountsClient == nil {
		txSpan.RecordError(errors.New("accountsClient is nil"))
		txSpan.SetAttributes(attribute.String("status", "error"))
		return errors.New("accountsClient is nil")
	}

	// Confirm the DB connection
	err := DB_OPs.EnsureDBConnection(accountsClient)
	if err != nil {
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "db_connection_failed"))
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Failed to establish database connection",
			err,
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("failed to establish database connection: %w", err)
	}

	logger().NamedLogger.Debug(txSpanCtx, "Database connection check successful",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.processTransaction"),
	)

	// Check if transaction was already processed (from previous blocks)
	txLock := getTransactionLock(tx.Hash.String())
	txLock.Lock()
	defer func() {
		txLock.Unlock()
		cleanupTransactionLock(tx.Hash.String()) // Always clean up the lock
	}()

	// First check with a preliminary key that shows we've started processing
	txProcessingKey := fmt.Sprintf("tx_processing:%s", tx.Hash)
	txKey := fmt.Sprintf("tx_processed:%s", tx.Hash)

	// Check if already completed
	processed, err := DB_OPs.Exists(accountsClient, txKey)
	if err == nil && processed {
		txSpan.SetAttributes(attribute.String("status", "already_processed"))
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Info(txSpanCtx, "Transaction already processed in previous block, skipping",
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return nil
	}

	// Check if we're currently processing this transaction
	processing, err := DB_OPs.Exists(accountsClient, txProcessingKey)
	if err == nil && processing {
		// Get the timestamp to check if this is a stale marker
		valueBytes, getErr := DB_OPs.Read(accountsClient, txProcessingKey)
		if getErr == nil {
			// If processing marker is older than 5 minutes, consider it stale
			var timestamp int64
			if err := json.Unmarshal(valueBytes, &timestamp); err == nil {
				if time.Now().UTC().Unix()-timestamp > 300 {
					txSpan.SetAttributes(attribute.String("processing_marker_status", "stale"), attribute.Int64("stale_timestamp", timestamp))
					logger().NamedLogger.Warn(txSpanCtx, "Found stale processing marker, continuing with transaction",
						ion.String("tx_hash", tx.Hash.Hex()),
						ion.Int64("stale_timestamp", timestamp),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("topic", TOPIC),
						ion.String("function", "BlockProcessing.processTransaction"),
					)
				} else {
					txSpan.SetAttributes(attribute.String("processing_marker_status", "active"))
					logger().NamedLogger.Warn(txSpanCtx, "Transaction is already being processed, possible duplicate",
						ion.String("tx_hash", tx.Hash.Hex()),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("topic", TOPIC),
						ion.String("function", "BlockProcessing.processTransaction"),
					)
					// We have the lock, so continue processing anyway as previous attempt might have failed
				}
			}
		}
	}

	// Mark transaction as being processed
	if err := DB_OPs.Create(accountsClient, txProcessingKey, time.Now().UTC().Unix()); err != nil {
		logger().NamedLogger.Warn(txSpanCtx, "Failed to mark transaction as processing",
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("error", err.Error()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		// Continue processing since this is just a precaution
	}

	// Store original balances for rollback if needed
	originalBalances := make(map[common.Address]string)
	affectedDIDs := []common.Address{*tx.From, *tx.To, coinbaseAddr, zkvmAddr}

	for _, did := range affectedDIDs {
		doc, err := DB_OPs.GetAccount(accountsClient, did)
		if err == nil {
			originalBalances[did] = doc.Balance
		} else if err == DB_OPs.ErrNotFound || strings.Contains(err.Error(), "key not found") {
			originalBalances[did] = "0"
		} else {
			txSpan.RecordError(err)
			txSpan.SetAttributes(attribute.String("status", "balance_retrieval_failed"), attribute.String("failed_account", did.Hex()))
			cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
			duration := time.Since(txStartTime).Seconds()
			txSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(txSpanCtx, "Failed to retrieve original balance",
				err,
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.String("account", did.Hex()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.processTransaction"),
			)
			return fmt.Errorf("failed to retrieve original balance for %s: %w", did.Hex(), err)
		}
	}

	// Parse the transaction values
	var parsedTx *config.ParsedZKTransaction
	parsedTx, err = parseTransaction(tx)
	if err != nil {
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "parse_failed"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Failed to parse transaction",
			err,
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("failed to parse transaction: %w", err)
	}

	// Gas Limit is already a bigInt
	var gasLimit *big.Int
	if tx.GasLimit != 0 {
		gasLimit = big.NewInt(int64(tx.GasLimit))
	} else {
		gasLimit = big.NewInt(DefaultGasLimit)
	}

	// Calculate gas fee (gasLimit * gasPrice / 1,000,000,000)
	gasFeeToDeduct := new(big.Int).Mul(gasLimit, parsedTx.EffectiveGasFee)

	// Transaction value should remain in Wei for balance calculations
	// parsedTx.ValueBig is already in Wei, no conversion needed

	// Calculate total amount to deduct from sender (amount + gas fee)
	totalDeduction := new(big.Int).Add(parsedTx.ValueBig, gasFeeToDeduct)
	// Split the gas fee between coinbase and ZKVM
	// Calculate half and remainder to avoid losing 1 wei in corner cases
	halfGasFee := new(big.Int).Div(gasFeeToDeduct, big.NewInt(2))
	remainder := new(big.Int).Mod(gasFeeToDeduct, big.NewInt(2))
	// coinbase gets halfGasFee, ZKVM gets halfGasFee + remainder (to account for odd wei)
	zkvmGasFee := new(big.Int).Set(halfGasFee)
	coinbaseGasFee := new(big.Int).Add(halfGasFee, remainder)

	txSpan.SetAttributes(
		attribute.String("value", parsedTx.ValueBig.String()),
		attribute.String("gas_limit", gasLimit.String()),
		attribute.String("gas_fee", gasFeeToDeduct.String()),
		attribute.String("total_deduction", totalDeduction.String()),
		attribute.String("coinbase_gas_fee", coinbaseGasFee.String()),
		attribute.String("zkvm_gas_fee", zkvmGasFee.String()),
	)

	logger().NamedLogger.Info(txSpanCtx, "Transaction Amount Calculated",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.String("from", tx.From.Hex()),
		ion.String("to", tx.To.Hex()),
		ion.String("value", parsedTx.ValueBig.String()),
		ion.String("gas_limit", gasLimit.String()),
		ion.String("gas_fee", gasFeeToDeduct.String()),
		ion.String("total_deduction", totalDeduction.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.processTransaction"),
	)

	// Check if sender exists before attempting deduction
	senderExists, _ := accountExists(tx.From, accountsClient)
	txSpan.SetAttributes(attribute.Bool("sender_exists", senderExists))
	if !senderExists {
		txSpan.RecordError(errors.New("sender DID does not exist"))
		txSpan.SetAttributes(attribute.String("status", "sender_not_found"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Sender DID does not exist",
			errors.New("sender DID does not exist"),
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("from", tx.From.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("sender DID %s does not exist", tx.From)
	}

	// Check if recipient exists (for better error reporting)
	recipientExists, _ := accountExists(tx.To, accountsClient)
	txSpan.SetAttributes(attribute.Bool("recipient_exists", recipientExists))
	if !recipientExists && !CreateMissingAccounts {
		txSpan.RecordError(errors.New("recipient DID does not exist"))
		txSpan.SetAttributes(attribute.String("status", "recipient_not_found"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Recipient DID does not exist",
			errors.New("recipient DID does not exist"),
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("to", tx.To.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("recipient DID %s does not exist and automatic creation is disabled", tx.To)
	}

	// 1. Deduct from sender
	if err := deductFromSender(txSpanCtx, *tx.From, totalDeduction.String(), accountsClient); err != nil {
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "deduction_failed"), attribute.String("failed_step", "deduct_from_sender"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Failed to deduct from sender",
			err,
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("from", tx.From.Hex()),
			ion.String("amount", totalDeduction.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return categorizeDeductionError(err)
	}

	txSpan.SetAttributes(attribute.String("deduction_step", "completed"))

	// 2. Add amount to recipient
	if err := addToRecipient(txSpanCtx, *tx.To, parsedTx.ValueBig.String(), accountsClient); err != nil {
		// Rollback sender deduction on failure
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "recipient_add_failed"), attribute.String("failed_step", "add_to_recipient"))
		if rollbackErr := DB_OPs.UpdateAccountBalance(accountsClient, *tx.From, originalBalances[*tx.From]); rollbackErr != nil {
			txSpan.RecordError(rollbackErr)
			logger().NamedLogger.Error(txSpanCtx, "Failed to rollback sender balance",
				rollbackErr,
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.String("from", tx.From.Hex()),
				ion.String("original_balance", originalBalances[*tx.From]),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.processTransaction"),
			)
		} else {
			logger().NamedLogger.Info(txSpanCtx, "Rolled back sender balance due to recipient update failure",
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.String("from", tx.From.Hex()),
				ion.String("original_balance", originalBalances[*tx.From]),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.processTransaction"),
			)
		}
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to add to recipient: %w", err)
	}

	txSpan.SetAttributes(attribute.String("recipient_add_step", "completed"))

	// 3. Split gas fee between coinbase and ZKVM
	if err := addToRecipient(txSpanCtx, coinbaseAddr, coinbaseGasFee.String(), accountsClient); err != nil {
		// Rollback previous operations
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "coinbase_gas_fee_failed"), attribute.String("failed_step", "add_to_coinbase"))
		rollbackAccounts := []common.Address{*tx.From, *tx.To, coinbaseAddr, zkvmAddr}
		for _, accounts := range rollbackAccounts {
			if rollbackErr := DB_OPs.UpdateAccountBalance(accountsClient, accounts, originalBalances[accounts]); rollbackErr != nil {
				txSpan.RecordError(rollbackErr)
				logger().NamedLogger.Error(txSpanCtx, "Failed to rollback balance",
					rollbackErr,
					ion.String("tx_hash", tx.Hash.Hex()),
					ion.String("account", accounts.Hex()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.processTransaction"),
				)
			} else {
				logger().NamedLogger.Info(txSpanCtx, "Rolled back balance due to gas fee update failure",
					ion.String("tx_hash", tx.Hash.Hex()),
					ion.String("account", accounts.Hex()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.processTransaction"),
				)
			}
		}
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to add gas fee to coinbase: %w", err)
	}

	txSpan.SetAttributes(attribute.String("coinbase_gas_fee_step", "completed"))

	if err := addToRecipient(txSpanCtx, zkvmAddr, zkvmGasFee.String(), accountsClient); err != nil {
		// Rollback previous operations
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "zkvm_gas_fee_failed"), attribute.String("failed_step", "add_to_zkvm"))
		rollbackAccounts := []common.Address{*tx.From, *tx.To, coinbaseAddr, zkvmAddr}
		for _, accounts := range rollbackAccounts {
			if rollbackErr := DB_OPs.UpdateAccountBalance(accountsClient, accounts, originalBalances[accounts]); rollbackErr != nil {
				txSpan.RecordError(rollbackErr)
				logger().NamedLogger.Error(txSpanCtx, "Failed to rollback balance",
					rollbackErr,
					ion.String("tx_hash", tx.Hash.Hex()),
					ion.String("account", accounts.Hex()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.processTransaction"),
				)
			} else {
				logger().NamedLogger.Info(txSpanCtx, "Rolled back balance due to gas fee update failure",
					ion.String("tx_hash", tx.Hash.Hex()),
					ion.String("account", accounts.Hex()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.processTransaction"),
				)
			}
		}
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to add gas fee to ZKVM: %w", err)
	}

	txSpan.SetAttributes(attribute.String("zkvm_gas_fee_step", "completed"))

	// Mark transaction as fully processed - this is the key that prevents double processing
	if err := DB_OPs.Create(accountsClient, txKey, time.Now().UTC().Unix()); err != nil {
		txSpan.RecordError(err)
		logger().NamedLogger.Error(txSpanCtx, "Failed to mark transaction as processed",
			err,
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		// Still continue as the transaction was processed successfully
	}

	// Clean up the processing marker
	cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())

	duration := time.Since(txStartTime).Seconds()
	txSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(txSpanCtx, "Transaction processed successfully",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.processTransaction"),
	)

	return nil
}

// accountExists checks if an account exists in the database
func accountExists(account *common.Address, accountsClient *config.PooledConnection) (bool, error) {
	fmt.Println("Checking if account exists: ", account.Hex()) // Debugging
	_, err := DB_OPs.GetAccount(accountsClient, *account)
	if err != nil {
		if err == DB_OPs.ErrNotFound || strings.Contains(err.Error(), "key not found") {
			fmt.Println("Account does not exist: ", account.Hex()) // Debugging
			return false, nil
		}
		fmt.Println("Error checking account existence: ", account.Hex(), "Error: ", err.Error()) // Debugging
		return false, err
	}
	fmt.Println("Account exists: ", account.Hex()) // Debugging
	return true, nil
}

// categorizeDeductionError provides more specific error types for deduction failures
func categorizeDeductionError(err error) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	if contains(errStr, "insufficient balance") {
		return fmt.Errorf("insufficient funds: %w", err)
	} else if contains(errStr, "failed to retrieve sender DID") {
		return fmt.Errorf("account not found: %w", err)
	} else if contains(errStr, "invalid balance format") {
		return fmt.Errorf("account data corrupted: %w", err)
	}

	return fmt.Errorf("transaction failed: %w", err)
}

// contains checks if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr
}

// parseTransaction parses the numeric values in a transaction
func parseTransaction(tx config.Transaction) (*config.ParsedZKTransaction, error) {
	parsed := &config.ParsedZKTransaction{
		Original: &tx,
	}

	// Set the value directly since it's already a *big.Int
	if tx.Value != nil {
		parsed.ValueBig = new(big.Int).Set(tx.Value)
	} else {
		parsed.ValueBig = big.NewInt(0)
	}

	// Determine gas fee based on transaction type
	// Type 0x0 = Legacy, 0x1 = AccessList, 0x2 = DynamicFee (EIP-1559)
	if tx.Type == 2 { // EIP-1559 transaction
		// For EIP-1559, use MaxFee as the effective gas fee
		if tx.MaxFee != nil {
			parsed.MaxFeeBig = new(big.Int).Set(tx.MaxFee)
			parsed.EffectiveGasFee = new(big.Int).Set(tx.MaxFee)
		} else {
			// Fallback to MaxPriorityFee if MaxFee is not set
			if tx.MaxPriorityFee != nil {
				parsed.MaxFeeBig = new(big.Int).Set(tx.MaxPriorityFee)
				parsed.EffectiveGasFee = new(big.Int).Set(tx.MaxPriorityFee)
			} else {
				// Fallback to GasPrice if available
				if tx.GasPrice != nil {
					parsed.MaxFeeBig = new(big.Int).Set(tx.GasPrice)
					parsed.EffectiveGasFee = new(big.Int).Set(tx.GasPrice)
				} else {
					// Last resort: use default gas price
					parsed.MaxFeeBig = big.NewInt(DefaultGasPrice)
					parsed.EffectiveGasFee = big.NewInt(DefaultGasPrice)
				}
			}
		}
	} else {
		// For Legacy or AccessList transactions, use GasPrice if available
		if tx.GasPrice != nil {
			parsed.EffectiveGasFee = new(big.Int).Set(tx.GasPrice)
		} else if tx.MaxFee != nil {
			// Fallback to MaxFee if GasPrice is not set
			parsed.EffectiveGasFee = new(big.Int).Set(tx.MaxFee)
		} else if tx.MaxPriorityFee != nil {
			// Fallback to MaxPriorityFee if others are not set
			parsed.EffectiveGasFee = new(big.Int).Set(tx.MaxPriorityFee)
		} else {
			// Last resort: use default gas price
			parsed.EffectiveGasFee = big.NewInt(DefaultGasPrice)
		}

		// For non-EIP-1559 transactions, MaxFeeBig is not applicable
		parsed.MaxFeeBig = nil
	}

	return parsed, nil
}

// deductFromSender deducts an amount from a sender's DID account
func deductFromSender(span_ctx context.Context, fromDID common.Address, amount string, accountsClient *config.PooledConnection) error {
	// Get the current DID document using the provided accounts client
	didDoc, err := DB_OPs.GetAccount(accountsClient, fromDID)
	if err != nil {
		return fmt.Errorf("failed to retrieve sender DID %s: %w", fromDID, err)
	}

	// Parse current balance
	currentBalance, ok := new(big.Int).SetString(didDoc.Balance, 10)
	if !ok {
		return fmt.Errorf("invalid balance format for DID %s: %s", fromDID, didDoc.Balance)
	}

	// Parse amount to deduct
	deductAmount, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return fmt.Errorf("invalid deduction amount: %s", amount)
	}

	// Check if sufficient balance
	if currentBalance.Cmp(deductAmount) < 0 {
		return fmt.Errorf("insufficient balance for DID %s: has %s, needs %s",
			fromDID, currentBalance.String(), deductAmount.String())
	}

	// Calculate new balance
	newBalance := new(big.Int).Sub(currentBalance, deductAmount)

	// Update the balance in the database using the provided accounts client
	if err := DB_OPs.UpdateAccountBalance(accountsClient, fromDID, newBalance.String()); err != nil {
		return fmt.Errorf("failed to update sender balance: %w", err)
	}

	logger().NamedLogger.Debug(span_ctx, "Deducted amount from sender",
		ion.String("account", fromDID.String()),
		ion.String("amount", amount),
		ion.String("old_balance", currentBalance.String()),
		ion.String("new_balance", newBalance.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.deductFromSender"),
	)

	return nil
}

// addToRecipient adds an amount to a recipient's DID account
func addToRecipient(span_ctx context.Context, ToAddress common.Address, amount string, accountsClient *config.PooledConnection) error {
	// Get the current DID document using the provided accounts client
	didDoc, err := DB_OPs.GetAccount(accountsClient, ToAddress)
	if err != nil {
		// If DID doesn't exist,
		return fmt.Errorf("failed to retrieve recipient DID %s: %w", ToAddress, err)
	}

	// Parse current balance
	currentBalance, ok := new(big.Int).SetString(didDoc.Balance, 10)
	if !ok {
		return fmt.Errorf("invalid balance format for DID %s: %s", ToAddress, didDoc.Balance)
	}

	// Parse amount to add
	addAmount, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return fmt.Errorf("invalid addition amount: %s", amount)
	}

	// Calculate new balance
	newBalance := new(big.Int).Add(currentBalance, addAmount)

	// Update the balance in the database using the provided accounts client
	if err := DB_OPs.UpdateAccountBalance(accountsClient, ToAddress, newBalance.String()); err != nil {
		return fmt.Errorf("failed to update recipient balance: %w", err)
	}

	logger().NamedLogger.Debug(span_ctx, "Added amount to recipient",
		ion.String("account", ToAddress.String()),
		ion.String("amount", amount),
		ion.String("old_balance", currentBalance.String()),
		ion.String("new_balance", newBalance.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.addToRecipient"),
	)

	return nil
}
