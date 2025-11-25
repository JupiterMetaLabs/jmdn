package BloomFilter

import (
	"encoding/base64"
	"encoding/json"
	"hash"
	"hash/fnv"
	"math"
)

/*
BloomFilter is a data structure that allows for efficient membership testing.
It is a probabilistic data structure that allows for efficient membership testing.
This is the Abstract Interface for the Implementation of the BloomFilter.
*/


// NewBloomFilter creates a new Bloom filter with the specified size and number of hash functions
// size: the number of bits in the filter
// numHashFunctions: the number of hash functions to use (k)
func NewBloomFilter(size uint, numHashFunctions uint) *StandardBloomFilter {
	bf := &StandardBloomFilter{
		bitSet:           make([]bool, size),
		size:             size,
		numHashFunctions: numHashFunctions,
		hashFn:           make([]hash.Hash64, numHashFunctions),
	}

	// Initialize hash functions
	for i := uint(0); i < numHashFunctions; i++ {
		bf.hashFn[i] = fnv.New64a()
	}

	return bf
}

// NewBloomFilterWithFalsePositiveRate creates a Bloom filter optimized for a given false positive rate
// expectedElements: expected number of elements to be inserted
// falsePositiveRate: desired false positive rate (e.g., 0.01 for 1%)
func NewBloomFilterWithFalsePositiveRate(expectedElements uint, falsePositiveRate float64) *StandardBloomFilter {
	size, numHashFunctions := optimalParameters(expectedElements, falsePositiveRate)
	return NewBloomFilter(size, numHashFunctions)
}

// Add inserts an item into the Bloom filter
func (bf *StandardBloomFilter) Add(item string) {
	for i := uint(0); i < bf.numHashFunctions; i++ {
		index := bf.hash(item, i)
		bf.bitSet[index] = true
	}
}

// IsMember checks if an item might be in the Bloom filter
// Returns true if the item might be present (could be false positive)
// Returns false if the item is definitely not present
func (bf *StandardBloomFilter) IsMember(item string) bool {
	for i := uint(0); i < bf.numHashFunctions; i++ {
		index := bf.hash(item, i)
		if !bf.bitSet[index] {
			return false
		}
	}
	return true
}

// GetBloomFilter returns a string representation of the Bloom filter
// The string is a base64-encoded JSON containing the bit array and metadata
func (bf *StandardBloomFilter) GetBloomFilter() string {
	// Convert bool slice to byte slice (packed bits)
	byteArray := bf.boolSliceToByteSlice(bf.bitSet)

	data := BloomFilterData{
		BitSet:           byteArray,
		Size:             bf.size,
		NumHashFunctions: bf.numHashFunctions,
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return ""
	}

	// Encode to base64 for easy string representation
	return base64.StdEncoding.EncodeToString(jsonData)
}

// FromString reconstructs a Bloom filter from its string representation
func FromString(filterString string) (*StandardBloomFilter, error) {
	// Decode from base64
	jsonData, err := base64.StdEncoding.DecodeString(filterString)
	if err != nil {
		return nil, err
	}

	var data BloomFilterData
	err = json.Unmarshal(jsonData, &data)
	if err != nil {
		return nil, err
	}

	bf := &StandardBloomFilter{
		size:             data.Size,
		numHashFunctions: data.NumHashFunctions,
		hashFn:           make([]hash.Hash64, data.NumHashFunctions),
	}

	// Convert byte slice back to bool slice
	bf.bitSet = bf.byteSliceToBoolSlice(data.BitSet, data.Size)

	// Initialize hash functions
	for i := uint(0); i < bf.numHashFunctions; i++ {
		bf.hashFn[i] = fnv.New64a()
	}

	return bf, nil
}

// boolSliceToByteSlice converts a bool slice to a packed byte slice
// Each byte stores 8 bool values
func (bf *StandardBloomFilter) boolSliceToByteSlice(bools []bool) []byte {
	numBytes := (len(bools) + 7) / 8
	bytes := make([]byte, numBytes)

	for i, b := range bools {
		if b {
			byteIndex := i / 8
			bitIndex := uint(i % 8)
			bytes[byteIndex] |= 1 << bitIndex
		}
	}

	return bytes
}

// byteSliceToBoolSlice converts a packed byte slice back to a bool slice
func (bf *StandardBloomFilter) byteSliceToBoolSlice(bytes []byte, size uint) []bool {
	bools := make([]bool, size)

	for i := uint(0); i < size; i++ {
		byteIndex := i / 8
		bitIndex := i % 8
		if byteIndex < uint(len(bytes)) {
			bools[i] = (bytes[byteIndex] & (1 << bitIndex)) != 0
		}
	}

	return bools
}

// hash computes the hash for an item using the i-th hash function
func (bf *StandardBloomFilter) hash(item string, i uint) uint {
	bf.hashFn[i].Reset()
	bf.hashFn[i].Write([]byte(item))
	// Use double hashing technique: h(i) = (h1 + i * h2) mod m
	hash1 := bf.hashFn[i].Sum64()
	hash2 := hash1 >> 32 // Use upper 32 bits as second hash
	combinedHash := (hash1 + uint64(i)*hash2) % uint64(bf.size)
	return uint(combinedHash)
}

// optimalParameters calculates optimal size and number of hash functions
// based on expected elements and desired false positive rate
func optimalParameters(expectedElements uint, falsePositiveRate float64) (size uint, numHashFunctions uint) {
	// Optimal size: m = -(n * ln(p)) / (ln(2)^2)
	// Optimal k: k = (m/n) * ln(2)
	n := float64(expectedElements)
	p := falsePositiveRate

	m := -(n * math.Log(p)) / (math.Log(2) * math.Log(2))
	k := (m / n) * math.Log(2)

	size = uint(math.Ceil(m))
	numHashFunctions = uint(math.Ceil(k))

	// Ensure at least 1 hash function
	if numHashFunctions < 1 {
		numHashFunctions = 1
	}

	return size, numHashFunctions
}

// Additional utility methods

// GetSize returns the size of the bit array
func (bf *StandardBloomFilter) GetSize() uint {
	return bf.size
}

// GetNumHashFunctions returns the number of hash functions being used
func (bf *StandardBloomFilter) GetNumHashFunctions() uint {
	return bf.numHashFunctions
}

// GetFillRatio returns the percentage of bits set to true
func (bf *StandardBloomFilter) GetFillRatio() float64 {
	count := 0
	for _, bit := range bf.bitSet {
		if bit {
			count++
		}
	}
	return float64(count) / float64(bf.size)
}

// EstimateFalsePositiveRate estimates the current false positive rate
// based on the number of bits set
func (bf *StandardBloomFilter) EstimateFalsePositiveRate() float64 {
	fillRatio := bf.GetFillRatio()
	// FPR ≈ (1 - e^(-kn/m))^k
	// Where fillRatio ≈ 1 - e^(-kn/m)
	k := float64(bf.numHashFunctions)
	return math.Pow(fillRatio, k)
}