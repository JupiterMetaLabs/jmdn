#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# setup_certs.sh - Cross-Platform TLS Certificate Generator for JMDN
# ==============================================================================
#
# CHANGELOG:
#   v2.0.0 (2026-03-02):
#     - Changed shebang to #!/usr/bin/env bash for better portability
#     - Added sourcing of lib/platform.sh for cross-platform utilities
#     - Replaced inline IP detection with detect_local_ip function from platform.sh
#     - Changed process substitution to temporary file for broader shell compatibility
#     - Ensures full cross-platform support (Linux, macOS, FreeBSD)
#
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
#
# REQUIREMENTS:
#   - bash (sh-compatible via #!/usr/bin/env bash)
#   - openssl
#   - Platform detection library (lib/platform.sh in same parent directory)
# ==============================================================================

# Source the platform detection library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/platform.sh"

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

# Detect Local IP using cross-platform helper
LOCAL_IP=$(detect_local_ip)

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
    local tmpfile

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
    # Create temporary file for extensions (cross-platform compatible)
    tmpfile=$(mktemp) || { echo "ERROR: mktemp failed for $name"; return 1; }
    trap "rm -f '$tmpfile'" RETURN

    printf "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:${LOCAL_IP}" > "$tmpfile"

    openssl x509 -req -in "$CERT_DIR/$name.csr" \
        -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" -CAcreateserial \
        -out "$CERT_DIR/$name.crt" -days 365 -sha256 \
        -extfile "$tmpfile" 2>/dev/null

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
