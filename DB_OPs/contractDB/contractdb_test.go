package contractDB

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	pbdid "gossipnode/DID/proto"
)

// ============================================================================
// Mock DID client (pure in-memory, no network required)
// ============================================================================

type mockDIDClient struct {
	accounts map[string]*pbdid.DIDInfo
}

func newMockDIDClient() *mockDIDClient {
	return &mockDIDClient{accounts: make(map[string]*pbdid.DIDInfo)}
}

func (m *mockDIDClient) GetDID(ctx context.Context, req *pbdid.GetDIDRequest, opts ...grpc.CallOption) (*pbdid.DIDResponse, error) {
	info, ok := m.accounts[req.Did]
	if !ok {
		return &pbdid.DIDResponse{
			Exists:  false,
			DidInfo: &pbdid.DIDInfo{Did: req.Did, Balance: "0", Nonce: "0"},
		}, nil
	}
	return &pbdid.DIDResponse{Exists: true, DidInfo: info}, nil
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

// newTestDB returns a ContractDB backed by MemKVStore — no disk, no network.
func newTestDB(t *testing.T, did *mockDIDClient) *ContractDB {
	t.Helper()
	repo := NewPebbleAdapter(NewMemKVStore())
	return NewContractDB(did, repo)
}

// ============================================================================
// Unit tests — exercising public ContractDB behaviour via the StateDB interface
// ============================================================================

func TestCreateAccount(t *testing.T) {
	db := newTestDB(t, newMockDIDClient())
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")

	db.CreateAccount(addr)
	assert.Equal(t, uint256.NewInt(0), db.GetBalance(addr))
}

func TestSetGetBalance(t *testing.T) {
	did := newMockDIDClient()
	addr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	did.setBalance(addr, big.NewInt(1000))

	db := newTestDB(t, did)

	t.Run("loads from DID on first access", func(t *testing.T) {
		assert.Equal(t, uint256.NewInt(1000), db.GetBalance(addr))
	})

	t.Run("AddBalance", func(t *testing.T) {
		db.AddBalance(addr, uint256.NewInt(500), 0)
		assert.Equal(t, uint256.NewInt(1500), db.GetBalance(addr))
	})

	t.Run("SubBalance", func(t *testing.T) {
		db.SubBalance(addr, uint256.NewInt(200), 0)
		assert.Equal(t, uint256.NewInt(1300), db.GetBalance(addr))
	})
}

func TestSetGetCode(t *testing.T) {
	db := newTestDB(t, newMockDIDClient())
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	code := common.Hex2Bytes("6060604052600a8060106000396000f360606040526008565b00")

	db.SetCode(addr, code, 0)

	assert.Equal(t, code, db.GetCode(addr), "code round-trip")
	assert.Equal(t, crypto.Keccak256Hash(code), db.GetCodeHash(addr), "code hash")
	assert.Equal(t, len(code), db.GetCodeSize(addr), "code size")
}

func TestSetGetStorage(t *testing.T) {
	db := newTestDB(t, newMockDIDClient())
	addr := common.HexToAddress("0x4444444444444444444444444444444444444444")
	key := common.HexToHash("0x01")
	val := common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")

	db.SetState(addr, key, val)
	assert.Equal(t, val, db.GetState(addr, key))
}

func TestSnapshotAndRevert(t *testing.T) {
	did := newMockDIDClient()
	addr := common.HexToAddress("0x5555555555555555555555555555555555555555")
	did.setBalance(addr, big.NewInt(1000))

	db := newTestDB(t, did)

	assert.Equal(t, uint256.NewInt(1000), db.GetBalance(addr))

	// Accumulate some changes.
	db.AddBalance(addr, uint256.NewInt(200), 0)
	assert.Equal(t, uint256.NewInt(1200), db.GetBalance(addr))

	snap := db.Snapshot()

	// More changes after the snapshot.
	db.AddBalance(addr, uint256.NewInt(300), 0)
	db.SetNonce(addr, 5, 0)
	assert.Equal(t, uint256.NewInt(1500), db.GetBalance(addr))
	assert.Equal(t, uint64(5), db.GetNonce(addr))

	// Revert to snapshot — changes after snap must disappear.
	db.RevertToSnapshot(snap)

	assert.Equal(t, uint256.NewInt(1200), db.GetBalance(addr), "balance should revert to snapshot value")
	assert.Equal(t, uint64(0), db.GetNonce(addr), "nonce should revert to 0")
}

func TestLogCapture(t *testing.T) {
	db := newTestDB(t, newMockDIDClient())
	addr := common.HexToAddress("0x6666666666666666666666666666666666666666")

	txHash := common.HexToHash("0xaaaa")
	db.AddLog(&types.Log{
		Address: addr,
		Topics:  []common.Hash{common.HexToHash("0xdeadbeef")},
		Data:    []byte{0x01},
		TxHash:  txHash,
	})

	logs := db.Logs()
	require.Len(t, logs, 1)
	assert.Equal(t, addr, logs[0].Address)
	assert.Equal(t, txHash, logs[0].TxHash)
}

// ============================================================================
// Persistence round-trip: commit → reload from shared MemStore
// ============================================================================

func TestCommitAndReload(t *testing.T) {
	kvStore := NewMemKVStore()
	did := newMockDIDClient()

	// Instance 1 — write
	db1 := NewContractDB(did, NewPebbleAdapter(kvStore))
	addr := common.HexToAddress("0x7777777777777777777777777777777777777777")
	key := common.HexToHash("0x01")
	val := common.HexToHash("0xabcdef0000000000000000000000000000000000000000000000000000000000")
	code := common.Hex2Bytes("6060604052")

	db1.CreateAccount(addr)
	db1.SetCode(addr, code, 0)
	db1.SetState(addr, key, val)
	db1.SetNonce(addr, 3, 0)

	_, err := db1.CommitToDB(false)
	require.NoError(t, err)

	// Instance 2 — reload via new ContractDB sharing the same MemStore.
	db2 := NewContractDB(did, NewPebbleAdapter(kvStore))

	assert.Equal(t, code, db2.GetCode(addr), "code should survive CommitToDB")
	assert.Equal(t, val, db2.GetState(addr, key), "storage should survive CommitToDB")
	assert.Equal(t, uint64(3), db2.GetNonce(addr), "nonce should survive CommitToDB")
}
