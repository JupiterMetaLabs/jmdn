#!/usr/bin/env bash
# docker-entrypoint.sh - Container startup orchestrator for JMDN
#
# Runs as root. Drops to jmdn user (via gosu) for all long-running processes.
#
# Startup order:
#   1. Bootstrap check — download chain snapshot into /opt/jmdn if missing.
#   2. Restore paths   — root: ensure DB/, storage/, config/, certs/ exist with
#                        correct ownership.
#   3. gosu jmdn jmdn  — drops privilege, exec's the node process.
#
# Storage: ThebeDB is embedded in the jmdn process (BadgerDB KV + SQL
# projection under /opt/jmdn) — there is no external database to wait for.

set -euo pipefail

JMDN_USER="${JMDN_USER:-jmdn}"

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
# Populates node state from the GCS snapshot on first run.
# bootstrap_sync.sh writes a .bootstrapped sentinel — safe to re-run.
SENTINEL="/opt/jmdn/.bootstrapped"
if [ -f "$SENTINEL" ]; then
    log "Bootstrap sentinel found — node state already populated."
else
    log "Running bootstrap sync..."
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
mkdir -p \
    /opt/jmdn/config \
    /opt/jmdn/DB \
    /opt/jmdn/storage \
    /opt/jmdn/certs
chown "${JMDN_USER}:${JMDN_USER}" \
    /opt/jmdn/config \
    /opt/jmdn/DB \
    /opt/jmdn/storage \
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

# ── Step 3: Start JMDN as jmdn ───────────────────────────
# exec replaces this shell — SIGTERM/SIGINT forwarded to jmdn process.
# gosu re-execs so jmdn is PID 1's direct child, not a shell child.
log "Starting JMDN as ${JMDN_USER}..."
exec gosu "${JMDN_USER}" /usr/local/bin/start_jmdn_wrapper.sh "$@"
