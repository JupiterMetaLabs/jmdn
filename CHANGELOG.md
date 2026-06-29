# Changelog

All notable changes to JMDN are documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/),
adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **Docker deployment stack** (`Dockerfile`, `docker-compose.yml`,
  `Scripts/docker-entrypoint.sh`, `Scripts/bootstrap_sync.sh`, `DOCKER.md`).
  Multi-architecture container image (linux/amd64, linux/arm64, linux/arm/v7)
  built from `golang:1.25.3-bookworm` / `debian:bookworm-slim`. Includes
  Yggdrasil (required when `network.yggdrasil: true`). The `jmdn` user and
  group are pinned to UID/GID 3322 to match immudb container volume ownership.
  `docker-entrypoint.sh` handles root-to-unprivileged privilege drop (gosu),
  TLS certificate generation (CA + 8 service certs), and two deployment modes:
  embedded immudb or externally managed immudb (`IMMUDB_EXTERNAL=true`).
  `bootstrap_sync.sh` restores an immudb snapshot from GCS with MD5 verification
  and a sentinel guard (`.bootstrapped`) to skip restore on subsequent starts.
  `docker-compose.yml` composes immudb, Redis (AOF), and jmdn with an optional
  bootstrap profile. `DOCKER.md` is the full operator guide.

- **CatchUpSync — 8-phase post-bootstrap gap-fill protocol**
  (`FastsyncV2/catchup.go`).
  `HandleCatchUpSync(ctx, fromBlock, targetPeer)` fills header and data gaps
  accumulated after initial FastSync without repeating Merkle bisection.
  Phase 1: availability probe. Phase 2: header gap scan (`buildMissingTag`,
  O(n) cursor) and fetch. Phase 3: data gap scan (`buildDataMissingTag`, using
  a heuristic — empty StarkProof, or non-zero GasUsed with no transactions)
  and fetch, plus tagged-account scan over locally-held blocks. Phase 3.5:
  missing account fetch. Phase 4: account sync. Phase 5: delta reconciliation.
  Phase 6: re-auth (disabled; AUTH TTL = 48 h). Phase 7: PoTS. Phase 8:
  post-sync verification — re-runs the data gap scan and advances `latest_block`
  to the remote tip only on a clean pass. Batch size: 500.

- **`CatchUpSync` CLI RPC** (`CLI/proto/Connection.proto`,
  `CLI/proto/Connection.pb.go`, `CLI/proto/Connection_grpc.pb.go`,
  `CLI/client.go`).
  New `rpc CatchUpSync(CatchUpRequest) returns (SyncStats)` on `CLIService`.
  `CatchUpRequest` carries `peer string` and `from_block uint64`. Exposed as
  `jmdn -cmd catchup <peer-multiaddr> <from-block>`.

- **Single-pass delta reconciliation engine** (`FastsyncV2/deltas.go`).
  `computeAccountDeltas(fromBlock, toBlock)` performs one O(blocks) pass to
  compute per-account balance and nonce deltas, replacing the previous
  O(accounts × blocks) approach. Applies sender debit (value + gas fee, nonce
  advance), receiver credit, coinbase credit (gas/2, rounding remainder to
  coinbase), and ZKVM credit (gas/2). Gas price selection handles EIP-1559
  type-2 and legacy transactions. Used by both CatchUpSync and FastSync V2
  Phase 5. A SQLite key (`fastsync:last_reconciled_block`) anchors completed
  reconciliation ranges; `effectiveReconRange` and `markReconComplete` prevent
  double-counting on re-runs.

- **Canonical Merkle package** (`internal/merkle/builder.go`,
  `internal/merkle/hash.go`).
  `hashBlock` produces a canonical SHA-256 leaf hash over all `ZKBlock` fields
  (excluding `BlockHash`) using big-endian encoding, length-prefixed variable
  fields, nullable pointer flags, and a per-transaction sub-hash. The encoding
  is stable across node versions. `BuildLocalMerkleRoot` iterates from block 0
  to the current head, substituting zero-hashes for missing blocks. Replaces
  `DB_OPs/merkletree/merkle.go` (deleted).

- **SyncMonitor — background divergence detection daemon**
  (`internal/syncmonitor/monitor.go`, `internal/syncmonitor/monitor_test.go`).
  Runs on a configurable interval; builds the local Merkle root, reports it to
  the seednode via `ReportBlockState`, and triggers CatchUpSync when the
  seednode signals divergence. Six hardening measures: (1) startup jitter,
  (2) propagation guard — skips check if a block was received within 30 s,
  (3) consecutive threshold — requires two divergence detections before
  reconciling, (4) block-delta filter — ≤3 block difference treated as
  propagation lag, (5) seednode grace period — three consecutive RPC failures
  before marking seednode unreachable, (6) adaptive interval — 1.5× backoff
  when in sync, halved on divergence, implemented with `time.Timer`.
  Unit tests cover all six fixes. The monitor is wired in `main.go`; its
  reconcile function is activated only when `fastsync.enable_catchup: true`.

- **HTTP sync endpoints** (`gETH/Facade/rpc/sync_handlers.go`,
  `gETH/Facade/rpc/http_server.go`).
  `GET /sync/status` returns the current SyncMonitor state as JSON.
  `POST /sync/reconcile` triggers an immediate sync check.

- **`ReportBlockState` RPC on `PeerDirectory`**
  (`seednode/proto/seednode.proto`, `seednode/proto/seednode.pb.go`,
  `seednode/proto/seednode_grpc.pb.go`, `seednode/seednode.go`).
  New `BlockStateReport` message (peer ID, block head, 32-byte Merkle root,
  timestamp) and `BlockStateResponse` message (sync verdict, sequencer head and
  root, list of recommended sync peers). `seednode.Client.ReportBlockState`
  calls the RPC with a 15-second timeout and returns a `SyncStatus` struct.
  The proto source is renamed from `seednode.proto` to `peer.proto` and the
  Go package path updated to `gossipnode/seednode/proto;peerpb`.

- **`GetTransactionsByAccountInRange`** (`DB_OPs/account_immuclient.go`,
  `DB_OPs/Nodeinfo/immudb_account_manager.go`).
  Bounded block-range variant of the account transaction scan, used by the
  delta reconciliation engine.

- **`notifyBlockReceived` propagation signal**
  (`DB_OPs/Nodeinfo/immudb_adapter.go`, `DB_OPs/Nodeinfo/immudb_data_writer.go`,
  `DB_OPs/Nodeinfo/immudb_headers_writer.go`).
  `lastBlockReceivedNs atomic.Int64` updated after every successful `WriteData`
  and `WriteHeaders` call. Exposed as `LastBlockReceivedAt() time.Time` for the
  SyncMonitor propagation guard.

- **`DatabaseSettings.Address` and `DatabaseSettings.Port`**
  (`config/settings/config.go`, `config/settings/defaults.go`,
  `config/settings/loader.go`, `config/ImmudbConstants.go`, `main.go`,
  `jmdn_default.yaml`).
  ImmuDB target host and port are now configurable. `DBAddress` and `DBPort`
  are promoted from package constants to variables and overridden at startup
  from config. Defaults: `localhost:3322`.

- **`TxNonce` and `TxCountSent` fields on account update wire struct**
  (`DB_OPs/Nodeinfo/account_sync_worker.go`,
  `DB_OPs/Nodeinfo/immudb_account_manager.go`).
  Added to `accountUpdateWire` and propagated through `BatchUpdateAccounts`
  and `batchUpdateAccountsDirect`.

### Changed

- **`FastSyncSettings` config keys renamed and extended**
  (`config/settings/config.go`, `config/settings/defaults.go`,
  `config/settings/loader.go`).
  `pull_on_startup` removed (startup sync goroutine removed from `main.go`).
  `allowed_peers` removed. New keys: `enable_catchup` (bool, default `false`),
  `catch_up_from_block` (uint64, default `0`), `sync_check_interval` (duration,
  default `10m`).

- **FastSync V2 Phase 5 rewritten to use delta reconciliation**
  (`FastsyncV2/fastsyncv2.go`).
  Phase 5 and PoTS reconciliation now use `computeAccountDeltas` +
  `ReconcileWithDeltas` + `effectiveReconRange`/`markReconComplete`.
  `NewFastsyncV2` gains a `ctx context.Context` parameter.
  AccessList duplicate encoding bug fixed.

- **`latest_block` write consolidated to end of batch**
  (`DB_OPs/Nodeinfo/immudb_data_writer.go`).
  The per-block `latest_block` write is replaced by a single write at the end
  of each batch using `highestWritten` and `didWriteBlock` flags. Genesis block
  0 handled correctly.

- **Block iterator position correctness**
  (`DB_OPs/Nodeinfo/immudb_block_iterator.go`).
  Batch results are now placed into a positionally-correct nil-padded slice
  indexed by `BlockNumber`. Previously, when ImmuDB omitted a missing entry,
  subsequent blocks in the batch occupied wrong positions.

- **`configToFastsyncBlock` direct field assignment**
  (`DB_OPs/Nodeinfo/immudb_block_iterator.go`).
  Replaces a JSON marshal/unmarshal round-trip used to copy config block fields
  into the fastsync type.

- **Redis fallback for account writes**
  (`DB_OPs/Nodeinfo/immudb_account_manager.go`).
  `WriteAccounts` and `BatchUpdateAccounts` now fall back to direct ImmuDB
  writes (`writeAccountsDirect`, `batchUpdateAccountsDirect`) when Redis is
  unavailable or the enqueue fails.

- **`GetTransactionsForAccount` timeout extended; `GetTransactionsByAccount`
  timeout and batch size increased** (`DB_OPs/Nodeinfo/immudb_account_manager.go`,
  `DB_OPs/account_immuclient.go`).
  `GetTransactionsForAccount`: 10 s → 60 s.
  `GetTransactionsByAccount`: 8 s → 120 s; batch size 100 → 500; switched from
  per-block reads to `GetBlocksRange` batching.

- **`BulkGetBlock` context upgraded to timeout**
  (`DB_OPs/BulkGetBlock.go`).
  `context.WithCancel` replaced with `context.WithTimeout(30 s)`.

- **Seed server hardened** (`seed/seed.go`).
  Per-peer rate limiting: 5 registrations/hour, burst 5, lazy 2-hour eviction.
  Registry size cap: new registrations rejected when peer store reaches
  `MaxTrackedPeers`. All `fmt.Print*` replaced with structured `log.Printf`
  calls tagged `[seed]`.

- **`ConnectionPool.MaxConnections`** (`config/ConnectionPool.go`): 20 → 30.

### Removed

- **`DB_OPs/merkletree/merkle.go`** — replaced by `internal/merkle/`.

- **`FastSyncSettings.PullOnStartup` and `FastSyncSettings.AllowedPeers`** —
  removed along with the corresponding startup sync goroutine in `main.go`.

- **Debug `fmt.Printf` output from DB layer**
  (`DB_OPs/account_immuclient.go`, `DB_OPs/immuclient.go`).
  All `fmt.Printf(">>> [DB]...")` diagnostic calls removed from `GetAllKeys`,
  `getKeysBatch`, `SafeCreate`, and `GetZKBlockByNumber`.

### Performance

- **Explorer, JSON-RPC facade, and gRPC server switched to proof-free block reads**
  (`explorer/BlockOps.go`, `explorer/StreamTxns.go`, `explorer/utils.go`,
  `gETH/Facade/Service/Service.go`, `gETH/Facade/Service/Service_WS.go`,
  `gETH/gETH_Middleware.go`).
  Standard query endpoints previously called `GetZKBlockByNumber` /
  `GetZKBlockByHash`, which use ImmuDB's `VerifiedGet` path and generate a
  Merkle inclusion proof on every read. Routine queries — block-by-number,
  block-by-hash, latest block, transaction lookups, new-head subscriptions —
  do not require tamper-proof guarantees at the DB layer. Affected endpoints
  now use `ReadZKBlockByNumber` and `ReadZKBlockByHash` (plain `Get`, no proof):
  `getBlockByNumber`, `getBlock`, `listBlocks`, `getTransactionBlock`,
  `getLatestBlock`, `getLatestBlockStats`, `listTransactions_inBlock`,
  `getMissingBlocks`, `streamBlocks`, `checkForNewBlocks`, `BlockByNumber`,
  `TxByHash`, `getBlockForSubscription`, `_GetBlockByNumber`. Other endpoints
  in those files received context propagation only.

- **`ReadZKBlockByNumber`** (`DB_OPs/immuclient.go`).
  Renamed from `GetZKBlockByNumberFast` and extended with a `ctx context.Context`
  first parameter. Establishes a naming convention: `Read*` = plain `Get`, no
  Merkle proof (fast, for query and sync paths); `Get*` = `VerifiedGet`, Merkle
  proof (tamper-proof, for trust-critical paths). All call sites updated.

- **`ReadZKBlockByHash`** (`DB_OPs/immuclient.go`).
  New fast hash-based block lookup via plain `Get`, no proof generation.
  Two-step: `PREFIX_BLOCK_HASH + hash` → block key → block data. Used by
  `explorer/BlockOps.go` `getBlock` and `gETH/gETH_Middleware.go`
  `_GetBlockByHash`.

- **`withRetry` on `Read` and `SafeRead`** (`DB_OPs/immuclient.go`).
  The two lowest-level ImmuDB read primitives (`Get` and `VerifiedGet`) were
  the only read functions without retry logic. Both now wrapped with `withRetry`,
  matching the behaviour of write operations.

- **Context cancellation guards in block-scanning loops**
  (`DB_OPs/account_immuclient.go`, `DB_OPs/BlockLogs.go`,
  `explorer/utils.go`, `gETH/Facade/Service/Service_WS.go`).
  `ctx.Err() != nil` check added at the top of each iteration in
  `GetTransactionsByAccount`, `GetTransactionsByAccountPaginated`,
  `CheckNonceAndGetLatest` (outer and inner loops), `GetLogs` block scan,
  `checkForNewBlocks`, and the WS poller per-block loop. A cancelled or
  timed-out context now short-circuits immediately.

- **DB read functions now accept caller-owned context**
  (`DB_OPs/immuclient.go`, `DB_OPs/account_immuclient.go`,
  `DB_OPs/BlockLogs.go`, `DB_OPs/Facade_Receipts.go`,
  `DB_OPs/merkletree/merkle.go`,
  `DB_OPs/Nodeinfo/immudb_adapter.go`,
  `DB_OPs/Nodeinfo/immudb_headers_writer.go`,
  `Block/Server.go`, `Block/helper/stateroot.go`,
  `explorer/BlockOps.go`, `explorer/StreamTxns.go`, `explorer/utils.go`,
  `gETH/Facade/Service/Service.go`, `gETH/gETH_Middleware.go`,
  `gETH/Server.go`).
  `GetLatestBlockNumber`, `GetTransactionBlock`, and `ReadZKBlockByNumber`
  previously constructed their own `context.WithTimeout(context.Background(),
  5s)` internally, silently discarding HTTP handler deadlines and gRPC
  cancellations. All three functions now accept `ctx context.Context` as their
  first parameter. The hardcoded timeout is removed. HTTP handlers pass
  `c.Request.Context()`; gRPC handlers pass the RPC context; `GetLatestBlockNumber`
  nil-guards with `context.Background()` when a nil ctx is passed.

### Fixed

- **WebSocket block poller spawning duplicate goroutines**
  (`gETH/Facade/Service/Service_WS.go`).
  `startBlockPollerIfNeeded` had no concurrency guard. Two simultaneous
  `SubscribeNewHeads` calls could both pass the subscriber check and each
  launch a `pollForNewBlocks` goroutine, producing duplicate new-head
  notifications. Fixed with an `isPolling bool` flag inside the
  `newHeadsSubscriptions` mutex group; the flag is checked and set atomically
  under `Lock()` before any goroutine is launched. The subscriber-count check
  inside the poll loop upgraded from `RLock` to `Lock` to also reset
  `isPolling` when the map empties.

- **WebSocket new subscriber receiving historical blocks from genesis**
  (`gETH/Facade/Service/Service_WS.go`).
  `lastProcessedBlock` was initialised to 0, so a new subscriber would trigger
  emission of every block since genesis. `startBlockPollerIfNeeded` now reads
  `GetLatestBlockNumber` once on startup and seeds `lastProcessedBlock` before
  entering the poll loop.

- **`pollForNewBlocks` goroutine leaked on shutdown**
  (`gETH/Facade/Service/Service_WS.go`).
  The polling loop used `for range ticker.C` with no `ctx.Done()` arm; the
  goroutine had no exit path when the GRO context was cancelled. Replaced with
  `select { case <-ctx.Done(): return; case <-ticker.C: ... }`. `isPolling`
  reset to `false` when the subscriber map empties.

### Changed

- **`explorer/BlockOps_Helper.go` wrappers deleted** (`explorer/BlockOps_Helper.go`).
  `GetLatesBlockNumber` and `GetLatestBlockByNumber` were single-line
  pass-throughs to `DB_OPs` functions with no added logic. Both callers in
  `BlockOps.go` now call `DB_OPs` directly.

- **gRPC middleware context propagation** (`gETH/gETH_Middleware.go`,
  `gETH/Server.go`).
  All seven middleware functions (`_GetBlockByNumber`, `_GetBlockByHash`,
  `_GetTransactionByHash`, `_GetReceiptByHash`, `_GetAccountState`,
  `_SubmitRawTransaction`, `_GetChainID`) now accept `ctx context.Context` and
  forward it to DB calls. All seven corresponding gRPC server handlers pass
  their RPC context through. `_SubmitRawTransaction` had its own
  `context.WithCancel(context.Background())` removed. Commented-out DB init
  scaffolding cleaned up from five functions.

- **`.gitignore`** — `test_results/` added.

### Fixed

- **Consensus-not-reached propagated as an error**
  (`Sequencer/consensus_statemachine.go`, `Sequencer/Consensus.go`).
  `BroadcastAndProcessBlock` returned an error when a BFT quorum vote failed,
  causing `ProcessVoteCollection` to treat a valid consensus outcome as a node
  failure. A network experiencing peer churn could produce continuous false failures,
  masking real issues. `BroadcastAndProcessBlock` now returns `nil` on
  consensus-not-reached; the round ends cleanly and the next round begins normally.

- **Local block processing responsibility separated from broadcast**
  (`messaging/broadcast.go`, `Sequencer/consensus_statemachine.go`).
  `BroadcastBlockToEveryNodeWithExtraData` contained `ProcessBlockLocally` call sites
  that did not belong in the broadcast layer. Removed; local processing is now the
  exclusive responsibility of `BroadcastAndProcessBlock` in the consensus state machine.

- **Pubsub unsubscribe failure logged at wrong level**
  (`Pubsub/Subscription/Subscription.go`).
  Topic unsubscribe failure downgraded from `Error` to `Warn`.

### Changed

- **Trace context propagation through consensus pipeline**
  (`Sequencer/consensus_statemachine.go`, `Sequencer/Consensus.go`).
  `warmup`, `BroadcastAndProcessBlock`, and `CleanupSubscriptions` now accept and
  propagate `context.Context`. The active OTEL span is now correctly carried through
  the full consensus execution path, enabling end-to-end distributed tracing of each
  consensus round.

- **Structured logging across consensus internals**
  (`Sequencer/consensus_statemachine.go`, `Sequencer/Consensus.go`).
  All unstructured `log.Printf` and `fmt.Printf` calls replaced with
  `logger().NamedLogger` structured calls carrying span context and ion fields.
  Block number, block hash, and consensus outcome are now indexed on every relevant
  log entry, making per-block trace correlation possible in log aggregation.

- **Error wrapping** (`Sequencer/consensus_statemachine.go`, `messaging/broadcast.go`).
  `fmt.Errorf("...: %v", err)` → `fmt.Errorf("...: %w", err)` for proper
  `errors.Is` / `errors.As` unwrapping by callers.

- **Hot-path per-vote logging removed** (`Sequencer/Triggers/Maps/vote_results.go`).
  `StoreVoteResult` and `ClearVoteResults` emitted `log.Printf` on every call.
  Removed.

### Added

- **FastSync V2 engine** (`FastsyncV2/fastsyncv2.go` — new, 851 lines).
  Replaces the legacy sync engine to solve node data divergence that was blocking
  consensus: nodes with inconsistent account state could not agree on block validity.
  V2 introduces a structured multi-phase protocol over libp2p —
  PriorSync (Merkle root comparison) → HeaderSync (skeleton headers) →
  DataSync (full transactions + ZK proofs) → Reconciliation (account balances) →
  PoTS (catch-up on blocks produced during sync) — that brings any node to full,
  verified parity before it participates in consensus. The Reconciliation phase
  resolves account state divergence independently of block sync.
  CLI aliases `fastsync`, `fastsyncv2`, and `firstsync` all dispatch to the V2 engine.
  Serve and pull are decoupled: `fastsync.enabled` registers protocol handlers;
  `fastsync.enable_pulling` gates any write to the local database, allowing sequencers
  to serve data without accepting remote state.

- **`accountsync` CLI command and gRPC RPC.**
  Calls `FastsyncV2.AccountSyncOnly`, syncing only missing accounts from a peer
  without touching block data. Backed by `CLI.handleAccountSync`,
  `CLI_GRPC.HandleAccountSync`, and `GRPC_Server.AccountSync`.

- **Startup sync** (`FastsyncV2.HandleStartupSync`).
  When `fastsync.pull_on_startup: true`, the node automatically pulls blocks missed
  while offline, starting from the local chain tip. Registered as goroutine-orchestrator
  thread `thread:startup:sync` (`config/GRO/constants.go`).

- **Redis Stream account sync worker** (`DB_OPs/Nodeinfo/account_sync_redis.go`,
  `account_sync_worker.go`).
  Account writes are enqueued via `XADD` and consumed by a background worker
  (`XREADGROUP` / `XACK`), decoupling callers from ImmuDB's ~15 s commit latency.
  `enqueueRecordsChunked` splits payloads at `maxRecordsPerMessage` to prevent Redis
  bulk-string size violations. Node boots without Redis (async 30 s retry loop).

- **ImmuDB block adapters for V2** (`DB_OPs/Nodeinfo/`).
  Seven new files providing isolated ImmuDB read/write layers for the V2 engine:
  `immudb_adapter.go`, `immudb_auth.go`, `immudb_block_iterator.go`,
  `immudb_blockheader_iterator.go`, `immudb_block_nonheaders.go`,
  `immudb_data_writer.go`, `immudb_headers_writer.go`.

- **`AccountSnapshot` struct** (`messaging/BlockProcessing/Processing.go`).
  Captures `{Balance, TxNonce, TxCountSent, UpdatedAt}` for every affected account
  before a block is applied. Used by `rollbackState` to restore all four fields if any
  transaction in the block fails.

- **`FastSyncSettings`, `RedisSettings`, `DatabaseSettings` config structs**
  (`config/settings/config.go`).
  New `fastsync:` and `database.redis:` config sections. Full Viper defaults and
  env-var bindings in `defaults.go` and `loader.go`. `jmdn_default.yaml` updated to
  reflect all new fields with correct key names.

- **OTEL custom exporter headers** (`config/settings/config.go`,
  `logging/otelsetup/setup.go`).
  `Headers map[string]string` field on `LogOTELSettings`. Setup function uses
  field-by-field assignment so `Headers` propagates correctly.

- **`FastSyncV2` and `AccountSync` gRPC RPCs** (`CLI/proto/Connection.proto`).
  Two new methods on `CLIService`. Existing `FastSync` and `FirstSync` RPCs preserved.
  `convertDBState` in `GRPC_Server.go` nil-guards before dereferencing.

- **`GetZKBlockByNumberFast`** (`DB_OPs/immuclient.go`).
  Proof-free block retrieval via plain `Get` (5–10× faster than `GetZKBlockByNumber`),
  for sync/reconciliation paths that do not require tamper-proof reads.

- **`PullAllowed` flag on `CommandHandler`** (`CLI/CLI.go`).
  Set from `fastsync.enable_pulling` at startup. All pull-capable CLI and gRPC
  handlers check it and return an error if false.

- **Security service Viper defaults** (`config/settings/loader.go`).
  All predefined `security.services.*` entries registered with `SetDefault`, enabling
  full env-var override of nested service policies.

- **`account_sync_enqueue_test.go`.**
  Unit tests for bounded-enqueue chunking logic using a recording mock streamer;
  no live Redis or ImmuDB required.

- **`jmdn.yaml` added to `.gitignore`.**
  Production node config is now excluded from version control by default, preventing
  accidental credential commits. Also added: `internal/WAL/.tmp/*`, `.claude/*`,
  `.code-review-graph/*`, `.cursor/*`.

### Fixed

- **Same-block nonce replay — two-layer defence.**

  *Layer 1 (admission, `Security/Security.go`):* Nonce validation now reads
  `account.TxNonce` from `SecurityCache` instead of querying ImmuDB.
  `UpdateTxNonce` advances the in-memory value immediately on each accepted
  transaction, so a second tx from the same sender in the same block is rejected at
  the gate.

  *Layer 2 (execution, `messaging/BlockProcessing/Processing.go`):*
  `deductFromSender` performs a second nonce check against the DB record
  (`tx.Nonce < didDoc.TxNonce`) as defense-in-depth. It also writes
  `TxNonce = tx.Nonce + 1` and `TxCountSent++` to ImmuDB via `DB_OPs.UpdateAccount`,
  making nonce state durable beyond cache lifetime.

- **`PutNonceofAccount` ART key collision** (`DB_OPs/account_immuclient.go`).
  Function packed `time.Now().UnixNano()` and an atomic counter into the ART key;
  under concurrency the counter collided and the timestamp was approaching overflow.
  Removed; `CreateAccount` now calls `GenerateARTNonce()`. Corresponding test
  `Test_Account_Nonce_Generation` removed.

- **`defer ctx.Done()` context leak in `CLI/client.go` `FastSync`.**
  `defer ctx.Done()` is a no-op on a `context.Background()` (returns a nil channel),
  but signals incorrect intent and masks real context lifecycle. Removed.

- **Block processing rollback left dirty nonce state**
  (`messaging/BlockProcessing/Processing.go`).
  `rollbackBalances` restored only `Balance`. Replaced by `rollbackState`, which
  overwrites `Balance`, `TxNonce`, `TxCountSent`, and `UpdatedAt` from the pre-block
  snapshot. Per-tx nested rollback inside `processTransaction` removed; `rollbackState`
  at block level is the sole rollback authority.

- **`BatchRestoreAccounts` duplicate-key error** (`DB_OPs/account_immuclient.go`).
  Reconciliation pages can deliver the same address multiple times. Deduplication
  (LWW by `UpdatedAt`) now applied before `ExecAll`.

- **`BatchRestoreAccounts` DID and metadata loss**
  (`DB_OPs/account_immuclient.go`, `DB_OPs/Nodeinfo/account_sync_worker.go`).
  Field-merges `DIDAddress`, `CreatedAt`, `AccountType`, and `Metadata` from the
  existing DB record before writing, preventing data loss for active accounts.

- **`getKeysBatch` prefix scan returning wrong results** (`DB_OPs/immuclient.go`).
  `Desc: true` → `Desc: false`. Descending scans with no matching keys fall backward
  past the prefix boundary and return unrelated entries.

- **Pubsub topic close race** (`Pubsub/Subscription/SubscriptionManager.go`).
  Both `Unsubscribe` and `Shutdown` called `managed.pubsubTopic.Close()` on a locally
  cached reference, racing with concurrent re-subscribe. Both now call
  `sm.gps.CloseTopic(topic)`.

- **`HeadersWriter` prematurely advancing `latest_block` marker**
  (`DB_OPs/Nodeinfo/immudb_headers_writer.go`).
  HeaderSync writes skeleton blocks before transactions are available; the marker was
  being updated, causing `StartupSync` and the explorer to consider the node fully
  synced. Marker is now snapshotted before `WriteHeaders` and restored unconditionally
  after.

- **Merkle hash divergence on fast-synced nodes**
  (`DB_OPs/Nodeinfo/immudb_block_nonheaders.go`, `immudb_data_writer.go`,
  `immudb_headers_writer.go`).
  `ChainID`, `AccessList`, and `LogsBloom` were not serialised in V2 protobufs.
  All three fields now round-trip correctly.

- **`CheckNonceAndGetLatest` uint64 underflow on fresh chains** (#22).
  Inner loop `for i := currentBlock; i >= startBlock; i--` wrapped to `math.MaxUint64`
  when `startBlock == 0`. Restructured as a top-decrement loop.

- **P2P DID gossip discarded network ART Nonce** (`messaging/DIDPropagation.go`).
  `CreateAccount` assigned a fresh local `Nonce`, diverging from the sender's ART
  index. Changed to `StorePropagatedAccount`, which writes the exact received `Nonce`.

- **`immudb_account_manager` key-not-found** (`DB_OPs/Nodeinfo/immudb_account_manager.go`).
  `GetAccountByAddress` returns zero balance on a missing key rather than an error.

- **`GetLatestBlockNumber` non-deterministic** (`DB_OPs/immuclient.go`).
  Retry-with-reconciliation wrapper removed; single direct read.

- **`eth_getBalance` error on unknown address** (`gETH/Facade/Service/Service.go`).
  On key-not-found: attempts `CreateAccountandPropagateDID` (error logged, not
  returned); always returns `big.NewInt(0)`.

- **Go 1.25 deprecation warnings** (`AVC/BLS/bls-sign/bls-sgin.go`, `seednode/seednode.go`).
  `ioutil.ReadFile/WriteFile` → `os.ReadFile/WriteFile`; `reflect.Ptr` → `reflect.Pointer`.

### Changed

- **Account struct** (`DB_OPs/account_immuclient.go`).

  | Field | Before | After | Purpose |
  |---|---|---|---|
  | `Nonce` (formerly `StateID`) | `time.Now()`-based ART key | deterministic (`GenerateARTNonce`) | Fastsync ART leaf index |
  | `TxNonce` | — | new `uint64` | Ethereum transaction nonce |
  | `TxCountSent` | — | new `uint64` | analytical send counter |

- **`UpdateAccountBalance` signature** (`DB_OPs/account_immuclient.go`).
  Added `blockTimestamp int64`; `UpdatedAt` is now deterministic across nodes.

- **`SecurityCache` method renames** (`Security/security_cache.go`).
  `UpdateNonce` → `UpdateTxNonce`; `GetNonce` → `GetTxNonce`.

- **`firstsync` command mode argument removed** (`main.go`, `CLI/CLI.go`).
  `jmdn -cmd firstsync <peer> <server|client>` no longer accepts a mode argument.
  All three aliases (`fastsync`, `fastsyncv2`, `firstsync`) now route to the V2 engine
  with a single `<peer>` argument. Scripts using `firstsync … server` or
  `firstsync … client` must be updated.

- **Block transaction ordering** (`messaging/BlockProcessing/Processing.go`).
  `sortTransactionsByNonce` removed. Sequencer-determined order is canonical.

- **`processTransaction`, `deductFromSender`, `addToRecipient` signatures**.
  All accept `blockTimestamp int64`. `deductFromSender` now takes
  `*config.Transaction` (full tx) to support the execution-time nonce check.

- **`BatchRestoreAccounts` signature** (`DB_OPs/account_immuclient.go`).
  `context.Context` as first param; operations chunked at 1000 per ImmuDB tx;
  single `GetAll` RPC replaces per-account `Get` calls.

- **Vote submission logging** (`Vote/Trigger.go`).
  `SubmitVote` now logs the target peer ID on each retry failure and on success,
  replacing a generic error message. Aids diagnosis of vote propagation issues.

- **`SyncStats.Error` checked in CLI output** (`main.go`).
  `fastsync`, `fastsyncv2`, `firstsync`, and `accountsync` commands now print a
  specific failure message and exit non-zero when `stats.Error` is non-empty, instead
  of silently succeeding with zero stats.

- **`TimeTaken` unit in CLI output** (`main.go`).
  Sync duration now printed as seconds (`%ds`) instead of milliseconds (`%dms`),
  matching the `SyncStats.TimeTaken` field unit.

- **`DID.RegisterDID` timestamps** (`DID/DID.go`). `UnixNano()` instead of `Unix()`.

- **HTTP server timeouts** (`explorer/api.go`). 10 s → 60 s.

- **Legacy FastSync V1** (`fastsync/fastsync.go`).
  `BatchRestoreAccounts` call updated to new signature (`context.Background()`).

### Dependencies

| Package | Change |
|---|---|
| `protoc` (build tool) | `v6.33.1` → `v7.34.1`; proto source path normalised |
| `JupiterMetaLabs/JMDN-FastSync` | Added — `v0.0.0-20260604113915-c1470ecc039d` |
| `redis/go-redis/v9` | Added — `v9.19.0` |
| `shirou/gopsutil` | Added — `v3.21.11+incompatible` (indirect) |
| `JupiterMetaLabs/JMDN_Merkletree` | `v0.0.0-20260205…` → `v0.0.0-20260413…` |
| `JupiterMetaLabs/ion` | `v0.3.5` → `v0.4.2` |
| `go.opentelemetry.io/otel` | `v1.40.0` → `v1.42.0` |
| `google.golang.org/grpc` | `v1.78.0` → `v1.79.3` |
| `grpc-ecosystem/grpc-gateway/v2` | `v2.27.3` → `v2.28.0` |
| `klauspost/compress` | `v1.18.2` → `v1.18.5` |

---

## [1.1.1] - 2026-04-24

### Added
- CERT-IN security audit certificate (TERA/CERT-IN/03/2026/CR/16)
  with verification instructions ([VERIFICATION.md](./audits/2026-03-terasoft-certin-vapt/VERIFICATION.md))
- Trusted clients configuration for rate-limit bypass (#20)
- Security audit badges and section in README

### Fixed
- Initialize expectedChainID at startup, independent of BlockGen —
  fixes crash on non-sequencer nodes (#21)
- Alerts viper bindings and centralized config access (#17)
- Replace hardcoded web3_clientVersion with build-flag driven version (#26)

### Changed
- Lazy load alerts service and isolate configuration (#16)
- CI workflows now trigger on release branches (#15)

### Removed
- Internal pre-release analysis files containing sensitive findings
- Temporary rollout observability logs (#18)

## [1.1.0] - 2026-03-09

### Added
- Initial public release of JMDN
- Open source release baseline documentation
- SonarQube pipeline configuration
- Rate limiting and security hardening (#3)
- File/directory permission tightening and ReadHeaderTimeout (#4)
- Parameterization of systemd SERVICE_USER (#11)

### Fixed
- SQL injection findings in sqlops using pre-built statements (#12)
- Dynamic SQL execution warnings resolved (#9)
- Config viper override merging (#7)
- Staticcheck formatting and redundant types (#6)

## [1.0.0] - 2026-02-24

### Added
- Initial open source release

[Unreleased]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.1.1...HEAD
[1.1.1]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/JupiterMetaLabs/jmdn/releases/tag/v1.0.0
