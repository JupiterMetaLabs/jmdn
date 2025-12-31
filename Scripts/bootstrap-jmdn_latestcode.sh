#!/bin/bash
# bootstrap-jmdn.sh — Bootstrap JMDN Installation and Systemd Service
# --------------------------------------------------------------
# Sets up JMDN (JMZK Decentralized Network) with systemd service,
# creates necessary directories, builds binary, installs dependencies,
# configures logs, and starts the service.
# --------------------------------------------------------------

set -euo pipefail

APP_NAME="jmdn"
SERVICE_NAME="jmdn"
WORK_DIR="/opt/jmdn"
DATA_DIR="${WORK_DIR}/data"
BIN_PATH="/usr/local/bin/jmdn"
START_SCRIPT="/usr/local/bin/start_JMDN.sh"
LOG_DIR="/var/log/${APP_NAME}"

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
if [ -d "$HOME/JMZK-Decentalized-Network" ]; then
  PROJECT_DIR="$HOME/JMZK-Decentalized-Network"
elif [ -d "/root/JMZK-Decentalized-Network" ]; then
  PROJECT_DIR="/root/JMZK-Decentalized-Network"
elif [ -d "$(pwd)/.git" ] && [ -f "$(pwd)/main.go" ]; then
  PROJECT_DIR="$(pwd)"
else
  error "JMDN project directory not found."
  echo "Please run this script from the project root or ensure it’s in:"
  echo "  - $HOME/JMZK-Decentalized-Network"
  echo "  - /root/JMZK-Decentalized-Network"
  exit 1
fi

info "Found project directory: ${PROJECT_DIR}"
cd "${PROJECT_DIR}" || exit 1
echo "[OK] Working in $(pwd)"

# ===== Install prerequisites =====
section
info "Checking prerequisites..."

install_build_essentials_if_needed() {
  if ! command -v gcc >/dev/null 2>&1; then
    warn "GCC compiler not found. Installing build-essential..."
    if command -v apt >/dev/null 2>&1; then
      apt update -y && apt install -y build-essential
    elif command -v yum >/dev/null 2>&1; then
      yum groupinstall -y "Development Tools"
    elif command -v dnf >/dev/null 2>&1; then
      dnf groupinstall -y "Development Tools"
    else
      error "No supported package manager found (apt, yum, dnf)."
      exit 1
    fi

    if ! command -v gcc >/dev/null 2>&1; then
      error "GCC installation failed. Please install manually."
      exit 1
    else
      info "GCC successfully installed: $(gcc --version | head -n1)"
    fi
  else
    info "GCC compiler found: $(gcc --version | head -n1)"
  fi
}

install_go_if_needed() {
  if ! command -v go >/dev/null 2>&1; then
    warn "Go is not installed. Installing Go..."
    if [ -f "${PROJECT_DIR}/Scripts/Go_Prerequisite.sh" ]; then
      info "Running Go_Prerequisite.sh..."
      bash "${PROJECT_DIR}/Scripts/Go_Prerequisite.sh"
      if [ $? -eq 0 ]; then
        info "Go installed successfully"
        if [ -f ~/.bashrc ]; then
          source ~/.bashrc 2>/dev/null || true
        elif [ -f ~/.bash_profile ]; then
          source ~/.bash_profile 2>/dev/null || true
        elif [ -f ~/.zshrc ]; then
          source ~/.zshrc 2>/dev/null || true
        fi
      else
        error "Go installation failed"
        exit 1
      fi
    else
      error "Go_Prerequisite.sh not found at ${PROJECT_DIR}/Scripts/Go_Prerequisite.sh"
      exit 1
    fi
  else
    info "Go found: $(go version)"
  fi
}

install_immudb_if_needed() {
  if ! command -v immudb >/dev/null 2>&1; then
    warn "ImmuDB not found. Installing..."
    if [ -f "${PROJECT_DIR}/Scripts/ImmuDB_Prerequisite.sh" ]; then
      bash "${PROJECT_DIR}/Scripts/ImmuDB_Prerequisite.sh"
      info "ImmuDB installed successfully"
    else
      error "ImmuDB_Prerequisite.sh not found"
      exit 1
    fi
  else
    info "ImmuDB found: $(immudb version 2>/dev/null | head -n1 || echo 'installed')"
  fi
}

install_yggdrasil_if_needed() {
  if ! command -v yggdrasil >/dev/null 2>&1; then
    warn "Yggdrasil not found. Installing..."
    if [ -f "${PROJECT_DIR}/Scripts/YGG_Prerequisite.sh" ]; then
      echo -e "y\ny" | bash "${PROJECT_DIR}/Scripts/YGG_Prerequisite.sh"
      info "Yggdrasil installed successfully"
    else
      error "YGG_Prerequisite.sh not found"
      exit 1
    fi
  else
    info "Yggdrasil found: $(yggdrasil -version 2>/dev/null | head -n1 || echo 'installed')"
  fi
}

install_build_essentials_if_needed
install_go_if_needed
install_immudb_if_needed
install_yggdrasil_if_needed

info "Prerequisites check completed"

# ===== Directories =====
section
info "Creating directories..."
mkdir -p /etc/${APP_NAME} "${WORK_DIR}" "${DATA_DIR}" "${LOG_DIR}"
chmod 755 "${LOG_DIR}"
info "Directories created successfully"

# ===== Config =====
CONFIG_PATH="/etc/${APP_NAME}/config.env"
if [ ! -f "$CONFIG_PATH" ]; then
  info "Creating config.env..."
  cat <<EOF > "$CONFIG_PATH"
NODE_ENV=production
LOG_LEVEL=info
WORK_DIR=${WORK_DIR}
DATA_DIR=${DATA_DIR}
EOF
fi

# ===== Build JMDN =====
section
info "Building JMDN binary..."
cd "${PROJECT_DIR}" || exit 1

# Capture version info
GIT_COMMIT=$(git rev-parse --short HEAD)
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
GIT_TAG=$(git describe --tags --always --dirty 2>/dev/null | tr -d '`' || echo "unknown")
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')

info "Version: ${GIT_TAG} (${GIT_COMMIT}) on ${GIT_BRANCH}"

LDFLAGS="-X 'gossipnode/config/version.gitCommit=${GIT_COMMIT}' -X 'gossipnode/config/version.gitBranch=${GIT_BRANCH}' -X 'gossipnode/config/version.gitTag=${GIT_TAG}' -X 'gossipnode/config/version.buildTime=${BUILD_TIME}' -linkmode=external -w -s"

CGO_ENABLED=1 go build -ldflags="${LDFLAGS}" -o jmdn .

if [ ! -f "./jmdn" ]; then
  error "Build failed - binary not found"
  exit 1
fi

info "Build successful, installing..."
cp ./jmdn "${BIN_PATH}"
chmod 755 "${BIN_PATH}"
rm -f ./jmdn

# ===== Copy structure =====
section
info "Setting up working directories..."
if [ -d "./config" ]; then
  cp -r ./config "${WORK_DIR}/"
else
  mkdir -p "${WORK_DIR}/config"
  echo '{}' > "${WORK_DIR}/config/peer.json"
fi
mkdir -p "${WORK_DIR}/.immudb_state" "${WORK_DIR}/DB"
chmod -R 755 "${WORK_DIR}"

# ===== Install scripts =====
info "Installing start scripts..."
cp ./Scripts/start_JMDN.sh "${START_SCRIPT}"
chmod 755 "${START_SCRIPT}"
cp ./Scripts/firstStart.sh /usr/local/bin/firstStart.sh
chmod 755 /usr/local/bin/firstStart.sh

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
ExecStart=${START_SCRIPT}
Restart=always
RestartSec=10

# Logging (journal + files)
StandardOutput=append:${LOG_DIR}/${APP_NAME}.out.log
StandardError=append:${LOG_DIR}/${APP_NAME}.err.log
SyslogIdentifier=${APP_NAME}

# Resource limits
LimitNOFILE=65536
LimitNPROC=32768

[Install]
WantedBy=multi-user.target
EOF

info "Systemd service file created: ${SERVICE_FILE}"

# ===== Log rotation =====
info "Setting up log rotation..."
cat <<EOF > "/etc/logrotate.d/${APP_NAME}"
${LOG_DIR}/*.log {
  rotate 7
  daily
  missingok
  notifempty
  compress
  delaycompress
  copytruncate
}
EOF
info "Log rotation configured at /etc/logrotate.d/${APP_NAME}"

# ===== Enable and start service =====
section
info "Reloading systemd and starting ${SERVICE_NAME}..."
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}" --now

sleep 5
if systemctl is-active --quiet "${SERVICE_NAME}"; then
  info "${SERVICE_NAME} service is running successfully!"
else
  error "${SERVICE_NAME} service failed to start"
  systemctl status "${SERVICE_NAME}" --no-pager || true
  exit 1
fi

section
info "JMDN bootstrap completed successfully!"
echo ""
info "Service management commands:"
echo "  sudo systemctl status ${SERVICE_NAME}"
echo "  sudo systemctl restart ${SERVICE_NAME}"
echo "  sudo journalctl -u ${SERVICE_NAME} -f"
echo "  tail -f ${LOG_DIR}/${APP_NAME}.out.log"
echo "  tail -f ${LOG_DIR}/${APP_NAME}.err.log"
