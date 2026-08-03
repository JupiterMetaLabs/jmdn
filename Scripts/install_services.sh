#!/usr/bin/env bash
################################################################################
# install_services.sh - Cross-Platform Service Installation for JMDN
#
# CHANGELOG:
# v4.0 (2026-06-19): Dual-mode: local (native) + docker
#   - --mode local  : install Postgres + Redis natively, register system services
#   - --mode docker : generate docker-compose.yml, start containers, register JMDN
#   - Removed ImmuDB entirely (replaced by ThebeDB/Postgres)
#   - DuckDB is embedded — no daemon required
# v3.0 (2026-06-19): Replace ImmuDB with Postgres + Redis
# v2.0 (2026-03-02): Cross-platform support (systemd/launchd/openrc/rc.d)
#
# USAGE:
#   sudo ./install_services.sh --mode local    # native install
#   sudo ./install_services.sh --mode docker   # docker-compose install
#   sudo ./install_services.sh                 # prompts for mode
# ============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/platform.sh"

APP_NAME="jmdn"
SERVICE_USER="${SERVICE_USER:-root}"
INSTALL_MODE=""

# ── Parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --mode=local|--mode=docker) INSTALL_MODE="${1#--mode=}"; shift ;;
        --mode) INSTALL_MODE="${2:-}"; shift 2 ;;
        *) shift ;;
    esac
done

if [ -z "${INSTALL_MODE}" ]; then
    echo ""
    echo "Select installation mode:"
    echo "  1) local   — install Postgres + Redis natively, register system services"
    echo "  2) docker  — run Postgres + Redis in Docker containers"
    echo ""
    read -r -p "Enter choice [1/2]: " choice
    case "${choice}" in
        1|local)  INSTALL_MODE="local"  ;;
        2|docker) INSTALL_MODE="docker" ;;
        *) echo "Invalid choice. Use 1 (local) or 2 (docker)."; exit 1 ;;
    esac
fi

require_root

################################################################################
# Common: directories + binary
################################################################################

log_info "Creating directories..."
ensure_dir "${JMDN_DATA}" "755"
ensure_dir "${JMDN_DATA}/data" "755"
ensure_dir "${JMDN_DATA}/config" "755"
ensure_dir "${JMDN_LOG}" "755"
ensure_dir "${JMDN_ETC}" "755"

if [ ! -f "${JMDN_ETC}/jmdn.yaml" ]; then
    log_warn "Config not found at ${JMDN_ETC}/jmdn.yaml"
    log_warn "Copy jmdn_default.yaml to ${JMDN_ETC}/jmdn.yaml before starting."
fi

log_info "Installing binaries to ${JMDN_BIN}..."
for wrapper in "Scripts/start_jmdn_wrapper.sh" "start_jmdn_wrapper.sh"; do
    if [ -f "${wrapper}" ]; then
        cp "${wrapper}" "${JMDN_BIN}/start_jmdn_wrapper.sh"
        chmod +x "${JMDN_BIN}/start_jmdn_wrapper.sh"
        log_ok "Installed start_jmdn_wrapper.sh"
        break
    fi
done
if [ -f "${APP_NAME}" ]; then
    cp "${APP_NAME}" "${JMDN_BIN}/${APP_NAME}"
    chmod +x "${JMDN_BIN}/${APP_NAME}"
    log_ok "Installed ${APP_NAME} binary"
else
    log_warn "${APP_NAME} binary not found — run 'make build' first."
fi

################################################################################
# MODE: DOCKER
################################################################################

install_docker() {
    log_info "Installing in Docker mode..."

    # Configure Journald Limits to prevent disk exhaustion
    log_info "Configuring journald log limits..."
    mkdir -p /etc/systemd/journald.conf.d
    cat > /etc/systemd/journald.conf.d/jmdn-limits.conf <<EOF
[Journal]
SystemMaxUse=${JMDN_JOURNALD_MAX_USE:-5G}
MaxRetentionSec=${JMDN_JOURNALD_MAX_RETENTION:-30d}
EOF
    systemctl restart systemd-journald
    log_ok "Applied journald limits (Max: ${JMDN_JOURNALD_MAX_USE:-5G}, Retention: ${JMDN_JOURNALD_MAX_RETENTION:-30d})"

    # Verify docker + compose are available
    if ! command -v docker &>/dev/null; then
        log_die "Docker not found. Install Docker Desktop or Docker Engine first."
    fi
    if ! docker compose version &>/dev/null 2>&1; then
        log_die "Docker Compose v2 not found. Update Docker or install the compose plugin."
    fi

    local compose_dir="${JMDN_DATA}/docker"
    ensure_dir "${compose_dir}" "755"
    local compose_file="${compose_dir}/docker-compose.yml"
    local docker_bin
    docker_bin="$(command -v docker)"

    log_info "Writing ${compose_file}..."
    cat > "${compose_file}" <<'COMPOSE'
# JMDN infrastructure — Postgres + Redis
# Postgres is configured for logical replication (required for CDC/ThebeDB WAL consumer).
# DuckDB is embedded in the jmdn binary — no container needed.

services:
  postgres:
    image: postgres:16-alpine
    container_name: jmdn-postgres
    restart: unless-stopped
    environment:
      POSTGRES_USER: jmdn
      POSTGRES_PASSWORD: jmdndefault
      POSTGRES_DB: jmdn
    # wal_level=logical is required for ThebeDB CDC (WAL consumer).
    command: >
      postgres
        -c wal_level=logical
        -c max_replication_slots=4
        -c max_wal_senders=4
    ports:
      - "5430:5432"
    volumes:
      - jmdn_postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U jmdn -d jmdn"]
      interval: 10s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    container_name: jmdn-redis
    restart: unless-stopped
    command: redis-server --appendonly yes
    ports:
      - "6379:6379"
    volumes:
      - jmdn_redis:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5

volumes:
  jmdn_postgres:
  jmdn_redis:
COMPOSE
    log_ok "Created ${compose_file}"

    log_info "Starting containers..."
    docker compose -f "${compose_file}" up -d
    log_ok "Postgres (port 5430) and Redis (port 6379) are running."

    # One-time DB setup: create publication for CDC
    log_info "Waiting for Postgres to be ready..."
    local retries=0
    until docker exec jmdn-postgres pg_isready -U jmdn -d jmdn &>/dev/null; do
        retries=$((retries + 1))
        if [ "${retries}" -ge 30 ]; then
            log_die "Postgres did not become ready in time."
        fi
        sleep 2
    done
    log_info "Creating Postgres publication for CDC (thebe_pub)..."
    docker exec jmdn-postgres psql -U jmdn -d jmdn \
        -c "CREATE PUBLICATION thebe_pub FOR ALL TABLES;" 2>/dev/null \
        || log_warn "thebe_pub already exists — skipping."
    log_ok "CDC publication ready."

    # Register JMDN as a system service (depends on docker being up)
    install_jmdn_service_docker
}

# Register JMDN as a system service that starts after docker
install_jmdn_service_docker() {
    local compose_dir="${JMDN_DATA}/docker"
    local compose_file="${compose_dir}/docker-compose.yml"

    case "${SVC_MANAGER}" in
    systemd)
        cat > /etc/systemd/system/${APP_NAME}.service <<EOF
[Unit]
Description=JMDT Decentralized Network Node (jmdn)
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=${SERVICE_USER}
WorkingDirectory=${JMDN_DATA}
Environment="WORK_DIR=${JMDN_DATA}"
Environment="DATA_DIR=${JMDN_DATA}/data"
ExecStartPre=${docker_bin} compose -f ${compose_file} up -d
ExecStart=${JMDN_BIN}/start_jmdn_wrapper.sh
ExecStopPost=${docker_bin} compose -f ${compose_file} stop
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=${APP_NAME}
LimitNOFILE=65536
LimitNPROC=32768

[Install]
WantedBy=multi-user.target
EOF
        svc_reload_daemon
        svc_enable "${APP_NAME}"
        log_ok "Created systemd ${APP_NAME}.service (docker mode)"
        ;;

    launchd)
        local launchd_dir
        [ "${IS_ROOT}" = true ] && launchd_dir="/Library/LaunchDaemons" \
                                || launchd_dir="${HOME}/Library/LaunchAgents"
        ensure_dir "${launchd_dir}" "755"
        cat > "${launchd_dir}/com.jmdn.jmdn.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>           <string>com.jmdn.jmdn</string>
    <key>ProgramArguments</key>
    <array>
        <string>${JMDN_BIN}/start_jmdn_wrapper.sh</string>
    </array>
    <key>WorkingDirectory</key> <string>${JMDN_DATA}</string>
    <key>RunAtLoad</key>        <true/>
    <key>KeepAlive</key>        <true/>
    <key>StandardOutPath</key>  <string>${JMDN_LOG}/jmdn.log</string>
    <key>StandardErrorPath</key><string>${JMDN_LOG}/jmdn.err</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>WORK_DIR</key>  <string>${JMDN_DATA}</string>
        <key>DATA_DIR</key>  <string>${JMDN_DATA}/data</string>
    </dict>
</dict>
</plist>
PLIST
        chmod 644 "${launchd_dir}/com.jmdn.jmdn.plist"
        launchctl load "${launchd_dir}/com.jmdn.jmdn.plist" || true
        log_ok "Created launchd com.jmdn.jmdn.plist (docker mode)"
        ;;

    openrc)
        cat > /etc/init.d/jmdn <<OPENRC
#!/sbin/openrc-run
description="JMDT Decentralized Network Node (jmdn)"
command="${JMDN_BIN}/start_jmdn_wrapper.sh"
command_background="true"
pidfile="/var/run/jmdn.pid"
directory="${JMDN_DATA}"
depend() { need net docker; }
start_pre() { docker compose -f ${compose_file} up -d; }
OPENRC
        chmod 755 /etc/init.d/jmdn
        rc-update add jmdn boot
        log_ok "Created OpenRC jmdn service (docker mode)"
        ;;

    rcd)
        sysrc jmdn_enable=YES
        cat > /usr/local/etc/rc.d/jmdn <<RCD
#!/bin/sh
# PROVIDE: jmdn
# REQUIRE: NETWORKING docker
# KEYWORD: shutdown
. /etc/rc.subr
name="jmdn"; rcvar="jmdn_enable"
pidfile="/var/run/\${name}.pid"
load_rc_config \${name}
: \${jmdn_enable:="NO"}
command="${JMDN_BIN}/start_jmdn_wrapper.sh"
start_precmd="docker compose -f ${compose_file} up -d"
run_rc_command "\$1"
RCD
        chmod 755 /usr/local/etc/rc.d/jmdn
        log_ok "Created rc.d jmdn service (docker mode)"
        ;;
    esac
}

################################################################################
# MODE: LOCAL (native)
################################################################################

install_local() {
    log_info "Installing in local (native) mode..."

    case "${SVC_MANAGER}" in
    systemd)   install_local_systemd ;;
    launchd)   install_local_launchd ;;
    openrc)    install_local_openrc  ;;
    rcd)       install_local_rcd     ;;
    *) log_die "Unsupported service manager: ${SVC_MANAGER}" ;;
    esac
}

install_local_systemd() {
    # Install Postgres
    if command -v psql &>/dev/null; then
        log_ok "PostgreSQL already installed"
    else
        log_info "Installing PostgreSQL (apt)..."
        apt-get update -qq && apt-get install -y postgresql postgresql-client
    fi
    # Enable + configure wal_level=logical for CDC
    PG_VER=$(pg_lsclusters -h 2>/dev/null | awk '{print $1}' | head -1 || ls /usr/lib/postgresql/ 2>/dev/null | sort -V | tail -1)
    PG_CLUSTER=$(pg_lsclusters -h 2>/dev/null | awk '{print $2}' | head -1 || echo "main")
    PG_CONF="/etc/postgresql/${PG_VER}/${PG_CLUSTER}/postgresql.conf"
    if [ -f "${PG_CONF}" ]; then
        sed -i "s/^#*wal_level\s*=.*/wal_level = logical/" "${PG_CONF}" \
            || echo "wal_level = logical" >> "${PG_CONF}"
        log_ok "wal_level=logical set in ${PG_CONF} (restart postgres to apply)"
    fi
    systemctl enable postgresql
    systemctl start postgresql || true
    log_ok "PostgreSQL enabled"

    # Create publication for CDC (after postgres is up)
    sleep 2
    sudo -u postgres psql -c "CREATE PUBLICATION thebe_pub FOR ALL TABLES;" 2>/dev/null \
        || log_warn "thebe_pub already exists — skipping."

    # Install Redis
    if command -v redis-server &>/dev/null; then
        log_ok "Redis already installed"
    else
        log_info "Installing Redis (apt)..."
        apt-get update -qq && apt-get install -y redis-server
    fi
    svc_reload_daemon
    local redis_unit
    redis_unit=$(systemctl list-unit-files | grep -E "^redis" | awk '{print $1}' | head -1)
    redis_unit="${redis_unit:-redis-server.service}"
    systemctl enable "${redis_unit}"
    systemctl start "${redis_unit}" || true
    log_ok "Redis enabled (${redis_unit})"

    # JMDN service
    cat > /etc/systemd/system/${APP_NAME}.service <<EOF
[Unit]
Description=JMDT Decentralized Network Node (jmdn)
After=network.target postgresql.service redis.service
Requires=postgresql.service redis.service

[Service]
Type=simple
User=${SERVICE_USER}
WorkingDirectory=${JMDN_DATA}
Environment="WORK_DIR=${JMDN_DATA}"
Environment="DATA_DIR=${JMDN_DATA}/data"
ExecStart=${JMDN_BIN}/start_jmdn_wrapper.sh
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=${APP_NAME}
LimitNOFILE=65536
LimitNPROC=32768

[Install]
WantedBy=multi-user.target
EOF
    svc_reload_daemon
    svc_enable "${APP_NAME}"
    log_ok "Created systemd ${APP_NAME}.service"
}

install_local_launchd() {
    if ! command -v brew &>/dev/null; then
        log_die "Homebrew not found. Install it first: https://brew.sh"
    fi

    # Install + start Postgres
    brew list postgresql@16 &>/dev/null || brew install postgresql@16
    brew services start postgresql@16
    log_ok "postgresql@16 started via brew services"

    # Set wal_level=logical for CDC
    local pg_data
    pg_data="$(brew --prefix)/var/postgresql@16"
    local pg_conf="${pg_data}/postgresql.conf"
    if [ -f "${pg_conf}" ]; then
        grep -q "^wal_level" "${pg_conf}" \
            && sed -i '' "s/^wal_level.*/wal_level = logical/" "${pg_conf}" \
            || echo "wal_level = logical" >> "${pg_conf}"
        brew services restart postgresql@16
        log_ok "wal_level=logical set — postgresql@16 restarted"
    fi

    # Create publication for CDC
    sleep 3
    "$(brew --prefix)/opt/postgresql@16/bin/psql" -U "$(whoami)" -d postgres \
        -c "CREATE PUBLICATION thebe_pub FOR ALL TABLES;" 2>/dev/null \
        || log_warn "thebe_pub already exists — skipping."

    # Install + start Redis
    brew list redis &>/dev/null || brew install redis
    brew services start redis
    log_ok "redis started via brew services"

    # JMDN launchd plist
    local launchd_dir
    [ "${IS_ROOT}" = true ] && launchd_dir="/Library/LaunchDaemons" \
                            || launchd_dir="${HOME}/Library/LaunchAgents"
    ensure_dir "${launchd_dir}" "755"
    cat > "${launchd_dir}/com.jmdn.jmdn.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>           <string>com.jmdn.jmdn</string>
    <key>ProgramArguments</key>
    <array>
        <string>${JMDN_BIN}/start_jmdn_wrapper.sh</string>
    </array>
    <key>WorkingDirectory</key> <string>${JMDN_DATA}</string>
    <key>RunAtLoad</key>        <true/>
    <key>KeepAlive</key>        <true/>
    <key>StandardOutPath</key>  <string>${JMDN_LOG}/jmdn.log</string>
    <key>StandardErrorPath</key><string>${JMDN_LOG}/jmdn.err</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>WORK_DIR</key>  <string>${JMDN_DATA}</string>
        <key>DATA_DIR</key>  <string>${JMDN_DATA}/data</string>
    </dict>
</dict>
</plist>
PLIST
    chmod 644 "${launchd_dir}/com.jmdn.jmdn.plist"
    launchctl load "${launchd_dir}/com.jmdn.jmdn.plist" || true
    log_ok "Created launchd com.jmdn.jmdn.plist"
}

install_local_openrc() {
    # Install Postgres
    if ! command -v psql &>/dev/null; then
        apk add --no-cache postgresql postgresql-client
    fi
    rc-update add postgresql boot
    rc-service postgresql start || true
    # wal_level for CDC — Alpine stores data under /var/lib/postgresql/data/
    local pg_conf=""
    for candidate in \
        "/var/lib/postgresql/data/postgresql.conf" \
        "/etc/postgresql/postgresql.conf"; do
        [ -f "${candidate}" ] && { pg_conf="${candidate}"; break; }
    done
    if [ -n "${pg_conf}" ]; then
        grep -q "^wal_level" "${pg_conf}" \
            && sed -i "s/^wal_level.*/wal_level = logical/" "${pg_conf}" \
            || echo "wal_level = logical" >> "${pg_conf}"
        grep -q "^max_replication_slots" "${pg_conf}" \
            && sed -i "s/^max_replication_slots.*/max_replication_slots = 4/" "${pg_conf}" \
            || echo "max_replication_slots = 4" >> "${pg_conf}"
        grep -q "^max_wal_senders" "${pg_conf}" \
            && sed -i "s/^max_wal_senders.*/max_wal_senders = 4/" "${pg_conf}" \
            || echo "max_wal_senders = 4" >> "${pg_conf}"
        rc-service postgresql restart || true
        log_ok "WAL settings patched in ${pg_conf}"
    else
        log_warn "postgresql.conf not found — patch wal_level=logical manually before enabling CDC."
    fi
    sleep 2
    psql -U postgres -c "CREATE PUBLICATION thebe_pub FOR ALL TABLES;" 2>/dev/null \
        || log_warn "thebe_pub already exists — skipping."
    log_ok "PostgreSQL enabled"

    # Install Redis
    if ! command -v redis-server &>/dev/null; then
        apk add --no-cache redis
    fi
    rc-update add redis boot
    rc-service redis start || true
    log_ok "Redis enabled"

    # JMDN OpenRC service
    cat > /etc/init.d/jmdn <<OPENRC
#!/sbin/openrc-run
description="JMDT Decentralized Network Node (jmdn)"
command="${JMDN_BIN}/start_jmdn_wrapper.sh"
command_background="true"
pidfile="/var/run/jmdn.pid"
directory="${JMDN_DATA}"
depend() { need net postgresql redis; }
start_pre() { checkpath -d -m 0755 "\$directory"; }
OPENRC
    chmod 755 /etc/init.d/jmdn
    rc-update add jmdn boot
    log_ok "Created OpenRC jmdn service"
}

install_local_rcd() {
    # Postgres + Redis assumed installed via pkg; just enable them
    if [ -f "/usr/local/etc/rc.d/postgresql" ]; then
        sysrc postgresql_enable=YES
        service postgresql start || true
        # WAL patch for CDC — FreeBSD path
        local pg_conf="/usr/local/etc/postgresql/postgresql.conf"
        if [ -f "${pg_conf}" ]; then
            grep -q "^wal_level" "${pg_conf}" \
                && sed -i '' "s/^wal_level.*/wal_level = logical/" "${pg_conf}" \
                || echo "wal_level = logical" >> "${pg_conf}"
            grep -q "^max_replication_slots" "${pg_conf}" \
                && sed -i '' "s/^max_replication_slots.*/max_replication_slots = 4/" "${pg_conf}" \
                || echo "max_replication_slots = 4" >> "${pg_conf}"
            grep -q "^max_wal_senders" "${pg_conf}" \
                && sed -i '' "s/^max_wal_senders.*/max_wal_senders = 4/" "${pg_conf}" \
                || echo "max_wal_senders = 4" >> "${pg_conf}"
            service postgresql restart || true
            log_ok "WAL settings patched in ${pg_conf}"
        else
            log_warn "${pg_conf} not found — patch wal_level=logical manually before enabling CDC."
        fi
        log_ok "PostgreSQL enabled"
    else
        log_warn "postgresql rc.d not found — install via: pkg install postgresql16-server"
    fi
    if [ -f "/usr/local/etc/rc.d/redis" ]; then
        sysrc redis_enable=YES
        service redis start || true
        log_ok "Redis enabled"
    else
        log_warn "redis rc.d not found — install via: pkg install redis"
    fi

    # JMDN rc.d service
    cat > /usr/local/etc/rc.d/jmdn <<RCD
#!/bin/sh
# PROVIDE: jmdn
# REQUIRE: NETWORKING postgresql redis
# KEYWORD: shutdown
. /etc/rc.subr
name="jmdn"; rcvar="jmdn_enable"
pidfile="/var/run/\${name}.pid"
load_rc_config \${name}
: \${jmdn_enable:="NO"}
: \${jmdn_workdir:="${JMDN_DATA}"}
command="${JMDN_BIN}/start_jmdn_wrapper.sh"
start_cmd="\${name}_start"
stop_cmd="\${name}_stop"
jmdn_start() {
    /usr/sbin/daemon -u root -p \${pidfile} -f -D \${jmdn_workdir} \
        env WORK_DIR="\${jmdn_workdir}" DATA_DIR="\${jmdn_workdir}/data" \
        \${command}
}
jmdn_stop() { [ -f \${pidfile} ] && kill -TERM \$(cat \${pidfile}); }
run_rc_command "\$1"
RCD
    chmod 755 /usr/local/etc/rc.d/jmdn
    sysrc jmdn_enable=YES
    log_ok "Created rc.d jmdn service"
}

################################################################################
# Main
################################################################################

log_info "Detected platform: ${PLATFORM} (service manager: ${SVC_MANAGER})"
log_info "Install mode: ${INSTALL_MODE}"
log_info "Note: DuckDB is embedded — no daemon required."

case "${INSTALL_MODE}" in
    local)  install_local  ;;
    docker) install_docker ;;
    *) log_die "Unknown mode '${INSTALL_MODE}'. Use --mode local or --mode docker." ;;
esac

log_ok "Services installed (mode: ${INSTALL_MODE})"

# ── Run Postgres DB setup ─────────────────────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Next: Postgres DB setup"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "  Creates the jmdn user/database, sets WAL config, and wires CDC."
read -r -p "Run setup_postgres.sh now? (Y/n): " run_pg
if [[ "${run_pg}" != "n" && "${run_pg}" != "N" ]]; then
    bash "${SCRIPT_DIR}/setup_postgres.sh" --mode "${INSTALL_MODE}"
else
    log_info "Skipped. Run manually:"
    log_info "  sudo ${SCRIPT_DIR}/setup_postgres.sh --mode ${INSTALL_MODE}"
fi
