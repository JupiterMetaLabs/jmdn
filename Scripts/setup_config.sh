#!/usr/bin/env bash
################################################################################
# setup_config.sh
# ------------------------------------------------------------------
# CHANGELOG:
#   - Changed shebang to #!/usr/bin/env bash for portability
#   - Source lib/platform.sh for cross-platform support
#   - Use require_root() instead of $EUID check
#   - Use $JMDN_ETC instead of hardcoded /etc/jmdn
#   - Use sed_inplace() function for macOS/Linux compatibility
#   - Maintains all interactive prompts and config generation logic
# ------------------------------------------------------------------
# Interactive CLI to generate JMDN configuration file
# ------------------------------------------------------------------

set -euo pipefail

# Source the platform helper library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/platform.sh"

# Config
DEFAULT_TEMPLATE="jmdn_default.yaml"
TARGET_DIR="${JMDN_ETC}"
TARGET_FILE="${TARGET_DIR}/jmdn.yaml"

echo -e "${COLOR_GREEN}=== JMDN Configuration Setup ===${COLOR_NC}"

# Require root privileges
require_root

# Ensure target directory exists
mkdir -p "${TARGET_DIR}"

if [ -f "${TARGET_FILE}" ]; then
    echo -e "${COLOR_YELLOW}Configuration file already exists at ${TARGET_FILE}${COLOR_NC}"
    read -p "Overwrite? (y/N): " confirm
    if [[ "$confirm" != "y" ]]; then
        echo "Aborting."
        exit 0
    fi
fi

# Check for template
if [ ! -f "${DEFAULT_TEMPLATE}" ]; then
     # Try one level up if running from scripts dir
    if [ -f "../${DEFAULT_TEMPLATE}" ]; then
        DEFAULT_TEMPLATE="../${DEFAULT_TEMPLATE}"
    else
        echo -e "${COLOR_RED}Template ${DEFAULT_TEMPLATE} not found! Run this from the repo root.${COLOR_NC}"
        exit 1
    fi
fi

echo "Copying template..."
cp "${DEFAULT_TEMPLATE}" "${TARGET_FILE}"
chmod 600 "${TARGET_FILE}"

# Interactive setup
read -p "Enter Node Alias (default: $(hostname)): " ALIAS
ALIAS=${ALIAS:-$(hostname)}

read -p "Enter Seed Node URL (leave empty for default): " SEED
read -p "Enable Explorer? (y/N): " EXPLORER

# Helper: escape a string for use as sed replacement (handles /, &, \)
_sed_escape_replacement() {
    printf '%s' "$1" | sed -e 's/[\/&\\]/\\&/g'
}

# Update Alias using cross-platform sed
ALIAS_ESC=$(_sed_escape_replacement "${ALIAS}")
sed_inplace "s/alias: \"\"/alias: \"${ALIAS_ESC}\"/" "${TARGET_FILE}"

# Update Seed
if [ -n "${SEED:-}" ]; then
    SEED_ESC=$(_sed_escape_replacement "${SEED}")
    sed_inplace "s/seednode: \"\"/seednode: \"${SEED_ESC}\"/" "${TARGET_FILE}"
fi

# Update Explorer features
if [[ "$EXPLORER" == "y" ]]; then
    sed_inplace "s/explorer: false/explorer: true/" "${TARGET_FILE}"
    # Also enable API port
    sed_inplace "s/api: 0/api: 8090/" "${TARGET_FILE}"
fi

echo -e "${COLOR_GREEN}Configuration generated at ${TARGET_FILE}${COLOR_NC}"
echo -e "${COLOR_YELLOW}Please review the file and add secrets (JWT, API Keys) manually.${COLOR_NC}"
