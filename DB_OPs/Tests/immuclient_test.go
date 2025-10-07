package DB_OPs_Tests

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
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
	testKey := fmt.Sprintf("test:create-read-update-%d", time.Now().UnixNano())
	testValue := map[string]interface{}{
		"message":   "Hello, ImmuDB!",
		"timestamp": time.Now().Unix(),
		"test":      true,
	}

	// Test Create
	fmt.Printf("Testing Create operation...\n")
	conn, err := DB_OPs.GetMainDBConnection()
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
		"timestamp": time.Now().Unix(),
		"test":      true,
		"updated":   true,
	}

	err = DB_OPs.Update(conn, testKey, updatedValue)
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
	testKey := fmt.Sprintf("test:readjson-%d", time.Now().UnixNano())
	testValue := map[string]interface{}{
		"name":   "Test User",
		"age":    30,
		"active": true,
		"tags":   []string{"test", "json", "immudb"},
	}

	// Create test data
	conn, err := DB_OPs.GetMainDBConnection()
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
	err = DB_OPs.ReadJSON(conn, testKey, &retrievedValue)
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
	prefix := fmt.Sprintf("test:getkeys-%d", time.Now().UnixNano())
	conn, err := DB_OPs.GetMainDBConnection()
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
			"timestamp": time.Now().Unix(),
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
	prefix := fmt.Sprintf("test:getallkeys-%d", time.Now().UnixNano())
	conn, err := DB_OPs.GetMainDBConnection()
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
			"timestamp": time.Now().Unix(),
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

	// Create test data with prefix
	prefix := fmt.Sprintf("test:countkeys-%d", time.Now().UnixNano())
	conn, err := DB_OPs.GetMainDBConnection()
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Create multiple test keys
	testKeys := []string{
		fmt.Sprintf("%s:count1", prefix),
		fmt.Sprintf("%s:count2", prefix),
		fmt.Sprintf("%s:count3", prefix),
	}

	for i, key := range testKeys {
		value := map[string]interface{}{
			"count":     i + 1,
			"key":       key,
			"timestamp": time.Now().Unix(),
		}
		err = DB_OPs.Create(conn, key, value)
		if err != nil {
			DB_OPs.PutMainDBConnection(conn)
			t.Fatalf("Failed to create test key %s: %v", key, err)
		}
		fmt.Printf("✅ Created test key: %s\n", key)
	}

	// Test CountAllKeys
	fmt.Printf("Testing CountAllKeys operation...\n")
	count, err := DB_OPs.CountAllKeys(conn, prefix)
	if err != nil {
		DB_OPs.PutMainDBConnection(conn)
		t.Fatalf("Failed to count keys: %v", err)
	}

	fmt.Printf("✅ Found %d keys with prefix\n", count)

	// Verify we got at least our test keys
	if count < len(testKeys) {
		t.Fatalf("Expected at least %d keys, got %d", len(testKeys), count)
	}

	DB_OPs.PutMainDBConnection(conn)
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
	prefix := fmt.Sprintf("test:batch-%d", time.Now().UnixNano())
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
	conn, err := DB_OPs.GetMainDBConnection()
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
	testKey := fmt.Sprintf("test:safe-%d", time.Now().UnixNano())
	testValue := map[string]interface{}{
		"message":   "Safe operation test",
		"timestamp": time.Now().Unix(),
		"verified":  true,
	}

	// Test SafeCreate
	fmt.Printf("Testing SafeCreate operation...\n")
	conn, err := DB_OPs.GetMainDBConnection()
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
	existingKey := fmt.Sprintf("test:exists-%d", time.Now().UnixNano())
	nonExistingKey := fmt.Sprintf("test:nonexistent-%d", time.Now().UnixNano())

	conn, err := DB_OPs.GetMainDBConnection()
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Create a key
	testValue := map[string]interface{}{
		"message":   "Exists test",
		"timestamp": time.Now().Unix(),
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

	conn, err := DB_OPs.GetMainDBConnection()
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

	conn, err := DB_OPs.GetMainDBConnection()
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

	conn, err := DB_OPs.GetMainDBConnection()
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

	conn, err := DB_OPs.GetMainDBConnection()
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

	conn, err := DB_OPs.GetMainDBConnection()
	if err != nil {
		t.Fatalf("Failed to get main DB connection: %v", err)
	}

	// Test Transaction
	fmt.Printf("Testing Transaction operation...\n")
	prefix := fmt.Sprintf("test:transaction-%d", time.Now().UnixNano())

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

	conn, err := DB_OPs.GetMainDBConnection()
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
	testKey := fmt.Sprintf("test:pool-integration-%d", time.Now().UnixNano())
	testValue := map[string]interface{}{
		"message":   "Pool integration test",
		"timestamp": time.Now().Unix(),
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
	prefix := fmt.Sprintf("test:stress-%d", time.Now().UnixNano())

	// Create multiple keys rapidly
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("%s:stress%d", prefix, i)
		value := map[string]interface{}{
			"index":     i,
			"timestamp": time.Now().UnixNano(),
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
			"timestamp":   time.Now().UnixNano(),
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
