#!/bin/bash
# bootstrap-jmdn.sh — Bootstrap JMDN Installation and Systemd Service
# --------------------------------------------------------------
# Sets up JMDN (JMZK Decentralized Network) with systemd service,
# creates necessary directories, builds binary, and starts the service.
# 
# Usage:
#   sudo ./scripts/bootstrap-jmdn.sh
# --------------------------------------------------------------

set -euo pipefail

APP_NAME="jmdn"
SERVICE_NAME="jmdn"
WORK_DIR="/opt/jmdn"
DATA_DIR="${WORK_DIR}/data"
BIN_PATH="/usr/local/bin/jmdn"
START_SCRIPT="/usr/local/bin/start_JMDN.sh"

# ===== Utility colors =====
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; RED='\033[0;31m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }
section() { echo -e "${BLUE}========================================${NC}"; }

# ===== Root check =====
if [ "$EUID" -ne 0 ]; then
  error "Please run this script as root:"
  echo "    sudo $0"
  exit 1
fi

section
info "JMDN Bootstrap Script"
section

# ===== Detect project directory =====
PROJECT_DIR=""
# Try common locations
if [ -d "$HOME/JMZK-Decentalized-Network" ]; then
  PROJECT_DIR="$HOME/JMZK-Decentalized-Network"
elif [ -d "/root/JMZK-Decentalized-Network" ]; then
  PROJECT_DIR="/root/JMZK-Decentalized-Network"
elif [ -d "$(pwd)/.git" ] && [ -f "$(pwd)/main.go" ]; then
  PROJECT_DIR="$(pwd)"
else
  error "JMDN project directory not found."
  echo "Please run this script from the project root or ensure the project is in:"
  echo "  - $HOME/JMZK-Decentalized-Network"
  echo "  - /root/JMZK-Decentalized-Network"
  exit 1
fi

info "Found project directory: ${PROJECT_DIR}"
cd "${PROJECT_DIR}" || exit 1
echo "[OK] Working in $(pwd)"

# ===== Check prerequisites =====
info "Checking prerequisites..."

if ! command -v go >/dev/null 2>&1; then
  error "Go is not installed. Please install Go first:"
  echo "  ./scripts/Go_Prerequisite.sh"
  exit 1
fi
info "Go found: $(go version)"

if ! command -v immudb >/dev/null 2>&1; then
  error "ImmuDB is not installed. Please install ImmuDB first:"
  echo "  ./scripts/ImmuDB_Prerequisite.sh"
  exit 1
fi
info "ImmuDB found: $(immudb version 2>/dev/null | head -n1 || echo 'installed')"

if ! command -v gcc >/dev/null 2>&1; then
  warn "GCC compiler not found. CGO may fail."
  echo "  Consider installing: build-essential (Linux) or Xcode Command Line Tools (macOS)"
else
  info "GCC compiler found"
fi

# ===== Create directories =====
section
info "Creating directories..."

mkdir -p /etc/${APP_NAME}
mkdir -p "${WORK_DIR}"
mkdir -p "${DATA_DIR}"
mkdir -p /var/log/${APP_NAME}

info "Directories created successfully"

# ===== Create config.env (optional) =====
CONFIG_PATH="/etc/${APP_NAME}/config.env"
if [ ! -f "$CONFIG_PATH" ]; then
  info "Creating config.env at ${CONFIG_PATH}..."
  cat <<EOF > "$CONFIG_PATH"
# Environment configuration for JMDN Daemon
# This file is optional - defaults are used if not present
NODE_ENV=production
LOG_LEVEL=info
WORK_DIR=${WORK_DIR}
DATA_DIR=${DATA_DIR}
EOF
  info "Config file created"
else
  warn "Config file already exists at ${CONFIG_PATH}"
fi

# ===== Build JMDN binary =====
section
info "Building JMDN binary..."

cd "${PROJECT_DIR}" || exit 1

info "Building with CGO enabled..."
CGO_ENABLED=1 go build -ldflags='-linkmode=external -w -s' -o jmdn .

if [ ! -f "./jmdn" ]; then
  error "Build failed - jmdn binary not found"
  exit 1
fi

info "Build successful"

# ===== Install binary =====
section
info "Installing JMDN binary..."
cp ./jmdn "${BIN_PATH}"
chmod 755 "${BIN_PATH}"
rm -f ./jmdn
info "Binary installed to ${BIN_PATH}"

# ===== Install start script =====
info "Installing start_JMDN.sh script..."
if [ ! -f "./scripts/start_JMDN.sh" ]; then
  error "start_JMDN.sh not found in ./scripts/"
  exit 1
fi

cp ./scripts/start_JMDN.sh "${START_SCRIPT}"
chmod 755 "${START_SCRIPT}"
info "Start script installed to ${START_SCRIPT}"

# ===== Create systemd service =====
section
info "Creating systemd service..."

SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

cat <<EOF > "${SERVICE_FILE}"
[Unit]
Description=JMZK Decentralized Network Node (jmdn)
After=network.target
Wants=network.target

[Service]
Type=simple
User=root
WorkingDirectory=${WORK_DIR}
Environment="WORK_DIR=${WORK_DIR}"
Environment="DATA_DIR=${DATA_DIR}"
Environment="BIN_PATH=${BIN_PATH}"
ExecStart=${START_SCRIPT}
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=${APP_NAME}

# Resource limits
LimitNOFILE=65536
LimitNPROC=32768

[Install]
WantedBy=multi-user.target
EOF

info "Systemd service file created: ${SERVICE_FILE}"

# ===== Reload and start service =====
section
info "Reloading systemd daemon..."
systemctl daemon-reload

# Stop service if already running
if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
  info "Stopping existing ${SERVICE_NAME} service..."
  systemctl stop "${SERVICE_NAME}"
  sleep 2
fi

info "Starting ${SERVICE_NAME} service..."
systemctl start "${SERVICE_NAME}"

info "Enabling ${SERVICE_NAME} to start on boot..."
systemctl enable "${SERVICE_NAME}"

# Wait for service to start
sleep 5

# ===== Check service status =====
section
info "Checking service status..."
if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
  info "${SERVICE_NAME} service is running"
else
  error "${SERVICE_NAME} service failed to start"
  systemctl status "${SERVICE_NAME}" --no-pager || true
  exit 1
fi

systemctl status "${SERVICE_NAME}" --no-pager --lines=20

# ===== Show logs =====
section
info "Showing first 50 lines of logs..."
echo "---------------------------------------------"

# Check journald logs first
if command -v journalctl >/dev/null 2>&1; then
  journalctl -u "${SERVICE_NAME}" -n 50 --no-pager || true
else
  # Fallback to log file
  if [ -f "/var/log/${APP_NAME}/${APP_NAME}.log" ]; then
    head -n 50 "/var/log/${APP_NAME}/${APP_NAME}.log" || true
  else
    warn "Log file not found yet"
  fi
fi

section
info "JMDN bootstrap completed successfully!"
echo ""
info "Service management commands:"
echo "  sudo systemctl status ${SERVICE_NAME}  - Check service status"
echo "  sudo systemctl stop ${SERVICE_NAME}     - Stop service"
echo "  sudo systemctl start ${SERVICE_NAME}    - Start service"
echo "  sudo systemctl restart ${SERVICE_NAME}  - Restart service"
echo "  sudo journalctl -u ${SERVICE_NAME} -f  - Follow logs"
echo ""
info "Working directory: ${WORK_DIR}"
info "Data directory: ${DATA_DIR}"
info "Binary location: ${BIN_PATH}"
section