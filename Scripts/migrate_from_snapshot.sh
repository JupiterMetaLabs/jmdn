#!/usr/bin/env bash
################################################################################
# migrate_from_snapshot.sh
#
# PURPOSE:
#   Restore an ImmuDB backup (tar.bz2) to a temporary ImmuDB instance, then
#   run the immudb-to-thebe migration tool to write all blocks + accounts into
#   ThebeDB (BadgerDB KV + Postgres). ImmuDB is stopped after migration.
#   JMDN is then started against ThebeDB only.
#
# USAGE:
#   sudo ./Scripts/migrate_from_snapshot.sh [TAR_URL_OR_PATH] [OPTIONS]
#
# ARGUMENTS:
#   TAR_URL_OR_PATH   URL (http/https) or local path to the .tar.bz2 snapshot.
#                     Default: $DEFAULT_TAR_URL
#
# OPTIONS:
#   --kv-path PATH        BadgerDB KV directory  (default: /opt/jmdn/thebe-kv)
#   --sql-dsn DSN         Postgres DSN            (default: read from jmdn.yaml)
#   --start-block N       Resume from block N     (default: 0)
#   --batch-size N        ImmuDB read batch size  (default: 500)
#   --skip-blocks         Skip block migration
#   --skip-accounts       Skip account migration
#   --migration-bin PATH  Path to migrate binary or .py script (default: auto-detected)
#   --no-start            Do not start jmdn after migration
#
# FLOW:
#   1. Download / locate tar
#   2. Stop jmdn (and immudb if running)
#   3. Clear ImmuDB data dir + state files
#   4. Extract tar → /
#   5. Fix ImmuDB permissions
#   6. Start ImmuDB (temporary — source only)
#   7. Wait for ImmuDB healthy
#   8. Ensure ThebeDB infra is up (KV dir + Postgres)
#   9. Run migration binary (ImmuDB → ThebeDB)
#  10. Stop ImmuDB
#  11. Start jmdn (ThebeDB mode)
#  12. Tail logs
################################################################################

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/platform.sh"

require_root

# ── Defaults ──────────────────────────────────────────────────────────────────
DEFAULT_TAR_URL="https://storage.googleapis.com/jmzk-releases/JMZK-Decentralised-Network/jmdn_data_20260604_145256.tar.bz2"

TAR_SOURCE="${1:-${DEFAULT_TAR_URL}}"
THEBE_KV_PATH="/opt/jmdn/thebe-kv"
THEBE_SQL_DSN=""
START_BLOCK=0
BATCH_SIZE=500
SKIP_BLOCKS=false
SKIP_ACCOUNTS=false
MIGRATION_BIN=""
NO_START=false
IMMUDB_DATA="/opt/jmdn/data"
IMMUDB_STATE="/opt/jmdn/.immudb_state"
IMMUDB_READY_RETRIES=30

# ── Parse options (skip first positional arg) ──────────────────────────────────
shift || true
while [[ $# -gt 0 ]]; do
    case "$1" in
        --kv-path)       THEBE_KV_PATH="$2";   shift 2 ;;
        --sql-dsn)       THEBE_SQL_DSN="$2";   shift 2 ;;
        --start-block)   START_BLOCK="$2";     shift 2 ;;
        --batch-size)    BATCH_SIZE="$2";      shift 2 ;;
        --skip-blocks)   SKIP_BLOCKS=true;     shift   ;;
        --skip-accounts) SKIP_ACCOUNTS=true;   shift   ;;
        --migration-bin) MIGRATION_BIN="$2";   shift 2 ;;
        --no-start)      NO_START=true;        shift   ;;
        *) log_warn "Unknown option: $1"; shift ;;
    esac
done

# ── Locate migration tool (Python script preferred, Go binary fallback) ───────
MIGRATION_USE_PYTHON=false
if [ -z "${MIGRATION_BIN}" ]; then
    for candidate in \
        "${SCRIPT_DIR}/migrate_immudb_to_thebe.py" \
        "${SCRIPT_DIR}/../migrate_immudb_to_thebe" \
        "/usr/local/bin/migrate_immudb_to_thebe" \
        "$(command -v migrate_immudb_to_thebe 2>/dev/null || true)"; do
        [ -z "${candidate}" ] && continue
        if [[ "${candidate}" == *.py ]] && [ -f "${candidate}" ]; then
            MIGRATION_BIN="${candidate}"
            MIGRATION_USE_PYTHON=true
            break
        elif [ -x "${candidate}" ]; then
            MIGRATION_BIN="${candidate}"
            break
        fi
    done
fi
if [ -z "${MIGRATION_BIN}" ]; then
    log_die "Migration tool not found. Expected one of:
  ${SCRIPT_DIR}/migrate_immudb_to_thebe.py   (Python, preferred)
  ${SCRIPT_DIR}/../migrate_immudb_to_thebe   (Go binary)
Or pass --migration-bin /path/to/script"
fi
log_ok "Migration tool: ${MIGRATION_BIN}"

# Ensure Python deps if using Python script
if [ "${MIGRATION_USE_PYTHON}" = "true" ]; then
    log_info "Checking Python dependencies..."
    python3 -c "import immudb, psycopg2" 2>/dev/null || {
        log_info "Installing immudb-py and psycopg2-binary..."
        pip3 install immudb-py psycopg2-binary --break-system-packages --quiet || \
        pip3 install immudb-py psycopg2-binary --quiet
    }
    log_ok "Python dependencies ready"
fi

# ── Resolve SQL DSN ───────────────────────────────────────────────────────────
if [ -z "${THEBE_SQL_DSN}" ]; then
    # Try to read from jmdn.yaml
    for yaml_path in "${JMDN_ETC}/jmdn.yaml" "$(dirname "${SCRIPT_DIR}")/jmdn_default.yaml"; do
        if [ -f "${yaml_path}" ]; then
            THEBE_SQL_DSN=$(grep -E "^\s*sql_dsn:" "${yaml_path}" | head -1 \
                | sed 's/.*sql_dsn:\s*"\?\([^"]*\)"\?/\1/' | tr -d '"' | xargs)
            [ -n "${THEBE_SQL_DSN}" ] && break
        fi
    done
fi
if [ -z "${THEBE_SQL_DSN}" ]; then
    log_die "Could not determine Postgres DSN. Pass --sql-dsn or set thebe.sql_dsn in jmdn.yaml"
fi
log_info "ThebeDB KV:  ${THEBE_KV_PATH}"
log_info "ThebeDB SQL: ${THEBE_SQL_DSN}"

################################################################################
# Step 1 — Obtain the tar
################################################################################

echo ""
log_info "Step 1: Obtaining snapshot..."

if [[ "${TAR_SOURCE}" == http* ]]; then
    TAR_FILE="/root/$(basename "${TAR_SOURCE}")"
    log_info "Downloading ${TAR_SOURCE}..."
    wget -O "${TAR_FILE}" "${TAR_SOURCE}"
    log_ok "Downloaded → ${TAR_FILE}"
elif [ -f "${TAR_SOURCE}" ]; then
    TAR_FILE="${TAR_SOURCE}"
    log_ok "Using local file: ${TAR_FILE}"
else
    log_die "TAR source not found: ${TAR_SOURCE}"
fi

################################################################################
# Step 2 — Stop services
################################################################################

echo ""
log_info "Step 2: Stopping services..."

systemctl stop jmdn 2>/dev/null && log_ok "jmdn stopped" || log_warn "jmdn was not running"
systemctl stop immudb 2>/dev/null && log_ok "immudb stopped" || log_warn "immudb was not running"
sleep 3

################################################################################
# Step 3 — Clear ImmuDB data
################################################################################

echo ""
log_info "Step 3: Clearing ImmuDB data..."

if [ -d "${IMMUDB_DATA}" ]; then
    rm -rf "${IMMUDB_DATA}"
    log_ok "Cleared ${IMMUDB_DATA}"
fi
rm -f "${IMMUDB_STATE}"/.identity-* "${IMMUDB_STATE}"/.state-* 2>/dev/null || true
log_ok "ImmuDB state files cleared"

################################################################################
# Step 4 — Extract snapshot
################################################################################

echo ""
log_info "Step 4: Extracting ${TAR_FILE}..."
tar -xjf "${TAR_FILE}" -C /
log_ok "Extracted"

################################################################################
# Step 5 — Fix ImmuDB permissions
################################################################################

echo ""
log_info "Step 5: Fixing permissions..."

IMMUDB_USER=$(systemctl cat immudb 2>/dev/null | grep "^User=" | cut -d= -f2 || echo "root")
IMMUDB_USER="${IMMUDB_USER:-root}"
log_info "immudb runs as: ${IMMUDB_USER}"
chown -R "${IMMUDB_USER}:${IMMUDB_USER}" "${IMMUDB_DATA}" 2>/dev/null || true
log_ok "Permissions fixed"

################################################################################
# Step 6 — Start ImmuDB (source — temporary)
################################################################################

echo ""
log_info "Step 6: Starting ImmuDB (source, temporary)..."
systemctl start immudb
log_ok "immudb service started"

################################################################################
# Step 7 — Wait for ImmuDB to be healthy
################################################################################

echo ""
log_info "Step 7: Waiting for ImmuDB to be ready..."

RETRIES=0
until systemctl is-active --quiet immudb && \
      (immuadmin login immudb --password immudb 2>/dev/null \
       || nc -z 127.0.0.1 3322 2>/dev/null); do
    RETRIES=$((RETRIES + 1))
    if [ "${RETRIES}" -ge "${IMMUDB_READY_RETRIES}" ]; then
        log_die "ImmuDB did not become ready after ${IMMUDB_READY_RETRIES} attempts."
    fi
    log_info "  waiting... (${RETRIES}/${IMMUDB_READY_RETRIES})"
    sleep 3
done
log_ok "ImmuDB is ready"

################################################################################
# Step 8 — Ensure ThebeDB infra is up
################################################################################

echo ""
log_info "Step 8: Preparing ThebeDB infrastructure..."

# KV directory
mkdir -p "${THEBE_KV_PATH}"
log_ok "KV directory ready: ${THEBE_KV_PATH}"

# Postgres — check connectivity
if command -v psql &>/dev/null; then
    if psql "${THEBE_SQL_DSN}" -c "SELECT 1;" &>/dev/null 2>&1; then
        log_ok "Postgres connection verified"
    else
        log_warn "Cannot connect to Postgres at ${THEBE_SQL_DSN}"
        log_warn "Ensure Postgres is running and the DB/user exists."
        log_warn "Run: sudo ./Scripts/setup_postgres.sh to set it up."
        read -r -p "Continue anyway? (y/N): " cont
        [[ "${cont}" == "y" ]] || log_die "Aborted."
    fi
else
    log_warn "psql not found — cannot verify Postgres. Continuing..."
fi

################################################################################
# Step 9 — Run migration
################################################################################

echo ""
log_info "Step 9: Running migration (ImmuDB → ThebeDB)..."
log_info "  Source: ImmuDB @ 127.0.0.1:3322"
log_info "  Dest KV:  ${THEBE_KV_PATH}"
log_info "  Dest SQL: ${THEBE_SQL_DSN}"
echo ""

if [ "${MIGRATION_USE_PYTHON}" = "true" ]; then
    MIGRATION_ARGS=(
        "--pg-dsn=${THEBE_SQL_DSN}"
        "--start-block=${START_BLOCK}"
        "--batch-size=${BATCH_SIZE}"
    )
    [ "${SKIP_BLOCKS}" = "true" ]    && MIGRATION_ARGS+=("--skip-blocks")
    [ "${SKIP_ACCOUNTS}" = "true" ]  && MIGRATION_ARGS+=("--skip-accounts")

    python3 "${MIGRATION_BIN}" "${MIGRATION_ARGS[@]}"
    MIGRATION_EXIT=$?
    RESUME_CMD="python3 ${MIGRATION_BIN} --pg-dsn='${THEBE_SQL_DSN}' --start-block=<last_good_block>"
else
    MIGRATION_ARGS=(
        "--thebe-kv-path=${THEBE_KV_PATH}"
        "--thebe-sql-dsn=${THEBE_SQL_DSN}"
        "--start-block=${START_BLOCK}"
        "--batch-size=${BATCH_SIZE}"
    )
    [ "${SKIP_BLOCKS}" = "true" ]    && MIGRATION_ARGS+=("--skip-blocks")
    [ "${SKIP_ACCOUNTS}" = "true" ]  && MIGRATION_ARGS+=("--skip-accounts")

    "${MIGRATION_BIN}" "${MIGRATION_ARGS[@]}"
    MIGRATION_EXIT=$?
    RESUME_CMD="${MIGRATION_BIN} --thebe-kv-path=${THEBE_KV_PATH} --thebe-sql-dsn='${THEBE_SQL_DSN}' --start-block=<last_good_block>"
fi

if [ "${MIGRATION_EXIT}" -ne 0 ]; then
    log_error "Migration exited with code ${MIGRATION_EXIT}."
    log_error "Fix the error then resume with:"
    log_error "  ${RESUME_CMD}"
    log_die "Migration failed."
fi
log_ok "Migration complete"

################################################################################
# Step 10 — Stop ImmuDB (no longer needed)
################################################################################

echo ""
log_info "Step 10: Stopping ImmuDB (no longer needed)..."
systemctl stop immudb
systemctl disable immudb 2>/dev/null || true
log_ok "ImmuDB stopped and disabled"

################################################################################
# Step 11 — Start jmdn (ThebeDB mode)
################################################################################

if [ "${NO_START}" = "true" ]; then
    log_info "Step 11: --no-start set — skipping jmdn start."
    log_info "Start manually: systemctl start jmdn"
else
    echo ""
    log_info "Step 11: Starting jmdn..."
    systemctl start jmdn
    log_ok "jmdn started"

    ################################################################################
    # Step 12 — Tail logs
    ################################################################################

    echo ""
    log_info "Step 12: Tailing logs (ctrl-c to stop)..."
    journalctl -u jmdn -f -n 200 --output=short-precise
fi
