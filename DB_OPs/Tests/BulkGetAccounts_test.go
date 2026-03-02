package DB_OPs_Tests

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"jmdn/DB_OPs"
	"jmdn/config/settings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func Test_GetMultipleAccounts(t *testing.T) {
	// Initialize settings for logger
	if _, err := settings.Load(); err != nil {
		t.Logf("Failed to load settings: %v", err)
	}

	fmt.Printf("=== Testing GetMultipleAccounts Function ===\n")

	// Initialize the accounts pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts pool: %v", err)
	}

	// Create test accounts
	fmt.Printf("Creating test accounts used for bulk retrieval...\n")
	numAccounts := 5
	accounts := make([]struct {
		didAddress string
		address    common.Address
		metadata   map[string]interface{}
	}, 0, numAccounts)

	// Keep track of addresses to query later
	queryAddresses := make([]common.Address, 0, numAccounts)

	// Get a connection for creating accounts
	conn, err := DB_OPs.GetAccountsConnections(context.Background())
	if err != nil {
		t.Fatalf("Failed to get connection for setup: %v", err)
	}
	defer DB_OPs.PutAccountsConnection(conn)

	for i := 0; i < numAccounts; i++ {
		privateKey, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
		if err != nil {
			t.Fatalf("Failed to generate private key %d: %v", i, err)
		}
		address := crypto.PubkeyToAddress(privateKey.PublicKey)
		didAddress := fmt.Sprintf("did:superjtest:bulk-test-%d-%d", i, time.Now().UTC().UnixNano())
		metadata := map[string]interface{}{
			"test":     true,
			"function": "Test_GetMultipleAccounts",
			"index":    i,
		}

		accounts = append(accounts, struct {
			didAddress string
			address    common.Address
			metadata   map[string]interface{}
		}{didAddress, address, metadata})

		queryAddresses = append(queryAddresses, address)

		err = DB_OPs.CreateAccount(conn, didAddress, address, metadata)
		if err != nil {
			t.Fatalf("Failed to create account %d: %v", i, err)
		}
		fmt.Printf("✅ Created account %d: %s -> %s\n", i+1, didAddress, address.Hex())
	}

	// Also add a non-existent address to query
	nonExistentAddr := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	queryAddresses = append(queryAddresses, nonExistentAddr)
	fmt.Printf("Added non-existent address to query: %s\n", nonExistentAddr.Hex())

	// Test GetMultipleAccounts
	fmt.Printf("\n--- Retrieving Multiple Accounts ---\n")

	// Pass nil connection to let the function handle it, or pass `conn` since we have one.
	// The function signature takes *config.PooledConnection.
	// Our `conn` is of that type. Let's reuse it to test that path,
	// or pass nil to test internal acquisition.
	// Let's pass nil to test the full flow as SecurityCache would use it (or similar).

	// Create AccountsSet for query
	queryAccountsSet := DB_OPs.NewAccountsSet()
	for _, addr := range queryAddresses {
		queryAccountsSet.Add(addr)
	}

	retrievedAccounts, err := DB_OPs.GetMultipleAccounts(nil, queryAccountsSet)
	if err != nil {
		t.Fatalf("Failed to bulk get accounts: %v", err)
	}

	fmt.Printf("✅ Retrieved %d accounts (queried %d)\n", len(retrievedAccounts), len(queryAddresses))

	// Verification
	for _, expected := range accounts {
		addrHex := expected.address.Hex()
		acc, exists := retrievedAccounts[addrHex]

		if !exists {
			t.Errorf("Failed to retrieve account: %s", addrHex)
			continue
		}

		if acc.DIDAddress != expected.didAddress {
			t.Errorf("Account %s DID mismatch: matched %s, got %s", addrHex, expected.didAddress, acc.DIDAddress)
		}
		fmt.Printf("   Verified account: %s OK\n", addrHex)
	}

	// Verify non-existent account is NOT in the map
	if _, exists := retrievedAccounts[nonExistentAddr.Hex()]; exists {
		t.Errorf("Non-existent account %s should not be retrieved", nonExistentAddr.Hex())
	} else {
		fmt.Printf("   Verified non-existent account properly skipped\n")
	}

	fmt.Printf("\n✅ GetMultipleAccounts test completed successfully!\n")
}
