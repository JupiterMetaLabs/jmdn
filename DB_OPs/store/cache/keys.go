// MODULE: DB_OPs/store/cache/keys.go
// PURPOSE: Provide pure cache key builders and TTL constants — zero state, zero imports of internal packages.
//
// CORE DATA STRUCTURES:
//   - All functions: pure string formatters. No allocation beyond fmt.Sprintf result. Fixed arity.
//
// TO MODIFY BEHAVIOR:
//   - Change a key scheme: update the function here and all cache decorators pick it up automatically (single call site per key type).
//   - Add a new key type: add a new func here + new TTL constant. No other file changes needed.
//
// DO NOT:
//   - Add state to this file — key builders must be pure functions.
//   - Import any gossipnode internal package — this file is a leaf with no transitive deps.
//
// EXTENSION POINT: add new key builder func here → reference from new cache decorator file.

package cache

import (
	"fmt"
	"time"
)

const (
	TTLBlock       = 5 * time.Minute
	TTLBlockLatest = 2 * time.Second
	TTLAccount     = 30 * time.Second
	TTLTx          = 5 * time.Minute
	TTLZKProof     = 10 * time.Minute
)

// Block returns the cache key for a block record by block number.
func Block(blockNumber uint64) string { return fmt.Sprintf("block:%d", blockNumber) }

// BlockHash returns the cache key for a block record by block hash.
func BlockHash(hash string) string { return fmt.Sprintf("block:hash:%s", hash) }

// BlockLatest returns the cache key for the latest block number.
func BlockLatest() string { return "block:latest" }

// Account returns the cache key for an account record by address.
func Account(address string) string { return fmt.Sprintf("account:%s", address) }

// AccountDID returns the cache key for an account record looked up by DID.
func AccountDID(did string) string { return fmt.Sprintf("account:did:%s", did) }

// Tx returns the cache key for a transaction record by hash.
func Tx(txHash string) string { return fmt.Sprintf("tx:%s", txHash) }

// ZKProof returns the cache key for a ZK proof record by block number.
func ZKProof(blockNumber uint64) string { return fmt.Sprintf("zk:%d", blockNumber) }
