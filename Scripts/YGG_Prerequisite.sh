#!/bin/bash
# install_yggdrasil.sh
# Script to install Yggdrasil with autoconfiguration and store config.json

set -e

echo "=== Installing dependencies ==="
sudo apt update -y
sudo apt install -y curl jq

echo "=== Downloading Yggdrasil binary ==="
LATEST_URL=$(curl -s https://api.github.com/repos/yggdrasil-network/yggdrasil-go/releases/latest | jq -r '.assets[] | select(.name | test("linux_amd64")) | .browser_download_url')
curl -L "$LATEST_URL" -o /tmp/yggdrasil.tar.gz

echo "=== Extracting Yggdrasil ==="
tar -xzf /tmp/yggdrasil.tar.gz -C /tmp
sudo mv /tmp/yggdrasil /usr/local/bin/
sudo chmod +x /usr/local/bin/yggdrasil

echo "=== Running autoconfiguration ==="
sudo mkdir -p /etc/yggdrasil
sudo yggdrasil -autoconf > /etc/yggdrasil/config.json

echo "=== Creating systemd service ==="
sudo tee /etc/systemd/system/yggdrasil.service > /dev/null <<EOF
[Unit]
Description=Yggdrasil Network Daemon
After=network.target

[Service]
ExecStart=/usr/local/bin/yggdrasil -useconffile /etc/yggdrasil/config.json
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

echo "=== Enabling and starting Yggdrasil service ==="
sudo systemctl daemon-reload
sudo systemctl enable yggdrasil
sudo systemctl start yggdrasil

echo "=== Installation complete ==="
sudo systemctl status yggdrasil --no-pager