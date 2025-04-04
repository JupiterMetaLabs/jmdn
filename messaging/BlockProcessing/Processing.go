package BlockProcessing

import (
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"math/big"
	"time"

	"github.com/rs/zerolog/log"
)

// processTransaction handles a single transaction's balance updates
func ProcessTransaction(tx config.ZKBlockTransaction, coinbaseAddr, zkvmAddr string, accountsClient *config.ImmuClient) error {
    // Parse the transaction values
    parsedTx, err := parseTransaction(tx)
    if err != nil {
        return fmt.Errorf("failed to parse transaction: %w", err)
    }

    // Calculate the gas fee to deduct (gasFee/1,000,000,000)
    gasFeeToDeduct := new(big.Int).Div(parsedTx.EffectiveGasFee, big.NewInt(1000000000))
    
    // Calculate total amount to deduct from sender (amount + gasFee/1,000,000,000)
    totalDeduction := new(big.Int).Add(parsedTx.ValueBig, gasFeeToDeduct)
    
    // Split the gas fee between coinbase and ZKVM
    halfGasFee := new(big.Int).Div(gasFeeToDeduct, big.NewInt(2))
    
    log.Debug().
        Str("tx_hash", tx.Hash).
        Str("from", tx.From).
        Str("to", tx.To).
        Str("value", tx.Value).
        Str("gas_fee", gasFeeToDeduct.String()).
        Str("total_deduction", totalDeduction.String()).
        Msg("Processing transaction")

    // 1. Deduct from sender
    if err := deductFromSender(tx.From, totalDeduction.String(), accountsClient); err != nil {
        return fmt.Errorf("failed to deduct from sender: %w", err)
    }

    // 2. Add amount to recipient
    if err := addToRecipient(tx.To, parsedTx.ValueBig.String(), accountsClient); err != nil {
        return fmt.Errorf("failed to add to recipient: %w", err)
    }

    // 3. Split gas fee between coinbase and ZKVM
    if err := addToRecipient(coinbaseAddr, halfGasFee.String(), accountsClient); err != nil {
        return fmt.Errorf("failed to add gas fee to coinbase: %w", err)
    }

    if err := addToRecipient(zkvmAddr, halfGasFee.String(), accountsClient); err != nil {
        return fmt.Errorf("failed to add gas fee to ZKVM: %w", err)
    }

    log.Info().
        Str("tx_hash", tx.Hash).
        Str("from", tx.From).
        Str("to", tx.To).
        Str("value", parsedTx.ValueBig.String()).
        Str("gas_fee", gasFeeToDeduct.String()).
        Msg("Transaction processed successfully")

    return nil
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
    if tx.Type == "EIP-1559" {
        // Use max fee for EIP-1559 tx
        maxFee := new(big.Int)
        maxFee, ok = maxFee.SetString(tx.MaxFee, 10)
        if !ok {
            return nil, fmt.Errorf("invalid max fee: %s", tx.MaxFee)
        }
        parsed.MaxFeeBig = maxFee
        parsed.EffectiveGasFee = maxFee
    } else {
        // Use gas price for legacy tx
        gasPrice := new(big.Int)
        gasPrice, ok = gasPrice.SetString(tx.MaxFee, 10) // Assuming MaxFee contains gasPrice for non-EIP-1559 txs
        if !ok || gasPrice.Cmp(big.NewInt(0)) == 0 {
            // Fallback to a default if not specified
            gasPrice = big.NewInt(20000000000) // 20 gwei
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