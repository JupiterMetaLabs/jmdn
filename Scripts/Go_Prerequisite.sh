#!/bin/bash

# Go Installation Script for Ubuntu and macOS
# Downloads and installs the latest Go version from https://go.dev/dl/

set -e  # Exit on any error

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

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
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
        *)
            print_error "Unsupported architecture: $arch" >&2
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
            print_error "Unsupported OS: $os" >&2
            print_error "This script only supports macOS (darwin) and Linux" >&2
            exit 1
            ;;
    esac
    
    # Only output OS and architecture to stdout
    echo "$os $arch"
}

# Function to get latest Go version
get_latest_go_version() {
    print_status "Fetching latest Go version..."
    
    # Try to get version from the API first
    local version=$(curl -s "https://go.dev/dl/?mode=json" 2>/dev/null | grep -o '"version":"[^"]*"' | head -n1 | cut -d'"' -f4)
    
    # If API fails, try alternative method
    if [ -z "$version" ]; then
        print_warning "API method failed, trying alternative method..."
        version=$(curl -s "https://go.dev/dl/" 2>/dev/null | grep -o 'go[0-9]\+\.[0-9]\+\.[0-9]\+' | head -n1 | sed 's/go//')
    fi
    
    if [ -z "$version" ]; then
        print_error "Could not determine latest Go version" >&2
        exit 1
    fi
    
    print_status "Latest Go version: $version"
    # Only output the version number to stdout
    echo "$version"
}

# Function to check if Go is already installed
check_existing_go() {
    if command_exists go; then
        local current_version=$(go version | grep -o 'go[0-9]\+\.[0-9]\+\.[0-9]\+' | sed 's/go//')
        print_warning "Go is already installed (version: $current_version)"
        read -p "Do you want to continue and install the latest version? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            print_status "Installation cancelled"
            exit 0
        fi
    fi
}

# Function to install Go
install_go() {
    local version=$1
    local os=$2
    local arch=$3
    
    local filename="go${version}.${os}-${arch}.tar.gz"
    local url="https://go.dev/dl/${filename}"
    local install_dir="/usr/local"
    
    print_status "Installing Go $version for $os-$arch..."
    print_status "Download URL: $url"
    
    # Check if we have permission to write to /usr/local
    if [ ! -w "$install_dir" ] && [ "$EUID" -ne 0 ]; then
        print_error "Permission denied. This script needs sudo privileges to install Go in $install_dir"
        print_status "Please run: sudo $0"
        exit 1
    fi
    
    # Create temporary directory
    local temp_dir=$(mktemp -d)
    cd "$temp_dir"
    
    # Download Go
    print_status "Downloading $filename..."
    if ! curl -L -o "$filename" "$url"; then
        print_error "Failed to download Go from $url"
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
    
    print_success "Download completed successfully!"
    
    # Remove existing Go installation
    print_status "Removing existing Go installation..."
    if [ -d "$install_dir/go" ]; then
        rm -rf "$install_dir/go"
    fi
    
    # Extract Go
    print_status "Extracting Go to $install_dir..."
    if ! tar -C "$install_dir" -xzf "$filename"; then
        print_error "Failed to extract Go archive"
        rm -rf "$temp_dir"
        exit 1
    fi
    
    # Clean up
    rm -rf "$temp_dir"
    
    print_success "Go $version extracted successfully!"
}

# Function to update PATH
update_path() {
    local go_bin="/usr/local/go/bin"
    local shell_config=""
    
    # Determine shell configuration file
    if [ -n "$ZSH_VERSION" ]; then
        shell_config="$HOME/.zshrc"
    elif [ -n "$BASH_VERSION" ]; then
        shell_config="$HOME/.bashrc"
    else
        # Try to detect from SHELL variable
        case "$SHELL" in
            */zsh)
                shell_config="$HOME/.zshrc"
                ;;
            */bash)
                shell_config="$HOME/.bashrc"
                ;;
            *)
                print_warning "Could not determine shell configuration file"
                return 1
                ;;
        esac
    fi
    
    # Check if Go is already in PATH
    if echo "$PATH" | grep -q "$go_bin"; then
        print_status "Go is already in PATH"
        return 0
    fi
    
    # Add Go to PATH
    print_status "Adding Go to PATH in $shell_config..."
    
    # Create shell config if it doesn't exist
    if [ ! -f "$shell_config" ]; then
        touch "$shell_config"
    fi
    
    # Add Go to PATH if not already present
    if ! grep -q "export PATH.*go/bin" "$shell_config"; then
        echo "" >> "$shell_config"
        echo "# Go" >> "$shell_config"
        echo "export PATH=\$PATH:$go_bin" >> "$shell_config"
        print_success "Added Go to PATH in $shell_config"
    else
        print_status "Go PATH already configured in $shell_config"
    fi
    
    # Update current session PATH
    export PATH="$PATH:$go_bin"
    
    # Source the shell config file to update PATH in current session
    print_status "Sourcing shell configuration to update PATH in current session..."
    if [ -n "$BASH_VERSION" ]; then
        # Try .bashrc first, then .bash_profile (macOS), then .profile
        if [ -f ~/.bashrc ]; then
            source ~/.bashrc 2>/dev/null || true
        elif [ -f ~/.bash_profile ]; then
            source ~/.bash_profile 2>/dev/null || true
        elif [ -f ~/.profile ]; then
            source ~/.profile 2>/dev/null || true
        fi
    elif [ -n "$ZSH_VERSION" ]; then
        if [ -f ~/.zshrc ]; then
            source ~/.zshrc 2>/dev/null || true
        fi
    else
        # Fallback: try to source common config files
        if [ -f ~/.bashrc ]; then
            source ~/.bashrc 2>/dev/null || true
        elif [ -f ~/.bash_profile ]; then
            source ~/.bash_profile 2>/dev/null || true
        elif [ -f ~/.profile ]; then
            source ~/.profile 2>/dev/null || true
        elif [ -f ~/.zshrc ]; then
            source ~/.zshrc 2>/dev/null || true
        fi
    fi
    
    print_status "PATH updated in current session"
}

# Function to verify installation
verify_installation() {
    print_status "Verifying Go installation..."
    
    # Change to a safe directory for Go commands
    local original_dir=$(pwd)
    cd /tmp 2>/dev/null || cd / 2>/dev/null || true
    
    if command_exists go; then
        local installed_version=$(go version 2>/dev/null)
        print_success "Go installation verified!"
        print_success "Version: $installed_version"
        
        # Test Go installation
        print_status "Testing Go installation..."
        if go version >/dev/null 2>&1; then
            print_success "Go is working correctly!"
        else
            print_warning "Go command found but may have issues. Try running 'go version' manually."
        fi
    else
        print_error "Go command not found. Installation may have failed."
        print_status "Please try running: source ~/.bashrc && go version"
        return 1
    fi
    
    # Return to original directory
    cd "$original_dir" 2>/dev/null || true
}

# Main function
main() {
    print_status "Starting Go installation script..."
    
    # Check dependencies
    if ! command_exists curl; then
        print_error "curl is required but not installed. Please install curl first."
        exit 1
    fi
    
    # Detect OS and architecture
    print_status "Detecting system information..."
    local os_arch=$(detect_os_arch)
    local os=$(echo $os_arch | cut -d' ' -f1)
    local arch=$(echo $os_arch | cut -d' ' -f2)
    print_status "Detected OS: $os, Architecture: $arch"
    
    # Check for existing Go installation
    check_existing_go
    
    # Get latest version
    local version=$(get_latest_go_version)
    
    # Install Go
    install_go "$version" "$os" "$arch"
    
    # Update PATH
    update_path
    
    # Verify installation
    verify_installation
    
    print_success "Go installation completed successfully!"
    print_status "To use Go in your current session, run:"
    print_status "  source ~/.bashrc"
    print_status "  go version"
    print_status "Or simply restart your terminal."
}

# Run main function
main "$@"