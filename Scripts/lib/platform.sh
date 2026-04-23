#!/usr/bin/env bash
################################################################################
# platform.sh - Cross-Platform Helper Library for JMDN
#
# This library provides platform detection and abstraction for the JMDN
# blockchain node project, supporting Linux, macOS, and FreeBSD.
#
# Usage: source /path/to/lib/platform.sh
#        All variables and functions are automatically initialized on source.
################################################################################

set -euo pipefail

# Guard against being sourced twice (readonly would fail on re-source)
if [[ -n "${_PLATFORM_SH_LOADED:-}" ]]; then
	return 0 2>/dev/null || true
fi
_PLATFORM_SH_LOADED=1

# Colors for logging
COLOR_RED='\033[0;31m'
COLOR_GREEN='\033[0;32m'
COLOR_YELLOW='\033[1;33m'
COLOR_BLUE='\033[0;34m'
COLOR_NC='\033[0m' # No Color

################################################################################
# Logging Functions
################################################################################

log_info() {
	echo -e "${COLOR_BLUE}[INFO]${COLOR_NC} $*"
}

log_ok() {
	echo -e "${COLOR_GREEN}[OK]${COLOR_NC} $*"
}

log_warn() {
	echo -e "${COLOR_YELLOW}[WARN]${COLOR_NC} $*" >&2
}

log_error() {
	echo -e "${COLOR_RED}[ERROR]${COLOR_NC} $*" >&2
}

log_die() {
	log_error "$@"
	exit 1
}

################################################################################
# Platform Detection Functions
################################################################################

# Detect the operating system platform
# Sets: PLATFORM ("linux", "macos", "freebsd")
detect_platform() {
	local uname_s
	uname_s=$(uname -s)

	case "${uname_s}" in
	Linux)
		PLATFORM="linux"
		;;
	Darwin)
		PLATFORM="macos"
		;;
	FreeBSD)
		PLATFORM="freebsd"
		;;
	MINGW* | CYGWIN* | MSYS*)
		PLATFORM="windows"
		;;
	*)
		PLATFORM="unknown"
		;;
	esac
}

# Detect the CPU architecture
# Sets: ARCH (normalized), ARCH_RAW (raw uname -m output)
detect_arch() {
	ARCH_RAW=$(uname -m)

	case "${ARCH_RAW}" in
	x86_64)
		ARCH="amd64"
		;;
	amd64)
		ARCH="amd64"
		;;
	aarch64)
		ARCH="arm64"
		;;
	arm64)
		ARCH="arm64"
		;;
	armv7l | armv6l)
		ARCH="armv7"
		;;
	i386 | i686)
		ARCH="i386"
		;;
	*)
		ARCH="${ARCH_RAW}"
		;;
	esac
}

# Detect the package manager
# Sets: PKG_MANAGER ("apt", "dnf", "yum", "pacman", "apk", "brew", "pkg", "unknown")
detect_pkg_manager() {
	if [[ "${PLATFORM}" == "linux" ]]; then
		# Check for common Linux package managers
		if command -v apt-get &>/dev/null; then
			PKG_MANAGER="apt"
		elif command -v dnf &>/dev/null; then
			PKG_MANAGER="dnf"
		elif command -v yum &>/dev/null; then
			PKG_MANAGER="yum"
		elif command -v pacman &>/dev/null; then
			PKG_MANAGER="pacman"
		elif command -v apk &>/dev/null; then
			PKG_MANAGER="apk"
		else
			PKG_MANAGER="unknown"
		fi
	elif [[ "${PLATFORM}" == "macos" ]]; then
		PKG_MANAGER="brew"
	elif [[ "${PLATFORM}" == "freebsd" ]]; then
		PKG_MANAGER="pkg"
	elif [[ "${PLATFORM}" == "windows" ]]; then
		if command -v choco &>/dev/null; then
			PKG_MANAGER="choco"
		elif command -v scoop &>/dev/null; then
			PKG_MANAGER="scoop"
		else
			PKG_MANAGER="unknown"
		fi
	else
		PKG_MANAGER="unknown"
	fi
}

# Detect the service manager
# Sets: SVC_MANAGER ("systemd", "launchd", "openrc", "rcd", "unknown")
detect_svc_manager() {
	if [[ "${PLATFORM}" == "linux" ]]; then
		# Check both that systemctl exists AND systemd is actually running
		# (containers may have the binary but no active systemd PID 1)
		if command -v systemctl &>/dev/null && [[ -d /run/systemd/system ]]; then
			SVC_MANAGER="systemd"
		elif command -v rc-service &>/dev/null; then
			SVC_MANAGER="openrc"
		else
			SVC_MANAGER="unknown"
		fi
	elif [[ "${PLATFORM}" == "macos" ]]; then
		SVC_MANAGER="launchd"
	elif [[ "${PLATFORM}" == "freebsd" ]]; then
		SVC_MANAGER="rcd"
	else
		SVC_MANAGER="unknown"
	fi
}

# Detect if running as root
# Sets: IS_ROOT (true/false)
detect_root() {
	if [[ $(id -u) -eq 0 ]]; then
		IS_ROOT=true
	else
		IS_ROOT=false
	fi
}

# Detect and set JMDN-specific directory paths based on platform
detect_jmdn_paths() {
	if [[ "${PLATFORM}" == "linux" ]]; then
		JMDN_PREFIX="/usr/local"
		JMDN_ETC="/etc/jmdn"
		JMDN_LOG="/var/log/jmdn"
		JMDN_DATA="/opt/jmdn"
		JMDN_BIN="${JMDN_PREFIX}/bin"
	elif [[ "${PLATFORM}" == "macos" ]]; then
		# Use Homebrew prefix if available, otherwise /usr/local
		if command -v brew &>/dev/null; then
			JMDN_PREFIX=$(brew --prefix)
		else
			JMDN_PREFIX="/usr/local"
		fi
		JMDN_ETC="${JMDN_PREFIX}/etc/jmdn"
		JMDN_LOG="${JMDN_PREFIX}/var/log/jmdn"
		JMDN_DATA="${JMDN_PREFIX}/var/jmdn"
		JMDN_BIN="${JMDN_PREFIX}/bin"
	elif [[ "${PLATFORM}" == "freebsd" ]]; then
		JMDN_PREFIX="/usr/local"
		JMDN_ETC="${JMDN_PREFIX}/etc/jmdn"
		JMDN_LOG="${JMDN_PREFIX}/var/log/jmdn"
		JMDN_DATA="${JMDN_PREFIX}/var/jmdn"
		JMDN_BIN="${JMDN_PREFIX}/bin"
	else
		JMDN_PREFIX="/usr/local"
		JMDN_ETC="/etc/jmdn"
		JMDN_LOG="/var/log/jmdn"
		JMDN_DATA="/opt/jmdn"
		JMDN_BIN="${JMDN_PREFIX}/bin"
	fi
}

################################################################################
# Utility Functions
################################################################################

# Check if a command exists
# Usage: check_command "curl"
# Returns: 0 if command exists, 1 otherwise
check_command() {
	local cmd="$1"
	if command -v "${cmd}" &>/dev/null; then
		return 0
	else
		return 1
	fi
}

# Ensure a directory exists with proper permissions
# Usage: ensure_dir "/var/log/jmdn" "755" "jmdn"
ensure_dir() {
	local dir="$1"
	local perms="${2:-755}"
	local owner="${3:-}"

	if [[ ! -d "${dir}" ]]; then
		mkdir -p "${dir}"
		chmod "${perms}" "${dir}"
		if [[ -n "${owner}" ]]; then
			chown "${owner}" "${dir}" || true
		fi
	fi
}

# Cross-platform sed in-place editing
# Usage: sed_inplace 's/old/new/g' /path/to/file
# Handles both GNU sed (Linux) and BSD sed (macOS/FreeBSD)
sed_inplace() {
	if [[ "${PLATFORM}" == "macos" ]] || [[ "${PLATFORM}" == "freebsd" ]]; then
		# BSD sed requires empty string after -i
		sed -i '' "$@"
	else
		# GNU sed (Linux)
		sed -i "$@"
	fi
}

# Cross-platform realpath implementation
# Provides fallback for systems without realpath command
realpath_portable() {
	local path="$1"

	if command -v realpath &>/dev/null; then
		realpath "${path}"
	else
		# Fallback: use cd and pwd in a subshell to avoid changing caller's cwd
		if [[ -f "${path}" ]]; then
			(cd "$(dirname "${path}")" && echo "$(pwd)/$(basename "${path}")")
		elif [[ -d "${path}" ]]; then
			(cd "${path}" && pwd)
		else
			echo "${path}"
		fi
	fi
}

# Detect local IP address
# Echoes the local IP address, handles various system configurations
detect_local_ip() {
	local ip=""
	if [[ "${PLATFORM}" == "linux" ]]; then
		# Linux: use ip route to get the source IP for outbound traffic
		# Output format: "1.0.0.0 via 192.168.1.1 dev eth0 src 192.168.1.100 uid 1000"
		# We need the field after "src", NOT $NF (which is the uid)
		ip=$(ip route get 1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") {print $(i+1); exit}}')
	else
		# macOS/FreeBSD: use ifconfig to get first non-loopback IPv4 address
		ip=$(ifconfig 2>/dev/null | grep "inet " | grep -v "127.0.0.1" | head -n 1 | awk '{print $2}')
	fi
	# Fallback to localhost if detection failed
	echo "${ip:-127.0.0.1}"
}

# Print a summary of detected platform configuration
platform_summary() {
	cat <<EOF
${COLOR_BLUE}=== JMDN Platform Configuration ===${COLOR_NC}
Platform:         ${PLATFORM}
Architecture:     ${ARCH} (raw: ${ARCH_RAW})
Package Manager:  ${PKG_MANAGER}
Service Manager:  ${SVC_MANAGER}
Running as root:  ${IS_ROOT}
Local IP:         $(detect_local_ip)

${COLOR_BLUE}Directory Configuration:${COLOR_NC}
Prefix:           ${JMDN_PREFIX}
Config (/etc):    ${JMDN_ETC}
Logs:             ${JMDN_LOG}
Data:             ${JMDN_DATA}
Binaries:         ${JMDN_BIN}
EOF
}

################################################################################
# Root Check Function
################################################################################

# Require root privileges, exit with error if not running as root
require_root() {
	if [[ $(id -u) -ne 0 ]]; then
		log_die "This operation requires root privileges. Please run with sudo."
	fi
}

################################################################################
# Package Management Functions
################################################################################

# Map package names to platform-specific names
# Usage: _map_package_name "build-essential"
_map_package_name() {
	local pkg="$1"

	case "${pkg}" in
	build-essential)
		case "${PKG_MANAGER}" in
		apt)
			echo "build-essential"
			;;
		dnf | yum)
			echo "gcc gcc-c++ make"
			;;
		pacman)
			echo "base-devel"
			;;
		apk)
			echo "build-base"
			;;
		brew)
			echo "" # macOS: use xcode-select --install
			;;
		pkg)
			echo "" # FreeBSD: base system has cc
			;;
		*)
			echo "${pkg}"
			;;
		esac
		;;
	curl)
		echo "curl"
		;;
	git)
		echo "git"
		;;
	*)
		echo "${pkg}"
		;;
	esac
}

# Install packages via detected package manager
# Usage: pkg_install "curl" "git" "build-essential"
pkg_install() {
	local packages=("$@")

	if [[ ${#packages[@]} -eq 0 ]]; then
		log_warn "pkg_install: No packages specified"
		return 0
	fi

	case "${PKG_MANAGER}" in
	apt)
		log_info "Installing packages via apt..."
		# Note: caller is responsible for running apt-get update before pkg_install
		apt-get install -y "${packages[@]}"
		;;
	dnf)
		log_info "Installing packages via dnf..."
		dnf install -y "${packages[@]}"
		;;
	yum)
		log_info "Installing packages via yum..."
		yum install -y "${packages[@]}"
		;;
	pacman)
		log_info "Installing packages via pacman..."
		pacman -Sy --noconfirm "${packages[@]}"
		;;
	apk)
		log_info "Installing packages via apk..."
		apk add --no-cache "${packages[@]}"
		;;
	brew)
		log_info "Installing packages via brew..."
		for pkg in "${packages[@]}"; do
			if [[ "${pkg}" == "build-essential" ]]; then
				log_info "Setting up Xcode Command Line Tools..."
				xcode-select --install || true
			else
				brew install "${pkg}"
			fi
		done
		;;
	pkg)
		log_info "Installing packages via pkg..."
		pkg install -y "${packages[@]}"
		;;
	*)
		log_error "Unknown package manager: ${PKG_MANAGER}"
		return 1
		;;
	esac

	log_ok "Packages installed successfully"
}

################################################################################
# Service Management Functions
################################################################################

# Start a service
# Usage: svc_start "jmdn-node"
svc_start() {
	local service="$1"

	case "${SVC_MANAGER}" in
	systemd)
		systemctl start "${service}"
		;;
	launchd)
		launchctl start "com.jmdn.${service}"
		;;
	openrc)
		rc-service "${service}" start
		;;
	rcd)
		service "${service}" start
		;;
	*)
		log_error "Unknown service manager: ${SVC_MANAGER}"
		return 1
		;;
	esac
}

# Stop a service
# Usage: svc_stop "jmdn-node"
svc_stop() {
	local service="$1"

	case "${SVC_MANAGER}" in
	systemd)
		systemctl stop "${service}"
		;;
	launchd)
		launchctl stop "com.jmdn.${service}"
		;;
	openrc)
		rc-service "${service}" stop
		;;
	rcd)
		service "${service}" stop
		;;
	*)
		log_error "Unknown service manager: ${SVC_MANAGER}"
		return 1
		;;
	esac
}

# Restart a service
# Usage: svc_restart "jmdn-node"
svc_restart() {
	local service="$1"

	case "${SVC_MANAGER}" in
	systemd)
		systemctl restart "${service}"
		;;
	launchd)
		launchctl stop "com.jmdn.${service}"
		sleep 1
		launchctl start "com.jmdn.${service}"
		;;
	openrc)
		rc-service "${service}" restart
		;;
	rcd)
		service "${service}" restart
		;;
	*)
		log_error "Unknown service manager: ${SVC_MANAGER}"
		return 1
		;;
	esac
}

# Enable a service to start at boot
# Usage: svc_enable "jmdn-node"
svc_enable() {
	local service="$1"

	case "${SVC_MANAGER}" in
	systemd)
		systemctl enable "${service}"
		;;
	launchd)
		# launchd services are enabled by default if the plist is in the correct location
		# This is typically handled by the service installation script
		log_info "launchd service ${service} will be enabled via plist placement"
		;;
	openrc)
		rc-update add "${service}"
		;;
	rcd)
		# rc.d services are enabled via /etc/rc.conf.d/
		log_info "FreeBSD rc.d service ${service} must be enabled via /etc/rc.conf.d/"
		;;
	*)
		log_error "Unknown service manager: ${SVC_MANAGER}"
		return 1
		;;
	esac
}

# Check if a service is active/running
# Usage: svc_status "jmdn-node"
# Returns: 0 if active, 1 if not
svc_status() {
	local service="$1"

	case "${SVC_MANAGER}" in
	systemd)
		systemctl is-active --quiet "${service}"
		;;
	launchd)
		launchctl list "com.jmdn.${service}" &>/dev/null
		;;
	openrc)
		rc-service "${service}" status &>/dev/null
		;;
	rcd)
		service "${service}" status &>/dev/null
		;;
	*)
		log_error "Unknown service manager: ${SVC_MANAGER}"
		return 1
		;;
	esac
}

# Reload the service daemon configuration
# Usage: svc_reload_daemon
svc_reload_daemon() {
	case "${SVC_MANAGER}" in
	systemd)
		systemctl daemon-reload
		;;
	launchd)
		# launchd doesn't have a daemon-reload equivalent
		log_info "launchd does not require daemon reload"
		;;
	openrc)
		# OpenRC doesn't require daemon reload
		log_info "OpenRC does not require daemon reload"
		;;
	rcd)
		# FreeBSD rc.d doesn't require daemon reload
		log_info "FreeBSD rc.d does not require daemon reload"
		;;
	*)
		log_error "Unknown service manager: ${SVC_MANAGER}"
		return 1
		;;
	esac
}

################################################################################
# Initialize all platform variables on source
################################################################################

# Declare all exported variables
PLATFORM=""
ARCH=""
ARCH_RAW=""
PKG_MANAGER=""
SVC_MANAGER=""
IS_ROOT=false
JMDN_PREFIX=""
JMDN_ETC=""
JMDN_LOG=""
JMDN_DATA=""
JMDN_BIN=""

# Run all detection functions
detect_platform
detect_arch
detect_pkg_manager
detect_svc_manager
detect_root
detect_jmdn_paths
