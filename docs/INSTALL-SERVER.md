# Install the relay server

The relay is the only public part of Homie: Caddy for TLS and frps to hand the traffic to your home box. It keeps
no keys, no admin, no plugins and no database — just a domain, a certificate and one secret token. Your home box dials
**out** to it, so the home needs no open port, no router setup and no static IP.

```
ChatGPT ──https──▶ Caddy :443 ──▶ homie-frps :7000 ◀══ wss :443 (opened by the home box) ══ homed
                  one domain      one port, not published
```

Time: about five minutes, most of it waiting for DNS.

- [What you need](#what-you-need)
- [1. Point a domain at the server](#1-point-a-domain-at-the-server)
- [2. Install Docker](#2-install-docker)
- [3. Run the installer](#3-run-the-installer)
- [Standalone or behind your proxy](#standalone-or-behind-your-proxy)
- [The connect code](#the-connect-code)
- [Firewall](#firewall)
- [Operations](#operations): rotate the token, update, uninstall, logs
- [Installer options](#installer-options)

## What you need

- **A Linux server with a public IPv4 address** and Docker: any small VPS works (1 vCPU and 512 MB are plenty; frps
  and Caddy are tiny). amd64 and arm64 are both fine.
- **Root** or `sudo`, and `curl`, plus `ss` or `netstat` (present on every common distribution).
- **A domain or subdomain you control**, e.g. `home.example.com`. One name serves everything.
- Ports **80 and 443** reachable from the internet — or an existing reverse proxy (Caddy, nginx, Traefik) in Docker
  that already owns them.

## 1. Point a domain at the server

Create an **A record** for your domain pointing at the server's public IPv4 address. If your DNS is on Cloudflare,
set it to **DNS only** (the grey cloud): a proxying CDN breaks the long-lived WebSocket and terminates TLS where you
cannot see it.

```
home.example.com.   300   IN   A   203.0.113.10
```

Check it from the server (the installer checks too):

```sh
dig +short A home.example.com
curl -fsS https://api.ipify.org; echo
```

Both must print the same address.

## 2. Install Docker

Skip if `docker compose version` already prints v2 or later. Otherwise, Docker's official script installs the engine
and the compose plugin on Ubuntu, Debian, Fedora, CentOS and Raspberry Pi OS:

```sh
curl -fsSL https://get.docker.com | sudo sh
docker compose version
```

## 3. Run the installer

```sh
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh \
  | sudo bash -s -- --domain home.example.com
```

It is safe to pipe: everything runs inside one function called on the last line, so a cut-off download runs
nothing. Prefer to read it first?

```sh
curl -fsSLo server.sh https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh
less server.sh
sudo bash server.sh --domain home.example.com
```

What it does:

1. **Preflight:** root, Docker, `docker compose` v2, `curl`, `ss` or `netstat`.
2. **DNS check:** the domain's A records must include one of this server's addresses. On a mismatch it explains and
   asks whether to continue (with `--yes` it stops instead).
3. **Mode:** *standalone* if nothing listens on 80 and 443, otherwise *behind-proxy* (see below).
4. **Files** in `/opt/homie-relay`: the compose file, `frps.toml`, the offline page, `.env`, the Caddyfile
   (standalone) and `token` — 64 random hex characters, mode `0600`, kept across reruns.
5. `docker compose up -d`.
6. **Standalone:** waits up to two minutes for Let's Encrypt and a TLS handshake on `https://<domain>`.
   **Behind-proxy:** prints the snippet for the proxy it finds on the network.
7. Prints the **connect code** and the next step for the home box.

Rerunning it is safe: unchanged files stay as they are, the token is kept, and only what changed is restarted.

Check it:

```sh
curl -sS https://home.example.com/healthz
```

Until a home box connects you get the **offline page** (HTML, status 404, *"Home is offline / Дом не на связи"*):
that means the relay works.

## Standalone or behind your proxy

### Standalone (the default on a fresh server)

The compose file runs two containers: `homie-caddy` (owns 80, 443 and 443/udp; certificates from Let's Encrypt)
and `homie-frps` (no published port). Nothing else to do.

### Behind an existing proxy

When something already listens on 80 or 443, the installer runs only `homie-frps` and attaches it to your proxy's
Docker network (`--network`, default `proxy`). Your proxy keeps TLS; you add one site that forwards the domain to
`homie-frps:7000`. Force the mode with `--mode behind-proxy`:

```sh
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh \
  | sudo bash -s -- --domain home.example.com --mode behind-proxy --network proxy
```

The network must exist (`docker network ls`) and your proxy must be on it.

<details>
<summary><b>Caddy</b></summary>

```
home.example.com {
	reverse_proxy homie-frps:7000 {
		stream_close_delay 30m
	}
}
```

`stream_close_delay 30m` is required: without it Caddy closes the home's WebSocket on every config reload. Then:

```sh
docker exec <caddy-container> caddy validate --config /etc/caddy/Caddyfile
docker exec <caddy-container> caddy reload --config /etc/caddy/Caddyfile
```

If the Caddyfile is a single-file bind mount, edit it in place (`cat new > Caddyfile`); `sed -i`, `mv` and "atomic
save" editors replace the file and the container keeps seeing the old one. Full notes:
[relay/snippets/caddy.md](../relay/snippets/caddy.md).

</details>

<details>
<summary><b>nginx</b></summary>

In the `http {}` context:

```nginx
map $http_upgrade $connection_upgrade {
	default upgrade;
	''      close;
}
```

The server block (TLS as you usually do it):

```nginx
server {
	listen 443 ssl;
	http2 on;
	server_name home.example.com;

	# ssl_certificate / ssl_certificate_key ...

	resolver 127.0.0.11 valid=10s;                     # Docker's DNS: nginx starts even when frps is down
	set $homie_upstream http://homie-frps:7000;

	client_max_body_size 50m;

	location / {
		proxy_pass $homie_upstream;
		proxy_http_version 1.1;
		proxy_set_header Host $host;
		proxy_set_header X-Real-IP $remote_addr;
		proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_set_header X-Forwarded-Proto $scheme;
		proxy_set_header Upgrade $http_upgrade;
		proxy_set_header Connection $connection_upgrade;
		proxy_read_timeout 1h;
		proxy_send_timeout 1h;
		proxy_buffering off;
	}
}
```

`proxy_read_timeout` must stay above 330 s, or long actions such as TV pairing end in a 504. Full notes:
[relay/snippets/nginx.md](../relay/snippets/nginx.md).

</details>

<details>
<summary><b>Traefik</b></summary>

Put labels on the frps service in `/opt/homie-relay/docker-compose.override.yml` (the installer leaves that file
alone), then rerun the installer or `docker compose up -d` in that directory:

```yaml
services:
  frps:
    labels:
      - traefik.enable=true
      - traefik.docker.network=proxy
      - traefik.http.routers.homie.rule=Host(`home.example.com`)
      - traefik.http.routers.homie.entrypoints=websecure
      - traefik.http.routers.homie.tls.certresolver=letsencrypt
      - traefik.http.services.homie.loadbalancer.server.port=7000
```

Adjust `proxy`, `websecure` and `letsencrypt` to your setup, and keep rate-limit or buffering middlewares off this
router: the home's channel is one long request. Full notes: [relay/snippets/traefik.md](../relay/snippets/traefik.md).

</details>

<details>
<summary><b>A proxy that is not in Docker</b></summary>

Create a network for frps, install behind it, and publish frps on the loopback only with an override file:

```sh
sudo docker network create homie
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh \
  | sudo bash -s -- --domain home.example.com --mode behind-proxy --network homie
sudo tee /opt/homie-relay/docker-compose.override.yml >/dev/null <<'EOF'
services:
  frps:
    ports:
      - "127.0.0.1:7000:7000"
EOF
sudo docker compose -f /opt/homie-relay/docker-compose.yml -f /opt/homie-relay/docker-compose.override.yml up -d
```

Then point your proxy at `http://127.0.0.1:7000` with the same settings as the nginx block above.

</details>

## The connect code

At the end the installer prints a line that starts with `ghr1.`: the **connect code**. It carries the domain and the
relay token, so it is a secret — as powerful as access to your home while the home box is offline. Do not paste it
into a chat, a ticket or a screenshot.

On the home box, give it to the hub through stdin (never as an argument, which would land in the shell history and
the process list):

```sh
homed relay connect -
```

Paste the code, press Enter. Or paste it into the first-run page in the browser. Next: [INSTALL-HOME.md](INSTALL-HOME.md).

### Show the connect code again

```sh
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh | sudo bash -s -- --print-code
```

(`--dir` if you installed elsewhere.)

## Firewall

| Mode | Open to the internet | Never open |
|---|---|---|
| Standalone | `80/tcp` (certificate challenges and the redirect to HTTPS), `443/tcp`, `443/udp` (HTTP/3) | `7000` — frps is not published at all |
| Behind a proxy | nothing new: your proxy's 80/443 | `7000` |

With `ufw`:

```sh
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 443/udp
```

Cloud providers often have a second firewall (a security group) in their console; open the same ports there. Docker
publishes ports through its own iptables rules, so `ufw` alone may not reflect what is open: check from outside with
`curl -I http://home.example.com`. The home box needs nothing inbound — only outbound 443.

## Operations

All commands below take `--dir` if you did not install into `/opt/homie-relay`. Running the installer from the
saved file works the same as piping it: `sudo bash server.sh …`.

### Rotate the token

Do it whenever the connect code or the token may have been seen: a pasted chat, a screenshot, a shared terminal, a
backup of the home data dir, someone who should no longer have it.

```sh
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh | sudo bash -s -- --rotate-token
```

frps restarts with the new token and the new connect code is printed. The home box disconnects at once; give it the
new code (`homed relay connect -`, or **Settings → Server → Update the server code** in the admin — that one needs the
home to be connected, so use the CLI right after a rotation). After a suspected leak, also revoke and reissue the
ChatGPT access keys: requests may have been captured.

### Update

Rerun the installer. It keeps the token, rewrites only the files that changed (pinned image versions included) and
restarts only what needs it.

```sh
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh \
  | sudo bash -s -- --domain home.example.com
```

### Uninstall

```sh
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh | sudo bash -s -- --uninstall
```

It stops and removes the containers and deletes the installed files. The Caddy certificate volumes are kept, because
Let's Encrypt limits reissuance; remove them with
`sudo docker volume rm homie-relay_caddy_data homie-relay_caddy_config`. Behind a proxy, also remove the site
block from your proxy.

### Logs and files

```sh
sudo docker compose -f /opt/homie-relay/docker-compose.yml ps
sudo docker compose -f /opt/homie-relay/docker-compose.yml logs -f frps
sudo docker compose -f /opt/homie-relay/docker-compose.yml logs -f caddy      # standalone
```

`/opt/homie-relay/` holds `docker-compose.yml`, `.env`, `frps.toml`, `offline.html`, `token` (0600) and, standalone,
`Caddyfile`.

## Installer options

| Option | Meaning |
|---|---|
| `--domain D` | the relay's domain (asked for when missing) |
| `--mode M` | `auto` (default), `standalone` (own Caddy on 80/443) or `behind-proxy` (frps only) |
| `--network NAME` | behind-proxy: the proxy's Docker network (default `proxy`) |
| `--dir PATH` | install directory (default `/opt/homie-relay`) |
| `--rotate-token` | generate a new token; home boxes must connect again |
| `--print-code` | print the connect code of an existing install and exit |
| `--uninstall` | stop the containers and remove the installed files |
| `--yes` | ask nothing (a failed DNS check then stops the install) |
| `--skip-dns-check` | skip the DNS check (tests) |

Environment: `HOMIE_REF` picks the git ref the relay files are downloaded from (default `main`).

How the relay works inside — the single frps port, `stream_close_delay`, the token, the home client's TLS settings:
[RELAY.md](RELAY.md). What the server can and cannot see: [SECURITY.md](SECURITY.md#the-relay-server). Problems:
[TROUBLESHOOTING.md](TROUBLESHOOTING.md#the-relay-server-and-certificates).
