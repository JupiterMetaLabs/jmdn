#!/usr/bin/env bash
# docker-entrypoint.sh - Container startup orchestrator for JMDN
#
# Runs as root. Drops to jmdn user (via gosu) for all long-running processes.
#
# Startup order:
#   1. Bootstrap check — skipped when IMMUDB_EXTERNAL=true (separate container mode).
#                        In embedded mode only: download snapshot into /opt/jmdn/data.
#   2. Restore paths   — root: ensure DB/, config/, certs/ exist with correct ownership.
#   3. Wait for ImmuDB — nc loop against immudb host:port.
#   4. gosu jmdn jmdn  — drops privilege, exec's the node process.
#
# External ImmuDB mode (IMMUDB_EXTERNAL=true, default in docker-compose.yml):
#   Bootstrap is handled by the jmdn-bootstrap profile service which mounts
#   immudb-data:/opt/jmdn/data and writes the snapshot there. The jmdn container
#   mounts jmdn-state:/opt/jmdn — running bootstrap here would write into the
#   wrong volume and the data would never reach immudb.
#   Run bootstrap once before starting the stack:
#     docker compose run --rm jmdn-bootstrap

set -euo pipefail

JMDN_USER="${JMDN_USER:-jmdn}"
IMMUDB_PORT="${IMMUDB_PORT:-3322}"
IMMUDB_DIR="${IMMUDB_DIR:-/opt/jmdn/data}"
IMMUDB_READY_TIMEOUT="${IMMUDB_READY_TIMEOUT:-120}"
IMMUDB_EXTERNAL="${IMMUDB_EXTERNAL:-false}"

log() { echo "[entrypoint] $*"; }

# ── Config guard ──────────────────────────────────────────
# jmdn.yaml must be mounted — node must not start with compiled defaults
# (wrong chain_id, wrong seednode). Mount via: -v /your/jmdn.yaml:/etc/jmdn/jmdn.yaml
if [ ! -f /etc/jmdn/jmdn.yaml ]; then
    log "ERROR: /etc/jmdn/jmdn.yaml not found."
    log "Mount your config: -v \$(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml"
    exit 1
fi

# ── Step 1: Bootstrap sync ────────────────────────────────
# External mode: skip — bootstrap must run via the jmdn-bootstrap service so
# it writes into the immudb-data volume (not jmdn-state).
# Embedded mode: run bootstrap_sync.sh before immudb starts.
if [ "${IMMUDB_EXTERNAL}" = "true" ]; then
    log "External ImmuDB mode — skipping bootstrap."
    # Guard: ensure bootstrap was run against the immudb-data volume before this
    # container started. Without it, jmdn connects to an empty immudb and fails.
    SENTINEL="/opt/jmdn/data/.bootstrapped"
    if [ ! -f "$SENTINEL" ]; then
        log "ERROR: Bootstrap sentinel not found at $SENTINEL."
        log "Run bootstrap first: docker compose run --rm jmdn-bootstrap"
        exit 1
    fi
    log "Sentinel found — immudb-data volume is populated."
else
    log "Embedded ImmuDB mode — running bootstrap sync..."
    if ! /usr/local/bin/bootstrap_sync.sh; then
        log "ERROR: Bootstrap failed — aborting."
        exit 1
    fi
fi

# ── Step 2: Restore paths (runs as root before privilege drop) ────────────────

# Ensure required subdirectories exist on the volume with correct ownership.
# JMDN_DATA = /opt/jmdn — matches bare-metal WorkingDirectory=${JMDN_DATA}.
# The jmdn-state volume overlays the image filesystem at /opt/jmdn on first run,
# so the volume starts empty and these dirs must be recreated here.
# NOTE: /opt/jmdn/data is immudb's dir — managed by the immudb container and
#       mounted separately (immudb-data volume). Do NOT create it here.
mkdir -p \
    /opt/jmdn/config \
    /opt/jmdn/DB \
    /opt/jmdn/certs
chown "${JMDN_USER}:${JMDN_USER}" \
    /opt/jmdn/config \
    /opt/jmdn/DB \
    /opt/jmdn/certs

# TLS certs — mirrors Scripts/setup_certs.sh.
# Self-signed certs generated only if missing.
# For production: mount real certs at /opt/jmdn/certs
if [ ! -f /opt/jmdn/certs/ca.crt ]; then
    log "Generating self-signed TLS certs..."
    CERT_DIR=/opt/jmdn/certs

    openssl req -x509 -newkey rsa:4096 -nodes -days 3650 \
        -keyout "$CERT_DIR/ca.key" -out "$CERT_DIR/ca.crt" \
        -subj "/C=US/O=JMDN/CN=JMDN Dev Root CA" 2>/dev/null

    for SVC in cli_admin block_ingest_grpc block_ingest_http did_service \
               explorer_api mempool_service admin_client explorer_client; do
        openssl genrsa -out "$CERT_DIR/$SVC.key" 2048 2>/dev/null
        openssl req -new -key "$CERT_DIR/$SVC.key" \
            -out "$CERT_DIR/$SVC.csr" \
            -subj "/C=US/O=JMDN/CN=$SVC" 2>/dev/null
        openssl x509 -req -in "$CERT_DIR/$SVC.csr" \
            -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" -CAcreateserial \
            -out "$CERT_DIR/$SVC.crt" -days 365 -sha256 \
            -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1") 2>/dev/null
        rm -f "$CERT_DIR/$SVC.csr"
    done
    chown -R "${JMDN_USER}:${JMDN_USER}" "$CERT_DIR"
    log "TLS certs generated."
fi

# ── Step 3: Start ImmuDB (or wait for external) ──────────
# IMMUDB_EXTERNAL=true  → immudb runs as a separate container (docker-compose);
#                          entrypoint just waits for JMDN_DATABASE_ADDRESS:port.
# IMMUDB_EXTERNAL=false → entrypoint starts the embedded immudb process.
IMMUDB_HOST="${JMDN_DATABASE_ADDRESS:-127.0.0.1}"
IMMUDB_PID=""

_shutdown() {
    log "Shutting down..."
    [ -n "${IMMUDB_PID}" ] && kill "${IMMUDB_PID}" 2>/dev/null || true
}
trap _shutdown TERM INT

if [ "${IMMUDB_EXTERNAL}" = "true" ]; then
    log "External ImmuDB mode — waiting for ${IMMUDB_HOST}:${IMMUDB_PORT}..."
else
    log "Starting embedded ImmuDB as ${JMDN_USER} (dir: ${IMMUDB_DIR})..."
    gosu "${JMDN_USER}" immudb --dir "${IMMUDB_DIR}" &
    IMMUDB_PID=$!
fi

log "Waiting for ImmuDB on ${IMMUDB_HOST}:${IMMUDB_PORT}..."
elapsed=0
until nc -z "${IMMUDB_HOST}" "${IMMUDB_PORT}" 2>/dev/null; do
    if [ "${elapsed}" -ge "${IMMUDB_READY_TIMEOUT}" ]; then
        log "ERROR: ImmuDB did not become ready within ${IMMUDB_READY_TIMEOUT}s"
        log "  ImmuDB loads all databases before binding port ${IMMUDB_PORT}."
        log "  Large snapshots can take 2-5 min. Increase timeout:"
        log "    docker run -e IMMUDB_READY_TIMEOUT=300 ..."
        [ -n "${IMMUDB_PID}" ] && kill "${IMMUDB_PID}" 2>/dev/null || true
        exit 1
    fi
    sleep 1
    elapsed=$((elapsed + 1))
done
log "ImmuDB ready (${elapsed}s)"

# ── Step 4: Start JMDN as jmdn ───────────────────────────
# exec replaces this shell — SIGTERM/SIGINT forwarded to jmdn process.
# gosu re-execs so jmdn is PID 1's direct child, not a shell child.
log "Starting JMDN as ${JMDN_USER}..."
exec gosu "${JMDN_USER}" /usr/local/bin/start_jmdn_wrapper.sh "$@"
