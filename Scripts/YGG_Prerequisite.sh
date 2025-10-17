#!/bin/bash
# install_yggdrasil.sh
# Script to install Yggdrasil using official Debian package method
# Compatible with Ubuntu, Debian, elementaryOS, Linux Mint and similar distributions
# Based on: https://yggdrasil-network.github.io/installation-linux-deb.html

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_status() {
    echo -e "${BLUE}[INFO]${NC} $1" >&2
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1" >&2
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1" >&2
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

# Function to check if running on supported OS
check_os() {
    if [ ! -f /etc/os-release ]; then
        print_error "Cannot determine OS version"
        exit 1
    fi
    
    . /etc/os-release
    if [[ "$ID" != "ubuntu" && "$ID" != "debian" && "$ID" != "elementary" && "$ID" != "linuxmint" ]]; then
        print_warning "This script is designed for Debian-based distributions. Detected: $ID"
        read -p "Continue anyway? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            print_status "Installation cancelled"
            exit 0
        fi
    fi
}

# Function to check if systemd is available
check_systemd() {
    if ! command -v systemctl >/dev/null 2>&1; then
        print_error "systemd is required but not found. This script requires a systemd-based system."
        exit 1
    fi
}

# Function to check if Yggdrasil is already installed
check_existing_installation() {
    if command -v yggdrasil >/dev/null 2>&1; then
        local current_version=$(yggdrasil -version 2>/dev/null | head -n1 || echo "unknown")
        print_warning "Yggdrasil is already installed: $current_version"
        read -p "Do you want to reinstall/update? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            print_status "Installation cancelled"
            exit 0
        fi
    fi
}

print_status "Starting Yggdrasil installation using official Debian package method..."

# Check OS compatibility
check_os

# Check systemd availability
check_systemd

# Check for existing installation
check_existing_installation

print_status "Installing dependencies..."
sudo apt update -y
sudo apt install -y curl dirmngr

print_status "Setting up Yggdrasil repository..."

# Create directory for apt keys
sudo mkdir -p /usr/local/apt-keys

# Import repository key
print_status "Importing Yggdrasil repository key..."
gpg --fetch-keys https://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/key.txt
gpg --export BC1BF63BD10B8F1A | sudo tee /usr/local/apt-keys/yggdrasil-keyring.gpg > /dev/null

# Add repository to apt sources
print_status "Adding Yggdrasil repository to apt sources..."
echo 'deb [signed-by=/usr/local/apt-keys/yggdrasil-keyring.gpg] http://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/ debian yggdrasil' | sudo tee /etc/apt/sources.list.d/yggdrasil.list

# Update package list
print_status "Updating package list..."
sudo apt update

print_status "Installing Yggdrasil package..."
sudo apt install -y yggdrasil

print_status "Enabling and starting Yggdrasil service..."
sudo systemctl enable yggdrasil
sudo systemctl start yggdrasil

print_success "Yggdrasil installation completed successfully!"

# Display service status
print_status "Service status:"
sudo systemctl status yggdrasil --no-pager

print_status "Configuration file location: /etc/yggdrasil.conf"
print_status "To make configuration changes, edit /etc/yggdrasil.conf and restart the service:"
print_status "  sudo systemctl restart yggdrasil"

print_success "Installation complete! Yggdrasil is now running."