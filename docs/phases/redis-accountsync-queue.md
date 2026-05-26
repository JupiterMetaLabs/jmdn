# Redis AccountSync Queue — Implementation Phases

## Context

**Problem:** `BatchRestoreAccounts` (ImmuDB commit) takes ~15 s. AccountSync callers time
out waiting, push to DLQ, retry, and waste throughput.

**Solution:** `WriteAccounts` and `BatchUpdateAccounts` enqueue payloads to a Redis Stream
and return an immediate ACK. A single background worker (`XREADGROUP` + `XAUTOCLAIM`)
drains the stream, coalesces batches, and writes to ImmuDB asynchronously.

## Design Decisions (locked)

| Decision | Choice | Rationale |
|---|---|---|
| Interface contract | Unchanged (`types.AccountManager`) | External module; signatures fixed |
| Redis unavailable | Fail fast | Caller already has DLQ/retry; B degrades to 15 s latency |
| Worker lifecycle | Explicit `StartAccountSyncWorker(ctx, streamer, cfg)` from main.go | main.go owns all infra lifecycles |
| Queue mechanism | Redis Streams (`XADD`/`XREADGROUP`/`XACK`/`XAUTOCLAIM`) | Built-in PEL, ACK semantics, crash recovery |
| Batch coalescing | Drain `MaxDrainItems` entries per `XREADGROUP`; write in `MaxAccountsPerBatch` sub-batches | Reduces DB round trips under burst |
| ACK semantics | ACK only after `BatchRestoreAccounts` succeeds | At-least-once; `BatchRestoreAccounts` is LWW-idempotent |
| Redis client injection | Interface `RedisStreamer` injected via `StartAccountSyncWorker`; `NewRedisStreamer(*redis.Client)` adapter in package | DIP; no concrete cross-package import |

## SOLID Gates

**S — Single Responsibility**
- `account_sync_redis.go`: owns "define the Redis stream transport abstraction"
- `account_sync_worker.go`: owns "drain Redis stream → write to ImmuDB (at-least-once)"
- `immudb_account_manager.go`: owns "enqueue account sync payloads and return ACK immediately"

**O — Open/Closed**
Extension point: new payload types (e.g., DID sync) → add `case` in `processBatch` switch +
new `enqueue*` helper in `immudb_account_manager.go`. Worker loop and stream infra untouched.

**I — Interface Segregation**
`RedisStreamer` has exactly 5 methods: `Enqueue`, `EnsureConsumerGroup`, `ReadGroup`, `Ack`,
`AutoClaim`. All 5 are used by the worker. No caller sees unused methods.

**D — Dependency Inversion**
Worker and account_manager both depend on `RedisStreamer` (interface in this package).
Only `redisStreamerAdapter` imports `*redis.Client` (concrete, local to the adapter).
No concrete cross-package import anywhere else in `DB_OPs/Nodeinfo`.

## Pattern Selection

**Primary pattern: Adapter** (Structural)
`redisStreamerAdapter` adapts the concrete `*redis.Client` API to the domain `RedisStreamer`
interface. Callers depend on the interface; the adapter is the only concrete import.

**Secondary: Command** (Behavioral)
Each stream entry is a serialized command (account write operation) consumed by the worker.
Enables at-least-once replay via PEL without reissuing the original RPC.

**Anti-pattern avoided:** Direct concrete dependency on `*redis.Client` throughout
`DB_OPs/Nodeinfo` — would couple the package to a specific Redis client library forever.

---

## Phase 1.0: RedisStreamer interface + adapter
- **What:** New file `account_sync_redis.go`.
  - `StreamEntry` struct
  - `RedisStreamer` interface (5 methods; no go-redis types exposed)
  - `redisStreamerAdapter` wrapping `*redis.Client`
  - `NewRedisStreamer(*redis.Client) RedisStreamer` factory
  - Package-level `pkgStreamer`/`pkgStreamerMu` + `setStreamer`/`getStreamer`
  - Stream constants: `accountSyncStream`, `accountSyncGroup`, `accountSyncConsumer`
  - Payload type constants: `payloadTypeAccounts`, `payloadTypeUpdates`
- **Data structures:**
  - `StreamEntry`: ephemeral per read; unbounded count, capped by `MaxDrainItems` at call site.
  - `pkgStreamer`: singleton reference; set once by Phase 2's `StartAccountSyncWorker`.
- **Inputs:** none
- **Done when:** package compiles; `NewRedisStreamer` returns a non-nil `RedisStreamer`
- **Status:** [x]

## Phase 2.0: Worker — `account_sync_worker.go`
- **What:** New file with:
  - `AccountSyncWorkerConfig` struct + `DefaultWorkerConfig()`
  - `StartAccountSyncWorker(ctx, streamer, cfg) error`
  - `runWorker` (XREADGROUP BLOCK loop, ctx-aware exit)
  - `reclaimPending` (XAUTOCLAIM on startup for crash recovery)
  - `processBatch` (parse → coalesce → sub-batch write → ACK; poison pill handling)
  - `parseAccountsPayload` / `parseUpdatesPayload`
  - `accountUpdateWire` (stable JSON wire type for `types.AccountUpdate`)
  - `dbEntry` type alias for `struct { Key string; Value []byte }`
- **Data structures:**
  - `[]StreamEntry`: ephemeral per `runWorker` iteration; bounded by `MaxDrainItems` (100)
  - `[]dbEntry`: ephemeral per `processBatch`; bounded by `MaxDrainItems × avg-accounts-per-payload`; sub-batched by `MaxAccountsPerBatch` (500)
  - PEL (Redis-side): unbounded count of unacked entries; evicted by XAUTOCLAIM after `PendingIdleTimeout` (30 s)
- **Inputs:** Phase 1.0 complete
- **Done when:** `StartAccountSyncWorker` compiles; worker exits cleanly on ctx cancel
- **Status:** [x]

## Phase 3.0: Modify `immudb_account_manager.go`
- **What:**
  - `WriteAccounts` → `getStreamer()` → `json.Marshal(accounts)` → `s.Enqueue(...)` → return
  - `BatchUpdateAccounts` → convert to `[]accountUpdateWire` → `json.Marshal` → `s.Enqueue(...)` → return
  - Remove: direct `DB_OPs.GetAccountConnectionandPutBack` + `DB_OPs.BatchRestoreAccounts` calls from these two methods
- **Data structures:** none introduced; removes ephemeral `[]struct{Key,Value}` from both methods
- **Inputs:** Phase 2.0 complete (`accountUpdateWire` defined there; same package)
- **Done when:** `go build ./DB_OPs/Nodeinfo/...` succeeds; both methods no longer block on ImmuDB
- **Status:** [x]

## Phase 4.0: main.go wiring (caller's responsibility)
- **What:** In main.go (or lifecycle coordinator), after Redis client is initialized:
  ```go
  streamer := NodeInfo.NewRedisStreamer(redisClient)
  if err := NodeInfo.StartAccountSyncWorker(rootCtx, streamer, NodeInfo.DefaultWorkerConfig()); err != nil {
      log.Fatalf("account sync worker: %v", err)
  }
  ```
- **Inputs:** Phase 3.0 complete
- **Done when:** node boots, worker log line appears, WriteAccounts returns in < 100 ms
- **Status:** [ ] — caller's responsibility
