#!/usr/bin/env bash
# Homie relay installer.
#
# Installs the server side of Homie (Caddy + frps, see docs/RELAY.md) on a
# Docker host and prints the connect code for the home box.
#
#   curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh \
#     | sudo bash -s -- --domain home.example.com
#
# Safe for `curl | bash`: everything happens inside main(), which is called on
# the last line, so a truncated download runs nothing.
set -euo pipefail

readonly HOMIE_RAW_BASE="${HOMIE_RAW_BASE:-https://raw.githubusercontent.com/devXpro/homie/${HOMIE_REF:-main}}"
readonly DEFAULT_DIR="/opt/homie-relay"
readonly TLS_WAIT_SECONDS=120

DOMAIN=""
MODE="auto"
NETWORK="proxy"
DIR="$DEFAULT_DIR"
ROTATE_TOKEN=0
PRINT_CODE=0
UNINSTALL=0
YES=0
SKIP_DNS_CHECK=0
NO_ROOT_CHECK=0 # tests only
NO_START=0      # tests only

# Set by write_file when a file's content changed.
FILE_CHANGED=0
CHANGED_FRPS=0
CHANGED_CADDY=0

TMP_DIR=""

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
Usage: server.sh --domain DOMAIN [options]

  --domain D          public domain of the relay (A record -> this server). Asked for if missing.
  --mode M            auto (default), standalone (own Caddy on 80/443) or behind-proxy (frps only).
  --network NAME      docker network of the existing proxy (behind-proxy). Default: proxy.
  --dir PATH          install directory. Default: /opt/homie-relay.
  --rotate-token      generate a new token (home boxes must connect again).
  --print-code        print the connect code of an existing install and exit.
  --uninstall         stop the containers and remove the installed files.
  --yes               do not ask questions.
  --skip-dns-check    skip the DNS check (tests only).
  -h, --help          show this help.
EOF
}

parse_args() {
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--domain)
			[[ $# -ge 2 ]] || die "--domain needs a value"
			DOMAIN="$2"
			shift 2
			;;
		--mode)
			[[ $# -ge 2 ]] || die "--mode needs a value"
			MODE="$2"
			shift 2
			;;
		--network)
			[[ $# -ge 2 ]] || die "--network needs a value"
			NETWORK="$2"
			shift 2
			;;
		--dir)
			[[ $# -ge 2 ]] || die "--dir needs a value"
			DIR="$2"
			shift 2
			;;
		--rotate-token)
			ROTATE_TOKEN=1
			shift
			;;
		--print-code)
			PRINT_CODE=1
			shift
			;;
		--uninstall)
			UNINSTALL=1
			shift
			;;
		--yes | -y)
			YES=1
			shift
			;;
		--skip-dns-check)
			SKIP_DNS_CHECK=1
			shift
			;;
		--no-root-check)
			NO_ROOT_CHECK=1
			shift
			;;
		--no-start)
			NO_START=1
			shift
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

	case "$MODE" in
	auto | standalone | behind-proxy) ;;
	*) die "--mode must be auto, standalone or behind-proxy" ;;
	esac
	[[ "$NETWORK" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid --network: $NETWORK"
	[[ -n "$DIR" ]] || die "--dir must not be empty"
	if [[ "$DIR" != /* ]]; then
		DIR="$PWD/$DIR"
	fi
}

have_tty() { (: </dev/tty) 2>/dev/null; }

# ask PROMPT: reads one line from the terminal, even when stdin is a pipe.
ask() {
	local answer=""
	have_tty || return 1
	printf '%s' "$1" >&2
	IFS= read -r answer </dev/tty || return 1
	printf '%s' "$answer"
}

# confirm PROMPT: yes/no, default no. With --yes it always agrees.
confirm() {
	local answer
	if [[ "$YES" -eq 1 ]]; then
		return 0
	fi
	answer="$(ask "$1 [y/N] ")" || die "no terminal to ask '$1'; rerun with --yes"
	[[ "$answer" =~ ^[Yy]([Ee][Ss])?$ ]]
}

validate_domain() {
	DOMAIN="$(printf '%s' "$DOMAIN" | tr '[:upper:]' '[:lower:]')"
	[[ "$DOMAIN" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$ ]] ||
		die "'$DOMAIN' is not a valid domain name (example: home.example.com)"
	if [[ "$DOMAIN" =~ ^[0-9.]+$ ]]; then
		die "the domain must be a name, not an IP address"
	fi
}

need_domain() {
	if [[ -z "$DOMAIN" ]]; then
		DOMAIN="$(ask "Domain of the relay (an A record must point at this server, e.g. home.example.com): ")" ||
			die "--domain is required"
	fi
	[[ -n "$DOMAIN" ]] || die "--domain is required"
	validate_domain
}

require_root() {
	if [[ "$NO_ROOT_CHECK" -eq 1 || "$(id -u)" -eq 0 ]]; then
		return 0
	fi
	die "run as root, for example: curl -fsSL <url> | sudo bash -s -- --domain $DOMAIN"
}

preflight() {
	local v
	command -v docker >/dev/null 2>&1 || die "docker is not installed (https://docs.docker.com/engine/install/)"
	docker info >/dev/null 2>&1 || die "cannot talk to the docker daemon (is it running, and are you root?)"
	v="$(docker compose version --short 2>/dev/null || true)"
	v="${v#v}"
	[[ "${v%%.*}" =~ ^[0-9]+$ && "${v%%.*}" -ge 2 ]] ||
		die "docker compose v2 is required (the 'docker compose' plugin, not 'docker-compose')"
	command -v curl >/dev/null 2>&1 || die "curl is not installed"
	if ! command -v ss >/dev/null 2>&1 && ! command -v netstat >/dev/null 2>&1; then
		die "neither ss nor netstat is installed"
	fi
}

# ---------- DNS ----------

resolve_a() {
	local d="$1"
	{
		if command -v dig >/dev/null 2>&1; then
			dig +short +time=3 +tries=1 A "$d" 2>/dev/null
		elif command -v getent >/dev/null 2>&1; then
			getent ahostsv4 "$d" 2>/dev/null | awk '{print $1}'
		elif command -v host >/dev/null 2>&1; then
			host -t A "$d" 2>/dev/null | awk '/has address/ {print $NF}'
		elif command -v nslookup >/dev/null 2>&1; then
			nslookup -type=A "$d" 2>/dev/null | awk '/^Address: / {print $2}'
		fi
	} | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | sort -u || true
}

local_ips() {
	if command -v ip >/dev/null 2>&1; then
		ip -4 -o addr show 2>/dev/null | awk '{print $4}' | cut -d/ -f1
	elif command -v ifconfig >/dev/null 2>&1; then
		ifconfig 2>/dev/null | awk '/inet / {print $2}'
	fi
}

public_ip() {
	local ip
	ip="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
	if [[ "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
		printf '%s' "$ip"
	fi
}

check_dns() {
	local records mine ip rec
	if [[ "$SKIP_DNS_CHECK" -eq 1 ]]; then
		info "DNS check skipped."
		return 0
	fi
	records="$(resolve_a "$DOMAIN")"
	if [[ -z "$records" ]]; then
		info "The domain $DOMAIN has no A record yet."
	else
		mine="$(local_ips)"
		ip="$(public_ip)"
		if [[ -n "$ip" ]]; then
			mine="$mine"$'\n'"$ip"
		fi
		while IFS= read -r rec; do
			if printf '%s\n' "$mine" | grep -qxF "$rec"; then
				info "DNS ok: $DOMAIN -> $rec"
				return 0
			fi
		done <<<"$records"
		info "The A record of $DOMAIN points at: $(printf '%s' "$records" | tr '\n' ' ')"
		info "This server has: $(printf '%s' "$mine" | grep -v '^$' | tr '\n' ' ')"
	fi
	info "Create an A record for $DOMAIN pointing at this server's public IP (DNS only, no CDN proxy)."
	info "Without it the TLS certificate cannot be issued and the home box cannot connect."
	if [[ "$YES" -eq 1 ]]; then
		die "DNS check failed; fix the record or rerun with --skip-dns-check"
	fi
	confirm "Continue anyway?" || die "aborted"
}

# ---------- mode ----------

port_listening() {
	local port="$1"
	if command -v ss >/dev/null 2>&1; then
		ss -ltn 2>/dev/null | awk -v suffix=":$port" \
			'substr($4, length($4) - length(suffix) + 1) == suffix {f = 1} END {exit !f}'
	else
		netstat -an 2>/dev/null | grep -i LISTEN | grep -Eq "[.:]${port}[[:space:]]"
	fi
}

detect_mode() {
	local busy=0
	port_listening 80 && busy=1
	port_listening 443 && busy=1
	case "$MODE" in
	auto)
		if [[ "$busy" -eq 1 ]]; then
			MODE="behind-proxy"
			info "Something already listens on port 80/443: using behind-proxy mode (network '$NETWORK')."
		else
			MODE="standalone"
			info "Ports 80 and 443 are free: using standalone mode (own Caddy)."
		fi
		;;
	standalone)
		if [[ "$busy" -eq 1 ]]; then
			die "standalone mode needs ports 80 and 443, but something already listens there; use --mode behind-proxy"
		fi
		;;
	*) ;;
	esac
	if [[ "$MODE" == "behind-proxy" && "$NO_START" -eq 0 ]]; then
		docker network inspect "$NETWORK" >/dev/null 2>&1 ||
			die "docker network '$NETWORK' does not exist; pass the network of your proxy with --network"
	fi
}

# ---------- files ----------

SCRIPT_RELAY_DIR=""

locate_assets() {
	local src="${BASH_SOURCE[0]:-}" d
	if [[ -n "$src" && -f "$src" ]]; then
		d="$(cd "$(dirname "$src")/../relay" 2>/dev/null && pwd)" || d=""
		if [[ -n "$d" && -f "$d/frps.toml" ]]; then
			SCRIPT_RELAY_DIR="$d"
		fi
	fi
}

# fetch_asset NAME DEST: copy from the repo checkout, or download from GitHub.
fetch_asset() {
	if [[ -n "$SCRIPT_RELAY_DIR" ]]; then
		cp -- "$SCRIPT_RELAY_DIR/$1" "$2"
	else
		curl -fsSL --retry 3 "$HOMIE_RAW_BASE/relay/$1" -o "$2" ||
			die "cannot download relay/$1 from $HOMIE_RAW_BASE"
	fi
}

# write_file SRC DEST MODE: replace DEST in place (keeps the inode, which matters
# for single-file bind mounts) only if the content differs.
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

env_get() {
	local key="$1" file="$DIR/.env"
	[[ -f "$file" ]] || return 0
	sed -n "s/^${key}=//p" "$file" | tail -n 1
}

gen_token() {
	head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

read_token() {
	[[ -f "$DIR/token" ]] || return 0
	tr -d '[:space:]' <"$DIR/token"
}

# connect_code DOMAIN TOKEN: "ghr1." + base64url without padding of the JSON.
connect_code() {
	local json
	json="$(printf '{"v":1,"domain":"%s","token":"%s"}' "$1" "$2")"
	printf 'ghr1.%s' "$(printf '%s' "$json" | base64 | tr -d '\n=' | tr '+/' '-_')"
}

install_files() {
	local tmp token current compose_src
	TMP_DIR="$(mktemp -d)"
	tmp="$TMP_DIR"

	mkdir -p "$DIR"
	chmod 700 "$DIR"

	# Token: keep the existing one unless it is rotated.
	current="$(read_token)"
	if [[ -n "$current" && "$ROTATE_TOKEN" -eq 0 ]]; then
		[[ "$current" =~ ^[0-9a-f]{64}$ ]] ||
			die "$DIR/token is not 64 hex characters; fix it or use --rotate-token"
		token="$current"
	else
		token="$(gen_token)"
		[[ "$token" =~ ^[0-9a-f]{64}$ ]] || die "could not generate a token"
	fi
	printf '%s' "$token" >"$tmp/token"
	write_file "$tmp/token" "$DIR/token" 600
	[[ "$FILE_CHANGED" -eq 1 ]] && CHANGED_FRPS=1

	fetch_asset frps.toml "$tmp/frps.toml"
	write_file "$tmp/frps.toml" "$DIR/frps.toml" 644
	[[ "$FILE_CHANGED" -eq 1 ]] && CHANGED_FRPS=1

	fetch_asset offline.html "$tmp/offline.html"
	write_file "$tmp/offline.html" "$DIR/offline.html" 644
	[[ "$FILE_CHANGED" -eq 1 ]] && CHANGED_FRPS=1

	if [[ "$MODE" == "standalone" ]]; then
		compose_src="docker-compose.yml"
		fetch_asset Caddyfile.tmpl "$tmp/Caddyfile.tmpl"
		sed "s/__DOMAIN__/$DOMAIN/g" "$tmp/Caddyfile.tmpl" >"$tmp/Caddyfile"
		write_file "$tmp/Caddyfile" "$DIR/Caddyfile" 644
		[[ "$FILE_CHANGED" -eq 1 ]] && CHANGED_CADDY=1
	else
		compose_src="docker-compose.behind-proxy.yml"
	fi
	fetch_asset "$compose_src" "$tmp/docker-compose.yml"
	write_file "$tmp/docker-compose.yml" "$DIR/docker-compose.yml" 644

	printf 'HOMIE_DOMAIN=%s\nHOMIE_MODE=%s\nHOMIE_PROXY_NETWORK=%s\n' "$DOMAIN" "$MODE" "$NETWORK" >"$tmp/env"
	write_file "$tmp/env" "$DIR/.env" 600
}

compose() {
	(cd "$DIR" && docker compose "$@")
}

start_stack() {
	local first="$1"
	compose up -d --remove-orphans
	if [[ "$first" -eq 0 ]]; then
		# Bind-mounted files changed in place: frps reads them at start only.
		if [[ "$CHANGED_FRPS" -eq 1 ]]; then
			info "Restarting frps to pick up the new configuration..."
			compose restart frps
		fi
		if [[ "$MODE" == "standalone" && "$CHANGED_CADDY" -eq 1 ]]; then
			info "Reloading Caddy..."
			compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --force
		fi
	fi
}

wait_tls() {
	local waited=0
	info "Waiting up to ${TLS_WAIT_SECONDS}s for a TLS certificate on https://$DOMAIN ..."
	while [[ "$waited" -lt "$TLS_WAIT_SECONDS" ]]; do
		if curl -sS -o /dev/null --max-time 5 "https://$DOMAIN/" 2>/dev/null; then
			info "TLS is up."
			return 0
		fi
		sleep 3
		waited=$((waited + 3))
	done
	warn "no TLS handshake after ${TLS_WAIT_SECONDS}s. Check the DNS record, that ports 80 and 443 are open, and: (cd $DIR && docker compose logs caddy)"
	return 1
}

# ---------- output ----------

detect_proxies() {
	docker ps --filter "network=$NETWORK" --format '{{.Image}}' 2>/dev/null |
		grep -oiE 'caddy|nginx|traefik' | tr '[:upper:]' '[:lower:]' | sort -u || true
}

print_snippet() {
	local kind="$1"
	case "$kind" in
	caddy)
		cat >&2 <<EOF

Add this to your Caddyfile, then validate and reload Caddy:

$DOMAIN {
	reverse_proxy homie-frps:7000 {
		stream_close_delay 30m
	}
}
EOF
		;;
	nginx)
		cat >&2 <<EOF

nginx: proxy $DOMAIN to homie-frps:7000 with WebSocket upgrade headers
(Upgrade/Connection from \$http_upgrade), proxy_http_version 1.1,
proxy_read_timeout 1h and proxy_buffering off. Full example:
$HOMIE_RAW_BASE/relay/snippets/nginx.md
EOF
		;;
	traefik)
		cat >&2 <<EOF

Traefik: add these labels to the homie-frps service (docker-compose.override.yml):

  traefik.enable=true
  traefik.docker.network=$NETWORK
  traefik.http.routers.homie.rule=Host(\`$DOMAIN\`)
  traefik.http.routers.homie.entrypoints=websecure
  traefik.http.routers.homie.tls.certresolver=<your resolver>
  traefik.http.services.homie.loadbalancer.server.port=7000
EOF
		;;
	*) ;;
	esac
}

print_proxy_help() {
	local found kind
	found="$(detect_proxies)"
	if [[ -z "$found" ]]; then
		info ""
		info "Could not tell which proxy runs on network '$NETWORK'. Examples for the common ones:"
		found=$'caddy\nnginx\ntraefik'
	fi
	while IFS= read -r kind; do
		print_snippet "$kind"
	done <<<"$found"
	info ""
	info "frps is reachable from the proxy as homie-frps:7000 (network '$NETWORK'). No port is published."
}

print_result() {
	local code="$1"
	info ""
	info "Relay installed in $DIR (mode: $MODE)."
	if [[ "$MODE" == "behind-proxy" ]]; then
		print_proxy_help
	fi
	info ""
	info "Connect code. It contains the relay token, so keep it secret:"
	info ""
	printf '%s\n' "$code"
	info ""
	info "Next step, on the home computer:  homed relay connect -"
	info "                                   then paste the code and press Enter (this keeps it out of"
	info "                                   shell history and the process list)."
	info "To show the code again:           bash server.sh --print-code --dir $DIR"
}

# ---------- commands ----------

do_print_code() {
	local domain token
	domain="$DOMAIN"
	[[ -n "$domain" ]] || domain="$(env_get HOMIE_DOMAIN)"
	[[ -n "$domain" ]] || die "no install found in $DIR; pass --domain"
	DOMAIN="$domain"
	validate_domain
	token="$(read_token)"
	[[ -n "$token" ]] || die "no token in $DIR/token; run the installer first"
	printf '%s\n' "$(connect_code "$DOMAIN" "$token")"
}

do_uninstall() {
	local f
	[[ -d "$DIR" ]] || die "nothing to uninstall: $DIR does not exist"
	confirm "Stop the relay and delete the installed files in $DIR?" || die "aborted"
	if [[ -f "$DIR/docker-compose.yml" ]]; then
		compose down --remove-orphans || warn "docker compose down failed"
	fi
	for f in docker-compose.yml docker-compose.override.yml .env Caddyfile frps.toml offline.html token; do
		rm -f -- "${DIR:?}/$f"
	done
	rmdir "$DIR" 2>/dev/null || warn "$DIR still holds other files; left in place"
	info "Removed. Caddy's certificate volumes were kept (Let's Encrypt rate limits)."
	info "Delete them with: docker volume rm homie-relay_caddy_data homie-relay_caddy_config"
}

do_install() {
	local first=1 code
	need_domain
	require_root
	preflight
	check_dns
	detect_mode
	locate_assets
	if [[ -f "$DIR/docker-compose.yml" ]]; then
		first=0
	fi
	install_files
	if [[ "$NO_START" -eq 0 ]]; then
		start_stack "$first"
		if [[ "$MODE" == "standalone" ]]; then
			wait_tls || exit 1
		fi
	fi
	code="$(connect_code "$DOMAIN" "$(read_token)")"
	print_result "$code"
}

main() {
	trap cleanup EXIT
	parse_args "$@"
	if [[ "$PRINT_CODE" -eq 1 ]]; then
		do_print_code
		return 0
	fi
	if [[ "$UNINSTALL" -eq 1 ]]; then
		require_root
		do_uninstall
		return 0
	fi
	do_install
}

main "$@"
