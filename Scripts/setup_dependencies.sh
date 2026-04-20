#!/usr/bin/env bash
################################################################################
# setup_dependencies.sh - JMDN Cross-Platform Dependency Installer
#
# Installs JMDN dependencies: Go, Yggdrasil, GCC/build tools, and storage services
#
# Usage: sudo ./setup_dependencies.sh [options]
# Options:
#   --go          Install Go
#   --yggdrasil   Install Yggdrasil
#   --all         Install all dependencies (default if no flags provided)
#   --storage-local   Install PostgreSQL + Redis as native services
#   --storage-docker  Install Docker tooling and run PostgreSQL + Redis via compose
#   --storage-none    Skip PostgreSQL/Redis setup
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
#   - 2025-03-02: Added FreeBSD Go installation
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
GO_FALLBACK_VER="1.25.3"
YGG_FALLBACK_VER="0.5.12"
PG_APP_USER="${PG_APP_USER:-thebedb}"
PG_APP_PASSWORD="${PG_APP_PASSWORD:-thebedb}"
PG_APP_DB="${PG_APP_DB:-thebedb_test}"

################################################################################
# Helper: Version Comparison
# Returns 0 (true) if $1 < $2
################################################################################
ver_lt() {
	[ "$1" = "$2" ] && return 1 || [ "$1" = "$(printf "%s\n%s" "$1" "$2" | sort -V | head -n1)" ]
}

################################################################################
# Root check and platform validation
################################################################################
require_root

# Map platform names for Go downloads
case "${PLATFORM}" in
linux)
	OS_GO="linux"
	;;
macos)
	OS_GO="darwin"
	;;
freebsd)
	OS_GO="freebsd"
	;;
*)
	log_die "Unsupported platform: ${PLATFORM}"
	;;
esac

# Map architecture for downloads
case "${ARCH}" in
amd64)
	ARCH_GO="amd64"
	;;
arm64)
	ARCH_GO="arm64"
	;;
armv7)
	ARCH_GO="armv7"
	;;
*)
	log_die "Unsupported architecture: ${ARCH} (raw: ${ARCH_RAW})"
	;;
esac

################################################################################
# Parse command-line arguments
################################################################################
INSTALL_GO=false
INSTALL_YGG=false
INSTALL_STORAGE=false
STORAGE_MODE="" # local | docker | none
INTERACTIVE=false

if [ $# -eq 0 ]; then
	INTERACTIVE=true
	INSTALL_GO=true
	INSTALL_YGG=true
	INSTALL_STORAGE=true
else
	for arg in "$@"; do
		case $arg in
		--go)
			INSTALL_GO=true
			;;
		--yggdrasil)
			INSTALL_YGG=true
			;;
		--all)
			INSTALL_GO=true
			INSTALL_YGG=true
			INSTALL_STORAGE=true
			;;
		--storage-local)
			INSTALL_STORAGE=true
			STORAGE_MODE="local"
			;;
		--storage-docker)
			INSTALL_STORAGE=true
			STORAGE_MODE="docker"
			;;
		--storage-none)
			INSTALL_STORAGE=true
			STORAGE_MODE="none"
			;;
		*)
			log_die "Unknown argument: $arg"
			;;
		esac
	done
fi

if [[ "${INTERACTIVE}" == true ]] && [[ -t 0 ]] && [[ -z "${STORAGE_MODE}" ]]; then
	echo ""
	echo "Select PostgreSQL/Redis installation type:"
	echo "  1) Local services (native packages)"
	echo "  2) Docker-based (docker compose)"
	echo "  3) Skip storage setup"
	read -r -p "Enter choice [1-3] (default: 2): " storage_choice
	case "${storage_choice:-2}" in
	1)
		STORAGE_MODE="local"
		;;
	2)
		STORAGE_MODE="docker"
		;;
	3)
		STORAGE_MODE="none"
		;;
	*)
		log_warn "Invalid choice, defaulting to docker-based setup"
		STORAGE_MODE="docker"
		;;
	esac
fi

prompt_storage_credentials() {
	if [[ ! -t 0 ]]; then
		return 0
	fi

	echo ""
	echo "PostgreSQL setup values (press Enter to keep defaults):"
	read -r -p "Database name [${PG_APP_DB}]: " input_db
	read -r -p "Username [${PG_APP_USER}]: " input_user
	read -r -s -p "Password [hidden]: " input_pass
	echo ""

	if [[ -n "${input_db}" ]]; then
		PG_APP_DB="${input_db}"
	fi
	if [[ -n "${input_user}" ]]; then
		PG_APP_USER="${input_user}"
	fi
	if [[ -n "${input_pass}" ]]; then
		PG_APP_PASSWORD="${input_pass}"
	fi
}

################################################################################
# 1. System Dependencies (GCC/Build Essentials)
################################################################################
install_sys_deps() {
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
# 3. Yggdrasil Installation
################################################################################
install_yggdrasil() {
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
# 4. Storage Installation (PostgreSQL + Redis)
################################################################################
install_storage_local() {
	log_info "Installing PostgreSQL + Redis locally..."
	case "${PKG_MANAGER}" in
	apt)
		apt-get update
		pkg_install postgresql postgresql-contrib redis-server
		systemctl enable postgresql redis-server || true
		systemctl restart postgresql redis-server || true
		;;
	dnf)
		dnf install -y postgresql-server redis
		postgresql-setup --initdb || true
		systemctl enable postgresql redis || true
		systemctl restart postgresql redis || true
		;;
	yum)
		yum install -y postgresql-server redis
		postgresql-setup initdb || true
		systemctl enable postgresql redis || true
		systemctl restart postgresql redis || true
		;;
	pacman)
		pacman -Sy --noconfirm postgresql redis
		systemctl enable postgresql redis || true
		systemctl restart postgresql redis || true
		;;
	apk)
		apk add --no-cache postgresql redis
		rc-update add postgresql default || true
		rc-update add redis default || true
		rc-service postgresql start || true
		rc-service redis start || true
		;;
	brew)
		brew install postgresql@15 redis
		brew services start postgresql@15 || true
		brew services start redis || true
		;;
	pkg)
		pkg install -y postgresql15-server redis
		service postgresql initdb || true
		service postgresql start || true
		service redis start || true
		;;
	*)
		log_warn "Unsupported package manager for local storage setup. Install PostgreSQL and Redis manually."
		return 1
		;;
	esac
	log_ok "Local PostgreSQL/Redis setup complete."
	configure_postgres_local
}

postgres_exec() {
	local sql="$1"
	if command -v runuser >/dev/null 2>&1; then
		runuser -u postgres -- psql -v ON_ERROR_STOP=1 -tAc "$sql"
	else
		su - postgres -c "psql -v ON_ERROR_STOP=1 -tAc \"$sql\""
	fi
}

configure_postgres_local() {
	if ! check_command psql; then
		log_warn "psql not found; skipping PostgreSQL user/database bootstrap."
		return 0
	fi

	log_info "Configuring local PostgreSQL app user/database..."
	log_info "Target user='${PG_APP_USER}' db='${PG_APP_DB}'"

	# Ensure postgres service is reachable before attempting SQL setup.
	if ! postgres_exec "SELECT 1;" >/dev/null 2>&1; then
		log_warn "PostgreSQL service is not reachable as postgres user. Skipping DB bootstrap."
		log_warn "Start PostgreSQL and re-run with --storage-local to apply user/database setup."
		return 0
	fi

	# Create role if missing.
	if [[ "$(postgres_exec "SELECT 1 FROM pg_roles WHERE rolname='${PG_APP_USER}';")" != "1" ]]; then
		postgres_exec "CREATE ROLE ${PG_APP_USER} WITH LOGIN PASSWORD '${PG_APP_PASSWORD}';"
		log_ok "Created PostgreSQL role: ${PG_APP_USER}"
	else
		log_info "PostgreSQL role already exists: ${PG_APP_USER}"
	fi

	# Create database if missing.
	if [[ "$(postgres_exec "SELECT 1 FROM pg_database WHERE datname='${PG_APP_DB}';")" != "1" ]]; then
		postgres_exec "CREATE DATABASE ${PG_APP_DB} OWNER ${PG_APP_USER};"
		log_ok "Created PostgreSQL database: ${PG_APP_DB}"
	else
		log_info "PostgreSQL database already exists: ${PG_APP_DB}"
		postgres_exec "ALTER DATABASE ${PG_APP_DB} OWNER TO ${PG_APP_USER};" >/dev/null || true
	fi

	# Minimal grants for idempotent safety.
	postgres_exec "GRANT ALL PRIVILEGES ON DATABASE ${PG_APP_DB} TO ${PG_APP_USER};" >/dev/null
	log_ok "PostgreSQL bootstrap completed for ${PG_APP_USER}/${PG_APP_DB}"
}

install_storage_docker() {
	log_info "Installing Docker tooling for compose-based storage..."
	case "${PKG_MANAGER}" in
	apt)
		apt-get update
		pkg_install docker.io docker-compose-plugin
		systemctl enable docker || true
		systemctl restart docker || true
		;;
	dnf)
		dnf install -y docker docker-compose-plugin
		systemctl enable docker || true
		systemctl restart docker || true
		;;
	yum)
		yum install -y docker docker-compose-plugin
		systemctl enable docker || true
		systemctl restart docker || true
		;;
	pacman)
		pacman -Sy --noconfirm docker docker-compose
		systemctl enable docker || true
		systemctl restart docker || true
		;;
	apk)
		apk add --no-cache docker docker-cli-compose
		rc-update add docker default || true
		rc-service docker start || true
		;;
	brew)
		brew install docker docker-compose
		log_warn "Docker daemon on macOS is provided by Docker Desktop. Ensure it is installed and running."
		;;
	pkg)
		pkg install -y docker docker-compose
		service docker start || true
		;;
	*)
		log_warn "Unsupported package manager for docker setup. Install Docker and Compose manually."
		return 1
		;;
	esac

	local repo_root
	repo_root="$(cd "${SCRIPT_DIR}/.." && pwd)"
	if [[ -f "${repo_root}/docker-compose.yml" ]]; then
		log_info "Starting PostgreSQL + Redis via docker compose..."
		(
			cd "${repo_root}" && \
				POSTGRES_USER="${PG_APP_USER}" \
				POSTGRES_PASSWORD="${PG_APP_PASSWORD}" \
				POSTGRES_DB="${PG_APP_DB}" \
				docker compose up -d postgres redis
		)
		log_ok "Docker-based PostgreSQL/Redis services started."
	else
		log_warn "docker-compose.yml not found at ${repo_root}; skipping compose up."
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

if [[ "${INSTALL_YGG}" == true ]]; then
	install_yggdrasil
fi

if [[ "${INSTALL_STORAGE}" == true ]]; then
	if [[ "${STORAGE_MODE:-docker}" != "none" ]]; then
		prompt_storage_credentials
	fi
	case "${STORAGE_MODE:-docker}" in
	local)
		install_storage_local
		;;
	docker)
		install_storage_docker
		;;
	none)
		log_info "Skipping PostgreSQL/Redis setup."
		;;
	*)
		log_warn "Unknown storage mode '${STORAGE_MODE}', skipping storage setup."
		;;
	esac
fi

if [[ "${INSTALL_STORAGE}" == true ]] && [[ "${STORAGE_MODE:-docker}" != "none" ]]; then
	log_info "Thebe SQL DSN:"
	echo "postgres://${PG_APP_USER}:${PG_APP_PASSWORD}@localhost:5432/${PG_APP_DB}?sslmode=disable"
fi

log_ok "Dependencies setup complete!"
