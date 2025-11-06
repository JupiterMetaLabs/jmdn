#!/bin/bash
# rebuild_and_restart.sh — Rebuild and Restart JMDN After Git Pull
# --------------------------------------------------------------
# Simple helper for developers to rebuild and restart JMDN after pulling updates
#
# Usage:
#   sudo ./Scripts/rebuild_and_restart.sh
# --------------------------------------------------------------

set -euo pipefail

# Always run from the project root (one level above Scripts/)
cd "$(dirname "$0")/.." || exit 1

echo "Doing Git Pull"
git pull

APP_NAME="jmdn"
SERVICE_NAME="jmdn"

# ===== Utility colors =====
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }

info "Checking if Go is available..."
if ! command -v go >/dev/null 2>&1; then
    if [ -x "/usr/local/go/bin/go" ]; then
        export PATH="/usr/local/go/bin:$PATH"
        info "Using Go from /usr/local/go/bin"
    else
        warn "Go not found. Please install Go or run ./Scripts/Go_Prerequisite.sh"
        exit 1
    fi
fi

info "Checking if C compiler is available..."
if ! command -v gcc >/dev/null 2>&1; then
    warn "GCC compiler not found. CGO may fail. Consider installing build-essential (Linux) or Xcode Command Line Tools (macOS)"
fi

info "Building JMDN binary..."
CGO_ENABLED=1 go build -ldflags='-linkmode=external -w -s' -o jmdn . || exit 1

# Check if service is running and stop it before installing new binary
SERVICE_WAS_RUNNING=false
if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
    SERVICE_WAS_RUNNING=true
    info "Stopping ${SERVICE_NAME} service..."
    sudo systemctl stop "${SERVICE_NAME}"
    sleep 1
fi

info "Installing binary..."
sudo cp jmdn /usr/local/bin/jmdn
sudo chmod 755 /usr/local/bin/jmdn
info "Binary installed successfully"

info "Updating start_JMDN.sh script..."
sudo cp ./Scripts/start_JMDN.sh /usr/local/bin/start_JMDN.sh
sudo chmod 755 /usr/local/bin/start_JMDN.sh
info "Script updated successfully"

# Restart service if it was running
if [ "$SERVICE_WAS_RUNNING" = true ]; then
    info "Starting ${SERVICE_NAME} service..."
    sudo systemctl start "${SERVICE_NAME}"
    sleep 2
    if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
        info "Service restarted successfully"
    else
        warn "Service failed to start. Check: sudo systemctl status ${SERVICE_NAME}"
    fi
else
    warn "Service ${SERVICE_NAME} was not running. Start it with: sudo systemctl start ${SERVICE_NAME}"
fi

rm -f jmdn
info "Done!"
