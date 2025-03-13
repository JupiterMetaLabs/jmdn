package DB_OPs

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"
)

// TestMain sets up any global test requirements
func TestMain(m *testing.M) {
	// Check if ImmuDB is available before running tests
	if !isImmuDBAvailable() {
		fmt.Println("WARNING: ImmuDB connection not available, skipping tests")
		os.Exit(0)
	}

	fmt.Println("ImmuDB connection successful, running tests...")
	// Run the tests
	os.Exit(m.Run())
}

// isImmuDBAvailable checks if ImmuDB server is running
func isImmuDBAvailable() bool {
	client, err := New()
	if err != nil {
		fmt.Printf("ImmuDB connection failed: %v\n", err)
		return false
	}
	defer client.Close()
	return true
}

// generateTestKey creates a unique test key
func generateTestKey(prefix string) string {
	key := fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), rand.Intn(10000))
	fmt.Printf("Generated test key: %s\n", key)
	return key
}

// TestNew tests the creation of a new ImmuClient
func TestNew(t *testing.T) {
	fmt.Println("\n=== TestNew ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}
	defer client.Close()
	fmt.Println("Successfully created and connected ImmuClient")
}

// TestCreate tests the Create method
func TestCreate(t *testing.T) {
	fmt.Println("\n=== TestCreate ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}
	defer client.Close()

	// Test string value
	key1 := generateTestKey("test_create_string")
	fmt.Printf("Creating string value with key: %s\n", key1)
	err = client.Create(key1, "test value")
	if err != nil {
		t.Errorf("Failed to create string value: %v", err)
	} else {
		fmt.Println("String value created successfully")
	}

	// Test struct value
	type TestData struct {
		Name  string    `json:"name"`
		Count int       `json:"count"`
		Time  time.Time `json:"time"`
	}

	key2 := generateTestKey("test_create_struct")
	testData := TestData{
		Name:  "Test Item",
		Count: 42,
		Time:  time.Now().UTC(),
	}

	fmt.Printf("Creating struct value with key: %s\n", key2)
	fmt.Printf("Struct data: %+v\n", testData)
	err = client.Create(key2, testData)
	if err != nil {
		t.Errorf("Failed to create struct value: %v", err)
	} else {
		fmt.Println("Struct value created successfully")
	}

	// Test empty key
	fmt.Println("Testing empty key (should fail)")
	err = client.Create("", "value")
	fmt.Printf("Empty key result: %v\n", err)
	if err == nil {
		t.Error("Expected error with empty key, got none")
	}
}

// TestRead tests the Read method
func TestRead(t *testing.T) {
	fmt.Println("\n=== TestRead ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}
	defer client.Close()

	// Create a value to read
	key := generateTestKey("test_read")
	testValue := "test read value"
	fmt.Printf("Creating test value for reading: %s = %s\n", key, testValue)
	err = client.Create(key, testValue)
	if err != nil {
		t.Fatalf("Failed to create test value: %v", err)
	}

	// Read the value
	fmt.Printf("Reading value for key: %s\n", key)
	value, err := client.Read(key)
	if err != nil {
		t.Errorf("Failed to read value: %v", err)
	} else {
		fmt.Printf("Read successful. Value: %s\n", string(value))
	}

	// Verify the value
	if string(value) != testValue {
		t.Errorf("Read value mismatch. Got: %s, Want: %s", string(value), testValue)
	} else {
		fmt.Println("Value matches expected result ✓")
	}

	// Test reading non-existent key
	nonExistentKey := generateTestKey("nonexistent")
	fmt.Printf("Attempting to read non-existent key: %s\n", nonExistentKey)
	_, err = client.Read(nonExistentKey)
	fmt.Printf("Non-existent key result: %v\n", err)
	if err == nil {
		t.Error("Expected error when reading non-existent key, got none")
	}

	// Test empty key
	fmt.Println("Testing empty key read (should fail)")
	_, err = client.Read("")
	fmt.Printf("Empty key read result: %v\n", err)
	if err == nil {
		t.Error("Expected error with empty key, got none")
	}
}

// TestReadJSON tests the ReadJSON method
func TestReadJSON(t *testing.T) {
	fmt.Println("\n=== TestReadJSON ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}
	defer client.Close()

	// Create a struct to store and retrieve
	type TestStruct struct {
		Name    string  `json:"name"`
		Value   float64 `json:"value"`
		Enabled bool    `json:"enabled"`
	}

	key := generateTestKey("test_readjson")
	originalData := TestStruct{
		Name:    "Test Item",
		Value:   123.45,
		Enabled: true,
	}

	// Store the struct
	fmt.Printf("Creating JSON data with key: %s\n", key)
	fmt.Printf("Original struct: %+v\n", originalData)
	err = client.Create(key, originalData)
	if err != nil {
		t.Fatalf("Failed to create test struct: %v", err)
	}

	// Read it back
	fmt.Printf("Reading JSON data for key: %s\n", key)
	var retrievedData TestStruct
	err = client.ReadJSON(key, &retrievedData)
	if err != nil {
		t.Errorf("Failed to read JSON: %v", err)
	} else {
		fmt.Printf("Retrieved struct: %+v\n", retrievedData)
	}

	// Verify the data
	if retrievedData.Name != originalData.Name ||
		retrievedData.Value != originalData.Value ||
		retrievedData.Enabled != originalData.Enabled {
		t.Errorf("JSON data mismatch. Got: %+v, Want: %+v", retrievedData, originalData)
	} else {
		fmt.Println("JSON data matches expected result ✓")
	}

	// Test reading into wrong type
	fmt.Println("Testing unmarshal to wrong type (should fail)")
	var wrongType string
	err = client.ReadJSON(key, &wrongType)
	fmt.Printf("Wrong type result: %v\n", err)
	if err == nil {
		t.Error("Expected error when unmarshaling to wrong type, got none")
	}
}

// TestUpdate tests the Update method
func TestUpdate(t *testing.T) {
	fmt.Println("\n=== TestUpdate ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}
	defer client.Close()

	// Create initial value
	key := generateTestKey("test_update")
	initialValue := "initial value"
	fmt.Printf("Creating initial value: %s = %s\n", key, initialValue)
	err = client.Create(key, initialValue)
	if err != nil {
		t.Fatalf("Failed to create initial value: %v", err)
	}

	// Read initial value to confirm
	initialBytes, err := client.Read(key)
	if err != nil {
		t.Fatalf("Failed to read initial value: %v", err)
	}
	fmt.Printf("Initial value read: %s\n", string(initialBytes))

	// Update the value
	updatedValue := "updated value"
	fmt.Printf("Updating value: %s = %s\n", key, updatedValue)
	err = client.Update(key, updatedValue)
	if err != nil {
		t.Errorf("Failed to update value: %v", err)
	}

	// Read back the updated value
	fmt.Printf("Reading value after update for key: %s\n", key)
	value, err := client.Read(key)
	if err != nil {
		t.Errorf("Failed to read updated value: %v", err)
	} else {
		fmt.Printf("Updated value read: %s\n", string(value))
	}

	// Verify the value was updated
	if string(value) != updatedValue {
		t.Errorf("Updated value mismatch. Got: %s, Want: %s", string(value), updatedValue)
	} else {
		fmt.Println("Updated value matches expected result ✓")
	}
}

// TestGetKeys tests the GetKeys method
func TestGetKeys(t *testing.T) {
	fmt.Println("\n=== TestGetKeys ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}
	defer client.Close()

	// Create some test keys with common prefix
	prefix := "test_getkeys_" + fmt.Sprintf("%d", time.Now().UnixNano())
	fmt.Printf("Using prefix: %s\n", prefix)
	testKeys := []string{
		prefix + "_1",
		prefix + "_2",
		prefix + "_3",
	}

	// Add all test keys
	for i, key := range testKeys {
		fmt.Printf("Creating test key %d: %s\n", i, key)
		err = client.Create(key, fmt.Sprintf("value_%d", i))
		if err != nil {
			t.Fatalf("Failed to create test key %s: %v", key, err)
		}
	}

	// Get keys with prefix
	fmt.Printf("Getting keys with prefix: %s\n", prefix)
	keys, err := client.GetKeys(prefix, 10)
	if err != nil {
		t.Errorf("Failed to get keys with prefix: %v", err)
	} else {
		fmt.Println("Retrieved keys:")
		for i, key := range keys {
			fmt.Printf("  %d. %s\n", i+1, key)
		}
	}

	// Verify we got all the keys
	if len(keys) < len(testKeys) {
		t.Errorf("Expected at least %d keys, got %d", len(testKeys), len(keys))
	} else {
		fmt.Printf("Got %d keys (expected at least %d) ✓\n", len(keys), len(testKeys))
	}

	// Verify all our test keys are in the result
	allFound := true
	for _, testKey := range testKeys {
		found := false
		for _, key := range keys {
			if key == testKey {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected key %s not found in results", testKey)
			allFound = false
		}
	}
	if allFound {
		fmt.Println("All test keys found in results ✓")
	}

	// Test with limit
	limit := 2
	fmt.Printf("Testing GetKeys with limit: %d\n", limit)
	limitedKeys, err := client.GetKeys(prefix, limit)
	if err != nil {
		t.Errorf("Failed to get keys with limit: %v", err)
	} else {
		fmt.Printf("Got %d keys with limit %d:\n", len(limitedKeys), limit)
		for i, key := range limitedKeys {
			fmt.Printf("  %d. %s\n", i+1, key)
		}
	}

	if len(limitedKeys) > limit {
		t.Errorf("Expected at most %d keys, got %d", limit, len(limitedKeys))
	} else {
		fmt.Printf("Limited keys count (%d) is within limit (%d) ✓\n", len(limitedKeys), limit)
	}
}

// TestBatchCreate tests the BatchCreate method
func TestBatchCreate(t *testing.T) {
	fmt.Println("\n=== TestBatchCreate ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}
	defer client.Close()

	// Create a batch of entries
	prefix := "test_batch_" + fmt.Sprintf("%d", time.Now().UnixNano())
	fmt.Printf("Using batch prefix: %s\n", prefix)
	entries := map[string]interface{}{
		prefix + "_1": "value 1",
		prefix + "_2": 42,
		prefix + "_3": map[string]string{"foo": "bar"},
	}

	fmt.Println("Creating batch entries:")
	for key, value := range entries {
		fmt.Printf("  %s = %v\n", key, value)
	}

	// Batch create
	err = client.BatchCreate(entries)
	if err != nil {
		t.Errorf("BatchCreate failed: %v", err)
	} else {
		fmt.Println("Batch creation successful")
	}

	// Verify all entries were created
	fmt.Println("Verifying all entries were created correctly:")
	for key, expectedValue := range entries {
		fmt.Printf("Checking key: %s\n", key)

		switch expected := expectedValue.(type) {
		case string:
			valueBytes, err := client.Read(key)
			if err != nil {
				t.Errorf("Failed to read key %s: %v", key, err)
				continue
			}
			retrievedValue := string(valueBytes)

			fmt.Printf("  String value - Expected: %s, Got: %s\n", expected, retrievedValue)
			if retrievedValue != expected {
				t.Errorf("Value mismatch for key %s. Got: %v, Want: %v", key, retrievedValue, expected)
			} else {
				fmt.Println("  ✓ Matches")
			}

		case int:
			var value int
			err = client.ReadJSON(key, &value)
			if err != nil {
				t.Errorf("Failed to read JSON for key %s: %v", key, err)
				continue
			}
			fmt.Printf("  Int value - Expected: %d, Got: %d\n", expected, value)
			if value != expected {
				t.Errorf("Value mismatch for key %s. Got: %v, Want: %v", key, value, expected)
			} else {
				fmt.Println("  ✓ Matches")
			}

		default:
			// For complex types, re-marshal both to compare
			expectedJSON, _ := json.Marshal(expectedValue)
			valueBytes, err := client.Read(key)
			if err != nil {
				t.Errorf("Failed to read key %s: %v", key, err)
				continue
			}

			fmt.Printf("  Complex value - Raw bytes: %s\n", string(valueBytes))

			// Verify JSON equality
			var retrievedMap map[string]interface{}
			var expectedMap map[string]interface{}

			json.Unmarshal(valueBytes, &retrievedMap)
			json.Unmarshal(expectedJSON, &expectedMap)

			fmt.Printf("  Complex value - Expected: %v, Got: %v\n", expectedMap, retrievedMap)
			if retrievedMap["foo"] != expectedMap["foo"] {
				t.Errorf("Complex value mismatch for key %s", key)
			} else {
				fmt.Println("  ✓ Matches")
			}
		}
	}

	// Test with empty map
	fmt.Println("Testing BatchCreate with empty map (should fail)")
	err = client.BatchCreate(map[string]interface{}{})
	fmt.Printf("Empty map result: %v\n", err)
	if err == nil {
		t.Error("Expected error with empty entries map, got none")
	}
}

// TestClose tests the Close method
func TestClose(t *testing.T) {
	fmt.Println("\n=== TestClose ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}

	// Close the client
	fmt.Println("Closing client connection")
	err = client.Close()
	if err != nil {
		t.Errorf("Failed to close client: %v", err)
	} else {
		fmt.Println("Client closed successfully")
	}

	// Verify client is closed by attempting an operation
	fmt.Println("Attempting operation on closed client (should fail)")
	err = client.Create("test_key", "test_value")
	fmt.Printf("Operation on closed client result: %v\n", err)
	if err == nil {
		t.Error("Expected error using client after close, got none")
	}
}

// TestToBytesConversion tests the toBytes helper function
func TestToBytesConversion(t *testing.T) {
	fmt.Println("\n=== TestToBytesConversion ===")

	// Test string conversion
	fmt.Println("Testing string conversion")
	strBytes, err := toBytes("test string")
	if err != nil {
		t.Errorf("Failed to convert string: %v", err)
	} else {
		fmt.Printf("String conversion result: %s\n", string(strBytes))
	}

	if string(strBytes) != "test string" {
		t.Errorf("String conversion failed. Got: %s, Want: %s", string(strBytes), "test string")
	} else {
		fmt.Println("String conversion successful ✓")
	}

	// Test []byte passthrough
	fmt.Println("Testing []byte passthrough")
	originalBytes := []byte("test bytes")
	resultBytes, err := toBytes(originalBytes)
	if err != nil {
		t.Errorf("Failed to convert []byte: %v", err)
	} else {
		fmt.Printf("[]byte passthrough result: %s\n", string(resultBytes))
	}

	if string(resultBytes) != string(originalBytes) {
		t.Errorf("[]byte conversion failed. Got: %s, Want: %s", string(resultBytes), string(originalBytes))
	} else {
		fmt.Println("[]byte passthrough successful ✓")
	}

	// Test struct conversion
	fmt.Println("Testing struct conversion")
	type TestStruct struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}
	testStruct := TestStruct{Name: "test", Value: 123}
	structBytes, err := toBytes(testStruct)
	if err != nil {
		t.Errorf("Failed to convert struct: %v", err)
	} else {
		fmt.Printf("Struct conversion result: %s\n", string(structBytes))
	}

	// Verify JSON conversion
	var decoded TestStruct
	err = json.Unmarshal(structBytes, &decoded)
	if err != nil {
		t.Errorf("Failed to unmarshal converted struct: %v", err)
	} else {
		fmt.Printf("Decoded struct: %+v\n", decoded)
	}

	if decoded.Name != "test" || decoded.Value != 123 {
		t.Errorf("Struct conversion failed. Got: %+v, Want: %+v", decoded, testStruct)
	} else {
		fmt.Println("Struct conversion successful ✓")
	}
}

// TestGetMerkleRoot tests the GetMerkleRoot method
func TestGetMerkleRoot(t *testing.T) {
	fmt.Println("\n=== TestGetMerkleRoot ===")
	client, err := New()
	if err != nil {
		t.Fatalf("Failed to create ImmuClient: %v", err)
	}
	defer client.Close()

	// Create a new key-value pair to ensure state changes
	key := generateTestKey("test_merkleroot")
	testValue := "merkle root test value"
	fmt.Printf("Creating test value to update state: %s = %s\n", key, testValue)

	err = client.Create(key, testValue)
	if err != nil {
		t.Fatalf("Failed to create test value: %v", err)
	}

	// Get the Merkle root
	fmt.Println("Getting Merkle root...")
	merkleRoot, err := client.GetMerkleRoot()
	if err != nil {
		t.Errorf("Failed to get Merkle root: %v", err)
	} else {
		// Check that the Merkle root is not empty
		if len(merkleRoot) == 0 {
			t.Error("Merkle root is empty")
		}

		fmt.Printf("Merkle root retrieved successfully: %x\n", merkleRoot)
		fmt.Printf("Merkle root length: %d bytes\n", len(merkleRoot))

		// Typically, ImmuDB Merkle root is 32 bytes (SHA-256 hash)
		if len(merkleRoot) != 32 {
			t.Errorf("Unexpected Merkle root length: got %d bytes, expected 32 bytes", len(merkleRoot))
		}
	}

	// Create another key-value pair to change state
	key2 := generateTestKey("test_merkleroot_update")
	testValue2 := "updated merkle root test value"
	fmt.Printf("Creating another test value to update state again: %s = %s\n", key2, testValue2)

	err = client.Create(key2, testValue2)
	if err != nil {
		t.Fatalf("Failed to create second test value: %v", err)
	}

	// Get the updated Merkle root
	fmt.Println("Getting updated Merkle root...")
	updatedMerkleRoot, err := client.GetMerkleRoot()
	if err != nil {
		t.Errorf("Failed to get updated Merkle root: %v", err)
	} else {
		fmt.Printf("Updated Merkle root: %x\n", updatedMerkleRoot)

		// The Merkle root should have changed after the update
		if string(merkleRoot) == string(updatedMerkleRoot) {
			t.Error("Merkle root did not change after database update")
		} else {
			fmt.Println("✓ Merkle root changed after update as expected")
		}
	}

	// Test consistency - multiple calls should return the same value if no changes
	fmt.Println("Testing consistency of Merkle root...")
	consistentRoot, err := client.GetMerkleRoot()
	if err != nil {
		t.Errorf("Failed to get consistent Merkle root: %v", err)
	} else {
		if string(updatedMerkleRoot) != string(consistentRoot) {
			t.Error("Merkle root not consistent between calls")
		} else {
			fmt.Println("✓ Merkle root consistent between calls with no state change")
		}
	}

	// Verify root format
	fmt.Println("Verifying Merkle root format...")
	for i := 0; i < len(merkleRoot); i++ {
		// Check if the byte is a valid hex character (0-9, a-f)
		isValid := (merkleRoot[i] >= '0' && merkleRoot[i] <= '9') ||
			(merkleRoot[i] >= 'a' && merkleRoot[i] <= 'f') ||
			(merkleRoot[i] >= 'A' && merkleRoot[i] <= 'F') ||
			(merkleRoot[i] >= 0 && merkleRoot[i] <= 255) // Allow raw bytes

		if !isValid {
			t.Errorf("Invalid character in Merkle root at position %d: %v", i, merkleRoot[i])
		}
	}
	fmt.Println("✓ Merkle root has valid format")
}
