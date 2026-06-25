#!/usr/bin/env bash
# docker-entrypoint.sh - Container startup orchestrator for JMDN
#
# Runs as root. Drops to jmdn user (via gosu) for all long-running processes.
#
# Startup order:
#   1. Bootstrap sync  — root only: download snapshot, chown to jmdn, write sentinel
#                        MUST run before ImmuDB — bootstrap mv's /opt/jmdn/data
#   2. Restore paths   — root: peer.json, certs (may be wiped by bootstrap)
#   3. gosu jmdn immudb — drops privilege, starts ImmuDB, waits for :3322
#   4. gosu jmdn jmdn  — drops privilege, exec's the node process

set -euo pipefail

JMDN_USER="${JMDN_USER:-jmdn}"
IMMUDB_PORT="${IMMUDB_PORT:-3322}"
IMMUDB_DIR="${IMMUDB_DIR:-/opt/jmdn/data}"
IMMUDB_READY_TIMEOUT="${IMMUDB_READY_TIMEOUT:-30}"

log() { echo "[entrypoint] $*"; }

# ── Step 1: Bootstrap sync (first run only) ──────────────
# Runs as root — bootstrap_sync.sh chowns the extracted snapshot to jmdn:jmdn.
# ImmuDB is NOT running here: bootstrap mv's /opt/jmdn/data, so starting
# ImmuDB first would leave it with stale file handles after the directory swap.
if ! /usr/local/bin/bootstrap_sync.sh; then
    log "ERROR: Bootstrap failed — aborting."
    exit 1
fi

# ── Step 2: Restore paths (runs as root before privilege drop) ────────────────

# peer.json — hardcoded relative path in config/constants.go:
#   PeerFile = "./config/peer.json" resolved from WORKDIR (/opt/jmdn/data)
if [ ! -f /opt/jmdn/data/config/peer.json ]; then
    log "peer.json missing — restoring from /etc/jmdn/peer.json"
    mkdir -p /opt/jmdn/data/config
    cp /etc/jmdn/peer.json /opt/jmdn/data/config/peer.json
    chown -R "${JMDN_USER}:${JMDN_USER}" /opt/jmdn/data/config
fi

mkdir -p /opt/jmdn/data/certs

# TLS certs — mirrors Scripts/setup_certs.sh.
# Self-signed certs generated only if missing.
# For production: mount real certs at /opt/jmdn/data/certs
if [ ! -f /opt/jmdn/data/certs/ca.crt ]; then
    log "Generating self-signed TLS certs..."
    CERT_DIR=/opt/jmdn/data/certs

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
IMMUDB_EXTERNAL="${IMMUDB_EXTERNAL:-false}"
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
