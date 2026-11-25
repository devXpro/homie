# Troubleshooting

Start at the top: most problems show up in the first two checks.

- [First: walk the chain](#first-walk-the-chain)
- [Logs](#logs)
- [The tunnel: states and errors](#the-tunnel-states-and-errors)
- [The assistant says it cannot reach the home](#the-assistant-says-it-cannot-reach-the-home)
- [Quick tunnels](#quick-tunnels)
- [Cloudflare and your domain](#cloudflare-and-your-domain)
- [The hub](#the-hub)
- [Sign-in, passkeys and the first-run page](#sign-in-passkeys-and-the-first-run-page)
- [Guests and terms](#guests-and-terms)
- [macOS: Local Network permission](#macos-local-network-permission)
- [TVs](#tvs)
- [ChatGPT and Claude](#chatgpt-and-claude)
- [Plugins](#plugins)

## First: walk the chain

A request goes ChatGPT or Claude → Cloudflare → `cloudflared` on the home box → `homed` → plugin → device. Check it
from the outside in.

**1. From anywhere** (your laptop, your phone's browser):

```sh
curl -sS -i https://home.example.com/healthz
```

| Answer | Meaning |
|---|---|
| `200 {"status":"ok"}` with an `X-Homie-Instance` header | the tunnel works and the hub answers. Look further in: the plugin, the device, the assistant's connector. |
| a Cloudflare page with `530` (error `1033`), or `502` | Cloudflare is fine; no `cloudflared` is connected for this host, or it cannot reach `homed`. Go to step 2. |
| `524` | Cloudflare waited about two minutes for the hub. See [524](#cloudflare-and-your-domain). |
| `421 {"error":"misdirected"}` | the hub answered, but not for this host name: see [the hub](#the-hub). |
| a DNS error (`Could not resolve host`) | the DNS record is missing: run `homed cloudflare connect home.example.com` again. |
| a TLS or certificate error | Cloudflare's certificate for the domain is not active yet (a new zone can take a few minutes), or the zone's SSL/TLS mode is wrong. |

**2. On the home box:**

```sh
homed cloudflare status
```

```
mode:  named
state: connected
url:   https://home.example.com
mcp:   https://home.example.com/mcp
since: 2026-11-15 18:42:07
```

| You see | Do |
|---|---|
| `cannot reach the homed on … Is homed running?` | start it: see [Logs](#logs) for your platform, and why it stopped |
| `mode: off`, `state: stopped` | no tunnel is connected: `homed cloudflare connect home.example.com` (or `homed cloudflare quick`) |
| `state: starting` | `cloudflared` is starting; wait a few seconds |
| `state: probing`, `error: the public URL does not answer yet: …` | `cloudflared` runs, but `https://<host>/healthz` does not come back to this hub through Cloudflare yet. The error says why (a DNS record not there yet, a timeout, a certificate). Wait a minute, then fix the cause below. |
| `state: failing` | `cloudflared` exited and is restarting with a backoff of 1 to 60 seconds; read `<data dir>/logs/cloudflared.log` |
| `state: connected` | the tunnel works. The problem is further in. |

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

`cloudflared`'s own output is in `<data dir>/logs/cloudflared.log` (rotated at 5 MB, three files). A plugin's own log
(its stderr) is in the admin under **Devices → the module → Module log**, and in `<data dir>/logs/<plugin id>.log` on
the home box.

## The tunnel: states and errors

| Where | Symptom | Cause and fix |
|---|---|---|
| `cloudflared was not found; the tunnel cannot start` in the log | | `cloudflared` is not on the service's `PATH`: the installer installs it next to `homed` (rerun it), or set `tunnel.cloudflared` in `homed.yaml` to its path. Homebrew users: `brew install cloudflared`. |
| `homed cloudflare connect`: *the domain was not connected (step dns): … already has a DNS record* | the host name already points somewhere | Replace it only if you mean to: `homed cloudflare connect home.example.com --overwrite-dns`. Or choose another host name. |
| `homed cloudflare connect`: *tunnel: bad hostname* | the argument is not a full lower-case host name | use `home.example.com`, not `home` or `HTTPS://…` |
| The login link says the zone is missing, or you cannot pick one | the domain is not on your Cloudflare account | add the domain to Cloudflare and switch its nameservers (see [INSTALL-HOME.md](INSTALL-HOME.md#pick-how-it-reaches-the-internet)), then connect again |
| *Waiting for the authorization…* never ends | the Cloudflare login link was not opened, or `cloudflared` gave up after about four minutes | run the command again for a fresh link; on a headless box open the printed link on another computer |
| `state: probing` for a long time on a new domain | the DNS record or the certificate is not live yet | wait a few minutes; `homed cloudflare status` shows the last error |
| `state: failing`, the log says *failed to connect* / *no more connections* | the box has no internet, or outbound traffic to Cloudflare (port 7844, UDP and TCP) is blocked | check the network; cloudflared falls back from QUIC to HTTP/2 by itself |
| A tunnel exists in Cloudflare but the hub reports nothing | the credentials in `tunnel/` do not match, e.g. the tunnel was deleted in the dashboard | `homed cloudflare connect home.example.com` makes a new one (it reuses an existing one of the same name) |

## The assistant says it cannot reach the home

ChatGPT or Claude says the connector failed, timed out, or "cannot reach the home". In order of likelihood:

1. **The home box sleeps.** A Mac laptop with the lid closed, or a Mac with sleep enabled, drops the tunnel. Keep it
   awake (System Settings → Battery/Energy → *Prevent automatic sleeping when the display is off*), or move the hub to
   an always-on box: a Raspberry Pi is ideal ([INSTALL-HOME.md](INSTALL-HOME.md)).
2. **homed is not running.** Check the service ([Logs](#logs)). After a hub update it exits with status 75 on purpose
   and the service manager starts the new binary; a homed started by hand in a terminal does not come back by itself.
3. **No internet at home**, or outbound traffic to Cloudflare is blocked.
4. **The address changed.** On a quick tunnel the address changes at every restart of the hub: remove the old connector
   and add the new address ([Quick tunnels](#quick-tunnels)).
5. **The connection was revoked or the guest's term ended**: People shows the person and their connections.

## Quick tunnels

A quick tunnel (`homed cloudflare quick`, or "Try without a domain") is Cloudflare's testing service. Expect:

| Symptom | Cause and fix |
|---|---|
| The address and every connector stopped working after a restart | the `trycloudflare.com` address is random and **changes whenever `cloudflared` starts again**, including every restart of the hub and every update. Open the first-run page on the home box (`homed admin firstrun`): it shows the new address and a **Reconnect** button (a new setup link for the same owner). Then remove the old connector in ChatGPT and Claude and add the new address. |
| *This passkey doesn't open this home* after a restart | a passkey belongs to one address: the old one is dead. Reconnect as above. |
| The old address answers `421` | by design: the hub answers only for its current address. |
| *Invites need the home's own domain* (`409 quick_mode`) | invites are off in quick mode: a guest's passkey would die at the next restart. Move to your own domain. |
| The admin's live updates are slow | no Server-Sent Events pass through a quick tunnel, so the admin polls (a wait of up to 20 seconds). |
| `429` or errors under load | Cloudflare allows about 200 requests in flight per quick tunnel, with no uptime promise. It is for trying. |
| The assistant does not accept the `trycloudflare.com` address | quick tunnels are for trying, and whether an assistant accepts a connector on a `trycloudflare.com` host is not guaranteed; move to your own domain. |
| Everyone shares one rate limit | the client address may not be available on quick tunnels; the hub then falls back to the connection's address (one log line at warn level). |

Move to a domain at any time: `homed cloudflare connect home.example.com`, then `homed admin setup-link` (the old
passkeys do not carry over: they belong to the old address).

## Cloudflare and your domain

| Symptom | Cause and fix |
|---|---|
| **524** from the domain | Cloudflare waits about 125 seconds for an answer. The hub keeps assistant tool calls under 90 seconds and the admin runs long actions (pairing) in the background, so a 524 means the hub or a plugin hung: look at the hub's log and the module log. |
| **530** with error **1033** | `cloudflared` is not connected for this host: [walk the chain](#first-walk-the-chain). |
| **502** | `cloudflared` is connected but the hub does not answer on its port: homed is down or listening on another port than the tunnel's config. Restart the hub. |
| A different site answers on `home.example.com` | an old DNS record or a wildcard wins: look in the zone's DNS records; `--overwrite-dns` replaces the record of the host. |
| The zone proxy settings | the DNS record for the tunnel must be **proxied** (orange cloud, a CNAME to `<id>.cfargotunnel.com`); `connect` creates it like that. |
| Cloudflare Access or a WAF rule blocks the assistants | do not put Cloudflare Access in front of this host: the assistants cannot pass it. Skip challenges for `/mcp` and `/oauth/*` if you add firewall rules. |

## The hub

| Symptom | Cause and fix |
|---|---|
| `homed is already running` | one homed per data dir. Stop the other one (the service, or a terminal) before starting a second. |
| `cannot reach the homed on … Is homed running?` from a CLI command | most commands need the running hub; only `run`, `version` and `cloudflare connect` work without it. Start the service. |
| `421 Misdirected Request` | the request's Host is neither the current public host nor the hub's loopback address. Expected for anything that is not your domain, and for a quick tunnel's previous address. |
| `homed.yaml`: *relay is ignored* in the log | an old v0.1 file; the first v0.2 start migrates it ([upgrading](INSTALL-HOME.md#upgrading-from-v01)). |
| `/` answers `503` with a hint | a homed built from source without the admin SPA: `make build` (it runs `make web` first). Release builds always include it. |
| The admin says the home is still starting | the store and plugins are loading; wait a few seconds. |
| `listen: address already in use` | something else uses `127.0.0.1:8080` (common on a NAS). Set another port: `listen: 127.0.0.1:18080` in `homed.yaml`, or `homed run --listen 127.0.0.1:18080`. The hub listens on loopback only; other addresses are refused. |
| An unknown `tunnel.mode` in `homed.yaml` | the hub refuses to start; the modes are `named`, `quick` and `off`. |

## Sign-in, passkeys and the first-run page

| Symptom | Cause and fix |
|---|---|
| *This passkey doesn't open this home* | the passkey was made for another host or another install. If you changed the domain (or a quick tunnel got a new address), passkeys are bound to the old one: `homed admin reset-access` on the home box, then register again. |
| *This home isn't set up yet* | no owner passkey exists for this address: open the first-run page or run `homed admin setup-link` on the home box. |
| *This link has expired* / *already been used* | setup links live 15 minutes (3 minutes from the first-run page) and work once; invites live 24 hours. Make a new one: `homed admin setup-link`, or invite again. |
| *Your access has ended* | a guest's term is over: ask the owner to **Extend** it ([USING.md](USING.md#change-extend-revoke)). |
| The first-run page says it is closed | it closes 30 minutes after homed starts, after the owner registered, or after too many wrong PINs. Reopen it: `homed admin firstrun` (prints the address and a new PIN). |
| *Too many tries from this device* | five wrong PINs in ten minutes lock that device out for a while; wait, or run `homed admin firstrun` for a fresh PIN. |
| The first-run page does not open from my phone | once a tunnel is configured the page listens only on `127.0.0.1:8090` (the home box itself). Use `homed admin setup-link` and open the link on the phone. Before that it listens on `0.0.0.0:8090`; a firewall on the box (`ufw`) may block it. |
| No PIN in the log | under a service there is no terminal, so the PIN is never logged. `homed admin firstrun` prints it. |
| *Access was reset on the home computer* on the Home screen | someone ran `homed admin reset-access` on the home box (the notice names the program that asked). If it was not you, see [SECURITY.md](SECURITY.md#the-admin-socket-on-a-desktop-macos). |
| Lost the only device with a passkey | on the home box: `homed admin reset-access`. It deletes every passkey and session and prints a new setup link. |
| Signed out unexpectedly | sessions end after 12 hours idle or 7 days in total, on every device after a reset, and when a guest's term ends. |

## Guests and terms

| Symptom | Cause and fix |
|---|---|
| A guest's assistant stopped working | their term ended, or their connection was disconnected. **People** → the person: **Extend** (or change access), or let them connect again. |
| A guest sees no tools | they were invited with no devices, or the plugin they may use is turned off: check the person's *Devices*. |
| A guest cannot see a device I just added | they have *Only selected* devices: change their access, or invite with *All devices*. |
| *Extend* asks for Face ID | widening access (more devices, a later end, no end) needs a fresh passkey. |
| A co-owner cannot change another owner | by design: only the owner who set the home up can change or remove an owner. |

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

## ChatGPT and Claude

| Symptom | Cause and fix |
|---|---|
| Voice says it did it, but nothing happens | **Always allow** was never chosen: voice cannot approve an action. Ask once in text, choose *Always allow*, start a new chat. |
| The consent page does not open, or says the request is not valid | the assistant's request was too old (open the connector again), or the connector's address is not the home's current address. |
| The connector says it needs to sign in again | the connection was revoked, or a guest's term ended. |
| The assistant calls a tool that no longer exists | the chat started with an old tool list: start a new chat. |
| A new device or tool does not show up | start a new chat; if it still does not appear, disconnect the connector in the assistant's settings and add it again. |
| *rate limited* | more than 120 calls a minute on one connection; a loop in a conversation. Wait a minute. |
| Many requests at once are refused | at most 8 requests per person are handled at the same time; wait a moment. |

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

Still stuck? Open an issue with the output of `homed version`, `homed cloudflare status` and the relevant log lines —
and never paste a setup link, a token or a key.
