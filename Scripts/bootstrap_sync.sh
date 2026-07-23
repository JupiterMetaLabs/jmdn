#!/usr/bin/env bash
# bootstrap_sync.sh - JMDN Docker Bootstrap Sync
#
# Mirrors bootstrap.yml Ansible playbook for Docker deployments.
# Runs ONCE — guarded by /opt/jmdn/data/.bootstrapped sentinel on the volume.
#
# Flow (matches ansible task order):
#   1. List + download multipart files and checksums.md5 from GCS (public HTTP)
#   2. Normalise checksums (strip GCS paths → local basenames) and verify
#   3. Backup existing data → /opt/jmdn/backup/data_<timestamp>
#   4. Clear immudb identity/state files
#   5. cat parts* | tar -xzf - into sandbox
#   6. Auto-discover real data root (parent dir of systemdb/)
#   7. Move to /opt/jmdn/data, fix permissions, clean up
#   8. Write sentinel
#
# Env overrides:
#   GCS_BUCKET     GCS bucket name          (default: jmdn-bootstrap)
#   GCS_PREFIX     Path prefix in bucket    (default: staging/bootstrap-20260709_180202)
#   PARTS_PREFIX   Part filename prefix     (default: data-patched.part)
#   CHECKSUM_FILE  Checksum filename        (default: checksums.md5)
#
# To force a re-sync (delete sentinel from the immudb-data volume):
#   docker run --rm -v jmdn_immudb-data:/data alpine rm -f /data/.bootstrapped
#   docker compose run --rm jmdn-bootstrap

set -euo pipefail

# ── Config ───────────────────────────────────────────────
# Defaults point at the 2026-07-09 snapshot (chain tip 12172). This is the
# pre-promotion `staging/` location — repoint GCS_PREFIX to the promoted public
# prefix before fleet rollout. The prefix must be world-readable: this script
# fetches over public HTTP (storage.googleapis.com), not authenticated gsutil.
GCS_BUCKET="${GCS_BUCKET:-jmdn-bootstrap}"
GCS_PREFIX="${GCS_PREFIX:-staging/bootstrap-20260709_180202}"
PARTS_PREFIX="${PARTS_PREFIX:-data-patched.part}"
CHECKSUM_FILE="${CHECKSUM_FILE:-checksums.md5}"
# UID:GID the immudb container runs as. Override if your immudb image differs.
IMMUDB_UID="${IMMUDB_UID:-3322}"

BASE_DIR="/opt/jmdn"
DATA_DIR="${BASE_DIR}/data"
WORK_DIR="${BASE_DIR}/bootstrap_tmp"
# BACKUP_BASE is on the container's writable layer (not a named volume).
# It exists only for the duration of this bootstrap run — not persistent storage.
# Its purpose is to clear DATA_DIR before extraction, with a safety copy in-flight.
BACKUP_BASE="${BASE_DIR}/backup"
IMMUDB_STATE_DIR="${BASE_DIR}/.immudb_state"
SENTINEL="${DATA_DIR}/.bootstrapped"

GCS_HTTP="https://storage.googleapis.com"
GCS_API="${GCS_HTTP}/storage/v1/b/${GCS_BUCKET}/o"

log() { echo "[bootstrap] $*"; }
die() { echo "[bootstrap] ERROR: $*" >&2; exit 1; }

# ── Guard ─────────────────────────────────────────────────
if [ -f "$SENTINEL" ]; then
    log "Sentinel found — skipping bootstrap."
    exit 0
fi

log "First run detected — starting bootstrap sync."

# ── Ensure services are stopped ───────────────────────────
# If running on a live system, immudb will hold file locks on the data directory.
# We must stop it (and jmdn which depends on it) before manipulating data.
if command -v systemctl >/dev/null 2>&1; then
    log "Stopping jmdn and immudb services (if they exist)..."
    systemctl stop jmdn 2>/dev/null || true
    systemctl stop immudb 2>/dev/null || true
fi

# ── Ensure required tools ─────────────────────────────────
for tool in curl wget awk md5sum tar python3; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is required but not found."
done

# ── Prepare work directory ───────────────────────────────
log "Preparing work directory: $WORK_DIR"
rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

# ── List parts from GCS (public HTTP, no gsutil needed) ──
log "Listing parts from GCS: gs://${GCS_BUCKET}/${GCS_PREFIX}/${PARTS_PREFIX}*"
PARTS_JSON=$(curl -sf "${GCS_API}?prefix=${GCS_PREFIX}/${PARTS_PREFIX}" \
    || die "Failed to list objects from GCS. Check bucket name and network.")

PART_NAMES=$(echo "$PARTS_JSON" | python3 -c "
import json, sys
data = json.load(sys.stdin)
items = data.get('items', [])
if not items:
    raise SystemExit('No parts found matching prefix')
for item in sorted(items, key=lambda x: x['name']):
    print(item['name'])
") || die "Failed to parse GCS listing."

[ -z "$PART_NAMES" ] && die "No parts found for prefix ${GCS_PREFIX}/${PARTS_PREFIX}"

log "Found parts:"
echo "$PART_NAMES" | while read -r p; do log "  gs://${GCS_BUCKET}/$p"; done

# ── Download parts ────────────────────────────────────────
log "Downloading parts to $WORK_DIR..."
echo "$PART_NAMES" | while read -r part_path; do
    fname=$(basename "$part_path")
    log "  $fname"
    wget -q --show-progress -O "$WORK_DIR/$fname" "${GCS_HTTP}/${GCS_BUCKET}/${part_path}"
done

# ── Download checksums ────────────────────────────────────
log "Downloading ${CHECKSUM_FILE}..."
wget -q -O "$WORK_DIR/$CHECKSUM_FILE" \
    "${GCS_HTTP}/${GCS_BUCKET}/${GCS_PREFIX}/${CHECKSUM_FILE}" \
    || die "Failed to download ${CHECKSUM_FILE} from GCS."

# ── Verify checksums ──────────────────────────────────────
# Mirrors ansible:
#   awk '{n=split($2,a,"/"); print $1 "  " a[n]}' checksums.md5 > checksums_local.md5
#   md5sum -c checksums_local.md5
log "Normalising and verifying checksums..."
awk '{n=split($2,a,"/"); print $1 "  " a[n]}' \
    "$WORK_DIR/$CHECKSUM_FILE" > "$WORK_DIR/checksums_local.md5"

(cd "$WORK_DIR" && md5sum -c checksums_local.md5) \
    || die "Checksum verification failed — aborting to prevent corrupt data."
log "Checksums OK."

# ── Backup existing data ──────────────────────────────────
if [ -d "$DATA_DIR" ] && [ "$(ls -A "$DATA_DIR" 2>/dev/null)" ]; then
    TIMESTAMP=$(date -u '+%Y%m%dT%H%M%S')
    BACKUP_DIR="${BACKUP_BASE}/data_${TIMESTAMP}"
    log "Backing up existing data → $BACKUP_DIR"
    mkdir -p "$BACKUP_DIR"
    # DATA_DIR may be a Docker volume mount point — cannot mv the directory
    # itself (Device or resource busy). Move contents instead.
    find "$DATA_DIR" -mindepth 1 -maxdepth 1 -exec mv {} "$BACKUP_DIR/" \;
    
    # Prune old backups (keep only the 1 most recent) to prevent disk bloat
    log "Pruning old backups (keeping 1)..."
    ls -1dt "${BACKUP_BASE}"/data_* 2>/dev/null | tail -n +2 | xargs -r rm -rf
fi

# ── Clear immudb identity/state files ────────────────────
log "Clearing immudb identity and state files..."
rm -f "${IMMUDB_STATE_DIR}"/.identity-* 2>/dev/null || true
rm -f "${IMMUDB_STATE_DIR}"/.state-*    2>/dev/null || true

# ── Extract into sandbox ──────────────────────────────────
# Mirrors ansible: cat parts* | tar -xzf - -C sandbox
SANDBOX="${BASE_DIR}/data_tmp/sandbox"
log "Extracting parts into sandbox: $SANDBOX"
mkdir -p "$SANDBOX"

# shellcheck disable=SC2086
cat "$WORK_DIR"/${PARTS_PREFIX}* | tar -xzf - -C "$SANDBOX" \
    || die "Extraction failed."

# ── Auto-discover real data root ──────────────────────────
# Mirrors ansible: find systemdb dir → its parent is the real data root
log "Discovering data root (searching for systemdb/)..."
REAL_DATA_DIR=$(find "$SANDBOX" -type d -name "systemdb" 2>/dev/null \
    | head -n 1 | xargs -I{} dirname {})

if [ -z "$REAL_DATA_DIR" ] || [ "$REAL_DATA_DIR" = "." ]; then
    die "Could not find systemdb in extracted sandbox. Check snapshot integrity."
fi
log "Data root found: $REAL_DATA_DIR"

# ── Move to final location ────────────────────────────────
log "Moving data to $DATA_DIR..."
# DATA_DIR always exists as a Docker volume mount point — `mv src dst` when
# dst exists as a directory would move src *into* dst, giving the wrong path.
# Move contents of REAL_DATA_DIR into DATA_DIR instead.
find "$REAL_DATA_DIR" -mindepth 1 -maxdepth 1 -exec mv {} "$DATA_DIR/" \;
rm -rf "${BASE_DIR}/data_tmp"

# ── Fix permissions ───────────────────────────────────────
# Bootstrap runs as root → extracted files are root-owned (mode 644/755).
# immudb runs as IMMUDB_UID:IMMUDB_UID and needs write access to tx logs.
# chown so immudb can read and write its own data directory.
# jmdn connects via gRPC and never mounts this directory.
log "Setting ownership of $DATA_DIR to $IMMUDB_UID:$IMMUDB_UID..."
chown -R "${IMMUDB_UID}:${IMMUDB_UID}" "$DATA_DIR"

# ── Clean up work directory ───────────────────────────────
log "Cleaning up work directory..."
rm -rf "$WORK_DIR"

# ── Write sentinel ────────────────────────────────────────
touch "$SENTINEL"
log "Bootstrap complete. Sentinel written → $SENTINEL"
