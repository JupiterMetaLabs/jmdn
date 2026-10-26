# Validation gate — `ci/telemetry-conventions-gate`

**Status: ALL CHECKS PASS. Two defects found and fixed. Merge-ready pending operator approval.**
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
| Telemetry step is in `quality-gate`, is last, `continue-on-error: true` | ✅ |
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
| **2026-11-09** | Flip `continue-on-error: false` in `ci.yml`. The date is in the file |

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
