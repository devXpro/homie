#!/usr/bin/env bash
# Homie home installer.
#
# Installs the home hub (homed) from a GitHub release and runs it as a service:
#   macOS                 a LaunchAgent in your login session
#   Linux / Raspberry Pi  a systemd service under the system user homie (needs root)
# It also installs cloudflared, which homed starts as a child process to reach the internet through Cloudflare (see
# "cloudflared" below), and can connect your domain: --domain HOST, or --quick-tunnel to try without a domain.
#
#   macOS:  curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/home.sh | bash
#   Linux:  curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/home.sh | sudo bash
#
# Run it again to update in place (the data is kept). Pass options after `bash -s --`, for example
# `... | sudo bash -s -- --uninstall`. See --help.
#
# Safe for `curl | bash`: everything happens inside main(), which is called on the last line, so a truncated download
# runs nothing. Works with the bash 3.2 that macOS ships.
set -euo pipefail

readonly RELEASE_BASE_DEFAULT="https://github.com/devXpro/homie/releases"
readonly RAW_BASE_DEFAULT="https://raw.githubusercontent.com/devXpro/homie"
readonly DEFAULT_LABEL="com.github.devxpro.homie.homed"
readonly SERVICE_USER="homie"
readonly LINUX_DATA_DIR="/var/lib/homie"
readonly UNIT_NAME="homie.service"
readonly UNIT_PATH="/etc/systemd/system/$UNIT_NAME"
readonly WRAPPER_MARK="Installed by the Homie installer"

# cloudflared: ONE pinned release. Every download is checked against the sha256 below before it is installed; bump the
# version and all the values together (install/test/run.sh pins checks that Dockerfile and Dockerfile.dev agree).
# Source of the values: the "digest" of each asset in https://api.github.com/repos/cloudflare/cloudflared/releases/tags/
# <version> (the release notes list the same SHA256s). macOS prefers Homebrew when it is installed; Linux and a Mac
# without Homebrew use these files, put next to homed. Re-running this installer updates cloudflared the same way.
readonly CLOUDFLARED_VERSION="2026.11.2"
readonly CLOUDFLARED_BASE_DEFAULT="https://github.com/cloudflare/cloudflared/releases/download/$CLOUDFLARED_VERSION"
readonly CLOUDFLARED_SHA_DARWIN_AMD64="6153d9ec629174882a3adbff2f7dc5a24b4f3bdd7ccc38eaa84fba26eb890311"
readonly CLOUDFLARED_SHA_DARWIN_ARM64="f6d58eeec91ad1ec5fd1f7e4a587303772929b2625be5d24068033851c36ff55"
readonly CLOUDFLARED_SHA_LINUX_AMD64="56f13335143ec0e22772963290683a84a04114a35ec64cbc794a4d39ff9bb8cf"
readonly CLOUDFLARED_SHA_LINUX_ARM64="04a7b7f08bce85d56b26ffe1fdbfbb21a074495a363d16f0783c40e25b5c5281"
readonly CLOUDFLARED_SHA_LINUX_ARM="d219494581180764c6d2454409691bb06879d01b43cdeae12476070a7e4e4762"

RELEASE_BASE="${HOMIE_RELEASE_BASE:-$RELEASE_BASE_DEFAULT}"
RAW_BASE="${HOMIE_RAW_BASE:-$RAW_BASE_DEFAULT}"
CLOUDFLARED_BASE="$CLOUDFLARED_BASE_DEFAULT"

VERSION=""
PREFIX=""
UNINSTALL=0
PURGE=0
YES=0
NO_START=0
DRY_RUN=0
ALLOW_HTTP=0
DOMAIN=""
QUICK=0
CF_SHA_OVERRIDE=""
OS=""
ARCH=""
LABEL="$DEFAULT_LABEL"

TAG=""
ARCHIVE=""
ARCHIVE_SHA=""
TMP_DIR=""
BIN_SRC=""

# cloudflared: the asset of this platform, its pinned sha256, the verified file, and where the installed one is.
CF_ASSET=""
CF_SHA=""
CF_SRC=""
CF_BIN=""
# How it gets installed on this run: brew, download, or none (--dry-run only reads).
CF_VIA=""
# What the Cloudflare connect did: none, named, quick or failed.
CONNECT_STATE="none"

# Set by write_file and install_binary when something changed on disk. BIN_CHANGED covers homed and cloudflared: a
# changed binary means a restart; the caller resets it to 0 before installing.
FILE_CHANGED=0
BIN_CHANGED=0

# Paths, filled in by set_paths.
BIN_DIR=""
LIB_DIR=""
HOMED_BIN=""
DATA_DIR=""
PLIST_PATH=""
LOG_DIR=""
LOG_FILE=""
CF_BIN=""
# The PATH the service runs with, so that homed finds cloudflared; BREW_BIN is Homebrew's bin dir on a Mac that has it.
SERVICE_PATH=""
BREW_BIN=""

info() { printf '%s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

cleanup() {
	if [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]]; then
		rm -rf -- "$TMP_DIR"
	fi
}

usage() {
	cat <<'EOF'
Usage: home.sh [options]

Installs the Homie hub (homed) and starts it as a service. Run it again to update in place.

  --version vX.Y.Z   install this release. Default: the latest release.
  --prefix DIR       macOS: install the binary as DIR/bin/homed (default: ~/Library/Application Support/homie).
                     Linux: DIR/bin/homed is the command, DIR/lib/homie/homed the program (default: /usr/local).
  --domain HOST      after the service starts, connect your own domain through Cloudflare (it opens a Cloudflare
                     login link; the domain's zone must be on your Cloudflare account), for example home.example.com.
  --quick-tunnel     after the service starts, use a temporary Cloudflare quick tunnel instead: no domain, no account,
                     the link changes at every restart ("for trying"). Cannot be combined with --domain.
  --no-start         install the files only; do not touch launchd or systemd (no connect either).
  --dry-run          show which release and archive would be installed; change nothing.
  --uninstall        stop the service and remove it and the program (cloudflared too, unless Homebrew owns it).
                     The data is kept unless --purge.
  --purge            with --uninstall: also delete the data (people, passkeys, tunnel credentials, paired devices,
                     logs) and, on Linux, the user homie.
  --yes              do not ask questions (uninstall and purge ask first).
  -h, --help         show this help.

Tests only:
  --release-base URL   release server instead of https://github.com/devXpro/homie/releases
  --allow-http         allow a release server without TLS
  --cloudflared-base URL   download cloudflared from URL/<asset> instead of the pinned GitHub release
  --cloudflared-sha256 H   the sha256 to expect instead of the pinned one (for the fake cloudflared of the tests)
  --os OS --arch ARCH  pretend to be darwin|linux and amd64|arm64|armv7
  --label LABEL        macOS: the LaunchAgent label (the real one is com.github.devxpro.homie.homed)
EOF
}

parse_args() {
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--version)
			[[ $# -ge 2 ]] || die "--version needs a value"
			VERSION="$2"
			shift 2
			;;
		--prefix)
			[[ $# -ge 2 ]] || die "--prefix needs a value"
			PREFIX="$2"
			shift 2
			;;
		--domain)
			[[ $# -ge 2 ]] || die "--domain needs a value"
			DOMAIN="$(printf '%s' "$2" | tr '[:upper:]' '[:lower:]')"
			shift 2
			;;
		--quick-tunnel)
			QUICK=1
			shift
			;;
		--no-start)
			NO_START=1
			shift
			;;
		--dry-run)
			DRY_RUN=1
			shift
			;;
		--uninstall)
			UNINSTALL=1
			shift
			;;
		--purge)
			PURGE=1
			shift
			;;
		--yes | -y)
			YES=1
			shift
			;;
		--release-base)
			[[ $# -ge 2 ]] || die "--release-base needs a value"
			RELEASE_BASE="${2%/}"
			shift 2
			;;
		--allow-http)
			ALLOW_HTTP=1
			shift
			;;
		--cloudflared-base)
			[[ $# -ge 2 ]] || die "--cloudflared-base needs a value"
			CLOUDFLARED_BASE="${2%/}"
			shift 2
			;;
		--cloudflared-sha256)
			[[ $# -ge 2 ]] || die "--cloudflared-sha256 needs a value"
			CF_SHA_OVERRIDE="$(printf '%s' "$2" | tr '[:upper:]' '[:lower:]')"
			shift 2
			;;
		--os)
			[[ $# -ge 2 ]] || die "--os needs a value"
			OS="$2"
			shift 2
			;;
		--arch)
			[[ $# -ge 2 ]] || die "--arch needs a value"
			ARCH="$2"
			shift 2
			;;
		--label)
			[[ $# -ge 2 ]] || die "--label needs a value"
			LABEL="$2"
			shift 2
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage >&2
			die "unknown option: $1"
			;;
		esac
	done

	if [[ "$PURGE" -eq 1 && "$UNINSTALL" -eq 0 ]]; then
		die "--purge only goes with --uninstall"
	fi
	if [[ -n "$DOMAIN" && "$QUICK" -eq 1 ]]; then
		die "use either --domain HOST or --quick-tunnel, not both"
	fi
	if [[ -n "$DOMAIN" || "$QUICK" -eq 1 ]] && [[ "$UNINSTALL" -eq 1 ]]; then
		die "--domain and --quick-tunnel go with an install, not with --uninstall"
	fi
	if [[ -n "$DOMAIN" ]]; then
		valid_host "$DOMAIN" || die "--domain must be a host name such as home.example.com, got '$DOMAIN'"
	fi
	if [[ -n "$CF_SHA_OVERRIDE" && ! "$CF_SHA_OVERRIDE" =~ ^[0-9a-f]{64}$ ]]; then
		die "--cloudflared-sha256 must be 64 hex digits"
	fi
	if [[ -n "$VERSION" ]]; then
		VERSION="v${VERSION#v}"
		valid_tag "$VERSION" || die "--version must look like v1.2.3, got '$VERSION'"
	fi
	[[ "$LABEL" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "invalid --label: $LABEL"
	case "$RELEASE_BASE" in
	https://*) ;;
	http://*) [[ "$ALLOW_HTTP" -eq 1 ]] || die "the release server must use https (--allow-http is for tests)" ;;
	*) die "the release server must be an https URL, got '$RELEASE_BASE'" ;;
	esac
	case "$CLOUDFLARED_BASE" in
	https://*) ;;
	http://*) [[ "$ALLOW_HTTP" -eq 1 ]] || die "the cloudflared server must use https (--allow-http is for tests)" ;;
	*) die "the cloudflared server must be an https URL, got '$CLOUDFLARED_BASE'" ;;
	esac
}

# ---------- helpers ----------

valid_tag() {
	local re='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
	[[ "$1" =~ $re ]]
}

# valid_host H: a plain lower-case DNS name with at least two labels (no scheme, port, path or IP address).
valid_host() {
	local re='^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$'
	[[ "${#1}" -le 253 && "$1" =~ $re ]]
}

# safe_path P: absolute, no "..", and only characters that need no quoting in a plist, a unit or sed.
safe_path() {
	local re='^/[A-Za-z0-9._/@+, =-]*$'
	[[ "$1" =~ $re ]] || return 1
	[[ "$1" != *"/../"* && "$1" != */.. ]]
}

have() { command -v "$1" >/dev/null 2>&1; }

have_tty() { (: </dev/tty) 2>/dev/null; }

# confirm PROMPT: yes/no, default no. With --yes it always agrees. Reads the terminal even when stdin is a pipe.
confirm() {
	local answer=""
	if [[ "$YES" -eq 1 ]]; then
		return 0
	fi
	have_tty || die "no terminal to ask '$1'; rerun with --yes"
	printf '%s [y/N] ' "$1" >&2
	IFS= read -r answer </dev/tty || return 1
	[[ "$answer" =~ ^[Yy]([Ee][Ss])?$ ]]
}

# fetch URL DEST: https only (also across redirects), unless --allow-http.
fetch() {
	if [[ "$ALLOW_HTTP" -eq 1 ]]; then
		curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1" || die "cannot download $1"
	else
		curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 -o "$2" "$1" ||
			die "cannot download $1"
	fi
}

sha256_of() {
	if have sha256sum; then
		sha256sum -- "$1" | awk '{print $1}'
	elif have shasum; then
		shasum -a 256 -- "$1" | awk '{print $1}'
	elif have openssl; then
		openssl dgst -sha256 -- "$1" | awk '{print $NF}'
	else
		die "no sha256 tool found (sha256sum, shasum or openssl)"
	fi
}

# ---------- platform ----------

detect_platform() {
	local m
	if [[ -z "$OS" ]]; then
		case "$(uname -s)" in
		Darwin) OS="darwin" ;;
		Linux) OS="linux" ;;
		*) die "unsupported system '$(uname -s)': home.sh supports macOS and Linux (NAS boxes: use the Docker image)" ;;
		esac
	fi
	case "$OS" in
	darwin | linux) ;;
	*) die "--os must be darwin or linux" ;;
	esac
	if [[ -n "$ARCH" ]]; then
		case "$ARCH" in
		amd64 | arm64 | armv7) return 0 ;;
		*) die "--arch must be amd64, arm64 or armv7" ;;
		esac
	fi
	m="$(uname -m)"
	case "$m" in
	x86_64 | amd64) ARCH="amd64" ;;
	aarch64 | arm64) ARCH="arm64" ;;
	armv7l | armv8l | armv7) ARCH="armv7" ;;
	armv6l | armv6) die "this is a 32-bit ARMv6 board (Pi Zero or Pi 1): homed has no build for it" ;;
	*) die "unsupported CPU '$m': homed is built for amd64, arm64 and armv7" ;;
	esac
	if [[ "$OS" == "darwin" && "$ARCH" == "amd64" ]]; then
		# A shell running under Rosetta reports x86_64 on an Apple Silicon Mac.
		[[ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" == "1" ]] && ARCH="arm64"
	fi
	if [[ "$OS" == "linux" && "$ARCH" == "arm64" ]]; then
		# A 32-bit Raspberry Pi OS can run on a 64-bit kernel and still report aarch64.
		[[ "$(getconf LONG_BIT 2>/dev/null || true)" == "32" ]] && ARCH="armv7"
	fi
	return 0
}

set_paths() {
	[[ -n "${HOME:-}" ]] || die "HOME is not set"
	if [[ "$OS" == "darwin" ]]; then
		DATA_DIR="$HOME/Library/Application Support/homie"
		[[ -n "$PREFIX" ]] || PREFIX="$DATA_DIR"
		BIN_DIR="$PREFIX/bin"
		LIB_DIR="$BIN_DIR"
		HOMED_BIN="$BIN_DIR/homed"
		CF_BIN="$BIN_DIR/cloudflared"
		PLIST_PATH="$HOME/Library/LaunchAgents/$LABEL.plist"
		LOG_DIR="$HOME/Library/Logs/homie"
		LOG_FILE="$LOG_DIR/homed.log"
		safe_path "$DATA_DIR" || die "HOME contains characters this installer does not support: $HOME"
	else
		DATA_DIR="$LINUX_DATA_DIR"
		[[ -n "$PREFIX" ]] || PREFIX="/usr/local"
		BIN_DIR="$PREFIX/bin"
		LIB_DIR="$PREFIX/lib/homie"
		HOMED_BIN="$LIB_DIR/homed"
		CF_BIN="$LIB_DIR/cloudflared"
	fi
	[[ "$PREFIX" == /* ]] || die "--prefix must be an absolute path"
	safe_path "$PREFIX" || die "--prefix may only contain letters, digits, spaces and . _ / @ + , = - (got '$PREFIX')"
}

# ---------- the release ----------

# resolve_tag: the --version, or the tag the "latest release" page redirects to. No API call, so no rate limit.
resolve_tag() {
	local loc
	if [[ -n "$VERSION" ]]; then
		TAG="$VERSION"
		return 0
	fi
	loc="$(curl -sS --max-time 30 -o /dev/null -w '%{redirect_url}' "$RELEASE_BASE/latest" 2>/dev/null || true)"
	TAG="${loc##*/tag/}"
	valid_tag "$TAG" || die "cannot find the latest release at $RELEASE_BASE/latest; pass --version vX.Y.Z"
}

# archive_candidates: GoReleaser names (homed_<version>_<os>_<arch>.tar.gz, PLAN 5.1) in order of preference.
archive_candidates() {
	local v="${TAG#v}" a
	case "$ARCH" in
	armv7) for a in armv7 armv6 arm; do printf 'homed_%s_%s_%s.tar.gz\n' "$v" "$OS" "$a"; done ;;
	*) printf 'homed_%s_%s_%s.tar.gz\n' "$v" "$OS" "$ARCH" ;;
	esac
}

# checksum_of NAME FILE: the sha256 FILE (checksums.txt) lists for NAME, or nothing.
checksum_of() {
	awk -v n="$1" '($2 == n || $2 == "*" n) && length($1) == 64 {print tolower($1); exit}' "$2"
}

# pick_archive: sets ARCHIVE and ARCHIVE_SHA from the release's checksums.txt.
pick_archive() {
	local name sum
	fetch "$RELEASE_BASE/download/$TAG/checksums.txt" "$TMP_DIR/checksums.txt"
	while IFS= read -r name; do
		sum="$(checksum_of "$name" "$TMP_DIR/checksums.txt")"
		if [[ "$sum" =~ ^[0-9a-f]{64}$ ]]; then
			ARCHIVE="$name"
			ARCHIVE_SHA="$sum"
			return 0
		fi
	done < <(archive_candidates)
	die "release $TAG has no homed build for $OS/$ARCH (looked for $(archive_candidates | head -n 1))"
}

# download_release: downloads, verifies and unpacks the archive; sets BIN_SRC to the new homed.
download_release() {
	local dir="$TMP_DIR/unpacked" got entry out
	fetch "$RELEASE_BASE/download/$TAG/$ARCHIVE" "$TMP_DIR/$ARCHIVE"
	got="$(sha256_of "$TMP_DIR/$ARCHIVE")"
	[[ "$got" == "$ARCHIVE_SHA" ]] || die "checksum mismatch for $ARCHIVE (expected $ARCHIVE_SHA, got $got); nothing was installed"
	info "Checksum ok: $ARCHIVE"

	out="$(tar -tzf "$TMP_DIR/$ARCHIVE")" || die "$ARCHIVE is not a valid tar.gz"
	while IFS= read -r entry; do
		case "$entry" in
		/* | ../* | */../* | */..) die "unsafe path in $ARCHIVE: $entry" ;;
		*) ;;
		esac
	done <<<"$out"
	mkdir -p "$dir"
	tar -xzf "$TMP_DIR/$ARCHIVE" -C "$dir"
	if [[ -f "$dir/homed" && ! -L "$dir/homed" ]]; then
		BIN_SRC="$dir/homed"
	else
		# GoReleaser may wrap the files in one directory.
		BIN_SRC="$(find "$dir" -type f -name homed | head -n 1)"
	fi
	[[ -n "$BIN_SRC" && -f "$BIN_SRC" ]] || die "$ARCHIVE has no homed binary"
	chmod 755 "$BIN_SRC"
	"$BIN_SRC" version >/dev/null 2>&1 ||
		die "the downloaded homed does not run on this system ($OS/$ARCH); nothing was installed"
	info "Release $TAG: $("$BIN_SRC" version 2>/dev/null | head -n 1)"
}

# ---------- cloudflared ----------

# cloudflared_pick: sets CF_ASSET and CF_SHA for this platform (the pinned values, or the test override).
cloudflared_pick() {
	case "$OS/$ARCH" in
	darwin/amd64)
		CF_ASSET="cloudflared-darwin-amd64.tgz"
		CF_SHA="$CLOUDFLARED_SHA_DARWIN_AMD64"
		;;
	darwin/arm64)
		CF_ASSET="cloudflared-darwin-arm64.tgz"
		CF_SHA="$CLOUDFLARED_SHA_DARWIN_ARM64"
		;;
	linux/amd64)
		CF_ASSET="cloudflared-linux-amd64"
		CF_SHA="$CLOUDFLARED_SHA_LINUX_AMD64"
		;;
	linux/arm64)
		CF_ASSET="cloudflared-linux-arm64"
		CF_SHA="$CLOUDFLARED_SHA_LINUX_ARM64"
		;;
	linux/armv7)
		CF_ASSET="cloudflared-linux-arm"
		CF_SHA="$CLOUDFLARED_SHA_LINUX_ARM"
		;;
	*) die "there is no cloudflared build for $OS/$ARCH" ;;
	esac
	[[ -z "$CF_SHA_OVERRIDE" ]] || CF_SHA="$CF_SHA_OVERRIDE"
}

# cloudflared_current: the first line of `cloudflared --version` of the installed copy at CF_BIN (nothing when there is
# none). On Linux it is the service user who runs it, never root: the program dir belongs to that user.
cloudflared_current() {
	[[ -x "$CF_BIN" ]] || return 0
	if [[ "$OS" == "linux" ]]; then
		getent passwd "$SERVICE_USER" >/dev/null || return 0
		as_service_user "$CF_BIN" --version 2>/dev/null | head -n 1 || true
	else
		"$CF_BIN" --version 2>/dev/null | head -n 1 || true
	fi
}

# cloudflared_current_ok: the installed copy is the pinned version.
cloudflared_current_ok() {
	local line
	line="$(cloudflared_current)"
	[[ "$line " == *"version $CLOUDFLARED_VERSION "* ]]
}

# cloudflared_download: downloads the pinned asset, checks its sha256 and sets CF_SRC to the cloudflared binary.
cloudflared_download() {
	local dir="$TMP_DIR/cloudflared" got out entry
	fetch "$CLOUDFLARED_BASE/$CF_ASSET" "$TMP_DIR/$CF_ASSET"
	got="$(sha256_of "$TMP_DIR/$CF_ASSET")"
	[[ "$got" == "$CF_SHA" ]] || die "checksum mismatch for $CF_ASSET (expected $CF_SHA, got $got); nothing was installed"
	info "Checksum ok: $CF_ASSET"
	if [[ "$OS" == "darwin" ]]; then
		out="$(tar -tzf "$TMP_DIR/$CF_ASSET")" || die "$CF_ASSET is not a valid tar.gz"
		while IFS= read -r entry; do
			case "$entry" in
			/* | ../* | */../* | */..) die "unsafe path in $CF_ASSET: $entry" ;;
			*) ;;
			esac
		done <<<"$out"
		mkdir -p "$dir"
		tar -xzf "$TMP_DIR/$CF_ASSET" -C "$dir"
		CF_SRC="$(find "$dir" -type f -name cloudflared | head -n 1)"
		[[ -n "$CF_SRC" && -f "$CF_SRC" ]] || die "$CF_ASSET has no cloudflared binary"
		chmod 755 "$CF_SRC"
		"$CF_SRC" --version >/dev/null 2>&1 ||
			die "the downloaded cloudflared does not run on this system ($OS/$ARCH); nothing was installed"
	else
		# Root does not run this file; the service user does, after it is installed (linux_install_cloudflared).
		CF_SRC="$TMP_DIR/$CF_ASSET"
		chmod 755 "$CF_SRC"
	fi
}

# cloudflared_prepare: before anything is installed. Decides how cloudflared gets here (Homebrew on a Mac that has it,
# else the pinned download), downloads and verifies it, and sets SERVICE_PATH. CF_SRC stays empty when nothing is to
# be installed from a file (Homebrew, or the pinned version is already there).
cloudflared_prepare() {
	local prefix=""
	cloudflared_pick
	CF_SRC=""
	CF_VIA="download"
	if [[ "$OS" == "darwin" ]] && have brew; then
		CF_VIA="brew"
		prefix="$(brew --prefix 2>/dev/null || true)"
		if [[ -n "$prefix" ]] && safe_path "$prefix/bin"; then
			BREW_BIN="$prefix/bin"
		fi
	fi
	if [[ "$OS" == "darwin" ]]; then
		SERVICE_PATH="$BIN_DIR${BREW_BIN:+:$BREW_BIN}:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	else
		SERVICE_PATH="$LIB_DIR:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	fi
	[[ "$DRY_RUN" -eq 0 && "$CF_VIA" == "download" ]] || return 0
	if cloudflared_current_ok; then
		info "cloudflared $CLOUDFLARED_VERSION is already installed."
		return 0
	fi
	cloudflared_download
}

# mac_cloudflared_brew: installs or upgrades cloudflared with Homebrew. Fails when brew does not leave a working one.
mac_cloudflared_brew() {
	local before="" after=""
	[[ -n "$BREW_BIN" ]] || return 1
	if brew list --versions cloudflared >/dev/null 2>&1; then
		before="$("$BREW_BIN/cloudflared" --version 2>/dev/null | head -n 1 || true)"
		brew upgrade cloudflared >&2 || warn "brew upgrade cloudflared failed; keeping the installed version"
	else
		brew install cloudflared >&2 || return 1
	fi
	[[ -x "$BREW_BIN/cloudflared" ]] || return 1
	after="$("$BREW_BIN/cloudflared" --version 2>/dev/null | head -n 1)" || return 1
	[[ -n "$after" ]] || return 1
	# The running homed keeps the old cloudflared as its child until it restarts.
	[[ "$before" == "$after" ]] || BIN_CHANGED=1
}

mac_install_cloudflared() {
	if [[ "$CF_VIA" == "brew" ]]; then
		if mac_cloudflared_brew; then
			# An earlier pinned copy would shadow it in the service's PATH.
			rm -f -- "$CF_BIN" "$CF_BIN.prev"
			info "cloudflared: $("$BREW_BIN/cloudflared" --version 2>/dev/null | head -n 1) (Homebrew)"
			return 0
		fi
		warn "Homebrew could not install cloudflared; using the pinned download instead"
		CF_VIA="download"
		cloudflared_current_ok || cloudflared_download
	fi
	if [[ -n "$CF_SRC" ]]; then
		install_binary "$CF_SRC" "$CF_BIN"
	fi
	info "cloudflared: $(cloudflared_current)"
}

linux_install_cloudflared() {
	if [[ -n "$CF_SRC" ]]; then
		linux_install_binary "$CF_SRC" "$CF_BIN"
	fi
	cloudflared_current_ok || die "the installed cloudflared does not run on this system (or is not $CLOUDFLARED_VERSION): $CF_BIN"
	info "cloudflared: $(cloudflared_current)"
}

# ---------- files ----------

SCRIPT_ASSET_DIR=""

locate_assets() {
	local src="${BASH_SOURCE[0]:-}" d
	if [[ -n "$src" && -f "$src" ]]; then
		d="$(cd "$(dirname "$src")" 2>/dev/null && pwd)" || d=""
		if [[ -n "$d" && -f "$d/launchd/$DEFAULT_LABEL.plist" ]]; then
			SCRIPT_ASSET_DIR="$d"
		fi
	fi
}

# fetch_asset NAME DEST: a template from the repo checkout this script lives in, or from GitHub at the release tag.
fetch_asset() {
	if [[ -n "$SCRIPT_ASSET_DIR" ]]; then
		cp -- "$SCRIPT_ASSET_DIR/$1" "$2"
	else
		fetch "$RAW_BASE/${HOMIE_REF:-$TAG}/install/$1" "$2"
	fi
}

# write_file SRC DEST MODE: replace DEST only if the content differs.
write_file() {
	local src="$1" dest="$2" mode="$3"
	FILE_CHANGED=0
	if [[ -f "$dest" ]] && cmp -s -- "$src" "$dest"; then
		chmod "$mode" "$dest"
		return 0
	fi
	FILE_CHANGED=1
	(
		umask 077
		cat -- "$src" >"$dest"
	)
	chmod "$mode" "$dest"
}

# install_binary SRC DEST: atomic replace (a running homed keeps its old inode), keeps the old one as DEST.prev, the
# same rollback point the hub's own updater leaves.
install_binary() {
	local src="$1" dest="$2" tmp
	if [[ -f "$dest" ]] && cmp -s -- "$src" "$dest"; then
		chmod 755 "$dest"
		return 0
	fi
	BIN_CHANGED=1
	mkdir -p "$(dirname "$dest")"
	tmp="$dest.new.$$"
	cp -- "$src" "$tmp"
	chmod 755 "$tmp"
	if [[ -f "$dest" ]]; then
		cp -p -- "$dest" "$dest.prev" 2>/dev/null || true
	fi
	mv -f -- "$tmp" "$dest"
}

# as_service_user CMD...: runs CMD as the Linux service user. Its program dir belongs to it (the hub replaces its own
# binary there on an update from the admin), so root never writes into that dir or runs the binary from it: a symlink
# or a swapped binary left there would act with root's rights on the next update.
as_service_user() { (cd / && runuser -u "$SERVICE_USER" -- "$@"); }

# linux_install_binary SRC DEST: install_binary for Linux, done by the service user; the file comes in on stdin. Used for
# homed and for cloudflared, which both live in the program dir.
linux_install_binary() {
	local src="$1" dest="$2"
	if as_service_user cmp -s - "$dest" <"$src"; then
		return 0
	fi
	BIN_CHANGED=1
	# shellcheck disable=SC2016 # $1 belongs to the inner shell
	as_service_user sh -c 'set -e; tmp="$1.new.$$"; cat >"$tmp"; chmod 755 "$tmp"
		if [ -f "$1" ]; then cp -p -- "$1" "$1.prev" 2>/dev/null || true; fi
		mv -f -- "$tmp" "$1"' sh "$dest" <"$src" || die "cannot install $dest"
}

installed_version() {
	[[ -x "$HOMED_BIN" ]] || return 0
	if [[ "$OS" == "linux" ]]; then
		getent passwd "$SERVICE_USER" >/dev/null || return 0
		as_service_user "$HOMED_BIN" version 2>/dev/null | head -n 1 || true
	else
		"$HOMED_BIN" version 2>/dev/null | head -n 1 || true
	fi
}

# ---------- macOS ----------

launchd_domain() { printf 'gui/%s' "$(id -u)"; }
agent_loaded() { launchctl print "$(launchd_domain)/$LABEL" >/dev/null 2>&1; }
agent_running() { launchctl print "$(launchd_domain)/$LABEL" 2>/dev/null | grep -q 'state = running'; }

# wait_until SECONDS CMD...: polls once a second.
wait_until() {
	local n="$1" i=0
	shift
	while [[ "$i" -lt "$n" ]]; do
		if "$@"; then
			return 0
		fi
		sleep 1
		i=$((i + 1))
	done
	return 1
}

agent_unloaded() { ! agent_loaded; }

mac_preflight() {
	[[ "$(id -u)" -ne 0 ]] || die "run it as yourself, not as root: the hub is a LaunchAgent of your login session"
	have curl || die "curl is not installed"
	have tar || die "tar is not installed"
	have launchctl || [[ "$NO_START" -eq 1 ]] || die "launchctl not found: is this macOS?"
}

mac_install() {
	local was fresh=1 plist_changed=0 tmpl
	was="$(installed_version)"
	[[ -z "$was" ]] || fresh=0

	mkdir -p "$LOG_DIR" "$(dirname "$PLIST_PATH")" "$DATA_DIR"
	chmod 700 "$DATA_DIR"
	BIN_CHANGED=0
	install_binary "$BIN_SRC" "$HOMED_BIN"
	mac_install_cloudflared

	tmpl="$TMP_DIR/plist.tmpl"
	fetch_asset "launchd/$DEFAULT_LABEL.plist" "$tmpl"
	sed -e "s|__LABEL__|$LABEL|g" -e "s|__HOMED_BIN__|$HOMED_BIN|g" -e "s|__DATA_DIR__|$DATA_DIR|g" \
		-e "s|__LOG_FILE__|$LOG_FILE|g" -e "s|__PATH__|$SERVICE_PATH|g" "$tmpl" >"$TMP_DIR/plist"
	if have plutil; then
		plutil -lint "$TMP_DIR/plist" >/dev/null || die "the generated LaunchAgent is not a valid plist (this is a bug)"
	fi
	write_file "$TMP_DIR/plist" "$PLIST_PATH" 644
	plist_changed="$FILE_CHANGED"
	info "Installed $HOMED_BIN ($(installed_version))"
	info "LaunchAgent: $PLIST_PATH"

	if [[ "$NO_START" -eq 1 ]]; then
		info "Not started (--no-start). To start it: launchctl bootstrap $(launchd_domain) '$PLIST_PATH'"
		connect_later_hint "'$HOMED_BIN'"
		return 0
	fi
	mac_start "$plist_changed" "$fresh"
}

mac_start() {
	local plist_changed="$1" fresh="$2" err=""
	local domain
	domain="$(launchd_domain)"
	if agent_loaded; then
		if [[ "$plist_changed" -eq 1 ]]; then
			launchctl bootout "$domain/$LABEL" || true
			wait_until 15 agent_unloaded || die "launchd did not unload $LABEL; run: launchctl bootout $domain/$LABEL"
		elif [[ "$BIN_CHANGED" -eq 1 ]] || ! agent_running; then
			launchctl kickstart -k "$domain/$LABEL" || die "launchctl kickstart failed"
		else
			info "Already installed and running; nothing to restart."
		fi
	fi
	if ! agent_loaded; then
		err="$(launchctl bootstrap "$domain" "$PLIST_PATH" 2>&1)" || {
			printf '%s\n' "$err" >&2
			die "launchctl bootstrap failed. It needs a logged-in desktop session of this user; if you are on ssh, log in at the Mac once, then run: launchctl bootstrap $domain '$PLIST_PATH'"
		}
	fi
	wait_until 20 agent_running || die "homed did not start; see $LOG_FILE"
	sleep 2
	agent_running || die "homed started and stopped again; see $LOG_FILE"
	info "homed is running (logs: $LOG_FILE)."
	mac_next_steps "$fresh"
}

mac_next_steps() {
	local fresh="$1" h="'$HOMED_BIN'"
	info ""
	if [[ "$fresh" -eq 1 ]]; then
		show_first_run env "HOMED_DATA_DIR=$DATA_DIR" "$HOMED_BIN"
	fi
	connect_cloudflare
	cat >&2 <<EOF

Local Network permission. macOS asks ONCE whether homed may find devices on your local network, the first time a
device plugin (the TV one, for example) talks to the LAN: click Allow. If you missed or denied it, the TV shows as
offline: turn homed on in System Settings > Privacy & Security > Local Network, then run:
  launchctl kickstart -k $(launchd_domain)/$LABEL

Next steps (homed is not on your PATH, so the commands use its full path):
$(tunnel_step "$h")
  2. Open the admin and create your passkey (Touch ID):
       $h admin setup-link
     It prints a one-time link. Or open the first-run page: $h admin firstrun
  3. In the admin, Devices > + adds the TV plugin.

Logs:      tail -f '$LOG_FILE'
Restart:   launchctl kickstart -k $(launchd_domain)/$LABEL
Homebrew:  an alternative to this installer, not an addition to it (use one of the two):
             brew tap devxpro/homie https://github.com/devXpro/homie && brew install homie
             brew services start homie
           (the formula brings cloudflared with it). The hub's self-update (Settings > Updates) replaces the binary in
           the Cellar, and the next brew upgrade puts the brew version back, so with brew update through brew.
Update:    run this installer again (the data is kept; cloudflared is updated too), or use Settings > Updates
Uninstall: bash home.sh --uninstall   (add --purge to delete the data too)
EOF
}

mac_uninstall() {
	have launchctl || [[ "$NO_START" -eq 1 ]] || die "launchctl not found: is this macOS?"
	confirm "Stop homed and remove it ($HOMED_BIN and the LaunchAgent)?" || die "aborted"
	if [[ "$NO_START" -eq 0 ]] && agent_loaded; then
		launchctl bootout "$(launchd_domain)/$LABEL" || true
		wait_until 15 agent_unloaded || warn "launchd still lists $LABEL"
	fi
	# Our own cloudflared copy goes too; one that Homebrew installed stays (it may serve other things).
	rm -f -- "$PLIST_PATH" "$HOMED_BIN" "$HOMED_BIN.prev" "$CF_BIN" "$CF_BIN.prev"
	rmdir "$BIN_DIR" 2>/dev/null || true
	if [[ "$PURGE" -eq 1 ]]; then
		confirm "Delete ALL data in $DATA_DIR (people, passkeys, tunnel credentials, paired devices)?" || die "aborted"
		if [[ "$DATA_DIR" == "$HOME/Library/Application Support/homie" ]]; then
			rm -rf -- "$DATA_DIR" "$LOG_DIR"
		fi
		info "Removed homed and its data. Keychain items are not touched. The Cloudflare tunnel and its DNS record stay in"
		info "your Cloudflare account: remove them there if you will not use them again."
	else
		info "Removed homed. The data in $DATA_DIR is kept; add --purge to delete it."
	fi
}

# ---------- Linux ----------

linux_preflight() {
	local t
	[[ "$(id -u)" -eq 0 ]] || die "run as root, for example: curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/home.sh | sudo bash"
	for t in curl tar install useradd getent awk find cmp runuser; do
		have "$t" || die "$t is not installed"
	done
	if [[ "$NO_START" -eq 0 ]]; then
		have systemctl && [[ -d /run/systemd/system ]] ||
			die "systemd is not running here. Use --no-start to only install the files, or run the Docker image (docs: Dockerfile)"
	fi
}

nologin_path() {
	local p
	for p in /usr/sbin/nologin /sbin/nologin /usr/bin/nologin; do
		if [[ -x "$p" ]]; then
			printf '%s' "$p"
			return 0
		fi
	done
	printf '/bin/false'
}

linux_user_and_dirs() {
	if ! getent passwd "$SERVICE_USER" >/dev/null; then
		useradd --system --user-group --home-dir "$DATA_DIR" --no-create-home --shell "$(nologin_path)" \
			--comment "Homie hub" "$SERVICE_USER" || die "cannot create the user $SERVICE_USER"
		info "Created the system user $SERVICE_USER."
	fi
	install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
	chmod 700 "$DATA_DIR"
	# Data left by an earlier install can belong to an older uid of the same name.
	if [[ -z "$(find "$DATA_DIR" -maxdepth 0 -user "$SERVICE_USER")" ]]; then
		chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
	fi
	# The program dir is owned by the service user: the hub replaces its own binary there on an update from the admin.
	install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$LIB_DIR"
	mkdir -p "$BIN_DIR"
}

linux_install() {
	local was fresh=1 unit_changed=0 unit_tmpl wrapper_tmpl wrapper="$BIN_DIR/homed"
	was="$(installed_version)"
	[[ -z "$was" ]] || fresh=0
	if [[ -e "$wrapper" ]] && ! grep -qF "$WRAPPER_MARK" "$wrapper" 2>/dev/null; then
		die "$wrapper exists and was not installed by this installer; remove it or choose another --prefix"
	fi

	linux_user_and_dirs
	BIN_CHANGED=0
	linux_install_binary "$BIN_SRC" "$HOMED_BIN"
	linux_install_cloudflared

	wrapper_tmpl="$TMP_DIR/homed.wrapper.tmpl"
	fetch_asset "systemd/homed.wrapper" "$wrapper_tmpl"
	sed -e "s|__HOMED_BIN__|$HOMED_BIN|g" "$wrapper_tmpl" >"$TMP_DIR/homed.wrapper"
	write_file "$TMP_DIR/homed.wrapper" "$wrapper" 755

	unit_tmpl="$TMP_DIR/unit.tmpl"
	fetch_asset "systemd/homie.service" "$unit_tmpl"
	sed -e "s|__HOMED_BIN__|$HOMED_BIN|g" -e "s|__LIB_DIR__|$LIB_DIR|g" -e "s|__PATH__|$SERVICE_PATH|g" "$unit_tmpl" >"$TMP_DIR/unit"
	write_file "$TMP_DIR/unit" "$UNIT_PATH" 644
	unit_changed="$FILE_CHANGED"
	info "Installed $HOMED_BIN ($(installed_version)), the command $wrapper and the unit $UNIT_PATH"

	if [[ "$NO_START" -eq 1 ]]; then
		info "Not started (--no-start). To start it: systemctl daemon-reload && systemctl enable --now $UNIT_NAME"
		connect_later_hint homed
		return 0
	fi
	linux_start "$unit_changed" "$fresh"
}

unit_active() { systemctl is-active --quiet "$UNIT_NAME"; }

linux_start() {
	local unit_changed="$1" fresh="$2"
	if [[ "$unit_changed" -eq 1 ]]; then
		systemctl daemon-reload
	fi
	systemctl enable "$UNIT_NAME" >/dev/null 2>&1 || warn "could not enable $UNIT_NAME at boot"
	if [[ "$unit_changed" -eq 1 || "$BIN_CHANGED" -eq 1 ]] || ! unit_active; then
		systemctl restart "$UNIT_NAME" || die "systemctl restart failed; see: journalctl -u homie -n 50"
	else
		info "Already installed and running; nothing to restart."
	fi
	wait_until 20 unit_active || die "homed did not start; see: journalctl -u homie -n 50"
	sleep 2
	unit_active || die "homed started and stopped again; see: journalctl -u homie -n 50"
	info "homed is running ($UNIT_NAME)."
	linux_next_steps "$fresh"
}

linux_next_steps() {
	local fresh="$1"
	info ""
	if [[ "$fresh" -eq 1 ]]; then
		show_first_run "$BIN_DIR/homed"
	fi
	connect_cloudflare
	cat >&2 <<EOF

The hub runs as the user $SERVICE_USER with its data in $DATA_DIR. The command homed runs the CLI as that user
(it uses sudo; "sudo -u $SERVICE_USER homed ..." works as well). cloudflared is installed next to the hub
($CF_BIN) and homed starts it itself.

Next steps:
$(tunnel_step homed)
  2. Open the admin and create your passkey. From any browser:
       homed admin setup-link
     It prints a one-time link (valid 15 minutes). Or open the first-run page of the home network:
       homed admin firstrun
     It prints the address (port 8090 on this machine) and the PIN. Once a Cloudflare tunnel is connected the page is
     only reachable from this machine itself.
  3. In the admin, Devices > + adds the TV plugin.

Status:    systemctl status homie        Logs: journalctl -u homie -f
Update:    run this installer again (the data is kept; cloudflared is updated too), or use Settings > Updates
Uninstall: curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/home.sh | sudo bash -s -- --uninstall
           (add --purge to delete the data and the user too)
EOF
}

linux_uninstall() {
	confirm "Stop homed and remove it ($HOMED_BIN, the homed command and $UNIT_NAME)?" || die "aborted"
	if [[ "$NO_START" -eq 0 ]] && have systemctl && [[ -d /run/systemd/system ]]; then
		systemctl disable --now "$UNIT_NAME" >/dev/null 2>&1 || true
	fi
	rm -f -- "$UNIT_PATH"
	if [[ "$NO_START" -eq 0 ]] && have systemctl && [[ -d /run/systemd/system ]]; then
		systemctl daemon-reload || true
	fi
	if [[ -f "$BIN_DIR/homed" ]] && grep -qF "$WRAPPER_MARK" "$BIN_DIR/homed" 2>/dev/null; then
		rm -f -- "$BIN_DIR/homed"
	fi
	rm -f -- "$HOMED_BIN" "$HOMED_BIN.prev" "$CF_BIN" "$CF_BIN.prev"
	rmdir "$LIB_DIR" 2>/dev/null || true
	if [[ "$PURGE" -eq 1 ]]; then
		confirm "Delete ALL data in $DATA_DIR and the user $SERVICE_USER (people, passkeys, tunnel credentials, paired devices)?" ||
			die "aborted"
		rm -rf -- "$LINUX_DATA_DIR"
		if getent passwd "$SERVICE_USER" >/dev/null; then
			userdel "$SERVICE_USER" 2>/dev/null || warn "could not delete the user $SERVICE_USER"
		fi
		info "Removed homed, its data and the user $SERVICE_USER. The Cloudflare tunnel and its DNS record stay in your"
		info "Cloudflare account: remove them there if you will not use them again."
	else
		info "Removed homed. The data in $DATA_DIR and the user $SERVICE_USER are kept; add --purge to delete them."
	fi
}

# ---------- output ----------

# show_first_run CMD [ENV...]: asks the running homed for the first-run page and prints its address and PIN. homed
# needs a few seconds to open its socket; a failure is not an error (the next steps explain the fallback).
show_first_run() {
	local out="" i=0
	while [[ "$i" -lt 15 ]]; do
		if out="$("$@" admin firstrun 2>/dev/null)"; then
			info "Set it up from a browser (the PIN is shown only here):"
			printf '%s\n' "$out"
			return 0
		fi
		sleep 1
		i=$((i + 1))
	done
	return 0
}

# cloudflare_cli ARGS...: runs `homed cloudflare ARGS` as the service sees things: its data dir and a PATH that finds the
# installed cloudflared. stdin is closed, so a script piped into bash (curl | bash) is never read by it.
cloudflare_cli() {
	if [[ "$OS" == "darwin" ]]; then
		env "HOMED_DATA_DIR=$DATA_DIR" "PATH=$SERVICE_PATH" "$HOMED_BIN" cloudflare "$@" </dev/null
	else
		as_service_user env "HOMED_DATA_DIR=$DATA_DIR" "PATH=$SERVICE_PATH" "$HOMED_BIN" cloudflare "$@" </dev/null
	fi
}

cloudflare_ready() { cloudflare_cli status >/dev/null 2>&1; }

# connect_cloudflare: --domain / --quick-tunnel, run once the service is up. A failure leaves the install in place, says
# how to retry and makes the installer exit with an error at the end (CONNECT_STATE=failed).
connect_cloudflare() {
	[[ -n "$DOMAIN" || "$QUICK" -eq 1 ]] || return 0
	# homed opens its admin socket a moment after it starts.
	wait_until 20 cloudflare_ready || true
	info ""
	if [[ -n "$DOMAIN" ]]; then
		info "Connecting $DOMAIN through Cloudflare: open the login link below and pick the zone of that domain."
		if cloudflare_cli connect "$DOMAIN"; then CONNECT_STATE="named"; else CONNECT_STATE="failed"; fi
	else
		info "Starting a Cloudflare quick tunnel (for trying; the link changes at every restart)."
		if cloudflare_cli quick; then CONNECT_STATE="quick"; else CONNECT_STATE="failed"; fi
	fi
}

# connect_later_hint CMD: with --no-start nothing runs, so say what to do once the service is started.
connect_later_hint() {
	if [[ -n "$DOMAIN" ]]; then
		info "Cloudflare is not connected (--no-start). After the service is running: $1 cloudflare connect $DOMAIN"
	elif [[ "$QUICK" -eq 1 ]]; then
		info "Cloudflare is not connected (--no-start). After the service is running: $1 cloudflare quick"
	fi
}

# tunnel_step CMD: the "1." of the next steps, by what the connect did. CMD is how to run homed.
tunnel_step() {
	case "$CONNECT_STATE" in
	named)
		printf '  1. Cloudflare: connected. Your home answers at https://%s (the MCP address for ChatGPT and Claude\n' "$DOMAIN"
		printf '     is https://%s/mcp).\n' "$DOMAIN"
		;;
	quick)
		printf '  1. Cloudflare quick tunnel: running, for trying only. Its link changes at every restart; see it in the\n'
		printf '     admin or with: %s cloudflare status\n' "$1"
		printf '     To move to your own domain: %s cloudflare connect home.example.com\n' "$1"
		;;
	failed)
		printf '  1. Cloudflare: the connect did NOT finish. homed is installed and running; try again with\n'
		if [[ -n "$DOMAIN" ]]; then
			printf '       %s cloudflare connect %s\n' "$1" "$DOMAIN"
		else
			printf '       %s cloudflare quick\n' "$1"
		fi
		;;
	*)
		printf '  1. Connect Cloudflare (the admin offers the same on its first-run page):\n'
		printf '       %s cloudflare connect home.example.com     your own domain, on your Cloudflare account\n' "$1"
		printf '       %s cloudflare quick                        a temporary link, no domain, for trying\n' "$1"
		;;
	esac
}

# ---------- main ----------

main() {
	trap cleanup EXIT
	parse_args "$@"
	detect_platform
	set_paths
	locate_assets

	if [[ "$UNINSTALL" -eq 1 ]]; then
		if [[ "$OS" == "darwin" ]]; then
			mac_uninstall
		else
			[[ "$(id -u)" -eq 0 ]] || die "run as root: ... | sudo bash -s -- --uninstall"
			linux_uninstall
		fi
		return 0
	fi

	if [[ "$DRY_RUN" -eq 0 ]]; then
		if [[ "$OS" == "darwin" ]]; then
			mac_preflight
		else
			linux_preflight
		fi
	else
		have curl || die "curl is not installed"
	fi

	TMP_DIR="$(mktemp -d)"
	resolve_tag
	pick_archive
	cloudflared_prepare
	info "Platform $OS/$ARCH, release $TAG, archive $ARCHIVE"
	if [[ "$DRY_RUN" -eq 1 ]]; then
		printf 'release=%s\narchive=%s\nurl=%s\nsha256=%s\nbinary=%s\n' \
			"$TAG" "$ARCHIVE" "$RELEASE_BASE/download/$TAG/$ARCHIVE" "$ARCHIVE_SHA" "$HOMED_BIN"
		printf 'cloudflared=%s\ncloudflared_via=%s\ncloudflared_url=%s\ncloudflared_sha256=%s\ncloudflared_binary=%s\n' \
			"$CLOUDFLARED_VERSION" "$CF_VIA" "$CLOUDFLARED_BASE/$CF_ASSET" "$CF_SHA" "$CF_BIN"
		return 0
	fi
	download_release
	if [[ "$OS" == "darwin" ]]; then
		mac_install
	else
		linux_install
	fi
	[[ "$CONNECT_STATE" != "failed" ]] || die "homed is installed and running, but the Cloudflare connect did not finish (see above)"
}

main "$@"
