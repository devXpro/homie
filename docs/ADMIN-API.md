# Admin API

The contract between `homed` and the admin SPA (`web/`): what is served where, the JSON of Homie's own API under
`/api/home/*`, the first-run page's API, and the parts that belong to
[MCP Gatekeeper](https://github.com/devXpro/mcp-gatekeeper). This is v0.2 (the v0.1 API under `/api/admin/*`, with its
keys, relay and Sites, is gone).

Examples use the neutral host `home.example.com`.

## 1. What is served where

| Prefix | Owner | Notes |
|---|---|---|
| `/api/auth/*`, `/api/me*` | gatekeeper `auth` | sign-in, setup, join, step-up, passkeys, sessions (the UI package does it all) |
| `/api/oauth/txn/*`, `/oauth/*`, `/.well-known/*` | gatekeeper `oauth` | the OAuth 2.1 server and the consent screen |
| `/api/account*`, `/api/admin/accounts`, `/api/admin/audit` | gatekeeper `people` | people, invites (devices and a term), connections, the **audit of admin actions** |
| `/api/home/*` | **Homie (this file)** | devices, tools, calls, jobs, activity, settings, tunnel, system, events |
| `/api/firstrun/*` | Homie, on the **first-run listener only** (`http://127.0.0.1:8090`, or the home network while no tunnel is configured) | §7 |
| `/mcp` | Homie's gateway behind the gatekeeper's `mcpguard` | the assistants only: OAuth bearer tokens, stateless JSON |
| `/healthz` | — | `{"status":"ok"}`; every answer of the public listener carries `X-Homie-Instance` |

Brand on the Go side: `{Product:"Homie", SessionCookie:"__Host-homie_session", TxnCookie:"__Host-homie_txn",
CSRFHeader:"X-Homie", ResourceNoun:"home"}`; `people.Options{Permissions: <plugins>, CoOwners: true, SingleAccount: true}`.
The gatekeeper's endpoints are documented in its [README](https://github.com/devXpro/mcp-gatekeeper#readme) and
[docs](https://github.com/devXpro/mcp-gatekeeper/tree/main/docs); §5 lists the Homie-specific fields.

## 2. Conventions of `/api/home/*`

- JSON keys are **camelCase**. Optional keys are omitted, never `null`.
- Times are RFC 3339 UTC with milliseconds (`"2026-11-15T12:04:05.123Z"`); durations are integer milliseconds in keys
  ending `Ms`. Ids are strings (activity ids are decimal strings).
- **Errors are the gatekeeper's form**: `{"error":"<code>","message":"…"}`. Some answers add flat extra keys next to
  them:
  - `400 invalid_args` adds `"fields":[{"path":"/percent","code":"maximum","message":"must be at most 100"}]` (tool
    arguments, JSON Pointer paths);
  - `409 call_running` adds `"callId"`, `409 job_running` adds `"jobId"`.
- Every mutating request needs `X-Homie: 1`, a same-origin `Origin`/`Sec-Fetch-Site` when the browser sends them, and
  `Content-Type: application/json` when it has a body (else `415 unsupported_media_type`). Bodies are at most 1 MiB
  (`413 too_large`), tool arguments at most 64 KiB.
- Session: the gatekeeper's. `401 no_session` without one, `403 step_up_required` for ✋ endpoints (do a passkey
  step-up and retry), `403 forbidden` for a member on an owner endpoint and for anyone but the superadmin on a
  superadmin endpoint (checked before the step-up, so a co-owner is never asked for a passkey there).
- **Roles.** *owner* = `role: "owner"` (the first owner and a co-owner alike); *superadmin* = the owner who set the home
  up (`person.superadmin` in `GET /api/me`): the endpoints that run code on the home box or change the system —
  installing, updating and removing plugins, updating the hub, reconnecting the tunnel — are theirs alone, and a
  co-owner gets `403 {"error":"forbidden","message":"only the home's superadmin may do this: …"}`, so the SPA hides
  those controls from co-owners; *session* = any signed-in person (owner or member). Members never see devices they may
  not use, nor anyone else's activity. The admin socket (the homed CLI on the home box) is the local operator and may
  do everything.
- Rate limit: 600 requests a minute per session → `429 rate_limited` with `Retry-After`.
- Codes used besides the gatekeeper's: `invalid_json`, `invalid_args`, `unsupported_media_type`, `too_large`,
  `unknown_tool`, `no_inventory`, `call_running`, `job_running`, `downgrade`, `tool_failed` (422), `timeout` (504),
  `plugin_unavailable` (503), `home_not_ready` (503: no tunnel, not set up, or the feature is not available),
  `upstream` (502), `method_not_allowed` (405), `rate_limited`, `conflict`, `not_found`, `forbidden`, `internal`, and
  `misdirected` (421, on any path: the Host is not the current public host — for instance the old address after a
  quick tunnel changed).
- Unknown paths answer `404 not_found` as JSON; there is no `/api/v1/*`.

## 3. Objects

`?` marks an optional (omitted when empty) key.

**TunnelStatus**
```json
{"mode":"named","state":"connected","url":"https://home.example.com","mcpUrl":"https://home.example.com/mcp",
 "hostname?":"home.example.com","since?":"2026-11-15T18:42:07.000Z","lastError?":"…"}
```
- `mode`: `named` (own domain) | `quick` (trycloudflare, "for trying") | `off` (not connected; also `homed run
  --dev-origin`, where `url` is the development origin).
- `state`: `starting` | `probing` (the address is known, the self-check through Cloudflare does not pass yet;
  `lastError` says why) | `connected` | `failing` (cloudflared exited and restarts with a backoff) | `stopped` (mode
  off).
- `url` and `mcpUrl` are absent while a quick tunnel has no hostname yet. `lastError` is English, for "Details".

**PluginSummary**
```json
{"id":"tv","name":"homie-tv","title":"TV","description?":"LG webOS TVs: power, volume, inputs.",
 "version":"0.2.0","state":"up","since?":"…","lastError?":"…","enabled":true,
 "source":{"kind":"github","repo?":"devXpro/homie-tv","homepage?":"https://github.com/devXpro/homie-tv"},
 "update?":{"latest":"0.3.0","checkedAt":"…"},
 "tools":{"assistant":5,"admin":5},"inventoryTool?":"tv_list","devices?":1}
```
- `state`: `starting|up|failing|stopped`, or `disabled` when `enabled` is false.
- `title` and `version` come from the plugin's `serverInfo`; before the first start, from `homie-plugin.json`, or the id.
- `devices` is the item count of the last inventory read, when there was one.

**PluginDetail** = PluginSummary plus
```json
{"instructions?":"Controls the TVs at home…","previousVersion?":"0.1.0","command?":["/path/to/homie-tv","serve"],
 "tools":[Tool…]}
```
(`tools` replaces the counts with the full list, assistant and admin, sorted by name.)

**Tool**
```json
{"name":"tv_pair","pluginId":"tv","title":"Pair a TV","description":"Pairs with a TV that is on…",
 "audience":"admin","inputSchema":{…},"outputSchema?":{…},
 "annotations?":{"readOnlyHint":false,"destructiveHint":false,"idempotentHint":true},
 "timeoutMs":180000,"inventory":false,"target?":"tv","i18n?":{"ru":{"title":"…","description":"…","properties":{…}}}}
```
`target` is the input property that names an inventory item: the property marked `"x-homie-ref":"inventory"` in its
schema, else a required string property named like the plugin id, `device` or `item`. `i18n` comes from the plugin's
`_meta "homie/i18n"` (admin API only, never sent to the assistants).

**InventoryItem** — `{"id":"tv","name":"Living room TV","subtitle?":"192.0.2.20 · webOS TV","state?":"on"}`; `state` is
`on|off|offline|unknown|paired|unpaired`.

**Call** — one admin or CLI tool call.
```json
{"id":"c_9fQ…","tool":"tv_pair","pluginId":"tv","title":"Pair a TV","args":{"tv":"tv"},
 "state":"running","startedAt":"…","endedAt?":"…","durationMs?":41234,"timeoutMs":180000,
 "progress?":{"progress":1,"total?":2,"message?":"Press OK on the TV remote",
              "messageI18n?":{"ru":"Нажмите OK на пульте телевизора"}},
 "result?":{"text":"Living room TV is paired.","structured?":{…}},
 "error?":{"code":"timeout","message":"the tool did not answer within 180 s"}}
```
- `state`: `running`; `ok` (ran, `result` set); `failed` (ran and reported `isError`; `result.text` is the plugin's own
  text); `error` (could not run: `error.code` is `plugin_unavailable|timeout|unknown_tool|internal`); `cancelled`.
- `args` are redacted before they are echoed back or sent over events.
- The hub keeps finished calls for 15 minutes (at most 100), so a reloaded page can find them.

**Job** — a long hub operation that is not a tool call.
```json
{"id":"j_4Rt…","kind":"plugin.install","target":"tv","state":"running",
 "steps":["resolve","download","verify","unpack","start"],"step":"download",
 "progress?":{"done":1200000,"total":5300000},"message?":"homie-tv 0.2.0",
 "startedAt":"…","endedAt?":"…","error?":{"code":"checksum_mismatch","message":"…"}}
```
| `kind` | `target` | `steps` |
|---|---|---|
| `plugin.install` | plugin id | `resolve, download, verify, unpack, start` |
| `plugin.update` | plugin id | `resolve, download, verify, unpack, switch, start` (+ `rollback` on failure) |
| `plugin.remove` | plugin id | `stop, remove` |
| `hub.update` | — | `resolve, download, verify, replace, restart` |

`state` is `running|ok|failed`. Job error codes: `checksum_mismatch`, `no_asset` (no build for this OS and CPU),
`incompatible` (`minHubVersion` too high), `start_failed` (the new version did not come up; for updates the old one is
back), `downgrade` (the release, "latest" included, is older than the installed version), `job_running` (another
install, update or removal of the same plugin holds it), `upstream`, `internal`. Finished jobs are kept for 1 hour (at
most 50).

**ActivityItem** — one tool call of an assistant, the admin or the CLI.
```json
{"id":"1042","at":"…","person?":{"id?":"per_ab…","name":"Alice"},"via":"chatgpt","tool":"tv_power_on",
 "toolTitle?":"Turn TV on","pluginId":"tv","args?":{"tv":"tv"},"ok":true,"error?":"…","durationMs":812}
```
`via` is `chatgpt|claude|admin|cli`. `args` are redacted. `person.id` is a gatekeeper person id (absent for the CLI).

**Settings** — `{"homeName":"Home","language":"en"}`. `homeName` **is the gatekeeper account's name** (one source).

**SystemInfo** — `{"version","commit","os","arch","startedAt","update?":{"latest","notesUrl","checkedAt"}}`.

**Attention item** — `{"kind":"…","target?":"tv","version?":"0.3.0","at?":"…","peer?":"/usr/bin/example (pid 4242)"}`;
kinds, by severity:
- `access_reset` — someone ran `homed admin reset-access` on the home box in the last 7 days (`at`; `peer` is the
  program that asked, e.g. `"/usr/local/bin/homed (pid 812)"`, followed by `", run from <ancestors>"` when the socket
  knew them: `"<exe> (pid N) < <exe> (pid N)"`, nearest first);
- `tunnel_offline` — state `failing`;
- `tunnel_unreachable` — state `probing` with a `lastError`;
- `plugin_failing` (`target`), `hub_update` (`version`), `plugin_update` (`target`, `version`);
- `tunnel_quick` — quick mode: show "for trying" and the way to move to a domain.

## 4. Endpoints (`/api/home`)

✋ = needs a fresh passkey (step-up). *superadmin* = the home's superadmin only (a co-owner gets `403 forbidden`).

| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /bootstrap` | public | — | `200 {"homeName":"Home","language":"en","ownerReady":true,"tunnel":{"mode":"quick"}}` |
| `GET /overview` | owner | — | `200 {"home":{"name"},"tunnel":TunnelStatus,"plugins":[PluginSummary…],"attention":[…],"recent":[ActivityItem…]}` (`recent` = the last 5 assistant calls) |
| `GET /plugins` | owner | — | `200 {"items":[PluginSummary…]}` |
| `GET /plugins/preview` | owner | `?repo=owner/name&version=latest` | `200` a preview; `404 not_found`, `502 upstream` |
| `POST /plugins/check-updates` | owner | `{}` | `200 {"items":[PluginSummary…]}` |
| `POST /plugins` | superadmin ✋ | `{"repo":"devXpro/homie-tv","version?":"0.2.0","id?":"tv"}` | `202 Job` (`plugin.install`); `409 conflict`, `409 job_running` |
| `GET /plugins/{id}` | owner | — | `200 PluginDetail` |
| `PATCH /plugins/{id}` | owner | `{"enabled":false}` | `200 PluginSummary` |
| `DELETE /plugins/{id}` | superadmin ✋ | `?keepData=false` | `202 Job` (`plugin.remove`); when it succeeds, `plugin:<id>` is taken from every person (`account.member.update`, detail `reason: plugin.remove`) and every open invite that gives it is revoked (`account.invite.revoke`) |
| `POST /plugins/{id}/restart` | owner | `{}` | `202 PluginSummary` |
| `POST /plugins/{id}/update` | superadmin ✋ | `{"version?":"0.3.0"}` | `202 Job` (`plugin.update`); `409 downgrade`; `allowDowngrade` → `403 forbidden` (CLI only) |
| `GET /plugins/{id}/inventory` | owner | — | `200 {"items":[InventoryItem…],"fetchedAt":"…"}`; `404 no_inventory`, `422 tool_failed`, `503 plugin_unavailable`, `504 timeout` |
| `GET /plugins/{id}/logs` | owner | `?tail=200` | `200 {"lines":["…"]}` |
| `GET /tools` | owner | `?audience=admin\|assistant&plugin=tv` | `200 {"items":[Tool…]}` |
| `POST /tools/{name}/calls` | owner | `{"args":{…}}`, `?wait=1` | `202 Call` / `200 Call`; `400 invalid_args` (+`fields`), `404 unknown_tool`, `409 call_running` (+`callId`), `503 plugin_unavailable` |
| `GET /calls` | owner | `?state=running` | `200 {"items":[Call…]}` |
| `GET /calls/{id}` | owner | — | `200 Call` |
| `DELETE /calls/{id}` | owner | — | `202 Call` |
| `GET /jobs` | owner | `?state=running` | `200 {"items":[Job…]}` |
| `GET /jobs/{id}` | owner | — | `200 Job` |
| `GET /activity` | session | `?person=<id>&plugin=<id>&tool=<name>&via=chatgpt,claude&status=ok\|failed&since=&until=&cursor=&limit=` | `200 {"items":[ActivityItem…],"nextCursor?":"…"}`; a member sees only their own (their `person` filter is forced) |
| `GET /activity/{id}` | session | — | `200 ActivityItem`; a member gets `404` for anyone else's |
| `GET /settings` | owner | — | `200 Settings` |
| `PATCH /settings` | owner | `{"homeName?":"Home","language?":"en"}` | `200 Settings`; renames the account, the MCP title and the instructions at once; audited as `settings.update` |
| `GET /tunnel` | owner | — | `200 TunnelStatus` |
| `POST /tunnel/check` | owner | `{}` | `200 TunnelStatus` after a self-check now |
| `POST /tunnel/reconnect` | superadmin ✋ | `{}` | `202 TunnelStatus`: restarts cloudflared. **Quick mode: a new address** — every passkey and connector of the old one stops working, and the owner continues on the first-run page of the home box; `409 conflict` with mode `off` |
| `GET /system` | owner | — | `200 SystemInfo` |
| `POST /system/check-update` | owner | `{}` | `200 SystemInfo` |
| `POST /system/update` | superadmin ✋ | `{"version?":"0.3.0"}` | `202 Job` (`hub.update`); after `replace` homed restarts: wait for the `hello` event with the new version |
| `GET /events` | session | `Last-Event-ID`, `?logs=<plugin id>` (owner) | SSE, §6 |
| `GET /events/poll` | session | `?after=<cursor>&logs=<plugin id>` | long poll, §6 |

## 5. What lives in the gatekeeper's part

For the People and Connect screens:

- `GET /api/account` carries Homie's extra fields:
  - `"resources":[{"id":"home","name":"Home","description":"…","mcp_url":"https://home.example.com/mcp","enabled":true}]`
    — the UI package's `resources`;
  - `"devices":[{"id":"tv","title":"TV","permission":"plugin:tv"}]` — the plugins the viewer may use (an owner: all; a
    member: only theirs; `[]` when none);
  - `"tunnel":{"mode":"named|quick|off"}` — the same as bootstrap's, so the UI can poll in quick mode.
- **Permissions.** `"all"` (every device, now and later) or `"plugin:<plugin id>"`. The catalog for the pickers is
  `GET /api/account/permissions` (owner) → `[{"id":"plugin:tv","title":"TV","group":""}]`: every installed plugin,
  disabled ones included; `"all"` is not in it and is always accepted besides.
- **Invites.** `POST /api/account/invites {"name","permissions","access_until?"}` (a member) or `{"name","role":"owner"}`
  (a co-owner; the superadmin only) → `200 {"id":"inv_…","url":"https://home.example.com/join#t=…","expires_at":"…"}`.
  `access_until` is RFC 3339, in the future, at most one year ahead. Switching to a **quick tunnel** revokes every
  open member and owner invite (`account.invite.revoke`, detail `reason: tunnel.quick`), and in **quick mode** every
  invite answers `409 {"error":"quick_mode","message":"invites need the home's own domain: …"}`.
- **Members.** `PATCH /api/account/members/{id}` with `{"access_until?":"…"|null,"permissions?":[…]}`; widening access
  needs a step-up. `DELETE /api/account/members/{id}` removes a person. Members and invites in listings carry
  `access_until`, `permissions` and `active`; an expired person stays listed.
- **Connections.** `DELETE /api/account/connections/{grant id}` revokes one assistant connection.
- **The consent screen** (`GET /api/oauth/txn/{txn}`, the gatekeeper's) lists in `resource.tools` only the tools the
  signed-in viewer may use: a guest sees their devices' tools, never the rest of the home. The resource metadata
  (no viewer) lists every tool.
- **Audit of admin actions**: `GET /api/account/audit`. Homie's own actions: `plugin.install`, `plugin.update`,
  `plugin.remove`, `plugin.enable`, `plugin.disable`, `plugin.restart`, `settings.update` (detail `keys`),
  `hub.update`, `tunnel.connect`, `tunnel.quick`, `tunnel.disconnect`, `tunnel.reconnect`, `firstrun.link`; the detail
  `via` is `admin` or `cli`. Plus the gatekeeper's own (`auth.*`, `account.*`, `oauth.*`, `admin.setup-link`,
  `admin.reset-access`, whose detail names the socket peer: `peer_pid`, `peer_exe` and `peer_chain`, its ancestors).
- `DELETE /api/account` («delete everything») is refused: `403 forbidden` (after the gatekeeper's own `400 invalid`
  without `{"confirm":"<account name>"}`). The UI hides that screen.
- `/api/admin/accounts` lists the one account (single-account mode): the UI hides the families section.

## 6. Events

`GET /api/home/events` (SSE) and `GET /api/home/events/poll` (a long poll for quick mode, where no SSE passes through
the tunnel) share one ring of 500 events. Event ids are `"<run>-<seq>"` strings.

- **SSE:** `retry: 3000`, `: ping` every 20 s; the session is re-checked at every ping and the stream ends with
  `session.revoked` when it is gone. `Last-Event-ID` resumes; otherwise the first event is `resync`.
- **Poll:** without `after` it answers at once `{"events":[{"id":"…","event":"hello","data":{…}}],"cursor":"…"}`; with
  `after=<cursor>` it waits up to **20 s** for newer events and answers `{"events":[…],"cursor":"…"}` (`events` may be
  empty: poll again with the new cursor). A cursor that cannot resume answers a `resync` event. Each item is
  `{"id","event","data"}`, with the same `data` an SSE frame carries. `401 no_session` when the session ended.

| `event` | `data` | members? |
|---|---|---|
| `hello` | `{"version":"0.2.0","serverTime":"…"}` | yes |
| `resync` | `{}` | yes |
| `tunnel` | TunnelStatus | no |
| `plugin` | PluginSummary | no |
| `plugin.removed` | `{"id":"tv"}` | no |
| `tools` | `{"pluginId":"tv"}` | no |
| `call` | Call | no |
| `job` | Job | no |
| `activity` | ActivityItem | **own only** |
| `settings` | `{"homeName"}` for members, Settings for owners | yes |
| `attention` | `{"kind":"access_reset",…}` (an alert raised now) | no |
| `plugin.log` | `{"pluginId":"tv","lines":["…"]}` (`?logs=`) | no |
| `session.revoked` | `{}` (SSE only; the poll answers 401) | yes |

## 7. The first-run page (`/firstrun`, its own listener, plain HTTP)

Listener: `0.0.0.0:8090` only while **no tunnel is configured** and `setup_listen` is unset; once a tunnel is
configured it is `127.0.0.1:8090` (the page moves there by itself after a connect from the home network: the same PIN
and cookie keep working on `localhost`). The PIN rules: 8 digits, rotating every 10 minutes and after every link; 5
wrong per device per 10 minutes, 20 in total close the page; 30 minutes of life; links live **3 minutes**. The cookie
is `homie_firstrun`. Errors are in the form of §2, and POST needs `X-Homie: 1`. Every cookie endpoint answers `401
pin_required` without the cookie (back to the PIN step); a cross-site POST gets `403 forbidden`; an unknown `/api/…`
path `404 not_found`.

| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /api/firstrun/state` | public | — | `200 {"step":"pin"}`; with the cookie `{"step":"tunnel\|connecting\|link","homeName?":"Home","ownerExists":false,"tunnel":TunnelStatus,"connect?":ConnectJob}` |
| `POST /api/firstrun/pin` | public | `{"pin":"12345678"}` | `204` + cookie; `401 wrong_pin`; `429 rate_limited`; `410 setup_closed` |
| `POST /api/firstrun/tunnel` | cookie | `{"mode":"named","hostname":"home.example.com","overwriteDns?":false}` or `{"mode":"quick"}` | `202 {"tunnel":TunnelStatus,"connect?":ConnectJob}`; `409 conflict` (a domain is connected already: change it on the home box; quick only from `off`; a connect is running); `400 invalid_args` (`/hostname`) |
| `GET /api/firstrun/events` | cookie | — | SSE: `tunnel` (TunnelStatus) and `connect` (ConnectJob) events; ends when the owner is set up |
| `POST /api/firstrun/link` | cookie | `{}` | `200 {"url":"https://home.example.com/setup#t=…","expiresAt":"…"}`; `409 tunnel_not_ready` |

- `step`: `tunnel` = nothing connected (offer "My domain" or "Try without a domain"); `connecting` = a connect job
  runs (show `connect.loginUrl` as a button **and** a QR code as soon as it is there) or the tunnel is starting or
  probing; `link` = connected and the self-check passed: offer to create the link (QR code, or "open on this
  computer").
- `ownerExists: true` with `step: "link"` is the **quick-mode reconnect** case: the address changed, the owner's
  passkeys of the old address no longer count, and the page reopened by itself. The page offers **Reconnect** (a new
  setup link for the same owner) and the steps to add the connector again.
- **ConnectJob** — `{"hostname":"home.example.com","state":"running|ok|failed","step":"login|tunnel|dns|config",
  "loginUrl?":"https://dash.cloudflare.com/…","error?":"…","code?":"dns_exists","startedAt":"…","endedAt?":"…"}`. A
  DNS record that exists fails with `"code":"dns_exists"` (and an English `error`): connect again with
  `"overwriteDns": true` after the owner agrees.
- The page answers `410 setup_done` (and a bilingual HTML page) once an owner has a passkey of the current address,
  `410 setup_closed` once closed, and `421 misdirected` for a public Host name (DNS rebinding). `POST /link` also
  answers `410 setup_done` when the owner could sign in meanwhile.

## 8. Web routes Homie serves `index.html` for

`/`, `/home`, `/login`, `/setup`, `/join`, `/consent`, `/connect`, `/account`, `/account/{rest...}`, `/devices`,
`/devices/{rest...}`, `/activity`, `/activity/{rest...}`, `/people`, `/people/{rest...}`, `/settings`,
`/settings/{rest...}`, and `/firstrun` on the first-run listener. `/admin` and `/admin/{rest...}` answer `308` to `/`
(old bookmarks). Hashed assets live under `/assets/` (`Cache-Control: immutable`); the top-level files of the build
(theme, icons, manifest) are served too.

## 9. The local socket (`<data dir>/homed.sock`)

The CLI is a client of the gatekeeper's admin socket (`0600`, in the `0700` data dir; the peer's uid must be the hub's
own user or root). It carries a command registry, not the HTTP API above:

| Command | Does |
|---|---|
| `homed admin firstrun` | opens the first-run page and prints its address and PIN |
| `homed admin setup-link` | prints a setup link (15 minutes, once) — the link alone on stdout |
| `homed admin reset-access` | wipes passkeys, sessions and connections of every person, prints a new setup link |
| `homed cloudflare connect <host> [--overwrite-dns]` | connects a domain; follows the job; also works with homed stopped (writes into `<data dir>/tunnel`) |
| `homed cloudflare quick` / `status` / `disconnect` | the quick tunnel, its status, stop |
| `homed plugin add\|list\|remove` | local plugins |
| `homed tool list` / `homed tool call <name> ['<json>' \| -]` | every tool, admin ones included; a call runs as the owner, via `cli` |
| `homed update [--version V] [--allow-downgrade]` | updates homed to a release; it restarts |

Everything except `run`, `version` and `cloudflare connect` needs a running homed.
