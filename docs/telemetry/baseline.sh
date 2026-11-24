#!/usr/bin/env bash
# Telemetry convention baseline — reproducible counts the gate holds the line on.
#
# Run before and after any telemetry change. Static only: git + grep, no build,
# no toolchain, no network. Safe to run on any ref.
#
#   ./docs/telemetry/baseline.sh                 # current working tree
#   ./docs/telemetry/baseline.sh origin/v3base   # a specific ref
#
# Counting rules are fixed here so numbers stay comparable across runs. They
# intentionally include _test.go: the gate sets tests:true, and new test code is
# held to the same conventions.
set -euo pipefail

REF="${1:-}"
if [ -n "$REF" ]; then
  SHA="$(git rev-parse --short "$REF")"
  g() { git grep -h -E "$1" "$REF" -- '*.go' 2>/dev/null | wc -l | tr -d ' '; }
  gl() { git grep -l -E "$1" "$REF" -- '*.go' 2>/dev/null | wc -l | tr -d ' '; }
else
  SHA="$(git rev-parse --short HEAD)(worktree)"
  g() { git grep -h -E "$1" -- '*.go' 2>/dev/null | wc -l | tr -d ' '; }
  gl() { git grep -l -E "$1" -- '*.go' 2>/dev/null | wc -l | tr -d ' '; }
fi

printf '# telemetry baseline\nref: %s\ndate: %s\n\n' "$SHA" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%-42s %8s\n' "metric" "count"
printf '%-42s %8s\n' "------------------------------------------" "--------"

# ── What the CI gate blocks ──────────────────────────────────────────────────
printf '%-42s %8s\n' "zerolog calls"                "$(g 'log\.(Debug|Info|Warn|Error|Fatal)\(\)')"
printf '%-42s %8s\n' "zerolog files"                "$(gl 'rs/zerolog')"
printf '%-42s %8s\n' "zap files (direct)"           "$(gl 'go\.uber\.org/zap')"
printf '%-42s %8s\n' "fmt.Print*"                   "$(g 'fmt\.Print(f|ln)?\(')"
printf '%-42s %8s\n' "fmt.Fprint*"                  "$(g 'fmt\.Fprint(f|ln)?\(')"
printf '%-42s %8s\n' "ctx.Background() in log calls" "$(g '\.(Debug|Info|Warn|Error|Critical)\(context\.Background\(\)')"

# ── Migration surface (not gated; tracks W2/W4) ──────────────────────────────
printf '%-42s %8s\n' "logger() accessor reads"      "$(g '(^|[^A-Za-z0-9_])logger\(\)')"
printf '%-42s %8s\n' "logger().Tracer( reads"       "$(g 'logger\(\)\.Tracer\(')"
printf '%-42s %8s\n' "NewAsyncLogger() sites"       "$(g 'NewAsyncLogger\(\)')"
printf '%-42s %8s\n' "ion-importing files"          "$(gl 'JupiterMetaLabs/ion')"

# ── Frozen-claim tripwires (these must stay at 0 until the work starts) ──────
printf '%-42s %8s\n' "span SetStatus calls"         "$(g '\.SetStatus\(')"
printf '%-42s %8s\n' "ion .Meter() calls"           "$(g '\.Meter\(')"
printf '%-42s %8s\n' "RecordError calls"            "$(g '\.RecordError\(')"

# ── Exemptions accrued (the output manifest) ────────────────────────────────
printf '%-42s %8s\n' "//nolint:forbidigo exemptions" "$(g 'nolint:forbidigo')"

# ── Self-check ───────────────────────────────────────────────────────────────
# A pattern that silently matches nothing reports 0, which is indistinguishable
# from real progress. `package ` must appear in every Go file, so a zero here
# means the regex engine rejected the pattern rather than the count being right.
# This caught a real defect: \b is a GNU extension, undefined in POSIX ERE, so
# the original logger() pattern reported 0 on macOS and 1376 on Linux.
SENTINEL="$(g '^package ')"
if [ "$SENTINEL" -lt 100 ]; then
  printf '\nFATAL: sentinel pattern matched %s files (expected >100).\n' "$SENTINEL" >&2
  printf 'The regex engine is rejecting patterns — these counts are NOT trustworthy.\n' >&2
  exit 2
fi
printf '\n(sentinel: %s files matched "^package " — regex engine OK)\n' "$SENTINEL"
