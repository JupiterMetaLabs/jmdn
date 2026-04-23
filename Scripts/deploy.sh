#!/usr/bin/env bash
################################################################################
# deploy.sh — Robust Cross-Platform JMDN Updater & Deployer
################################################################################
#
# CHANGELOG:
# 2026-03-02: Complete rewrite for cross-platform support
#   - Replaced shebang with #!/usr/bin/env bash for portability
#   - Integrated lib/platform.sh for platform-agnostic service management
#   - Replaced all systemctl calls with svc_* helpers (systemd, launchd, rcd, openrc)
#   - Replaced hardcoded /usr/local/bin with $JMDN_BIN from platform detection
#   - Replaced journalctl with cross-platform log viewing (journalctl on systemd,
#     log show on launchd, tail on others)
#   - Replaced custom color functions with shared log_* functions
#   - Maintained atomic swap, rollback, and health check logic
#   - Preserved Ansible compatibility
#
# Usage:
#   sudo ./Scripts/deploy.sh
#   (Ansible handles checking out the correct version before calling this)
#
# Supports:
#   - Linux (systemd, OpenRC)
#   - macOS (launchd)
#   - FreeBSD (rc.d)
################################################################################

set -euo pipefail

# 1. PREVENT HANGS
export GIT_TERMINAL_PROMPT=0
export GIT_ASKPASS=/bin/false

# 2. ENVIRONMENT
# Ensure Go is on PATH — critical for build.sh.
# Go is installed to /usr/local/go by setup_dependencies.sh.
export PATH="/usr/local/go/bin:${PATH}"

# 3. SOURCE PLATFORM LIBRARY
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
# shellcheck source=lib/platform.sh
source "${SCRIPT_DIR}/lib/platform.sh"

# 4. CONFIGURATION
APP_NAME="jmdn"
SERVICE_NAME="jmdn"
# Use cross-platform binary destination from platform detection
BINARY_DEST="${JMDN_BIN}/${APP_NAME}"
BINARY_NEW="${APP_NAME}.new"
BINARY_BAK="${BINARY_DEST}.bak"

# 5. DIRECTORY NAVIGATION
cd "$PROJECT_DIR"
log_info "Working directory: $(pwd)"
log_info "Detected platform: ${PLATFORM}"
log_info "Service manager: ${SVC_MANAGER}"

# 6. DEPENDENCIES (Safety Check)
log_info "Checking dependencies..."
if [ -x "./Scripts/setup_dependencies.sh" ]; then
    # Don't suppress output — failures must be visible for debugging
    ./Scripts/setup_dependencies.sh --all || log_warn "Dependency setup exited with code $? (non-fatal, continuing)"
else
    log_warn "setup_dependencies.sh not found, assuming environment is ready."
fi

# 7. BUILD (Using Standardized Build Script)
log_info "Building Binary..."

# Use dedicated build script
if [ -x "./Scripts/build.sh" ]; then
    # Build to temporary file
    if ./Scripts/build.sh .; then
        mv "${APP_NAME}" "${BINARY_NEW}"
        log_ok "Build Successful: ${BINARY_NEW}"
    else
        log_error "Build Script Failed!"
        exit 1
    fi
else
    log_error "Scripts/build.sh not found!"
    exit 1
fi

log_info "Artifact prepared: ${BINARY_NEW}"

# 8. ATOMIC SWAP
log_info "Performing atomic swap..."

# Backup existing deployment
if [ -f "${BINARY_DEST}" ]; then
    cp "${BINARY_DEST}" "${BINARY_BAK}"
    log_info "Backed up previous binary to ${BINARY_BAK}"
fi

# Set permissions before move so destination has correct perms atomically
chmod 755 "${BINARY_NEW}"
mv "${BINARY_NEW}" "${BINARY_DEST}"
log_ok "Binary deployed to ${BINARY_DEST}"

# Update wrapper script if changed
if [ -f "./Scripts/start_jmdn_wrapper.sh" ]; then
    WRAPPER_DEST="${JMDN_BIN}/jmdn_wrapper"
    cp ./Scripts/start_jmdn_wrapper.sh "${WRAPPER_DEST}"
    chmod +x "${WRAPPER_DEST}"
    log_ok "Wrapper script updated at ${WRAPPER_DEST}"
fi

# 9. RESTART & HEALTH CHECK
log_info "Restarting ${SERVICE_NAME}..."

# Reload daemon config (platform-aware)
svc_reload_daemon

# Restart or start the service
if svc_status "${SERVICE_NAME}" >/dev/null 2>&1; then
    svc_restart "${SERVICE_NAME}"
    log_ok "Service restarted"
else
    svc_start "${SERVICE_NAME}"
    log_ok "Service started"
fi

HEALTH_RETRIES=10
HEALTH_DELAY=3
log_info "Waiting for service to stabilize (${HEALTH_RETRIES} retries)..."

SUCCESS=false
for i in $(seq 1 $HEALTH_RETRIES); do
    sleep "$HEALTH_DELAY"
    if svc_status "${SERVICE_NAME}" >/dev/null 2>&1; then
        log_ok "Deployment Complete! Service is RUNNING (attempt $i)."
        SUCCESS=true
        break
    fi
    log_warn "Health check attempt $i failed, retrying..."
done

if [ "$SUCCESS" = true ]; then
    log_ok "Service Stable."
    # Optional: cleanup. Keeping backup for safety.
else
    log_error "Service failed to start after ${HEALTH_RETRIES} attempts!"

    # ROLLBACK
    if [ -f "${BINARY_BAK}" ]; then
        log_warn "Rolling back to previous version..."
        cp "${BINARY_BAK}" "${BINARY_DEST}"
        svc_restart "${SERVICE_NAME}"

        sleep 5
        if svc_status "${SERVICE_NAME}" >/dev/null 2>&1; then
            log_ok "Rollback successful. Service running on PREVIOUS version."
        else
            log_error "Rollback failed! Manual intervention required."
        fi
    else
        log_error "No backup available for rollback!"
    fi

    # Display service logs in a cross-platform way
    log_info "Recent service logs:"
    case "${SVC_MANAGER}" in
    systemd)
        journalctl -u "${SERVICE_NAME}" -n 50 --no-pager || true
        ;;
    launchd)
        log show --predicate "process==\"${SERVICE_NAME}\"" --last 1h 2>/dev/null | tail -50 || true
        ;;
    openrc | rcd)
        if [ -f "${JMDN_LOG}/${SERVICE_NAME}.log" ]; then
            tail -50 "${JMDN_LOG}/${SERVICE_NAME}.log" || true
        fi
        ;;
    *)
        log_warn "Unable to display logs for unknown service manager: ${SVC_MANAGER}"
        ;;
    esac

    exit 1
fi
