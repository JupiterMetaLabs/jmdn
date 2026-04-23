#!/usr/bin/env bash
################################################################################
# setup_dependencies.sh - JMDN Cross-Platform Dependency Installer
#
# Installs JMDN dependencies: Go, ImmuDB, Yggdrasil, GCC/build tools
#
# Usage: sudo ./setup_dependencies.sh [options]
# Options:
#   --go          Install Go
#   --immudb      Install ImmuDB
#   --yggdrasil   Install Yggdrasil
#   --solidity    Install Solidity compiler (solc)
#   --all         Install all dependencies (default if no flags provided)
#
# Supported Platforms:
#   - Linux (Debian/Ubuntu, RHEL/CentOS, Arch, Alpine)
#   - macOS (with Homebrew)
#   - FreeBSD
#
# Changelog:
#   - 2025-03-02: Added cross-platform support for Linux, macOS, and FreeBSD
#   - 2025-03-02: Integrated platform.sh for shared helpers and detection
#   - 2025-03-02: Added support for pacman, apk, brew, pkg managers
#   - 2025-03-02: Added FreeBSD Go and ImmuDB installation
#   - 2025-03-02: Enhanced Yggdrasil with multiple installation methods
#   - 2025-03-02: Use JMDN_BIN for binary installation paths
################################################################################

set -euo pipefail

# Script directory for sourcing lib/platform.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source platform library
if [[ ! -f "${SCRIPT_DIR}/lib/platform.sh" ]]; then
	echo "ERROR: lib/platform.sh not found at ${SCRIPT_DIR}/lib/platform.sh" >&2
	exit 1
fi
source "${SCRIPT_DIR}/lib/platform.sh"

# Config
GO_INSTALL_DIR="/usr/local"
# Known stable versions as fallbacks (matching current prod)
IMMUDB_FALLBACK_VER="1.10.0"
GO_FALLBACK_VER="1.25.3"
YGG_FALLBACK_VER="0.5.12"

################################################################################
# Helper: Version Comparison
# Returns 0 (true) if $1 < $2
################################################################################
ver_lt() {
	[ "$1" = "$2" ] && return 1 || [ "$1" = "$(printf "%s\n%s" "$1" "$2" | sort -V | head -n1)" ]
}

# Map platform names for Go and ImmuDB downloads
case "${PLATFORM}" in
linux)
	OS_GO="linux"
	OS_IMMU="linux"
	;;
macos)
	OS_GO="darwin"
	OS_IMMU="darwin"
	;;
freebsd)
	OS_GO="freebsd"
	OS_IMMU="freebsd"
	;;
*)
	log_die "Unsupported platform: ${PLATFORM}"
	;;
esac

# Map architecture for downloads
case "${ARCH}" in
amd64)
	ARCH_GO="amd64"
	ARCH_IMMU="amd64"
	;;
arm64)
	ARCH_GO="arm64"
	ARCH_IMMU="arm64"
	;;
armv7)
	ARCH_GO="armv7"
	ARCH_IMMU="armv7"
	;;
*)
	log_die "Unsupported architecture: ${ARCH} (raw: ${ARCH_RAW})"
	;;
esac

################################################################################
# Parse command-line arguments
################################################################################
INSTALL_GO=false
INSTALL_IMMUDB=false
INSTALL_YGG=false
INSTALL_SOLIDITY=false

if [ $# -eq 0 ]; then
	log_warn "No arguments provided."
	echo "Usage: sudo $0 [options]"
	echo "Options:"
	echo "  --go          Install Go"
	echo "  --immudb      Install ImmuDB"
	echo "  --yggdrasil   Install Yggdrasil"
	echo "  --solidity    Install Solidity compiler (solc)"
	echo "  --all         Install all dependencies"
	exit 1
else
	for arg in "$@"; do
		case $arg in
		--go)
			INSTALL_GO=true
			;;
		--immudb)
			INSTALL_IMMUDB=true
			;;
		--solidity)
			INSTALL_SOLIDITY=true
			;;
		--all)
			INSTALL_GO=true
			INSTALL_IMMUDB=true
			INSTALL_YGG=true
			INSTALL_SOLIDITY=true
			;;
		*)
			log_die "Unknown argument: $arg"
			;;
		esac
	done
fi

################################################################################
# 1. System Dependencies (GCC/Build Essentials)
################################################################################
install_sys_deps() {
	if [[ "${PLATFORM}" != "macos" ]]; then
		require_root
	fi
	log_info "Checking system build dependencies..."

	local gcc_missing=false
	local git_missing=false

	if check_command gcc; then
		log_ok "GCC is already installed: $(gcc --version | head -n1)"
	else
		gcc_missing=true
		log_warn "GCC not found. Will install..."
	fi

	if check_command git; then
		log_ok "Git is already installed: $(git --version | head -n1)"
	else
		git_missing=true
		log_warn "Git not found. Will install..."
	fi

	# Install if missing
	if [[ "${gcc_missing}" == true ]] || [[ "${git_missing}" == true ]]; then
		case "${PKG_MANAGER}" in
		apt)
			log_info "Installing via apt..."
			apt-get update
			pkg_install build-essential curl git
			;;
		dnf)
			log_info "Installing via dnf..."
			dnf install -y gcc gcc-c++ make curl git
			;;
		yum)
			log_info "Installing via yum..."
			yum groupinstall -y "Development Tools"
			yum install -y curl git
			;;
		pacman)
			log_info "Installing via pacman..."
			pacman -Sy --noconfirm base-devel curl git
			;;
		apk)
			log_info "Installing via apk..."
			apk add --no-cache build-base curl git
			;;
		brew)
			log_info "Installing via brew..."
			if [[ "${gcc_missing}" == true ]]; then
				log_info "Setting up Xcode Command Line Tools..."
				xcode-select --install || true
			fi
			if ! check_command curl; then
				brew install curl
			fi
			if ! check_command git; then
				brew install git
			fi
			;;
		pkg)
			log_info "Installing via pkg (FreeBSD)..."
			pkg install -y curl git
			# Note: FreeBSD base system includes cc
			;;
		*)
			log_warn "Unknown package manager. Please install gcc/build-essential manually."
			;;
		esac
	fi
}

################################################################################
# 2. Go Installation
################################################################################
install_go() {
	require_root
	local target_ver="go${GO_FALLBACK_VER}"

	log_info "Checking Go (Target: ${target_ver})..."
	if check_command go; then
		local installed_ver=$(go version | awk '{print $3}')
		local v_inst="${installed_ver#go}"
		local v_targ="${target_ver#go}"

		if ver_lt "$v_inst" "$v_targ"; then
			log_warn "Go version is old (${installed_ver}). Upgrading to ${target_ver}..."
		else
			log_ok "Go is up-to-date or newer: ${installed_ver} (Target: ${target_ver})"
			return 0
		fi
	fi

	log_info "Installing Go ${target_ver}..."

	local filename="${target_ver}.${OS_GO}-${ARCH_GO}.tar.gz"
	local url="https://go.dev/dl/${filename}"
	local tmp_dir
	tmp_dir=$(mktemp -d)

	log_info "Downloading from $url..."
	if curl -L -o "$tmp_dir/$filename" "$url"; then
		# Remove old version
		rm -rf "$GO_INSTALL_DIR/go"

		# Install new version
		tar -C "$GO_INSTALL_DIR" -xzf "$tmp_dir/$filename"
		rm -rf "$tmp_dir"

		# Setup PATH for current session
		export PATH=$PATH:$GO_INSTALL_DIR/go/bin
		log_ok "Go installed to $GO_INSTALL_DIR/go"

		# Add to profile if not exists
		for profile in "$HOME/.bashrc" "$HOME/.zshrc" "/etc/profile"; do
			if [[ -f "$profile" ]] && ! grep -q "$GO_INSTALL_DIR/go/bin" "$profile"; then
				echo "export PATH=\$PATH:$GO_INSTALL_DIR/go/bin" >>"$profile"
			fi
		done
	else
		rm -rf "$tmp_dir"
		log_die "Failed to download Go from $url"
	fi
}

################################################################################
# 3. ImmuDB Installation
################################################################################
install_immudb() {
	require_root
	local target_ver="v${IMMUDB_FALLBACK_VER}"

	log_info "Checking ImmuDB (Target: ${target_ver})..."
	if check_command immudb; then
		# Output: immudb 1.10.0
		local installed_raw
		installed_raw=$(immudb version 2>/dev/null | head -n1 | awk '{print $2}')
		local installed_ver="v${installed_raw}"

		# Clean versions for comparison (remove v)
		local v_inst="${installed_raw}"
		local v_targ="${target_ver#v}"

		if ver_lt "$v_inst" "$v_targ"; then
			log_warn "ImmuDB version is old (${installed_ver}). Upgrading to ${target_ver}..."
		else
			log_ok "ImmuDB is up-to-date or newer: ${installed_ver} (Target: ${target_ver})"
			return 0
		fi
	fi

	# Strip 'v' for filename construction
	local clean_ver="${target_ver#v}"
	local filename="immudb-v${clean_ver}-${OS_IMMU}-${ARCH_IMMU}"
	local url="https://github.com/codenotary/immudb/releases/download/${target_ver}/${filename}"

	log_info "Downloading ImmuDB ${target_ver}..."

	# Ensure JMDN_BIN directory exists
	if [[ ! -d "${JMDN_BIN}" ]]; then
		log_info "Creating binary directory: ${JMDN_BIN}"
		mkdir -p "${JMDN_BIN}"
	fi

	local tmp_dir
	tmp_dir=$(mktemp -d)

	if curl -L -o "$tmp_dir/immudb" "$url"; then
		chmod +x "$tmp_dir/immudb"
		mv "$tmp_dir/immudb" "${JMDN_BIN}/immudb"
		rm -rf "$tmp_dir"
		log_ok "ImmuDB installed to ${JMDN_BIN}/immudb"
	else
		rm -rf "$tmp_dir"
		if [[ "${PLATFORM}" == "freebsd" ]]; then
			log_warn "Failed to download ImmuDB binary for FreeBSD from $url"
			log_warn "ImmuDB may not have official FreeBSD binaries."
			log_warn "Consider building from source: https://github.com/codenotary/immudb"
			return 1
		else
			log_die "Failed to download ImmuDB from $url"
		fi
	fi
}

################################################################################
# 4. Yggdrasil Installation
################################################################################
install_yggdrasil() {
	require_root
	log_info "Checking Yggdrasil (Target: ${YGG_FALLBACK_VER})..."

	if check_command yggdrasil; then
		# Output: Build version: 0.5.12
		local installed_ver
		installed_ver=$(yggdrasil -version 2>/dev/null | grep "Build version" | awk '{print $3}')

		if ver_lt "$installed_ver" "$YGG_FALLBACK_VER"; then
			log_warn "Yggdrasil version is old (${installed_ver}). Upgrading to ${YGG_FALLBACK_VER}..."
		else
			log_ok "Yggdrasil is up-to-date or newer: ${installed_ver} (Target: ${YGG_FALLBACK_VER})"
			return 0
		fi
	fi

	case "${PLATFORM}" in
	linux)
		_install_yggdrasil_linux
		;;
	macos)
		_install_yggdrasil_macos
		;;
	freebsd)
		_install_yggdrasil_freebsd
		;;
	*)
		log_warn "Yggdrasil installation not supported on ${PLATFORM}"
		return 1
		;;
	esac
}

# Yggdrasil installation for Linux
_install_yggdrasil_linux() {
	case "${PKG_MANAGER}" in
	apt)
		log_info "Installing/Updating Yggdrasil via apt (Debian/Ubuntu)..."
		mkdir -p /usr/local/apt-keys
		if curl -fsSL https://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/key.txt | \
			gpg --dearmor --yes -o /usr/local/apt-keys/yggdrasil-keyring.gpg; then
			echo 'deb [signed-by=/usr/local/apt-keys/yggdrasil-keyring.gpg] http://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/ debian yggdrasil' \
				>/etc/apt/sources.list.d/yggdrasil.list
			apt-get update
			pkg_install yggdrasil
			log_ok "Yggdrasil installed via apt"
		else
			log_error "Failed to download Yggdrasil GPG key"
			return 1
		fi
		;;
	dnf | yum)
		log_info "Installing Yggdrasil via ${PKG_MANAGER} (RHEL/CentOS)..."
		log_info "Adding Yggdrasil COPR repository..."
		if [[ "${PKG_MANAGER}" == "dnf" ]]; then
			dnf copr enable neilalexander/yggdrasil -y
			dnf install -y yggdrasil
		else
			yum copr enable neilalexander/yggdrasil -y
			yum install -y yggdrasil
		fi
		log_ok "Yggdrasil installed via ${PKG_MANAGER}"
		;;
	pacman)
		log_warn "Installing Yggdrasil on Arch Linux..."
		log_info "Yggdrasil is available in AUR (aur/yggdrasil or aur/yggdrasil-bin)"
		log_info "Manual installation instructions:"
		cat <<'EOF'
  For binary package (faster):
    git clone https://aur.archlinux.org/yggdrasil-bin.git
    cd yggdrasil-bin
    makepkg -si

  For building from source:
    git clone https://aur.archlinux.org/yggdrasil.git
    cd yggdrasil
    makepkg -si

  Or use an AUR helper like yay:
    yay -S yggdrasil-bin
EOF
		return 1
		;;
	apk)
		log_warn "Installing Yggdrasil on Alpine..."
		log_info "Yggdrasil may be available in community repository"
		log_info "Attempting installation..."
		if apk add --no-cache yggdrasil 2>/dev/null; then
			log_ok "Yggdrasil installed via apk"
		else
			log_warn "Yggdrasil not found in Alpine repos. Manual download required."
			_install_yggdrasil_manual "linux-amd64"
		fi
		;;
	*)
		log_warn "Unknown package manager for Linux: ${PKG_MANAGER}"
		log_info "Attempting manual installation..."
		_install_yggdrasil_manual "linux-amd64"
		;;
	esac
}

# Yggdrasil installation for macOS
_install_yggdrasil_macos() {
	log_info "Installing Yggdrasil on macOS via Homebrew..."
	if check_command brew; then
		if brew install yggdrasil; then
			log_ok "Yggdrasil installed via Homebrew"
			return 0
		else
			log_warn "Yggdrasil installation failed via Homebrew"
		fi
	else
		log_warn "Homebrew not found. Please install Homebrew first."
		return 1
	fi

	log_info "Attempting manual installation..."
	_install_yggdrasil_manual "macos-amd64"
}

# Yggdrasil installation for FreeBSD
_install_yggdrasil_freebsd() {
	log_info "Installing Yggdrasil on FreeBSD..."

	if pkg search yggdrasil >/dev/null 2>&1; then
		log_info "Installing via pkg..."
		pkg install -y yggdrasil
		log_ok "Yggdrasil installed via pkg"
	else
		log_warn "Yggdrasil not found in FreeBSD packages"
		log_info "Attempting manual installation..."
		_install_yggdrasil_manual "freebsd-amd64"
	fi
}

# Manual Yggdrasil installation (fallback for unsupported platforms)
# Usage: _install_yggdrasil_manual "linux-amd64"
_install_yggdrasil_manual() {
	local target_arch="${1:-linux-amd64}"
	local release_url="https://github.com/yggdrasil-network/yggdrasil-go/releases/latest/download/yggdrasil-${target_arch}"

	log_info "Attempting manual Yggdrasil installation (${target_arch})..."
	log_info "Downloading from GitHub releases..."

	local tmp_dir
	tmp_dir=$(mktemp -d)

	if curl -L -o "$tmp_dir/yggdrasil" "$release_url"; then
		chmod +x "$tmp_dir/yggdrasil"

		# Ensure JMDN_BIN directory exists
		if [[ ! -d "${JMDN_BIN}" ]]; then
			mkdir -p "${JMDN_BIN}"
		fi

		mv "$tmp_dir/yggdrasil" "${JMDN_BIN}/yggdrasil"
		rm -rf "$tmp_dir"
		log_ok "Yggdrasil installed to ${JMDN_BIN}/yggdrasil"
	else
		rm -rf "$tmp_dir"
		log_error "Failed to download Yggdrasil binary"
		log_info "Please visit: https://github.com/yggdrasil-network/yggdrasil-go/releases"
		return 1
	fi
}

################################################################################
# 5. Solidity (solc) Installation
################################################################################
install_solidity() {
	if [[ "${PLATFORM}" != "macos" ]]; then
		require_root
	fi
	log_info "Checking Solidity compiler (solc)..."

	if check_command solc; then
		log_ok "Solidity compiler is already installed: $(solc --version | grep Version | awk '{print $2}')"
		return 0
	fi

	case "${PLATFORM}" in
	linux)
		case "${PKG_MANAGER}" in
		apt)
			log_info "Installing solc via apt (PPA)..."
			# We need software-properties-common for add-apt-repository
			pkg_install software-properties-common
			add-apt-repository -y ppa:ethereum/ethereum
			apt-get update
			pkg_install solc
			;;
		dnf | yum)
			log_info "Installing solc via ${PKG_MANAGER} (COPR)..."
			if [[ "${PKG_MANAGER}" == "dnf" ]]; then
				dnf copr enable @ethereum/solidity -y
				dnf install -y solidity
			else
				yum install -y yum-plugin-copr
				yum copr enable @ethereum/solidity -y
				yum install -y solidity
			fi
			;;
		pacman)
			log_info "Installing solc via pacman..."
			pacman -Sy --noconfirm solidity
			;;
		apk)
			log_info "Installing solc via apk..."
			apk add --no-cache solidity --repository=http://dl-cdn.alpinelinux.org/alpine/edge/community
			;;
		*)
			log_error "Unknown package manager for Linux: ${PKG_MANAGER}. Please install solc manually."
			return 1
			;;
		esac
		;;
	macos)
		log_info "Installing solc via Homebrew..."
		if check_command brew; then
			brew install solidity
		else
			log_error "Homebrew not found. Cannot install solidity."
			return 1
		fi
		;;
	windows)
		case "${PKG_MANAGER}" in
		choco)
			log_info "Installing solc via Chocolatey..."
			choco install solidity -y
			;;
		scoop)
			log_info "Installing solc via Scoop..."
			scoop install solidity
			;;
		*)
			log_error "No supported Windows package manager (choco/scoop) found."
			return 1
			;;
		esac
		;;
	*)
		log_error "Solidity installation not supported on ${PLATFORM}"
		return 1
		;;
	esac

	if check_command solc; then
		log_ok "Solidity compiler installed successfully: $(solc --version | grep Version | awk '{print $2}')"
	else
		log_error "Solidity installation appeared to succeed but 'solc' not found in PATH."
		return 1
	fi
}

################################################################################
# Main Execution
################################################################################

# Always check sys deps
install_sys_deps

if [[ "${INSTALL_GO}" == true ]]; then
	install_go
fi

if [[ "${INSTALL_IMMUDB}" == true ]]; then
	install_immudb
fi

if [[ "${INSTALL_YGG}" == true ]]; then
	install_yggdrasil
fi

if [[ "${INSTALL_SOLIDITY}" == true ]]; then
	install_solidity
fi

log_ok "Dependencies setup complete!"
