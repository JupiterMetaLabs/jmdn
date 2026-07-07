package BlockProcessing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/SmartContract"
	"gossipnode/config"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/holiman/uint256"
	"go.opentelemetry.io/otel/attribute"
)

const (
	LOG_FILE = "block_processing.log"
	TOPIC    = "BlockProcessing"
)

// ErrStaleNonce is returned when a transaction's nonce is lower than the
// account's current DB nonce. This is a skippable condition — it means the
// tx was valid at security-check time but the account moved on before
// ProcessBlockLocally ran (race between vote validation and execution).
var ErrStaleNonce = errors.New("stale nonce")

// AccountSnapshot captures the mutable state of an account before block processing begins.
// All three fields must be restored atomically on rollback to prevent nonce/count corruption.
type AccountSnapshot struct {
	Balance     string
	TxNonce     uint64
	TxCountSent uint64
	UpdatedAt   int64
}

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

	// Smart Contract Configuration
	// This ChainID must be set via SetChainID() from main.go
	GlobalChainID = 0
)

// SetChainID sets the global network chain ID for transaction processing
func SetChainID(chainID int) {
	GlobalChainID = chainID
}

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

// ContractDeploymentInfo carries the essential details of a contract deployed
// within a block.  Returned by ProcessBlockTransactions so the sequencer can
// propagate the contract to the rest of the network post-consensus.
type ContractDeploymentInfo struct {
	ContractAddress common.Address
	Deployer        common.Address
	TxHash          common.Hash
	BlockNumber     uint64
	GasUsed         uint64
}

// ProcessBlockTransactions processes all transactions in a block atomically.
// If any transaction fails, all are rolled back.
// If commitToDB is true, state changes are persisted to the database.
// Returns a slice of ContractDeploymentInfo for every successfully deployed contract.
func ProcessBlockTransactions(block *config.ZKBlock, accountsClient *config.PooledConnection, commitToDB bool) ([]ContractDeploymentInfo, error) {
	span_ctx, span := logger().Tracer("BlockProcessing").Start(context.Background(), "BlockProcessing.ProcessBlockTransactions")
	defer span.End()
	startTime := time.Now().UTC()
	var deployments []ContractDeploymentInfo

	// Note: StateDB is NOT initialized here for regular transactions
	// It will be created on-demand inside processTransaction() only for smart contract transactions

	// Check if block was already processed
	blockKey := fmt.Sprintf("block_processed:%s", block.BlockHash.Hex())
	processed, err := DB_OPs.Exists(accountsClient, blockKey)
	if err == nil && processed {
		logger().Info(context.Background(), "Block already processed, skipping", ion.String("block_hash", block.BlockHash.Hex()))
		return nil, nil
	}

	ClearProcessedTransactions()

	// Store original state to enable rollback - captures balance + nonce + txcount atomically
	originalState := make(map[common.Address]AccountSnapshot)
	affectedAccounts := make(map[common.Address]bool)

	// First, collect all affected DIDs from the block
	for _, tx := range block.Transactions {
		affectedAccounts[*tx.From] = true
		// Smart contracts should be type 2 transactions and their To address is the contract address that will be generated while processing
		if tx.To != nil && tx.Type == 2 {
			affectedAccounts[*tx.To] = true
		}
	}
	affectedAccounts[*block.CoinbaseAddr] = true
	affectedAccounts[*block.ZKVMAddr] = true

	span.SetAttributes(attribute.Int("affected_accounts", len(affectedAccounts)))

	// Fetch and store original state BEFORE any processing
	for addr := range affectedAccounts {
		doc, err := DB_OPs.GetAccount(accountsClient, addr)
		if err == nil {
			originalState[addr] = AccountSnapshot{
				Balance:     doc.Balance,
				TxNonce:     doc.TxNonce,
				TxCountSent: doc.TxCountSent,
				UpdatedAt:   doc.UpdatedAt,
			}
		} else {
			// Account doesn't exist yet — zero-value snapshot, rollback will restore to 0
			originalState[addr] = AccountSnapshot{Balance: "0"}
		}
	}

	span.SetAttributes(attribute.Int("sorted_transactions", len(block.Transactions)))

	logger().Info(span_ctx, "Starting block processing",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.Int("transaction_count", len(block.Transactions)),
		ion.Int("affected_accounts", len(affectedAccounts)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
	)

	// Track successfully processed transactions for atomic commit
	successfullyProcessedTxs := make([]string, 0, len(block.Transactions))

	// Process all transactions exactly as ordered by the Sequencer
	// If ANY fails, rollback ALL affected accounts
	for i, tx := range block.Transactions {
		// Check if this transaction was already processed within this block
		processedTxsMutex.Lock()
		if processedTxs[tx.Hash.Hex()] {
			logger().Warn(context.Background(), "Duplicate transaction in block, skipping", ion.Err(errors.New("duplicate transaction")), ion.String("tx_hash", tx.Hash.Hex()))
			processedTxsMutex.Unlock()
			continue
		}
		processedTxs[tx.Hash.Hex()] = true
		processedTxsMutex.Unlock()

		// Check if this transaction was already processed in a previous block
		txKey := fmt.Sprintf("tx_processed:%s", tx.Hash)
		alreadyProcessed, err := DB_OPs.Exists(accountsClient, txKey)
		if err == nil && alreadyProcessed {
			continue
		}

		// Process the transaction with span context
		info, Process_err := processTransaction(span_ctx, tx, *block.CoinbaseAddr, *block.ZKVMAddr, accountsClient, block.Timestamp, commitToDB)
		if Process_err != nil {
			// Stale nonce: security check passed at vote time but the account nonce
			// advanced before execution (race with PoTS / concurrent block processing).
			// Skip this tx — do NOT roll back other txs or fail the block.
			if errors.Is(Process_err, ErrStaleNonce) {
				cleanupProcessingMarkers(accountsClient, tx.Hash.Hex())
				logger().Warn(span_ctx, "Skipping tx with stale nonce — account nonce advanced between security check and execution",
					ion.String("tx_hash", tx.Hash.Hex()),
					ion.Int("tx_index", i),
					ion.String("error", Process_err.Error()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
				)
				continue
			}

			// ATOMICITY: If any non-stale transaction fails, roll back ALL affected accounts
			span.RecordError(Process_err)
			span.SetAttributes(attribute.String("status", "failed"), attribute.String("failed_tx_hash", tx.Hash.Hex()), attribute.Int("failed_tx_index", i))

			logger().Error(span_ctx, "Transaction failed, rolling back entire block",
				Process_err,
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.Int("tx_index", i),
				ion.Int("total_transactions", len(block.Transactions)),
				ion.Int("successful_before_failure", len(successfullyProcessedTxs)),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)

			// Rollback all account state to original snapshot
			rollbackError := rollbackState(span_ctx, originalState, accountsClient)
			if rollbackError != nil {
				span.RecordError(rollbackError)
				logger().Error(span_ctx, "Failed to rollback balances after transaction failure",
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
				cleanupProcessingMarkers(accountsClient, txHash)
			}
			cleanupProcessingMarkers(accountsClient, tx.Hash.Hex())

			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return nil, fmt.Errorf("block processing failed at transaction %d/%d (hash: %s): %w", i+1, len(block.Transactions), tx.Hash.Hex(), Process_err)
		}

		// Track successfully processed transaction
		successfullyProcessedTxs = append(successfullyProcessedTxs, tx.Hash.Hex())
		
		if info != nil {
			info.BlockNumber = block.BlockNumber
			deployments = append(deployments, *info)
		}
	}

	// Mark all processed tx/block keys in one logical batch.
	// ThebeDB: these markers are derived from SQL state — Set is a no-op kept
	// for flow parity with the legacy write path.
	if len(successfullyProcessedTxs) > 0 {
		err := DB_OPs.Transaction(func() error {
			// Mark all successfully processed transactions
			for _, txHash := range successfullyProcessedTxs {
				txKey := fmt.Sprintf("tx_processed:%s", txHash)
				if err := DB_OPs.Set(txKey, time.Now().UTC().Unix()); err != nil {
					return fmt.Errorf("failed to add transaction marker for %s: %w", txHash, err)
				}

				// Clean up processing markers (set to -1 to mark as cleaned)
				processingKey := fmt.Sprintf("tx_processing:%s", txHash)
				if err := DB_OPs.Set(processingKey, int64(-1)); err != nil {
					return fmt.Errorf("failed to add cleanup marker for %s: %w", txHash, err)
				}
			}

			// Mark the block as processed - this is the final operation in the transaction
			if err := DB_OPs.Set(blockKey, time.Now().UTC().Unix()); err != nil {
				return fmt.Errorf("failed to add block marker: %w", err)
			}

			return nil
		})

		if err != nil {
			// Transaction failed - Immudb automatically rolled back all operations
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "atomic_marking_failed"))
			logger().Error(span_ctx, "Failed to atomically mark transactions and block, rolling back balances",
				err,
				ion.Int("transaction_count", len(successfullyProcessedTxs)),
				ion.String("block_hash", block.BlockHash.Hex()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)
			// Rollback account state since transaction marking failed
			rollbackState(span_ctx, originalState, accountsClient)
			// Clean up processing markers (they weren't committed due to transaction failure)
			for _, txHash := range successfullyProcessedTxs {
				cleanupProcessingMarkers(accountsClient, txHash)
			}

			return nil, fmt.Errorf("block processing failed: %w", err)
		}
	}

	// Mark all transactions as successfully processed in the database
	for txHash := range processedTxs {
		txKey := fmt.Sprintf("tx_processed:%s", txHash)
		if err := DB_OPs.Create(accountsClient, txKey, time.Now().UTC().Unix()); err != nil {
			logger().Warn(context.Background(), "Failed to mark transaction as processed", ion.Err(err), ion.String("tx_hash", txHash))
		}

		// Clean up the processing key
		processingKey := fmt.Sprintf("tx_processing:%s", txHash)
		if exists, _ := DB_OPs.Exists(accountsClient, processingKey); exists {
			if err := DB_OPs.Create(accountsClient, processingKey, int64(-1)); err != nil {
				logger().Warn(context.Background(), "Failed to clean up processing marker", ion.Err(err), ion.String("tx_hash", txHash))
			}
		}
	}

	// Mark the block as processed (regular transactions already committed via DB_OPs)
	if err := DB_OPs.Create(accountsClient, blockKey, time.Now().UTC().Unix()); err != nil {
		logger().Warn(context.Background(), "Failed to mark block as processed", ion.Err(err), ion.String("block_hash", block.BlockHash.Hex()))
	}

	return deployments, nil
}

// cleanupProcessingMarkers removes temporary processing markers
func cleanupProcessingMarkers(accountsClient *config.PooledConnection, txHash string) {
	processingKey := fmt.Sprintf("tx_processing:%s", txHash)
	if exists, _ := DB_OPs.Exists(accountsClient, processingKey); exists {
		if err := DB_OPs.Create(accountsClient, processingKey, int64(-1)); err != nil {
		}
	}

	// Also clean up the transaction lock
	cleanupTransactionLock(txHash)
}

// rollbackState restores all affected accounts to their pre-block snapshot atomically.
// It restores balance, TxNonce, and TxCountSent in a single write per account.
func rollbackState(span_ctx context.Context, snapshots map[common.Address]AccountSnapshot, accountsClient *config.PooledConnection) error {
	rollbackSpanCtx, rollbackSpan := logger().Tracer("BlockProcessing").Start(span_ctx, "BlockProcessing.rollbackState")
	defer rollbackSpan.End()

	rollbackStartTime := time.Now().UTC()
	rollbackSpan.SetAttributes(attribute.Int("accounts_to_rollback", len(snapshots)))

	rollbackCount := 0
	for addr, snap := range snapshots {
		doc, err := DB_OPs.GetAccount(accountsClient, addr)
		if err != nil {
			// If it doesn't exist yet, we create an empty placeholder to zero it out
			doc = &DB_OPs.Account{Address: addr}
		}

		doc.Balance = snap.Balance
		doc.TxNonce = snap.TxNonce
		doc.TxCountSent = snap.TxCountSent
		doc.UpdatedAt = snap.UpdatedAt

		if err := DB_OPs.UpdateAccount(accountsClient, doc); err != nil {
			rollbackSpan.RecordError(err)
			rollbackSpan.SetAttributes(attribute.String("status", "partial_failure"), attribute.String("failed_account", addr.Hex()))
			logger().Error(rollbackSpanCtx, "Failed to restore account state during rollback",
				err,
				ion.String("account", addr.Hex()),
				ion.String("original_balance", snap.Balance),
				ion.Uint64("original_tx_nonce", snap.TxNonce),
				ion.Uint64("original_tx_count_sent", snap.TxCountSent),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.rollbackState"),
			)
			return fmt.Errorf("failed to restore state for %s: %w", addr, err)
		}
		rollbackCount++
		logger().Debug(rollbackSpanCtx, "Rolled back account state to original snapshot",
			ion.String("account", addr.Hex()),
			ion.String("balance", snap.Balance),
			ion.Uint64("tx_nonce", snap.TxNonce),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.rollbackState"),
		)
	}

	duration := time.Since(rollbackStartTime).Seconds()
	rollbackSpan.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
		attribute.Int("rolled_back_accounts", rollbackCount),
	)
	logger().Info(rollbackSpanCtx, "Rollback completed successfully",
		ion.Int("rolled_back_accounts", rollbackCount),
		ion.Float64("duration", duration),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.rollbackState"),
	)
	return nil
}

// ProcessTransaction handles a single transaction's balance updates
func processTransaction(span_ctx context.Context, tx config.Transaction, coinbaseAddr common.Address, zkvmAddr common.Address, accountsClient *config.PooledConnection, blockTimestamp int64, commitToDB bool) (*ContractDeploymentInfo, error) {
	// Record trace span and close it
	txSpanCtx, txSpan := logger().Tracer("BlockProcessing").Start(span_ctx, "BlockProcessing.processTransaction")
	defer txSpan.End()
	txStartTime := time.Now().UTC()

	// ========== SMART CONTRACT DETECTION ==========
	// Check if this is a contract deployment (To == nil) or execution (code exists at To)
	isContract := (tx.To == nil && tx.Type == 2)
	if !isContract && tx.To != nil {
		// Lightweight code-presence check — avoids allocating a full StateDB.
		isContract = SmartContract.HasCode(*tx.To)
	}
	// Declare StateDB and snapshot variables (used by both smart contracts and regular transfers)
	var stateDB SmartContract.StateDB
	var snapshot int
	var err error

	// Only create StateDB for smart contracts (variables declared below in regular transfer section)
	if isContract {
		stateDB, err = SmartContract.NewStateDB(GlobalChainID)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize StateDB for contract: %w", err)
		}
		snapshot = stateDB.Snapshot()
	}

	// ========== CONTRACT DEPLOYMENT ==========
	if tx.To == nil && tx.Type == 2 {

		logger().Info(context.Background(), "🚀 [CONSENSUS] CONTRACT DEPLOYMENT detected", ion.String("tx_hash", tx.Hash.Hex()))

		// Call SmartContract module's deployment processor with StateDB
		result, err := SmartContract.ProcessContractDeployment(&tx, stateDB, GlobalChainID)
		if err != nil {
			stateDB.RevertToSnapshot(snapshot) // Rollback
			logger().Error(context.Background(), "❌ [CONSENSUS] Contract deployment failed", err, ion.String("tx_hash", tx.Hash.Hex()))
			cleanupProcessingMarkers(accountsClient, tx.Hash.Hex())
			return nil, fmt.Errorf("contract deployment failed: %w", err)
		}

		if !result.Success {
			stateDB.RevertToSnapshot(snapshot) // Rollback
			logger().Error(context.Background(), "❌ [CONSENSUS] Contract deployment unsuccessful", errors.New("deployment unsuccessful"), ion.String("tx_hash", tx.Hash.Hex()))
			return nil, result.Error
		}

		// Handle gas fees
		parsedTx, err := parseTransaction(tx)
		if err != nil {
			stateDB.RevertToSnapshot(snapshot)
			return nil, fmt.Errorf("failed to parse transaction for gas: %w", err)
		}

		gasUsed := big.NewInt(int64(result.GasUsed))
		gasFeeToDeduct := new(big.Int).Mul(gasUsed, parsedTx.EffectiveGasFee)

		// Split gas fee between validators
		halfGasFee := new(big.Int).Div(gasFeeToDeduct, big.NewInt(2))
		remainder := new(big.Int).Mod(gasFeeToDeduct, big.NewInt(2))
		zkvmGasFee := new(big.Int).Set(halfGasFee)
		coinbaseGasFee := new(big.Int).Add(halfGasFee, remainder)

		// Deduct ONLY gas fee from sender (EVM handles value transfer via transferFn)
		// EVM's Create() method automatically transfers parsedTx.Value from sender to contract
		gasDeductAmount, overflow := uint256.FromBig(gasFeeToDeduct)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return nil, fmt.Errorf("gas fee amount overflow")
		}
		stateDB.SubBalance(*tx.From, gasDeductAmount, tracing.BalanceChangeTransfer)

		// Note: Value transfer to contract is handled by EVM's Create() via transferFn
		// No manual transfer needed here to avoid double-counting

		// Pay coinbase their share of gas fees
		coinbaseAmount, overflow := uint256.FromBig(coinbaseGasFee)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return nil, fmt.Errorf("coinbase gas fee overflow")
		}
		stateDB.AddBalance(coinbaseAddr, coinbaseAmount, tracing.BalanceChangeTransfer)

		// Pay ZKVM their share of gas fees
		zkvmAmount, overflow := uint256.FromBig(zkvmGasFee)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return nil, fmt.Errorf("zkvm gas fee overflow")
		}
		stateDB.AddBalance(zkvmAddr, zkvmAmount, tracing.BalanceChangeTransfer)

		logger().Info(context.Background(), "💰 Gas fees processed for deployment", ion.String("contract", result.ContractAddress.Hex()))

		// Commit StateDB changes if requested
		if commitToDB {
			logger().Info(context.Background(), "💾 Committing contract deployment state to database")

			// Update balances in DID service before committing StateDB
			for addr, balance := range stateDB.GetBalanceChanges() {
				if err := DB_OPs.UpdateAccountBalance(accountsClient, addr, balance.String(), blockTimestamp); err != nil {
					return nil, fmt.Errorf("failed to update DID service balance for %s: %w", addr.Hex(), err)
				}
			}

			if _, err := stateDB.CommitToDB(false); err != nil {
				return nil, fmt.Errorf("failed to commit contract deployment state: %w", err)
			}

			// Return deployment info so sequencer can propagate via gossip.
			return &ContractDeploymentInfo{
				ContractAddress: result.ContractAddress,
				Deployer:        *tx.From,
				TxHash:          tx.Hash,
				GasUsed:         result.GasUsed,
				// BlockNumber is filled in by the caller (ProcessBlockTransactions)
			}, nil
		}

		logger().Info(context.Background(), "🚫 Skipping state commit (verification mode)")
		return nil, nil
	}

	// ========== SMART CONTRACT EXECUTION DETECTION ==========
	// Check if this is a transaction to an existing contract (To != nil and has code)
	// We use stateDB.GetCodeSize to check if the target address is a contract
	if tx.To != nil && stateDB.GetCodeSize(*tx.To) > 0 {
		logger().Info(context.Background(), "⚙️ [CONSENSUS] CONTRACT EXECUTION detected", ion.String("tx_hash", tx.Hash.Hex()))

		// Call SmartContract module's execution processor with StateDB
		result, err := SmartContract.ProcessContractExecution(&tx, stateDB, GlobalChainID)
		if err != nil {
			stateDB.RevertToSnapshot(snapshot) // Rollback
			logger().Error(context.Background(), "❌ [CONSENSUS] Contract execution failed", err, ion.String("tx_hash", tx.Hash.Hex()))
			cleanupProcessingMarkers(accountsClient, tx.Hash.Hex())
			return nil, fmt.Errorf("contract execution failed: %w", err)
		}

		// Handle gas fees
		parsedTx, err := parseTransaction(tx)
		if err != nil {
			stateDB.RevertToSnapshot(snapshot)
			return nil, fmt.Errorf("failed to parse transaction for gas: %w", err)
		}

		gasUsed := big.NewInt(int64(result.GasUsed))
		gasFeeToDeduct := new(big.Int).Mul(gasUsed, parsedTx.EffectiveGasFee)

		// Split gas fee between validators
		halfGasFee := new(big.Int).Div(gasFeeToDeduct, big.NewInt(2))
		remainder := new(big.Int).Mod(gasFeeToDeduct, big.NewInt(2))
		zkvmGasFee := new(big.Int).Set(halfGasFee)
		coinbaseGasFee := new(big.Int).Add(halfGasFee, remainder)

		// Deduct ONLY gas fee from sender (EVM handles value transfer via transferFn)
		// EVM's Call() method automatically transfers parsedTx.Value from sender to contract
		gasDeductAmount, overflow := uint256.FromBig(gasFeeToDeduct)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return nil, fmt.Errorf("gas fee amount overflow")
		}
		stateDB.SubBalance(*tx.From, gasDeductAmount, tracing.BalanceChangeTransfer)

		// Note: Value transfer to contract is handled by EVM's Call() via transferFn
		// No manual transfer needed here to avoid double-counting

		// Pay coinbase their share of gas fees
		coinbaseExecAmount, overflow := uint256.FromBig(coinbaseGasFee)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return nil, fmt.Errorf("coinbase gas fee overflow")
		}
		stateDB.AddBalance(coinbaseAddr, coinbaseExecAmount, tracing.BalanceChangeTransfer)

		// Pay ZKVM their share of gas fees
		zkvmExecAmount, overflow := uint256.FromBig(zkvmGasFee)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return nil, fmt.Errorf("zkvm gas fee overflow")
		}
		stateDB.AddBalance(zkvmAddr, zkvmExecAmount, tracing.BalanceChangeTransfer)

		logger().Info(context.Background(), "💰 Gas fees processed for execution", ion.String("contract", tx.To.Hex()))

		// Commit StateDB changes if requested
		if commitToDB {
			logger().Info(context.Background(), "💾 Committing contract execution state to database")

			// Update balances in DID service before committing StateDB
			for addr, balance := range stateDB.GetBalanceChanges() {
				if err := DB_OPs.UpdateAccountBalance(accountsClient, addr, balance.String(), blockTimestamp); err != nil {
					return nil, fmt.Errorf("failed to update DID service balance for %s: %w", addr.Hex(), err)
				}
			}

			if _, err := stateDB.CommitToDB(false); err != nil {
				return nil, fmt.Errorf("failed to commit contract execution state: %w", err)
			}
		} else {
			logger().Info(context.Background(), "🚫 Skipping state commit (verification mode)")
		}

		return nil, nil
	}

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
		logger().Info(context.Background(), "Transaction already processed in previous block, skipping", ion.String("tx_hash", tx.Hash.Hex()))
		return nil, nil
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
				} else {
					// We have the lock, so continue processing anyway as previous attempt might have failed
				} // We have the lock, so continue processing anyway as previous attempt might have failed
			}
		}
	}

	// Mark transaction as being processed
	if err := DB_OPs.Create(accountsClient, txProcessingKey, time.Now().UTC().Unix()); err != nil {
		// Continue processing since this is just a precaution
	}

	// Store original state for rollback if needed
	originalState := make(map[common.Address]AccountSnapshot)
	affectedDIDs := []common.Address{*tx.From, *tx.To, coinbaseAddr, zkvmAddr}

	for _, did := range affectedDIDs {
		doc, err := DB_OPs.GetAccount(accountsClient, did)
		if err == nil {
			originalState[did] = AccountSnapshot{
				Balance:     doc.Balance,
				TxNonce:     doc.TxNonce,
				TxCountSent: doc.TxCountSent,
				UpdatedAt:   doc.UpdatedAt,
			}
		} else if err == DB_OPs.ErrNotFound || strings.Contains(err.Error(), "key not found") {
			originalState[did] = AccountSnapshot{Balance: "0"}
		} else {
			return nil, fmt.Errorf("failed to retrieve original balance for %s: %w", did.Hex(), err)
		}
	}

	// Parse the transaction values
	var parsedTx *config.ParsedZKTransaction
	parsedTx, err = parseTransaction(tx)
	if err != nil {
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return nil, fmt.Errorf("failed to parse transaction: %w", err)
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

	// Check if sender exists before attempting deduction
	senderExists, _ := accountExists(tx.From, accountsClient)
	if !senderExists {
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		logger().Error(context.Background(), "Balance transfer blocked: sender account not found in accounts DB",
			errors.New("sender not found"),
			ion.String("sender", tx.From.Hex()),
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("hint", "CreateAccount must be called for this address before it can send transactions"))
		return nil, fmt.Errorf("sender DID %s does not exist", tx.From)
	}

	// Check if recipient exists (for better error reporting)
	recipientExists, _ := accountExists(tx.To, accountsClient)
	if !recipientExists && !CreateMissingAccounts {
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return nil, fmt.Errorf("recipient DID %s does not exist and automatic creation is disabled", tx.To)
	}

	// ========== REGULAR TRANSFER: Create StateDB ==========
	// All transactions now use StateDB for Ethereum-style verification
	stateDB, err = SmartContract.NewStateDB(GlobalChainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create StateDB for regular transfer: %w", err)
	}
	snapshot = stateDB.Snapshot()

	// Log sender balance before deduction so failures are diagnosable
	senderBalance := stateDB.GetBalance(*tx.From)
	logger().Info(context.Background(), "Regular transfer: sender balance check",
		ion.String("sender", tx.From.Hex()),
		ion.String("sender_balance_wei", senderBalance.String()),
		ion.String("total_deduction_wei", totalDeduction.String()),
		ion.String("tx_hash", tx.Hash.Hex()))

	// 1. Deduct from sender
	if err := deductFromSender(txSpanCtx, &tx, totalDeduction.String(), accountsClient, blockTimestamp); err != nil {
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "deduction_failed"), attribute.String("failed_step", "deduct_from_sender"))
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().Error(txSpanCtx, "Failed to deduct from sender",
			err,
			ion.String("sender", tx.From.Hex()),
			ion.String("sender_balance_wei", senderBalance.String()),
			ion.String("total_deduction_wei", totalDeduction.String()),
			ion.String("tx_hash", tx.Hash.Hex()))
		return nil, categorizeDeductionError(err)
	}

	// 2. Add amount to recipient
	if err := addToRecipient(txSpanCtx, *tx.To, parsedTx.ValueBig.String(), accountsClient, blockTimestamp); err != nil {
		// Remove nested rollback logic: parent loop will handle full block rollback via rollbackState
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "recipient_add_failed"), attribute.String("failed_step", "add_to_recipient"))

		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to add to recipient: %w", err)
	}

	// Add gas fees to coinbase and zkvm
	if err := addToRecipient(txSpanCtx, coinbaseAddr, coinbaseGasFee.String(), accountsClient, blockTimestamp); err != nil {
		// Remove nested rollback logic: parent loop will handle full block rollback via rollbackState
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "coinbase_gas_fee_failed"), attribute.String("failed_step", "add_to_coinbase"))
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to add gas fee to coinbase: %w", err)
	}

	txSpan.SetAttributes(attribute.String("coinbase_gas_fee_step", "completed"))

	if err := addToRecipient(txSpanCtx, zkvmAddr, zkvmGasFee.String(), accountsClient, blockTimestamp); err != nil {
		// Remove nested rollback logic: parent loop will handle full block rollback via rollbackState
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "zkvm_gas_fee_failed"), attribute.String("failed_step", "add_to_zkvm"))
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to add gas fee to ZKVM: %w", err)
	}

	// Commit StateDB if requested (Ethereum-style)
	if commitToDB {
		logger().Info(context.Background(), "💾 Committing regular transfer state to database")

		// Update balances in DID service before committing StateDB
		for addr, balance := range stateDB.GetBalanceChanges() {
			if err := DB_OPs.UpdateAccountBalance(accountsClient, addr, balance.String(), blockTimestamp); err != nil {
				return nil, fmt.Errorf("failed to update DID service balance for %s: %w", addr.Hex(), err)
			}
		}

		if _, err := stateDB.CommitToDB(false); err != nil {
			return nil, fmt.Errorf("failed to commit regular transfer state: %w", err)
		}
	} else {
		logger().Info(context.Background(), "🚫 Skipping state commit for regular transfer (verification mode)")
	}

	// Mark transaction as fully processed - this is the key that prevents double processing
	if err := DB_OPs.Create(accountsClient, txKey, time.Now().UTC().Unix()); err != nil {
		// Still continue as the transaction was processed successfully
	}

	// Clean up the processing marker
	cleanupProcessingMarkers(accountsClient, tx.Hash.String())

	return nil, nil
}

// accountExists checks if an account exists in the database
func accountExists(account *common.Address, accountsClient *config.PooledConnection) (bool, error) {
	_, err := DB_OPs.GetAccount(accountsClient, *account)
	if err != nil {
		if err == DB_OPs.ErrNotFound || strings.Contains(err.Error(), "key not found") {
			return false, nil
		}
		return false, err
	}
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
		// EIP-1559 effective gas price = min(maxFee, baseFee + tip)
		// JMDN uses a flat 35 gwei base fee.
		const baseFeeWei = int64(35_000_000_000)

		maxFee := tx.MaxFee
		if maxFee == nil {
			maxFee = big.NewInt(baseFeeWei) // safe fallback
		}
		parsed.MaxFeeBig = new(big.Int).Set(maxFee)

		tip := tx.MaxPriorityFee
		if tip == nil {
			tip = new(big.Int)
		}
		basePlusTip := new(big.Int).Add(big.NewInt(baseFeeWei), tip)

		// effective = min(maxFee, baseFee + tip)
		if maxFee.Cmp(basePlusTip) <= 0 {
			parsed.EffectiveGasFee = new(big.Int).Set(maxFee)
		} else {
			parsed.EffectiveGasFee = basePlusTip
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
func deductFromSender(span_ctx context.Context, tx *config.Transaction, amount string, accountsClient *config.PooledConnection, blockTimestamp int64) error {
	fromDID := *tx.From
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

	// Foolproof execution-time nonce check (prevents same-block replay attacks).
	// Returns ErrStaleNonce so the caller can skip this tx rather than rolling
	// back the entire block — the tx was valid at security-check time but the
	// account nonce advanced before ProcessBlockLocally ran.
	if tx.Nonce < didDoc.TxNonce {
		return fmt.Errorf("%w: submitted nonce %d is lower than account's current DB nonce %d", ErrStaleNonce, tx.Nonce, didDoc.TxNonce)
	}

	// Parse amount to deduct
	deductAmount, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return fmt.Errorf("invalid deduction amount: %s", amount)
	}

	// Check balance
	if currentBalance.Cmp(deductAmount) < 0 {
		return fmt.Errorf("insufficient balance for DID %s: has %s, needs %s",
			fromDID.Hex(), currentBalance.String(), deductAmount.String())
	}

	// Calculate new balance
	newBalance := new(big.Int).Sub(currentBalance, deductAmount)

	// Update balance, TxNonce, and TxCountSent sequentially using the fetched doc
	didDoc.Balance = newBalance.String()
	didDoc.TxNonce = tx.Nonce + 1
	didDoc.TxCountSent = didDoc.TxCountSent + 1
	didDoc.UpdatedAt = blockTimestamp

	if err := DB_OPs.UpdateAccount(accountsClient, didDoc); err != nil {
		return fmt.Errorf("failed to update sender balance and state: %w", err)
	}

	logger().Debug(span_ctx, "Deducted amount from sender and updated state",
		ion.String("account", fromDID.String()),
		ion.String("amount", amount),
		ion.String("old_balance", currentBalance.String()),
		ion.String("new_balance", newBalance.String()),
		ion.Uint64("new_nonce", tx.Nonce+1),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.deductFromSender"),
	)

	return nil
}

// addToRecipient adds an amount to a recipient's account.
// blockTimestamp is used as updatedAt to keep account state deterministic across nodes.
func addToRecipient(span_ctx context.Context, ToAddress common.Address, amount string, accountsClient *config.PooledConnection, blockTimestamp int64) error {
	// Get the current DID document using the provided accounts client
	didDoc, err := DB_OPs.GetAccount(accountsClient, ToAddress)
	if err != nil {
		return fmt.Errorf("failed to retrieve recipient DID %s (account must exist before transfer): %w", ToAddress, err)
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

	// Update the balance and timestamp sequentially using the fetched doc
	didDoc.Balance = newBalance.String()
	didDoc.UpdatedAt = blockTimestamp

	if err := DB_OPs.UpdateAccount(accountsClient, didDoc); err != nil {
		return fmt.Errorf("failed to update recipient balance: %w", err)
	}

	// Log the addition with original format

	return nil
}
