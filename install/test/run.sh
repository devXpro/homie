#!/usr/bin/env bash
# Tests of install/home.sh against a fake release (docs/TESTING.md, "Home installers").
#
#   run.sh [lint|mac|linux|all]     default: all
#
#   lint:       shellcheck on the scripts and the wrapper
#   mac:        macOS, in a temporary HOME: --no-start files + `plutil -lint` (any launchctl call fails the test), then a
#               real launchd start/update/restart/uninstall under a TEST label (never the real LaunchAgent)
#   linux:      Debian containers homie-t41-*: --no-start flows on every platform, a real systemd start where it can
#
# Environment: PLATFORMS (default "native linux/amd64"; "native" is the Docker host's CPU; add linux/arm/v7 if you can wait: building its image under qemu takes 30+ minutes),
# SYSTEMD_PLATFORMS (default "native linux/amd64"), KEEP_IMAGES=1 keeps the test images for the next run.
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
	if [[ "$(uname -s)" == "Darwin" ]]; then
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

# ---------- macOS ----------

mac_home() { env HOME="$1" PATH="$WORK/shim:$PATH" bash "$HOME_SH" --release-base "http://127.0.0.1:$PORT" --allow-http "${@:2}"; }

runs_grew() { [[ "$(grep -c . "$1")" -gt "$2" ]]; }

mac_label_pid() { launchctl print "gui/$(id -u)/$1" 2>/dev/null | awk '/^[[:space:]]*pid = / {print $3; exit}'; }

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
	run_capture mac_home "$t" --dry-run --arch amd64
	out_has "archive=homed_0.0.1_darwin_amd64.tar.gz"
	run_capture mac_home "$t" --dry-run --arch armv7
	[[ "$RC" -ne 0 ]] || fail "darwin has no armv7 build"
	out_has "has no homed build"

	run_capture mac_home "$t" --no-start
	[[ "$RC" -eq 0 ]] || fail "install failed"
	out_has "Checksum ok: homed_0.0.1_darwin_arm64.tar.gz"
	out_has "Not started (--no-start)"
	check_mac_files "$t" "$REAL_LABEL" "$bin"
	assert_eq "$("$bin" version)" "homed 0.0.1" "installed version"
	assert_absent "$bin.prev"
	: >"$t/Library/Application Support/homie/keep"

	local before after
	before="$(ls -li "$bin" "$t/Library/LaunchAgents/$REAL_LABEL.plist")"
	expect_ok "re-run" mac_home "$t" --no-start
	after="$(ls -li "$bin" "$t/Library/LaunchAgents/$REAL_LABEL.plist")"
	assert_eq "$after" "$before" "same inodes and times after a re-run"

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
	mac_launchd_home() { env HOME="$t" bash "$HOME_SH" --release-base "http://127.0.0.1:$PORT" --allow-http --label "$TEST_LABEL" "$@"; }

	run_capture mac_launchd_home --version v0.0.1
	[[ "$RC" -eq 0 ]] || fail "install with launchd failed"
	out_has "homed is running"
	out_has "PIN: 1234 5678"
	out_has "Local Network permission"
	out_has "relay connect -"
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
	test_mac_launchd
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
