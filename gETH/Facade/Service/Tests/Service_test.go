package tests

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/Service/Logger"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var service *Service.ServiceImpl
var once sync.Once
var once_accounts sync.Once

func init_logger() {
	// Intilize the DB first
	once.Do(func() { DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig()) })
	once_accounts.Do(func() { DB_OPs.InitAccountsPool() })
	err := Logger.InitLogger()
	if err != nil {
		panic(err)
	}
	service = Service.NewService().(*Service.ServiceImpl)
}

func Test_ChainID(t *testing.T) {
	init_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	chainID, err := service.ChainID(ctx)
	if err != nil {
		t.Fatalf("Failed to get chain ID: %v", err)
	}

	t.Logf("Chain ID: %d", chainID.Int64())
}

func Test_ClientVersion(t *testing.T) {
	init_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientVersion, err := service.ClientVersion(ctx)
	if err != nil {
		t.Fatalf("Failed to get client version: %v", err)
	}

	t.Logf("Client Version: %s", clientVersion)
}

func Test_BlockNumber(t *testing.T) {
	init_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	blockNumber, err := service.BlockNumber(ctx)
	if err != nil {
		t.Fatalf("Failed to get block number: %v", err)
	}

	t.Logf("Block Number: %d", blockNumber.Int64())
}

func Test_BlockByNumber(t *testing.T) {
	init_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	blockNumber, err := service.BlockByNumber(ctx, big.NewInt(0), false)
	if err != nil {
		t.Fatalf("Failed to get block by number: %v", err)
	}

	t.Logf("Block: %+v", blockNumber)
}

// 0x630f87b95c414056572380164d7edbfb6c3b20f8

func Test_Balance(t *testing.T) {
	init_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	balance, err := service.Balance(ctx, "0x630f87b95c414056572380164d7edbfb6c3b20f8", big.NewInt(0))
	if err != nil {
		t.Fatalf("Failed to get balance: %v", err)
	}
	t.Logf("Balance: %+v", balance)
}

func Test_SendRawTx(t *testing.T) {
	init_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create addresses
	fromAddr := common.HexToAddress("0x630f87b95c414056572380164d7edbfb6c3b20f8")
	toAddr := common.HexToAddress("0x742d35cc6c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c")

	// Generate a complete transaction
	transaction := config.Transaction{
		Hash:       common.Hash{}, // Will be calculated after signing
		From:       &fromAddr,
		To:         &toAddr,
		Value:      big.NewInt(1000000000000000000), // 1 ETH in wei
		Type:       0,                               // Legacy transaction
		Timestamp:  uint64(time.Now().Unix()),
		ChainID:    big.NewInt(7000700),     // Your chain ID
		Nonce:      1,                       // Transaction nonce
		GasLimit:   21000,                   // Standard transfer gas limit
		GasPrice:   big.NewInt(20000000000), // 20 gwei
		Data:       []byte{},                // No data for simple transfer
		AccessList: config.AccessList{},     // Empty access list for legacy tx
		// Signature fields will be set after signing
		V: nil,
		R: nil,
		S: nil,
	}

	// Calculate transaction hash (simplified - in real implementation you'd sign it)
	transactionBytes, err := json.Marshal(transaction)
	if err != nil {
		t.Fatalf("Failed to marshal transaction: %v", err)
	}

	// Create a simple hash for testing (in production, this would be the actual signed transaction)
	hash := crypto.Keccak256Hash(transactionBytes)
	transaction.Hash = hash

	// Convert the transaction to a JSON string for SendRawTx
	transactionJSON, err := json.Marshal(transaction)
	if err != nil {
		t.Fatalf("Failed to marshal transaction: %v", err)
	}

	// Convert to hex string for SendRawTx
	rawTxHex := "0x" + hex.EncodeToString(transactionJSON)

	// Send the raw transaction
	txHash, err := service.SendRawTx(ctx, rawTxHex)
	if err != nil {
		t.Fatalf("Failed to send raw tx: %v", err)
	}

	t.Logf("Transaction sent successfully!")
	t.Logf("Transaction Hash: %s", txHash)
	t.Logf("From: %s", transaction.From.Hex())
	t.Logf("To: %s", transaction.To.Hex())
	t.Logf("Value: %s wei", transaction.Value.String())
	t.Logf("Gas Limit: %d", transaction.GasLimit)
	t.Logf("Gas Price: %s wei", transaction.GasPrice.String())
}

// Test_SendRawTx_EIP1559 tests sending an EIP-1559 transaction
func Test_SendRawTx_EIP1559(t *testing.T) {
	init_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create addresses
	fromAddr := common.HexToAddress("0x630f87b95c414056572380164d7edbfb6c3b20f8")
	toAddr := common.HexToAddress("0x742d35cc6c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c")

	// Generate an EIP-1559 transaction
	transaction := config.Transaction{
		Hash:      common.Hash{}, // Will be calculated after signing
		From:      &fromAddr,
		To:        &toAddr,
		Value:     big.NewInt(2000000000000000000), // 2 ETH in wei
		Type:      2,                               // EIP-1559 transaction
		Timestamp: uint64(time.Now().Unix()),
		ChainID:   big.NewInt(7000700), // Your chain ID
		Nonce:     2,                   // Transaction nonce
		GasLimit:  21000,               // Standard transfer gas limit
		// EIP-1559 fee fields
		MaxFee:         big.NewInt(30000000000), // 30 gwei max fee
		MaxPriorityFee: big.NewInt(2000000000),  // 2 gwei priority fee
		Data:           []byte{},                // No data for simple transfer
		AccessList:     config.AccessList{},     // Empty access list
		// Signature fields will be set after signing
		V: nil,
		R: nil,
		S: nil,
	}

	// Calculate transaction hash
	transactionBytes, err := json.Marshal(transaction)
	if err != nil {
		t.Fatalf("Failed to marshal transaction: %v", err)
	}

	hash := crypto.Keccak256Hash(transactionBytes)
	transaction.Hash = hash

	// Convert the transaction to a JSON string for SendRawTx
	transactionJSON, err := json.Marshal(transaction)
	if err != nil {
		t.Fatalf("Failed to marshal transaction: %v", err)
	}

	// Convert to hex string for SendRawTx
	rawTxHex := "0x" + hex.EncodeToString(transactionJSON)

	// Send the raw transaction
	txHash, err := service.SendRawTx(ctx, rawTxHex)
	if err != nil {
		t.Fatalf("Failed to send raw tx: %v", err)
	}

	t.Logf("EIP-1559 Transaction sent successfully!")
	t.Logf("Transaction Hash: %s", txHash)
	t.Logf("Type: EIP-1559 (Type 2)")
	t.Logf("Max Fee: %s wei", transaction.MaxFee.String())
	t.Logf("Max Priority Fee: %s wei", transaction.MaxPriorityFee.String())
}

// Test_SendRawTx_ContractCreation tests sending a contract creation transaction
func Test_SendRawTx_ContractCreation(t *testing.T) {
	init_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create from address (contract creation has no 'to' address)
	fromAddr := common.HexToAddress("0x630f87b95c414056572380164d7edbfb6c3b20f8")

	// Simple contract bytecode (just returns "Hello World")
	contractBytecode := []byte{
		0x60, 0x60, 0x60, 0x40, 0x52, 0x60, 0x20, 0x60, 0x0c, 0x60, 0x00, 0x39, 0x60, 0x00, 0xf3,
		0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x20, 0x57, 0x6f, 0x72, 0x6c, 0x64, 0x00, 0x00, 0x00, 0x00,
	}

	// Generate a contract creation transaction
	transaction := config.Transaction{
		Hash:       common.Hash{}, // Will be calculated after signing
		From:       &fromAddr,
		To:         nil,           // nil for contract creation
		Value:      big.NewInt(0), // No ETH sent
		Type:       0,             // Legacy transaction
		Timestamp:  uint64(time.Now().Unix()),
		ChainID:    big.NewInt(7000700),     // Your chain ID
		Nonce:      3,                       // Transaction nonce
		GasLimit:   100000,                  // Higher gas limit for contract creation
		GasPrice:   big.NewInt(20000000000), // 20 gwei
		Data:       contractBytecode,        // Contract bytecode
		AccessList: config.AccessList{},     // Empty access list
		// Signature fields will be set after signing
		V: nil,
		R: nil,
		S: nil,
	}

	// Calculate transaction hash
	transactionBytes, err := json.Marshal(transaction)
	if err != nil {
		t.Fatalf("Failed to marshal transaction: %v", err)
	}

	hash := crypto.Keccak256Hash(transactionBytes)
	transaction.Hash = hash

	// Convert the transaction to a JSON string for SendRawTx
	transactionJSON, err := json.Marshal(transaction)
	if err != nil {
		t.Fatalf("Failed to marshal transaction: %v", err)
	}

	// Convert to hex string for SendRawTx
	rawTxHex := "0x" + hex.EncodeToString(transactionJSON)

	// Send the raw transaction
	txHash, err := service.SendRawTx(ctx, rawTxHex)
	if err != nil {
		t.Fatalf("Failed to send raw tx: %v", err)
	}

	t.Logf("Contract Creation Transaction sent successfully!")
	t.Logf("Transaction Hash: %s", txHash)
	t.Logf("Contract Bytecode Length: %d bytes", len(contractBytecode))
	t.Logf("Gas Limit: %d", transaction.GasLimit)
}
