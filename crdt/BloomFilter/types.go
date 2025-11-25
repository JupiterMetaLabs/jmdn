package BloomFilter

import "hash"

type BloomFilter interface {
	Add(item string)
	IsMember(item string) bool
	GetBloomFilter() string
}

// StandardBloomFilter implements the BloomFilter interface
type StandardBloomFilter struct {
	bitSet           []bool
	size             uint
	hashFn           []hash.Hash64
	numHashFunctions uint
}

// BloomFilterData is used for serialization/deserialization
type BloomFilterData struct {
	BitSet           []byte `json:"bitSet"`
	Size             uint   `json:"size"`
	NumHashFunctions uint   `json:"numHashFunctions"`
}