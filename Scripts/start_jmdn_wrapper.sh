#!/usr/bin/env bash
################################################################################
# start_jmdn_wrapper.sh - Runtime Wrapper for JMDN
#
# This script is SELF-CONTAINED by design. It does NOT depend on platform.sh
# or any external library. Rationale:
#
#   - This file gets copied to /usr/local/bin at install time, far from the
#     repo and far from lib/platform.sh.
#   - It is invoked by the init system (systemd, launchd, rc.d, openrc) —
#     not by a human at a terminal. If it fails, the node goes down silently.
#   - Its job is tiny: resolve paths, validate, exec. It needs no package
#     management, no sed helpers, no service wrappers.
#
# platform.sh is an orchestration-time library (build, deploy, install).
# This wrapper is a runtime tool. Keep them decoupled.
#
# Usage: ./start_jmdn_wrapper.sh [args...]
#
# Override paths via environment variables:
#   BIN_PATH      — path to the jmdn binary
#   CONFIG_PATH   — path to jmdn.yaml
#   JMDN_BIN      — directory containing the jmdn binary
#   JMDN_ETC      — directory containing jmdn.yaml
################################################################################

set -euo pipefail

# --- Logging (minimal, no dependencies) --------------------------------------

log_info()  { echo -e "\033[0;34m[INFO]\033[0m  $*"; }
log_error() { echo -e "\033[0;31m[ERROR]\033[0m $*" >&2; }

# --- Platform-aware path defaults --------------------------------------------
# Detect once, branch once, done. No library needed for 10 lines.

case "$(uname -s)" in
    Linux)
        JMDN_BIN="${JMDN_BIN:-/usr/local/bin}"
        JMDN_ETC="${JMDN_ETC:-/etc/jmdn}"
        ;;
    Darwin)
        _prefix="${HOMEBREW_PREFIX:-/usr/local}"
        JMDN_BIN="${JMDN_BIN:-${_prefix}/bin}"
        JMDN_ETC="${JMDN_ETC:-${_prefix}/etc/jmdn}"
        ;;
    FreeBSD)
        JMDN_BIN="${JMDN_BIN:-/usr/local/bin}"
        JMDN_ETC="${JMDN_ETC:-/usr/local/etc/jmdn}"
        ;;
    *)
        JMDN_BIN="${JMDN_BIN:-/usr/local/bin}"
        JMDN_ETC="${JMDN_ETC:-/etc/jmdn}"
        ;;
esac

# --- Resolve binary and config -----------------------------------------------

BIN_PATH="${BIN_PATH:-${JMDN_BIN}/jmdn}"
CONFIG_PATH="${CONFIG_PATH:-${JMDN_ETC}/jmdn.yaml}"

# Fall back to local config if system config is missing (dev convenience)
if [[ ! -f "${CONFIG_PATH}" ]] && [[ -f "./jmdn.yaml" ]]; then
    CONFIG_PATH="./jmdn.yaml"
fi

# Source auto-generated Redis credentials if present and readable.
# -r (not -f): the file is root-owned mode 600; if the service runs as a
# non-root SERVICE_USER, a bare `source` on an unreadable file would kill
# the wrapper under `set -e` and the node would never start.
if [[ -r "${JMDN_ETC}/redis.env" ]]; then
    # shellcheck disable=SC1090
    source "${JMDN_ETC}/redis.env"
elif [[ -e "${JMDN_ETC}/redis.env" ]]; then
    log_info "Skipping ${JMDN_ETC}/redis.env (not readable by $(id -un)) — set database.redis.password in jmdn.yaml instead"
fi

# --- Validate and exec -------------------------------------------------------

log_info "Starting JMDN..."
log_info "Binary: ${BIN_PATH}"
log_info "Config: ${CONFIG_PATH}"

if [[ ! -x "${BIN_PATH}" ]]; then
    log_error "Binary not found or not executable at ${BIN_PATH}"
    exit 1
fi

# exec replaces this shell process — no extra PID, clean signal handling
exec "${BIN_PATH}" "$@"
