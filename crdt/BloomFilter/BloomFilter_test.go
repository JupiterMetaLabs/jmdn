package BloomFilter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

// Example demonstrates the usage of BloomFilter
// This is a niladic function used for Go documentation examples
func Example() {
	RunHashTest(10)
}

// RunHashTest tests the Bloom filter with hash strings of variable lengths
func RunHashTest(count int) {
	fmt.Printf("=== Bloom Filter Hash Test (Count: %d) ===\n\n", count)

	// Create a Bloom filter using the interface
	var bf BloomFilter = NewBloomFilter(uint(count*10), 3)

	// Generate and add hashes with variable lengths
	fmt.Println("Adding hashes to Bloom filter...")
	hashes := make([]string, count)
	start := time.Now()

	for i := 0; i < count; i++ {
		// Generate hash of variable length (16, 32, 64 characters)
		hashLength := []int{16, 32, 64}[i%3]
		hash := generateHash(i, hashLength)
		hashes[i] = hash
		bf.Add(hash)
		fmt.Printf("Added [%d]: %s (len: %d)\n", i+1, hash, len(hash))
	}

	addDuration := time.Since(start)
	fmt.Printf("\nAdded %d hashes in %v\n", count, addDuration)

	// Test membership for added hashes
	fmt.Println("\n--- Testing Membership (True Positives) ---")
	truePositives := 0
	start = time.Now()

	for i, hash := range hashes {
		if bf.IsMember(hash) {
			truePositives++
			fmt.Printf("✓ Hash [%d] found: %s\n", i+1, hash)
		} else {
			fmt.Printf("✗ Hash [%d] NOT found (ERROR): %s\n", i+1, hash)
		}
	}

	queryDuration := time.Since(start)
	fmt.Printf("\nTrue positives: %d/%d in %v\n", truePositives, count, queryDuration)

	// Test with non-existent hashes (false positive test)
	fmt.Println("\n--- Testing False Positives ---")
	falsePositives := 0
	testCount := count
	start = time.Now()

	for i := 0; i < testCount; i++ {
		// Generate different hashes that were not added
		hash := generateHash(i+count+1000, 32)
		if bf.IsMember(hash) {
			falsePositives++
			fmt.Printf("✗ False positive [%d]: %s\n", i+1, hash)
		}
	}

	fpDuration := time.Since(start)
	falsePositiveRate := float64(falsePositives) / float64(testCount) * 100
	fmt.Printf("\nFalse positives: %d/%d (%.2f%%) in %v\n",
		falsePositives, testCount, falsePositiveRate, fpDuration)

	// Get the Bloom filter as a string
	filterString := bf.GetBloomFilter()
	fmt.Println("\n--- Bloom Filter Serialization ---")
	fmt.Printf("Serialized length: %d bytes\n", len(filterString))
	if len(filterString) > 100 {
		fmt.Printf("First 100 chars: %s...\n", filterString[:100])
	} else {
		fmt.Printf("Full string: %s\n", filterString)
	}

	// Reconstruct the Bloom filter from the string
	fmt.Println("\n--- Reconstructing Bloom Filter ---")
	start = time.Now()
	reconstructedBF, err := FromString(filterString)
	if err != nil {
		fmt.Println("Error reconstructing:", err)
		return
	}
	reconstructDuration := time.Since(start)
	fmt.Printf("Reconstructed in %v\n", reconstructDuration)

	// Verify reconstructed filter
	fmt.Println("\n--- Verifying Reconstructed Filter ---")
	reconstructedMatches := 0
	for i, hash := range hashes {
		if reconstructedBF.IsMember(hash) {
			reconstructedMatches++
		} else {
			fmt.Printf("✗ Reconstruction error for hash [%d]: %s\n", i+1, hash)
		}
	}
	fmt.Printf("Reconstructed filter matches: %d/%d\n", reconstructedMatches, count)

	// Display filter statistics
	if sbf, ok := bf.(*StandardBloomFilter); ok {
		fmt.Println("\n--- Filter Statistics ---")
		fmt.Printf("Size: %d bits\n", sbf.GetSize())
		fmt.Printf("Number of hash functions: %d\n", sbf.GetNumHashFunctions())
		fmt.Printf("Fill ratio: %.2f%%\n", sbf.GetFillRatio()*100)
		fmt.Printf("Estimated false positive rate: %.4f (%.2f%%)\n",
			sbf.EstimateFalsePositiveRate(),
			sbf.EstimateFalsePositiveRate()*100)
		fmt.Printf("Actual false positive rate: %.2f%%\n", falsePositiveRate)
	}
}

// generateHash generates a hash string of specified length
func generateHash(seed int, length int) string {
	// Use SHA256 to generate a hash
	data := fmt.Sprintf("data_%d_%d", seed, time.Now().UnixNano())
	hash := sha256.Sum256([]byte(data))
	hexHash := hex.EncodeToString(hash[:])

	// Truncate or extend to desired length
	if length > len(hexHash) {
		// Extend by repeating
		for len(hexHash) < length {
			hash = sha256.Sum256([]byte(hexHash))
			hexHash += hex.EncodeToString(hash[:])
		}
	}

	return hexHash[:length]
}

// HashTestSmall demonstrates with a small dataset
func HashTestSmall() {
	fmt.Println("=== Small Hash Test (10 items) ===")
	RunHashTest(10)
}

// HashTestMedium demonstrates with a medium dataset
func HashTestMedium() {
	fmt.Println("=== Medium Hash Test (100 items) ===")
	RunHashTest(100)
}

// HashTestLarge demonstrates with a large dataset
func HashTestLarge() {
	fmt.Println("=== Large Hash Test (1000 items) ===")
	RunHashTest(1000)
}

// HashTestVariableLengths tests hashes of different lengths
func HashTestVariableLengths() {
	fmt.Println("=== Variable Length Hash Test ===")

	bf := NewBloomFilter(1000, 4)

	lengths := []int{8, 16, 32, 64, 128, 256}

	for _, length := range lengths {
		fmt.Printf("\nTesting with hash length: %d\n", length)

		// Add hashes
		testHashes := make([]string, 10)
		for i := 0; i < 10; i++ {
			hash := generateHash(i*length, length)
			testHashes[i] = hash
			bf.Add(hash)
			fmt.Printf("  Added: %s...\n", hash[:min(length, 40)])
		}

		// Verify
		found := 0
		for _, hash := range testHashes {
			if bf.IsMember(hash) {
				found++
			}
		}
		fmt.Printf("  Verification: %d/10 found\n", found)
	}

	fmt.Printf("\nFinal Statistics:\n")
	fmt.Printf("Fill ratio: %.2f%%\n", bf.GetFillRatio()*100)
	fmt.Printf("Estimated FPR: %.4f\n", bf.EstimateFalsePositiveRate())
}

// OptimizedHashTest demonstrates optimized filter with hash testing
func OptimizedHashTest() {
	count := 500
	fmt.Printf("=== Optimized Bloom Filter Hash Test (Count: %d) ===\n\n", count)

	// Create optimized filter for expected count with 1% false positive rate
	bf := NewBloomFilterWithFalsePositiveRate(uint(count), 0.01)

	fmt.Printf("Optimal parameters calculated:\n")
	fmt.Printf("  Size: %d bits\n", bf.GetSize())
	fmt.Printf("  Hash functions: %d\n", bf.GetNumHashFunctions())

	// Add hashes
	fmt.Printf("\nAdding %d hashes...\n", count)
	hashes := make([]string, count)
	for i := 0; i < count; i++ {
		hash := generateHash(i, 64)
		hashes[i] = hash
		bf.Add(hash)
	}

	// Test false positive rate
	fmt.Println("\nTesting false positive rate...")
	falsePositives := 0
	testCount := count * 2

	for i := 0; i < testCount; i++ {
		hash := generateHash(i+count+10000, 64)
		if bf.IsMember(hash) {
			falsePositives++
		}
	}

	actualFPR := float64(falsePositives) / float64(testCount)
	fmt.Printf("Actual false positive rate: %.4f (%.2f%%)\n", actualFPR, actualFPR*100)
	fmt.Printf("Expected false positive rate: 0.0100 (1.00%%)\n")
	fmt.Printf("Fill ratio: %.2f%%\n", bf.GetFillRatio()*100)
	fmt.Printf("Estimated FPR: %.4f (%.2f%%)\n",
		bf.EstimateFalsePositiveRate(),
		bf.EstimateFalsePositiveRate()*100)
}

// RunAllExamples runs all example functions
func RunAllExamples() {
	fmt.Println("=== Running All Bloom Filter Examples ===")

	HashTestSmall()
	fmt.Println("\n" + string(make([]byte, 80)) + "\n")

	HashTestVariableLengths()
	fmt.Println("\n" + string(make([]byte, 80)) + "\n")

	OptimizedHashTest()
}

// Helper function
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestRunHashTest tests the Bloom filter with a small dataset
func TestRunHashTest(t *testing.T) {
	RunHashTest(10)
}

// TestHashTestSmall tests with a small dataset
func TestHashTestSmall(t *testing.T) {
	HashTestSmall()
}

// TestHashTestMedium tests with a medium dataset
func TestHashTestMedium(t *testing.T) {
	HashTestMedium()
}

// TestHashTestLarge tests with a large dataset
func TestHashTestLarge(t *testing.T) {
	HashTestLarge()
}

// TestHashTestVariableLengths tests hashes of different lengths
func TestHashTestVariableLengths(t *testing.T) {
	HashTestVariableLengths()
}

// TestOptimizedHashTest tests optimized filter with hash testing
func TestOptimizedHashTest(t *testing.T) {
	OptimizedHashTest()
}

// TestBloomFilterBasic tests basic Bloom filter operations
func TestBloomFilterBasic(t *testing.T) {
	bf := NewBloomFilter(100, 3)

	// Test adding and checking membership
	testItems := []string{"item1", "item2", "item3"}
	for _, item := range testItems {
		bf.Add(item)
	}

	// All added items should be members
	for _, item := range testItems {
		if !bf.IsMember(item) {
			t.Errorf("Expected %s to be a member, but it wasn't", item)
		}
	}

	// Non-existent item might be a false positive, but we can test
	nonExistent := "not_in_filter"
	// We can't assert it's false because of false positives, but we can check it doesn't crash
	_ = bf.IsMember(nonExistent)
}

// TestBloomFilterSerialization tests serialization and deserialization
func TestBloomFilterSerialization(t *testing.T) {
	bf := NewBloomFilter(100, 3)

	// Add some items
	testItems := []string{"item1", "item2", "item3"}
	for _, item := range testItems {
		bf.Add(item)
	}

	// Serialize
	filterString := bf.GetBloomFilter()
	if filterString == "" {
		t.Fatal("GetBloomFilter returned empty string")
	}

	// Deserialize
	reconstructed, err := FromString(filterString)
	if err != nil {
		t.Fatalf("Failed to reconstruct Bloom filter: %v", err)
	}

	// Verify all items are still members
	for _, item := range testItems {
		if !reconstructed.IsMember(item) {
			t.Errorf("Reconstructed filter: expected %s to be a member, but it wasn't", item)
		}
	}
}

// TestBloomFilterWithFalsePositiveRate tests optimized filter creation
func TestBloomFilterWithFalsePositiveRate(t *testing.T) {
	expectedElements := uint(100)
	falsePositiveRate := 0.01

	bf := NewBloomFilterWithFalsePositiveRate(expectedElements, falsePositiveRate)

	if bf.GetSize() == 0 {
		t.Error("Bloom filter size should be greater than 0")
	}

	if bf.GetNumHashFunctions() == 0 {
		t.Error("Number of hash functions should be greater than 0")
	}
}
