#!/usr/bin/env bash
################################################################################
# setup_dependencies.sh - JMDN Cross-Platform Dependency Installer
#
# Installs JMDN dependencies: Go, ImmuDB, Yggdrasil, PostgreSQL, GCC/build tools
#
# Usage: sudo ./setup_dependencies.sh [options]
# Options:
#   --go          Install Go
#   --immudb      Install ImmuDB
#   --yggdrasil   Install Yggdrasil
#   --postgres    Install and configure PostgreSQL for ThebeDB
#   --all         Install all dependencies (default if no flags provided)
#
# PostgreSQL defaults (override via environment variables):
#   JMDN_PG_USER  (default: postgres)
#   JMDN_PG_PASS  (default: postgres)
#   JMDN_PG_DB    (default: jmdn_thebe)
#   JMDN_PG_PORT  (default: 5432)
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
#   - 2026-03-31: Added PostgreSQL setup with default credentials for ThebeDB
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

################################################################################
# Root check and platform validation
################################################################################
require_root

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
INSTALL_POSTGRES=false

if [ $# -eq 0 ]; then
	log_warn "No arguments provided."
	echo "Usage: sudo $0 [options]"
	echo "Options:"
	echo "  --go          Install Go"
	echo "  --immudb      Install ImmuDB"
	echo "  --yggdrasil   Install Yggdrasil"
	echo "  --postgres    Install and configure PostgreSQL for ThebeDB"
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
		--yggdrasil)
			INSTALL_YGG=true
			;;
		--postgres)
			INSTALL_POSTGRES=true
			;;
		--all)
			INSTALL_GO=true
			INSTALL_IMMUDB=true
			INSTALL_YGG=true
			INSTALL_POSTGRES=true
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
# 3. ImmuDB Installation
################################################################################
install_immudb() {
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
# 5. PostgreSQL Installation
#
# Goal: same zero-config UX as ImmuDB.
# After this runs, the node connects with the defaults in main.go:
#   postgres://postgres:postgres@127.0.0.1:5433/jmdn_thebe?sslmode=disable
# Note: port 5433 is used because ImmuDB occupies 5432 with its PG wire protocol.
################################################################################

# Credentials — override with env vars before running the script.
JMDN_PG_USER="${JMDN_PG_USER:-postgres}"
JMDN_PG_PASS="${JMDN_PG_PASS:-postgres}"
JMDN_PG_DB="${JMDN_PG_DB:-jmdn_thebe}"
JMDN_PG_PORT="${JMDN_PG_PORT:-5433}"  # 5433 avoids conflict with ImmuDB's built-in PG wire protocol on 5432

# Returns the OS service name for PostgreSQL on this platform.
_postgres_service_name() {
	case "${PKG_MANAGER}" in
	apt)
		echo "postgresql"
		;;
	dnf | yum)
		# RHEL/CentOS: may be versioned (postgresql-16) or generic
		if systemctl list-unit-files 2>/dev/null | grep -q "postgresql-[0-9]"; then
			systemctl list-unit-files 2>/dev/null | grep "postgresql-[0-9]" | awk '{print $1}' | sed 's/\.service//' | head -1
		else
			echo "postgresql"
		fi
		;;
	pacman | apk)
		echo "postgresql"
		;;
	pkg)
		# FreeBSD: versioned service (postgresql16)
		local ver
		ver=$(pkg info -x 'postgresql[0-9]+-server' 2>/dev/null | head -1 | grep -oE '[0-9]+' | head -1)
		echo "postgresql${ver:-}"
		;;
	*)
		echo "postgresql"
		;;
	esac
}

# Initialize the PostgreSQL data directory on distros that require an explicit
# initdb step (Debian/Ubuntu handle this automatically during package install).
_postgres_init_datadir() {
	case "${PKG_MANAGER}" in
	apt)
		# No-op: Debian/Ubuntu run initdb automatically
		;;
	dnf | yum)
		log_info "Initializing PostgreSQL data directory..."
		if check_command postgresql-setup; then
			postgresql-setup --initdb 2>/dev/null || true
		elif check_command pg_ctl; then
			su -c "initdb --pgdata=/var/lib/pgsql/data" postgres 2>/dev/null || true
		fi
		;;
	pacman)
		log_info "Initializing PostgreSQL data directory (Arch)..."
		su -c "initdb --locale=en_US.UTF-8 -E UTF8 -D /var/lib/postgres/data" postgres 2>/dev/null || true
		;;
	apk)
		log_info "Initializing PostgreSQL data directory (Alpine)..."
		su -c "initdb -D /var/lib/postgresql/data" postgres 2>/dev/null || true
		;;
	pkg)
		log_info "Initializing PostgreSQL data directory (FreeBSD)..."
		local data_dir="/var/db/postgres/data16"
		if [[ ! -f "${data_dir}/PG_VERSION" ]]; then
			mkdir -p "${data_dir}"
			chown postgres "${data_dir}"
			su -m pgsql -c "initdb -D ${data_dir}" 2>/dev/null || true
			echo 'postgresql_enable="YES"' >> /etc/rc.conf
		fi
		;;
	esac
}

# Find the pg_hba.conf path by querying the running postgres instance.
# Falls back to common known paths if the query fails.
_postgres_find_hba_conf() {
	# Ask postgres directly — most reliable
	local hba
	hba=$(su -c "psql -tA -c 'SHOW hba_file;'" postgres 2>/dev/null | tr -d '[:space:]')
	if [[ -n "${hba}" && -f "${hba}" ]]; then
		echo "${hba}"
		return 0
	fi

	# Fallback: scan common locations (glob-expanded one at a time)
	local dirs=(
		"/etc/postgresql"
		"/var/lib/pgsql"
		"/var/lib/postgres"
		"/var/lib/postgresql"
		"/var/db/postgres"
		"/usr/local/var/postgres"
	)
	for d in "${dirs[@]}"; do
		local f
		f=$(find "${d}" -name "pg_hba.conf" 2>/dev/null | head -1)
		if [[ -n "${f}" ]]; then
			echo "${f}"
			return 0
		fi
	done

	return 1
}

# Patch pg_hba.conf so TCP connections from localhost accept password (md5) auth.
# Replaces any existing method on the 127.0.0.1/32 and ::1/128 host lines,
# or appends fresh lines if they are absent.
_postgres_configure_tcp_auth() {
	local hba_conf="$1"
	log_info "Patching ${hba_conf} for TCP md5 auth..."

	# IPv4 loopback
	if grep -qE '^host[[:space:]]+all[[:space:]]+all[[:space:]]+127\.0\.0\.1/32' "${hba_conf}"; then
		sed_inplace \
			's|^\(host[[:space:]]*all[[:space:]]*all[[:space:]]*127\.0\.0\.1/32\).*|\1            md5|' \
			"${hba_conf}"
	else
		echo "host    all             all             127.0.0.1/32            md5" >> "${hba_conf}"
	fi

	# IPv6 loopback
	if grep -qE '^host[[:space:]]+all[[:space:]]+all[[:space:]]+::1/128' "${hba_conf}"; then
		sed_inplace \
			's|^\(host[[:space:]]*all[[:space:]]*all[[:space:]]*::1/128\).*|\1                 md5|' \
			"${hba_conf}"
	else
		echo "host    all             all             ::1/128                 md5" >> "${hba_conf}"
	fi

	log_ok "pg_hba.conf updated"
}

install_postgres() {
	log_info "Checking PostgreSQL (user=${JMDN_PG_USER}, db=${JMDN_PG_DB}, port=${JMDN_PG_PORT})..."

	# If already fully configured, skip everything.
	if check_command psql; then
		if PGPASSWORD="${JMDN_PG_PASS}" psql \
			-h 127.0.0.1 -p "${JMDN_PG_PORT}" \
			-U "${JMDN_PG_USER}" -d "${JMDN_PG_DB}" \
			-c "SELECT 1;" &>/dev/null 2>&1; then
			log_ok "PostgreSQL already configured — skipping setup"
			return 0
		fi
	fi

	# ── 1. Install packages ──────────────────────────────────────────────────
	log_info "Installing PostgreSQL packages..."
	case "${PKG_MANAGER}" in
	apt)
		apt-get update
		pkg_install postgresql postgresql-contrib
		;;
	dnf)
		pkg_install postgresql-server postgresql-contrib
		;;
	yum)
		pkg_install postgresql-server postgresql-contrib
		;;
	pacman)
		pkg_install postgresql
		;;
	apk)
		pkg_install postgresql postgresql-contrib
		;;
	brew)
		brew install postgresql@16 || brew install postgresql
		brew link --force postgresql@16 2>/dev/null || true
		;;
	pkg)
		pkg_install postgresql16-server postgresql16-client || \
			pkg_install postgresql-server
		;;
	*)
		log_die "PostgreSQL installation not supported for package manager: ${PKG_MANAGER}"
		;;
	esac
	log_ok "PostgreSQL packages installed"

	# ── 2. Init data directory (no-op on Debian/Ubuntu) ─────────────────────
	_postgres_init_datadir

	# ── 3. Set the port in postgresql.conf before starting the service ───────
	# ImmuDB occupies port 5432 (PG wire protocol). We use 5433 to avoid the
	# conflict. Patch postgresql.conf before the first start so the port is
	# set before any connections are attempted.
	local pg_conf
	pg_conf=$(find /etc/postgresql /var/lib/pgsql /var/lib/postgres /var/lib/postgresql /var/db/postgres /usr/local/var/postgres \
		-name "postgresql.conf" 2>/dev/null | head -1 || true)
	if [[ -n "${pg_conf}" && -f "${pg_conf}" ]]; then
		log_info "Setting PostgreSQL port to ${JMDN_PG_PORT} in ${pg_conf}..."
		if grep -qE '^#?[[:space:]]*port[[:space:]]*=' "${pg_conf}"; then
			sed_inplace "s|^#*[[:space:]]*port[[:space:]]*=.*|port = ${JMDN_PG_PORT}|" "${pg_conf}"
		else
			echo "port = ${JMDN_PG_PORT}" >> "${pg_conf}"
		fi
		log_ok "Port set to ${JMDN_PG_PORT}"
	fi

	# ── 4. Start the service ─────────────────────────────────────────────────
	log_info "Starting PostgreSQL service..."
	case "${PKG_MANAGER}" in
	brew)
		brew services start postgresql@16 2>/dev/null || \
			brew services start postgresql
		;;
	pkg)
		service "$(_postgres_service_name)" start || true
		;;
	*)
		local svc
		svc=$(_postgres_service_name)
		svc_reload_daemon
		svc_start "${svc}"
		svc_enable "${svc}"
		;;
	esac

	# Wait for postgres to accept connections (up to 30s)
	log_info "Waiting for PostgreSQL to be ready..."
	local retries=30
	until su -c "pg_isready -q" postgres &>/dev/null 2>&1 || \
		  pg_isready -h 127.0.0.1 -p "${JMDN_PG_PORT}" -q 2>/dev/null; do
		sleep 1
		retries=$((retries - 1))
		if [[ ${retries} -eq 0 ]]; then
			log_die "PostgreSQL did not become ready — check service logs"
		fi
	done
	log_ok "PostgreSQL is ready"

	# ── 4. Set password for the postgres user ────────────────────────────────
	log_info "Setting password for user '${JMDN_PG_USER}'..."
	case "${PKG_MANAGER}" in
	brew)
		# On macOS, postgres runs as the current user — connect directly
		psql postgres -c "ALTER USER ${JMDN_PG_USER} WITH PASSWORD '${JMDN_PG_PASS}';" 2>/dev/null || \
			psql -c "ALTER USER ${JMDN_PG_USER} WITH PASSWORD '${JMDN_PG_PASS}';"
		;;
	*)
		su -c "psql -c \"ALTER USER ${JMDN_PG_USER} WITH PASSWORD '${JMDN_PG_PASS}';\"" postgres
		;;
	esac
	log_ok "Password set"

	# ── 5. Configure TCP password auth in pg_hba.conf ────────────────────────
	local hba_conf
	hba_conf=$(_postgres_find_hba_conf) || \
		log_die "Could not locate pg_hba.conf — is the data directory initialised?"
	_postgres_configure_tcp_auth "${hba_conf}"

	# ── 6. Restart to apply pg_hba.conf ──────────────────────────────────────
	log_info "Restarting PostgreSQL to apply auth config..."
	case "${PKG_MANAGER}" in
	brew)
		brew services restart postgresql@16 2>/dev/null || \
			brew services restart postgresql
		;;
	pkg)
		service "$(_postgres_service_name)" restart || true
		;;
	*)
		svc_restart "$(_postgres_service_name)"
		;;
	esac
	sleep 2

	# ── 7. Create the jmdn_thebe database ────────────────────────────────────
	log_info "Creating database '${JMDN_PG_DB}'..."
	if PGPASSWORD="${JMDN_PG_PASS}" psql \
		-h 127.0.0.1 -p "${JMDN_PG_PORT}" \
		-U "${JMDN_PG_USER}" \
		-tc "SELECT 1 FROM pg_database WHERE datname='${JMDN_PG_DB}';" \
		2>/dev/null | grep -q 1; then
		log_ok "Database '${JMDN_PG_DB}' already exists"
	else
		PGPASSWORD="${JMDN_PG_PASS}" createdb \
			-h 127.0.0.1 -p "${JMDN_PG_PORT}" \
			-U "${JMDN_PG_USER}" "${JMDN_PG_DB}"
		log_ok "Database '${JMDN_PG_DB}' created"
	fi

	# ── 8. Verify end-to-end ─────────────────────────────────────────────────
	if PGPASSWORD="${JMDN_PG_PASS}" psql \
		-h 127.0.0.1 -p "${JMDN_PG_PORT}" \
		-U "${JMDN_PG_USER}" -d "${JMDN_PG_DB}" \
		-c "SELECT 1;" &>/dev/null; then
		log_ok "PostgreSQL setup complete"
		log_ok "DSN: postgres://${JMDN_PG_USER}:${JMDN_PG_PASS}@127.0.0.1:${JMDN_PG_PORT}/${JMDN_PG_DB}?sslmode=disable"
	else
		log_die "PostgreSQL connection test failed — check pg_hba.conf and service logs"
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

if [[ "${INSTALL_POSTGRES}" == true ]]; then
	install_postgres
fi

log_ok "Dependencies setup complete!"
