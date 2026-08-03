// MODULE: DB_OPs/store/cache/zkproof.go
// PURPOSE: Add Redis cache layer to ZKProofStore — reads check cache first, writes invalidate cache after SQL commit.
//
// CORE DATA STRUCTURES:
//   - cachedZKProofStore: fixed struct (inner ZKProofStore + cache.Cache). Stateless per-call.
//     Growth: fixed-size struct. No map, no slice, no unbounded state.
//     ZK proofs are immutable after finality — long TTL (10min) is safe.
//
// TO MODIFY BEHAVIOR:
//   - Change TTL: update keys.go TTLZKProof constant — this file unchanged.
//
// DO NOT:
//   - Call cache.Set before inner on writes — SQL/KV must commit first.
//   - Return error on cache miss or cache.Set failure — cache is best-effort.
//   - Import any concrete Redis or DB package here — depends on interfaces only.
//   - Store request-scoped state on the struct — stateless by design.
//
// EXTENSION POINT: implement store.ZKProofStore — swap inner without changing this file.

package cache

import (
	"context"
	"encoding/json"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// cachedZKProofStore wraps a store.ZKProofStore with a cache read-through / write-invalidation layer.
type cachedZKProofStore struct {
	inner store.ZKProofStore
	c     cache.Cache
}

// Compile-time assertion: cachedZKProofStore implements store.ZKProofStore.
var _ store.ZKProofStore = (*cachedZKProofStore)(nil)

// NewCachedZKProofStore wraps inner with cache. Pass NewNoopCache() for no-cache mode.
func NewCachedZKProofStore(inner store.ZKProofStore, c cache.Cache) store.ZKProofStore {
	return &cachedZKProofStore{inner: inner, c: c}
}

// StoreZKBlock delegates to inner, then invalidates the block number cache key on success.
// Time: O(1) inner write; cache invalidation is best-effort.
func (s *cachedZKProofStore) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	if err := s.inner.StoreZKBlock(ctx, block); err != nil {
		return err
	}
	_ = s.c.Delete(ctx, ZKProof(block.BlockNumber))
	return nil
}

// GetZKProof returns the ZK proof for blockNumber. Cache hit returns immediately; miss fetches from inner.
// Time: O(1) cache hit; O(1) SQL PK lookup on miss.
// DS: cache.Cache — key zk:<blockNumber>, value JSON-encoded ZKProofRecord.
// ZK proofs are immutable after finality — TTLZKProof (10min) is safe.
func (s *cachedZKProofStore) GetZKProof(ctx context.Context, blockNumber uint64) (*thebegateway.ZKProofRecord, error) {
	key := ZKProof(blockNumber)
	if raw, err := s.c.Get(ctx, key); err == nil && len(raw) > 0 {
		var rec thebegateway.ZKProofRecord
		if json.Unmarshal(raw, &rec) == nil {
			return &rec, nil
		}
	}
	rec, err := s.inner.GetZKProof(ctx, blockNumber)
	if err != nil {
		return nil, err
	}
	if raw, merr := json.Marshal(rec); merr == nil {
		_ = s.c.Set(ctx, key, raw, TTLZKProof)
	}
	return rec, nil
}
