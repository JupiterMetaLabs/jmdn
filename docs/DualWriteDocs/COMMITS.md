# Commit-by-Commit Breakdown — `fix/DB_DualWrite`

> **Purpose:** Complete record of every commit on this branch.
> Every file touched, every function added or changed, and the exact reason — in plain English.

---

## Full Branch Timeline (Oldest → Newest)

| # | Short SHA | Message |
|---|-----------|---------|
| 1 | `774d058` | feat(storage): implement dual-write async database coordinator with telemetry |
| 2 | `863dc0c` | feat: migrate secondary dual-write databases to ThebeDB |
| 3 | `4ac12b0` | chore: update go-kzg-4844 dependency and reorder Go imports |
| 4 | `153a44e` | Merge branch 'main' into fix/DB_DualWrite |
| 5 | `1953e3f` | push proper tag for thebe in go mod |
| 6 | `dda6b83` | feat: Persist account data atomically in both KV and SQL stores in ThebeDB; global MasterRepository instance |
| 7 | `a7892f8` | Merge branch 'main' into fix/DB_DualWrite |
| 8 | `cdbd2ae` | Merge branch 'main' into fix/DB_DualWrite |
| 9 | `9fbe398` | fix: prevent uint64 underflow in CheckNonceAndGetLatest block scan |
| 10 | `93752e7` | Fix fetching first from immu rather than thebe |
| 11 | `201a8ab` | feat: Implement a data migration system with backfill, verification, configuration, and dedicated tests |
| 12 | `34331b1` | feat: Add BackfillManager with admin HTTP API and explicit lifecycle control |
| 13 | `64e6339` | fix: Resolve nil DB connections, IPv4 loopback, and promote lib/pq dependency |
| 14 | `c03f3ae` | feat: Wire ThebeDB PostgresDSN into unified settings system |
| 15 | `66a2473` | Port Conflict for PG |
| 16 | `d7b0836` | Fix loader for the new PG |
| 17 | `54d05cd` | refactor: rename Immu-bypass DB_OPs functions to clarify write target |

---

## Commit 1 — `774d058`
**feat(storage): implement dual-write async database coordinator with telemetry**

### What problem does this solve?
The node had only ImmuDB. This commit lays the foundation for writing to a second and third database in parallel — without blocking the main path.

### Files Added / Changed

| File | Change |
|------|--------|
| `internal/repository/interfaces.go` | **New.** Defines 3 Go interfaces: `AccountRepository`, `BlockRepository`, `TransactionRepository`. They are combined into `CoordinatorRepository`. All repos — Immu, SQL, KV — must satisfy this. |
| `internal/repository/coordinator.go` | **New.** `MasterRepository` struct. Has fields `SQL`, `KV`, `Immu` (each a `CoordinatorRepository`). Writes: Immu synchronously first, SQL+KV asynchronously via `writeSecondary`. Reads: KV → ImmuDB fallback. |
| `internal/repository/setup.go` | **New.** `InitRepositories()` function. Called once at node startup. Spins up Postgres pool (pgxpool), PebbleDB, ImmuRepo adapter, assembles `MasterRepository`. |
| `internal/repository/immu_repo/immu_repo.go` | **New.** `ImmuRepository` struct — a thin wrapper around existing `DB_OPs.*` functions so ImmuDB satisfies `CoordinatorRepository`. |
| `internal/repository/sql_repo/sql_repo.go` | **New.** `SQLRepository` using `pgxpool`. Writes blocks, accounts, transactions to Postgres tables. Later deleted in commit 2. |
| `internal/repository/kv_repo/kv_repo.go` | **New.** `PebbleRepository` using PebbleDB (embedded key-value). Stores accounts and blocks as JSON. Later deleted in commit 2. |
| `internal/repository/logger.go` | **New.** `repoLogger()` helper — fetches the `ion.Ion` logger instance for tracing. |
| `config/db_pools.go` | **New.** Postgres pool init helpers (pgxpool). |
| `DB_OPs/account_immuclient.go` | Modified — `StoreAccount` now checks `GlobalRepo` first (delegates if set). `UpdateAccountBalance` same pattern. |
| `DB_OPs/immuclient.go` | Modified — `StoreZKBlock` checks `GlobalRepo`. If set → delegate to coordinator. Else → write to Immu directly. |
| `DB_OPs/DBConstants.go` | Added `GlobalRepo interface{}` variable — the global handle to `MasterRepository`. |
| `config/GRO/constants.go` | Added `DB_OPsCoordinatorWriteThread` constant for GRO goroutine naming. |
| `config/settings/config.go` | Added `PostgresDSN` field stub to settings. |
| `main.go` | Added `InitRepositories()` call at startup. Sets `DB_OPs.GlobalRepo = repos.Master`. |
| `go.mod` / `go.sum` | Added `pgxpool`, PebbleDB, `goroutine-orchestrator`, `ThebeDB` references. |

### Key design decisions in this commit
- `writeSecondary` uses `context.WithoutCancel(parentCtx)` — the async goroutine inherits the **trace context** (so spans are children) but **not the cancellation** (so if the HTTP request finishes, the async write continues).
- If the GRO manager is set, secondary goroutines are tracked; otherwise a plain `go` is used as fallback.
- `GlobalRepo` is an `interface{}` on purpose — avoids circular imports between `DB_OPs` and `internal/repository`.

---

## Commit 2 — `863dc0c`
**feat: migrate secondary dual-write databases to ThebeDB**

### What problem does this solve?
Postgres (pgxpool) and PebbleDB were two separate libraries to maintain. ThebeDB is an internal library that wraps both — KV (PebbleDB-like) + SQL (Postgres) in one unified API with an atomic `builder` pattern. This commit replaces both separate repos with a single `ThebeRepository`.

### Files Added / Changed

| File | Change |
|------|--------|
| `internal/repository/thebe_repo/thebe_repo.go` | **New.** `ThebeRepository` implementing `CoordinatorRepository` using `ThebeDB`. Block + transaction writes use `builder.New(r.db).ExecuteKv(...).ExecuteSQL(...).Atomic()` — one call commits to both KV and SQL atomically. |
| `internal/repository/sql_repo/sql_repo.go` | **Deleted.** pgxpool Postgres adapter removed. |
| `internal/repository/coordinator.go` | Simplified: `MasterRepository.SQL` and `MasterRepository.KV` replaced by single `MasterRepository.Thebe`. |
| `internal/repository/setup.go` | Now opens `ThebeDB` via `thebedb.Open(cfg, nil)`. KV path + SQL DSN combined into one `thebedb.Config`. |
| `main.go` | Removed pgxpool / PebbleDB path setup. Uses `ThebeDB_KVPath` + `ThebeDB_SQLPath` from config. |
| `go.mod` / `go.sum` | Removed pgxpool, PebbleDB. Added `github.com/JupiterMetaLabs/ThebeDB` as a local replace dependency. |

### Key insight about ThebeDB's builder
```go
builder.New(r.db).
    ExecuteKv(builder.KVPutDerived(kvKey, data)).  // writes to PebbleDB-equivalent
    ExecuteSQL(query, args...).                     // writes to Postgres
    Atomic(ctx, true)                              // commits both or rolls both back
```
The `Atomic(ctx, true)` call means: "if SQL fails, roll back KV too." This gives strong consistency within ThebeDB's dual-layer.

---

## Commit 3 — `4ac12b0`
**chore: update go-kzg-4844 dependency to v1.1.0 and reorder Go imports**

### What problem does this solve?
Housekeeping — dependency bump and goimports formatting on the new files. No behavioral change.

### Files Changed
`go.mod`, `go.sum`, plus import sorting in `coordinator.go`, `immu_repo.go`, `interfaces.go`, `kv_repo.go` (kv_repo was still present at this point), `setup.go`.

---

## Commits 4, 7, 8 — `153a44e`, `a7892f8`, `cdbd2ae`
**Merge branch 'main' into fix/DB_DualWrite (×3)**

These three commits pull upstream changes from `main` into the branch:
- `main` had `sqlops` production hardening (prepared statements, safer DDL patterns)
- `main` had a SonarQube CI workflow added
These changes come in via the merge and the dual-write branch picks them up.

---

## Commit 5 — `1953e3f`
**push proper tag for thebe in go mod**

Fixes the `go.mod` replace directive to point to the correct tagged version of `ThebeDB`. No code logic change.

---

## Commit 6 — `dda6b83`
**feat: Persist account data atomically in both KV and SQL stores; introduce global MasterRepository instance**

### What problem does this solve?
Two separate issues:
1. `ThebeRepository.StoreAccount` was only writing to SQL — the KV side was not being populated.
2. The `MasterRepository` had no global accessor, making it impossible to call from other packages.

### Files Changed

| File | Change |
|------|--------|
| `internal/repository/thebe_repo/thebe_repo.go` | **Major rewrite.** `StoreAccount` now writes to both KV and SQL atomically. `UpdateAccountBalance` also writes to both. `StoreZKBlock` similarly writes block → SQL, block JSON → KV, transactions → SQL, ZK proofs → SQL, all in one `builder` chain. |
| `internal/repository/coordinator.go` | Added `var globalMasterRepo *MasterRepository` and `GetMasterRepository()` function. `NewMasterRepository` now sets the global. |
| `internal/repository/setup.go` | Minor: references updated. |
| `messaging/BlockProcessing/Processing.go` | Updated to use `GetMasterRepository()` instead of legacy DB_OPs calls for block writes in some paths. |
| `messaging/blockPropagation.go` | Similar — references global MasterRepository. |
| `messaging/broadcast.go` | Same pattern. |

### Key additions in ThebeDB writes

**Account KV key format:** `account:{0xADDRESS}`

**Block KV key format:** `block:{blockNumber}`

**Transaction KV key format:** `tx:{0xHASH}`

The SQL schema mirrors the Postgres tables: `accounts`, `blocks`, `transactions`, `zk_proofs`.

---

## Commit 9 — `9fbe398`
**fix: prevent uint64 underflow in CheckNonceAndGetLatest block scan**

### What problem does this solve?
In `DB_OPs/account_immuclient.go`, `CheckNonceAndGetLatest` scanned blocks in reverse to find the latest nonce for an address. The loop was:

```go
for i := currentBlock; i >= startBlock; i-- {
```

With `uint64` variables, when `startBlock = 0` and the loop processes block `0` then decrements, `i` becomes `18446744073709551615` (max uint64) — never less than 0 — causing an **infinite loop** that floods logs with `"tbtree: key not found"` errors on fresh chains with few blocks.

### Fix
Restructured as a **top-decrement loop**:
```go
for i := currentBlock + 1; i > startBlock; {
    i--
    // process block i
}
```
`i` never passes through zero. The `uint64` underflow is impossible.

### File Changed
`DB_OPs/account_immuclient.go` — 7 lines changed in the loop body.

---

## Commit 10 — `93752e7`
**Fix fetching first from immu rather than thebe**

### What problem does this solve?
In `coordinator.go`, reads were accidentally going to ThebeDB first and ImmuDB as fallback — the opposite of the intended design. During migration, ThebeDB lags behind ImmuDB (it's being backfilled). If you read from Thebe first, you can return stale or missing data.

### Fix
Swapped the read order in all `Get*` methods in `MasterRepository`:
- **Before:** Try `m.Thebe` → fallback to `m.Immu`
- **After:** Try `m.Immu` → fallback to `m.Thebe`

Span attributes updated: `immu_hit` / `thebe_fallback`.

### File Changed
`internal/repository/coordinator.go` — 72 lines, all `Get*` methods reordered.

---

## Commit 11 — `201a8ab`
**feat: Implement a data migration system with backfill, verification, configuration, and dedicated tests**

### What problem does this solve?
The dual-write only captures **new data** from this point forward. Historical data (all blocks and accounts already in ImmuDB) needs to be **copied** to ThebeDB. This commit adds the entire migration subsystem.

### Files Added

| File | Purpose |
|------|---------|
| `internal/repository/migration_config.go` | `Config` struct + `DefaultConfig()` + `ConfigFromEnv()`. Controls batch sizes, throttle duration, which data types to migrate. Backfill disabled by default. |
| `internal/repository/migration_state.go` | `StateTracker`. Two methods: `EnsureSchema(ctx)` — creates all 4 Postgres tables if missing; `LastSyncedBlock(ctx)` — `SELECT MAX(block_number) FROM blocks` to find where we left off. |
| `internal/repository/migration_backfill.go` | `BackfillWorker`. `Run()` → `migrateBlocks()` + `migrateAccounts()`. Reads from ImmuDB (source), writes to ThebeDB (target). Idempotent — ThebeDB uses `ON CONFLICT DO NOTHING`. Batched with throttle sleep. |
| `internal/repository/migration_verifier.go` | `Verifier`. `VerifyBlocks(ctx, start, end)` — for each block in range, fetches from Immu and compares `block_hash`, `state_root`, `txns_root`, and transaction count against ThebeDB SQL. Returns a `Report` with mismatches. |
| `internal/repository/migration_backfill_test.go` | Unit tests for `BackfillWorker` using mock sources/targets. |
| `internal/repository/migration_integration_test.go` | Integration tests that spin up real ThebeDB and run backfill end-to-end. |

### Changes to existing files

| File | Change |
|------|--------|
| `internal/repository/setup.go` | Added goroutine to auto-start `BackfillWorker.Run` if `ConfigFromEnv().Enabled`. |
| `internal/repository/thebe_repo/thebe_repo.go` | Added defensive nil checks around async logger; `to_timestamp($4)` for timestamp column. |

### Schema created by EnsureSchema

```
accounts      — address (PK), did_address, balance_wei, nonce, account_type, metadata (JSONB), created_at, updated_at
blocks        — block_number (PK), block_hash, parent_hash, timestamp, txns_root, state_root, logs_bloom, coinbase_addr, zkvm_addr, gas_limit, gas_used, status, extra_data (JSONB)
transactions  — tx_hash (PK), block_number, tx_index, from_addr, to_addr, value_wei, nonce, type, gas fields, data (BYTEA), access_list (JSONB), sig_v/r/s
zk_proofs     — block_number (PK), proof_hash, stark_proof (BYTEA), commitment (JSONB)
```

---

## Commit 12 — `34331b1`
**feat: Add BackfillManager with admin HTTP API and explicit lifecycle control**

### What problem does this solve?
The backfill in commit 11 launched as a fire-and-forget goroutine with no way to:
- Know if it's still running
- Stop it safely
- Start it on demand without restarting the node

### Files Added

| File | Purpose |
|------|---------|
| `internal/repository/migration_manager.go` | `BackfillManager`. Wraps `BackfillWorker` with lifecycle control. State machine: `idle → running → done/failed/stopped`. Mutex-protected. `Start()` rejects duplicate runs. `Stop()` cancels context. `Status()` returns a `Progress` snapshot. |
| `internal/repository/migration_admin.go` | HTTP handler for 3 admin endpoints. Auth via `X-Admin-Token` header (matches `ADMIN_TOKEN` env var). Endpoints: `POST /admin/backfill/start`, `POST /admin/backfill/stop`, `GET /admin/backfill/status`. |

### Changes to existing files

| File | Change |
|------|--------|
| `internal/repository/migration_backfill.go` | Added `onProgress` and `onError` callbacks to `BackfillWorker`. Called by manager to update live `Progress`. |
| `internal/repository/migration_config.go` | Added `ConfigFromEnv()` function. `BACKFILL_ENABLED=true` opt-in. |
| `internal/repository/setup.go` | Replaced fire-and-forget goroutine with `BackfillManager`. Manager stored in `Repositories.Manager`. Auto-start only if `BACKFILL_ENABLED=true`. |
| `main.go` | Added `--thebe-dsn` CLI flag. Added admin HTTP server startup when `ADMIN_PORT` env is set. `envOrDefault()` helper added. |

### Progress struct
```go
type Progress struct {
    Status       RunStatus   // idle | running | done | failed | stopped
    StartedAt    time.Time
    FinishedAt   time.Time
    CurrentBlock uint64
    BlocksDone   uint64
    ErrorCount   int
    LastError    string
}
```

---

## Commit 13 — `64e6339`
**fix: Resolve nil DB connections, IPv4 loopback, and promote lib/pq dependency**

### Three independent fixes in one commit

**Fix 1: IPv4 loopback**
- `config/ImmudbConstants.go`: `DBAddress` changed from `"localhost"` to `"127.0.0.1"`
- **Why:** On macOS, `localhost` resolves to IPv6 `::1` before IPv4. ImmuDB binds on IPv4. The mismatch causes connection failures on macOS dev machines.

**Fix 2: Nil connection panic**
- `internal/repository/immu_repo/immu_repo.go`: `StoreAccount` and `StoreZKBlock` now call `DB_OPs.GetAccountsConnections(ctx)` / `DB_OPs.GetMainDBConnection(ctx)` to acquire a real pooled connection before calling the legacy DB_OPs functions.
- **Why:** The legacy `DB_OPs.StoreAccount(nil, ...)` path had a nil check that skipped writes silently. During backfill, this meant blocks were not actually stored in ImmuDB from the repo layer.

**Fix 3: lib/pq direct dependency**
- `go.mod`: `github.com/lib/pq` promoted from `indirect` to `direct`
- **Why:** ThebeDB v0.1.0 uses `lib/pq` internally. Go's module system requires it listed as direct when the local package directly imports or relies on it through the integration.

---

## Commit 14 — `c03f3ae`
**feat: Wire ThebeDB PostgresDSN into unified settings system**

### What problem does this solve?
The Postgres DSN was hardcoded in `main.go` via `envOrDefault("THEBE_SQL_DSN", "...")`. This bypassed the unified Viper-based config system that all other settings use. This commit makes `postgres_dsn` a proper first-class config field.

### Changes

| File | Change |
|------|--------|
| `config/settings/defaults.go` | Added `PostgresDSN` to `DatabaseSettings`. Default: `postgres://postgres:postgres@127.0.0.1:5432/jmdn_thebe?sslmode=disable`. |
| `jmdn_default.yaml` | Added `database.postgres_dsn` field under the `database:` section. |
| `main.go` | Replaced `envOrDefault("THEBE_SQL_DSN", ...)` with `cfg.Database.PostgresDSN`. Added `--thebe-dsn` CLI flag that overrides. |
| `Scripts/setup_dependencies.sh` | **Major addition** — added full `--postgres` flag support: installs PostgreSQL, creates `jmdn_thebe` database and `postgres` user, starts the service. |

### DSN resolution priority (highest → lowest)
1. `--thebe-dsn` CLI flag
2. `JMDN_DATABASE_POSTGRES_DSN` environment variable
3. `database.postgres_dsn` in `jmdn.yaml`
4. Default in `defaults.go`

---

## Commit 15 — `66a2473`
**Port Conflict for PG**

### What problem does this solve?
ImmuDB exposes a **PostgreSQL wire protocol endpoint on port 5432** for compatibility queries. If the real Postgres server also listens on 5432, there is a port conflict and one of them fails to start.

### Fix
- `config/settings/defaults.go`: Default DSN port changed from `5432` → `5433`
- `Scripts/setup_dependencies.sh`: Added logic to patch `postgresql.conf` to `port = 5433` before starting the service.

**Summary:** Real Postgres = 5433. ImmuDB PG wire = 5432. No conflict.

---

## Commit 16 — `d7b0836`
**Fix loader for the new PG**

### What problem does this solve?
Even though `defaults.go` had `PostgresDSN` and `jmdn_default.yaml` had the field, Viper's `setDefault()` was not called for `database.postgres_dsn` in the loader. Without `setDefault`, Viper doesn't know the key exists and won't populate it from defaults when the YAML key is absent.

### Fix
`config/settings/loader.go`: Added one line:
```go
v.SetDefault("database.postgres_dsn", defaults.Database.PostgresDSN)
```

---

## Commit 17 — `54d05cd`
**refactor: rename Immu-bypass DB_OPs functions to clarify write target**

### What problem does this solve?
After the dual-write architecture, some `DB_OPs` functions were ambiguously named. `StoreZKBlock` could mean "write to ImmuDB" or "write to both DBs via GlobalRepo". The distinction was not obvious from the name alone.

### Renames

| Old Name | New Name | What it does |
|----------|----------|-------------|
| `StoreZKBlockDirect` | `StoreZKBlockImmu` | Writes **only to ImmuDB**, bypasses GlobalRepo |
| `UpdateAccountBalanceDirect` | `UpdateAccountBalanceImmu` | Updates balance **only in ImmuDB**, bypasses GlobalRepo |

### Public-facing functions (unchanged names, new routing)

| Function | Behavior |
|----------|---------|
| `StoreZKBlock` | Checks `GlobalRepo`. If set → calls `GlobalRepo.StoreZKBlock` (dual-write via MasterRepository). Else → calls `StoreZKBlockImmu`. |
| `UpdateAccountBalance` | Same pattern — routes through `GlobalRepo` or falls back to `UpdateAccountBalanceImmu`. |

### Why this matters
`ImmuRepository.StoreZKBlock` (in `immu_repo.go`) must call `StoreZKBlockImmu` — not `StoreZKBlock`. If it called the plain `StoreZKBlock`, the call chain would be:

```
MasterRepository.StoreZKBlock
  → ImmuRepository.StoreZKBlock
    → DB_OPs.StoreZKBlock
      → GlobalRepo.StoreZKBlock     ← BACK TO MasterRepository
        → ImmuRepository.StoreZKBlock  ← INFINITE LOOP
```

The `*Immu` suffix names make this anti-pattern impossible — you can clearly see when you're bypassing the coordinator.

### Files Changed
`DB_OPs/account_immuclient.go`, `DB_OPs/immuclient.go`, `internal/repository/immu_repo/immu_repo.go`

---

## Summary Table — All Files Introduced on This Branch

| File | Status | Purpose |
|------|--------|---------|
| `internal/repository/interfaces.go` | Added | `CoordinatorRepository` interface definition |
| `internal/repository/coordinator.go` | Added | `MasterRepository` — dual-write coordinator |
| `internal/repository/setup.go` | Added | `InitRepositories()` — startup wiring |
| `internal/repository/logger.go` | Added | `repoLogger()` helper |
| `internal/repository/immu_repo/immu_repo.go` | Added | ImmuDB adapter for `CoordinatorRepository` |
| `internal/repository/thebe_repo/thebe_repo.go` | Added | ThebeDB adapter for `CoordinatorRepository` |
| `internal/repository/migration_config.go` | Added | Backfill config + env loading |
| `internal/repository/migration_state.go` | Added | StateTracker + `EnsureSchema` |
| `internal/repository/migration_backfill.go` | Added | `BackfillWorker` — historical copy Immu → Thebe |
| `internal/repository/migration_manager.go` | Added | `BackfillManager` — lifecycle control |
| `internal/repository/migration_admin.go` | Added | Admin HTTP API for backfill |
| `internal/repository/migration_verifier.go` | Added | Parity checker Immu vs Thebe |
| `internal/repository/sql_repo/sql_repo.go` | Added then **Deleted** | pgxpool Postgres (replaced by ThebeDB) |
| `config/db_pools.go` | Added | pgxpool init helpers (used early, replaced by ThebeDB) |
| `DB_OPs/DBConstants.go` | Modified | Added `GlobalRepo interface{}` |
| `DB_OPs/account_immuclient.go` | Modified | `StoreAccount`/`UpdateAccountBalance` → GlobalRepo routing; uint64 underflow fix; `*Immu` renames |
| `DB_OPs/immuclient.go` | Modified | `StoreZKBlock` → GlobalRepo routing; `*Immu` renames |
| `config/ImmudbConstants.go` | Modified | `DBAddress = "127.0.0.1"` |
| `config/settings/defaults.go` | Modified | `PostgresDSN` default, port 5433 |
| `config/settings/loader.go` | Modified | `setDefault` for `database.postgres_dsn` |
| `config/settings/config.go` | Modified | `DatabaseSettings.PostgresDSN` field |
| `config/GRO/constants.go` | Modified | GRO thread name constant |
| `jmdn_default.yaml` | Modified | `database.postgres_dsn` YAML field |
| `main.go` | Modified | `InitRepositories`, `--thebe-dsn` flag, admin HTTP server |
| `Scripts/setup_dependencies.sh` | Modified | `--postgres` install path, port 5433 patch |
| `go.mod` / `go.sum` | Modified | ThebeDB, lib/pq, goroutine-orchestrator, removed pgxpool/Pebble |

---

*Document date: 2026-04-02. Branch: `fix/DB_DualWrite`.*
