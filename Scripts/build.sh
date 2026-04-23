#!/usr/bin/env bash
################################################################################
# build.sh - Cross-Platform Build Script for JMDN
# ------------------------------------------------------------------
# CHANGELOG:
# - Changed shebang to #!/usr/bin/env bash for portability
# - Sourced lib/platform.sh for cross-platform helpers
# - Replaced local color/logging functions with platform.sh versions
#   (log_info, log_ok, log_warn, log_error, log_die)
# - Replaced hardcoded GCC check with check_command helper
# - All existing functionality preserved for Linux, macOS, FreeBSD
# - Maintained CGO_ENABLED=1 and Go build command unchanged
# ------------------------------------------------------------------
# Usage: ./build.sh [output_dir]
# ------------------------------------------------------------------

set -euo pipefail

# Source platform helpers - locate relative to this script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/platform.sh
source "${SCRIPT_DIR}/lib/platform.sh"

# Config
APP_NAME="jmdn"
DEFAULT_OUTPUT_DIR="."
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Args
OUTPUT_DIR="${1:-$DEFAULT_OUTPUT_DIR}"

log_info "Building ${APP_NAME}..."
log_info "Project Root: ${PROJECT_ROOT}"
log_info "Output Dir:   ${OUTPUT_DIR}"

cd "${PROJECT_ROOT}" || log_die "Could not cd to project root"

# Git Metadata
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
GIT_TAG=$(git describe --tags --always --dirty 2>/dev/null | tr -d '`' || echo "unknown")
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')

log_info "Version Info:"
echo "  Commit: ${GIT_COMMIT}"
echo "  Branch: ${GIT_BRANCH}"
echo "  Tag:    ${GIT_TAG}"
echo "  Time:   ${BUILD_TIME}"

# LDFLAGS matching existing legacy scripts
# Note: 'gossipnode' seems to be the module name based on legacy scripts
LDFLAGS="-X 'gossipnode/config/version.gitCommit=${GIT_COMMIT}' \
-X 'gossipnode/config/version.gitBranch=${GIT_BRANCH}' \
-X 'gossipnode/config/version.gitTag=${GIT_TAG}' \
-X 'gossipnode/config/version.buildTime=${BUILD_TIME}' \
-linkmode=external -w -s"

# Build
export CGO_ENABLED=1

# Check for GCC (required for CGO)
if ! check_command gcc; then
    log_die "GCC is required for CGO build but not found. Please run setup_dependencies.sh."
fi

log_info "Ensuring dependencies are clean (go mod tidy)..."
go mod tidy || log_warn "go mod tidy failed, but continuing with build..."

log_info "Running go build..."
if go build -ldflags="${LDFLAGS}" -o "${OUTPUT_DIR}/${APP_NAME}" .; then
    log_ok "Build successful: ${OUTPUT_DIR}/${APP_NAME}"
else
    log_die "Build failed"
fi
