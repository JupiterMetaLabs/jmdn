package BlockProcessing

import (
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/SmartContract"
	"gossipnode/config"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/holiman/uint256"
	"github.com/rs/zerolog/log"
)

const (
	LOG_FILE = "block_processing.log"
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

// ProcessBlockTransactions processes all transactions in a block atomically
// If any transaction fails, all are rolled back.
// If commitToDB is true, state changes are persisted to the database.
func ProcessBlockTransactions(block *config.ZKBlock, accountsClient *config.PooledConnection, commitToDB bool) error {
	fmt.Printf("=== DEBUG: ProcessBlockTransactions called for block %d (Commit: %v) ===\n", block.BlockNumber, commitToDB)

	// Note: StateDB is NOT initialized here for regular transactions
	// It will be created on-demand inside processTransaction() only for smart contract transactions

	// Check if block was already processed
	blockKey := fmt.Sprintf("block_processed:%s", block.BlockHash.Hex())
	fmt.Printf("DEBUG: Checking if block already processed with key: %s\n", blockKey)
	processed, err := DB_OPs.Exists(accountsClient, blockKey)
	if err == nil && processed {
		fmt.Printf("DEBUG: Block %s already processed, skipping\n", block.BlockHash.Hex())
		log.Info().Str("block_hash", block.BlockHash.Hex()).Msg("Block already processed, skipping")
		return nil
	}
	fmt.Printf("DEBUG: Block not previously processed, continuing...\n")

	ClearProcessedTransactions()

	// Store original balances to enable rollback
	originalBalances := make(map[common.Address]string)
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

	// Fetch and store original balances
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
	fmt.Printf("DEBUG: Found %d transactions to process\n", len(sortedTxs))

	// Process all transactions
	for i, tx := range sortedTxs {
		fmt.Printf("DEBUG: Processing transaction %d/%d - Hash: %s\n", i+1, len(sortedTxs), tx.Hash.Hex())

		// Check if this transaction was already processed within this block
		processedTxsMutex.Lock()
		if processedTxs[tx.Hash.Hex()] {
			fmt.Printf("DEBUG: Transaction %s already processed in this block, skipping\n", tx.Hash.Hex())
			log.Warn().Str("tx_hash", tx.Hash.Hex()).Msg("Duplicate transaction in block, skipping")
			processedTxsMutex.Unlock()
			continue
		}
		processedTxs[tx.Hash.Hex()] = true
		processedTxsMutex.Unlock()

		// Check if this transaction was already processed in a previous block
		txKey := fmt.Sprintf("tx_processed:%s", tx.Hash)
		fmt.Printf("DEBUG: Checking if transaction already processed with key: %s\n", txKey)
		alreadyProcessed, err := DB_OPs.Exists(accountsClient, txKey)
		if err == nil && alreadyProcessed {
			fmt.Printf("DEBUG: Transaction %s already processed in previous block, skipping\n", tx.Hash.Hex())
			continue
		}

		// Process transaction (State DB created inside if it's a smart contract)
		if err := processTransaction(tx, *block.CoinbaseAddr, *block.ZKVMAddr, accountsClient, commitToDB); err != nil {
			fmt.Printf("DEBUG: processTransaction failed for tx %s: %v\n", tx.Hash.Hex(), err)
			// If any transaction fails, roll back all affected DIDs
			rollbackError := rollbackBalances(originalBalances, accountsClient)
			if rollbackError != nil {
			}

			// Clean up any processing markers for failed transactions
			cleanupProcessingMarkers(accountsClient, tx.Hash.Hex())

			return fmt.Errorf("block processing failed: %w", err)
		}
	}

	// Mark all transactions as successfully processed in the database
	for txHash := range processedTxs {
		txKey := fmt.Sprintf("tx_processed:%s", txHash)
		if err := DB_OPs.Create(accountsClient, txKey, time.Now().UTC().Unix()); err != nil {
			log.Warn().Err(err).Str("tx_hash", txHash).Msg("Failed to mark transaction as processed")
		}

		// Clean up the processing key
		processingKey := fmt.Sprintf("tx_processing:%s", txHash)
		if exists, _ := DB_OPs.Exists(accountsClient, processingKey); exists {
			if err := DB_OPs.Create(accountsClient, processingKey, int64(-1)); err != nil {
				log.Warn().Err(err).Str("tx_hash", txHash).Msg("Failed to clean up processing marker")
			}
		}
	}

	// Mark the block as processed (regular transactions already committed via DB_OPs)
	if err := DB_OPs.Create(accountsClient, blockKey, time.Now().UTC().Unix()); err != nil {
		log.Warn().Err(err).Str("block_hash", block.BlockHash.Hex()).Msg("Failed to mark block as processed")
	}

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
func cleanupProcessingMarkers(accountsClient *config.PooledConnection, txHash string) {
	processingKey := fmt.Sprintf("tx_processing:%s", txHash)
	if exists, _ := DB_OPs.Exists(accountsClient, processingKey); exists {
		if err := DB_OPs.Create(accountsClient, processingKey, int64(-1)); err != nil {
		}
	}

	// Also clean up the transaction lock
	cleanupTransactionLock(txHash)
}

// rollbackBalances restores original balances for all affected DIDs
func rollbackBalances(originalBalances map[common.Address]string, accountsClient *config.PooledConnection) error {
	for did, balance := range originalBalances {
		// Optimization: If original balance is "0", checking if account exists first can avoid "key not found" error
		// when trying to update a non-existent account (e.g. 0x...02 or new contract)
		if balance == "0" {
			_, err := DB_OPs.GetAccount(accountsClient, did)
			if err != nil {
				// If account doesn't exist and we want to roll it back to 0, just do nothing (it's effectively 0)
				// avoiding the "key not found" error on UpdateAccountBalance
				log.Info().Str("did", did.String()).Msg("Skipping rollback for non-existent account (original balance was 0)")
				continue
			}
		}

		if err := DB_OPs.UpdateAccountBalance(accountsClient, did, balance); err != nil {
			// If key not found (and we didn't catch it above), log warning but continue rolling back others
			if strings.Contains(err.Error(), "key not found") {
				log.Warn().Str("did", did.String()).Msg("Skipping rollback for non-existent account (key not found)")
				continue
			}
			return fmt.Errorf("failed to restore balance for %s: %w", did, err)
		}
		log.Info().Str("did", did.String()).Str("balance", balance).Msg("Rolled back balance to original value")
	}
	return nil
}

// ProcessTransaction handles a single transaction's balance updates.
// For smart contracts, a StateDB is created and changes are committed based on commitToDB flag.
// For regular transfers, DB_OPs is used directly (always commits).
func processTransaction(tx config.Transaction, coinbaseAddr common.Address, zkvmAddr common.Address, accountsClient *config.PooledConnection, commitToDB bool) error {
	// Enhanced logging at start
	// First check the connection
	if accountsClient == nil {
		fmt.Println("DEBUG: accountsClient is nil!")
		log.Error().Msg("Function: messaging.processTransaction - accountsClient is nil")
		return fmt.Errorf("accountsClient is nil")
	}

	// Confirm the DB connection
	err := DB_OPs.EnsureDBConnection(accountsClient)
	if err != nil {
		log.Error().Err(err).Msg("Failed to establish database connection")
		return fmt.Errorf("failed to establish database connection: %w", err)
	}

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

	// Only create StateDB for smart contracts (variables declared below in regular transfer section)
	if isContract {
		stateDB, err = SmartContract.NewStateDB(GlobalChainID)
		if err != nil {
			return fmt.Errorf("failed to initialize StateDB for contract: %w", err)
		}
		snapshot = stateDB.Snapshot()
	}

	// ========== CONTRACT DEPLOYMENT ==========
	if tx.To == nil && tx.Type == 2 {

		log.Info().Str("tx_hash", tx.Hash.Hex()).Msg("🚀 [CONSENSUS] CONTRACT DEPLOYMENT detected")

		// Call SmartContract module's deployment processor with StateDB
		result, err := SmartContract.ProcessContractDeployment(&tx, stateDB, GlobalChainID)
		if err != nil {
			stateDB.RevertToSnapshot(snapshot) // Rollback
			log.Error().Err(err).Str("tx_hash", tx.Hash.Hex()).Msg("❌ [CONSENSUS] Contract deployment failed")
			cleanupProcessingMarkers(accountsClient, tx.Hash.Hex())
			return fmt.Errorf("contract deployment failed: %w", err)
		}

		if !result.Success {
			stateDB.RevertToSnapshot(snapshot) // Rollback
			log.Error().Str("tx_hash", tx.Hash.Hex()).Msg("❌ [CONSENSUS] Contract deployment unsuccessful")
			return result.Error
		}

		// Handle gas fees
		parsedTx, err := parseTransaction(tx)
		if err != nil {
			stateDB.RevertToSnapshot(snapshot)
			return fmt.Errorf("failed to parse transaction for gas: %w", err)
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
			return fmt.Errorf("gas fee amount overflow")
		}
		stateDB.SubBalance(*tx.From, gasDeductAmount, tracing.BalanceChangeTransfer)

		// Note: Value transfer to contract is handled by EVM's Create() via transferFn
		// No manual transfer needed here to avoid double-counting

		// Pay coinbase their share of gas fees
		coinbaseAmount, overflow := uint256.FromBig(coinbaseGasFee)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return fmt.Errorf("coinbase gas fee overflow")
		}
		stateDB.AddBalance(coinbaseAddr, coinbaseAmount, tracing.BalanceChangeTransfer)

		// Pay ZKVM their share of gas fees
		zkvmAmount, overflow := uint256.FromBig(zkvmGasFee)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return fmt.Errorf("zkvm gas fee overflow")
		}
		stateDB.AddBalance(zkvmAddr, zkvmAmount, tracing.BalanceChangeTransfer)

		log.Info().Str("contract", result.ContractAddress.Hex()).Msg("💰 Gas fees processed for deployment")

		// Commit StateDB changes if requested
		if commitToDB {
			log.Info().Msg("💾 Committing contract deployment state to database")

			// Update balances in DID service before committing StateDB
			for addr, balance := range stateDB.GetBalanceChanges() {
				if err := DB_OPs.UpdateAccountBalance(accountsClient, addr, balance.String()); err != nil {
					return fmt.Errorf("failed to update DID service balance for %s: %w", addr.Hex(), err)
				}
			}

			if _, err := stateDB.CommitToDB(false); err != nil {
				return fmt.Errorf("failed to commit contract deployment state: %w", err)
			}
		} else {
			log.Info().Msg("🚫 Skipping state commit (verification mode)")
		}

		return nil
	}

	// ========== SMART CONTRACT EXECUTION DETECTION ==========
	// Check if this is a transaction to an existing contract (To != nil and has code)
	// We use stateDB.GetCodeSize to check if the target address is a contract
	if tx.To != nil && stateDB.GetCodeSize(*tx.To) > 0 {
		log.Info().Str("tx_hash", tx.Hash.Hex()).Msg("⚙️ [CONSENSUS] CONTRACT EXECUTION detected")

		// Call SmartContract module's execution processor with StateDB
		result, err := SmartContract.ProcessContractExecution(&tx, stateDB, GlobalChainID)
		if err != nil {
			stateDB.RevertToSnapshot(snapshot) // Rollback
			log.Error().Err(err).Str("tx_hash", tx.Hash.Hex()).Msg("❌ [CONSENSUS] Contract execution failed")
			cleanupProcessingMarkers(accountsClient, tx.Hash.Hex())
			return fmt.Errorf("contract execution failed: %w", err)
		}

		// Handle gas fees
		parsedTx, err := parseTransaction(tx)
		if err != nil {
			stateDB.RevertToSnapshot(snapshot)
			return fmt.Errorf("failed to parse transaction for gas: %w", err)
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
			return fmt.Errorf("gas fee amount overflow")
		}
		stateDB.SubBalance(*tx.From, gasDeductAmount, tracing.BalanceChangeTransfer)

		// Note: Value transfer to contract is handled by EVM's Call() via transferFn
		// No manual transfer needed here to avoid double-counting

		// Pay coinbase their share of gas fees
		coinbaseExecAmount, overflow := uint256.FromBig(coinbaseGasFee)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return fmt.Errorf("coinbase gas fee overflow")
		}
		stateDB.AddBalance(coinbaseAddr, coinbaseExecAmount, tracing.BalanceChangeTransfer)

		// Pay ZKVM their share of gas fees
		zkvmExecAmount, overflow := uint256.FromBig(zkvmGasFee)
		if overflow {
			stateDB.RevertToSnapshot(snapshot)
			return fmt.Errorf("zkvm gas fee overflow")
		}
		stateDB.AddBalance(zkvmAddr, zkvmExecAmount, tracing.BalanceChangeTransfer)

		log.Info().Str("contract", tx.To.Hex()).Msg("💰 Gas fees processed for execution")

		// Commit StateDB changes if requested
		if commitToDB {
			log.Info().Msg("💾 Committing contract execution state to database")

			// Update balances in DID service before committing StateDB
			for addr, balance := range stateDB.GetBalanceChanges() {
				if err := DB_OPs.UpdateAccountBalance(accountsClient, addr, balance.String()); err != nil {
					return fmt.Errorf("failed to update DID service balance for %s: %w", addr.Hex(), err)
				}
			}

			if _, err := stateDB.CommitToDB(false); err != nil {
				return fmt.Errorf("failed to commit contract execution state: %w", err)
			}
		} else {
			log.Info().Msg("🚫 Skipping state commit (verification mode)")
		}

		return nil
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
		log.Info().Str("tx_hash", tx.Hash.Hex()).Msg("Transaction already processed in previous block, skipping")
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

	// Store original balances for rollback if needed
	originalBalances := make(map[common.Address]string)
	affectedDIDs := []common.Address{*tx.From, *tx.To, coinbaseAddr, zkvmAddr}

	for _, did := range affectedDIDs {
		fmt.Println("Getting account for: ", did.Hex()) // Debugging
		doc, err := DB_OPs.GetAccount(accountsClient, did)
		if err == nil {
			fmt.Println("Account found, balance: ", doc.Balance) // Debugging
			originalBalances[did] = doc.Balance
		} else if err == DB_OPs.ErrNotFound || strings.Contains(err.Error(), "key not found") {
			fmt.Println("Account not found, using 0 balance for: ", did.Hex()) // Debugging
			originalBalances[did] = "0"
		} else {
			fmt.Println("Error retrieving account for: ", did.Hex(), "Error: ", err.Error()) // Debugging
			return fmt.Errorf("failed to retrieve original balance for %s: %w", did.Hex(), err)
		}
	}

	// Parse the transaction values
	var parsedTx *config.ParsedZKTransaction
	parsedTx, err = parseTransaction(tx)
	if err != nil {
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
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
	fmt.Println("Total deduction: ", totalDeduction.String())
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
		return fmt.Errorf("sender DID %s does not exist", tx.From)
	}
	fmt.Println("Sender exists: ", senderExists) // Debugging

	// Check if recipient exists (for better error reporting)
	recipientExists, _ := accountExists(tx.To, accountsClient)
	if !recipientExists && !CreateMissingAccounts {
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("recipient DID %s does not exist and automatic creation is disabled", tx.To)
	}
	fmt.Println("Recipient exists: ", recipientExists) // Debugging

	// ========== REGULAR TRANSFER: Create StateDB ==========
	// All transactions now use StateDB for Ethereum-style verification
	stateDB, err = SmartContract.NewStateDB(GlobalChainID)
	if err != nil {
		return fmt.Errorf("failed to create StateDB for regular transfer: %w", err)
	}
	snapshot = stateDB.Snapshot()

	// 1. Deduct from sender
	if err := deductFromSender(*tx.From, totalDeduction.String(), stateDB, accountsClient); err != nil {
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return categorizeDeductionError(err)
	}

	// Debugging
	fmt.Println(">>>>>> Deducted amount from sender: ", totalDeduction.String())

	// 2. Add amount to recipient
	if err := addToRecipient(*tx.To, parsedTx.ValueBig.String(), stateDB, accountsClient); err != nil {
		// Rollback using StateDB snapshot
		stateDB.RevertToSnapshot(snapshot)
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("failed to add to recipient: %w", err)
	}

	// Debugging
	fmt.Println(">>>>>> Added amount to recipient:", parsedTx.ValueBig.String(), "with address", tx.To.Hex())

	// 3. Split gas fee between coinbase and ZKVM
	if err := addToRecipient(coinbaseAddr, coinbaseGasFee.String(), stateDB, accountsClient); err != nil {
		// Rollback using StateDB snapshot
		stateDB.RevertToSnapshot(snapshot)
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("failed to add gas fee to coinbase: %w", err)
	}

	// Debugging
	fmt.Println(">>>>>> Added amount to Coinbase:", coinbaseGasFee.String(), "with address", coinbaseAddr.Hex())

	if err := addToRecipient(zkvmAddr, zkvmGasFee.String(), stateDB, accountsClient); err != nil {
		// Rollback using StateDB snapshot
		stateDB.RevertToSnapshot(snapshot)
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("failed to add gas fee to ZKVM: %w", err)
	}

	// Debugging
	fmt.Println(">>>>>> Added amount to ZKVM:", zkvmGasFee.String(), "with address", zkvmAddr.Hex())

	// Commit StateDB if requested (Ethereum-style)
	if commitToDB {
		log.Info().Msg("💾 Committing regular transfer state to database")

		// Update balances in DID service before committing StateDB
		for addr, balance := range stateDB.GetBalanceChanges() {
			if err := DB_OPs.UpdateAccountBalance(accountsClient, addr, balance.String()); err != nil {
				return fmt.Errorf("failed to update DID service balance for %s: %w", addr.Hex(), err)
			}
		}

		if _, err := stateDB.CommitToDB(false); err != nil {
			return fmt.Errorf("failed to commit regular transfer state: %w", err)
		}
	} else {
		log.Info().Msg("🚫 Skipping state commit for regular transfer (verification mode)")
	}

	// Mark transaction as fully processed - this is the key that prevents double processing
	if err := DB_OPs.Create(accountsClient, txKey, time.Now().UTC().Unix()); err != nil {
		// Still continue as the transaction was processed successfully
	}

	// Clean up the processing marker
	cleanupProcessingMarkers(accountsClient, tx.Hash.String())

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

// deductFromSender deducts an amount from a sender's DID account using StateDB
func deductFromSender(fromDID common.Address, amount string, stateDB SmartContract.StateDB, accountsClient *config.PooledConnection) error {
	// Parse amount to deduct
	deductAmount, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return fmt.Errorf("invalid deduction amount: %s", amount)
	}

	// Convert to uint256
	amt, overflow := uint256.FromBig(deductAmount)
	if overflow {
		return fmt.Errorf("deduction amount overflow")
	}

	// Check balance using StateDB
	currentBalance := stateDB.GetBalance(fromDID)
	if currentBalance.Cmp(amt) < 0 {
		return fmt.Errorf("insufficient balance for DID %s: has %s, needs %s",
			fromDID.Hex(), currentBalance.String(), amt.String())
	}

	// Deduct using StateDB
	stateDB.SubBalance(fromDID, amt, tracing.BalanceChangeTransfer)

	// Log the deduction with original format

	return nil
}

// addToRecipient adds an amount to a recipient's DID account using StateDB
func addToRecipient(ToAddress common.Address, amount string, stateDB SmartContract.StateDB, accountsClient *config.PooledConnection) error {
	// Parse amount to add
	addAmount, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return fmt.Errorf("invalid addition amount: %s", amount)
	}

	// Convert to uint256
	amt, overflow := uint256.FromBig(addAmount)
	if overflow {
		return fmt.Errorf("addition amount overflow")
	}

	// Add using StateDB (automatically creates account if needed)
	stateDB.AddBalance(ToAddress, amt, tracing.BalanceChangeTransfer)

	// Log the addition with original format

	return nil
}
