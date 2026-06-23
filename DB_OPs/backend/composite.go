// MODULE: DB_OPs/backend/composite.go
// PURPOSE: Assemble cache-decorated domain stores into a single store.ThebeHandle for the connection pool.
//
// CORE DATA STRUCTURES:
//   - compositeHandle: fixed struct holding 6 domain store interfaces + 1 Closer.
//     Allocated once per pool slot (max 20). Not shared across goroutines.
//     Access pattern: method dispatch — each method delegates to one field.
//
// TO MODIFY BEHAVIOR:
//   - Change cache policy: edit the relevant DB_OPs/store/cache/* decorator.
//   - Add new entity store: add field to compositeHandle, wire in NewComposite().
//
// DO NOT:
//   - Add business logic here — this is pure assembly/wiring.
//   - Import config.ImmuClient or DB_OPs/dualdb.
//
// EXTENSION POINT: add new XStore field → wire in NewComposite() → this file only changes here.

package backend

import (
	"context"
	"io"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"

	"gossipnode/DB_OPs/store"
	stcache "gossipnode/DB_OPs/store/cache"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// compositeHandle assembles cache-decorated domain stores into a single store.ThebeHandle.
// Each field is an interface; the concrete type behind each is a cache decorator wrapping thebeBackend.
// ReceiptStore and LogStore are wired directly to thebeBackend (no cache decorator).
type compositeHandle struct {
	blocks   store.BlockStore
	accounts store.AccountStore
	txs      store.TxStore
	zkproofs store.ZKProofStore
	receipts store.ReceiptStore
	logs     store.LogStore
	closer   io.Closer
}

// compile-time assertion: compositeHandle must satisfy store.ThebeHandle.
var _ store.ThebeHandle = (*compositeHandle)(nil)

// NewComposite builds a ThebeHandle by wrapping backend with cache decorators.
// c may be nil — a noopCache is used in that case.
// b must be non-nil; panics if nil (programming error at wiring site).
func NewComposite(b *thebeBackend, c cache.Cache) store.ThebeHandle {
	if c == nil {
		c = stcache.NewNoopCache()
	}
	return &compositeHandle{
		blocks:   stcache.NewCachedBlockStore(b, c),
		accounts: stcache.NewCachedAccountStore(b, c),
		txs:      stcache.NewCachedTxStore(b, c),
		zkproofs: stcache.NewCachedZKProofStore(b, c),
		receipts: b, // ReceiptStore — no cache decorator (generated on-the-fly)
		logs:     b, // LogStore — no cache (filter queries, complex invalidation)
		closer:   b,
	}
}

// — BlockStore —

// StoreBlock delegates to the cache-decorated BlockStore.
func (h *compositeHandle) StoreBlock(ctx context.Context, block *config.ZKBlock) error {
	return h.blocks.StoreBlock(ctx, block)
}

// GetBlock delegates to the cache-decorated BlockStore.
func (h *compositeHandle) GetBlock(ctx context.Context, blockNumber uint64) (*thebegateway.BlockRecord, error) {
	return h.blocks.GetBlock(ctx, blockNumber)
}

// GetBlockByHash delegates to the cache-decorated BlockStore.
func (h *compositeHandle) GetBlockByHash(ctx context.Context, hash string) (*thebegateway.BlockRecord, error) {
	return h.blocks.GetBlockByHash(ctx, hash)
}

// GetLatestBlockNumber delegates to the cache-decorated BlockStore.
func (h *compositeHandle) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	return h.blocks.GetLatestBlockNumber(ctx)
}

// BulkGetBlocks delegates to the cache-decorated BlockStore.
func (h *compositeHandle) BulkGetBlocks(ctx context.Context, from, to uint64) ([]*thebegateway.BlockRecord, error) {
	return h.blocks.BulkGetBlocks(ctx, from, to)
}

// — AccountStore —

// CreateAccount delegates to the cache-decorated AccountStore.
func (h *compositeHandle) CreateAccount(ctx context.Context, account *store.Account) error {
	return h.accounts.CreateAccount(ctx, account)
}

// UpdateAccountBalance delegates to the cache-decorated AccountStore.
func (h *compositeHandle) UpdateAccountBalance(ctx context.Context, address, balance string) error {
	return h.accounts.UpdateAccountBalance(ctx, address, balance)
}

// GetAccount delegates to the cache-decorated AccountStore.
func (h *compositeHandle) GetAccount(ctx context.Context, address string) (*store.Account, error) {
	return h.accounts.GetAccount(ctx, address)
}

// GetAccountByDID delegates to the cache-decorated AccountStore.
func (h *compositeHandle) GetAccountByDID(ctx context.Context, did string) (*store.Account, error) {
	return h.accounts.GetAccountByDID(ctx, did)
}

// CheckNonceDuplicate delegates to the cache-decorated AccountStore.
func (h *compositeHandle) CheckNonceDuplicate(ctx context.Context, address string, nonce uint64) (bool, error) {
	return h.accounts.CheckNonceDuplicate(ctx, address, nonce)
}

// GetLatestNonce delegates to the cache-decorated AccountStore.
func (h *compositeHandle) GetLatestNonce(ctx context.Context, address string) (uint64, error) {
	return h.accounts.GetLatestNonce(ctx, address)
}

// BulkGetAccounts delegates to the cache-decorated AccountStore.
func (h *compositeHandle) BulkGetAccounts(ctx context.Context, addresses []string) ([]*store.Account, error) {
	return h.accounts.BulkGetAccounts(ctx, addresses)
}

// ListAccounts delegates to the cache-decorated AccountStore.
func (h *compositeHandle) ListAccounts(ctx context.Context, limit int) ([]*store.Account, error) {
	return h.accounts.ListAccounts(ctx, limit)
}

func (h *compositeHandle) ListAccountsPaginated(ctx context.Context, limit, offset int) ([]*store.Account, error) {
	return h.accounts.ListAccountsPaginated(ctx, limit, offset)
}

func (h *compositeHandle) CountAccounts(ctx context.Context) (uint64, error) {
	return h.accounts.CountAccounts(ctx)
}

func (h *compositeHandle) GetAccountsByNonces(ctx context.Context, nonces []uint64) ([]*store.Account, error) {
	return h.accounts.GetAccountsByNonces(ctx, nonces)
}

// — TxStore —

// StoreTransaction delegates to the cache-decorated TxStore.
func (h *compositeHandle) StoreTransaction(ctx context.Context, tx *config.Transaction, blockNumber uint64, txIndex int) error {
	return h.txs.StoreTransaction(ctx, tx, blockNumber, txIndex)
}

// GetTransaction delegates to the cache-decorated TxStore.
func (h *compositeHandle) GetTransaction(ctx context.Context, txHash string) (*thebegateway.TransactionRecord, error) {
	return h.txs.GetTransaction(ctx, txHash)
}

// GetTransactionsByBlock delegates to the cache-decorated TxStore.
func (h *compositeHandle) GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]*thebegateway.TransactionRecord, error) {
	return h.txs.GetTransactionsByBlock(ctx, blockNumber)
}

// GetTransactionsPaginated delegates to the cache-decorated TxStore.
func (h *compositeHandle) GetTransactionsPaginated(ctx context.Context, limit, offset int) ([]*thebegateway.TransactionRecord, error) {
	return h.txs.GetTransactionsPaginated(ctx, limit, offset)
}

// CountTransactions delegates to the cache-decorated TxStore.
func (h *compositeHandle) CountTransactions(ctx context.Context) (uint64, error) {
	return h.txs.CountTransactions(ctx)
}

// RefreshAccountTxStats delegates to the cache-decorated TxStore.
func (h *compositeHandle) RefreshAccountTxStats(ctx context.Context, address string) error {
	return h.txs.RefreshAccountTxStats(ctx, address)
}

// GetTransactionsByAddress delegates to the cache-decorated TxStore.
func (h *compositeHandle) GetTransactionsByAddress(ctx context.Context, address string, limit int) ([]*thebegateway.TransactionRecord, error) {
	return h.txs.GetTransactionsByAddress(ctx, address, limit)
}

// SetTransactionStatus delegates to the cache-decorated TxStore.
func (h *compositeHandle) SetTransactionStatus(ctx context.Context, txHash string, status int) error {
	return h.txs.SetTransactionStatus(ctx, txHash, status)
}

// SetTxProcessing delegates to the cache-decorated TxStore.
func (h *compositeHandle) SetTxProcessing(ctx context.Context, txHash string) error {
	return h.txs.SetTxProcessing(ctx, txHash)
}

// ClearTxProcessing delegates to the cache-decorated TxStore.
func (h *compositeHandle) ClearTxProcessing(ctx context.Context, txHash string) error {
	return h.txs.ClearTxProcessing(ctx, txHash)
}

// IsTxProcessing delegates to the cache-decorated TxStore.
func (h *compositeHandle) IsTxProcessing(ctx context.Context, txHash string) (bool, error) {
	return h.txs.IsTxProcessing(ctx, txHash)
}

// — ZKProofStore —

// StoreZKBlock delegates to the cache-decorated ZKProofStore.
func (h *compositeHandle) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	return h.zkproofs.StoreZKBlock(ctx, block)
}

// GetZKProof delegates to the cache-decorated ZKProofStore.
func (h *compositeHandle) GetZKProof(ctx context.Context, blockNumber uint64) (*thebegateway.ZKProofRecord, error) {
	return h.zkproofs.GetZKProof(ctx, blockNumber)
}

// — ReceiptStore —

// GetReceipt delegates directly to thebeBackend (no cache — generated on-the-fly).
func (h *compositeHandle) GetReceipt(ctx context.Context, txHash string) (*config.Receipt, error) {
	return h.receipts.GetReceipt(ctx, txHash)
}

// — LogStore —

// StoreLogs delegates directly to thebeBackend (no cache — write path).
func (h *compositeHandle) StoreLogs(ctx context.Context, logs []*ethtypes.Log) error {
	return h.logs.StoreLogs(ctx, logs)
}

// GetLogs delegates directly to thebeBackend (no cache — filter queries, complex invalidation).
func (h *compositeHandle) GetLogs(ctx context.Context, filter store.LogFilter) ([]*ethtypes.Log, error) {
	return h.logs.GetLogs(ctx, filter)
}

// — Closer —

// Close releases resources held by the underlying thebeBackend.
// Satisfies io.Closer as part of store.ThebeHandle.
func (h *compositeHandle) Close() error {
	return h.closer.Close()
}
