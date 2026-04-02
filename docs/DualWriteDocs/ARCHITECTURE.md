# Architecture Deep-Dive — Dual-Write & ThebeDB Integration

> **Audience:** Engineers picking up this codebase for the first time.
> Covers the full call graph, package responsibilities, and design decisions.

---

## Table of Contents

1. [The Problem We're Solving](#the-problem-were-solving)
2. [Three-Layer Database Model](#three-layer-database-model)
3. [Package Map](#package-map)
4. [CoordinatorRepository Interface](#coordinatorrepository-interface)
5. [MasterRepository — Write Flow](#masterrepository--write-flow)
6. [MasterRepository — Read Flow](#masterrepository--read-flow)
7. [GlobalRepo — The Bridge Between Old and New Code](#globalrepo--the-bridge-between-old-and-new-code)
8. [ThebeDB Integration](#thebedb-integration)
9. [ImmuRepository — Adapter Pattern](#immurepository--adapter-pattern)
10. [Async Secondary Writes and GRO](#async-secondary-writes-and-gro)
11. [Migration Subsystem](#migration-subsystem)
12. [Startup Sequence](#startup-sequence)
13. [The `*Immu` Suffix Pattern — Avoiding Recursion](#the-immu-suffix-pattern--avoiding-recursion)
14. [Tracing / Observability](#tracing--observability)

---

## The Problem We're Solving

The node currently stores all blockchain data exclusively in **ImmuDB** — a tamper-proof key-value store using cryptographic verification trees. While ImmuDB is excellent for audit-proof storage, it is:

- Not queryable via SQL (no `WHERE`, `JOIN`, `ORDER BY`)
- Not designed for analytical reads at scale
- Not replaceable without a migration strategy

**ThebeDB** (PostgreSQL + embedded KV) is the target store. The goal is:

1. **Run both in parallel** (dual-write) — no data loss if either side fails.
2. **Reads stay on ImmuDB** until ThebeDB is fully caught up.
3. **Backfill** historical data from ImmuDB → ThebeDB.
4. **Eventually**, flip reads to ThebeDB and decommission ImmuDB.

This branch implements steps 1–3.

---

## Three-Layer Database Model

```
┌───────────────────────────────────────────────────────────┐
│                        Caller                             │
│   (DB_OPs.StoreZKBlock, messaging, block processing)     │
└──────────────────────────┬────────────────────────────────┘
                           │
                    DB_OPs.GlobalRepo
                           │
                           ▼
┌───────────────────────────────────────────────────────────┐
│                   MasterRepository                        │
│               (internal/repository/coordinator.go)        │
│                                                           │
│  WRITES:                           READS:                 │
│  ┌─────────────────┐               ImmuDB first           │
│  │ ImmuDB (sync)   │ ◄── primary   ThebeDB fallback       │
│  └─────────────────┘                                      │
│  ┌─────────────────┐                                      │
│  │ ThebeDB (async) │ ◄── secondary (fire-and-forget)      │
│  └─────────────────┘                                      │
└───────────────────────────────────────────────────────────┘
         │                          │
         ▼                          ▼
┌─────────────────┐       ┌──────────────────────┐
│  ImmuRepository │       │   ThebeRepository     │
│  (immu_repo/)   │       │   (thebe_repo/)       │
└────────┬────────┘       └──────────┬────────────┘
         │                           │
         ▼                           ▼
┌─────────────────┐       ┌──────────────────────┐
│    DB_OPs.*     │       │      ThebeDB          │
│  (ImmuDB gRPC)  │       │  ┌──────┐ ┌────────┐ │
└─────────────────┘       │  │  KV  │ │  SQL   │ │
                          │  │(Pbl) │ │ (PG)   │ │
                          │  └──────┘ └────────┘ │
                          └──────────────────────┘
```

---

## Package Map

| Package | Path | Responsibility |
|---------|------|---------------|
| `DB_OPs` | `DB_OPs/` | Legacy ImmuDB operations. Entry point for all writes (via GlobalRepo check). Contains `GlobalRepo`. |
| `repository` | `internal/repository/` | Coordinator, interfaces, migration subsystem, setup. |
| `immu_repo` | `internal/repository/immu_repo/` | Thin adapter: wraps `DB_OPs.*` to satisfy `CoordinatorRepository`. |
| `thebe_repo` | `internal/repository/thebe_repo/` | ThebeDB adapter: writes to KV + SQL atomically via `builder`. |
| `config/settings` | `config/settings/` | Unified Viper-backed config. `PostgresDSN` lives here. |
| `config` | `config/` | ImmuDB constants (address, port). `ZKBlock`, `Transaction`, `ImmuClient` types. |

---

## CoordinatorRepository Interface

Defined in `internal/repository/interfaces.go`. All three repos must implement it.

```go
type AccountRepository interface {
    StoreAccount(ctx, *Account) error
    GetAccount(ctx, address) (*Account, error)
    GetAccountByDID(ctx, did) (*Account, error)
    UpdateAccountBalance(ctx, address, newBalance) error
}

type BlockRepository interface {
    StoreZKBlock(ctx, *ZKBlock) error
    GetZKBlockByNumber(ctx, number) (*ZKBlock, error)
    GetZKBlockByHash(ctx, hash) (*ZKBlock, error)
    GetLatestBlockNumber(ctx) (uint64, error)
    GetLogs(ctx, FilterQuery) ([]Log, error)
}

type TransactionRepository interface {
    StoreTransaction(ctx, tx) error
    GetTransactionByHash(ctx, hash) (*Transaction, error)
}

// Combined:
type CoordinatorRepository interface {
    AccountRepository
    BlockRepository
    TransactionRepository
}
```

Both `ImmuRepository` and `ThebeRepository` satisfy `CoordinatorRepository`. `MasterRepository` also satisfies it (so it can be used wherever a `CoordinatorRepository` is needed).

---

## MasterRepository — Write Flow

```
caller calls DB_OPs.StoreZKBlock(conn, block)
    │
    ├─ GlobalRepo is set? YES
    │       │
    │       └─► MasterRepository.StoreZKBlock(ctx, block)
    │                   │
    │                   ├─ 1. m.Immu.StoreZKBlock(ctx, block)   ← SYNCHRONOUS
    │                   │        │
    │                   │        └─► ImmuRepository.StoreZKBlock
    │                   │                └─► DB_OPs.StoreZKBlockImmu(conn, block)   ← ImmuDB gRPC write
    │                   │
    │                   │   [ImmuDB succeeded]
    │                   │
    │                   └─ 2. writeSecondary("thebe", ...)       ← ASYNC goroutine
    │                              │
    │                              └─► ThebeRepository.StoreZKBlock(ctx, block)
    │                                       └─► builder.Atomic()  ← KV + SQL in one transaction
    │
    └─ GlobalRepo is nil? Fallback → DB_OPs.StoreZKBlockImmu(conn, block)
```

**Critical invariant:** If ImmuDB write fails, the function returns an error immediately. The ThebeDB write is **never attempted**. If ThebeDB async write fails, it is **logged and dropped** — the caller already got a success response.

---

## MasterRepository — Read Flow

```
caller calls MasterRepository.GetZKBlockByNumber(ctx, 42)
    │
    ├─ 1. m.Immu.GetZKBlockByNumber(ctx, 42)
    │        └─► ImmuRepository → DB_OPs.GetZKBlockByNumber(nil, 42)
    │
    ├─ Got result AND no error? → return it  [span: "immu_hit"]
    │
    └─ Immu miss or error?
            └─ 2. m.Thebe.GetZKBlockByNumber(ctx, 42)
                     └─► ThebeRepository → SQL SELECT  [span: "thebe_fallback"]
                             └─ return result
```

**Why Immu first?** During the migration period, ThebeDB only has data up to the last backfill checkpoint. ImmuDB always has the latest data. Reads from Thebe first would return missing/stale data for recently written blocks.

**Note:** `ThebeRepository` read stubs currently return `nil, nil` for most `Get*` methods except `GetAccount`. Full SQL read implementation is future work once ThebeDB is caught up.

---

## GlobalRepo — The Bridge Between Old and New Code

`DB_OPs/DBConstants.go`:
```go
var GlobalRepo interface{}
```

Set in `main.go` during startup:
```go
DB_OPs.GlobalRepo = repos.Master
```

### Why is it `interface{}`?

`DB_OPs` is a foundational package imported by almost every other package. `internal/repository` imports `DB_OPs`. If `GlobalRepo` were typed as `*repository.MasterRepository`, `DB_OPs` would need to import `internal/repository`, creating a **circular import**. Using `interface{}` breaks the cycle — `DB_OPs` functions use type assertions at call time:

```go
func StoreZKBlock(conn *config.PooledConnection, block *config.ZKBlock) error {
    if repo, ok := GlobalRepo.(interface {
        StoreZKBlock(context.Context, *config.ZKBlock) error
    }); ok {
        ctx, _ := context.WithTimeout(context.Background(), 30*time.Second)
        return repo.StoreZKBlock(ctx, block)
    }
    return StoreZKBlockImmu(conn, block)   // fallback
}
```

This is a **duck-typed dispatch** pattern in Go. The type assertion checks only for the method signature needed — not the concrete type.

---

## ThebeDB Integration

### What is ThebeDB?
`github.com/JupiterMetaLabs/ThebeDB` — an internal JupiterMeta library that combines:
- **KV layer:** PebbleDB-based embedded key-value store (fast local lookups)
- **SQL layer:** PostgreSQL connection pool via `lib/pq`
- **Builder API:** Atomic operations across both stores

### Opening ThebeDB

```go
thebeCfg := thebedb.Config{
    KVPath:  "/path/to/pebble/dir",         // local filesystem
    SQLPath: "postgres://user@host:5433/db", // PostgreSQL DSN
}
thebeInstance, err := thebedb.Open(thebeCfg, nil)
thebeInstance.Start(ctx)  // starts the SQL projector goroutine
```

`thebedb.Open` uses a shared instance pool — calling it twice with the same config returns the same instance. `thebedb.New` creates a fresh instance every time.

### Builder pattern

```go
_, err = builder.New(r.db).
    ExecuteKv(builder.KVPutDerived(kvKey, data)).   // KV write
    ExecuteSQL(insertQuery, args...).                 // SQL write 1
    ExecuteSQL(txQuery, txArgs...).                   // SQL write 2 (multiple allowed)
    Atomic(ctx, true)                                // commit or rollback both
```

`Atomic(ctx, true)` — the `true` means "rollback KV if SQL fails."

Multiple `ExecuteSQL` calls in one builder chain are batched in a single Postgres transaction.

### Data keys in KV

| Entity | Key Format |
|--------|-----------|
| Account | `account:0x<address>` |
| Block | `block:<blockNumber>` |
| Transaction | `tx:0x<hash>` |
| Balance update | `account_update:0x<address>:<nanosecond_timestamp>` |

### Port configuration

| Service | Port | Note |
|---------|------|------|
| Real PostgreSQL (ThebeDB) | **5433** | Configured in `defaults.go` and `jmdn_default.yaml` |
| ImmuDB PG wire protocol | **5432** | ImmuDB exposes a Postgres-compatible endpoint; cannot be changed |

If both run on the same machine, they **must** use different ports.

---

## ImmuRepository — Adapter Pattern

`ImmuRepository` is a zero-field struct that wraps legacy `DB_OPs` package calls:

```go
type ImmuRepository struct{}

func (r *ImmuRepository) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
    conn, err := DB_OPs.GetMainDBConnection(ctx)  // acquire from pool
    defer DB_OPs.PutMainDBConnection(conn)
    return DB_OPs.StoreZKBlockImmu(conn, block)   // *Immu suffix — no GlobalRepo check
}
```

Key points:
- Uses `StoreZKBlockImmu` (not `StoreZKBlock`) to prevent recursion
- Acquires a pooled connection explicitly — never passes `nil` to legacy functions
- Implements all `CoordinatorRepository` methods; `StoreTransaction` is a no-op (ImmuDB embeds txs in blocks)

---

## Async Secondary Writes and GRO

`writeSecondary` in `coordinator.go`:

```go
func (m *MasterRepository) writeSecondary(
    parentCtx context.Context,
    backend string,
    operation string,
    fn func(ctx context.Context) error,
) {
    detachedCtx := context.WithoutCancel(parentCtx)  // keep trace, drop cancellation

    doWrite := func() {
        ctx, cancel := context.WithTimeout(detachedCtx, 30*time.Second)
        defer cancel()
        // ... tracing + error logging + fn(ctx)
    }

    if m.gro != nil {
        m.gro.Go(GRO.DB_OPsCoordinatorWriteThread, func(_ context.Context) error {
            doWrite()
            return nil
        })
    } else {
        go doWrite()  // untracked fallback
    }
}
```

**`context.WithoutCancel`** — This is the key: the detached context keeps all **values** (including OpenTelemetry trace/span IDs so child spans link correctly) but is **not cancelled** when the parent request context ends. The async write outlives the HTTP request.

**GRO (Goroutine Orchestrator):** If `gro` is set, goroutines are tracked and named. In production this enables observability into how many async writes are in-flight. If GRO is unavailable, a plain `go` goroutine is used.

**30-second timeout:** Prevents goroutines hanging forever if ThebeDB is unresponsive.

---

## Migration Subsystem

Four files in `internal/repository/`:

### `migration_config.go` — Configuration
```go
type Config struct {
    Enabled             bool          // opt-in; default false
    MaxBlocksPerBatch   int           // 50 blocks before sleep
    MaxAccountsPerBatch int           // 100 accounts before sleep
    ThrottleDuration    time.Duration // 200ms sleep between batches
    MigrateBlocks       bool          // true
    MigrateAccounts     bool          // true
}
```
Enable via: `BACKFILL_ENABLED=true` environment variable.

### `migration_state.go` — Progress Tracking
`StateTracker.LastSyncedBlock(ctx)` → `SELECT MAX(block_number) FROM blocks`

This is the **resume point**. On restart, backfill continues from `maxBlock + 1`. No separate state table needed — the data itself is the state.

`StateTracker.EnsureSchema(ctx)` → creates all 4 tables if missing. Safe to call on every startup.

### `migration_backfill.go` — The Worker
```
Run()
  ├── EnsureSchema()
  ├── migrateBlocks()
  │     ├── GetLatestBlockNumber from Immu   → find target head
  │     ├── LastSyncedBlock from Thebe SQL   → find resume point
  │     └── for block := start; block <= head; block++
  │               ├── GetZKBlockByNumber(block) from Immu
  │               └── StoreZKBlock(block) to Thebe  ← ON CONFLICT DO NOTHING
  └── migrateAccounts()
        ├── DB_OPs.GetKeys(nil, "account:", 1_000_000)  ← scan all account keys from Immu
        └── for each key:
                ├── GetAccount(addr) from Immu
                └── StoreAccount(account) to Thebe
```

**Idempotent:** Both `blocks` and `accounts` tables use `ON CONFLICT DO NOTHING`. Safe to re-run.

**Throttling:** After every batch, `time.Sleep(ThrottleDuration)` to avoid overwhelming the node.

### `migration_manager.go` — Lifecycle Control
```
BackfillManager
    ├── Start(ctx)   → rejects if already StatusRunning; creates BackfillWorker; goroutine
    ├── Stop()       → cancels runCtx
    └── Status()     → returns Progress snapshot (mutex-protected)
```

State machine:
```
idle ─► running ─► done
                ├─► failed
                └─► stopped  (via Stop())
```

### `migration_verifier.go` — Parity Checking
```
Verifier.VerifyBlocks(ctx, startBlock, endBlock) → Report
    for each block:
        1. fetch from Immu: block_hash, state_root, txns_root
        2. SELECT from Thebe SQL: block_hash, state_root, txns_root
        3. compare; add to Report.Mismatches if different
        4. COUNT(*) transactions in Thebe vs len(block.Transactions) in Immu
```

Returns `Report{TotalChecked, Mismatches []string, Duration}`.

### `migration_admin.go` — HTTP API

Enabled by setting `ADMIN_PORT` env var. Binds on `127.0.0.1:{ADMIN_PORT}`.

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/admin/backfill/start` | Starts backfill. Returns 409 if already running. |
| `POST` | `/admin/backfill/stop` | Cancels active run. No-op if idle. |
| `GET` | `/admin/backfill/status` | Returns `Progress` JSON. |

Auth: `X-Admin-Token: <value>` header must match `ADMIN_TOKEN` env var. If `ADMIN_TOKEN` is unset, auth is skipped (dev mode).

---

## Startup Sequence

```
main()
  │
  ├─ 1. Load config (Viper: YAML → env → CLI flags)
  │       cfg.Database.PostgresDSN   ← postgres://...@127.0.0.1:5433/jmdn_thebe
  │
  ├─ 2. repository.InitRepositories(ctx, RepositoryConfig{
  │         ThebeDB_KVPath:  "/path/to/kv",
  │         ThebeDB_SQLPath: cfg.Database.PostgresDSN,
  │     })
  │         │
  │         ├─ Create GRO local manager
  │         ├─ thebedb.Open(cfg) → thebeInstance
  │         ├─ thebeInstance.Start(ctx)
  │         ├─ thebe_repo.NewThebeRepository(thebeInstance) → thebeRepo
  │         ├─ immu_repo.NewImmuRepository() → immuRepo
  │         ├─ NewMasterRepository(thebeRepo, immuRepo, gro) → master
  │         │       └─ sets globalMasterRepo
  │         └─ NewBackfillManager(immuRepo, thebeRepo, thebeInstance, ConfigFromEnv())
  │               └─ if BACKFILL_ENABLED=true → manager.Start(ctx)
  │
  ├─ 3. DB_OPs.GlobalRepo = repos.Master
  │       └─ All legacy DB_OPs calls now route through MasterRepository
  │
  ├─ 4. (optional) Start admin HTTP server on ADMIN_PORT
  │       └─ repository.NewAdminHandler(ctx, repos.Manager)
  │
  └─ 5. Start all node subsystems (P2P, RPC, etc.)
```

---

## The `*Immu` Suffix Pattern — Avoiding Recursion

This is the most subtle design decision in the codebase. Understanding it is critical.

### The problem

Before this branch, `DB_OPs.StoreZKBlock` wrote directly to ImmuDB. Now it checks `GlobalRepo` first. `GlobalRepo` is `MasterRepository`. `MasterRepository.StoreZKBlock` calls `m.Immu.StoreZKBlock` which is `ImmuRepository.StoreZKBlock`.

**If `ImmuRepository.StoreZKBlock` calls `DB_OPs.StoreZKBlock`:**

```
MasterRepository.StoreZKBlock
  → ImmuRepository.StoreZKBlock
    → DB_OPs.StoreZKBlock          ← checks GlobalRepo
      → GlobalRepo.StoreZKBlock    ← GlobalRepo is MasterRepository!
        → ImmuRepository.StoreZKBlock   ← INFINITE LOOP
          → DB_OPs.StoreZKBlock
            → ...
```

### The solution

Two parallel functions exist at the `DB_OPs` layer:

| Function | Routes through GlobalRepo? | Used by |
|----------|--------------------------|---------|
| `StoreZKBlock` | YES — checks GlobalRepo first | External callers (messaging, block processing) |
| `StoreZKBlockImmu` | NO — writes directly to ImmuDB gRPC | `ImmuRepository` only |
| `UpdateAccountBalance` | YES | External callers |
| `UpdateAccountBalanceImmu` | NO | `ImmuRepository` only |

`ImmuRepository` always calls the `*Immu` suffixed versions. This breaks the cycle.

---

## Tracing / Observability

Every operation in `MasterRepository`, `ImmuRepository`, and `ThebeRepository` creates an OpenTelemetry span via the `ion` logger:

```go
ctx, span = logger.Tracer("MasterRepo").Start(ctx, "DB.StoreZKBlock")
defer span.End()
span.SetAttributes(
    attribute.Int64("block_number", int64(block.BlockNumber)),
    attribute.String("status", "success"),
    attribute.Float64("duration_ms", ...),
)
```

Span attributes on reads indicate which backend served the data:
- `read_source: "immu_hit"` — ImmuDB had it
- `read_source: "thebe_fallback"` — had to fall back to ThebeDB

Secondary write spans are **child spans** of the originating request context (due to `context.WithoutCancel` propagating trace IDs), so the full write path is visible in traces even for async operations.

---

*Document date: 2026-04-02. Branch: `fix/DB_DualWrite`.*
