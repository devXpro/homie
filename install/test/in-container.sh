#!/usr/bin/env bash
# Runs inside a homie-t41-* Debian container as root; started by run.sh, which also serves the fake release.
#   in-container.sh nostart   home.sh --no-start flows: files, modes, user, unit, wrapper, update, refusals, uninstall
#   in-container.sh systemd   the same container booted with systemd as PID 1: a real service start, restart on 75
# Environment: BASE (the fake release server, http://host.docker.internal:PORT), LATEST_FILE_CMD is not needed: the
# server's LATEST is changed by run.sh through a request-less file, so this script asks for versions with --version.
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
source /src/install/test/lib.sh

readonly MODE="${1:?mode}"
readonly HOME_SH="/src/install/home.sh"
readonly DATA="/var/lib/homie"
readonly LIB="/usr/local/lib/homie"
readonly CMD_PATH="/usr/local/bin/homed"
readonly UNIT="/etc/systemd/system/homie.service"
: "${BASE:?BASE}"

case "$(uname -m)" in
aarch64) NATIVE=arm64 ;;
x86_64) NATIVE=amd64 ;;
armv7l | armv8l) NATIVE=armv7 ;;
*) NATIVE=unknown ;;
esac

install_sh() { bash "$HOME_SH" --release-base "$BASE" --allow-http "$@"; }

as_tester() { su tester -s /bin/bash -c "$1"; }

setup_tester() {
	id tester >/dev/null 2>&1 || useradd -m tester
	# tester may use sudo only to become homie: enough to exercise the wrapper's sudo path.
	printf 'tester ALL=(homie) NOPASSWD: ALL\n' >/etc/sudoers.d/tester
	chmod 440 /etc/sudoers.d/tester
}

check_files() { # VERSION
	local v="$1"
	step "files of $v"
	id homie >/dev/null || fail "user homie missing"
	assert_eq "$(getent passwd homie | cut -d: -f6)" "$DATA" "home dir of homie"
	case "$(getent passwd homie | cut -d: -f7)" in */nologin | /bin/false) pass "login shell is disabled" ;; *) fail "homie has a login shell" ;; esac
	assert_mode "$DATA" 700
	assert_owner "$DATA" homie
	assert_file "$LIB/homed"
	assert_mode "$LIB/homed" 755
	assert_owner "$LIB/homed" homie
	assert_owner "$LIB" homie
	assert_file "$CMD_PATH"
	assert_mode "$CMD_PATH" 755
	assert_owner "$CMD_PATH" root
	file_has "$CMD_PATH" "Installed by the Homie installer"
	file_has "$CMD_PATH" "REAL=$LIB/homed"
	file_lacks "$CMD_PATH" "__"
	assert_eq "$("$LIB/homed" version)" "homed $v" "installed version"
	assert_file "$UNIT"
	assert_mode "$UNIT" 644
	file_has "$UNIT" "ExecStart=$LIB/homed run"
	file_has "$UNIT" "User=homie"
	file_has "$UNIT" "Restart=always"
	file_has "$UNIT" "Environment=HOMED_DATA_DIR=$DATA"
	file_has "$UNIT" "ReadWritePaths=$DATA $LIB"
	file_has "$UNIT" "NoNewPrivileges=true"
	file_has "$UNIT" "AF_NETLINK"
	file_lacks "$UNIT" "PrivateNetwork"
	file_lacks "$UNIT" "IPAddressDeny"
	file_lacks "$UNIT" "__"
	if command -v systemd-analyze >/dev/null 2>&1; then
		run_capture systemd-analyze verify "$UNIT"
		[[ -z "$OUT" ]] || fail "systemd-analyze verify complains"
		pass "systemd-analyze verify is clean"
	fi
}

check_wrapper() {
	step "the homed command"
	run_capture "$CMD_PATH" env
	assert_eq "$OUT" "homie $DATA" "root runs the CLI as homie on the service data dir"
	run_capture runuser -u homie -- "$CMD_PATH" env
	assert_eq "$OUT" "homie $DATA" "homie itself runs it directly"
	run_capture as_tester "$CMD_PATH version"
	assert_eq "$OUT" "homed $(cat /tmp/expect-version)" "an unprivileged user can run 'homed version'"
	run_capture as_tester "$CMD_PATH env"
	assert_eq "$OUT" "homie $DATA" "an unprivileged user with sudo rights runs the CLI as homie"
	rm -f /etc/sudoers.d/tester
	run_capture as_tester "$CMD_PATH env </dev/null"
	[[ "$RC" -ne 0 ]] || fail "a user without sudo rights must not get the CLI"
	pass "a user without sudo rights is refused"
	setup_tester
}

nostart() {
	local v1=0.0.1 v2=0.0.2
	echo "$v1" >/tmp/expect-version
	setup_tester

	step "dry run"
	run_capture install_sh --version v$v1 --dry-run
	[[ "$RC" -eq 0 ]] || fail "dry run failed"
	out_has "archive=homed_${v1}_linux_${NATIVE}.tar.gz"
	out_has "url=$BASE/download/v$v1/homed_${v1}_linux_${NATIVE}.tar.gz"
	assert_absent "$LIB/homed"
	assert_absent "$UNIT"
	local a
	for a in amd64 arm64 armv7; do
		run_capture install_sh --version v$v1 --dry-run --arch "$a"
		out_has "archive=homed_${v1}_linux_${a}.tar.gz"
	done

	step "first install (latest = v$v1, found through the /latest redirect)"
	run_capture install_sh --no-start
	[[ "$RC" -eq 0 ]] || fail "install failed"
	out_has "Checksum ok: homed_${v1}_linux_${NATIVE}.tar.gz"
	out_has "Not started (--no-start)"
	check_files "$v1"
	check_wrapper
	assert_absent "$LIB/homed.prev"
	: >"$DATA/keep"
	chown homie:homie "$DATA/keep"
	chmod 600 "$DATA/keep"

	step "re-run is a no-op for the files"
	local before after
	before="$(ls -li "$LIB/homed" "$UNIT" "$CMD_PATH")"
	expect_ok "second run" install_sh --no-start
	after="$(ls -li "$LIB/homed" "$UNIT" "$CMD_PATH")"
	assert_eq "$after" "$before" "same inodes and times after a re-run"

	step "update to v$v2 in place"
	echo "$v2" >/tmp/expect-version
	expect_ok "update" install_sh --no-start --version v$v2
	check_files "$v2"
	assert_eq "$("$LIB/homed.prev" version)" "homed $v1" "the old binary is kept as homed.prev"
	assert_file "$DATA/keep"
	pass "data kept"

	step "files the service user leaves in the program dir never act as root"
	printf 'canary\n' >/etc/homie-canary
	chmod 644 /etc/homie-canary
	runuser -u homie -- rm -f "$LIB/homed.prev"
	runuser -u homie -- ln -s /etc/homie-canary "$LIB/homed.prev"
	# shellcheck disable=SC2016 # $1 belongs to the inner shell
	runuser -u homie -- sh -c 'printf "#!/bin/sh\ntouch /etc/homie-pwned 2>/dev/null\necho homed 9.9.9\n" >"$1.x" &&
		chmod 755 "$1.x" && mv -f "$1.x" "$1"' sh "$LIB/homed"
	expect_ok "update over a swapped binary and a symlinked homed.prev" install_sh --no-start --version v$v2
	assert_eq "$(cat /etc/homie-canary)" "canary" "root did not write through the homed.prev symlink"
	assert_absent /etc/homie-pwned
	pass "the swapped binary did not run as root"
	check_files "$v2"
	rm -f /etc/homie-canary "$LIB/homed.prev"

	step "refusals leave the install alone"
	expect_fail "bad checksum" "checksum mismatch" install_sh --no-start --version v0.0.4
	assert_eq "$("$LIB/homed" version)" "homed $v2" "still v$v2 after a checksum mismatch"
	expect_fail "unsafe archive" "unsafe path" install_sh --no-start --version v0.0.5
	assert_eq "$("$LIB/homed" version)" "homed $v2" "still v$v2 after an unsafe archive"
	if [[ "$NATIVE" != arm64 ]]; then
		expect_fail "no build for this CPU" "has no homed build" install_sh --no-start --version v0.0.6
	fi
	expect_fail "unknown version" "cannot download" install_sh --no-start --version v9.9.9
	expect_fail "version syntax" "--version must look like" install_sh --no-start --version latest
	expect_fail "plain http without the test flag" "must use https" bash "$HOME_SH" --release-base "$BASE" --dry-run
	expect_fail "not root" "run as root" as_tester "bash $HOME_SH --release-base $BASE --allow-http --no-start"
	echo "$v2" >/tmp/expect-version

	step "a foreign file at the command path"
	mv "$CMD_PATH" /tmp/homed.wrapper.keep
	printf '#!/bin/sh\necho mine\n' >"$CMD_PATH"
	chmod 755 "$CMD_PATH"
	expect_fail "foreign homed" "was not installed by this installer" install_sh --no-start --version v$v2
	assert_eq "$("$CMD_PATH")" "mine" "the foreign file is untouched"
	mv /tmp/homed.wrapper.keep "$CMD_PATH"

	step "curl | bash: templates fetched from the raw URL at the release tag, wrapped archive"
	expect_ok "uninstall" install_sh --no-start --uninstall --yes
	assert_absent "$LIB/homed"
	assert_absent "$UNIT"
	assert_absent "$CMD_PATH"
	assert_file "$DATA/keep"
	getent passwd homie >/dev/null || fail "uninstall without --purge must keep the user"
	pass "uninstall keeps data and the user"
	echo 0.0.3 >/tmp/expect-version
	run_capture bash -c "cat '$HOME_SH' | HOMIE_RAW_BASE='$BASE/raw' bash -s -- --release-base '$BASE' --allow-http --no-start --version v0.0.3"
	[[ "$RC" -eq 0 ]] || fail "piped install failed"
	check_files 0.0.3
	assert_file "$DATA/keep"

	step "--prefix"
	expect_ok "prefix install" install_sh --no-start --version v$v1 --prefix "/opt/my homie"
	assert_file "/opt/my homie/lib/homie/homed"
	file_has "/opt/my homie/bin/homed" "REAL=/opt/my homie/lib/homie/homed"
	file_has "$UNIT" "ExecStart=/opt/my homie/lib/homie/homed run"
	expect_fail "bad prefix" "--prefix may only contain" install_sh --no-start --prefix '/opt/x;y'
	expect_fail "relative prefix" "--prefix must be an absolute path" install_sh --no-start --prefix opt
	expect_ok "prefix uninstall" install_sh --no-start --uninstall --yes --prefix "/opt/my homie"
	assert_absent "/opt/my homie/lib/homie/homed"

	step "--uninstall --purge"
	expect_ok "reinstall" install_sh --no-start --version v$v1
	expect_ok "purge" install_sh --no-start --uninstall --purge --yes
	assert_absent "$DATA"
	if getent passwd homie >/dev/null; then fail "--purge must delete the user"; fi
	pass "purge deleted the data and the user"
	expect_fail "purge needs uninstall" "--purge only goes with --uninstall" install_sh --purge
	step "nostart: all checks passed ($NATIVE)"
}

wait_system() { # systemd booted far enough to take unit commands
	local i=0 s
	while [[ "$i" -lt 60 ]]; do
		s="$(systemctl is-system-running 2>/dev/null || true)"
		case "$s" in running | degraded) return 0 ;; *) ;; esac
		sleep 1
		i=$((i + 1))
	done
	fail "systemd did not finish booting (is-system-running: $s)"
}

runs_grew() { [[ "$(runs_count)" -gt "$1" ]]; }
runs_count() { grep -c . "$DATA/runs" 2>/dev/null || echo 0; }

systemd_mode() {
	local v1=0.0.1 v2=0.0.2 n
	echo "$v1" >/tmp/expect-version
	setup_tester
	wait_system
	pass "systemd is up ($(systemctl is-system-running || true))"

	step "install and start"
	run_capture install_sh --version v$v1
	[[ "$RC" -eq 0 ]] || fail "install failed"
	out_has "homed is running (homie.service)"
	out_has "PIN: 1234 5678"
	out_has "homed relay connect -"
	check_files "$v1"
	expect_ok "unit is active" systemctl is-active homie
	expect_ok "unit is enabled" systemctl is-enabled homie
	assert_eq "$(systemctl show -p Restart --value homie)" "always" "Restart=always loaded"
	assert_eq "$(systemctl show -p NoNewPrivileges --value homie)" "yes" "NoNewPrivileges loaded"
	assert_eq "$(systemctl show -p ProtectSystem --value homie)" "strict" "ProtectSystem loaded"
	wait_for 10 "the service wrote its first run" test -s "$DATA/runs"
	assert_eq "$(cut -d"|" -f2,3,4 "$DATA/runs" | head -n 1)" "homie|$DATA|$v1" "runs as homie with HOMED_DATA_DIR"
	pid="$(systemctl show -p MainPID --value homie)"
	assert_eq "$(ps -o user= -p "$pid" | tr -d ' ')" "homie" "the process belongs to homie"
	assert_owner "$DATA/runs" homie

	step "the sandbox keeps the LAN usable (broadcast, multicast)"
	touch "$DATA/nettest"
	chown homie "$DATA/nettest"
	systemctl restart homie
	wait_for 15 "nettest result written" test -s "$DATA/nettest.out"
	assert_eq "$(cat "$DATA/nettest.out")" "broadcast=0 multicast=124" "UDP broadcast sent, multicast group joined"
	rm -f "$DATA/nettest" "$DATA/nettest.out"

	step "exit 75 (hub update) restarts the service"
	n="$(runs_count)"
	systemctl kill -s USR1 homie
	wait_for 25 "a new run after exit 75" runs_grew "$n"
	expect_ok "still active" systemctl is-active homie

	step "re-run with nothing to change does not restart"
	pid="$(systemctl show -p MainPID --value homie)"
	run_capture install_sh --version v$v1
	[[ "$RC" -eq 0 ]] || fail "re-run failed"
	out_has "Already installed and running"
	out_lacks "PIN:"
	assert_eq "$(systemctl show -p MainPID --value homie)" "$pid" "same process after a no-op re-run"

	step "update to v$v2 restarts into the new binary"
	echo "$v2" >/tmp/expect-version
	n="$(runs_count)"
	expect_ok "update" install_sh --version v$v2
	wait_for 15 "a run of v$v2" grep -q "|$v2\$" "$DATA/runs"
	check_files "$v2"
	check_wrapper

	step "uninstall"
	expect_ok "uninstall" install_sh --uninstall --yes
	run_capture systemctl is-active homie
	[[ "$RC" -ne 0 ]] || fail "service still active"
	pass "service stopped"
	assert_absent "$UNIT"
	assert_absent "$LIB/homed"
	assert_file "$DATA/runs"
	expect_ok "purge" install_sh --uninstall --purge --yes
	assert_absent "$DATA"
	step "systemd: all checks passed ($NATIVE)"
}

case "$MODE" in
nostart) nostart ;;
systemd) systemd_mode ;;
*) fail "unknown mode $MODE" ;;
esac
