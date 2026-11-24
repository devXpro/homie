# Security model

What each part of Homie can see and do, what it trusts, and what follows from that. Phase 2 state: the relay, the
hub with its admin and passkeys, person keys, plugins over stdio, the plugin installer and the first-run page.

```
ChatGPT / a Site ──https──▶ relay server (Caddy: TLS ends here) ──▶ frps ◀──wss── homed (127.0.0.1) ──stdio──▶ plugins ──LAN──▶ devices
the admin (browser) ──https──▶ the same relay ──▶ homed: /admin, /api/admin (passkeys, sessions)
a browser on the home LAN ──http──▶ homed's first-run page (while the home has no owner with a passkey)
the home box's terminal ──unix socket──▶ homed (the CLI: the owner, no passkey needed)
```

## Secrets and what they unlock

| Secret | Where it lives | Who holds it | What it unlocks |
|---|---|---|---|
| Person key (`ghk_…`) | Only its SHA-256 in `homed.db`; the key itself in the person's ChatGPT Site settings (or the macOS Keychain) | The person's Site | The assistant tools of the plugins that person may use, through the REST façade and `/mcp`. |
| Relay token | `relay-token` (0600) in the home data dir; `token` (0600) on the server; inside the connect code | The home box and the server | Registering the domain on frps, i.e. **being** the home as seen from the internet. |
| Passkeys | The admin's devices (iCloud Keychain, a phone); public keys in `homed.db` | Each person | Signing in to the admin, and a fresh assertion (step-up) for dangerous actions. |
| Session cookie `__Host-homie_session` | The browser; only its SHA-256 in `homed.db` | The signed-in browser | The admin as that person, for 12 h idle / 7 days, without step-up. |
| Setup link (`…/admin/setup#t=…`) | Shown once (CLI or first-run page); only its SHA-256 in `homed.db` | Whoever has the link | Registering a passkey for the owner (or creating the owner): the admin. Single use, 15 min (CLI) or 3 min (first-run page). |
| First-run PIN and cookie `homie_firstrun` | homed's memory; the PIN on homed's terminal or from `homed admin firstrun` | The person at the home box | The first-run page: storing the first relay code, and making a setup link. |
| The local socket `homed.sock` | The data dir (0600, own uid checked) | The OS user that runs homed | Everything: the whole admin API without passkeys, setup links, `reset-access`. |
| TV client key | `tvs.json` in the homie-tv plugin data dir | homie-tv | Controlling that TV over the LAN. |

### The relay token is as powerful as the person keys

Anyone with the relay token (or the connect code, which contains it) can log in to frps and register the domain
whenever the home box is not connected — when it sleeps, reboots or loses its network. From then on:

- every request the Sites send, **bearer person keys included**, reaches them instead of the home;
- they can serve their own pages on the home's origin (`https://<domain>`), e.g. a fake admin login (see "A
  compromised relay" below for what that can leave behind);
- the real home box cannot register the domain again until they disconnect (frps keeps the first registration).

So keep the token and the connect code as secret as the person keys: never paste them into a chat or a ticket, and
give the code only on stdin — `homed relay connect -` refuses a code on the command line, where it would land in the
shell history and the process list. **Rotate** the token (`install/server.sh --rotate-token`, then
`homed relay connect -` at home) whenever it may have leaked. After a suspected leak, also issue new person keys
(`homed key new` / `homed key revoke`), since they may have been captured.

What a token holder still cannot do: open TCP or UDP listeners on the relay (`allowPorts` only offers frps's own,
taken port; see `docs/RELAY.md`), reach the home box (it accepts no inbound connections), forge a passkey assertion,
or read anything stored at home.

## The relay server

- **TLS ends at the server.** Caddy holds the certificate, so the server (and whoever runs it) can read every request
  and response: person keys in `Authorization` headers, tool arguments and results, session cookies, admin pages and
  setup links in flight. Run the relay on a server you control, and do not put a CDN that terminates TLS in front of
  it.
- **It holds no home secrets.** No person keys (only what passes through), no passkeys, no plugins, no database. Its
  only secret is the relay token.
- **Passkeys are verified at home.** The server cannot forge an assertion, so it cannot perform a step-up action on
  its own. It can, however, serve modified pages on the origin while an admin uses it: a compromised relay server is
  a compromised admin session, and the admin trusts the server operator as much as the domain.
- **Client addresses come from Caddy.** homed sees every public request from `127.0.0.1` (frpc). For rate limits and
  the audit it takes the second-to-last `X-Forwarded-For` hop: the one Caddy appended (Caddy replaces any
  `X-Forwarded-For` a client sends), followed by the one frps appended (Caddy's own address). Values further left come
  from the client and are never used. This holds for the shipped Caddy setup (and the nginx and Traefik snippets in
  `relay/snippets`); a CDN or another proxy in front of Caddy would make every client look like that proxy.
- frps publishes no port; it is reachable only from Caddy (or your proxy) inside the docker network.

### A compromised relay

A relay server (or a relay token holder, while the home is offline) that served the origin can leave things behind
that outlive its own cleanup:

- **Year-long cached files.** The admin's hashed assets (`/admin/assets/…`) are sent `Cache-Control: immutable` for a
  year. A tampered copy served under a real asset name stays in the browser's cache, and the genuine `index.html`
  keeps loading it after the relay is clean again.
- **A service worker.** The admin registers none, but the origin allows same-origin workers (`worker-src 'self'`);
  one installed by a hostile relay keeps intercepting the origin's requests in that browser.
- **Stolen secrets.** Setup links and session cookies that passed through it, person keys sent by the Sites.

Recovery, on every device that opened the admin while the relay may have been hostile: **clear the site's data**
(Safari on iPhone: Settings → Apps → Safari → Advanced → Website Data → remove the domain; Safari on a Mac: Settings →
Privacy → Manage Website Data; Chrome: the lock icon → Site settings → Delete data). Then, on the home box:

1. `homed admin reset-access` — deletes every passkey and session (see its scope below) and prints a new setup link;
2. rotate the relay token (`install/server.sh --rotate-token`, then `homed relay connect -`);
3. issue new person keys and revoke the old ones (`homed key new`, `homed key revoke`), and update the Sites.

The admin's pages carry `Strict-Transport-Security` (a year) next to their strict CSP, so a browser that has seen the
admin never talks to the domain over plain http again; Caddy may add its own.

## The hub (`homed`)

- **Loopback only.** It listens on `127.0.0.1` (or `localhost`); other addresses are refused. The relay client forwards
  to `127.0.0.1:<port>`. A Host allowlist (the relay domain and the hub's own loopback address) answers anything else
  with 421, which is the DNS-rebinding guard. The listener reads a request's headers within 10 s and the whole
  request, body included (1 MiB at most), within 2 minutes; idle keep-alive connections close after 2 minutes. There is
  no write timeout, because the admin's event stream and long tool calls (up to 300 s) keep their answers open.
- **Person keys.** 32 random bytes, stored as SHA-256 hex, compared in constant time. Each person sees only the
  assistant tools of the plugins they may use; a hidden tool and a missing one both answer 404. 120 calls per minute
  per key. A revoked key stops working before the admin answers, and every rebuild of the key set reads the store and
  swaps under one lock, so a reload that started before a revoke cannot bring the key back.
- **Names.** Person, home and passkey names are 1–40 characters with no control characters, line breaks, quotes,
  backslashes or backticks; the home name goes into the assistants' instructions as a quoted string.
- **Audit.** Every tool call through every surface and every admin action is logged, kept 180 days (pruned at start
  and daily). Argument values of keys matching `(?i)(token|key|secret|password|pin|code)` become `"[redacted]"`.
  Schema-validation errors quote the rejected values, so the audit keeps only "invalid arguments" for them; the caller
  still gets the full message. Failed sign-ins, step-ups, registrations and passkey additions are logged with the
  client's address, as are setup links and relay codes from the first-run page.
- **A bad plugin tool cannot take the API down.** A tool whose schema the MCP server refuses is skipped with a warning
  when the plugin lists it and again when the gateway builds its tool set; the plugin's other tools keep working.
- **Local admin socket.** `<data_dir>/homed.sock`, 0600, and homed accepts only connections from its own OS user
  (peer credentials). Whoever can open it is the hub's owner: it serves the whole admin API (every tool, admin ones
  included) without a key, a cookie or step-up, makes setup links (`homed admin setup-link`), shows the first-run PIN
  (`homed admin firstrun`), resets access, and is the only way to install an older release.
- **Data dir.** Created 0700. Secret files in it are 0600, and `relay-token` is refused when group or others can read
  it. homed never changes the mode of an existing directory it did not create (a mistaken `--data-dir ~` must not lock
  up a home directory); it only tightens its platform default dir and warns about any other dir open to others.
- **One instance per data dir.** `homed run` holds an `flock` on `<data_dir>/.homed.lock` for its whole life; a second
  one is refused and never touches the first one's socket.
- **Toolchain.** Both repos pin a patched Go toolchain (`toolchain` in go.mod) and CI runs `govulncheck`; a known
  vulnerability reachable from the code fails the build.

## The admin: passkeys, sessions, step-up

- **Passkeys only.** The relying party ID is the relay domain and the only origin is `https://<domain>`; every
  ceremony needs user verification (Face ID, Touch ID) and a discoverable credential. Assertions are verified at
  home; a signature counter that goes backwards is refused.
- **The relying party is the relay domain.** A relay change to a new domain is done on the home box only (the web
  accepts a code for the current domain, a token rotation). Passkeys of the old domain can no longer sign in: they do
  not count as the owner's passkeys (the first-run page opens again) nor as the "last passkey" that may not be
  removed; `homed admin reset-access` then gives a new setup link.
- **Sessions.** `__Host-homie_session` (`Secure; HttpOnly; SameSite=Strict`), stored as a hash, 12 h idle and 7 days
  absolute. Every mutating request needs `X-Homie: 1`, and a present `Origin` must be `https://<domain>` and a
  present `Sec-Fetch-Site` `same-origin`.
- **Step-up.** Installing, updating or removing plugins, updating the hub, creating or revoking keys, adding or
  removing passkeys and changing the relay need a passkey assertion no older than 5 minutes. **Signing in and
  registering count as step-up**: right after either, a stolen session cookie is as good as a fresh assertion for 5
  minutes. A setup link is therefore as powerful as a step-up session.
- **Rate limits.** Each endpoint class has its own buckets: auth/token and registration per client address and in
  total; sign-in begins and failed finishes per client address with no shared bucket; step-up and adding a passkey per
  session. Anonymous traffic cannot spend the owner's sign-in (from another address) or a signed-in owner's step-up.
  Requests without a client address (loopback, development) have a bucket of their own.

## Setup links, the first-run page and reset-access

- **Setup links** carry a 32-byte random token in the URL fragment (never in Caddy's or frps's logs, or a Referer).
  They work once; a new one revokes the older ones. A link from `homed admin setup-link` lives 15 minutes, one from the
  first-run page 3 minutes. Whoever opens a link first becomes, or adds a passkey to, the owner.
- **The first-run page is plain HTTP on the home network.** Anyone who can watch the LAN traffic (shared Wi-Fi, a
  hostile device on it) can read the PIN, the page's cookie and the setup link as they pass, and use the link before
  you do. What limits it:
  - it runs only while no owner has a passkey, and closes 30 minutes after it opened; `homed admin firstrun` opens it
    again;
  - it listens on the home network (`0.0.0.0:8090`) only while homed has no relay yet — the step that may need another
    device; with a relay it listens on `127.0.0.1:8090`, the home box itself;
  - the PIN has 8 digits, changes every 10 minutes and after every issued link, and is printed only to a terminal
    (never to homed's log under launchd or systemd); one device gets 5 wrong PINs per 10 minutes, and 20 wrong PINs
    from anyone close the page until `homed admin firstrun` or a restart;
  - links it issues live 3 minutes; every link and relay code is logged and audited with the device's LAN address;
  - it never replaces a relay that is configured (409); that is done on the home box or with step-up in the admin.

  On a network you do not trust, skip the page: run `homed admin setup-link` in a terminal on the home box and open
  the link there.
- **`homed admin reset-access`** (socket only) deletes every passkey and every session of every person, revokes every
  unused setup link and invite, and prints a new setup link for the owner. It does **not** touch person keys (the
  Sites keep working), the relay token, plugins or the audit log: after a compromise, rotate those as well (see "A
  compromised relay").

## Plugins

Plugins are separate executables that the hub starts with a minimal environment and talks to over stdio. What that
does and does not isolate:

- **A plugin runs as the same OS user as homed. It is fully trusted.** It can read everything that user can:
  `homed.db` (key hashes, sessions, the audit log), **`relay-token`**, the admin socket, other plugins' data dirs, the
  user's files and, on a Mac, Keychain items that user can open without a prompt. Install only plugins whose authors
  you trust as much as the hub itself. Their per-plugin data dir (`HOMIE_DATA_DIR`, 0700) is for tidiness, not a
  sandbox.
- Plugins never receive the callers' keys, and they are not reachable from the network through the hub: the hub talks
  to them over stdio only. They may make their own network connections (homie-tv talks to TVs on the LAN).
- The request `_meta` `homie/caller` and `homie/via` are informational; plugins must not base security
  decisions on them.

### Installing from GitHub

- **The trust anchor is the repository the owner typed.** The installer downloads the release's archive and its
  `homie-plugin.json` over https (every redirect too) and checks the archive's SHA-256 against `checksums.txt`
  **from the same release**. That catches corruption and a bad mirror, not a compromised repository or release:
  there are no signatures. The same holds for the hub's self-update.
- **Unpacking** refuses absolute and `..` paths, links of any kind (symbolic or hard), devices and FIFOs, duplicate
  entries and anything over the size and count limits; every write goes through `os.Root`, and the stored manifest is
  written through it with `O_EXCL` (no link followed). Modes become 0755 or 0644.
- **The manifest is the author's text.** Its homepage is shown only when it is an `https` link, and next to the source
  link (always `https://github.com/<repo>`), never instead of it.
- **No downgrades from the web.** An update to a release older than the installed one — also a "latest" that points
  back, e.g. after a newer release was pulled — is refused; only the local socket can ask for it, with an explicit
  `allowDowngrade`. One install, update or removal at a time per plugin.

## The home LAN (homie-tv)

- LG webOS 3.x speaks SSAP over plain `ws://` on port 3000, so the TV client key travels **in clear** on the LAN;
  anyone who can sniff the LAN can capture it and control the TV. See `homie-tv/docs/families/lg-webos.md`.
- Discovery fetches the UPnP description only from the IP that answered the SSDP search and follows no redirects; the
  SSAP websocket dial follows no redirects either.
- Wake-on-LAN packets are broadcast on the LAN; they carry only the TV's MAC address.

## Reporting a problem

Open a GitHub issue without details for anything that could be abused, and ask for a private channel; do not post
tokens, keys or connect codes anywhere.
