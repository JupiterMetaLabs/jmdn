#!/bin/bash
# install_immudb.sh
# Script to install ImmuDB with OS detection
# Compatible with Ubuntu, Debian, macOS, and other Linux distributions

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

# Function to detect OS and architecture
detect_os_arch() {
    local os=$(uname -s | tr '[:upper:]' '[:lower:]')
    local arch=$(uname -m)
    
    # Convert architecture names
    case $arch in
        x86_64)
            arch="amd64"
            ;;
        arm64|aarch64)
            arch="arm64"
            ;;
        i386|i686)
            arch="386"
            ;;
        armv6l)
            arch="armv6l"
            ;;
        armv7l)
            arch="armv7"
            ;;
        *)
            print_error "Unsupported architecture: $arch"
            exit 1
            ;;
    esac
    
    # Validate OS
    case $os in
        darwin)
            os="darwin"
            ;;
        linux)
            os="linux"
            ;;
        *)
            print_error "Unsupported OS: $os"
            print_error "This script only supports macOS (darwin) and Linux"
            exit 1
            ;;
    esac
    
    echo "$os $arch"
}

# Function to check if ImmuDB is already installed
check_existing_installation() {
    if command -v immudb >/dev/null 2>&1; then
        local current_version=$(immudb version 2>/dev/null | head -n1 || echo "unknown")
        print_warning "ImmuDB is already installed: $current_version"
        read -p "Do you want to reinstall/update? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            print_status "Installation cancelled"
            exit 0
        fi
    fi
}

# Function to get latest ImmuDB version
get_latest_version() {
    print_status "Fetching latest ImmuDB version..."
    
    # Try multiple methods to get the latest version
    local version=""
    
    # Method 1: Try GitHub API with jq if available
    if command -v jq >/dev/null 2>&1; then
        version=$(curl -s "https://api.github.com/repos/codenotary/immudb/releases/latest" 2>/dev/null | jq -r '.tag_name' 2>/dev/null)
    fi
    
    # Method 2: Try GitHub API with grep
    if [ -z "$version" ] || [ "$version" = "null" ]; then
        version=$(curl -s "https://api.github.com/repos/codenotary/immudb/releases/latest" 2>/dev/null | grep -o '"tag_name":"[^"]*"' | head -n1 | cut -d'"' -f4)
    fi
    
    # Method 3: Try parsing the releases page
    if [ -z "$version" ] || [ "$version" = "null" ]; then
        print_warning "GitHub API failed, trying alternative method..."
        version=$(curl -s "https://github.com/codenotary/immudb/releases" 2>/dev/null | grep -o 'tag/v[0-9]\+\.[0-9]\+\.[0-9]\+' | head -n1 | sed 's/tag\/v//')
    fi
    
    # Method 4: Use a known stable version as fallback
    if [ -z "$version" ] || [ "$version" = "null" ]; then
        print_warning "Could not fetch latest version, using fallback version..."
        version="1.4.1"  # Known stable version
    fi
    
    # Remove 'v' prefix if present
    version=${version#v}
    print_status "Latest ImmuDB version: $version"
    echo "$version"
}

# Function to install ImmuDB
install_immudb() {
    local version=$1
    local os=$2
    local arch=$3
    
    local filename="immudb-${version}-${os}-${arch}.tar.gz"
    local url="https://github.com/codenotary/immudb/releases/download/v${version}/${filename}"
    local install_dir="/usr/local/bin"
    
    print_status "Installing ImmuDB $version for $os-$arch..."
    print_status "Download URL: $url"
    
    # Validate version is not empty
    if [ -z "$version" ]; then
        print_error "Version is empty, cannot proceed with installation"
        exit 1
    fi
    
    # Check if we have permission to write to /usr/local/bin
    if [ ! -w "$install_dir" ] && [ "$EUID" -ne 0 ]; then
        print_error "Permission denied. This script needs sudo privileges to install ImmuDB in $install_dir"
        print_status "Please run: sudo $0"
        exit 1
    fi
    
    # Create temporary directory
    local temp_dir=$(mktemp -d)
    cd "$temp_dir"
    
    # Download ImmuDB
    print_status "Downloading $filename..."
    if ! curl -L -o "$filename" "$url"; then
        print_error "Failed to download ImmuDB from $url"
        print_error "Please check the URL and try again"
        rm -rf "$temp_dir"
        exit 1
    fi
    
    # Verify download
    if [ ! -f "$filename" ] || [ ! -s "$filename" ]; then
        print_error "Downloaded file is empty or corrupted"
        rm -rf "$temp_dir"
        exit 1
    fi
    
    # Check if downloaded file is actually a tar.gz file
    if ! file "$filename" | grep -q "gzip compressed"; then
        print_error "Downloaded file is not a valid gzip archive"
        print_error "File type: $(file "$filename")"
        print_error "This might indicate the download URL is incorrect"
        rm -rf "$temp_dir"
        exit 1
    fi
    
    print_success "Download completed successfully!"
    
    # Extract ImmuDB
    print_status "Extracting ImmuDB..."
    if ! tar -xzf "$filename"; then
        print_error "Failed to extract ImmuDB archive"
        print_error "Archive might be corrupted or in wrong format"
        rm -rf "$temp_dir"
        exit 1
    fi
    
    # Check if immudb binary exists after extraction
    if [ ! -f "immudb" ]; then
        print_error "ImmuDB binary not found after extraction"
        print_error "Contents of extracted archive:"
        ls -la
        rm -rf "$temp_dir"
        exit 1
    fi
    
    # Install binary
    print_status "Installing ImmuDB binary to $install_dir..."
    sudo cp immudb "$install_dir/"
    sudo chmod +x "$install_dir/immudb"
    
    # Clean up
    rm -rf "$temp_dir"
    
    print_success "ImmuDB $version installed successfully!"
}

# Function to verify installation
verify_installation() {
    print_status "Verifying ImmuDB installation..."
    
    if command -v immudb >/dev/null 2>&1; then
        local installed_version=$(immudb version 2>/dev/null)
        print_success "ImmuDB installation verified!"
        print_success "Version: $installed_version"
        
        # Test ImmuDB installation
        print_status "Testing ImmuDB installation..."
        if immudb version >/dev/null 2>&1; then
            print_success "ImmuDB is working correctly!"
        else
            print_warning "ImmuDB command found but may have issues. Try running 'immudb version' manually."
        fi
    else
        print_error "ImmuDB command not found. Installation may have failed."
        return 1
    fi
}

# Main function
main() {
    print_status "Starting ImmuDB installation script..."
    
    # Check dependencies
    if ! command -v curl >/dev/null 2>&1; then
        print_error "curl is required but not installed. Please install curl first."
        exit 1
    fi
    
    # Detect OS and architecture
    print_status "Detecting system information..."
    local os_arch=$(detect_os_arch)
    local os=$(echo $os_arch | cut -d' ' -f1)
    local arch=$(echo $os_arch | cut -d' ' -f2)
    print_status "Detected OS: $os, Architecture: $arch"
    
    # Check for existing ImmuDB installation
    check_existing_installation
    
    # Get latest version
    local version=$(get_latest_version)
    
    # Install ImmuDB
    install_immudb "$version" "$os" "$arch"
    
    # Verify installation
    verify_installation
    
    print_success "ImmuDB installation completed successfully!"
    print_status "ImmuDB binary installed:"
    print_status "  - immudb: Database server"
    print_status "To start ImmuDB server: immudb"
}

# Run main function
main "$@"
