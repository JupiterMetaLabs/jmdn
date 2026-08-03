# Phase B — Thebe + Smart-Contract Deliverables Checklist

Extracted 2026-08-04 per the reconciliation plan (step 6), after the Phase A gate closed
(build + `-short` tests green on feat/thebe-sc-layer @ 877dd66). Each item is traceable to a
design doc or the reconciliation tracker; each carries a verification condition. Work one item
at a time; keep build/tests green per item (step 7 discipline).

**Standing operator decisions:** ThebeDB is the sole storage backend (no ImmuDB selectability);
FastSync stays disabled fleet-wide until B4's validation gate passes.

## B1 — Integration validation on live infra  *(gate for most items below)*
Source: Makefile/CLAUDE.md test notes; tracker "Verification status".
Run the non-short suite + a real node against the docker-compose stack (postgres wal_level=logical,
redis AOF) with `../ThebeDB` sibling; exercise explorer reads via docs/THEBE_DEBUG_URLS.md.
→ Verify: `go test ./...` green with infra up; node boots with `thebe.enabled=true`; debug routes
return projected data; outbox drains after an induced SQL failure.

## B2 — Migration Phase 7: remove DualDB + ImmuDB dependency — **DONE ✓ (gate green 2026-08-04, cc0a015+f926706+8fb9102)**
Source: docs/phases/thebedb-primary-migration.md §Phase 7 (status open); tracker follow-up #1.
Delete `DB_OPs/dualdb/`, `DB_OPs/thebe_shadow.go`, `DB_OPs/thebe_gateway_adapter.go`; drop
`codenotary/immudb` from go.mod (`go mod tidy`); remove the `SetThebeShadowWriter` wiring
(main.go:1139) after tracing every `getThebeShadowWriter()` call site into a direct gateway path;
delete stale junk (migrate_immudb_to_thebe binary, gETH ImmuDB.log, grafana immudb dashboard,
Scripts/migrate_immudb_to_thebe.py if migration is done fleet-wide); prune `config/ImmudbConstants.go`
remnants and `DB_OPs/immuclient_helper.go` if dead.
→ Verify: `grep -rn "codenotary/immudb\|dualdb\|thebe_shadow" --include='*.go' .` empty;
build + tests green; a node processes blocks with the shadow hook gone.

## B3 — Migration Phase 8: integration seal — **static portion DONE 2026-08-04; two operator items open**
Source: docs/phases/thebedb-primary-migration.md §Phase 8.
Results: compile-time interface assertions PASS (backend, composite, 4 cache decorators);
AI-doc MODULE blocks PASS on all new packages; zero `ImmuClient`/`immudb` code references
(one sanctioned string remains: `State_Path_Hidden = "./.immudb_state"` — on-disk TLS path
kept so existing deployments don't rotate certificates); repo-wide gofmt now clean (51
pre-existing files formatted); stale comments pointing at renamed/deleted files corrected.
Recorded deviations from the original seal wording (accepted, with rationale):
(1) `PooledConnection` still threads through 38 files as an inert compat parameter — its
retirement is a standalone refactor, not a seal blocker; (2) package-internal tests
(merge guard, latest_block, txindex…) legitimately live in-package — the "no tests outside
Tests/" rule applies to the external integration suites; (3) `DB_OPs/store` imports
`thebegateway` (record types) + `config` (ZKBlock) — both sanctioned by the Phase 1 spec.
→ Open (operator): `golangci-lint run --new-from-rev=main`; dependabot review of the 9
moderate advisories on the default branch (`gh api` or the security tab) — fold fixes here.

## B4 — FastSync on ThebeDB: validate, then re-enable
Source: cc000e4 (fleet-wide disable "pending redesign"); operator decision 2026-08-04 (keep off);
main's c010de1 machinery (sync_session.go, DeferLatestBlockAdvance, thebe_data_writer tail).
Two-node sync test on the Thebe backend: fresh node fastsyncs from a seeded node; session
defers latest_block; endSyncSession advances it; live blocks admit at marker+1 afterward;
reconciliation converges balances (watch the transient-negative warning path in account_recon).
→ Verify: synced node's statefingerprint matches the source at equal height; then flip the
default in config/settings/defaults.go on its own commit.

## B5 — Contract propagation: validate pull-on-demand + amend ADR-001
Source: docs/ADR-001 (status Proposed, push model) — superseded in-branch by
messaging/ContractPropagation.go (F4 note: push retired, pull-on-demand via
ContractPullProtocol; RegisterContractFromGossip fills registry+ABI).
Validate: deploy on node A, `GetContractCode`/ABI resolves on node B via pull; no
double-registration; apply-before-broadcast ordering (7a0b56f) holds around deployment.
→ Verify: two-node deploy/call/pull test green; ADR-001 updated to Superseded/Amended with the
pull design + post-7a0b56f call flow.

## B6 — Smart-contract layer hardening pass
Source: SmartContract/README.md, architecture.md, processing_changes.md, smart_contract_flow.md.
End-to-end EVM paths on a live node: deploy (HelloWorld/SimpleToken), call, payable transfer,
receipt + logs via gETH; contract state through contractDB→Thebe KV (no Pebble); StateDB
journal/revert behavior under a failing tx inside a block; refresh SC docs that still say
PebbleDB/ImmuDB.
→ Verify: SmartContract/cmd + grpcurl_commands.txt flows succeed against a node; docs match code.

## B7 — Config surface pruning (retired event-bus fields)
Source: tracker follow-up #6; ThebeDB CLAUDE.md (pkg/events removed; Redis = standalone cache).
Decide CDC direction (ThebeConfig.CDC wires db.StartCDC today — keep if CDC is the projection
strategy), then drop dead fields (RedisURL/StreamName/GroupName/MaxLen if unused by cache/worker),
their BindEnv lines (loader.go) and defaults; align jmdn_default.yaml.
→ Verify: config round-trips (`config/settings` tests), node boots, no references to dropped keys.

## B8 — Deferred small items
Source: tracker follow-ups #4, #5, #7; mainline_ports.go port notes.
(a) state_fingerprint ordering: switch reader listing to `ORDER BY address` if B4's cross-node
fingerprint comparison shows tie instability. (b) Optional: port redis AOF live-migration into
setup_dependencies `--storage-local` for in-place upgrades. (c) Fix doc drift in the two phase
docs (deletion manifest vs tree). (d) Consider ThebeDB builder-2PC for commitReconGroup's
account+marker batch (current: accounts-first/marker-last, bounded double-apply on crash).
→ Verify: per item; each is its own commit.
