package BlockProcessing

import (
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Global map to track processed transactions during block processing
var (
    processedTxs = make(map[string]bool)
    processedTxsMutex sync.Mutex
    txProcessingLocks = make(map[string]*sync.Mutex)
    txLocksGuard = sync.Mutex{}
    
    // Configurable defaults that can be adjusted as needed
    DefaultGasLimit = int64(21000)
    DefaultGasPrice = int64(1000000000) // 1 Gwei
    CreateMissingAccounts = true // Set to false to disable automatic DID creation
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
func ProcessBlockTransactions(block *config.ZKBlock, accountsClient *config.ImmuClient) error {
    // Check if block was already processed
    blockKey := fmt.Sprintf("block_processed:%s", block.BlockHash.Hex())
    processed, err := DB_OPs.Exists(accountsClient, blockKey)
    if err == nil && processed {
        log.Info().Str("block_hash", block.BlockHash.Hex()).Msg("Block already processed, skipping")
        return nil
    }
    
    ClearProcessedTransactions()
    
    // Store original balances to enable rollback
    originalBalances := make(map[string]string)
    affectedDIDs := make(map[string]bool)
    
    // First, collect all affected DIDs from the block
    for _, tx := range block.Transactions {
        affectedDIDs[tx.From] = true
        affectedDIDs[tx.To] = true
    }
    affectedDIDs[block.CoinbaseAddr] = true
    affectedDIDs[block.ZKVMAddr] = true
    
    // Fetch and store original balances
    for did := range affectedDIDs {
        doc, err := DB_OPs.GetDID(accountsClient, did)
        if err == nil {
            originalBalances[did] = doc.Balance
        } else {
            // DID doesn't exist yet, so original balance is 0
            originalBalances[did] = "0"
        }
    }
    
    // Sort transactions by nonce if available to ensure proper ordering
    sortedTxs := sortTransactionsByNonce(block.Transactions)
    
    // Process all transactions
    for _, tx := range sortedTxs {
        // Check if this transaction was already processed within this block
        processedTxsMutex.Lock()
        if processedTxs[tx.Hash] {
            log.Warn().Str("tx_hash", tx.Hash).Msg("Duplicate transaction in block, skipping")
            processedTxsMutex.Unlock()
            continue
        }
        processedTxs[tx.Hash] = true
        processedTxsMutex.Unlock()
        
        // Check if this transaction was already processed in a previous block
        txKey := fmt.Sprintf("tx_processed:%s", tx.Hash)
        alreadyProcessed, err := DB_OPs.Exists(accountsClient, txKey)
        if err == nil && alreadyProcessed {
            log.Warn().Str("tx_hash", tx.Hash).Msg("Transaction already processed in previous block, skipping")
            continue
        }
        
        // Process the transaction
        Process_err := processTransaction(tx, block.CoinbaseAddr, block.ZKVMAddr, accountsClient)
        if Process_err != nil {
            // If any transaction fails, roll back all affected DIDs
            log.Error().Err(Process_err).Str("tx_hash", tx.Hash).Msg("Transaction failed, rolling back block")
            rollbackError := rollbackBalances(originalBalances, accountsClient)
            if rollbackError != nil {
                log.Error().Err(rollbackError).Msg("Failed to rollback balances after transaction failure")
            }
            
            // Clean up any processing markers for failed transactions
            cleanupProcessingMarkers(accountsClient, tx.Hash)
            
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
func sortTransactionsByNonce(txs []config.ZKBlockTransaction) []config.ZKBlockTransaction {
    // Create a copy to avoid modifying the original
    sortedTxs := make([]config.ZKBlockTransaction, len(txs))
    copy(sortedTxs, txs)
    
    // Group transactions by sender address
    txsBySender := make(map[string][]config.ZKBlockTransaction)
    for _, tx := range sortedTxs {
        txsBySender[tx.From] = append(txsBySender[tx.From], tx)
    }
    
    // Sort each sender's transactions by nonce
    for sender, senderTxs := range txsBySender {
        sort.Slice(senderTxs, func(i, j int) bool {
            // If nonce is missing, maintain original order
            if senderTxs[i].Nonce == "" || senderTxs[j].Nonce == "" {
                return i < j
            }
            
            // Parse nonces as big.Int
            nonceI, okI := new(big.Int).SetString(senderTxs[i].Nonce, 10)
            nonceJ, okJ := new(big.Int).SetString(senderTxs[j].Nonce, 10)
            
            // If parsing fails, maintain original order
            if !okI || !okJ {
                return i < j
            }
            
            // Sort by nonce
            return nonceI.Cmp(nonceJ) < 0
        })
        
        txsBySender[sender] = senderTxs
    }
    
    // Rebuild the sorted transaction list
    result := []config.ZKBlockTransaction{}
    for _, senderTxs := range txsBySender {
        result = append(result, senderTxs...)
    }
    
    return result
}

// cleanupProcessingMarkers removes temporary processing markers
func cleanupProcessingMarkers(accountsClient *config.ImmuClient, txHash string) {
    processingKey := fmt.Sprintf("tx_processing:%s", txHash)
    if exists, _ := DB_OPs.Exists(accountsClient, processingKey); exists {
        if err := DB_OPs.Update(accountsClient, processingKey, -1); err != nil {
            log.Warn().Err(err).Str("tx_hash", txHash).Msg("Failed to clean up processing marker")
        }
    }
    
    // Also clean up the transaction lock
    cleanupTransactionLock(txHash)
}

// rollbackBalances restores original balances for all affected DIDs
func rollbackBalances(originalBalances map[string]string, accountsClient *config.ImmuClient) error {
    for did, balance := range originalBalances {
        if err := DB_OPs.UpdateDIDBalance(accountsClient, did, balance); err != nil {
            return fmt.Errorf("failed to restore balance for %s: %w", did, err)
        }
        log.Info().Str("did", did).Str("balance", balance).Msg("Rolled back balance to original value")
    }
    return nil
}

// ProcessTransaction handles a single transaction's balance updates 
func processTransaction(tx config.ZKBlockTransaction, coinbaseAddr, zkvmAddr string, accountsClient *config.ImmuClient) error {
    // Enhanced logging at start
    log.Info().
        Str("tx_hash", tx.Hash).
        Str("from", tx.From).
        Str("to", tx.To).
        Str("value", tx.Value).
        Str("type", tx.Type).
        Str("nonce", tx.Nonce).
        Msg("Starting transaction processing")
        
    // Check if transaction was already processed (from previous blocks)
    txLock := getTransactionLock(tx.Hash)
    txLock.Lock()
    defer func() {
        txLock.Unlock()
        cleanupTransactionLock(tx.Hash) // Always clean up the lock
    }()
    
    // First check with a preliminary key that shows we've started processing
    txProcessingKey := fmt.Sprintf("tx_processing:%s", tx.Hash)
    txKey := fmt.Sprintf("tx_processed:%s", tx.Hash)
    
    // Check if already completed
    processed, err := DB_OPs.Exists(accountsClient, txKey)
    if err == nil && processed {
        log.Info().Str("tx_hash", tx.Hash).Msg("Transaction already processed in previous block, skipping")
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
                if time.Now().Unix() - timestamp > 300 {
                    log.Warn().
                        Str("tx_hash", tx.Hash).
                        Int64("stale_timestamp", timestamp).
                        Msg("Found stale processing marker, continuing with transaction")
                } else {
                    log.Warn().Str("tx_hash", tx.Hash).Msg("Transaction is already being processed, possible duplicate")
                    // We have the lock, so continue processing anyway as previous attempt might have failed
                }// We have the lock, so continue processing anyway as previous attempt might have failed
            }
        }
    }

    // Mark transaction as being processed
    if err := DB_OPs.Create(accountsClient, txProcessingKey, time.Now().Unix()); err != nil {
        log.Warn().Err(err).Str("tx_hash", tx.Hash).Msg("Failed to mark transaction as processing")
        // Continue processing since this is just a precaution
    }

    // Store original balances for rollback if needed
    originalBalances := make(map[string]string)
    affectedDIDs := []string{tx.From, tx.To, coinbaseAddr, zkvmAddr}
    
    for _, did := range affectedDIDs {
        doc, err := DB_OPs.GetDID(accountsClient, did)
        if err == nil {
            originalBalances[did] = doc.Balance
        } else if err.Error() == "DID not found" {
            originalBalances[did] = "0"
        }
    }

    // Parse the transaction values
    var parsedTx *config.ParsedZKTransaction
    parsedTx, err = parseTransaction(tx)
    if err != nil {
        log.Error().Err(err).Str("tx_hash", tx.Hash).Msg("Failed to parse transaction")
        cleanupProcessingMarkers(accountsClient, tx.Hash)
        return fmt.Errorf("failed to parse transaction: %w", err)
    }

    // Get gas limit
    gasLimit := new(big.Int)
    var ok bool
    if tx.GasLimit != "" {
        gasLimit, ok = gasLimit.SetString(tx.GasLimit, 10)
        if !ok {
            gasLimit = big.NewInt(DefaultGasLimit) // Use configurable default
        }
    } else {
        gasLimit = big.NewInt(DefaultGasLimit) // Use configurable default
    }

    // Calculate gas fee (gasLimit * gasPrice / 1,000,000,000)
    gasFeeToDeduct := new(big.Int).Mul(gasLimit, parsedTx.EffectiveGasFee)
    gasFeeToDeduct = new(big.Int).Div(gasFeeToDeduct, big.NewInt(1000000000))
    
    // Calculate total amount to deduct from sender (amount + gas fee)
    totalDeduction := new(big.Int).Add(parsedTx.ValueBig, gasFeeToDeduct)
    
    // Split the gas fee between coinbase and ZKVM
    halfGasFee := new(big.Int).Div(gasFeeToDeduct, big.NewInt(2))
    
    log.Info().
        Str("tx_hash", tx.Hash).
        Str("from", tx.From).
        Str("to", tx.To).
        Str("value", parsedTx.ValueBig.String()).
        Str("gas_limit", gasLimit.String()).
        Str("gas_fee", gasFeeToDeduct.String()).
        Str("total_deduction", totalDeduction.String()).
        Msg("Transaction amounts calculated")

    // Check if sender exists before attempting deduction
    senderExists, _ := didExists(accountsClient, tx.From)
    if !senderExists {
        log.Error().
            Str("tx_hash", tx.Hash).
            Str("from", tx.From).
            Msg("Sender DID does not exist")
        cleanupProcessingMarkers(accountsClient, tx.Hash)
        return fmt.Errorf("sender DID %s does not exist", tx.From)
    }
    
    // Check if recipient exists (for better error reporting)
    recipientExists, _ := didExists(accountsClient, tx.To)
    if !recipientExists && !CreateMissingAccounts {
        log.Error().
            Str("tx_hash", tx.Hash).
            Str("to", tx.To).
            Msg("Recipient DID does not exist and automatic creation is disabled")
        cleanupProcessingMarkers(accountsClient, tx.Hash)
        return fmt.Errorf("recipient DID %s does not exist and automatic creation is disabled", tx.To)
    }

    // 1. Deduct from sender
    if err := deductFromSender(tx.From, totalDeduction.String(), accountsClient); err != nil {
        log.Error().Err(err).
            Str("tx_hash", tx.Hash).
            Str("from", tx.From).
            Str("amount", totalDeduction.String()).
            Msg("Failed to deduct from sender")
        cleanupProcessingMarkers(accountsClient, tx.Hash)
        return categorizeDeductionError(err)
    }

    // 2. Add amount to recipient
    if err := addToRecipient(tx.To, parsedTx.ValueBig.String(), accountsClient); err != nil {
        // Rollback sender deduction on failure
        if rollbackErr := DB_OPs.UpdateDIDBalance(accountsClient, tx.From, originalBalances[tx.From]); rollbackErr != nil {
            log.Error().Err(rollbackErr).
                Str("did", tx.From).
                Str("original_balance", originalBalances[tx.From]).
                Msg("Failed to rollback sender balance")
        } else {
            log.Info().
                Str("did", tx.From).
                Str("balance", originalBalances[tx.From]).
                Msg("Rolled back sender balance due to recipient update failure")
        }
        cleanupProcessingMarkers(accountsClient, tx.Hash)
        return fmt.Errorf("failed to add to recipient: %w", err)
    }

    // 3. Split gas fee between coinbase and ZKVM
    if err := addToRecipient(coinbaseAddr, halfGasFee.String(), accountsClient); err != nil {
        // Rollback previous operations
        rollbackDIDs := []string{tx.From, tx.To}
        for _, did := range rollbackDIDs {
            if rollbackErr := DB_OPs.UpdateDIDBalance(accountsClient, did, originalBalances[did]); rollbackErr != nil {
                log.Error().Err(rollbackErr).Str("did", did).Msg("Failed to rollback balance")
            } else {
                log.Info().Str("did", did).Str("balance", originalBalances[did]).Msg("Rolled back balance")
            }
        }
        cleanupProcessingMarkers(accountsClient, tx.Hash)
        return fmt.Errorf("failed to add gas fee to coinbase: %w", err)
    }

    if err := addToRecipient(zkvmAddr, halfGasFee.String(), accountsClient); err != nil {
        // Rollback previous operations
        rollbackDIDs := []string{tx.From, tx.To, coinbaseAddr}
        for _, did := range rollbackDIDs {
            if rollbackErr := DB_OPs.UpdateDIDBalance(accountsClient, did, originalBalances[did]); rollbackErr != nil {
                log.Error().Err(rollbackErr).Str("did", did).Msg("Failed to rollback balance")
            } else {
                log.Info().Str("did", did).Str("balance", originalBalances[did]).Msg("Rolled back balance")
            }
        }
        cleanupProcessingMarkers(accountsClient, tx.Hash)
        return fmt.Errorf("failed to add gas fee to ZKVM: %w", err)
    }

    // Mark transaction as fully processed - this is the key that prevents double processing
    if err := DB_OPs.Create(accountsClient, txKey, time.Now().Unix()); err != nil {
        log.Warn().Err(err).Str("tx_hash", tx.Hash).Msg("Failed to mark transaction as processed")
        // Still continue as the transaction was processed successfully
    }
    
    // Clean up the processing marker
    cleanupProcessingMarkers(accountsClient, tx.Hash)

    log.Info().
        Str("tx_hash", tx.Hash).
        Str("from", tx.From).
        Str("to", tx.To).
        Str("value", parsedTx.ValueBig.String()).
        Str("gas_fee", gasFeeToDeduct.String()).
        Msg("Transaction processed successfully")

    return nil
}

// didExists checks if a DID exists in the database
func didExists(accountsClient *config.ImmuClient, did string) (bool, error) {
    _, err := DB_OPs.GetDID(accountsClient, did)
    if err != nil {
        if err.Error() == "DID not found" {
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
func parseTransaction(tx config.ZKBlockTransaction) (*config.ParsedZKTransaction, error) {
    parsed := &config.ParsedZKTransaction{
        Original: &tx,
    }

    // Parse value
    value := new(big.Int)
    var ok bool
    value, ok = value.SetString(tx.Value, 10)
    if !ok {
        return nil, fmt.Errorf("invalid value: %s", tx.Value)
    }
    parsed.ValueBig = value

    // Determine gas fee based on transaction type
    if tx.Type == "eip1559" || tx.Type == "EIP-1559" {
        // Use max fee for EIP-1559 tx
        maxFee := new(big.Int)
        if tx.MaxFee != "" {
            maxFee, ok = maxFee.SetString(tx.MaxFee, 10)
            if !ok {
                // Default to a reasonable value if not specified
                maxFee = big.NewInt(DefaultGasPrice)
            }
        } else {
            maxFee = big.NewInt(DefaultGasPrice)
        }
        parsed.MaxFeeBig = maxFee
        parsed.EffectiveGasFee = maxFee
    } else {
        // For legacy transactions, try to get gas price from either MaxFee field or MaxPriorityFee
        var gasPrice *big.Int
        gasPrice = new(big.Int)
        ok = false
        
        // First try MaxFee field which might be used for gas price
        if tx.MaxFee != "" {
            gasPrice, ok = gasPrice.SetString(tx.MaxFee, 10)
        }
        
        // If that failed, try MaxPriorityFee field if it exists
        if !ok && tx.MaxPriorityFee != "" {
            gasPrice, ok = gasPrice.SetString(tx.MaxPriorityFee, 10)
        }
        
        // If still no valid gas price, use default
        if !ok || gasPrice.Cmp(big.NewInt(0)) == 0 {
            gasPrice = big.NewInt(DefaultGasPrice)
        }
        
        parsed.EffectiveGasFee = gasPrice
    }

    return parsed, nil
}

// deductFromSender deducts an amount from a sender's DID account
func deductFromSender(fromDID string, amount string, accountsClient *config.ImmuClient) error {
    // Get the current DID document
    didDoc, err := DB_OPs.GetDID(accountsClient, fromDID)
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

    // Update the balance in the database
    if err := DB_OPs.UpdateDIDBalance(accountsClient, fromDID, newBalance.String()); err != nil {
        return fmt.Errorf("failed to update sender balance: %w", err)
    }

    log.Debug().
        Str("did", fromDID).
        Str("old_balance", currentBalance.String()).
        Str("new_balance", newBalance.String()).
        Str("deducted", deductAmount.String()).
        Msg("Deducted amount from sender")

    return nil
}

// addToRecipient adds an amount to a recipient's DID account
func addToRecipient(toDID string, amount string, accountsClient *config.ImmuClient) error {
    // Get the current DID document
    didDoc, err := DB_OPs.GetDID(accountsClient, toDID)
    if err != nil {
        // If DID doesn't exist, create it with the initial balance
        if err.Error() == "DID not found" {
            if !CreateMissingAccounts {
                return fmt.Errorf("recipient DID %s does not exist and auto-creation is disabled", toDID)
            }
            
            newDID := &DB_OPs.DIDDocument{
                DID:       toDID,
                PublicKey: "auto-generated-for-recipient",
                Balance:   amount,
                CreatedAt: time.Now().Unix(),
                UpdatedAt: time.Now().Unix(),
            }
            
            if err := DB_OPs.StoreDID(accountsClient, newDID); err != nil {
                return fmt.Errorf("failed to create recipient DID: %w", err)
            }
            
            log.Info().
                Str("did", toDID).
                Str("initial_balance", amount).
                Msg("Created new DID for recipient")
                
            return nil
        }
        
        return fmt.Errorf("failed to retrieve recipient DID %s: %w", toDID, err)
    }

    // Parse current balance
    currentBalance, ok := new(big.Int).SetString(didDoc.Balance, 10)
    if !ok {
        return fmt.Errorf("invalid balance format for DID %s: %s", toDID, didDoc.Balance)
    }

    // Parse amount to add
    addAmount, ok := new(big.Int).SetString(amount, 10)
    if !ok {
        return fmt.Errorf("invalid addition amount: %s", amount)
    }

    // Calculate new balance
    newBalance := new(big.Int).Add(currentBalance, addAmount)

    // Update the balance in the database
    if err := DB_OPs.UpdateDIDBalance(accountsClient, toDID, newBalance.String()); err != nil {
        return fmt.Errorf("failed to update recipient balance: %w", err)
    }

    log.Debug().
        Str("did", toDID).
        Str("old_balance", currentBalance.String()).
        Str("new_balance", newBalance.String()).
        Str("added", addAmount.String()).
        Msg("Added amount to recipient")

    return nil
}