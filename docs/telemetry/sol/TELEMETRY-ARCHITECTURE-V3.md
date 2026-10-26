# jmdn v3 Telemetry Architecture Decision and Migration Plan

**Status:** Targeted iteration 6 complete; freeze candidate pending Opus metric and identity amendments

**Date:** 2026-09-25

**Owners:** jmdn, ion, validator SRE
**Reference incident:** 2026-09-17 chain halt

## 1. Decision summary

jmdn v3 will finish with one explicit process-level telemetry composition root under `internal/telemetry`. That root owns validated configuration, the ion instance, the private Prometheus registry, the OTel Prometheus reader, the metrics HTTP server, resource identity, health, flush, and shutdown. Domain packages receive narrow logger/tracer/metric dependencies from constructors; they do not initialize telemetry and do not resolve a package-global singleton.

Ion remains a thin, additive facade over the OpenTelemetry Go SDK and zap. It keeps raw OTel APIs for fleet compatibility, but jmdn application code uses the validated registry. jmdn owns the blockchain-domain schema: logs, spans, and metrics share canonical concepts and event names while retaining signal-specific types and cardinality rules. There is no automatic conversion from log fields to span attributes or metric labels.

Prometheus remains the operator-facing query model in v3. All live jmdn application metrics—including the current 41 declarations and the incident metrics—are dispositioned and moved behind the one registry before v3 GA. Existing safe Prometheus names remain the compatibility contract; migration changes the implementation, not PromQL. Native Go/process/libp2p collectors may remain Prometheus collectors, but only the telemetry root registers them. Package-init `promauto`, application-owned raw Prometheus collectors, and domain use of raw OTel meters are absent from the v3 end state.

The first production increment still instruments the 2026-09-17 failure path and enables scraping. Its repository-local adapter is explicitly temporary: it ships the detector early, then is removed when the final registry lands. v3 is not complete while that adapter, the old `logging/` architecture, or the split application-metrics path remains.

## 2. Goals and non-goals

### Goals

- Detect a recurrence of the 2026-09-17 eligibility collapse within two minutes.
- Deliver the chain-progress, consensus-agreement, and liveness-risk cockpit in v3; complete divergence, sync, mempool, p2p, RPC, and EVM coverage—and the timed eight-question cockpit—in v3.1 without changing the v3 structure.
- Converge all production application logging and metrics on one explicit bootstrap, schema, lifecycle, and ownership boundary before v3 GA.
- Leave no package-init application instruments, lazy logger construction, package-global telemetry service locator, or temporary logging/metrics adapter in the v3 release artifact.
- Complete trace-context propagation across HTTP, gRPC, and consensus/p2p process boundaries in v3.1 without changing the v3 composition root.
- Make telemetry non-blocking and incapable of changing consensus decisions.
- Bound metric cardinality before instruments reach production.
- Give the ion-consuming fleet a compatible migration path.

### Non-goals

- Replace Prometheus or Grafana in v3.
- Force logs and metric labels into one value type or one exporter pipeline. Unification means shared ownership, configuration, resource identity, schema governance, lifecycle, and enforcement—not identical signal semantics.
- Convert user-facing CLI/report output, serialization writers, or the final pre-bootstrap/post-shutdown stderr fallback into structured operational logs.
- Change a safe, externally consumed Prometheus name merely to make naming aesthetically uniform.
- Put transaction hashes, peer IDs, addresses, block hashes, or request IDs in metric labels.
- Use telemetry as an input to reputation, selection, quorum, or state transition logic.
- Guarantee every error has a trace. Metrics and structured error logs are the durable error signals; tracing is sampled.

### Operator questions this architecture must answer

1. Is the chain making progress?
2. Is this node in consensus with the fleet?
3. Is the fleet about to lose liveness?
4. Is local or fleet state diverging?
5. Is this node falling behind the network head?
6. Is the mempool healthy?
7. Are peers and protocol traffic healthy?
8. Is EVM execution healthy?

## 3. Phase 1: validated ground truth

The prompt's measured inventory is accepted except where this section records a correction. Spot checks focused on claims that change the architecture or incident-detection outcome.

### 3.1 Spot checks

1. **Halt-path quantities:** confirmed at `AVC/NodeSelection/pkg/selection/vrf.go:76,201` that both selection paths discard `usedFallback`; confirmed in `Sequencer/Consensus.go` that `consensusReached` is already available beside round-health recording. The proposed instruments do not require new computation or consensus-path I/O.
2. **Metrics availability and exposure:** confirmed a private Prometheus registry and `/metrics` handler. The server is conditional on `cfg.Ports.Metrics > 0`; the default and checked devnet gate configuration use `0`. The `peer_id` label is present on a histogram and multiple vectors.
3. **Tracing semantics:** confirmed 296 production `RecordError` calls, zero production `SetStatus` calls, and no OTel HTTP/gRPC middleware or manual propagator invocation in jmdn.
4. **Configuration and initialization:** confirmed tracing is configured only inside the `logging.otel.enabled && endpoint != ""` branch; ion metrics are never configured; ion warnings are returned by `otelsetup.Setup` and discarded by `NewAsyncLogger`.
5. **Fleet compatibility:** checked current workspace modules and raw meter consumers. This changed the ion API decision.

### 3.2 Corrections and new findings

- The migration is not accurately described as six repositories. In the checked workspace, active v3 worktrees consume ion v0.5.0, while checked main/release refs range from v0.3.5 to v0.4.2. `seedNodes` and `JMDN-Mempool` currently do not import ion. The release inventory must be generated from source control at rollout time rather than maintained as prose.
- `v0.4.2..v0.5.0` changes no Go source; it is a dependency/tooling refresh, so the exported API surface is unchanged. The operational break is the module floor: ion moves from Go 1.25 to `go 1.26.0` plus `toolchain go1.26.8`. A maintenance branch must not acquire that toolchain migration merely to receive a telemetry correctness fix.
- Mempool-Routing-Engine already passes raw `metric.Meter` values through several component APIs; Sequencer Orchestrator also exposes `Ion.Meter`. Changing `Ion.Meter`'s return type is a breaking change.
- MRE is a useful but mixed reference. Some constructors return metric-construction errors; `hashlookup.NewResolver` instead logs warnings and degrades because its signature returns only `*Resolver`. The v3 API must make the startup decision explicit rather than copying either pattern blindly.
- Ion already exports `StatusError`/`StatusOK`, and Sequencer Orchestrator uses them. jmdn's zero `SetStatus` calls are a local enforcement failure, not a missing ion capability.
- `ion/fields` is used by jmdt-simulator. Its complete absence is jmdn-specific evidence that an optional schema retrofit without a gate does not stick.
- The claim that logging settings mirror ion's full configuration is false. `LoggingSettings` has no metrics block. jmdn cannot enable ion metrics through its unified settings.
- `settings.Load` supports defaults, YAML, `JMDN_*` environment variables, then explicit CLI overrides. Contrary to the initial competing review, logging environment overrides work under the pinned Viper v1.21.0. A fresh-process probe with a partial YAML file loaded `level=debug`, OTLP enabled, endpoint, username, password, tracing enabled, and `sampler=ratio:0.37`; it also preserved the YAML-only console setting. Logging keys work because defaults register them with Viper. The existing comments about `AutomaticEnv` are too broad or describe keys that were not registered. Keep explicit binds where present and add a regression test before relying on this behavior in production.
- There are two permanent initialization pins: `logging.NewAsyncLogger` and `otelsetup.Setup` each use `sync.Once`. A call before `settings.Load` installs default warn/console-only telemetry for the process lifetime. Production is currently safe by call order, not by construction.
- CI is stronger than the initial inventory suggests: build, `go test -short -race`, formatting, and full-tree golangci-lint run on main/release pull requests. However, neither CI nor Sonar runs on `v3base`, so every proposed gate is dormant for the active v3 branch. Its lint policy is also narrow: `govet`, `ineffassign`, `unused`, and `nolintlint`; `staticcheck` and `errcheck` are disabled and no telemetry invariants are checked.
- ThebeDB embeds `ion.Config` in its on-disk YAML model. That constrains field names and decoding. Only `ConsoleConfig` and `FileConfig` currently rely on Go comparability; OTEL/Tracing/Metrics use `reflect.DeepEqual`, so adding a map or slice to those three is not itself a compile break.
- The committee-auth `fmt.Printf` path is deliberate: its comment says it bypasses zerolog so denials reliably reach journald. Migration must prove the structured sink first and dual-emit during the cutover; replacing it mechanically could blind operators.
- Shutdown does wait for orchestrated workers before syncing and shutting down ion, but observability flush has a single shared three-second deadline. The target makes shutdown order and per-signal failure reporting explicit.
- The consensus audit register already asks for entropy-source, cache-occupancy, and rollout-divergence signals in `docs/audit/AVC-CONSENSUS-HANDOVER.md`. New consensus instrumentation must close those registered items rather than create a parallel backlog.
- An AST audit at the pinned `efa4501` tree finds **1,445** exact zero-argument `logger()` reads in production function bodies: **940** in receiver methods across 37 files and **505** in free functions across 42 files. Of these, 1,408 are direct selector receivers, 34 assign the returned root to a variable, and 3 pass it directly to another call. The competing 939/484 result came from a direct-selector regex that could not see any assignment or call-argument shape; the AST inventory is the costing and migration authority.
- **105** of those reads across 20 files are `logger().Tracer(...)`, split 72 in receiver methods and 33 in free functions. A fresh source count resolves Sol's previous 104/71/33 result in Opus's favour. Replacing every accessor with an `ion.Logger` field cannot compile those sites because `ion.Logger` has no `Tracer` method; logger and tracer are separate injected capabilities.
- The proposed `gETH` spike example is larger than described: `convertEthTxToConfigTx` contains 18, not 7, `logger().Debug` calls. They are pure conversion diagnostics and remain strong deletion candidates, but the discrepancy reinforces that the spike must produce a checked-in AST inventory rather than a hand count.
- The OTel Prometheus exporter does not copy resource attributes onto application metric points by default. Its documented default is to expose them through the separate `target_info` family; `WithoutTargetInfo()` removes that family. Suppressing both resource-as-constant-labels and `target_info` therefore makes `service.name`, environment, network, chain, role, and stable node identity unavailable to PromQL. `target_info` does not change an application metric's descriptor, so disabling it is not required for application-series parity. The [OTel Prometheus exporter contract](https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/metrics/sdk_exporters/prometheus.md#configuration) is the external authority; the pinned Go exporter's `Gather()` result remains the executable authority.
- Adding `Enabled` to the exported `ion.Logger` interface is not additive in Go. Eight external test doubles already implement that interface—five in Mempool-Routing-Engine and three in JMDT-Sequencer-Orchestrator—so the previously proposed interface edit would break both consumers at compile time. MRE's `hashlookup/testdoubles_test.go` also implements and assigns one external `ion.Span` and one external `ion.Tracer`; the interface method-set rule therefore covers all four exported ion interfaces (`Logger`, `Span`, `Tracer`, `SpanOption`). v0.6 adds an `ion.Level` type, a new optional `ion.LevelEnabler` interface, and a concrete `(*Ion).Enabled(ion.Level) bool` method; existing interface method sets remain unchanged.
- The shipped fleet defaults do not provide usable identity: `node.alias` is empty in `config/settings/defaults.go` and `jmdn_default.yaml`, `logging.service_name` is the same `"jmdn"` value on every default node, and the exchange profile contains an unenforced `<YOUR_NODE_ALIAS>` placeholder. Resource identity therefore has to be validated and provisioned; it cannot be inferred from the current defaults.
- The repository's only Prometheus alert file already warns that an `on(instance)` join fails when proxying or relabeling drops or rewrites `instance`. Resource-aware v3 queries inherit that risk. The final post-relabel `job`/`instance` pair must remain unique per live target, and the release proof must exercise the production relabel path rather than only clean direct scrapes.
- The [Go OTel Prometheus exporter at v0.68.0](https://github.com/open-telemetry/opentelemetry-go/blob/exporters/prometheus/v0.68.0/exporters/prometheus/exporter.go) implements `Describe` with no descriptors, so Prometheus registers it as an unchecked collector. Its translated-name/type validation runs during `Collect`; a type conflict drops a metric then, and a HELP conflict adopts the first description. `WithRegisterer` returning nil therefore does not establish the claimed bootstrap collision guarantee. The [Prometheus v1.24.1 registry contract](https://github.com/prometheus/client_golang/blob/v1.24.1/prometheus/registry.go) makes the distinction explicit: unchecked collectors always register, and even its pedantic collection-time descriptor check excludes them.
- The native collectors have two registration times in current jmdn: Go/process collectors register through `metrics/goroutines_metrics.go` at package initialization, while `node.NewNode` passes a `libp2p_`-prefixed registerer into `libp2p.New` (`node/node.go:144,177`). The v3 root moves Go/process registration into bootstrap, but libp2p families cannot be fully gathered there before the host exists. Their prefix and constructor-time registration require a separate boundary check. The pinned libp2p TCP transport has an opt-in `tcp.WithMetrics()` path that registers directly with the global Prometheus registerer on first connection; jmdn does not currently enable it, and v3 must not enable it without an explicit private-registry fix.

### 3.3 Actual jmdn configuration branches

| Input | Result |
|---|---|
| Console enabled | Ion console core receives configured format, color, and stderr split. |
| File enabled with path | Configured rotating file sink is used. |
| File enabled without path, setup args non-empty | Falls back to `logs/<filename>`; current filename default is empty, so this usually disables file output. |
| OTEL disabled or endpoint empty | OTLP logs off; tracing is also silently left off regardless of `logging.tracing.enabled`. |
| OTEL enabled with endpoint | OTLP log config is copied; tracing config is then copied and inherits the OTLP endpoint inside ion. |
| Tracing enabled alone | No effect because of the outer OTEL branch. |
| Metrics port greater than zero | Starts the Prometheus HTTP server on `binds.metrics`. |
| Metrics port zero | Prometheus registry exists in memory but is unreachable. |
| Ion metrics requested | Impossible through current jmdn settings; `cfg.Metrics` is never populated. |
| Ion warning | Stored in `globalWarnings`, returned by setup, then discarded by the only caller. |
| First logger call before settings load | Compiled defaults become permanent because both initialization layers are one-shot. |

## 4. Phase 2: target architecture

### 4.1 Signal flow

```text
jmdn process
  main
   └── internal/telemetry.Bootstrap(validated config)
        ├── ion root
        ├── domain logger factory ───── console/file + optional OTLP
        ├── validated metric registry ─ OTel Prometheus reader ─┐
        ├── native Go/process/libp2p collector adapters ────────┼─ private registry ─ /metrics ─ Prometheus
        ├── tracer provider ──────────── optional OTLP           │
        └── health/flush/shutdown ───────────────────────────────┘

  domain constructors receive only the logger, tracer, and typed metric set they need
```

The process performs no telemetry network I/O on a consensus/apply path. Synchronous recording is permitted only as measured, bounded local SDK work; it may allocate or contend internally and therefore has an explicit latency/allocation budget. SDK batch queues are bounded. Export failure drops telemetry and increments exporter/self-health signals; it never retries on a caller goroutine and never changes protocol behavior.

The clean boundary is structural, not merely a renamed singleton:

```text
internal/telemetry/
  bootstrap.go       # the only construction entry point
  runtime.go         # immutable handles, health, flush, shutdown
  logger.go          # sealed emission-only EventLogger wrapper; no root dynamic type,
                     # lifecycle, child construction, or level control
  config.go          # validated config mapping
  prometheus.go      # private registry, reader, native collector adapters, HTTP server
  schema/
    events.go        # stable event names
    fields.go        # log/span concepts; signal-specific constructors
    labels.go        # bounded metric enums with mandatory other
    metrics.go       # name/kind/unit/help/labels/buckets declared once
    errors.go        # stable error.kind taxonomy
  metrics/
    consensus.go selection.go sync.go p2p.go mempool.go evm.go rpc.go process.go
  testkit/
    logs.go metrics.go traces.go
```

`Runtime.Logger(component)`, `Runtime.Tracer(scope)`, and metric-set assembly are bootstrap operations, not package-global accessors. `main` passes only the capabilities each service needs. There is no `telemetry.Global()`, package-level `Log(component)`, or lazy fallback. Temporary migration adapters may exist on intermediate commits, but they are absent from the v3 release tree.

“Narrow logger” does not mean `ion.Logger`. That fleet interface also exposes `With`, `Named`, `Sync`, `Shutdown`, `SetLevel`, and `GetLevel`; giving it to every domain grants lifecycle and control-plane authority that belongs only to the runtime. jmdn defines an emission-only structural interface:

```go
type EventLogger interface {
    Enabled(ion.Level) bool
    Debug(context.Context, string, ...ion.Field)
    Info(context.Context, string, ...ion.Field)
    Warn(context.Context, string, ...ion.Field)
    Error(context.Context, string, error, ...ion.Field)
    Critical(context.Context, string, error, ...ion.Field)
    sealed()
}
```

The seal is load-bearing. Without it, `*ion.Ion` satisfies the emission subset and remains the dynamic value behind the interface; domain code can recover the root with a type assertion and call `Shutdown`, `SetLevel`, `Tracer`, or `Meter`. `Runtime` instead wraps the named ion logger in an unexported jmdn type that implements `sealed` and forwards only the methods above. `*ion.Ion` deliberately cannot satisfy `EventLogger`, and the wrapper exposes no `Named`, `With`, child, unwrap, lifecycle, or control method.

Sealing must not destroy testability. `internal/telemetry/testkit` provides a capture logger by asking the parent package to wrap a public emission-only `EventSink`; domain tests implement that sink, while the value passed to the service remains the same sealed wrapper used in production. Construction of the wrapper is allowlisted only in `Runtime` and the testkit. The `telemetrydeps` analyzer additionally rejects direct root/full-logger dependencies, assertions or conversions to `*ion.Ion`, and direct wrapper construction outside those locations.

`Enabled` does not extend `ion.Logger`. Ion v0.6 introduces an additive level enum and optional capability:

```go
type Level uint8

type LevelEnabler interface {
    Enabled(Level) bool
}

func (i *Ion) Enabled(level Level) bool
```

Adding a concrete method and a new interface preserves every existing implementation of `ion.Logger`; adding a method to `ion.Logger` would not. `Runtime` has the concrete root/child and builds the wrapper from its emission and level-check capabilities. The capture-sink test seam implements the jmdn-local sink contract, not the fleet interface.

If a wrapped emission sink does not implement `ion.LevelEnabler`, `EventLogger.Enabled` returns **true** so a guarded log still emits. Missing level-check support may cost an avoidable allocation; it must never suppress an event. The testkit exercises this fallback with a capture sink that has no `Enabled` method.

Only `Runtime` retains the full root and may flush, shut down, create children, or change levels. A service that traces receives `ion.Tracer` separately; it never recovers a tracer or meter from the logger. Metrics arrive only as a domain-specific typed set such as `SelectionMetrics`.

### 4.2 ADR-1: ion API and role

**Decision:** keep ion, but reduce it to a stable facade over OTel SDK providers, zap, propagation middleware, lifecycle, and schema enforcement.

Arguments for retaining ion:

- It is already the fleet's construction and lifecycle boundary.
- It owns the only tracer provider in jmdn and already has the OTLP metrics backend.
- Centralized fixes to resource attributes, propagation, sampler validation, and shutdown benefit every service.
- Replacing it would add a migration without resolving vocabulary, cardinality, or operational deployment.

Arguments for removing ion:

- Its wrappers currently obscure SDK behavior, differ between tracing and metrics, and contain untested no-op configuration.
- A custom facade can lag OTel versions and semantic conventions.
- Fleet call sites sometimes create multiple independent roots, defeating a process-wide lifecycle boundary.

The deciding constraint is migration safety. Ion stays, but it must not invent a second telemetry model. Raw OTel types remain available to existing fleet consumers; jmdn domain packages do not use them directly after the v3 migration. Wrappers add policy rather than alternate semantics.

#### Compatible metrics API

`Ion.Meter(name, ...opts) metric.Meter` remains unchanged. The generic declaration API is bootstrap-only; domain code receives a typed metric set:

```go
specs := []ion.MetricSpec{
    ion.Int64GaugeSpec(
        "jmdn.selection.candidate_set",
        ion.Unit("{peer}"),
        ion.Description("Peers remaining after a selection filter stage"),
        ion.Labels(
            ion.EnumLabel("stage", "active", "eligible", "final"),
        ),
    ),
    ion.Int64CounterSpec(
        "jmdn.selection.failsafe.engaged",
        ion.Unit("{event}"),
        ion.Description("Eligibility fail-safe engagements"),
    ),
}

registry, err := app.RegisterMetrics("jmdn.selection", specs...)
if err != nil {
    return fmt.Errorf("register selection metrics: %w", err)
}

selection, err := metrics.BindSelection(registry)
if err != nil {
    return fmt.Errorf("bind selection metrics: %w", err)
}

selection.CandidateSet.Record(
    ctx,
    int64(len(eligible)),
    schema.Metric.SelectionStage(schema.StageEligible),
)
selection.FailsafeEngaged.Add(ctx, 1)
```

Required surface:

- `MetricSpec`: name, kind, number type, unit, description, histogram boundaries, and label schema.
- Kinds: `Int64Counter`, `Float64Counter`, `Int64UpDownCounter`, synchronous `Int64Gauge`/`Float64Gauge`, and `Int64Histogram`/`Float64Histogram`.
- `RegisterMetrics(scope string, specs ...MetricSpec) (*MetricRegistry, error)` creates every instrument at boot and returns all construction/duplicate/schema errors together. Only telemetry bootstrap and schema binders may call it.
- Registry lookup methods return `(handle, error)` and are used only by a domain binder such as `BindSelection`. The binder resolves constant spec identities once and returns a concrete `SelectionMetrics`; absent or wrong-kind lookup is a bootstrap error.
- Domain recording code never looks up an instrument by string and never constructs a raw `ion.Label`. It uses fields on the injected typed set and generated schema label constructors, so misspelled names/keys and wrong label sets are compile-time failures in governed code. The runtime validator remains defense in depth for compatibility callers.
- A process-level registry rejects the same metric name with a different kind, unit, boundaries, description, or labels. An identical declaration is idempotent.
- The process-level registry also rejects distinct canonical specs that translate to the same Prometheus family or generated histogram/counter series, even when their canonical names differ. It checks the translated name, type, HELP, labels, and boundaries against every application spec, `target_info`, and the fixed Go/process collector families before constructing workers. `libp2p_` is reserved for the prefixed host-construction registerer; no OTel application metric may translate into that namespace. Translation uses the pinned exporter strategy and is verified by the actual exporter golden; a hand-maintained sanitizer is not the sole authority.
- Generated per-instrument constructors make undeclared label keys a compile-time error for governed code. Every enum requires an `other` value at registration. Unknown external values normalize to `other`, increment a bounded per-instrument violation counter, and do not create new series. Numeric identifiers are not label values.
- `MustRegisterMetrics` is permitted only in `main`/bootstrap and tests, before protocol goroutines start. Library packages return errors.
- Instrument errors are never discarded. CI bans `counter, _ :=`, ignored `ion.New` warnings, and ignored registration errors.
- Concrete `*Ion` exposes `ForceFlush(ctx)` separately from `Shutdown(ctx)` and reports per-signal shutdown errors with `errors.Join`; `ForceFlush` is not added to `ion.Logger`.

Failure semantics are deliberately asymmetric:

- Telemetry may refuse to start a node for a deterministic binary/schema defect; it may never stop a running node because a backend is unavailable.
- Invalid or duplicate schema, wrong-kind lookup, forbidden labels, and failed instrument construction fail bootstrap before protocol workers start. These are programming/configuration defects, not exporter outages.
- An optional exporter becoming unavailable after successful bootstrap degrades to bounded loss and a visible health signal. It never stops consensus.
- A required backend may fail bootstrap only in non-consensus environments. A validator never makes telemetry reachability a condition of participation.
- All jmdn application metrics use the validated registry by v3 GA. Raw `Ion.Meter` remains an ion compatibility API and may be used only inside the telemetry implementation or explicitly allowlisted third-party adapters; CI rejects it in domain packages.

#### Instrument registry implementation

- Registry creation is mutex-protected and boot-only.
- Recording handles are immutable and lock-free apart from OTel SDK internals.
- Label validation uses precomputed indexes and enum tables. No reflection or map construction occurs on the hot path. An out-of-enum value records as `other`; an enum without `other` is rejected at registration. A still-unmappable observation—possible only through a low-level API bypass or implementation defect—is dropped whole, increments a bounded self-counter, and emits a rate-limited internal warning. CI fails if that counter is non-zero in tests. Never strip only the bad key: doing so can merge the observation into another valid series and corrupt it.
- Asynchronous observations read atomics or immutable snapshots. A callback must not acquire a consensus, mempool, peer-manager, or database-pool lock.
- Histogram buckets are explicit in the spec; OTel defaults are not accepted for production instruments.

#### Signal vocabulary: shared names, separate types

Do not convert `ion.Field` to `attribute.KeyValue` or metric labels automatically. Logs legitimately carry unbounded diagnostic values; metrics must not. Instead, add a `schema` package that owns key constants and signal-specific constructors:

```go
schema.Log.BlockHeight(h)       // ion.Field
schema.Span.BlockHeight(h)      // attribute.KeyValue
schema.Metric.Outcome(outcome)  // ion.Label, bounded enum
```

Within ion, deprecate but retain `fields/` during the fleet compatibility window and forward its constructors to the log schema. Within jmdn, the old helpers and ad-hoc string keys are migrated before v3 GA. Name unification is mandatory; type unification is deliberately rejected.

#### Ion defects required before fleet adoption

- Apply or remove `MetricsConfig.Temporality` and `TracingConfig.Propagators`; validated no-ops are release blockers.
- Fix log-context bridging and prove native OTLP `TraceID`/`SpanID` population with an in-memory exporter test. Explicit string attributes remain useful but are not proof of correlation.
- Apply common resource attributes to all three signals with one semconv version.
- Reject unknown sampler strings and malformed ratios; never fall back to 100%.
- Replace the global unsynchronized `tracingDisabledLogged` boolean with per-instance `sync.Once` or `atomic.Bool`.
- Make `ionhttp.Client` honor its options.
- Make tracer and meter provider resolution symmetrical: both use the providers held by the `Ion` instance. Globals are installed only as an opt-in interoperability bridge during bootstrap.
- Add tests for meter setup/lifecycle, multi-reader configuration, logger core routing, context correlation, child shutdown behavior, and concurrent disabled tracing.

### 4.3 ADR-2: Prometheus scrape and OTLP push

**Decision:** use direct Prometheus scrape for v3 metrics. Add an optional OTel Prometheus reader adapter that registers into jmdn's existing private registry. Keep OTLP push available; adopt a Collector gateway when an infrastructure owner and backend SLO exist, but do not make it a v3 metrics dependency.

| Option | Operator fit | Cost/cardinality | Migration risk | Decision |
|---|---|---|---|---|
| OTel Prom reader into the telemetry root's registry | Familiar endpoint and one registry | One isolated dependency; in-process label gate | Low after descriptor-golden tests | Adopt for v3 |
| Collector scrapes current jmdn | Preserves PromQL/Grafana | Central place for limits and filtering, but new component/owner | Low code, medium operational | Target state, not v3 metrics blocker |
| Migrate/disposition all 41 application metrics through the validated registry | Clean structural end state; existing safe names preserved | Bounded implementation and query-compatibility work | Medium | Required for v3 |
| Unmanaged dual emission | Familiar during transition | Doubles series and can disagree | High | Prohibited |

Deployment contract:

- Enable `ports.metrics` and expose it only on the monitoring network. In containers, loopback is not reachable from a separate Prometheus container; bind to the private service network without publishing the port to the host/public network.
- Put the Prometheus adapter in an optional `ion/prometheus` package or equivalent additive constructor option so non-Prometheus consumers do not inherit API coupling. Do not put a `prometheus.Registerer`, map, or slice into `ion.Config`.
- Preserve `ion.New(cfg)` exactly. Add `ion.NewWithOptions(cfg, ion.WithMetricReader(reader))` or an equivalent additive entry point; the jmdn telemetry root supplies the reader backed by its private registry.
- During migration only, the old application collectors and the OTel reader may share the registry, but the same measurement never has two owners. Before v3 GA, every one of the 41 declarations has a checked-in disposition: migrate under its compatible Prometheus name, replace through an explicit query migration, or remove after proving no recording path and no external query consumer. Native Go/process/libp2p collectors remain isolated infrastructure adapters.
- Package-init `promauto` and the global `metrics.DefaultRegistry` are removed from the final application architecture. The application schema preflight makes duplicate translated names, incompatible descriptors, and schema errors fail bootstrap before workers start. Registering the OTel exporter alone cannot enforce this: it is an unchecked Prometheus collector whose own collision checks occur during collection.
- Bootstrap validates the OTel application schema, `target_info`, and fixed Go/process output in an isolated probe registry with the pinned reader and resource. It materializes representative samples for every declared OTel instrument, including histograms and otherwise dormant families, then gathers and compares the result with the generated schema and fixed-native inventory. Synthetic observations never enter the live registry. The root passes a telemetry-owned, `libp2p_`-prefixed validating registerer into host construction; it checks registration outcomes before protocol workers start and reports later registration failures as telemetry health faults without stopping a running validator. The pinned libp2p registration helper panics on non-`AlreadyRegistered` errors, so this adapter records an underlying rejection but returns a non-error to that helper; the constructor boundary then closes the host and fails startup if any rejection was recorded. A post-start rejection marks telemetry unhealthy without allowing a library panic to escape. This containment is limited to the libp2p adapter and must never be mistaken for successful metric registration. CI separately builds a real libp2p host against the combined registry and compares its gathered families with the approved native inventory. A clean initial live `Gather()` is a smoke check, not proof that dormant instruments are collision-free.
- Pin the Prometheus export contract explicitly rather than inheriting exporter defaults: register into the private registry, suppress instrumentation-scope labels on application series, retain the default `target_info` resource family, and pin the Prometheus translation strategy that adds conventional unit/counter suffixes. `target_info` is one separately budgeted infrastructure series per process resource; it does not add labels to, or otherwise alter, application metric descriptors. A `Gather()` probe over the pinned exporter version is the authority for its exact HELP, type, value, and sanitized label names.
- The final reader owns `target_info` exactly once. Its resource-label allowlist is `service.name`, `service.version`, `service.instance.id`, `deployment.environment.name`, `jmdn.network.name`, `jmdn.chain.id`, `jmdn.node.role`, `cloud.region` when configured, and a bounded stable node alias. The alias is operator-provisioned, unique within the fleet, persistent across restart, and never a peer ID or transient address. `service.instance.id` separately follows the [OpenTelemetry uniqueness contract](https://opentelemetry.io/docs/specs/semconv/registry/attributes/service/): it distinguishes concurrent instances of the same service and may use a stable inherent identifier. Values are length-bounded and non-secret; `peer_id` is prohibited. Before the OTel reader lands, Stage 0 relies only on Prometheus discovery's `job`/`instance` target labels. The atomic adapter cutover must never register a hand-written and exporter-generated `target_info` together.
- Fleet/production posture validates identity before any worker starts: empty, whitespace-only, known-placeholder, malformed, or over-length node aliases are fatal configuration defects. Local single-node development may use an explicitly selected relaxed posture with a visibly generated development identity; that posture cannot pass the production release profile. One process cannot prove fleet-wide uniqueness at bootstrap, so configuration management owns uniqueness and the duplicate-identity alert detects collisions after scrape.
- Prometheus adds `job` and `instance` to every scraped series, including `target_info`; those are the join keys, not the resource attributes themselves. The v3 deployment contract pins the **final post-relabel** `job`/`instance` pair as present and unique per live target; proxying may rewrite a value but may not drop or collapse the pair. Every dashboard, recording rule, and alert that selects or groups by environment, network, chain, role, version, or stable node ID uses an explicit `on(job, instance) group_left(...) target_info` join. The integration suite applies the production proxy/relabel configuration and proves both the input-to-output cardinality and the query result against the deployed Prometheus version instead of assuming resource attributes decorate each application series.
- “One measurement has one owner” governs the recording site and instrument identity, not the number of managed SDK readers. Registering an OTel Prometheus reader into the existing private registry satisfies the rule. Any later OTLP fan-out is an explicit bootstrap topology, and alert/recording rules must still select one authoritative route so samples are not double-counted.
- Deploy a real Prometheus and Grafana in devnet/staging and assign a production owner. Existing provisioning pointing at absent hostnames is not a deployment.
- Cross-process tracing is not accepted without a concrete receiving trace backend in devnet/staging. Ion may export directly to that backend. If a Collector is used, it has bounded memory, batch, retry, and sending queues and is failure-tested under node load.
- Grafana provisioning and alert rules are integration-tested against a disposable Prometheus plus the selected trace receiver.

### 4.4 ADR-3: logging consolidation

**Decision:** complete logging convergence in v3, in risk order rather than by a blind repository-wide replacement.

1. Prove the configured structured sink and default/runtime levels in staging before touching deliberate journald bypasses. The current default `warn` level does include `Warn`; the risk is sink/routing equivalence, not level suppression.
2. For security denials, add a bounded counter and governed `Warn` event alongside the existing `fmt.Printf`, verify both reach operations, then remove the print after one release. Do not classify an expected remote authentication denial as `Critical`; reserve `Critical` for invariant failure or imminent termination.
3. Install temporary stdlib and zerolog adapters only where they shorten an intermediate migration. They route into the initialized root, preserve levels, and map `Fatal` to a logged critical error plus explicit caller control flow; ion `Critical` itself never exits. These adapters do not survive the v3 gate.
4. Replace package-local lazy logger accessors with constructor injection. A package-global `telemetry.Log(component)` would reproduce the current service-locator problem under a cleaner name and is prohibited.
5. Migrate packages by domain: consensus/selection/security, p2p, sync, mempool/RPC/EVM, then service startup. Keep user-facing CLI/report output as output, not logs.
6. Delete the dead logging interface/builder/topic-routing layer and ban production imports of stdlib `log`, zerolog, direct zap, and operational `fmt.Print*`. Allowlists are line/function scoped for CLI output, serialization writers, and pre-bootstrap/post-shutdown stderr fallback; whole files such as `main.go`, `seed/`, or `seednode/` are not allowlisted.

Risks and required regression tests:

- Zerolog's global level, fluent field accumulation, sampling, hooks, caller reporting, and fatal/panic semantics do not map one-for-one.
- Stdlib log flags/prefixes and multiline output change.
- Tests that match human-formatted strings must move to structured core assertions.
- Ion caller depth must be adjusted for adapters or caller data will point to the bridge.
- Journald routing and severity prefixes must remain stable until operations changes its parser.
- `cassata` receives an ion-to-zap core adapter or a scoped real zap logger; `zap.NewNop` is removed.
- Keep the compiled default at `warn` for v3. A runtime/local administrative override may enable `info`; changing the fleet default is a separate measured decision after devnet log-volume, storage, and hot-path allocation evidence. Security `Warn` visibility does not require a default-level change.
- Do not delete every `topic` field mechanically. Split the current overloaded key: logger-routing constants become bounded `component`; real GossipSub/message topics become governed `p2p.topic`. Do not bulk-delete `function` as caller duplication: migrate logical values to governed `operation`, omitting a site only when review proves the value has no operational meaning. Downstream log queries/parsers are inventoried before either field contract changes.

#### Allocation policy

Add `(*Ion).Enabled(ion.Level) bool` plus the separate `ion.LevelEnabler` capability; do **not** add a method to the existing `ion.Logger` interface. The sealed jmdn `EventLogger` exposes the check. Use it when a disabled log statement would format, serialize, marshal, copy a large slice/map, compute a hash/string, or build more than roughly five fields. Do not guard primitive field construction; the branch and visual noise are not justified. Replace `fmt.Sprintf` messages with constant event messages and typed fields. Benchmarks cover suppressed hot-path logging and are compared in CI.

### 4.5 ADR-4: fleet strategy

- Release ion v0.6 from the Go 1.26 line with additive APIs and defect fixes. Do not change existing signatures.
- Backport independently safe correctness fixes needed by Go 1.25 maintenance consumers to ion v0.4.3 instead of forcing a compiler, CI-image, and builder migration onto release branches. Go 1.24 consumers remain on their supported line unless that line receives a deliberate backport.
- Publish a compatibility table and generate a consumer inventory from all organization `go.mod` files.
- Canary in ion itself, MRE (raw-meter compatibility), jmdn, then remaining consumers.
- Require each consumer to have exactly one root `ion.New` in production. Existing fleet libraries may retain `ion.Logger`, `ion.Tracer`, raw `metric.Meter`, or the registry under the additive compatibility contract. New jmdn domain constructors receive only the emission-only `EventLogger`, `ion.Tracer`, and their typed metric set; they do not receive the root or full lifecycle logger and do not construct providers.
- Freeze the exported `ion.Logger` method set through the v0.x compatibility window. New optional capabilities use new interfaces or concrete `*Ion` methods; adding a method to an existing public interface requires a major version even when ion's own implementation already has it.
- Keep deprecated APIs for one minor-release window. Removal or a changed `Meter` signature requires ion v1.0.
- Mixed v0.4.2/v0.5.0/v0.6.0 operation is source- and wire-compatible because v0.5.0 changed no Go source and telemetry does not enter protocol state. Requiring v0.6 still raises the Go toolchain floor to 1.26 for that consumer; CI, Docker builders, and developer toolchains are explicit rollout gates.
- Rollback is a dependency-version rollback and configuration switch; no stored state or consensus data changes.

## 5. Instrumentation specification

### 5.1 Naming and label rules

- Canonical OTel metric names use dots; the Prometheus translation uses underscores and appends `_total` to monotonic counters.
- Every metric begins `jmdn.`. Existing legacy names remain until migrated and are mapped in recording rules.
- Resource attributes are not copied onto every application series. The allowlisted set—`service.name`, `service.version`, `service.instance.id`, current-semconv `deployment.environment.name`, `cloud.region` when present, `jmdn.network.name`, `jmdn.chain.id`, `jmdn.node.role`, and a bounded stable node alias—is exported once through `target_info` and joined in PromQL on scrape-time `job`/`instance`. `peer_id` is not a resource attribute, and arbitrary resource keys are rejected at bootstrap. The old `deployment.environment` key is not introduced into the v3 schema.
- Forbidden metric labels: peer/node/client ID, transaction/block hash, account/contract/address, signature/public key, request/trace ID, remote address, block height, round, slot, epoch, free-form error/message/reason, URL, or SQL.
- Allowed labels must be declared as enums. Unknown external values become `other`; a telemetry self-counter records normalization.
- Cardinality budget: the schema below has a calculated upper bound of 1,086 application series per process when every bounded label combination is present, the gas histogram has at most 16 explicit boundaries, and Prometheus histogram `+Inf`, sum, and count series are included. The previously shared 1,079 result was the table subtotal and omitted the required seven-value `security.denials{reason}` counter; 1,079 + 7 = 1,086. The single `target_info` resource series, SDK/runtime self-metrics, and native third-party collectors are budgeted separately; there is no permanent legacy-application bucket. CI generates exact checked-in application and infrastructure goldens, rejects an application increase above 25% without approval, and enforces a 2,000-series application hard cap. Preinitialized counter combinations form a stable boot-time floor; histogram and gauge families reach the upper bound only as their bounded combinations are observed. Any new label or resource attribute requires a series and disclosure review.

### 5.2 Metrics

Prometheus names in parentheses are the required translated names. The table is the target schema, not a claim that every new row blocks v3:

- **v3:** migrate/disposition every historical application metric; add the chain, consensus, selection, security, panic, and telemetry-health signals required for the reference incident and operator questions 1–3. Existing live p2p/database/process measurements remain available through the final registry or telemetry-owned native adapters.
- **v3.1 coverage:** add the new divergence, sync, mempool, richer p2p, RPC, and EVM rows needed to complete questions 4–8. They use the already-frozen registry, schema, cardinality, and CI contracts; no second architecture or compatibility adapter is introduced.

| Canonical name | Type/unit | Labels and bounds | Recording point |
|---|---|---|---|
| `jmdn.chain.head.height` (`jmdn_chain_head_height`) | Int64 gauge `{block}` | none | Successful local commit |
| `jmdn.chain.last_commit.time` (`jmdn_chain_last_commit_time_seconds`) | Int64 gauge `s` | none | Successful local commit, Unix seconds |
| `jmdn.consensus.rounds` (`jmdn_consensus_rounds_total`) | Int64 counter `{round}` | `outcome`: `success`, `timeout`, `no_quorum`, `validation_error`, `internal_error` | Exactly once when a round resolves |
| `jmdn.consensus.round.duration` (`jmdn_consensus_round_duration_seconds`) | Float64 histogram `s`; buckets .01,.025,.05,.1,.25,.5,1,2.5,5,10,30 | `outcome` as above | Round resolution |
| `jmdn.consensus.committee.size` | Int64 gauge `{validator}` | `kind`: `seated`, `expected` | Committee resolution |
| `jmdn.consensus.votes` | Int64 counter `{vote}` | `phase`: `prepare`, `commit`; `decision`: `agree`, `disagree`, `invalid`, `missing` | Add one for each classified received vote; at resolution add the unresolved expected-vote count to `missing` |
| `jmdn.consensus.quorum.margin` | Int64 histogram `{vote}`; buckets -10,-5,-3,-2,-1,0,1,2,3,5,8,13 | `outcome`: `success`, `timeout`, `no_quorum` | Once when a vote-bearing round resolves; value is received minus required. Validation/internal failures do not record a semantically meaningless margin. |
| `jmdn.consensus.view_changes` | Int64 counter `{event}` | `reason`: `timeout`, `invalid_proposal`, `leader_unavailable`, `other` | View change accepted |
| `jmdn.consensus.equivocations` | Int64 counter `{event}` | none | Verified equivocation |
| `jmdn.selection.candidate_set` (`jmdn_selection_candidate_set`) | Int64 gauge `{peer}` | `stage`: `active`, `eligible`, `final` | Each selection pass, before and after filtering |
| `jmdn.selection.failsafe.engaged` (`jmdn_selection_failsafe_engaged_total`) | Int64 counter `{event}` | none | `usedFallback == true` |
| `jmdn.selection.reputation.weight` | Float64 histogram `1`; buckets 0,.1,.2,.3,.35,.4,.45,.5,.55,.6,.7,.8,.9,1 | none | Once per peer when a reputation snapshot is installed, not every selection |
| `jmdn.selection.below_threshold` | Int64 gauge `{peer}` | none | Reputation snapshot install |
| `jmdn.block.apply` | Int64 counter `{block}` | `outcome`: `success`, `validation_error`, `state_error`, `storage_error`, `panic` | One result per apply attempt |
| `jmdn.block.apply.duration` | Float64 histogram `s`; buckets .005,.01,.025,.05,.1,.25,.5,1,2.5,5,10 | `outcome` as above | Apply completion |
| `jmdn.chain.divergence` | Int64 counter `{event}` | `kind`: `fingerprint_mismatch`, `fork`, `reorg`, `rollback` | Detection/accepted recovery action |
| `jmdn.sync.network_head.height` | Int64 gauge `{block}` | none | Network head observation |
| `jmdn.sync.lag` | Int64 gauge `{block}` | none | Local/network head update |
| `jmdn.sync.blocks_applied` | Int64 counter `{block}` | `mode`: `live`, `catchup`, `fastsync` | Successful apply; rate is derived |
| `jmdn.sync.last_progress.time` | Int64 gauge `s` | `mode` as above | Sync progress |
| `jmdn.mempool.transactions` | Int64 gauge `{transaction}` | `state`: `pending`, `ready`, `deferred` | Atomic snapshot on state change |
| `jmdn.mempool.admissions` | Int64 counter `{transaction}` | `outcome`: `accepted`, `invalid`, `duplicate`, `capacity`, `fee`, `other` | Admission decision |
| `jmdn.mempool.evictions` | Int64 counter `{transaction}` | `reason`: `included`, `expired`, `capacity`, `replaced`, `other` | Eviction |
| `jmdn.p2p.peers` | Int64 gauge `{peer}` | `state`: `connected`, `managed`, `active`, `eligible` | Peer-state snapshot |
| `jmdn.p2p.peer_changes` | Int64 counter `{event}` | `direction`: `join`, `leave`; `reason`: `normal`, `timeout`, `protocol`, `auth`, `other` | Peer state transition |
| `jmdn.p2p.messages` | Int64 counter `{message}` | `direction`: `send`, `receive`; `topic`: governed enum of protocol topics; `outcome`: `ok`, `invalid`, `dropped`, `error` | Message completion, never peer-labelled |
| `jmdn.p2p.message.size` | Int64 histogram `By`; buckets 64,256,1024,4096,16384,65536,262144,1048576 | `direction` only | Message completion; topic-level traffic remains available from the message counter |
| `jmdn.evm.executions` | Int64 counter `{transaction}` | `outcome`: `success`, `revert`, `invalid`, `internal_error` | EVM completion |
| `jmdn.evm.execution.duration` | Float64 histogram `s`; buckets .0005,.001,.0025,.005,.01,.025,.05,.1,.25,1,5 | `outcome` as above | EVM completion |
| `jmdn.evm.gas.used` | Int64 histogram `{gas}`; buckets set from measured workload before rollout | `outcome`: `success`, `revert` | EVM completion |
| `jmdn.rpc.requests` | Int64 counter `{request}` | `transport`: `http`, `ws`, `grpc`; governed `method`; `outcome`: `ok`, `invalid`, `unauthorized`, `rate_limited`, `internal_error` | Request completion |
| `jmdn.rpc.request.duration` | Float64 histogram `s`; buckets .001,.0025,.005,.01,.025,.05,.1,.25,.5,1,2.5,5 | `transport` only | Request completion; method and outcome remain on the request counter to avoid spending 675 series on per-method latency distributions |
| `jmdn.security.denials` | Int64 counter `{event}` | `reason`: `eligibility_source_error`, `not_in_eligible_set`, `pubkey_mismatch`, `legacy_unpinned`, `balance_rejected`, `vote_send_failed`, `other` | Each governed security rejection; alert on the counter rate, not the log line |
| `jmdn.panics` | Int64 counter `{panic}` | governed `component` | Every recovery block, after stack capture |

`topic`, `method`, and `component` enums live in the schema package. They have an explicit `other` value and maxima of 16, 32, and 32 values respectively. The method bound covers the 29 JSON-RPC names measured under `gETH/`, plus `other` and two reserved slots. The topic bound covers the six centrally declared pubsub topics in `config/constants.go` plus `other` and headroom; CI derives the enum from the governed constants and fails if the real set outgrows the cap. Shard IDs may be labels only when the configured shard count is capped at 64 and values are normalized into that set; v3's base schema does not require them.

### 5.3 Required alerts and recording rules

| Alert | Release | Condition | For | Meaning |
|---|---|---|---:|---|
| `SelectionCandidateSetCritical` | v3 | `jmdn_selection_candidate_set{stage="final"} < 3` | 2m | Leading liveness risk; page validator SRE |
| `SelectionFailsafeEngaged` | v3 | `increase(jmdn_selection_failsafe_engaged_total[5m]) > 0` | 0m | Eligibility policy collapsed; page and annotate deployment timeline |
| `ConsensusNoSuccessfulRounds` | v3 | `sum(rate(jmdn_consensus_rounds_total{outcome="success"}[5m])) == 0` and node is expected to participate | 2m | Chain or node has stopped making progress |
| `ChainHeightStalled` | v3 | `time() - jmdn_chain_last_commit_time_seconds > 120` | 1m | Lagging confirmation of halt |
| `QuorumMarginLow` | v3 | more than 10% of successful rounds in 5m fall in `jmdn_consensus_quorum_margin_bucket{le="1"}`, with a non-zero round count | 0m | Repeated narrow quorum is a leading liveness risk; use the histogram bucket/count ratio recording rule |
| `StateFingerprintMismatch` | v3.1 | `increase(jmdn_chain_divergence_total{kind="fingerprint_mismatch"}[5m]) > 0` | 0m | Page immediately; potential fork |
| `SyncStalled` | v3.1 | sync lag > 0 and `time() - jmdn_sync_last_progress_time_seconds > 120` | 2m | Node is falling behind |

Fleet dashboards show per-node values and fleet aggregates side by side. Queries needing environment, network, chain, role, version, or stable node identity join the application series to `target_info` on `job`/`instance`; they do not assume OTel resources appear as application labels. The selection panel overlays connected, active, eligible, and final candidate counts so a healthy p2p layer cannot mask selection collapse.

The join contract is observable, not merely a dashboard convention. A resource-aware rule has a sibling missing-join rule over its unjoined input (`source_vector unless on(job, instance) target_info{...}`), and Prometheus rule-evaluation failures are monitored independently. This catches silent empty results and many-to-many join errors instead of allowing a broken relabel configuration to suppress a domain alert.

The `QuorumMarginLow` recording rule is the ratio of
`sum(rate(jmdn_consensus_quorum_margin_bucket{le="1",outcome="success"}[5m]))` to
`sum(rate(jmdn_consensus_quorum_margin_count{outcome="success"}[5m]))`, guarded by a non-zero increase in the count. This measures repeated narrow rounds rather than an arbitrary tally visible at scrape time.

All bounded counter label combinations are initialized with zero at bootstrap so absence is not confused with zero activity. Progress alerts are paired with target-health and absence alerts:

- `JMDNTelemetryTargetMissing`: expected validator inventory minus `up{job="jmdn"} == 1` for two scrape intervals.
- `JMDNTelemetryIdentityMissing`: an alive target returned by `(up{job="jmdn"} == 1) unless on(job, instance) target_info{service_instance_id!="",jmdn_node_alias!=""}` for two scrape intervals; a scrape without both governed service-instance and logical-node identity is degraded telemetry, not a healthy target. The exact generated label spellings are substituted from the exporter golden.
- `JMDNTelemetryIdentityDuplicate`: more than one live `target_info` series with the same non-empty stable node alias, or the same `service.instance.id` on concurrent service instances; cloned identity is a configuration error because fleet aggregation would merge distinct validators.
- `JMDNTelemetryResourceJoinMissing`: a resource-aware rule's non-empty source vector has no matching governed `target_info` on the final `job`/`instance` keys. This is shared infrastructure protection for all resource-aware domain rules, not a bespoke copy for each dashboard panel.
- `JMDNCriticalSeriesAbsent`: `absent(jmdn_consensus_rounds_total)` or `absent(jmdn_selection_candidate_set)` while the target is up.
- `ConsensusNoSuccessfulRounds` evaluates only for expected consensus participants and is accompanied by the target/series alerts; telemetry loss cannot suppress the incident.

CI gathers the real registry and freezes translated names, units, `_total` suffixes, and zero-initialized series before dashboards or alerts merge.

### 5.4 Span schema

Span names are constants. IDs, indices, heights, methods, and hashes never appear in names.

| Span name | Kind | Required attributes |
|---|---|---|
| `consensus.round` | internal/server | `jmdn.block.height`, `jmdn.consensus.round`, `jmdn.consensus.view`, `jmdn.consensus.outcome` |
| `consensus.propose` | producer | height, round, proposer ID |
| `consensus.vote.validate` | consumer/internal | height, round, validator ID, vote decision |
| `consensus.commit` | internal/producer | height, round, block hash |
| `selection.filter` | internal | active count, eligible count, final count, fail-safe used |
| `selection.choose` | internal | candidate count, requested count |
| `block.validate` | consumer/internal | height, validation stage |
| `block.apply` | internal | height, apply mode |
| `sync.catch_up` | client/internal | start height, target height, mode |
| `mempool.admit` | server/internal | transaction type, outcome |
| `p2p.message.send` / `p2p.message.receive` | producer/consumer | governed topic, peer ID, payload bytes |
| `rpc.request` | server | RPC system, governed method, response class |
| `evm.execute` | internal | transaction type, gas limit, outcome |
| `db.operation` | client | database system, governed operation, outcome |

High-cardinality values are permitted on sampled spans when useful: transaction hash, block hash, peer ID, account, and contract. Secrets, private keys, signatures, raw transactions, authorization headers, and full request bodies are prohibited.

On any returned error:

```go
span.RecordError(err, trace.WithAttributes(schema.Span.ErrorKind(kind)))
span.SetStatus(codes.Error, kind.String())
```

Do not put the raw error text in status description. Expected negative decisions such as an invalid remote signature set a bounded outcome attribute and error status only when they represent failure of the span's operation.

#### Cross-process propagation (v3.1)

- HTTP and gRPC clients/servers use ion middleware that invokes the configured W3C Trace Context propagator and a request-ID interceptor.
- Propagate baggage only from an allowlist; never forward arbitrary inbound baggage.
- Consensus/p2p adds an optional `trace_context` field to `PubSubMessages.Message`, the payload actually serialized on both custom-gossip and GossipSub paths. Do not use outer `GossipMessage.Metadata`: `PublishMessage` marshals only `.Data` on the active GossipSub branch, and receiver reconstructions also omit outer metadata. The carrier contains only `traceparent` and allowlisted `tracestate`, capped at 512 bytes. Invalid context is ignored and counted.
- Trace context is excluded from block hashes, transaction hashes, signatures, quorum inputs, deduplication keys, and state-machine data. A node must make the same consensus decision whether the field is absent, valid, or malformed.
- This additive JSON field requires no protocol feature gate: old Go decoders ignore it and new peers accept its absence. Retain a local propagation toggle for rapid rollback, but do not negotiate it as consensus capability.
- Sampling is trust-aware. Authenticated, bounded committee peers may continue a remote sampled parent. Arbitrary p2p senders cannot force local recording with the sampled bit; their context is represented as a span link and local sampling/rate limits apply.
- A child span is created before handing work to a goroutine; the goroutine receives that context. A parent cannot end before child work is joined or intentionally linked.

### 5.5 Log schema

Messages are stable event descriptions, not interpolated prose. `event.name` is a governed dotted identifier.

Required common fields:

| Key | Type | Use |
|---|---|---|
| `event.name` | string enum | Stable aggregation key, e.g. `selection.failsafe_engaged` |
| `component` | string enum | Owning subsystem |
| `operation` | string enum | Current operation when useful |
| `error.kind` | string enum | Stable taxonomy on errors |
| `error` | error | Human diagnostic text through `ion.Err` |
| `duration_ms` | float64 | Aggregatable elapsed time; never `ion.Duration` |
| `block.height`, `slot`, `epoch`, `round`, `view` | uint64 | Chain position |
| `block.hash`, `tx.hash`, `peer.id`, `validator.id`, `proposer.id` | string | Diagnostic identifiers, logs/spans only |
| `count`, `size_bytes`, `pending_count` | integer | Quantities |
| `outcome`, `reason` | governed string enum | Bounded machine-readable result |
| `trace_id`, `span_id`, `request_id` | string | Added from context, not hand-written |

Delete `created_at` and `log_file` after downstream-parser inventory; these duplicate journald/backend time and routing. Reclassify rather than delete the other two high-volume keys: component-like `topic` values become governed `component`, real protocol topics become governed `p2p.topic`, and logical `function` values become governed `operation`.

`function` is not redundant with shipped caller metadata. Production uses `development: false`; ion adds `zap.AddCaller()` only in development, and the default systemd encoder removes the caller key entirely. Even when enabled, zap emits `file:line`, whereas the existing values are stable logical names such as `Type.Method`. The safe deletion count is therefore 725 (`created_at` + `log_file`), while 1,388 `topic`/`function` observations require semantic migration.

Levels:

- **Debug:** per-message/per-transaction detail, cache hits, normal retries; normally suppressed.
- **Info:** node lifecycle and infrequent state transitions—round success summaries, peer topology changes, sync mode changes, committed blocks. Do not emit one Info per hot-path substep.
- **Warn:** degraded but continuing behavior, eligibility fail-safe, near-quorum, remote invalid input, retry exhaustion with fallback, security denial.
- **Error:** requested local operation failed, state apply/storage failure, exporter disabled after initialization, recovered panic. Include error kind and error.
- **Critical:** invariant violation, state divergence, or condition after which the caller will terminate/refuse consensus. `Critical` does not exit; control flow remains explicit.

Security-relevant events use normal ion sinks plus `event.name=security.*`, `security.action`, and bounded `reason`; they are never stdout-only. Public keys and signatures are redacted or truncated according to policy.

### 5.6 Error taxonomy

Every operational error maps to one stable `error.kind`; raw concrete types remain available through `%w`, `errors.Is`, and `errors.As`.

Top-level kinds:

- `invalid_input`
- `unauthorized`
- `unavailable`
- `timeout`
- `conflict`
- `capacity`
- `not_found`
- `protocol_violation`
- `consensus_no_quorum`
- `consensus_divergence`
- `state_validation`
- `storage`
- `internal`
- `canceled`

Domain packages expose typed errors that implement `Kind() telemetry.ErrorKind` and `Unwrap() error`. Boundary helpers map known sentinels and typed errors; unknown errors become `internal`. Metric labels use only `error.kind` where the relevant metric specification explicitly permits it. Logs/spans may include the wrapped error. `%v` wrapping that breaks the chain is rejected in review and by static analysis where detectable.

### 5.7 Sampling

- Propagation is always on, independent of recording.
- Parent-based custom head sampler: 100% for consensus, selection, security, divergence, and block-apply root spans; 10% for p2p/RPC/EVM/sync initially. Honor sampled parents from authenticated bounded committee peers; arbitrary remote sampled bits cannot force local recording and instead become links subject to local policy.
- Tune only from measured span volume and a declared monthly budget. An invalid sampler configuration fails startup when tracing is required or disables tracing with a surfaced warning in optional mode; it never becomes 100% implicitly.
- Metrics and error logs remain unsampled. Collector tail policies may retain errors and high latency only for spans that reached it; they are not a substitute for metrics/logs.

## 6. Phase 3: institutional controls

### 6.1 Ownership and change process

- `CODEOWNERS` requires one node-domain owner and one named observability operations owner for ion, schema, Prometheus/Grafana, trace backend, dashboards, alerts, and any Collector configuration. “Validator SRE” without an accountable team/person and service SLO is not ownership.
- Every new feature's design includes failure quantities, leading indicators, error kinds, and cardinality calculation.
- Every incident review asks which signal should have detected, diagnosed, and bounded the incident, and creates a regression test or alert where appropriate.
- The schema package and this document are versioned. Breaking metric or field changes get a deprecation period and recording rule.
- Before freeze, the architecture document contains only current normative decisions. Retracted alternatives, superseded diagrams, and iteration provenance live in the iteration review files, not inline beside executable guidance.

### 6.2 Static enforcement

No analysis harness exists today. Budget approximately three engineer-days for the multichecker, CI wiring, baselining, and first three rules, then roughly half a day per additional rule before repository-specific false-positive tuning. Run it in golangci-lint or as a dedicated CI step with these rules:

- `ctxlog`: reject `context.Background()`/`TODO()` passed to log calls when an enclosing context or derived span context is available; allow only documented bootstrap/shutdown sites.
- `metriclabels`: instruments must be declared through the schema registry; reject forbidden labels and undeclared label values.
- `dynamicspan`: reject non-constant span names and formatting/concatenation in `Start`.
- `spanstatus`: on error-return paths that call `RecordError`, require `SetStatus(codes.Error, ...)` or a shared helper that does both.
- `loglibs`: reject production imports/calls of stdlib log, zerolog, direct zap, and `fmt.Print*` outside allowlisted CLI/bootstrap adapters.
- `ignoredtelemetry`: reject ignored ion warnings, metric constructor errors, registration errors, `Sync`, `ForceFlush`, and `Shutdown` errors.
- `logschema`: reject arbitrary field keys outside the schema package and interpolated log messages.
- `telemetrydeps`: reject `*ion.Ion`, full `ion.Logger`, raw meters, string instrument lookup, and raw label construction in jmdn domain fields/parameters; permit them only in the composition root, schema binders, and explicitly reviewed third-party adapters. Also reject enabling libp2p `tcp.WithMetrics()` while its implementation registers into the global Prometheus default rather than the telemetry-owned registry.
- `locklog`: flag logger calls and metric callbacks while a mutex/rwmutex lock is held; initial rollout is advisory until reviewed.
- `wrapcheck`: reject new `fmt.Errorf` error wrapping with `%v` when the operand is an error.

Roll out each rule in diff-only mode while migration is in progress. Burn the production baseline to zero before the v3 clean-room gate for the v3 structural rules. `spanstatus` is the deliberate exception: it is active in v3 and rejects every new or modified `RecordError` path without `SetStatus`, while the existing 296-site baseline is burned down in v3.1 with trace completion. New consensus, selection, security, and telemetry code has no grandfathering at any stage.

The first enforcement change is making CI run on `v3base`; without it, every later gate is decorative. Add cheap `forbidigo`/`depguard` rules before the custom analyzer, with explicit allowlists for CLI output and adapter packages.
Set golangci-lint's test-file analysis on as part of the same change; otherwise telemetry helpers and forbidden logging patterns can enter through `_test.go` and later be copied into production unnoticed.

Add a telemetry reconciliation check that fails when a dashboard or alert references an undefined metric, an alert lacks `runbook_url`, or a critical metric has no recording-point test. A statically “defined but never observed” metric is reported for review, not deleted automatically; external consumers and conditional paths must be inventoried through a deprecation window.

### 6.3 Runtime and integration tests

- Unit-test every metric declaration, allowed label value, histogram boundary, and error mapping.
- Gather the Prometheus registry in tests and assert exact metric names/help/types and absence of peer/hash/address labels. Freeze `target_info` separately: exactly one series, value 1, the reviewed resource-label set, and no peer ID, secret, or unbounded attribute.
- Exercise the pinned exporter in a materialized, isolated registry: two distinct OTel names that sanitize to one Prometheus family, a counter suffix collision, a histogram-generated-series collision, a fixed-native family collision, a reserved `libp2p_` namespace collision, and a HELP-only conflict must each fail the schema preflight before workers. Assert the real exporter can otherwise defer or normalize these conflicts at collection, so a passing `WithRegisterer` call cannot be mistaken for proof. The probe covers every declared OTel family and resource label, including instruments that are normally dormant. A separate combined-registry integration test constructs a libp2p host, verifies its actual prefixed families, injects a registration rejection during construction and after startup, and asserts respectively controlled host cleanup/startup refusal and a health fault without a library panic.
- Run ion tests with `-race`, including concurrent calls to disabled tracing and registry declaration.
- Use in-memory OTel exporters to assert resource attributes, span status, parentage, and native log TraceID/SpanID.
- Run a two-process propagation test for HTTP, gRPC, and the additive p2p payload field, including old/new peers and malformed/oversized carriers.
- CI boots two distinguishable jmdn targets plus Prometheus and the selected trace receiver, applies the production proxy/relabel configuration, scrapes real metrics, evaluates alert rules with `promtool`, and verifies the required dashboard queries return exactly two correctly attributed series after the explicit `target_info` join. The test fails if `job`/`instance` is dropped or collapsed, a join returns empty or many-to-many, resource labels leak onto every application series, or two targets share a stable identity. A Collector is included only when it is the selected deployment topology.
- A synthetic recurrence test installs 29 active peers with weights 0.30–0.43 and asserts: eligible/final candidate set becomes zero or fallback is engaged, the fail-safe counter increments, and the leading alert fires within its window.
- Benchmark consensus selection, vote handling, block apply, and suppressed logging with telemetry on/off. Reject material regression: target under 1% throughput and under 2% allocation increase on these paths unless explicitly approved.

### 6.4 Context, concurrency, and lifecycle rules

- Context is the first argument across service boundaries. Do not store request contexts in structs or replace them inside a call graph.
- Detached work uses `context.WithoutCancel(parent)` only with a written reason and a new timeout; it preserves trace context. `context.Background()` is limited to process bootstrap and bounded shutdown.
- Synchronous handles from the validated registry are used directly; domain code does not construct OTel or Prometheus instruments. Custom shared gauge state uses `atomic.Int64`/`atomic.Uint64` or immutable atomic pointers.
- Never log or call user metric callbacks while holding a domain lock. Copy primitive state, unlock, then record.
- Telemetry bootstrap occurs once in `main`, after validated settings and before worker construction. Packages receive dependencies; package-level `logger()` cannot initialize the system.
- Initialization state is explicit: `Uninitialized -> Bootstrapped -> ShuttingDown -> Closed`. Calls before bootstrap use a visible bootstrap logger or fail in tests; they never silently pin defaults.
- Shutdown order: stop accepting requests; stop protocol producers; cancel workers; join consensus/apply work; stop metrics HTTP server; force-flush metrics, traces, then logs under separate bounded child deadlines; shut providers; report joined errors to stderr. Deadline expiry never blocks process exit indefinitely.
- All 12 panic recoveries record `jmdn.panics`, log the recovered value and `debug.Stack()`, mark the active span failed, then follow the documented recovery/termination policy. Silent recovery is prohibited.

### 6.5 Operational practice

- Dashboard one is the validator cockpit: chain progress, selection funnel, fail-safe, round outcome, quorum margin, divergence, sync lag, mempool, and exporter health.
- Every page links to a versioned runbook with verification queries, node-local versus fleet-wide comparison, safe remediation, and escalation owner.
- Alerts have a quarterly game day. The first game day replays the 2026-09-17 collapse.
- Telemetry has an explicit series, bytes/day, and retention budget per environment. Monthly review examines top series and log/span volume producers.
- Backend and optional Collector health are monitored independently; absence of telemetry is itself an alert, using external scrape/heartbeat checks rather than the impaired pipeline alone.
- Cardinality budgets are computed from instrument schemas, node count, scrape interval, and retention before approval. As the reference bound, the current `p2p_heartbeat_latency_seconds{peer_id}` creates roughly 14 series per peer—about 140,000 series at 10,000 peer IDs from one metric—so removing all five peer-labelled vectors is a v3 gate, not later cleanup.

## 7. Phase 4: independently shippable migration plan

Estimates are engineer-days, not elapsed calendar time. Operational deployment work is shown separately. The workstreams below are deliberately non-overlapping so the programme total can be reproduced by addition; two representative constructor-injection spikes and the 41-metric disposition inventory must replace these ranges with measured throughput before commitment.

### Workstream 0 — Incident detector and scrape path (8–12 engineer-days + 3–5 SRE days; ship first)

Scope:

- Add `v3base` to CI and Sonar triggers so subsequent gates actually run.
- Deploy Prometheus and Grafana in devnet/staging with a named owner; replace provisioned hostnames that currently point to nonexistent services.
- Add the reference-incident metrics through an explicit boot-time `RegisterHaltMetrics(registerer) (*HaltMetrics, error)` boundary. Do not use package-init `promauto` or return no-op handles. The adapter is a temporary delivery seam: Workstream 1 removes it, and v3 GA rejects its presence.
- Remove `peer_id` from the five existing metric vectors before broadly enabling the endpoint; preserve metric names and publish the label-removal query migration.
- Stop discarding `usedFallback` in both selection paths.
- Set the metrics port to 9101 with a loopback default bind and retain port `0` as an explicit opt-out. Container profiles use a private service interface without host publication; bare-metal profiles keep loopback with same-host or host-network scraping. The endpoint stays on the private mux.
- Reconcile the 15 statically unobserved metrics and two phantom dashboard references through recording-site and external-query inventory; static non-observation alone is not deletion authority.
- Add the first four alerts, the selection-funnel dashboard, and the synthetic 29-peer collapse test.

Verification:

- A staging replay causes candidate set 29→0 or engages fallback, increments the fail-safe counter, and fires the leading alert within two minutes.
- Prometheus is up, Grafana returns current data, and no application metric has a peer-labelled series.
- Duplicate or invalid registration refuses startup before consensus workers; the direct Prometheus collector used by this temporary increment supplies registration-time checks. Workstream 1 adds translated-name preflight for the unchecked OTel exporter. A Prometheus outage after startup does not affect consensus.

Rollback: disable the scrape/deployment increment and revert its additive recording. This rollback does not satisfy the final v3 structural gate.

### Workstream 1 — Unified kernel, ion minimum, and configuration safety (10–15 engineer-days; v3 gate)

Scope:

- Create `internal/telemetry`; one `Bootstrap` after validated settings owns the ion root, private registry, OTel reader, native collector adapters, metrics server, resource identity, health, flush, and shutdown.
- Own a validating registerer for the later `libp2p.New` call. It enforces the reserved prefix and captures underlying registration errors without returning them to libp2p's panic-on-error helper. The constructor checks the captured errors, closes the host, and fails before consensus/application workers start. After startup, a captured error marks telemetry unhealthy without terminating the validator. The clean-room integration test uses the real host path and combined registry.
- Add explicit settings for logs, traces, metrics, resources, required/optional mode, and endpoints. Remove the nested trace gate, plumb metrics, surface warnings, and add authenticated/local runtime level control or SIGHUP reload.
- Add explicit deployment posture and stable node identity settings. Fleet/production bootstrap rejects empty, placeholder, malformed, or over-length identity before workers; the relaxed single-node development posture is explicit and cannot satisfy the production configuration gate. Fleet collision remains alert-detected because a node cannot establish global uniqueness locally.
- Add the minimum additive ion v0.6 capabilities required by the final jmdn structure: `ion.Level`, the new `ion.LevelEnabler` interface, concrete `(*Ion).Enabled`, validated metric registration, explicit exporter/translation options, provider-local tracer/meter resolution, concrete-root force flush, joined shutdown errors, and race-tested lifecycle behavior. Do not extend `ion.Logger`. Broader fleet adoption remains independently scheduled.
- Publish the v0.6 migration/compatibility test module on the Go 1.26 line; backport independently safe maintenance fixes to Go 1.25 consumers rather than coupling them to the new registry.
- Define the constructor boundary for narrow logger, tracer, and typed metric dependencies. No new package-global accessor or service locator is permitted.
- Remove the Workstream 0 collector atomically when the equivalent schema instruments land. Old and new collectors never coexist; a gathered-descriptor golden proves compatibility or records an explicit query migration.

Verification:

- Configuration tests cover YAML, partial YAML plus environment, CLI, invalid sampler, missing endpoint, optional degradation, required-mode refusal, and fleet identity. Nested level, OTLP credentials, trace settings, and sampler values are asserted with and without a file; fleet posture rejects the shipped empty value and exchange placeholder before workers start.
- An early package call cannot install a provider. Shutdown reports bounded per-signal flush results.
- An isolated full-schema OTel/fixed-native probe and translated-name preflight complete before workers; counter, histogram, HELP, and resource-family collision fixtures fail startup even when OTel exporter registration itself succeeds. The live registry is left free of synthetic observations. The later libp2p host constructor reports registration errors before protocol workers, and CI gathers the combined registry with a real host.
- Ion unit/race tests pass; existing v0.4.2/v0.5 call patterns, all eight known external `ion.Logger` test doubles, MRE's `Span` and `Tracer` doubles, and MRE raw-meter patterns compile unchanged against v0.6 under Go 1.26. An API-diff gate rejects method-set growth in all four exported ion interfaces.

Rollback: compatibility scaffolding may exist during migration. It cannot be present in the release artifact, and a final candidate cannot roll back to split registry/lifecycle ownership and still pass v3.

### Workstream 2 — Existing application-metric convergence (6–10 engineer-days; v3 gate)

Scope:

- Give all 41 historical declarations a checked-in migrate/replace/remove disposition backed by recording-site, dashboard, alert, and external-query evidence.
- Move every live application measurement into the validated registry domain by domain. Preserve safe names, HELP text, type, labels, and buckets; never dual-emit one measurement.
- Remove package-init `promauto`, the application-global `metrics.DefaultRegistry`, and raw Prometheus/OTel instrument construction from domain packages. Keep Go/process/libp2p collectors only behind telemetry-owned adapters.
- Add the chain, consensus, selection, security, panic, and telemetry-health signals required for the incident and operator questions 1–3. Preserve existing live p2p, database, and process visibility while ownership changes.
- Reconcile consensus signals with `docs/audit/AVC-CONSENSUS-HANDOVER.md`; build the v3 cockpit, recording rules, alerts, and runbooks.

Verification:

- Registry and dashboard goldens prove descriptor compatibility or an approved versioned query migration for every changed series.
- No dead registered application metric or phantom dashboard reference remains.
- The generated v3 schema count is exact, below 2,000, and bounded under 10,000 synthetic peer IDs. Fault injection covers no quorum, low margin, storage failure, selection collapse, panic, and telemetry loss.

Rollback: revert the complete domain owner and its queries before the clean-room gate; never re-enable two owners concurrently.

### Workstream 3 — Logging structure and call-site convergence (27–38 engineer-days; v3 gate)

Scope:

- Finish constructor injection and delete lazy package logger accessors. The AST inventory is 1,445 reads: 940 in methods and 505 in free functions. Classify every free function as method conversion, explicit parameter, or log deletion; a package variable is not an allowed fourth outcome.
- Split the old root use into three dependencies. Domain structs/functions receive the sealed emission-only `EventLogger` wrapper; the 105 `logger().Tracer(...)` sites receive a separate scoped `ion.Tracer`; metrics arrive as typed domain sets. Domain code never receives `*ion.Ion` or full `ion.Logger` and never type-asserts or converts back to them. The testkit wraps capture sinks without weakening the production dynamic type.
- Delete the dead `LoggingInterface`, half-dead builder, topic-to-identical-logger map, one-shot setup path, and all temporary forwarding adapters after callers move.
- Introduce governed event, component, operation, field, and error-kind schemas. Reclassify overloaded `topic` as `component` or `p2p.topic`; remove `created_at` and `log_file` after consumer inventory; migrate logical `function` to governed `operation` rather than pretending shipped caller metadata replaces it.
- Migrate 514 stdlib-log calls, 223 zerolog calls, the direct zap site, and operational `fmt.Print*` calls to injected ion loggers. The allowlist is function-scoped: `main.go`, `seed/`, and `seednode/` are not exempt as files or directories.
- Classify all 43 production `fmt.Fprint*` calls. Buffer/hash serialization stays output; pre-bootstrap and final-shutdown stderr are explicit fallbacks.
- Prove the six security decisions in the structured sink plus bounded counters before removing temporary stdout dual emission.
- Repair the 163 unambiguous context severances, triage the remainder, remove log-under-lock sites, capture all panic stacks, and remove `zap.NewNop`.
- Install `loglibs`, `logschema`, `ctxlog`, `ignoredtelemetry`, `telemetrydeps`, and the remaining dependency-boundary gates on `v3base`.

Verification:

- No package initializes or globally resolves telemetry outside bootstrap. No domain field/parameter has type `*ion.Ion` or full `ion.Logger`; only the runtime has lifecycle/control-plane methods. The old `logging/` package has zero production importers before deletion.
- Production service code has zero unapproved stdlib-log, zerolog, direct-zap, operational stdout, or lazy-root use. Every surviving output exception has an exact function and rationale.
- Schema tests reject unknown event/field values; security failure injection reaches logs and metrics; suppressed hot-path allocation and consensus throughput remain within budget.

Rollback: domains can migrate and revert independently before the clean-room gate. The final release has no forwarding adapter.

### Workstream 4 — Clean-room release and rollout (4–7 engineer-days + 3–5 SRE days; v3 gate)

Scope:

- Delete all migration adapters and obsolete logging/metrics construction; enable analyzers repository-wide with no production baseline exemptions.
- Run the 29-peer recurrence, cardinality, configuration, backend-outage, shutdown, race, performance, and query-reconciliation suites against the release artifact.
- Conduct a timed exercise for operator questions 1–3 plus a telemetry-loss game day.
- Canary validators, compare query continuity and log volume, and promote only after the agreed soak.

Verification: section 9.1 passes as one release checklist. The artifact under test is the clean tree, not a promise to remove adapters later.

Rollback: roll back the v3 candidate as a unit. Telemetry never changes stored state or protocol data.

### Workstream 5 — Domain-coverage completion (10–15 engineer-days; v3.1)

- Add new divergence, sync, mempool, richer p2p, RPC, and EVM signals through the v3 registry and schema; no new composition path is introduced.
- Complete cockpit questions 4–8, the remaining alerts/runbooks, and fault injection for fingerprint mismatch, stalled sync, mempool pressure, peer/protocol failure, RPC overload, and EVM failure.
- Freeze the full target-schema series golden; the calculated target upper bound is 1,086 application series per process.

### Workstream 6 — Distributed trace completion (4–6 engineer-days; v3.1)

- Repair remaining span-status and lifecycle defects; deploy a concrete trace receiver with an accountable owner.
- Install HTTP/gRPC propagation and the optional p2p field in serialized `PubSubMessages.Message`; fuzz malformed/oversized carriers and test remote-sampling abuse.
- Prove one two-node proposal-to-vote-to-commit trace and identical consensus output when context is absent or corrupt.

### Workstream 7 — Fleet and Collector evolution (v3.1+; separately estimated)

- Upgrade other ion consumers in canary order, respecting the Go 1.26 toolchain floor and maintenance backports.
- Adopt a Collector only with an owner, SLO, retention/cost model, bounded queues, and failure-game evidence.
- Remove ion compatibility APIs only under normal semantic-versioning rules; jmdn cleanliness does not require a fleet flag day.

### Programme effort and critical path

- **v3:** Workstreams 0–4 total **55–82 engineer-days plus 6–10 SRE days**. This is the arithmetic sum of 8–12, 10–15, 6–10, 27–38, and 4–7; it replaces the prior 49–75 headline, which did not equal its own stage ranges. Plan roughly **9–13 elapsed weeks** with two engineers and an SRE partner, pending the two injection spikes and metric inventory.
- Opus's independently rebuilt sum is 59–85 engineer-days plus the same SRE range. Use **55–85 engineer-days** as the cross-review planning envelope until the two spikes replace both estimates; the four-day disagreement is estimation noise, not an architecture decision.
- “Incident detector first” is a promotion dependency, not a ban on parallel engineering. The detector and its golden must merge and ship first, but the `gETH/`/`FastsyncV2/` spikes and non-conflicting runtime work may proceed while SRE deployment and soak run. Serializing all restructuring behind operational verification adds elapsed time without reducing code risk.
- **v3.1 coverage:** Workstream 5 adds **10–15 engineer-days**. It expands operator coverage without changing the v3 ownership or construction model.
- **v3.1 tracing:** Workstream 6 adds **4–6 engineer-days** plus backend operations work. Fleet/Collector changes remain separately owned and estimated.
- The smaller incident increment remains valuable, but it is not the definition of v3 completion. Conversely, full new-domain coverage is not required to prove logging and metric unification. The v3/v3.1 boundary is structural versus additive coverage, not clean versus incomplete architecture.

## 8. What we will not do in v3

- Replace ion outright or hide raw OTel APIs.
- Change `Ion.Meter`'s return type.
- Add Prometheus coupling to ion core or a second Prometheus registry; the optional adapter must register into jmdn's existing registry.
- Perform an unreviewable big-bang commit. The migration is domain-by-domain, but every domain reaches the one final architecture before v3 GA.
- Require the new divergence, sync, mempool, richer p2p, RPC, or EVM instrument set—or cockpit questions 4–8—to complete the v3 structural release. Those additions are v3.1; any existing live measurements in those domains still migrate behind the v3 registry.
- Run two emitters for the same measurement.
- Put peer IDs, hashes, addresses, heights, rounds, or raw errors into metric labels.
- Sample metrics or error logs.
- Make telemetry availability a prerequisite for consensus, block apply, or node startup unless an operator explicitly selects `telemetry.required` for a non-consensus environment.
- Accept arbitrary baggage or include trace context in signed/hashed consensus data.
- Require cross-process trace propagation, a Collector, exemplars, continuous profiling, eBPF auto-instrumentation, or per-peer metric debugging for v3. Those are v3.1+ capabilities; local tracing remains healthy and lifecycle-correct.
- Promise trace completeness for every transaction; sampled traces supplement, not replace, metrics and logs.
- Rename safe legacy metric contracts merely for visual uniformity. A clean implementation can preserve an old emitted name.
- Ship a global `telemetry.Log(component)` accessor, permanent compatibility adapter, package-init application metric, or whole-file stdout allowlist and call the result pristine.

## 9. Acceptance criteria

### 9.1 v3 release gate

1. Exactly one `internal/telemetry.Bootstrap` constructs telemetry after validated settings and before workers. No domain package constructs or globally resolves a root, holds `*ion.Ion` or full `ion.Logger`, or can call telemetry lifecycle/level controls. Services receive only the sealed wrapper (including through the capture-sink test seam), a separate tracer where needed, and typed metric sets; compile/analyzer fixtures prove the root cannot satisfy or be recovered from `EventLogger`. All four exported ion interface method sets are unchanged, the eight logger plus one span plus one tracer external implementations compile, and a sink without `LevelEnabler` still emits guarded logs. Initialization and shutdown state transitions are race-tested.
2. The old logging interface/builder/topic-routing architecture and every temporary logging/metrics adapter are absent. Production service code has zero unapproved stdlib-log, zerolog, direct-zap, operational stdout, package-init `promauto`, raw Prometheus collector, or raw OTel-meter use. The narrow CLI/serialization/stderr allowlist is function-scoped and reviewed.
3. All 41 historical application metrics have a checked-in disposition and query evidence. Every live application measurement is declared once in schema and recorded through the validated registry; domain code has no string instrument lookup or raw label construction. No dead application metric or phantom dashboard series remains. Native Go/process/libp2p collectors are telemetry-owned adapters, not domain APIs.
4. Exact registry goldens freeze names, HELP text, types, labels, buckets, exporter options, and zero-initialized bounded counters. A full-schema isolated OTel/fixed-native probe covers dormant families before workers, and a real-host combined-registry test covers later libp2p families; the reserved namespace and validating host registerer prevent unreviewed overlap. Exporter registration alone is insufficient. `target_info` exists exactly once with the governed resource set. A live two-target test applies the production proxy/relabel configuration, proves unique post-relabel `job`/`instance` keys, and returns exactly two correctly attributed resource-aware query results; missing/colliding identity, silent-empty joins, and rule-evaluation failures alert. No production metric has a peer/hash/address/request identifier label; application series remain below 2,000 and bounded under 10,000 synthetic peers.
5. Operators can answer validator questions 1–3—chain progress, consensus agreement, and approaching liveness loss—from the cockpit in under one minute during a timed exercise. Every v3 alert references a metric proven to move, has target/absence companions where required, links a runbook, and passes `promtool` plus live integration tests.
6. The 2026-09-17 synthetic recurrence pages within two minutes and distinguishes healthy p2p connectivity from collapsed selection eligibility.
7. Security denials reach a governed structured sink and bounded counter with no stdout-only path. Every panic recovery records the counter, stack, governed error kind, and explicit recovery/termination policy.
8. Configuration cannot be pinned before settings load; nested environment overrides are regression-tested with and without a file; fleet/production posture refuses empty or placeholder node identity before workers; warnings cannot be discarded; optional backend loss is visible but cannot stop a validator; flush/shutdown is bounded per signal.
9. CI runs on `v3base` and enforces the analyzer set. Structural rules have no production baseline exemptions; `spanstatus` rejects new or modified violations while its checked-in legacy baseline is reserved for the v3.1 sweep. Configuration, race, cardinality, metric-query reconciliation, backend outage, and shutdown integration suites pass.
10. Telemetry remains observation-only and stays within the measured hot-path budget: target under 1% throughput and under 2% allocation regression unless explicitly approved with evidence.

### 9.2 v3.1+ completion criteria

1. New divergence, sync, mempool, richer p2p, RPC, and EVM signals use the v3 registry and schema; the generated full-target golden matches the reviewed schema and remains at or below the calculated 1,086-series bound.
2. Operators can answer all eight validator questions in under one minute; question-specific alerts, runbooks, reconciliation tests, and fault injection pass.
3. Every recorded span error has native error status, and log records have empirically verified native trace correlation.
4. One two-node proposal-to-vote-to-commit trace crosses the real GossipSub path without affecting signed/hashed data, and arbitrary peers cannot force sampling.
5. Collector adoption, if chosen, has a named owner, operations SLO, retention/cost model, bounded queues, and failure-game evidence.
6. Fleet consumers complete their independently scheduled ion migration or supported maintenance backport without a protocol flag day.
