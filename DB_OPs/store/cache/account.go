// MODULE: DB_OPs/store/cache/account.go
// PURPOSE: Add Redis cache layer to AccountStore — reads check cache first, writes invalidate cache after SQL commit.
//
// CORE DATA STRUCTURES:
//   - cachedAccountStore: fixed struct (inner AccountStore + cache.Cache). Stateless per-call.
//     Growth: fixed-size struct. No map, no slice, no unbounded state.
//
// TO MODIFY BEHAVIOR:
//   - Change TTL: update keys.go TTLAccount constant — this file unchanged.
//   - Add a new cached method: follow get/set pattern with the Account(addr) key.
//   - Nonce methods (CheckNonceDuplicate, GetLatestNonce): intentionally not cached — see method comments.
//
// DO NOT:
//   - Cache nonce operations — nonce freshness is a safety invariant. Stale nonce enables replay attacks.
//   - Call cache.Set before inner on writes — SQL/KV must commit first.
//   - Return error on cache miss or cache.Set failure — cache is best-effort.
//   - Import any concrete Redis or DB package here — depends on interfaces only.
//   - Store request-scoped state on the struct — stateless by design.
//
// EXTENSION POINT: implement store.AccountStore — swap inner without changing this file.

package cache

import (
	"context"
	"encoding/json"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"

	"gossipnode/DB_OPs/store"
)

// cachedAccountStore wraps a store.AccountStore with a cache read-through / write-invalidation layer.
type cachedAccountStore struct {
	inner store.AccountStore
	c     cache.Cache
}

// Compile-time assertion: cachedAccountStore implements store.AccountStore.
var _ store.AccountStore = (*cachedAccountStore)(nil)

// NewCachedAccountStore wraps inner with cache. Pass NewNoopCache() for no-cache mode.
func NewCachedAccountStore(inner store.AccountStore, c cache.Cache) store.AccountStore {
	return &cachedAccountStore{inner: inner, c: c}
}

// CreateAccount delegates to inner, then invalidates the address cache key on success.
// Time: O(1) inner write; cache invalidation is best-effort.
func (s *cachedAccountStore) CreateAccount(ctx context.Context, account *store.Account) error {
	if err := s.inner.CreateAccount(ctx, account); err != nil {
		return err
	}
	_ = s.c.Delete(ctx, Account(account.Address.Hex()))
	return nil
}

// UpdateAccountBalance delegates to inner, then invalidates the address cache key on success.
// Balance changed — must not serve stale balance from cache.
// Time: O(1) inner write; cache invalidation is best-effort.
func (s *cachedAccountStore) UpdateAccountBalance(ctx context.Context, address, balance string) error {
	if err := s.inner.UpdateAccountBalance(ctx, address, balance); err != nil {
		return err
	}
	_ = s.c.Delete(ctx, Account(address))
	return nil
}

// GetAccount returns the account for address. Cache hit returns immediately; miss fetches from inner.
// Time: O(1) cache hit; O(1) SQL PK lookup on miss.
// DS: cache.Cache — key account:<address>, value JSON-encoded store.Account.
func (s *cachedAccountStore) GetAccount(ctx context.Context, address string) (*store.Account, error) {
	key := Account(address)
	if raw, err := s.c.Get(ctx, key); err == nil && len(raw) > 0 {
		var acc store.Account
		if json.Unmarshal(raw, &acc) == nil {
			return &acc, nil
		}
	}
	acc, err := s.inner.GetAccount(ctx, address)
	if err != nil {
		return nil, err
	}
	if raw, merr := json.Marshal(acc); merr == nil {
		_ = s.c.Set(ctx, key, raw, TTLAccount)
	}
	return acc, nil
}

// GetAccountByDID returns the account for did. Cache hit returns immediately; miss fetches from inner.
// Time: O(1) cache hit; O(1) SQL DID-index lookup on miss.
// DS: cache.Cache — key account:did:<did>, value JSON-encoded store.Account.
func (s *cachedAccountStore) GetAccountByDID(ctx context.Context, did string) (*store.Account, error) {
	key := AccountDID(did)
	if raw, err := s.c.Get(ctx, key); err == nil && len(raw) > 0 {
		var acc store.Account
		if json.Unmarshal(raw, &acc) == nil {
			return &acc, nil
		}
	}
	acc, err := s.inner.GetAccountByDID(ctx, did)
	if err != nil {
		return nil, err
	}
	if raw, merr := json.Marshal(acc); merr == nil {
		_ = s.c.Set(ctx, key, raw, TTLAccount)
	}
	return acc, nil
}

// CheckNonceDuplicate delegates directly — nonce freshness is a safety invariant.
// No cache — stale nonce data enables replay attacks.
func (s *cachedAccountStore) CheckNonceDuplicate(ctx context.Context, address string, nonce uint64) (bool, error) {
	return s.inner.CheckNonceDuplicate(ctx, address, nonce)
}

// GetLatestNonce delegates directly — nonce must be fresh to prevent replay attacks.
// No cache — nonce must be fresh to prevent replay attacks.
func (s *cachedAccountStore) GetLatestNonce(ctx context.Context, address string) (uint64, error) {
	return s.inner.GetLatestNonce(ctx, address)
}

// BulkGetAccounts delegates directly — bulk reads bypass cache.
// Time: O(n) — not cached; bulk reads bypass cache.
func (s *cachedAccountStore) BulkGetAccounts(ctx context.Context, addresses []string) ([]*store.Account, error) {
	return s.inner.BulkGetAccounts(ctx, addresses)
}

// ListAccounts delegates directly — list results are unbounded; not cached.
// Time: O(n) — not cached.
func (s *cachedAccountStore) ListAccounts(ctx context.Context, limit int) ([]*store.Account, error) {
	return s.inner.ListAccounts(ctx, limit)
}

func (s *cachedAccountStore) ListAccountsPaginated(ctx context.Context, limit, offset int) ([]*store.Account, error) {
	return s.inner.ListAccountsPaginated(ctx, limit, offset)
}

func (s *cachedAccountStore) CountAccounts(ctx context.Context) (uint64, error) {
	return s.inner.CountAccounts(ctx)
}

func (s *cachedAccountStore) GetAccountsByNonces(ctx context.Context, nonces []uint64) ([]*store.Account, error) {
	return s.inner.GetAccountsByNonces(ctx, nonces)
}
