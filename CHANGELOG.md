# Changelog

All notable changes to JMDN are documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/),
adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.2.2] - 2026-07-14

### Fixed

**RPC**

- **Spec-compliant transaction marshaling** (`gETH/Facade/rpc/handlers.go`).
  Transaction objects returned by `eth_getBlockByNumber(full=true)` now carry
  `blockHash`/`blockNumber`/`transactionIndex` from the parent block (injected
  at marshal time without mutating shared tx objects); pending transactions
  return these fields as JSON `null`. `v`/`r`/`s` are always emitted (`0x0`
  when zero — a zero `v` is valid for EIP-1559), `chainId` is always present,
  and type-2 transactions additionally carry `yParity`, `accessList`, and
  always-present `maxFeePerGas`/`maxPriorityFeePerGas`, with `gasPrice`
  reported as the effective gas price (`min(maxFeePerGas, baseFee + tip)`).
  `accessList`/`yParity` are correctly omitted for legacy (type 0)
  transactions. Locked down by golden-shape tests
  (`gETH/Facade/rpc/marshal_test.go`), including a signer-level proof that
  type-2 `V` is the raw parity bit, keeping the `yParity` mapping valid. (#75)

**Security / Accounts**

- **Fail-closed account loading** (`Security/security_cache.go`,
  `Security/Security.go`). `LoadAccounts` now propagates database errors
  instead of swallowing them; both submission validation (`AllChecks`) and
  block validation (`CheckZKBlockValidation`) reject on load failure rather
  than treating a transient DB error as "account not found".

- **Zero-balance overwrite guard** (`DB_OPs/account_immuclient.go`).
  `storeAccount`'s existence pre-check now distinguishes "key not found"
  from transient errors (timeout, connection drop) and aborts on the
  latter — previously a failed read fell through to writing a fresh
  `Balance: "0"` document over a potentially funded account.

- **Unknown receivers auto-registered safely on submit**
  (`Security/Security.go`). A transaction to a not-yet-registered address
  no longer fails validation: the receiver is simulated with an in-cache
  placeholder during checks and persisted to the accounts DB only after
  the transaction passes every check — rejected transactions never create
  database entries. The cached copy is re-read from the DB after creation
  so it always matches the stored document.

- **Submit-time balance precheck uses the consensus gas formula**
  (`Security/security_cache.go`). `CheckBalanceWithCache` now computes
  gas cost via `config.GasFee` (EIP-1559 aware, identical to what block
  execution charges) instead of an ad-hoc `gasPrice → maxFee → 0`
  fallback.

## [1.2.1] - 2026-07-08

### Added

**Explorer**

- **`/api/stats` total transaction count** now served from the SQLite tx-address
  index (`DB_OPs/txindex/txindex.go`). `CountTransactions` uses
  `SELECT COUNT(DISTINCT tx_hash)` so sender and recipient rows for the same
  transfer are not double-counted. Falls back to the ImmuDB prefix-scan on any
  index error. The index is gated — a partial count during `RebuildIndex` never
  reaches the API. (#67)

**RPC**

- **`eth_getTransactionsByAddress`** (`gETH/Facade/rpc/handlers.go`). Paginated
  address-transaction lookup backed by the new index, with address validation
  and bounded-concurrency (10 in flight) ImmuDB hydration per page. (#55)

- **JSON-RPC batch requests** (`gETH/Facade/rpc/http_server.go`).
  `handleJSONRPC` now detects a `[...]` batch body and processes up to 100
  requests concurrently, returning one array of responses — standard
  JSON-RPC 2.0 batching, previously unsupported. (#50)

**CLI & Ops**

- **`rebuildindex`, `rebuildrange <from> <to>`, `txindexstatus`** — new
  console and `-cmd` gRPC commands (`CLI/CLI.go`, `CLI/CLI_GRPC.go`,
  `CLI/GRPC_Server.go`, `CLI/client.go`, `CLI/proto/Connection.proto`) for
  operating the transaction index from a running node, e.g.
  `docker exec -it jmdn jmdn -cmd txindexstatus`. Documented in `DOCKER.md`. (#55)

**L1 Finality**

- **L1 finality tracking** (`Block/Server.go`, `config/ZKBlock.go`,
  `gETH/Facade/rpc/handlers.go`). Blocks now carry `l1TxHash` / `l1BlockNumber`
  once their rollup commitment is confirmed on L1. New `POST /api/l1-commit`
  and `POST /api/l1-commit-range` endpoints ingest commit data and broadcast
  it to peers over a dedicated gossip channel (`pubsub-l1-commit`) so every
  node's local record stays in sync. `eth_getBlockByNumber` accepts a
  `wantL1Commit` flag to fetch the latest L1-committed block directly. (#59)

**Account Sync**

- **Redis is now auto-provisioned** (`Scripts/setup_dependencies.sh --redis`)
  for the account-sync queue, with secure, idempotent password setup. (#60)

- **Redis AOF persistence configured on bare-metal installs**
  (`Scripts/setup_dependencies.sh`). `appendonly yes`, `appendfsync everysec`,
  and `maxmemory-policy noeviction` are now set and verified against the live
  server on every install — matching the Docker Compose configuration. A Redis
  crash between RDB snapshots loses the account-sync stream; AOF is a
  correctness requirement, not a tuning option. (#66)

**Docker & Deployment**

- **Docker deployment hardened for production, with exchange nodes in
  mind.** Everything below ships in `docker-compose.yml` and a new
  `Scripts/docker-deploy.sh` — no action needed to benefit, though sizing
  and version pinning are worth reviewing on upgrade (`DOCKER.md` §4, §13).

  - **Network reachability improved** — the node's peer-to-peer port is now
    published by default, so a Docker-deployed node can be dialed by other
    peers instead of only reaching out on its own.
  - **Resource governance** — memory/CPU/file-descriptor/process caps on
    every service, scaled to host size via `.env` (see the new
    `.env.docker.example` template and `DOCKER.md`'s sizing table for
    8GB/16GB/32GB/64GB hosts).
  - **Safe, automatic upgrades** — `docker-deploy.sh` pulls, restarts, and
    health-checks the node, automatically rolling back to the previous
    image if the new one fails to come up healthy.
  - **Steadier shutdowns and restarts** — per-service grace periods tuned
    to how long each component actually needs to stop cleanly, zombie
    process reaping, and a two-tier health check (Explorer API with a
    time-bounded JSON-RPC fallback) so a node running the minimal config
    doesn't show falsely unhealthy.
  - **Tighter default port exposure** — only what's needed for standard
    node operation is published out of the box; a few narrowly-scoped
    ports stay off by default, documented in `PORTS.md` for anyone with a
    specific reason to turn them on.
  - **Cleaner upgrades going forward** — the image version and Compose
    project name now live in a local `.env` file instead of the tracked
    `docker-compose.yml`, so pulling the latest repo changes never
    conflicts with an operator's pinned version again. (#62)

### Changed

- **Address-transaction pagination hardened for accuracy and consistency**
  (`explorer/addressOps.go`, `gETH/Facade/rpc/handlers.go`). Results are
  strictly and deterministically ordered, so paging through a large
  transaction history returns each entry exactly once, including for
  addresses with several transactions in the same block. Page size and
  offset are bounded for consistent response times on large histories,
  and lookup availability is clearly signaled to callers for smooth retry
  behavior. (#55)

- **Transaction parsing switched from RLP decoding to `UnmarshalBinary`**
  (`gETH/Facade/Service/Service.go`), so legacy, EIP-2930, and EIP-1559
  transactions are all parsed consistently on submission. (#53)

- **FastSync V1 retired** — superseded by FastsyncV2. CLI commands
  (`fastsync`, `firstsync`) return an explicit retirement message pointing to
  the V2 equivalents. The AVRO whole-DB exchange path and its backing code are
  removed. (#66)

**Graceful Shutdown**

- **Shutdown sequence now bounded end-to-end** (`main.go`,
  `logging/ion_Builder.go`). OTEL/tracing flush on shutdown is now
  time-boxed at 3s instead of running under an unbounded context — matters
  once tracing is enabled and the collector is slow or unreachable. The
  overall shutdown sequence also now runs under its own deadline, kept
  under Docker's `stop_grace_period`, so a stall anywhere in it is logged
  and the process exits cleanly instead of being silently SIGKILLed.

### Fixed

**RPC**

- **`eth_getTransactionCount` returned the same value for every address**
  (`gETH/Facade/Service/Service.go`) — the lookup ignored the `addr` parameter
  entirely (`DB_OPs.CountTransactions(nil)`). Now returns the account's actual
  `TxNonce`. (#50)

- **Block responses missing standard Ethereum JSON-RPC fields**
  (`gETH/Facade/rpc/handlers.go`) — `eth_getBlockByNumber` /
  `eth_getBlockByHash` did not include `stateRoot`, `receiptsRoot`,
  `logsBloom`, `extraData`, `miner`, or PoW-compatibility placeholders
  (`sha3Uncles`, `nonce`, `difficulty`, `mixHash`, `uncles`), which some
  wallets and explorers require to accept a block. `baseFeePerGas` now falls
  back safely for blocks written before the field existed. (#50)

- **`eth_getTransactionReceipt` fabricated a log entry and errored on pending
  transactions** (`DB_OPs/Facade_Receipts.go`, `gETH/Facade/rpc/types.go`).
  Generated receipts no longer include a synthetic log entry that never came
  from the chain. A transaction that hasn't been mined yet now returns
  `null`, per spec, instead of a JSON-RPC error — fixes repeated failed
  lookups from wallets polling for a receipt. (#56)

- **Transaction-hash lookups were case-sensitive** (`DB_OPs/immuclient.go`) —
  stored keys are always lowercase; a mixed-case hash from a caller would
  silently fail to match. Reads now normalize to lowercase, matching the
  write path. (#56)

**Transaction Submission & Security**

- **Signature verification for typed transactions** (`Security/Security.go`).
  `CheckSignature` now uses `LatestSignerForChainID` to recover EIP-2930 and
  EIP-1559 signatures correctly, alongside the existing legacy (V=27/28) path. (#53)

- **Contract deployment transactions were rejected** (`Security/Security.go`).
  `CheckAddressExistWithCache` treated a nil `to` address (a contract
  deployment) as invalid; deployments are now correctly exempted from the
  receiver-existence check. A related nil-pointer panic in trace attributes
  was also fixed. (#53)

**mTLS**

- **Missing client certificate caused a fatal error instead of a fallback**
  (`pkg/gatekeeper/tls.go`). Nodes connecting to endpoints that don't require
  a client certificate (e.g. public gateway endpoints) failed outright,
  blocking all transaction submissions through that route. Now falls back to
  one-way TLS and only hard-fails if a cert file exists but is unreadable or
  invalid. (#51)

**Consensus & Voting**

- **Vote rejection reasons were discarded** (`Sequencer/Consensus.go`,
  `Vote/Trigger.go`, `AVC/BuddyNodes/MessagePassing/`). `ProcessVotesFromCRDT`
  now returns and propagates the reason a vote was rejected, surfaced through
  to consensus output instead of a bare pass/fail — improves operator
  visibility into why a vote didn't succeed. (#57)

- **Block processing and storage could run out of order**
  (`messaging/broadcast.go`) — local block processing now happens strictly
  before the block is considered committed, closing a window where a block
  could be marked stored before its transactions were fully applied. (#57)

**CLI**

- **DID lookups used the wrong ImmuDB client** (`CLI/CLI_GRPC.go`) —
  corrected to use the DID-specific client, matching how other CLI
  account/DID commands connect. (#57)

**Networking**

- **Duplicate GossipSub routers could silently break existing subscriptions**
  (`config/PubSubMessages/GossipSub_Helper.go`) — router/topic instances are
  now a per-host singleton, preventing a second registration from orphaning
  the first. (#59)

**Account Sync & Balance Correctness**

- **Balance corruption on reconciliation** (`config/gasfee.go`,
  `FastsyncV2/deltas.go`, `messaging/BlockProcessing/Processing.go`). Gas fee
  calculation during reconciliation used different constants and fallback values
  than the live execution path, causing balances to be re-corrupted on every
  catchup. A shared `config/gasfee.go` now provides one formula used by both
  paths. (#64)

- **Account updates clobbered DID, type, and metadata fields** — drain worker
  updates now merge into the stored account instead of overwriting it. LWW
  timestamps are set by the producer so replayed stale entries cannot overwrite
  newer data. (#64)

- **Per-transaction balance and marker writes were not atomic**
  (`messaging/BlockProcessing/Processing.go`, `DB_OPs/tx_markers.go`). A crash
  mid-block left partially-applied balances with no marker; replay re-applied
  the prefix, double-counting. All staged balance effects and the tx-processed
  marker now commit in one ImmuDB `ExecAll` per transaction. (#64)

- **Recon anchor could advance ahead of drained data**
  (`DB_OPs/Nodeinfo/account_sync_drainwait.go`). The reconciliation anchor
  marked ranges as applied before the Redis queue was flushed; a queue loss
  (crash, eviction) silently skipped those ranges permanently. The anchor now
  only advances after confirming the enqueued stream IDs have been applied and
  ACKed by the drain worker. (#64)

- **`latest_block` marker had four concurrent writers with no ordering guard**
  (`DB_OPs/latest_block.go`). Out-of-order stores (PoTS WAL replay, catchup
  batches, sync workers) regressed the tip; header-skeleton blocks advanced it
  past the data-complete tip. All writers now go through a single monotonic
  choke point. (#64)

**Transaction Index**

- **HandleSync and PoTS blocks were not indexed** — blocks synced by the
  FastsyncV2 HandleSync and PoTS gap-fill paths were stored but never written
  to the SQLite tx-address index, causing `eth_getTransactionsByAddress` to
  return stale results between catchups. `EnsureReady` is now called at the
  end of `handleSyncInternal` to close the gap. (#66)

- **Non-sequencer nodes' live blocks were not indexed** — `blockPropagation`
  stored and processed pubsub-received blocks but never called `IndexBlockAsync`,
  so the index drifted continuously staler on non-sequencer nodes.
  `IndexBlockAsync` is now called after every stored block on this path,
  matching the sequencer path. (#66)

**Deployment**

- **Redeploying via `deploy.sh` silently failed to update the node's
  startup wrapper** (`Scripts/deploy.sh`) — the script wrote the updated
  wrapper to a filename the systemd/launchd/rc.d service definitions never
  referenced, so wrapper changes never took effect on redeploy, only on a
  fresh install. Now writes to the correct path. (#60)

### Security

- **Data race on chain-ID and cached transaction signers**
  (`Security/Security.go`) — `expectedChainID` and its derived signers are now
  guarded by a `sync.RWMutex` (`signerMu`), closing a race between startup
  configuration and concurrent transaction verification. (#53)

### Performance

- **Address-history lookups optimized for concurrent throughput and
  reliability.** Reads and writes use separated processing paths so lookups
  stay fast even during a large background catch-up. Startup no longer waits
  on a full historical catch-up to complete — indexing continues in the
  background while the node comes online, with a clear readiness signal
  until the first pass finishes. Progress tracking is now monotonic, so an
  out-of-order background batch can never move it backwards relative to
  already-processed live data. Background indexing work is bounded and
  queued rather than unbounded, with a clean shutdown path. Covered by 18
  new unit tests, including concurrency and shutdown-race scenarios. (#55)

- **Bounded-concurrency ImmuDB hydration for address-transaction pages**
  (`explorer/addressOps.go`, `gETH/Facade/rpc/handlers.go`) — up to 10
  concurrent point-fetches per page instead of a sequential loop. (#55)

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

[1.2.2]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/JupiterMetaLabs/jmdn/releases/tag/v1.0.0
