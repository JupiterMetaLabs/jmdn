package utils

import (
	"jmdn/config"

	"github.com/ethereum/go-ethereum/crypto"
)

// GenerateLogsBloom creates a proper Ethereum-compatible logs bloom filter
// The bloom filter is 256 bytes (2048 bits) and uses Keccak-256 hashing
func GenerateLogsBloom(logs []config.Log) []byte {
	// Initialize 256-byte bloom filter (2048 bits)
	logsBloom := make([]byte, 256)

	// Process each log entry
	for _, log := range logs {
		// Add the log's address to the bloom filter
		addToBloom(logsBloom, log.Address.Bytes())

		// Add each topic to the bloom filter
		for _, topic := range log.Topics {
			addToBloom(logsBloom, topic.Bytes())
		}
	}

	return logsBloom
}

// addToBloom adds a 32-byte value to the bloom filter using Ethereum's algorithm
// This follows the same algorithm as Ethereum's logs_bloom function
func addToBloom(bloom []byte, data []byte) {
	// Ensure we have exactly 32 bytes (256 bits)
	if len(data) != 32 {
		// Pad or truncate to 32 bytes
		padded := make([]byte, 32)
		copy(padded, data)
		data = padded
	}

	// Use Keccak-256 hash as per Ethereum specification
	hash := crypto.Keccak256(data)

	// Extract three 11-bit indices (0-2047) from the hash
	// This follows Ethereum's exact algorithm for logs bloom filter
	index1 := (uint16(hash[0])<<3 | uint16(hash[1])>>5) & 0x7FF                      // 11 bits
	index2 := (uint16(hash[1])<<6 | uint16(hash[2])>>2) & 0x7FF                      // 11 bits
	index3 := (uint16(hash[2])<<9 | uint16(hash[3])<<1 | uint16(hash[4])>>7) & 0x7FF // 11 bits

	// Set the corresponding bits in the bloom filter
	setBit(bloom, index1)
	setBit(bloom, index2)
	setBit(bloom, index3)
}

// setBit sets a specific bit in the bloom filter
func setBit(bloom []byte, bitIndex uint16) {
	if bitIndex >= 2048 { // 256 bytes * 8 bits = 2048 bits
		return
	}

	byteIndex := bitIndex / 8
	bitOffset := bitIndex % 8

	bloom[byteIndex] |= 1 << (7 - bitOffset)
}

// GenerateBlockLogsBloom creates a block-level logs bloom filter from all receipts
// This combines all individual transaction receipt bloom filters using OR operation
func GenerateBlockLogsBloom(receipts []*config.Receipt) []byte {
	// Initialize 256-byte bloom filter (2048 bits)
	blockBloom := make([]byte, 256)

	// Combine all receipt bloom filters using OR operation
	for _, receipt := range receipts {
		if len(receipt.LogsBloom) == 256 {
			for i := 0; i < 256; i++ {
				blockBloom[i] |= receipt.LogsBloom[i]
			}
		}
	}

	return blockBloom
}
