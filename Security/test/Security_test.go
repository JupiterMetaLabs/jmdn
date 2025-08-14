package test

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"

	"gossipnode/Security"
	"gossipnode/config"
)

// toPtr is a helper function to get a pointer to a common.Address
func toPtr(a common.Address) *common.Address {
	return &a
}

// toGethAccessList is a helper to convert our AccessList to go-ethereum's AccessList
func toGethAccessList(accessList config.AccessList) types.AccessList {
	var result types.AccessList
	for _, at := range accessList {
		result = append(result, types.AccessTuple{
			Address:     at.Address,
			StorageKeys: at.StorageKeys,
		})
	}
	return result
}

// toDID converts an Ethereum address to a DID string
func toDID(addr common.Address) string {
	return fmt.Sprintf("did:jmdt:superj:%s", addr.Hex())
}

// TestLegacyTransaction tests signature verification for legacy transactions
func TestLegacyTransaction(t *testing.T) {
	t.Log("--- Running TestLegacyTransaction ---")
	// Generate a new private key
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, _ := publicKey.(*ecdsa.PublicKey)
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	fromDID := toDID(fromAddress)
	t.Logf("Generated address: %s (DID: %s)", fromAddress.Hex(), fromDID)

	// Create a legacy transaction
	tx := &config.Transaction{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		GasLimit: 21000,
		To:       toPtr(common.HexToAddress("0x1234567890123456789012345678901234567890")),
		Value:    big.NewInt(1000000000000000000), // 1 ETH
		Data:     []byte{},
		ChainID:  big.NewInt(1), // Mainnet
	}
	t.Logf("Unsigned legacy transaction: %+v", tx)

	// Sign the transaction
	signedTx, err := signLegacyTx(tx, privateKey)
	if err != nil {
		t.Fatalf("Failed to sign transaction: %v", err)
	}

	// Set the From field to the DID after signing
	signedTx.From =  &fromAddress
	t.Logf("Signed legacy transaction with V: %s, R: %s, S: %s", signedTx.V, signedTx.R, signedTx.S)

	// Test valid signature
	t.Log("Checking valid signature for legacy transaction...")
	isValid, err := Security.CheckSignature(signedTx)
	assert.NoError(t, err)
	assert.True(t, isValid, "Expected valid signature")
	t.Logf("Validation result: isValid=%v, err=%v", isValid, err)

	// Test with tampered data
	tamperedTx := *signedTx
	tamperedTx.Value = big.NewInt(2000000000000000000) // Change the value
	t.Logf("Tampered legacy transaction: %+v", tamperedTx)
	t.Log("Checking tampered signature for legacy transaction...")
	isValid, err = Security.CheckSignature(&tamperedTx)
	assert.NoError(t, err)
	assert.False(t, isValid, "Expected invalid signature after tampering")
	t.Logf("Tampered validation result: isValid=%v, err=%v", isValid, err)
}

// TestEIP2930Transaction tests signature verification for EIP-2930 transactions
func TestEIP2930Transaction(t *testing.T) {
	t.Log("--- Running TestEIP2930Transaction ---")
	// Generate a new private key
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, _ := publicKey.(*ecdsa.PublicKey)
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	fromDID := toDID(fromAddress)
	t.Logf("Generated address: %s (DID: %s)", fromAddress.Hex(), fromDID)

	// Create an EIP-2930 transaction
	tx := &config.Transaction{
		ChainID:  big.NewInt(1), // Mainnet
		Nonce:    1,
		GasPrice: big.NewInt(20000000000),
		GasLimit: 50000,
		To:       toPtr(common.HexToAddress("0x1234567890123456789012345678901234567890")),
		Value:    big.NewInt(1000000000000000000), // 1 ETH
		Data:     []byte("hello"),
		AccessList: []config.AccessTuple{
			{
				Address:     common.HexToAddress("0x0000000000000000000000000000000000000001"),
				StorageKeys: []common.Hash{common.HexToHash("0x00")},
			},
		},
	}
	t.Logf("Unsigned EIP-2930 transaction: %+v", tx)

	// Sign the transaction
	signedTx, err := signEIP2930Tx(tx, privateKey)
	if err != nil {
		t.Fatalf("Failed to sign transaction: %v", err)
	}

	// Set the From field to the DID after signing
	signedTx.From = &fromAddress
	t.Logf("Signed EIP-2930 transaction with V: %s, R: %s, S: %s", signedTx.V, signedTx.R, signedTx.S)

	// Test valid signature
	t.Log("Checking valid signature for EIP-2930 transaction...")
	isValid, err := Security.CheckSignature(signedTx)
	assert.NoError(t, err)
	assert.True(t, isValid, "Expected valid signature")
	t.Logf("Validation result: isValid=%v, err=%v", isValid, err)

	// Test with tampered data
	tamperedTx := *signedTx
	tamperedTx.Data = []byte("tampered")
	t.Logf("Tampered EIP-2930 transaction: %+v", tamperedTx)
	t.Log("Checking tampered signature for EIP-2930 transaction...")
	isValid, err = Security.CheckSignature(&tamperedTx)
	assert.NoError(t, err)
	assert.False(t, isValid, "Expected invalid signature after tampering data")
	t.Logf("Tampered validation result: isValid=%v, err=%v", isValid, err)
}

// TestEIP1559Transaction tests signature verification for EIP-1559 transactions
func TestEIP1559Transaction(t *testing.T) {
	t.Log("--- Running TestEIP1559Transaction ---")
	// Generate a new private key
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, _ := publicKey.(*ecdsa.PublicKey)
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	fromDID := toDID(fromAddress)
	t.Logf("Generated address: %s (DID: %s)", fromAddress.Hex(), fromDID)

	// Create an EIP-1559 transaction
	tx := &config.Transaction{
		ChainID:             big.NewInt(1), // Mainnet
		Nonce:               2,
		MaxPriorityFeePerGas: big.NewInt(1000000000),  // 1 Gwei
		MaxFeePerGas:        big.NewInt(20000000000), // 20 Gwei
		GasLimit:            21000,
		To:                  toPtr(common.HexToAddress("0x1234567890123456789012345678901234567890")),
		Value:               big.NewInt(1000000000000000000), // 1 ETH
		Data:                []byte{},
		AccessList: []config.AccessTuple{
			{
				Address:     common.HexToAddress("0x0000000000000000000000000000000000000001"),
				StorageKeys: []common.Hash{common.HexToHash("0x00")},
			},
		},
	}
	t.Logf("Unsigned EIP-1559 transaction: %+v", tx)

	// Sign the transaction
	signedTx, err := signEIP1559Tx(tx, privateKey)
	if err != nil {
		t.Fatalf("Failed to sign transaction: %v", err)
	}

	// Set the From field to the DID after signing
	signedTx.From = &fromAddress
	t.Logf("Signed EIP-1559 transaction with V: %s, R: %s, S: %s", signedTx.V, signedTx.R, signedTx.S)

	// Test valid signature
	t.Log("Checking valid signature for EIP-1559 transaction...")
	isValid, err := Security.CheckSignature(signedTx)
	assert.NoError(t, err)
	assert.True(t, isValid, "Expected valid signature")
	t.Logf("Validation result: isValid=%v, err=%v", isValid, err)

	// Test with tampered data
	tamperedTx := *signedTx
	tamperedTx.MaxFeePerGas = big.NewInt(99999)
	t.Logf("Tampered EIP-1559 transaction: %+v", tamperedTx)
	t.Log("Checking tampered signature for EIP-1559 transaction...")
	isValid, err = Security.CheckSignature(&tamperedTx)
	assert.NoError(t, err)
	assert.False(t, isValid, "Expected invalid signature after tampering max fee")
	t.Logf("Tampered validation result: isValid=%v, err=%v", isValid, err)
}

// TestInvalidSignature tests various invalid signature scenarios
func TestInvalidSignature(t *testing.T) {
	t.Log("--- Running TestInvalidSignature ---")
	// Test with nil transaction
	t.Log("Checking signature for nil transaction...")
	isValid, err := Security.CheckSignature(nil)
	assert.Error(t, err, "Expected error for nil transaction")
	assert.False(t, isValid)
	t.Logf("Validation result for nil tx: isValid=%v, err=%v", isValid, err)

	// Test with nil From address
	tx := &config.Transaction{
		ChainID:  big.NewInt(1),
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		GasLimit: 21000,
		To:       toPtr(common.HexToAddress("0x1234567890123456789012345678901234567890")),
		Value:    big.NewInt(1000000000000000000),
	}
	t.Logf("Checking signature for transaction with nil 'From' address: %+v", tx)
	isValid, err = Security.CheckSignature(tx)
	assert.NoError(t, err)
	assert.False(t, isValid, "Expected invalid signature for nil From address")
	t.Logf("Validation result for nil 'From' tx: isValid=%v, err=%v", isValid, err)
}

// signLegacyTx signs a legacy transaction and populates the signature fields.
func signLegacyTx(tx *config.Transaction, privKey *ecdsa.PrivateKey) (*config.Transaction, error) {
	inner := &types.LegacyTx{
		Nonce:    tx.Nonce,
		GasPrice: tx.GasPrice,
		Gas:      tx.GasLimit,
		To:       tx.To,
		Value:    tx.Value,
		Data:     tx.Data,
	}
	ethTx := types.NewTx(inner)

	signer := types.NewEIP155Signer(tx.ChainID)
	signedEthTx, err := types.SignTx(ethTx, signer, privKey)
	if err != nil {
		return nil, err
	}

	v, r, s := signedEthTx.RawSignatureValues()
	tx.V, tx.R, tx.S = v, r, s

	return tx, nil
}

// signEIP2930Tx signs an EIP-2930 transaction and populates the signature fields.
func signEIP2930Tx(tx *config.Transaction, privKey *ecdsa.PrivateKey) (*config.Transaction, error) {
	inner := &types.AccessListTx{
		ChainID:    tx.ChainID,
		Nonce:      tx.Nonce,
		GasPrice:   tx.GasPrice,
		Gas:        tx.GasLimit,
		To:         tx.To,
		Value:      tx.Value,
		Data:       tx.Data,
		AccessList: toGethAccessList(tx.AccessList),
	}
	ethTx := types.NewTx(inner)

	signer := types.NewEIP2930Signer(tx.ChainID)
	signedEthTx, err := types.SignTx(ethTx, signer, privKey)
	if err != nil {
		return nil, err
	}

	v, r, s := signedEthTx.RawSignatureValues()
	tx.V, tx.R, tx.S = v, r, s
	return tx, nil
}

// signEIP1559Tx signs an EIP-1559 transaction and populates the signature fields.
func signEIP1559Tx(tx *config.Transaction, privKey *ecdsa.PrivateKey) (*config.Transaction, error) {
	inner := &types.DynamicFeeTx{
		ChainID:    tx.ChainID,
		Nonce:      tx.Nonce,
		GasTipCap:  tx.MaxPriorityFeePerGas,
		GasFeeCap:  tx.MaxFeePerGas,
		Gas:        tx.GasLimit,
		To:         tx.To,
		Value:      tx.Value,
		Data:       tx.Data,
		AccessList: toGethAccessList(tx.AccessList),
	}
	ethTx := types.NewTx(inner)

	signer := types.NewLondonSigner(tx.ChainID)
	signedEthTx, err := types.SignTx(ethTx, signer, privKey)
	if err != nil {
		return nil, err
	}

	v, r, s := signedEthTx.RawSignatureValues()
	tx.V, tx.R, tx.S = v, r, s
	return tx, nil
}
