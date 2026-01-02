#!/bin/bash
# rebuild_and_restart_dynamic.sh — Rebuild JMDN with Specific Version
# ------------------------------------------------------------------
# Usage:
#   sudo ./rebuild_and_restart_dynamic.sh [BRANCH_OR_TAG]
#   Example: sudo ./rebuild_and_restart_dynamic.sh v2.5.0
# ------------------------------------------------------------------

set -euo pipefail

# ===== Configuration =====
TARGET_REF=${1:-"M-Freeze"}  # Default to 'M-Freeze' if no argument provided
SERVICE_NAME="jmdn"
BUILD_TIMEOUT=300  # 5 minutes
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; RED='\033[0;31m'; NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()  { echo -e "${RED}[ERROR]${NC} $*"; }
cmd()  { echo -e "${CYAN}> $*${NC}"; "$@"; }

# Track if we stashed changes
STASHED=false

# Cleanup function to restore stash on exit
cleanup() {
    if [ "$STASHED" = true ]; then
        info "Restoring stashed changes..."
        git stash pop || warn "Failed to restore stashed changes (may need manual intervention)"
    fi
}
trap cleanup EXIT

# Ensure we are in project root
cd "$(dirname "$0")/.." || exit 1

info "Starting Deployment for Target: ${YELLOW}${TARGET_REF}${NC}"

# 1. GIT OPERATIONS
info "Fetching latest refs..."
cmd git fetch --all --tags --prune

# Check if local changes exist
if [ -n "$(git status --porcelain)" ]; then
    warn "Local changes detected. Stashing..."
    cmd git stash
    STASHED=true
fi

info "Checking out ${TARGET_REF}..."
if cmd git checkout "${TARGET_REF}"; then
    # Only pull if it's a branch (not a detached HEAD / tag)
    if git symbolic-ref -q HEAD >/dev/null; then
        info "Pulling latest changes for branch ${TARGET_REF}..."
        cmd git pull origin "${TARGET_REF}"
    else
        info "${TARGET_REF} appears to be a Tag/Commit. No pull needed."
    fi
else
    err "Failed to checkout ${TARGET_REF}. Aborting deployment."
    exit 1
fi

# 2. DEPENDENCY CHECKS
info "Checking environment..."
if ! command -v go >/dev/null 2>&1; then
    [ -x "/usr/local/go/bin/go" ] && export PATH="/usr/local/go/bin:$PATH" || { err "Go not found"; exit 1; }
fi

# 3. BUILD (with timeout)
info "Building Binary (timeout: ${BUILD_TIMEOUT}s)..."
GIT_COMMIT=$(git rev-parse --short HEAD)
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
GIT_TAG=$(git describe --tags --always --dirty 2>/dev/null | tr -d '`' || echo "unknown")
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')

LDFLAGS="-X 'gossipnode/config/version.gitCommit=${GIT_COMMIT}' -X 'gossipnode/config/version.gitBranch=${GIT_BRANCH}' -X 'gossipnode/config/version.gitTag=${GIT_TAG}' -X 'gossipnode/config/version.buildTime=${BUILD_TIME}' -linkmode=external -w -s"

if timeout "${BUILD_TIMEOUT}" bash -c "CGO_ENABLED=1 go build -ldflags=\"${LDFLAGS}\" -o jmdn ."; then
    info "Build Success: ${GIT_TAG} (${GIT_COMMIT})"
else
    err "Build Failed or Timed Out!"
    exit 1
fi

# 4. BACKUP EXISTING BINARY
info "Backing up existing binary..."
if [ -f /usr/local/bin/jmdn ]; then
    cmd sudo cp /usr/local/bin/jmdn /usr/local/bin/jmdn.bak
    info "Backup created: /usr/local/bin/jmdn.bak"
else
    warn "No existing binary to backup"
fi

# 5. INSTALL & RESTART
info "Updating Service..."
if systemctl is-active --quiet "${SERVICE_NAME}"; then
    cmd sudo systemctl stop "${SERVICE_NAME}"
fi

cmd sudo cp jmdn /usr/local/bin/jmdn
cmd sudo chmod 755 /usr/local/bin/jmdn

# Update helper scripts if present
[ -f "./Scripts/start_JMDN.sh" ] && cmd sudo cp ./Scripts/start_JMDN.sh /usr/local/bin/
[ -f "./Scripts/firstStart.sh" ] && cmd sudo cp ./Scripts/firstStart.sh /usr/local/bin/

info "Restarting Service..."
cmd sudo systemctl start "${SERVICE_NAME}"
sleep 3

# 6. HEALTH CHECK WITH ROLLBACK
if systemctl is-active --quiet "${SERVICE_NAME}"; then
    info "Deployment Complete! Service is RUNNING."
    # Cleanup build artifact
    rm -f jmdn
else
    err "Service failed to start!"
    
    # Attempt rollback
    if [ -f /usr/local/bin/jmdn.bak ]; then
        warn "Attempting rollback to previous version..."
        cmd sudo cp /usr/local/bin/jmdn.bak /usr/local/bin/jmdn
        cmd sudo systemctl start "${SERVICE_NAME}"
        sleep 2
        
        if systemctl is-active --quiet "${SERVICE_NAME}"; then
            warn "Rollback successful. Service is running with PREVIOUS version."
            warn "Please investigate the build issue."
        else
            err "Rollback also failed! Manual intervention required."
            err "Check logs: journalctl -u ${SERVICE_NAME} -n 50"
        fi
    else
        err "No backup available for rollback!"
        err "Check logs: journalctl -u ${SERVICE_NAME} -n 50"
    fi
    
    exit 1
fi
