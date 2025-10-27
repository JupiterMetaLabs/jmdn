#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="jmdn"
REPO_DIR="/root/JMZK-Decentalized-Network"
BIN_PATH="${REPO_DIR}/jmdn"
LOG_PATH="/root/jmdn.log"
UPDATE_SCRIPT="/usr/local/bin/jmdn-update.sh"
UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
CRON_LOG="/root/jmdn-cron.log"
CRON_ENTRY="* * * * * ${UPDATE_SCRIPT} >> ${CRON_LOG} 2>&1"

# --- Sanity checks ---
if [ ! -d "$REPO_DIR" ]; then
  echo "❌ Repo dir ${REPO_DIR} not found. Clone your repo there first."
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "❌ Go toolchain not found in PATH. Install Go and re-run."
  exit 1
fi

# --- 0. Ensure cron log file exists ---
echo "[init] Ensuring cron log file exists..."
touch "$CRON_LOG"
chmod 644 "$CRON_LOG"

# --- 1. Create update script ---
echo "[init] Writing updater script: ${UPDATE_SCRIPT}"
cat > "$UPDATE_SCRIPT" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

SERVICE_NAME="jmdn"
REPO_DIR="/root/JMZK-Decentalized-Network"
BIN_PATH="${REPO_DIR}/jmdn"

cd "$REPO_DIR"

# Ensure upstream is set
CURR_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if ! git rev-parse --abbrev-ref "@{u}" &>/dev/null; then
  git branch --set-upstream-to="origin/${CURR_BRANCH}" "${CURR_BRANCH}" || true
fi

git fetch --prune --quiet
LOCAL_SHA="$(git rev-parse HEAD)"
REMOTE_SHA="$(git rev-parse @{u} 2>/dev/null || echo "$LOCAL_SHA")"

if [ "$LOCAL_SHA" = "$REMOTE_SHA" ]; then
  echo "[update] No repo changes."
  exit 0
fi

echo "[update] Changes detected — pulling and rebuilding."
git pull --rebase --autostash
go mod tidy || true
go build -o "${BIN_PATH}.new" .

if [ ! -x "${BIN_PATH}.new" ]; then
  echo "❌ Build failed."
  exit 1
fi

mv -f "${BIN_PATH}.new" "${BIN_PATH}"
chmod +x "${BIN_PATH}"
echo "[update] New binary deployed."

systemctl restart "${SERVICE_NAME}"
echo "[update] ${SERVICE_NAME} restarted successfully."
EOF

chmod +x "$UPDATE_SCRIPT"

# --- 2. Create systemd unit ---
echo "[init] Writing systemd unit: ${UNIT_FILE}"
cat > "$UNIT_FILE" <<EOF
[Unit]
Description=JMZK Decentralized Network Node (jmdn)
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${REPO_DIR}
ExecStartPre=${UPDATE_SCRIPT}
ExecStart=${BIN_PATH} \
  -heartbeat 10 \
  -metrics 8081 \
  -api 8090 \
  -blockgen 15050 \
  -did 0.0.0.0:15052 \
  -cli 15053 \
  -seednode 34.174.233.203:17002 \
  -facade 8545 \
  -ws 8546 \
  -chainID 7000700 \
  -mempool 34.129.53.115:18001 \
  -explorer
Restart=always
RestartSec=10
NoNewPrivileges=true
LimitNOFILE=1048576
StandardOutput=append:${LOG_PATH}
StandardError=append:${LOG_PATH}

[Install]
WantedBy=multi-user.target
EOF

# --- 3. Enable + start service ---
echo "[init] Reloading systemd and starting ${SERVICE_NAME}..."
systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"

# --- 4A. Add cron job for updater ---
echo "[init] Setting up cron job to run every minute..."
( crontab -l 2>/dev/null || true ) > /tmp/current_cron.txt

if grep -Fq "$UPDATE_SCRIPT" /tmp/current_cron.txt; then
  echo "✅ Cron job already exists."
else
  echo "$CRON_ENTRY" >> /tmp/current_cron.txt
  crontab /tmp/current_cron.txt
  echo "✅ Cron job added: runs every minute."
fi

# --- 4B. Add cron job to start ImmuDB in screen at reboot ---
echo "[init] Ensuring ImmuDB @reboot cron is configured..."
IMMUD_CRON="@reboot sleep 10 && screen -dmS immu /usr/local/bin/immudb --dir ${REPO_DIR}/data >> /root/immudb.log 2>&1"

if grep -Fq "/usr/local/bin/immudb" /tmp/current_cron.txt; then
  echo "✅ ImmuDB @reboot cron already exists."
else
  echo "$IMMUD_CRON" >> /tmp/current_cron.txt
  crontab /tmp/current_cron.txt
  echo "✅ ImmuDB @reboot cron added."
fi

# Ensure cron daemon is running
if systemctl is-active --quiet cron 2>/dev/null; then
  echo "✅ Cron service active."
elif systemctl is-active --quiet crond 2>/dev/null; then
  echo "✅ Crond service active."
else
  echo "⚠️ Cron service not running. Starting it now..."
  (systemctl start cron 2>/dev/null || systemctl start crond 2>/dev/null) || true
fi
systemctl daemon-reload

# --- 5. Summary ---
echo "✅ Bootstrap complete."
echo "• Service:    systemctl status ${SERVICE_NAME}"
echo "• Logs:       journalctl -u ${SERVICE_NAME} -f"
echo "• File Log:   tail -f ${LOG_PATH}"
echo "• Cron Log:   tail -f ${CRON_LOG}"
echo "• Manual Run: ${UPDATE_SCRIPT}"