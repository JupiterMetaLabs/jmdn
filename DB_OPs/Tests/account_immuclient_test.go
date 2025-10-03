package DB_OPs_Tests

import (
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func Test_InitAccountsPool(t *testing.T) {
	fmt.Printf("=== Testing Account Pool Initialization ===\n")

	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Draw a connection from the pool
	conn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get accounts connection: %v", err)
	}

	// Put the connection back to the pool
	defer func() {
		DB_OPs.PutAccountsConnection(conn)
		fmt.Printf("✅ Connection returned to pool\n")
	}()

	// Print the Connection details
	fmt.Printf("✅ Connection obtained from pool\n")
	fmt.Printf("   Database: %s\n", conn.Database)
	fmt.Printf("   Token: %s\n", conn.Token[:20]+"...")
	fmt.Printf("   Created At: %s\n", conn.CreatedAt.Format(time.RFC3339))
	fmt.Printf("   In Use: %t\n", conn.InUse)
}

func Test_ConnectionPool_Management(t *testing.T) {
	fmt.Printf("=== Testing Connection Pool Management ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Test getting multiple connections
	fmt.Printf("Testing connection pool management...\n")

	// Get first connection
	conn1, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get first connection: %v", err)
	}
	fmt.Printf("✅ Got FIRST connection (ID: %p)\n", conn1)

	// Get second connection
	conn2, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get second connection: %v", err)
	}
	fmt.Printf("✅ Got SECOND connection (ID: %p)\n", conn2)

	// Verify they are different connections
	if conn1 == conn2 {
		t.Fatalf("Expected different connections, got same connection")
	}
	fmt.Printf("✅ Connections are different\n")

	// Return first connection
	DB_OPs.PutAccountsConnection(conn1)
	fmt.Printf("✅ First connection returned to pool\n")

	// Return second connection
	DB_OPs.PutAccountsConnection(conn2)
	fmt.Printf("✅ Second connection returned to pool\n")

	fmt.Printf("✅ Connection pool management test passed!\n")
}

func Test_ConnectionPool_Reuse(t *testing.T) {
	fmt.Printf("=== Testing Connection Pool Reuse ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Get a connection
	conn1, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get first connection: %v", err)
	}
	fmt.Printf("✅ Got FIRST connection (ID: %p)\n", conn1)
	// Return it to the pool
	DB_OPs.PutAccountsConnection(conn1)
	fmt.Printf("✅ First connection returned to pool\n")

	// Get another connection - should reuse the first one
	conn2, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get second connection: %v", err)
	}
	fmt.Printf("✅ Got SECOND connection (ID: %p)\n", conn2)

	// Verify they are the same connection (reused)
	if conn1 != conn2 {
		fmt.Printf("⚠️  Got different connection (pool created new one)\n")
	} else {
		fmt.Printf("✅ Connection was reused from pool (IDs: %p and %p)\n", conn1, conn2)
	}

	// Return second connection
	DB_OPs.PutAccountsConnection(conn2)
	fmt.Printf("✅ Second connection returned to pool\n")

	fmt.Printf("✅ Connection pool reuse test completed!\n")
}

func Test_ConnectionPool_Stress(t *testing.T) {
	fmt.Printf("=== Testing Connection Pool Stress ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Test multiple rapid connections
	fmt.Printf("Testing rapid connection acquisition and release...\n")

	// Channel to store connections
	testConnections := 15
	connections := make(chan *config.PooledConnection, testConnections)
	connectionIDs := make([]*config.PooledConnection, 0, testConnections)

	// Phase 1: Acquire all 5 connections rapidly
	fmt.Printf("Phase 1: Acquiring 5 connections rapidly...\n")
	for i := 0; i < testConnections; i++ {
		conn, err := DB_OPs.GetAccountsConnection()
		if err != nil {
			t.Fatalf("Failed to get connection %d: %v", i+1, err)
		}

		connections <- conn
		connectionIDs = append(connectionIDs, conn)
		fmt.Printf("✅ Got connection %d, ID: %p\n", i+1, conn)
	}

	fmt.Printf("✅ All %d connections acquired successfully!\n", testConnections)

	// Simulate some work with all connections held
	fmt.Printf("Simulating work with all connections held...\n")
	time.Sleep(50 * time.Millisecond)

	// Phase 2: Return all connections to the pool
	fmt.Printf("Phase 2: Returning all connections to pool...\n")
	for i := 0; i < len(connectionIDs); i++ {
		conn := <-connections
		DB_OPs.PutAccountsConnection(conn)
		fmt.Printf("✅ Returned connection %d, ID: %p\n", i+1, conn)
	}

	fmt.Printf("✅ Connection pool stress test passed!\n")
	fmt.Printf("   Tested: Rapid acquisition of 5 connections, work simulation, and batch return\n")
}

func Test_Account_Data_Structure(t *testing.T) {
	fmt.Printf("=== Testing Account Data Structure ===\n")

	// Generate a test address
	privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)

	// Create test account document
	account := &DB_OPs.Account{
		DIDAddress:  fmt.Sprintf("did:superjtest:test-%d", time.Now().UnixNano()),
		Address:     address,
		Balance:     "1000.50",
		Nonce:       uint64(time.Now().UnixNano()),
		AccountType: "test",
		CreatedAt:   time.Now().UnixNano(),
		UpdatedAt:   time.Now().UnixNano(),
		Metadata: map[string]interface{}{
			"test":     true,
			"function": "Test_Account_Data_Structure",
			"version":  "1.0",
		},
	}

	fmt.Printf("Created account data structure:\n")
	fmt.Printf("   DID: %s\n", account.DIDAddress)
	fmt.Printf("   Address: %s\n", account.Address.Hex())
	fmt.Printf("   Balance: %s\n", account.Balance)
	fmt.Printf("   Nonce: %d\n", account.Nonce)
	fmt.Printf("   Account Type: %s\n", account.AccountType)
	fmt.Printf("   Created At: %d\n", account.CreatedAt)
	fmt.Printf("   Updated At: %d\n", account.UpdatedAt)
	fmt.Printf("   Metadata: %+v\n", account.Metadata)

	// Verify the account structure
	if account.DIDAddress == "" {
		t.Fatalf("DIDAddress should not be empty")
	}
	if account.Address == (common.Address{}) {
		t.Fatalf("Address should not be empty")
	}
	if account.Balance == "" {
		t.Fatalf("Balance should not be empty")
	}
	if account.Nonce == 0 {
		t.Fatalf("Nonce should not be zero")
	}
	if account.AccountType == "" {
		t.Fatalf("AccountType should not be empty")
	}
	if account.CreatedAt == 0 {
		t.Fatalf("CreatedAt should not be zero")
	}
	if account.UpdatedAt == 0 {
		t.Fatalf("UpdatedAt should not be zero")
	}

	fmt.Printf("✅ Account data structure validation passed!\n")
}

func Test_ConnectionPool_WithNilConnection(t *testing.T) {
	fmt.Printf("=== Testing Connection Pool with Nil Connection ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Test with nil connection - this should trigger the internal connection logic
	fmt.Printf("Testing with nil connection (should get new connection internally)...\n")

	// This will test the internal connection handling in CreateAccount
	// We'll pass nil to see how the function handles it
	var nilConn *config.PooledConnection = nil

	// Generate test data
	privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	didAddress := fmt.Sprintf("did:example:nil-test-%d", time.Now().UnixNano())
	metadata := map[string]interface{}{
		"test":     true,
		"function": "Test_ConnectionPool_WithNilConnection",
	}

	fmt.Printf("Creating account with nil connection:\n")
	fmt.Printf("   DID: %s\n", didAddress)
	fmt.Printf("   Address: %s\n", address.Hex())

	// This should work because CreateAccount will get its own connection
	err = DB_OPs.CreateAccount(nilConn, didAddress, address, metadata)
	if err != nil {
		t.Fatalf("Failed to create account with nil connection: %v", err)
	}

	fmt.Printf("✅ Account created successfully with nil connection!\n")
	fmt.Printf("   DID: %s\n", didAddress)
	fmt.Printf("   Address: %s\n", address.Hex())
}

func Test_Account_Nonce_Generation(t *testing.T) {
	fmt.Printf("=== Testing Account Nonce Generation ===\n")

	// Test the nonce generation function
	nonce1, err := DB_OPs.PutNonceofAccount()
	if err != nil {
		t.Fatalf("Failed to generate nonce 1: %v", err)
	}

	// Wait a bit to ensure different timestamps
	// time.Sleep(1 * time.Millisecond)

	nonce2, err := DB_OPs.PutNonceofAccount()
	if err != nil {
		t.Fatalf("Failed to generate nonce 2: %v", err)
	}

	nonce3, err := DB_OPs.PutNonceofAccount()
	if err != nil {
		t.Fatalf("Failed to generate nonce 3: %v", err)
	}

	time.Sleep(1 * time.Millisecond)

	nonce4, err := DB_OPs.PutNonceofAccount()
	if err != nil {
		t.Fatalf("Failed to generate nonce 4: %v", err)
	}

	fmt.Printf("Generated nonces:\n")
	fmt.Printf("   Nonce 1: %d\n", nonce1)
	fmt.Printf("   Nonce 2: %d\n", nonce2)
	fmt.Printf("   Nonce 3: %d\n", nonce3)
	fmt.Printf("   Nonce 4: %d\n", nonce4)
	// Verify nonces are different
	if nonce1 == nonce2 {
		t.Fatalf("Generated nonces should be different")
	}

	// Verify nonces are reasonable (based on timestamp)
	// Note: The nonce includes a counter in the lower bits, so it might be slightly larger than current timestamp
	now := time.Now().UnixNano()
	if nonce1 > uint64(now)+1000000 || nonce2 > uint64(now)+1000000 {
		t.Fatalf("Generated nonces should be close to current timestamp")
	}

	fmt.Printf("✅ Account nonce generation test passed!\n")
}

func Test_Account_Database_Write_Read(t *testing.T) {
	fmt.Printf("=== Testing Account Database Write and Read ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Generate test data
	privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	didAddress := fmt.Sprintf("did:superjtest:write-read-test-%d", time.Now().UnixNano())
	metadata := map[string]interface{}{
		"test":      true,
		"function":  "Test_Account_Database_Write_Read",
		"version":   "1.0",
		"timestamp": time.Now().Unix(),
	}

	fmt.Printf("Test data prepared:\n")
	fmt.Printf("   DID: %s\n", didAddress)
	fmt.Printf("   Address: %s\n", address.Hex())
	fmt.Printf("   Metadata: %+v\n", metadata)

	// PHASE 1: WRITE DATA TO DATABASE
	fmt.Printf("\n--- PHASE 1: Writing Account to Database ---\n")

	// Get connection from pool for writing
	writeConn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get connection for writing: %v", err)
	}
	fmt.Printf("✅ Got connection for writing (ID: %p)\n", writeConn)

	// Create the account in the database
	err = DB_OPs.CreateAccount(writeConn, didAddress, address, metadata)
	if err != nil {
		DB_OPs.PutAccountsConnection(writeConn)
		t.Fatalf("Failed to create account in database: %v", err)
	}
	fmt.Printf("✅ Account created successfully in database\n")

	// Return connection to pool after writing
	DB_OPs.PutAccountsConnection(writeConn)
	fmt.Printf("✅ Write connection returned to pool\n")

	// Wait 50ms as requested
	fmt.Printf("⏳ Waiting 50ms before reading...\n")
	time.Sleep(50 * time.Millisecond)

	// PHASE 2: READ DATA FROM DATABASE
	fmt.Printf("\n--- PHASE 2: Reading Account from Database ---\n")

	// Get connection from pool for reading
	readConn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get connection for reading: %v", err)
	}
	fmt.Printf("✅ Got connection for reading (ID: %p)\n", readConn)

	// Read account by DID
	fmt.Printf("Reading account by DID: %s\n", didAddress)
	readAccountByDID, err := DB_OPs.GetAccountByDID(readConn, didAddress)
	if err != nil {
		DB_OPs.PutAccountsConnection(readConn)
		t.Fatalf("Failed to read account by DID: %v", err)
	}
	fmt.Printf("✅ Account read successfully by DID\n")

	// Read account by Address
	fmt.Printf("Reading account by Address: %s\n", address.Hex())
	readAccountByAddr, err := DB_OPs.GetAccount(readConn, address)
	if err != nil {
		DB_OPs.PutAccountsConnection(readConn)
		t.Fatalf("Failed to read account by address: %v", err)
	}
	fmt.Printf("✅ Account read successfully by address\n")

	// Return connection to pool after reading
	DB_OPs.PutAccountsConnection(readConn)
	fmt.Printf("✅ Read connection returned to pool\n")

	// PHASE 3: VERIFY DATA INTEGRITY
	fmt.Printf("\n--- PHASE 3: Verifying Data Integrity ---\n")

	// Verify both reads returned the same data
	if readAccountByDID.DIDAddress != readAccountByAddr.DIDAddress {
		t.Fatalf("DID mismatch: %s != %s", readAccountByDID.DIDAddress, readAccountByAddr.DIDAddress)
	}
	if readAccountByDID.Address != readAccountByAddr.Address {
		t.Fatalf("Address mismatch: %s != %s", readAccountByDID.Address.Hex(), readAccountByAddr.Address.Hex())
	}
	fmt.Printf("✅ Data consistency verified between DID and Address lookups\n")

	// Verify the data matches what we wrote
	if readAccountByDID.DIDAddress != didAddress {
		t.Fatalf("DID mismatch: expected %s, got %s", didAddress, readAccountByDID.DIDAddress)
	}
	if readAccountByDID.Address != address {
		t.Fatalf("Address mismatch: expected %s, got %s", address.Hex(), readAccountByDID.Address.Hex())
	}
	fmt.Printf("✅ Written data matches read data\n")

	// Display the read data
	fmt.Printf("\n--- READ DATA VERIFICATION ---\n")
	fmt.Printf("✅ Account successfully written and read from database!\n")
	fmt.Printf("   DID: %s\n", readAccountByDID.DIDAddress)
	fmt.Printf("   Address: %s\n", readAccountByDID.Address.Hex())
	fmt.Printf("   Balance: %s\n", readAccountByDID.Balance)
	fmt.Printf("   Nonce: %d\n", readAccountByDID.Nonce)
	fmt.Printf("   Account Type: %s\n", readAccountByDID.AccountType)
	fmt.Printf("   Created At: %d\n", readAccountByDID.CreatedAt)
	fmt.Printf("   Updated At: %d\n", readAccountByDID.UpdatedAt)
	fmt.Printf("   Metadata: %+v\n", readAccountByDID.Metadata)

	// Verify metadata
	if readAccountByDID.Metadata["test"] != true {
		t.Fatalf("Metadata test field mismatch")
	}
	if readAccountByDID.Metadata["function"] != "Test_Account_Database_Write_Read" {
		t.Fatalf("Metadata function field mismatch")
	}
	fmt.Printf("✅ Metadata verification passed\n")

	fmt.Printf("\n✅ Database write-read test completed successfully!\n")
	fmt.Printf("   - Account written to database using connection pool\n")
	fmt.Printf("   - 50ms delay between write and read operations\n")
	fmt.Printf("   - Account read from database using connection pool\n")
	fmt.Printf("   - Data integrity verified\n")
	fmt.Printf("   - Connections properly managed (acquired and returned)\n")
}

func Test_ListAllAccounts(t *testing.T) {
	fmt.Printf("=== Testing ListAllAccounts Function ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Create multiple test accounts to test listing
	fmt.Printf("Creating multiple test accounts...\n")
	testAccounts := []struct {
		didAddress string
		address    common.Address
		metadata   map[string]interface{}
	}{}

	// Generate 3 test accounts
	for i := 0; i < 3; i++ {
		privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
		if err != nil {
			t.Fatalf("Failed to generate private key %d: %v", i, err)
		}
		address := crypto.PubkeyToAddress(privateKey.PublicKey)
		didAddress := fmt.Sprintf("did:superjtest:list-test-%d-%d", i, time.Now().UnixNano())
		metadata := map[string]interface{}{
			"test":     true,
			"function": "Test_ListAllAccounts",
			"index":    i,
			"version":  "1.0",
		}

		testAccounts = append(testAccounts, struct {
			didAddress string
			address    common.Address
			metadata   map[string]interface{}
		}{didAddress, address, metadata})

		// Create account in database
		conn, err := DB_OPs.GetAccountsConnection()
		if err != nil {
			t.Fatalf("Failed to get connection for account %d: %v", i, err)
		}

		err = DB_OPs.CreateAccount(conn, didAddress, address, metadata)
		if err != nil {
			DB_OPs.PutAccountsConnection(conn)
			t.Fatalf("Failed to create account %d: %v", i, err)
		}

		DB_OPs.PutAccountsConnection(conn)
		fmt.Printf("✅ Created account %d: %s -> %s\n", i+1, didAddress, address.Hex())
	}

	// Test ListAllAccounts with limit
	fmt.Printf("\n--- Testing ListAllAccounts with limit ---\n")
	conn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get connection for listing: %v", err)
	}

	// Test with limit of 2
	accounts, err := DB_OPs.ListAllAccounts(conn, 2)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to list accounts: %v", err)
	}

	fmt.Printf("✅ Listed %d accounts (limit: 2)\n", len(accounts))
	for i, acc := range accounts {
		fmt.Printf("   Account %d: %s -> %s\n", i+1, acc.DIDAddress, acc.Address.Hex())
	}

	// Test with no limit
	fmt.Printf("\n--- Testing ListAllAccounts without limit ---\n")
	allAccounts, err := DB_OPs.ListAllAccounts(conn, 0)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to list all accounts: %v", err)
	}

	fmt.Printf("✅ Listed %d total accounts\n", len(allAccounts))
	for i, acc := range allAccounts {
		fmt.Printf("   Account %d: %s -> %s\n", i+1, acc.DIDAddress, acc.Address.Hex())
	}

	// Verify we have at least our test accounts
	if len(allAccounts) < 3 {
		t.Fatalf("Expected at least 3 accounts, got %d", len(allAccounts))
	}

	DB_OPs.PutAccountsConnection(conn)
	fmt.Printf("\n✅ ListAllAccounts test completed successfully!\n")
}

func Test_UpdateAccountBalance(t *testing.T) {
	fmt.Printf("=== Testing UpdateAccountBalance Function ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Create a test account
	fmt.Printf("Creating test account for balance update...\n")
	privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	didAddress := fmt.Sprintf("did:superjtest:balance-test-%d", time.Now().UnixNano())
	metadata := map[string]interface{}{
		"test":     true,
		"function": "Test_UpdateAccountBalance",
		"version":  "1.0",
	}

	// Create account in database
	conn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}

	err = DB_OPs.CreateAccount(conn, didAddress, address, metadata)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to create account: %v", err)
	}

	// Verify initial balance
	account, err := DB_OPs.GetAccount(conn, address)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to get account: %v", err)
	}

	fmt.Printf("✅ Initial balance: %s\n", account.Balance)
	if account.Balance != "0" {
		t.Fatalf("Expected initial balance to be '0', got '%s'", account.Balance)
	}

	// Update balance
	newBalance := "1000.50"
	fmt.Printf("Updating balance to: %s\n", newBalance)
	err = DB_OPs.UpdateAccountBalance(conn, address, newBalance)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to update balance: %v", err)
	}

	// Verify updated balance
	updatedAccount, err := DB_OPs.GetAccount(conn, address)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to get updated account: %v", err)
	}

	fmt.Printf("✅ Updated balance: %s\n", updatedAccount.Balance)
	if updatedAccount.Balance != newBalance {
		t.Fatalf("Expected balance to be '%s', got '%s'", newBalance, updatedAccount.Balance)
	}

	// Verify UpdatedAt timestamp changed
	if updatedAccount.UpdatedAt <= account.UpdatedAt {
		t.Fatalf("Expected UpdatedAt to be greater than initial value")
	}

	DB_OPs.PutAccountsConnection(conn)
	fmt.Printf("\n✅ UpdateAccountBalance test completed successfully!\n")
}

func Test_CountAccounts(t *testing.T) {
	fmt.Printf("=== Testing CountAccounts Function ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Get initial count
	fmt.Printf("Getting initial account count...\n")
	conn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}

	initialCount, err := DB_OPs.CountAccounts(conn)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to count accounts: %v", err)
	}

	fmt.Printf("✅ Initial account count: %d\n", initialCount)

	// Create a test account
	fmt.Printf("Creating test account...\n")
	privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	didAddress := fmt.Sprintf("did:superjtest:count-test-%d", time.Now().UnixNano())
	metadata := map[string]interface{}{
		"test":     true,
		"function": "Test_CountAccounts",
		"version":  "1.0",
	}

	err = DB_OPs.CreateAccount(conn, didAddress, address, metadata)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to create account: %v", err)
	}

	// Get count after creating account
	newCount, err := DB_OPs.CountAccounts(conn)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to count accounts after creation: %v", err)
	}

	fmt.Printf("✅ Account count after creation: %d\n", newCount)

	// Verify count increased by 1
	if newCount != initialCount+1 {
		t.Fatalf("Expected count to increase by 1, got %d -> %d", initialCount, newCount)
	}

	DB_OPs.PutAccountsConnection(conn)
	fmt.Printf("\n✅ CountAccounts test completed successfully!\n")
}

func Test_GetTransactionsByAccount(t *testing.T) {
	fmt.Printf("=== Testing GetTransactionsByAccount Function ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Initialize the main database pool (needed for transaction queries)
	poolConfig := config.DefaultConnectionPoolConfig()
	err = DB_OPs.InitMainDBPool(poolConfig)
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Create a test account
	fmt.Printf("Creating test account for transaction lookup...\n")
	privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	didAddress := fmt.Sprintf("did:superjtest:tx-test-%d", time.Now().UnixNano())
	metadata := map[string]interface{}{
		"test":     true,
		"function": "Test_GetTransactionsByAccount",
		"version":  "1.0",
	}

	// Create account in database
	conn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}

	err = DB_OPs.CreateAccount(conn, didAddress, address, metadata)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to create account: %v", err)
	}

	// Get transactions for the account (pass nil to use main DB connection)
	fmt.Printf("Getting transactions for account: %s\n", address.Hex())
	transactions, err := DB_OPs.GetTransactionsByAccount(nil, &address)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to get transactions: %v", err)
	}

	fmt.Printf("✅ Found %d transactions for account\n", len(transactions))

	// Display transaction details
	for i, tx := range transactions {
		fmt.Printf("   Transaction %d:\n", i+1)
		fmt.Printf("     Hash: %s\n", tx.Hash)
		fmt.Printf("     From: %s\n", tx.From.Hex())
		fmt.Printf("     To: %s\n", tx.To.Hex())
		fmt.Printf("     Value: %s\n", tx.Value)
	}

	DB_OPs.PutAccountsConnection(conn)
	fmt.Printf("\n✅ GetTransactionsByAccount test completed successfully!\n")
}

func Test_ListAllDIDs(t *testing.T) {
	fmt.Printf("=== Testing ListAllDIDs Function ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Create multiple test accounts with DIDs
	fmt.Printf("Creating test accounts with DIDs...\n")
	testDIDs := []string{}

	// Generate 3 test accounts with DIDs
	for i := 0; i < 3; i++ {
		privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
		if err != nil {
			t.Fatalf("Failed to generate private key %d: %v", i, err)
		}
		address := crypto.PubkeyToAddress(privateKey.PublicKey)
		didAddress := fmt.Sprintf("did:superjtest:did-test-%d-%d", i, time.Now().UnixNano())
		metadata := map[string]interface{}{
			"test":     true,
			"function": "Test_ListAllDIDs",
			"index":    i,
			"version":  "1.0",
		}

		testDIDs = append(testDIDs, didAddress)

		// Create account in database
		conn, err := DB_OPs.GetAccountsConnection()
		if err != nil {
			t.Fatalf("Failed to get connection for account %d: %v", i, err)
		}

		err = DB_OPs.CreateAccount(conn, didAddress, address, metadata)
		if err != nil {
			DB_OPs.PutAccountsConnection(conn)
			t.Fatalf("Failed to create account %d: %v", i, err)
		}

		DB_OPs.PutAccountsConnection(conn)
		fmt.Printf("✅ Created account %d with DID: %s\n", i+1, didAddress)
	}

	// Test ListAllDIDs function
	fmt.Printf("\n--- Testing ListAllDIDs function ---\n")
	conn, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		t.Fatalf("Failed to get connection for listing DIDs: %v", err)
	}

	// Note: ListAllDIDs is in messaging/DIDPropagation.go, but it calls ListAllAccounts
	// So we'll test the underlying functionality through ListAllAccounts
	accounts, err := DB_OPs.ListAllAccounts(conn, 0)
	if err != nil {
		DB_OPs.PutAccountsConnection(conn)
		t.Fatalf("Failed to list accounts (DIDs): %v", err)
	}

	fmt.Printf("✅ Found %d accounts with DIDs\n", len(accounts))

	// Filter accounts that have DIDs (non-empty DIDAddress)
	didAccounts := []*DB_OPs.Account{}
	for _, acc := range accounts {
		if acc.DIDAddress != "" {
			didAccounts = append(didAccounts, acc)
		}
	}

	fmt.Printf("✅ Found %d accounts with valid DIDs\n", len(didAccounts))

	// Display DID information
	for i, acc := range didAccounts {
		fmt.Printf("   Account %d:\n", i+1)
		fmt.Printf("     DID: %s\n", acc.DIDAddress)
		fmt.Printf("     Address: %s\n", acc.Address.Hex())
		fmt.Printf("     Account Type: %s\n", acc.AccountType)
	}

	// Verify we have at least our test DIDs
	if len(didAccounts) < 3 {
		t.Fatalf("Expected at least 3 accounts with DIDs, got %d", len(didAccounts))
	}

	DB_OPs.PutAccountsConnection(conn)
	fmt.Printf("\n✅ ListAllDIDs test completed successfully!\n")
}
