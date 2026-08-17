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

## B4 — FastSync on ThebeDB: validate, then re-enable — **SKIPPED by operator (2026-08-04); FastSync remains disabled. Revisit before any re-enable.**
Source: cc000e4 (fleet-wide disable "pending redesign"); operator decision 2026-08-04 (keep off);
main's c010de1 machinery (sync_session.go, DeferLatestBlockAdvance, thebe_data_writer tail).
Two-node sync test on the Thebe backend: fresh node fastsyncs from a seeded node; session
defers latest_block; endSyncSession advances it; live blocks admit at marker+1 afterward;
reconciliation converges balances (watch the transient-negative warning path in account_recon).
→ Verify: synced node's statefingerprint matches the source at equal height; then flip the
default in config/settings/defaults.go on its own commit.

## B5 — Contract propagation — **ADR-001 amended + prefetch regression fixed (3fb140e, 0e31040); two-node validation pending infra**
Source: docs/ADR-001 (status Proposed, push model) — superseded in-branch by
messaging/ContractPropagation.go (F4 note: push retired, pull-on-demand via
ContractPullProtocol; RegisterContractFromGossip fills registry+ABI).
Validate: deploy on node A, `GetContractCode`/ABI resolves on node B via pull; no
double-registration; apply-before-broadcast ordering (7a0b56f) holds around deployment.
→ Verify: two-node deploy/call/pull test green; ADR-001 updated to Superseded/Amended with the
pull design + post-7a0b56f call flow.

## B6 — Smart-contract layer hardening — **doc refresh DONE (storage-reality banners, ThebeDB participant); live EVM E2E pending infra**
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


---

## Review triage — 2026-08-05 deep review, findings verified independently

| # | Verdict | Disposition |
|---|---|---|
| R1 (missing quorum fixes) | Confirmed in substance (commits lived on fix/committee-quorum-formation, not main) | **Resolved by operator merge 76cfb26**; v2 verifier + quorum gates both verified present post-merge |
| R2 (outbox worker never started) | **Confirmed** — Start() only in tests | **Fixed 931f002**: worker started in main.go (5s), graceful Stop |
| R3 (broken cassata contract path in cmd binary) | **Confirmed** — namespaces unprojected by the profile | **Fixed d12fbc1**: cmd wires KVStateRepository (same as node); ThebeStateRepository + ThebeBatch deleted |
| R4 (CommitToDB atomicity + no state root) | Partially confirmed with corrected mechanism: staging is safe (nothing lands pre-Commit); the real defects are (a) KVStateBatch.Commit flushes ops one-by-one — a mid-commit failure leaves partial state, (b) obj.commitState() marks memory clean BEFORE the flush succeeds, (c) empty state root = no cryptographic commitment | Filed as **ThebeDB task**: atomic derived-write batch primitive on kv.Store (per reconciliation stop-condition: no ThebeDB changes from jmdn). jmdn follow-up: state-root commitment design for validating-node determinism |
| R5 (contract pull fail-open) | Already documented (ADR-001 Amendment 1) | Open operator decision before mainnet; fail-closed alternative named in the ADR |
| R6 (contract_receipt dispatch missing) | **Confirmed** — default acked + discarded | **Fixed 931f002** |
| R7 (migration unsealed, immudb imported, stale binary) | **STALE** — codenotary importers 0, binary deleted, Phase 7 sealed (cc0a015); true residue: live-infra integration tests unrun (= B1) | No action beyond B1 |
| R8 (builder SQL-commit-then-KV-commit window) | Confirmed as narrow window: KV-prepare → SQL-commit → KV-commit; a KV-commit failure orphans one SQL row | Filed as **ThebeDB task**: compensation record or commit-order rework in pkg/builder |
| R9 (doc drift: attempts 10 vs 3, ErrStaleNonce comment) | **Confirmed** | **Fixed 8a4f1d4** |

Review's KB correction (projection = synchronous 2PC Profile, not CDC) matches this branch's
reality; the CDC pipeline is separate downstream analytics gated by cfg.Thebe.CDC.

### ThebeDB tasks filed from this triage (recorded in the ThebeDB repo: docs/TASKS-from-jmdn-reconciliation.md)
1. kv.Store: atomic batch primitive for derived writes (Badger WriteBatch) so
   contractDB.CommitToDB can flush all-or-nothing (R4a).
2. pkg/builder: close the SQL-committed/KV-commit-failed window (compensation
   record or order rework) (R8).
3. pkg/query: implement execFilter — OpFilter dispatch exists but the body is an
   unconditional ErrOpNotSupported stub (external-review correction, 2026-08-06).


---

## KB-evaluation triage — 2026-08-08, knowledge-base lens (double-apply / fee-formula invariants)

A KB-context evaluator audited the branch against the fleet's operational memory (the
FastSync double-apply incident, fee-formula centralization, PRE-1..4 gates). Findings were
independently re-verified before action; this round was compiled and tested IN-SESSION
(`go vet ./...` green tree-wide; DB_OPs, contractDB, BlockProcessing, FastsyncV2, Tests/*,
config suites green).

| Finding | Verdict | Disposition |
|---|---|---|
| Authoritative writers behind the LWW gate → recon deltas silently dropped, markers still written | **Confirmed** (systematic: stored docs carry wall-clock UpdatedAt, authoritative docs carry block-ts) | **Fixed**: BatchPutAccountsAuthoritative raw path + regression tests |
| Same gate could drop live ApplyTxAtomic writes | **Confirmed** (narrower window) | **Fixed** (same change) |
| False safety comments (bypass claim, one-ExecAll atomicity) | **Confirmed** | **Fixed** — comments truthful again |
| Recon crash window (accounts-first/markers-last) double-credits on replay, not bounded-by-filter | **Confirmed** | **Mitigated**: recon_intent guard fails LOUD on replay-after-crash; full fix = ThebeDB builder-2PC task. Live-path window (one tx) remains accepted until 2PC |
| To==nil deployment txs nil-panic block apply | **Confirmed** (2 deref sites) | **Fixed**: guards; deployments carry no recipient account |
| historical_balance.go inline fee-split copy, ignores FeeRecipients | **Confirmed** (read-only path) | **Fixed**: config.SplitFee; FeeRecipients-era per-recipient attribution → B8 (BlockRecord lacks the field) |
| Fee centralization otherwise | **HOLDS** — config.GasFee/EffectiveGasPrice/SplitFee single mutation-path formula, parity tests present; SC EVM computes no fees |
| PRE-2 intra-block ordering | **HOLDS** (live: sequencer order; recon: block-order groups, commutative deltas) |
| PRE-4 fee-split-only balance effect at the store | **HOLDS** — CommitToDB never persists balances; GetBalanceChanges has zero consumers. **Open operator decision:** EVM-internal value movements (payable forwarding, selfdestruct) are discarded — decide persist-through-central-apply vs reject value-bearing contract calls BEFORE any payable contract ships |
| Fee-grid health check vs FeeRecipients | Advisory: weighted splits put fee credits off the clean per-tx wei grid by construction — update the fleet's integer-multiple triage tooling before FeeRecipients activates |

Also fixed in-session: `isNotFound` helper lost with the R3 adapter deletion (surfaced by the
first full in-session compile — inherited static-only greens are exactly what the divergence
review warned about).


---

## External audit (THEBE-AUDIT-HLD Rev 4) — triage 2026-08-11

The first audit run with a working toolchain (probes, not inference). I spot-verified 10/10
CRITICALs against the tree — **all confirmed**; this audit is accurate where checked, unlike the
framing issues in the divergence-review round. **Record correction:** the audit falsifies my earlier
"Phase A gate green / go test -short green" claim. It was false — `go test -short ./...` fails in
`gossipnode/Security`, a regression MY (nil,nil) compat-shim (409ed52) introduced, missed because my
later test runs were scoped to changed packages and never re-ran `./Security/`. Owned and fixed.

### Fixed this round (confirmed, safe, verified in-session — Security + DB_OPs suites green)
| Finding | Sev | Fix |
|---|---|---|
| PRC-01 | HIGH | Security cache tests skip without a handle instead of panicking (d83f25a); recorded-green claim corrected |
| STO-01 | CRITICAL | storeAccountFromStore copies TxNonce/TxCountSent (edddd3c) — completes last round's authoritative-write P0, which had exposed a nonce regression |
| NET-01 | CRITICAL | CacheConsensuMessage RWMutex + snapshot iteration + nil-block guard (233cf5e) |
| SEC-02 (crash half) | CRITICAL | tx.Value nil guard before arithmetic (1a9622f) |

### Operator action required — I cannot and must not do these unilaterally
- **SEC-01 (CRITICAL, verified): three real BLS private keys are tracked in git** (bls_priv in
  AVC/**/config/bls.json) on feat/thebe-sc-layer + remove/immudb, and the repo is PUBLIC. .gitignore
  does not untrack them. This is an active key compromise: rotate all three keypairs, purge history
  (git filter-repo) on both branches, force-push, treat the old keys as permanently public, add a
  CI secret scan. These keys rode in from the remove/immudb branch; the merge did not create them
  but did carry them. **Do this first, independent of everything else.**

### Requires a scope/design decision before I touch code (verified real, NOT safe one-liners)
- **CON trust model (CON-01..04, CON-08, CON-12): consensus is forgeable by default.** Verified:
  keyAuthorized returns true for any key when SeedAuthorityBLSPub=="" (the shipped default);
  BlockHash commits to neither height nor parent (cross-height cert replay); vote-requester authz
  defaults off and fails open; catch-up/fastsync apply blocks with no certificate. These are a
  consensus-security redesign, out of the original reconciliation scope ("consensus core was never
  in scope for any prior review round"), and several must land together (CON-07+10/11/17). Needs an
  operator-pinned authority-key decision + design review — not a hasty patch.
- **EVM-01: the smart-contract layer is unreachable from main** (call-graph proven). This is an
  integration project or a formal shelving decision, per §6 of the audit. EVM-02..20 are downstream
  of it. Decide integrate-vs-shelve before any EVM code changes.
- **STO-02 (timestamp LWW across two hosts' clocks), STO-03/09/13 (2PC + batch atomicity, partly
  ThebeDB-repo), SYN-01..04 (fastsync — already gated off by B4-skip), NET-02..06, API-01..08**:
  each verified, each real, each needs its own scoped change with a regression test. Not landable as
  drive-by edits.

### Process findings accepted (P1-P6, PRC-01..07)
The audit's core process critique is correct and I own my share: I inherited numbers (B3's "9
advisories" vs 32 reachable), reported green from scoped runs, and filed the ThebeDB tasks as a repo
doc that the auditor could not see was real until this session created it. The lesson — verify
against executed code, not documents — is exactly right.


### Safe one-liner round (2026-08-11, operator-greenlit; build+vet+tests green on touched pkgs)
| Finding | Sev | Fix |
|---|---|---|
| CON-05 | CRITICAL | signature verified before checkAndMarkSeq, PREPARE + COMMIT (63f0e89) — closes the unauthenticated seq-censor. NOTE: still inert until CON-07 (engine never constructed); fixing it now means it's correct when CON-07 lands |
| NET-02 | CRITICAL | channel send+close serialized under mu, buffered 256 (ecb3376) — closes send-on-closed process death |
| CON-08 | HIGH | durable equivocation read error rejects (fail closed) (0167cd2) |
| STO-11 | MEDIUM | marker read error fails the block instead of re-applying (0167cd2) |
| SYN-08(a) | MEDIUM | recon_intent read error fails closed (0167cd2). SYN-08(b) wedge-forever still open — needs scoped-intent redesign, not a flip |
| P4 doc drift | LOW | ErrStaleNonce return-site + MaxOutboxAttempts(3) comments |

STOPPING HERE for review per operator decision. Deliberately NOT touched (need your decision /
design review): SEC-01 (operator key rotation — logged), CON-01..04/06/12 consensus trust model,
EVM-01..20 integration, STO-02/03/09 storage-atomicity semantics, SYN-01..07/09 fastsync
(B4-gated off), NET-03..06 gossip, API-01..08. All verified real and registered above.

Verification honesty: these gates were run in-session against ThebeDB @ 02f802e with the go1.26.5
toolchain; the full-binary link exceeds sandbox disk, so a full `go build ./... && go test ./...`
on the host is still the authoritative gate before merge.

---

## Audit Rev 8 (THEBE-AUDIT-HLD) — response, 2026-08-14

The auditor independently verified remediation round 1: **all 9 fixes real, correctly targeted, no
regressions; full `-short` gate genuinely green tree-wide (unscoped); NET-01 proven under `-race`.**
It flagged that three fixes were completed at the declaration but not the paired use site (the same
P4/P5 shape). Those paired residuals were mine to finish — done this round, build+vet+`-short` green
on touched packages (DB_OPs, thebegateway, messaging, PubSubMessages):

| Finding | Sev | Fix |
|---|---|---|
| STO-19 | LOW (latent) | storeAccountToStore write-converter now lossless (1f94831) |
| STO-20 | HIGH | GetSyncKV uses errors.Is(kv.ErrKeyNotFound), not a string match — my STO-11 had made this liveness-critical (1f94831) |
| CON-21 | MEDIUM-HIGH | equivocation durable WRITE path fails closed, pairing CON-08 (1cfcc76) |
| NET-08 | LOW | cacheConsensuMessage unexported → NET-01 invariant compiler-enforced (9b8520a) |

### Left open with rationale (not silently skipped)
- **STO-21** (no code): STO-11 routes storage flakiness into the uncertified catch-up path (CON-04),
  making CON-04 more load-bearing. Operational monitor: block-rejection rate. Ordering note only.
- **NET-07** (MEDIUM): NET-02's buffer traded 1-message loss for up to 256 on close. Net large
  improvement (was ~100% loss). Clean fix = don't close at all (remove close+reassign, rely on
  isStarted) — a small redesign, deferred to a scoped Pubsub pass.
- **NET-01 eviction / PRC-08 shared skip helper**: unbounded map growth and the `(nil,nil)` shim
  root cause (BulkGetAccounts_test still uses the never-firing guard) — both real, both a scoped
  cleanup, neither a correctness regression.
- **CON-11 residual** (from CON-05): PrepareProof still outside DigestCommit — a relayed *genuine*
  signed COMMIT can carry a spliced proof. Part of the CON-07+10/11 group that must land together;
  belongs to the consensus-trust-model decision.

### §15 ThebeDB-seam findings — deferred to the EVM/ThebeDB decision, NOT drive-by fixes
- **STO-22** (HIGH): cassata.appendRecord reflects `ThebeDB.Append`, a method that does not exist, so
  every contract receipt/registry SQL write silently errors on the loopback EVM path. Fix = typed
  builder.Append call (+ compile-time interface assertion). Lives inside the EVM integrate-or-shelve
  decision; fixing it revives EVM-24 (registry poisoning), which must be handled in the same change.
- **STO-23** (CRITICAL liveness), **STO-24** (no fsync), **STO-25** (no read integrity check),
  **STO-26** (CDC/planner) — all in the **ThebeDB sibling repo** (badger_store/builder/eventlog),
  out of jmdn scope per the reconciliation stop-condition. Append to ThebeDB/docs/TASKS-from-jmdn-
  reconciliation.md alongside T1/T2/T3.

### Still operator P0 (unchanged, pushing round 1 extended the exposure window)
- **SEC-01**: 3 BLS private keys tracked on the public remote. Rotate + purge history + force-push.
- **DEP-01**: go.mod still `go 1.25.0`; one line to `go 1.25.12` closes 29 reachable stdlib CVEs —
  cheapest item in the whole audit, do it in the next mechanical round if you want it batched.

---

## Deferred-item triage, one-by-one (2026-08-17)

- **DEP-01 — DONE** (e66665a): go 1.25.0→1.25.12 (29 reachable stdlib CVEs), x/text v0.39.0,
  otlploghttp v0.19.0. pion/dtls has no fix (v2→v3 migration) — still tracked. Verified: go mod
  tidy no-op, go mod verify clean; full CGO build is the host gate.
- **SEC-02 impersonation half — DONE** (da3ca59): TxOrigin (Block/origin.go) threads transport +
  peer into SubmitRawTransaction; bypass gated on origin.Trusted() (loopback/same-host), not tx
  shape. HTTP uses socket RemoteAddr (not spoofable ClientIP), eth gRPC uses peer.FromContext,
  JSON-RPC facade is OriginUntrusted. Trust logic proven in isolation; full CGO build = host gate.
