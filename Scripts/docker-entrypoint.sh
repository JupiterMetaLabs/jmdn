#!/usr/bin/env bash
# docker-entrypoint.sh - Container startup orchestrator for JMDN
#
# Order of operations:
#   1. Bootstrap sync  (first run only — downloads + extracts snapshot)
#   2. ImmuDB          (starts in background, waits for port 3322)
#   3. jmdn            (exec via start_jmdn_wrapper.sh)

set -eo pipefail

IMMUDB_PORT="${IMMUDB_PORT:-3322}"
IMMUDB_DIR="${IMMUDB_DIR:-/opt/jmdn/data}"
IMMUDB_READY_TIMEOUT="${IMMUDB_READY_TIMEOUT:-30}"

log() { echo "[entrypoint] $*"; }

# ── Step 1: Bootstrap sync (first run only) ──────────────
/usr/local/bin/bootstrap_sync.sh

# ── Step 2: Start ImmuDB ─────────────────────────────────
log "Starting ImmuDB (dir: ${IMMUDB_DIR})..."
immudb --dir "${IMMUDB_DIR}" &
IMMUDB_PID=$!

log "Waiting for ImmuDB on port ${IMMUDB_PORT}..."
elapsed=0
until nc -z 127.0.0.1 "${IMMUDB_PORT}" 2>/dev/null; do
    if [ "${elapsed}" -ge "${IMMUDB_READY_TIMEOUT}" ]; then
        log "ERROR: ImmuDB did not start within ${IMMUDB_READY_TIMEOUT}s"
        kill "${IMMUDB_PID}" 2>/dev/null || true
        exit 1
    fi
    sleep 1
    elapsed=$((elapsed + 1))
done
log "ImmuDB ready (${elapsed}s)"

# Forward SIGTERM/SIGINT to ImmuDB on shutdown
_shutdown() {
    log "Shutting down..."
    kill "${IMMUDB_PID}" 2>/dev/null || true
}
trap _shutdown TERM INT

# ── Step 3: Start JMDN ───────────────────────────────────
log "Starting JMDN..."
exec /usr/local/bin/start_jmdn_wrapper.sh "$@"
