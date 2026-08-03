# Reconciliation tracker — feat/thebe-sc-layer

Merge of `origin/remove/immudb` (cc000e4, 273 commits: ThebeDB migration + smart-contract layer)
into `main` (cfb4eef, 22 commits: AVC v2 consensus, apply-before-broadcast, block-carried ART nonce).
Merge-base: f8fd577 (2026-07-14). 35 conflicted files. Executed 2026-08-03.

**Operator decision:** ThebeDB is the only storage backend (no ImmuDB selectability). Main's
consensus/state-consistency semantics are preserved everywhere and ported onto the Thebe path
where their original ImmuDB implementations were deleted.

## Resolution rules applied

1. Consensus semantics: `main` wins (fail-whole-block on stale nonce, apply-before-broadcast,
   block-bound BLS votes, fail-closed VerifyCertificate 2f+1, no submit-time receiver
   auto-registration, block-carried identity creation, DID propagation truth-telling).
2. Storage plumbing: `remove/immudb` wins (getHandle/store.ThebeHandle, Thebe sync-state KV
   markers, gateway/outbox, compat shims).
3. Logging: branch's per-package `logger() *ion.Ion` helpers kept; main's
   `logger().NamedLogger.X(...)` accessor normalized to `logger().X(...)` (identical ion.Ion
   method underneath — zero semantic change). Files main authored wholesale
   (e.g. messaging/consensus_hardening.go) keep their own zerolog imports.

## Per-file decisions

| File | Resolution |
|---|---|
| .gitignore | Union (main's db ignores + branch's Thebe runtime ignores) |
| Makefile | Union .PHONY; branch body + main's `test-unit` fast gate (survived auto-merge) |
| docker-compose.yml | Branch (postgres+redis+jmdn); immudb + immudb-perms services dropped |
| Scripts/bootstrap_sync.sh | Branch layout (storage/ DB/) + main's backup-pruning and owner-write restore retargeted to Thebe dirs |
| Scripts/docker-entrypoint.sh | Branch (no embedded DB); immudb orchestration dropped |
| Scripts/install_services.sh | Main's journald limits kept + branch's docker-compose stack; immudb.service dropped |
| Scripts/setup_dependencies.sh | Branch wholesale. Main's only delta was the Redis AOF *live-migration* suite (cf09a26); branch provisions `--appendonly yes` at install. Follow-up: port live-migration into `--storage-local` flow if pre-existing unpersisted Redis nodes must upgrade in place |
| config/settings/config.go | NodeConfig union: main's Consensus/Selection/Orchestrator + branch's Thebe section; ConsensusSettings (BlockBuddy, SeedAuthorityBLSPub, CommitteeEpochSeconds, MaxValidators, P2P) kept in full |
| config/constants.go | `MaxMainPeers = 7` (main's BFT fix — n=5 quorum intersection < f+1 was unsafe); DID protocol stays `2.0.0`; branch's contract gossip protocols added |
| config/PubSubMessages/GossipSub_Helper.go | Union imports (config + logging + ion, all used) |
| profiler/profiler.go | Union imports; 3 main-side zerolog calls converted to ion (branch migrated file) |
| node/node.go | SendFile stays deleted (main removed transfer/); no importers remain |
| transfer/file.go, DB_OPs/sqlops/test.db | Deleted (main's deletions stand; test.db also gitignored) |
| CLI/CLI.go | Branch's de-immudb'd help text + main's `statefingerprint` entry (handler survived) |
| Block/Server.go | Main: `EnrichBlockAccountNonces` fail-closed block enrichment kept (bb142df) |
| explorer/BlockOps.go | Main: DBState/MerkleRoot stats removal stands (perf, 967276c) |
| gETH/Facade/Service/Service.go | Main: register-on-read stays dead (receiver-not-found consensus fix) |
| DB_OPs/latest_block.go | Union: main's onAdvance hook (fires at marker advance) + branch's LatestBlockMarkerKey |
| AVC/.../Structs/Utils.go | Main: equal-weights fallback when buddy can't read seed peer list |
| Security/security_cache.go | Main: AllowNewReceiverAccounts-aware receiver check |
| Security/Security.go | Main: negative-value gate (CheckTransactionValues) + no submit-time auto-registration; conns via branch's compat shims |
| Sequencer/Consensus.go | Main ×4: BLS diagnostics, block-bound vote verification (+legacy detection), fail-closed VerifyCertificate 2f+1 over authenticated committee, reject summary |
| Sequencer/consensus_statemachine.go | Main ×2: apply-before-broadcast (7a0b56f) + rejected-block broadcast handling |
| messaging/BlockProcessing/Processing.go | Main ×2: fail-WHOLE-block on stale nonce (determinism), block-carried identity log; branch's skip-tx + cleanup call dropped (markers are revoked in the rollback path) |
| DB_OPs/tx_markers.go | Branch: markers in Thebe sync-state KV (single authoritative population; marker-last atomic ordering already ports main's crash-safety) |
| messaging/broadcast.go | Main's VerifyCertificate fail-closed gate (ion-ified); branch's majority-of-responders dropped |
| messaging/blockPropagation.go | Main's transport-tag logs (ion-ified); hunk at process goroutine: rejected-notice + old BLS majority dropped (v2 admitZKBlock gate upstream covers both); pooled handle passed into ProcessBlockTransactions |
| messaging/DIDPropagation.go | Main: no-peers IS an error; delivery truth-telling with committeeDeliveryStatus (ion-ified); zerolog import dropped |
| main.go | Imports union (blockgossip + SmartContract); Thebe init + handle factory FIRST, then main's stats hook + one-time seed (now via ported CountAccountsWithTimeout); main's pool clients kept; FileProtocol/transfer handler stays removed |
| go.mod | Union, max versions (otel 1.43.0, net 0.55.0, newer xerrors) + zap; ThebeDB `replace ../ThebeDB` kept |
| go.sum | Union superset (safe; run `go mod tidy` to prune) |

## Modify/delete ports (main modified, branch deleted)

| Deleted file | Main's changes | Ported to |
|---|---|---|
| DB_OPs/account_immuclient.go | 7 commits incl. AVC v2 | Most functions already reimplemented on branch (thebe_ops.go, thebe_missing.go, merge_account.go). Ported the 3 with no Thebe equivalent → **DB_OPs/mainline_ports.go**: `NormalizePropagatedAccountState` (verbatim, pure, unit test survives), `ListAccountsPaginatedFrom` (opaque cursor now offset-based over SQL `ORDER BY created_at`; caveat: fingerprint iteration order changed — switch reader to ORDER BY address if cross-node tie instability appears), `CountAccountsWithTimeout` (real count via CountAccountsCtx — branch's CountBuilder stubs return 0) |
| DB_OPs/Accounts_helper.go | stats seed helpers (52aec00) | Deleted; no external callers (CountBuilder duplicated by branch's count_builder.go; seed uses ported CountAccountsWithTimeout) |
| DB_OPs/Nodeinfo/immudb_data_writer.go | c010de1 fastsync apply consistency | Tail ported into **thebe_data_writer.go**: didWriteBlock/highestWritten tracking, `DeferLatestBlockAdvance` during FastSync sessions, single MONOTONIC latest_block advance, notify only after real writes |

## Follow-ups (non-blocking, tracked for Phase B)

1. `DB_OPs/account_recon.go` still imports codenotary/immudb → port, then complete migration
   Phase 7 (drop immudb dep + dualdb/ + thebe_shadow.go; `go mod tidy`).
2. Committed junk to delete: `migrate_immudb_to_thebe` binary (references ThebeDB pkg/events,
   which no longer exists), stale `gETH/.../ImmuDB.log`, grafana immudb dashboard.
3. cc000e4 fastsync fleet-wide disable is on this branch (defaults.go "DISABLED pending the
   ThebeDB FastSync redesign") — decide before rollout; main's session machinery
   (sync_session.go, DeferLatestBlockAdvance) is merged and functional.
4. ADR-001 flow diagram still shows broadcast-then-apply; re-anchor to apply-before-broadcast
   (semantics compatible: deployments known before broadcast).
5. Redis live AOF migration port into setup_dependencies `--storage-local` (see above).
6. ThebeConfig still carries RedisURL/StreamName/GroupName/CDC of the retired event-bus design —
   prune config surface once CDC direction is confirmed.
7. Doc drift: thebedb-primary-migration.md claims immuclient_helper.go deleted (still present);
   integration doc's deletion manifest partially disagrees with the tree. Trust the tree.

## Verification status

- Textual: 0 conflict markers tree-wide; all 35 files resolved with rationale above.
- Build/test: **pending** — CGO_ENABLED=1 build with ../ThebeDB sibling; see merge commit notes.
