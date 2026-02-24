#!/bin/bash
set -euo pipefail

# ------------------------------------------------------------------------------
# setup_certs.sh
# ------------------------------------------------------------------------------
# PURPOSE:
#   Generates a local Certificate Authority (CA) and issues TLS certificates
#   for JMDN services. Designed for LOCAL DEVELOPMENT and TESTING.
#
#   For Production/Staging, use the Ansible 'pki' role which manages
#   certificates via the control node.
#
# USAGE:
#   ./Scripts/setup_certs.sh [output_dir]
#
#   Default output: ./certs
# ------------------------------------------------------------------------------

# 1. Configuration
CERT_DIR="${1:-certs}"
mkdir -p "$CERT_DIR"

# Superset of all possible services (Sequencer + Standard Node)
# Matches infra/host_vars/jmdn-sequencer-server.yml
SERVICES=(
    "cli_admin"
    "block_ingest_grpc"
    "block_ingest_http"
    "did_service"
    "explorer_api"
    "mempool_service"
    "admin_client"     # Client identity
    "explorer_client"     # Python Backend Identity
)

# Detect Local IP (Best Effort for Mac/Linux)
LOCAL_IP="127.0.0.1"
if command -v ip >/dev/null; then
    # Linux
    DETECTED=$(ip route get 1 | awk '{print $7;exit}')
    [ -n "$DETECTED" ] && LOCAL_IP=$DETECTED
elif command -v ifconfig >/dev/null; then
    # Mac (en0 usually)
    DETECTED=$(ifconfig en0 2>/dev/null | grep inet | grep -v inet6 | awk '{print $2}')
    [ -n "$DETECTED" ] && LOCAL_IP=$DETECTED
fi

echo "Detected Local IP: ${LOCAL_IP}"

# ------------------------------------------------------------------------------
# 2. Helpers
# ------------------------------------------------------------------------------

generate_ca() {
    if [[ -f "$CERT_DIR/ca.crt" && -f "$CERT_DIR/ca.key" ]]; then
        echo "✅ CA already exists in $CERT_DIR"
        return
    fi

    echo "🔐 Generating Root CA..."
    openssl req -x509 -newkey rsa:4096 -nodes -days 3650 \
        -keyout "$CERT_DIR/ca.key" -out "$CERT_DIR/ca.crt" \
        -subj "/C=US/O=JMDN/CN=JMDN Dev Root CA" 2>/dev/null
}

generate_cert() {
    local name=$1
    if [[ -f "$CERT_DIR/$name.crt" && -f "$CERT_DIR/$name.key" ]]; then
        echo "✅ Certificate for $name already exists"
        return
    fi

    echo "📜 Generating Certificate for $name..."

    # 1. Key
    openssl genrsa -out "$CERT_DIR/$name.key" 2048 2>/dev/null

    # 2. CSR
    # Note: SANs include localhost, 127.0.0.1, and detected LAN IP for cross-machine dev testing
    openssl req -new -key "$CERT_DIR/$name.key" \
        -out "$CERT_DIR/$name.csr" \
        -subj "/C=US/O=JMDN/CN=$name" \
        -addext "subjectAltName = DNS:localhost,IP:127.0.0.1,IP:${LOCAL_IP}" 2>/dev/null

    # 3. Sign
    openssl x509 -req -in "$CERT_DIR/$name.csr" \
        -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" -CAcreateserial \
        -out "$CERT_DIR/$name.crt" -days 365 -sha256 \
        -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:${LOCAL_IP}") 2>/dev/null

    # Cleanup CSR
    rm "$CERT_DIR/$name.csr"
}

# ------------------------------------------------------------------------------
# 3. Execution
# ------------------------------------------------------------------------------

generate_ca

for svc in "${SERVICES[@]}"; do
    generate_cert "$svc"
done

echo ""
echo "🎉 Certificates generated in: $CERT_DIR"
echo "   CA Root: $CERT_DIR/ca.crt"
echo "   Hosts:   localhost, 127.0.0.1, ${LOCAL_IP}"
