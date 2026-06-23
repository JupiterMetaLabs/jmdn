#!/usr/bin/env bash
# bootstrap_sync.sh - First-run snapshot sync for JMDN Docker container
#
# Downloads and extracts the latest data snapshot before the node starts.
# Runs ONLY on the first container start — guarded by a sentinel file on the
# volume (/opt/jmdn/data/.bootstrapped). Subsequent restarts skip this entirely.
#
# Override the snapshot URL via env var:
#   -e BOOTSTRAP_TAR_URL=https://your-bucket/snapshot.tar.bz2
#
# To force a re-sync (e.g. after wiping the volume):
#   docker exec jmdn rm /opt/jmdn/data/.bootstrapped && docker restart jmdn

set -eo pipefail

SENTINEL="/opt/jmdn/data/.bootstrapped"
DEFAULT_TAR_URL="https://storage.googleapis.com/jmzk-releases/JMZK-Decentralised-Network/jmdn_data_20260604_145256.tar.bz2"
TAR_URL="${BOOTSTRAP_TAR_URL:-$DEFAULT_TAR_URL}"
TAR_FILE="/tmp/$(basename "$TAR_URL")"

log()  { echo "[bootstrap] $*"; }
die()  { echo "[bootstrap] ERROR: $*" >&2; exit 1; }

# ── Guard: skip if already bootstrapped ──────────────────
if [ -f "$SENTINEL" ]; then
    log "Sentinel found at $SENTINEL — skipping bootstrap."
    exit 0
fi

log "First run detected — starting bootstrap sync."
log "Snapshot URL: $TAR_URL"

# ── Download ─────────────────────────────────────────────
log "Downloading snapshot..."
wget -q --show-progress -O "$TAR_FILE" "$TAR_URL"

# ── Checksum verification ─────────────────────────────────
# Expects a checksums.md5 file in the same bucket directory as the snapshot.
# Format matches the original: md5sum data-patched.part* > checksums_local.md5
#                              diff checksums.md5 checksums_local.md5
SNAPSHOT_DIR="${TAR_URL%/*}"
CHECKSUM_URL="${SNAPSHOT_DIR}/checksums.md5"
CHECKSUM_REMOTE="/tmp/checksums_remote.md5"
CHECKSUM_LOCAL="/tmp/checksums_local.md5"

log "Downloading remote checksum from: $CHECKSUM_URL"
if wget -q -O "$CHECKSUM_REMOTE" "$CHECKSUM_URL"; then
    log "Computing local checksum..."
    md5sum "$TAR_FILE" | awk -v fname="$(basename "$TAR_FILE")" '{print $1 "  " fname}' > "$CHECKSUM_LOCAL"

    # Normalise: compare only filenames present in the local file
    if diff \
        <(grep "$(basename "$TAR_FILE")" "$CHECKSUM_REMOTE" | awk '{print $1}') \
        <(awk '{print $1}' "$CHECKSUM_LOCAL") > /dev/null 2>&1; then
        log "Checksum OK."
    else
        rm -f "$TAR_FILE" "$CHECKSUM_REMOTE" "$CHECKSUM_LOCAL"
        die "Checksum mismatch — aborting bootstrap to prevent corrupt data."
    fi
    rm -f "$CHECKSUM_REMOTE" "$CHECKSUM_LOCAL"
else
    log "WARNING: No checksums.md5 found at remote — skipping verification."
fi

# ── Wipe existing data (mirrors original script) ─────────
# /opt/jmdn/data is a Docker volume mount point — cannot rm the dir itself,
# only its contents.
log "Cleaning /opt/jmdn/data contents..."
find /opt/jmdn/data -mindepth 1 -delete

log "Clearing immudb identity and state files..."
rm -rf /opt/jmdn/.immudb_state/.identity-* 2>/dev/null || true
rm -rf /opt/jmdn/.immudb_state/.state-*    2>/dev/null || true

# ── Extract ──────────────────────────────────────────────
log "Extracting snapshot to /..."
tar -xjf "$TAR_FILE" -C /

# ── Permissions ──────────────────────────────────────────
log "Fixing permissions on /opt/jmdn/data..."
chown -R root:root /opt/jmdn/data

# ── Cleanup ──────────────────────────────────────────────
log "Removing downloaded tar..."
rm -f "$TAR_FILE"

# ── Write sentinel ───────────────────────────────────────
# Sentinel lives on the volume → persists across container restarts
touch "$SENTINEL"
log "Bootstrap complete. Sentinel written → $SENTINEL"
