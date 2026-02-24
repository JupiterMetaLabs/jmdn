#!/bin/bash
# install_services.sh
# ------------------------------------------------------------------
# Installs JMDN and ImmuDB systemd services.
# Compatible with Linux (Debian/Ubuntu/RPi/CentOS) using systemd.
#
# Usage: sudo ./install_services.sh
# ------------------------------------------------------------------

set -euo pipefail

# Config
APP_NAME="jmdn"
SERVICE_USER="root" # Running as root matching existing legacy setup
WORK_DIR="/opt/jmdn"
DATA_DIR="${WORK_DIR}/data"
LOG_DIR="/var/log/${APP_NAME}"
BIN_DIR="/usr/local/bin"

# Colors
BLUE='\033[0;34m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC}  $*"; }
success() { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
die()   { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# Root check
if [ "$EUID" -ne 0 ]; then
  die "Please run as root: sudo $0"
fi

# 1. Create Directories
info "Creating directories..."
mkdir -p "${WORK_DIR}" "${DATA_DIR}" "${LOG_DIR}" "/etc/${APP_NAME}" "${WORK_DIR}/config" "${WORK_DIR}/DB"
chmod 755 "${LOG_DIR}"
chmod 755 "${DATA_DIR}"
chmod 755 "${WORK_DIR}/DB"

# 1.1 Check Configuration
if [ ! -f "/etc/${APP_NAME}/jmdn.yaml" ]; then
    warn "Configuration file not found at /etc/${APP_NAME}/jmdn.yaml"
    warn "Ensure you copy 'jmdn_default.yaml' to '/etc/jmdn/jmdn.yaml' or deploy via Ansible."
fi

# 1.5. Copy Binaries
info "Installing binaries to ${BIN_DIR}..."

# Wrapper Script
if [ -f "Scripts/start_jmdn_wrapper.sh" ]; then
    cp "Scripts/start_jmdn_wrapper.sh" "${BIN_DIR}/start_jmdn_wrapper.sh"
    chmod +x "${BIN_DIR}/start_jmdn_wrapper.sh"
    success "Installed start_jmdn_wrapper.sh"
elif [ -f "start_jmdn_wrapper.sh" ]; then
    # Fallback if running from proper dir
    cp "start_jmdn_wrapper.sh" "${BIN_DIR}/start_jmdn_wrapper.sh"
    chmod +x "${BIN_DIR}/start_jmdn_wrapper.sh"
    success "Installed start_jmdn_wrapper.sh"
else
    warn "start_jmdn_wrapper.sh not found in current directory. Please ensure it is present."
fi

# JMDN Binary (Check if built)
if [ -f "${APP_NAME}" ]; then
    cp "${APP_NAME}" "${BIN_DIR}/${APP_NAME}"
    chmod +x "${BIN_DIR}/${APP_NAME}"
    success "Installed ${APP_NAME} binary"
else
    warn "${APP_NAME} binary not found. Please run build.sh first."
fi

# 2. Install ImmuDB Service
info "Installing immudb.service..."
cat <<EOF > /etc/systemd/system/immudb.service
[Unit]
Description=ImmuDB (Immutable Database)
After=network.target
Documentation=https://docs.immudb.io

[Service]
Type=simple
User=${SERVICE_USER}
ExecStart=${BIN_DIR}/immudb --dir "${DATA_DIR}"
Restart=always
RestartSec=5
# Output to journal (Ansible/Sysadmin preference)
StandardOutput=journal
StandardError=journal
SyslogIdentifier=immudb

[Install]
WantedBy=multi-user.target
EOF
success "Created /etc/systemd/system/immudb.service"

# 3. Install JMDN Service
info "Installing jmdn.service..."
# JMDN depends on ImmuDB being up.
cat <<EOF > /etc/systemd/system/${APP_NAME}.service
[Unit]
Description=JMDT Decentralized Network Node (jmdn)
After=network.target immudb.service
Requires=immudb.service

[Service]
Type=simple
User=${SERVICE_USER}
WorkingDirectory=${WORK_DIR}
Environment="WORK_DIR=${WORK_DIR}"
Environment="DATA_DIR=${DATA_DIR}"
# Use the wrapper script we created
ExecStart=${BIN_DIR}/start_jmdn_wrapper.sh
Restart=always
RestartSec=10

# Logging defaults (Can be overridden by Ansible drop-ins)
StandardOutput=journal
StandardError=journal
SyslogIdentifier=${APP_NAME}

# Resource limits (matching legacy values)
LimitNOFILE=65536
LimitNPROC=32768

[Install]
WantedBy=multi-user.target
EOF
success "Created /etc/systemd/system/${APP_NAME}.service"

# 4. Reload and Enable
info "Reloading systemd daemon..."
systemctl daemon-reload

info "Enabling services..."
systemctl enable immudb.service
systemctl enable ${APP_NAME}.service

info "Services installed and enabled."
info "To start them manually now, run:"
info "  sudo systemctl start immudb"
info "  sudo systemctl start ${APP_NAME}"
