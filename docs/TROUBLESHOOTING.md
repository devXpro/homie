# Troubleshooting

Start at the top: most problems show up in the first two checks.

- [First: walk the chain](#first-walk-the-chain)
- [Logs](#logs)
- ["Home is offline"](#home-is-offline)
- [The relay server and certificates](#the-relay-server-and-certificates)
- [The hub](#the-hub)
- [Sign-in, passkeys and the first-run page](#sign-in-passkeys-and-the-first-run-page)
- [macOS: Local Network permission](#macos-local-network-permission)
- [TVs](#tvs)
- [ChatGPT](#chatgpt)
- [Plugins](#plugins)

## First: walk the chain

A request goes ChatGPT → relay server → home box → plugin → device. Check it from the outside in.

**1. From anywhere** (your laptop, your phone's browser):

```sh
curl -sS https://home.example.com/healthz
```

| Answer | Meaning |
|---|---|
| `{"ok":true}` | the relay works and the home box is connected. Look further in: the plugin, the device, ChatGPT. |
| an HTML page *"Home is offline / Дом не на связи"* (status 404) | the relay works; no home box is connected. Go to step 2. |
| `502 Bad Gateway` | the proxy in front cannot reach frps. See [the relay server](#the-relay-server-and-certificates). |
| a TLS or certificate error | the certificate is not issued yet or is wrong. See [certificates](#the-relay-server-and-certificates). |
| no answer / connection refused | DNS, the server or its firewall. |

**2. On the home box:**

```sh
homed relay status
```

```
relay:      home.example.com
state:      connected
self-check: ok at 2026-11-15 18:42:07
```

| You see | Do |
|---|---|
| `homed is not running` | start it: see [Logs](#logs) for your platform, and why it stopped |
| `state: connecting`, `last error: … token …` | the token at home differs from the server's (it was rotated): on the server print the code again, at home `homed relay connect -` |
| `last error: … x509 …` | the server's certificate is not valid for the domain (not issued yet, or a proxy re-signs TLS) |
| `last error: … dial … / timeout` | the home box has no internet, or something blocks outbound 443 |
| `self-check: FAILED … another Homie hub` | a second home box is connected to the same relay with the same token; stop one |
| `self-check: FAILED` with anything else | the channel is up but `https://<domain>/healthz` does not reach this hub: check the proxy's site block for the domain |
| `relay: … (configured)` and nothing else | homed is not running; the relay is only configured |

On macOS with the installer, `homed` is not on your `PATH`; use the full path
`~/Library/Application\ Support/homie/bin/homed`. On Linux the `homed` command runs as the service user for you.

## Logs

| Where homed runs | Status | Logs |
|---|---|---|
| macOS, `install/home.sh` | `launchctl print gui/$(id -u)/com.github.devxpro.homie.homed \| grep -E 'state\|pid'` | `tail -f ~/Library/Logs/homie/homed.log` |
| macOS, Homebrew | `brew services info homie` | `tail -f "$(brew --prefix)/var/log/homie/homed.log"` |
| Linux / Raspberry Pi | `systemctl status homie` | `journalctl -u homie -f` |
| Docker | `docker ps --filter name=homie` | `docker logs -f homie` |
| A terminal | — | stderr; `HOMED_LOG_LEVEL=debug homed run` for more |
| Relay server | `docker compose -f /opt/homie-relay/docker-compose.yml ps` | `docker compose -f /opt/homie-relay/docker-compose.yml logs -f frps` (and `caddy`) |

A plugin's own log (its stderr) is in the admin under **Devices → the module → Module log**, and in
`<data dir>/logs/<plugin id>.log` on the home box.

## "Home is offline"

ChatGPT says *"The home hub is offline right now"*, and `https://<domain>/` shows the offline page. The relay is
fine; the home box is not connected. In order of likelihood:

1. **The home box sleeps.** A Mac laptop with the lid closed, or a Mac with sleep enabled, drops the connection. Keep
   it awake (System Settings → Battery/Energy → *Prevent automatic sleeping when the display is off*), or move the hub
   to an always-on box: a Raspberry Pi is ideal ([INSTALL-HOME.md](INSTALL-HOME.md)).
2. **homed is not running.** Check the service ([Logs](#logs)). After a hub update it exits with status 75 on purpose
   and the service manager starts the new binary; a homed started by hand in a terminal does not come back by itself.
3. **No internet at home**, or outbound 443 is blocked.
4. **The token was rotated** on the server: `homed relay status` shows a token error; reconnect with the new code.

## The relay server and certificates

| Symptom | Check |
|---|---|
| `server.sh` stops at the DNS check | the domain's A record must point at this server's public IP and be **DNS only** (on Cloudflare: the grey cloud). Fix it and run again. |
| No certificate in standalone mode | DNS first; ports 80 and 443 open to the internet (cloud firewall and `ufw`); `docker compose -f /opt/homie-relay/docker-compose.yml logs caddy`. Let's Encrypt needs port 80 or 443 reachable from outside. |
| `too many certificates` in the Caddy log | Let's Encrypt's rate limit after many reinstalls. Wait, and keep the `caddy_data` volume between reinstalls (`--uninstall` keeps it on purpose). |
| `x509: certificate is valid for …, not home.example.com` | another site on the same proxy catches the domain, or a CDN in front re-signs TLS. DNS only, and check the site block order. |
| `502 Bad Gateway` | frps is down or not on the proxy's network: `docker ps`, `docker network inspect <network>`; the container must be named `homie-frps`. |
| The channel drops on every proxy reload | the Caddy route lacks `stream_close_delay 30m`; nginx closes idle WebSockets: `proxy_read_timeout 1h`. |
| The channel drops after about a minute of silence | a proxy or CDN in front has a short idle timeout (nginx `proxy_read_timeout`, Cloudflare's orange cloud). |
| A long action (pairing) ends with `504` | the proxy gives up before the hub's 300 s tool timeout: raise its response timeout above 330 s. |
| frps logs `token in login doesn't match` | the home has an old token: print the code again on the server ([how](INSTALL-SERVER.md#show-the-connect-code-again)) and run `homed relay connect -` at home. |

More in [RELAY.md](RELAY.md#troubleshooting).

## The hub

| Symptom | Cause and fix |
|---|---|
| `homed is already running` | one homed per data dir. Stop the other one (the service, or a terminal) before starting a second. |
| `homed is not running` from a CLI command | `tool …` and `relay status` need the running hub; the other commands also work without it. Start the service. |
| `421 Misdirected Request` | the request's Host is neither the relay domain nor the hub's loopback address. Expected for anything that is not your domain. |
| `/admin` answers `503` | a homed built from source without the admin SPA: `make build` (it runs `make web` first). Release builds always include it. |
| The admin says the home is still starting | the store and plugins are loading; wait a few seconds. |
| `homed relay connect` refuses a code on the command line | by design: the code is a secret. Use `homed relay connect -` and paste it. |
| `listen: address already in use` | something else uses `127.0.0.1:8080` (common on a NAS). Set another port: `listen: 127.0.0.1:18080` in `homed.yaml`, or `homed run --listen 127.0.0.1:18080`. |

## Sign-in, passkeys and the first-run page

| Symptom | Cause and fix |
|---|---|
| *This passkey doesn't open this home* | the passkey was made for another domain or another install. If you changed the relay domain, passkeys are bound to the old one: `homed admin reset-access` on the home box, then register again. |
| *This home isn't set up yet* | no owner passkey exists: open the first-run page or run `homed admin setup-link` on the home box. |
| *This link has expired* / *already been used* | setup links live 15 minutes (3 minutes from the first-run page) and work once. Make a new one: `homed admin setup-link`. |
| The first-run page says it is closed | it closes 30 minutes after homed starts, after the owner registered, or after too many wrong PINs. Reopen it: `homed admin firstrun` (prints the address and a new PIN). |
| *Too many tries from this device* | five wrong PINs in ten minutes lock that device out for a while; wait, or run `homed admin firstrun` for a fresh PIN. |
| The first-run page does not open from my phone | once the relay is connected it listens only on `127.0.0.1:8090` (the home box itself). Use `homed admin setup-link` and open the link on the phone. Before that it listens on `0.0.0.0:8090`; a firewall on the box (`ufw`) may block it. |
| No PIN in the log | under a service there is no terminal, so the PIN is never logged. `homed admin firstrun` prints it. |
| Lost the only device with a passkey | on the home box: `homed admin reset-access`. It deletes every passkey and session and prints a new setup link. ChatGPT's access keys keep working. |
| Signed out unexpectedly | sessions end after 12 hours idle or 7 days in total, and on every device after a reset. |

## macOS: Local Network permission

macOS asks **once** whether homed may find devices on your local network, the first time a plugin (the TV one) talks
to the LAN. Click **Allow**.

If you missed or denied it, every LAN call fails instantly and the TV shows as *offline*:

1. System Settings → **Privacy & Security** → **Local Network** → turn **homed** on.
2. Restart it: `launchctl kickstart -k gui/$(id -u)/com.github.devxpro.homie.homed` (Homebrew:
   `brew services restart homie`).

A homed started from a terminal runs under the terminal's permission, so the prompt comes again the first time it runs
as a service; a new binary (an update) may be asked again.

## TVs

| Symptom | Cause and fix |
|---|---|
| **Find TVs** finds nothing | the TV must be **on**, with **LG Connect Apps** on (Settings → Network). The home box and the TV must be on the same network: a guest Wi-Fi or a VPN on the box hides the TV. Docker without `--network host` sees no TV. Or add it by IP with **Add a TV**. |
| **The TV does not turn on** | turn on **Mobile TV On → Turn on via Wi-Fi** (Settings → General) and **Quick Start+**. Some models' Wi-Fi sleeps in deep standby: a network cable helps most. The TV needs a fixed IP (a DHCP reservation in the router). |
| The TV shows *offline* although it is in standby | in deep standby some TVs do not even answer ARP. *Turn TV on* may still wake it. |
| **A pairing prompt appears on every command** | the TV forgot its key (a factory reset, a firmware update). Turn it on, run **Pair a TV** and press **OK**; the new key replaces the old one. |
| *Not paired* | run **Pair a TV** with the TV on, and press **OK** on the remote within two minutes. |
| Pairing never shows a prompt | newer webOS (roughly 2020+) closes the plain `ws://:3000` API that homie-tv uses; not supported yet. |
| The TV moved to a new IP | remove it and add it again, then give it a DHCP reservation. |
| Volume does nothing | sound goes to a soundbar or receiver; the command sets the TV's own speakers. |

TV setup in full: [homie-tv: LG webOS](https://github.com/devXpro/homie-tv/blob/main/docs/families/lg-webos.md).

## ChatGPT

| Symptom | Cause and fix |
|---|---|
| Voice says it did it, but nothing happens | **Always allow** was never chosen: voice cannot approve an action. Ask once in text, choose *Always allow*, start a new chat. |
| *refused this Site's key* | `HOME_API_KEY` of the Site is wrong or revoked (Settings → Access keys). Store the right key and republish with `--settings-only`. |
| ChatGPT calls a tool that no longer exists | the chat started with an old tool list: start a new chat. |
| A new device or tool does not show up in ChatGPT | republish the Site settings (`--settings-only`); ChatGPT refreshes its snapshot in about ten minutes. `--check` shows what it sees now. |
| *rate limited* | more than 120 calls a minute with one key; a loop in a conversation. Wait a minute. |
| *The home hub is offline right now* | see ["Home is offline"](#home-is-offline). |

## Plugins

| Symptom | Cause and fix |
|---|---|
| *The "…" module isn't responding* | the plugin keeps crashing (after 5 crashes in 5 minutes it is retried every 5 minutes). Open **Module log**; **Restart** after fixing the cause. |
| *No build for this computer* | the release has no archive for this OS and CPU (e.g. 32-bit ARM). Ask the author, or build it yourself and add it with `homed plugin add`. |
| *Needs a newer Homie* | the plugin's `minHubVersion` is newer than your hub: Settings → About → Check for updates. |
| *The files are damaged or tampered with* | the archive's SHA-256 does not match `checksums.txt`. Try again later; if it repeats, do not install it. |
| *The new version didn't start; the previous one is back* | the update was rolled back automatically; see the module log. |
| *GitHub isn't answering* | GitHub is down or rate-limits anonymous requests (60 an hour per IP). Try later, or give homed a token in the environment: `HOMIE_GITHUB_TOKEN`. |
| *That's an older version* | the admin never downgrades. |

Still stuck? Open an issue with the output of `homed version`, `homed relay status` and the relevant log lines — and
never paste a connect code, a token or a key.
