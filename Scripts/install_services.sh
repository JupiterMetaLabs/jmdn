#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/platform.sh"

APP_NAME="jmdn"
SERVICE_USER="${SERVICE_USER:-root}"

require_root

log_info "Creating directories..."
ensure_dir "${JMDN_DATA}" "755"
ensure_dir "${JMDN_DATA}/data" "755"
ensure_dir "${JMDN_DATA}/config" "755"
ensure_dir "${JMDN_LOG}" "755"
ensure_dir "${JMDN_ETC}" "755"

if [ -f "Scripts/start_jmdn_wrapper.sh" ]; then
	cp "Scripts/start_jmdn_wrapper.sh" "${JMDN_BIN}/start_jmdn_wrapper.sh"
	chmod +x "${JMDN_BIN}/start_jmdn_wrapper.sh"
fi

if [ -f "${APP_NAME}" ]; then
	cp "${APP_NAME}" "${JMDN_BIN}/${APP_NAME}"
	chmod +x "${JMDN_BIN}/${APP_NAME}"
fi

install_systemd_services() {
	cat >"/etc/systemd/system/${APP_NAME}.service" <<EOF
[Unit]
Description=JMDT Decentralized Network Node (jmdn)
After=network.target

[Service]
Type=simple
User=${SERVICE_USER}
WorkingDirectory=${JMDN_DATA}
Environment="WORK_DIR=${JMDN_DATA}"
Environment="DATA_DIR=${JMDN_DATA}/data"
ExecStart=${JMDN_BIN}/start_jmdn_wrapper.sh
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
	svc_reload_daemon
	svc_enable "${APP_NAME}"
}

case "${SVC_MANAGER}" in
systemd)
	install_systemd_services
	;;
*)
	log_warn "Service manager '${SVC_MANAGER}' is not automated. Install ${APP_NAME} service manually."
	;;
esac

log_ok "Service installation completed."
