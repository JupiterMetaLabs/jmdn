// MODULE: DB_OPs/store/cache/tx.go
// PURPOSE: Add Redis cache layer to TxStore — reads check cache first, writes invalidate cache after SQL commit.
//
// CORE DATA STRUCTURES:
//   - cachedTxStore: fixed struct (inner TxStore + cache.Cache). Stateless per-call.
//     Growth: fixed-size struct. No map, no slice, no unbounded state.
//
// TO MODIFY BEHAVIOR:
//   - Change TTL: update keys.go TTLTx constant — this file unchanged.
//   - Add caching to GetTransactionsByBlock/Address: not recommended — unbounded result sets.
//
// DO NOT:
//   - Call cache.Set before inner on writes — SQL/KV must commit first.
//   - Return error on cache miss or cache.Set failure — cache is best-effort.
//   - Import any concrete Redis or DB package here — depends on interfaces only.
//   - Store request-scoped state on the struct — stateless by design.
//
// EXTENSION POINT: implement store.TxStore — swap inner without changing this file.

package cache

import (
	"context"
	"encoding/json"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// cachedTxStore wraps a store.TxStore with a cache read-through / write-invalidation layer.
type cachedTxStore struct {
	inner store.TxStore
	c     cache.Cache
}

// Compile-time assertion: cachedTxStore implements store.TxStore.
var _ store.TxStore = (*cachedTxStore)(nil)

// NewCachedTxStore wraps inner with cache. Pass NewNoopCache() for no-cache mode.
func NewCachedTxStore(inner store.TxStore, c cache.Cache) store.TxStore {
	return &cachedTxStore{inner: inner, c: c}
}

// StoreTransaction delegates to inner, then invalidates the tx hash cache key on success.
// Time: O(1) inner write; cache invalidation is best-effort.
func (s *cachedTxStore) StoreTransaction(ctx context.Context, tx *config.Transaction, blockNumber uint64, txIndex int) error {
	if err := s.inner.StoreTransaction(ctx, tx, blockNumber, txIndex); err != nil {
		return err
	}
	if tx != nil {
		_ = s.c.Delete(ctx, Tx(tx.Hash.Hex()))
	}
	return nil
}

// GetTransaction returns the transaction for txHash. Cache hit returns immediately; miss fetches from inner.
// Time: O(1) cache hit; O(1) SQL PK lookup on miss.
// DS: cache.Cache — key tx:<hash>, value JSON-encoded TransactionRecord.
func (s *cachedTxStore) GetTransaction(ctx context.Context, txHash string) (*thebegateway.TransactionRecord, error) {
	key := Tx(txHash)
	if raw, err := s.c.Get(ctx, key); err == nil && len(raw) > 0 {
		var rec thebegateway.TransactionRecord
		if json.Unmarshal(raw, &rec) == nil {
			return &rec, nil
		}
	}
	rec, err := s.inner.GetTransaction(ctx, txHash)
	if err != nil {
		return nil, err
	}
	if raw, merr := json.Marshal(rec); merr == nil {
		_ = s.c.Set(ctx, key, raw, TTLTx)
	}
	return rec, nil
}

// GetTransactionsByBlock delegates directly — result set is unbounded per block.
// Time: O(n) — not cached.
func (s *cachedTxStore) GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]*thebegateway.TransactionRecord, error) {
	return s.inner.GetTransactionsByBlock(ctx, blockNumber)
}

// GetTransactionsPaginated delegates directly — not cached (paginated reads vary by offset).
func (s *cachedTxStore) GetTransactionsPaginated(ctx context.Context, limit, offset int) ([]*thebegateway.TransactionRecord, error) {
	return s.inner.GetTransactionsPaginated(ctx, limit, offset)
}

// CountTransactions delegates directly — not cached (count changes on every new block).
func (s *cachedTxStore) CountTransactions(ctx context.Context) (uint64, error) {
	return s.inner.CountTransactions(ctx)
}

// RefreshAccountTxStats delegates directly — targeted SQL UPDATE, not cached.
func (s *cachedTxStore) RefreshAccountTxStats(ctx context.Context, address string) error {
	return s.inner.RefreshAccountTxStats(ctx, address)
}

// GetTransactionsByAddress delegates directly — result set is bounded only by caller-supplied limit.
// Time: O(n) — not cached.
func (s *cachedTxStore) GetTransactionsByAddress(ctx context.Context, address string, limit int) ([]*thebegateway.TransactionRecord, error) {
	return s.inner.GetTransactionsByAddress(ctx, address, limit)
}

// GetTransactionsByAddressInRange delegates directly — range reads are not cached.
func (s *cachedTxStore) GetTransactionsByAddressInRange(ctx context.Context, address string, fromBlock, toBlock uint64) ([]*thebegateway.TransactionRecord, error) {
	return s.inner.GetTransactionsByAddressInRange(ctx, address, fromBlock, toBlock)
}

// SetTransactionStatus delegates to inner, then invalidates the tx hash cache key on success.
// Time: O(1) inner write; cache invalidation is best-effort.
func (s *cachedTxStore) SetTransactionStatus(ctx context.Context, txHash string, status int) error {
	if err := s.inner.SetTransactionStatus(ctx, txHash, status); err != nil {
		return err
	}
	_ = s.c.Delete(ctx, Tx(txHash))
	return nil
}

// WriteContractReceipt delegates to inner (gateway 2PC → SQL); invalidates the tx
// hash cache key so a subsequent receipt/tx read reflects the new status.
func (s *cachedTxStore) WriteContractReceipt(ctx context.Context, rec *thebegateway.ContractReceiptRecord) error {
	if err := s.inner.WriteContractReceipt(ctx, rec); err != nil {
		return err
	}
	_ = s.c.Delete(ctx, Tx(rec.TxHash))
	return nil
}

// SetTxProcessing delegates directly — KV-backed, no cache layer.
func (s *cachedTxStore) SetTxProcessing(ctx context.Context, txHash string) error {
	return s.inner.SetTxProcessing(ctx, txHash)
}

// ClearTxProcessing delegates directly — KV-backed, no cache layer.
func (s *cachedTxStore) ClearTxProcessing(ctx context.Context, txHash string) error {
	return s.inner.ClearTxProcessing(ctx, txHash)
}

// IsTxProcessing delegates directly — KV-backed, no cache layer.
func (s *cachedTxStore) IsTxProcessing(ctx context.Context, txHash string) (bool, error) {
	return s.inner.IsTxProcessing(ctx, txHash)
}
