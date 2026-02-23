package state

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"gossipnode/SmartContract/internal/storage"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// MockDIDClient is a simple mock for the DID service client
type MockDIDClient struct{}

func (m *MockDIDClient) GetDID(ctx interface{}, in interface{}, opts ...interface{}) (interface{}, error) {
	return nil, nil
}

func TestPersistence(t *testing.T) {
	// Setup temporary PebbleDB
	dbPath := "./test_pebble_persistence"
	defer os.RemoveAll(dbPath)

	config := storage.Config{
		Type: storage.StoreTypePebble,
		Path: dbPath,
	}
	kvStore, err := storage.NewKVStore(config)
	if err != nil {
		t.Fatalf("Failed to create KVStore: %v", err)
	}
	defer kvStore.Close()

	// Initialize ContractDB
	contractDB := NewContractDB(nil, kvStore) // nil DID client is fine for this test

	// Test 1: Contract Metadata Persistence
	t.Run("ContractMetadata", func(t *testing.T) {
		addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
		deployer := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcdef")
		txHash := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

		meta := ContractMetadata{
			ContractAddress:  addr,
			CodeHash:         common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
			CodeSize:         100,
			DeployerAddress:  deployer,
			DeploymentTxHash: txHash,
			DeploymentBlock:  10,
			CreatedAt:        time.Now().Unix(),
		}

		// Save
		if err := contractDB.SetContractMetadata(addr, meta); err != nil {
			t.Fatalf("SetContractMetadata failed: %v", err)
		}

		// Load
		loadedMeta, err := contractDB.GetContractMetadata(addr)
		if err != nil {
			t.Fatalf("GetContractMetadata failed: %v", err)
		}

		if loadedMeta == nil {
			t.Fatal("GetContractMetadata returned nil")
		}

		if loadedMeta.ContractAddress != meta.ContractAddress {
			t.Errorf("Address mismatch: got %v, want %v", loadedMeta.ContractAddress, meta.ContractAddress)
		}
		if loadedMeta.CodeHash != meta.CodeHash {
			t.Errorf("CodeHash mismatch: got %v, want %v", loadedMeta.CodeHash, meta.CodeHash)
		}
	})

	// Test 2: Transaction Receipt Persistence
	t.Run("TransactionReceipt", func(t *testing.T) {
		txHash := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
		contractAddr := common.HexToAddress("0x4444444444444444444444444444444444444444")

		log := &types.Log{
			Address: contractAddr,
			Topics:  []common.Hash{common.HexToHash("0xdeadbeef")},
			Data:    []byte{0x01, 0x02, 0x03},
			TxHash:  txHash,
		}

		receipt := TransactionReceipt{
			TxHash:          txHash,
			BlockNumber:     15,
			TxIndex:         2,
			Status:          1,
			GasUsed:         21000,
			ContractAddress: contractAddr,
			Logs:            []*types.Log{log},
			CreatedAt:       time.Now().Unix(),
		}

		// Write
		if err := contractDB.WriteReceipt(receipt); err != nil {
			t.Fatalf("WriteReceipt failed: %v", err)
		}

		// Read
		loadedReceipt, err := contractDB.GetReceipt(txHash)
		if err != nil {
			t.Fatalf("GetReceipt failed: %v", err)
		}
		if loadedReceipt == nil {
			t.Fatal("GetReceipt returned nil")
		}

		if loadedReceipt.TxHash != receipt.TxHash {
			t.Errorf("TxHash mismatch: got %v, want %v", loadedReceipt.TxHash, receipt.TxHash)
		}
		if len(loadedReceipt.Logs) != 1 {
			t.Errorf("Log count mismatch: got %d, want 1", len(loadedReceipt.Logs))
		}
		if loadedReceipt.Logs[0].Address != contractAddr {
			t.Errorf("Log address mismatch: got %v, want %v", loadedReceipt.Logs[0].Address, contractAddr)
		}
	})

	// Test 3: Storage Metadata Persistence (CommitToDB interaction)
	t.Run("StorageMetadata", func(t *testing.T) {
		addr := common.HexToAddress("0x5555555555555555555555555555555555555555")
		txHash := common.HexToHash("0x6666666666666666666666666666666666666666")

		contractDB.SetTxContext(txHash, 20)

		// Create state object
		sObj := contractDB.getOrNewStateObject(addr)
		sObj.setBalance(uint256.NewInt(1000)) // Ensure strict balance updates if needed, but not critical for storage test

		// Update storage
		key := common.HexToHash("0xaaaa")
		val := common.HexToHash("0xbbbb")
		sObj.setState(key, val)

		// Commit
		if _, err := contractDB.CommitToDB(false); err != nil {
			t.Fatalf("CommitToDB failed: %v", err)
		}

		// Verify Metadata exists in DB
		// Since we don't have a GetStorageMetadata public method, we verify by checking raw key
		metaPrefix := []byte("meta:storage:")
		metaKey := append(metaPrefix, append(addr.Bytes(), key.Bytes()...)...)

		data, err := kvStore.Get(metaKey)
		if err != nil {
			t.Fatalf("Failed to get storage metadata raw: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("Storage metadata not found in DB")
		}

		var meta StorageMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("Failed to unmarshal storage metadata: %v", err)
		}

		if meta.LastModifiedTx != txHash {
			t.Errorf("Metadata TxHash mismatch: got %v, want %v", meta.LastModifiedTx, txHash)
		}
		if meta.LastModifiedBlock != 20 {
			t.Errorf("Metadata Block mismatch: got %d, want 20", meta.LastModifiedBlock)
		}
	})
}
