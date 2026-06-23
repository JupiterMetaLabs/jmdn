// MODULE: DB_OPs/thebegateway/interfaces.go
// PURPOSE: Define the write, read, and outbox-WAL contracts for the ThebeDB integration layer.
//
// CORE DATA STRUCTURES:
//   - ThebeGateway: stateless per-call write surface; implementations hold builder+cache+outbox deps
//   - ThebeReader: stateless per-call read surface; implementations hold sql+cache deps
//   - OutboxStore: WAL persistence; implementations backed by SQLite thebe_outbox table
//   - ThebeKVStore: minimal kv.Store surface for direct contract KV writes (Phase 7 — DONE)
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
//   Phase 7 contract KV layer: DONE — WriteContractCode/GetContractCode + 4 more pairs implemented
//   Swap outbox backend: implement OutboxStore with different backing — OutboxWorker unchanged

package thebegateway

import (
	"context"
	"time"
)

// ThebeKVStore is the minimal kv.Store surface for direct contract KV writes.
// kv.Store from github.com/JupiterMetaLabs/ThebeDB/pkg/kv satisfies this.
// PutWorm: write-once (immutable). PutDerived: overwrite (mutable).
// Time: O(1) each — single BadgerDB round trip
type ThebeKVStore interface {
	PutWorm(key, value []byte) error
	PutDerived(key, value []byte) error
	Get(key []byte) ([]byte, error)
}

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

	// Contract KV layer — Phase 7
	WriteContractCode(ctx context.Context, rec *ContractCodeRecord) error
	WriteContractNonce(ctx context.Context, rec *ContractNonceRecord) error
	WriteContractStorage(ctx context.Context, rec *ContractStorageRecord) error
	WriteContractMeta(ctx context.Context, rec *ContractMetaRecord) error
	WriteContractReceipt(ctx context.Context, rec *ContractReceiptRecord) error

	// Tx processing flag — KV direct write.
	// SetTxProcessing marks txHash as in-flight (value="-1") in BadgerDB.
	// ClearTxProcessing removes the flag once the tx is confirmed or dropped.
	SetTxProcessing(ctx context.Context, txHash string) error
	ClearTxProcessing(ctx context.Context, txHash string) error
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

	// Phase 2.0 — bulk and alternate-key reads
	GetBlockByHash(ctx context.Context, hash string) (*BlockRecord, error)
	BulkGetBlocks(ctx context.Context, from, to uint64) ([]*BlockRecord, error)
	GetAccountByDID(ctx context.Context, did string) (*AccountRecord, error)
	BulkGetAccounts(ctx context.Context, addresses []string) ([]*AccountRecord, error)
	ListAccounts(ctx context.Context, limit int) ([]*AccountRecord, error)
	ListAccountsPaginated(ctx context.Context, limit, offset int) ([]*AccountRecord, error)
	CountAccounts(ctx context.Context) (uint64, error)
	GetAccountsByNonces(ctx context.Context, nonces []uint64) ([]*AccountRecord, error)
	GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]*TransactionRecord, error)
	GetTransactionsPaginated(ctx context.Context, limit, offset int) ([]*TransactionRecord, error)
	CountTransactions(ctx context.Context) (uint64, error)
	RefreshAccountTxStats(ctx context.Context, address string) error

	// Contract KV layer — Phase 7
	GetContractCode(ctx context.Context, address string) (*ContractCodeRecord, error)
	GetContractNonce(ctx context.Context, address string) (*ContractNonceRecord, error)
	GetContractStorage(ctx context.Context, address string, slot []byte) (*ContractStorageRecord, error)
	GetContractMeta(ctx context.Context, address string) (*ContractMetaRecord, error)
	GetContractReceipt(ctx context.Context, txHash string) (*ContractReceiptRecord, error)

	// IsTxProcessing returns true if txHash has an in-flight processing flag in KV.
	IsTxProcessing(ctx context.Context, txHash string) (bool, error)
}

// OutboxStore — WAL persistence for failed ThebeGateway writes.
// Entries retried by OutboxWorker with exponential backoff. Max 10 attempts.
type OutboxStore interface {
	Enqueue(ctx context.Context, entry OutboxEntry) error
	Next(ctx context.Context, limit int) ([]OutboxEntry, error)
	Ack(ctx context.Context, id int64) error
	IncrementAttempts(ctx context.Context, id int64, nextRetryAt time.Time) error
}
