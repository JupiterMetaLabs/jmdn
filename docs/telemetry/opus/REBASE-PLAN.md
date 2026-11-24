# Rebase — executed 2026-10-26

**From:** frozen baseline `efa45013` (2026-09-21) · **To:** `ab4f5013` / PR #258 (2026-10-23)
**Scale:** 113 commits · 606 files · +33,494 / −30,854
**Verdict: the architecture survives unchanged. All twelve load-bearing claims hold. The migration surface shrank 8–13% in every dimension but one.**
**Companions:** `JMDN-V3-TELEMETRY-ARCHITECTURE.md` (normative, frozen) · `DECISION-RECORD.md` · `STEP-1-CI-GATE-PATCH.md`

`jmdn-dev` and `origin/v3base` are now the same commit, which removes the rebase-target ambiguity the
earlier plan flagged as its main risk.

---

## 1. Executive status

| | |
|---|---|
| **Design** | Frozen 2026-09-25. **Nothing in the 113 commits invalidates it.** |
| **Surface** | Shrank. `logger()` reads −122, `fmt.Print*` −87, `context.Background()` in logs −26, `function` −43, `topic` −62, ion-importing files −16 |
| **Already done by the team** | CI on `v3base` · 12 dead DB metrics deleted · `peer_id` cardinality bounded · a bounded-label panic counter |
| **Moving the wrong way** | **zerolog: 224 → 239 calls, 34 → 36 files.** The only metric that grew |
| **Blocker** | Unchanged and organisational: no named production observability owner |
| **Effort** | **55–85 engineer-days** unchanged; the shrinkage is inside estimate noise |

---

## 2. The team has independently built part of the plan

Three items landed without reference to these documents, and all three converge on the design.

### 2.1 CI runs on `v3base` — our single highest-priority recommendation

```yaml
push:         branches: ["main", "v3base", "release/**"]
pull_request: branches: ["main", "v3base", "release/**"]
```

**Patch 1 of `STEP-1-CI-GATE-PATCH.md` is complete.** Every enforcement mechanism in §10 now has a
live surface to run on. This was the precondition for everything else in §10.

### 2.2 The twelve dead DB-pool metrics are gone

`metrics/DBMetrics.go` (455 lines, zero callers outside `metrics/`) is **deleted**, replaced by
`metrics/db_task_pools.go`. All twelve `main_db_*` / `accounts_db_*` declarations removed.
Declarations went **43 → 32**.

Our finding was "15 statically unobserved; inventory before deleting." **Twelve are dispositioned.**
The remaining three — `p2p_heartbeats_failed_total`, `p2p_log_entries_total`,
`jmdn_tx_status_submit_log_records` — still need the consumer inventory. W3's disposition workload
drops by roughly 30%.

### 2.3 `peer_id` is bounded — by a different mechanism than ours

PR #223. `metrics/labels.go:19` sets `maxPeerLabels = 256` with an LRU whose eviction callback
**deletes the evicted peer's series from every peer-keyed metric**, and `ProtocolLabel` maps unknown
message types to `"other"`. The commit message is explicit: *"Label names are unchanged, so the
existing dashboards keep working."*

This defuses the bomb. **It does not implement our decision, and the difference is worth a deliberate
call rather than silent acceptance:**

| | Their approach | Ours (§8.1) |
|---|---|---|
| `peer_id` label | retained, LRU-bounded at 256 | removed; peer detail lives in logs and spans |
| Steady-state series | ~3,584 from the heartbeat histogram alone (256 × 14) | 0 |
| Dashboards | unchanged | require migration |
| Series **churn** under ID rotation | **unbounded** — every eviction deletes series and every new peer creates them | none |

Bounding the *count* does not bound the *churn*, and churn is what costs Prometheus: new series mean
new head-block entries and WAL growth regardless of the steady-state ceiling. An adversary rotating
peer IDs still drives continuous create/delete cycles.

**Recommendation: accept their fix for v3 and revisit in v3.1, not now.** It removes the
140,000-series hazard, which is what made this a gate item, and `labels_test.go` proves the bound. The
churn question is real but is a tuning matter against a mechanism that already exists — not a reason
to reopen a shipped fix or to block the scrape path.

**Consequence for the plan: v3 gate step 2 is satisfied.** The ordering constraint — "remove
`peer_id` *before* enabling scrape" — no longer binds, because enabling scrape no longer arms a bomb.
That removes a sequencing dependency from W1.

### 2.4 A new metric that follows our label discipline

`jmdn_handler_panics_recovered_total{where}` (`metrics/metrics.go:47-53`), with the comment:
*"`where` is a fixed handler name / protocol ID — bounded, never taken from a message."*

That is §8.1's rule, arrived at independently. Also added: `consensus_legacy_votes_total`,
`validator_vote_ingest_total`. **Metric work is happening; it is simply not following a schema**,
which is the gap W3 closes.

---

## 3. The twelve load-bearing claims — all hold

| # | Claim | Status at `ab4f5013` |
|---|---|---|
| 1 | Both selection paths discard `usedFallback` | **HOLDS** — 2 sites, `vrf.go` |
| 2 | Round outcome computed, discarded into a 20-slot ring | **HOLDS** — `Sequencer/Consensus.go:1695` (was `:1635`) |
| 3 | Span error status never set | **HOLDS** — `SetStatus` = **0** |
| 4 | ion metrics dark in jmdn | **HOLDS** — `.Meter()` = **0** |
| 5 | Metrics unreachable by default | **HOLDS** — `Metrics: 0, // disabled` |
| 6 | Tracing double-gated under the OTLP branch | **HOLDS** |
| 7 | GossipSub serializes only `.Data` | **HOLDS** — 2 sites; the propagation finding stands |
| 8 | `peer_id` on the heartbeat histogram | **present, now LRU-bounded** (§2.3) |
| 9 | CI not on `v3base` | **RESOLVED** (§2.1) |
| 10 | Caller annotation off under the shipped encoder | **HOLDS** — still gated on `Development` |
| 11 | Node identity unprovisioned | **HOLDS** — `Alias: ""` |
| 12 | Dead `LoggingInterface` still present | **HOLDS** — survived the dead-code cleanup |

The dead-code sweep removed real dead code elsewhere but **did not touch `logging/`**. The dead
interface, the half-dead builder and the inert topic map are all still there — W2 and W4 are
undiminished.

### 3.1 One citation re-homed

`messaging/timeout_certificates.go` was **deleted** by PR #196 (*"take Period from the block; remove
timeout-certificate flow"*). We cited `:43-45` as proof that signatures cover canonical content
rather than the message envelope — the safety argument for putting trace context in
`PubSubMessages.Message` (§9.1).

**The proof survives on a stronger citation.** `AVC/BuddyNodes/MessagePassing/BLS_Signer/Signer.go:118-123`
— `CanonicalVoteMessageV3` builds *"the EXACT bytes signed and verified"* from
`(chainID, height, bindings, consensusHash, vote)`, all content fields. Zero sites sign
`messageGossip` or `GossipMessage`. `Message` still carries its `omitempty` BFT fields.

This is a better citation than the one it replaces: V3 is the primary vote path, where the deleted
file was the timeout path. **§9.1 needs the reference updated; the conclusion is unchanged.**

---

## 4. Counts

Same rule both sides, so deltas are sound. Absolutes use a looser test-exclusion than the frozen
figures and are not directly comparable to them.

| Metric | Frozen | Now | Δ |
|---|---:|---:|---:|
| `logger()` reads | 1,498 | 1,376 | **−122** |
| `logger().Tracer(` | 105 | 94 | −11 |
| `fmt.Print*` | 657 | 570 | **−87** |
| `context.Background()` in log calls | 459 | 433 | −26 |
| `RecordError(` | 296 | 287 | −9 |
| `ion.String("function"` | 867 | 824 | −43 |
| `ion.String("topic"` | 521 | 459 | −62 |
| ion-importing files | 135 | 119 | −16 |
| **zerolog fluent calls** | **224** | **239** | **+15** |
| **zerolog-importing files** | **34** | **36** | **+2** |

### 4.1 The zerolog number is worse than +15 looks

`messaging/timeout_request.go` — the 498-line file I flagged in the last status as new zerolog debt on
the consensus path — was **deleted** with the timeout-certificate flow. That removed roughly 25 calls.
The net is still **+15**, so something near **+40 new zerolog calls** landed elsewhere, in four new
files:

`internal/saferun/saferun.go` · `internal/sequencerid/sequencerid.go` ·
`messaging/committee_anchor.go` · `messaging/entropy_recovery.go`

**Every other dimension shrank; this one grew against a falling baseline.** CI now runs on `v3base`,
but `.golangci.yml` is unchanged — four linters, `tests: false`, no `depguard`. The surface exists;
the rule does not.

---

## 5. What this changes in the plan

| Item | Change |
|---|---|
| **Architecture** | None. Frozen, intact |
| **Effort 55–85 days** | None. Shrinkage is inside noise |
| **v3 gate step 1** (CI on `v3base`) | **DONE** |
| **v3 gate step 2** (`peer_id` before scrape) | **Satisfied differently** — bomb defused, ordering constraint lifted |
| **W1 sequencing** | Simpler: one dependency removed |
| **W3 metric convergence** | ~30% smaller — 12 of 15 dead metrics already dispositioned; 3 new schema-less metrics added to absorb |
| **W4 logging** | Marginally smaller, except zerolog |
| **§9.1 propagation** | Citation re-homed to `Signer.go:118-123`; conclusion unchanged |
| **`STEP-1-CI-GATE-PATCH.md`** | Patch 1 done. Patches 2, 3, 3b, 4 outstanding — and 3b still required: the quality-gate checkout at `ci.yml:28-29` is still a bare `actions/checkout@v4` with no `fetch-depth` |

---

## 6. Recommended next action

**Land Step-1 patches 2, 3, 3b and 4.** The argument is now stronger than when written:

- The precondition the team already met (CI on `v3base`) was the hard half.
- What remains is one new config file, one CI step, one `fetch-depth` line, two Makefile targets.
- It is the only intervention that addresses the one metric moving backwards.
- The acceptance test in that document needs re-pointing: `messaging/timeout_request.go` no longer exists, so use `internal/saferun/saferun.go` or `messaging/committee_anchor.go` as the regression target instead.

Everything else — the observability owner, W1, the spikes — is unchanged in both content and priority.

## 7. Risks

- `Assumed: ab4f5013 is the current tip. Fetched 2026-10-26 11:27 and confirmed by the user's terminal.`
- `Unverified: nothing was executed. No Go toolchain is available. All findings are grep/git over the two refs.`
- **The 606-file churn is large enough that a claim could have moved without the grep noticing it.** The twelve checks in §3 cover the load-bearing set; §8.2's full instrumentation schema and §2.5's `logging/` inventory were spot-checked, not exhaustively re-derived. If implementation hits a citation that does not resolve, amend per the freeze rule rather than treating it as a design failure.
- **The zerolog trend is the only compounding risk**, and it now has a live CI surface that could stop it.
