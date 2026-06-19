#!/usr/bin/env bash
################################################################################
# setup_postgres.sh — Interactive Postgres setup for JMDN / ThebeDB
#
# Handles:
#   1. Create DB user + database
#   2. Set password
#   3. Patch postgresql.conf: wal_level=logical, max_replication_slots, max_wal_senders
#   4. Create CDC publication (thebe_pub) + replication slot (thebe_eventlog)
#   5. Print DSN + jmdn.yaml snippet
#
# Modes:
#   --mode local   — connects via psql to a local Postgres instance
#   --mode docker  — connects via docker exec to jmdn-postgres container
#   (no flag)      — prompts
#
# Usage:
#   sudo ./Scripts/setup_postgres.sh
#   sudo ./Scripts/setup_postgres.sh --mode local
#        ./Scripts/setup_postgres.sh --mode docker   # (no sudo needed for docker)
################################################################################

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/platform.sh"

# ── Defaults ──────────────────────────────────────────────────────────────────
SETUP_MODE=""
DB_USER="jmdn"
DB_PASS=""
DB_NAME="jmdn"
DB_HOST="127.0.0.1"
DB_PORT="5432"
PG_SUPERUSER="postgres"
CDC_SLOT="thebe_eventlog"
CDC_PUB="thebe_pub"
CDC_LOG_PATH=""   # resolved after platform.sh sets JMDN_DATA
DOCKER_CONTAINER="jmdn-postgres"
SKIP_WAL_PATCH="false"

# ── Parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --mode=local)  SETUP_MODE="local";  shift ;;
        --mode=docker) SETUP_MODE="docker"; shift ;;
        --mode)        SETUP_MODE="${2:-}"; shift 2 ;;
        --no-wal-patch) SKIP_WAL_PATCH="true"; shift ;;
        *) shift ;;
    esac
done

# ── Helpers ───────────────────────────────────────────────────────────────────

prompt() {
    local var_name="$1"
    local prompt_text="$2"
    local default="${3:-}"
    local secret="${4:-false}"

    if [ -n "${default}" ]; then
        local display_default
        if [ "${secret}" = "true" ]; then
            display_default="[hidden]"
        else
            display_default="${default}"
        fi
        read -r -p "${prompt_text} [${display_default}]: " input
    else
        read -r -p "${prompt_text}: " input
    fi
    printf -v "${var_name}" '%s' "${input:-${default}}"
}

prompt_password() {
    local var_name="$1"
    local prompt_text="$2"
    local val=""
    local val2="x"
    while [ "${val}" != "${val2}" ] || [ -z "${val}" ]; do
        read -r -s -p "${prompt_text}: " val; echo
        read -r -s -p "Confirm password: " val2; echo
        if [ -z "${val}" ]; then
            echo "  Password cannot be empty."
        elif [ "${val}" != "${val2}" ]; then
            echo "  Passwords do not match. Try again."
        fi
    done
    printf -v "${var_name}" '%s' "${val}"
}

# Run SQL as the Postgres superuser.
# In docker mode we connect to DB_NAME (not 'postgres') because the image only
# creates the database named by POSTGRES_DB — a bare 'postgres' DB may not exist.
run_sql() {
    local sql="$1"
    if [ "${SETUP_MODE}" = "docker" ]; then
        docker exec "${DOCKER_CONTAINER}" psql -U "${PG_SUPERUSER}" -d "${DB_NAME}" -c "${sql}"
    else
        psql -U "${PG_SUPERUSER}" -h "${DB_HOST}" -p "${DB_PORT}" -d postgres -c "${sql}"
    fi
}

# Run SQL as the app user against the app database
run_sql_as_user() {
    local sql="$1"
    if [ "${SETUP_MODE}" = "docker" ]; then
        docker exec "${DOCKER_CONTAINER}" psql -U "${DB_USER}" -d "${DB_NAME}" -c "${sql}"
    else
        psql -U "${DB_USER}" -h "${DB_HOST}" -p "${DB_PORT}" -d "${DB_NAME}" -c "${sql}"
    fi
}

# Patch a postgresql.conf key; appends if not found
patch_pg_conf() {
    local conf_path="$1"
    local key="$2"
    local value="$3"

    if grep -qE "^#*\s*${key}\s*=" "${conf_path}"; then
        sed_inplace "s|^#*\s*${key}\s*=.*|${key} = ${value}|" "${conf_path}"
    else
        echo "${key} = ${value}" >> "${conf_path}"
    fi
    log_ok "  ${key} = ${value}"
}

# ── Mode selection ────────────────────────────────────────────────────────────

if [ -z "${SETUP_MODE}" ]; then
    echo ""
    echo "Select Postgres connection mode:"
    echo "  1) local   — connect to a local Postgres instance"
    echo "  2) docker  — connect to the jmdn-postgres Docker container"
    echo ""
    read -r -p "Enter choice [1/2]: " choice
    case "${choice}" in
        1|local)  SETUP_MODE="local"  ;;
        2|docker) SETUP_MODE="docker" ;;
        *) log_die "Invalid choice." ;;
    esac
fi

echo ""
log_info "Setup mode: ${SETUP_MODE}"

# ── Gather connection params ──────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Step 1: Connection"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if [ "${SETUP_MODE}" = "local" ]; then
    prompt DB_HOST   "Postgres host"          "${DB_HOST}"
    prompt DB_PORT   "Postgres port"          "${DB_PORT}"
    # macOS brew installs Postgres with the current user as superuser, not 'postgres'
    local default_super="${PG_SUPERUSER}"
    [ "${PLATFORM}" = "macos" ] && default_super="$(whoami)"
    prompt PG_SUPERUSER "Superuser name"      "${default_super}"
    log_info "Connecting as superuser '${PG_SUPERUSER}' — you may be prompted for its password."
elif [ "${SETUP_MODE}" = "docker" ]; then
    prompt DOCKER_CONTAINER "Docker container name" "${DOCKER_CONTAINER}"
    if ! docker inspect "${DOCKER_CONTAINER}" &>/dev/null; then
        log_die "Container '${DOCKER_CONTAINER}' not found. Start it first: cd \${JMDN_DATA}/docker && docker compose up -d"
    fi
    log_ok "Container '${DOCKER_CONTAINER}' found."
    # The official postgres image creates a superuser named after POSTGRES_USER (not always 'postgres').
    # Read it directly from the container so we connect with the right role.
    PG_SUPERUSER="$(docker exec "${DOCKER_CONTAINER}" printenv POSTGRES_USER 2>/dev/null || echo "postgres")"
    DB_NAME="$(docker exec "${DOCKER_CONTAINER}" printenv POSTGRES_DB 2>/dev/null || echo "${DB_NAME}")"
    log_info "Detected container superuser: '${PG_SUPERUSER}', default DB: '${DB_NAME}'"
fi

# ── Gather DB credentials ─────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Step 2: Database user + name"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

prompt DB_USER "DB username" "${DB_USER}"
prompt DB_NAME "DB name"     "${DB_NAME}"
prompt_password DB_PASS "Password for '${DB_USER}'"

# ── CDC config ────────────────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Step 3: CDC (Change Data Capture)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

prompt CDC_PUB  "Publication name" "${CDC_PUB}"
prompt CDC_SLOT "Replication slot name (leave empty to skip slot creation)" "${CDC_SLOT}"
prompt CDC_LOG_PATH "DuckDB event log directory" "${JMDN_DATA}/eventlog"
# Append filename if user gave a plain directory path (no .duckdb extension)
if [[ "${CDC_LOG_PATH}" != *.duckdb ]]; then
    CDC_LOG_PATH="${CDC_LOG_PATH%/}/eventlog.duckdb"
fi
CDC_LOG_DIR="$(dirname "${CDC_LOG_PATH}")"
mkdir -p "${CDC_LOG_DIR}" && log_ok "Event log directory ready: ${CDC_LOG_DIR}"

# ── WAL config (local only) ───────────────────────────────────────────────────

PG_CONF_PATH=""
if [ "${SETUP_MODE}" = "local" ] && [ "${SKIP_WAL_PATCH}" = "false" ]; then
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " Step 4: postgresql.conf (WAL / CDC settings)"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "  CDC requires wal_level=logical, max_replication_slots≥1, max_wal_senders≥1."
    echo "  Provide the path to postgresql.conf, or leave empty to skip (patch manually)."
    echo ""

    # Try to auto-detect postgresql.conf across all supported platforms
    local_auto=""
    if [ "${PLATFORM}" = "macos" ] && command -v brew &>/dev/null; then
        # macOS Homebrew
        for pg_ver in 16 15 14; do
            candidate="$(brew --prefix)/var/postgresql@${pg_ver}/postgresql.conf"
            [ -f "${candidate}" ] && { local_auto="${candidate}"; break; }
        done
    elif [ "${PLATFORM}" = "freebsd" ]; then
        # FreeBSD pkg
        candidate="/usr/local/etc/postgresql/postgresql.conf"
        [ -f "${candidate}" ] && local_auto="${candidate}"
    elif [ "${PLATFORM}" = "linux" ]; then
        # Debian/Ubuntu (pg_lsclusters)
        PG_VER=$(pg_lsclusters -h 2>/dev/null | awk '{print $1}' | head -1 || true)
        PG_CLUSTER=$(pg_lsclusters -h 2>/dev/null | awk '{print $2}' | head -1 || true)
        if [ -n "${PG_VER}" ] && [ -n "${PG_CLUSTER}" ]; then
            candidate="/etc/postgresql/${PG_VER}/${PG_CLUSTER}/postgresql.conf"
            [ -f "${candidate}" ] && local_auto="${candidate}"
        fi
        # Alpine / generic Linux — data dir
        if [ -z "${local_auto}" ]; then
            for candidate in \
                "/var/lib/postgresql/data/postgresql.conf" \
                "/etc/postgresql/postgresql.conf"; do
                [ -f "${candidate}" ] && { local_auto="${candidate}"; break; }
            done
        fi
    fi

    prompt PG_CONF_PATH "Path to postgresql.conf" "${local_auto}"
fi

# ── Execute ───────────────────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Applying configuration..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 1. WAL patch (local mode, if path provided)
NEEDS_RESTART=false
if [ "${SETUP_MODE}" = "local" ] && [ -n "${PG_CONF_PATH}" ] && [ -f "${PG_CONF_PATH}" ]; then
    log_info "Patching ${PG_CONF_PATH}..."
    patch_pg_conf "${PG_CONF_PATH}" "wal_level"              "logical"
    patch_pg_conf "${PG_CONF_PATH}" "max_replication_slots"  "4"
    patch_pg_conf "${PG_CONF_PATH}" "max_wal_senders"        "4"
    NEEDS_RESTART=true
    log_warn "Postgres must be restarted for WAL changes to take effect."
    read -r -p "Restart Postgres now? (y/N): " do_restart
    if [[ "${do_restart}" == "y" ]]; then
        case "${SVC_MANAGER}" in
            systemd) systemctl restart postgresql ;;
            launchd) brew services restart postgresql@16 ;;
            openrc)  rc-service postgresql restart ;;
            rcd)     service postgresql restart ;;
            *) log_warn "Cannot auto-restart — restart Postgres manually." ;;
        esac
        log_ok "Postgres restarted."
        NEEDS_RESTART=false
        sleep 2
    fi
elif [ "${SETUP_MODE}" = "docker" ]; then
    log_info "Docker mode: wal_level=logical is set in docker-compose.yml — no manual patch needed."
fi

# 2. Create user
log_info "Creating user '${DB_USER}'..."
run_sql "DO \$\$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${DB_USER}') THEN
    CREATE ROLE \"${DB_USER}\" LOGIN REPLICATION PASSWORD '${DB_PASS}';
  ELSE
    ALTER ROLE \"${DB_USER}\" WITH LOGIN REPLICATION PASSWORD '${DB_PASS}';
  END IF;
END\$\$;" && log_ok "User '${DB_USER}' ready."

# 3. Create database
log_info "Creating database '${DB_NAME}'..."
run_sql "SELECT 'exists' FROM pg_database WHERE datname = '${DB_NAME}';" | grep -q exists \
    && log_warn "Database '${DB_NAME}' already exists — skipping CREATE." \
    || (run_sql "CREATE DATABASE \"${DB_NAME}\" OWNER \"${DB_USER}\";" && log_ok "Database '${DB_NAME}' created.")

# 4. Grant privileges
log_info "Granting privileges..."
run_sql "GRANT ALL PRIVILEGES ON DATABASE \"${DB_NAME}\" TO \"${DB_USER}\";"
log_ok "Grants applied."

# 5. Create publication
log_info "Creating publication '${CDC_PUB}'..."
if [ "${SETUP_MODE}" = "docker" ]; then
    docker exec "${DOCKER_CONTAINER}" psql -U "${DB_USER}" -d "${DB_NAME}" \
        -c "CREATE PUBLICATION \"${CDC_PUB}\" FOR ALL TABLES;" 2>/dev/null \
        && log_ok "Publication '${CDC_PUB}' created." \
        || log_warn "Publication '${CDC_PUB}' already exists — skipping."
else
    psql -U "${DB_USER}" -h "${DB_HOST}" -p "${DB_PORT}" -d "${DB_NAME}" \
        -c "CREATE PUBLICATION \"${CDC_PUB}\" FOR ALL TABLES;" 2>/dev/null \
        && log_ok "Publication '${CDC_PUB}' created." \
        || log_warn "Publication '${CDC_PUB}' already exists — skipping."
fi

# 6. Create replication slot (optional)
if [ -n "${CDC_SLOT}" ]; then
    log_info "Creating replication slot '${CDC_SLOT}' (pgoutput)..."
    if [ "${SETUP_MODE}" = "docker" ]; then
        docker exec "${DOCKER_CONTAINER}" psql -U "${PG_SUPERUSER}" -d "${DB_NAME}" \
            -c "SELECT pg_create_logical_replication_slot('${CDC_SLOT}', 'pgoutput');" 2>/dev/null \
            && log_ok "Replication slot '${CDC_SLOT}' created." \
            || log_warn "Slot '${CDC_SLOT}' may already exist — skipping."
    else
        psql -U "${PG_SUPERUSER}" -h "${DB_HOST}" -p "${DB_PORT}" -d "${DB_NAME}" \
            -c "SELECT pg_create_logical_replication_slot('${CDC_SLOT}', 'pgoutput');" 2>/dev/null \
            && log_ok "Replication slot '${CDC_SLOT}' created." \
            || log_warn "Slot '${CDC_SLOT}' may already exist — skipping."
    fi
else
    log_info "Skipping replication slot creation."
fi

# ── Print result ──────────────────────────────────────────────────────────────

if [ "${SETUP_MODE}" = "docker" ]; then
    FINAL_HOST="127.0.0.1"
    FINAL_PORT="5430"  # docker-compose maps 5430→5432
else
    FINAL_HOST="${DB_HOST}"
    FINAL_PORT="${DB_PORT}"
fi

DSN="postgres://${DB_USER}:${DB_PASS}@${FINAL_HOST}:${FINAL_PORT}/${DB_NAME}?sslmode=disable"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Done. Add this to jmdn.yaml:"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "thebe:"
echo "  enabled: true"
echo "  sql_dsn: \"${DSN}\""
echo "  redis_url: \"redis://127.0.0.1:6379\""
if [ -n "${CDC_SLOT}" ]; then
    echo "  cdc:"
    echo "    enabled: true"
    echo "    slot_name: ${CDC_SLOT}"
    echo "    publication: ${CDC_PUB}"
    echo "    log_path: ${CDC_LOG_PATH}"
fi
echo ""

if [ "${NEEDS_RESTART}" = "true" ]; then
    log_warn "Postgres WAL settings were patched but NOT yet applied — restart Postgres before starting jmdn."
fi
