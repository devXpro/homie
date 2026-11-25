#!/usr/bin/env bash
# Tests of install/home.sh against a fake release (docs/TESTING.md, "Home installers").
#
#   run.sh [lint|mac|linux|all]     default: all
#
#   lint:       shellcheck on the scripts and the wrapper, and the cloudflared pins (home.sh, Dockerfile, Dockerfile.dev)
#   mac:        macOS, in a temporary HOME: --no-start files + `plutil -lint` (any launchctl call fails the test), the
#               Homebrew and connect flows with a fake brew and a fake launchctl, then a real launchd
#               start/update/restart/uninstall under a TEST label (never the real LaunchAgent)
#   linux:      Debian containers homie-t41-*: --no-start flows on every platform, a real systemd start where it can
#
# Environment: PLATFORMS (default "native linux/amd64"; "native" is the Docker host's CPU; add linux/arm/v7 if you can wait: building its image under qemu takes 30+ minutes),
# SYSTEMD_PLATFORMS (default "native linux/amd64"), KEEP_IMAGES=1 keeps the test images for the next run,
# NO_LAUNCHCTL=1 skips the real-launchd test and every read of the live agent (the fake-launchctl tests still run),
# PINS_ONLINE=1 also checks the cloudflared pins against GitHub's release metadata and Docker Hub (read-only web).
# cloudflared is a fake everywhere: nothing here talks to Cloudflare, and `brew` is a fake one (the real one is never run).
# Needs: bash, python3 (a venv is made in the temp dir), curl, shellcheck, plutil and launchctl (mac), docker (linux).
# The live install of this Mac (its data dir, LaunchAgent and logs) is never touched: HOME is a temp dir everywhere,
# and the real label is only read, to prove it did not move.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly HERE
readonly INSTALL_DIR="${HERE%/test}"
readonly HOME_SH="$INSTALL_DIR/home.sh"
readonly REAL_LABEL="com.github.devxpro.homie.homed"
readonly TEST_LABEL="com.github.devxpro.homie.homed.t41"
readonly IMAGE_REPO="homie-t41-linux"
readonly REPO_DIR="${INSTALL_DIR%/install}"
readonly MAC_PATH="/usr/bin:/bin:/usr/sbin:/sbin"
CF_VERSION="$(sed -n 's/^readonly CLOUDFLARED_VERSION="\(.*\)"$/\1/p' "$HOME_SH")"
readonly CF_VERSION

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$HERE/lib.sh"

WHAT="${1:-all}"
WORK=""
PORT=""
SERVER_PID=""
PLATFORMS="${PLATFORMS:-native linux/amd64}"
SYSTEMD_PLATFORMS="${SYSTEMD_PLATFORMS-native linux/amd64}"

cleanup() {
	local c
	if [[ -n "$SERVER_PID" ]]; then
		kill "$SERVER_PID" 2>/dev/null || true
	fi
	if [[ "$(uname -s)" == "Darwin" && "${NO_LAUNCHCTL:-0}" != 1 ]]; then
		launchctl bootout "gui/$(id -u)/$TEST_LABEL" >/dev/null 2>&1 || true
	fi
	if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
		for c in $(docker ps -aq --filter "name=homie-t41-"); do
			docker rm -f "$c" >/dev/null 2>&1 || true
		done
		if [[ "${KEEP_IMAGES:-0}" != "1" ]]; then
			for c in $(docker images -q "$IMAGE_REPO"); do
				docker rmi -f "$c" >/dev/null 2>&1 || true
			done
		fi
	fi
	if [[ -n "$WORK" && -d "$WORK" ]]; then
		rm -rf -- "$WORK"
	fi
}

start_fake_release() {
	WORK="$(mktemp -d /tmp/homie-t41.XXXXXX)"
	python3 -m venv "$WORK/venv"
	"$WORK/venv/bin/python" "$HERE/fakerelease.py" build "$WORK/release"
	"$WORK/venv/bin/python" "$HERE/fakerelease.py" serve "$WORK/release" "$WORK/port" "$INSTALL_DIR" &
	SERVER_PID=$!
	wait_for 10 "the fake release server is up" test -s "$WORK/port"
	PORT="$(cat "$WORK/port")"
	wait_for 5 "the fake server answers" curl -fsS -o /dev/null -I "http://127.0.0.1:$PORT/download/v0.0.1/checksums.txt"
}

# ---------- shellcheck ----------

test_shellcheck() {
	step "shellcheck"
	command -v shellcheck >/dev/null 2>&1 || fail "shellcheck is not installed"
	expect_ok "shellcheck home.sh, the wrapper and the tests" shellcheck -x "$HOME_SH" "$INSTALL_DIR/systemd/homed.wrapper" \
		"$HERE/run.sh" "$HERE/lib.sh" "$HERE/in-container.sh"
	bash -n "$HOME_SH"
	pass "bash -n"
}

# ---------- the privacy scan (make privacy) ----------

# The generic patterns of .github/privacy-check.sh in a scratch git repository: fixtures pass, private values fail.
test_privacy_script() {
	step "make privacy: the generic patterns"
	local d line
	d="$(mktemp -d /tmp/homie-t41-privacy.XXXXXX)"
	git -C "$d" init -q
	mkdir -p "$d/.github"
	cp "$REPO_DIR/.github/privacy-check.sh" "$d/.github/privacy-check.sh"
	printf '%s\n' 'host 192.0.2.7, wifi 02:00:00:00:00:01, path /home/user/x, 8.8.8.8, 1.10.0.1.5, version 10.12' \
		'172.15.0.1 172.32.0.1 11.0.0.1 1920.168.1.1 a1:02:00:00:00:01:99' >"$d/clean.txt"
	run_capture "$d/.github/privacy-check.sh"
	assert_eq "$RC:$OUT" "0:" "fixtures and public addresses pass"
	# The private values are spelled with placeholders, so this file passes the scan it tests: U=/Users, A=192.168,
	# B=10, D and E=private 172 ranges, M and N=vendor MAC prefixes.
	while IFS= read -r line; do
		line="${line//@U@//Users}"
		line="${line//@A@/192.168}"
		line="${line//@B@/10}"
		line="${line//@M@/20:3d:bd}"
		line="${line//@D@/172.16}"
		line="${line//@E@/172.31}"
		line="${line//@N@/00:1a:2b}"
		printf '%s\n' "$line" >"$d/dirty.txt"
		run_capture "$d/.github/privacy-check.sh"
		[[ "$RC" -eq 1 && "$OUT" == *"dirty.txt"* ]] || fail "not caught: $line"
		pass "caught: $line"
	done <<'EOF'
path @U@/someone/work
x @B@.1.2.3 y
x @B@.0.0.0/8 y
@D@.5.5
@E@.255.1:80
host @A@.1.20 up
mac @M@:38:e1:c8 up
two 02:00:00:00:00:01 and @N@:3c:4d:5e
EOF
	printf 'allowed %s.1.20 privacy:ok\n' "192.168" >"$d/dirty.txt"
	run_capture "$d/.github/privacy-check.sh"
	assert_eq "$RC:$OUT" "0:" "a privacy:ok line passes"
	rm -rf -- "$d"
}

# ---------- the cloudflared pins ----------

# The one pinned cloudflared release lives in home.sh (version + sha256 of each asset) and in both Dockerfiles (version,
# index digest of the image, sha256 of the arm binary). They must say the same.
test_pins() {
	step "cloudflared pins agree"
	local f arm
	arm="$(sed -n 's/^readonly CLOUDFLARED_SHA_LINUX_ARM="\(.*\)"$/\1/p' "$HOME_SH")"
	[[ "$arm" =~ ^[0-9a-f]{64}$ ]] || fail "home.sh has no sha256 for cloudflared-linux-arm"
	for f in Dockerfile Dockerfile.dev; do
		file_has "$REPO_DIR/$f" "ARG CLOUDFLARED_VERSION=$CF_VERSION"
		file_has "$REPO_DIR/$f" "ARG CLOUDFLARED_ARM_SHA256=$arm"
		grep -qE '^ARG CLOUDFLARED_IMAGE_DIGEST=sha256:[0-9a-f]{64}$' "$REPO_DIR/$f" || fail "$f has no pinned image digest"
	done
	[[ "$(sed -n 's/^ARG CLOUDFLARED_IMAGE_DIGEST=//p' "$REPO_DIR/Dockerfile")" == "$(sed -n 's/^ARG CLOUDFLARED_IMAGE_DIGEST=//p' "$REPO_DIR/Dockerfile.dev")" ]] ||
		fail "Dockerfile and Dockerfile.dev pin different cloudflared images"
	pass "the same image digest in both Dockerfiles"
	if [[ "${PINS_ONLINE:-0}" == 1 ]]; then
		test_pins_online
	fi
}

# test_pins_online: the pinned values against the public metadata (read-only: GitHub's release API, Docker Hub's tag API).
test_pins_online() {
	step "cloudflared pins match the published release (PINS_ONLINE=1)"
	command -v jq >/dev/null 2>&1 || fail "jq is not installed"
	local rel asset var want got digest
	rel="$(mktemp /tmp/homie-t41-release.XXXXXX)"
	curl -fsS "https://api.github.com/repos/cloudflare/cloudflared/releases/tags/$CF_VERSION" -o "$rel"
	for asset in darwin-amd64.tgz darwin-arm64.tgz linux-amd64 linux-arm64 linux-arm; do
		var="CLOUDFLARED_SHA_$(printf '%s' "${asset%.tgz}" | tr '[:lower:]-' '[:upper:]_')"
		want="$(sed -n "s/^readonly $var=\"\(.*\)\"\$/\1/p" "$HOME_SH")"
		got="$(jq -r --arg n "cloudflared-$asset" '.assets[] | select(.name == $n) | .digest' "$rel")"
		assert_eq "$got" "sha256:$want" "sha256 of cloudflared-$asset"
	done
	rm -f -- "$rel"
	digest="$(curl -fsS "https://hub.docker.com/v2/repositories/cloudflare/cloudflared/tags/$CF_VERSION" | jq -r .digest)"
	file_has "$REPO_DIR/Dockerfile" "ARG CLOUDFLARED_IMAGE_DIGEST=$digest"
}

# ---------- macOS ----------

# cf_sha KIND: the sha256 of the fake cloudflared (KIND is darwin or linux) of the variant CF_VARIANT (default new).
cf_sha() { curl -fsS "http://127.0.0.1:$PORT/cf/${CF_VARIANT:-new}/$1.sha256"; }

# mac_home HOME ARGS...: the installer with a minimal PATH, so the real Homebrew is never found (MAC_EXTRA_PATH adds the
# fakes of a test), the fake release server and the fake cloudflared.
MAC_EXTRA_PATH=""
mac_home() {
	env HOME="$1" PATH="${MAC_EXTRA_PATH:+$MAC_EXTRA_PATH:}$WORK/shim:$MAC_PATH" bash "$HOME_SH" \
		--release-base "http://127.0.0.1:$PORT" --allow-http \
		--cloudflared-base "http://127.0.0.1:$PORT/cf/${CF_VARIANT:-new}" --cloudflared-sha256 "$(cf_sha darwin)" "${@:2}"
}

runs_grew() { [[ "$(grep -c . "$1")" -gt "$2" ]]; }

mac_label_pid() {
	[[ "${NO_LAUNCHCTL:-0}" != 1 ]] || return 0
	launchctl print "gui/$(id -u)/$1" 2>/dev/null | awk '/^[[:space:]]*pid = / {print $3; exit}'
}

# The same checks for every way the plist is written.
check_mac_files() { # HOME LABEL BIN
	local t="$1" label="$2" bin="$3"
	local plist="$t/Library/LaunchAgents/$label.plist" data="$t/Library/Application Support/homie"
	assert_file "$bin"
	assert_mode "$bin" 755
	assert_file "$plist"
	assert_mode "$plist" 644
	expect_ok "plutil -lint" plutil -lint "$plist"
	assert_eq "$(plutil -extract Label raw -o - "$plist")" "$label" "plist Label"
	assert_eq "$(plutil -extract ProgramArguments.0 raw -o - "$plist")" "$bin" "plist program"
	assert_eq "$(plutil -extract ProgramArguments.1 raw -o - "$plist")" "run" "plist argument"
	assert_eq "$(plutil -extract KeepAlive raw -o - "$plist")" "true" "plist KeepAlive"
	assert_eq "$(plutil -extract RunAtLoad raw -o - "$plist")" "true" "plist RunAtLoad"
	assert_eq "$(plutil -extract EnvironmentVariables.HOMED_DATA_DIR raw -o - "$plist")" "$data" "plist data dir"
	[[ "$(plutil -extract EnvironmentVariables.PATH raw -o - "$plist")" == "$(dirname "$bin"):"* ]] ||
		fail "the plist PATH must start with the program dir, where cloudflared is"
	pass "plist PATH starts with $(dirname "$bin")"
	assert_eq "$(plutil -extract StandardOutPath raw -o - "$plist")" "$t/Library/Logs/homie/homed.log" "plist log"
	assert_eq "$(plutil -extract StandardErrorPath raw -o - "$plist")" "$t/Library/Logs/homie/homed.log" "plist error log"
	file_lacks "$plist" "__"
	[[ -d "$t/Library/Logs/homie" ]] || fail "log dir missing"
	assert_mode "$data" 700
}

test_mac_files_only() {
	step "macOS: files only (--no-start), temp HOME, launchctl is a trap"
	local t="$WORK/home-a" bin live_plist="$HOME/Library/LaunchAgents/$REAL_LABEL.plist" live_sum="" live_pid
	mkdir -p "$t" "$WORK/shim"
	printf '#!/bin/sh\necho "launchctl $*" >>"%s/launchctl.log"\nexit 99\n' "$WORK" >"$WORK/shim/launchctl"
	chmod +x "$WORK/shim/launchctl"
	[[ ! -f "$live_plist" ]] || live_sum="$(cksum <"$live_plist")"
	live_pid="$(mac_label_pid "$REAL_LABEL" || true)"
	bin="$t/Library/Application Support/homie/bin/homed"

	run_capture mac_home "$t" --dry-run
	[[ "$RC" -eq 0 ]] || fail "dry run failed"
	out_has "archive=homed_0.0.1_darwin_arm64.tar.gz"
	assert_absent "$bin"
	out_has "cloudflared=$CF_VERSION"
	out_has "cloudflared_via=download"
	out_has "cloudflared_url=http://127.0.0.1:$PORT/cf/new/cloudflared-darwin-arm64.tgz"
	assert_absent "$t/Library/Application Support/homie/bin/cloudflared"
	run_capture mac_home "$t" --dry-run --arch amd64
	out_has "archive=homed_0.0.1_darwin_amd64.tar.gz"
	out_has "cloudflared_url=http://127.0.0.1:$PORT/cf/new/cloudflared-darwin-amd64.tgz"
	run_capture mac_home "$t" --dry-run --arch armv7
	[[ "$RC" -ne 0 ]] || fail "darwin has no armv7 build"
	out_has "has no homed build"

	run_capture mac_home "$t" --no-start
	[[ "$RC" -eq 0 ]] || fail "install failed"
	out_has "Checksum ok: homed_0.0.1_darwin_arm64.tar.gz"
	out_has "Checksum ok: cloudflared-darwin-arm64.tgz"
	out_has "Not started (--no-start)"
	check_mac_files "$t" "$REAL_LABEL" "$bin"
	assert_eq "$("$bin" version)" "homed 0.0.1" "installed version"
	assert_absent "$bin.prev"
	: >"$t/Library/Application Support/homie/keep"

	step "macOS: cloudflared is installed next to homed (no Homebrew here)"
	local cf="${bin%/homed}/cloudflared"
	assert_file "$cf"
	assert_mode "$cf" 755
	assert_eq "$("$cf" --version)" "cloudflared version $CF_VERSION (built fake)" "installed cloudflared"
	assert_absent "$cf.prev"

	local before after
	before="$(ls -li "$bin" "$cf" "$t/Library/LaunchAgents/$REAL_LABEL.plist")"
	run_capture mac_home "$t" --no-start
	[[ "$RC" -eq 0 ]] || fail "re-run failed"
	out_has "cloudflared $CF_VERSION is already installed"
	after="$(ls -li "$bin" "$cf" "$t/Library/LaunchAgents/$REAL_LABEL.plist")"
	assert_eq "$after" "$before" "same inodes and times after a re-run (cloudflared included)"

	step "macOS: a re-run updates an older cloudflared and keeps it as .prev"
	printf '#!/bin/sh\necho "cloudflared version 2026.10.0"\n' >"$cf"
	chmod 755 "$cf"
	expect_ok "re-run over an older cloudflared" mac_home "$t" --no-start
	assert_eq "$("$cf" --version)" "cloudflared version $CF_VERSION (built fake)" "cloudflared is the pinned version again"
	assert_eq "$("$cf.prev" --version)" "cloudflared version 2026.10.0" "the old cloudflared is kept as .prev"

	step "macOS: --domain / --quick-tunnel with --no-start only say what to run later"
	run_capture mac_home "$t" --no-start --domain home.example.com
	[[ "$RC" -eq 0 ]] || fail "--domain with --no-start failed"
	out_has "Cloudflare is not connected (--no-start). After the service is running: '$bin' cloudflare connect home.example.com"
	run_capture mac_home "$t" --no-start --quick-tunnel
	out_has "cloudflare quick"
	expect_fail "domain and quick tunnel together" "either --domain HOST or --quick-tunnel" mac_home "$t" --no-start --domain home.example.com --quick-tunnel
	expect_fail "domain with a path" "--domain must be a host name" mac_home "$t" --no-start --domain home.example.com/mcp
	expect_fail "domain without a dot" "--domain must be a host name" mac_home "$t" --no-start --domain localhost
	expect_fail "domain with uninstall" "go with an install" mac_home "$t" --no-start --uninstall --domain home.example.com

	expect_ok "update" mac_home "$t" --no-start --version v0.0.2
	assert_eq "$("$bin" version)" "homed 0.0.2" "updated version"
	assert_eq "$("$bin.prev" version)" "homed 0.0.1" "the old binary is kept as homed.prev"
	[[ -f "$t/Library/Application Support/homie/keep" ]] || fail "data lost on update"
	pass "data kept on update"

	expect_fail "bad checksum" "checksum mismatch" mac_home "$t" --no-start --version v0.0.4
	expect_fail "unsafe archive" "unsafe path" mac_home "$t" --no-start --version v0.0.5
	expect_fail "no build" "has no homed build" mac_home "$t" --no-start --version v0.0.6
	assert_eq "$("$bin" version)" "homed 0.0.2" "still v0.0.2 after the refusals"
	expect_ok "wrapped archive" mac_home "$t" --no-start --version v0.0.3
	assert_eq "$("$bin" version)" "homed 0.0.3" "wrapped archive installed"

	step "macOS: cloudflared refusals leave nothing behind"
	local t5="$WORK/home-e" cfbin
	mkdir -p "$t5"
	cfbin="$t5/Library/Application Support/homie/bin/cloudflared"
	expect_fail "cloudflared that is not the pinned build" "checksum mismatch for cloudflared-darwin-arm64.tgz" \
		env HOME="$t5" PATH="$WORK/shim:$MAC_PATH" bash "$HOME_SH" --release-base "http://127.0.0.1:$PORT" --allow-http \
		--cloudflared-base "http://127.0.0.1:$PORT/cf/new" --no-start
	assert_absent "$cfbin"
	assert_absent "$t5/Library/Application Support/homie/bin/homed"
	CF_VARIANT=evil expect_fail "unsafe cloudflared archive" "unsafe path in cloudflared-darwin-arm64.tgz" mac_home "$t5" --no-start
	assert_absent "$cfbin"

	step "macOS: --prefix with a space"
	local t2="$WORK/home-b" bin2
	mkdir -p "$t2"
	bin2="$WORK/pre fix/bin/homed"
	expect_ok "prefix install" mac_home "$t2" --no-start --prefix "$WORK/pre fix"
	check_mac_files "$t2" "$REAL_LABEL" "$bin2"
	expect_fail "bad prefix" "--prefix may only contain" mac_home "$t2" --no-start --prefix '/tmp/a&b'
	expect_ok "prefix uninstall" mac_home "$t2" --no-start --uninstall --yes --prefix "$WORK/pre fix"
	assert_absent "$bin2"

	step "macOS: uninstall keeps data, --purge deletes it"
	expect_ok "uninstall" mac_home "$t" --no-start --uninstall --yes
	assert_absent "$bin"
	assert_absent "$cf"
	assert_absent "$cf.prev"
	assert_absent "$t/Library/LaunchAgents/$REAL_LABEL.plist"
	assert_file "$t/Library/Application Support/homie/keep"
	expect_ok "reinstall" mac_home "$t" --no-start
	expect_ok "purge" mac_home "$t" --no-start --uninstall --purge --yes
	assert_absent "$t/Library/Application Support/homie"
	assert_absent "$t/Library/Logs/homie"

	[[ ! -e "$WORK/launchctl.log" ]] || fail "launchctl was called: $(cat "$WORK/launchctl.log")"
	pass "launchctl was never called"
	[[ -z "$live_sum" || "$(cksum <"$live_plist")" == "$live_sum" ]] || fail "the live LaunchAgent plist changed"
	assert_eq "$(mac_label_pid "$REAL_LABEL" || true)" "$live_pid" "the live agent's pid did not move"
}

# A fake brew: its prefix is $1; `install cloudflared` puts the fake cloudflared there, `FAIL` in the prefix makes it fail.
make_fake_brew() { # DIR PREFIX
	mkdir -p "$1" "$2/bin"
	cat >"$1/brew" <<EOF
#!/bin/sh
echo "\$*" >>"$2/brew.log"
case "\$1" in
--prefix) echo "$2" ;;
list) [ -x "$2/bin/cloudflared" ] ;;
install)
	[ ! -e "$2/FAIL" ] || { echo "brew: cannot install" >&2; exit 1; }
	printf '#!/bin/sh\necho "cloudflared version $CF_VERSION (built fake)"\n' >"$2/bin/cloudflared"
	chmod 755 "$2/bin/cloudflared" ;;
upgrade) ;;
esac
EOF
	chmod +x "$1/brew"
}

test_mac_brew() {
	step "macOS: Homebrew present: brew installs and upgrades cloudflared (a fake brew)"
	local t="$WORK/home-f" prefix="$WORK/brewprefix" data bin plist
	mkdir -p "$t"
	data="$t/Library/Application Support/homie"
	bin="$data/bin/homed"
	plist="$t/Library/LaunchAgents/$REAL_LABEL.plist"
	make_fake_brew "$WORK/brewshim" "$prefix"
	MAC_EXTRA_PATH="$WORK/brewshim"
	run_capture mac_home "$t" --dry-run
	out_has "cloudflared_via=brew"
	assert_absent "$prefix/brew.log.x"
	[[ ! -e "$prefix/bin/cloudflared" ]] || fail "a dry run must not install cloudflared"
	# a copy of ours from an earlier install would shadow brew's in the service's PATH
	mkdir -p "$data/bin"
	printf '#!/bin/sh\necho "cloudflared version 2026.10.0"\n' >"$data/bin/cloudflared"
	chmod 755 "$data/bin/cloudflared"
	run_capture mac_home "$t" --no-start
	[[ "$RC" -eq 0 ]] || fail "install with brew failed"
	out_has "cloudflared: cloudflared version $CF_VERSION (built fake) (Homebrew)"
	out_lacks "Checksum ok: cloudflared"
	file_has "$prefix/brew.log" "install cloudflared"
	assert_absent "$data/bin/cloudflared"
	assert_eq "$(plutil -extract EnvironmentVariables.PATH raw -o - "$plist")" "$data/bin:$prefix/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" "plist PATH has our dir, then brew's"
	check_mac_files "$t" "$REAL_LABEL" "$bin"
	expect_ok "re-run" mac_home "$t" --no-start
	file_has "$prefix/brew.log" "upgrade cloudflared"

	step "macOS: brew that fails: the pinned download takes over"
	rm -f "$prefix/bin/cloudflared"
	: >"$prefix/FAIL"
	run_capture mac_home "$t" --no-start
	[[ "$RC" -eq 0 ]] || fail "install with a failing brew failed"
	out_has "Homebrew could not install cloudflared; using the pinned download instead"
	assert_file "$data/bin/cloudflared"
	assert_eq "$("$data/bin/cloudflared" --version)" "cloudflared version $CF_VERSION (built fake)" "the pinned cloudflared"
	MAC_EXTRA_PATH=""
	expect_ok "purge" mac_home "$t" --no-start --uninstall --purge --yes
}

# The install with a start, against a fake launchctl (no real launchd): the connect that follows the start.
test_mac_connect() {
	step "macOS: --domain and --quick-tunnel after the start (a fake launchctl)"
	local t="$WORK/home-g" state="$WORK/lcstate" data bin log
	mkdir -p "$t" "$state" "$WORK/lcshim"
	cat >"$WORK/lcshim/launchctl" <<EOF
#!/bin/sh
case "\$1" in
bootstrap) : >"$state/loaded" ;;
bootout) rm -f "$state/loaded" ;;
print) [ -e "$state/loaded" ] || exit 113; echo "state = running" ;;
esac
exit 0
EOF
	chmod +x "$WORK/lcshim/launchctl"
	data="$t/Library/Application Support/homie"
	bin="$data/bin/homed"
	log="$data/cloudflare.log"
	MAC_EXTRA_PATH="$WORK/lcshim"
	run_capture mac_home "$t"
	[[ "$RC" -eq 0 ]] || fail "install failed"
	out_has "homed is running"
	out_has "PIN: 1234 5678"
	out_has "'$bin' cloudflare connect home.example.com"
	[[ ! -e "$log" ]] || fail "no connect was asked for"

	run_capture mac_home "$t" --domain home.example.com
	[[ "$RC" -eq 0 ]] || fail "install with --domain failed"
	out_has "Connecting home.example.com through Cloudflare"
	out_has "https://dash.cloudflare.com/login/fake"
	out_has "Cloudflare: connected. Your home answers at https://home.example.com"
	assert_eq "$(grep -F 'cloudflare connect' "$log")" "cloudflare connect home.example.com|$(id -un)|$data|${bin%/homed}/cloudflared" "connect ran with the data dir and cloudflared on the PATH"

	run_capture mac_home "$t" --quick-tunnel
	[[ "$RC" -eq 0 ]] || fail "install with --quick-tunnel failed"
	out_has "Cloudflare quick tunnel: running, for trying only"
	assert_eq "$(grep -F 'cloudflare quick' "$log")" "cloudflare quick|$(id -un)|$data|${bin%/homed}/cloudflared" "quick ran"

	run_capture mac_home "$t" --domain fail.example.com
	[[ "$RC" -ne 0 ]] || fail "a failed connect must fail the installer"
	out_has "the login link expired"
	out_has "the connect did NOT finish"
	out_has "'$bin' cloudflare connect fail.example.com"
	[[ -e "$state/loaded" ]] || fail "the agent must stay loaded after a failed connect"
	MAC_EXTRA_PATH=""
	expect_ok "uninstall" mac_home "$t" --uninstall --purge --yes
}

test_mac_launchd() {
	step "macOS: real launchd under the test label $TEST_LABEL"
	local t="$WORK/home-c" data bin plist domain n pid
	domain="gui/$(id -u)"
	[[ "$TEST_LABEL" != "$REAL_LABEL" ]] || fail "the test label must not be the live one"
	if ! launchctl print "$domain" >/dev/null 2>&1; then
		printf '   skipped: no GUI launchd session for this user\n'
		return 0
	fi
	mkdir -p "$t"
	data="$t/Library/Application Support/homie"
	bin="$data/bin/homed"
	plist="$t/Library/LaunchAgents/$TEST_LABEL.plist"
	mac_launchd_home() {
		env HOME="$t" PATH="$MAC_PATH" bash "$HOME_SH" --release-base "http://127.0.0.1:$PORT" --allow-http --label "$TEST_LABEL" \
			--cloudflared-base "http://127.0.0.1:$PORT/cf/new" --cloudflared-sha256 "$(cf_sha darwin)" "$@"
	}

	run_capture mac_launchd_home --version v0.0.1
	[[ "$RC" -eq 0 ]] || fail "install with launchd failed"
	out_has "homed is running"
	out_has "PIN: 1234 5678"
	out_has "Local Network permission"
	out_has "cloudflare connect home.example.com"
	out_has "brew services start homie"
	check_mac_files "$t" "$TEST_LABEL" "$bin"
	expect_ok "agent is loaded" launchctl print "$domain/$TEST_LABEL"
	wait_for 10 "the agent wrote its first run" test -s "$data/runs"
	assert_eq "$(cut -d"|" -f3,4 "$data/runs" | head -n 1)" "$data|0.0.1" "launchd gave the agent HOMED_DATA_DIR"

	step "macOS: re-run is a no-op"
	pid="$(mac_label_pid "$TEST_LABEL")"
	run_capture mac_launchd_home --version v0.0.1
	[[ "$RC" -eq 0 ]] || fail "re-run failed"
	out_has "Already installed and running"
	out_lacks "PIN:"
	assert_eq "$(mac_label_pid "$TEST_LABEL")" "$pid" "same process"

	step "macOS: --quick-tunnel runs the connect under the real launchd"
	run_capture mac_launchd_home --version v0.0.1 --quick-tunnel
	[[ "$RC" -eq 0 ]] || fail "install with --quick-tunnel failed"
	out_has "Cloudflare quick tunnel: running"
	file_has "$data/cloudflare.log" "cloudflare quick|$(id -un)|$data|$data/bin/cloudflared"
	assert_eq "$(mac_label_pid "$TEST_LABEL")" "$pid" "same process"

	step "macOS: update restarts into the new binary"
	expect_ok "update" mac_launchd_home --version v0.0.2
	wait_for 15 "a run of v0.0.2" grep -q "|0.0.2\$" "$data/runs"
	assert_eq "$("$bin" version)" "homed 0.0.2" "new binary"

	step "macOS: exit 75 (hub update) restarts the agent"
	n="$(grep -c . "$data/runs")"
	kill -USR1 "$(mac_label_pid "$TEST_LABEL")"
	wait_for 30 "a new run after exit 75" runs_grew "$data/runs" "$n"

	step "macOS: uninstall"
	expect_ok "uninstall" mac_launchd_home --uninstall --yes
	run_capture launchctl print "$domain/$TEST_LABEL"
	[[ "$RC" -ne 0 ]] || fail "agent still loaded"
	pass "agent unloaded"
	assert_absent "$plist"
	assert_absent "$bin"
	expect_ok "purge" mac_launchd_home --uninstall --purge --yes
	assert_absent "$data"
}

test_mac() {
	[[ "$(uname -s)" == "Darwin" ]] || {
		printf '   skipped: not macOS\n'
		return 0
	}
	test_mac_files_only
	test_mac_brew
	test_mac_connect
	if [[ "${NO_LAUNCHCTL:-0}" == 1 ]]; then
		printf '   skipped: the real launchd test (NO_LAUNCHCTL=1)\n'
	else
		test_mac_launchd
	fi
}

# ---------- Linux (Docker) ----------

native_platform() { printf 'linux/%s' "$(docker info --format '{{.Architecture}}' | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')"; }

platform_tag() { printf '%s' "$1" | tr '/' '-'; }

image_tag() { printf '%s:%s-%s' "$IMAGE_REPO" "$2" "$(platform_tag "$1")"; }

build_image() { # PLATFORM TARGET (base or systemd)
	local tag
	tag="$(image_tag "$1" "$2")"
	if docker image inspect "$tag" >/dev/null 2>&1; then
		return 0
	fi
	docker build -q --platform "$1" --target "$2" -t "$tag" -f "$HERE/Dockerfile.linux" "$HERE" >/dev/null || return 1
}

linux_nostart() { # PLATFORM
	local p="$1" tag
	tag="$(image_tag "$p" base)"
	step "linux $p: --no-start flows"
	if ! build_image "$p" base; then
		printf '   skipped: cannot build the image for %s (no emulation?)\n' "$p"
		return 0
	fi
	run_capture docker run --rm --name "homie-t41-$(platform_tag "$p")-ns" \
		--platform "$p" --add-host host.docker.internal:host-gateway \
		-v "$INSTALL_DIR:/src/install:ro" -e "BASE=http://host.docker.internal:$PORT" \
		"$tag" bash /src/install/test/in-container.sh nostart
	printf '%s\n' "$OUT" | sed 's/^/   | /'
	[[ "$RC" -eq 0 ]] || fail "the container test for $p failed"
	pass "linux $p --no-start"
}

linux_systemd() { # PLATFORM
	local p="$1" tag name
	tag="$(image_tag "$p" systemd)"
	name="homie-t41-$(platform_tag "$p")-sd"
	step "linux $p: systemd as PID 1"
	build_image "$p" systemd || {
		printf '   skipped: cannot build the image for %s\n' "$p"
		return 0
	}
	docker run -d --name "$name" --privileged --cgroupns=host --tmpfs /run --tmpfs /run/lock \
		--platform "$p" --add-host host.docker.internal:host-gateway \
		-v "$INSTALL_DIR:/src/install:ro" -e "BASE=http://host.docker.internal:$PORT" "$tag" >/dev/null
	run_capture docker exec "$name" bash /src/install/test/in-container.sh systemd
	printf '%s\n' "$OUT" | sed 's/^/   | /'
	if [[ "$RC" -ne 0 ]]; then
		docker exec "$name" journalctl -u homie --no-pager -n 30 2>&1 | sed 's/^/   journal| /' >&2 || true
		fail "the systemd container test for $p failed"
	fi
	docker rm -f "$name" >/dev/null
	pass "linux $p systemd"
}

test_linux() {
	local p native
	if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
		printf '   skipped: docker is not running\n'
		return 0
	fi
	native="$(native_platform)"
	for p in $PLATFORMS; do
		[[ "$p" != native ]] || p="$native"
		linux_nostart "$p"
	done
	for p in $SYSTEMD_PLATFORMS; do
		[[ "$p" != native ]] || p="$native"
		linux_systemd "$p"
	done
}

main() {
	trap cleanup EXIT
	case "$WHAT" in
	lint | mac | linux | all) ;;
	*) fail "usage: run.sh [lint|mac|linux|all]" ;;
	esac
	if [[ "$WHAT" == lint || "$WHAT" == all ]]; then
		test_shellcheck
		test_privacy_script
		test_pins
	fi
	if [[ "$WHAT" == mac || "$WHAT" == linux || "$WHAT" == all ]]; then
		start_fake_release
	fi
	if [[ "$WHAT" == mac || "$WHAT" == all ]]; then
		test_mac
	fi
	if [[ "$WHAT" == linux || "$WHAT" == all ]]; then
		test_linux
	fi
	printf '\nALL OK (%s)\n' "$WHAT"
}

main
