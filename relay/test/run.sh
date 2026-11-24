#!/usr/bin/env bash
# Docker smoke test of the Homie relay (see docs/RELAY.md, docs/TESTING.md).
#
# Brings up the standalone stack (Caddy + frps) from install/server.sh output,
# with a `tls internal` Caddyfile for the fake domain homie.test, connects a real
# frpc over wss on port 443 and checks, all through Caddy on ONE domain and ONE
# frps port (7000):
#   - normal vhost HTTP (GET and POST) reaches the "home" echo backend;
#   - frpc's wss channel (/~!frp) works on the same hostname;
#   - a Caddy reload does not drop the channel;
#   - with frpc stopped the offline page is served with status 404;
#   - the channel is TLS-verified and token-protected;
#   - even a valid token cannot open tcp proxies (allowPorts), and vhostHTTPTimeout is 330 s.
#
# Everything is named homie-test-* and removed on exit. Needs: docker (compose
# v2), go, curl, openssl. Nothing leaves this machine except image pulls.
set -euo pipefail

readonly PROJECT="homie-test"
readonly NET="homie-test-net"
readonly DOMAIN="homie.test"
readonly C_CADDY="homie-test-caddy"
readonly C_FRPS="homie-test-frps"
readonly C_ECHO="homie-test-echo"
readonly C_FRPC="homie-test-frpc"
readonly C_BAD="homie-test-frpc-bad"
readonly C_CURL="homie-test-curl"
readonly C_E2E="homie-test-frps-e2e"
readonly NET_E2E="homie-test-proxy"
readonly VOL_DATA="homie-test-caddy-data"
readonly VOL_CONFIG="homie-test-caddy-config"
readonly FRPC_IMAGE="ghcr.io/fatedier/frpc:v0.72.0"
readonly ALPINE_IMAGE="alpine:3.21"
readonly CURL_IMAGE="curlimages/curl:8.11.1"

ROOT=""
WORK=""
COMPOSE_ARGS=()
RESP_CODE=""
RESP_CT=""
RESP_BODY=""
MARKER=""

log() { printf '== %s\n' "$*"; }
pass() { printf '   ok: %s\n' "$*"; }

dump_logs() {
	local c
	for c in "$C_FRPS" "$C_FRPC" "$C_CADDY"; do
		printf '\n---- docker logs %s (tail) ----\n' "$c" >&2
		docker logs --tail 40 "$c" >&2 2>&1 || true
	done
}

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	dump_logs
	exit 1
}

remove_containers() {
	docker rm -f "$C_CURL" "$C_FRPC" "$C_BAD" "$C_ECHO" "$C_E2E" >/dev/null 2>&1 || true
}

remove_stack() {
	remove_containers
	if [[ -n "$WORK" && -f "$WORK/relay/docker-compose.yml" ]]; then
		docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
	fi
	docker rm -f "$C_CADDY" "$C_FRPS" >/dev/null 2>&1 || true
	docker network rm "$NET" "$NET_E2E" >/dev/null 2>&1 || true
	docker volume rm "$VOL_DATA" "$VOL_CONFIG" >/dev/null 2>&1 || true
}

cleanup() {
	local rc=$?
	trap - EXIT
	remove_stack
	if [[ -n "$WORK" && -d "$WORK" ]]; then
		rm -rf -- "$WORK"
	fi
	if [[ $rc -eq 0 ]]; then
		printf '\nALL OK\n'
	fi
	exit "$rc"
}

# wait_for DESC SECONDS CMD...: poll CMD every 0.5 s until it succeeds or the deadline passes.
wait_for() {
	local desc="$1" seconds="$2" deadline
	shift 2
	deadline=$((SECONDS + seconds))
	while ! "$@" >/dev/null 2>&1; do
		if [[ $SECONDS -ge $deadline ]]; then
			fail "timed out after ${seconds}s waiting for: $desc"
		fi
		sleep 0.5
	done
}

# req METHOD PATH [BODY]: one request to https://homie.test through Caddy, from inside the docker network.
req() {
	local method="$1" path="$2" body="${3:-}" out
	local args=(--cacert /ca/root.crt -sS --max-time 10 -X "$method" -w '\n__CODE__%{http_code}__CT__%{content_type}')
	if [[ -n "$body" ]]; then
		args+=(--data-binary "$body" -H 'Content-Type: text/plain')
	fi
	out="$(docker exec "$C_CURL" curl "${args[@]}" "https://$DOMAIN$path" 2>&1)" || {
		RESP_CODE="curl-failed"
		RESP_CT=""
		RESP_BODY="$out"
		return 0
	}
	RESP_CODE="$(printf '%s' "$out" | sed -n 's/.*__CODE__\([0-9]*\)__CT__.*/\1/p')"
	RESP_CT="$(printf '%s' "$out" | sed -n 's/.*__CT__//p')"
	RESP_BODY="${out%$'\n'__CODE__*}"
}

resp_is_echo() { req GET "/ping"; [[ "$RESP_CODE" == "200" && "$RESP_BODY" == *'"marker":"'"$MARKER"'"'* ]]; }
resp_is_offline() { req GET "/ping"; [[ "$RESP_CODE" == "404" ]]; }

count_logs() { # count_logs CONTAINER PATTERN
	docker logs "$1" 2>&1 | grep -c -- "$2" || true
}

caddy_cmd() { docker exec "$C_CADDY" caddy "$@"; }

# write_caddyfile SERIAL [strict] [nodelay]: the generated Caddyfile plus `tls internal` and a header that makes
# every config different, rewritten in place (single-file bind mount).
#   strict  adds a global `grace_period 3s`, so Caddy really tears down the old config on reload;
#   nodelay removes `stream_close_delay` (the control case).
write_caddyfile() {
	local serial="$1" strict="no" nodelay="no" opt
	shift
	for opt in "$@"; do
		case "$opt" in
		strict) strict="yes" ;;
		nodelay) nodelay="yes" ;;
		*) fail "write_caddyfile: unknown option $opt" ;;
		esac
	done
	awk -v d="$DOMAIN" -v nodelay="$nodelay" -v strict="$strict" -v serial="$serial" '
		BEGIN { if (strict == "yes") { print "{\n\tgrace_period 3s\n}\n" } }
		$0 ~ "^" d " \\{" { print; print "\ttls internal"; print "\theader X-Test-Reload \"" serial "\""; next }
		nodelay == "yes" && /stream_close_delay/ { next }
		{ print }
	' "$WORK/relay/Caddyfile.generated" >"$WORK/Caddyfile.next"
	cat -- "$WORK/Caddyfile.next" >"$WORK/relay/Caddyfile"
}

reload_caddy() {
	caddy_cmd validate --config /etc/caddy/Caddyfile >/dev/null 2>&1 || fail "Caddyfile does not validate"
	caddy_cmd reload --config /etc/caddy/Caddyfile --force >/dev/null 2>&1 || fail "caddy reload failed"
}

frpc_toml() { # frpc_toml TOKEN_FILE_IN_CONTAINER CA_FILE_IN_CONTAINER
	cat <<EOF
serverAddr = "$DOMAIN"
serverPort = 443
loginFailExit = false
transport.protocol = "wss"
transport.tls.trustedCaFile = "$2"
auth.method = "token"
auth.tokenSource.type = "file"
auth.tokenSource.file.path = "$1"

[[proxies]]
name = "homed"
type = "http"
customDomains = ["$DOMAIN"]
localIP = "$C_ECHO"
localPort = 8080
EOF
}

main() {
	local arch token frps_ports i code payload decoded new_token logins started e2e_env e2e_args

	ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
	WORK="$(mktemp -d "${TMPDIR:-/tmp}/homie-test-relay.XXXXXX")"
	WORK="$(cd "$WORK" && pwd -P)"
	COMPOSE_ARGS=(-p "$PROJECT" -f "$WORK/relay/docker-compose.yml" -f "$WORK/relay/docker-compose.test.yml")
	trap cleanup EXIT
	trap 'exit 130' INT TERM

	command -v docker >/dev/null || fail "docker is required"
	command -v go >/dev/null || fail "go is required"
	command -v openssl >/dev/null || fail "openssl is required"
	remove_stack # leftovers of a crashed earlier run

	log "building the echo backend"
	arch="$(docker version --format '{{.Server.Arch}}')"
	mkdir -p "$WORK/bin"
	GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go -C "$ROOT" build -o "$WORK/bin/echo" ./relay/test/echo
	MARKER="echo-$RANDOM$RANDOM"

	log "installer: files, token, connect code (no docker start)"
	bash "$ROOT/install/server.sh" --mode standalone --dir "$WORK/relay" --domain "$DOMAIN" \
		--skip-dns-check --yes --no-root-check --no-start >"$WORK/install.out" 2>&1 ||
		fail "server.sh failed: $(cat "$WORK/install.out")"
	[[ -s "$WORK/relay/Caddyfile" && -s "$WORK/relay/frps.toml" && -s "$WORK/relay/offline.html" &&
		-s "$WORK/relay/docker-compose.yml" ]] || fail "server.sh did not write the expected files"
	token="$(tr -d '[:space:]' <"$WORK/relay/token")"
	[[ "$token" =~ ^[0-9a-f]{64}$ ]] || fail "token is not 64 hex characters"
	pass "files written, token is 64 hex"

	log "installer: connect code, idempotency, token rotation, behind-proxy files"
	code="$(bash "$ROOT/install/server.sh" --print-code --dir "$WORK/relay")"
	[[ "$code" == ghr1.* && "$code" =~ ^ghr1\.[A-Za-z0-9_-]+$ ]] || fail "connect code has the wrong shape: $code"
	payload="${code#ghr1.}"
	payload="$(printf '%s' "$payload" | tr '_-' '/+')"
	while [[ $((${#payload} % 4)) -ne 0 ]]; do payload+="="; done
	decoded="$(printf '%s' "$payload" | base64 -d 2>/dev/null || printf '%s' "$payload" | base64 -D)"
	[[ "$decoded" == "{\"v\":1,\"domain\":\"$DOMAIN\",\"token\":\"$token\"}" ]] || fail "connect code decodes to: $decoded"
	pass "connect code decodes to {v,domain,token}"
	bash "$ROOT/install/server.sh" --mode standalone --dir "$WORK/relay" --domain "$DOMAIN" \
		--skip-dns-check --yes --no-root-check --no-start >/dev/null 2>&1 || fail "second server.sh run failed"
	[[ "$(tr -d '[:space:]' <"$WORK/relay/token")" == "$token" ]] || fail "a rerun changed the token"
	pass "a rerun keeps the token"
	cp -- "$WORK/relay/token" "$WORK/token.before"
	bash "$ROOT/install/server.sh" --mode standalone --dir "$WORK/relay" --domain "$DOMAIN" --rotate-token \
		--skip-dns-check --yes --no-root-check --no-start >/dev/null 2>&1 || fail "--rotate-token run failed"
	new_token="$(tr -d '[:space:]' <"$WORK/relay/token")"
	[[ "$new_token" =~ ^[0-9a-f]{64}$ && "$new_token" != "$token" ]] || fail "--rotate-token did not produce a new token"
	token="$new_token"
	# GNU stat first: on Linux `stat -f` is the file system status and prints something else.
	[[ "$(stat -c %a "$WORK/relay/token" 2>/dev/null || stat -f %Lp "$WORK/relay/token")" == "600" ]] || fail "token is not 0600"
	pass "--rotate-token writes a new 0600 token"
	bash "$ROOT/install/server.sh" --mode behind-proxy --network homie-test-proxy --dir "$WORK/behind" \
		--domain "$DOMAIN" --skip-dns-check --yes --no-root-check --no-start >/dev/null 2>&1 || fail "behind-proxy run failed"
	[[ -s "$WORK/behind/frps.toml" && ! -e "$WORK/behind/Caddyfile" ]] || fail "behind-proxy wrote the wrong files"
	docker compose --project-directory "$WORK/behind" -f "$WORK/behind/docker-compose.yml" config >"$WORK/behind.config" 2>&1 ||
		fail "behind-proxy compose file does not validate: $(cat "$WORK/behind.config")"
	grep -q 'name: homie-test-proxy' "$WORK/behind.config" || fail "behind-proxy network name is not taken from --network"
	pass "behind-proxy mode writes frps-only files that validate"

	log "installer end to end, behind-proxy mode: up, rotate (frps restarts), uninstall"
	docker network create "$NET_E2E" >/dev/null
	mkdir -p "$WORK/e2e"
	printf 'services:\n  frps:\n    container_name: %s\n' "$C_E2E" >"$WORK/e2e/docker-compose.test.yml"
	e2e_env=(COMPOSE_FILE=docker-compose.yml:docker-compose.test.yml COMPOSE_PROJECT_NAME=homie-test-e2e)
	e2e_args=(--mode behind-proxy --network "$NET_E2E" --dir "$WORK/e2e" --domain "$DOMAIN" --skip-dns-check --yes --no-root-check)
	env "${e2e_env[@]}" bash "$ROOT/install/server.sh" "${e2e_args[@]}" >"$WORK/e2e.out" 2>&1 ||
		fail "behind-proxy install failed: $(cat "$WORK/e2e.out")"
	[[ "$(docker inspect -f '{{.State.Running}}' "$C_E2E")" == "true" ]] || fail "frps of the e2e install is not running"
	[[ -n "$(docker inspect -f '{{index .NetworkSettings.Networks "'"$NET_E2E"'"}}' "$C_E2E")" ]] ||
		fail "frps did not join the proxy network"
	[[ -z "$(docker port "$C_E2E")" ]] || fail "frps of the e2e install publishes ports"
	grep -q '^ghr1\.' "$WORK/e2e.out" || fail "the installer did not print a connect code"
	pass "frps runs on the proxy network without published ports and the code is printed"
	started="$(docker inspect -f '{{.State.StartedAt}}' "$C_E2E")"
	sleep 1
	env "${e2e_env[@]}" bash "$ROOT/install/server.sh" "${e2e_args[@]}" >/dev/null 2>&1 || fail "rerun failed"
	[[ "$(docker inspect -f '{{.State.StartedAt}}' "$C_E2E")" == "$started" ]] || fail "a rerun without changes restarted frps"
	env "${e2e_env[@]}" bash "$ROOT/install/server.sh" "${e2e_args[@]}" --rotate-token >/dev/null 2>&1 || fail "rotate failed"
	[[ "$(docker inspect -f '{{.State.StartedAt}}' "$C_E2E")" != "$started" ]] || fail "--rotate-token did not restart frps"
	pass "a rerun leaves frps alone, --rotate-token restarts it"
	env "${e2e_env[@]}" bash "$ROOT/install/server.sh" --uninstall --dir "$WORK/e2e" --yes --no-root-check >/dev/null 2>&1 ||
		fail "uninstall failed"
	[[ -z "$(docker ps -aq -f name="$C_E2E")" ]] || fail "uninstall left the container"
	[[ ! -e "$WORK/e2e/token" && ! -e "$WORK/e2e/docker-compose.yml" ]] || fail "uninstall left files"
	pass "uninstall removes the container and the files"

	log "stack: Caddy (tls internal) + frps from docker-compose.yml"
	cp -- "$WORK/relay/Caddyfile" "$WORK/relay/Caddyfile.generated"
	write_caddyfile 0
	cat >"$WORK/relay/docker-compose.test.yml" <<EOF
services:
  caddy:
    container_name: $C_CADDY
    ports: !reset []
    networks:
      default:
        aliases: [$DOMAIN]
  frps:
    container_name: $C_FRPS
    networks:
      default:
        aliases: [homie-frps]
networks:
  default:
    name: $NET
volumes:
  caddy_data:
    name: $VOL_DATA
  caddy_config:
    name: $VOL_CONFIG
EOF
	docker compose "${COMPOSE_ARGS[@]}" up -d >"$WORK/up.out" 2>&1 || fail "compose up failed: $(cat "$WORK/up.out")"
	frps_ports="$(docker port "$C_FRPS" 2>/dev/null || true)"
	[[ -z "$frps_ports" ]] || fail "frps publishes ports: $frps_ports"
	pass "frps publishes no host port"

	log "Caddy root CA"
	wait_for "Caddy's internal root CA" 60 docker cp "$C_CADDY:/data/caddy/pki/authorities/local/root.crt" "$WORK/root.crt"
	chmod 644 "$WORK/root.crt"
	openssl x509 -in "$WORK/root.crt" -noout >/dev/null 2>&1 || fail "root.crt is not a certificate"

	log "echo backend, probe container and frpc (wss to $DOMAIN:443)"
	chmod +x "$WORK/bin/echo"
	docker run -d --name "$C_ECHO" --network "$NET" -v "$WORK/bin/echo:/echo:ro" "$ALPINE_IMAGE" \
		/echo -addr :8080 -marker "$MARKER" >/dev/null
	docker run -d --name "$C_CURL" --network "$NET" -v "$WORK/root.crt:/ca/root.crt:ro" \
		--entrypoint sleep "$CURL_IMAGE" 3600 >/dev/null
	mkdir -p "$WORK/frpc"
	frpc_toml /etc/frp/token /ca/root.crt >"$WORK/frpc/frpc.toml"
	docker run -d --name "$C_FRPC" --network "$NET" \
		-v "$WORK/frpc/frpc.toml:/etc/frp/frpc.toml:ro" \
		-v "$WORK/relay/token:/etc/frp/token:ro" \
		-v "$WORK/root.crt:/ca/root.crt:ro" \
		"$FRPC_IMAGE" -c /etc/frp/frpc.toml >/dev/null
	wait_for "frpc to start the proxy" 60 bash -c "docker logs $C_FRPC 2>&1 | grep -q 'start proxy success'"
	wait_for "the first request through Caddy -> frps -> frpc -> echo" 30 resp_is_echo
	pass "frpc logged in over wss on 443 and the proxy is up"

	log "vhost traffic and the wss channel share one domain and one frps port"
	req GET "/anything?x=1"
	[[ "$RESP_CODE" == "200" ]] || fail "GET /anything -> $RESP_CODE: $RESP_BODY"
	[[ "$RESP_BODY" == *'"method":"GET"'* && "$RESP_BODY" == *'"path":"/anything?x=1"'* &&
		"$RESP_BODY" == *"\"host\":\"$DOMAIN\""* && "$RESP_BODY" == *"\"marker\":\"$MARKER\""* ]] ||
		fail "GET reached the backend but the echo is wrong: $RESP_BODY"
	pass "GET /anything?x=1 reached the echo backend with the original Host"
	req POST "/api/v1/tools/x" '{"a":"b"}'
	[[ "$RESP_CODE" == "200" && "$RESP_BODY" == *'"method":"POST"'* && "$RESP_BODY" == *'"body":"{\"a\":\"b\"}"'* ]] ||
		fail "POST body did not arrive intact: $RESP_CODE $RESP_BODY"
	pass "POST with a body arrived intact"
	[[ "$(count_logs "$C_FRPC" 'login to server success')" == "1" ]] || fail "expected exactly one frpc login"

	log "Caddy reload (stream_close_delay 30m) keeps the channel"
	docker exec -d "$C_CURL" sh -c "i=0; while [ \$i -lt 60 ]; do curl --cacert /ca/root.crt -sS -o /dev/null --max-time 5 -w '%{http_code}\\n' https://$DOMAIN/ping >> /tmp/loop.out 2>&1; i=\$((i+1)); sleep 0.25; done; echo done >> /tmp/loop.out"
	sleep 2 # let the background loop get going before the first reload
	for i in 1 2 3; do
		write_caddyfile "$i"
		reload_caddy
		sleep 1
	done
	wait_for "the request loop to finish" 60 docker exec "$C_CURL" grep -q '^done$' /tmp/loop.out
	if docker exec "$C_CURL" grep -v -e '^200$' -e '^done$' /tmp/loop.out >/dev/null; then
		fail "some requests failed during the reloads: $(docker exec "$C_CURL" sort /tmp/loop.out | uniq -c | tr '\n' ' ')"
	fi
	[[ "$(count_logs "$C_FRPC" 'login to server success')" == "1" ]] ||
		fail "frpc logged in again: the reload dropped the channel"
	[[ "$(count_logs "$C_FRPS" 'client login info')" == "1" ]] ||
		fail "frps saw more than one client login: the reload dropped the channel"
	pass "3 forced reloads with changed config: all requests 200, still one login on both sides"

	log "Caddy reload with a short grace_period: stream_close_delay keeps the channel, its absence drops it"
	write_caddyfile 4 strict
	reload_caddy
	sleep 8 # well past the 3 s grace period: the old config is really gone by now
	resp_is_echo || fail "traffic broke after a reload with grace_period 3s: $RESP_CODE $RESP_BODY"
	[[ "$(count_logs "$C_FRPC" 'login to server success')" == "1" ]] ||
		fail "frpc logged in again: stream_close_delay did not protect the channel"
	pass "grace_period 3s + stream_close_delay 30m: the channel survives"
	# A stream belongs to the config it was opened under, so the control needs a channel that was opened under a
	# config without the delay: switch to it, reconnect frpc, then reload once more.
	write_caddyfile 5 strict nodelay
	reload_caddy
	docker restart "$C_FRPC" >/dev/null
	wait_for "frpc to reconnect under the control config" 60 resp_is_echo
	logins="$(count_logs "$C_FRPC" 'login to server success')"
	write_caddyfile 6 strict nodelay
	reload_caddy
	wait_for "frpc to log in again (the control reload must drop the channel)" 60 \
		bash -c "[ \"\$(docker logs $C_FRPC 2>&1 | grep -c 'login to server success')\" -gt $logins ]"
	pass "control: grace_period 3s without stream_close_delay drops the channel, so the tests above are not vacuous"
	write_caddyfile 7
	reload_caddy
	wait_for "the proxy to work again" 60 resp_is_echo

	log "frpc down -> offline page"
	docker stop "$C_FRPC" >/dev/null
	wait_for "404 from frps once frpc is gone" 30 resp_is_offline
	req GET "/ping"
	[[ "$RESP_CODE" == "404" ]] || fail "offline status is $RESP_CODE, want 404"
	[[ "$RESP_CT" == text/html* ]] || fail "offline content type is '$RESP_CT'"
	[[ "$RESP_BODY" == *"Home is offline"* && "$RESP_BODY" == *"Дом не на связи"* ]] ||
		fail "offline body is not our page: $RESP_BODY"
	pass "404 text/html with the bilingual offline page"
	req POST "/api/v1/tools/x" '{"a":"b"}'
	[[ "$RESP_CODE" == "404" && "$RESP_BODY" == *"Home is offline"* ]] || fail "POST while offline -> $RESP_CODE"
	pass "POST while offline gets the same page"
	docker start "$C_FRPC" >/dev/null
	wait_for "traffic after frpc is back" 60 resp_is_echo
	pass "frpc reconnects by itself and traffic flows again"

	log "wrong token is rejected"
	frpc_toml /etc/frp/badtoken /ca/root.crt >"$WORK/frpc/bad-token.toml"
	printf '%064d' 0 >"$WORK/frpc/badtoken"
	docker run -d --name "$C_BAD" --network "$NET" \
		-v "$WORK/frpc/bad-token.toml:/etc/frp/frpc.toml:ro" \
		-v "$WORK/frpc/badtoken:/etc/frp/badtoken:ro" \
		-v "$WORK/root.crt:/ca/root.crt:ro" \
		"$FRPC_IMAGE" -c /etc/frp/frpc.toml >/dev/null
	wait_for "frps to refuse the wrong token" 30 \
		bash -c "docker logs $C_BAD 2>&1 | grep -qi \"token in login doesn't match\""
	pass "a frpc with a wrong token cannot log in"
	docker rm -f "$C_BAD" >/dev/null

	log "a token holder cannot open tcp listeners on the relay (allowPorts)"
	{
		frpc_toml /etc/frp/token /ca/root.crt | sed '/^\[\[proxies\]\]/,$d'
		printf '\n[[proxies]]\nname = "rogue"\ntype = "tcp"\nlocalIP = "%s"\nlocalPort = 8080\nremotePort = 6000\n' "$C_ECHO"
		printf '\n[[proxies]]\nname = "rogue-any"\ntype = "tcp"\nlocalIP = "%s"\nlocalPort = 8080\nremotePort = 0\n' "$C_ECHO"
	} >"$WORK/frpc/tcp.toml"
	docker run -d --name "$C_BAD" --network "$NET" \
		-v "$WORK/frpc/tcp.toml:/etc/frp/frpc.toml:ro" \
		-v "$WORK/relay/token:/etc/frp/token:ro" \
		-v "$WORK/root.crt:/ca/root.crt:ro" \
		"$FRPC_IMAGE" -c /etc/frp/frpc.toml >/dev/null
	wait_for "frps to refuse the fixed tcp port" 30 \
		bash -c "docker logs $C_BAD 2>&1 | grep -q '\\[rogue\\] start error: port not allowed'"
	wait_for "frps to refuse a random tcp port" 30 \
		bash -c "docker logs $C_BAD 2>&1 | grep -q '\\[rogue-any\\] start error'"
	if docker logs "$C_BAD" 2>&1 | grep -q '\[rogue[a-z-]*\] start proxy success'; then
		fail "frps accepted a tcp proxy"
	fi
	pass "tcp proxies are refused, with a fixed port and with a random one"
	docker rm -f "$C_BAD" >/dev/null
	grep -q '^vhostHTTPTimeout = 330$' "$WORK/relay/frps.toml" || fail "frps.toml lacks vhostHTTPTimeout = 330"
	pass "frps waits 330 s for response headers (above the 300 s tool timeout)"

	log "an untrusted server certificate is rejected by frpc"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 1 \
		-subj "/CN=homie-test-other-ca" -keyout "$WORK/frpc/other.key" -out "$WORK/frpc/other-ca.crt" >/dev/null 2>&1
	frpc_toml /etc/frp/token /ca/other-ca.crt >"$WORK/frpc/bad-ca.toml"
	docker run -d --name "$C_BAD" --network "$NET" \
		-v "$WORK/frpc/bad-ca.toml:/etc/frp/frpc.toml:ro" \
		-v "$WORK/relay/token:/etc/frp/token:ro" \
		-v "$WORK/frpc/other-ca.crt:/ca/other-ca.crt:ro" \
		"$FRPC_IMAGE" -c /etc/frp/frpc.toml >/dev/null
	wait_for "frpc to refuse the untrusted certificate" 30 \
		bash -c "docker logs $C_BAD 2>&1 | grep -qi 'certificate signed by unknown authority\\|x509'"
	pass "frpc verifies the wss server certificate against trustedCaFile"
}

main "$@"
