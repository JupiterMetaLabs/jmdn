# jmdn v3 telemetry — baseline evidence and handoff

**Status:** Targeted iteration 6 complete; freeze candidate pending Opus metric and identity amendments.

**Date:** 2026-09-25

**Normative document:** `TELEMETRY-ARCHITECTURE-V3.md`

This file preserves the evidence and adversarial-review history from the requested five-round cycle and the later targeted audit. It is not a second architecture specification. If this ledger and the normative document differ, the normative document controls until a later iteration changes it explicitly.

## 1. Operator intent and interpretation

The operator's v3 goal is to unify logging and metrics and leave a pristine jmdn structure.

The baseline interprets that as:

- one explicit process composition root for configuration, construction, resource identity, health, lifecycle, and shutdown;
- one governed schema with signal-specific log fields, span attributes, and bounded metric labels;
- all live jmdn application metrics behind the validated registry before v3 GA;
- all production service logging on injected ion loggers before v3 GA;
- the v3 cockpit proves chain progress, consensus agreement, and approaching liveness loss; new divergence, sync, mempool, richer p2p, RPC, and EVM coverage completes the eight-question cockpit in v3.1 without changing the structure;
- no lazy/package-global telemetry root, package-init application metric, temporary adapter, or duplicate measurement owner in the release artifact;
- Prometheus remains the operator query model; unification does not mean forcing logs and metrics through one value type or exporter.

The previous incident-only v3 gate is superseded. It remains the first shippable increment, not the definition of v3 completion.

## 2. Independently verified facts

### Configuration and lifecycle

- `settings.Load` applies defaults, YAML, `JMDN_*` environment values, and explicit CLI overrides. A fresh-process probe with partial YAML verified nested logging level, OTLP enabled/endpoint/credentials, tracing enabled, and sampler overrides under the pinned Viper version. Existing source comments claiming `AutomaticEnv` never reaches nested keys are too broad; key-specific regression tests remain required.
- `logging.NewAsyncLogger` and ion setup each use one-shot initialization. A call before validated settings can permanently pin default warn/console-only behavior.
- jmdn settings do not currently expose ion metrics configuration, and tracing is nested under the OTLP logging branch.
- ion warnings returned at construction are discarded by the current caller.

### Fleet and ion compatibility

- Checked main/release lines consume ion v0.3.5 or v0.4.2; active v3 worktrees use v0.5.0.
- `v0.4.2..v0.5.0` changes no Go source, so the API surface is effectively identical. The material boundary for v0.6 is the Go 1.26 toolchain floor, not a v0.4-to-v0.5 API break.
- Existing consumers pass raw `metric.Meter` values and embed `ion.Config`; `Ion.Meter` must remain source-compatible. The new registry is additive.

### Metrics

- jmdn declares 41 Prometheus application metrics at package initialization through `promauto` across the metrics package.
- The current private registry and `/metrics` handler exist, but supported defaults set the metrics port to zero.
- Five current metrics carry unbounded attacker-influenced `peer_id`; enabling scraping before removing those labels would activate a cardinality hazard.
- Fifteen declarations are statically unobserved, but that alone is not deletion authority. External queries and conditional call paths require inventory.
- The target table subtotal is 1,079 series per process after removing `method` from RPC duration, retaining a governed 32-value method enum on the request counter, and limiting quorum-margin outcomes to success, timeout, and no-quorum. Iteration 2 found that both documents omitted the separately required seven-value `security.denials{reason}` counter from that arithmetic; the complete application bound is therefore **1,086**.
- The OTel Prometheus exporter must explicitly pin scope-info, target-info, and translation behavior. Descriptor compatibility is decided by a `Gather()` golden against the pinned exporter version, not by assumed naming rules.
- The exporter does not copy resource attributes onto application metric points by default; it exposes them through the separate `target_info` family. `WithoutTargetInfo()` therefore removes the only in-band PromQL representation of the resource identity the architecture requires. Because `target_info` is a separate infrastructure family, retaining it does not alter application descriptors or the 1,086 application-series bound.

### Incident path

- Both selection paths discard `usedFallback`, and consensus already has the round result beside its health update.
- A synthetic 29-peer set with weights 0.30–0.43 is the regression that proves the 2026-09-17 failure is detectable. It must drive candidate set to zero or engage fallback, increment the fail-safe signal, and page inside two minutes.
- A zero-rate alert is insufficient alone. Bounded counter combinations are preinitialized and progress alerts have target-missing and critical-series-absent companions.

### Tracing and propagation

- `RecordError` is widely used while native span error status is absent in production.
- Outer `GossipMessage.Metadata` is not a reliable carrier on the active GossipSub path: the serialized `PubSubMessages.Message` is the compatible additive carrier.
- Cross-process propagation is not required to complete the operator's logging/metrics structural goal and remains v3.1. Local context correctness and log correlation are still v3 quality requirements.

### Logging structure and migration surface

- The dead `LoggingInterface`, half-dead builder, one-shot root, and topic map do not provide a sound final boundary.
- Current-tree counts differ depending on the accessor definition: 36 exact zero-argument `func logger() *ion.Ion` functions, 40 production functions named exactly `logger` when four parameterized variants are included, 52 production functions with `logger` anywhere in the name, and 62 production `NewAsyncLogger()` occurrences across 58 files. Any future estimate must name its counting rule.
- The original 568 count covers production `fmt.Print`, `fmt.Printf`, and `fmt.Println`. There are another 43 production `fmt.Fprint*` calls with mixed semantics such as stderr fallback, buffer rendering, and hash serialization.
- A file-wide 497-site CLI/startup allowlist is not defensible. In `main.go`, 131 print-family sites are in the CLI-command path below line 700, while 61 are in the node service path; `seednode/seednode.go` has 43 and `seed/seedhelper.go` has 20 long-running service diagnostics. The migration needs a line/function manifest, not directory guesses.
- The 521 `topic` fields are overloaded, not uniformly redundant. At least 102 carry a real or variable protocol topic (`topic`, `topicName`, `topicToSubscribe`, `msg.Topic`, or the governed consensus channel). Component-like values become `component`; real protocol topics become `p2p.topic`.
- The four high-volume keys occur exactly as measured in production ion calls: `function` 867, `topic` 521, `created_at` 380, and `log_file` 345. `created_at` and `log_file` are redundant. `function` requires semantic migration to governed `operation`: shipped production has `development: false`, ion enables `zap.AddCaller()` only in development, and the default systemd encoder removes caller output. Even when enabled, `file:line` is not the same concept as the existing logical `Type.Method` values.
- A Go-AST pass over production function bodies at `efa4501` finds 1,445 exact zero-argument `logger()` reads: 940 in receiver methods across 37 files and 505 in free functions across 42 files. The source shapes are 1,408 direct selector receivers, 34 assignments, and 3 direct call arguments. The competing 939/484 count came from a direct-selector regex that could not see any assignment or call-argument shape; the AST inventory is authoritative.
- Of those reads, 105 across 20 files derive `logger().Tracer(...)`: 72 in methods and 33 in free functions. A fresh source count resolves Sol's earlier 104 result in Opus's favour. These require separate tracer injection; `ion.Logger` does not expose `Tracer`.
- `ion.Logger` is not a narrow domain dependency: it includes `Sync`, `Shutdown`, `SetLevel`, and `GetLevel` in addition to emission and child-logger methods. The root/runtime keeps that interface; domains receive an emission-only structural interface plus a separate tracer and typed metric set.
- An unsealed emission-only interface is not a capability boundary when its dynamic value is `*ion.Ion`: domain code can type-assert the root back and recover lifecycle, tracer, and meter methods. The final interface is sealed and backed by a jmdn-owned wrapper. A parent-owned capture-sink adapter gives domain tests a mock seam without allowing external implementations of the sealed production interface.
- Adding `Enabled` to exported `ion.Logger` would violate the agreed additive-only v0.6 contract. Five MRE and three Sequencer Orchestrator test doubles implement the current interface. The compatible shape is a new `ion.Level` type, a new optional `ion.LevelEnabler` interface, and a concrete `(*Ion).Enabled` method; `ion.Logger` remains unchanged.
- The same MRE `hashlookup/testdoubles_test.go` file contains `nopSpan` and `nopTracer`. Its `Deps.Tracer ion.Tracer` receives `nopTracer{}` at line 330, so Opus iteration 10's count of zero external `Tracer` implementations is refuted by its own cited file. The checked external canary covers eight `Logger`, one `Span`, and at least one `Tracer` implementation.
- Shipped defaults cannot identify fleet members: `node.alias` is empty in both Go defaults and `jmdn_default.yaml`, `logging.service_name` is identical across default nodes, and the exchange profile's `<YOUR_NODE_ALIAS>` value is an unenforced placeholder. Fleet/production bootstrap must therefore reject missing or placeholder node identity before workers.
- The only existing Prometheus rule warns that proxy/relabel processing can invalidate its `on(instance)` join. The v3 resource join must pin unique final `job`/`instance` keys, exercise the production relabel path, and alert when a resource-aware join loses its input.
- The Go OTel Prometheus exporter is an unchecked collector: its `Describe` yields nothing, and its translated-name/type/HELP collision handling runs during `Collect`. `WithRegisterer` succeeding cannot prove that two OTel specs, or an OTel and native collector, will not collide on a later scrape. The v3 bootstrap needs a translated-schema preflight and materialized isolated `Gather()` probe.
- Current jmdn registers libp2p collectors during `node.NewNode`, by passing `metrics.GetLibp2pRegisterer()` to `libp2p.New`; that registerer adds `libp2p_`. A bootstrap probe cannot gather actual libp2p families before the host exists. The v3 boundary must reserve the prefix and validate host-time registration separately, with a real-host combined-registry integration test.
- The pinned libp2p TCP transport also contains an optional metrics path that registers into Prometheus's global default registerer on first connection. Current jmdn passes `tcp.NewTCPTransport` without `tcp.WithMetrics()`, so this path is dormant. v3 should keep it disabled until a private-registry integration exists; otherwise it would create an unowned side path outside the one served registry.
- `gETH/Facade/Service.convertEthTxToConfigTx` contains 18 debug calls, not 7. Deleting pure-conversion diagnostics remains the right likely disposition, but manual examples are not reliable costing evidence.

## 3. Settled architectural decisions

1. Keep ion as a thin additive facade; do not replace it or break `Ion.Meter`.
2. jmdn owns domain schema; ion owns generic SDK/export/lifecycle mechanics.
3. One jmdn telemetry runtime owns logging, application metrics, the private registry, native collector adapters, health, flush, and shutdown.
4. Domain packages receive narrow dependencies. A renamed global service locator is rejected.
5. Logs, spans, and metrics share concept names but retain different types and cardinality budgets. No automatic field-to-label conversion.
6. Prometheus direct scrape remains the v3 operator path. A Collector is optional v3.1+ infrastructure and requires an owner/SLO.
7. All live application metrics converge before v3 GA. Safe emitted Prometheus names are preserved where possible; implementation cleanliness does not justify query churn.
8. All production service logging converges before v3 GA. CLI/report output and serialization remain output, governed by narrow allowlists.
9. Deterministic schema/construction defects fail before workers; backend loss after bootstrap degrades visibly and never changes consensus behavior.
10. The reference halt detector ships first, but its local adapter is removed by the final registry migration.
11. The compiled log level remains `warn`; authenticated/local runtime elevation is permitted and audited.
12. Metric and log changes that affect external consumers require query/parser inventory and explicit migration evidence.
13. The domain logger is a sealed jmdn wrapper, not a structural view of `*ion.Ion`; test capture sinks pass through the same wrapper.
14. The final OTel reader retains exactly one governed `target_info` resource series. Scope labels remain suppressed on application metrics, and resource-aware PromQL uses an explicit `job`/`instance` join.
15. Fleet/production posture fails bootstrap on empty, placeholder, malformed, or over-length node identity; uniqueness is configuration-managed and collision-alerted because one node cannot prove a fleet-wide invariant locally.
16. The `job`/`instance` resource join is a deployment contract over final post-relabel labels. Release tests apply the production relabel path and require missing-join plus rule-evaluation-failure detection.
17. All four exported ion interface method sets are frozen through v0.x. New capabilities use new interfaces or concrete methods; existing public interfaces do not grow methods in an “additive” release. If an injected sink lacks optional `LevelEnabler`, guarded logs fail open and still emit.
18. Application and fixed-native metric families are checked after Prometheus translation before workers; the OTel exporter's unchecked registration is never treated as collision proof. An isolated probe exercises dormant OTel instruments without contaminating live counters or histograms. The later libp2p host path uses a reserved prefix and a validating registerer, with a real-host combined-registry test.

## 4. Compressed review history

| Round | Durable result |
|---|---|
| Initial designs | Both identified ion as the foundation, direct Prometheus scrape as the immediate operator path, schema enforcement, bounded labels, and the reference-incident metrics. |
| Cross-review 1 | Environment-variable behavior was reprobed; outer gossip metadata was rejected as the carrier; vote/quorum gauges were corrected to event-preserving counters/histograms. |
| Cross-review 2 | Mandatory metric enum `other`, fail-fast boot registration, v3/v3.1 separation, and the synthetic 29-peer recurrence became shared requirements. |
| Cross-review 3 | Prometheus exporter options/name/help compatibility were pinned; RPC cardinality was cut; method cap became 32; the then-calculated table subtotal became 1,079. |
| Scope correction | The operator promoted logging and metrics unification plus clean structure into v3. The prior incident-only gate and v3.x logging deferral were superseded. |
| Baseline consolidation | The scope correction was carried through the architecture: all application metrics and production service logging now converge before v3 GA; global accessors and permanent adapters are explicitly rejected. |
| Fresh cycle iteration 2 | Structural convergence is the v3 boundary; new domain coverage and full cross-process tracing are v3.1. The exact accessor-definition count, `function` semantics, 1,086 complete series bound, and additive programme estimate were corrected. |
| Fresh cycle iteration 3 | The accessor-use tail was measured by AST (1,445); logger and tracer injection were separated; full `ion.Logger` was rejected as a domain capability; metric bindings became typed rather than string/raw-label based. |
| Fresh cycle iteration 4 | Opus's sealed-logger escape-hatch finding was accepted and completed with a test seam; the tracer count was corrected to 105; both designs' `WithoutTargetInfo` choice was rejected because it erased fleet resource identity from PromQL. |
| Fresh cycle iteration 5 | Opus iteration 9 fully adopted Sol iteration 4 and added valid identity-provisioning and relabel-aware join requirements. Sol accepted and tightened both, then found a Go compatibility break: adding `Enabled` to `ion.Logger` would break eight fleet test doubles. The capability moved to a new optional interface and concrete `*Ion` method. |
| Targeted iteration 6 | Opus iteration 10 accepted the interface correction and added fail-open level checks plus a wider four-interface API gate; Sol accepted both and corrected its `Tracer` implementer count. Tagged exporter source refuted both documents' implied registration-time collision guarantee. Sol added translated-family preflight, an isolated materialized probe, and a separate libp2p host gate; Opus exporter reconciliation is requested. |

## 5. Questions for the fresh review cycle

These were the baseline review targets. Iteration 1 below begins the operator-authorized fresh cycle.

1. Does the operator mean structural convergence only, or also completion of every new metric needed for the eight-question cockpit? The baseline commits to both because the document already treats those questions as goals.
2. Can every live legacy metric preserve byte-compatible Prometheus descriptors through the pinned OTel exporter? Produce the executable `Gather()` diff before finalizing query-migration cost.
3. What exact line/function output allowlist survives? Build a checked-in manifest and independently review `main.go`, `seed/`, `seednode/`, and metrics report writers.
4. Which downstream log queries parse `topic`, `function`, `created_at`, or `log_file`? Split semantics before deleting keys.
5. Which of the 15 statically unobserved metrics have external consumers? Resolve before the v3 RC compatibility window starts.
6. Is the 49–75 engineer-day estimate defensible after constructor-injection spikes in the largest packages? Re-estimate from two representative domains rather than raw call counts.
7. Does the final target need an exported `jmdn/telemetry` package, or can it remain `internal/telemetry`? The baseline prefers `internal` to prevent accidental fleet coupling.

## 6. Workspace boundary

- Sol-owned work is confined to `docs/telemetry/sol/`.
- Opus files under `docs/telemetry/opus/` are read-only from Sol's side.
- Future suggestions to Opus must be written in the Sol folder, never applied to Opus files.

## 7. Fresh convergence cycle — iteration 1 response to the Opus baseline

**Reviewed:** `../opus/JMDN-V3-TELEMETRY-ARCHITECTURE.md` (919 lines) and `../opus/DECISION-RECORD.md` (218 lines), both timestamped 2026-09-23.

**Historical note:** this section records the iteration-1 position. Section 8 supersedes its scope decision, accessor count, and effort estimate after Opus's updated baseline and additional verification.

**Disposition:** not freezeable yet. Opus preserved the strongest prior decisions, but its normative document applies the operator's scope change to logging while leaving the application-metrics architecture split. It also substitutes one new global logger accessor for the old accessors and treats two overloaded log fields as uniformly redundant. These are architecture disputes, not editorial cleanup.

### 7.1 Accepted from Opus

1. **The incident slice ships first.** The 29-peer detector, peer-label removal, scrape deployment, absence companions, and bootstrap safety complete before broad restructuring. Early value and final cleanliness are separate gates.
2. **Metrics should be reachable in shipped defaults.** Sol previously kept the compiled port at zero and enabled it only in deployment profiles. Opus's position is better when combined with its safety constraints: default port 9101 on loopback, container override to a private service interface, no host publication, private mux, and explicit port-zero opt-out. The normative Sol document now adopts this.
3. **Test files must be linted.** CI-on-`v3base` plus test-file analysis is now explicit in Sol's enforcement section.
4. **Structure precedes volume.** The clean ownership/configuration/schema boundary must exist before migrated call sites target it. Sol agrees with the sequence, but not with Opus's proposed global access mechanism.
5. **Security cutover order remains correct:** prove sink and level behavior, dual-emit structured `Warn` plus counter alongside stdout during canary, then remove stdout before v3 GA.

### 7.2 Blocker 1 — Opus does not actually unify application metrics in v3

The Opus decision says v3 unifies logging and metrics, but the normative details say all of the following:

- the topology labels `promauto` plus the 41 metrics as **"existing, unchanged in v3"** (§5 diagram);
- the halt adapter is replaced by the OTel-backed equivalent only in **v3.1** (§11);
- migrating the 41 metrics is explicitly **not in v3** (§11);
- sync, mempool, EVM, and RPC metrics are explicitly **v3.1** (§11);
- acceptance nevertheless requires all eight operator questions to be answerable in v3 (§12.2).

Those statements cannot all be true. A schema package beside a package-init `promauto` application path is shared documentation, not unified metric ownership. The acceptance criterion is also unimplementable while the metrics needed for four of its eight questions are deferred.

**Sol decision:** v3 dispositions all 41 historical declarations, migrates every live application measurement through the validated registry, removes package-init application `promauto`, and implements the metrics required for the eight-question cockpit. Safe Prometheus names, HELP text, types, labels, and buckets are preserved where possible, so this does **not** imply breaking PromQL. Query compatibility is a data contract; it is not a reason to preserve the old construction architecture.

Native Go/process/libp2p collectors may remain Prometheus-native behind telemetry-owned adapters. That exception is explicit and does not grant domain packages a second metric API.

### 7.3 Blocker 2 — `telemetry.Log(component)` is a renamed service locator

Opus proposes:

```go
Log(c schema.Component) ion.Logger
```

and treats the fact that 1,680 existing ion calls can continue through redefined accessors as proof of a clean structure. It proves a cheap migration, not a clean dependency boundary. Domain behavior still depends on hidden process-global state; parallel tests still share it; package initialization remains capable of calling it; and constructors do not reveal their logging dependency.

The state machine can turn an early call from silent misconfiguration into a loud failure, which is an improvement, but a loud global is still global. The proposed exported top-level `jmdn/telemetry` package also invites other repositories to couple to a jmdn-specific composition layer.

**Sol decision:** use `internal/telemetry.Runtime`, construct it once in `main`, derive component loggers and typed metric sets during composition, and inject narrow dependencies into services. A temporary forwarding accessor may reduce intermediate diff size, but the clean-room gate deletes it. `internal/` keeps jmdn composition policy from becoming an accidental fleet API.

The 78-accessor estimate is not reproducible on the current tree. The explicit counting rules yield:

| Definition | Current count |
|---|---:|
| exact zero-argument production `func logger() *ion.Ion` | 36 |
| all production functions named exactly `logger` (including four parameterized variants) | 40 |
| production functions with `logger` in the name | 52 |
| production `NewAsyncLogger()` occurrences | 62 |
| production files containing `NewAsyncLogger()` | 58 |

Future costing must state which unit it counts. This numeric correction does not weaken the structural objection.

### 7.4 Blocker 3 — the 497/71 output split is refuted as an allowlist

The raw count is reproducible: 568 production `fmt.Print`/`Printf`/`Println` calls. The proposed classification is not.

| Evidence | Count | Consequence |
|---|---:|---|
| `main.go` print-family calls | 192 | not one semantic category |
| `main.go` CLI command path below line 700 | 131 | plausible output allowlist |
| `main.go` node/service path at or above line 700 | 61 | operational diagnostics; review/migrate individually |
| `seednode/seednode.go` | 43 | long-running service/library diagnostics, not a CLI |
| `seed/seedhelper.go` | 20 | long-running service/library diagnostics, not a CLI |
| additional production `fmt.Fprint*` calls | 43 | mixed stderr, report rendering, and serialization; classify by semantics |

Even before reviewing `main.go`, reclassifying `seed/` and `seednode/` raises the migration surface from 809 to **at least 872** calls. If the 61 service-path calls in `main.go` migrate, it becomes **933** before the separate `Fprint*` review. Some startup/final-shutdown stderr will remain allowlisted, so 933 is not asserted as the final number.

**Sol decision:** the allowlist is function-scoped with a reason. Directories and whole files are never the unit of exemption. `CLI/CLI.go`, report renderers, examples, and serialization writers remain output only where their specific function semantics prove it.

### 7.5 Blocker 4 — `topic` and `function` cannot be deleted mechanically

Opus labels all 521 `topic` fields inert and all 867 `function` fields caller duplication. The first claim is directly false in the current tree:

- all 521 `ion.String("topic", ...)` sites are reproducible;
- at least **102** carry an obviously variable or real protocol topic: `topic` (34), `topicName` (9), `topicToSubscribe` (3), `msg.Topic` (3), or `config.PubSub_ConsensusChannel` (53);
- additional fixed values such as `BLOCKTOPIC` may also be protocol semantics rather than logger routing.

**Sol decision:** split the overloaded key. Logger-routing constants become bounded `component`; actual protocol channel/topic becomes governed `p2p.topic`. For `function`, zap caller metadata replaces only source-location duplication; stable business-operation identity becomes governed `operation`. `created_at` and `log_file` are redundant, subject to downstream-parser inventory.

Deleting the 47 inert logger topic constants remains correct. Deleting all 521 `topic` observations does not.

### 7.6 Blocker 5 — Opus's cardinality result is internally stale

Opus's schema already restricts quorum-margin outcomes to `{success, no_quorum, timeout}`, saving 30 series relative to the five-outcome version, and correctly removes `method` from RPC duration while raising the governed method cap to 32. Its stated result remains **1,109**, which includes the 30 series it has already removed.

**Iteration-1 table subtotal (superseded): 1,079 application series per process.** Section 8.3 adds the omitted seven-series security-denial counter and corrects the complete bound to 1,086. Native SDK/runtime/third-party collector series remain budgeted separately.

### 7.7 Blocker 6 — effort omits the work its own acceptance criteria require

Opus estimates 36–50 engineer-days by costing the incident gate plus logging structure/migration, while explicitly excluding application metric convergence, full domain metrics, and constructor injection. Those exclusions are the work needed to satisfy the operator goal and Opus's own eight-question acceptance criterion.

**Superseded iteration-1 planning baseline:** **49–75 engineer-days plus 6–10 SRE days**, approximately 9–13 elapsed weeks with two engineers and an SRE partner. Section 8.3 withdraws this range because it did not equal the underlying stages and replaces it with additive workstreams. Two representative constructor-injection spikes and the 41-metric disposition inventory are still required before commitment.

### 7.8 Required Opus changes for iteration 2

1. Choose one coherent v3 meaning: either retract “unified metrics/pristine structure” or move all live application metrics, the final OTel-backed registry, and the eight-question domain set into v3. Sol recommends the latter because it follows the operator's explicit goal.
2. Replace the global `telemetry.Log(component)` end state with constructor injection, or defend why hidden global state qualifies as pristine under parallel tests and explicit-dependency review.
3. Replace the 497/71 allowlist claim with a function-level manifest; at minimum reclassify `seed/` and `seednode/` and separate the two halves of `main.go`.
4. Split logger component from real protocol topic; do not delete all 521 `topic` fields. Classify semantic `function` values before replacing them.
5. Correct the then-known table subtotal to 1,079 and reconcile the effort estimate with the actual v3 acceptance criteria. Section 8 subsequently finds the omitted security counter and raises the complete bound to 1,086.

### 7.9 Iteration-1 convergence state

Accepted unchanged: ion as an additive thin facade; signal-specific types; mandatory metric `other`; fail-fast schema registration and runtime exporter degradation; one private registry and direct Prometheus scrape; no unmanaged dual emission; incident regression first; bounded labels; alert absence/denominator guards; error taxonomy; shutdown state machine; hot-path budgets; Collector and cross-process propagation deferred to v3.1.

New convergence this round: Sol adopts metrics reachable in safe shipped defaults and test-file linting.

Still contested: the meaning of v3 metric unification, global accessor versus dependency injection, output allowlist scope, field deletion semantics, and the resulting effort. Freeze is premature.

## 8. Fresh convergence cycle — iteration 2 response to the updated Opus baseline

**Reviewed:** the current `../opus/JMDN-V3-TELEMETRY-ARCHITECTURE.md` (1,018 lines), `../opus/ITERATION-5-REVIEW.md` (246 lines), `../opus/ITERATION-6-REVIEW.md` (185 lines), and the existing decision record on 2026-09-23. Opus updated its normative document during this review; this response targets the updated version, not the earlier 977-line snapshot.

**Disposition:** strong convergence, but not freezeable yet. The central architecture dispute is resolved: Opus now adopts an internal runtime, constructor injection, migration of all live historical application metrics in v3, deletion of package-init application `promauto`, and removal of the global logger accessor. Sol accepts Opus's structural-v3/coverage-v3.1 split. What remains is primarily internal consistency in the Opus normative document and measured costing, not a competing architecture.

### 8.1 Accepted from Opus

1. **Structural convergence is the v3 boundary.** The operator asked for unified logging and metrics with a pristine structure, not every possible new domain signal. v3 therefore freezes the composition root, schema, registry, lifecycle, injection boundary, enforcement, and ownership model. v3.1 adds coverage without creating a second path.
2. **All historical application metrics still migrate in v3.** Every one of the 41 declarations receives a disposition; every live measurement moves behind the validated registry; application `promauto` and the temporary halt adapter are absent at v3 GA. Structural scope is not permission to leave the old metric architecture beside the new one.
3. **The global logger accessor is withdrawn.** Opus now reaches the same end state as Sol: one `internal/telemetry.Runtime` built in `main`, with component loggers and typed metric sets injected through constructors or explicit entry-point parameters.
4. **`function` becomes `operation`; it is not deleted as caller duplication.** Opus's verification is decisive: caller annotation is off under `development: false`, the shipped systemd encoder removes caller output, and `file:line` would not preserve logical `Type.Method` identity anyway. Only `created_at` plus `log_file`—725 observations—are safely redundant before downstream-consumer checks. The 867 `function` and 521 `topic` observations require semantic migration.
5. **The accessor correction is accepted.** The exact zero-argument accessor count is 36, not Sol's earlier 40. The latter regex included four functions named `logger` that accept a parameter. Planning should use 62 `NewAsyncLogger()` sites across 58 files, then measure the 939 method and 484 free-function tail.
6. **The output and field inventories have converged.** Directory allowlists are rejected; `main.go`, `seed/`, and `seednode/` need function-level classification; 43 `fmt.Fprint*` calls are semantically mixed; `topic` splits into 419 component-like and 102 protocol-topic observations.
7. **The compiled default remains `warn`.** Scoped, authenticated, audited elevation is safer than globally lighting up the 964 currently suppressed sites.

### 8.2 Resolved scope boundary

No operator arbitration is now necessary unless the operator wants to accelerate v3.1 coverage into the v3 date. The shared boundary is:

| Release | Required outcome |
|---|---|
| **v3** | One internal runtime; constructor-injected logging and typed metrics; all 41 historical metrics dispositioned and every live one migrated; no application `promauto`, old `logging/`, global accessor, or temporary adapter; chain/consensus/selection/security/panic/telemetry-health signals; timed cockpit answers for questions 1–3; incident recurrence proof. |
| **v3.1 coverage** | New divergence, sync, mempool, richer p2p, RPC, and EVM instruments; questions 4–8 and their fault-injection/runbook evidence; full target series golden. |
| **v3.1 tracing** | Cross-process HTTP/gRPC/p2p propagation, concrete backend ownership, native error status/correlation completion, and the two-node trace proof. |

Sol does **not** include question 7 in the v3 timed gate merely because old p2p metrics survive migration. Both documents show that those metrics read healthy throughout the reference halt and do not constitute complete peer/protocol-health coverage. Either add a precise v3 peer-health signal set or keep the v3 gate to questions 1–3; Sol chooses the latter.

The complete target schema remains documented now so v3 does not make later coverage impossible. Its corrected 1,086-series calculation is the v3.1 full-target bound; v3 CI generates and freezes the exact smaller subset rather than pretending unimplemented rows exist.

### 8.3 Sol corrections in this round

1. **Accessor count:** 36 exact zero-argument accessors, 40 functions named exactly `logger` when parameterized variants are included, 52 functions with `logger` in the name, and 62 construction sites across 58 files.
2. **Field semantics:** the earlier conditional wording about deleting `function` was too weak. Under shipped configuration it must migrate to governed `operation` unless a site-specific review proves it is meaningless; caller metadata is not a replacement.
3. **Programme arithmetic:** Sol's prior 49–75 engineer-day headline did not equal its own stage ranges and is withdrawn. The non-overlapping v3 workstreams now sum to **55–82 engineer-days plus 6–10 SRE days**: incident 8–12, unified kernel/ion minimum 10–15, historical metric convergence 6–10, logging structure and call sites 27–38, and clean-room release 4–7. v3.1 coverage adds 10–15; trace completion adds 4–6 plus backend operations. These are planning ranges pending the `gETH/` and `FastsyncV2/` injection spikes and the 41-metric inventory.
4. **Series bound:** 1,079 is the subtotal of the 33 rows previously listed. The v3 security design also mandates `jmdn.security.denials{reason}` with seven governed values but neither document included that 34th row in the table arithmetic. The complete target bound is **1,086**; runtime/SDK self-metrics remain a separately budgeted family.

### 8.4 Remaining blockers in the current Opus normative document

1. **The halt adapter has two removal releases.** The topology and §11 item B2 remove it during v3 metric migration, but the gate's “Descriptor-gated migration” says v3.1 replaces the direct collector. The former is the agreed architecture: the temporary adapter must be gone at v3 GA.
2. **The logging migration story still contains the withdrawn shortcut.** §2.7 and §6.2a correctly say 62 construction sites across 58 files and describe constructor injection. §6.1, its diagram, and the risk section still say 78 accessors/files, 809 calls, unchanged call sites compiling through a redefined accessor, and “surgical” restructuring. That is the rejected global-accessor plan and contradicts the 939-method/484-free-function tail.
3. **The effort numbers do not add.** The top states 52–70 total and 44–58 for structure; §11 labels structure 28–38; the itemized A–H rows actually sum to 47–66, making 55–78 before an explicit clean-room release allowance. Choose non-overlapping workstreams and make the headline their arithmetic sum.
4. **The scope question is no longer open between the documents.** Sol now accepts structural v3 and coverage v3.1. Opus should remove the operator-arbitration text, its stale reference to Sol's 49–75 wider scope, and the claim that full coverage makes v3 roughly 50% larger unless it supplies the additive calculation.
5. **Acceptance mixes structural v3 with selective coverage.** Question 7 is required while richer p2p coverage is deferred and the gap analysis calls today's peer answer misleading. Either define the exact existing signals that make question 7 answerable or move it to v3.1. Criterion 9 also requires all 296 error-recording sites to gain native status in v3 although trace completion is otherwise v3.1 and the migration estimate contains no corresponding work item. Sol places the complete status sweep in v3.1; if Opus keeps it in v3, it must cost and sequence it.
6. **The v3.1 Collector row says Prometheus answers all eight questions while four question domains are separately deferred.** Prometheus is the correct query path, but a backend cannot answer questions whose instruments do not yet exist. Reword this as “Prometheus remains sufficient once the v3.1 domain instruments land.”
7. **The 1,079 cardinality bound omits the required security counter.** Add `jmdn.security.denials{reason}` to the schema and seven series to the arithmetic, yielding 1,086 before separately budgeted runtime/SDK self-metrics.

### 8.5 Required Opus changes for the next iteration

1. Scrub the normative document of the old 78-file/global-forwarder migration model and align every diagram, risk, and count with constructor injection.
2. Make temporary halt-adapter removal unambiguously a v3 gate.
3. Rebuild the effort section from additive, non-overlapping workstreams and include the final clean-room gate.
4. Close the now-resolved scope question; use questions 1–3 for v3 unless a concrete peer-health v3 specification is added.
5. Align trace-status acceptance with the chosen release and its estimate.
6. Add the security-denial counter to the target schema and correct the full-target bound to 1,086.

### 8.6 Iteration-2 convergence state

Resolved this round: structural-v3 versus coverage-v3.1; constructor injection versus a global logger accessor; exact zero-argument accessor count; `function` semantics; all historical application metrics and halt-adapter removal as v3 work; safe warn default; output-manifest method; corrected 1,086 full-target bound.

Still open: executable descriptor compatibility, downstream log-field consumers, the 15 unobserved-metric consumer inventory, the generated output manifest, two injection spikes, named production observability ownership, and reconciliation of Opus's internal plan/estimate contradictions. Freeze remains premature, but the two designs now describe substantially the same end state.

## 9. Fresh convergence cycle — iteration 3 response to Opus iterations 6 and 7

**Reviewed:** `../opus/ITERATION-6-REVIEW.md`, the subsequently published `../opus/ITERATION-7-REVIEW.md`, and the updated 1,052-line normative architecture, all in full at `efa4501`.

**Disposition:** the architectural scope dispute is closed, and Opus iteration 7 correctly resolves the seven consistency blockers from Sol iteration 2: v3 halt-adapter removal, structural-v3 scope, questions 1–3 only, 1,086 series, additive 59–85-day costing, the clean-room allowance, and a v3 `spanstatus` gate with the legacy sweep in v3.1. Sol adopts the last of those as a better debt-control boundary. The new work this round is making constructor injection type-correct, recounting its full surface, and closing the metric API's remaining escape hatch.

### 9.1 The 939/484 tail is directionally right but numerically incomplete

An independent `go/parser` walk counted every zero-argument identifier call named `logger` in non-test production Go files:

| Shape | Receiver methods | Free functions | Total |
|---|---:|---:|---:|
| Direct `logger().X` selector | 932 | 476 | 1,408 |
| Assigned root or direct call argument | 8 | 29 | 37 |
| **All exact reads** | **940 across 37 files** | **505 across 42 files** | **1,445** |

Opus's 939/484 and 32/36-file figures combine most direct selectors with only part of the other 37 reads. That hybrid rule drops 22 reads and 11 affected-file memberships. The delta is not large enough to change the architecture, but it is large enough to invalidate the count as an estimating baseline. The spike manifest must come from the AST and record the disposition of all 1,445 reads.

The concrete `gETH` example also undercounts: `convertEthTxToConfigTx` has 18 `logger().Debug` calls at lines 501–536, not 7. They should probably all be deleted, which strengthens Opus's qualitative recommendation while weakening its manual quantity.

### 9.2 Injecting `ion.Logger` is both insufficient and overpowered

Opus's method strategy says to add an `ion.Logger` field and replace `logger()` with that field. Two source facts prevent this from being the final rule:

1. **Insufficient:** 105 accessor reads across 20 files call `logger().Tracer(...)`—72 in methods and 33 in free functions. Iteration 4 corrects the earlier 104 count. `ion.Logger` has no `Tracer` method, so those sites require an independently scoped `ion.Tracer` or deletion.
2. **Overpowered:** `ion.Logger` exposes `Sync`, `Shutdown`, `SetLevel`, and `GetLevel` (`ion/logger.go:49-62`). Ordinary domain services must not be able to flush or shut down the process root or mutate fleet log level.

**Sol decision:** `Runtime` retains the root/full logger. Domain constructors receive an emission-only `EventLogger`, a separate `ion.Tracer` only where tracing is used, and a domain-specific typed metric set. Components are named during composition, so domains do not need `Named`; lifecycle and level controls never cross the boundary. A `telemetrydeps` analyzer rejects `*ion.Ion` and full `ion.Logger` in domain fields/parameters.

### 9.3 The proposed metric API still has a type-safety escape hatch

Both documents claimed undeclared labels would become compile-time failures while showing domain code that performed string instrument lookup and constructed raw `ion.Label` values. Those statements cannot both be true.

**Sol correction:** generic `RegisterMetrics` and string lookup are confined to bootstrap/schema binders. A binder returns a concrete set such as `SelectionMetrics{CandidateSet, FailsafeEngaged}`. Recording code addresses fields, not instrument-name strings, and uses generated constructors such as `schema.Metric.SelectionStage(schema.StageEligible)`, not `ion.Label("stage", "eligible")`. Lookup returns `(handle, error)` inside the binder; it never panics or returns a no-op. Runtime schema validation remains defense in depth for fleet compatibility callers.

Opus should make the same correction to §4.1. Its current `Counter(name string) Counter`/`Gauge(name string) Gauge` API has no error channel and permits exactly the name/key drift the schema is meant to prevent.

### 9.4 Opus iteration 7 resolves the prior blockers, with three stale remnants

Iteration 7 moves the debate forward rather than re-litigating it. Sol accepts:

- 59–85 engineer-days plus 6–10 SRE days as Opus's additive estimate. Together with Sol's 55–82 sum, the honest pre-spike planning envelope is **55–85**, not a negotiated midpoint.
- Question 7 moves to v3.1, while v3 pairs migrated p2p counts with the selection funnel so operators can see the exact discrepancy that masked the reference halt.
- `spanstatus` is active for new/modified code in v3; only the existing 296-site sweep is deferred to v3.1. This prevents the refactor from adding new trace debt without smuggling the whole trace programme into v3.

Most normative contradictions are now fixed. Three stale remnants remain in the updated file:

1. §6.1's Mermaid diagram still says 1,680 ion sites compile unchanged through the redefined accessor and only 809 calls migrate—the withdrawn global-forwarder model.
2. §2.5 still labels 78 functions as logger accessors even though §2.7 correctly explains that 78 was a double count; the planning unit is 62 construction sites plus the full usage inventory.
3. The decision preamble still says “Sol estimates 49–75 under a wider scope,” although that estimate and scope disagreement are both withdrawn.

These are editorial consistency defects rather than architecture disputes, but the normative file should not be frozen while it teaches a rejected migration path.

### 9.5 Sequencing correction

The incident detector must merge, ship, and be proven first. That is a release/promotion dependency, not a reason to leave engineers idle during SRE deployment and soak. Once the incident metric contract and descriptor golden are frozen, the `gETH/` and `FastsyncV2/` spikes plus non-conflicting runtime work can proceed in parallel. Opus's “nothing else starts” rule adds elapsed time without protecting the production gate.

### 9.6 Required Opus changes for the next iteration

1. Replace 939/484 with the reproducible 940/505 AST inventory, including all 1,445 reads and the 105 tracer derivations (corrected in iteration 4).
2. Replace domain `ion.Logger` fields with an emission-only logger plus separately injected tracer and typed metric sets; reserve lifecycle/control methods for `Runtime`.
3. Return typed domain metric sets from binders; ban string lookup and raw labels outside bootstrap/schema code.
4. Remove the three stale remnants in §9.4; the substantive adapter, estimate, Q7, trace-status, and 1,086-series corrections are accepted as complete.
5. Treat “detector first” as promotion ordering while allowing non-conflicting spikes during operational soak.

### 9.7 Iteration-3 convergence state

Resolved: v3/v3.1 scope, internal composition root, constructor injection, free-function disposition strategy, historical metric migration, output manifest, field semantics, safe warn default, no permanent adapter, questions 1–3 acceptance, 1,086 full-target bound, additive planning method, and span-status gate/sweep split.

Newly hardened: full accessor-use inventory; capability-separated injection; lifecycle authority; typed metric binding; complete 1,086-series bound; parallel-safe programme sequencing.

Still architecture-gated: Opus adoption or rebuttal of capability-separated injection, typed metric binders, the corrected AST inventory, and parallel-safe sequencing; plus removal of its three stale normative remnants.

Still evidence/operations-gated: descriptor golden, output manifest, downstream log consumers, 15-metric consumer inventory, both injection spikes, and a named production observability owner. The owner is the only blocker neither document can resolve from source. Freeze remains premature.

## 10. Fresh convergence cycle — iteration 4 response to Opus iteration 8

**Reviewed:** `../opus/ITERATION-8-REVIEW.md` and the updated 1,126-line `../opus/JMDN-V3-TELEMETRY-ARCHITECTURE.md`, both published on 2026-09-23 after Sol iteration 3.

**Disposition:** Opus applied all five requested iteration-3 changes and found a real escape hatch in Sol's proposed logger boundary. Sol accepts that correction. A fresh recount also settles the small tracer discrepancy in Opus's favour. The new blocking issue is shared rather than adversarial: both designs disabled the only default Prometheus representation of their resource identity. That would make fleet-aware dashboards contradict the architecture's own operator model.

### 10.1 Accepted from Opus

1. **Seal the logger boundary.** An emission-only method subset does not constrain the dynamic type. If `Runtime` stores `*ion.Ion` behind an unsealed `EventLogger`, a domain can assert it back to `*ion.Ion` and call `Shutdown`, `SetLevel`, `Tracer`, or `Meter`. The final interface carries an unexported marker and the dynamic value is a jmdn-owned wrapper, not the ion root.
2. **Keep the seal testable.** Sol adds one refinement: domain-package tests cannot implement an interface containing another package's unexported method. `internal/telemetry/testkit` therefore accepts a public emission-only capture sink and asks the parent telemetry package to wrap it. Production and tests both inject the same sealed dynamic type; wrapper construction is analyzer-allowlisted only in `Runtime` and the testkit.
3. **Correct the tracer inventory.** Direct source inspection finds **105** `logger().Tracer(...)` reads across 20 files: **72** inside receiver methods and **33** inside free functions. Sol's previous 104/71/33 figure missed one method read. The broader 1,445-read AST inventory remains unchanged.
4. **The regex mechanism is clarified.** Opus's old expression matched only `logger().X` selectors; it did not capture part of the assignment/argument tail as Sol had inferred. That correction does not change the 1,445 AST result or its authority.
5. **The five requested changes are substantively complete.** Opus now uses 940/505/1,445, capability-separated injection, typed metric binders, the joint 55–85-day envelope, corrected stale diagrams/counts, and parallel-safe engineering behind a detector-first promotion gate.

### 10.2 Shared blind spot — `WithoutTargetInfo` deletes fleet identity

Both normative documents said resource concepts such as environment, network, chain ID, role, version, and stable node alias belong on the OTel resource rather than every measurement. Both then proposed `WithoutTargetInfo()` for Prometheus descriptor parity.

Those decisions are incompatible. Under the [OTel Prometheus exporter contract](https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/metrics/sdk_exporters/prometheus.md#configuration), resource attributes are not copied to application metric labels by default and target info is enabled by default. The Go exporter emits one separate `target_info` family from the resource; `WithoutTargetInfo()` removes it. Prometheus still adds scrape-time `job` and `instance`, but it cannot query, group, or alert by chain, network, role, environment, version, or stable node identity unless those values come from service discovery/relabeling or `target_info`. Neither document specified the former.

Suppressing `target_info` is also unnecessary for the stated compatibility goal. It is a separate infrastructure family, not a label added to each application series, so it cannot change an application's name, HELP, type, labels, or buckets. Instrumentation-scope labels are different: they decorate every application point and should remain suppressed for descriptor stability.

**Sol decision:** retain exporter-generated `target_info` exactly once in the final reader. Allowlist and bound the resource fields; prohibit peer IDs, secrets, and arbitrary resource keys; freeze its exact generated label names and value in a separate golden. The 1,086 bound remains the application-series bound, with the one resource series accounted for in the infrastructure budget.

Prometheus supplies `job` and `instance` on scrape. Every resource-aware dashboard, recording rule, or alert uses an explicit join of the form:

```promql
jmdn_consensus_rounds_total
  * on(job, instance)
    group_left(service_version, deployment_environment_name, jmdn_network_name, jmdn_chain_id, jmdn_node_role, jmdn_node_alias)
    target_info
```

The exact sanitized label names come from the pinned exporter's golden; the example is not authority for spelling. Stage 0 relies on discovery's `job`/`instance` until the OTel reader lands. At cutover, the reader is the sole `target_info` producer; a hand-written identity family and the exporter family never coexist.

Required proof is a two-target integration test: give the nodes identical application labels but distinct stable identities, scrape both, run the real dashboard/alert joins, and assert that attribution remains distinct. A stable ID is operator-provisioned, unique within the fleet, persists across restart, and is not a peer ID or transient container address. The test also fails if resource attributes leak onto every application series, `target_info` contains an unapproved key, or two live targets expose the same stable ID. Missing-identity and duplicate-identity alerts make both failure modes visible.

### 10.3 Two remaining consistency defects in the Opus baseline

1. **Detector sequencing is still stated both ways.** The corrected sequencing paragraph says W1 gates production promotion while spikes and non-conflicting W2 work may proceed during SRE soak. The opening numbered order still says the detector “completes and is verified before anything else starts,” and §11's Gate says it completes before any restructuring begins. Those two sentences preserve the serial rule Opus explicitly withdrew.
2. **The accessor-definition dispute is already resolved but is still called contested.** §2.7's table correctly says 36 exact zero-argument accessors and 40 functions named `logger` when parameterized variants are included. The sentence below then says “R1 is contested: sol measures 40.” Sol does not: iteration 2 accepted 36 under R1 and 40 under the broader named-function rule.

These are editorial rather than architectural, but they affect schedule and estimating instructions and should not survive a frozen normative baseline.

### 10.4 Required Opus changes for the next iteration

1. Retain and govern one exporter-owned `target_info`; suppress scope labels, not the resource family. State explicitly that it is outside the 1,086 application bound.
2. Add the `job`/`instance` join contract, missing/duplicate-identity alerts, exact resource golden, and two-target attribution integration test.
3. Keep the sealed `EventLogger`, but specify the parent-owned wrapper and capture-sink test seam so domain tests do not need to implement an impossible unexported marker.
4. Use the resolved 105 tracer reads (72 method, 33 free) and preserve the 1,445 total.
5. Remove the two serial-sequencing remnants and the stale R1 “contested” sentence.
6. Pin the resource schema to the same current semantic-convention version across signals and use `deployment.environment.name`, not the older `deployment.environment` key currently shown in §8.1.

### 10.5 Iteration-4 convergence state

Resolved this round: the five iteration-3 requests; sealed capability separation; testability of the sealed boundary; the 105 tracer count.

Newly hardened: resource identity is now an executable Prometheus contract rather than prose attached only to OTel; `target_info` is governed separately from application descriptor parity; fleet query attribution and missing identity have named tests and alerts.

Still architecture-gated: Opus adoption or rebuttal of the Prometheus identity contract and cleanup of its two contradictory schedule/count remnants. Still evidence/operations-gated: descriptor and resource goldens, two-target query proof, output manifest, downstream log consumers, 15-metric consumer inventory, both injection spikes, and a named production observability owner.

This is round 4 of the requested fresh 3–5-round cycle. The architecture is close, but freezing before the fleet-identity contract is shared would certify dashboards that cannot select the resource dimensions both designs promise.

## 11. Fresh convergence cycle — iteration 5 freeze audit

The freeze state in this historical round is superseded by the targeted iteration 6 audit in §12.

**Reviewed:** `../opus/ITERATION-9-REVIEW.md` and the corresponding 1,196-line `../opus/JMDN-V3-TELEMETRY-ARCHITECTURE.md`, both updated on 2026-09-24.

**Disposition:** this is the fifth and final planned design iteration. Opus has resolved every item in Sol iteration 4 and supplied two valid repository-grounded additions. Sol accepts both, with explicit failure semantics and executable proof below. The freeze audit also found one source-compatibility blocker in a capability both documents had considered settled. Sol is now a freeze candidate; Opus has one narrow public-interface correction to reconcile.

### 11.1 Accepted from Opus iteration 9

1. **The `target_info` correction is complete.** Opus now retains the exporter-owned resource family exactly once, keeps scope labels suppressed, budgets the family outside the 1,086 application-series bound, and gives it a separate golden.
2. **The capability boundary is sealed and testable.** Opus adopted the parent-owned capture-sink seam without reopening the `*ion.Ion` escape hatch.
3. **The migration facts are aligned.** Both documents now use 105 tracer reads—72 method and 33 free-function—and 1,445 total accessor reads.
4. **The resource schema is current.** Opus pins one semantic-convention version and uses `deployment.environment.name`.
5. **The stale sequencing/count language is gone.** Detector-first is a production promotion gate, not an instruction to idle independent engineering; R1 is settled at 36 under Opus's exact-zero-parameter rule.
6. **Identity provisioning and relabel-aware joins are real missing requirements.** The shipped empty/default aliases and the existing Prometheus rule's proxy/relabel warning independently verify both findings.

### 11.2 Sol acknowledges and tightens the identity requirements

Opus asked Sol to acknowledge boot-time identity validation. **Acknowledged.** In fleet/production posture, an empty, whitespace-only, known-placeholder, malformed, or over-length node alias is a deterministic configuration defect and fails bootstrap before protocol workers. Logging an error and continuing would publish knowingly ambiguous telemetry. An explicitly selected local single-node development posture may generate a visibly development-only identity, but it cannot satisfy the production configuration gate.

The local validator cannot prove that an otherwise valid alias is unique across a fleet. Configuration management therefore owns uniqueness, while `JMDNTelemetryIdentityDuplicate` detects collisions after scrape. The stable logical-node alias remains distinct from `service.instance.id`, which follows the [OpenTelemetry service-instance uniqueness contract](https://opentelemetry.io/docs/specs/semconv/registry/attributes/service/); both are governed resource fields rather than peer IDs or transient addresses.

The join contract is also strengthened. v3 pins the final post-relabel `job`/`instance` pair as present and unique per target. The two-target release test applies the production proxy/relabel configuration and must still return exactly two attributed results. Each resource-aware rule has missing-join protection over its unjoined source, and Prometheus rule-evaluation failures are monitored, so an empty or many-to-many join cannot silently suppress a domain alert.

### 11.3 `Logger.Enabled` was a breaking interface change

Both normative documents require an `Enabled(Level)` check so suppressed hot-path logs do not eagerly serialize or allocate. Both also promise that ion v0.6 is additive-only and preserves existing fleet call patterns. The shorthand “add `Logger.Enabled(Level) bool`” violates that promise: in Go, adding a method to an exported interface breaks every type outside ion that implements it.

This is already observable in the checked fleet, not hypothetical. Eight external test doubles implement the current `ion.Logger` method set:

- JMDT-Sequencer-Orchestrator: `cmd/orchestrator/gapnet_integration_test.go`, `internal/block/txdict_to_test.go`, and `internal/txncheck/security_sig_test.go`;
- Mempool-Routing-Engine: `internal/v1/components/hashlookup/testdoubles_test.go`, `internal/v1/components/monitor/monitor_test.go`, `internal/v1/components/pending/fetcher_test.go`, `internal/v1/lifecycle/manager_test.go`, and `internal/v1/service/routing_test.go`.

MRE also aliases `ports.Logger = ion.Logger`, so the break propagates through its local port. Each listed type compiles today because it implements the eleven current logger methods. Adding a twelfth method makes the consumer's tests fail to compile even if no production call site uses `Enabled`.

### 11.4 Compatible capability shape

The requirement survives; only its public-API shape changes:

```go
package ion

type Level uint8

const (
    LevelDebug Level = iota
    LevelInfo
    LevelWarn
    LevelError
    LevelCritical
)

// New optional capability. Existing Logger implementations need not implement it.
type LevelEnabler interface {
    Enabled(Level) bool
}

// Additive concrete method; does not change Logger's method set.
func (i *Ion) Enabled(level Level) bool
```

The exact enum values are API detail, but parsing arbitrary strings on the hot path is rejected. `Runtime` holds the concrete `*ion.Ion`, creates the named child, and wraps its emission plus level-check capabilities in the sealed jmdn `EventLogger`. Domain code still receives `Enabled(ion.Level)`; fleet consumers that only implement `ion.Logger` remain unchanged. The capture-sink test seam implements the jmdn-local sink contract and is independent of the fleet interface.

`ForceFlush` follows the same rule: it is a concrete root/runtime lifecycle capability or a new separate interface, never a new method on `ion.Logger`. More generally, no existing exported interface grows methods during the v0.x compatibility window. If a capability must be optional, use a new one-method interface; if it belongs only to the root, add it to `*Ion`.

### 11.5 Required compatibility proof

Ion's v0.6 canary is more than “ion's own tests pass”:

1. Compile the eight known external logger implementations unchanged against v0.6.
2. Compile MRE's `type Logger = ion.Logger` port and its raw-meter flow unchanged.
3. Add an ion test fixture whose external-package type implements the old `Logger` method set and assert it remains assignable after every v0.x change.
4. Add an API-diff gate for exported interface method sets; any growth fails unless the module is taking an explicitly approved major-version break.
5. Separately prove `*Ion` and the jmdn wrapper satisfy `LevelEnabler`/`EventLogger`, including dynamic level changes and the disabled-log allocation benchmark.

### 11.6 Outstanding Opus reconciliation and freeze state

Opus iteration 9's only request to Sol—boot-time identity validation—is accepted and incorporated, including the relabel-aware proof. Iteration 4 is fully converged. The sole remaining design mismatch is now concrete: Opus §10.6 still says “Add `Logger.Enabled(Level) bool`,” which changes the exported interface and breaks eight checked fleet test doubles. Opus should adopt the optional `LevelEnabler` plus concrete `(*Ion).Enabled` shape above and keep `ForceFlush` off `ion.Logger`.

No remaining disagreement justifies another broad redesign round. The two freeze conditions are narrow:

- both normative documents preserve the current `ion.Logger` method set and use optional/concrete capabilities for level checks and root lifecycle;
- a production observability owner is named, or the absent owner is explicitly recorded as the sole organizational blocker to production promotion.

Descriptor/resource goldens, the API-diff and external-compile canaries, relabel-aware two-target proof, output manifest, downstream log-consumer inventory, 15-metric consumer inventory, and both injection spikes remain mandatory implementation evidence. They are promotion gates, not reasons to reopen settled architecture by prose.

**Freeze recommendation:** once Opus makes the one interface correction, freeze the technical architecture at this five-round baseline. Do not open a sixth general design iteration. If executable evidence falsifies a frozen assumption, amend the relevant ADR with the failing probe and impact; do not restart from preference. Production promotion remains blocked until the named-owner condition is satisfied.

## 12. Targeted iteration 6 — the unchecked Prometheus collector

**Reviewed:** Opus iteration 9 and normative §4.1/§5/§11 for the exporter audit; Opus published `ITERATION-10-FREEZE.md` and an updated normative document during this pass, which are reconciled in §12.4. This targeted round was requested after the planned five-round cycle and tests an implementation assumption in the proposed unified metrics boundary.

### 12.1 Source-level finding

Both architectures expect invalid metric declarations to fail before workers and treat exporter registration into the private Prometheus registry as a loud duplicate-name boundary. That holds for checked native collectors, but it does **not** follow for the Go OTel Prometheus exporter. In tagged [v0.68.0 `exporter.go`](https://github.com/open-telemetry/opentelemetry-go/blob/exporters/prometheus/v0.68.0/exporters/prometheus/exporter.go), `collector.Describe` yields no descriptors and explicitly identifies itself as *unchecked*. The exporter is registered at construction, while `validateMetrics` runs only inside `Collect`, after translation. A type conflict is dropped during collection; a HELP conflict keeps the first description. The same behavior is visible in tagged v0.66.0. The checked repository uses Prometheus client v1.24.1; its [registry contract](https://github.com/prometheus/client_golang/blob/v1.24.1/prometheus/registry.go) states that an unchecked collector always registers and that output inconsistency can surface at scrape time. Its pedantic registry also excludes unchecked collectors from descriptor checks.

The direct consequence is a gap in both documents' fail-fast promise: `WithRegisterer(privateRegistry)` returning nil does not rule out two distinct OTel names translating to the same Prometheus family, an OTel/native collision, or a dormant instrument that first collides after launch. A `Gather()` golden in CI catches only families the fixture actually emits. A clean first live scrape has the same coverage limit. This is source-level evidence, **not** an executed jmdn exporter probe; the selected version and exact generated names still need a runnable check.

### 12.2 Sol correction

The validated registry preflights the **translated** family schema, including names generated for counters and histograms, HELP, type, label set, boundaries, `target_info`, and fixed Go/process collectors. `libp2p_` is a reserved native namespace that no OTel application family may enter. The translation is pinned to the selected exporter strategy and checked against that exporter; a handwritten naming helper cannot silently drift.

Bootstrap also constructs an isolated registry with the real pinned OTel reader, resource, and fixed Go/process adapters. The probe emits representative synthetic points for every declared OTel instrument, including otherwise dormant ones, then calls `Gather()` and compares the result to the generated schema and fixed-native inventory. It fails before workers if a conflict or invalid family appears. The isolated probe prevents fake observations from contaminating production counters and histograms. The initial live `Gather()` remains a smoke check, while CI supplies explicit counter suffix, histogram series, HELP-only, translated-name, and OTel/native collision fixtures.

The live libp2p families are registered later: current `node.NewNode` passes a `libp2p_`-prefixed registerer into `libp2p.New` (`node/node.go:144,177`). They cannot be gathered in bootstrap without a host. The telemetry root instead owns that prefixed registerer, reserves its namespace against translated application names, and captures registration errors for the host constructor to reject before protocol workers begin. This adapter must contain a third-party panic path: pinned libp2p's `metricshelper.RegisterCollectors` panics on registry errors other than `AlreadyRegistered`, so the adapter records an underlying rejection while returning success to the helper; the host constructor checks the recorded fault, closes the host, and fails before workers. A later rejection is a visible telemetry health fault and cannot halt a running validator. CI constructs a real host against the combined registry, checks the resulting family inventory, and injects both constructor-time and post-start rejections. This keeps the boot guarantee aligned with the actual construction order.

This is inside Workstream 1's validated registry obligation. The 55–85 engineer-day planning envelope remains provisional until the existing kernel and metric spikes measure the probe's cost; no new confidence interval is claimed from source inspection alone.

### 12.3 Handoff to Opus and freeze state

Opus §5 says registration into the existing registry turns a duplicate name into a bootstrap error. That statement needs a qualifier: native checked collectors may reject at registration, but the OTel exporter is unchecked. Opus should add translated-family preflight and the isolated materialized `Gather()` proof to its v3 kernel and acceptance gate, including dormant OTel and fixed-native collisions. The late libp2p path needs the reserved prefix, validating registerer, and real-host combined-registry test. Its `schema.PromName` helper is valuable only when its mapping is tested against the pinned exporter, not treated as an independent authority.

Opus iteration 10 has already adopted the interface correction. Two narrow Opus wording/behavior gaps remain against Sol's normative contract: make the unchecked exporter collision boundary genuinely fail before workers (§12), and remove the option to continue a fleet node with an empty alias after merely logging at `error` (Opus §5.1). The latter is a deterministic configuration defect; Sol's production posture rejects it before workers. The production observability owner remains a separate organizational gate.

### 12.4 Response to Opus iteration 10

**Accepted:** Opus applied the non-breaking `LevelEnabler` shape and widened the API-diff gate to all four exported ion interfaces. Its fail-open rule is necessary: when an injected emission sink lacks the optional level-check capability, `EventLogger.Enabled` returns true and the guarded event is emitted. Sol's normative document now states that behavior and tests it through the capture sink.

**Corrected:** Opus reports zero external `ion.Tracer` implementers, but the same MRE test file it cites for `nopSpan` defines `nopTracer.Start(ctx, name, ...ion.SpanOption) (context.Context, ion.Span)` at line 42. `hashlookup.Deps.Tracer` has type `ion.Tracer`, and that test assigns `nopTracer{}` at line 330. The verified external canary is therefore eight loggers, one span, **at least one tracer**. No external `SpanOption` implementation is expected because that interface includes an ion-private method.

Opus marked its five-round baseline frozen before this targeted exporter audit was available. Its interface correction is settled. The requested technical amendments are the unchecked-exporter preflight plus late libp2p registration boundary, and fail-fast fleet identity validation: Opus §5.1 still permits logging an error and continuing on an empty alias, which contradicts Sol's production posture and would emit known-ambiguous `target_info`. The `nopTracer` canary count is a separate factual correction. The named production observability owner still gates promotion.
