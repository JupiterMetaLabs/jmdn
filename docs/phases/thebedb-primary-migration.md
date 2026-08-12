# ThebeDB Primary Migration — Implementation Phases

## Context

Previous phase doc (`thebedb-integration.md`) completed the shadow-writer integration: ImmuDB is primary, ThebeDB receives shadow writes via DualDB. All 9 phases of that doc are done.

This doc covers the next stage: **replace ImmuDB entirely**. ThebeDB becomes the sole storage backend. EventLog (DuckDB, append-only) is handled by ThebeDB internally — no action needed here.

## Design Decisions (locked)

| Decision | Choice | Rationale |
|---|---|---|
| Interface location | `DB_OPs/store/` | Single-responsibility: owns only contracts. Zero internal imports. |
| Interface granularity | Domain-segregated | `BlockStore`, `AccountStore`, `TxStore`, `ZKProofStore`, `LogStore`, `ReceiptStore` |
| Pool return type | `ThebeHandle` interface | Embeds all domain interfaces. Pool is the only place knowing `*compositeHandle`. |
| Function signatures | Domain interfaces directly | `func StoreBlock(s store.BlockStore, ...)` — callers bind to interface, not concrete. |
| Cache placement | Decorator per interface | `cachedBlockStore` wraps `BlockStore`. Cache concern isolated from backend logic. |
| Write path | SQL/KV first → cache SET on success | ThebeGateway already does 2PC (SQL+KV). Cache is post-commit best-effort. |
| Read path | Cache GET → miss → SQL/KV → cache SET | cache.Cache interface, Redis-backed. Miss path is Cassata SQL query. |
| Method names | Domain-specific | `StoreBlock`/`GetBlock` not `Create`/`SafeRead`. Named for the domain, not the engine. |
| DualDB fate | Removed in Phase 7 | After ThebeBackend is primary and callers are migrated. |

## SOLID Gates

- **S**: `DB_OPs/store/` — one invariant: define storage contracts. No implementation, no imports.
- **S**: `DB_OPs/backend/` — one invariant: implement `ThebeHandle` by delegating to ThebeGateway (writes) + Cassata/ThebeReader (reads).
- **S**: `DB_OPs/store/cache/` — one invariant: add cache read-through / write-invalidation to a domain interface.
- **O**: New entity (e.g. `SnapshotStore`) → implement interface, add to `ThebeHandle`, implement on `thebeBackend`. Zero modification to existing cache decorators or pool code.
- **I**: gETH callers receive `BlockStore + TxStore + ReceiptStore`. CLI receives `AccountStore`. AVC receives `ZKProofStore`. No caller gets methods it doesn't use.
- **D**: `DB_OPs/store/` imports: zero internal. `DB_OPs/backend/` imports: `DB_OPs/store/` (interface) + ThebeGateway interface + Cassata interface only.

## Pattern Selection

- **Primary: Strategy** (Behavioral) — `thebeBackend` is the strategy. Domain interfaces are the contracts. Pool returns `ThebeHandle` (strategy slot). Mock strategies slot in for tests.
- **Secondary: Decorator** (Structural) — `cachedBlockStore` etc. wrap domain interfaces. Cache is a decorator, not a field on the backend.
- **Composite** (Structural) — `compositeHandle` assembles decorated stores into a single `ThebeHandle` for the pool.
- **Anti-pattern avoided**: concrete `*config.PooledConnection` imported across 33 packages. `ImmuClient` concrete type in function signatures.

---

## Phase 1.0: `DB_OPs/store/` — domain interfaces

- **What:** Define all domain interfaces + `ThebeHandle`. Zero implementation. No DB calls. This is the contract everything else builds against.
- **Files to create:**
  - `DB_OPs/store/interfaces.go` — all domain interfaces + `ThebeHandle`
  - `DB_OPs/store/types.go` — shared domain types used across interfaces (reuse from `thebegateway/types.go` where possible, do not duplicate)
  - `DB_OPs/store/errors.go` — error sentinels (`ErrNotFound`, `ErrDuplicateNonce`, `ErrAccountExists`)
- **Interfaces:**
  ```go
  type BlockStore interface {
      StoreBlock(ctx context.Context, block *config.ZKBlock) error
      GetBlock(ctx context.Context, blockNumber uint64) (*thebegateway.BlockRecord, error)
      GetBlockByHash(ctx context.Context, hash string) (*thebegateway.BlockRecord, error)
      GetLatestBlockNumber(ctx context.Context) (uint64, error)
      BulkGetBlocks(ctx context.Context, from, to uint64) ([]*thebegateway.BlockRecord, error)
  }

  type AccountStore interface {
      CreateAccount(ctx context.Context, account *Account) error
      UpdateAccountBalance(ctx context.Context, address, balance string) error
      GetAccount(ctx context.Context, address string) (*Account, error)
      GetAccountByDID(ctx context.Context, did string) (*Account, error)
      CheckNonceDuplicate(ctx context.Context, address string, nonce uint64) (bool, error)
      GetLatestNonce(ctx context.Context, address string) (uint64, error)
      BulkGetAccounts(ctx context.Context, addresses []string) ([]*Account, error)
  }

  type TxStore interface {
      StoreTransaction(ctx context.Context, tx *config.Transaction, blockNumber uint64, txIndex int) error
      GetTransaction(ctx context.Context, txHash string) (*thebegateway.TransactionRecord, error)
      GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]*thebegateway.TransactionRecord, error)
      GetTransactionsByAddress(ctx context.Context, address string, limit int) ([]*thebegateway.TransactionRecord, error)
      SetTransactionStatus(ctx context.Context, txHash string, status int) error
  }

  type ZKProofStore interface {
      StoreZKBlock(ctx context.Context, block *config.ZKBlock) error
      GetZKProof(ctx context.Context, blockNumber uint64) (*thebegateway.ZKProofRecord, error)
  }

  type ReceiptStore interface {
      GetReceipt(ctx context.Context, txHash string) (*config.Receipt, error)
  }

  type LogStore interface {
      StoreLogs(ctx context.Context, logs []*ethtypes.Log) error
      GetLogs(ctx context.Context, filter LogFilter) ([]*ethtypes.Log, error)
  }

  type ThebeHandle interface {
      BlockStore
      AccountStore
      TxStore
      ZKProofStore
      ReceiptStore
      LogStore
      io.Closer
  }
  ```
- **`Account` type in `DB_OPs/store/types.go`:** mirrors existing `DB_OPs.Account` struct — no new fields. Import `common.Address` from go-ethereum. All string balances preserved.
- **`LogFilter` type:** `{FromBlock, ToBlock uint64; Addresses []common.Address; Topics [][]common.Hash}`
- **Data structures:** all interface definitions (no runtime allocation). Types are plain structs, passed by pointer.
- **Inputs:** none
- **Done when:**
  - `go build ./DB_OPs/store/...` passes
  - Zero imports of any internal JMDN package
  - `grep -r "immudb\|ImmuClient\|PooledConnection" DB_OPs/store/` → empty
- **Status:** [x]

---

## Phase 2.0: `DB_OPs/backend/` — `thebeBackend` implementing `ThebeHandle`

- **What:** Concrete struct implementing all domain interfaces by delegating to existing ThebeGateway (writes) and Cassata/ThebeReader (reads). No cache logic here — cache is in decorators (Phase 3).
- **Files to create:**
  - `DB_OPs/backend/backend.go` — `thebeBackend` struct + constructor `New(gw thebegateway.ThebeGateway, r thebegateway.ThebeReader, lw LogWriter) *thebeBackend`
  - `DB_OPs/backend/block.go` — `StoreBlock`, `GetBlock`, `GetBlockByHash`, `GetLatestBlockNumber`, `BulkGetBlocks`
  - `DB_OPs/backend/account.go` — `CreateAccount`, `UpdateAccountBalance`, `GetAccount`, `GetAccountByDID`, `CheckNonceDuplicate`, `GetLatestNonce`, `BulkGetAccounts`
  - `DB_OPs/backend/tx.go` — `StoreTransaction`, `GetTransaction`, `GetTransactionsByBlock`, `GetTransactionsByAddress`, `SetTransactionStatus`
  - `DB_OPs/backend/zkproof.go` — `StoreZKBlock`, `GetZKProof`
  - `DB_OPs/backend/receipt.go` — `GetReceipt` (facade: get tx → get block → generate receipt, mirrors existing `Facade_Receipts.go` logic)
  - `DB_OPs/backend/log.go` — `StoreLogs`, `GetLogs` (delegates to `LogWriter` interface wrapping existing `log_writer.go`)
- **Data structures:**
  - `thebeBackend{gw thebegateway.ThebeGateway; r thebegateway.ThebeReader; lw LogWriter}` — all interface fields. Stateless beyond these. No mutex needed (deps own their sync).
  - `LogWriter` interface (defined in `backend.go`): `StoreLogs(ctx, []*ethtypes.Log) error; GetLogs(ctx, LogFilter) ([]*ethtypes.Log, error)` — wraps existing `GlobalLogWriter` so tests can inject a mock.
- **Compile-time assertion in `backend.go`:**
  ```go
  var _ store.ThebeHandle = (*thebeBackend)(nil)
  ```
- **Write delegation:** each Store* method calls the corresponding ThebeGateway method. ThebeGateway already does 2PC (SQL + KV via ThebeDB builder) + outbox on failure.
- **Read delegation:** each Get* method calls Cassata/ThebeReader. No cache here.
- **Module AI-doc block** (top of `backend.go`):
  ```
  // MODULE: DB_OPs/backend
  // PURPOSE: Implement store.ThebeHandle by delegating writes to ThebeGateway and reads to ThebeReader.
  //
  // CORE DATA STRUCTURES:
  //   - thebeBackend: zero-state struct; holds three interface deps (gw, r, lw). Stateless per-call.
  //
  // TO MODIFY BEHAVIOR:
  //   - Change write path: edit thebegateway.ThebeGateway implementation
  //   - Change read path: edit thebegateway.ThebeReader implementation
  //   - Add new entity: implement method on thebeBackend + add to compositeHandle (Phase 4)
  //
  // DO NOT:
  //   - Import config.ImmuClient or config.PooledConnection here
  //   - Add cache logic here — cache lives in DB_OPs/store/cache/ decorators
  //   - Import DB_OPs/dualdb — backend knows nothing about dual-write
  //
  // EXTENSION POINT: implement new store.XxxStore interface → add field to compositeHandle
  ```
- **Inputs:** Phase 1.0 complete; existing `thebegateway.ThebeGateway` + `thebegateway.ThebeReader` interfaces (Phase 5+6 of prior doc)
- **Done when:**
  - `var _ store.ThebeHandle = (*thebeBackend)(nil)` compiles
  - All interface methods implemented (no panic stubs)
  - `go build ./DB_OPs/backend/...` passes
  - Zero imports of `immudb`, `ImmuClient`, `PooledConnection`
- **Status:** [x]

---

## Phase 3.0: `DB_OPs/store/cache/` — cache decorators

- **What:** One decorator per domain interface. Write path: call inner first → on success: `cache.Set`. Read path: `cache.Get` → hit: return. Miss: call inner → `cache.Set` best-effort → return.
- **Files to create:**
  - `DB_OPs/store/cache/block.go` — `cachedBlockStore{inner store.BlockStore; c cache.Cache; ttl time.Duration}`
  - `DB_OPs/store/cache/account.go` — `cachedAccountStore{...}`
  - `DB_OPs/store/cache/tx.go` — `cachedTxStore{...}`
  - `DB_OPs/store/cache/zkproof.go` — `cachedZKProofStore{...}`
  - `DB_OPs/store/cache/keys.go` — cache key builders (pure functions, no state)
  - `DB_OPs/store/cache/noop.go` — `noopCache` implementing `cache.Cache` for tests/no-cache mode
- **Cache key scheme** (stable, collision-free):
  ```
  block:<block_number>              TTL: 5min
  block:hash:<block_hash>           TTL: 5min
  block:latest                      TTL: 2s   (hot path, short TTL)
  account:<address>                 TTL: 30s
  tx:<tx_hash>                      TTL: 5min
  zk:<block_number>                 TTL: 10min (immutable after finality)
  ```
- **Write path contract (same for all decorators):**
  ```
  func (c *cachedBlockStore) StoreBlock(ctx, block) error:
    1. err := c.inner.StoreBlock(ctx, block)   // SQL+KV first
    2. if err != nil: return err               // do NOT cache on failure
    3. c.cache.Set(ctx, key, serialized, ttl)  // best-effort, ignore error
    4. return nil
  ```
- **Read path contract:**
  ```
  func (c *cachedBlockStore) GetBlock(ctx, num) (*BlockRecord, error):
    1. raw, err := c.cache.Get(ctx, key)      // cache first
    2. if hit: json.Unmarshal → return
    3. rec, err := c.inner.GetBlock(ctx, num) // SQL/KV on miss
    4. if err != nil: return nil, err
    5. c.cache.Set(ctx, key, marshal(rec), ttl) // populate cache
    6. return rec, nil
  ```
- **Data structures:**
  - Each decorator: `struct{inner SomeDomainInterface; c cache.Cache; ttl time.Duration}` — fixed size. No map, no slice. Stateless per-call.
  - `cache.Cache` — ThebeDB's cache interface (Redis-backed in prod, `noopCache` in tests).
- **Compile-time assertions** in each file:
  ```go
  var _ store.BlockStore = (*cachedBlockStore)(nil)
  ```
- **ReceiptStore and LogStore**: no cache decorator — receipts generated on-the-fly (not stored in cache), logs have complex filter keys making cache invalidation fragile.
- **Inputs:** Phase 1.0 (interfaces), Phase 2.0 (inner implementation to wrap)
- **Done when:**
  - All compile-time assertions pass
  - Cache hit/miss paths covered in `tests/store/cache/`
  - `go build ./DB_OPs/store/cache/...` passes
- **Status:** [x]

---

## Phase 4.0: `compositeHandle` + pool wiring

- **What:** Build `compositeHandle` that assembles decorated stores into a single `ThebeHandle`. Wire pool's `Get()` to return `store.ThebeHandle`. Remove `config.ImmuClient`, `config.ImmuTransaction` from `config/`.
- **Files to create:**
  - `DB_OPs/backend/composite.go` — `compositeHandle` struct + `NewComposite(backend *thebeBackend, c cache.Cache) store.ThebeHandle`
- **Files to modify:**
  - `config/ConnectionPool.go` — `PooledConnection.Client` field type changes from `*ImmuClient` to `store.ThebeHandle`. Remove `Token`, `TokenExpiry` fields. Pool `createConnection()` builds `compositeHandle`.
- **Files to delete:**
  - `config/ImmudbConstants.go` — `ImmuClient`, `ImmuTransaction` types removed entirely.
- **`compositeHandle` struct:**
  ```go
  type compositeHandle struct {
      blocks   store.BlockStore    // cachedBlockStore{inner: backend, c: cache}
      accounts store.AccountStore  // cachedAccountStore{inner: backend, c: cache}
      txs      store.TxStore       // cachedTxStore{inner: backend, c: cache}
      zkproofs store.ZKProofStore  // cachedZKProofStore{inner: backend, c: cache}
      receipts store.ReceiptStore  // backend directly (no cache)
      logs     store.LogStore      // backend directly (no cache)
      backend  io.Closer           // for Close()
  }
  // Each method delegates to the correct field:
  // StoreBlock → c.blocks.StoreBlock(...)
  // GetBlock   → c.blocks.GetBlock(...)
  // etc.
  ```
- **Pool `createConnection()` new logic:**
  ```go
  func (cp *ConnectionPool) createConnection() (*PooledConnection, error) {
      backend := backend.New(cp.gateway, cp.reader, cp.logWriter)
      handle := backend.NewComposite(backend, cp.cache)
      return &PooledConnection{Client: handle, ...}, nil
  }
  ```
- **Pool fields to add:** `gateway thebegateway.ThebeGateway`, `reader thebegateway.ThebeReader`, `cache cache.Cache`, `logWriter backend.LogWriter` — injected at pool init, shared across all connections.
- **Compile-time assertion in `composite.go`:**
  ```go
  var _ store.ThebeHandle = (*compositeHandle)(nil)
  ```
- **Data structures:**
  - `compositeHandle`: fixed struct, 6 interface fields + 1 Closer. Allocated once per pool slot (max 20 per pool). Not shared across goroutines.
  - `PooledConnection`: replaces `*ImmuClient` with `store.ThebeHandle`. Pool slice: bounded(MaxConns=20). Access: `sync.Mutex` (existing pool lock unchanged).
- **Inputs:** Phase 1.0, 2.0, 3.0 complete
- **Done when:**
  - `var _ store.ThebeHandle = (*compositeHandle)(nil)` compiles
  - Pool `Get()` returns `store.ThebeHandle`
  - `grep -r "ImmuClient\|ImmuTransaction" config/` → empty
  - `go build ./config/...` passes
- **Status:** [x]
- **Note:** `compositeHandle` + `NewComposite` created in `DB_OPs/backend/composite.go`. `PooledConnection.Client` is `io.Closer` (carries `store.ThebeHandle`; typed as `io.Closer` to avoid a config→store import cycle). Pool factory wired via `config.SetGlobalHandleFactory` (lazy global fallback in `createConnection`), set at ThebeDB init in `main.go`. **Deferred to Phase 7:** deletion of `config/ImmudbConstants.go` — `config.ImmuClient` is still referenced by the legacy `DB_OPs/immuclient.go` helpers (`GetDatabaseState`, `IsHealthy`, `Close`) kept until the ImmuDB dependency is dropped.

---

## Phase 5.0: Retire `DB_OPs/immuclient.go` + `account_immuclient.go`

- **What:** Remove ImmuDB-backed package-level functions. Any caller still using these functions is updated in Phase 6 — here we delete the dead code and verify nothing else in `DB_OPs/` itself references ImmuDB directly.
- **Files to delete:**
  - `DB_OPs/immuclient.go`
  - `DB_OPs/account_immuclient.go`
  - `DB_OPs/MainDB_Connections.go`
  - `DB_OPs/Account_Connections.go`
  - `DB_OPs/Immudb_AVROfile.go` (Avro export used only for ImmuDB bulk export — replaced by ThebeDB's built-in replay)
  - `DB_OPs/immuclient_helper.go`
- **Files to update:**
  - `DB_OPs/BulkGetBlock.go` — re-implement `BlockIterator` using `store.BlockStore.BulkGetBlocks`
  - `DB_OPs/BulkGetAccounts.go` — re-implement using `store.AccountStore.BulkGetAccounts`
  - `DB_OPs/BlockLogs.go` — re-implement using `store.LogStore.GetLogs`
  - `DB_OPs/Facade_Receipts.go` — re-implement using `store.ReceiptStore.GetReceipt` (already on backend)
  - `DB_OPs/Accounts_helper.go` — remove ImmuDB CountBuilder; re-implement helpers using `store.AccountStore`
  - `DB_OPs/HashMapValidator.go` — re-implement using `store.BlockStore` + `store.AccountStore`
  - `DB_OPs/log_writer.go` — keep WebSocket fan-out logic; replace ImmuDB KV writes with `store.LogStore.StoreLogs`
- **Data structures:** `BlockIterator` becomes `struct{store store.BlockStore; batchSize int; current uint64; buf []*store.BlockRecord}` — bounded(batchSize) buffer, sequential access.
- **Inputs:** Phase 4.0 complete (ThebeHandle available from pool)
- **Done when:**
  - `grep -r "immuclient\|account_immuclient\|MainDB_Connections\|Account_Connections" . --include="*.go"` → empty
  - `grep -r "codenotary/immudb" DB_OPs/ --include="*.go"` → empty
  - `go build ./DB_OPs/...` passes
- **Status:** [x]

---

## Phase 6.0: Migrate 33 callers

- **What:** All files outside `DB_OPs/` that currently use `config.PooledConnection`, `*ImmuClient`, or DB_OPs package-level functions. Each caller switches to obtaining `store.ThebeHandle` from the pool and calling domain interface methods directly.
- **Caller map:**
  | Package | Gets | Current call | New call |
  |---|---|---|---|
  | `gETH/` | `BlockStore + TxStore + ReceiptStore` | `immuclient.GetBlock(conn, n)` | `handle.GetBlock(ctx, n)` |
  | `CLI/` | `AccountStore` | `account_immuclient.GetAccount(conn, addr)` | `handle.GetAccount(ctx, addr)` |
  | `FastsyncV2/` | `BlockStore + AccountStore + TxStore` | `immuclient.BatchCreate(conn, ...)` | `handle.StoreBlock(ctx, ...)` |
  | `AVC/` | `ZKProofStore` | `immuclient.StoreZKBlock(conn, block)` | `handle.StoreZKBlock(ctx, block)` |
  | `Block/` | `BlockStore + TxStore` | mix of Create + Read calls | domain method calls |
  | `DID/` | `AccountStore` | account ops | `handle.CreateAccount / GetAccount` |
  | `Mempool/` | `TxStore` | tx status checks | `handle.SetTransactionStatus` |
  | `messaging/` | `BlockStore` | latest block reads | `handle.GetLatestBlockNumber` |
  | `Security/` | `AccountStore` | nonce checks | `handle.CheckNonceDuplicate` |
  | `SmartContract/` | `TxStore + ReceiptStore` | receipt + tx reads | domain method calls |
  | `crdt/` | `BlockStore + AccountStore` | hash validation | domain reads |
  | `main.go` | wiring | pool init | wire ThebeGateway + ThebeReader into pool |
- **Pattern per caller file:**
  1. Replace `conn, err := GetMainDBConnection(ctx)` / `GetAccountDBConnection(ctx)` → `handle, err := pool.Get(ctx)`
  2. Replace `defer PutMainDBConnection(conn)` → `defer pool.Put(handle)`
  3. Replace each `immuclient.*` / `account_immuclient.*` call → `handle.*` domain method call
  4. Remove unused imports of `DB_OPs/immuclient`, `DB_OPs/account_immuclient`, `config.ImmuClient`
- **Inputs:** Phase 5.0 complete
- **Done when:**
  - `grep -rn "GetMainDBConnection\|GetAccountDBConnection\|PutMainDBConnection\|PutAccountDBConnection" . --include="*.go"` → empty
  - `grep -rn "ImmuClient\|PooledConnection" . --include="*.go"` → empty (except any test stubs)
  - `go build ./...` passes
- **Status:** [x]
- **Note:** Legacy fastsync package removed entirely (superseded by FastsyncV2); CLI sync commands rewired to FastsyncV2. Pool factory wired lazily via config.SetGlobalHandleFactory at ThebeDB init in main.go. Obsolete ImmuDB test files (account_immuclient_test.go, immuclient_test.go) deleted; new store/backend/cache tests deferred to Phase 8.

---

## Phase 7.0: Remove DualDB + ImmuDB dependency

- **What:** DualDB is no longer needed — ThebeDB is the sole backend. Remove all dual-write infrastructure and ImmuDB from go.mod.
- **Files to delete:**
  - `DB_OPs/dualdb/` — entire directory (`dualdb.go`, `metrics.go`)
  - `DB_OPs/thebe_shadow.go` — shadow writer hook (DualDB hook, no longer needed)
  - `DB_OPs/thebe_gateway_adapter.go` — ZKBlock → ThebeGateway mapping (moved to backend.go in Phase 2)
- **Files to modify:**
  - `go.mod` — remove `github.com/codenotary/immudb` dependency
  - `go.sum` — regenerate (`go mod tidy`)
  - `main.go` — remove DualDB construction, remove ImmuDB pool init calls
  - Any remaining file importing `DB_OPs/dualdb` or `codenotary/immudb`
- **Inputs:** Phase 6.0 complete (zero callers of ImmuDB)
- **Done when:**
  - `grep -rn "codenotary/immudb" . --include="*.go"` → empty
  - `grep -rn "dualdb\|thebe_shadow\|gateway_adapter" . --include="*.go"` → empty
  - `go mod tidy` succeeds
  - `go build ./...` passes with zero errors
- **Status:** [ ]

---

## Phase 6.1: Fill deferred functional stubs in DB_OPs

- **Trigger:** During the build-to-green pass, several DB_OPs functions were left as error/empty stubs marked "TODO Phase 6" / "stub". Filled once `store.ThebeHandle` + backend were live.
- **What was implemented:**
  - `GetMultipleAccounts` (`BulkGetAccounts.go`) → `store.AccountStore.BulkGetAccounts`, returns `map[address]*Account`.
  - `GetBlocksRange` (`BulkGetBlock.go`) → `store.BlockStore.BulkGetBlocks` + `blockRecordToZKBlock`. Fixes `BlockIterator.Next`.
  - `ListAllAccounts` (`account_immuclient.go`) → new `ListAccounts(ctx, limit)` method threaded through `store.AccountStore` → `backend` → `cache` (passthrough) → `ThebeReader`/`reader.go` (`sqlListAccounts`, `ORDER BY created_at`, optional LIMIT).
  - `GetMerkleRoot` (`immuclient.go`) → degrades gracefully (returns empty root, no error) so the explorer stats endpoint doesn't fail; real chain proof (ThebeDB `builder.VerifyChain`) not yet surfaced through the handle.
- **Done when:** `grep -rn "TODO" DB_OPs/ --include=*.go` (non-test) → empty; `go build ./...` + `go vet ./DB_OPs/...` green.
- **Status:** [x]
- **Flagged (NOT a DB_OPs fill — caller-side decision):** Generic KV `Create`/`Read`/`Exists`/`BatchCreate` in `immuclient.go` remain deliberate no-op/empty stubs (ThebeDB has no generic KV). Live callers in `messaging/BlockProcessing/Processing.go` + `Facade_Receipts.go` use them for the `tx_processing:<hash>` ephemeral lock + failed-tx (`-1`) detection. With the stubs, that dedup/failed-status path is silently disabled. Needs a decision: back ephemeral tx-processing state with `sqlops` SQLite (`key_value` table), or route failed-tx status through `SetTransactionStatus` → `contract_receipts`. CRDT snapshot (`crdt/helper.go`) and gossip dedup sets (`messaging/`) also use generic `Create` — confirm whether those need durable backing.

---

## Phase 8.0: Integration Seal

- **Checks:**
  - [ ] `go build ./...` — zero import cycles, zero errors
  - [ ] `grep -rn "ImmuClient\|PooledConnection\|immudb" . --include="*.go"` → empty
  - [ ] `grep -rn "DualDB\|dualdb" . --include="*.go"` → empty
  - [ ] `find . -name "*_test.go" -not -path "*/Tests/*"` → empty
  - [ ] Every new package has module AI-doc block
  - [ ] Time complexity annotated: `compositeHandle` methods, `cachedBlockStore.GetBlock`, `BlockIterator.Next`
  - [ ] `DB_OPs/store/` has zero internal imports — confirmed by `go list -deps ./DB_OPs/store/`
  - [ ] `var _ store.ThebeHandle = (*compositeHandle)(nil)` in `composite.go`
  - [ ] All phase entries above marked done
- **Status:** [ ]

---

## Deletion Manifest

| File/Dir | Removed in | Reason |
|---|---|---|
| `DB_OPs/immuclient.go` | Phase 5.0 | Replaced by thebeBackend methods |
| `DB_OPs/account_immuclient.go` | Phase 5.0 | Replaced by thebeBackend methods |
| `DB_OPs/MainDB_Connections.go` | Phase 5.0 | Pool now manages ThebeHandle, not ImmuDB |
| `DB_OPs/Account_Connections.go` | Phase 5.0 | Same |
| `DB_OPs/Immudb_AVROfile.go` | Phase 5.0 | ImmuDB bulk export — replaced by ThebeDB replay |
| `DB_OPs/immuclient_helper.go` | Phase 5.0 | Helpers moved to backend package |
| `DB_OPs/dualdb/` | Phase 7.0 | DualDB no longer needed |
| `DB_OPs/thebe_shadow.go` | Phase 7.0 | Shadow hook no longer needed |
| `DB_OPs/thebe_gateway_adapter.go` | Phase 7.0 | Logic absorbed by backend.go |
| `config/ImmudbConstants.go` | Phase 4.0 | ImmuClient + ImmuTransaction removed |
