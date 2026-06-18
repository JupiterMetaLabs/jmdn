// MODULE: DB_OPs/store/interfaces.go
// PURPOSE: Define storage contracts (interfaces) for all domain operations — the single source of truth
//          that every DB caller and every DB implementation binds to.
//
// CORE DATA STRUCTURES:
//   - ThebeHandle: composed interface (BlockStore+AccountStore+TxStore+ZKProofStore+ReceiptStore+LogStore+Closer).
//     Returned by the connection pool. Callers hold it by interface — never by concrete type.
//   - Domain interfaces: each is a narrow slice of ThebeHandle. Callers that need only BlockStore declare
//     that dependency; they never see AccountStore methods.
//   - Account (types.go): plain struct, fixed size, passed by pointer. Mirrors DB_OPs.Account.
//   - LogFilter (types.go): plain struct, fixed size, passed by value.
//
// TO MODIFY BEHAVIOR:
//   - Change a method signature: update the interface here + update thebeBackend in DB_OPs/backend/ +
//     update all callers. The compiler will flag every site.
//   - Add a new entity (e.g. SnapshotStore): add new interface here, embed in ThebeHandle, implement
//     in DB_OPs/backend/. No existing interface changes.
//
// DO NOT:
//   - Add implementation code or DB calls here — this package is contracts only.
//   - Import DB_OPs/backend, DB_OPs/dualdb, DB_OPs/cassata, or any package with DB calls.
//     config and DB_OPs/thebegateway are acceptable (shared domain types, no DB logic, no cycle).
//
// EXTENSION POINT: add a new XStore interface → embed in ThebeHandle → implement on thebeBackend.
//   This file does not need to change for any existing interface.

package store

import (
	"context"
	"io"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// BlockStore covers all block read/write operations.
type BlockStore interface {
	StoreBlock(ctx context.Context, block *config.ZKBlock) error
	GetBlock(ctx context.Context, blockNumber uint64) (*thebegateway.BlockRecord, error)
	GetBlockByHash(ctx context.Context, hash string) (*thebegateway.BlockRecord, error)
	GetLatestBlockNumber(ctx context.Context) (uint64, error)
	BulkGetBlocks(ctx context.Context, from, to uint64) ([]*thebegateway.BlockRecord, error)
}

// AccountStore covers account lifecycle and nonce management.
type AccountStore interface {
	CreateAccount(ctx context.Context, account *Account) error
	UpdateAccountBalance(ctx context.Context, address, balance string) error
	GetAccount(ctx context.Context, address string) (*Account, error)
	GetAccountByDID(ctx context.Context, did string) (*Account, error)
	CheckNonceDuplicate(ctx context.Context, address string, nonce uint64) (bool, error)
	GetLatestNonce(ctx context.Context, address string) (uint64, error)
	BulkGetAccounts(ctx context.Context, addresses []string) ([]*Account, error)
	ListAccounts(ctx context.Context, limit int) ([]*Account, error)
}

// TxStore covers transaction persistence and retrieval.
type TxStore interface {
	StoreTransaction(ctx context.Context, tx *config.Transaction, blockNumber uint64, txIndex int) error
	GetTransaction(ctx context.Context, txHash string) (*thebegateway.TransactionRecord, error)
	GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]*thebegateway.TransactionRecord, error)
	GetTransactionsByAddress(ctx context.Context, address string, limit int) ([]*thebegateway.TransactionRecord, error)
	SetTransactionStatus(ctx context.Context, txHash string, status int) error

	// Tx processing flag — backed by BadgerDB KV.
	// SetTxProcessing marks a transaction as in-flight ("-1" sentinel in KV).
	// ClearTxProcessing removes the flag once confirmed or dropped.
	// IsTxProcessing returns true when the sentinel exists and is non-empty.
	SetTxProcessing(ctx context.Context, txHash string) error
	ClearTxProcessing(ctx context.Context, txHash string) error
	IsTxProcessing(ctx context.Context, txHash string) (bool, error)
}

// ZKProofStore covers ZK proof persistence and retrieval.
type ZKProofStore interface {
	StoreZKBlock(ctx context.Context, block *config.ZKBlock) error
	GetZKProof(ctx context.Context, blockNumber uint64) (*thebegateway.ZKProofRecord, error)
}

// ReceiptStore covers on-the-fly receipt generation.
type ReceiptStore interface {
	GetReceipt(ctx context.Context, txHash string) (*config.Receipt, error)
}

// LogStore covers event log persistence and filtered retrieval.
type LogStore interface {
	StoreLogs(ctx context.Context, logs []*ethtypes.Log) error
	GetLogs(ctx context.Context, filter LogFilter) ([]*ethtypes.Log, error)
}

// ThebeHandle is the unified store surface. Implementations back it with
// ThebeDB (SQL + KV) as the primary store. io.Closer releases DB resources.
type ThebeHandle interface {
	BlockStore
	AccountStore
	TxStore
	ZKProofStore
	ReceiptStore
	LogStore
	io.Closer
}
