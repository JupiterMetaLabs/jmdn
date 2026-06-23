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

# ── Restore / create required paths after bootstrap ──────
# Bootstrap wipes the volume — restore anything that must exist before jmdn starts.

# peer.json (hardcoded in config/constants.go: PeerFile = "./config/peer.json",
# resolved relative to WORKDIR /opt/jmdn/data)
if [ ! -f /opt/jmdn/data/config/peer.json ]; then
    log "peer.json missing — restoring from /etc/jmdn/peer.json"
    mkdir -p /opt/jmdn/data/config
    cp /etc/jmdn/peer.json /opt/jmdn/data/config/peer.json
fi

# Required directories (jmdn.yaml: cdc.dlq_path, thebe.kv_path)
mkdir -p \
    /opt/jmdn/data/certs \
    /opt/jmdn/data/dlq \
    /opt/jmdn/data/storage/thebe-kv

# TLS certs (security.cert_dir: "certs", resolved relative to WORKDIR)
# Mirrors Scripts/setup_certs.sh — generates a local CA + per-service certs.
# For production, mount real certs via -v /your/certs:/opt/jmdn/data/certs
if [ ! -f /opt/jmdn/data/certs/ca.crt ]; then
    log "Generating self-signed TLS certs in /opt/jmdn/data/certs..."
    CERT_DIR=/opt/jmdn/data/certs

    # CA
    openssl req -x509 -newkey rsa:4096 -nodes -days 3650 \
        -keyout "$CERT_DIR/ca.key" -out "$CERT_DIR/ca.crt" \
        -subj "/C=US/O=JMDN/CN=JMDN Dev Root CA" 2>/dev/null

    # Per-service certs (matches setup_certs.sh SERVICES list)
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
