# Changelog

All notable changes to JMDN are documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/),
adhering to [Semantic Versioning](https://semver.org/).

## [1.2.0] — 2026-06-29

### Added

**Docker & Deployment**

- **Docker deployment stack** (`Dockerfile`, `docker-compose.yml`,
  `Scripts/docker-entrypoint.sh`, `Scripts/bootstrap_sync.sh`, `DOCKER.md`).
  Multi-architecture image (linux/amd64, linux/arm64, linux/arm/v7) built from
  `golang:1.25.3-bookworm` / `debian:bookworm-slim`. Includes Yggdrasil
  (required when `network.yggdrasil: true`). The `jmdn` user/group are pinned
  to UID/GID 3322 to match immudb container volume ownership.
  `docker-entrypoint.sh`: root-to-unprivileged privilege drop (gosu), TLS
  certificate generation (CA + 8 service certs), two deployment modes —
  embedded immudb or externally managed (`IMMUDB_EXTERNAL=true`).
  `bootstrap_sync.sh`: GCS snapshot restore with MD5 verification and a
  `.bootstrapped` sentinel to skip restore on subsequent starts.
  `docker-compose.yml`: immudb, Redis (AOF), and jmdn with optional bootstrap
  profile. `DOCKER.md` is the full operator guide.

**Sync Protocol**

- **FastSync V2 engine** (`FastsyncV2/fastsyncv2.go`).
  Replaces the legacy sync engine. V2 introduces a structured 5-phase protocol
  over libp2p — PriorSync → HeaderSync → DataSync → Reconciliation → PoTS —
  that brings any node to full, verified parity before it participates in
  consensus. Resolves account state divergence independently of block sync.
  CLI aliases `fastsync`, `fastsyncv2`, and `firstsync` all dispatch to V2.
  `fastsync.enabled` registers protocol handlers; `fastsync.enable_pulling`
  gates all local DB writes, allowing sequencers to serve without accepting
  remote state.

- **CatchUpSync — 8-phase post-bootstrap gap-fill** (`FastsyncV2/catchup.go`).
  `HandleCatchUpSync(ctx, fromBlock, targetPeer)` fills header and data gaps
  without repeating Merkle bisection. Phases: (1) availability probe,
  (2) header gap scan + fetch, (3) data gap scan + fetch + tagged-account scan,
  (3.5) missing account fetch, (4) account sync, (5) delta reconciliation,
  (6) re-auth (disabled; AUTH TTL = 48 h), (7) PoTS, (8) post-sync
  verification — advances `latest_block` to remote tip only on clean pass.

- **Single-pass delta reconciliation engine** (`FastsyncV2/deltas.go`).
  `computeAccountDeltas(fromBlock, toBlock)` — one O(blocks) pass; replaces
  O(accounts × blocks) per-account scan. Applies sender debit, receiver credit,
  coinbase credit (gas/2, remainder to coinbase), ZKVM credit (gas/2).
  EIP-1559 and legacy gas price selection. SQLite anchor
  (`fastsync:last_reconciled_block`) via `effectiveReconRange` /
  `markReconComplete` prevents double-counting on re-runs.

- **Canonical Merkle package** (`internal/merkle/`).
  `hashBlock` — canonical SHA-256 leaf hash over all `ZKBlock` fields
  (excluding `BlockHash`); big-endian, length-prefixed, nullable pointer flags,
  per-tx sub-hash; encoding stable across node versions.
  `BuildLocalMerkleRoot` — iterates 0..head, zero-hashes missing blocks.
  Replaces `DB_OPs/merkletree/merkle.go` (deleted).

- **SyncMonitor — background divergence detection** (`internal/syncmonitor/`).
  Builds local Merkle root on a configurable interval, reports to seednode via
  `ReportBlockState`, triggers CatchUpSync on divergence. Six hardening
  measures: startup jitter, propagation guard (30 s window), consecutive
  threshold (2 detections before reconcile), block-delta filter (≤3 blocks =
  propagation lag), seednode grace period (3 failures before marking
  unreachable), adaptive interval (1.5× backoff in sync / halve on divergence,
  `time.Timer`). Unit tests cover all six. Activated when
  `fastsync.enable_catchup: true`.

- **`ReportBlockState` RPC on `PeerDirectory`** (`seednode/proto/`).
  `BlockStateReport` (peer ID, block head, 32-byte Merkle root, timestamp) →
  `BlockStateResponse` (sync verdict, sequencer head/root, recommended sync
  peers). `seednode.Client.ReportBlockState`: 15 s timeout. Proto source
  renamed to `peer.proto`; Go package path updated to
  `gossipnode/seednode/proto;peerpb`.

**CLI & RPC**

- **`CatchUpSync` CLI RPC** (`CLI/proto/Connection.proto`).
  `rpc CatchUpSync(CatchUpRequest) returns (SyncStats)`. Exposed as
  `jmdn -cmd catchup <peer-multiaddr> <from-block>`.

- **`FastSyncV2` and `AccountSync` gRPC RPCs** (`CLI/proto/Connection.proto`).
  Two new methods on `CLIService`. Existing `FastSync` / `FirstSync` preserved.

- **`accountsync` CLI command** — syncs only missing accounts from a peer
  without touching block data. Backed by `CLI_GRPC.HandleAccountSync` /
  `GRPC_Server.AccountSync`.

- **HTTP sync endpoints** (`gETH/Facade/rpc/sync_handlers.go`).
  `GET /sync/status` — current SyncMonitor state as JSON.
  `POST /sync/reconcile` — immediate sync check.

**Database & Storage**

- **Redis Stream account sync worker** (`DB_OPs/Nodeinfo/account_sync_redis.go`,
  `account_sync_worker.go`). Account writes enqueued via `XADD`, consumed by
  background worker (`XREADGROUP` / `XACK`), decoupling callers from ImmuDB's
  ~15 s commit latency. Node boots without Redis (async 30 s retry loop).

- **ImmuDB block adapters for V2** (`DB_OPs/Nodeinfo/`).
  Seven new files providing isolated read/write layers for the V2 engine:
  `immudb_adapter.go`, `immudb_auth.go`, `immudb_block_iterator.go`,
  `immudb_blockheader_iterator.go`, `immudb_block_nonheaders.go`,
  `immudb_data_writer.go`, `immudb_headers_writer.go`.

- **`GetTransactionsByAccountInRange`** (`DB_OPs/account_immuclient.go`).
  Bounded block-range scan; used by the delta reconciliation engine.

- **`notifyBlockReceived` propagation signal** (`DB_OPs/Nodeinfo/immudb_adapter.go`).
  `lastBlockReceivedNs atomic.Int64` updated after every `WriteData` /
  `WriteHeaders`. Exposed as `LastBlockReceivedAt() time.Time` for the
  SyncMonitor propagation guard.

**Configuration**

- **`FastSyncSettings`, `RedisSettings`, `DatabaseSettings` config structs**
  (`config/settings/`). New `fastsync:` and `database.redis:` sections.
  Full Viper defaults and env-var bindings. `jmdn_default.yaml` updated.

- **`DatabaseSettings.Address` and `DatabaseSettings.Port`**.
  ImmuDB target configurable at runtime. `DBAddress` / `DBPort` promoted from
  constants to variables, overridden at startup. Defaults: `localhost:3322`.

**Security & Observability**

- **`AccountSnapshot` struct** (`messaging/BlockProcessing/Processing.go`).
  Captures `{Balance, TxNonce, TxCountSent, UpdatedAt}` before each block;
  used by `rollbackState` on any transaction failure.

- **`PullAllowed` flag on `CommandHandler`** (`CLI/CLI.go`).
  Set from `fastsync.enable_pulling`. All pull-capable handlers check it and
  reject if false.

- **OTEL custom exporter headers** (`config/settings/`, `logging/otelsetup/`).
  `Headers map[string]string` on `LogOTELSettings`; field-by-field assignment
  ensures propagation through setup.

- **Security service Viper defaults** (`config/settings/loader.go`).
  All `security.services.*` entries registered with `SetDefault` for full
  env-var override.

- **`account_sync_enqueue_test.go`** — unit tests for chunked-enqueue logic
  using a recording mock; no live Redis or ImmuDB required.

- **`jmdn.yaml` added to `.gitignore`** — prevents accidental credential
  commits. Also added: `internal/WAL/.tmp/*`, `.claude/*`, `.cursor/*`.

### Changed

**Sync**

- **FastSync V2 Phase 5 rewritten to delta reconciliation**
  (`FastsyncV2/fastsyncv2.go`). Uses `computeAccountDeltas` +
  `ReconcileWithDeltas` + `effectiveReconRange` / `markReconComplete`.
  `NewFastsyncV2` gains `ctx context.Context`. AccessList duplicate encoding
  fixed.

- **`FastSyncSettings` keys updated** (`config/settings/`).
  Removed: `pull_on_startup`, `allowed_peers`.
  Added: `enable_catchup` (bool, default `false`), `catch_up_from_block`
  (uint64, default `0`), `sync_check_interval` (duration, default `10m`).

- **`firstsync` mode argument removed** (`main.go`, `CLI/CLI.go`).
  `jmdn -cmd firstsync <peer> <server|client>` → `jmdn -cmd firstsync <peer>`.
  All three aliases route to V2. ⚠️ Scripts using `firstsync … server` or
  `firstsync … client` must be updated.

**Database**

- **`latest_block` write consolidated to end of batch**
  (`DB_OPs/Nodeinfo/immudb_data_writer.go`). Single write using
  `highestWritten` / `didWriteBlock` flags; genesis block 0 handled correctly.

- **Block iterator position correctness**
  (`DB_OPs/Nodeinfo/immudb_block_iterator.go`). Positionally-correct
  nil-padded slice indexed by `BlockNumber`; prevents position corruption when
  ImmuDB omits missing entries.

- **`configToFastsyncBlock` direct field assignment** — replaces JSON
  round-trip with direct struct field copy.

- **Redis fallback for account writes** (`DB_OPs/Nodeinfo/immudb_account_manager.go`).
  `WriteAccounts` / `BatchUpdateAccounts` fall back to direct ImmuDB writes
  when Redis is unavailable or enqueue fails.

- **DB timeouts and batch sizes increased**.
  `GetTransactionsForAccount`: 10 s → 60 s.
  `GetTransactionsByAccount`: 8 s → 120 s; batch 100 → 500; uses
  `GetBlocksRange`. `BulkGetBlock`: `WithCancel` → `WithTimeout(30 s)`.

- **Account struct** (`DB_OPs/account_immuclient.go`).

  | Field | Before | After | Purpose |
  |---|---|---|---|
  | `Nonce` (formerly `StateID`) | `time.Now()`-based | `GenerateARTNonce()` | deterministic ART leaf index |
  | `TxNonce` | — | `uint64` | Ethereum transaction nonce |
  | `TxCountSent` | — | `uint64` | send counter |

- **`UpdateAccountBalance` signature** — adds `blockTimestamp int64`;
  `UpdatedAt` is now deterministic across nodes.

- **`BatchRestoreAccounts` signature** — `context.Context` first param;
  operations chunked at 1000 per ImmuDB tx; single `GetAll` replaces
  per-account `Get` calls.

- **`SecurityCache` method renames** — `UpdateNonce` → `UpdateTxNonce`;
  `GetNonce` → `GetTxNonce`.

- **`TxNonce` and `TxCountSent` on account update wire struct**
  (`DB_OPs/Nodeinfo/account_sync_worker.go`). Propagated through
  `BatchUpdateAccounts` and `batchUpdateAccountsDirect`.

**Consensus & Messaging**

- **Trace context propagation through consensus pipeline**
  (`Sequencer/consensus_statemachine.go`, `Sequencer/Consensus.go`).
  `warmup`, `BroadcastAndProcessBlock`, `CleanupSubscriptions` propagate
  `context.Context`; active OTEL span carried end-to-end.

- **Structured logging across consensus internals** — all `log.Printf` /
  `fmt.Printf` replaced with `logger().NamedLogger` structured calls carrying
  span context, block number, hash, and consensus outcome.

- **Error wrapping** — `fmt.Errorf("…: %v", err)` → `fmt.Errorf("…: %w", err)`
  throughout `Sequencer/` and `messaging/`.

- **Hot-path per-vote logging removed** (`Sequencer/Triggers/Maps/vote_results.go`).
  `StoreVoteResult` and `ClearVoteResults` no longer emit on every call.

- **Block transaction ordering** — `sortTransactionsByNonce` removed;
  sequencer-determined order is canonical.

- **`processTransaction` / `deductFromSender` / `addToRecipient` signatures**
  — all accept `blockTimestamp int64`; `deductFromSender` takes
  `*config.Transaction` for execution-time nonce check.

**API & gRPC**

- **gRPC middleware context propagation** (`gETH/gETH_Middleware.go`,
  `gETH/Server.go`). All seven middleware functions accept and forward
  `ctx context.Context`; hardcoded `context.WithCancel(context.Background())`
  in `_SubmitRawTransaction` removed.

- **`DID.RegisterDID` timestamps** — `Unix()` → `UnixNano()`.

- **HTTP server timeouts** (`explorer/api.go`) — 10 s → 60 s.

- **`SyncStats.Error` checked in CLI output** — sync commands exit non-zero
  on failure instead of silently succeeding.

- **`TimeTaken` unit** — CLI prints seconds (`%ds`) not milliseconds.

- **Vote submission logging** (`Vote/Trigger.go`) — logs target peer ID on
  each retry and on success.

**Seed Server**

- **Seed server hardened** (`seed/seed.go`). Per-peer rate limiting:
  5 reg/hour, burst 5, 2 h lazy eviction. Registry size cap at
  `MaxTrackedPeers`. All `fmt.Print*` → structured `log.Printf("[seed] …")`.

- **`ConnectionPool.MaxConnections`** (`config/ConnectionPool.go`): 20 → 30.

**Housekeeping**

- **`.gitignore`** — `test_results/` added.

- **Legacy FastSync V1** (`fastsync/fastsync.go`) — `BatchRestoreAccounts`
  updated to new signature.

### Fixed

**Sync & Merkle**

- **Stale `latest_block` marker before catchup**
  (`DB_OPs/Nodeinfo/immudb_adapter.go`). `ReconcileBlockNumber()` scans up to
  500 blocks ahead of the stored marker to find the true highest contiguous
  block. PubSub propagation can write blocks without advancing the marker,
  causing a lower Merkle fingerprint to be reported than the node actually holds.

- **SyncMonitor reconcile goroutine ordering race**
  (`internal/syncmonitor/monitor.go`). State reset (counter + interval) now
  occurs before `reconciling.Store(false)`, closing the window for a concurrent
  re-trigger. `immediateRecheck` only fires on success.

- **Merkle hash divergence between PubSub and DataSync paths**
  (`internal/merkle/hash.go`). `nil` and `big.Int(0)` now produce identical
  leaf hash encoding (`v == nil || v.Sign() == 0`).

- **Merkle hash divergence on fast-synced nodes**
  (`DB_OPs/Nodeinfo/immudb_block_nonheaders.go` et al.). `ChainID`,
  `AccessList`, and `LogsBloom` were not serialised in V2 protobufs; all three
  now round-trip correctly.

- **`HeadersWriter` prematurely advancing `latest_block` marker**
  (`DB_OPs/Nodeinfo/immudb_headers_writer.go`). Marker is snapshotted before
  `WriteHeaders` and restored unconditionally, preventing explorer and
  `StartupSync` from treating a headers-only node as fully synced.

- **Duplicate ChainID / AccessList encoding**
  (`DB_OPs/Nodeinfo/immudb_block_nonheaders.go`). Redundant encoding block
  removed — both fields were written twice into the serialised non-header record.

- **TLS client loader fails when `ca.crt` is absent** (`pkg/gatekeeper/tls.go`).
  Three-tier CA resolution: explicit policy override → local `ca.crt` → OS
  system cert pool. Missing `ca.crt` is no longer an error for public endpoints.

**Consensus**

- **Consensus-not-reached propagated as an error**
  (`Sequencer/consensus_statemachine.go`). `BroadcastAndProcessBlock` returns
  `nil` on quorum failure; round ends cleanly.

- **Local block processing inside broadcast layer**
  (`messaging/broadcast.go`). `ProcessBlockLocally` call sites removed from
  `BroadcastBlockToEveryNodeWithExtraData`; processing is now the sole
  responsibility of `BroadcastAndProcessBlock`.

- **Pubsub unsubscribe failure logged at wrong level**
  (`Pubsub/Subscription/Subscription.go`). `Error` → `Warn`.

- **Pubsub topic close race** (`Pubsub/Subscription/SubscriptionManager.go`).
  Both `Unsubscribe` and `Shutdown` now call `sm.gps.CloseTopic(topic)`
  instead of a cached local reference.

**Security / Nonce**

- **Same-block nonce replay — two-layer defence.**
  Layer 1 (admission, `Security/Security.go`): `SecurityCache` advances
  `TxNonce` immediately on acceptance; duplicate rejected at gate.
  Layer 2 (execution, `Processing.go`): `deductFromSender` re-checks nonce
  against DB and writes `TxNonce = tx.Nonce + 1` + `TxCountSent++` durably.

- **Block processing rollback left dirty nonce state**
  (`Processing.go`). `rollbackBalances` replaced by `rollbackState`, which
  restores `Balance`, `TxNonce`, `TxCountSent`, and `UpdatedAt` from pre-block
  snapshot.

- **`PutNonceofAccount` ART key collision**
  (`DB_OPs/account_immuclient.go`). `time.Now().UnixNano()` + atomic counter
  removed; `CreateAccount` calls `GenerateARTNonce()`.

**Database**

- **`BatchRestoreAccounts` duplicate-key error** — deduplication (LWW by
  `UpdatedAt`) applied before `ExecAll`.

- **`BatchRestoreAccounts` DID and metadata loss** — merges `DIDAddress`,
  `CreatedAt`, `AccountType`, `Metadata` from existing DB record before write.

- **`getKeysBatch` prefix scan** (`DB_OPs/immuclient.go`). `Desc: true` →
  `Desc: false`; descending scans were falling past the prefix boundary.

- **`CheckNonceAndGetLatest` uint64 underflow** (#22). Loop restructured to
  top-decrement; `startBlock == 0` no longer wraps to `math.MaxUint64`.

- **`immudb_account_manager` key-not-found** — `GetAccountByAddress` returns
  zero balance instead of error on missing key.

- **`GetLatestBlockNumber` non-deterministic** — retry-with-reconciliation
  wrapper removed; single direct read.

**WebSocket**

- **WebSocket block poller spawning duplicate goroutines**
  (`Service_WS.go`). `isPolling bool` flag inside the `newHeadsSubscriptions`
  mutex closes the race between two simultaneous `SubscribeNewHeads` calls.

- **WebSocket new subscriber receiving historical blocks from genesis** —
  `lastProcessedBlock` seeded from `GetLatestBlockNumber` before the poll loop.

- **`pollForNewBlocks` goroutine leaked on shutdown** — `for range ticker.C`
  replaced with `select { case <-ctx.Done(): return; … }`.

**Housekeeping**

- **`eth_getBalance` error on unknown address** (`gETH/Facade/Service/Service.go`)
  — returns `big.NewInt(0)` on key-not-found instead of an error.

- **P2P DID gossip discarding network ART Nonce** (`messaging/DIDPropagation.go`)
  — `StorePropagatedAccount` replaces `CreateAccount`; received nonce preserved.

- **`defer ctx.Done()` no-op** (`CLI/client.go`) — removed.

- **Go 1.25 deprecation warnings** — `ioutil` → `os`; `reflect.Ptr` →
  `reflect.Pointer`.

### Removed

- **`DB_OPs/merkletree/merkle.go`** — replaced by `internal/merkle/`.
- **`FastSyncSettings.PullOnStartup` and `FastSyncSettings.AllowedPeers`** — removed along with the startup sync goroutine in `main.go`.
- **`explorer/BlockOps_Helper.go`** — single-line pass-through wrappers; callers now use `DB_OPs` directly.
- **Debug `fmt.Printf(">>> [DB]…")` output** — removed from `GetAllKeys`, `getKeysBatch`, `SafeCreate`, and `GetZKBlockByNumber`.

### Performance

- **Explorer, JSON-RPC facade, and gRPC server switched to proof-free reads**.
  Routine query endpoints (`getBlockByNumber`, `getBlock`, `listBlocks`,
  `getTransactionBlock`, `getLatestBlock`, `streamBlocks`, `checkForNewBlocks`,
  `BlockByNumber`, `TxByHash`, `getBlockForSubscription`, `_GetBlockByNumber`)
  now use `ReadZKBlockByNumber` / `ReadZKBlockByHash` (plain `Get`, no Merkle
  proof). `VerifiedGet` preserved for trust-critical paths only.

- **`ReadZKBlockByNumber`** — renamed from `GetZKBlockByNumberFast`; accepts
  `ctx context.Context`. Establishes convention: `Read*` = plain `Get`;
  `Get*` = `VerifiedGet`.

- **`ReadZKBlockByHash`** — new fast hash-based lookup; two-step:
  `PREFIX_BLOCK_HASH + hash` → key → data.

- **`withRetry` on `Read` and `SafeRead`** — the two lowest-level ImmuDB
  primitives now retry on transient failure, matching write behaviour.

- **Context cancellation guards in block-scanning loops** — `ctx.Err()`
  checked at loop head in `GetTransactionsByAccount`,
  `GetTransactionsByAccountPaginated`, `CheckNonceAndGetLatest`, `GetLogs`,
  `checkForNewBlocks`, and the WS poller.

- **Caller-owned context propagated to DB read functions** —
  `GetLatestBlockNumber`, `GetTransactionBlock`, and `ReadZKBlockByNumber` no
  longer construct internal `context.WithTimeout(context.Background(), 5s)`;
  they accept and forward the caller's context.

### Dependencies

| Package | Change |
|---|---|
| `protoc` (build tool) | `v6.33.1` → `v7.34.1` |
| `JupiterMetaLabs/JMDN-FastSync` | Added — `v0.0.0-20260604113915-c1470ecc039d` |
| `redis/go-redis/v9` | Added — `v9.19.0` |
| `shirou/gopsutil` | Added — `v3.21.11+incompatible` (indirect) |
| `JupiterMetaLabs/JMDN_Merkletree` | `v0.0.0-20260205…` → `v0.0.0-20260413…` |
| `JupiterMetaLabs/ion` | `v0.3.5` → `v0.4.2` |
| `go.opentelemetry.io/otel` | `v1.40.0` → `v1.42.0` |
| `google.golang.org/grpc` | `v1.78.0` → `v1.79.3` |
| `grpc-ecosystem/grpc-gateway/v2` | `v2.27.3` → `v2.28.0` |
| `klauspost/compress` | `v1.18.2` → `v1.18.5` |

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

[1.2.0]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/JupiterMetaLabs/jmdn/releases/tag/v1.0.0
