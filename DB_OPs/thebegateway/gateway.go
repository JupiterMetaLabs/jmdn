// MODULE: DB_OPs/thebegateway/gateway.go
// PURPOSE: Concrete ThebeGateway — translates JMDN domain write events into ThebeDB
//          2PC appends (KV log + SQL projection) followed by best-effort cache population.
//          On 2PC failure: payload enqueued to OutboxStore WAL for retry by OutboxWorker.
//          Phase 7: contract KV writes (code/nonce/storage/meta) go direct to BadgerDB via
//          ThebeKVStore; contract_receipt goes via 2PC append (SQL projection).
//
// CORE DATA STRUCTURES:
//   - thebeGateway: holds ThebeAppender + ThebeKVStore + cache.Cache + OutboxStore (all interfaces).
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
//   Phase 7 contract writes: DONE — WriteContractCode/Nonce/Storage/Meta use KV directly; WriteContractReceipt uses 2PC
//   Disable cache: inject a no-op cache.Cache implementation — gateway code unchanged

package thebegateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
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
	kv       ThebeKVStore // direct KV writes for contract data
	cache    cache.Cache
	outbox   OutboxStore
}

// NewThebeGateway constructs a ThebeGateway. All deps are interfaces.
// appender: *builder.Builder satisfies ThebeAppender
// kv: kv.Store from ThebeDB satisfies ThebeKVStore; may be nil (contract KV writes will error)
// c: *cache.RedisCache or any cache.Cache implementation; may be nil (cache skipped)
// outbox: SQLite-backed OutboxStore from NewOutboxStore()
func NewThebeGateway(appender ThebeAppender, kv ThebeKVStore, c cache.Cache, outbox OutboxStore) ThebeGateway {
	return &thebeGateway{appender: appender, kv: kv, cache: c, outbox: outbox}
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

// writeKV is the shared write path for immutable KV entries (PutWorm).
// Marshals record to JSON, calls kv.PutWorm. No outbox — KV writes are local BadgerDB.
// Time: O(1) — single BadgerDB write
func (g *thebeGateway) writeKV(key []byte, record any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("thebeGateway KV marshal: %w", err)
	}
	return g.kv.PutWorm(key, data)
}

// writeMutableKV is the shared write path for mutable KV entries (PutDerived).
// Time: O(1) — single BadgerDB write
func (g *thebeGateway) writeMutableKV(key []byte, record any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("thebeGateway KV marshal: %w", err)
	}
	return g.kv.PutDerived(key, data)
}

// hexDecode strips the optional 0x prefix and decodes hex.
func hexDecode(s string) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	return hex.DecodeString(s)
}

// hexToBytes20 decodes a 0x-prefixed hex string to exactly 20 bytes.
func hexToBytes20(h string) ([]byte, error) {
	b, err := hexDecode(h)
	if err != nil {
		return nil, err
	}
	if len(b) != 20 {
		return nil, fmt.Errorf("expected 20 bytes, got %d", len(b))
	}
	return b, nil
}

// hexToBytes32 decodes a 0x-prefixed hex string to exactly 32 bytes.
func hexToBytes32(h string) ([]byte, error) {
	b, err := hexDecode(h)
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	return b, nil
}

// WriteContractCode writes contract bytecode to KV (PutWorm — immutable after deploy).
// Time: O(1) — PutWorm (immutable after deploy)
func (g *thebeGateway) WriteContractCode(_ context.Context, rec *ContractCodeRecord) error {
	return g.writeKV(kvCodeKey(rec.Address), rec)
}

// WriteContractNonce writes contract nonce to KV as raw big-endian uint64 (PutDerived — mutable).
// Time: O(1) — PutDerived (mutable)
func (g *thebeGateway) WriteContractNonce(_ context.Context, rec *ContractNonceRecord) error {
	return g.kv.PutDerived(kvNonceKey(rec.Address), encodeUint64(rec.Nonce))
}

// WriteContractStorage writes a contract storage slot to KV (PutDerived — mutable, changes on every SSTORE).
// Time: O(1) — PutDerived (mutable, storage changes on every SSTORE)
func (g *thebeGateway) WriteContractStorage(_ context.Context, rec *ContractStorageRecord) error {
	addrBytes, err := hexToBytes20(rec.Address)
	if err != nil {
		return fmt.Errorf("WriteContractStorage: invalid address: %w", err)
	}
	slotBytes, err := hexToBytes32(rec.Slot)
	if err != nil {
		return fmt.Errorf("WriteContractStorage: invalid slot: %w", err)
	}
	return g.writeMutableKV(kvStorageKey(addrBytes, slotBytes), rec)
}

// WriteContractMeta writes contract deployment metadata to KV (PutWorm — immutable after deploy).
// Time: O(1) — PutWorm (immutable after deploy)
func (g *thebeGateway) WriteContractMeta(_ context.Context, rec *ContractMetaRecord) error {
	return g.writeKV(kvMetaKey(rec.Address), rec)
}

// WriteContractReceipt appends a contract receipt to ThebeDB via 2PC → SQL contract_receipts table.
// Time: O(1) amortized — appends CanonicalRecord → ThebeDB 2PC → SQL contract_receipts
func (g *thebeGateway) WriteContractReceipt(ctx context.Context, rec *ContractReceiptRecord) error {
	return g.write(ctx, NamespaceContractReceipt, "WriteContractReceipt", rec,
		TransactionKey(rec.TxHash), TTLTransaction)
}

// SetTxProcessing writes a "-1" sentinel to KV marking txHash as in-flight.
// Time: O(1) — PutDerived (mutable, will be cleared on confirmation/drop)
func (g *thebeGateway) SetTxProcessing(_ context.Context, txHash string) error {
	if err := g.kv.PutDerived(kvTxProcessingKey(txHash), kvTxProcessingValue); err != nil {
		return fmt.Errorf("SetTxProcessing(%s): %w", txHash, err)
	}
	return nil
}

// ClearTxProcessing removes the in-flight flag for txHash by writing an empty tombstone.
// Time: O(1) — PutDerived (overwrites sentinel with empty value)
func (g *thebeGateway) ClearTxProcessing(_ context.Context, txHash string) error {
	if err := g.kv.PutDerived(kvTxProcessingKey(txHash), []byte{}); err != nil {
		return fmt.Errorf("ClearTxProcessing(%s): %w", txHash, err)
	}
	return nil
}

// PutSyncKV writes a sync-state key (marker / anchor / tip) with overwrite
// semantics into BadgerDB. The "sync-state:" prefix keeps these outside the
// contract and tx-processing namespaces.
func (g *thebeGateway) PutSyncKV(key string, value []byte) error {
	if err := g.kv.PutDerived([]byte("sync-state:"+key), value); err != nil {
		return fmt.Errorf("PutSyncKV(%s): %w", key, err)
	}
	return nil
}

// GetSyncKV reads a sync-state key. Absent keys return (nil, nil).
func (g *thebeGateway) GetSyncKV(key string) ([]byte, error) {
	v, err := g.kv.Get([]byte("sync-state:" + key))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetSyncKV(%s): %w", key, err)
	}
	return v, nil
}
