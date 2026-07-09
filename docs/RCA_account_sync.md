# RCA — Account State Corruption on Restart / Catchup (temp state)

Status: working document. Code-verified against this repo @ 2026-07-09.
Companion input: `rca2.md` (colleague agent diagnostic questions + prior findings). Verdicts below.

> **Update (post F1/F2, sync-up with Doc):** F1+F2 landed on `fix/account-recon-corruption`
> (commit `4324084`). Doc's branch `fix/CatchUpAccountMiss` contains a NEW verified
> corruption vector we missed — mixed-unit LWW timestamps (§3a below) — plus a partially
> overlapping F1/F2. Merge plan in §7. F3-F5 still open on both branches.

---

## 1. Symptom

Block reconciliation is solid (dedup, gap scan, hash checks self-heal via `buildMissingTag`).
Account state (accountsdb) drifts/corrupts, correlated with node restarts/downtime and sync triggers.

---

## 2. State inventory — every "last/latest block" key

Five stores track sync position. **None share a transaction.**

| Store | Key | Written by | Read by |
|---|---|---|---|
| SQLite `key_value` | `fastsync:last_reconciled_block` | `markReconComplete()` `FastsyncV2/fastsyncv2.go:712` | `effectiveReconRange()` `fastsyncv2.go:694`; call sites `catchup.go:286,302`, `fastsyncv2.go:480,498,605,617` |
| SQLite `index_meta` | `last_indexed_block` | `txindex.go:446` — **monotonic-max guarded** (`setMetaMonotonicMax` `txindex.go:478`) | `txindex.go:461` |
| ImmuDB defaultdb | `latest_block` | `StoreZKBlock` `immuclient.go:1949` (every block, blind); `immudb_data_writer.go:200` (batch end); `immudb_headers_writer.go:118` (snapshot-restore); `catchup.go:342` (phase 8) | `GetLatestBlockNumber` `immuclient.go:2230` → NodeInfo adapter, explorer, stateroot, gETH |
| ImmuDB defaultdb | `header_latest_block` | `immudb_headers_writer.go:110` | `immudb_adapter.go:87` |
| ImmuDB accountsdb | `<Prefix><addr>`, `<DIDPrefix><did>` account objects | `BatchRestoreAccounts` `account_immuclient.go:330` (LWW on `UpdatedAt`); direct `immudb_account_manager.go:291`; async Redis worker `account_sync_worker.go:305` | `GetAccount*`, gETH `eth_getBalance`, recon |

Entry point wiring (`main.go:1155-1214`): SyncMonitor → `ReconcileFunc` → `HandleCatchUpSync(cfg.FastSync.CatchUpFromBlock)` → phase 5 recon → SQLite key.

---

## 3. Validation of rca2.md diagnostic questions

### Q1 — eth_getBalance: STORED vs REPLAYED → **STORED. Verdict: not a corruption source, but exposes it.**
`gETH/Facade/rpc/handlers.go:143` → `Facade/Service/Service.go` → `DB_OPs.GetAccount` (stored `Balance` field on the account object in accountsdb). No replay. Balances shown are exactly whatever the writers below last wrote.
Side finding: the getBalance path can **auto-create + propagate a DID** (`Service.go:232`) — a read endpoint that writes account objects. Another uncoordinated writer.

### Q2 — ADDITIVE vs ABSOLUTE → **ABSOLUTE at the DB layer; ADDITIVE one layer up. Verdict: partially wrong premise, real bug found.**
- `processBatch` (`account_sync_worker.go:305`) writes **full absolute account objects** via `BatchRestoreAccounts`. Redis redelivery (PEL + `XAUTOCLAIM`, `reclaimPending` `account_sync_worker.go:258`) is therefore NOT a direct double-count vector.
- BUT the additive step lives above: `ReconcileWithDeltas` does read-balance → add delta → emit absolute `NewBalance` (`accountUpdateWire`, `account_sync_worker.go:75`). Classic **read-modify-write with an async write** → lost updates when live processing writes between the read and the drain.
- **Confirmed bug (new): `parseUpdatesPayload` stamps `UpdatedAt: time.Now()` at parse time** (`account_sync_worker.go:484`), i.e., at *drain* time, not at delta-computation time. A replayed/reclaimed stale update gets a fresh timestamp and **wins LWW over newer correct data**. Stale balance resurrection after any worker crash/restart.
- **Confirmed bug (new): field clobbering** — same function hardcodes `DIDAddress: w.Address` (hex addr, not the real DID), `AccountType: "user"`, drops `CreatedAt`/`Metadata`/DID key emission. Every recon update degrades the account object beyond balance.

### Q3 — Two-writer overlap → **CONFIRMED. This is the core defect.**
Live path `messaging/BlockProcessing/Processing.go` (`processTransaction` → `deductFromSender`/credits, lines 585-688) and recon path (`computeAccountDeltas` + `ReconcileWithDeltas`) both mutate the same accounts. `deltas.go:9` says it mirrors Processing.go. The only coordination is the SQLite watermark — **which live processing never advances**. Every catchup re-applies deltas for blocks the live path already applied since the last `markReconComplete`. In catchup, AccountSync/FetchAccounts (phases 3.5/4, `catchup.go:238-278`) pull full remote account objects (already reflecting remoteTip state), then phase 5 applies deltas on top → double-apply for fetched accounts.

### Q4 — Idempotency → **NONE at the account level. Verdict: confirmed.**
No per-tx applied marker, no per-account height, no stream dedup key. Only the single uint64 watermark, which (a) can't represent partial application, (b) is advanced optimistically (see H2/H3), (c) lives in a different store than the data it guards.

### Q5 — Formula drift → **CONFIRMED. Deterministic divergence, restarts not even required.**
EIP-1559 (type 2):
- Live: `effective = min(maxFee, 35 gwei baseFee + tip)`; nil-maxFee fallback = **35 gwei** (`Processing.go:815-837`).
- Recon: `effective = MaxFee → MaxPriorityFee → GasPrice → **1 gwei**` — **no baseFee, no min-clamp** (`deltas.go:147-171`).

Any type-2 tx with `maxFee > baseFee+tip`: live charges `base+tip`, recon charges `maxFee`. Recon "corrects" balances to *different wrong* values → Merkle mismatch persists → SyncMonitor fires again → repeat. Gas split (coinbase half+remainder / zkvm half) matches on both sides (`Processing.go:603-607` vs `deltas.go:81-84`) — note `Processing.go:605` comment contradicts its own code; code is what matches. Both use gasLimit (not gasUsed) — consistent, if economically odd.

---

## 3a. NEW (from Doc's e60ff10, code-verified) — Mixed-unit LWW timestamps

**Live executor stamps `UpdatedAt = blockTimestamp` in Unix SECONDS** (`Processing.go:875,921`
via `deductFromSender`/`addToRecipient`), while every sync path (recon updates, account sync,
drain worker) stamps `time.Now().UnixNano()`. LWW compares them raw
(`account_immuclient.go`, pre-fix), so a nano-stamped value exceeds a second-stamped value by
~9 orders of magnitude:

**Once any sync/recon write touches an account, every subsequent LIVE write loses LWW
forever.** Live execution keeps computing correct balances and they keep being discarded on
the next sync write; the account is pinned to whatever the last sync computed (which, pre-F1,
also used the wrong gas formula). This compounds with Q5: wrong values, then made sticky.

Fix (e60ff10): `normalizeUpdatedAtNanos()` — range-based unit detection (s/ms/µs/ns) applied
to BOTH sides of every LWW comparison in `BatchRestoreAccounts`, with unit tests including
the ordering-inversion case. Correct approach; stored values stay mixed-unit, comparisons
become unit-safe.

Also in e60ff10, verified good:
- `EqualFold` DID guard — legacy forged DIDs were lowercase, `Address.Hex()` is EIP-55
  checksummed, so the old case-sensitive "mistakenly set to hex address" guard NEVER matched.
- Monotonic guards: `TxNonce`/`TxCountSent` never decrease on merge; `Nonce==0` (producer had
  no value, e.g. receiver-only delta) preserves the existing identity nonce.

---

## 4. Clean consolidated hypothesis

**Root cause: account balances are maintained by two (plus) writers that compute effects with different formulas, apply them non-idempotently, and coordinate only through a monotonic watermark stored in a different database that only one writer updates.**

Failure chain on a typical restart:

1. Last catchup set `fastsync:last_reconciled_block = T` (SQLite).
2. Node restarts. Live pubsub processing resumes, applies blocks `T+1..T+k` to accountsdb. Watermark stays `T`.
3. Restart/downtime → seednode Merkle mismatch → SyncMonitor fires `HandleCatchUpSync`.
4. Phase 3.5/4 fetch remote account objects @ remoteTip state (LWW may accept them).
5. Phase 5: `effectiveReconRange` → reconFrom `T+1` → **re-applies deltas for blocks already applied live** (Q3), **with a different gas formula** (Q5), on top of possibly already-tip-state fetched objects (H5), through an **async queue whose replay path forges fresh LWW timestamps** (Q2).
6. `markReconComplete(remoteTip)` persists even when accounts failed (`err == nil`, `failedAccounts > 0`, `catchup.go:295-303`) and even when blocks in range were data-incomplete (phase 5 runs before phase 8 verification; `computeAccountDeltas` silently skips empty/skeleton blocks, `deltas.go:41`).
7. Result: some accounts double-applied, some under-applied, some clobbered with stale/defaulted objects. The watermark now blocks any retry of the damaged range. Blocks self-heal; balances never do.

Secondary (feeds trigger frequency, matches original "wrong start point" hypothesis): `latest_block` has no monotonic guard — `StoreZKBlock` `immuclient.go:1949` blindly overwrites per block; HeadersWriter snapshot/restore (`immudb_headers_writer.go:43,118`) races DataSync workers; `ReconcileBlockNumber` only heals +500 (`immudb_adapter.go:135`); `HandleStartupSync` anchors at localTip (`fastsyncv2.go:269`) against catchup.go's own warning (`catchup.go:66-68`).

Ruled out / downgraded:
- Redis redelivery double-count as such (writes are absolute) — the real Redis issue is the parse-time `UpdatedAt` + lazy worker startup (pending entries sit undrained until the next enqueue, `account_sync_worker.go:130`).
- `eth_getBalance` read path (stored, faithful).
- Gas split coinbase/zkvm mismatch (code matches; comment is wrong).

---

## 5. Fix plan (ordered by impact, smallest safe diffs first)

**F1 — Unify the gas/fee formula (deterministic bug, do first).**
Extract one shared `EffectiveGasPrice(tx)` (incl. 35 gwei baseFee + min-clamp + fallbacks) used by BOTH `Processing.go:parseTransaction` and `FastsyncV2/deltas.go`. Without this, every other fix still converges to wrong balances.

**F2 — Fix LWW timestamp forgery + field clobbering in the drain worker.**
`account_sync_worker.go:parseUpdatesPayload`: carry `UpdatedAt` from the producer (add to `accountUpdateWire`, set at delta-computation time); merge into the existing account instead of constructing a defaulted object (preserve real `DIDAddress`, `AccountType`, `CreatedAt`, `Metadata`, DID key).

**F3 — Make the recon anchor honest.**
- Call `markReconComplete` only after phase 8 verification PASS, never when `failedAccounts > 0` or the delta iterator errored.
- Move the anchor out of SQLite into **accountsdb itself** (`accounts:last_applied_block`), updated by BOTH live processing and recon. Single store = survives volume restores together; live path advancing it kills the H1 double-apply window.

**F4 — Make account application idempotent.**
Either (a) recon writes absolute balances recomputed from a verified base (snapshot @ block N + deltas N+1..tip in one atomic batch that also writes the anchor), or (b) per-account `last_applied_block` field checked before applying a delta. (a) is simpler and matches the existing absolute-write DB layer.

**F5 — Serialize the writers during recon.**
Pause/queue live account application while phase 5 runs (blocks can still be written; only balance application defers), or route live effects through the same delta engine. Eliminates read-modify-write races with the async queue.

**F6 — `latest_block` hygiene (secondary).**
Monotonic-max guard (mirror `txindex.setMetaMonotonicMax`); delete the per-block write at `immuclient.go:1949` (DataWriter batch-end owns it); drop HeadersWriter snapshot/restore in favor of never touching `latest_block` there; make `HandleStartupSync` anchor at `catch_up_from_block` like catchup does.

**F7 — Ops/lifecycle.**
Drain Redis PEL eagerly at boot (worker currently lazy — `account_sync_worker.go:130`); reset `fastsync:last_reconciled_block` on any AVRO/bootstrap restore of accountsdb.

---

## 6. Branch comparison & merge plan (sync-up with Doc)

Two branches, overlapping fixes:

| | ours `fix/account-recon-corruption` (4324084) | Doc's `fix/CatchUpAccountMiss` (8949d06 + e60ff10) |
|---|---|---|
| F1 gas formula | **Superior**: scalar-param `config.EffectiveGasPrice/GasFee` — ONE implementation for both live and recon; historical semantics preserved (nil-checks); GasLimit==0→21000 fixed; exhaustive golden test | 8949d06 keeps a **duplicated formula copy** in deltas.go ("update in same commit" comment); changes consensus semantics (`Sign()<=0`→35gwei — old blocks replay differently); GasLimit==0 drift left unfixed |
| Facade sweep | Ported from his 8949d06 | Origin of it |
| Wire `UpdatedAt` + producer stamp | Yes (identical intent) | Yes (identical intent) — trivial conflict |
| Update clobbering fix | Worker-side merge (`buildUpdateEntries`): +N DB reads/batch; new accounts get proper CreatedAt/AccountType; unit tests | **DB-layer** (`BatchRestoreAccounts` hardened merge): fewer reads, covers ALL write paths; new-from-update accounts keep zero CreatedAt/AccountType |
| Mixed-unit LWW normalization (§3a) | **Missing** | **Yes — must take** |
| EqualFold DID guard, monotonic counters | Missing | Yes — take |
| Eager PEL drain at boot | Yes | Missing |
| Tests | gasfee golden + worker merge/timestamp/retry (10) | normalize_ts (1) |

**Plan:**
1. Base = ours (F1 must be the scalar-API version; his 8949d06 must NOT merge — superseded).
2. Cherry-pick from e60ff10: `DB_OPs/account_immuclient.go` (normalization + hardened merge)
   + `DB_OPs/normalize_ts_test.go`. Different files → applies clean on our branch.
3. Worker overlap — one decision: (a) keep our worker merge as belt-and-braces, or
   (b) simplify to his zero-valued-fields parse shape and rely on the hardened DB merge
   (lean (b) for perf; fold in our new-account CreatedAt/AccountType handling; adapt tests).
4. Both wire formats are compatible (`updated_at`, UnixNano, omitempty) — no migration needed.
5. Then F3 (recon watermark honesty) — untouched by both branches, still the dominant
   restart-corruption path. F4/F5 after.

## 6b. F3 Phase-0 ground truths (branch f3/recon-watermark-honesty @ a6c6e78)

**T1 — Live application path.** `ProcessBlockTransactions` (`Processing.go:83`) applies all
account effects per block with snapshot+rollback atomicity (`:115-142`, `:227`, `rollbackState:357`).
Callers: `blockPropagation.go:349` (pubsub, non-sequencer live path) and `broadcast.go:779`
(`ProcessBlockLocally`, sequencer consensus — `consensus_statemachine.go:235`). No cross-block
ordering guarantee; replay protection relies solely on the `block_processed:<hash>` guard (`:96-110`).

**T2 — Persistent markers exist, but are SPLIT-BRAIN across DBs (NEW FINDING — H0).**
- Markers: `tx_processing:<hash>` (written at tx start), `tx_processed:<hash>` +
  `block_processed:<hash>` (written in ONE atomic ExecAll after balances commit, `:258-281`).
- **Reads always hit maindb**: `Exists` → `Read` → unconditional `ensureMainDBSelected`
  (`immuclient.go:374`), even on accounts-pool connections.
- **The atomic marker write hits whatever DB the session last selected**: `Transaction` →
  `ExecAll` with NO selection (`immuclient.go:1697-1732`). By the time it runs, the session was
  last selected to accountsdb by account reads/writes (`GetAccount` → `ensureAccountsDBSelected`,
  `account_immuclient.go:782`).
- **EMPIRICAL RESULT (jmdn-mainnet-6, 2026-07-09, block 12077): prediction WRONG in direction.**
  Current markers (`block_processed:`/`tx_processed:` for tip block, value 1783583380) are in
  **defaultdb** — same DB the guards read → **dedup guards currently work**. BUT scans show BOTH
  DBs contain `tx_processed:` keys: defaultdb has Nov-2025 + current markers; accountsdb has a
  separate **historical cluster (~Dec 2025, values 176697xxxx) invisible to the guards**.
- Reclassified H0: (a) marker placement is an accident of session call-order (last main-selecting
  call before the ExecAll) — fragile, must be made explicit; (b) the Dec-2025 accountsdb cluster
  means recon marker-exclusion MUST dual-read both DBs or it re-applies those live-applied txs.
- Cross-DB restore caveat: markers (defaultdb) describe balances (accountsdb). An accountsdb-only
  restore rolls balances back while markers still say "applied" → marker exclusion would wrongly
  skip. Mitigation: anchor lives in accountsdb (rolls back WITH balances, re-opening the range);
  documented residual: post-restore repair must ignore markers.
- Side observation (backlog): block 12077 extradata carries block_number 18491538 vs blocknumber
  12077 — two numbering schemes coexist.

**T3 — Watermark call sites** (unchanged from §2): `catchup.go:286,302`, `fastsyncv2.go:480,498,605,617`.

**T4 — Both writers coexist on catchup nodes.** Confirmed: pubsub live processing runs on
non-sequencer nodes (`blockPropagation.go:349`) — the same nodes that run `HandleCatchUpSync`.
I2 (no double-apply) is a real requirement, not theoretical.

**T5 — Anchor storage.** `BatchRestoreAccounts` only routes `address:`/`did:` prefixed entries
through merge machinery (`account_immuclient.go:389-393`); a `sync:`-prefixed anchor key written
via explicit accounts-selected `SafeCreate` bypasses it safely.

**T6 — Rollback.** `rollbackState` restores balances; markers for rolled-back txs are cleaned
(`:240-244`). Anchor advancement must therefore happen only on the success path AFTER the atomic
marker commit (`:258-315`).

**Known residual (accepted, documented):** balances commit BEFORE markers (`:161-253` vs `:258`).
A crash in that window leaves applied-but-unmarked txs → a later replay/recon can re-apply them.
Bounded to the crash window; full fix (balances+markers in one ExecAll) is F4 scope.

## 6c. F3 design decision D1 → D1a (anchor + marker exclusion), with H0 prerequisite

Chosen: **D1a**, amended. A single anchor advanced by the live path alone is unsound (gap paradox:
advance past a gap → I1 violation; don't advance → recon re-applies live-processed blocks → I2
violation). The persistent `tx_processed:` markers resolve the paradox at tx granularity — but only
after H0 is fixed. Plan, in dependency order:

1. **H0 fix (prerequisite): marker DB consistency.** All marker reads AND writes explicitly target
   accountsdb (co-located with the balances they describe — satisfies I3 for markers). Marker
   helpers in Processing.go with explicit selection; migration: dual-read (accountsdb, then maindb
   fallback) for existing markers, write accountsdb only. This alone fixes the live-path replay
   double-apply, independent of recon.
2. **Anchor** `sync:accounts_last_applied_block` in accountsdb. Monotonic-max writes only.
   Writers: (a) live path, end of successful ProcessBlockTransactions, only when
   block == anchor+1 (contiguity rule); (b) recon, only after phase-8 verification PASS and
   failedAccounts == 0 and no delta-iterator error.
3. **Recon range derivation**: from = anchor+1 (accountsdb). SQLite `fastsync:last_reconciled_block`
   becomes read-only legacy (logged for comparison, never trusted — it is the dishonest value we
   are replacing). `computeAccountDeltas` EXCLUDES any tx whose `tx_processed:` marker exists
   (dual-read) → I2 holds for live-processed blocks above the anchor; gap blocks have no markers
   and get full deltas → I1 holds.
4. **markReconComplete** rewrites the accountsdb anchor (gated as above) instead of SQLite.
5. **Timestamp-at-source**: `Processing.go:875,921` stamp `blockTimestamp * int64(time.Second)`
   (normalizeUpdatedAtNanos remains the compare-time safety net).

Failure modes: crash before marker commit → replay re-applies (residual, F4); crash after markers,
before anchor → anchor lags, recon excludes marked txs → no double-apply (I2 holds); accountsdb
restore → anchor and markers restore WITH the data (I3 holds); SQLite wipe → irrelevant (legacy).

## 6d. F3 branch comparison — `f3/recon-watermark-honesty` (ours) vs Doc's `fix/f3` (7643df0)

Both fork from a6c6e78; independently converged on the anchor skeleton: accountsdb
placement (I3), pure decision function, contiguous-only live advance (I1 gap paradox),
monotonic recon advance, err==nil && failedAccounts==0 gating, non-fatal live errors.

| Requirement | ours | Doc's 7643df0 |
|---|---|---|
| I3 anchor in accountsdb | ✓ `sync:accounts_last_applied_block` (JSON uint64) | ✓ `recon:last_reconciled_block` (decimal string) |
| I1 live contiguity | ✓ NextLiveAnchor | ✓ reconAnchorNext(contiguous=true) — identical semantics |
| H3 failed-accounts gate | ✓ | ✓ |
| H2 verification gate | ✓ catchup marks in phase 8 after PASS; HandleSync/PoTS re-verify via buildDataMissingTag | ✗ still marks in phase 5, before verification; silent iterator skips remain |
| **I2 double-apply defense** | ✓ tx_processed marker exclusion in deltas (dual-DB) | **✗ none — relies on "recon re-covers with absolute writes (idempotent)", which is FALSE: ReconcileWithDeltas applies RELATIVE deltas.** Distinguishing scenario: downtime gap [T+1..B-1], live resumes at B (anchor correctly stuck at T), catchup covers [T+1..tip] → re-applies deltas for live-processed B..tip → double-count. H1 unfixed for exactly the downtime case. |
| MaxUint64 poison | ✓ capped at local tip | ✗ unfixed — and now writes MaxUint64 into the AUTHORITATIVE accountsdb anchor (worse than SQLite) |
| Writer serialization | ✗→✓ **adopted his mutex.** Adversarial check disproved our "benign race" claim: live(read 10) → recon(write 500) → live(write 11) regresses the anchor, re-opening a range of RECON-applied txs (no markers) → double-apply | ✓ reconAnchorMu — the one thing his branch had that ours lacked |
| Migration | ✓ seed-once from SQLite (survives accountsdb restore: key exists → SQLite never consulted again) | ✗ read-time max(anchor, SQLite): after an accountsdb restore the surviving SQLite value re-poisons the range → stale balances |
| H0 explicit marker DB, dual-read, timestamp-at-source, fail-closed deltas | ✓ | ✗ absent (he was not in the empirical-check loop) |
| Tests | anchor rules + gating + delta exclusion (3 scenarios incl. fully-live-applied block) | anchor rules only |

**Merge plan:** base = ours; mutex adopted (done); one key name to agree — recommend ours
("applied" is the true semantic; the live path advances it, "reconciled" doesn't cover that).
Review note for Doc: the "absolute writes / idempotent" premise in 7643df0's commit message
is load-bearing and wrong — it is why I2 was skipped there.

### 6d-i. Hardening self-review (same adversarial standard applied to our own branch)

Three real bugs found in our F3 implementation and fixed before handoff:

1. **Nil-connection write path (would have failed every markReconComplete).** The original
   Advance* functions acquired a pooled connection inside GetAppliedAnchor, released it, then
   passed the caller's (possibly nil) conn into the write step → `ensureAccountsDBSelected(nil)`
   errors. Rewritten as a single locked read-decide-write cycle (`advanceAnchor`) that acquires
   the connection ONCE for both steps — convergent with Doc's `getAnchorConn` shape.
2. **Seed could import the MaxUint64 poison.** Pre-F3 nodes may carry MaxUint64 in the legacy
   SQLite watermark (the very bug being fixed); seeding it verbatim would poison the NEW anchor.
   Added `CapAnchorTarget(target, tip)` — applied to both the migration seed and every recon
   advance — with tests. (Doc's read-time `max()` variant has the same exposure, uncapped.)
3. **Marker filter used the auto-return connection getter** (`GetMainDBConnectionandPutBack`),
   whose recycle-at-deadline goroutine races long GetAll sequences — the exact pattern the
   drain worker's module header warns against. Switched to explicit Get/Put.

Known accepted trade-off: the anchor mutex is held across two immudb RPCs (read+write, ≤15 s
ctx). Under pool exhaustion the recon-side advance can time out — it fails SAFE (anchor lags,
range retries). Not a deadlock: bounded by the acquire ctx.

## 6e. F4 design gate record (2026-07-09, both operators)

Scope: close the two documented F3 residuals — (R1) live balances-before-markers crash
window; (R2) recon leaves no markers / re-run double-apply.

**Phase-0 ground truths (merged from both operators, all code-verified):**
- One tx = up to 6 independent commits across 2 DBs (Doc G1): sender/recipient/coinbase/zkvm
  each a separate accountsdb commit, then the marker Create in defaultdb. The crash window is
  PER-TX (mid-tx crash = sender debited, recipient never credited, no marker → replay
  re-debits), not just block-end.
- Cross-DB atomicity impossible (G2≡T2): ExecAllRequest has no database field; balances
  (accountsdb) and markers (defaultdb) can never share a transaction → any atomic design
  forces marker relocation to accountsdb.
- Within-block read dependency (G3≡T1): tx2 reads tx1's committed write.
- **ExecAll cap = 1024 entries** (ours, T3: immudb DefaultMaxTxEntries, options.go:37).
- **C2 (NEW, latent chain-halt on merged base):** the current block-end marker commit writes
  2×txs+1 entries in ONE ExecAll → any block >511 txs fails the commit → rollback → permanent
  block failure → node halt. Max theoretical block ≈1428 txs. Never fired (observed ~15 tx
  blocks) but must be fixed; D-B fixes it by construction.
- Recon's only atomic point is the drain worker's BatchRestoreAccounts ExecAll (G5);
  ReconcileWithDeltas returning means ENQUEUED, not applied.

**Decision: Doc's D-B (per-tx atomic) + 3a (markers via wire), with amendments A1-A3.**
Our block-atomic D2a withdrawn: it hits the 1024 cap at ~340-tx blocks (fallback complexity),
changes mid-block read visibility, and rips out the rollback machinery — D-B is always under
the cap (≤~10 entries/tx), keeps read semantics, and closes the window at tx granularity
(applied prefix fully marked → replay resumes exactly).

**A1 (blocking) — rollback/marker revocation.** Per-tx durable markers + whole-block
rollbackState are incompatible as proposed: prefix markers survive the rollback (immudb is
append-only, no delete) → replay skips the prefix txs against rolled-back balances →
permanent skip (I1). Fix: value-aware markers — rollback overwrites prefix markers with a
revoked sentinel (-1); ALL marker consumers become value-aware (the three Exists guards at
Processing.go:97,180,475 and FilterProcessedTxMarkers treat -1 as not-processed). Test must
pin rollback-then-replay.

**A2 — 3a ordering + cap.** Recon marker entries commit strictly AFTER all account chunks of
the batch (markers-last): losing markers = bounded double-apply on retry (repairable);
markers-before-accounts inverts to silent permanent skip. Per-batch entry accounting must
respect the 1024 cap (500 accounts ×2 + hash list can exceed it).

**A3 — reuse.** Guard dual-read (accountsdb → defaultdb legacy) reuses the
FilterProcessedTxMarkers machinery; no second dual-reader.

Division of labor: Doc implements D-B+3a+A1-A3 on `f4/atomic-account-application`; we run the
adversarial review (roles reversed from F3). Our `f4/atomic-apply` branch stands by and is
deleted if his lands clean.

## 6f. F4 adversarial review — APPROVED (2026-07-09)

Branch: `f4/atomic-account-application` @ 6dd9727 (dc67138 D-B+A1+C2, 6dd9727 3a/A2).
Reviewed against gate §6e. Full build + test gate green on the branch (operator-run).

**Gate compliance (all verified in code, not from the commit messages):**
- D-B: txStage read-through preserves sequential semantics (self-transfer, sender==coinbase);
  ApplyTxAtomic = staged docs + tx_processed marker in ONE accountsdb ExecAll (≤5 entries);
  commit failure fails the tx (correct — nothing landed).
- A1: revoke-before-restore, revocation failure aborts rollback (applied+marked stays
  consistent); every crash window fails toward bounded double-apply, never skip.
- A1 consumers: FilterProcessedTxMarkers rewritten value-aware with accountsdb-presence
  precedence (a -1 revocation cannot be overridden by a stale defaultdb legacy marker);
  IsMarkerApplied dual-read for point checks; block-level prefilter is FAIL-CLOSED.
- A2: drain commits recon markers strictly after all account chunks; wire rejects
  applied_at ≤ 0 (a -1 on the wire would revoke a live marker); separate ExecAlls under
  the 1024 cap. Marker enqueue only on clean recon; appliedHashes = included (non-excluded)
  txs only, so unverified/data-missing blocks' txs are never marked.
- A3: single dual-reader; tx_processing advisory locks deliberately left in defaultdb.
- C2: the 2N+1 block-end ExecAll is GONE (chain-halt landmine on >511-tx blocks removed);
  block marker is single-entry + non-fatal.

**New residual found in review (PROBE D) — opens the F5 ledger:**
The recon anchor can be AHEAD of the database by design: markReconComplete runs after
data verification, but recon balance effects are still on the Redis queue (enqueue ≠
applied — Doc's own G5). Redis queue loss (crash without persistence, eviction, flush)
⇒ anchor claims applied-≤-N for effects that never landed ⇒ silent skip (I1). Pre-existing
since F3; F4's marker-enqueue makes the queue dependency load-bearing.
Remedies: (ops, IMMEDIATE) Redis persistence `appendonly yes` is now a CORRECTNESS
requirement — document in DOCKER.md/deploy docs; (F5) gate anchor advance on drain
confirmation or queue-depth==0 check.

**Accepted residuals (disclosed by implementer, concurred):** XAUTOCLAIM cross-page
marker/balance reorder (bounded — both messages eventually apply; markers idempotent);
rollbackState balance restore still per-account commits (fails toward re-apply due to
revoke-first ordering).

**Test debt (agreed follow-up — integration harness with dockerized immudb, build-tagged):**
A1 rollback-then-replay; C2 big-block (>511 txs); PROBE-D Redis-loss scenario;
FilterProcessedTxMarkers precedence (revoked-in-accountsdb vs applied-in-defaultdb).

**Nit (non-blocking):** markerValueApplied treats unparseable values as APPLIED — defensible
(all legacy values are JSON int64s) but deserves a comment linking to the repair job.

## Remaining ledger after F4
- F5: PROBE D (anchor vs queue durability) + recon read-add-write vs concurrent live write.
- F6: latest_block monotonic guard; HandleStartupSync localTip anchoring.
- Integration test harness (above).
- One-time repair job for historically corrupted balances (three-way triage + nonce-regression
  signature).
- Housekeeping: DB_OPs/Tests/Merkle_test.go references a nonexistent package (broken since
  before F1); sqlops TestGetConnectedPeers requires a live DB file (should skip when absent).

## 6a. Verification plan

1. Unit: golden gas-fee test comparing `Processing.go` vs `deltas.go` effective price across type 0/1/2 × {nil, low, high} fee fields — fails today, passes after F1.
2. Replay test: apply blocks live → run catchup over same range → assert balances unchanged (idempotency, F3-F5).
3. Crash test: kill worker mid-drain, restart, assert reclaimed entries do not overwrite newer writes (F2).
4. Chaos: restart node mid-catchup at each phase boundary; diff accountsdb against a from-genesis recompute.
5. Field triage on a corrupted node: for suspect accounts, recompute balance from genesis via tx scan; classify error as `+delta` (double-apply → H1/H5), formula-sized diff (Q5), or stale object (Q2) — tells you which fix would have prevented each instance.
