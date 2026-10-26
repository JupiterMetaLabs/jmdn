# JMDN v3 Telemetry — Handover

**For:** an incoming developer and their AI coding agent
**Date:** 2026-10-26 · **Repo state this describes:** `jmdn` @ `origin/v3base` = `ab4f5013` (PR #258, 2026-10-23)
**Status:** architecture **frozen** after ten adversarial review rounds · **no implementation code written yet**

Read this file top to bottom once. It is the only document you need to start; everything else is
referenced from here with a reason to open it.

---

## A. What this is, in one paragraph

jmdn's telemetry was never designed — it accreted over three years across four logging libraries, 32
Prometheus metrics that nothing scrapes, and 146 spans that never record an error status. On
**2026-09-17 the chain halted for over an hour and went undiagnosed for its full duration**, because
the quantity that failed was not instrumented and the three metrics an operator *was* watching read
healthy throughout. This programme designs the telemetry architecture for v3 and migrates to it. The
design is finished and frozen. **Your job is to build it.**

**Operator goal for v3, stated by the operator:** *unify logging and metrics, pristine clean
structure.* Not "add more metrics" — that is v3.1.

---

## B. The incident that defines "done"

Reputation decayed 29 peers to selection weights 0.30–0.43. The eligibility band (score ≥ 0.5)
filtered every one of them out. The candidate set hit zero, `ErrNoPeersAvailable` returned, consensus
stopped.

The peers were **connected and heartbeating normally the whole time**. So
`p2p_connected_peers_total`, `p2p_managed_peers_total` and `p2p_active_peers_total` all read healthy
and flat for the full hour. An operator on the p2p dashboard would have concluded the network was
fine. They were measuring the set the *transport layer* maintains, not the set the *consensus
algorithm draws from*.

Three quantities on the failure path, all already in memory, none recorded:

| Quantity | Where it is today | What it would have shown |
|---|---|---|
| candidate-set size | `AVC/NodeSelection/pkg/selection/vrf.go:76,201` — `eligible, _ := FilterEligibleOrActive(...)` | 29 → 0 |
| fail-safe engagement | the discarded `_` on those same two lines | fires **before** liveness is lost |
| round outcome | `Sequencer/Consensus.go:1695` — computed, then thrown into a 20-slot in-memory ring (`internal/reputation/push_guard.go`) | `rate(success[5m]) == 0` |

**The release gate is a test, not a document:** install 29 peers at weights 0.30–0.43, assert the
candidate set reaches zero or the fail-safe engages, the counter increments, and the alert pages
within two minutes — while the dashboard simultaneously shows healthy p2p counts, because the
misdiagnosis is part of what is being tested against.

**This is the single most important paragraph in this handover.** Everything else is in service of it.

---

## C. The three documents, and which to read when

| File | When |
|---|---|
| **`HANDOVER.md`** (this) | now, once |
| **`JMDN-V3-TELEMETRY-ARCHITECTURE.md`** (~1,250 lines, frozen, normative) | before writing any code in the area you are touching. §-numbered; §11 is the migration plan |
| **`REBASE-PLAN.md`** | before trusting any `file:line` in the architecture — it records what moved in the 113 commits since the freeze |
| **`STEP-1-CI-GATE-PATCH.md`** | if you are doing the first task (§G) |
| `DECISION-RECORD.md` | only when tempted to reopen a decision. It records what was argued, who conceded, and on what evidence |

There is a **parallel review** at `../sol/` — a second independent architecture produced adversarially
against this one. The two converged; they describe the same end state. Read it if you want the
argument from the other side, not as a competing spec.

---

## D. The decisions you inherit — do not relitigate these

Ten rounds produced these. Each was contested and settled on evidence.

| # | Decision |
|---|---|
| 1 | **Keep ion** (`github.com/JupiterMetaLabs/ion`) as a thin facade over the OTel SDK. Every ion type is an alias of, or thin wrapper over, an OTel type. ion adds ergonomics and schema, never transport |
| 2 | **One private Prometheus registry, one `/metrics`, direct scrape.** No OTel Collector in v3 — it is correct target state but unowned infrastructure |
| 3 | **v3 = structural convergence. v3.1 = coverage.** All 32 metrics migrate and package-init `promauto` is removed in v3; new domain metrics (sync, mempool, EVM, RPC) are v3.1 |
| 4 | **Constructor injection. No global accessor.** `internal/telemetry.Runtime` built once in `main`; narrow dependencies injected. A global with a state machine is still a global |
| 5 | **Capability-separated injection.** Domains get a **sealed** emission-only `EventLogger`, a separate `ion.Tracer` only where used, and a typed metric set. Never `ion.Logger` — it exposes `Shutdown`/`SetLevel`, so any service could kill process-wide export |
| 6 | **Three signal types, three cardinality budgets.** `ion.Field` for logs (unbounded OK) · `ion.Attr` for spans (medium, sampled) · `ion.Label` for metrics (bounded enum, mandatory `other`). Names unify; **types deliberately do not** |
| 7 | **Registration errors are boot-fatal; exporter outages degrade.** *Telemetry may refuse to start a node; it may never stop a running one* |
| 8 | **No exported ion interface gains a method during v0.x.** Optional capability → new one-method interface. Root-only capability → concrete method on `*Ion` |
| 9 | **Cross-process tracing is v3.1**, and the carrier is `PubSubMessages.Message`, not the outer envelope |
| 10 | **Nothing in the telemetry path may be read back by consensus, selection or reputation logic.** Observation only, one direction — reputation feeding selection is how 2026-09-17 happened |

**Amendment rule:** if executable evidence falsifies a frozen assumption, amend that section with the
failing probe and its impact. Do not reopen by preference.

---

## E. Current repo state — what is true today

Verified at `ab4f5013` on 2026-10-26. **The working tree may be older — check.**

### Still broken (this is the work)

- `SetStatus` on spans: **0 calls** against 287 `RecordError`. Every error span is unmarked
- `Ion.Meter()`: **0 calls**. ion's metrics pipeline is fully wired and completely dark
- `ports.metrics` defaults to **0** — the `/metrics` endpoint never binds. Not in the Dockerfile `EXPOSE`, not published in compose
- Tracing is **double-gated**: `logging/otelsetup/setup.go` assigns `cfg.Tracing` *inside* `if OTEL.Enabled && endpoint != ""`, so `tracing.enabled: true` alone is silently ignored. `cfg.Metrics` is never populated at all
- ion's startup warnings are **discarded** at the single site that could observe them
- `logging/` is mostly dead: `LoggingInterface` (7 methods, never implemented, never referenced), a half-dead builder with a latent nil, an inert 47-constant topic map, and **51 `NewAsyncLogger()` construction sites across 45 files** (was 62/58 at the freeze — the dead-code sweep shrank it)
- Log level defaults to `warn`, leaving ~57% of ion calls dark; **zero** `SetLevel` call sites
- `node.alias` defaults to `""` and `service_name` to `"jmdn"` — **a default fleet publishes identical identity on every node**

### Migration surface

| | Count |
|---|---:|
| `logger()` accessor reads | **1,376** (940 in methods / 505 in free functions at the freeze — re-derive from an AST, not a regex) |
| of which call `.Tracer(` | 94 |
| `fmt.Print*` | 570 |
| zerolog fluent calls | **239 — and growing** |
| stdlib `log` | ~514 |
| `context.Background()` in log calls | 433 |
| distinct log field keys | ~430 |

### Already fixed by the team, independently

- **CI runs on `v3base`** (`ci.yml:5,7`) — the precondition for every enforcement mechanism
- **12 dead DB-pool metrics deleted**; `metrics/DBMetrics.go` (455 lines, zero callers) gone. Declarations 43 → 32
- **`peer_id` cardinality bounded** (PR #223) — LRU of 256 at `metrics/labels.go:19` with series deletion on eviction. Not our design (we said remove the label) but it defuses the 140,000-series hazard. **Accept it for v3**; revisit churn in v3.1
- `jmdn_handler_panics_recovered_total{where}` added with an explicit bounded-label comment — our §8.1 rule, arrived at independently

### The one thing moving backwards

**zerolog grew from 224 → 239 calls and 34 → 36 files** while every other surface shrank 8–13%. The
file I flagged last cycle was deleted, so the true addition is nearer **+40 calls**, in
`internal/saferun/saferun.go`, `internal/sequencerid/sequencerid.go`,
`messaging/committee_anchor.go`, `messaging/entropy_recovery.go`. CI has a live surface now;
`.golangci.yml` still has no `depguard`. **That is task one.**

---

## F. How to work in this repo

Surveyed at `ab4f5013`. Treat `CLAUDE.md` as **partly stale** — see F.6.

### F.1 Build

```bash
make build          # CGO_ENABLED=1 go build -o jmdn .   — CGO is REQUIRED (go-ethereum)
make verify-pins    # THE PRE-PUSH GATE. Rejects any `replace` in go.mod
```

Go floor **1.27.0**, toolchain `1.27.3`. No vendor dir.

**Private modules.** `avc` and `ThebeDB` are private; `ion`, `JMDN-FastSync`, `JMDN_Merkletree`,
`goroutine-orchestrator` are public. Two paths:

```bash
./Scripts/dev-setup-modules.sh          # sets scoped GOPRIVATE + git ssh rewrite. --check to verify
make local-replace && make build        # offline: points the four at ../ siblings
make local-replace-undo                 # MUST run before pushing — verify-pins rejects replaces
```

### F.2 Test

```bash
make test-unit      # go test -short ./...   — the fast gate; skips integration
make test           # go test ./...          — needs live ThebeDB + seed node
```

Three independent gates separate integration tests: `testing.Short()`, build tags
(`applygate`, `sequencertest`, `pgtest`), and env vars (`JMDN_TEST_PG_DSN`, `SEED_NODE_URL`,
`APPLYGATE_DEPLOY_BYTECODE`). **Only `nightly.yml` passes build tags** — tag-gated files are invisible
to the PR gate, and PR #250 broke `-tags applygate` without CI noticing. If you touch anything
tag-gated, run it locally.

No compose file provides test dependencies, and CI provisions no services — so **every CI test run is
effectively `-short`.**

### F.3 CI — four workflows

| Workflow | Triggers | Blocking |
|---|---|---|
| `ci.yml` **Quality Gate** | push/PR on `main`, **`v3base`**, `release/**` | **every step.** `go mod verify` → tidy check → `go build ./...` → `golangci-lint fmt --diff` → `golangci-lint run` (full tree). **No tests run in the PR gate** |
| `nightly.yml` | 18:00 UTC cron + push to `v3base` | `vet` (5 tag matrix), `test` (9 legs incl. `-race`, `-shuffle`), `static` lint — blocking. Scanners (`staticcheck`, `deadcode`, `govulncheck`, `gosec`, gitleaks) — advisory |
| `docker.yml` | `v*` tags | build + push multi-arch to GHCR |
| `sonarqube.yml` | — | **disabled** (`if: false`) |

golangci-lint pinned **`v2.15.0`**. Fork PRs fail at the private-module step by design (no secrets).

### F.4 Lint

`.golangci.yml` enables only `govet`, `ineffassign`, `unused`, `nolintlint`. `staticcheck`, `errcheck`
(229 violations), `gosec` (192) are staged but commented. **`run.tests: false`** — test files are not
linted by the PR gate.

`nolintlint` requires **a specific linter and an explanation** on every `//nolint`. This is
load-bearing for our design (§G).

### F.5 Branch and PR

```bash
git checkout -b feat/telemetry-<thing>    # prefixes in use: feat/ fix/ chore/ ci/ docs/ audit/
make fmt-check && make lint && make build && make verify-pins
```

Conventional Commits. PR template checklist: build, lint, fmt-check, conventional commits, no secrets
— **it omits `verify-pins`, run it anyway.** At least one maintainer approval.

> ⚠️ **`CONTRIBUTING.md` says target `main`. The actual programme line is `v3base`** — every recent
> merge is a squash onto `v3base` with a PR number, and `nightly.yml` schedules exclusively against
> it. Neither `CONTRIBUTING.md` nor `RELEASING.md` mentions `v3base`. **Confirm the target branch with
> a maintainer before your first PR.**
>
> ⚠️ `CONTRIBUTING.md:47` lists SonarQube as a required check while `sonarqube.yml:15` is `if: false`.
> If GitHub branch protection still requires it, every PR is blocked. **Check before you need it.**

### F.6 `CLAUDE.md` is stale — verify before trusting

Confirmed wrong at this ref:

- Claims flags `-seed` and `-connect` exist. **They do not.** Real flags are at `main.go:771-792`
- Architecture section omits `consensus/`, `consensushash/`, `Vote/`, `blockgossip/`, `txstatus/`, `execbridge/`, `pkg/gatekeeper/`, `seed/`, `explorer/` — several directly on the consensus path
- Omits `test-unit`, `fmt-check`, `lint-new`, `local-replace`, and **`verify-pins`**
- No mention of `v3base`, the module-pin freeze, or `Scripts/dev-setup-modules.sh`

Module path is **`gossipnode`**, not `jmdn` — imports read `gossipnode/...`.

### F.7 Devnet

`../jmdt-devnet` (separate repo). `./scripts/devnet_up.sh [NODE_COUNT]` — one command, bootstraps its
own `.env`. Requires **nine sibling repos checked out alongside**, all on `v3base`. Images build from
your *working trees*, not from go.mod tags, so what you run is what you have checked out. No registry
credentials needed; you do need clone access to private `avc` and `ThebeDB`.

Its README has two stale bits: it names `make dev-setup`/`dev-check` (don't exist — use
`Scripts/dev-setup-modules.sh`) and lists older module pins than `go.mod` carries.

---

## G. Your first task

**Land the telemetry CI gate.** Full patch set with diffs, validation and rollout:
**`STEP-1-CI-GATE-PATCH.md`**.

Why first: it is the only intervention whose cost compounds while it waits, it addresses the only
metric moving backwards, and it depends on nothing — not the rebase, not the architecture, not the
unresolved ownership question. Roughly **half a day**.

Shape: a separate `.golangci-telemetry.yml` (so the existing full-tree gate stays green), run
**diff-only and blocking in `ci.yml`** to stop new debt, and **full-tree and advisory in
`nightly.yml`** to track the backlog burning down.

**The elegant part, and the thing to understand before you touch it:** `nolintlint` already requires a
specific linter and an explanation on every `//nolint`. So each exemption must read
`//nolint:forbidigo // CLI output: <reason>`. **Those annotations *are* the function-scoped output
manifest** that the architecture made a 5–7 day deliverable — accrued at the point of change, each
one justified and reviewed, extracted by `make telemetry-manifest`. You do not build the manifest up
front; you make it impossible not to build it.

Acceptance test is in §6 of that document: replay the four files that gained zerolog since the
freeze and assert `depguard` flags each.

---

## H. Then what — the workstreams

| | Workstream | Days | Notes |
|---|---|---:|---|
| **W1** | Incident gate | 8–12 + SRE | The §B detector. Needs a **deployed** Prometheus — owner resolved, see §I |
| **W2** | Telemetry kernel | 16–23 | `internal/telemetry.Runtime`, capability-separated injection, `schema` package. **Includes the 505 free-function reads with no constructor to inject into** |
| **W3** | Historical metric convergence | 6–9 | 32 declarations dispositioned, package-init `promauto` removed. ~30% smaller than scoped — the team already deleted 12 |
| **W4** | Logging structure + call sites | 21–29 | The four high-volume keys, the output manifest, stdlib/zerolog migration, 163 context bugs |
| **W5** | Enforcement | 4–5 | The analyzer harness and nine rules. §G is the cheap first slice |
| **W6** | Clean-room release gate | 4–7 | Temporary forwarder deleted, adapters deleted, `logging/` deleted |

**55–85 engineer-days + 6–10 SRE.** Sequencing: W1 must *ship and be proven* before anything promotes
to production — but that is a **promotion gate, not a work-stop**. Once W1's metric contract and
descriptor golden are frozen, spikes and non-conflicting W2 work proceed in parallel.

Structure precedes volume: W2 before W4. Migrating call sites into the current structure would land
~2,490 of them on the thing we are removing.

**Two spikes replace the estimate ranges with measured numbers.** Run them early: `gETH/` (the
free-function injection tail — `convertEthTxToConfigTx` alone has 18 `logger()` calls in a pure
conversion function) and `FastsyncV2/` (113 stdlib `log` calls in 2 files, bulk throughput).

---

## I. Observability ownership — resolved for v3, open for production

**Resolved 2026-10-26: the operator acts as POC, with a second agent.** This unblocks W1 — including
step 3, "deployed Prometheus with a named owner" — for devnet and staging. The programme proceeds.

**It is not yet an operating model for production promotion.** The architecture's own acceptance
criteria call for retention, HA and missing-telemetry detection, which a single POC cannot cover.
Two things follow, and the second is a design constraint rather than a staffing note:

1. Scale of the existing gap: `grafana/provisioning/datasources/datasource.yml` points at `prometheus:9090` and `loki:3100`, and **neither host is defined in any compose file in either repo** — nor has been for the life of that directory. W1 step 3 means standing those up for real, not referencing them.

2. **Missing-telemetry detection must be external to the pipeline it watches.** An alert that depends on the Prometheus it monitors cannot fire when that Prometheus is the thing that failed. Design it as an outside-in heartbeat or scrape check from day one — retrofitting it later is how you end up with a monitoring stack that reports healthy because it is down. That is the 2026-09-17 shape exactly: absence of signal read as health.

**Status: satisfied for v3 delivery, open for production promotion.**

---

## J. How to work on this (and how to brief your agent)

This programme was produced under a specific discipline, and it caught real errors ten times. Keep it.

1. **Every claim carries `file:line`.** If you cannot cite it, write `NOT VERIFIED` and give the command that settles it.
2. **Never promote an unverified item to a premise.** The one real failure in ten rounds: a round-1 claim ("no consumer implements `ion.Logger`") was flagged `NOT VERIFIED` *in our own survey*, never checked, and became a load-bearing assumption. Eight rounds later it turned out to be false and to break the compatibility contract. **Absent evidence is not evidence of absence** — which is the same defect as the metric that read healthy through the halt.
3. **Re-verify before trusting any citation in the architecture.** It is pinned to `efa45013`; 113 commits have landed. `REBASE-PLAN.md` §3 records what moved. Line numbers drifted; one file was deleted and its proof re-homed to `BLS_Signer/Signer.go:118-123`.
4. **Prefer `grep`/`git` over building.** The repo is ~1,400 packages; never run a repo-wide build to answer a question.
5. **If you delegate to a subagent, re-verify its load-bearing findings yourself at `file:line`.** Relaying a subagent's finding on its authority has produced wrong conclusions on this codebase.
6. **Quantify.** No "many" or "several" without a number.
7. **Mark confidence.** Verified facts stated plainly; `Likely: <claim> (basis)`; `Assumed: <claim>. If wrong: <what changes>`.

A useful framing for the whole programme, learned the hard way: **the errors that survive adversarial
review are the ones both parties assume identically.** The single worst defect found in ten rounds —
both documents suppressing `target_info` while separately putting identity on the OTel resource — was
not found by either side arguing. It was found by reading both documents against an external contract.
When you and your agent agree, that is when to go and check.

---

## K. Quick reference

```bash
# state
git -C jmdn log -1 --oneline origin/v3base      # ab4f5013 at time of writing
git -C jmdn status --short                      # working tree may be behind

# the incident path — confirm it is still unfixed before building the detector
git grep -n "eligible, _ := FilterEligibleOrActive" origin/v3base -- 'AVC/NodeSelection/pkg/selection/vrf.go'
git grep -n "DefaultRoundHealth.Record"           origin/v3base -- 'Sequencer/Consensus.go'

# the four headline gaps
git grep -c "\.SetStatus(" origin/v3base -- '*.go'     # expect 0
git grep -c "\.Meter("     origin/v3base -- '*.go'     # expect 0
git grep -n "Metrics: *0"  origin/v3base -- 'config/settings/defaults.go'
git grep -n 'Alias: *""'   origin/v3base -- 'config/settings/defaults.go'

# the surface
git grep -h "rs/zerolog" origin/v3base -- '*.go' | wc -l
git grep -h -E '\blogger\(\)' origin/v3base -- '*.go' | wc -l

# pre-push
make fmt-check && make lint && make build && make verify-pins
```

**Questions this handover cannot answer** — ask a maintainer: does this programme PR into `v3base` or
`main`; is the disabled SonarQube check still required by branch protection; and **who owns production
observability**.
