#!/bin/bash
# start_JMDN.sh — Start the JMDN Daemon
# --------------------------------------------------------------
# Starts ImmuDB (if not already running) and then launches the JMDN 
# (JMZK Decentralized Network) binary. Both ImmuDB and JMDN use the same 
# data directory to ensure consistency. Handles logging, PID management,
# and graceful shutdown signals.
#
# ImmuDB data directory: ${WORK_DIR}/data (default: /opt/jmdn/data)
# JMDN working directory: ${WORK_DIR} (default: /opt/jmdn)
#
# Usage:
#   sudo ./scripts/start_JMDN.sh [--background] [additional flags...]
#
# Environment variables:
#   WORK_DIR    - Working directory for JMDN (default: /opt/jmdn or auto-detected)
#   DATA_DIR    - Data directory for ImmuDB (default: ${WORK_DIR}/data)
#   BIN_PATH    - Path to jmdn binary (default: /usr/local/bin/jmdn)
# --------------------------------------------------------------

set -euo pipefail

# ===== Basic Setup =====
APP_NAME="jmdn"
BIN_PATH="${BIN_PATH:-/usr/local/bin/jmdn}"
CONF_ENV="${CONF_ENV:-/etc/${APP_NAME}/config.env}"
LOG_DIR="${LOG_DIR:-/var/log/${APP_NAME}}"
PID_FILE="${PID_FILE:-/var/run/${APP_NAME}.pid}"
IMMUDB_PID_FILE="${IMMUDB_PID_FILE:-/var/run/immudb.pid}"

# Working directory - where JMDN runs and where data is stored
# Default: if binary is in /usr/local/bin, use /opt/jmdn, else use parent directory of binary
if [[ "${BIN_PATH}" == /usr/local/bin/* ]]; then
  WORK_DIR="${WORK_DIR:-/opt/jmdn}"
else
  # If binary path is relative or in current directory structure, try to find repo root
  BIN_DIR="$(cd "$(dirname "${BIN_PATH}")" 2>/dev/null && pwd)" || BIN_DIR="$(dirname "${BIN_PATH}")"
  if [[ -d "${BIN_DIR}/../data" ]]; then
    # If data directory exists relative to binary, use that structure
    WORK_DIR="${WORK_DIR:-$(cd "${BIN_DIR}/.." 2>/dev/null && pwd)}"
  else
    # Default to /opt/jmdn for system installs
    WORK_DIR="${WORK_DIR:-/opt/jmdn}"
  fi
fi
DATA_DIR="${DATA_DIR:-${WORK_DIR}/data}"

RUN_BACKGROUND="${1:-}"

# Default JMDN command-line arguments (can be overridden via config.env or command-line)
JMDN_ARGS=(
  -heartbeat 10
  -metrics 8081
  -api 8090
  -blockgen 15050
  -did 0.0.0.0:15052
  -cli 15053
  -seednode 34.174.94.172:17002
  -facade 8545
  -ws 8546
  -chainID 7000700
  -mempool 34.174.252.23:18001
  -explorer
  -blockgrpc 15055
  -alias "$(hostname)"
)

# ===== Utility colors =====
BLUE='\033[0;34m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC}  $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
die()   { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# ===== Pre-flight checks =====
[ -x "${BIN_PATH}" ] || die "Binary not executable: ${BIN_PATH}"
mkdir -p "${LOG_DIR}"
mkdir -p "${DATA_DIR}"
# Ensure DB directory exists for SQLite database
mkdir -p "${WORK_DIR}/DB"
chmod 755 "${WORK_DIR}/DB"

# ===== ImmuDB setup =====
# Check if ImmuDB is installed
if ! command -v immudb >/dev/null 2>&1; then
  die "ImmuDB not found. Please install ImmuDB first (see ./Scripts/ImmuDB_Prerequisite.sh)"
fi

# Check if ImmuDB is already running
IMMUDB_RUNNING=false
if [ -f "${IMMUDB_PID_FILE}" ] && kill -0 "$(cat "${IMMUDB_PID_FILE}")" 2>/dev/null; then
  IMMUDB_RUNNING=true
  info "ImmuDB already running with PID $(cat "${IMMUDB_PID_FILE}")"
elif pgrep -f "immudb.*--dir.*${DATA_DIR}" >/dev/null 2>&1; then
  IMMUDB_RUNNING=true
  info "ImmuDB process detected for data directory ${DATA_DIR}"
fi

# Track if we started ImmuDB (for cleanup)
IMMUDB_STARTED_BY_SCRIPT=false

# Start ImmuDB if not running
if [ "$IMMUDB_RUNNING" = false ]; then
  IMMUDB_STARTED_BY_SCRIPT=true
  info "Starting ImmuDB with data directory: ${DATA_DIR}"
  
  # Ensure data directory has proper permissions
  chmod 755 "${DATA_DIR}" 2>/dev/null || true
  
  # Start ImmuDB in the background with the shared data directory
  cd "${WORK_DIR}" || die "Cannot change to working directory: ${WORK_DIR}"
  
  # Clear any old log entries for fresh start
  echo "Starting ImmuDB at $(date)" >>"${LOG_DIR}/immudb.log" 2>&1 || true
  
  # Start ImmuDB with explicit flags for better compatibility
  nohup immudb --dir "${DATA_DIR}" >>"${LOG_DIR}/immudb.log" 2>&1 &
  IMMUDB_PID=$!
  
  # Wait briefly to see if the process starts
  sleep 1
  
  # Check if process is still running immediately
  if ! kill -0 "${IMMUDB_PID}" 2>/dev/null; then
    warn "ImmuDB process exited immediately. Checking logs..."
    # Show last 20 lines of log for debugging
    if [ -f "${LOG_DIR}/immudb.log" ]; then
      warn "Last 20 lines of ImmuDB log:"
      tail -n 20 "${LOG_DIR}/immudb.log" || true
    fi
    die "ImmuDB failed to start. Process exited with PID ${IMMUDB_PID}. Check logs: ${LOG_DIR}/immudb.log"
  fi
  
  echo "${IMMUDB_PID}" > "${IMMUDB_PID_FILE}"
  info "ImmuDB started with PID ${IMMUDB_PID}"
  
  # Wait for ImmuDB to be ready with progressive checks
  info "Waiting for ImmuDB to be ready..."
  IMMUDB_READY=false
  for i in {1..10}; do
    sleep 1
    if ! kill -0 "${IMMUDB_PID}" 2>/dev/null; then
      warn "ImmuDB process died after ${i} seconds. Checking logs..."
      if [ -f "${LOG_DIR}/immudb.log" ]; then
        warn "Last 30 lines of ImmuDB log:"
        tail -n 30 "${LOG_DIR}/immudb.log" || true
      fi
      die "ImmuDB failed to start. Process exited after ${i} seconds. Check logs: ${LOG_DIR}/immudb.log"
    fi
    
    # Try to connect via immuclient if available
    if command -v immuclient >/dev/null 2>&1; then
      if timeout 2 immuclient status >/dev/null 2>&1; then
        IMMUDB_READY=true
        break
      fi
    else
      # If no immuclient, just check if process is still running after a few seconds
      if [ $i -ge 3 ]; then
        IMMUDB_READY=true
        break
      fi
    fi
  done
  
  # Final verification
  if ! kill -0 "${IMMUDB_PID}" 2>/dev/null; then
    if [ -f "${LOG_DIR}/immudb.log" ]; then
      warn "Last 30 lines of ImmuDB log:"
      tail -n 30 "${LOG_DIR}/immudb.log" || true
    fi
    die "ImmuDB process died during startup. Check logs: ${LOG_DIR}/immudb.log"
  fi
  
  if [ "$IMMUDB_READY" = true ]; then
    ok "ImmuDB is running and ready"
  else
    warn "ImmuDB is running but connection check timed out (this may be normal)"
  fi
else
  info "Using existing ImmuDB instance"
fi

# ===== Load environment (optional) =====
if [ -f "${CONF_ENV}" ]; then
  info "Loading configuration from ${CONF_ENV}"
  set -a
  # shellcheck disable=SC1090
  source "${CONF_ENV}"
  set +a
  # If JMDN_ARGS is set in config.env, use it instead of defaults
  if [ -n "${JMDN_ARGS_OVERRIDE:-}" ]; then
    read -ra JMDN_ARGS <<< "${JMDN_ARGS_OVERRIDE}"
  fi
else
  info "No config file found at ${CONF_ENV}, using default arguments"
fi

# ===== Parse additional command-line arguments =====
# Collect all arguments except --background
EXTRA_ARGS=()
for arg in "$@"; do
  if [ "${arg}" != "--background" ]; then
    EXTRA_ARGS+=("${arg}")
  fi
done

# Use custom args if provided, otherwise use defaults
FINAL_ARGS=("${JMDN_ARGS[@]}")
if [ ${#EXTRA_ARGS[@]} -gt 0 ]; then
  FINAL_ARGS=("${EXTRA_ARGS[@]}")
  info "Using command-line arguments instead of defaults"
fi

# ===== Ensure no duplicate instance =====
if [ -f "${PID_FILE}" ] && kill -0 "$(cat "${PID_FILE}")" 2>/dev/null; then
  warn "${APP_NAME} already running with PID $(cat "${PID_FILE}")"
  exit 0
fi

# ===== Logging setup =====
LOG_FILE="${LOG_DIR}/${APP_NAME}.log"
info "Logging to ${LOG_FILE}"

# ===== Start Daemon =====
info "Starting ${APP_NAME} daemon with arguments: ${FINAL_ARGS[*]}"
info "Working directory: ${WORK_DIR}"
info "Data directory: ${DATA_DIR}"

# Ensure we're in the working directory when starting JMDN
cd "${WORK_DIR}" || warn "Warning: Could not change to working directory ${WORK_DIR}, using current directory"

# Marker file to track that first start has completed
FIRST_START_MARKER="${WORK_DIR}/.first_start_complete"

# Detect if running under systemd (systemd sets INVOCATION_ID / JOURNAL_STREAM)
if [[ -n "${INVOCATION_ID:-}" || -n "${JOURNAL_STREAM:-}" ]]; then
  # Under systemd: do NOT tee; systemd handles logging (StandardOutput=append)
  if [[ "${RUN_BACKGROUND}" == "--background" ]]; then
      nohup "${BIN_PATH}" "${FINAL_ARGS[@]}" >/dev/null 2>&1 &
      echo $! > "${PID_FILE}"
      ok "Started ${APP_NAME} (PID $(cat "${PID_FILE}"))"
      # Create marker file to indicate first start completed
      touch "${FIRST_START_MARKER}"
      info "First start completed. Marker file created: ${FIRST_START_MARKER}"
  else
    # Cleanup function for foreground mode
    cleanup() {
      warn "Stopping ${APP_NAME}..."
      if [ "$IMMUDB_STARTED_BY_SCRIPT" = true ] && [ -f "${IMMUDB_PID_FILE}" ]; then
        warn "Stopping ImmuDB (started by this script)..."
        kill "$(cat "${IMMUDB_PID_FILE}")" 2>/dev/null || true
        rm -f "${IMMUDB_PID_FILE}"
      fi
      kill 0
    }
      trap cleanup SIGINT SIGTERM
      # Create marker file before exec (process will replace this script)
      touch "${FIRST_START_MARKER}"
      info "First start completed. Marker file created: ${FIRST_START_MARKER}"
      exec "${BIN_PATH}" "${FINAL_ARGS[@]}"
  fi
else
  # Not under systemd: keep existing tee-to-file behavior
  if [[ "${RUN_BACKGROUND}" == "--background" ]]; then
      nohup "${BIN_PATH}" "${FINAL_ARGS[@]}" >>"${LOG_FILE}" 2>&1 &
      echo $! > "${PID_FILE}"
      ok "Started ${APP_NAME} (PID $(cat "${PID_FILE}"))"
      # Create marker file to indicate first start completed
      touch "${FIRST_START_MARKER}"
      info "First start completed. Marker file created: ${FIRST_START_MARKER}"
      # Note: ImmuDB will continue running in background, which is desired
  else
    # Cleanup function for foreground mode
    cleanup() {
      warn "Stopping ${APP_NAME} and ImmuDB..."
      if [ "$IMMUDB_STARTED_BY_SCRIPT" = true ] && [ -f "${IMMUDB_PID_FILE}" ]; then
        warn "Stopping ImmuDB (started by this script)..."
        kill "$(cat "${IMMUDB_PID_FILE}")" 2>/dev/null || true
        rm -f "${IMMUDB_PID_FILE}"
      fi
      kill 0
    }
      trap cleanup SIGINT SIGTERM
      # Create marker file before starting (will be created when process starts)
      touch "${FIRST_START_MARKER}"
      info "First start completed. Marker file created: ${FIRST_START_MARKER}"
      "${BIN_PATH}" "${FINAL_ARGS[@]}" | tee -a "${LOG_FILE}"
  fi
fi
