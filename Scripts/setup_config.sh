#!/bin/bash
# setup_config.sh
# ------------------------------------------------------------------
# Interactive CLI to generate /etc/jmdn/jmdn.yaml
# ------------------------------------------------------------------

set -e

# Config
DEFAULT_TEMPLATE="jmdn_default.yaml"
TARGET_DIR="/etc/jmdn"
TARGET_FILE="${TARGET_DIR}/jmdn.yaml"

# Colors
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'

echo -e "${GREEN}=== JMDN Configuration Setup ===${NC}"

if [ "$EUID" -ne 0 ]; then
  echo -e "${RED}Please run as root to write to /etc/jmdn${NC}"
  exit 1
fi

mkdir -p "${TARGET_DIR}"

if [ -f "${TARGET_FILE}" ]; then
    echo -e "${YELLOW}Configuration file already exists at ${TARGET_FILE}${NC}"
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
        echo -e "${RED}Template ${DEFAULT_TEMPLATE} not found! Run this from the repo root.${NC}"
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

# Apply changes using sed (basic key replacement)
if [[ "$(uname)" == "Darwin" ]]; then SED_CMD="sed -i ''"; else SED_CMD="sed -i"; fi

# Update Alias
$SED_CMD "s/alias: \"\"/alias: \"${ALIAS}\"/" "${TARGET_FILE}"

# Update Seed
if [ -n "$SEED" ]; then
    # Escape slashes
    SEED_ESC=$(echo "$SEED" | sed 's/\//\\\//g')
    $SED_CMD "s/seednode: \"\"/seednode: \"${SEED_ESC}\"/" "${TARGET_FILE}"
fi

# Update Explorer features
if [[ "$EXPLORER" == "y" ]]; then
    $SED_CMD "s/explorer: false/explorer: true/" "${TARGET_FILE}"
    # Also enable API port
    $SED_CMD "s/api: 0/api: 8090/" "${TARGET_FILE}"
fi

echo -e "${GREEN}Configuration generated at ${TARGET_FILE}${NC}"
echo -e "${YELLOW}Please review the file and add secrets (JWT, API Keys) manually.${NC}"
