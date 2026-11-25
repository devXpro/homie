#!/usr/bin/env bash
# The generic privacy scan of this public repository (`make privacy`, run by CI). It prints every line that holds
#   - a home-directory path of a Mac (the one that starts with a slash, "Users" and a slash),
#   - a private network address (RFC 1918: the 10/8, 172.16/12 and 192.168/16 blocks; the documentation range
#     192.0.2.0/24 is not private and is what fixtures use),
#   - a MAC address (the locally administered 02:00:... ones are the fixtures).
# and exits 1 if there is any. Names, domains and the like are checked by a private script that is not in this repository.
#
# A line that has to hold a path or an address on purpose (a test of an address filter, say) carries the word privacy:ok
# (MAC addresses are matched alone: use 02:00:... there).
# Searched: the tracked files and the new, not ignored ones; go.sum and package-lock.json are skipped.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT
readonly SKIP=(':!go.sum' ':!**/package-lock.json' ':!.github/privacy-check.sh')

# scan LABEL ARGS...: git grep with ARGS; the hits (minus the allowed lines) go to stdout under LABEL.
status=0
scan() {
	local label="$1" hits
	shift
	hits="$(git -C "$ROOT" grep -I -n --untracked --exclude-standard "$@" -- . "${SKIP[@]}" | grep -v 'privacy:ok' || true)"
	if [[ -n "$hits" ]]; then
		printf '%s\n%s\n' "$label" "$hits"
		status=1
	fi
}

scan "local path (a Mac home directory):" -E '/Use''rs/'

scan "private network address (RFC 1918):" -E \
	'(^|[^0-9.])(10(\.[0-9]{1,3}){3}|172\.(1[6-9]|2[0-9]|3[01])(\.[0-9]{1,3}){2}|192\.168(\.[0-9]{1,3}){2})($|[^0-9])'

# git grep -o prints each match on its own, so a line with a fixture MAC and a real one still reports the real one.
macs="$(git -C "$ROOT" grep -I -n -o --untracked --exclude-standard -E \
	'(^|[^0-9A-Fa-f:])([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}($|[^0-9A-Fa-f:])' -- . "${SKIP[@]}" |
	grep -viE '(^|[^0-9a-f])02:00(:[0-9a-f]{2}){4}($|[^0-9a-f:])' || true)"
if [[ -n "$macs" ]]; then
	printf 'MAC address (only 02:00:... is allowed):\n%s\n' "$macs"
	status=1
fi

exit "$status"
