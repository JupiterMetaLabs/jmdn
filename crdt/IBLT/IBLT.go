// Package iblt provides a minimal, production-ready Invertible Bloom Lookup Table implementation.
package IBLT

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

// cell represents a single IBLT cell.
type cell struct {
	Count   int    `json:"count"`
	KeySum  uint64 `json:"keysum"`
	HashSum uint64 `json:"hashsum"`
}

// IBLT is an Invertible Bloom Lookup Table for set reconciliation and fast key existence checks.
type IBLT struct {
	M     int    `json:"m"`     // Number of cells
	K     int    `json:"k"`     // Number of hash functions
	Table []cell `json:"table"` // The table of cells
}

func (ib *IBLT) To_JsonRawMessage() json.RawMessage {
	// Convert IBLT to JSON
	jsonBytes, err := json.Marshal(ib)
	if err != nil {
		return nil
	}
	return json.RawMessage(jsonBytes)
}

func OptimalIBLTParams(nKeys int, expectedDiffRatio, safetyFactor float64) (m, k int) {
	// Default parameters if caller passes 0
	if expectedDiffRatio == 0 {
		expectedDiffRatio = 1.0
	}
	if safetyFactor == 0 {
		safetyFactor = 2.0
	}

	if nKeys <= 5 {
		return 10, 3
	}

	// Theoretical peel thresholds α* for k = 3,4,5
	const (
		thresh3 = 0.81
		thresh4 = 0.89
		thresh5 = 0.93
	)

	// Δ = how many keys we expect to reconcile
	diff := float64(nKeys) * expectedDiffRatio

	// table size m = safetyFactor × diff
	m = int(math.Ceil(diff * safetyFactor))

	// actual occupancy
	alpha := diff / float64(m)

	// choose k based on where alpha falls
	switch {
	case alpha <= thresh3:
		k = 3
	case alpha <= thresh4:
		k = 4
	case alpha <= thresh5:
		k = 5
	default:
		k = 6
	}

	return m, k
}

// New creates a new IBLT with m cells and k hash functions.
func New(m, k int) *IBLT {
	if m <= 0 || k <= 0 {
		panic("iblt: m and k must be positive")
	}
	return &IBLT{
		M:     m,
		K:     k,
		Table: make([]cell, m),
	}
}

// Insert adds a key to the IBLT.
func (ib *IBLT) Insert(key []byte) {
	keyInt := toUint64(key)
	hashInt := hashUint64(keyInt)
	for i := 0; i < ib.K; i++ {
		idx := ib.index(keyInt, i)
		c := &ib.Table[idx]
		c.Count++
		c.KeySum ^= keyInt
		c.HashSum ^= hashInt
	}
}

// Exists checks if a key may be present in the IBLT.
// Returns true if the key may be present (false positives possible), false if definitely not present.
func (ib *IBLT) Exists(key []byte) bool {
	keyInt := toUint64(key)
	for i := 0; i < ib.K; i++ {
		idx := ib.index(keyInt, i)
		if ib.Table[idx].Count <= 0 {
			return false
		}
	}
	return true
}

// --- New Functions: Add and Subtract ---

// Add combines two IBLTs (ib + other), effectively creating a union of the elements.
// Both IBLTs must have the same parameters (m and k).
func (ib *IBLT) Add(other *IBLT) (*IBLT, error) {
	if err := ib.validateCompatibility(other); err != nil {
		return nil, err
	}

	result := New(ib.M, ib.K)

	for i := 0; i < ib.M; i++ {
		cellA := ib.Table[i]
		cellB := other.Table[i]

		// Count uses standard addition
		result.Table[i].Count = cellA.Count + cellB.Count

		// KeySum and HashSum use XOR
		result.Table[i].KeySum = cellA.KeySum ^ cellB.KeySum
		result.Table[i].HashSum = cellA.HashSum ^ cellB.HashSum
	}

	return result, nil
}

// Subtract calculates the difference between two IBLTs (ib - other).
// The resulting IBLT represents the set difference, which is used for reconciliation.
// Both IBLTs must have the same parameters (m and k).
func (ib *IBLT) Subtract(other *IBLT) (*IBLT, error) {
	if err := ib.validateCompatibility(other); err != nil {
		return nil, err
	}

	result := New(ib.M, ib.K)

	for i := 0; i < ib.M; i++ {
		cellA := ib.Table[i]
		cellB := other.Table[i]

		// Count uses standard subtraction
		result.Table[i].Count = cellA.Count - cellB.Count

		// KeySum and HashSum use XOR (since XOR is its own inverse)
		result.Table[i].KeySum = cellA.KeySum ^ cellB.KeySum
		result.Table[i].HashSum = cellA.HashSum ^ cellB.HashSum
	}

	return result, nil
}

func (ib *IBLT) validateCompatibility(other *IBLT) error {
	if ib.M != other.M || ib.K != other.K {
		return fmt.Errorf("iblt: cannot combine IBLTs with different parameters (m=%d/%d, k=%d/%d)", ib.M, other.M, ib.K, other.K)
	}
	// Note: We assume the hash functions themselves are identical, which they are in this implementation.
	return nil
}

// --- Utility Functions (Unchanged) ---

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
	return int(binary.LittleEndian.Uint64(h[:8]) % uint64(ib.M))
}

// Size returns the number of cells in the IBLT.
func (ib *IBLT) Size() int {
	return ib.M
}

// HashCount returns the number of hash functions used.
func (ib *IBLT) HashCount() int {
	return ib.K
}

// String returns a summary of the IBLT for debugging.
func (ib *IBLT) String() string {
	return fmt.Sprintf("IBLT(m=%d, k=%d)", ib.M, ib.K)
}

// ListEntries attempts to peel the IBLT and returns the positive and negative keys.
// If decoding fails (i.e., not all keys can be peeled), returns an error.
// validateCompatibility checks if two IBLTs can be added or subtracted.

// Client: After subtracting (your IBLT - peer's IBLT),
// positive keys = items you need to send to the peer.
// negative keys = items you are missing and need to request from the peer.
func (ib *IBLT) ListEntries() (positiveKeys [][]byte, negativeKeys [][]byte, err error) {
	// Make a copy of the table to avoid mutating the original
	table := make([]cell, len(ib.Table))
	copy(table, ib.Table)

	var (
		m      = ib.M
		k      = ib.K
		peeled = make([]bool, m)
		found  = true
	)

	positive := make([][]byte, 0)
	negative := make([][]byte, 0)

	for found {
		found = false
		for i := 0; i < m; i++ {
			if peeled[i] {
				continue
			}
			c := table[i]
			if c.Count == 1 || c.Count == -1 {
				// Try to recover the key
				keyInt := c.KeySum
				hashInt := hashUint64(keyInt)
				// Validate the cell
				if c.HashSum != hashInt {
					continue // Not a valid singleton
				}
				// Reconstruct the key bytes (best effort)
				keyBytes := make([]byte, 8)
				binary.LittleEndian.PutUint64(keyBytes, keyInt)
				if c.Count == 1 {
					positive = append(positive, keyBytes)
				} else {
					negative = append(negative, keyBytes)
				}
				// Remove the key's effect from all k cells
				for j := 0; j < k; j++ {
					idx := ib.index(keyInt, j)
					table[idx].Count -= c.Count
					table[idx].KeySum ^= keyInt
					table[idx].HashSum ^= hashInt
				}
				peeled[i] = true
				found = true
			}
		}
	}

	// Check if all cells are zeroed out (successful peel)
	for i := 0; i < m; i++ {
		if table[i].Count != 0 || table[i].KeySum != 0 || table[i].HashSum != 0 {
			return nil, nil, fmt.Errorf("IBLT: failed to fully decode (unpeeled cells remain)")
		}
	}

	return positive, negative, nil
}
