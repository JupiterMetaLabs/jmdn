# Changelog

All notable changes to JMDN are documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/),
adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
