#!/bin/bash
# install_docker.sh
# Script to install Docker and Docker Compose on Debian/Ubuntu systems.

set -e

echo "=== Updating system packages ==="
sudo apt update -y
sudo apt upgrade -y

echo "=== Installing dependencies ==="
sudo apt install -y apt-transport-https ca-certificates curl gnupg lsb-release

echo "=== Adding Docker’s official GPG key ==="
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg

echo "=== Adding Docker repository ==="
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

echo "=== Installing Docker Engine and Compose ==="
sudo apt update -y
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

echo "=== Enabling and starting Docker service ==="
sudo systemctl enable docker
sudo systemctl start docker

echo "=== Verifying Docker installation ==="
docker --version
docker compose version

echo "=== Adding current user to Docker group ==="
sudo usermod -aG docker $USER
echo "⚠️  You must log out and back in (or run 'newgrp docker') for group changes to take effect."

echo "✅ Docker and Docker Compose installation complete!"