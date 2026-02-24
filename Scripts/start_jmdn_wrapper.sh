#!/bin/bash
# start_jmdn_wrapper.sh
# ------------------------------------------------------------------
# Unified Runtime Wrapper for JMDN
# Usage: ./start_jmdn_wrapper.sh [args...]
# ------------------------------------------------------------------

set -euo pipefail

# Config
BIN_PATH="${BIN_PATH:-/usr/local/bin/jmdn}"

# Colors
BLUE='\033[0;34m'; NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC}  $*"; }

# Execution
info "Starting JMDN..."
info "Binary: ${BIN_PATH}"
info "Config: Auto-loaded from /etc/jmdn/jmdn.yaml or ./jmdn.yaml"

# Ensure binary exists
if [ ! -x "${BIN_PATH}" ]; then
    echo "Error: Binary not found or not executable at ${BIN_PATH}"
    exit 1
fi

# Exec replaces the shell process with JMDN
exec "${BIN_PATH}" "$@"
