#!/bin/bash
# rebuild_and_restart.sh — Rebuild and Restart JMDN After Git Pull
# --------------------------------------------------------------
# Simple helper for developers to rebuild and restart JMDN after pulling updates
#
# Usage:
#   sudo ./Scripts/rebuild_and_restart.sh
# --------------------------------------------------------------

set -euo pipefail

# ===== Utility colors =====
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }

# Always run from the project root (one level above Scripts/)
cd "$(dirname "$0")/.." || exit 1

info "Rebuilding and restarting JMDN..."

info "Getting latest code from github..."
git pull
info "Latest code fetched"


APP_NAME="jmdn"
SERVICE_NAME="jmdn"



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

# Capture version info
GIT_COMMIT=$(git rev-parse --short HEAD)
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
GIT_TAG=$(git describe --tags --always --dirty 2>/dev/null | tr -d '`' || echo "unknown")
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')

info "Version: ${GIT_TAG} (${GIT_COMMIT}) on ${GIT_BRANCH}"

LDFLAGS="-X 'gossipnode/config/version.gitCommit=${GIT_COMMIT}' -X 'gossipnode/config/version.gitBranch=${GIT_BRANCH}' -X 'gossipnode/config/version.gitTag=${GIT_TAG}' -X 'gossipnode/config/version.buildTime=${BUILD_TIME}' -linkmode=external -w -s"

CGO_ENABLED=1 go build -ldflags="${LDFLAGS}" -o jmdn . || exit 1

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

info "Updating start scripts..."
sudo cp ./Scripts/start_JMDN.sh /usr/local/bin/start_JMDN.sh
sudo chmod 755 /usr/local/bin/start_JMDN.sh
info "start_JMDN.sh updated successfully"

if [ -f "./Scripts/firstStart.sh" ]; then
    sudo cp ./Scripts/firstStart.sh /usr/local/bin/firstStart.sh
    sudo chmod 755 /usr/local/bin/firstStart.sh
    info "firstStart.sh updated successfully"
else
    warn "firstStart.sh not found in ./Scripts/"
fi

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