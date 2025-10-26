package BlockProcessing

import (
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/logging"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
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
func ProcessBlockTransactions(block *config.ZKBlock, accountsClient *config.PooledConnection) error {
	// Check if block was already processed
	blockKey := fmt.Sprintf("block_processed:%s", block.BlockHash.Hex())
	processed, err := DB_OPs.Exists(accountsClient, blockKey)
	if err == nil && processed {
		log.Info().Str("block_hash", block.BlockHash.Hex()).Msg("Block already processed, skipping")
		return nil
	}

	ClearProcessedTransactions()

	// Store original balances to enable rollback
	originalBalances := make(map[common.Address]string)
	affectedAccounts := make(map[common.Address]bool)

	// First, collect all affected DIDs from the block
	for _, tx := range block.Transactions {
		affectedAccounts[*tx.From] = true
		affectedAccounts[*tx.To] = true
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

	// Process all transactions
	for _, tx := range sortedTxs {
		// Check if this transaction was already processed within this block
		processedTxsMutex.Lock()
		if processedTxs[tx.Hash.Hex()] {
			log.Warn().Str("tx_hash", tx.Hash.Hex()).Msg("Duplicate transaction in block, skipping")
			processedTxsMutex.Unlock()
			continue
		}
		processedTxs[tx.Hash.Hex()] = true
		processedTxsMutex.Unlock()

		// Check if this transaction was already processed in a previous block
		txKey := fmt.Sprintf("tx_processed:%s", tx.Hash)
		alreadyProcessed, err := DB_OPs.Exists(accountsClient, txKey)
		if err == nil && alreadyProcessed {
			accountsClient.Client.Logger.Logger.Warn("Transaction already processed in previous block, skipping",
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "messaging.BlockProcessing.ProcessBlockTransactions"),
				zap.String("tx_hash", tx.Hash.Hex()),
			)
			continue
		}

		// Process the transaction
		Process_err := processTransaction(tx, *block.CoinbaseAddr, *block.ZKVMAddr, accountsClient)
		if Process_err != nil {
			// If any transaction fails, roll back all affected DIDs
			accountsClient.Client.Logger.Logger.Error("Transaction failed, rolling back block",
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "messaging.ProcessBlockTransactions"),
				zap.String("tx_hash", tx.Hash.Hex()),
				zap.Error(Process_err),
			)
			rollbackError := rollbackBalances(originalBalances, accountsClient)
			if rollbackError != nil {
				accountsClient.Client.Logger.Logger.Error("Failed to rollback balances after transaction failure",
					zap.Time(logging.Created_at, time.Now()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Loki_url, config.LOKI_URL),
					zap.String(logging.Function, "messaging.ProcessBlockTransactions"),
					zap.String("tx_hash", tx.Hash.Hex()),
					zap.Error(rollbackError),
				)
			}

			// Clean up any processing markers for failed transactions
			cleanupProcessingMarkers(accountsClient, tx.Hash.Hex())

			return fmt.Errorf("block processing failed: %w", Process_err)
		}
	}

	// Mark all transactions as successfully processed in the database
	for txHash := range processedTxs {
		txKey := fmt.Sprintf("tx_processed:%s", txHash)
		if err := DB_OPs.Create(accountsClient, txKey, time.Now().Unix()); err != nil {
			log.Warn().Err(err).Str("tx_hash", txHash).Msg("Failed to mark transaction as processed")
		}

		// Clean up the processing key
		processingKey := fmt.Sprintf("tx_processing:%s", txHash)
		if exists, _ := DB_OPs.Exists(accountsClient, processingKey); exists {
			if err := DB_OPs.Update(accountsClient, processingKey, -1); err != nil {
				log.Warn().Err(err).Str("tx_hash", txHash).Msg("Failed to clean up processing marker")
			}
		}
	}

	// Mark the block as processed
	if err := DB_OPs.Create(accountsClient, blockKey, time.Now().Unix()); err != nil {
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
		if err := DB_OPs.Update(nil, processingKey, -1); err != nil {
			accountsClient.Client.Logger.Logger.Warn("Failed to clean up processing marker",
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "messaging.cleanupProcessingMarkers"),
				zap.String("tx_hash", txHash),
				zap.Error(err),
			)
		}
	}

	// Also clean up the transaction lock
	cleanupTransactionLock(txHash)
}

// rollbackBalances restores original balances for all affected DIDs
func rollbackBalances(originalBalances map[common.Address]string, accountsClient *config.PooledConnection) error {
	for did, balance := range originalBalances {
		if err := DB_OPs.UpdateAccountBalance(accountsClient, did, balance); err != nil {
			return fmt.Errorf("failed to restore balance for %s: %w", did, err)
		}
		log.Info().Str("did", did.String()).Str("balance", balance).Msg("Rolled back balance to original value")
	}
	return nil
}

// ProcessTransaction handles a single transaction's balance updates
func processTransaction(tx config.Transaction, coinbaseAddr common.Address, zkvmAddr common.Address, accountsClient *config.PooledConnection) error {
	// Enhanced logging at start
	// First check the connection
	if accountsClient == nil {
		log.Error().Msg("Function: messaging.processTransaction - accountsClient is nil")
		return fmt.Errorf("accountsClient is nil")
	}

	// Confirm the DB connection
	err := DB_OPs.EnsureDBConnection(accountsClient)
	if err != nil {
		log.Error().Err(err).Msg("Failed to establish database connection")
		return fmt.Errorf("failed to establish database connection: %w", err)
	}
	accountsClient.Client.Logger.Logger.Info("Database connection check successful",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "messaging.processTransaction"),
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
				if time.Now().Unix()-timestamp > 300 {
					accountsClient.Client.Logger.Logger.Warn("Found stale processing marker, continuing with transaction",
						zap.Time(logging.Created_at, time.Now()),
						zap.String(logging.Log_file, LOG_FILE),
						zap.String(logging.Topic, TOPIC),
						zap.String(logging.Loki_url, config.LOKI_URL),
						zap.String(logging.Function, "messaging.processTransaction"),
						zap.String("tx_hash", tx.Hash.Hex()),
						zap.Int64("stale_timestamp", timestamp),
					)
				} else {
					accountsClient.Client.Logger.Logger.Warn("Transaction is already being processed, possible duplicate",
						zap.Time(logging.Created_at, time.Now()),
						zap.String(logging.Log_file, LOG_FILE),
						zap.String(logging.Topic, TOPIC),
						zap.String(logging.Loki_url, config.LOKI_URL),
						zap.String(logging.Function, "messaging.processTransaction"),
						zap.String("tx_hash", tx.Hash.Hex()),
					)
					// We have the lock, so continue processing anyway as previous attempt might have failed
				} // We have the lock, so continue processing anyway as previous attempt might have failed
			}
		}
	}

	// Mark transaction as being processed
	if err := DB_OPs.Create(accountsClient, txProcessingKey, time.Now().Unix()); err != nil {
		accountsClient.Client.Logger.Logger.Warn("Failed to mark transaction as processing",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "messaging.processTransaction"),
			zap.String("tx_hash", tx.Hash.Hex()),
			zap.Error(err),
		)
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
		accountsClient.Client.Logger.Logger.Error("Failed to parse transaction",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "messaging.processTransaction"),
			zap.String("tx_hash", tx.Hash.Hex()),
			zap.Error(err),
		)
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
	gasFeeToDeduct = new(big.Int).Div(gasFeeToDeduct, big.NewInt(1000000000000000000))

	// Transaction value will be in Wei
	parsedTx.ValueBig = new(big.Int).Div(parsedTx.ValueBig, big.NewInt(1000000000000000000))

	// Calculate total amount to deduct from sender (amount + gas fee)
	totalDeduction := new(big.Int).Add(parsedTx.ValueBig, gasFeeToDeduct)
	fmt.Println("Total deduction: ", totalDeduction.String())
	// Split the gas fee between coinbase and ZKVM
	halfGasFee := new(big.Int).Div(gasFeeToDeduct, big.NewInt(2))

	accountsClient.Client.Logger.Logger.Info("Transaction Amount Calculated",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "messaging.processTransaction"),
		zap.String("tx_hash", tx.Hash.Hex()),
		zap.String("from", tx.From.Hex()),
		zap.String("to", tx.To.Hex()),
		zap.String("value", parsedTx.ValueBig.String()),
		zap.String("gas_limit", gasLimit.String()),
		zap.String("gas_fee", gasFeeToDeduct.String()),
		zap.String("total_deduction", totalDeduction.String()),
	)

	// Check if sender exists before attempting deduction
	senderExists, _ := accountExists(tx.From, accountsClient)
	if !senderExists {
		accountsClient.Client.Logger.Logger.Error("Sender DID does not exist",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "messaging.processTransaction"),
			zap.String("tx_hash", tx.Hash.Hex()),
			zap.String("from", tx.From.Hex()),
		)
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("sender DID %s does not exist", tx.From)
	}
	fmt.Println("Sender exists: ", senderExists) // Debugging

	// Check if recipient exists (for better error reporting)
	recipientExists, _ := accountExists(tx.To, accountsClient)
	if !recipientExists && !CreateMissingAccounts {
		accountsClient.Client.Logger.Logger.Error("Recipient DID does not exist",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "messaging.processTransaction"),
			zap.String("tx_hash", tx.Hash.Hex()),
			zap.String("to", tx.To.Hex()),
		)
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("recipient DID %s does not exist and automatic creation is disabled", tx.To)
	}
	fmt.Println("Recipient exists: ", recipientExists) // Debugging

	// 1. Deduct from sender
	if err := deductFromSender(*tx.From, totalDeduction.String(), accountsClient); err != nil {
		accountsClient.Client.Logger.Logger.Error("Failed to deduct from sender",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "messaging.processTransaction"),
			zap.String("tx_hash", tx.Hash.Hex()),
			zap.String("from", tx.From.Hex()),
			zap.String("amount", totalDeduction.String()),
		)
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return categorizeDeductionError(err)
	}

	// Debugging
	fmt.Println(">>>>>> Deducted amount from sender: ", totalDeduction.String())

	// 2. Add amount to recipient
	if err := addToRecipient(*tx.To, parsedTx.ValueBig.String(), accountsClient); err != nil {
		// Rollback sender deduction on failure
		if rollbackErr := DB_OPs.UpdateAccountBalance(accountsClient, *tx.From, originalBalances[*tx.From]); rollbackErr != nil {
			accountsClient.Client.Logger.Logger.Error("Failed to rollback sender balance",
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "messaging.processTransaction"),
				zap.String("tx_hash", tx.Hash.Hex()),
				zap.String("from", tx.From.Hex()),
				zap.String("original_balance", originalBalances[*tx.From]),
			)
		} else {
			accountsClient.Client.Logger.Logger.Info("Rolled back sender balance due to recipient update failure",
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "messaging.processTransaction"),
				zap.String("tx_hash", tx.Hash.Hex()),
				zap.String("from", tx.From.Hex()),
				zap.String("original_balance", originalBalances[*tx.From]),
			)
		}
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("failed to add to recipient: %w", err)
	}

	// Debugging
	fmt.Println(">>>>>> Added amount to recipient:", halfGasFee.String(), "with address", tx.To.Hex())

	// 3. Split gas fee between coinbase and ZKVM
	if err := addToRecipient(coinbaseAddr, halfGasFee.String(), accountsClient); err != nil {
		// Rollback previous operations
		rollbackAccounts := []common.Address{*tx.From, *tx.To, coinbaseAddr, zkvmAddr}
		for _, accounts := range rollbackAccounts {
			if rollbackErr := DB_OPs.UpdateAccountBalance(accountsClient, accounts, originalBalances[accounts]); rollbackErr != nil {
				accountsClient.Client.Logger.Logger.Error("Failed to rollback balance",
					zap.Time(logging.Created_at, time.Now()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Loki_url, config.LOKI_URL),
					zap.String(logging.Function, "messaging.processTransaction"),
					zap.String("tx_hash", tx.Hash.Hex()),
					zap.String("from", tx.From.Hex()),
					zap.String("original_balance", originalBalances[*tx.From]),
				)
			} else {
				accountsClient.Client.Logger.Logger.Info("Rolled back balance due to gas fee update failure",
					zap.Time(logging.Created_at, time.Now()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Loki_url, config.LOKI_URL),
					zap.String(logging.Function, "messaging.processTransaction"),
					zap.String("tx_hash", tx.Hash.Hex()),
					zap.String("from", tx.From.Hex()),
					zap.String("original_balance", originalBalances[*tx.From]),
				)
			}
		}
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("failed to add gas fee to coinbase: %w", err)
	}

	// Debugging
	fmt.Println(">>>>>> Added amount to Coinbase:", halfGasFee.String(), "with address", coinbaseAddr.Hex())

	if err := addToRecipient(zkvmAddr, halfGasFee.String(), accountsClient); err != nil {
		// Rollback previous operations
		rollbackAccounts := []common.Address{*tx.From, *tx.To, coinbaseAddr, zkvmAddr}
		for _, accounts := range rollbackAccounts {
			if rollbackErr := DB_OPs.UpdateAccountBalance(accountsClient, accounts, originalBalances[accounts]); rollbackErr != nil {
				accountsClient.Client.Logger.Logger.Error("Failed to rollback balance",
					zap.Time(logging.Created_at, time.Now()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Loki_url, config.LOKI_URL),
					zap.String(logging.Function, "messaging.processTransaction"),
					zap.String("tx_hash", tx.Hash.Hex()),
					zap.String("from", tx.From.Hex()),
					zap.String("original_balance", originalBalances[*tx.From]),
				)
			} else {
				accountsClient.Client.Logger.Logger.Info("Rolled back balance due to gas fee update failure",
					zap.Time(logging.Created_at, time.Now()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Loki_url, config.LOKI_URL),
					zap.String(logging.Function, "messaging.processTransaction"),
					zap.String("tx_hash", tx.Hash.Hex()),
					zap.String("from", tx.From.Hex()),
					zap.String("original_balance", originalBalances[*tx.From]),
				)
			}
		}
		cleanupProcessingMarkers(accountsClient, tx.Hash.String())
		return fmt.Errorf("failed to add gas fee to ZKVM: %w", err)
	}

	// Debugging
	fmt.Println(">>>>>> Added amount to ZKVM:", halfGasFee.String(), "with address", zkvmAddr.Hex())

	// Mark transaction as fully processed - this is the key that prevents double processing
	if err := DB_OPs.Create(nil, txKey, time.Now().Unix()); err != nil {
		accountsClient.Client.Logger.Logger.Error("Failed to mark transaction as processed",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "messaging.processTransaction"),
			zap.String("tx_hash", tx.Hash.String()),
		)
		// Still continue as the transaction was processed successfully
	}

	// Clean up the processing marker
	cleanupProcessingMarkers(accountsClient, tx.Hash.String())

	accountsClient.Client.Logger.Logger.Info("Transaction processed successfully",
		zap.String("tx_hash", tx.Hash.String()),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "messaging.processTransaction"),
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
func deductFromSender(fromDID common.Address, amount string, accountsClient *config.PooledConnection) error {
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

	accountsClient.Client.Logger.Logger.Info("Deducted amount from sender",
		zap.String(logging.Account, fromDID.String()),
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DB_OPs.UpdateAccountBalance"),
	)

	return nil
}

// addToRecipient adds an amount to a recipient's DID account
func addToRecipient(ToAddress common.Address, amount string, accountsClient *config.PooledConnection) error {
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

	accountsClient.Client.Logger.Logger.Info("Added amount to recipient",
		zap.String(logging.Account, ToAddress.String()),
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, config.LOKI_URL),
		zap.String(logging.Function, "DB_OPs.UpdateAccountBalance"),
	)

	return nil
}
