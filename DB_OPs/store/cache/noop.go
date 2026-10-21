// MODULE: DB_OPs/store/cache/noop.go
// PURPOSE: Provide a no-op cache.Cache implementation for tests and no-cache mode.
//
// CORE DATA STRUCTURES:
//   - noopCache: zero-size struct. No allocation, no state, no locking needed.
//
// TO MODIFY BEHAVIOR:
//   - Do not add state here — this type must remain a transparent pass-through.
//
// DO NOT:
//   - Use this in production hot paths when a real cache is available.
//   - Add any logging or metrics here — callers own observability.
//
// EXTENSION POINT: swap for any cache.Cache implementation at construction time (NewCachedBlockStore, etc).

package cache

import (
	"context"
	"time"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"
)

// noopCache is a no-op implementation of cache.Cache for tests and no-cache mode.
// All operations succeed immediately without storing or retrieving anything.
type noopCache struct{}

// isNoop reports whether c can never return a hit (nil or the no-op cache).
// The New*Store constructors then return the inner store unwrapped: a
// read-through decorator over a cache that discards every Set would still
// json.Marshal each record it reads — for a ZK proof that is ~2.66 MB of proof
// bytes base64-encoded and thrown away on every block read (14.5 GB in 204 min
// on the testnet sequencer, runtime bundle 2026-10-15).
func isNoop(c cache.Cache) bool {
	if c == nil {
		return true
	}
	_, ok := c.(*noopCache)
	return ok
}

// NewNoopCache returns a cache.Cache that never stores anything.
func NewNoopCache() cache.Cache { return &noopCache{} }

// Set is a no-op. Always returns nil.
func (n *noopCache) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}

// Get is a no-op. Always returns ErrMiss so callers fall through to the inner store.
func (n *noopCache) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, cache.ErrMiss
}

// Delete is a no-op. Always returns nil.
func (n *noopCache) Delete(_ context.Context, _ ...string) error {
	return nil
}

// Exists is a no-op. Always returns false, nil.
func (n *noopCache) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// Keys is a no-op. Always returns an empty slice.
func (n *noopCache) Keys(_ context.Context, _ string, _ int64) ([]string, error) {
	return nil, nil
}

// TTL is a no-op. Returns TTLAbsent to signal the key does not exist.
func (n *noopCache) TTL(_ context.Context, _ string) (time.Duration, error) {
	return cache.TTLAbsent, nil
}

// Close is a no-op. Always returns nil.
func (n *noopCache) Close() error {
	return nil
}

// Compile-time assertion: noopCache implements cache.Cache.
var _ cache.Cache = (*noopCache)(nil)
