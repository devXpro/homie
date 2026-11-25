# Security model

What each part of Homie can see and do, what it trusts, and what follows from that. This is the model of v0.2: the
hub with the sign-in and access control of [MCP Gatekeeper](https://github.com/devXpro/mcp-gatekeeper), a Cloudflare
Tunnel to the internet, guests with terms, plugins over stdio, the plugin installer and the first-run page. The
gatekeeper's own review notes are in its [docs/security](https://github.com/devXpro/mcp-gatekeeper/tree/main/docs/security).

```
ChatGPT / Claude ──https──▶ Cloudflare (TLS ends here) ◀══ outbound tunnel ══ cloudflared ──▶ homed (127.0.0.1) ──stdio──▶ plugins ──LAN──▶ devices
the admin (browser) ──https──▶ the same edge ──▶ homed: /, /api/home, /api/account (passkeys, sessions)
a browser on the home LAN ──http──▶ homed's first-run page (only while the home has no tunnel, then on the home box itself)
the home box's terminal ──unix socket──▶ homed (the CLI: the owner, no passkey needed)
```

## Secrets and what they unlock

| Secret | Where it lives | Who holds it | What it unlocks |
|---|---|---|---|
| OAuth access token (about one hour) and refresh token (30 days) | Only their SHA-256 in `homie.db`; the tokens themselves in the person's ChatGPT or Claude | The person's assistant | The tools the person's permissions allow, through `/mcp`, until the term ends or the connection is revoked. |
| Passkeys | The person's devices (iCloud Keychain, a phone); public keys in `homie.db` | Each person | Signing in to the admin, approving a connector on the consent page, and a fresh assertion (step-up) for dangerous actions. |
| Session cookie `__Host-homie_session` | The browser; only its SHA-256 in `homie.db` | The signed-in browser | The admin as that person, for 12 h idle / 7 days, without step-up. |
| Setup link (`…/setup#t=…`) | Shown once (CLI or first-run page); only its SHA-256 in `homie.db` | Whoever has the link | Registering the owner's passkey. Single use, 15 min (CLI) or 3 min (first-run page). |
| Invite link (`…/join#t=…`) | Shown once in the admin; only its SHA-256 in `homie.db` | The person it was made for | Creating that person's passkey, with the devices and term the owner chose. Single use, 24 h. |
| First-run PIN and cookie `homie_firstrun` | homed's memory; the PIN on homed's terminal or from `homed admin firstrun` | The person at the home box | The first-run page: connecting Cloudflare and making a setup link. |
| Tunnel credentials (`tunnel/credentials.json`) | The data dir (`0600`) | The home box | Running the named tunnel, i.e. **being** the home as seen from your domain. |
| Cloudflare account | Cloudflare | You | The domain's DNS, the tunnel, and everything Cloudflare can see. Not stored by Homie: the login link is used once and the account certificate is deleted. |
| The local socket `homed.sock` | The data dir (`0600`, own uid checked) | The OS user that runs homed | Everything: the admin commands without passkeys, setup links, `reset-access`. |
| TV client key | `tvs.json` in the homie-tv plugin data dir | homie-tv | Controlling that TV over the LAN. |

## The trust model: Cloudflare in the middle

Cloudflare terminates TLS for your domain, so it **can read every request and response**: bearer tokens, tool
arguments and results, session cookies, the admin's pages and setup links in flight. This is the price of reaching a
home with no open port and no server of yours. What follows:

- **It cannot sign in as you.** A passkey assertion is verified at home and bound to the host name, so Cloudflare
  cannot forge one and cannot, on its own, perform a step-up action.
- **It can still do damage while a session is open**: steal a live session cookie or an unused setup link, serve
  modified admin pages, or capture and replay an access token. Whoever controls your Cloudflare account (or the DNS of
  the domain) controls what answers on the host. Protect that account with two-factor authentication and treat it as
  part of the home's security. Homie v0.2 has no other way onto the internet: if you do not accept this trade, do not
  expose the hub (see the [FAQ](FAQ.md#what-does-cloudflare-see)).
- **The tunnel credentials only run the tunnel.** They do not open the Cloudflare account. If they leak, delete the
  tunnel in the Cloudflare dashboard and connect again (`homed cloudflare connect <host>` makes a new one).
- **Client addresses come from Cloudflare.** `cloudflared` connects to the hub on `127.0.0.1`, so the hub trusts the
  `CF-Connecting-IP` header **only from loopback peers** and listens on loopback only. A local process of the same
  user could still spoof it; that process is trusted anyway (below).
- **A quick tunnel gives Cloudflare the same view** and has no account behind it. Whether it sets `CF-Connecting-IP`
  is not documented; if it does not, the hub falls back to the connection's address, which makes the rate limits
  shared (acceptable for "trying") and logs it once at warn level.

### Quick mode

A **quick tunnel** (`homed cloudflare quick`, or "Try without a domain") is Cloudflare's service for testing. Homie
treats it as exactly that:

- **Owner only.** Invites are off (`409 quick_mode`): a guest's passkey would die at the next restart.
- **The address changes at every start of cloudflared**, so at every restart of the hub. `trycloudflare.com` is on the
  Public Suffix List, so the passkey's relying party is the full random host. The previous address's passkeys, sessions
  and connectors stop counting; the hub answers `421` on the old Host and reopens the first-run page **on the home
  box** for a new setup link for the same owner ("a passkey per address, always from the home box").
- **Cloudflare's limits:** up to 200 requests in flight, no Server-Sent Events (the admin polls instead), no uptime
  guarantee, for testing and development.
- The UI labels it «для пробы» / "for trying". Moving to your own domain is `homed cloudflare connect <host>`.

## Guests, terms and co-owners

People are owners (the superadmin and co-owners) or members. Members are what an invite creates.

- **An invite** is one link, valid 24 hours, burned on first use; the guest then has their **own passkey**. The owner
  picks **which devices** (all, or the ticked ones) and **how long** (forever, 1 day, 3 days, a week, a month, or until
  a date; at most one year). The ids are stored with the invite and copied to the person when it is redeemed, in the
  same transaction.
- **A term is enforced everywhere**, with the same refusal as a disabled person:

  | Place | After the term |
  |---|---|
  | the admin session | refused and deleted; signing in says "your access has ended" |
  | the consent page | refused; nothing is written |
  | code exchange and refresh | `invalid_grant` |
  | an access token | ends no later than the term |
  | every `/mcp` call | `401 invalid_token`, the same bytes as a disabled person |

  A term never deletes the connection: when the owner extends it («Продлить»), the same connectors work again.
- **Permissions are enforced by Homie.** The gatekeeper stores `all` or `plugin:<id>` and validates them against the
  installed plugins; the gateway filters `tools/list` and refuses calls outside them, so a guest never sees another
  device's tools. A person with no permissions sees nothing.
- **Revoking.** Per connection (the assistant's connection stops at once), per person now («Закрыть доступ сейчас»:
  the term is set to now, history stays), or for good (remove: passkeys, sessions, grants and tokens are deleted).
  Widening access (a later end, no end, more devices) needs a fresh passkey; narrowing never does.
- **Known gap.** A token issued before an "end access now" is refused by the gate at once, but it counts against the
  address's failure limit until it expires (at most an hour).
- **Co-owners.** Only the **superadmin** can invite another owner, and only into the same home. A co-owner has every
  device and no term, invites and manages members and guests, revokes their connections, reads the audit, changes the
  settings and runs the devices' tools from the admin. Changing or removing an owner (or the superadmin), or resetting
  access, is the superadmin's alone: a co-owner gets `403`. Nobody can remove the last superadmin.
- **Running code and changing the system is the superadmin's alone.** Installing, updating or removing a plugin (a
  plugin is a program homed runs with its own rights), updating the hub and reconnecting the tunnel answer a co-owner
  `403 forbidden`, even with a fresh passkey: a co-owner's stolen phone or passkey must never run new code on the home
  box. The full list is in [ADMIN-API.md](ADMIN-API.md#4-endpoints-apihome) (level *superadmin*). The homed CLI on the
  home box (the admin socket) is the local operator and is not limited by this.
- **A removed plugin's permission goes with it.** Removing a plugin takes `plugin:<id>` from every person (audited as
  `account.member.update`, reason `plugin.remove`) and revokes every open invite that gives it (`account.invite.revoke`),
  so a plugin installed later under the same id is visible to nobody until it is granted again.
- **Quick mode revokes the open invites.** Switching to a quick tunnel revokes every open member and owner invite
  (audited as `account.invite.revoke`, reason `tunnel.quick`): an invite link carries no host, and quick mode is the
  owner's alone.
- **The consent page shows only what the viewer may use.** When a guest connects an assistant, the consent page lists
  only the tools of the devices they were given, never the rest of the home.
- **What a member sees.** Their own devices, connections and activity, and nothing of anyone else's.

## The hub (`homed`)

- **Loopback only.** The public listener is `127.0.0.1:<port>`; other addresses are refused. `cloudflared` forwards
  to it. A Host allowlist (the current public host and the hub's own loopback address) answers anything else with
  `421`, which is also the DNS-rebinding guard. Every answer carries `X-Homie-Instance` for the tunnel self-check. The
  listener reads headers within 10 s and the whole request, body included (1 MiB at most), within 2 minutes.
- **One door.** Exactly `/mcp`, behind the gatekeeper's `MiddlewareFor("home")` (`/mcp/<anything>` is 404), with at
  most 8 requests in flight per person and 120 calls per minute per connection. A token made for another resource is
  refused.
- **Names.** Person, home and passkey names are 1–40 characters with no control characters, line breaks, quotes,
  backslashes or backticks; the home name goes into the assistants' instructions as a quoted string.
- **Activity and audit.** Every tool call through every path lands in the **activity** log, with its arguments (values
  of keys matching `(?i)(token|key|secret|password|pin|code)` become `"[redacted]"`), duration and result. Admin
  actions (`plugin.install`, `settings.update`, `tunnel.connect`, `firstrun.link`, …), sign-ins, invites and revokes are
  in the gatekeeper's **audit**. Schema-validation errors quote the rejected values, so the log keeps only "invalid
  arguments" for them.
- **A bad plugin tool cannot take the API down.** A tool whose schema the MCP server refuses is skipped with a
  warning; the plugin's other tools keep working.
- **Data dir.** Created `0700`. Secret files in it are `0600`, and so are the SQLite file and its `-wal`/`-shm`.
  homed never changes the mode of an existing directory it did not create (a mistaken `--data-dir ~` must not lock up
  a home directory); it only tightens its platform default dir and warns about any other dir open to others.
- **One instance per data dir.** `homed run` holds an `flock` on `<data_dir>/.homed.lock` for its whole life.
- **Toolchain.** Both repos pin a patched Go toolchain and CI runs `govulncheck`; a known vulnerability reachable from
  the code fails the build.

## `cloudflared`, the child process

- **Where it comes from.** The installer puts it next to `homed`: Homebrew's formula on a Mac that has Homebrew, else
  a pinned release whose SHA-256 is pinned in `install/home.sh`. A mismatch installs nothing. In Docker it is copied
  from a pinned image (amd64, arm64) or a pinned binary (arm/v7).
- **How it runs.** As a child of `homed`, as the same OS user, with a private empty `HOME` inside `tunnel/` (so a
  `~/.cloudflared/config.yml` of the user can never change or block it), a minimal environment (the `PATH` of its own
  directory) and **no secret on its command line**: named mode reads `tunnel/credentials.json`, quick mode needs none.
  It is restarted with a backoff of 1 to 60 seconds when it exits.
- **Connecting a domain.** The Cloudflare login link is used once; the account certificate it yields is deleted when the
  tunnel and the DNS record exist, so only the tunnel's own credentials stay. The DNS step refuses to replace an
  existing record unless you pass `--overwrite-dns` (or tick the confirmation on the first-run page).
- **On Linux** it runs inside the same systemd sandbox as `homed` (`ProtectSystem=strict`, writes only under
  `/var/lib/homie`); the unit's hardening was not loosened for it.
- Its output goes to `logs/cloudflared.log` (`0600`, rotated).

## The admin: passkeys, sessions, step-up

- **Passkeys only.** The relying party ID is the hub's public host, **not** its parent domain: a parent domain would
  let any other site under it ask for Homie's passkeys. Every ceremony needs user verification (Face ID, Touch ID) and
  a discoverable credential. A signature counter that goes backwards is refused.
- **A new host means new passkeys.** The relying party is fixed to the host the passkey was made on. Moving to
  another domain (or a quick tunnel getting a new address) leaves the old passkeys unusable: they do not count as the
  owner's, the first-run page opens again, and `homed admin reset-access` gives a new setup link.
- **Sessions.** `__Host-homie_session` (`Secure; HttpOnly; SameSite=Strict`), stored as a hash, 12 h idle and 7 days
  absolute. Every mutating request needs `X-Homie: 1`, and a present `Origin` must be the public origin and a present
  `Sec-Fetch-Site` `same-origin`.
- **Step-up.** Installing, updating or removing plugins, updating the hub, reconnecting the tunnel (all four the
  superadmin's alone, see [Guests, terms and co-owners](#guests-terms-and-co-owners)) and widening a
  person's access need a passkey assertion no older than 5 minutes. **Signing in and registering count as step-up**:
  right after either, a stolen session cookie is as good as a fresh assertion for 5 minutes. A setup link is therefore
  as powerful as a step-up session.
- **Rate limits.** Each endpoint class has its own buckets: sign-in and registration per client address and in total;
  step-up per session; the admin API 600 requests a minute per session. Anonymous traffic cannot spend the owner's
  sign-in or step-up.
- **Erasing the home from the web is refused.** `DELETE /api/account` answers `403`; the home is not deletable from a
  browser.

## Setup links, the first-run page and reset-access

- **Setup links** carry a 32-byte random token in the URL fragment (never in Cloudflare's logs or a Referer). They
  work once; a new one revokes the older ones. A link from `homed admin setup-link` lives 15 minutes, one from the
  first-run page 3 minutes. Whoever opens a link first becomes, or adds a passkey to, the owner.
- **The first-run page is plain HTTP on the home box.** Anyone who can watch the LAN traffic can read the PIN, the
  page's cookie and the setup link as they pass, and use the link before you do. What limits it:
  - it runs only while no owner can sign in, and closes 30 minutes after it opened; `homed admin firstrun` opens it
    again;
  - it listens on the home network (`0.0.0.0:8090`) only while no tunnel is configured, the step that may need
    another device. Once a tunnel is configured, it listens on `127.0.0.1:8090`, the home box itself;
  - the PIN has 8 digits, changes every 10 minutes and after every issued link, and is printed only to a terminal
    (never to homed's log under launchd or systemd); one device gets 5 wrong PINs per 10 minutes, and 20 wrong PINs
    from anyone close the page until `homed admin firstrun` or a restart;
  - links it issues live 3 minutes and are audited (`firstrun.link`);
  - it answers `421` to any public Host name (DNS rebinding), and its POSTs need `X-Homie` and the same origin.

  On a network you do not trust, skip the page: run `homed cloudflare connect <host>` and `homed admin setup-link` in a
  terminal on the home box and open the link there.
- **`homed admin reset-access`** (socket only) deletes every passkey and every session of every person, revokes every
  unused setup link and invite, and prints a new setup link for the owner. After a compromise also remove the
  connections of the assistants and, if the tunnel credentials may have leaked, make a new tunnel (see "A compromised
  Cloudflare account" below).

### A compromised Cloudflare account or tunnel

Anyone who controls the host can serve pages on the home's origin, and may leave things behind that outlive their own
cleanup:

- **Year-long cached files.** The admin's hashed assets are sent `Cache-Control: immutable` for a year. A tampered copy
  served under a real asset name stays in the browser's cache.
- **A service worker.** The admin registers none, but the origin allows same-origin workers; one installed by a hostile
  edge keeps intercepting the origin's requests in that browser.
- **Stolen secrets.** Setup links, session cookies and access tokens that passed through it.

Recovery, on every device that opened the admin while the account may have been hostile: **clear the site's data**
(Safari on iPhone: Settings → Apps → Safari → Advanced → Website Data → remove the host; Safari on a Mac: Settings →
Privacy → Manage Website Data; Chrome: the lock icon → Site settings → Delete data). Then, on the home box:

1. Secure the Cloudflare account, and delete the tunnel in its dashboard if the credentials may have leaked;
2. `homed cloudflare connect <host>` to make a new tunnel;
3. `homed admin reset-access` — deletes every passkey and session and prints a new setup link;
4. in **People**, remove the people and connections you do not recognise, and add the assistants' connections again.

The admin's pages carry `Strict-Transport-Security` (a year) next to their strict CSP.

## The admin socket on a desktop (macOS)

The socket `homed.sock` is `0600` in a `0700` directory, and homed accepts only connections of its own OS user (peer
credentials). Whoever can open it is the hub's owner: it makes setup links and resets access.

On **Linux** the hub runs as a dedicated user (`homie`), so another user's malware cannot reach the socket. On
**macOS** it runs as **you**, in your login session. A program of your own user could therefore also use the socket —
but a same-user program can already replace the `homed` binary in the data dir, read `homie.db` and the TV keys, and
read the tunnel credentials, so hardening the socket alone would buy little. What Homie does instead is *detection*:

- the audit and the log record the **process id and executable path** of whoever asked for `setup-link` or
  `reset-access`, and of up to three programs above it (detail `peer_chain`);
- a `reset-access` raises an **"access was reset"** alert in the admin's Home screen (kept for 7 days), naming the
  program that asked and, when the socket knew them, the programs that ran it (up to three ancestors, e.g. `…/homed
  (pid 812), run from /bin/zsh (pid 790) < /System/Applications/Utilities/Terminal.app/… (pid 655)`): the program on
  the socket is usually the homed CLI itself, so the chain above it is what tells who asked;
- the boundary is documented: on a Mac that is not yours alone, do not run Homie as an account that others use.

A dedicated macOS user for the daemon would be a real boundary, but needs `sudo` at install and has unverified
side effects on the Local Network permission; it is not part of v0.2.

## Plugins

Plugins are separate executables that the hub starts with a minimal environment and talks to over stdio. What that
does and does not isolate:

- **A plugin runs as the same OS user as homed. It is fully trusted.** It can read everything that user can:
  `homie.db` (hashes, sessions, the audit log), the tunnel credentials, the admin socket, other plugins' data dirs, the
  user's files and, on a Mac, Keychain items that user can open without a prompt. Install only plugins whose authors
  you trust as much as the hub itself. Their per-plugin data dir (`HOMIE_DATA_DIR`, `0700`) is for tidiness, not a
  sandbox.
- Plugins never receive the callers' tokens, and they are not reachable from the network through the hub: the hub
  talks to them over stdio only. They may make their own network connections (homie-tv talks to TVs on the LAN).
- The request `_meta` `homie/caller` and `homie/via` (`chatgpt`, `claude`, `admin`, `cli`) are informational; plugins
  must not base security decisions on them.

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
- **No downgrades from the web.** An update to a release older than the installed one is refused; only the local
  socket can ask for it, with an explicit `--allow-downgrade`. One install, update or removal at a time per plugin.

## The home LAN (homie-tv)

- LG webOS 3.x speaks SSAP over plain `ws://` on port 3000, so the TV client key travels **in clear** on the LAN;
  anyone who can sniff the LAN can capture it and control the TV.
- Discovery fetches the UPnP description only from the IP that answered the SSDP search and follows no redirects; the
  SSAP websocket dial follows no redirects either.
- Wake-on-LAN packets are broadcast on the LAN; they carry only the TV's MAC address.

## Reporting a problem

Open a GitHub issue without details for anything that could be abused, and ask for a private channel; do not post
tokens, keys or setup links anywhere.
