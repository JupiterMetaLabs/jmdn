package DB_OPs

import (
    "os"
    "path/filepath"
    "testing"
	"fmt"
)

const (
    testDBDir  = "DB"
    testDBName = "test_database"
)

// Setup creates a test database and returns a cleanup function
func setupTestDB(t *testing.T) (*DB, func()) {
    // Create a fresh database for testing
    db := NewDB()
    err := db.InitDB(testDBDir, testDBName)
    if err != nil {
        t.Fatalf("Failed to initialize test database: %v", err)
    }

    // Return a cleanup function to be deferred
    cleanup := func() {
        db.Close()
        // Remove the test database directory
        os.RemoveAll(filepath.Join(testDBDir, "DB", testDBName))
    }

    return db, cleanup
}

func TestNewDB(t *testing.T) {
    db := NewDB()
    if db == nil {
        t.Error("NewDB() returned nil")
    }
}

func TestInitDB(t *testing.T) {
    db, cleanup := setupTestDB(t)
    defer cleanup()

    if db.instance == nil {
        t.Error("Database instance is nil after initialization")
    }

    if db.path == "" {
        t.Error("Database path is empty after initialization")
    }
}

func TestConnectDB(t *testing.T) {
    // First create a database
    db := NewDB()
    err := db.InitDB(testDBDir, "connect_test")
    if err != nil {
        t.Fatalf("Failed to initialize test database: %v", err)
    }
    db.Close()

    // Now try to connect to it
    db2 := NewDB()
    err = db2.ConnectDB(testDBDir, "connect_test")
    if err != nil {
        t.Errorf("Failed to connect to existing database: %v", err)
    }
    defer func() {
        db2.Close()
        os.RemoveAll(filepath.Join(testDBDir, "DB", "connect_test"))
    }()

    if db2.instance == nil {
        t.Error("Database instance is nil after connecting")
    }
}

func TestAddAndReadData(t *testing.T) {
    db, cleanup := setupTestDB(t)
    defer cleanup()

    // Test adding data
    testKey := "test_key"
    testValue := "test_value"
    err := db.AddData(testKey, testValue)
    if err != nil {
        t.Errorf("Failed to add data: %v", err)
    }

    // Test reading the data back
    value, err := db.ReadData(testKey)
	fmt.Println(value)
    if err != nil {
        t.Errorf("Failed to read data: %v", err)
    }

    if value != testValue {
        t.Errorf("Read data mismatch. Expected: %s, Got: %s", testValue, value)
    }

    // Test reading non-existent key
    _, err = db.ReadData("nonexistent_key")
    if err == nil {
        t.Error("Reading non-existent key did not return error")
    }
}

func TestDeleteData(t *testing.T) {
    db, cleanup := setupTestDB(t)
    defer cleanup()

    // Add some test data
    testKey1 := "test_key1"
    testValue1 := "test_value1"
    testKey2 := "test_key2"
    testValue2 := "test_value2"

    err := db.AddData(testKey1, testValue1)
    if err != nil {
        t.Fatalf("Failed to add data: %v", err)
    }

    err = db.AddData(testKey2, testValue1) // Same value, different key
    if err != nil {
        t.Fatalf("Failed to add data: %v", err)
    }

    err = db.AddData("another_key", testValue2)
    if err != nil {
        t.Fatalf("Failed to add data: %v", err)
    }

    // Test deleting by key
    err = db.DeleteData(testKey1, "key")
    if err != nil {
        t.Errorf("Failed to delete by key: %v", err)
    }

    // Verify key is gone
    _, err = db.ReadData(testKey1)
    if err == nil {
        t.Error("Key still exists after deletion")
    }

    // Test deleting by value
    err = db.DeleteData(testValue1, "value")
    if err != nil {
        t.Errorf("Failed to delete by value: %v", err)
    }

    // Verify all entries with the value are gone
    _, err = db.ReadData(testKey2)
    if err == nil {
        t.Error("Entry still exists after deletion by value")
    }

    // Test invalid flag
    err = db.DeleteData("something", "invalid_flag")
    if err == nil {
        t.Error("Delete with invalid flag did not return error")
    }
}

func TestReadAll(t *testing.T) {
    db, cleanup := setupTestDB(t)
    defer cleanup()

    // Add multiple entries
    testData := map[string]string{
        "key1": "value1",
        "key2": "value2",
        "key3": "value3",
    }

    for k, v := range testData {
        if err := db.AddData(k, v); err != nil {
            t.Fatalf("Failed to add data: %v", err)
        }
    }

    // Read all entries
    allData, err := db.ReadAll()
    if err != nil {
        t.Errorf("Failed to read all data: %v", err)
    }
	fmt.Println(allData)
    // Verify all entries are present
    if len(allData) != len(testData) {
        t.Errorf("ReadAll returned %d entries, expected %d", len(allData), len(testData))
    }

    for k, v := range testData {
        readVal, exists := allData[k]
        if !exists {
            t.Errorf("Key %s missing from ReadAll results", k)
        }
        if readVal != v {
            t.Errorf("Value mismatch for key %s. Expected: %s, Got: %s", k, v, readVal)
        }
    }
}

func TestComputeMerkleRoot(t *testing.T) {
    db, cleanup := setupTestDB(t)
    defer cleanup()

    // Test on empty database
    _, err := db.ComputeMerkleRoot()
    if err == nil {
        t.Error("ComputeMerkleRoot on empty database did not return error")
    }

    // Add some entries
    data := map[string]string{
        "key1": "value1",
        "key2": "value2",
    }

    for k, v := range data {
        if err := db.AddData(k, v); err != nil {
            t.Fatalf("Failed to add data: %v", err)
        }
    }

    // Compute Merkle root
    root1, err := db.ComputeMerkleRoot()
    if err != nil {
        t.Errorf("Failed to compute Merkle root: %v", err)
    }

	fmt.Println(root1)

    if root1 == "" {
        t.Error("Computed Merkle root is empty")
    }

    // Add more data and verify root changes
    err = db.AddData("key3", "value3")
    if err != nil {
        t.Fatalf("Failed to add data: %v", err)
    }

    root2, err := db.ComputeMerkleRoot()
    if err != nil {
        t.Errorf("Failed to compute second Merkle root: %v", err)
    }

    if root1 == root2 {
        t.Error("Merkle root did not change after adding data")
    }
}


func TestDBStats(t *testing.T) {
    db, cleanup := setupTestDB(t)
    defer cleanup()

    // Add some data to make the stats more meaningful
    for i := 0; i < 100; i++ {
        key := fmt.Sprintf("key%d", i)
        value := fmt.Sprintf("This is test value %d", i)
        if err := db.AddData(key, value); err != nil {
            t.Fatalf("Failed to add data: %v", err)
        }
    }

    // Get stats
    stats := db.GetStats()

    // Check that required stats are present
    requiredStats := []string{"lsm_size", "vlog_size", "total_size", "db_path"}
    for _, stat := range requiredStats {
        if _, exists := stats[stat]; !exists {
            t.Errorf("Required stat %s missing from GetStats result", stat)
        }
    }

    // Path should match what we expect
    expectedPath := filepath.Join(testDBDir, "DB", testDBName)
    if stats["db_path"] != expectedPath {
        t.Errorf("DB path mismatch. Expected: %s, Got: %s", expectedPath, stats["db_path"])
    }
}

func TestDBErrors(t *testing.T) {
    // Test operations on uninitialized DB
    db := NewDB()

    // These operations should all fail with "database not initialized"
    if err := db.AddData("key", "value"); err == nil {
        t.Error("AddData on uninitialized DB did not return error")
    }

    if _, err := db.ReadData("key"); err == nil {
        t.Error("ReadData on uninitialized DB did not return error")
    }

    if _, err := db.ComputeMerkleRoot(); err == nil {
        t.Error("ComputeMerkleRoot on uninitialized DB did not return error")
    }

    if err := db.DeleteData("key", "key"); err == nil {
        t.Error("DeleteData on uninitialized DB did not return error")
    }

    if _, err := db.ReadAll(); err == nil {
        t.Error("ReadAll on uninitialized DB did not return error")
    }

    if err := db.RunGC(); err == nil {
        t.Error("RunGC on uninitialized DB did not return error")
    }

    // Verify stats returns an error indicator
    stats := db.GetStats()
    if _, exists := stats["error"]; !exists {
        t.Error("GetStats on uninitialized DB did not return error indicator")
    }
}

func TestConcurrentAccess(t *testing.T) {
    db, cleanup := setupTestDB(t)
    defer cleanup()

    // Number of concurrent operations
    const concurrentOps = 50

    // Add some initial data
    for i := 0; i < 10; i++ {
        key := fmt.Sprintf("init_key%d", i)
        value := fmt.Sprintf("init_value%d", i)
        if err := db.AddData(key, value); err != nil {
            t.Fatalf("Failed to add initial data: %v", err)
        }
    }

    // Use a channel to track errors
    errChan := make(chan error, concurrentOps*4) // *4 because we have 4 types of operations

    // Start concurrent read operations
    for i := 0; i < concurrentOps; i++ {
        go func(idx int) {
            key := fmt.Sprintf("init_key%d", idx%10)
            _, err := db.ReadData(key)
            errChan <- err
        }(i)
    }

    // Start concurrent write operations
    for i := 0; i < concurrentOps; i++ {
        go func(idx int) {
            key := fmt.Sprintf("new_key%d", idx)
            value := fmt.Sprintf("new_value%d", idx)
            err := db.AddData(key, value)
            errChan <- err
        }(i)
    }

    // Start concurrent merkle root computations
    for i := 0; i < concurrentOps; i++ {
        go func() {
            _, err := db.ComputeMerkleRoot()
            errChan <- err
        }()
    }

    // Start concurrent read all operations
    for i := 0; i < concurrentOps; i++ {
        go func() {
            _, err := db.ReadAll()
            errChan <- err
        }()
    }

    // Check for errors
    errorCount := 0
    expectedResponses := concurrentOps * 4
    for i := 0; i < expectedResponses; i++ {
        if err := <-errChan; err != nil {
            errorCount++
        }
    }

    // Some errors are expected due to concurrent operations, but not too many
    if errorCount > expectedResponses/2 {
        t.Errorf("Too many errors during concurrent access: %d out of %d operations failed", 
            errorCount, expectedResponses)
    }

    // Final verification - the database should still be usable
    _, err := db.ReadAll()
    if err != nil {
        t.Errorf("Database is in bad state after concurrent operations: %v", err)
    }
}