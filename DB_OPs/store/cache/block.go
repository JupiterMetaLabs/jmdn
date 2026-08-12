// MODULE: DB_OPs/store/cache/block.go
// PURPOSE: Add Redis cache layer to BlockStore — reads check cache first, writes invalidate cache after SQL commit.
//
// CORE DATA STRUCTURES:
//   - cachedBlockStore: fixed struct (inner BlockStore + cache.Cache). Stateless per-call.
//     Growth: fixed-size struct. No map, no slice, no unbounded state.
//
// TO MODIFY BEHAVIOR:
//   - Change TTLs: update keys.go constants (TTLBlock, TTLBlockLatest) — this file unchanged.
//   - Change serialization: swap json.Marshal/Unmarshal in read/write paths.
//   - Change which methods are cached: add/remove cache.Get + cache.Set calls per method.
//
// DO NOT:
//   - Call cache.Set before inner on writes — SQL/KV must commit first.
//   - Return error on cache miss or cache.Set failure — cache is best-effort.
//   - Import any concrete Redis or DB package here — depends on interfaces only.
//   - Store request-scoped state on the struct — stateless by design.
//
// EXTENSION POINT: implement store.BlockStore — swap inner without changing this file.

package cache

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// cachedBlockStore wraps a store.BlockStore with a cache read-through / write-invalidation layer.
type cachedBlockStore struct {
	inner store.BlockStore
	c     cache.Cache
}

// Compile-time assertion: cachedBlockStore implements store.BlockStore.
var _ store.BlockStore = (*cachedBlockStore)(nil)

// NewCachedBlockStore wraps inner with cache. Pass NewNoopCache() for no-cache mode.
func NewCachedBlockStore(inner store.BlockStore, c cache.Cache) store.BlockStore {
	return &cachedBlockStore{inner: inner, c: c}
}

// StoreBlock delegates to inner first, then invalidates cache on success.
// Time: O(1) inner write; cache invalidation is best-effort and does not affect the return value.
func (s *cachedBlockStore) StoreBlock(ctx context.Context, block *config.ZKBlock) error {
	if err := s.inner.StoreBlock(ctx, block); err != nil {
		return err
	}
	_ = s.c.Delete(ctx, Block(block.BlockNumber))
	_ = s.c.Delete(ctx, BlockLatest())
	return nil
}

// GetBlock returns the block for blockNumber. Cache hit returns immediately; miss fetches from inner.
// Time: O(1) cache hit; O(1) SQL PK lookup on miss.
// DS: cache.Cache — key block:<number>, value JSON-encoded BlockRecord.
func (s *cachedBlockStore) GetBlock(ctx context.Context, blockNumber uint64) (*thebegateway.BlockRecord, error) {
	key := Block(blockNumber)
	if raw, err := s.c.Get(ctx, key); err == nil && len(raw) > 0 {
		var rec thebegateway.BlockRecord
		if json.Unmarshal(raw, &rec) == nil {
			return &rec, nil
		}
	}
	rec, err := s.inner.GetBlock(ctx, blockNumber)
	if err != nil {
		return nil, err
	}
	if raw, merr := json.Marshal(rec); merr == nil {
		_ = s.c.Set(ctx, key, raw, TTLBlock)
	}
	return rec, nil
}

// GetBlockByHash returns the block for hash. Populates both hash and number keys on miss.
// Time: O(1) cache hit; O(1) SQL hash-index lookup on miss.
// DS: cache.Cache — key block:hash:<hash> + block:<number>, value JSON-encoded BlockRecord.
func (s *cachedBlockStore) GetBlockByHash(ctx context.Context, hash string) (*thebegateway.BlockRecord, error) {
	hashKey := BlockHash(hash)
	if raw, err := s.c.Get(ctx, hashKey); err == nil && len(raw) > 0 {
		var rec thebegateway.BlockRecord
		if json.Unmarshal(raw, &rec) == nil {
			return &rec, nil
		}
	}
	rec, err := s.inner.GetBlockByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if raw, merr := json.Marshal(rec); merr == nil {
		_ = s.c.Set(ctx, hashKey, raw, TTLBlock)
		_ = s.c.Set(ctx, Block(rec.BlockNumber), raw, TTLBlock)
	}
	return rec, nil
}

// GetLatestBlockNumber returns the latest block number. Short TTL (2s) for hot-path freshness.
// Time: O(1) cache hit; O(1) SQL max() on miss.
// DS: cache.Cache — key block:latest, value ASCII decimal uint64 string.
func (s *cachedBlockStore) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	key := BlockLatest()
	if raw, err := s.c.Get(ctx, key); err == nil && len(raw) > 0 {
		if n, perr := strconv.ParseUint(string(raw), 10, 64); perr == nil {
			return n, nil
		}
	}
	n, err := s.inner.GetLatestBlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	_ = s.c.Set(ctx, key, []byte(strconv.FormatUint(n, 10)), TTLBlockLatest)
	return n, nil
}

// BulkGetBlocks delegates directly — range queries have unbounded key sets, not cached.
// Time: O(n) range scan — not cached; range queries have unbounded key sets.
func (s *cachedBlockStore) BulkGetBlocks(ctx context.Context, from, to uint64) ([]*thebegateway.BlockRecord, error) {
	return s.inner.BulkGetBlocks(ctx, from, to)
}

// StoreL1Finality delegates directly — L1 finality writes are not cached.
func (s *cachedBlockStore) StoreL1Finality(ctx context.Context, rec *thebegateway.L1FinalityRecord) error {
	return s.inner.StoreL1Finality(ctx, rec)
}

// GetL1FinalityForBlock delegates directly — reads are infrequent (facade queries).
func (s *cachedBlockStore) GetL1FinalityForBlock(ctx context.Context, blockNumber uint64) (*thebegateway.L1FinalityRecord, error) {
	return s.inner.GetL1FinalityForBlock(ctx, blockNumber)
}

// GetBlocksByRewardAddress delegates directly — historical range reads are not cached.
func (s *cachedBlockStore) GetBlocksByRewardAddress(ctx context.Context, address string, fromBlock, toBlock uint64) ([]*thebegateway.BlockRecord, error) {
	return s.inner.GetBlocksByRewardAddress(ctx, address, fromBlock, toBlock)
}

// PutSyncKV / GetSyncKV delegate directly — sync-state keys are not cached.
func (s *cachedBlockStore) PutSyncKV(key string, value []byte) error {
	return s.inner.PutSyncKV(key, value)
}

func (s *cachedBlockStore) GetSyncKV(key string) ([]byte, error) {
	return s.inner.GetSyncKV(key)
}
