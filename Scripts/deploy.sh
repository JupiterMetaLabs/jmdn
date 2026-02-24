#!/bin/bash
# deploy.sh — Robust JMDN Updater & Deployer (Standardized)
# ------------------------------------------------------------------
# Usage:
#   sudo ./Scripts/deploy.sh
#   (Ansible handles checking out the correct version before calling this)
# ------------------------------------------------------------------

set -euo pipefail

# 1. PREVENT HANGS
export GIT_TERMINAL_PROMPT=0
export GIT_ASKPASS=/bin/false

# 2. ENVIRONMENT
# Ensure Go is on PATH — critical for build.sh.
# Go is installed to /usr/local/go by setup_dependencies.sh.
export PATH="/usr/local/go/bin:${PATH}"

# 3. CONFIGURATION
APP_NAME="jmdn"
SERVICE_NAME="jmdn"
# JMDN specific: The active binary lives in /usr/local/bin
BINARY_DEST="/usr/local/bin/${APP_NAME}"
BINARY_NEW="${APP_NAME}.new"
BINARY_BAK="${BINARY_DEST}.bak"

# Colors
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BLUE='\033[0;34m'; NC='\033[0m'
info() { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()  { echo -e "${RED}[ERROR]${NC} $*"; }
cmd()  { echo -e "${BLUE}> $*${NC}"; "$@"; }

# 4. DIRECTORY NAVIGATION
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$PROJECT_DIR"
info "Working directory: $(pwd)"

# 5. DEPENDENCIES (Safety Check)
info "Checking dependencies..."
if [ -x "./Scripts/setup_dependencies.sh" ]; then
    # Don't suppress output — failures must be visible for debugging
    cmd ./Scripts/setup_dependencies.sh --all || warn "Dependency setup returned warnings (non-fatal)"
else
    warn "setup_dependencies.sh not found, assuming environment is ready."
fi

# 5. BUILD (Using Standardized Build Script)
info "Building Binary..."

# Use dedicated build script
if [ -x "./Scripts/build.sh" ]; then
    # Build to temporary file
    if cmd ./Scripts/build.sh .; then
        mv "${APP_NAME}" "${BINARY_NEW}"
        info "Build Successful: ${BINARY_NEW}"
    else
        err "Build Script Failed!"
        exit 1
    fi
else
    err "Scripts/build.sh not found!"
    exit 1
fi

info "Artifact prepared: ${BINARY_NEW}"

# 6. ATOMIC SWAP
info "Performing atomic swap..."

# Backup existing deployment
if [ -f "${BINARY_DEST}" ]; then
    cmd cp "${BINARY_DEST}" "${BINARY_BAK}"
fi

# Move new binary to destination
cmd mv "${BINARY_NEW}" "${BINARY_DEST}"
cmd chmod 755 "${BINARY_DEST}"

# Update wrapper script if changed
if [ -f "./Scripts/start_jmdn_wrapper.sh" ]; then
    cmd cp ./Scripts/start_jmdn_wrapper.sh /usr/local/bin/jmdn_wrapper
    cmd chmod +x /usr/local/bin/jmdn_wrapper
fi

# 7. RESTART & HEALTH CHECK
info "Restarting ${SERVICE_NAME}..."
# Ensure systemd sees any changes
cmd systemctl daemon-reload

if systemctl is-active --quiet "${SERVICE_NAME}"; then
    cmd systemctl restart "${SERVICE_NAME}"
else
    cmd systemctl start "${SERVICE_NAME}"
fi

HEALTH_RETRIES=10
HEALTH_DELAY=3
info "Waiting for service to stabilize (${HEALTH_RETRIES} retries)..."

SUCCESS=false
for i in $(seq 1 $HEALTH_RETRIES); do
    sleep "$HEALTH_DELAY"
    if systemctl is-active --quiet "${SERVICE_NAME}"; then
        info "✅ Deployment Complete! Service is RUNNING (attempt $i)."
        SUCCESS=true
        break
    fi
    warn "Health check attempt $i failed, retrying..."
done

if [ "$SUCCESS" = true ]; then
    info "Service Stable."
    # Optional: cleanup. Keeping backup for safety.
else
    err "Service failed to start after ${HEALTH_RETRIES} attempts!"
    
    # ROLLBACK
    if [ -f "${BINARY_BAK}" ]; then
        warn "🔄 Rolling back to previous version..."
        cmd cp "${BINARY_BAK}" "${BINARY_DEST}"
        cmd systemctl restart "${SERVICE_NAME}"
        
        sleep 5
        if systemctl is-active --quiet "${SERVICE_NAME}"; then
            warn "✅ Rollback successful. Service running on PREVIOUS version."
        else
            err "⛔️ Rollback failed! Manual intervention required."
        fi
    else
        err "⛔️ No backup available for rollback!"
    fi
    
    cmd journalctl -u "${SERVICE_NAME}" -n 50 --no-pager
    exit 1
fi
