package Block

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"os"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	// "github.com/ethereum/go-ethereum/crypto"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

// AccountInfo represents the wallet account information from JSON
type AccountInfo struct {
    DID       string `json:"did"`
    Mnemonic  string `json:"mnemonic"`
    PublicKey string `json:"public_key"`
}

// GetPrivateKeyFromMnemonic derives the private key from the mnemonic phrase
func GetPrivateKeyFromMnemonic(mnemonic string) (*ecdsa.PrivateKey, common.Address, error) {
    wallet, err := hdwallet.NewFromMnemonic(mnemonic)
    if err != nil {
        return nil, common.Address{}, fmt.Errorf("failed to create wallet from mnemonic: %w", err)
    }

    // Standard Ethereum derivation path
    path := hdwallet.MustParseDerivationPath("m/44'/60'/0'/0/0")
    account, err := wallet.Derive(path, false)
    if err != nil {
        return nil, common.Address{}, fmt.Errorf("failed to derive account: %w", err)
    }

    privateKey, err := wallet.PrivateKey(account)
    if err != nil {
        return nil, common.Address{}, fmt.Errorf("failed to get private key: %w", err)
    }

    return privateKey, account.Address, nil
}

// LoadAccountInfo loads the account information from the provided JSON file
func LoadAccountInfo(filePath string) (*AccountInfo, error) {
    data, err := os.ReadFile(filePath)
    if err != nil {
        return nil, fmt.Errorf("error reading account file: %w", err)
    }

    var account AccountInfo
    if err := json.Unmarshal(data, &account); err != nil {
        return nil, fmt.Errorf("error parsing account data: %w", err)
    }

    return &account, nil
}

// GenerateLegacyTransaction creates a legacy (Type 0) transaction
func GenerateLegacyTransaction(accountPath string, chainID *big.Int, to string, amount *big.Int, 
                             nonce uint64, gasLimit uint64, gasPrice *big.Int, 
                             data []byte) (*Transaction, error) {
    // Load account info
    accountInfo, err := LoadAccountInfo(accountPath)
    if err != nil {
        return nil, fmt.Errorf("failed to load account: %w", err)
    }

    // Get private key from mnemonic
    privateKey, _, err := GetPrivateKeyFromMnemonic(accountInfo.Mnemonic)
    if err != nil {
        return nil, fmt.Errorf("failed to get private key: %w", err)
    }

    // Create transaction
    toAddr := common.HexToAddress(to)
    tx := &Transaction{
        ChainID:  chainID,
        Nonce:    nonce,
        To:       &toAddr,
        Value:    amount,
        GasLimit: gasLimit,
        GasPrice: gasPrice,
        Data:     data,
    }

    // Sign the transaction
    return signTransaction(tx, privateKey, chainID)
}

// GenerateEIP2930Transaction creates an AccessList (Type 1) transaction
func GenerateEIP2930Transaction(accountPath string, chainID *big.Int, to string, amount *big.Int, 
                              nonce uint64, gasLimit uint64, gasPrice *big.Int, 
                              data []byte, accessList AccessList) (*Transaction, error) {
    // Load account info
    accountInfo, err := LoadAccountInfo(accountPath)
    if err != nil {
        return nil, fmt.Errorf("failed to load account: %w", err)
    }

    // Get private key from mnemonic
    privateKey, _, err := GetPrivateKeyFromMnemonic(accountInfo.Mnemonic)
    if err != nil {
        return nil, fmt.Errorf("failed to get private key: %w", err)
    }

    // Create transaction
    toAddr := common.HexToAddress(to)
    tx := &Transaction{
        ChainID:    chainID,
        Nonce:      nonce,
        To:         &toAddr,
        Value:      amount,
        GasLimit:   gasLimit,
        GasPrice:   gasPrice,
        AccessList: accessList,
        Data:       data,
    }

    // Sign the transaction
    return signTransaction(tx, privateKey, chainID)
}

// GenerateEIP1559Transaction creates a fee market (Type 2) transaction
func GenerateEIP1559Transaction(accountPath string, chainID *big.Int, to string, amount *big.Int, 
                              nonce uint64, gasLimit uint64, maxFeePerGas *big.Int, 
                              maxPriorityFeePerGas *big.Int, data []byte, 
                              accessList AccessList) (*Transaction, error) {
    // Load account info
    accountInfo, err := LoadAccountInfo(accountPath)
    if err != nil {
        return nil, fmt.Errorf("failed to load account: %w", err)
    }

    // Get private key from mnemonic
    privateKey, _, err := GetPrivateKeyFromMnemonic(accountInfo.Mnemonic)
    if err != nil {
        return nil, fmt.Errorf("failed to get private key: %w", err)
    }

    // Create transaction
    toAddr := common.HexToAddress(to)
    tx := &Transaction{
        ChainID:             chainID,
        Nonce:               nonce,
        To:                  &toAddr,
        Value:               amount,
        GasLimit:            gasLimit,
        MaxFeePerGas:        maxFeePerGas,
        MaxPriorityFeePerGas: maxPriorityFeePerGas,
        AccessList:          accessList,
        Data:                data,
    }

    // Sign the transaction
    return signTransaction(tx, privateKey, chainID)
}

// signTransaction signs a transaction with the provided private key
func signTransaction(tx *Transaction, privateKey *ecdsa.PrivateKey, chainID *big.Int) (*Transaction, error) {
    var ethTx *types.Transaction
    
    // Convert our Transaction to go-ethereum's transaction types
    switch {
    case tx.MaxFeePerGas != nil:
        // EIP-1559 transaction (Type 2)
        accessList := convertAccessList(tx.AccessList)
        ethTx = types.NewTx(&types.DynamicFeeTx{
            ChainID:    chainID,
            Nonce:      tx.Nonce,
            GasTipCap:  tx.MaxPriorityFeePerGas,
            GasFeeCap:  tx.MaxFeePerGas,
            Gas:        tx.GasLimit,
            To:         tx.To,
            Value:      tx.Value,
            Data:       tx.Data,
            AccessList: accessList,
        })
    case tx.AccessList != nil && len(tx.AccessList) > 0:
        // Access list transaction (Type 1)
        accessList := convertAccessList(tx.AccessList)
        ethTx = types.NewTx(&types.AccessListTx{
            ChainID:    chainID,
            Nonce:      tx.Nonce,
            GasPrice:   tx.GasPrice,
            Gas:        tx.GasLimit,
            To:         tx.To,
            Value:      tx.Value,
            Data:       tx.Data,
            AccessList: accessList,
        })
    default:
        // Legacy transaction (Type 0)
        ethTx = types.NewTx(&types.LegacyTx{
            Nonce:    tx.Nonce,
            GasPrice: tx.GasPrice,
            Gas:      tx.GasLimit,
            To:       tx.To,
            Value:    tx.Value,
            Data:     tx.Data,
        })
    }
    
    // Use the appropriate signer based on the transaction type
    signer := types.LatestSignerForChainID(chainID)
    
    signedTx, err := types.SignTx(ethTx, signer, privateKey)
    if err != nil {
        return nil, fmt.Errorf("failed to sign transaction: %w", err)
    }
    
    // Extract V, R, S from the signed transaction
    v, r, s := signedTx.RawSignatureValues()
    
    // Copy the values back to our transaction type
    tx.V = v
    tx.R = r
    tx.S = s
    
    return tx, nil
}

// Helper function to convert our AccessList type to go-ethereum's types.AccessList
func convertAccessList(accessList AccessList) types.AccessList {
    result := make(types.AccessList, len(accessList))
    for i, tuple := range accessList {
        result[i] = types.AccessTuple{
            Address:     tuple.Address,
            StorageKeys: tuple.StorageKeys,
        }
    }
    return result
}