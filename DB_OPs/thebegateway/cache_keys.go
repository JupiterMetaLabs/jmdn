// MODULE: DB_OPs/thebegateway/cache_keys.go
// PURPOSE: Redis cache key builders and TTL constants for the ThebeDB integration layer.
//
// CORE DATA STRUCTURES:
//   - TTL constants: typed time.Duration values; immutable data → long TTL, mutable → short TTL
//   - Key builder funcs: pure functions; no state; safe for concurrent use
//
// TO MODIFY BEHAVIOR:
//   - Change TTL: update constant here — all callers pick up new value automatically
//   - Add new entity key: add TTL constant + key builder func following existing pattern
//
// DO NOT:
//   - Import gossipnode/DB_OPs (cycle risk)
//   - Add state or side effects to key builders
//   - Use bare string literals for prefixes in callers — always call builder funcs
//
// EXTENSION POINT: new cache entity → new TTL constant + key builder func here

package thebegateway

import (
	"fmt"
	"time"
)

// TTL constants — immutable data gets long TTL, mutable gets short.
const (
	TTLAccount      = 30 * time.Second
	TTLLatestBlock  = 30 * time.Second // changes every block — short TTL
	TTLBlock        = 1 * time.Hour
	TTLTransaction  = 1 * time.Hour
	TTLSnapshot     = 1 * time.Hour
	TTLZKProof      = 1 * time.Hour
	TTLL1Finality   = 1 * time.Hour
	TTLContractCode = 24 * time.Hour // immutable after deploy — Phase 7
)

// LatestBlockKey returns the Redis cache key for the latest block.
// Singleton key — no parameter. TTL = TTLLatestBlock.
func LatestBlockKey() string {
	return "jmdn:latest_block"
}

// AccountKey returns the Redis cache key for an account by address.
func AccountKey(address string) string {
	return "jmdn:account:" + address
}

// BlockKey returns the Redis cache key for a block by number.
func BlockKey(blockNumber uint64) string {
	return fmt.Sprintf("jmdn:block:%d", blockNumber)
}

// TransactionKey returns the Redis cache key for a transaction by hash.
func TransactionKey(txHash string) string {
	return "jmdn:tx:" + txHash
}

// SnapshotKey returns the Redis cache key for a snapshot by block number.
func SnapshotKey(blockNumber uint64) string {
	return fmt.Sprintf("jmdn:snapshot:%d", blockNumber)
}

// ZKProofKey returns the Redis cache key for a ZK proof by block number.
func ZKProofKey(blockNumber uint64) string {
	return fmt.Sprintf("jmdn:zk:%d", blockNumber)
}

// L1FinalityKey returns the Redis cache key for an L1 finality record by confirmation hash.
func L1FinalityKey(confirmation string) string {
	return "jmdn:l1finality:" + confirmation
}

// ContractReceiptKey returns the Redis cache key for a contract receipt by tx hash.
// Distinct from TransactionKey to prevent cache collision between receipts and txs.
func ContractReceiptKey(txHash string) string {
	return "jmdn:contract:receipt:" + txHash
}

// ContractCodeKey returns the Redis cache key for contract bytecode by address.
// Phase 7 — no matching ThebeReader.GetContractCode yet; wired when contract KV layer lands.
func ContractCodeKey(address string) string {
	return "jmdn:contract:code:" + address
}
