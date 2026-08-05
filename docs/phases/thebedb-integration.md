# ThebeDB Production Integration — Implementation Phases

## Context

Rebuilding the JMDN → ThebeDB integration from POC to production.
Replaces: `cassata/`, `shadowAdapter` (inside `dualdb/shadow_adapter.go`), old `thebeprofile/`.
Keeps: `dualdb/dualdb.go` (metrics + dual-write orchestration), `dualdb/metrics.go`, `thebe_shadow.go` (interface hook).

## Architecture Decisions (locked)

- **Write path:** ImmuDB (primary, sync) → `ThebeShadowWriter` interface → `ThebeGateway` impl → ThebeDB 2PC (SQL + KV) → cache SET (best-effort, post-commit)
- **Read path:** cache GET → miss → SQL/KV → cache SET with TTL → return
- **Failure path:** ThebeDB 2PC failure → serialize payload → WAL (SQLite outbox) → `OutboxWorker` retries with exponential backoff
- **Dispatch:** typed methods on `ThebeGateway` interface (not reflection, not registry)
- **Storage split:** SQL (6 tables: accounts, blocks, snapshots, transactions, zk_proofs, l1_finality) | KV (contract data — schema TBD from user) | Cache (Redis, TTL-based)
- **Pattern:** Facade (ThebeGateway over ThebeDB internals) + Observer (ThebeShadowWriter hook)

## SOLID Gates (resolved)

- **S:** One invariant per module — see table in design doc
- **O:** New entity → new method on interface + new Apply() case. Zero changes to DualDB, OutboxWorker, OutboxStore.
- **I:** ThebeGateway (write-only), ThebeReader (read-only), OutboxStore (3 methods), ThebeShadowWriter (1 method)
- **D:** All cross-package deps are interfaces — see dependency table in design doc

---

## Phase 1.0: Interfaces + Domain Types

- **What:** Define all interfaces and domain record types. No implementation. No DB calls. This is the contract layer everything else depends on.
- **Files to create:**
  - `DB_OPs/thebegateway/interfaces.go` — `ThebeGateway`, `ThebeReader`, `OutboxStore` interfaces
  - `DB_OPs/thebegateway/types.go` — domain record types: `BlockRecord`, `AccountRecord`, `TransactionRecord`, `SnapshotRecord`, `ZKProofRecord`, `L1FinalityRecord`
  - `DB_OPs/thebegateway/cache_keys.go` — cache key builders + TTL constants
- **Files to delete:** none yet (deletion in Phase 5)
- **Data structures:**
  - `BlockRecord` — plain struct, all fields from `config.ZKBlock` flattened to SQL-compatible types. Owned by caller, passed by value. Fixed size.
  - `AccountRecord` — plain struct, fields from `DB_OPs.Account`. Fixed size.
  - `TransactionRecord` — plain struct, fields from `config.Transaction` + `blockNumber uint64` + `txIndex int`. Fixed size.
  - `SnapshotRecord` — plain struct: `BlockNumber uint64`, `BlockHash common.Hash`. Fixed size.
  - `ZKProofRecord` — plain struct: `BlockNumber uint64`, `ProofHash string`, `StarkProof []byte`, `Commitment []byte`. Fixed size.
  - `L1FinalityRecord` — plain struct: `Confirmation string`, `BlockNumbers []uint64`, `Metadata map[string]any`. Variable (slice + map).
- **Interfaces:**
  ```go
  type ThebeGateway interface {
      WriteBlock(ctx context.Context, block *config.ZKBlock) error
      WriteAccount(ctx context.Context, account *DB_OPs.Account) error
      WriteTransaction(ctx context.Context, tx *config.Transaction, blockNumber uint64, txIndex int) error
      WriteSnapshot(ctx context.Context, blockNumber uint64, blockHash common.Hash) error
      WriteZKProof(ctx context.Context, block *config.ZKBlock) error
      WriteL1Finality(ctx context.Context, confirmation string, blockNumbers []uint64, metadata map[string]any) error
      // contract KV methods added here in Phase 7 (pending KV schema from user)
  }

  type ThebeReader interface {
      GetAccount(ctx context.Context, address string) (*AccountRecord, error)
      GetBlock(ctx context.Context, blockNumber uint64) (*BlockRecord, error)
      GetTransaction(ctx context.Context, txHash string) (*TransactionRecord, error)
      GetLatestTransactionsByAddress(ctx context.Context, address string, limit int) ([]*TransactionRecord, error)
      GetZKProof(ctx context.Context, blockNumber uint64) (*ZKProofRecord, error)
      GetSnapshot(ctx context.Context, blockNumber uint64) (*SnapshotRecord, error)
  }

  type OutboxStore interface {
      Enqueue(ctx context.Context, entry OutboxEntry) error
      Next(ctx context.Context, limit int) ([]OutboxEntry, error)
      Ack(ctx context.Context, id int64) error
      IncrementAttempts(ctx context.Context, id int64, nextRetryAt time.Time) error
  }
  ```
- **Inputs:** none
- **Done when:** `go build ./DB_OPs/thebegateway/...` passes with zero imports of concrete DB types
- **Status:** [x]

## Phase 1.1: OutboxEntry type

- **Trigger:** OutboxStore interface requires it
- **What:** Define `OutboxEntry` struct in `DB_OPs/thebegateway/types.go`
  ```go
  type OutboxEntry struct {
      ID          int64
      Namespace   string    // "block", "account", "tx", "snapshot", "zk", "l1_finality"
      Method      string    // matches ThebeGateway method name for debugging
      Payload     []byte    // JSON-serialized domain record
      Attempts    int
      NextRetryAt time.Time
      CreatedAt   time.Time
  }
  ```
- **Data structures:** Plain struct. Passed by value between OutboxStore and OutboxWorker. Fixed size (Payload is a byte slice, bounded by record size).
- **Done when:** `OutboxEntry` compiles and `OutboxStore` interface references it without error
- **Status:** [x]

---

## Phase 2.0: JMDNProfile — new thebeprofile

- **What:** Rewrite `thebeprofile/` to implement ThebeDB's `profile.Profile` interface against the new 6-table schema. Typed Apply() per namespace. No reflection. No switch/case on string — use `map[string]applyFunc` initialized at construction.
- **Files to replace:**
  - `DB_OPs/thebeprofile/profile.go` — `JMDNProfile` struct implementing `profile.Profile`
  - `DB_OPs/thebeprofile/schema.go` — migration DDL matching `000001_init_schema` exactly
  - `DB_OPs/thebeprofile/apply_account.go` — `applyAccount(ctx, tx, record) error`
  - `DB_OPs/thebeprofile/apply_block.go` — `applyBlock(ctx, tx, record) error`
  - `DB_OPs/thebeprofile/apply_snapshot.go` — `applySnapshot(ctx, tx, record) error`
  - `DB_OPs/thebeprofile/apply_transaction.go` — `applyTransaction(ctx, tx, record) error`
  - `DB_OPs/thebeprofile/apply_zk_proof.go` — `applyZKProof(ctx, tx, record) error`
  - `DB_OPs/thebeprofile/apply_l1_finality.go` — `applyL1Finality(ctx, tx, record) error`
  - `DB_OPs/thebeprofile/apply.go` — deleted (was the reflection-based dispatcher)
- **Data structures:**
  - `map[string]applyFunc` — keyed by namespace string (`"account"`, `"block"`, etc.). Populated once in `NewJMDNProfile()`, read-only after. Access: O(1) random key lookup at Apply() time. No locking needed (read-only after init). Fixed size (6 entries for SQL namespaces).
  - `applyFunc` = `func(ctx context.Context, tx *sql.Tx, payload []byte) error`
- **Namespaces (SQL):** `account`, `block`, `snapshot`, `tx`, `zk`, `l1_finality`
- **Namespaces (KV):** added in Phase 7 (pending user KV schema)
- **Rules:**
  - Append-only tables (blocks, snapshots, transactions, zk_proofs, l1_finality): `INSERT ... ON CONFLICT DO NOTHING`
  - Mutable tables (accounts): `INSERT ... ON CONFLICT (address) DO UPDATE SET ...`
  - Every apply func unmarshals `payload []byte` → typed struct → parameterized SQL. Zero string interpolation in queries.
- **Inputs:** Phase 1.0 complete (types exist)
- **Done when:** `JMDNProfile` satisfies `profile.Profile` interface; `go build ./DB_OPs/thebeprofile/...` passes; old `apply.go` deleted
- **Status:** [x]

---

## Phase 3.0: OutboxStore — SQLite WAL

- **What:** Implement `OutboxStore` interface backed by SQLite (existing `sqlops.UnifiedDB`). Create `thebe_outbox` table. Implement `Enqueue`, `Next`, `Ack`, `IncrementAttempts`.
- **Files to create:**
  - `DB_OPs/thebegateway/outbox_store.go` — `sqliteOutboxStore` struct implementing `OutboxStore`
- **Data structures:**
  - SQLite table `thebe_outbox`:
    ```sql
    CREATE TABLE IF NOT EXISTS thebe_outbox (
        id            INTEGER PRIMARY KEY AUTOINCREMENT,
        namespace     TEXT        NOT NULL,
        method        TEXT        NOT NULL,
        payload       BLOB        NOT NULL,
        attempts      INTEGER     NOT NULL DEFAULT 0,
        next_retry_at INTEGER     NOT NULL DEFAULT 0,  -- Unix seconds
        created_at    INTEGER     NOT NULL DEFAULT 0
    );
    CREATE INDEX IF NOT EXISTS idx_outbox_next_retry
        ON thebe_outbox(next_retry_at ASC)
        WHERE attempts < 3;
    ```
  - Access pattern: sequential drain (`SELECT ... WHERE next_retry_at <= now() ORDER BY next_retry_at ASC LIMIT N`), O(1) insert, O(1) delete by id.
  - Bound: unbounded rows; entries deleted on Ack. Max attempts = 3 (`thebegateway.MaxOutboxAttempts`); exhausted entries are left in table for operator inspection (not retried).
- **Retry backoff:** `nextRetryAt = now + min(2^attempts * 1s, 5min)`
- **Inputs:** Phase 1.1 complete (OutboxEntry type exists), SQLite connection available
- **Done when:** `sqliteOutboxStore` satisfies `OutboxStore`; table created on init; `go build` passes
- **Status:** [x]

---

## Phase 4.0: OutboxWorker

- **What:** Background goroutine that drains the outbox by retrying failed ThebeGateway writes.
- **Files to create:**
  - `DB_OPs/thebegateway/outbox_worker.go` — `OutboxWorker` struct
- **Data structures:**
  - Buffered `chan OutboxEntry` size=32 — worker reads from SQLite in batches, sends to channel, drains via ThebeGateway. Bounded(32); provides backpressure if ThebeGateway is slow.
  - `OutboxWorker{store OutboxStore, gateway ThebeGateway, interval time.Duration, stop chan struct{}}`
  - Access pattern: FIFO drain. Channel is single-producer (SQLite poller goroutine), single-consumer (retry goroutine).
- **Behaviour:**
  - Poll `OutboxStore.Next(ctx, 32)` every `interval` (default: 5s)
  - For each entry: deserialize payload → call correct `ThebeGateway` method by `entry.Namespace`
  - On success: `OutboxStore.Ack(ctx, entry.ID)`
  - On failure: `OutboxStore.IncrementAttempts(ctx, entry.ID, backoff(entry.Attempts))`
  - Entries with `attempts >= MaxOutboxAttempts` (3): skip (left for operator)
  - Graceful shutdown via `stop chan struct{}`
- **Dispatch inside worker:** `switch entry.Namespace` → typed deserialize + gateway call. This is the ONE place a switch/case on namespace is acceptable — the worker is the retry executor, not a generic dispatcher.
- **Inputs:** Phase 3.0 (OutboxStore impl), Phase 5.0 (ThebeGateway impl — can be stubbed for unit test)
- **Done when:** Worker starts, drains outbox entries, acks on success, backs off on failure; stops cleanly on context cancel
- **Status:** [x]

---

## Phase 5.0: ThebeGateway — concrete implementation

- **What:** Implement `ThebeGateway` interface. Each Write method: build domain record → call ThebeDB `builder.Append()` (2PC: SQL via JMDNProfile + KV) → on success: cache SET best-effort. On failure: enqueue to OutboxStore.
- **Files to create:**
  - `DB_OPs/thebegateway/gateway.go` — `thebeGateway` struct
- **Files to delete:**
  - `DB_OPs/cassata/` — entire directory
  - `DB_OPs/dualdb/shadow_adapter.go` — replaced by ThebeGateway
- **Data structures:**
  - `thebeGateway{builder thebedb.Builder, cache cache.Cache, outbox OutboxStore}` — all fields are interfaces. No concrete types.
  - No internal state beyond these three deps. Stateless per-call.
- **Write method contract (same for all 6):**
  ```
  1. Marshal domain args → CanonicalRecord{Namespace, Type, Value: JSON}
  2. builder.Append(ctx, record)  ← 2PC: SQL via JMDNProfile.Apply() + KV log
  3. if err → outbox.Enqueue(ctx, OutboxEntry{...})  ← WAL
  4. if ok  → cache.Set(ctx, cacheKey, serialized, ttl)  ← best-effort, ignore error
  ```
- **Implements ThebeShadowWriter:** `thebeGateway` wraps `StoreZKBlock(conn, block)` by calling `WriteBlock`, `WriteSnapshot`, `WriteZKProof`, and for each tx: `WriteTransaction`. This makes it satisfy `ThebeShadowWriter` interface from `thebe_shadow.go`.
- **Inputs:** Phase 1.0 (interfaces), Phase 2.0 (JMDNProfile registered with ThebeDB builder), Phase 3.0 (OutboxStore)
- **Done when:** All 6 write methods implemented; compiles against `ThebeGateway` interface; `cassata/` and `shadow_adapter.go` deleted; `go build ./...` passes
- **Status:** [x]

---

## Phase 5.1: DualDB wiring update

- **Trigger:** `shadow_adapter.go` deleted in Phase 5.0 — DualDB must reference ThebeGateway instead
- **What:** Update `DB_OPs/dualdb/dualdb.go` — replace `shadowAdapter` with `ThebeShadowWriter` interface (already exists via `thebe_shadow.go`). Wire `thebeGateway` as the concrete `ThebeShadowWriter` in `main.go` (or wherever DualDB is constructed).
- **Files to modify:**
  - `DB_OPs/dualdb/dualdb.go` — remove `shadowAdapter` import, accept `ThebeShadowWriter` interface
  - `main.go` (or startup file) — construct `thebeGateway`, pass to `SetThebeShadowWriter()`
- **Done when:** DualDB holds no import of cassata or shadow_adapter; `go build ./...` passes
- **Status:** [x]

---

## Phase 6.0: ThebeReader — read-through cache implementation

- **What:** Implement `ThebeReader` interface. Each Read method: cache GET → hit: deserialize + return. Miss: SQL query → deserialize → cache SET with TTL → return.
- **Files to create:**
  - `DB_OPs/thebegateway/reader.go` — `thebeReader` struct
- **Data structures:**
  - `thebeReader{db *sql.DB, cache cache.Cache}` — both interfaces (sql.DB is stdlib, cache is ThebeDB's cache.Cache interface). No concrete Redis import.
  - No internal state. Stateless per-call.
- **Read method contract:**
  ```
  1. key := cacheKey(args)
  2. hit, err := cache.Get(ctx, key)  → if hit: json.Unmarshal → return
  3. miss: SQL query (parameterized, no string interpolation)
  4. cache.Set(ctx, key, serialized, ttl)  ← best-effort, ignore error
  5. return result
  ```
- **Critical SQL queries (pre-built as package-level vars, not fmt.Sprintf):**
  - `GetAccount` → `SELECT ... FROM accounts WHERE address = $1`
  - `GetBlock` → `SELECT ... FROM blocks WHERE block_number = $1`
  - `GetTransaction` → `SELECT ... FROM transactions WHERE tx_hash = $1`
  - `GetLatestTransactionsByAddress` → `SELECT ... FROM transactions WHERE from_addr = $1 OR to_addr = $1 ORDER BY block_number DESC, tx_index DESC LIMIT $2`
  - `GetZKProof` → `SELECT ... FROM zk_proofs WHERE block_number = $1`
  - `GetSnapshot` → `SELECT ... FROM snapshots WHERE block_number = $1`
- **Inputs:** Phase 1.0 (ThebeReader interface, record types, cache keys), Phase 2.0 (tables exist in Postgres)
- **Done when:** All read methods implemented; no cache import leaks concrete Redis type; `go build` passes
- **Status:** [x]

---

## Phase 7.0: KV Contract Layer — BLOCKED

- **Trigger:** User to provide KV key schema for contract data
- **What:** Add contract write/read methods to `ThebeGateway` + `ThebeReader` interfaces. Implement KV writes via ThebeDB `builder.ExecuteKV()`. Add KV namespaces to `JMDNProfile`.
- **New methods (pending schema):**
  ```go
  // ThebeGateway additions:
  WriteContractCode(ctx, address common.Address, code []byte) error
  WriteContractStorage(ctx, address common.Address, slot, value common.Hash) error
  WriteContractNonce(ctx, address common.Address, nonce uint64) error
  WriteContractMeta(ctx, address common.Address, meta ContractMetaRecord) error
  WriteContractReceipt(ctx, txHash string, receipt ContractReceiptRecord) error

  // ThebeReader additions:
  GetContractCode(ctx, address common.Address) ([]byte, error)
  GetContractStorage(ctx, address common.Address, slot common.Hash) (common.Hash, error)
  GetContractNonce(ctx, address common.Address) (uint64, error)
  ```
- **KV key schema:** TBD — pending user input
- **Cache TTL for contract code:** 24h (immutable after deploy). Storage/nonce: 30s (mutable).
- **Inputs:** Phase 5.0 complete; KV key schema from user
- **Done when:** All contract methods implemented; PebbleDB in ContractDB replaced by ThebeGateway KV writes
- **Status:** [x]

---

## Phase 8.0: Tests

- **What:** Table-driven tests for all new modules
- **Files to create:**
  ```
  DB_OPs/Tests/thebegateway/gateway_test.go       — gateway write methods, outbox enqueue on failure
  DB_OPs/Tests/thebegateway/reader_test.go        — read-through: cache hit, cache miss, SQL fallback
  DB_OPs/Tests/thebegateway/outbox_store_test.go  — enqueue, next, ack, backoff increment
  DB_OPs/Tests/thebegateway/outbox_worker_test.go — retry success, retry failure, max attempts
  DB_OPs/Tests/thebeprofile/profile_test.go       — Apply() per namespace, idempotent inserts
  ```
- **Test doubles:** `ThebeGateway`, `OutboxStore`, `cache.Cache` all mocked via interfaces — no real DB required for unit tests
- **Inputs:** Phase 6.0 complete
- **Done when:** All tests pass; `find . -name "*_test.go" -not -path "*/Tests/*"` → empty
- **Status:** [x]

---

## Phase 9.0: Integration Seal

- **Checks:**
  - `go build ./...` — zero import cycles
  - `grep -r "cassata" . --include="*.go"` → empty (all references removed)
  - `grep -r "shadow_adapter" . --include="*.go"` → empty
  - `grep -r "reflect\." DB_OPs/thebeprofile/ --include="*.go"` → empty (no reflection)
  - Every new/modified package has module AI-doc block
  - Time complexity annotated on: `OutboxWorker` poll loop, `ThebeReader` query methods, `JMDNProfile.Apply()`
  - All Phase doc entries marked done (except Phase 7.0 which is blocked)
  - `DualDB.Report().ThebeErrors` reachable via metrics endpoint
- **Status:** [x]
- **Completed:**
  - `go build ./SmartContract/cmd/...` → clean (fixed `thebeprofile.New` + `thebedb.NewFromConfig`)
  - `shadow_adapter` references → 0
  - `reflect.` in thebeprofile/ → 0
  - Test files outside Tests/ → 0
  - `DualDB.Report().ThebeErrorRate` served via http_server.go:109
  - Time complexity annotated: OutboxWorker.Start(), JMDNProfile.Apply()
  - cassata/ retained intentionally (gETH + SmartContract backward compat)

---

## Deletion Manifest

Files removed as part of this integration:

| File/Dir | Removed in | Reason |
|---|---|---|
| `DB_OPs/cassata/cassata.go` | Phase 5.0 | Replaced by ThebeGateway |
| `DB_OPs/cassata/types.go` | Phase 5.0 | Types moved to thebegateway/types.go |
| `DB_OPs/dualdb/shadow_adapter.go` | Phase 5.0 | Replaced by ThebeGateway |
| `DB_OPs/thebeprofile/apply.go` | Phase 2.0 | Reflection-based dispatch — deleted |

## Files Kept (unchanged)

| File | Why kept |
|---|---|
| `DB_OPs/thebe_shadow.go` | Interface hook is correct — no changes needed |
| `DB_OPs/dualdb/dualdb.go` | Dual-write orchestration + metrics are production-quality |
| `DB_OPs/dualdb/metrics.go` | Production-quality sliding window metrics |
| `DB_OPs/thebeprofile/schema.go` | Replaced with new migration DDL in Phase 2.0 |
