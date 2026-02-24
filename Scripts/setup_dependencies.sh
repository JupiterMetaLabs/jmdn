#!/bin/bash
# setup_dependencies.sh
# ------------------------------------------------------------------
# Installs JMDN dependencies: Go, ImmuDB, Yggdrasil, GCC
# Usage: sudo ./setup_dependencies.sh [options]
# Options:
#   --go          Install Go
#   --immudb      Install ImmuDB
#   --yggdrasil   Install Yggdrasil
#   --all         Install all dependencies (default if no flags provided)
# ------------------------------------------------------------------

set -euo pipefail

# Config
GO_INSTALL_DIR="/usr/local"
IMMUDB_INSTALL_DIR="/usr/local/bin"
# Known stable versions as fallbacks (matching current prod)
IMMUDB_FALLBACK_VER="1.10.0"  
GO_FALLBACK_VER="1.25.3"
YGG_FALLBACK_VER="0.5.12"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()    { echo -e "${BLUE}[INFO]${NC}  $*"; }
success() { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error()   { echo -e "${RED}[ERROR]${NC} $*"; }
die()     { error "$*"; exit 1; }

# Helper: Version Comparison
# Returns 0 (true) if $1 < $2
ver_lt() {
    [ "$1" = "$2" ] && return 1 || [ "$1" = "$(printf "%s\n%s" "$1" "$2" | sort -V | head -n1)" ]
}

# Root check
if [ "$EUID" -ne 0 ]; then
  die "Please run as root: sudo $0 $*"
fi

# Detect OS/Arch
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case $ARCH in
    x86_64)  ARCH_GO="amd64"; ARCH_IMMU="amd64" ;;
    aarch64) ARCH_GO="arm64"; ARCH_IMMU="arm64" ;;
    arm64)   ARCH_GO="arm64"; ARCH_IMMU="arm64" ;;
    *)       die "Unsupported architecture: $ARCH" ;;
esac

case $OS in
    linux)  OS_GO="linux"; OS_IMMU="linux" ;;
    darwin) OS_GO="darwin"; OS_IMMU="darwin" ;;
    *)      die "Unsupported OS: $OS" ;;
esac

# flags
INSTALL_GO=false
INSTALL_IMMUDB=false
INSTALL_YGG=false

# Parse args
if [ $# -eq 0 ]; then
    warn "No arguments provided."
    echo "Usage: sudo $0 [options]"
    echo "Options:"
    echo "  --go          Install Go"
    echo "  --immudb      Install ImmuDB"
    echo "  --yggdrasil   Install Yggdrasil"
    echo "  --all         Install all dependencies"
    exit 1
else
    for arg in "$@"; do
        case $arg in
            --go)        INSTALL_GO=true ;;
            --immudb)    INSTALL_IMMUDB=true ;;
            --yggdrasil) INSTALL_YGG=true ;;
            --all)       INSTALL_GO=true; INSTALL_IMMUDB=true; INSTALL_YGG=true ;;
            *)           die "Unknown argument: $arg" ;;
        esac
    done
fi

# ------------------------------------------------------------------
# 1. System Dependencies (GCC/Build Essentials)
# ------------------------------------------------------------------
install_sys_deps() {
    info "Checking system build dependencies..."
    if command -v gcc >/dev/null 2>&1; then
        success "GCC is already installed: $(gcc --version | head -n1)"
    else 
        warn "GCC not found. Installing..."
    fi

    if command -v git >/dev/null 2>&1; then
        success "Git is already installed: $(git --version | head -n1)"
    else
        warn "Git not found. Installing..."
    fi
    
    if [ "$OS" == "linux" ]; then
        if command -v apt >/dev/null 2>&1; then
             # Install if either is missing
             if ! command -v gcc >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
                apt update -y && apt install -y build-essential curl git
             fi
        elif command -v yum >/dev/null 2>&1; then
             if ! command -v gcc >/dev/null 2>&1; then
                yum groupinstall -y "Development Tools"
             fi
             if ! command -v git >/dev/null 2>&1; then
                yum install -y curl git
             fi
        else
            if ! command -v gcc >/dev/null 2>&1; then
                warn "Package manager not found. Please install gcc/build-essential manually."
            fi
        fi
    fi
}

# ------------------------------------------------------------------
# 2. Go Installation
# ------------------------------------------------------------------
install_go() {
    local target_ver="go${GO_FALLBACK_VER}"
    
    info "Checking Go (Target: ${target_ver})..."
    if command -v go >/dev/null 2>&1; then
        local installed_ver=$(go version | awk '{print $3}')
        # Check if installed < target
        # installed_ver format is "go1.25.3", target is "go1.25.3"
        # we strip "go" prefix for comparison usually, but sort -V handles mixed text/numbers well enough often
        # Let's clean it to be safe:
        local v_inst="${installed_ver#go}"
        local v_targ="${target_ver#go}"
        
        if ver_lt "$v_inst" "$v_targ"; then
             warn "Go version is old (${installed_ver}). Upgrading to ${target_ver}..."
        else
             success "Go is up-to-date or newer: ${installed_ver} (Target: ${target_ver})"
             return
        fi
    fi

    info "Installing Go ${target_ver}..."
    
    local filename="${target_ver}.${OS_GO}-${ARCH_GO}.tar.gz"
    local url="https://go.dev/dl/${filename}"
    local tmp_dir=$(mktemp -d)

    info "Downloading from $url..."
    if curl -L -o "$tmp_dir/$filename" "$url"; then
        # Remove old version
        rm -rf "$GO_INSTALL_DIR/go"
        
        # Install new version
        tar -C "$GO_INSTALL_DIR" -xzf "$tmp_dir/$filename"
        rm -rf "$tmp_dir"
        
        # Setup PATH for current session
        export PATH=$PATH:$GO_INSTALL_DIR/go/bin
        success "Go installed to $GO_INSTALL_DIR/go"
        
        # Add to profile if not exists
        for profile in "$HOME/.bashrc" "$HOME/.zshrc" "/etc/profile"; do
            if [ -f "$profile" ] && ! grep -q "$GO_INSTALL_DIR/go/bin" "$profile"; then
                 echo "export PATH=\$PATH:$GO_INSTALL_DIR/go/bin" >> "$profile"
            fi
        done
    else
        rm -rf "$tmp_dir"
        die "Failed to download Go."
    fi
}

# ------------------------------------------------------------------
# 3. ImmuDB Installation
# ------------------------------------------------------------------
install_immudb() {
    local target_ver="v${IMMUDB_FALLBACK_VER}"
    
    info "Checking ImmuDB (Target: ${target_ver})..."
    if command -v immudb >/dev/null 2>&1; then
        # Output: immudb 1.10.0
        local installed_raw=$(immudb version 2>/dev/null | head -n1 | awk '{print $2}')
        local installed_ver="v${installed_raw}"
        
        # Clean versions for comparison (remove v)
        local v_inst="${installed_raw}"
        local v_targ="${target_ver#v}"

        if ver_lt "$v_inst" "$v_targ"; then
             warn "ImmuDB version is old (${installed_ver}). Upgrading to ${target_ver}..."
        else
             success "ImmuDB is up-to-date or newer: ${installed_ver} (Target: ${target_ver})"
             return
        fi
    fi

    # Strip 'v' for filename construction
    local clean_ver="${target_ver#v}"
    local filename="immudb-v${clean_ver}-${OS_IMMU}-${ARCH_IMMU}"
    local url="https://github.com/codenotary/immudb/releases/download/${target_ver}/${filename}"
    
    if [ "$OS" == "darwin" ]; then
         filename="immudb-v${clean_ver}-darwin-${ARCH_IMMU}"
    fi

    info "Downloading ImmuDB ${target_ver}..."
    local tmp_dir=$(mktemp -d)
    if curl -L -o "$tmp_dir/immudb" "$url"; then
        chmod +x "$tmp_dir/immudb"
        mv "$tmp_dir/immudb" "$IMMUDB_INSTALL_DIR/immudb"
        rm -rf "$tmp_dir"
        success "ImmuDB installed to $IMMUDB_INSTALL_DIR/immudb"
    else
        rm -rf "$tmp_dir"
        die "Failed to download ImmuDB from $url"
    fi
}

# ------------------------------------------------------------------
# 4. Yggdrasil Installation
# ------------------------------------------------------------------
install_yggdrasil() {
    info "Checking Yggdrasil (Target: ${YGG_FALLBACK_VER})..."
    if command -v yggdrasil >/dev/null 2>&1; then
        # Output: Build version: 0.5.12
        local installed_ver=$(yggdrasil -version 2>/dev/null | grep "Build version" | awk '{print $3}')
        
        if ver_lt "$installed_ver" "$YGG_FALLBACK_VER"; then
            warn "Yggdrasil version is old (${installed_ver}). Upgrading to ${YGG_FALLBACK_VER}..."
        else
            success "Yggdrasil is up-to-date or newer: ${installed_ver} (Target: ${YGG_FALLBACK_VER})"
            return
        fi
    fi

    if [ "$OS" == "linux" ]; then
        # Check for Debian/Ubuntu
        if [ -f /etc/debian_version ]; then
            info "Installing/Updating Yggdrasil via apt..."
            mkdir -p /usr/local/apt-keys
            curl -fsSL https://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/key.txt | gpg --dearmor --yes -o /usr/local/apt-keys/yggdrasil-keyring.gpg
            echo 'deb [signed-by=/usr/local/apt-keys/yggdrasil-keyring.gpg] http://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/ debian yggdrasil' > /etc/apt/sources.list.d/yggdrasil.list
             apt update
             # Allow downgrade if necessary or specific version? 
             # apt installs latest by default. To pin specific version in apt is harder without a tailored repo.
             # For now, we assume repo has the 'stable' we want or we accept latest from repo.
             # Strict version matching with apt requires 'apt install yggdrasil=0.5.12', checking availability first.
             
             apt install -y yggdrasil
            success "Yggdrasil installed via apt"
        else
            warn "Non-Debian Linux detected. Yggdrasil installation skipped (manual install required for RPM/Arch)."
        fi
    elif [ "$OS" == "darwin" ]; then
        warn "macOS detected. Please update Yggdrasil manually (e.g., brew upgrade yggdrasil)."
    fi
}

# ------------------------------------------------------------------
# Execution
# ------------------------------------------------------------------

# Always check sys deps
install_sys_deps

if [ "$INSTALL_GO" = true ]; then
    install_go
fi

if [ "$INSTALL_IMMUDB" = true ]; then
    install_immudb
fi

if [ "$INSTALL_YGG" = true ]; then
    install_yggdrasil
fi

success "Dependencies setup complete!"
