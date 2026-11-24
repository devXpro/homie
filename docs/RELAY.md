# Relay (server side)

The relay is the only thing Homie needs on a public server. It is a dumb pipe: Caddy terminates TLS, frps hands the
traffic to the home box over a connection the home box opened itself. The server keeps no keys, no admin, no plugins and
no database. Its only secret is the relay token.

```
ChatGPT / a Site ──https──▶ Caddy :443 ──▶ homie-frps :7000 ◀── wss (path /~!frp) ── home box (frpc inside homed)
                             one domain      one port, muxed
```

## How it works

- **One domain, one name.** `home.example.com` serves both the channel from home (`GET /~!frp`, a WebSocket upgrade) and
  the public traffic. The home box dials out over `wss` on port 443, so it needs no open port, no router setup and no
  static IP.
- **One frps port.** `bindPort` and `vhostHTTPPort` are both 7000, so frps looks at the first bytes of each connection:
  `GET /~!frp` is the frpc channel (it must carry the token), any other HTTP request is routed by its `Host` header to the
  connected client's `http` proxy (`customDomains = [<domain>]`). Verified in frp v0.72.0 `server/service.go` (mux with a
  websocket prefix listener and an HTTP listener on the same port).
- **Token.** `auth.method = "token"`, the token is read from a file (`auth.tokenSource.file.path`, whitespace trimmed).
  Nobody without it can register a proxy. Anyone *with* it can register the domain while the home box is offline and
  answer the public traffic in its place, so treat it like the person keys (see `docs/SECURITY.md`) and rotate it when
  it may have leaked.
- **Only the http proxy.** `allowPorts = [{ single = 7000 }]` gives `tcp`/`udp` proxies only frps's own port, which
  is taken, so even a token holder cannot open listeners on the relay (an empty `allowPorts` would mean "any port").
  The `http` proxy homed registers is routed by `Host` on `vhostHTTPPort` and needs no port. The smoke test checks it.
- **Slow tools.** `vhostHTTPTimeout = 330`: frps waits up to 330 s for the home's response headers (frp's default is
  60 s; hub tool calls may take up to 300 s, e.g. pairing).
- **Home offline.** With no client connected, frps answers vhost requests with `custom404Page`: `relay/offline.html`,
  status 404, `text/html`, bilingual (EN/RU), dark, self-contained. The ChatGPT Site recognizes a non-JSON 404 as "home is
  offline" (PLAN §5.7).
- **No frps port is published.** frps is reachable only inside the docker network, as `homie-frps:7000`.
- **Trust.** The server terminates TLS, so it can read request contents. It cannot make the home box do admin actions:
  those need passkey assertions that are verified at home.

## Files

| File | Purpose |
|---|---|
| `relay/frps.toml` | frps config (keys confirmed against frp v0.72.0) |
| `relay/offline.html` | the "home is offline" page |
| `relay/docker-compose.yml` | standalone: `caddy` (2.11.4-alpine, 80/443/443 udp) + `frps` |
| `relay/docker-compose.behind-proxy.yml` | `frps` only, joined to your proxy's docker network |
| `relay/Caddyfile.tmpl` | the Caddy site block, domain filled in by the installer |
| `relay/snippets/{caddy,nginx,traefik}.md` | what to add to an existing proxy |
| `install/server.sh` | the installer |
| `relay/test/run.sh` | the docker smoke test, see `docs/TESTING.md` |

## Install

DNS first: an A record for your domain pointing at the server's public IP, DNS only (no CDN proxying: it breaks the
long-lived WebSocket and the certificate issuance).

```sh
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh | sudo bash -s -- --domain home.example.com
```

The script needs root, Docker with the compose v2 plugin, `curl` and `ss` or `netstat`. It asks for the domain if it is
missing. It checks that the domain's A record includes one of this server's addresses (local interfaces plus
`api.ipify.org`), and explains and asks when it does not (`--yes` aborts instead; `--skip-dns-check` is for tests).

Flags: `--domain D`, `--mode auto|standalone|behind-proxy`, `--network NAME`, `--dir PATH` (default
`/opt/homie-relay`), `--rotate-token`, `--print-code`, `--uninstall`, `--yes`, `--skip-dns-check`. Two flags exist for
tests only: `--no-root-check` and `--no-start` (write the files, do not touch docker).

The files come from the repo checkout next to the script, or, for `curl | bash`, from
`https://raw.githubusercontent.com/devXpro/homie/<ref>/relay/` (`HOMIE_REF`, default `main`; `HOMIE_RAW_BASE`
overrides the whole base URL).

### Mode 1: standalone

Chosen by `auto` when nothing listens on 80 and 443. The compose file runs Caddy (owns 80, 443 and 443/udp, certificates
from Let's Encrypt) and frps. The script waits up to 120 s for a TLS handshake on `https://<domain>`, then prints the
connect code.

### Mode 2: behind an existing proxy

Chosen by `auto` when something already listens on 80 or 443. The compose file runs only frps and attaches it to the
external docker network `--network` (default `proxy`), as the container `homie-frps`. Nothing is published. The script
prints the snippet for the proxy it detects on that network (Caddy, nginx or Traefik) and the connect code. You then
add the snippet and reload the proxy. Full versions: `relay/snippets/`.

Caddy:

```
home.example.com {
	reverse_proxy homie-frps:7000 {
		stream_close_delay 30m
	}
}
```

nginx needs the WebSocket upgrade headers, `proxy_read_timeout 1h` and `proxy_buffering off`.

If the proxy's Caddyfile is a single-file bind mount, write it in place (`cat new > Caddyfile`) and back it up first;
`sed -i` or `mv` replace the inode and the container keeps seeing the old file.

### What `stream_close_delay` does

A Caddy WebSocket belongs to the config it was opened under. When that config is unloaded by a reload, Caddy closes its
streams, immediately by default, or after `stream_close_delay`. The home box reconnects on its own, so without the
directive a reload costs a few seconds of "home is offline". With `stream_close_delay 30m` the channel keeps working
through the reload, and is closed (and re-opened by frpc) only 30 minutes after that config was unloaded. The smoke test
proves both sides, including with a global `grace_period 3s`.

## Connect code

`ghr1.` + base64url (no padding) of `{"v":1,"domain":"<domain>","token":"<64 hex chars>"}`. It contains the token, so it
is a secret. The installer prints it once; show it again with:

```sh
sudo bash install/server.sh --print-code --dir /opt/homie-relay
```

On the home box run `homed relay connect -` and paste the code (it is read from stdin, so it stays out of the shell
history and the process list). `homed relay connect <code>` is refused for that reason.

## Home client settings (for the relay client in `homed`)

Verified in frp v0.72.0 (`client/connector.go`, `pkg/config/v1`):

```toml
serverAddr = "home.example.com"
serverPort = 443
loginFailExit = false                      # retry forever
transport.protocol = "wss"
transport.tls.useSystemCa = true           # REQUIRED for verification: see below
# transport.tls.trustedCaFile = "/path/ca.pem"   # test harness only (system roots are added when useSystemCa is also true)
auth.method = "token"
auth.tokenSource.type = "file"
auth.tokenSource.file.path = "<data dir>/relay-token"

[[proxies]]
name = "homed"
type = "http"
customDomains = ["home.example.com"]
localIP = "127.0.0.1"
localPort = 8080
```

With `wss`, frpc always uses TLS (`transport.tls.enable` is ignored), but **with neither `trustedCaFile` nor
`useSystemCa` it skips certificate verification** (`NewClientTLSConfigWithSystemCA`, `pkg/transport/tls.go`). The home
side must set `useSystemCa = true`, or the channel is open to a man in the middle. `disableCustomTLSFirstByte` defaults
to true and is irrelevant for wss.

## Operations

- **Rotate the token** (all home boxes must connect again with the new code):
  `sudo bash install/server.sh --rotate-token --dir /opt/homie-relay`. The script restarts frps and prints the new
  code; on the home box run `homed relay connect -`, paste it and restart homed. Rerunning the installer without the
  flag keeps the token and every file as it is. Rotate whenever the code or the token may have been seen by someone
  else: a pasted chat, a screenshot, a shared terminal, a backup of the home data dir, a person who no longer should
  have it.
- **Update** the stack: rerun the installer (the images are pinned in the compose files; bump them in the repo).
- **Uninstall:** `sudo bash install/server.sh --uninstall --dir /opt/homie-relay`. It stops and removes the containers
  and deletes the installed files. The Caddy volumes (certificates) are kept, because Let's Encrypt limits reissuance;
  remove them with `docker volume rm homie-relay_caddy_data homie-relay_caddy_config`. In behind-proxy mode also
  remove the site block from your proxy.
- **Logs:** `cd /opt/homie-relay && docker compose logs -f frps` (and `caddy`).
- **Files:** `/opt/homie-relay/{docker-compose.yml,.env,frps.toml,offline.html,token,Caddyfile}`; `token` is 0600.

## Troubleshooting

| Symptom | Check |
|---|---|
| The installer stops at the DNS check | The A record must point at this server and be DNS-only. Fix it, or use `--skip-dns-check` if you know better. |
| No TLS certificate in standalone mode | DNS, ports 80 and 443 reachable from the internet, `docker compose logs caddy`. |
| `https://<domain>/` shows the offline page | The relay works; no home box is connected. Check the relay status of the home box. |
| Browser shows a 502 | frps is down or not on the proxy's network: `docker ps`, `docker network inspect <network>`. |
| frpc logs `token in login doesn't match` | The token at home differs from the server's. Print the code again and reconnect. |
| frpc logs `x509: ...` | The certificate is not valid for the domain yet (Let's Encrypt not issued), or a proxy in between re-signs TLS. |
| The channel drops on every proxy reload | The route lacks `stream_close_delay` (Caddy), or the proxy closes idle WebSockets: raise its timeouts. |
| The channel drops after about a minute of silence | A proxy or CDN in front has a short idle timeout (nginx `proxy_read_timeout`, Cloudflare orange cloud). |
| A long tool call (pairing) ends with a 504 from the proxy | The proxy in front gives up before the 300 s tool timeout: raise its response timeout (nginx `proxy_read_timeout`) above 330 s. |
| The wrong site answers on 443 | Another vhost on the same Caddy/nginx catches the domain first; check the site block order. |
