# PR bodies — #261 and #262

Paste-ready. Both correct stale or false wording in the current bodies.

Apply with:

```bash
gh pr edit 261 --body-file <(sed -n '/^<!-- 261 -->$/,/^<!-- end 261 -->$/p' docs/telemetry/PR-BODIES.md | sed '1d;$d')
gh pr edit 262 --body-file <(sed -n '/^<!-- 262 -->$/,/^<!-- end 262 -->$/p' docs/telemetry/PR-BODIES.md | sed '1d;$d')
```

---

<!-- 261 -->
## What

A telemetry convention gate in a separate `.golangci-telemetry.yml`, run in two modes:

| Where | Mode | Blocking |
|---|---|---|
| `ci.yml` | diff-only (`--new-from-rev`) | **Yes — blocking** |
| `nightly.yml` `static` job | full tree | No — informational, tracks burn-down |

**`depguard`** denies `rs/zerolog` and `go.uber.org/zap` imports.
**`forbidigo`** flags the `fmt.Print*` and `fmt.Fprint*` families **and the zerolog call
surface** (`^log\.(Debug|Info|Warn|Error|Fatal)$`), with `analyze-types` so a local
variable named `fmt` is not falsely flagged.

## Why

Over the five weeks to `ab4f5013` every telemetry surface shrank 8–13% **except zerolog**,
which grew 224 → 239 calls and 34 → 36 files — in new consensus-path code. CI has run on
`v3base` for a while, but `.golangci.yml` has no rule that would notice.

## How

Kept out of `.golangci.yml` deliberately: adding these there would fail the existing
full-tree Mode A `Lint` step on contact (~570 print sites, 36 zerolog files). The main
config and that step are untouched.

**Both the import and the call surface are gated.** depguard alone is not sufficient — it
flags the import line, and `--new-from-rev` only reports changed lines, so calls added to a
file that *already* imports zerolog change no import line. Measured on this repo's history:
**#260 added 2 zerolog calls (239 → 241) with zero new zerolog files** — depguard alone
would have passed it. The forbidigo call pattern closes that; all 241 zerolog calls here are
package-level `log.X()`, so it reaches every one. It does not touch the 174 stdlib
`log.Print/Printf/Println/Fatalf` calls (different method names).

Exemptions are per-call-site, not per-path. `main.go` holds two programs — `runCommand`,
where printing is correct, and `main()`, where it is a logging defect — so no directory
allowlist is defensible. `nolintlint` already requires a named linter and a reason, so each
exemption reads `//nolint:forbidigo // CLI output: <reason>`, and the surviving annotations
**are** the output manifest (`make telemetry-manifest`).

`checkout` gains `fetch-depth: 0` — `--new-from-rev` resolves the PR base SHA, which a
shallow clone does not contain. Without it the step would skip silently and enforce nothing.

## Testing

golangci-lint 2.15.0 / go1.27.2, macOS:

| Check | Result |
|---|---|
| Config valid, rules bite | **872 issues — depguard 37, forbidigo 835** |
| Diff-only vs `origin/v3base` | 0 issues — blocks nobody today |
| Acceptance (import path) | depguard flags all four files that gained zerolog since `efa45013` |
| **F1 — call surface** | **forbidigo 610 → 835 after adding the call pattern; depguard unchanged at 37** |
| No regression | `fmt-check`, `make lint` (0 issues), `build`, `verify-pins` all pass |
| Baseline | reproducible, all 14 metrics match, sentinel fires |

Review round 2 raised six findings; all six are fixed in this branch and written up in
`docs/telemetry/VALIDATION.md`, including two verification attempts that proved nothing and
why.

## Checklist

- [x] `make build` passes
- [x] `make lint` passes
- [x] `make fmt-check` passes
- [x] `make verify-pins` passes
- [x] Commit messages follow conventional commits
- [x] No secrets or credentials in the diff

## Note for reviewers

**This gate blocks.** A new zerolog/zap import, a new zerolog call, or a new
`fmt.Print*`/`Fprint*` fails CI. There is no advisory window — there was no technical reason
to let regressions keep landing while people got used to it.

**Existing violations are untouched.** `--new-from-rev` only inspects changed lines, so the
~835 already in the tree are invisible to this gate; they go away with the migration, not
with the linter.

**Escape hatch**, enforced by `nolintlint` to be specific and justified:

```go
//nolint:forbidigo // CLI output: prints the balance table to the operator's terminal
```

**On the first run, check the step *ran*, not just that it passed.** If it prints
`no diff base available`, `fetch-depth: 0` is not taking effect. A base that exists but is
missing from the clone now fails the build rather than passing silently — a gate that cannot
resolve its diff base is enforcing nothing and should say so.

This PR is the safe first exercise: it changes **zero `.go` files**, so the gate must report
0 issues.
<!-- end 261 -->

---

<!-- 262 -->
## What

The v3 telemetry architecture: two independent designs produced adversarially against each
other over ten review rounds plus one targeted round, their convergence record, a rebase onto
the current `v3base`, and an A–Z handover for an incoming developer.

Entry point: `docs/telemetry/README.md` → `opus/HANDOVER.md`

Follows the existing `docs/audit/` precedent — ten handover and root-cause documents already
live on `v3base`.

## Why

Telemetry was never designed; it accreted across four logging libraries, 32 metrics nothing
scrapes, and 146 spans that never set an error status. The 2026-09-17 halt went undiagnosed
for its full hour because the quantity that failed was uninstrumented, and the three metrics
an operator *was* watching read healthy throughout — they measured the set the transport
layer maintains, not the set the consensus algorithm draws from.

## How

Design is **frozen**. No implementation code in this PR.

**Scope:** v3 = structural convergence — one telemetry runtime, one schema, all 32 metric
declarations migrated, package-init `promauto` removed, production logging on injected
loggers. v3.1 = domain coverage and cross-process tracing. **55–85 engineer-days + 6–10
SRE**, provisional until two spikes (`gETH/`, `FastsyncV2/`) measure it.

Evidence re-baselined onto the current `v3base` (113 commits past the frozen baseline). All
twelve load-bearing claims hold; line citations refreshed; one proof re-homed to
`BLS_Signer/Signer.go:118-123` after #196 deleted its file.

Three findings from the parallel review's targeted round are applied: the OTel Prometheus
exporter is an **unchecked collector**, so registration alone cannot carry the boot-fatal
guarantee; an empty fleet node alias must **refuse boot** rather than warn; and the external
ion-interface canary is **8 `Logger` / 1 `Span` / 1 `Tracer`**.

## Testing

Docs only — no code, config or workflow changes. Every factual claim carries `file:line`;
anything not executed is marked with the command that settles it.

## Checklist

- [x] `make build` passes
- [x] `make lint` passes
- [x] `make fmt-check` passes
- [x] Commit messages follow conventional commits
- [x] No secrets or credentials in the diff

## Note for reviewers

Both architectures are kept deliberately. They converged on the same end state; the
disagreements and how each was settled are the most useful part of the record, and
`DECISION-RECORD.md` exists to stop settled decisions being reopened by preference.

The one open item is organisational, not technical: production observability ownership is
the operator as POC, which is enough for devnet and staging but not for production
promotion. `README.md` records it as **satisfied for v3, open for production**, with one
design consequence called out — **missing-telemetry detection must be external to the
pipeline it watches**, or it cannot fire when that pipeline is what failed.
<!-- end 262 -->
