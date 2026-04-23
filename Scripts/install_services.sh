#!/usr/bin/env bash
################################################################################
# install_services.sh - Cross-Platform Service Installation for JMDN
#
# CHANGELOG:
# v2.0 (2026-03-02): Rewritten for cross-platform support
#   - Added support for systemd (Linux)
#   - Added support for launchd (macOS)
#   - Added support for OpenRC (Alpine Linux)
#   - Added support for rc.d (FreeBSD)
#   - Uses platform.sh for platform detection and helpers
#   - Supports platform-specific directory paths
#   - Maintains 100% backward compatibility with systemd path
#
# USAGE: sudo ./install_services.sh
# ============================================================================

set -euo pipefail

# Source platform detection library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/platform.sh"

# Config
APP_NAME="jmdn"
SERVICE_USER="${SERVICE_USER:-root}" # Now supports parameterized users via env variables

# Root check
require_root

# 1. Create Directories
log_info "Creating directories..."
ensure_dir "${JMDN_DATA}" "755"
ensure_dir "${JMDN_DATA}/data" "755"
ensure_dir "${JMDN_DATA}/config" "755"
ensure_dir "${JMDN_DATA}/DB" "755"
ensure_dir "${JMDN_LOG}" "755"
ensure_dir "${JMDN_ETC}" "755"

# 1.1 Check Configuration
if [ ! -f "${JMDN_ETC}/jmdn.yaml" ]; then
    log_warn "Configuration file not found at ${JMDN_ETC}/jmdn.yaml"
    log_warn "Ensure you copy 'jmdn_default.yaml' to '${JMDN_ETC}/jmdn.yaml' or deploy via Ansible."
fi

# 1.5. Copy Binaries
log_info "Installing binaries to ${JMDN_BIN}..."

# Wrapper Script
if [ -f "Scripts/start_jmdn_wrapper.sh" ]; then
    cp "Scripts/start_jmdn_wrapper.sh" "${JMDN_BIN}/start_jmdn_wrapper.sh"
    chmod +x "${JMDN_BIN}/start_jmdn_wrapper.sh"
    log_ok "Installed start_jmdn_wrapper.sh"
elif [ -f "start_jmdn_wrapper.sh" ]; then
    # Fallback if running from proper dir
    cp "start_jmdn_wrapper.sh" "${JMDN_BIN}/start_jmdn_wrapper.sh"
    chmod +x "${JMDN_BIN}/start_jmdn_wrapper.sh"
    log_ok "Installed start_jmdn_wrapper.sh"
else
    log_warn "start_jmdn_wrapper.sh not found in current directory. Please ensure it is present."
fi

# JMDN Binary (Check if built)
if [ -f "${APP_NAME}" ]; then
    cp "${APP_NAME}" "${JMDN_BIN}/${APP_NAME}"
    chmod +x "${JMDN_BIN}/${APP_NAME}"
    log_ok "Installed ${APP_NAME} binary"
else
    log_warn "${APP_NAME} binary not found. Please run build.sh first."
fi

################################################################################
# Service Installation Functions
################################################################################

# Install systemd services (Linux with systemd)
install_systemd_services() {
    log_info "Installing systemd services..."

    # ImmuDB systemd service
    log_info "Creating immudb.service..."
    cat > /etc/systemd/system/immudb.service <<EOF
[Unit]
Description=ImmuDB (Immutable Database)
After=network.target
Documentation=https://docs.immudb.io

[Service]
Type=simple
User=${SERVICE_USER}
ExecStart=${JMDN_BIN}/immudb --dir "${JMDN_DATA}/data"
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=immudb

[Install]
WantedBy=multi-user.target
EOF
    log_ok "Created /etc/systemd/system/immudb.service"

    # JMDN systemd service
    log_info "Creating ${APP_NAME}.service..."
    cat > /etc/systemd/system/${APP_NAME}.service <<EOF
[Unit]
Description=JMDT Decentralized Network Node (jmdn)
After=network.target immudb.service
Requires=immudb.service

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
    log_ok "Created /etc/systemd/system/${APP_NAME}.service"

    svc_reload_daemon
    svc_enable "immudb"
    svc_enable "${APP_NAME}"
}

# Install launchd services (macOS)
install_launchd_services() {
    log_info "Installing launchd services..."

    local launchd_dir
    if [ "${IS_ROOT}" = true ]; then
        launchd_dir="/Library/LaunchDaemons"
    else
        launchd_dir="${HOME}/Library/LaunchAgents"
    fi

    ensure_dir "${launchd_dir}" "755"

    # ImmuDB launchd plist
    log_info "Creating immudb.plist..."
    cat > "${launchd_dir}/com.jmdn.immudb.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.jmdn.immudb</string>
    <key>ProgramArguments</key>
    <array>
EOF
    echo "        <string>${JMDN_BIN}/immudb</string>" >> "${launchd_dir}/com.jmdn.immudb.plist"
    echo "        <string>--dir</string>" >> "${launchd_dir}/com.jmdn.immudb.plist"
    echo "        <string>${JMDN_DATA}/data</string>" >> "${launchd_dir}/com.jmdn.immudb.plist"
    cat >> "${launchd_dir}/com.jmdn.immudb.plist" <<'EOF'
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
EOF
    echo "    <string>${JMDN_LOG}/immudb.log</string>" >> "${launchd_dir}/com.jmdn.immudb.plist"
    cat >> "${launchd_dir}/com.jmdn.immudb.plist" <<'EOF'
    <key>StandardErrorPath</key>
EOF
    echo "    <string>${JMDN_LOG}/immudb.err</string>" >> "${launchd_dir}/com.jmdn.immudb.plist"
    cat >> "${launchd_dir}/com.jmdn.immudb.plist" <<'EOF'
    <key>UserName</key>
    <string>root</string>
</dict>
</plist>
EOF
    chmod 644 "${launchd_dir}/com.jmdn.immudb.plist"
    log_ok "Created ${launchd_dir}/com.jmdn.immudb.plist"

    # JMDN launchd plist
    log_info "Creating jmdn.plist..."
    cat > "${launchd_dir}/com.jmdn.jmdn.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.jmdn.jmdn</string>
    <key>ProgramArguments</key>
    <array>
EOF
    echo "        <string>${JMDN_BIN}/start_jmdn_wrapper.sh</string>" >> "${launchd_dir}/com.jmdn.jmdn.plist"
    cat >> "${launchd_dir}/com.jmdn.jmdn.plist" <<'EOF'
    </array>
    <key>WorkingDirectory</key>
EOF
    echo "    <string>${JMDN_DATA}</string>" >> "${launchd_dir}/com.jmdn.jmdn.plist"
    cat >> "${launchd_dir}/com.jmdn.jmdn.plist" <<'EOF'
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
EOF
    echo "    <string>${JMDN_LOG}/jmdn.log</string>" >> "${launchd_dir}/com.jmdn.jmdn.plist"
    cat >> "${launchd_dir}/com.jmdn.jmdn.plist" <<'EOF'
    <key>StandardErrorPath</key>
EOF
    echo "    <string>${JMDN_LOG}/jmdn.err</string>" >> "${launchd_dir}/com.jmdn.jmdn.plist"
    cat >> "${launchd_dir}/com.jmdn.jmdn.plist" <<'EOF'
    <key>UserName</key>
    <string>root</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>WORK_DIR</key>
EOF
    echo "        <string>${JMDN_DATA}</string>" >> "${launchd_dir}/com.jmdn.jmdn.plist"
    cat >> "${launchd_dir}/com.jmdn.jmdn.plist" <<'EOF'
        <key>DATA_DIR</key>
EOF
    echo "        <string>${JMDN_DATA}/data</string>" >> "${launchd_dir}/com.jmdn.jmdn.plist"
    cat >> "${launchd_dir}/com.jmdn.jmdn.plist" <<'EOF'
    </dict>
</dict>
</plist>
EOF
    chmod 644 "${launchd_dir}/com.jmdn.jmdn.plist"
    log_ok "Created ${launchd_dir}/com.jmdn.jmdn.plist"

    log_info "Loading launchd services..."
    launchctl load "${launchd_dir}/com.jmdn.immudb.plist" || true
    launchctl load "${launchd_dir}/com.jmdn.jmdn.plist" || true
}

# Install OpenRC services (Alpine Linux)
install_openrc_services() {
    log_info "Installing OpenRC services..."

    local openrc_dir="/etc/init.d"
    ensure_dir "${openrc_dir}" "755"

    # ImmuDB OpenRC service
    log_info "Creating immudb OpenRC service..."
    cat > "${openrc_dir}/immudb" <<'OPENRC_EOF'
#!/sbin/openrc-run

description="ImmuDB (Immutable Database)"
command="__JMDN_BIN__/immudb"
command_args="--dir __JMDN_DATA__/data"
command_background="true"
pidfile="/var/run/immudb.pid"

depend() {
    need net
}

start_pre() {
    checkpath -d -m 0755 "__JMDN_DATA__/data"
}
OPENRC_EOF
    # Inject actual paths via single atomic sed (avoids placeholder collision between passes)
    sed_inplace -e "s|__JMDN_BIN__|${JMDN_BIN}|g" -e "s|__JMDN_DATA__|${JMDN_DATA}|g" "${openrc_dir}/immudb"
    chmod 755 "${openrc_dir}/immudb"
    log_ok "Created ${openrc_dir}/immudb"

    # JMDN OpenRC service
    log_info "Creating jmdn OpenRC service..."
    cat > "${openrc_dir}/jmdn" <<'OPENRC_EOF'
#!/sbin/openrc-run

description="JMDT Decentralized Network Node (jmdn)"
command="__JMDN_BIN__/start_jmdn_wrapper.sh"
command_background="true"
pidfile="/var/run/jmdn.pid"
directory="__JMDN_DATA__"

depend() {
    need net immudb
}

start_pre() {
    checkpath -d -m 0755 "$directory"
}
OPENRC_EOF
    # Inject actual paths via single atomic sed
    sed_inplace -e "s|__JMDN_BIN__|${JMDN_BIN}|g" -e "s|__JMDN_DATA__|${JMDN_DATA}|g" "${openrc_dir}/jmdn"
    chmod 755 "${openrc_dir}/jmdn"
    log_ok "Created ${openrc_dir}/jmdn"

    # Enable services
    log_info "Enabling OpenRC services..."
    rc-update add immudb boot
    rc-update add jmdn boot
}

# Install rc.d services (FreeBSD)
install_rcd_services() {
    log_info "Installing FreeBSD rc.d services..."

    local rcd_dir="/usr/local/etc/rc.d"
    ensure_dir "${rcd_dir}" "755"

    # ImmuDB rc.d service
    log_info "Creating immudb rc.d service..."
    cat > "${rcd_dir}/immudb" <<'EOF'
#!/bin/sh

# PROVIDE: immudb
# REQUIRE: NETWORKING
# KEYWORD: shutdown

. /etc/rc.subr

name="immudb"
rcvar="immudb_enable"
pidfile="/var/run/${name}.pid"

load_rc_config ${name}

: ${immudb_enable:="NO"}
: ${immudb_user:="root"}
: ${immudb_group:="wheel"}

command="__IMMUDB_CMD__"
command_args="--dir __IMMUDB_DATADIR__"

start_cmd="${name}_start"
stop_cmd="${name}_stop"

immudb_start() {
    echo "Starting ${name}."
    /usr/sbin/daemon -u ${immudb_user} -g ${immudb_group} -p ${pidfile} \
        ${command} ${command_args}
}

immudb_stop() {
    echo "Stopping ${name}."
    if [ -f ${pidfile} ]; then
        kill -TERM $(cat ${pidfile})
    fi
}

run_rc_command "$1"
EOF
    chmod 755 "${rcd_dir}/immudb"
    # Inject paths via single atomic sed (avoids placeholder collision between passes)
    sed_inplace -e "s|__IMMUDB_CMD__|${JMDN_BIN}/immudb|g" -e "s|__IMMUDB_DATADIR__|${JMDN_DATA}/data|g" "${rcd_dir}/immudb"
    log_ok "Created ${rcd_dir}/immudb"

    # JMDN rc.d service
    log_info "Creating jmdn rc.d service..."
    cat > "${rcd_dir}/jmdn" <<'EOF'
#!/bin/sh

# PROVIDE: jmdn
# REQUIRE: NETWORKING immudb
# KEYWORD: shutdown

. /etc/rc.subr

name="jmdn"
rcvar="jmdn_enable"
pidfile="/var/run/${name}.pid"

load_rc_config ${name}

: ${jmdn_enable:="NO"}
: ${jmdn_user:="root"}
: ${jmdn_group:="wheel"}
: ${jmdn_workdir:="WORK_DIR_PLACEHOLDER"}

command="JMDN_BIN_PLACEHOLDER/start_jmdn_wrapper.sh"

start_cmd="${name}_start"
stop_cmd="${name}_stop"

jmdn_start() {
    echo "Starting ${name}."
    /usr/sbin/daemon -u ${jmdn_user} -g ${jmdn_group} -p ${pidfile} \
        -f -D ${jmdn_workdir} \
        env WORK_DIR="${jmdn_workdir}" DATA_DIR="${jmdn_workdir}/data" \
        ${command}
}

jmdn_stop() {
    echo "Stopping ${name}."
    if [ -f ${pidfile} ]; then
        kill -TERM $(cat ${pidfile})
    fi
}

run_rc_command "$1"
EOF
    chmod 755 "${rcd_dir}/jmdn"
    # Inject paths via single atomic sed
    sed_inplace -e "s|WORK_DIR_PLACEHOLDER|${JMDN_DATA}|g" -e "s|JMDN_BIN_PLACEHOLDER|${JMDN_BIN}|g" "${rcd_dir}/jmdn"
    log_ok "Created ${rcd_dir}/jmdn"

    log_info "FreeBSD rc.d services created."
    log_info "Enable via /etc/rc.conf:"
    log_info "  immudb_enable=\"YES\""
    log_info "  jmdn_enable=\"YES\""
}

################################################################################
# Main Installation Logic
################################################################################

log_info "Detected platform: ${PLATFORM} (service manager: ${SVC_MANAGER})"

# Install appropriate services based on detected service manager
case "${SVC_MANAGER}" in
systemd)
    install_systemd_services
    ;;
launchd)
    install_launchd_services
    ;;
openrc)
    install_openrc_services
    ;;
rcd)
    install_rcd_services
    ;;
*)
    log_die "Unsupported service manager: ${SVC_MANAGER}"
    ;;
esac

# Print summary
log_ok "Services installed successfully."
log_info ""
log_info "To start services manually, run:"
case "${SVC_MANAGER}" in
systemd)
    log_info "  sudo systemctl start immudb"
    log_info "  sudo systemctl start ${APP_NAME}"
    ;;
launchd)
    log_info "  launchctl start com.jmdn.immudb"
    log_info "  launchctl start com.jmdn.${APP_NAME}"
    ;;
openrc)
    log_info "  sudo rc-service immudb start"
    log_info "  sudo rc-service ${APP_NAME} start"
    ;;
rcd)
    log_info "  sudo service immudb start"
    log_info "  sudo service ${APP_NAME} start"
    ;;
esac
