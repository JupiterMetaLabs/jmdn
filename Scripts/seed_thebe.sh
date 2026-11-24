#!/usr/bin/env bash
# seed_thebe.sh — one-shot ThebeDB seed for a NATIVE (non-Docker) jmdn node.
#
# Sibling to bootstrap_sync.sh, but instead of restoring a raw storage/ filesystem
# snapshot (Badger KV only — leaves a Postgres projection empty), it pulls the
# backend-agnostic EXPORT (accounts.jsonl + blocks.jsonl) produced from the
# sequencer and loads it with thebe-import (BatchPutAccountsAuthoritative for exact
# balances/nonces + StoreZKBlock for the block/tx history — no replay). The node
# ends up byte-identical to the sequencer's baseline; thebesync then tail-syncs new
# blocks on top.
#
# WHY load-mode, not thebesync-from-empty: this chain's genesis funding lives in an
# account SNAPSHOT, not in block transactions, so replaying blocks from an empty
# state would fail/diverge. Seeding the snapshot first is the only path that yields
# correct validator balances.
#
# STORAGE TARGET: by default thebe-import reads the SAME /etc/jmdn/jmdn.yaml the
# node uses (thebe.sql_dsn + thebe.kv_path), so the seed lands exactly where the
# node will read it. Override with THEBE_SQL_DSN / THEBE_KV_PATH if needed.
#
# Runs ONCE — guarded by a sentinel.
#
# USAGE:
#   seed_thebe.sh <gcs-url>
#
#   <gcs-url> is the bucket+prefix holding thebe-export.tgz + checksums.md5, e.g.
#     gs://jmdn-bootstrap/thebe-seed-20260909
#     https://storage.googleapis.com/jmdn-bootstrap/thebe-seed-20260909
#   (the objects must be world-readable — this script fetches over public HTTP.)
#
# ENV OVERRIDES:
#   THEBE_IMPORT_BIN  path to thebe-import                 (default: /usr/local/bin/thebe-import)
#   THEBE_SQL_DSN     override the node's Postgres DSN      (default: read from jmdn.yaml)
#   THEBE_KV_PATH     override the node's Badger dir        (default: read from jmdn.yaml)
#   EXPORT_ARCHIVE    tarball name in the prefix            (default: thebe-export.tgz)
#   CHECKSUM_FILE     checksum filename                     (default: checksums.md5)
#   BLOCKS_SUBDIR     blocks dir inside the archive         (default: export/defaultdb)
#   ACCOUNTS_FILE     accounts.jsonl inside the archive     (default: export/accountsdb/accounts.jsonl)
#   STOP_CMD          stop the node before load             (default: systemctl stop jmdn)
#   START_CMD         start the node after a successful load (default: empty — start manually)
#   SENTINEL          run-once marker                       (default: /opt/jmdn/.thebe_seeded)
#
# Re-seed: delete the sentinel and re-run.

set -euo pipefail

log() { echo "[seed-thebe] $*"; }
die() { echo "[seed-thebe] ERROR: $*" >&2; exit 1; }

# ── Parse the GCS URL argument ───────────────────────────────────────────────
GCS_URL="${1:-}"
[ -n "$GCS_URL" ] || die "missing <gcs-url> (e.g. gs://jmdn-bootstrap/thebe-seed-YYYYMMDD)"

case "$GCS_URL" in
  gs://*)                              rest="${GCS_URL#gs://}" ;;
  https://storage.googleapis.com/*)    rest="${GCS_URL#https://storage.googleapis.com/}" ;;
  *) die "unsupported URL: use gs://bucket/prefix or https://storage.googleapis.com/bucket/prefix" ;;
esac
rest="${rest%/}"                                   # strip trailing slash
BUCKET="${rest%%/*}"                               # first path segment
if [ "$rest" = "$BUCKET" ]; then PREFIX=""; else PREFIX="${rest#*/}"; fi
[ -n "$BUCKET" ] || die "could not parse a bucket from $GCS_URL"

BASE_HTTP="https://storage.googleapis.com/${BUCKET}${PREFIX:+/$PREFIX}"

EXPORT_ARCHIVE="${EXPORT_ARCHIVE:-thebe-export.tgz}"
CHECKSUM_FILE="${CHECKSUM_FILE:-checksums.md5}"
BLOCKS_SUBDIR="${BLOCKS_SUBDIR:-export/defaultdb}"
ACCOUNTS_FILE="${ACCOUNTS_FILE:-export/accountsdb/accounts.jsonl}"
THEBE_IMPORT_BIN="${THEBE_IMPORT_BIN:-/usr/local/bin/thebe-import}"
STOP_CMD="${STOP_CMD-systemctl stop jmdn}"
START_CMD="${START_CMD-}"
SENTINEL="${SENTINEL:-/opt/jmdn/.thebe_seeded}"

# ── Guard ────────────────────────────────────────────────────────────────────
if [ -f "$SENTINEL" ]; then
  log "Sentinel $SENTINEL present — already seeded, skipping."
  exit 0
fi

for tool in curl wget md5sum tar awk; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required but not found."
done
[ -x "$THEBE_IMPORT_BIN" ] || die "thebe-import not executable at $THEBE_IMPORT_BIN (build+install it, or set THEBE_IMPORT_BIN)."

# ── Stop the node (Badger single-writer; live Postgres writes corrupt the load) ─
if [ -n "$STOP_CMD" ]; then
  log "Stopping node: $STOP_CMD"
  eval "$STOP_CMD" || log "WARNING: stop command returned non-zero (already stopped?)"
else
  log "WARNING: STOP_CMD empty — ensure jmdn is STOPPED before seeding."
fi

# ── Fetch export archive + checksum (public HTTP) ────────────────────────────
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT
log "Source: ${BASE_HTTP}"
log "Downloading ${EXPORT_ARCHIVE}"
wget -q --show-progress -O "$WORK_DIR/$EXPORT_ARCHIVE" "${BASE_HTTP}/${EXPORT_ARCHIVE}" \
  || die "download failed — check the URL and that the object is world-readable."
log "Downloading ${CHECKSUM_FILE}"
wget -q -O "$WORK_DIR/$CHECKSUM_FILE" "${BASE_HTTP}/${CHECKSUM_FILE}" \
  || die "download of ${CHECKSUM_FILE} failed."

log "Verifying checksum..."
awk '{n=split($2,a,"/"); print $1 "  " a[n]}' "$WORK_DIR/$CHECKSUM_FILE" > "$WORK_DIR/checksums_local.md5"
(cd "$WORK_DIR" && md5sum -c checksums_local.md5) \
  || die "checksum verification failed — refusing to load a corrupt export."

# ── Extract ──────────────────────────────────────────────────────────────────
log "Extracting ${EXPORT_ARCHIVE}"
tar -xzf "$WORK_DIR/$EXPORT_ARCHIVE" -C "$WORK_DIR" || die "extraction failed."
DIR_ARG="$WORK_DIR/$BLOCKS_SUBDIR"
ACC_ARG="$WORK_DIR/$ACCOUNTS_FILE"
[ -f "$DIR_ARG/blocks.jsonl" ] || die "no blocks.jsonl under $DIR_ARG (check BLOCKS_SUBDIR / archive layout)."
[ -f "$ACC_ARG" ]             || die "no accounts.jsonl at $ACC_ARG (check ACCOUNTS_FILE / archive layout)."

# ── Load + verify into the node's ThebeDB ────────────────────────────────────
# With no THEBE_SQL_DSN/THEBE_KV_PATH set, thebe-import loads /etc/jmdn/jmdn.yaml
# (the same config the node uses), so the seed lands exactly where the node reads.
log "Loading into ThebeDB (thebe-import) ..."
"$THEBE_IMPORT_BIN" -dir "$DIR_ARG" -accounts "$ACC_ARG" -verify "$ACC_ARG" \
  || die "thebe-import failed — sentinel NOT written; node left unseeded."

# ── Sentinel + optional start ────────────────────────────────────────────────
mkdir -p "$(dirname "$SENTINEL")"
touch "$SENTINEL"
log "Seed complete — sentinel written → $SENTINEL"
if [ -n "$START_CMD" ]; then
  log "Starting node: $START_CMD"
  eval "$START_CMD"
else
  log "Node left stopped. Start it when ready (e.g. systemctl start jmdn)."
fi
