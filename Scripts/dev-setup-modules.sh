#!/usr/bin/env bash
#
# dev-setup-modules.sh — module access setup for JupiterMetaLabs developers.
#
# WHAT IT DOES (three independent things, each idempotent)
#
#   1. GOPRIVATE — jmdn depends on two PRIVATE modules,
#      github.com/JupiterMetaLabs/avc and github.com/JupiterMetaLabs/ThebeDB.
#      proxy.golang.org cannot serve them (it 404s, then falls back to an
#      unauthenticated `git ls-remote`, which GitHub refuses). GOPRIVATE tells
#      Go to bypass the proxy AND the checksum DB for those two paths only.
#      Scoped deliberately: GOPRIVATE sets both GONOPROXY and GONOSUMDB, so a
#      wildcard like github.com/JupiterMetaLabs/* would also switch off
#      sum.golang.org verification for the PUBLIC modules (ion,
#      JMDN_Merkletree, JMDN-FastSync). We keep those verified.
#      Existing GOPRIVATE entries for other orgs are MERGED, never clobbered.
#
#   2. git URL rewrite — Go always asks git for https://github.com/... , but
#      your SSH key is what grants access. This maps that prefix to SSH.
#      NOTE: this is a --global git setting and applies to every
#      JupiterMetaLabs https URL on this machine, including jmdn's own origin.
#      If you currently authenticate to GitHub over https with a token, that
#      path stops being used for this org. `--undo` reverses it.
#
#   3. (optional, --workspace) a local Go workspace so you can build against
#      sibling checkouts of avc / ThebeDB / JMDN-FastSync without editing
#      go.mod. Written as `.go.work.local` INSIDE this repo and used only via
#      an explicit GOWORK= export.
#
#      Why not a plain `go.work` in the parent directory: Go auto-discovers a
#      file named exactly `go.work` by walking UP from your cwd. Placing one in
#      the parent of this repo would put every sibling repo under it, and any
#      sibling not listed in the `use` block then fails with
#        "directory prefix . does not contain modules listed in go.work"
#      That would break jmdn-dev, jmdn-prod, jmdn-thebe, JMDN-Mempool,
#      Mempool-Routing-Engine, seedNodes and the rest. Worse, several jmdn
#      clones all declare `module gossipnode`, and a workspace cannot list the
#      same module twice ("module gossipnode appears multiple times in
#      workspace"). Naming the file `.go.work.local` means Go NEVER discovers
#      it automatically — it applies only when you export GOWORK yourself.
#
# USAGE
#   ./Scripts/dev-setup-modules.sh              configure, then verify
#   ./Scripts/dev-setup-modules.sh --check      verify only, change nothing
#   ./Scripts/dev-setup-modules.sh --workspace  also write .go.work.local
#   ./Scripts/dev-setup-modules.sh --build      include a full `go build ./...`
#                                               (slow: jmdn pulls go-ethereum,
#                                               libp2p and duckdb — GBs, minutes)
#   ./Scripts/dev-setup-modules.sh --undo       remove what this script set
#
# Safe to re-run. Never modifies go.mod or go.sum.

set -euo pipefail

PRIVATE_PATHS=("github.com/JupiterMetaLabs/avc" "github.com/JupiterMetaLabs/ThebeDB")
ORG_HTTPS="https://github.com/JupiterMetaLabs/"
ORG_SSH="git@github.com:JupiterMetaLabs/"
SIBLINGS=(avc ThebeDB JMDN-FastSync)   # local-replace / workspace members
WORKFILE_NAME=".go.work.local"

BLUE='\033[0;34m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info() { echo -e "${BLUE}[INFO]${NC}  $*"; }
ok()   { echo -e "${GREEN}[ OK ]${NC}  $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()  { echo -e "${RED}[FAIL]${NC}  $*"; }

usage() { sed -n '/^# USAGE/,/^# Safe to re-run/p' "$0" | sed 's/^#\s\{0,1\}//'; }

MODE="setup"; MAKE_WORKSPACE=0; DO_BUILD=0
for arg in "${@:-}"; do
  case "$arg" in
    ""|--) ;;
    --check)     MODE="check" ;;
    --undo)      MODE="undo" ;;
    --workspace) MAKE_WORKSPACE=1 ;;
    --build)     DO_BUILD=1 ;;
    -h|--help)   usage; exit 0 ;;
    *)           err "unknown argument: $arg"; echo; usage; exit 2 ;;
  esac
done

if [[ "$MODE" == "check" && "$MAKE_WORKSPACE" -eq 1 ]]; then
  err "--check and --workspace conflict: --check changes nothing, --workspace writes a file"
  exit 2
fi

command -v go  >/dev/null || { err "go is not on PATH"; exit 1; }
command -v git >/dev/null || { err "git is not on PATH"; exit 1; }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_NAME="$(basename "$REPO_ROOT")"
PARENT_DIR="$(dirname "$REPO_ROOT")"
WORKFILE="${REPO_ROOT}/${WORKFILE_NAME}"

[[ -f "${REPO_ROOT}/go.mod" ]] || { err "no go.mod at ${REPO_ROOT}"; exit 1; }

echo
info "repo:   ${REPO_ROOT}  (module $(awk '/^module /{print $2; exit}' "${REPO_ROOT}/go.mod"))"
info "parent: ${PARENT_DIR}"
info "go:     $(go version)"
echo

# ------------------------------------------------------------------ undo ----
if [[ "$MODE" == "undo" ]]; then
  cur="$(go env GOPRIVATE)"
  keep=""
  IFS=',' read -ra entries <<< "$cur"
  for e in "${entries[@]:-}"; do
    [[ -z "$e" ]] && continue
    drop=0
    for p in "${PRIVATE_PATHS[@]}"; do [[ "$e" == "$p" ]] && drop=1; done
    [[ "$drop" -eq 0 ]] && keep="${keep:+$keep,}$e"
  done
  if [[ -n "$keep" ]]; then
    go env -w "GOPRIVATE=${keep}"
    ok "GOPRIVATE: removed our two paths, kept: ${keep}"
  else
    go env -u GOPRIVATE 2>/dev/null || true
    ok "GOPRIVATE unset (nothing else was in it)"
  fi
  git config --global --unset "url.${ORG_SSH}.insteadOf" 2>/dev/null || true
  ok "removed the ${ORG_HTTPS} -> ${ORG_SSH} rewrite"
  [[ -f "$WORKFILE" ]] && { rm -f "$WORKFILE"; ok "removed ${WORKFILE_NAME}"; }
  info "go.mod / go.sum were never touched."
  exit 0
fi

# ------------------------------------------------------------- configure ----
if [[ "$MODE" == "setup" ]]; then
  # --- GOPRIVATE, merged not clobbered -------------------------------------
  cur="$(go env GOPRIVATE)"
  merged="$cur"
  added=()
  for p in "${PRIVATE_PATHS[@]}"; do
    if [[ ",${merged}," != *",${p},"* ]]; then
      merged="${merged:+$merged,}$p"
      added+=("$p")
    fi
  done
  if [[ ${#added[@]} -gt 0 ]]; then
    [[ -n "$cur" ]] && info "existing GOPRIVATE preserved: ${cur}"
    go env -w "GOPRIVATE=${merged}"
    ok "GOPRIVATE += ${added[*]}"
  else
    ok "GOPRIVATE already covers both private modules"
  fi

  # --- git https -> ssh rewrite -------------------------------------------
  existing="$(git config --global --get "url.${ORG_SSH}.insteadOf" 2>/dev/null || true)"
  if [[ "$existing" != "$ORG_HTTPS" ]]; then
    git config --global "url.${ORG_SSH}.insteadOf" "$ORG_HTTPS"
    ok "git rewrite ${ORG_HTTPS} -> ${ORG_SSH} (global; --undo reverses)"
  else
    ok "git URL rewrite already configured"
  fi
  echo
fi

# ------------------------------------------------------------- workspace ----
if [[ "$MAKE_WORKSPACE" -eq 1 ]]; then
  missing=()
  for m in "${SIBLINGS[@]}"; do
    [[ -f "${PARENT_DIR}/${m}/go.mod" ]] || missing+=("$m")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    err "missing sibling checkouts next to ${REPO_NAME}: ${missing[*]}"
    err "clone them into ${PARENT_DIR} first"
    exit 1
  fi
  go_directive="$(awk '/^go /{print $2; exit}' "${REPO_ROOT}/go.mod")"
  {
    echo "// Generated by Scripts/dev-setup-modules.sh — local development only."
    echo "// NOT auto-discovered by Go (the filename is not \"go.work\"). Activate with:"
    echo "//   export GOWORK=${WORKFILE}"
    echo "go ${go_directive}"
    echo
    echo "use ("
    echo "	."                      # this checkout, whatever directory it is named
    for m in "${SIBLINGS[@]}"; do echo "	../${m}"; done
    echo ")"
  } > "$WORKFILE"
  ok "wrote ${WORKFILE_NAME} (go ${go_directive}; . + ${SIBLINGS[*]})"

  if ! grep -qxF "${WORKFILE_NAME}" "${REPO_ROOT}/.gitignore" 2>/dev/null; then
    warn "${WORKFILE_NAME} is not in .gitignore — add it so it is never committed"
  fi
  echo
  info "Activate it in your shell (per shell, or add to your profile):"
  echo "    export GOWORK=${WORKFILE}"
  warn "With GOWORK set, builds use the sibling checkouts. Before you push, ALWAYS run"
  warn "    GOWORK=off go build ./..."
  warn "That is the only thing that proves the pinned versions in go.mod resolve."
  echo
fi

# ---------------------------------------------------------------- verify ----
FAILED=0
HAS_LOCAL_REPLACE=0
grep -qE '^[[:space:]]*replace[[:space:]]+\S+[[:space:]]+=>[[:space:]]+\.{1,2}/' "${REPO_ROOT}/go.mod" \
  && HAS_LOCAL_REPLACE=1

info "1/4  SSH reachability of the private repos"
for repo in "${SIBLINGS[@]}"; do
  is_private=0
  for p in "${PRIVATE_PATHS[@]}"; do [[ "$p" == *"/${repo}" ]] && is_private=1; done
  [[ "$is_private" -eq 1 ]] || continue
  if GIT_TERMINAL_PROMPT=0 git ls-remote "${ORG_HTTPS}${repo}" >/dev/null 2>&1; then
    ok "  ${repo}: reachable"
  else
    err "  ${repo}: NOT reachable"
    err "     try:  ssh -T git@github.com      (should greet you by username)"
    err "     then: confirm your account has read access to JupiterMetaLabs/${repo}"
    FAILED=1
  fi
done
echo

info "2/4  Go environment"
for v in GOPRIVATE GONOPROXY GONOSUMDB GOTOOLCHAIN GOWORK GOFLAGS; do
  printf '     %-12s = %s\n' "$v" "$(go env "$v")"
done
echo

info "3/4  Fetching the pinned private modules at their exact versions"
# This MUST run outside REPO_ROOT. A `replace` directive for a module path
# short-circuits even an explicit `go list -m path@version` query — it returns
# success without touching the network, which makes an in-repo check a false
# positive whenever the replaces are still present. A throwaway module with no
# replaces is the only honest test, and it works identically pre- and post-freeze.
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT
printf 'module accesscheck\n\ngo %s\n' "$(awk '/^go /{print $2; exit}' "${REPO_ROOT}/go.mod")" > "${SCRATCH}/go.mod"

pins=0
while read -r mod ver; do
  [[ -z "${ver:-}" ]] && continue
  pins=$((pins + 1))
  if out="$(cd "$SCRATCH" && GOWORK=off go list -m "${mod}@${ver}" 2>&1)"; then
    ok "  resolved  ${out}"
    if (cd "$SCRATCH" && GOWORK=off go mod download "${mod}@${ver}" >/dev/null 2>&1); then
      ok "  fetched   ${mod}@${ver}"
    else
      err "  ${mod}@${ver} resolves but its content will not download"
      FAILED=1
    fi
  else
    err "  ${mod}@${ver} did NOT resolve — you cannot fetch this private module"
    echo "$out" | head -3 | sed 's/^/         /'
    FAILED=1
  fi
done < <(grep -oE 'github\.com/JupiterMetaLabs/(avc|ThebeDB)[[:space:]]+v[^[:space:]/]+' "${REPO_ROOT}/go.mod" \
         | awk '{print $1, $2}' | sort -u)
if [[ "$pins" -eq 0 ]]; then
  err "  found no avc/ThebeDB version pins in go.mod — cannot verify access"
  err "  (expected a require line such as: github.com/JupiterMetaLabs/avc v0.1.0-v3base.1)"
  FAILED=1
fi
echo

info "4/4  Build"

if [[ "$DO_BUILD" -eq 1 ]]; then
  info "      running go build ./... (slow)"
  if (cd "$REPO_ROOT" && go build ./... >/dev/null 2>&1); then
    ok "  go build ./...            (honours GOWORK if you exported it)"
  else
    err "  go build ./... FAILED"; FAILED=1
  fi
  if (cd "$REPO_ROOT" && GOWORK=off go build ./... >/dev/null 2>&1); then
    ok "  GOWORK=off go build ./... (the real gate: pinned versions only)"
  elif [[ "$HAS_LOCAL_REPLACE" -eq 1 ]]; then
    warn "  GOWORK=off go build ./... failed — expected while go.mod still has"
    warn "  'replace => ../' directives. Must pass once the freeze drops them."
  else
    err "  GOWORK=off go build ./... FAILED and there are no local replaces left."
    err "  The pinned versions do not build. This is a real problem."
    FAILED=1
  fi
else
  info "      (pass --build to also compile; skipped by default because jmdn's"
  info "       dependency tree is multi-GB and takes minutes cold)"
fi

echo
if [[ "$FAILED" -eq 0 ]]; then
  ok "Module access is correctly configured."
  [[ "$HAS_LOCAL_REPLACE" -eq 1 ]] && \
    info "Note: go.mod still carries local 'replace => ../' directives (pre-freeze state)."
  exit 0
else
  err "Setup incomplete — see the failures above."
  exit 1
fi
