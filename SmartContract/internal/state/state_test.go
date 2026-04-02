package state

import (
	"context"
	"math/big"
	"testing"

	"gossipnode/SmartContract/internal/repository"
	"gossipnode/SmartContract/internal/storage"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	pbdid "gossipnode/DID/proto"
)

// mockDIDClient implements a simple in-memory DID client for testing
type mockDIDClient struct {
	accounts map[string]*pbdid.DIDInfo
}

func newMockDIDClient() *mockDIDClient {
	return &mockDIDClient{
		accounts: make(map[string]*pbdid.DIDInfo),
	}
}

func (m *mockDIDClient) GetDID(ctx context.Context, req *pbdid.GetDIDRequest, opts ...grpc.CallOption) (*pbdid.DIDResponse, error) {
	info, ok := m.accounts[req.Did]
	if !ok {
		// Return empty account
		return &pbdid.DIDResponse{
			Exists: false,
			DidInfo: &pbdid.DIDInfo{
				Did:     req.Did,
				Balance: "0",
				Nonce:   "0",
			},
		}, nil
	}
	return &pbdid.DIDResponse{
		Exists:  true,
		DidInfo: info,
	}, nil
}

func (m *mockDIDClient) RegisterDID(ctx context.Context, req *pbdid.RegisterDIDRequest, opts ...grpc.CallOption) (*pbdid.RegisterDIDResponse, error) {
	return nil, nil
}

func (m *mockDIDClient) ListDIDs(ctx context.Context, req *pbdid.ListDIDsRequest, opts ...grpc.CallOption) (*pbdid.ListDIDsResponse, error) {
	return nil, nil
}

func (m *mockDIDClient) GetDIDStats(ctx context.Context, req *emptypb.Empty, opts ...grpc.CallOption) (*pbdid.DIDStats, error) {
	return nil, nil
}

func (m *mockDIDClient) setBalance(addr common.Address, balance *big.Int) {
	m.accounts[addr.Hex()] = &pbdid.DIDInfo{
		Did:     addr.Hex(),
		Balance: balance.String(),
		Nonce:   "0",
	}
}

// TestBalanceUpdates verifies balance operations work correctly
func TestBalanceUpdates(t *testing.T) {
	// Setup
	kvStore := storage.NewMemKVStore()
	mockDID := newMockDIDClient()
	repo := repository.NewPebbleAdapter(kvStore)
	db := NewContractDB(mockDID, repo)

	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Set initial balance in mock DID
	initialBalance := big.NewInt(1000)
	mockDID.setBalance(addr, initialBalance)

	t.Run("GetBalance loads from DID", func(t *testing.T) {
		balance := db.GetBalance(addr)
		assert.Equal(t, uint256.NewInt(1000), balance, "Should load initial balance from DID")
	})

	t.Run("AddBalance updates correctly", func(t *testing.T) {
		db.AddBalance(addr, uint256.NewInt(500), 0)
		balance := db.GetBalance(addr)
		assert.Equal(t, uint256.NewInt(1500), balance, "Balance should be 1000 + 500")
	})

	t.Run("SubBalance updates correctly", func(t *testing.T) {
		db.SubBalance(addr, uint256.NewInt(300), 0)
		balance := db.GetBalance(addr)
		assert.Equal(t, uint256.NewInt(1200), balance, "Balance should be 1500 - 300")
	})

	t.Run("Balance changes are in-memory only before commit", func(t *testing.T) {
		// Create new DB instance to verify nothing persisted
		repo := repository.NewPebbleAdapter(kvStore)
		db2 := NewContractDB(mockDID, repo)
		balance := db2.GetBalance(addr)
		assert.Equal(t, uint256.NewInt(1000), balance, "Should still see original balance before commit")
	})
}

// TestJournalSnapshotRevert verifies journal-based snapshot/revert works
func TestJournalSnapshotRevert(t *testing.T) {
	kvStore := storage.NewMemKVStore()
	mockDID := newMockDIDClient()
	repo := repository.NewPebbleAdapter(kvStore)
	db := NewContractDB(mockDID, repo)

	addr := common.HexToAddress("0xABCDEF1234567890ABCDEF1234567890ABCDEF12")
	mockDID.setBalance(addr, big.NewInt(1000))

	t.Run("Snapshot and revert balance changes", func(t *testing.T) {
		// Initial state
		assert.Equal(t, uint256.NewInt(1000), db.GetBalance(addr))

		// Make some changes
		db.AddBalance(addr, uint256.NewInt(200), 0)
		assert.Equal(t, uint256.NewInt(1200), db.GetBalance(addr))

		// Take snapshot
		snapshot := db.Snapshot()

		// Make more changes
		db.AddBalance(addr, uint256.NewInt(300), 0)
		db.SetNonce(addr, 5)
		assert.Equal(t, uint256.NewInt(1500), db.GetBalance(addr))
		assert.Equal(t, uint64(5), db.GetNonce(addr))

		// Revert to snapshot
		db.RevertToSnapshot(snapshot)

		// Should be back to snapshot state
		assert.Equal(t, uint256.NewInt(1200), db.GetBalance(addr))
		assert.Equal(t, uint64(0), db.GetNonce(addr), "Nonce should revert to 0")
	})

	t.Run("Nested snapshots", func(t *testing.T) {
		// Reset
		repo := repository.NewPebbleAdapter(kvStore)
		db = NewContractDB(mockDID, repo)
		mockDID.setBalance(addr, big.NewInt(1000))

		// Snapshot 1
		db.AddBalance(addr, uint256.NewInt(100), 0)
		snap1 := db.Snapshot()
		assert.Equal(t, uint256.NewInt(1100), db.GetBalance(addr))

		// Snapshot 2
		db.AddBalance(addr, uint256.NewInt(200), 0)
		snap2 := db.Snapshot()
		assert.Equal(t, uint256.NewInt(1300), db.GetBalance(addr))

		// Snapshot 3
		db.AddBalance(addr, uint256.NewInt(300), 0)
		assert.Equal(t, uint256.NewInt(1600), db.GetBalance(addr))

		// Revert to snap2
		db.RevertToSnapshot(snap2)
		assert.Equal(t, uint256.NewInt(1300), db.GetBalance(addr))

		// Revert to snap1
		db.RevertToSnapshot(snap1)
		assert.Equal(t, uint256.NewInt(1100), db.GetBalance(addr))
	})
}

// Test storage and code persistence
func TestStorageCodePersistence(t *testing.T) {
	kvStore := storage.NewMemKVStore()
	mockDID := newMockDIDClient()
	repo := repository.NewPebbleAdapter(kvStore)
	db := NewContractDB(mockDID, repo)

	contractAddr := common.HexToAddress("0xCONTRACT1234567890123456789012345678")

	// Set storage
	key := common.HexToHash("0x01")
	value := common.HexToHash("0xDEADBEEF")
	db.SetState(contractAddr, key, value)

	// Set code
	code := common.Hex2Bytes("6060604052")
	db.SetCode(contractAddr, code)

	// Verify in-memory
	assert.Equal(t, value, db.GetState(contractAddr, key))
	assert.Equal(t, code, db.GetCode(contractAddr))
	assert.Equal(t, crypto.Keccak256Hash(code), db.GetCodeHash(contractAddr))

	// Commit
	_, err := db.CommitToDB(false)
	require.NoError(t, err)

	// Load in new instance
	repo2 := repository.NewPebbleAdapter(kvStore)
	db2 := NewContractDB(mockDID, repo2)
	assert.Equal(t, value, db2.GetState(contractAddr, key), "Storage should persist")
	assert.Equal(t, code, db2.GetCode(contractAddr), "Code should persist")
}
