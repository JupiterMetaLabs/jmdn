#!/usr/bin/env bash
# docker-entrypoint.sh - Container startup orchestrator for JMDN
#
# Startup order (mirrors bootstrap.yml ansible play):
#   1. ImmuDB          — start in background, wait for port 3322
#   2. Bootstrap sync  — first run only (download snapshot, verify, extract)
#   3. Restore paths   — peer.json, certs, required dirs (wiped by bootstrap)
#   4. jmdn            — exec only if all prior steps succeed

set -euo pipefail

IMMUDB_PORT="${IMMUDB_PORT:-3322}"
IMMUDB_DIR="${IMMUDB_DIR:-/opt/jmdn/data}"
IMMUDB_READY_TIMEOUT="${IMMUDB_READY_TIMEOUT:-30}"

log() { echo "[entrypoint] $*"; }

# ── Step 1: Start ImmuDB ─────────────────────────────────
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

_shutdown() {
    log "Shutting down..."
    kill "${IMMUDB_PID}" 2>/dev/null || true
}
trap _shutdown TERM INT

# ── Step 2: Bootstrap sync (first run only) ──────────────
if ! /usr/local/bin/bootstrap_sync.sh; then
    log "ERROR: Bootstrap failed — not starting jmdn."
    kill "${IMMUDB_PID}" 2>/dev/null || true
    exit 1
fi

# ── Step 3: Restore paths wiped by bootstrap ─────────────

# peer.json — hardcoded relative path in config/constants.go:
#   PeerFile = "./config/peer.json" resolved from WORKDIR (/opt/jmdn/data)
if [ ! -f /opt/jmdn/data/config/peer.json ]; then
    log "peer.json missing — restoring from /etc/jmdn/peer.json"
    mkdir -p /opt/jmdn/data/config
    cp /etc/jmdn/peer.json /opt/jmdn/data/config/peer.json
fi

# Required dirs (jmdn.yaml: security.cert_dir, cdc.dlq_path, thebe.kv_path)
mkdir -p \
    /opt/jmdn/data/certs

# TLS certs — mirrors Scripts/setup_certs.sh
# Generated only if missing. For production mount real certs:
#   -v /your/certs:/opt/jmdn/data/certs
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
    log "TLS certs generated."
fi

# ── Step 4: Start JMDN ───────────────────────────────────
log "Starting JMDN..."
exec /usr/local/bin/start_jmdn_wrapper.sh "$@"
