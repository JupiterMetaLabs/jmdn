# Step 1 — the telemetry CI gate

> ## ⚠ IMPLEMENTED AND SUPERSEDED — this is the original proposal, kept as a record
>
> The gate shipped in **#261** and is **BLOCKING**, not advisory. The operator removed the
> advisory window on 2026-10-27: there was no technical reason to let regressions keep landing
> while people got used to it. The `2026-11-09` flip date in the diffs below was invented by
> me and is withdrawn.
>
> The gate also now covers the **zerolog call surface**, not just imports — review finding F1,
> which the diffs below predate.
>
> **Do not follow the diffs in this document.** For what is actually deployed, read
> `.github/workflows/ci.yml` and `docs/telemetry/VALIDATION.md`. This file is retained because
> the reasoning — why a separate config, why per-call-site exemptions rather than a path
> allowlist — is still the rationale for the design that shipped.

**Rebased 2026-10-26 onto `ab4f5013`.** Supersedes the 2026-10-13 draft, which was written against `efa45013` and is stale in three ways: Patch 1 has shipped, the linter pin moved `v2.10.1` → **`v2.15.0`**, and a **`nightly.yml`** now exists that is a better home for the full-tree scan than anything the original proposed.

**Prerequisite:** none. **Blocks:** nothing. **Stops:** the one telemetry metric moving backwards (zerolog 224 → 239 calls, 34 → 36 files, against a surface that shrank 8–13% everywhere else).

**Untested: execution unavailable** — no Go toolchain or `golangci-lint` here. Validation in §6.

---

## 1. What changed since the last draft

| | Then (`efa45013`) | Now (`ab4f5013`) |
|---|---|---|
| CI on `v3base` | **missing — Patch 1** | **shipped.** `ci.yml:5,7` include `v3base` |
| golangci-lint | `v2.10.1` | **`v2.15.0`** (`ci.yml:18`) |
| Nightly workflow | did not exist | **`nightly.yml`** — `static` job runs `golangci-lint run --tests --build-tags "${ALL_TAGS}" --timeout 15m` (`:228`), with `staticcheck` and `deadcode` alongside it as `continue-on-error` |
| `fetch-depth` on quality-gate checkout | absent | **still absent** (`ci.yml:27-29`) — Patch 3b stands |
| Acceptance target | `messaging/timeout_request.go` | **deleted** by PR #196 — re-pointed in §6 |
| Go floor | 1.26 | **1.27.0** / toolchain `1.27.3` |

**Patch 1 is done. Patches 2, 3, 3b, 4 remain, and Patch 3 is now better than it was.**

---

## 2. Design

**Two surfaces, because the repo now has two.**

| Surface | Mode | Blocking? | Purpose |
|---|---|---|---|
| **PR gate** (`ci.yml`) | diff-only, `--new-from-rev` | yes, after a 2-week advisory window | **stops new debt** |
| **Nightly** (`nightly.yml` `static` job) | full tree | no — `continue-on-error`, matching the `staticcheck`/`deadcode` convention already there | **tracks the backlog burning down** |

This is §10.3's "baseline, then burn down" using infrastructure that already exists. The new linters
stay in a **separate config** (`.golangci-telemetry.yml`) so the existing full-tree `Lint` step
(`ci.yml:118`, Mode A) is untouched — adding `depguard`/`forbidigo` to `.golangci.yml` would fail it
on 570 `fmt.Print*` sites and 36 zerolog files on contact, which is why no one has added a linter yet.

### The allowlist builds itself

`.golangci.yml` already sets `nolintlint` with `require-explanation: true` and `require-specific: true`,
so every exemption must read:

```go
//nolint:forbidigo // CLI output: runCommand prints the balance table to the operator's terminal
fmt.Printf("balance: %s\n", bal)
```

Bare and blanket forms are rejected. **Those annotations are the function-scoped manifest** that §2.6
made a 5–7 day v3 deliverable — accrued at the point of change, each justified, extracted by
`make telemetry-manifest`. This matters because our own finding killed the directory-level allowlist:
`main.go` is two programs (`runCommand` vs `main()`), so no file-scoped exemption is defensible.

Keep the up-front manifest only for the ~71 library-code violations that must migrate in v3; the CLI
side accrues annotations instead.

---

## 3. Patch 2 — `.golangci-telemetry.yml` (new file)

```yaml
version: "2"

# Telemetry convention gate. Two callers:
#   - ci.yml  : diff-only (--new-from-rev) — blocks NEW debt
#   - nightly : full tree, advisory — tracks the backlog
# Kept out of .golangci.yml so the Mode A full-tree gate stays green.
# Rationale: docs/telemetry/opus/
run:
  timeout: 5m
  tests: true            # deliberately differs from .golangci.yml (tests: false)
  modules-download-mode: readonly
  relative-path-mode: gomod

linters:
  default: none
  enable:
    - depguard
    - forbidigo
    - nolintlint         # required: it is what makes every exemption justified

  settings:
    depguard:
      rules:
        logging:
          deny:
            - pkg: "github.com/rs/zerolog"
              desc: "use the injected telemetry logger; zerolog is being removed in v3 (ADR-3)"
            - pkg: "go.uber.org/zap"
              desc: "use the injected telemetry logger; ion wraps zap internally"

        # Enable when internal/telemetry/ exists (W2). Until then it would fire
        # on all 119 ion-importing files.
        # telemetry-capability:
        #   files:
        #     - "!**/internal/telemetry/**"
        #     - "!**/main.go"
        #   deny:
        #     - pkg: "github.com/JupiterMetaLabs/ion"
        #       desc: "domain code receives an injected EventLogger/Tracer/metric set (ADR-1 §6.2b)"

    forbidigo:
      analyze-types: true
      forbid:
        - pattern: '^fmt\.Print(f|ln)?$'
          msg: "terminal output needs an explicit exemption: //nolint:forbidigo // CLI output: <reason>. Library code uses the injected logger."
        - pattern: '^fmt\.Fprint(f|ln)?$'
          msg: "stderr/report writing needs an explicit exemption; buffer and hash writers are legitimate — say which."
        - pattern: '\.(Debug|Info|Warn|Error|Critical)\(context\.Background\(\)'
          msg: "pass the request context — context.Background() severs trace correlation (433 existing sites)."

    nolintlint:
      require-explanation: true
      require-specific: true
      allow-no-explanation: []

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
```

---

## 4. Patch 3 — PR gate step, and Patch 3b — the checkout fix

### 3b is required, not optional

`--new-from-rev` cannot resolve a base SHA on a shallow clone, and the quality-gate checkout is
shallow: `ci.yml:27-29` is a bare `actions/checkout@v4` with no `with:` block. Only the *nightly*
and *security* jobs set `fetch-depth`. **Without this the new step hits its skip branch on every run
and silently does nothing** — the decorative-gate failure mode in §8.

```diff
--- a/.github/workflows/ci.yml
+++ b/.github/workflows/ci.yml
@@ -26,6 +26,10 @@ jobs:
     steps:
       - name: Checkout
         uses: actions/checkout@v4
+        with:
+          # Required by the telemetry gate's --new-from-rev: a shallow clone
+          # cannot resolve the PR base SHA, and the step would skip silently.
+          fetch-depth: 0
```

### Patch 3 — the gate step, after `Lint` (`ci.yml:118`)

```diff
@@ -120,6 +124,26 @@ jobs:
           # MODE A: uncomment the line below and comment out MODE B to enforce the full codebase.
           golangci-lint run
 
+      # ── Telemetry conventions (diff-only) ──────────────────────────────────
+      # Separate config + separate step, so the 570 fmt.Print* / 36 zerolog
+      # files in the backlog never block this, and this never destabilises
+      # MODE A above. Full-tree tracking lives in nightly.yml's `static` job.
+      # ADVISORY until 2026-11-09, then flip continue-on-error to false.
+      # See docs/telemetry/opus/STEP-1-CI-GATE-PATCH.md
+      - name: Telemetry conventions (new code only)
+        continue-on-error: true          # ← flip to false on 2026-11-09
+        run: |
+          BASE="${{ github.event.pull_request.base.sha }}"
+          if [ -z "$BASE" ]; then BASE="${{ github.event.before }}"; fi
+          if [ -z "$BASE" ] || [ "$BASE" = "0000000000000000000000000000000000000000" ]; then
+            echo "::notice::no diff base (first push on branch); skipping"
+            exit 0
+          fi
+          golangci-lint run \
+            --config .golangci-telemetry.yml \
+            --new-from-rev "$BASE"
```

### Patch 3c — nightly full-tree tracking (new)

In `nightly.yml`'s `static` job, after the existing `golangci-lint run` at `:228`. Matches the
`continue-on-error` convention `staticcheck` and `deadcode` already use there.

```diff
@@ (nightly.yml, job `static`, after the golangci-lint step)
+      # Telemetry convention backlog — informational, tracks burn-down.
+      # The PR gate (ci.yml) blocks NEW violations; this counts what remains.
+      - name: Telemetry conventions (full tree, informational)
+        continue-on-error: true
+        run: |
+          golangci-lint run --config .golangci-telemetry.yml \
+            --tests --build-tags "${{ env.ALL_TAGS }}" --timeout 15m \
+            --out-format colored-line-number 2>&1 | tee telemetry-backlog.txt
+          echo "### Telemetry backlog" >> "$GITHUB_STEP_SUMMARY"
+          grep -cE "forbidigo|depguard" telemetry-backlog.txt >> "$GITHUB_STEP_SUMMARY" || true
```

Add `telemetry-backlog.txt` to the job's existing `static-analysis` artifact upload.

---

## 5. Patch 4 — Makefile

```diff
--- a/Makefile
+++ b/Makefile
@@ (after lint-new)
+# Telemetry conventions, diff vs the v3base merge point (mirrors the PR gate).
+telemetry-lint:
+	golangci-lint run --config .golangci-telemetry.yml \
+	  --new-from-rev=$$(git merge-base HEAD origin/v3base)
+
+# Full-tree backlog count (mirrors nightly).
+telemetry-backlog:
+	@golangci-lint run --config .golangci-telemetry.yml --tests 2>&1 | tail -1
+
+# Extract the output allowlist manifest from the nolint annotations.
+telemetry-manifest:
+	@grep -rn "nolint:forbidigo" --include='*.go' . \
+	  | sed 's/:.*nolint:forbidigo *\/\/ */ → /' | sort
```

Add `telemetry-lint` to `.PHONY`.

---

## 6. Validation

```bash
cd WORKDIR2/jmdn

# 0. Pin parity — the local binary must match CI (v2.15.0).
golangci-lint --version

# 1. Config parses; full tree is NOT clean (proves the rules bite).
golangci-lint run --config .golangci-telemetry.yml --tests 2>&1 | tail -3
#    expect: hundreds — ~570 fmt.Print*, 36 zerolog files.

# 2. Diff-only against the tip is clean (proves it blocks nobody today).
golangci-lint run --config .golangci-telemetry.yml --new-from-rev=origin/v3base
#    expect: exit 0.

# 3. ACCEPTANCE — it catches real regressions. Re-pointed: the original target
#    (messaging/timeout_request.go) was deleted with the timeout-certificate
#    flow in PR #196. These four files gained zerolog in the same window:
golangci-lint run --config .golangci-telemetry.yml --new-from-rev=efa45013 \
  -- internal/saferun/saferun.go internal/sequencerid/sequencerid.go \
     messaging/committee_anchor.go messaging/entropy_recovery.go
#    expect: depguard flags the rs/zerolog import in each.

# 4. The exemption path works and is enforced.
#    fmt.Println("x")                                   → forbidigo fires
#    + //nolint:forbidigo                               → nolintlint fires (no reason)
#    + //nolint:forbidigo // CLI output: <reason>       → clean
```

Check 3 is the acceptance test: it replays the four regressions that actually happened since the
freeze, in code that is still present.

---

## 7. Rollout

| When | Action |
|---|---|
| Now | Patches 2, 3, 3b, 3c, 4. PR gate **advisory** |
| **2026-11-09** | Flip `continue-on-error: false`. **Put this date in the PR description** |
| W2 lands | Uncomment `telemetry-capability` in depguard |
| W5 | Replace forbidigo's `context.Background()` pattern with the `ctxlog` analyzer — a regex cannot separate the 163 real bugs from the sites with no ctx in scope |

**Rollback is deleting one file and two CI steps.** No code change, no runtime effect.

---

## 8. What this does not do

- **Fixes nothing existing.** 570 prints, 36 zerolog files, 433 severed contexts, 1,376 accessor reads all remain. This stops the bleeding; W4 does the surgery.
- **Does not replace the up-front manifest for library code.** The ~71 library-code `fmt.Print*` violations, including the committee-auth denials in `messaging/consensus_hardening.go`, still need the §9.4 dual-emit cutover. Annotation is not an acceptable outcome for those.
- **Needs no observability owner, no rebase, no design decision.** That is the whole argument for doing it first.

## 9. Risks

- `Assumed: golangci-lint v2.15.0 keeps the v2 schema used above (`analyze-types`, depguard `files` negation, `--out-format`). The pin moved two minor versions since the last draft. Settle with check 1 before writing the CI steps.`
- `Assumed: nightly.yml's static job tolerates one more continue-on-error step. Its header already states scanners stay advisory "because the vulnerability database and rule sets change daily" — same contract.`
- **The advisory window is the real risk.** A gate left `continue-on-error: true` indefinitely is decoration — the exact failure mode this programme documents. The flip date belongs in the PR description, not in someone's memory.
- `NOT VERIFIED: whether GitHub branch protection lists the now-disabled "SonarQube / SonarQube Analysis" as a required check. CONTRIBUTING.md:47 still names it while sonarqube.yml:15 is `if: false`. If it is still required, every PR is already blocked and that must be fixed before any of this lands.`
