#!/bin/bash
# build.sh
# ------------------------------------------------------------------
# Standardized build script for JMDN
# Usage: ./build.sh [output_dir]
# ------------------------------------------------------------------

set -euo pipefail

# Config
APP_NAME="jmdn"
DEFAULT_OUTPUT_DIR="."
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Colors
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC}  $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
die()   { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# Args
OUTPUT_DIR="${1:-$DEFAULT_OUTPUT_DIR}"

info "Building ${APP_NAME}..."
info "Project Root: ${PROJECT_ROOT}"
info "Output Dir:   ${OUTPUT_DIR}"

cd "${PROJECT_ROOT}" || die "Could not cd to project root"

# Git Metadata
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
GIT_TAG=$(git describe --tags --always --dirty 2>/dev/null | tr -d '`' || echo "unknown")
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')

info "Version Info:"
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
if ! command -v gcc >/dev/null 2>&1; then
    die "GCC is required for CGO build but not found. Please run setup_dependencies.sh."
fi

info "Ensuring dependencies are clean (go mod tidy)..."
go mod tidy || warn "go mod tidy failed, but continuing with build..."

info "Running go build..."
if go build -ldflags="${LDFLAGS}" -o "${OUTPUT_DIR}/${APP_NAME}" .; then
    ok "Build successful: ${OUTPUT_DIR}/${APP_NAME}"
else
    die "Build failed"
fi
