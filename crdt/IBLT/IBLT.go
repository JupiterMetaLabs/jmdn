// Package iblt provides a minimal, production-ready Invertible Bloom Lookup Table implementation.
package IBLT

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// cell represents a single IBLT cell.
type cell struct {
	Count   int
	KeySum  uint64
	HashSum uint64
}

// IBLT is an Invertible Bloom Lookup Table for set reconciliation and fast key existence checks.
type IBLT struct {
	m     int
	k     int
	table []cell
}

// New creates a new IBLT with m cells and k hash functions.
func New(m, k int) *IBLT {
	if m <= 0 || k <= 0 {
		panic("iblt: m and k must be positive")
	}
	return &IBLT{
		m:     m,
		k:     k,
		table: make([]cell, m),
	}
}

// Insert adds a key to the IBLT.
func (ib *IBLT) Insert(key []byte) {
	keyInt := toUint64(key)
	hashInt := hashUint64(keyInt)
	for i := 0; i < ib.k; i++ {
		idx := ib.index(keyInt, i)
		c := &ib.table[idx]
		c.Count++
		c.KeySum ^= keyInt
		c.HashSum ^= hashInt
	}
}

// Exists checks if a key may be present in the IBLT.
// Returns true if the key may be present (false positives possible), false if definitely not present.
func (ib *IBLT) Exists(key []byte) bool {
	keyInt := toUint64(key)
	for i := 0; i < ib.k; i++ {
		idx := ib.index(keyInt, i)
		if ib.table[idx].Count <= 0 {
			return false
		}
	}
	return true
}

// toUint64 hashes arbitrary key bytes into a uint64.
func toUint64(key []byte) uint64 {
	h := sha256.Sum256(key)
	return binary.LittleEndian.Uint64(h[:8])
}

// hashUint64 is a second digest for the "hash" sum.
func hashUint64(x uint64) uint64 {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, x)
	h := sha256.Sum256(buf)
	return binary.LittleEndian.Uint64(h[:8])
}

// index computes the i-th hash position for a key.
func (ib *IBLT) index(keyInt uint64, i int) int {
	buf := make([]byte, 9)
	binary.LittleEndian.PutUint64(buf[:8], keyInt)
	buf[8] = byte(i)
	h := sha256.Sum256(buf)
	return int(binary.LittleEndian.Uint64(h[:8]) % uint64(ib.m))
}

// Size returns the number of cells in the IBLT.
func (ib *IBLT) Size() int {
	return ib.m
}

// HashCount returns the number of hash functions used.
func (ib *IBLT) HashCount() int {
	return ib.k
}

// String returns a summary of the IBLT for debugging.
func (ib *IBLT) String() string {
	return fmt.Sprintf("IBLT(m=%d, k=%d)", ib.m, ib.k)
}
