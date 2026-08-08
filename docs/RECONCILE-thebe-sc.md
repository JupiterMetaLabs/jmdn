# Reconciliation tracker — feat/thebe-sc-layer

Merge of `origin/remove/immudb` (cc000e4, 273 commits: ThebeDB migration + smart-contract layer)
into `main` (cfb4eef, 22 commits: AVC v2 consensus, apply-before-broadcast, block-carried ART nonce).
Merge-base: f8fd577 (2026-07-14). 35 conflicted files. Executed 2026-08-03.

**Operator decision (explicit, recorded — not a silent override):** On 2026-08-03, before any
conflict was resolved, the reconciliation presented the dual-backend question directly to the
operator (Doc / saishibu@jupitermeta.io) with the recon document's recommendation attached
(Option A: keep main's ImmuDB code behind `thebe.enabled`, ImmuDB default). The operator's
answer — verbatim: "remove immudb wire to thebe" — overrode that recommendation deliberately:
ThebeDB is the only storage backend, no ImmuDB selectability. Every conflict resolution and the
Phase 7 removal (cc0a015) flow from that decision. Consequence accepted with it: no ImmuDB
fallback exists if ThebeDB has a production incident — which is why B1's live-infra validation
(incl. induced 2PC failure) is the master gate. Merge-to-main sign-off should REAFFIRM this
decision; it does not need to be made, only re-confirmed at the merge boundary.

Main's consensus/state-consistency semantics are preserved everywhere and ported onto the Thebe
path where their original ImmuDB implementations were deleted.

**Correction (2026-08-06, from external divergence review):** the recon-phase note that
ThebeDB's `OpFilter` gap was "stale" overstated it — the planner dispatches `OpFilter` to
`execFilter`, but `execFilter` unconditionally returns `ErrOpNotSupported` (planner.go:149).
Dispatch exists; implementation doesn't. No jmdn path depends on it; filed in the ThebeDB
task list.

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

## Post-merge resolution corrections (found during Phase B, both fixed)

1. **blockPropagation hunk-3 resolution dropped the branch's `PrefetchMissingContracts`
   call** (receive-path pull-on-demand hook, remove/immudb:326). Zero callers remained —
   a Type-2 call on a node missing the bytecode would have fallen through to the transfer
   path. Restored at the same point in the v2 flow (after admitZKBlock, before
   ProcessBlockTransactions) in 3fb140e.
2. **Compat pool shims contradicted their doc contract** — bodies always returned an
   error, so three resolutions that trusted the documented "synthetic nil-conn" behavior
   (main.go boot acquisition, receive-goroutine acquisition, Security.AllChecks) would
   have failed at runtime, invisible to unit tests. Shims now return (nil, nil) — the
   codebase-wide "use the process ThebeHandle" sentinel — and the dead acquisitions were
   removed (409ed52).

Lesson recorded: for merged-in compat layers, verify the BODY, not the doc comment.

3. **The Thebe port put both authoritative balance writers behind the LWW merge gate**
   (ApplyTxAtomic + commitReconGroup via BatchRestoreAccounts) — old-block recon deltas were
   silently dropped while their markers were written. Found by the KB-lens evaluation
   (2026-08-08); fixed with the raw BatchPutAccountsAuthoritative path + regression tests
   (see PHASE-B-CHECKLIST KB-triage table). Second lesson: 'route everything through the
   single merge decision point' sounds safer than it is — the merge gate exists for
   uncoordinated writers; coordinated lock-holding writers must win unconditionally.

## Follow-ups (non-blocking, tracked for Phase B)

1. `DB_OPs/account_recon.go` still imports codenotary/immudb → port, then complete migration
   Phase 7 (drop immudb dep + dualdb/ + thebe_shadow.go; `go mod tidy`).
2. Committed junk to delete: `migrate_immudb_to_thebe` binary (references ThebeDB pkg/events,
   which no longer exists), stale `gETH/.../ImmuDB.log`, grafana immudb dashboard.
3. cc000e4 fastsync fleet-wide disable: **DECIDED 2026-08-04 — stays disabled** until the
   Thebe-backed path passes end-to-end validation (docs/PHASE-B-CHECKLIST.md B4); main's
   session machinery (sync_session.go, DeferLatestBlockAdvance) is merged and unit-tested.
4. ADR-001 flow diagram still shows broadcast-then-apply; re-anchor to apply-before-broadcast
   (semantics compatible: deployments known before broadcast).
5. Redis live AOF migration port into setup_dependencies `--storage-local` (see above).
6. ThebeConfig still carries RedisURL/StreamName/GroupName/CDC of the retired event-bus design —
   prune config surface once CDC direction is confirmed.
7. Doc drift: thebedb-primary-migration.md claims immuclient_helper.go deleted (still present);
   integration doc's deletion manifest partially disagrees with the tree. Trust the tree.

## Verification status

Done in-session (2026-08-03):
- Textual: 0 conflict markers tree-wide; all 35 files resolved with rationale above.
- Syntax: `gofmt` parsed every .go file in the repo with **0 errors** (Go 1.24.9 parser);
  merge-resolved files reformatted in the follow-up style commit.
- Semantic survival audit: 18/18 discriminating symbols present — one per critical main-side
  commit (apply-before-broadcast, block-bound BLS, fail-closed VerifyCertificate,
  EnrichBlockAccountNonces + carried-nonce sentinel, NormalizePropagatedAccountState port,
  stats seed, DeferLatestBlockAdvance port, SeedAuthorityBLSPub, MaxMainPeers=7, DID v2,
  register-on-read removal, DID no-peers error) and per branch-side wiring point
  (SmartContract server, JMDN profile, handle factory, contract protocols, Thebe markers).

Pending — run on a machine with disk headroom (sandbox verified toolchain/network/credentials
but hit a hard 9.6G disk ceiling mid-module-download):

    cd jmdn   # with ../ThebeDB checked out (replace directive)
    git checkout feat/thebe-sc-layer
    CGO_ENABLED=1 go build ./...
    CGO_ENABLED=1 go test -short ./...      # unit gate; integration tests need live infra
    go vet ./...
    go mod tidy                              # prunes the union-merged go.sum superset
    golangci-lint run --new-from-rev=main    # lint only the merge delta

**PHASE A GATE CLOSED — 2026-08-04:** `CGO_ENABLED=1 go build ./...` green and
`go test -short ./...` fully green (operator run, Go 1.26.4/darwin-arm64) after
three fix rounds on top of the merge: 9c7c74e (merge) → b2c4fa9 (equivocation +
account-recon Thebe ports, otelsetup fallback, interface-drift test repairs) →
1eaffd8 (balance-clobber guard #7 port, captureHandle sync-KV, Badger-backed
contractDB tests, pool-init shims, vet format strings). Integration tests
(live ImmuDB→ThebeDB infra, seed node) remain to be exercised separately.
