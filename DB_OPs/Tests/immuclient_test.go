package DB_OPs_Tests

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"jmdn/DB_OPs"
	"jmdn/config"
)

// Test_Create_Read_Update tests the basic CRUD operations
func Test_Create_Read_Update(t *testing.T) {
	fmt.Printf("=== Testing Create, Read, Update Operations ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Test data
	testKey := fmt.Sprintf("test:create-read-update-%d", time.Now().UTC().UnixNano())
	testValue := map[string]interface{}{
		"message":   "Hello, ImmuDB!",
		"timestamp": time.Now().UTC().Unix(),
		"test":      true,
	}

	// Test Create
	fmt.Printf("Testing Create operation...\n")
	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	err = DB_OPs.Create(conn, testKey, testValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to create key: %v", err)
	}
	fmt.Printf("✅ Created key: %s\n", testKey)

	// Test Read
	fmt.Printf("Testing Read operation...\n")
	data, err := DB_OPs.Read(conn, testKey)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to read key: %v", err)
	}

	var retrievedValue map[string]interface{}
	err = json.Unmarshal(data, &retrievedValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to unmarshal data: %v", err)
	}

	fmt.Printf("✅ Read key: %s\n", testKey)
	fmt.Printf("   Retrieved value: %+v\n", retrievedValue)

	// Verify data integrity
	if retrievedValue["message"] != testValue["message"] {
		t.Fatalf("Data integrity check failed: expected %v, got %v", testValue["message"], retrievedValue["message"])
	}

	// Test Update
	fmt.Printf("Testing Update operation...\n")
	updatedValue := map[string]interface{}{
		"message":   "Hello, Updated ImmuDB!",
		"timestamp": time.Now().UTC().Unix(),
		"test":      true,
		"updated":   true,
	}

	err = DB_OPs.Update(testKey, updatedValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to update key: %v", err)
	}

	// Verify update
	data, err = DB_OPs.Read(conn, testKey)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to read updated key: %v", err)
	}

	var updatedRetrievedValue map[string]interface{}
	err = json.Unmarshal(data, &updatedRetrievedValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to unmarshal updated data: %v", err)
	}

	fmt.Printf("✅ Updated key: %s\n", testKey)
	fmt.Printf("   Updated value: %+v\n", updatedRetrievedValue)

	// Verify update integrity
	if updatedRetrievedValue["message"] != updatedValue["message"] {
		t.Fatalf("Update integrity check failed: expected %v, got %v", updatedValue["message"], updatedRetrievedValue["message"])
	}

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ Create, Read, Update test completed successfully!\n")
}

// Test_ReadJSON tests the ReadJSON functionality
func Test_ReadJSON(t *testing.T) {
	fmt.Printf("=== Testing ReadJSON Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Test data
	testKey := fmt.Sprintf("test:readjson-%d", time.Now().UTC().UnixNano())
	testValue := map[string]interface{}{
		"name":   "Test User",
		"age":    30,
		"active": true,
		"tags":   []string{"test", "json", "immudb"},
	}

	// Create test data
	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	err = DB_OPs.Create(conn, testKey, testValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to create test data: %v", err)
	}

	// Test ReadJSON
	fmt.Printf("Testing ReadJSON operation...\n")
	var retrievedValue map[string]interface{}
	err = DB_OPs.ReadJSON(testKey, &retrievedValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to read JSON: %v", err)
	}

	fmt.Printf("✅ ReadJSON key: %s\n", testKey)
	fmt.Printf("   Retrieved value: %+v\n", retrievedValue)

	// Verify data integrity
	if retrievedValue["name"] != testValue["name"] {
		t.Fatalf("JSON read integrity check failed: expected %v, got %v", testValue["name"], retrievedValue["name"])
	}

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ ReadJSON test completed successfully!\n")
}

// Test_GetKeys tests the GetKeys functionality
func Test_GetKeys(t *testing.T) {
	fmt.Printf("=== Testing GetKeys Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Create test data with prefix
	prefix := fmt.Sprintf("test:getkeys-%d", time.Now().UTC().UnixNano())
	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Create multiple test keys
	testKeys := []string{
		fmt.Sprintf("%s:key1", prefix),
		fmt.Sprintf("%s:key2", prefix),
		fmt.Sprintf("%s:key3", prefix),
	}

	for i, key := range testKeys {
		value := map[string]interface{}{
			"index":     i + 1,
			"key":       key,
			"timestamp": time.Now().UTC().Unix(),
		}
		err = DB_OPs.Create(conn, key, value)
		if err != nil {
			DB_OPs.PutMainDBConnection(conn)
			t.Fatalf("Failed to create test key %s: %v", key, err)
		}
		fmt.Printf("✅ Created test key: %s\n", key)
	}

	// Test GetKeys with limit
	fmt.Printf("Testing GetKeys with limit...\n")
	keys, err := DB_OPs.GetKeys(conn, prefix, 2)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to get keys: %v", err)
	}

	fmt.Printf("✅ Found %d keys with prefix (limit: 2):\n", len(keys))
	for i, key := range keys {
		fmt.Printf("   Key %d: %s\n", i+1, key)
	}

	// Test GetKeys without limit
	fmt.Printf("Testing GetKeys without limit...\n")
	allKeys, err := DB_OPs.GetKeys(conn, prefix, 0)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to get all keys: %v", err)
	}

	fmt.Printf("✅ Found %d total keys with prefix:\n", len(allKeys))
	for i, key := range allKeys {
		fmt.Printf("   Key %d: %s\n", i+1, key)
	}

	// Verify we got at least our test keys
	if len(allKeys) < len(testKeys) {
		t.Fatalf("Expected at least %d keys, got %d", len(testKeys), len(allKeys))
	}

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ GetKeys test completed successfully!\n")
}

// Test_GetAllKeys tests the GetAllKeys functionality
func Test_GetAllKeys(t *testing.T) {
	fmt.Printf("=== Testing GetAllKeys Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Create test data with prefix
	prefix := fmt.Sprintf("test:getallkeys-%d", time.Now().UTC().UnixNano())
	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Create multiple test keys
	testKeys := []string{
		fmt.Sprintf("%s:batch1", prefix),
		fmt.Sprintf("%s:batch2", prefix),
		fmt.Sprintf("%s:batch3", prefix),
	}

	for i, key := range testKeys {
		value := map[string]interface{}{
			"batch":     i + 1,
			"key":       key,
			"timestamp": time.Now().UTC().Unix(),
		}
		err = DB_OPs.Create(conn, key, value)
		if err != nil {
			DB_OPs.PutMainDBConnection(conn)
			t.Fatalf("Failed to create test key %s: %v", key, err)
		}
		fmt.Printf("✅ Created test key: %s\n", key)
	}

	// Test GetAllKeys
	fmt.Printf("Testing GetAllKeys operation...\n")
	allKeys, err := DB_OPs.GetAllKeys(conn, prefix)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to get all keys: %v", err)
	}

	fmt.Printf("✅ Found %d total keys with prefix:\n", len(allKeys))
	for i, key := range allKeys {
		fmt.Printf("   Key %d: %s\n", i+1, key)
	}

	// Verify we got at least our test keys
	if len(allKeys) < len(testKeys) {
		t.Fatalf("Expected at least %d keys, got %d", len(testKeys), len(allKeys))
	}

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ GetAllKeys test completed successfully!\n")
}

// Test_CountAllKeys tests the CountAllKeys functionality
func Test_CountAllKeys(t *testing.T) {
	fmt.Printf("=== Testing CountAllKeys Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	prefix := DB_OPs.DEFAULT_PREFIX_TX

	// Test CountAllKeys
	fmt.Printf("Testing CountAllKeys operation...\n")
	start := time.Now()
	count, err := DB_OPs.CountBuilder{}.GetMainDBCount(prefix)
	if err != nil {
		DB_OPs.PutMainDBConnection(nil)
		t.Fatalf("Failed to count keys: %v", err)
	}

	fmt.Printf("✅ Found %d keys with prefix\n", count)

	elapsed := time.Since(start)
	fmt.Printf("✅ CountAllKeys operation took %s\n", elapsed)

	DB_OPs.PutMainDBConnection(nil)
	fmt.Printf("✅ CountAllKeys test completed successfully!\n")
}

// Test_CountAllKeys tests the CountAllKeys functionality
func Test_CountAllKeysAccountsDB(t *testing.T) {
	fmt.Printf("=== Testing CountAllKeys AccountsDB Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitAccountsPool()
	if err != nil {
		t.Fatalf("Failed to initialize accounts DB pool: %v", err)
	}

	prefix := DB_OPs.Prefix

	// Test CountAllKeys
	fmt.Printf("Testing CountAllKeys operation...\n")
	start := time.Now()
	count, err := DB_OPs.CountBuilder{}.GetAccountsDBCount(prefix)
	if err != nil {
		DB_OPs.PutAccountsConnection(nil)
		t.Fatalf("Failed to count keys: %v", err)
	}

	fmt.Printf("✅ Found %d keys with prefix\n", count)

	elapsed := time.Since(start)
	fmt.Printf("✅ CountAllKeys operation took %s\n", elapsed)

	DB_OPs.PutAccountsConnection(nil)
	fmt.Printf("✅ CountAllKeys test completed successfully!\n")
}

// Test_BatchCreate tests the BatchCreate functionality
func Test_BatchCreate(t *testing.T) {
	fmt.Printf("=== Testing BatchCreate Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Prepare batch data
	prefix := fmt.Sprintf("test:batch-%d", time.Now().UTC().UnixNano())
	batchData := map[string]interface{}{
		fmt.Sprintf("%s:item1", prefix): map[string]interface{}{
			"id":     1,
			"name":   "Item 1",
			"active": true,
		},
		fmt.Sprintf("%s:item2", prefix): map[string]interface{}{
			"id":     2,
			"name":   "Item 2",
			"active": false,
		},
		fmt.Sprintf("%s:item3", prefix): map[string]interface{}{
			"id":     3,
			"name":   "Item 3",
			"active": true,
		},
	}

	// Test BatchCreate
	fmt.Printf("Testing BatchCreate operation...\n")
	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	err = DB_OPs.BatchCreate(conn, batchData)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to batch create: %v", err)
	}

	fmt.Printf("✅ Batch created %d items\n", len(batchData))

	// Verify each item was created
	for key, expectedValue := range batchData {
		data, err := DB_OPs.Read(conn, key)
		if err != nil {
			DB_OPs.PutMainDBConnection(conn)
			t.Fatalf("Failed to read batch created key %s: %v", key, err)
		}

		var retrievedValue map[string]interface{}
		err = json.Unmarshal(data, &retrievedValue)
		if err != nil {
			DB_OPs.PutMainDBConnection(conn)
			t.Fatalf("Failed to unmarshal batch created data for key %s: %v", key, err)
		}

		expectedMap := expectedValue.(map[string]interface{})
		// Convert to float64 for comparison since JSON unmarshaling converts numbers to float64
		expectedID := float64(expectedMap["id"].(int))
		retrievedID := retrievedValue["id"].(float64)
		if retrievedID != expectedID {
			t.Fatalf("Batch create integrity check failed for key %s: expected id %v, got %v", key, expectedID, retrievedID)
		}

		fmt.Printf("   ✅ Verified key: %s\n", key)
	}

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ BatchCreate test completed successfully!\n")
}

// Test_SafeCreate_SafeRead tests the SafeCreate and SafeRead functionality
func Test_SafeCreate_SafeRead(t *testing.T) {
	fmt.Printf("=== Testing SafeCreate and SafeRead Operations ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Test data
	testKey := fmt.Sprintf("test:safe-%d", time.Now().UTC().UnixNano())
	testValue := map[string]interface{}{
		"message":   "Safe operation test",
		"timestamp": time.Now().UTC().Unix(),
		"verified":  true,
	}

	// Test SafeCreate
	fmt.Printf("Testing SafeCreate operation...\n")
	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	err = DB_OPs.SafeCreate(conn.Client, testKey, testValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to safe create: %v", err)
	}

	fmt.Printf("✅ Safe created key: %s\n", testKey)

	// Test SafeRead
	fmt.Printf("Testing SafeRead operation...\n")
	data, err := DB_OPs.SafeRead(conn.Client, testKey)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to safe read: %v", err)
	}

	var retrievedValue map[string]interface{}
	err = json.Unmarshal(data, &retrievedValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to unmarshal safe read data: %v", err)
	}

	fmt.Printf("✅ Safe read key: %s\n", testKey)
	fmt.Printf("   Retrieved value: %+v\n", retrievedValue)

	// Verify data integrity
	if retrievedValue["message"] != testValue["message"] {
		t.Fatalf("Safe operation integrity check failed: expected %v, got %v", testValue["message"], retrievedValue["message"])
	}

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ SafeCreate and SafeRead test completed successfully!\n")
}

// Test_Exists tests the Exists functionality
func Test_Exists(t *testing.T) {
	fmt.Printf("=== Testing Exists Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Test data
	existingKey := fmt.Sprintf("test:exists-%d", time.Now().UTC().UnixNano())
	nonExistingKey := fmt.Sprintf("test:nonexistent-%d", time.Now().UTC().UnixNano())

	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Create a key
	testValue := map[string]interface{}{
		"message":   "Exists test",
		"timestamp": time.Now().UTC().Unix(),
	}
	err = DB_OPs.Create(conn, existingKey, testValue)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to create test key: %v", err)
	}

	// Test Exists for existing key
	fmt.Printf("Testing Exists for existing key...\n")
	exists, err := DB_OPs.Exists(conn, existingKey)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to check existence of existing key: %v", err)
	}
	if !exists {
		t.Fatalf("Expected existing key to exist, but it doesn't")
	}
	fmt.Printf("✅ Existing key exists: %s\n", existingKey)

	// Test Exists for non-existing key
	fmt.Printf("Testing Exists for non-existing key...\n")
	exists, err = DB_OPs.Exists(conn, nonExistingKey)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to check existence of non-existing key: %v", err)
	}
	if exists {
		t.Fatalf("Expected non-existing key to not exist, but it does")
	}
	fmt.Printf("✅ Non-existing key does not exist: %s\n", nonExistingKey)

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ Exists test completed successfully!\n")
}

// Test_GetMerkleRoot tests the GetMerkleRoot functionality
func Test_GetMerkleRoot(t *testing.T) {
	fmt.Printf("=== Testing GetMerkleRoot Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Test GetMerkleRoot
	fmt.Printf("Testing GetMerkleRoot operation...\n")
	merkleRoot, err := DB_OPs.GetMerkleRoot(conn)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to get Merkle root: %v", err)
	}

	if len(merkleRoot) == 0 {
		t.Fatalf("Merkle root should not be empty")
	}

	fmt.Printf("✅ Retrieved Merkle root: %x\n", merkleRoot)

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ GetMerkleRoot test completed successfully!\n")
}

// Test_GetDatabaseState tests the GetDatabaseState functionality
func Test_GetDatabaseState(t *testing.T) {
	fmt.Printf("=== Testing GetDatabaseState Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Test GetDatabaseState
	fmt.Printf("Testing GetDatabaseState operation...\n")
	state, err := DB_OPs.GetDatabaseState(conn.Client)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to get database state: %v", err)
	}

	if state == nil {
		t.Fatalf("Database state should not be nil")
	}

	fmt.Printf("✅ Retrieved database state:\n")
	fmt.Printf("   Transaction ID: %d\n", state.TxId)
	fmt.Printf("   Transaction Hash: %x\n", state.TxHash)

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ GetDatabaseState test completed successfully!\n")
}

// Test_IsHealthy tests the IsHealthy functionality
func Test_IsHealthy(t *testing.T) {
	fmt.Printf("=== Testing IsHealthy Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Test IsHealthy
	fmt.Printf("Testing IsHealthy operation...\n")
	healthy := DB_OPs.IsHealthy(conn.Client)
	if !healthy {
		t.Fatalf("Database should be healthy")
	}

	fmt.Printf("✅ Database is healthy: %v\n", healthy)

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ IsHealthy test completed successfully!\n")
}

// Test_Ping tests the Ping functionality
func Test_Ping(t *testing.T) {
	fmt.Printf("=== Testing Ping Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Test Ping
	fmt.Printf("Testing Ping operation...\n")
	err = DB_OPs.Ping(conn.Client)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Ping failed: %v", err)
	}

	fmt.Printf("✅ Ping successful\n")

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ Ping test completed successfully!\n")
}

// Test_Transaction tests the Transaction functionality
func Test_Transaction(t *testing.T) {
	fmt.Printf("=== Testing Transaction Operation ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Test Transaction
	fmt.Printf("Testing Transaction operation...\n")
	prefix := fmt.Sprintf("test:transaction-%d", time.Now().UTC().UnixNano())

	err = DB_OPs.Transaction(conn.Client, func(tx *config.ImmuTransaction) error {
		// Add multiple operations to the transaction
		err := DB_OPs.Set(tx, fmt.Sprintf("%s:key1", prefix), map[string]interface{}{
			"id":      1,
			"message": "Transaction item 1",
		})
		if err != nil {
			return err
		}

		err = DB_OPs.Set(tx, fmt.Sprintf("%s:key2", prefix), map[string]interface{}{
			"id":      2,
			"message": "Transaction item 2",
		})
		if err != nil {
			return err
		}

		err = DB_OPs.Set(tx, fmt.Sprintf("%s:key3", prefix), map[string]interface{}{
			"id":      3,
			"message": "Transaction item 3",
		})
		return err
	})

	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Transaction failed: %v", err)
	}

	fmt.Printf("✅ Transaction completed successfully\n")

	// Verify all keys were created
	keys := []string{
		fmt.Sprintf("%s:key1", prefix),
		fmt.Sprintf("%s:key2", prefix),
		fmt.Sprintf("%s:key3", prefix),
	}

	for _, key := range keys {
		exists, err := DB_OPs.Exists(conn, key)
		if err != nil {
			DB_OPs.PutMainDBConnection(conn)
			t.Fatalf("Failed to check existence of key %s: %v", key, err)
		}
		if !exists {
			t.Fatalf("Transaction key %s should exist but doesn't", key)
		}
		fmt.Printf("   ✅ Verified transaction key: %s\n", key)
	}

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ Transaction test completed successfully!\n")
}

// Test_ErrorHandling tests error handling scenarios
func Test_ErrorHandling(t *testing.T) {
	fmt.Printf("=== Testing Error Handling ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	conn, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Test empty key error
	fmt.Printf("Testing empty key error...\n")
	err = DB_OPs.Create(conn, "", "value")
	if err == nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Expected error for empty key, got nil")
	}
	fmt.Printf("✅ Empty key error handled correctly: %v\n", err)

	// Test nil value error
	fmt.Printf("Testing nil value error...\n")
	err = DB_OPs.Create(conn, "test:key", nil)
	if err == nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Expected error for nil value, got nil")
	}
	fmt.Printf("✅ Nil value error handled correctly: %v\n", err)

	// Test read non-existent key
	fmt.Printf("Testing read non-existent key...\n")
	_, err = DB_OPs.Read(conn, "test:nonexistent")
	if err == nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Expected error for non-existent key, got nil")
	}
	fmt.Printf("✅ Non-existent key error handled correctly: %v\n", err)

	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ Error handling test completed successfully!\n")
}

// Test_ConnectionPoolIntegration tests integration with connection pool
func Test_ConnectionPoolIntegration(t *testing.T) {
	fmt.Printf("=== Testing Connection Pool Integration ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Test with nil connection (should get connection from pool)
	fmt.Printf("Testing with nil connection...\n")
	testKey := fmt.Sprintf("test:pool-integration-%d", time.Now().UTC().UnixNano())
	testValue := map[string]interface{}{
		"message":   "Pool integration test",
		"timestamp": time.Now().UTC().Unix(),
	}

	// This should work because Create will get a connection from the pool
	err = DB_OPs.Create(nil, testKey, testValue)
	if err != nil {
		t.Fatalf("Failed to create with nil connection: %v", err)
	}
	fmt.Printf("✅ Created with nil connection: %s\n", testKey)

	// Verify the data was created
	data, err := DB_OPs.Read(nil, testKey)
	if err != nil {
		t.Fatalf("Failed to read with nil connection: %v", err)
	}

	var retrievedValue map[string]interface{}
	err = json.Unmarshal(data, &retrievedValue)
	if err != nil {
		t.Fatalf("Failed to unmarshal data: %v", err)
	}

	if retrievedValue["message"] != testValue["message"] {
		t.Fatalf("Pool integration integrity check failed: expected %v, got %v", testValue["message"], retrievedValue["message"])
	}

	fmt.Printf("✅ Pool integration test completed successfully!\n")
}

// Test_StressOperations tests stress scenarios
func Test_StressOperations(t *testing.T) {
	fmt.Printf("=== Testing Stress Operations ===\n")

	// Initialize the main database pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Test rapid operations
	fmt.Printf("Testing rapid operations...\n")
	prefix := fmt.Sprintf("test:stress-%d", time.Now().UTC().UnixNano())

	// Create multiple keys rapidly
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("%s:stress%d", prefix, i)
		value := map[string]interface{}{
			"index":     i,
			"timestamp": time.Now().UTC().UnixNano(),
		}

		err = DB_OPs.Create(nil, key, value)
		if err != nil {
			t.Fatalf("Failed to create stress key %s: %v", key, err)
		}
	}
	fmt.Printf("✅ Created 10 stress keys\n")

	// Read all keys rapidly
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("%s:stress%d", prefix, i)
		_, err := DB_OPs.Read(nil, key)
		if err != nil {
			t.Fatalf("Failed to read stress key %s: %v", key, err)
		}
	}
	fmt.Printf("✅ Read 10 stress keys\n")

	// Test batch operations
	fmt.Printf("Testing batch operations...\n")
	batchData := make(map[string]interface{})
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("%s:batch%d", prefix, i)
		value := map[string]interface{}{
			"batch_index": i,
			"timestamp":   time.Now().UTC().UnixNano(),
		}
		batchData[key] = value
	}

	err = DB_OPs.BatchCreate(nil, batchData)
	if err != nil {
		t.Fatalf("Failed to batch create: %v", err)
	}
	fmt.Printf("✅ Batch created 5 keys\n")

	fmt.Printf("✅ Stress operations test completed successfully!\n")
}

// Test_GetMainDBConnectionandPutBack tests that connections are automatically returned when context is cancelled
func Test_GetMainDBConnectionandPutBack(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnectionandPutBack Auto PutBack ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Test multiple connections to verify auto-putback works correctly
	for i := 0; i < 10; i++ {
		fmt.Printf("\n--- Iteration %d ---\n", i+1)

		// Create a context with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)

		// Get connection with auto-putback
		conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			t.Fatalf("Failed to get connection on iteration %d: %v", i+1, err)
		}
		if conn == nil {
			t.Fatalf("Expected non-nil connection on iteration %d, got nil", i+1)
		}
		if conn.Client == nil {
			t.Fatalf("Expected non-nil client on iteration %d, got nil", i+1)
		}

		// Debugging - print session ID
		sessionID := conn.Client.Client.GetSessionID()
		fmt.Printf("✅ Got connection with session ID: %s\n", sessionID)
		fmt.Printf("   Connection pointer: %p\n", conn)
		fmt.Printf("   In Use: %t\n", conn.InUse)

		// Wait for context timeout - this should trigger automatic cleanup
		<-ctx.Done()

		// Wait a bit for the goroutine to process the cancellation
		time.Sleep(300 * time.Millisecond)

		fmt.Printf("✅ Context cancelled, connection should be auto-returned\n")

		// Cancel to clean up
		cancel()

		// Verify we can still get connections from the pool (connection was returned)
		conn2, err := DB_OPs.GetMainDBConnection(context.Background())
		if err != nil {
			t.Fatalf("Failed to get second connection on iteration %d: %v", i+1, err)
		}
		defer DB_OPs.PutMainDBConnection(conn2)

		fmt.Printf("✅ Pool is still working after auto-return (iteration %d)\n", i+1)
	}

	fmt.Printf("\n✅ All 10 iterations completed successfully - auto-putback is working!\n")
}

// Test_GetMainDBConnectionandPutBack_Success tests successful connection retrieval and pool reuse
func Test_GetMainDBConnectionandPutBack_Success(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnectionandPutBack Success ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get first connection
	conn1, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		t.Fatalf("Failed to get first connection: %v", err)
	}
	if conn1 == nil {
		t.Fatalf("Expected non-nil connection, got nil")
	}
	if conn1.Client == nil {
		t.Fatalf("Expected non-nil client, got nil")
	}

	fmt.Printf("✅ First connection retrieved successfully\n")
	fmt.Printf("   Connection pointer: %p\n", conn1)
	fmt.Printf("   Database: %s\n", conn1.Database)
	fmt.Printf("   Created At: %s\n", conn1.CreatedAt.Format(time.RFC3339))
	fmt.Printf("   In Use: %t\n", conn1.InUse)

	// Get session ID for comparison
	sessionID1 := conn1.Client.Client.GetSessionID()
	fmt.Printf("   Session ID: %s\n", sessionID1)

	// Manually return connection to pool
	DB_OPs.PutMainDBConnection(conn1)
	fmt.Printf("✅ First connection returned to pool\n")

	// Wait a bit to ensure connection is back in pool
	time.Sleep(100 * time.Millisecond)

	// Get second connection - should be the same one if pool is working correctly
	conn2, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		t.Fatalf("Failed to get second connection: %v", err)
	}
	if conn2 == nil {
		t.Fatalf("Expected non-nil second connection, got nil")
	}

	fmt.Printf("✅ Second connection retrieved successfully\n")
	fmt.Printf("   Connection pointer: %p\n", conn2)
	fmt.Printf("   Database: %s\n", conn2.Database)
	fmt.Printf("   In Use: %t\n", conn2.InUse)

	// Get session ID for comparison
	sessionID2 := conn2.Client.Client.GetSessionID()
	fmt.Printf("   Session ID: %s\n", sessionID2)

	// Verify if it's the same connection (same pointer)
	if conn1 == conn2 {
		fmt.Printf("✅ Same connection reused from pool (pointer match)\n")
	} else {
		fmt.Printf("⚠️  Different connection pointer (pool may have multiple connections)\n")
	}

	// Verify session ID matches (more reliable check)
	if sessionID1 == sessionID2 {
		fmt.Printf("✅ Same session ID - connection was correctly reused from pool\n")
	} else {
		t.Fatalf("Expected same session ID, got different: %s vs %s", sessionID1, sessionID2)
	}

	// Return second connection
	DB_OPs.PutMainDBConnection(conn2)
	fmt.Printf("✅ Second connection returned to pool\n")
}

// Test_GetMainDBConnectionandPutBack_NilContext tests that the function properly handles nil context
func Test_GetMainDBConnectionandPutBack_NilContext(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnectionandPutBack with Nil Context ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(nil)
	if err == nil {
		t.Fatalf("Expected error for nil context, got nil error")
	}
	fmt.Printf("✅ Error for nil context: %v\n", err)
	if conn != nil {
		t.Fatalf("Expected nil connection, got non-nil connection")
	}
	fmt.Printf("✅ Nil context handled properly\n")
}

// Test_GetMainDBConnectionandPutBack_ContextCancellation tests automatic cleanup on context cancellation
func Test_GetMainDBConnectionandPutBack_ContextCancellation(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnectionandPutBack Context Cancellation ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
	if conn == nil {
		t.Fatalf("Expected non-nil connection, got nil")
	}

	fmt.Printf("✅ Connection retrieved\n")
	fmt.Printf("   Connection pointer: %p\n", conn)
	fmt.Printf("   In Use before cancel: %t\n", conn.InUse)

	// Cancel the context - this should trigger automatic cleanup
	cancel()

	// Wait a bit for the goroutine to process the cancellation
	time.Sleep(500 * time.Millisecond)

	fmt.Printf("✅ Context cancelled, connection should be auto-returned\n")

	// Verify we can still get connections from the pool (connection was returned)
	conn2, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get second connection: %v", err)
	}
	defer DB_OPs.PutMainDBConnection(conn2)

	fmt.Printf("✅ Second connection retrieved (pool is working)\n")
	fmt.Printf("   Second connection pointer: %p\n", conn2)
}

// Test_GetMainDBConnectionandPutBack_ContextTimeout tests automatic cleanup on context timeout
func Test_GetMainDBConnectionandPutBack_ContextTimeout(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnectionandPutBack Context Timeout ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Create a context with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
	if conn == nil {
		t.Fatalf("Expected non-nil connection, got nil")
	}

	fmt.Printf("✅ Connection retrieved\n")
	fmt.Printf("   Connection pointer: %p\n", conn)

	// Wait for context timeout
	<-ctx.Done()

	// Wait a bit for the goroutine to process the timeout
	time.Sleep(500 * time.Millisecond)

	fmt.Printf("✅ Context timed out, connection should be auto-returned\n")

	// Verify we can still get connections from the pool
	conn2, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get second connection: %v", err)
	}
	defer DB_OPs.PutMainDBConnection(conn2)

	fmt.Printf("✅ Second connection retrieved after timeout (pool is working)\n")
}

// Test_GetMainDBConnectionandPutBack_ManualReturn tests that manual return still works
func Test_GetMainDBConnectionandPutBack_ManualReturn(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnectionandPutBack Manual Return ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
	if conn == nil {
		t.Fatalf("Expected non-nil connection, got nil")
	}

	fmt.Printf("✅ Connection retrieved\n")
	fmt.Printf("   Connection pointer: %p\n", conn)

	// Manually return the connection before context is cancelled
	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ Connection manually returned\n")

	// Wait a bit to ensure no issues with double return
	time.Sleep(200 * time.Millisecond)

	// Cancel context - this should not cause issues even though connection is already returned
	cancel()
	time.Sleep(200 * time.Millisecond)

	fmt.Printf("✅ Manual return works correctly, no double-return issues\n")
}

// Test_GetMainDBConnectionandPutBack_MultipleConnections tests multiple connections with context
func Test_GetMainDBConnectionandPutBack_MultipleConnections(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnectionandPutBack Multiple Connections ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	// Get multiple connections with different contexts
	ctx1, cancel1 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel1()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()

	conn1, err := DB_OPs.GetMainDBConnectionandPutBack(ctx1)
	if err != nil {
		t.Fatalf("Failed to get first connection: %v", err)
	}

	conn2, err := DB_OPs.GetMainDBConnectionandPutBack(ctx2)
	if err != nil {
		t.Fatalf("Failed to get second connection: %v", err)
	}

	// Verify they are different connections
	if conn1 == conn2 {
		t.Fatalf("Expected different connections, got same connection")
	}

	fmt.Printf("✅ Got two different connections\n")
	fmt.Printf("   Connection 1 pointer: %p\n", conn1)
	fmt.Printf("   Connection 2 pointer: %p\n", conn2)

	// Cancel first context
	cancel1()
	time.Sleep(200 * time.Millisecond)
	fmt.Printf("✅ First context cancelled\n")

	// Cancel second context
	cancel2()
	time.Sleep(200 * time.Millisecond)
	fmt.Printf("✅ Second context cancelled\n")

	// Verify pool is still working
	conn3, err := DB_OPs.GetMainDBConnection(context.Background())
	if err != nil {
		t.Fatalf("Failed to get third connection: %v", err)
	}
	defer DB_OPs.PutMainDBConnection(conn3)

	fmt.Printf("✅ Pool is still working after multiple auto-returns\n")
}

// Test_GetMainDBConnectionandPutBack_ConcurrentAccess tests concurrent access with context
func Test_GetMainDBConnectionandPutBack_ConcurrentAccess(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnectionandPutBack Concurrent Access ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	const numGoroutines = 5
	done := make(chan bool, numGoroutines)

	// Launch multiple goroutines that get connections with context
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
			if err != nil {
				t.Errorf("Goroutine %d: Failed to get connection: %v", id, err)
				done <- false
				return
			}

			if conn == nil {
				t.Errorf("Goroutine %d: Got nil connection", id)
				done <- false
				return
			}

			fmt.Printf("   Goroutine %d: Got connection %p\n", id, conn)

			// Simulate some work
			time.Sleep(100 * time.Millisecond)

			// Context will timeout and auto-return connection
			<-ctx.Done()
			time.Sleep(100 * time.Millisecond)

			fmt.Printf("   Goroutine %d: Connection auto-returned\n", id)
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	successCount := 0
	for i := 0; i < numGoroutines; i++ {
		if <-done {
			successCount++
		}
	}

	if successCount != numGoroutines {
		t.Fatalf("Expected %d successful goroutines, got %d", numGoroutines, successCount)
	}

	fmt.Printf("✅ All %d goroutines completed successfully\n", numGoroutines)
}

// Test_GetMainDBConnection_Basic tests basic connection retrieval
func Test_GetMainDBConnection_Basic(t *testing.T) {
	fmt.Printf("=== Testing GetMainDBConnection Basic ===\n")

	// Initialize the main DB pool
	err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
	if err != nil {
		t.Fatalf("Failed to initialize main DB pool: %v", err)
	}

	ctx := context.Background()
	conn, err := DB_OPs.GetMainDBConnection(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
	if conn == nil {
		t.Fatalf("Expected non-nil connection, got nil")
	}
	if conn.Client == nil {
		t.Fatalf("Expected non-nil client, got nil")
	}

	fmt.Printf("✅ Connection retrieved successfully\n")
	fmt.Printf("   Database: %s\n", conn.Database)
	fmt.Printf("   Created At: %s\n", conn.CreatedAt.Format(time.RFC3339))
	fmt.Printf("   In Use: %t\n", conn.InUse)

	// Manually return connection
	DB_OPs.PutMainDBConnection(conn)
	fmt.Printf("✅ Connection returned to pool\n")
}
