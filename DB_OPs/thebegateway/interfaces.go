// MODULE: DB_OPs/thebegateway/interfaces.go
// PURPOSE: Define the write, read, and outbox-WAL contracts for the ThebeDB integration layer.
//
// CORE DATA STRUCTURES:
//   - ThebeGateway: stateless per-call write surface; implementations hold builder+cache+outbox deps
//   - ThebeReader: stateless per-call read surface; implementations hold sql+cache deps
//   - OutboxStore: WAL persistence; implementations backed by SQLite thebe_outbox table
//
// TO MODIFY BEHAVIOR:
//   - Add new entity: add method to ThebeGateway + ThebeReader → implement in concrete structs
//   - Swap cache backend: change concrete ThebeReader impl — interface unchanged
//   - Swap SQL backend: change concrete ThebeReader impl — interface unchanged
//
// DO NOT:
//   - Import gossipnode/DB_OPs from this package (creates import cycle)
//   - Add implementation logic here (interfaces only)
//   - Add methods unused by any single caller (ISP violation)
//
// EXTENSION POINT: new entity type → add method pair to ThebeGateway + ThebeReader interfaces
//
// CHANGE SCENARIOS:
//   Add contract KV methods: add WriteContractCode/GetContractCode to both interfaces — Phase 7
//   Swap outbox backend: implement OutboxStore with different backing — OutboxWorker unchanged

package thebegateway

import (
	"context"
	"time"
)

// ThebeGateway — write surface. Facade over ThebeDB 2PC (SQL + KV) + cache.
// Each method: atomic SQL+KV write via ThebeDB 2PC, then best-effort cache SET.
// On 2PC failure: payload written to outbox WAL for retry.
// Callers map from internal config.* types to *Record DTOs before calling.
type ThebeGateway interface {
	WriteBlock(ctx context.Context, block *BlockRecord) error
	WriteAccount(ctx context.Context, account *AccountRecord) error
	WriteTransaction(ctx context.Context, tx *TransactionRecord) error
	WriteSnapshot(ctx context.Context, snapshot *SnapshotRecord) error
	WriteZKProof(ctx context.Context, proof *ZKProofRecord) error
	WriteL1Finality(ctx context.Context, finality *L1FinalityRecord) error
}

// ThebeReader — read surface. Read-through cache: cache hit → return; miss → SQL/KV → cache SET with TTL → return.
type ThebeReader interface {
	GetLatestBlock(ctx context.Context) (*BlockRecord, error)
	GetAccount(ctx context.Context, address string) (*AccountRecord, error)
	GetBlock(ctx context.Context, blockNumber uint64) (*BlockRecord, error)
	GetTransaction(ctx context.Context, txHash string) (*TransactionRecord, error)
	GetLatestTransactionsByAddress(ctx context.Context, address string, limit int) ([]*TransactionRecord, error)
	GetZKProof(ctx context.Context, blockNumber uint64) (*ZKProofRecord, error)
	GetSnapshot(ctx context.Context, blockNumber uint64) (*SnapshotRecord, error)
}

// OutboxStore — WAL persistence for failed ThebeGateway writes.
// Entries retried by OutboxWorker with exponential backoff. Max 10 attempts.
type OutboxStore interface {
	Enqueue(ctx context.Context, entry OutboxEntry) error
	Next(ctx context.Context, limit int) ([]OutboxEntry, error)
	Ack(ctx context.Context, id int64) error
	IncrementAttempts(ctx context.Context, id int64, nextRetryAt time.Time) error
}
