# jmdn v3 Telemetry — document index

**Repo state these describe:** `jmdn` @ `ab4f5013` (PR #258, 2026-10-23) · **Index updated:** 2026-10-26

Two independent architectures were produced adversarially against each other over ten review rounds
plus one targeted round. They converged and describe the same end state. **Both are kept** — the
disagreements and their resolutions are the most useful part of the record.

**Start at [`opus/HANDOVER.md`](opus/HANDOVER.md).** It is the only file you need to read first.

> ### ⚠ What "v3" means in these documents
>
> **"v3" here is the line of work, not the `v3.0.0` tag.** These documents were written
> before the release plan settled, and they use "v3" for the structural telemetry increment
> and "v3.1" for the coverage increment.
>
> **`v3.0.0` is expected to ship before any of this lands.** Telemetry rolls out in a later
> `v3.x.x`. So when a document says "in v3", read it as **"in the first telemetry
> increment"** — it is a scope boundary between the two increments, not a claim about which
> tag contains what.
>
> Concretely: `v3.0.0` will ship with the telemetry described in §"Current state" — one
> private registry, metrics unreachable by default, no span error status — and **that is
> expected**, not a regression against these documents.
>
> The one thing that does land early is the **CI convention gate**, because its whole job is
> to stop the codebase accruing more telemetry debt while the rest waits.

---

## Status of every file

| File | Status | Read it when |
|---|---|---|
| **`opus/HANDOVER.md`** | **CURRENT** — the entry point | First. A–Z onboarding for a developer and their agent |
| **`opus/JMDN-V3-TELEMETRY-ARCHITECTURE.md`** | **CURRENT, FROZEN** (normative) | Before writing code in an area it covers. §-numbered; §11 is the migration plan |
| **`sol/TELEMETRY-ARCHITECTURE-V3.md`** | **CURRENT, FROZEN** (normative, parallel) | For the second independent treatment. Converged with opus; not a competing spec |
| **`opus/REBASE-PLAN.md`** | **CURRENT** | Before trusting any `file:line` in the architecture — records what moved across the 113 commits since the freeze |
| **`opus/STEP-1-CI-GATE-PATCH.md`** | **CURRENT** — now implemented on `ci/telemetry-conventions-gate` | The design behind the gate |
| **`VALIDATION.md`** | **CURRENT** — the gate's test record | Before merging the gate, or when adding a rule to it |
| **`baseline.sh` + `baseline-<sha>.txt`** | **CURRENT** — run before and after any telemetry change | Every time. Static, no toolchain needed |
| **`sol/BASELINE-EVIDENCE-AND-HANDOFF.md`** | **CURRENT** — sol's evidence ledger + full round-by-round record | Checking how a decision was reached from sol's side |
| **`opus/DECISION-RECORD.md`** | **CURRENT** — opus's convergence history | When tempted to reopen a settled decision |

**Nothing here is obsolete.** Superseded *content* was folded into its successor as it went; six
opus iteration files were consolidated into `DECISION-RECORD.md`, and the 2026-10-13 patch draft was
replaced in place by the rebased version. What remains is the minimum set.

### Which document wins on a conflict

They agree on architecture. Where wording differs, the **narrower, more fail-fast reading wins** —
that is how every one of the ten rounds resolved. Three live corrections flow from sol's targeted
round 6 and are already applied to the opus normative document:

1. The OTel Prometheus exporter is an **unchecked collector** — registration does *not* catch a duplicate family. The boot-fatal guarantee needs a translated-schema preflight in an isolated registry.
2. An empty fleet node alias **refuses boot**; it is not a warn-and-continue.
3. The external ion-interface canary is **8 `Logger`, 1 `Span`, 1 `Tracer`** — not 0 tracers.

---

## State of the work

| | |
|---|---|
| **Design** | Frozen. No open architecture disputes between the two reviews |
| **Implementation** | **None started** |
| **Scope** | v3 = structural convergence (one runtime, one schema, all 32 metrics migrated, package-init `promauto` removed, production logging on injected loggers). v3.1 = domain coverage + cross-process tracing |
| **Effort** | 55–85 engineer-days + 6–10 SRE, provisional until two spikes (`gETH/`, `FastsyncV2/`) measure it |
| **First task** | `opus/STEP-1-CI-GATE-PATCH.md` — ~half a day, blocks nothing, depends on nothing |
| **Observability owner** | **Operator, as POC**, with a second agent. Enough to unblock W1 for devnet and staging. See the risk below before production promotion |
| **CI gate** | Merged and live, advisory. Design: `opus/STEP-1-CI-GATE-PATCH.md`. Test record: `VALIDATION.md` |

### Questions — all three answered 2026-10-26

| Question | Answer |
|---|---|
| PR into `v3base` or `main`? | **`v3base`.** `CONTRIBUTING.md` says `main` and is stale — every recent merge is a squash onto `v3base`, and `nightly.yml` schedules exclusively against it |
| Is the disabled SonarQube check still *required* by branch protection? | **No.** Safe to proceed |
| Who owns production observability? | **The operator, as POC**, with a second agent |

#### Observability ownership — satisfied for v3, open for production

This unblocks W1 for devnet and staging, and the programme proceeds on it.

It is **not** an operating model for production promotion, and the architecture's own acceptance
criteria say as much: retention, HA and *missing-telemetry detection* need coverage a single person
cannot give. One consequence is concrete and must be designed in from the start —
**missing-telemetry detection has to be external to the pipeline it watches.** An alert that depends
on the Prometheus it monitors cannot fire when that Prometheus is the thing that failed. That is the
2026-09-17 shape exactly: absence of signal read as health.

Treat as **satisfied for v3 delivery, open for production promotion.**

---

## Amendment rule

These documents are frozen, not finished. If executable evidence falsifies a claim, **amend the
affected section with the failing probe and its impact**. Do not reopen a settled decision by
preference, and do not silently edit a number — the value of this record is that every claim is
traceable to the evidence that produced it.

Every factual claim carries `file:line`. Anything that could not be executed is marked
`Untested: execution unavailable` with the command that settles it. Keep that discipline: it caught
real errors in both documents, repeatedly.
