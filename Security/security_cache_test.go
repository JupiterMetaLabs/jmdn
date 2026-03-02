package Security_test

import (
	"context"
	"fmt"
	"math/big"
	"testing"

	"jmdn/DB_OPs"
	"jmdn/Security"
	"jmdn/config"
	"jmdn/config/settings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

// Helper to create a dummy transaction
func createDummyTx(from, to common.Address, value, gasPrice *big.Int, gasLimit, nonce uint64) *config.Transaction {
	return &config.Transaction{
		From:     &from,
		To:       &to,
		Value:    value,
		GasPrice: gasPrice,
		GasLimit: gasLimit,
		Nonce:    nonce,
		Hash:     common.HexToHash("0x123"), // Dummy hash
	}
}

func TestSecurityCache_BasicOperations(t *testing.T) {
	if _, err := settings.Load(); err != nil {
		t.Logf("Failed to load settings: %v", err)
	}
	fmt.Println("=== TestSecurityCache_BasicOperations ===")

	// Initialize Cache
	cache := Security.NewSecurityCache()
	defer cache.Close()

	// Manually add an account to simulate LoadAccounts (since we don't want to rely on DB here for basic logic)
	// We need to access the map, but it's private.
	// CheckBalanceWithCache relies on GetAccount.
	// We can use LoadAccounts with a mock connection if possible, or just skip LoadAccounts for basic logic?
	// Wait, SecurityCache doesn't verify "how" account got there.
	// But we can't inject accounts easily without using LoadAccounts and a real DB connection.

	// Option 2: Use LoadAccounts but mock DB_OPs? DB_OPs is hard to mock as it's a package func.
	// Option 3: Integ Test. We have access to DB_OPs.
	// Let's assume we can use DB_OPs.InitAccountsDBPool() if needed, but that requires running DB.
	// For unit testing logic, maybe we can expose a "SetAccount" for testing?
	// Or use `LoadAccounts` with a real DB if available.

	// Given the context of previous tests (repro_test.go), it seems we can use DB.
	// Let's try to initialize DB pool.
	if err := DB_OPs.InitAccountsPool(); err != nil {
		t.Logf("DB Pool might be already initialized: %v", err)
	}

	ctx := context.Background()

	// Setup Helper: Create Account in DB
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	did := "did:test:" + addr.Hex()
	initialBalance := new(big.Int).SetInt64(1000)

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		t.Skip("Skipping test as DB is not available:", err)
		return
	}
	// Ensure account exists
	_ = DB_OPs.CreateAccount(conn, did, addr, map[string]interface{}{"type": "test"})
	err = DB_OPs.UpdateAccountBalance(conn, addr, initialBalance.String())
	assert.NoError(t, err)
	DB_OPs.PutAccountsConnection(conn)

	fmt.Printf("Loading account %s into cache...\n", addr.Hex())
	accountsSet := DB_OPs.NewAccountsSet()
	accountsSet.Add(addr)
	cache.LoadAccounts(ctx, nil, accountsSet)

	// Verify GetAccount
	acc := cache.GetAccount(addr)
	assert.NotNil(t, acc, "Account should be in cache")
	assert.Equal(t, initialBalance.String(), acc.Balance, "Balance should match DB")
	fmt.Printf("Account loaded: Balance=%s\n", acc.Balance)

	// Test AddBalance / SubBalance
	fmt.Println("Testing AddBalance...")
	cache.AddBalance(addr, big.NewInt(500))
	acc = cache.GetAccount(addr)
	assert.Equal(t, "1500", acc.Balance, "Balance should be 1500 after adding 500")
	fmt.Printf("New Balance: %s\n", acc.Balance)

	fmt.Println("Testing SubBalance...")
	cache.SubBalance(addr, big.NewInt(200))
	acc = cache.GetAccount(addr)
	assert.Equal(t, "1300", acc.Balance, "Balance should be 1300 after subtracting 200")
	fmt.Printf("New Balance: %s\n", acc.Balance)
}

func TestSecurityCache_DoubleSpendProtection(t *testing.T) {
	if _, err := settings.Load(); err != nil {
		t.Logf("Failed to load settings: %v", err)
	}
	fmt.Println("\n=== TestSecurityCache_DoubleSpendProtection ===")

	if err := DB_OPs.InitAccountsPool(); err != nil {
		t.Logf("DB Pool might be already initialized: %v", err)
	}

	ctx := context.Background()
	cache := Security.NewSecurityCache()
	defer cache.Close()

	// Setup User A (Sender) and User B (Receiver)
	senderAddr := common.HexToAddress("0xAAAAA11111111111111111111111111111111111")
	receiverAddr := common.HexToAddress("0xBBBBB22222222222222222222222222222222222")

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		t.Skip("Skipping test as DB is not available:", err)
		return
	}
	defer DB_OPs.PutAccountsConnection(conn)

	// Create/Update Sender with 100 Wei
	_ = DB_OPs.CreateAccount(conn, "did:test:sender", senderAddr, nil)
	_ = DB_OPs.UpdateAccountBalance(conn, senderAddr, "100")

	// Create/Update Receiver with 0 Wei
	_ = DB_OPs.CreateAccount(conn, "did:test:receiver", receiverAddr, nil)
	_ = DB_OPs.UpdateAccountBalance(conn, receiverAddr, "0")

	fmt.Println("Loading Sender (Balance=100) and Receiver (Balance=0) into cache...")
	accountsSet := DB_OPs.NewAccountsSet()
	accountsSet.Add(senderAddr)
	accountsSet.Add(receiverAddr)
	cache.LoadAccounts(ctx, nil, accountsSet)

	// 1. Valid Transaction: Send 60 Wei (Gas=0 for simplicity)
	// Cost = 60 + 0 = 60. Remaining = 40.
	tx1 := createDummyTx(senderAddr, receiverAddr, big.NewInt(60), big.NewInt(0), 21000, 1)

	fmt.Println("Checking Tx1: Send 60 Wei...")
	valid, err := cache.CheckBalanceWithCache(tx1, ctx)
	assert.NoError(t, err)
	assert.True(t, valid, "Tx1 should be valid (100 >= 60)")

	// Verify State in Cache
	senderAcc := cache.GetAccount(senderAddr)
	receiverAcc := cache.GetAccount(receiverAddr)
	fmt.Printf("After Tx1: Sender Balance=%s (Expected 40), Receiver Balance=%s (Expected 60)\n", senderAcc.Balance, receiverAcc.Balance)
	assert.Equal(t, "40", senderAcc.Balance)
	assert.Equal(t, "60", receiverAcc.Balance)

	// 2. Double Spend Attempt: Send 50 Wei again
	// Current Balance in Cache: 40. Cost: 50. Should FAIL.
	tx2 := createDummyTx(senderAddr, receiverAddr, big.NewInt(50), big.NewInt(0), 21000, 2)

	fmt.Println("Checking Tx2 (Double Spend): Send 50 Wei...")
	valid, err = cache.CheckBalanceWithCache(tx2, ctx)
	// CheckBalanceWithCache returns false, nil if insufficient funds
	assert.False(t, valid, "Tx2 should fail insufficient funds (40 < 50)")
	if !valid {
		fmt.Println("Tx2 Failed as expected (Insufficient Funds)")
	}

	// Verify State Unchanged (or at least Sender didn't drop below 40)
	senderAcc = cache.GetAccount(senderAddr)
	fmt.Printf("After Tx2: Sender Balance=%s (Expected 40)\n", senderAcc.Balance)
	assert.Equal(t, "40", senderAcc.Balance)

	// 3. Valid Small Transaction: Send 30 Wei
	// Current: 40. Cost: 30. Remaining: 10. Should PASS.
	tx3 := createDummyTx(senderAddr, receiverAddr, big.NewInt(30), big.NewInt(0), 21000, 3)

	fmt.Println("Checking Tx3: Send 30 Wei...")
	valid, err = cache.CheckBalanceWithCache(tx3, ctx)
	assert.True(t, valid, "Tx3 should be valid (40 >= 30)")

	// Verify Final State
	senderAcc = cache.GetAccount(senderAddr)
	receiverAcc = cache.GetAccount(receiverAddr)
	fmt.Printf("After Tx3: Sender Balance=%s (Expected 10), Receiver Balance=%s (Expected 90)\n", senderAcc.Balance, receiverAcc.Balance)
	assert.Equal(t, "10", senderAcc.Balance)
	assert.Equal(t, "90", receiverAcc.Balance)
}
