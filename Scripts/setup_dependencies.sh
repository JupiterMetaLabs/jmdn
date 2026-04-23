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
#   --solidity    Install Solidity compiler (solc)
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
PG_APP_DB="${PG_APP_DB:-thebedb}"
PG_APP_PORT="${PG_APP_PORT:-5430}"

################################################################################
# Helper: Version Comparison
# Returns 0 (true) if $1 < $2
################################################################################
ver_lt() {
	[ "$1" = "$2" ] && return 1 || [ "$1" = "$(printf "%s\n%s" "$1" "$2" | sort -V | head -n1)" ]
}

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
INSTALL_SOLIDITY=false
INSTALL_STORAGE=false
STORAGE_MODE="" # local | docker | none
INTERACTIVE=false

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
		--immudb)
			INSTALL_IMMUDB=true
			;;
		--solidity)
			INSTALL_SOLIDITY=true
		--yggdrasil)
			INSTALL_YGG=true
			;;
		--all)
			INSTALL_GO=true
			INSTALL_YGG=true
			INSTALL_SOLIDITY=true
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
	read -r -p "Port [${PG_APP_PORT}]: " input_port
	read -r -s -p "Password [hidden]: " input_pass
	echo ""

	if [[ -n "${input_db}" ]]; then
		PG_APP_DB="${input_db}"
	fi
	if [[ -n "${input_user}" ]]; then
		PG_APP_USER="${input_user}"
	fi
	if [[ -n "${input_port}" ]]; then
		if [[ "${input_port}" =~ ^[0-9]+$ ]] && ((input_port >= 1 && input_port <= 65535)); then
			PG_APP_PORT="${input_port}"
		else
			log_warn "Invalid port '${input_port}', keeping default/current port ${PG_APP_PORT}."
		fi
	fi
	if [[ -n "${input_pass}" ]]; then
		PG_APP_PASSWORD="${input_pass}"
	fi
}

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
# 3. Yggdrasil Installation
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
	local primary_port="${PG_APP_PORT}"
	local fallback_port="5432"
	local detected_cluster_port=""

	if [[ "${PKG_MANAGER}" == "apt" ]] && command -v pg_lsclusters >/dev/null 2>&1; then
		detected_cluster_port="$(pg_lsclusters --no-header 2>/dev/null | awk 'NR==1 {print $3}')"
	fi

	_postgres_exec_with_port() {
		local target_port="$1"
		if command -v runuser >/dev/null 2>&1; then
			runuser -u postgres -- psql -p "${target_port}" -v ON_ERROR_STOP=1 -tAc "$sql"
		else
			su - postgres -c "psql -p ${target_port} -v ON_ERROR_STOP=1 -tAc \"$sql\""
		fi
	}

	# Prefer configured app port, but allow one-time fallback to default
	# PostgreSQL port to support first-run migrations.
	if _postgres_exec_with_port "${primary_port}"; then
		return 0
	fi

	# Debian/Ubuntu clusters can run on non-default ports (e.g. 5433).
	if [[ -n "${detected_cluster_port}" ]] && [[ "${detected_cluster_port}" != "${primary_port}" ]]; then
		if _postgres_exec_with_port "${detected_cluster_port}"; then
			return 0
		fi
	fi

	if [[ "${primary_port}" != "${fallback_port}" ]]; then
		_postgres_exec_with_port "${fallback_port}"
		return $?
	fi

	return 1
}

restart_postgres_service() {
	case "${PKG_MANAGER}" in
	apt)
		# Debian/Ubuntu use cluster services (postgresql@<ver>-<name>), while
		# postgresql.service is often only an "active (exited)" wrapper unit.
		if command -v pg_lsclusters >/dev/null 2>&1 && command -v pg_ctlcluster >/dev/null 2>&1; then
			local clusters
			clusters="$(pg_lsclusters --no-header 2>/dev/null || true)"
			if [[ -n "${clusters}" ]]; then
				while IFS= read -r line; do
					local ver name status
					ver="$(awk '{print $1}' <<<"${line}")"
					name="$(awk '{print $2}' <<<"${line}")"
					status="$(awk '{print $4}' <<<"${line}")"
					if [[ -n "${ver}" && -n "${name}" ]]; then
						if [[ "${status}" == "online" ]]; then
							pg_ctlcluster "${ver}" "${name}" restart >/dev/null 2>&1 || true
						else
							pg_ctlcluster "${ver}" "${name}" start >/dev/null 2>&1 || true
						fi
					fi
				done <<<"${clusters}"
			else
				systemctl restart postgresql || systemctl start postgresql || true
			fi
		else
			systemctl restart postgresql || systemctl start postgresql || true
		fi
		;;
	dnf | yum | pacman)
		systemctl restart postgresql || systemctl start postgresql || true
		;;
	apk)
		rc-service postgresql restart || rc-service postgresql start || true
		;;
	brew)
		brew services restart postgresql@15 || brew services start postgresql@15 || true
		;;
	pkg)
		service postgresql restart || service postgresql start || true
		;;
	*)
		log_warn "Unknown package manager for PostgreSQL restart; restart service manually."
		;;
	esac
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
		log_warn "PostgreSQL is not reachable yet; attempting service restart..."
		restart_postgres_service
		if ! postgres_exec "SELECT 1;" >/dev/null 2>&1; then
			log_warn "PostgreSQL service is not reachable as postgres user. Skipping DB bootstrap."
			log_warn "Start PostgreSQL and re-run with --storage-local to apply user/database setup."
			log_warn "Quick check: sudo -u postgres psql -p ${PG_APP_PORT} -c 'SELECT 1;'"
			return 0
		fi
	fi

	local current_port
	current_port="$(postgres_exec "SHOW port;" | tr -d '[:space:]' || true)"
	if [[ -n "${current_port}" ]] && [[ "${current_port}" != "${PG_APP_PORT}" ]]; then
		log_info "Updating PostgreSQL port from ${current_port} to ${PG_APP_PORT}..."
		postgres_exec "ALTER SYSTEM SET port = '${PG_APP_PORT}';" >/dev/null
		restart_postgres_service
		if ! postgres_exec "SELECT 1;" >/dev/null 2>&1; then
			log_warn "PostgreSQL did not become reachable on port ${PG_APP_PORT} after restart."
			log_warn "Please verify local PostgreSQL config and service logs."
			return 0
		fi
		log_ok "PostgreSQL is now configured on port ${PG_APP_PORT}"
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
				POSTGRES_PORT="${PG_APP_PORT}" \
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

if [[ "${INSTALL_SOLIDITY}" == true ]]; then
	install_solidity
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
	echo "postgres://${PG_APP_USER}:${PG_APP_PASSWORD}@localhost:${PG_APP_PORT}/${PG_APP_DB}?sslmode=disable"
fi

log_ok "Dependencies setup complete!"
