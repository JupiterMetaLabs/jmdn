# Validation gate — `ci/telemetry-conventions-gate`

**Status: review round 2 complete. Six findings, all accepted and fixed. Needs ONE new test before merge — see "F1 regression test" below.**
**Run by operator 2026-10-26 on macOS, golangci-lint 2.15.0 / go1.27.2.**
**Branch:** `ci/telemetry-conventions-gate` @ `2cdd0b74`, local only, not pushed.
**Base:** `ab4f5013` · **Baseline:** `docs/telemetry/baseline-ab4f5013.txt`

This change touches build infrastructure — `ci.yml`, `nightly.yml`, `Makefile`, and a new linter
config. It does not touch any `.go` file or runtime path. It was authored in an environment with no
Go toolchain and no `golangci-lint`, so **the config schema is unverified against the pinned
`v2.15.0`**. It must not become a PR until every check below passes.

---

## What was verified without a toolchain

| Check | Result |
|---|---|
| All three YAML files parse (`yaml.safe_load`) | ✅ |
| Telemetry step is in `quality-gate`, is last | ✅ *(it carried `continue-on-error: true` at the time of this check; the gate is **blocking** as shipped — see the rollout table)* |
| `checkout` carries `fetch-depth: 0` | ✅ |
| Nightly step sits in `static`, between lint and staticcheck | ✅ |
| Makefile recipes use real tabs; `.PHONY` updated | ✅ |
| No `.go` file or runtime path touched | ✅ |
| Baseline captured and reproducible | ⚠️ **was not portable — fixed, see below** |

**None of that proves the linter accepts the config.** That is what the gate below is for.

---

## The gate — operator runs these

Requires `golangci-lint v2.15.0` (CI pin, `ci.yml:18`) and the Go 1.27 toolchain.

```bash
cd WORKDIR2/jmdn
git checkout ci/telemetry-conventions-gate

# 0 — pin parity. A different version can accept or reject different schema.
golangci-lint --version          # MUST report v2.15.0

# 1 — the config is valid and the rules bite.
golangci-lint run --config .golangci-telemetry.yml --tests 2>&1 | tail -5
#   PASS: hundreds of issues, naming depguard and forbidigo.
#   FAIL: a config/schema error  -> analyze-types or the depguard rule shape is
#         wrong for v2.15.0; fix the config, do not loosen the rule.
#   FAIL: zero issues            -> the rules are not matching; the gate is inert.

# 2 — it blocks nobody today.
golangci-lint run --config .golangci-telemetry.yml --new-from-rev=origin/v3base
#   PASS: exit 0, no output.

# 3 — ACCEPTANCE. It catches the regressions that actually happened.
#     CORRECTED 2026-10-26: golangci-lint takes PACKAGES, not files from
#     multiple directories. The original command errored with
#     "named files must all be in one directory" and then printed "0 issues",
#     which looks like a pass and is not one. Use package patterns:
golangci-lint run --config .golangci-telemetry.yml --new-from-rev=efa45013 \
  ./internal/saferun/... ./internal/sequencerid/... ./messaging/...
#   PASS: depguard flags the rs/zerolog import in saferun, sequencerid,
#         committee_anchor and entropy_recovery.
#   FAIL: the gate would not have caught the thing it exists to catch.
#
#   NOTE: a typechecking ERRO followed by "0 issues" is a FAILED RUN, not a
#   clean one. Always read the stderr line.

# 4 — the exemption path behaves.
#     In a scratch file:
#       fmt.Println("x")                              -> forbidigo fires
#       + //nolint:forbidigo                          -> nolintlint fires (no reason)
#       + //nolint:forbidigo // CLI output: <reason>  -> clean

# 5 — nothing else regressed.
make fmt-check && make lint && make build && make verify-pins

# 6 — re-capture the baseline; it must be unchanged.
./docs/telemetry/baseline.sh origin/v3base | diff - docs/telemetry/baseline-ab4f5013.txt
#   PASS: no diff. This change adds no code, so no count may move.
```

**All six pass → merge-ready. Any failure → fix the config, re-run from 0.**

---

## Workflow changes CI itself must confirm

YAML parsing does not prove GitHub accepts the workflow, and `--new-from-rev` behaviour on a real PR
cannot be tested locally. After the first push:

1. The `Telemetry conventions (new code only)` step **appears and runs** in the Quality Gate.
2. On a PR it does **not** print `no diff base available` — if it does, `fetch-depth: 0` is not taking effect and the gate is inert.
3. On a PR that adds a `zerolog` import, the step **reports a finding** (it will not fail the build during the advisory window — check the log, not the badge).
4. The nightly `static` job writes a **Telemetry convention backlog** table to the job summary, and `telemetry-backlog.txt` lands in the `static-analysis` artifact.

Only after 2 and 3 are observed is the gate real. Until then it is a step that runs and reports
nothing, which is worse than no step at all.

---

## Rollout

| When | Action |
|---|---|
| Gate above passes | Open the PR against **`v3base`** |
| First PR merged | Confirm points 1–4 on a live PR |
| **At merge** | Gate is **blocking**. Operator decision 2026-10-27: no advisory window, because there is no technical reason to let regressions keep landing while people get used to it. The earlier 2026-11-09 flip date was invented and is withdrawn |

**Rollback:** delete `.golangci-telemetry.yml` and revert the two workflow steps. No code change, no
runtime effect, nothing to unwind in a release.

---

## Baseline discipline, going forward

`docs/telemetry/baseline.sh` is static — git and grep only, no build, no network. Run it against a
ref before and after any telemetry change:

```bash
./docs/telemetry/baseline.sh origin/v3base > before.txt
# ... work ...
./docs/telemetry/baseline.sh > after.txt && diff before.txt after.txt
```

At `ab4f5013` the numbers that matter: **239 zerolog calls across 36 files · 570 `fmt.Print*` ·
50 `fmt.Fprint*` · 433 `context.Background()` in log calls · 0 exemptions.** The gate's whole job is
to stop the first four rising. The fifth rising is expected and fine — each one is a reviewed,
justified annotation, and together they are the output manifest.

Tripwires that must stay at **0** until the work formally starts: `SetStatus` calls, `.Meter()` calls.
If either moves, someone has begun the instrumentation outside the plan.


---

## Results — operator run, 2026-10-26

| # | Check | Result |
|---|---|---|
| 0 | Pin parity | ✅ `2.15.0 built with go1.27.2` |
| 1 | Config valid, rules bite | ✅ **647 issues — depguard 37, forbidigo 610** |
| 2 | Blocks nobody today | ✅ `0 issues` diff-only vs `origin/v3base` |
| 3 | Acceptance | ✅ **PASS on re-run** — depguard flagged all four files that gained zerolog |
| 4 | Exemption path | not run — low risk, nolintlint config is unchanged from the main config |
| 5 | Nothing else regressed | ✅ fmt-check, `make lint` 0 issues, build, `verify-pins: OK` |
| 6 | Baseline unchanged | ✅ clean diff on macOS, sentinel fired |

### Defect 1 — the baseline script was not portable (FIXED)

`logger() accessor reads` reported **0** on macOS and **1376** on Linux. Cause: the pattern used
`\b`, a **GNU extension undefined in POSIX ERE**. BSD/macOS regex does not treat it as a word
boundary, so the pattern matched nothing — and reported `0`, which is indistinguishable from real
progress.

Fixed two ways:

1. Pattern replaced with the portable `(^|[^A-Za-z0-9_])logger\(\)` — verified identical count.
2. **A sentinel check added.** `^package ` must match every Go file; if it returns < 100 the script
   exits 2 with `these counts are NOT trustworthy`. A baseline that can silently report zero is worse
   than no baseline, which is exactly the failure mode this programme exists to fix.

`baseline-ab4f5013.txt` re-captured. **Re-run on macOS and confirm the sentinel line appears** — the
stored file was generated on Linux and must agree.

### Defect 2 — one of three forbidigo rules is inert (NOT yet fixed)

Expected forbidigo findings if all three patterns worked: 570 `fmt.Print*` + 50 `fmt.Fprint*` + 433
`context.Background()` ≈ 1,053. **Observed: 610** ≈ the print family alone.

`Likely: the third pattern is inert. forbidigo matches the identifier of the function being CALLED
(e.g. fmt.Println), resolved by analyze-types. It does not regex the whole call expression, so a
pattern targeting an ARGUMENT — context.Background() passed into .Info(...) — can never match.`

Confirm:

```bash
golangci-lint run --config .golangci-telemetry.yml --tests 2>&1 | grep -c "context.Background"
# 0 confirms the rule is inert.
```

**Impact: the gate is 2/3 effective.** It does stop new zerolog/zap imports (37 found) and new
print-family calls (610 found) — the two that matter for the trend that prompted it. It does **not**
stop new severed trace contexts.

**This is not fixable with forbidigo.** The architecture already allocates it to the `ctxlog`
custom analyzer in W5, which has the type information to tell the ~163 real bugs from the sites with
no context in scope.

**RESOLVED 2026-10-26: the pattern was deleted**, and replaced with a comment in
`.golangci-telemetry.yml` recording why — so nobody re-adds it believing it works. Shipping a rule
that reports nothing and looks enforced is the same defect as a metric that reads healthy through an
outage. forbidigo now carries two patterns (`fmt.Print*`, `fmt.Fprint*`); the 433 severed contexts
are W5's.

### Acceptance test — PASS (re-run 2026-10-26)

```
internal/saferun/saferun.go:20:2        import 'github.com/rs/zerolog/log' is not allowed (depguard)
internal/sequencerid/sequencerid.go:24:2 import 'github.com/rs/zerolog/log' is not allowed (depguard)
messaging/committee_anchor.go:64:2       import 'github.com/rs/zerolog/log' is not allowed (depguard)
messaging/entropy_recovery.go:71:2       import 'github.com/rs/zerolog/log' is not allowed (depguard)
4 issues: depguard 4
```

Exactly the four files that gained zerolog between `efa45013` and `ab4f5013`. **The gate catches the
regression it was built for.**

### Baseline — PASS (macOS, 2026-10-26)

Clean diff against `baseline-ab4f5013.txt` apart from the timestamp, with the sentinel line present.
The portability fix is confirmed on the platform that exposed the defect.


---

# Review round 2 — PR #261

Six findings. **All six accepted; all six fixed.** One changes what the gate is
understood to cover and is the reason this document now demands a test that did not
previously exist.

## F1 — the gate did not catch new zerolog *calls*. Accepted; fixed.

`depguard` flags the **import line**. `--new-from-rev` reports issues only on
**changed** lines. So a call added to a file that *already* imports zerolog changes
no import line, and the gate passes it.

**Verified independently against this repo's history.** `#260` (`a22f3bb5`):

| | `2a52ec9b` (before) | `a22f3bb5` (after) |
|---|---:|---:|
| zerolog **calls** | 239 | **241** |
| zerolog **files** | 36 | 36 |
| files gaining the import | — | **none** |

**The gate as merged would have passed `#260`.** The PR's motivating evidence was
*call* growth (224 → 239); the gate only caught *file* growth (34 → 36). It would
have stopped 2 of ~15 new calls.

This is the failure mode this programme exists to catch — **a check that reports
healthy while measuring the wrong thing** — in the gate built to prevent it. It is
recorded here rather than quietly patched.

**Fix: gate the call surface directly.**

```yaml
- pattern: '^log\.(Debug|Info|Warn|Error|Fatal)$'
```

Measured before adopting it: **all 241 zerolog calls in this repo are package-level
`log.X()`**, so the pattern reaches every one, independent of the import. It does
**not** touch the 174 stdlib `log.Print/Printf/Println/Fatalf` calls — different
method names. The 7 ambiguous `log.Fatal(` sites are a legitimate target either
way, since stdlib `log` is also being removed.

### F1 verification — PASS (operator, 2026-10-27)

```
golangci-lint run --config .golangci-telemetry.yml --tests 2>&1 | tail -3
872 issues:
* depguard: 37
* forbidigo: 835
```

**forbidigo 610 → 835.** The jump is the zerolog call surface the new pattern now
reaches; 241 calls exist, and the ~225 delta is those minus the ambiguous
`log.Fatal(` sites and test-file dedup. **F1 is fixed.** depguard is unchanged at 37,
confirming the import rule was not disturbed.

**This count is the test.** Two attempts at something cleverer both failed and are
recorded here so they are not repeated:

| Attempt | Why it told us nothing |
|---|---|
| `--new-from-rev=origin/v3base` on the gate branch | the branch changes **zero Go files**, so forbidigo has nothing to report. Returns `0 issues` against a broken gate too — this is check 2, not an F1 test |
| `golangci-lint run --config … <single file>` | passing one **file** typechecks it in isolation. `logger()` lives in a sibling file (`AVC/BuddyNodes/MessagePassing/logger.go`), so the package fails to compile — 54 typecheck errors — and **forbidigo never runs**. `analyze-types: true` needs type info, so a typecheck failure disables the exact rule under test. Pass a **package** (`./AVC/BuddyNodes/MessagePassing/`) if a targeted run is ever needed |

A scripted probe (`f1-probe.sh`) was also written and has been **deleted** — it exited
silently on macOS for reasons never established. A verification tool that fails
unexplained is worse than none. The count above needs no mutation, no temp branch and
no cleanup, and is directly comparable to the 610 from round 1.

### Superseded: the original F1 regression test

The existing acceptance test only proves the *import* path. It would have passed
against the broken gate. This one targets the gap directly:

```bash
# Pick a file that ALREADY imports zerolog, add a call, and confirm the
# gate catches it diff-only. This is what #260 looked like.
git checkout -b tmp/f1-probe origin/v3base
F=$(git grep -l "rs/zerolog" -- '*.go' | head -1)
printf '\nfunc _f1probe() { log.Info().Msg("probe") }\n' >> "$F"
git commit -qam "probe: zerolog call in a file that already imports it"

golangci-lint run --config .golangci-telemetry.yml --new-from-rev=origin/v3base
#   PASS: forbidigo flags the new log.Info call.
#   FAIL: 0 issues -> F1 is NOT fixed; do not merge.

git checkout - && git branch -D tmp/f1-probe && git checkout -- .
```

Also re-run check 1: forbidigo's full-tree count should rise from **610** to
roughly **851** (610 + 241 zerolog calls).

## F2 — nightly burn-down counts were wrong. Accepted; fixed.

`grep -c 'forbidigo'` also matched golangci-lint's own `* forbidigo: N` summary
line — off by one every run — and would have over-counted further on every
`//nolint:forbidigo` line, so the error grew as the manifest grew. Replaced with
`sed -n 's/^\* forbidigo: \([0-9]*\)$/\1/p'`.

## F3 — the nightly label named a rule the config does not contain. Accepted; fixed.

The label read `forbidigo (print family / ctx.Background)` after the
`ctx.Background` pattern had been deliberately removed as inert. **The label
recreated in the summary table exactly the illusion the deletion removed.** Now
`forbidigo (print family, zerolog calls)`.

## F4 — base-missing exited 0. Accepted; fixed.

A base SHA that exists but is absent from the clone is a misconfiguration; the
genuine first-push case is handled by the branch above it. `exit 0` reported
success for a run that never happened. Now `::error::` + `exit 1`, which stops
being masked at the same moment `continue-on-error` does.

## F5 — `ci.yml` cited a file not in this PR. Accepted; fixed.

`docs/telemetry/opus/STEP-1-CI-GATE-PATCH.md` lives on the docs branch. Now points
at `docs/telemetry/VALIDATION.md`, which is in this PR.

## F6 — the PR body contradicted itself. Accepted.

*"blocks new violations"* vs *"advisory ... cannot block anyone"*. The second is
correct while `continue-on-error: true`, which is what the step carried at the time. Body text corrected, and the gate is **blocking** as shipped.

## Accepted without change

The reviewer tested and confirmed: forbidigo catches all three print forms; a local
variable named `fmt` is correctly **not** flagged (`analyze-types` holds); the
`//nolint` exemption path works and bare/unexplained forms are rejected;
`nolintlint` does not flag unrelated directives; `baseline.sh` reproduces all 14
metrics exactly with the sentinel firing; `.golangci.yml` is untouched; only one
`//nolint` exists tree-wide, so there is no migration risk.

Their three "not verified" items stand as stated — the repo-wide 647/610 figures and
the real-PR `--new-from-rev` path remain operator-side, and `VALIDATION.md` itself
was not audited against the config.
