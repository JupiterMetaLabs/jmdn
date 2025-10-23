package tests

import (
	"context"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/Service/Logger"
	"gossipnode/gETH/Facade/Service/Utils"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

var balanceService *Service.ServiceImpl
var balanceOnce sync.Once
var balanceOnceAccounts sync.Once

func init_balance_logger() {
	// Initialize the DB first
	balanceOnce.Do(func() { DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig()) })
	balanceOnceAccounts.Do(func() { DB_OPs.InitAccountsPool() })
	err := Logger.InitLogger()
	if err != nil {
		panic(err)
	}
	balanceService = Service.NewService().(*Service.ServiceImpl)
}

// Test_Balance_ExistingAccount tests the Balance function for an existing account
func Test_Balance_ExistingAccount(t *testing.T) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test with a known address that should exist
	testAddress := "0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2"
	blockNumber := big.NewInt(0)
	network := "testnet"

	balance, err := balanceService.Balance(ctx, testAddress, blockNumber, network)
	if err != nil {
		t.Logf("Balance call returned error (this might be expected if account doesn't exist): %v", err)
		// If account doesn't exist, the function should create it and return 0
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			t.Logf("Account not found, function should create new account with zero balance")
		} else {
			t.Fatalf("Unexpected error: %v", err)
		}
	} else {
		t.Logf("Balance for address %s: %s", testAddress, balance.String())
		if balance.Cmp(big.NewInt(0)) < 0 {
			t.Errorf("Balance should not be negative, got: %s", balance.String())
		}
	}
}

// Test_Balance_NewAccount tests the Balance function for a new account (should create account)
func Test_Balance_NewAccount(t *testing.T) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use a random address that likely doesn't exist
	testAddress := "0x1234567890123456789012345678901234567890"
	blockNumber := big.NewInt(0)
	network := "testnet"

	balance, err := balanceService.Balance(ctx, testAddress, blockNumber, network)
	if err != nil {
		t.Fatalf("Failed to get balance for new account: %v", err)
	}

	// New account should have zero balance
	expectedBalance := big.NewInt(0)
	if balance.Cmp(expectedBalance) != 0 {
		t.Errorf("Expected balance %s, got %s", expectedBalance.String(), balance.String())
	}

	t.Logf("Successfully created new account with zero balance: %s", balance.String())
}

// Test_Balance_InvalidAddress tests the Balance function with invalid address formats
func Test_Balance_InvalidAddress(t *testing.T) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testCases := []struct {
		name    string
		address string
		network string
	}{
		{
			name:    "Empty address",
			address: "",
			network: "testnet",
		},
		{
			name:    "Invalid hex address",
			address: "0xinvalid",
			network: "testnet",
		},
		{
			name:    "Too short address",
			address: "0x123",
			network: "testnet",
		},
		{
			name:    "Too long address",
			address: "0x1234567890123456789012345678901234567890123456789012345678901234567890",
			network: "testnet",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			blockNumber := big.NewInt(0)

			// This should either fail gracefully or handle the invalid address
			balance, err := balanceService.Balance(ctx, tc.address, blockNumber, tc.network)
			if err != nil {
				t.Logf("Expected error for invalid address %s: %v", tc.address, err)
			} else {
				t.Logf("Balance for invalid address %s: %s", tc.address, balance.String())
			}
		})
	}
}

// Test_Balance_ContextTimeout tests the Balance function with context timeout
func Test_Balance_ContextTimeout(t *testing.T) {
	init_balance_logger()

	// Create a context with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	testAddress := "0x630f87b95c414056572380164d7edbfb6c3b20f8"
	blockNumber := big.NewInt(0)
	network := "testnet"

	// This should timeout
	balance, err := balanceService.Balance(ctx, testAddress, blockNumber, network)
	if err == nil {
		t.Errorf("Expected timeout error, but got balance: %s", balance.String())
	} else {
		t.Logf("Expected timeout error: %v", err)
	}
}

// Test_Balance_DifferentNetworks tests the Balance function with different network values
func Test_Balance_DifferentNetworks(t *testing.T) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testAddress := "0x9876543210987654321098765432109876543210"
	blockNumber := big.NewInt(0)

	networks := []string{
		"mainnet",
		"testnet",
		"devnet",
		"localhost",
		"custom-network",
	}

	for _, network := range networks {
		t.Run(fmt.Sprintf("Network_%s", network), func(t *testing.T) {
			balance, err := balanceService.Balance(ctx, testAddress, blockNumber, network)
			if err != nil {
				t.Logf("Balance call for network %s returned error: %v", network, err)
			} else {
				t.Logf("Balance for network %s: %s", network, balance.String())
				if balance.Cmp(big.NewInt(0)) < 0 {
					t.Errorf("Balance should not be negative, got: %s", balance.String())
				}
			}
		})
	}
}

// Test_Balance_DifferentBlockNumbers tests the Balance function with different block numbers
func Test_Balance_DifferentBlockNumbers(t *testing.T) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testAddress := "0x1111111111111111111111111111111111111111"
	network := "testnet"

	blockNumbers := []*big.Int{
		big.NewInt(0),
		big.NewInt(1),
		big.NewInt(100),
		big.NewInt(1000),
		big.NewInt(10000),
	}

	for _, blockNumber := range blockNumbers {
		t.Run(fmt.Sprintf("Block_%s", blockNumber.String()), func(t *testing.T) {
			balance, err := balanceService.Balance(ctx, testAddress, blockNumber, network)
			if err != nil {
				t.Logf("Balance call for block %s returned error: %v", blockNumber.String(), err)
			} else {
				t.Logf("Balance for block %s: %s", blockNumber.String(), balance.String())
				if balance.Cmp(big.NewInt(0)) < 0 {
					t.Errorf("Balance should not be negative, got: %s", balance.String())
				}
			}
		})
	}
}

// Test_Balance_ConcurrentAccess tests the Balance function with concurrent access
func Test_Balance_ConcurrentAccess(t *testing.T) {
	init_balance_logger()

	const numGoroutines = 10
	const numCalls = 5

	addresses := []string{
		"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"0xcccccccccccccccccccccccccccccccccccccccc",
		"0xdddddddddddddddddddddddddddddddddddddddd",
		"0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	}

	network := "testnet"
	blockNumber := big.NewInt(0)

	var wg sync.WaitGroup
	results := make(chan error, numGoroutines*numCalls)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numCalls; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

				address := addresses[j%len(addresses)]
				_, err := balanceService.Balance(ctx, address, blockNumber, network)
				results <- err

				cancel()
			}
		}(i)
	}

	wg.Wait()
	close(results)

	// Check results
	successCount := 0
	errorCount := 0

	for err := range results {
		if err != nil {
			errorCount++
			t.Logf("Concurrent balance call error: %v", err)
		} else {
			successCount++
		}
	}

	t.Logf("Concurrent test results: %d successful, %d errors", successCount, errorCount)
}

// Test_Balance_EdgeCases tests edge cases for the Balance function
func Test_Balance_EdgeCases(t *testing.T) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testCases := []struct {
		name        string
		address     string
		blockNumber *big.Int
		network     string
		expectError bool
	}{
		{
			name:        "Zero address",
			address:     "0x0000000000000000000000000000000000000000",
			blockNumber: big.NewInt(0),
			network:     "testnet",
			expectError: false,
		},
		{
			name:        "Max uint64 block number",
			address:     "0x1234567890123456789012345678901234567890",
			blockNumber: new(big.Int).SetUint64(^uint64(0)),
			network:     "testnet",
			expectError: false,
		},
		{
			name:        "Negative block number",
			address:     "0x1234567890123456789012345678901234567890",
			blockNumber: big.NewInt(-1),
			network:     "testnet",
			expectError: false,
		},
		{
			name:        "Empty network",
			address:     "0x1234567890123456789012345678901234567890",
			blockNumber: big.NewInt(0),
			network:     "",
			expectError: false,
		},
		{
			name:        "Very long network name",
			address:     "0x1234567890123456789012345678901234567890",
			blockNumber: big.NewInt(0),
			network:     strings.Repeat("a", 1000),
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			balance, err := balanceService.Balance(ctx, tc.address, tc.blockNumber, tc.network)

			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error for test case %s, but got balance: %s", tc.name, balance.String())
				}
			} else {
				if err != nil {
					t.Logf("Test case %s returned error (might be expected): %v", tc.name, err)
				} else {
					t.Logf("Test case %s returned balance: %s", tc.name, balance.String())
					if balance.Cmp(big.NewInt(0)) < 0 {
						t.Errorf("Balance should not be negative, got: %s", balance.String())
					}
				}
			}
		})
	}
}

// Test_Balance_UtilsIntegration tests the integration with Utils functions
func Test_Balance_UtilsIntegration(t *testing.T) {
	init_balance_logger()

	// Test address conversion
	testAddress := "0x630f87b95c414056572380164d7edbfb6c3b20f8"
	convertedAddress := Utils.ConvertAddress(testAddress)

	if convertedAddress == (common.Address{}) {
		t.Errorf("Address conversion failed for %s", testAddress)
	}

	t.Logf("Converted address: %s", convertedAddress.Hex())

	// Test balance conversion
	testBalance := "1000000000000000000" // 1 ETH in wei
	convertedBalance, err := Utils.ConvertBalance(testBalance)
	if err != nil {
		t.Errorf("Balance conversion failed: %v", err)
	}

	expectedBalance := big.NewInt(1000000000000000000)
	if convertedBalance.Cmp(expectedBalance) != 0 {
		t.Errorf("Expected balance %s, got %s", expectedBalance.String(), convertedBalance.String())
	}

	t.Logf("Converted balance: %s", convertedBalance.String())
}

// Test_Balance_ErrorHandling tests various error scenarios
func Test_Balance_ErrorHandling(t *testing.T) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test with nil context (should panic or handle gracefully)
	t.Run("NilContext", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Recovered from panic with nil context: %v", r)
			}
		}()

		// This might panic, which is expected
		_, err := balanceService.Balance(context.TODO(), "0x1234567890123456789012345678901234567890", big.NewInt(0), "testnet")
		if err != nil {
			t.Logf("Nil context error (expected): %v", err)
		}
	})

	// Test with nil block number
	t.Run("NilBlockNumber", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Recovered from panic with nil block number: %v", r)
			}
		}()

		_, err := balanceService.Balance(ctx, "0x1234567890123456789012345678901234567890", nil, "testnet")
		if err != nil {
			t.Logf("Nil block number error (expected): %v", err)
		}
	})
}

// Benchmark_Balance tests the performance of the Balance function
func Benchmark_Balance(b *testing.B) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testAddress := "0x630f87b95c414056572380164d7edbfb6c3b20f8"
	blockNumber := big.NewInt(0)
	network := "testnet"

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := balanceService.Balance(ctx, testAddress, blockNumber, network)
		if err != nil {
			b.Logf("Balance call error: %v", err)
		}
	}
}

// Test_Balance_IntegrationWithDatabase tests the integration with the database layer
func Test_Balance_IntegrationWithDatabase(t *testing.T) {
	init_balance_logger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Test creating an account and then retrieving its balance
	testAddress := "0x9999999999999999999999999999999999999999"
	network := "testnet"
	blockNumber := big.NewInt(0)

	// First call should create the account
	balance1, err1 := balanceService.Balance(ctx, testAddress, blockNumber, network)
	if err1 != nil {
		t.Fatalf("First balance call failed: %v", err1)
	}

	if balance1.Cmp(big.NewInt(0)) != 0 {
		t.Errorf("Expected zero balance for new account, got: %s", balance1.String())
	}

	// Second call should retrieve the existing account
	balance2, err2 := balanceService.Balance(ctx, testAddress, blockNumber, network)
	if err2 != nil {
		t.Fatalf("Second balance call failed: %v", err2)
	}

	if balance2.Cmp(big.NewInt(0)) != 0 {
		t.Errorf("Expected zero balance for existing account, got: %s", balance2.String())
	}

	// Both calls should return the same balance
	if balance1.Cmp(balance2) != 0 {
		t.Errorf("Balances should be equal, got %s and %s", balance1.String(), balance2.String())
	}

	t.Logf("Successfully tested database integration: balance %s", balance1.String())
}
