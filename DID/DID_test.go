package DID

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status" // Corrected import path
	"google.golang.org/protobuf/types/known/emptypb"

	"gossipnode/DB_OPs"
	pb "gossipnode/DID/proto"
	"gossipnode/config"
	"gossipnode/logging"
)

// mockDbOps is a mock implementation of the dbOps interface for testing.
type mockDbOps struct {
	// Using a map to simulate the database
	accounts map[string]*DB_OPs.Account
	// Control whether methods should return an error
	shouldError bool
}

// newMockDbOps creates a new mock database.
func newMockDbOps() *mockDbOps {
	return &mockDbOps{
		accounts: make(map[string]*DB_OPs.Account),
	}
}

// Implement the dbOps interface
func (m *mockDbOps) GetAccount(conn *config.PooledConnection, address common.Address) (*DB_OPs.Account, error) {
	if m.shouldError {
		return nil, fmt.Errorf("mock db error")
	}
	// The real DB stores by address, so we use the hex representation as the key.
	// Note: The original getAccount converted the input string to common.Address,
	// so we should expect a hex string with "0x" prefix.
	acc, ok := m.accounts[address.Hex()]
	if !ok {
		// This should mimic the actual behavior of DB_OPs.GetAccount on not found.
		return nil, fmt.Errorf("account not found")
	}
	return acc, nil
}

func (m *mockDbOps) ListAllAccounts(conn *config.PooledConnection, limit int) ([]*DB_OPs.Account, error) {
	if m.shouldError {
		return nil, fmt.Errorf("mock db error")
	}
	var result []*DB_OPs.Account
	count := 0
	for _, acc := range m.accounts {
		if count >= limit {
			break
		}
		result = append(result, acc)
		count++
	}
	return result, nil
}

func (m *mockDbOps) StoreAccount(conn *config.PooledConnection, doc *DB_OPs.KeyDocument) error {
	if m.shouldError {
		return fmt.Errorf("mock db error")
	}
	// Create an Account from the KeyDocument to store in our mock map.
	// The real DB might do this differently, but this simulates the outcome.
	m.accounts[common.HexToAddress(doc.Address).Hex()] = &DB_OPs.Account{
		Address:     doc.Address,
		DIDAddress:  doc.DIDAddress,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
		Balance:     "0",
		Nonce:       0,
		AccountType: "Standard",
		Metadata:    make(map[string]interface{}),
	}
	return nil
}

func (m *mockDbOps) GetAccountsConnection() (*config.PooledConnection, error) {
	if m.shouldError {
		return nil, fmt.Errorf("mock db connection error")
	}
	// The mock ImmuClient should have a *zap.Logger, not zerolog.Logger
	// For testing purposes, we can provide a dummy zap.Logger or nil if not used.
	return &config.PooledConnection{
		Client: &config.ImmuClient{Logger: &logging.AsyncLogger{Logger: nil}}, // Use nil or a mock zap.Logger
	}, nil
}

func (m *mockDbOps) PutAccountsConnection(conn *config.PooledConnection) {} // No-op

// Test RegisterAccount input validation without requiring any DB access.
func TestRegisterAccount_InvalidInput(t *testing.T) {
    s := &AccountServer{}


    // Both fields empty
    _, err := s.RegisterAccount(context.Background(), &pb.RegisterDIDRequest{})
    if err == nil {
        t.Fatalf("expected error for empty request, got nil")
    }
    st, ok := status.FromError(err)
    if !ok || st.Code() != codes.InvalidArgument {
        t.Fatalf("expected InvalidArgument, got: %v", err)
    }

    // DID provided but missing public key
    _, err = s.RegisterAccount(context.Background(), &pb.RegisterDIDRequest{Did: "did:example:123"})
    if err == nil {
        t.Fatalf("expected error for missing public key, got nil")
    }
    st, ok = status.FromError(err)
    if !ok || st.Code() != codes.InvalidArgument {
        t.Fatalf("expected InvalidArgument for missing public key, got: %v", err)
    }

    // Public key provided but missing DID
    _, err = s.RegisterAccount(context.Background(), &pb.RegisterDIDRequest{PublicKey: "0xabc"})
    if err == nil {
        t.Fatalf("expected error for missing DID, got nil")
    }
    st, ok = status.FromError(err)
    if !ok || st.Code() != codes.InvalidArgument {
        t.Fatalf("expected InvalidArgument for missing DID, got: %v", err)
    }
}

// Test RegisterAccount success path
func TestRegisterAccount_Success(t *testing.T) {
	mockDB := newMockDbOps()
	s := NewAccountServer(nil) // host is not needed for this test
	s.db = mockDB
	s.accountsClient, _ = mockDB.GetAccountsConnection()

	req := &pb.RegisterDIDRequest{
		Did:       "did:example:123",
		PublicKey: "0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B",
	}

	resp, err := s.RegisterAccount(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.True(t, resp.Success)
	assert.Equal(t, "DID registered successfully", resp.Message)
	assert.Equal(t, req.Did, resp.DidInfo.Did)
	assert.Equal(t, req.PublicKey, resp.DidInfo.PublicKey)

	// Verify it was stored in our mock DB
	storedAccount, dbErr := mockDB.GetAccount(nil, common.HexToAddress(req.PublicKey))
	require.NoError(t, dbErr)
	assert.Equal(t, req.Did, storedAccount.DIDAddress)
}

// Test RegisterAccount when the account already exists
func TestRegisterAccount_AlreadyExists(t *testing.T) {
	mockDB := newMockDbOps()
	s := NewAccountServer(nil)
	s.db = mockDB
	s.accountsClient, _ = mockDB.GetAccountsConnection()

	// Pre-populate the mock DB
	existingAccount := &DB_OPs.Account{
		Address:     "0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B",
		DIDAddress:  "did:example:existing",
		Balance:     "100",
		Nonce:       5,
		CreatedAt:   time.Now().Unix() - 1000,
		UpdatedAt:   time.Now().Unix() - 500,
		AccountType: "EOA",
		Metadata:    map[string]interface{}{"note": "i am old"},
	}
	mockDB.accounts[common.HexToAddress(existingAccount.Address).Hex()] = existingAccount

	req := &pb.RegisterDIDRequest{
		Did:       "did:example:new",
		PublicKey: "0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B",
	}

	resp, err := s.RegisterAccount(context.Background(), req)
	require.NoError(t, err) // Should not be a gRPC error
	require.NotNil(t, resp)

	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "already exists")
	assert.Equal(t, existingAccount.DIDAddress, resp.DidInfo.Did)
	assert.Equal(t, existingAccount.Balance, resp.DidInfo.Balance)
	assert.Equal(t, fmt.Sprintf("%d", existingAccount.Nonce), resp.DidInfo.Nonce)
}

func TestGetDID_Success(t *testing.T) {
	mockDB := newMockDbOps()
	s := NewAccountServer(nil)
	s.db = mockDB
	s.accountsClient, _ = mockDB.GetAccountsConnection()

	// Pre-populate the mock DB
	addr := "0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B"
	existingAccount := &DB_OPs.Account{
		Address:     addr,
		DIDAddress:  "did:example:123",
		Balance:     "100",
		Nonce:       1,
		AccountType: "Contract",
		Metadata:    map[string]interface{}{"info": "test"},
	}
	mockDB.accounts[common.HexToAddress(addr).Hex()] = existingAccount

	req := &pb.GetDIDRequest{Did: addr}
	resp, err := s.GetDID(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.True(t, resp.Exists)
	assert.Equal(t, existingAccount.DIDAddress, resp.DidInfo.Did)
	assert.Equal(t, existingAccount.Address, resp.DidInfo.PublicKey)
	assert.Equal(t, existingAccount.Balance, resp.DidInfo.Balance)
	assert.Equal(t, existingAccount.AccountType, resp.DidInfo.AccountType)
}

func TestGetDID_NotFound(t *testing.T) {
	mockDB := newMockDbOps()
	s := NewAccountServer(nil)
	s.db = mockDB
	s.accountsClient, _ = mockDB.GetAccountsConnection()

	req := &pb.GetDIDRequest{Did: "0x1111111111111111111111111111111111111111"}
	resp, err := s.GetDID(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.False(t, resp.Exists)
	assert.Equal(t, req.Did, resp.DidInfo.Did) // Should still return the requested DID
}

// Test GetDID input validation without touching DB.
func TestGetDID_InvalidInput(t *testing.T) {
    s := &AccountServer{}

    _, err := s.GetDID(context.Background(), &pb.GetDIDRequest{})
    if err == nil {
        t.Fatalf("expected error for empty DID, got nil")
    }
    st, ok := status.FromError(err)
    if !ok || st.Code() != codes.InvalidArgument {
        t.Fatalf("expected InvalidArgument for empty DID, got: %v", err)
    }
}

func TestListDIDs_Success(t *testing.T) {
	mockDB := newMockDbOps()
	s := NewAccountServer(nil)
	s.db = mockDB
	s.accountsClient, _ = mockDB.GetAccountsConnection()

	// Pre-populate
	mockDB.accounts["0x1"] = &DB_OPs.Account{Address: "0x1", DIDAddress: "did:1"}
	mockDB.accounts["0x2"] = &DB_OPs.Account{Address: "0x2", DIDAddress: "did:2"}

	req := &pb.ListDIDsRequest{Limit: 10}
	resp, err := s.ListDIDs(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, int32(2), resp.TotalCount)
	assert.Len(t, resp.Dids, 2)
}

func TestListDIDs_Empty(t *testing.T) {
	mockDB := newMockDbOps()
	s := NewAccountServer(nil)
	s.db = mockDB
	s.accountsClient, _ = mockDB.GetAccountsConnection()

	req := &pb.ListDIDsRequest{Limit: 10}
	resp, err := s.ListDIDs(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, int32(0), resp.TotalCount)
	assert.Len(t, resp.Dids, 0)
}

// Test GetAccountStats returns current stats if they are fresh (no refresh/DB needed).
func TestGetAccountStats_UsesCachedStats(t *testing.T) {
    s := &AccountServer{}
    // Pre-populate stats and age to avoid refresh path
    s.stats = &pb.DIDStats{
        TotalDids:    3,
        TotalBalance: "42",
        LastUpdate:   12345,
    }
    s.statsAge = time.Now().Unix()

    got, err := s.GetAccountStats(context.Background(), &emptypb.Empty{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got.TotalDids != 3 || got.TotalBalance != "42" || got.LastUpdate != 12345 {
        t.Fatalf("unexpected stats returned: %+v", got)
    }
}

func TestGetAccountStats_Refresh(t *testing.T) {
	mockDB := newMockDbOps()
	s := NewAccountServer(nil)
	s.db = mockDB
	s.accountsClient, _ = mockDB.GetAccountsConnection()

	// Pre-populate DB for refresh
	mockDB.accounts["0x1"] = &DB_OPs.Account{Address: "0x1", DIDAddress: "did:1", Balance: "1000"}
	mockDB.accounts["0x2"] = &DB_OPs.Account{Address: "0x2", DIDAddress: "did:2", Balance: "2500"}
	mockDB.accounts["0x3"] = &DB_OPs.Account{Address: "0x3", DIDAddress: "did:3", Balance: "500"}

	// Make stats appear stale
	s.statsAge = time.Now().Unix() - 100

	stats, err := s.GetAccountStats(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)
	require.NotNil(t, stats)

	assert.Equal(t, int32(3), stats.TotalDids)
	assert.Equal(t, "4000", stats.TotalBalance) // 1000 + 2500 + 500
	assert.True(t, time.Now().Unix()-stats.LastUpdate < 5, "LastUpdate should be recent")
}

// timeNowUnix is a tiny indirection to avoid importing time directly in assertions
// while keeping the value recent enough to skip refresh logic.
// no-op helpers can be added here if needed in the future
