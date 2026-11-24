# Decision Record — opus ↔ sol telemetry review, rounds 1–10 + targeted round 6

**Date:** 2026-09-23 · **Companion to:** `JMDN-V3-TELEMETRY-ARCHITECTURE.md`
**Counterpart:** `../sol/TELEMETRY-ARCHITECTURE-V3.md` and its handoff files
**Purpose:** preserve what was contested, what evidence settled it, and what is still open — so the
architecture document can state only its current position.

Consolidated from `ITERATION-1-CROSS-REVIEW.md`, `ITERATION-2-CROSS-REVIEW.md`,
`ITERATION-3-CROSS-REVIEW.md` and `ITERATION-4-SCOPE-CHANGE.md`, which this file replaces.

---

## 1. Corrections to the original brief

Five claims in the task brief did not survive measurement. Three were load-bearing.

| # | Brief said | Measured | Impact |
|---|---|---|---|
| C4 | Six ion consumers: seedNodes, avc, ThebeDB, JMDN-FastSync, JMDN-Mempool, jmdn | seedNodes and JMDN-Mempool **do not import ion**; Mempool-Routing-Engine (29 files) and JMDT-Sequencer-Orchestrator (26) were missing | Rollout costed against the wrong repos; the two missed ones are the deepest integrations |
| C5 | ion metrics have no ergonomic layer and no proven use | `Ion.Meter()` is in production at MRE `app.go:215,268,376,407,429` with 14 instruments | "Can ion do metrics" was already answered in-fleet |
| C6 | `SetStatus` is effectively unavailable | `ion.StatusOK/StatusError` exist (`ion/tracer.go:20-27`) and Orchestrator uses them at `processor.go:568+` | jmdn's 0 calls are an enforcement failure, not a capability gap |
| C7 | `ion/fields` has 0 imports | 6 production files in `jmdt-simulator` import it | Schema-as-code sticks when adopted early, fails when retrofitted without a gate — that is the diagnosis |
| C8 | (not mentioned) | **CI does not run on `v3base`** (`ci.yml:3-7`) | Every proposed gate is dormant; this is now commit #1 |

Minor: `fmt.Sprintf` in log calls is **115**, not 145. `Enabled()` guards are **2**, not 0.
`grpc.NewServer` sites are **2** (+13 clients), not 8 — which made gRPC propagation one edit at a
shared factory rather than eight.

---

## 2. Contested items and how each was settled

### 2.1 ADR-2 — Prometheus scrape vs Collector → **sol conceded**

opus argued `otelprom.New(otelprom.WithRegisterer(metrics.DefaultRegistry))` registers *into* jmdn's
existing registry rather than creating a second one, so sol's collision objection becomes a
bootstrap error — the desired failure mode. opus also argued sol's plan made the Collector a hard
dependency for the 2–3 week consensus instrumentation payload.

**Settled:** one private registry, one `/metrics`, direct scrape for v3. Collector is v3.1 target
state, out-of-band, not a metrics dependency. Sol added the refinement opus accepted: optional
Prometheus support lives in `ion/prometheus` or `NewWithOptions`, never in `ion.Config`.

### 2.2 The gossip propagation carrier → **opus conceded; opus was wrong**

opus claimed `GossipMessage.Metadata` was an already-transmitted W3C carrier needing no wire change
("weeks → 2 days, no gate"). Sol refuted it.

**Evidence:** `Pubsub/Publish/Publish.go:132-142` branches and marshals only `messageGossip.Data` on
the production GossipSub path; `PublisherHelper.go:80` does so unconditionally; both receive paths
rebuild a fresh envelope with `TTL: 0` and no metadata. The envelope never reaches the wire.

**How the error happened, recorded because it is the failure mode both documents warn about:** opus
saw both `json.Marshal(messageGossip.Data)` and `json.Marshal(messageGossip)` in the first read of
that file and did not read the branch between them, then verified three *properties* of `Metadata` —
unsigned, not in the dedup key, in-repo precedent — while never verifying it was transmitted. Three
correct verifications of an irrelevant object.

**Settled:** trace context goes in `PubSubMessages.Message`. opus contributed the supporting
verification that nothing signs or hashes that struct (`bft_pubsub_adapter.go:164,187`;
`timeout_certificates.go:43-45`) and explicitly withdrew any precedent argument after
`git log -S"RoundID          string"` showed its optional fields arrived at initial release. Estimate
converged at **4–6 days**, moved to v3.1.

### 2.3 Metrics API failure semantics → **opus conceded**

opus designed a `Metrics.Err()` accumulator returning working no-ops for failed instruments. Sol:
that rebuilds 2026-09-17 inside the telemetry layer — a typo'd registration makes
`selection_candidate_set` silently a no-op and the halt alert absent, while the dashboard looks fine.
A deferred `Err()` is also the same forgettable moment as `counter, _ :=`.

**Settled:** `RegisterMetrics(scope, specs...) (*Registry, error)` — a `*Registry` is unobtainable
without handling the error; registration failures are boot-fatal. opus contributed the reconciling
invariant: *telemetry may refuse to start a node; it may never stop a running one.*

### 2.4 Label bounding → **opus conceded, then refined**

Sol: bounding keys does not bound values — `peer_id` was a declared key — so `Reason(string)` is
unsafe; every value set must be a declared enum with `other`. opus conceded.

opus then showed sol's points 3 and 4 collided: "drop the whole observation" on
`consensus.rounds` would mean a real round resolved with nothing recorded, recreating sol's own
empty-vector bug on the metric that matters most.

**Settled** on the four-case model both accepted: undeclared key impossible by construction;
out-of-enum value normalises to a mandatory `other` plus a violation counter; enum without `other`
rejected at registration; drop-the-observation reserved for a defect CI catches.

### 2.5 The empty-vector alert bug → **sol found a live bug in opus's document**

opus's halt alert was `rate(jmdn_consensus_rounds_total{outcome="success"}[5m]) == 0`. If the series
does not exist, `rate()` returns an empty vector, `== 0` filters empty to empty, and the alert never
fires — a total chain halt would produce silence from the one alert written to catch it. 2026-09-17's
failure mode (absence of signal read as health) reproduced in the alerting layer.

**Settled:** preinitialize bounded counter combinations; every threshold alert gets `absent()` and
`up` companions. opus generalised it further: every *ratio* alert needs a minimum-denominator guard,
since a proportion goes undefined exactly when throughput collapses.

### 2.6 Env-var overrides → **opus conceded; sol executed the probe**

opus escalated `JMDN_LOGGING_*` to a v3 blocker, inferring from three `loader.go` comments
(`:69-71,74-77,95-98`) that `AutomaticEnv` cannot reach nested keys. Sol ran a fresh-process probe
with a partial `jmdn.yaml` present; all seven logging values resolved.

**Settled:** executed evidence over inference. Blocker withdrawn. The finding inverts into a
documentation defect (three misleading comments) and possibly 20+ redundant binds — but sol was right
that deletion needs key-by-key tests (`THEBE_*` and the selection aliases are non-prefixed by design),
so it is a configuration-hygiene ticket, not telemetry scope. opus's residual ask — cover
file-present *and* file-absent — was accepted.

### 2.7 Other opus errors corrected by sol

| Claim | Correction |
|---|---|
| MRE handles every instrument error properly | **Mixed reference.** `monitor.New` is fail-fast; `hashlookup.NewResolver` (`resolver.go:96`) returns no error and warns on all seven failures. opus generalised from the better constructor. opus contributed back: the warn path leaves instruments holding whatever was returned alongside a non-nil error — latent, not live, since the SDK returns a non-nil no-op, but the interface does not guarantee it |
| Adding a map/slice to any ion config struct breaks ThebeDB | **2 of 5.** `ConsoleConfig` and `FileConfig` use `!=`; OTEL/Tracing/Metrics use `reflect.DeepEqual` |
| Security denials should use `ion.Critical` | **`Warn` + counter.** `Critical` is for invariant violation or termination; using it for routine remote-input rejection makes critical pages semantically false and hands an attacker a way to generate them |
| Delete the 15 dead metrics in Phase 0 | **Deprecate and inventory first.** "Defined but never observed" is a review finding, not a licence to delete a series someone may scrape |
| Flip the node to `info` | **Scoped component rollout** plus an **authenticated, audited** level endpoint — opus had the global flip while listing volume as a risk, and missed that an unauthenticated level control is a remote log-flood primitive |
| "A Collector in front of the chain" | Unfair phrasing for an out-of-band component. The real objections — ownership, deployment maturity, failure budget — sol accepted on their merits |
| v0.4.2/v0.5.0 source identity means the fleet risk "largely dissolves" | **Source compatibility ≠ deployment compatibility.** v0.5.0 raises the Go floor 1.25 → 1.26. Settled on two tracks: v0.6.0 on Go 1.26, v0.4.3 for Go 1.25 maintenance backports |

### 2.8 Instrument types → **sol conceded**

opus argued `consensus.votes` and `quorum.margin` as gauges lose every intermediate tally between
15 s scrapes against sub-second rounds — so the quorum-narrowing signal that precedes a liveness loss
would be invisible. **Settled:** votes → counter; quorum margin → histogram of `received − required`
recorded once per resolved round, with negative buckets.

### 2.9 Cardinality → **agreement on the total, disagreement on the allocation**

opus independently recomputed sol's schema and reproduced **1,544 series exactly** (`N+3` per
histogram label combination). That agreement surfaced a problem the total hides:

- **JSON-RPC: 960 series (62%). Consensus + selection: 187 (12%).** Five times more budget on operator question 8, off the consensus path, than on questions 1–3.
- **The 16-value `method` cap is refuted by the code:** jmdn exposes **29** JSON-RPC methods, so 14 collapse into `other` *by declaration order rather than traffic* — during an RPC abuse incident the offending method is as likely as not invisible.

**Proposed** (open, §4): drop `method` from `rpc.request.duration` (720 → 45, the same trade sol made
twice for `outcome` and `topic`), raise the cap to 32. Result **1,109 series**, consensus+selection at
17%.

### 2.10 Stage 0 → Stage 2 metric migration → **opus accepted with three conditions**

Sol proposed a repository-local `RegisterHaltMetrics(reg) (*HaltMetrics, error)` for Stage 0,
generalised into ion's registry later with no dual emission. opus accepted, and supplied the migration
failure modes sol asked for:

1. **`otelprom` attaches `otel_scope_name`/`otel_scope_version` and `target_info` by default.** Alerts keep matching, but a `Gather()` golden fails, `sum without(...)` rules shift, and `otel_scope_version` mints new series on every ion upgrade. Pin the option set in Stage 0 and generate the golden under it.
2. **Names must come from one shared `schema.PromName(canonical, unit, kind)` helper**, not hand-typed twice — double unit suffixes are a real failure class, and annotation units must produce no suffix.
3. **`Help` and `WithDescription` share one constant per metric.**

### 2.11 Scope — logging consolidation → **operator override, round 4**

Both documents had deferred consolidation to v3.x (sol round-4 item 10; opus §5.4 "migrate 72 sites,
not 738"). The operator then stated the v3 goal as **unify logging + metrics, pristine clean
structure**, which overrides it.

Re-measurement showed the deferral rested on a bad number. **1,306 conflated user-facing terminal
output with library-code violations:** of 568 `fmt.Print*`, **497** are CLI/startup output
(`CLI/CLI.go` 204, `main.go` 192) and only **71** are library violations. Real surface is **~809
calls**, concentrated — 57% of stdlib `log` in 8 files, 30% of zerolog in `main.go`.

opus also established **structure-before-volume**: the 1,680 ion call sites reach the logger through
78 accessors, so redefining them touches 78 files while every call site compiles unchanged.
Migrating first would land ~2,490 sites on the structure being eliminated and make cleanup 3× larger.

---

## 3. Adopted from sol

Taken wholesale into the architecture document, listed so provenance is traceable:

the **synthetic 29-peer recurrence test** as the release gate (the best single idea in either
document — it verifies the *incident is detected*, not merely that metrics exist) · the 14-value
error taxonomy with `Kind() telemetry.ErrorKind` · the sampling policy including *propagation is
always on, independent of recording* · explicit shutdown ordering with per-signal deadlines and
`errors.Join` · the `Uninitialized→Bootstrapped→ShuttingDown→Closed` state machine · acceptance
criteria as a concept · enum `other` normalisation with a self-counter · the nine-rule analyzer set
(opus had three) · *"one measurement has one owner; unmanaged dual emission prohibited"* · the
baggage allowlist · the quantified perf budget (<1% throughput, <2% allocation) · Collector
processors as second-line defence · dot-notation canonical names with documented Prometheus
translation · `MustRegisterMetrics` restricted to main/bootstrap · the named production owner
requirement ("a compose service is not an operating model") · deprecate-don't-delete for dead metrics
· scoped level rollout with an authenticated endpoint · the two-track ion release.

## 4. Adopted from opus

`v3base` CI activation as the first commit · MRE and Orchestrator as compatibility canaries ·
ThebeDB's embedded `ion.Config` YAML contract as a fleet constraint · linkage to the AVC audit
register (`consensus_entropy_source`, RC-2/D-33b observe rung) · the security `fmt.Printf` sites as
deliberate sink bypasses, to be migrated only after the structured path is proven · the direct-scrape
topology · dashboard/alert-to-metric reconciliation as a CI gate · the 140,000-series quantification
of the heartbeat histogram · counter/histogram instead of gauges for votes and quorum margin ·
threshold-dense reputation buckets · `absent()`/`up` companions and the ratio denominator guard ·
peer-label removal ordered *before* enabling scrape · the container-network metrics bind · the three
`RegisterHaltMetrics` migration conditions · the v0.4.2 fleet-pin finding and Go floor · the 497/71
`fmt.Print*` split and structure-before-volume.

---

## 5. Open at freeze

| # | Item | Owner | Blocking? |
|---|---|---|---|
| 1 | Drop `method` from `rpc.request.duration` (720 → 45 series) | sol §5.2 — one cell | **yes**, cheap |
| 2 | Raise the `method` cap 16 → 32, evidenced at 29 methods | sol §5.2 — one number | **yes**, cheap |
| 3 | Three `RegisterHaltMetrics` conditions (§2.10) into Stage 0 text | sol | **yes**, cheap |
| 4 | Independent check of the 497/71 `fmt.Print*` split before it drives the plan | sol | **yes** — it is the number that most changes consolidation cost |
| 5 | Does deprecate-before-delete apply to the `topic` **log field** (521 sites) as it does to metrics? | sol — was right about this risk class once already | no |
| 6 | Restrict `quorum.margin` to `{success, no_quorum, timeout}` (saves 30 series) | sol's call | no |
| 7 | `topic` enum cap of 16 — unverified; count `config/constants.go` as the 29 RPC methods were counted | implementer | no |
| 8 | `otelprom` scope-label behaviour — probe it | implementation | no, but the golden depends on it |
| 9 | Effort reconciliation: v3 = gate (8–12d + SRE) + structure (28–38d) = **36–50 engineer-days** | both | **yes** — confirm |
| 10 | Production owner for Prometheus/Grafana/trace storage | operator | **yes** — no owner named in either repo |

**No remaining architectural disputes.** Items 1–4 and 9–10 are numeric, procedural or
organisational. Round 5 can be a freeze audit.

---

# Part II — Fresh convergence cycle (rounds 5–10), 2026-09-23 → 25

Consolidated from `ITERATION-5..10`, which this file replaces. Frozen 2026-09-25.

## 6. Scope change and what it cost each side

The operator set the v3 goal as *"unify logging metrics and have pristine clean structure."* That
superseded the incident-only gate both reviews had converged on, and forced a re-measurement that
invalidated several planning numbers on both sides.

| Contested | Resolution | Who conceded |
|---|---|---|
| Is v3 structural convergence or full cockpit coverage? | **Structural.** Unification is about how measurements are owned and constructed; coverage is how many exist. The 41 legacy metrics migrate in v3 (that is what removes package-init `promauto`); new domain metrics are v3.1 | sol accepted opus's split |
| Directory-level output allowlist (497 CLI / 71 violations) | **Refuted.** `main.go` is two programs — `runCommand` at `:315-698` vs `main()` at `:829` — splitting 131 CLI / 61 service. `seednode` (43) and `seed/seedhelper` (20) are service diagnostics. A function-scoped manifest is required | opus |
| Delete 2,113 "overhead" fields | **Refuted.** `topic` is overloaded — 419 component identity, **102 real protocol topics**. `function` cannot be deleted at all: `zap.AddCaller()` is gated on `Development` (`logger_factory.go:156-158`), the shipped systemd encoder clears `CallerKey` (`:253-254`), and zap's caller is `file:line` not `Type.Method`. **725 deletable, 1,388 reclassified** | opus (with the encoder mechanism found by opus, the semantics by sol) |
| `telemetry.Log(component)` global accessor | **Rejected.** *"It proves a cheap migration, not a clean dependency boundary."* Replaced by `internal/telemetry.Runtime` + constructor injection | opus |
| Cost of that injection | **940 method / 505 free-function reads, 1,445 total** (AST, not regex — a regex missed 38 assigned/argument reads). The 505 free-function tail has no constructor to inject into | sol corrected opus's 939/484 |
| `ion.Logger` as the injected type | **Rejected on two counts.** Insufficient — 105 reads call `logger().Tracer(` and `Logger` has no `Tracer`. Overpowered — it exposes `Sync`/`Shutdown`/`SetLevel`/`GetLevel` (`ion/logger.go:49-62`), so any domain service could kill process-wide export | sol |
| …and the emission-only interface that replaced it | **Insufficient as specified.** An interface subset is structurally satisfied by `*ion.Ion`; ion documents that `With`/`Named` return a `Logger` whose *"concrete type is `*Ion`"*. Sealed with an unexported method; `testkit` supplies a parent-wrapped capture sink so the seal stays testable | opus found the hole, sol fixed the testability |
| Metric API with `Counter(name string)` | **Escape hatch.** No error channel, permits the name drift the schema exists to prevent. Replaced by binder-internal lookup returning errors and typed sets (`SelectionMetrics{…}`) | opus |
| Cardinality 1,109 | **1,086.** 1,109 minus 30 (quorum-margin outcome restriction) plus the 7-value security-denial counter neither document had counted | both |
| `WithoutTargetInfo()` | **Shared blind spot.** Both documents suppressed `target_info` for descriptor parity while separately putting identity on the OTel resource. Scope labels decorate every series (suppress); `target_info` is a separate family (do not). Retained, governed, separate golden, outside the 1,086 bound | **both — found by sol reading both docs against the exporter contract** |
| `Logger.Enabled(Level) bool` | **Breaking interface change.** 8 external test doubles implement `ion.Logger`; MRE aliases `ports.Logger = ion.Logger`. Replaced by optional `LevelEnabler` + concrete `(*Ion).Enabled`, **fail-open** on a failed assertion | sol; opus generalised to all 4 ion interfaces (`ion.Span` already has 1 external implementer) and added fail-open |

## 7. One process failure worth recording

In round 1 opus asserted *"no consumer implements `ion.Logger`"* and built the additive-only contract
on it. Opus's **own** round-1 fleet survey had listed exactly that question under `NOT VERIFIED`, with
the command to settle it. It was never run. Eight rounds of adversarial review then treated it as
settled, and it turned out to be the one thing that breaks the contract.

The failure was not the wrong inference — it was promoting a flagged-unverified item to a premise.
Same class as the defect the programme exists to fix: an absent signal read as a healthy one.

Related, and the reason the `target_info` error survived ten rounds: **the mistakes that survive
adversarial pressure are the ones both parties assume identically.** Neither document was wrong
relative to the other, so arguing could not surface it; only reading both against an external contract
did.

## 8. Freeze

Frozen 2026-09-25 at the five-round convergence baseline, both documents agreeing. Amendment rule:
falsifying executable evidence amends the affected section with the failing probe and its impact;
settled decisions do not reopen by preference.

**Sole outstanding blocker — organisational:** a named production owner for Prometheus, Grafana and
trace storage. Open since round 1, cannot be closed from source. Blocks production promotion, not
design.

**Mandatory implementation evidence (promotion gates, not design questions):** application descriptor
golden · `target_info` resource golden · relabel-aware two-target attribution proof · API-diff and
external-compile canaries (8 `Logger` + 1 `Span`) · function-scoped output manifest · downstream
parser inventory for `created_at`/`log_file` · external-consumer inventory for the 15 unobserved
metrics · `gETH/` and `FastsyncV2/` injection spikes.

Post-freeze status and the rebase against moved code: **`REBASE-PLAN.md`**.
