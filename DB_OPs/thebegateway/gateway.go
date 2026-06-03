// MODULE: DB_OPs/thebegateway/gateway.go
// PURPOSE: Concrete ThebeGateway — translates JMDN domain write events into ThebeDB
//          2PC appends (KV log + SQL projection) followed by best-effort cache population.
//          On 2PC failure: payload enqueued to OutboxStore WAL for retry by OutboxWorker.
//
// CORE DATA STRUCTURES:
//   - thebeGateway: holds ThebeAppender + cache.Cache + OutboxStore (all interfaces).
//     Stateless per-call. Safe for concurrent use — all deps are interfaces.
//
// TO MODIFY BEHAVIOR:
//   - Add new entity: add Write<Entity> to ThebeGateway interface + implement here
//   - Swap cache backend: inject different cache.Cache — gateway unchanged
//   - Swap KV/SQL backend: inject different ThebeAppender — gateway unchanged
//
// DO NOT:
//   - Import gossipnode/DB_OPs (cycle — this package lives inside DB_OPs/)
//   - Import concrete ThebeDB types beyond pkg/core and pkg/cache
//   - Store per-request state on thebeGateway (stateless by design)
//
// EXTENSION POINT: new entity → new Write method on ThebeGateway interface + case in OutboxWorker.dispatch()
//
// CHANGE SCENARIOS:
//   Add contract writes (Phase 7): add WriteContractCode etc. — follow exact same write() helper pattern
//   Disable cache: inject a no-op cache.Cache implementation — gateway code unchanged

package thebegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"
	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
)

// ThebeAppender is the minimal ThebeDB surface required by thebeGateway.
// *builder.Builder satisfies this interface.
// Time: O(1) amortized — single KV append + SQL projection via 2PC
type ThebeAppender interface {
	Append(ctx context.Context, record *core.CanonicalRecord) (uint64, error)
}

// Compile-time interface check.
var _ ThebeGateway = (*thebeGateway)(nil)

type thebeGateway struct {
	appender ThebeAppender
	cache    cache.Cache
	outbox   OutboxStore
}

// NewThebeGateway constructs a ThebeGateway. All deps are interfaces.
// appender: *builder.Builder satisfies ThebeAppender
// c: *cache.RedisCache or any cache.Cache implementation; may be nil (cache skipped)
// outbox: SQLite-backed OutboxStore from NewOutboxStore()
func NewThebeGateway(appender ThebeAppender, c cache.Cache, outbox OutboxStore) ThebeGateway {
	return &thebeGateway{appender: appender, cache: c, outbox: outbox}
}

// write is the shared write path for all ThebeGateway methods.
// 1. Marshal record to JSON.
// 2. Append to ThebeDB (2PC: KV log + SQL projection).
// 3. On failure: enqueue to outbox WAL.
// 4. On success: populate cache (best-effort, errors ignored).
// Time: O(1) amortized — single Append round trip + one cache SET
func (g *thebeGateway) write(
	ctx context.Context,
	ns Namespace,
	method string,
	record any,
	cacheKey string,
	cacheTTL time.Duration,
) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("thebeGateway.%s: marshal: %w", method, err)
	}

	_, err = g.appender.Append(ctx, &core.CanonicalRecord{
		Namespace: string(ns),
		Type:      method,
		Value:     payload,
		Timestamp: uint64(time.Now().UnixNano()),
	})
	if err != nil {
		if outboxErr := g.outbox.Enqueue(ctx, OutboxEntry{
			Namespace:   ns,
			Method:      method,
			Payload:     payload,
			NextRetryAt: ExponentialBackoff(0),
			CreatedAt:   time.Now(),
		}); outboxErr != nil {
			return fmt.Errorf("thebeGateway.%s: append failed and outbox enqueue failed: append=%w outbox=%v", method, err, outboxErr)
		}
		return fmt.Errorf("thebeGateway.%s: append: %w (enqueued to outbox)", method, err)
	}

	// Best-effort cache population — ignore errors.
	if g.cache != nil && cacheKey != "" {
		_ = g.cache.Set(ctx, cacheKey, payload, cacheTTL)
	}
	return nil
}

// WriteBlock appends a block record to ThebeDB and populates the cache.
// Time: O(1) amortized — delegates to write()
func (g *thebeGateway) WriteBlock(ctx context.Context, block *BlockRecord) error {
	return g.write(ctx, NamespaceBlock, "WriteBlock", block,
		BlockKey(block.BlockNumber), TTLBlock)
}

// WriteAccount appends an account record to ThebeDB and populates the cache.
// Time: O(1) amortized
func (g *thebeGateway) WriteAccount(ctx context.Context, account *AccountRecord) error {
	return g.write(ctx, NamespaceAccount, "WriteAccount", account,
		AccountKey(account.Address), TTLAccount)
}

// WriteTransaction appends a transaction record to ThebeDB and populates the cache.
// Time: O(1) amortized
func (g *thebeGateway) WriteTransaction(ctx context.Context, tx *TransactionRecord) error {
	return g.write(ctx, NamespaceTransaction, "WriteTransaction", tx,
		TransactionKey(tx.TxHash), TTLTransaction)
}

// WriteSnapshot appends a snapshot record to ThebeDB and populates the cache.
// Time: O(1) amortized
func (g *thebeGateway) WriteSnapshot(ctx context.Context, snapshot *SnapshotRecord) error {
	return g.write(ctx, NamespaceSnapshot, "WriteSnapshot", snapshot,
		SnapshotKey(snapshot.BlockNumber), TTLSnapshot)
}

// WriteZKProof appends a ZK proof record to ThebeDB and populates the cache.
// Time: O(1) amortized
func (g *thebeGateway) WriteZKProof(ctx context.Context, proof *ZKProofRecord) error {
	return g.write(ctx, NamespaceZKProof, "WriteZKProof", proof,
		ZKProofKey(proof.BlockNumber), TTLZKProof)
}

// WriteL1Finality appends an L1 finality record to ThebeDB and populates the cache.
// Time: O(1) amortized
func (g *thebeGateway) WriteL1Finality(ctx context.Context, finality *L1FinalityRecord) error {
	return g.write(ctx, NamespaceL1Finality, "WriteL1Finality", finality,
		L1FinalityKey(finality.Confirmation), TTLL1Finality)
}
