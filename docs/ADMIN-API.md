# Admin API

The JSON API behind the admin SPA (`https://<domain>/admin`), the `/join` wizard (phase 3) and the `homed` CLI. T2.2
(auth), T2.3 (backend), T2.4 (UI), T3.1 (people and invites) and T3.2/T3.3 (connections) build against this file;
`internal/store/store.go` holds the repositories it uses, and `docs/DESIGN.md` the screens that call it.

Phase markers: endpoints without a marker are phase 2. **[P3]** endpoints are phase 3; T2.3 does not build them, but
their paths, objects and errors are fixed here so phase 2 leaves room for them.

## 1. Conventions

### 1.1 Transport
- Base path `/api/admin`. Phase 3 adds nothing outside it: the `/join` page uses the same endpoints.
- Requests and responses are JSON (`application/json; charset=utf-8`). A request with a body and another
  `Content-Type` gets `415 unsupported_media_type`. Bodies are capped at 1 MiB (`413 too_large`); tool arguments at
  64 KiB.
- Keys are camelCase. Absent optional keys are **omitted**, never `null` (the gateway's rule, §5.4).
- Times are RFC 3339 strings in UTC with milliseconds, e.g. `"2026-11-15T12:04:05.123Z"`. Durations are integer
  milliseconds in keys ending `Ms`.
- Ids are strings, also the audit ids (int64 in the store, a decimal string on the wire, so JavaScript never rounds
  them).
- Every response carries `X-Homie-Instance` (from the gateway), `Cache-Control: no-store`,
  `X-Content-Type-Options: nosniff` and `X-Request-Id`.
- Success bodies are the resource itself, with no envelope. `204` has no body.

### 1.2 Errors
Every error has this body:
```json
{"error":{"code":"invalid_args","message":"percent: must be at most 100","requestId":"r_5mK2q9",
  "fields":[{"path":"/percent","code":"maximum","message":"must be at most 100"}]}}
```
- `code` is stable and machine-readable; the SPA maps it to RU/EN copy (DESIGN.md §10). `message` is English for logs
  and the CLI, and never contains a secret. `fields` appears only with `invalid_args`; `path` is a JSON Pointer into
  the request.
- This differs from the gateway's `{"error":"…"}` on purpose: the Site needs one string, the admin needs codes.

| Status | `code` | When |
|---|---|---|
| 400 | `invalid_json` | the body is not JSON, or not the expected object |
| 400 | `invalid_args` | a field or tool argument fails validation; see `fields` |
| 400 | `webauthn_failed` | a passkey ceremony did not verify (wrong origin, RP ID, challenge, signature) |
| 401 | `unauthenticated` | no session, or it expired |
| 401 | `login_failed` | the assertion verified against no known passkey; the same answer for every cause |
| 401 | `wrong_pin` | first-run LAN page: bad PIN |
| 403 | `csrf_failed` | §1.4 rule broken |
| 403 | `step_up_required` | the endpoint needs a fresh passkey (§1.5) |
| 403 | `forbidden` | the role may not do this (a member calling an owner endpoint) |
| 404 | `not_found` | no such resource, or not visible to this person |
| 404 | `unknown_tool` | no such tool |
| 404 | `token_invalid` | setup or invite token not known |
| 404 | `no_inventory` | the plugin has no inventory tool |
| 405 | `method_not_allowed` | |
| 409 | `conflict` | generic duplicate (id taken, name taken); first run: homed already has a relay |
| 409 | `downgrade` | an update to a version older than the installed one (plugin or hub) without `allowDowngrade` on the socket |
| 409 | `last_passkey` | removing the person's only passkey that can still sign in (one of the current relay domain) |
| 409 | `passkey_exists` | this authenticator is already registered |
| 409 | `call_running` | the same tool already has a running admin call; body adds `"callId"` |
| 409 | `job_running` | a job of the same kind and target is running; body adds `"jobId"` |
| 409 | `owner_exists` | first run: the home already has an owner with a passkey |
| 409 | `relay_not_ready` | first run: no relay connection or the self-check fails |
| 409 | `domain_change_local_only` | a relay code for another domain sent from the web (§3.9) |
| 410 | `token_expired` / `token_used` | setup or invite token past its time, or already consumed |
| 410 | `setup_done` | first-run LAN page after an owner exists |
| 410 | `setup_closed` | first-run LAN page closed: 30 minutes passed or 20 wrong PINs; `homed admin firstrun` opens it again |
| 413 | `too_large` | body over the limit |
| 415 | `unsupported_media_type` | |
| 422 | `tool_failed` | a synchronous tool read (inventory) ran and reported `isError`; `message` is the plugin's text |
| 429 | `rate_limited` | over a limit; `Retry-After` header in seconds |
| 500 | `internal` | |
| 502 | `upstream` | GitHub (releases, update check) failed or answered nonsense |
| 503 | `plugin_unavailable` | the plugin is not running (`hub.ErrPluginDown`) |
| 503 | `home_not_ready` | the hub is starting or the store is not open yet |
| 504 | `timeout` | a synchronous tool read timed out |

### 1.3 Authentication and roles
- **Session cookie** `__Host-homie_session=<base64url of 32 random bytes>; Path=/; Secure; HttpOnly; SameSite=Strict`.
  Only its sha256 is stored (`store.Session.TokenHash`). Lifetime per PLAN §5.8: 12 h idle, 7 d absolute; `Max-Age` is
  the absolute remainder. Expired sessions answer `401 unauthenticated` and are deleted.
- **Levels** used in the tables below:

  | Level | Who |
  |---|---|
  | public | anybody; rate limited |
  | session | any signed-in person (owner or member) |
  | owner | a session whose person has `role: "owner"` |
  | owner ✋ | owner plus step-up (§1.5) |
  | self ✋ | the signed-in person on their own resource, plus step-up |
  | socket | the local unix socket only (§6) |

- A member calling an owner endpoint gets `403 forbidden`. Members see only themselves (phase 3).

### 1.4 CSRF
Every request with a method other than `GET`/`HEAD` (the auth ceremonies and logout included) must carry
`X-Homie: 1`. If `Origin` is present it must equal `https://<domain>`; if `Sec-Fetch-Site` is present it must be
`same-origin`. Otherwise `403 csrf_failed`. With `SameSite=Strict` and the JSON content type this blocks cross-site
writes even from old browsers. The socket is exempt (§6). Development mode (`homed run --dev-origin
http://localhost:5173`, T2.6) swaps the allowed origin and the RP ID to `localhost`; it refuses to start with a relay
configured.

### 1.5 Step-up
- A session is *stepped up* while `now - session.stepUpAt < 5 min`. Signing in counts, so right after login nothing
  asks for Touch ID again.
- Endpoints marked ✋ (PLAN §5.8: plugin install, update or remove; hub update; key create or revoke; invite create;
  Site publish and republish; passkey add or remove; relay change; person removal) answer
  `403 {"error":{"code":"step_up_required",…}}` without doing anything when the session is not stepped up.
- The SPA then runs `POST /auth/stepup/begin` → the browser's passkey prompt → `POST /auth/stepup/finish`, and retries
  the original request once. The response of `finish` carries `stepUpUntil`, so the SPA can skip the round trip while
  it is valid.

### 1.6 Pagination
Lists that can grow (the audit log) are cursor-paged:
- request: `?limit=50&cursor=<opaque>`; `limit` 1–200, default 50;
- response: `{"items":[…],"nextCursor":"<opaque>"}`; `nextCursor` is omitted on the last page;
- the cursor is bound to the filter: changing a filter means starting without a cursor; a cursor from another filter
  gets `400 invalid_args` (`fields[0].path = "/cursor"`).

Short lists (plugins, passkeys, people, keys, sessions) are returned whole as `{"items":[…]}`.

### 1.7 Rate limits
Every class has buckets of its own, so spending one never locks another (T2.F): an anonymous flood can neither stop
the owner's sign-in from their own address nor a signed-in owner's step-up.

| Scope | Limit |
|---|---|
| `auth/token`, `auth/register/begin`, `auth/register/finish` | every request: 20 per minute per client address, 60 per minute in total |
| `auth/login/begin`, `auth/login/finish` | per client address: 30 begins and 10 failed finishes per minute; no shared bucket |
| `auth/stepup/*`; `me/passkeys/begin`, `me/passkeys/finish` | per session, each its own: 30 begins and 10 failed finishes per minute |
| first-run PIN | per device (LAN address): 5 wrong PINs per 10 minutes; 20 wrong PINs from anyone close the page (`410 setup_closed`) |
| every other admin request | 600 per minute per session |

- A failed finish is one whose passkey or ceremony did not verify (`webauthn_failed`, `login_failed`); successful
  finishes cost nothing. While a key's failures are spent, its begins and finishes both answer `429`.
- Pending ceremonies: at most 1000 public ones (register, login; a new one replaces the oldest when full) and 1000 of
  sessions (step-up, another passkey), so public traffic never crowds out a step-up.
- The client address: homed only sees `127.0.0.1` from frpc. It takes the second-to-last `X-Forwarded-For` hop: the
  one Caddy appended (Caddy replaces any value a client sends), followed by the one frps appended (Caddy's). The
  left-most, client-controlled values are never used. A CDN or another proxy in front of Caddy would turn that hop
  into the proxy's address (docs/SECURITY.md). IPv6 clients count per /64. A request without the two hops (loopback,
  development, a local tool) has a bucket of its own, outside the totals, never the internet's.

## 2. Objects

Every object below is the exact JSON shape; `?` marks an optional (omitted when empty) key.

**Person**
```json
{"id":"roman","name":"Roman","role":"owner","plugins":["*"],"language?":"ru","createdAt":"…",
 "passkeys":2,"lastActiveAt?":"…","connection?":{"…Connection summary: id, kind, state, stale"}}
```
`passkeys`, `lastActiveAt` and `connection` are filled in lists and details, not stored.

**Session** — `{"id":"s_Q2v…","device":"Safari · iPhone","createdAt":"…","lastSeenAt":"…","current":true}`

**Me** (`GET /session`)
```json
{"person":{Person},
 "session":{"id":"s_…","createdAt":"…","expiresAt":"…","idleExpiresAt":"…","stepUpUntil?":"…"},
 "home":{"name":"Мой шалаш","language":"ru"},
 "version":"0.2.0","features":{"people":false,"connections":false}}
```

**Passkey** — `{"id":"<base64url credential id>","name":"MacBook Pro","provider?":"iCloud Keychain","createdAt":"…",
"lastUsedAt?":"…","current":true}` (`current`: the passkey this session signed in with).

**Key** — `{"id":"k_7Hd…","personId":"roman","label":"ChatGPT Site","hint?":"ghk_AbCd","connectionId?":"cn_…",
"createdAt":"…","lastUsedAt?":"…"}`. The key itself appears exactly once, as `secret` in the create response.

**RelayStatus**
```json
{"configured":true,"domain":"homie.jobbyjobs.work","publicUrl":"https://homie.jobbyjobs.work",
 "state":"connected","since":"…","lastError?":"dial tcp: i/o timeout",
 "selfCheck":{"ok":true,"checkedAt":"…","error?":"…"}}
```
`state` is derived from `relay.Client.Status()`: `not_configured` (no `relay` in homed.yaml), `connected`,
`connecting` (not connected and either no error yet or the last change was under 30 s ago), `offline` (otherwise).
`lastError` and `selfCheck.error` are English; the SPA shows them only under "Подробнее".

**PluginSummary**
```json
{"id":"tv","name":"homie-tv","title":"TV","description?":"LG webOS TVs: power, volume, inputs.",
 "version":"0.2.0","state":"up","since":"…","lastError?":"…","enabled":true,
 "source":{"kind":"github","repo?":"devXpro/homie-tv","homepage?":"https://github.com/devXpro/homie-tv"},
 "update?":{"latest":"0.3.0","checkedAt":"…"},
 "tools":{"assistant":5,"admin":5},"inventoryTool?":"tv_list","devices?":1}
```
- `state`: `starting|up|failing|stopped` from `hub.PluginStatus`, or `disabled` when `enabled` is false.
- `title` and `version` come from the plugin's `serverInfo`; before the first start, from `homie-plugin.json`, or
  the id.
- `devices` is the item count of the last inventory read, when there was one.

**PluginDetail** = PluginSummary plus
```json
{"instructions?":"Controls the TVs at home…","previousVersion?":"0.1.0","command?":["/…/homie-tv","serve"],
 "tools":[Tool…]}
```
(`tools` here replaces the counts with the full list, assistant and admin, sorted by name.)

**Tool**
```json
{"name":"tv_pair","pluginId":"tv","title":"Pair a TV","description":"Pairs with a TV that is on…",
 "audience":"admin","inputSchema":{…},"outputSchema?":{…},
 "annotations?":{"readOnlyHint":false,"destructiveHint":false,"idempotentHint":true},
 "timeoutMs":180000,"inventory":false,"target?":"tv"}
```
`target` is the input property that names an inventory item (DESIGN.md §9.3): the property marked
`"x-homie-ref":"inventory"` in its schema, else a required string property named like the plugin id, `device` or
`item`. The hub computes it once, so the SPA and the CLI agree.

**InventoryItem** — `{"id":"tv","name":"Living room TV","subtitle?":"192.168.68.66 · 49UJ634V-ZD","state?":"on"}`;
`state` ∈ `on|off|offline|unknown|paired|unpaired` (PLAN §5.1).

**Call** — one admin or CLI tool call.
```json
{"id":"c_9fQ…","tool":"tv_pair","pluginId":"tv","title":"Pair a TV","args":{"tv":"tv"},
 "state":"running","startedAt":"…","endedAt?":"…","durationMs?":41234,"timeoutMs":180000,
 "progress?":{"progress":1,"total?":2,"message?":"Press OK on the TV remote",
              "messageI18n?":{"ru":"Нажмите OK на пульте телевизора"}},
 "result?":{"text":"Living room TV is paired.","structured?":{…}},
 "error?":{"code":"timeout","message":"the tool did not answer within 180 s"}}
```
- `progress.messageI18n` (added in T2.3/T2.7): translations of `message` by language; the UI shows its language and
  falls back to `message`. Tool objects likewise carry `i18n?: {"<lang>": {"title","description","properties":{…}}}`
  from the plugin's `_meta "homie/i18n"` (admin API only; never sent to assistants).
- `state`: `running`; `ok` (ran, `result` set); `failed` (ran and reported `isError`; `result.text` is the plugin's
  own text); `error` (could not run: `error.code` ∈ `plugin_unavailable|timeout|unknown_tool|internal`);
  `cancelled`.
- `args` are redacted (`audit.Redact`) before they are echoed back or sent over SSE.
- The hub keeps finished calls for 15 minutes (at most 100), so a reloaded page can find them.

**Job** — a long hub operation that is not a tool call.
```json
{"id":"j_4Rt…","kind":"plugin.install","target":"tv","state":"running",
 "steps":["resolve","download","verify","unpack","start"],"step":"download",
 "progress?":{"done":1200000,"total":5300000},"message?":"homie-tv 0.2.0",
 "items?":[{"id":"tanya","label":"Tanya","state":"running","message?":"…"}],
 "startedAt":"…","endedAt?":"…","error?":{"code":"checksum_mismatch","message":"…"}}
```
| `kind` | `target` | `steps` |
|---|---|---|
| `plugin.install` | plugin id | `resolve, download, verify, unpack, start` |
| `plugin.update` | plugin id | `resolve, download, verify, unpack, switch, start` (+ `rollback` on failure) |
| `plugin.remove` | plugin id | `stop, remove` |
| `hub.update` | — | `resolve, download, verify, replace, restart` |
| `connection.publish` [P3] | connection id | `login, build, publish, deploy` (path A: `refresh`) |
| `connections.republish` [P3] | — | one item per person; item `state` ∈ `waiting`, `running`, `ok`, `failed`, `needs_login`, `skipped` |

`state` ∈ `running|ok|failed`. Job error codes: `checksum_mismatch`, `no_asset` (no build for this OS and CPU),
`incompatible` (`minHubVersion` too high), `start_failed` (the new version did not come up; for updates the old one
is back: `rolled_back`), `downgrade` (the release, "latest" included, is older than the installed version),
`job_running` (another install, update or removal of the same plugin holds it), `upstream`, `internal`. Finished jobs
are kept for 1 hour (at most 50).

**AuditItem**
```json
{"id":"1042","at":"…","kind":"tool","person?":{"id?":"roman","name":"Roman"},"via":"site",
 "tool?":"tv_power_on","toolTitle?":"Turn TV on","pluginId?":"tv",
 "action?":"plugin.install","target?":"tv",
 "args?":{"tv":"tv"},"ok":true,"error?":"The TV did not wake up…","durationMs":812}
```
`toolTitle` is the tool's current title, when the tool still exists. Admin `action` names are listed in §7.

**Settings** — `{"homeName":"Мой шалаш","language":"ru","connectionKind?":"site","siteSignOut?":false}`
([P3] keys appear once T3.0 sets `connectionKind`).

**SystemInfo**
```json
{"version":"0.2.0","commit":"8cd45e2","os":"darwin","arch":"arm64","startedAt":"…",
 "update?":{"latest":"0.3.0","notesUrl":"https://github.com/devXpro/homie/releases/tag/v0.3.0","checkedAt":"…"}}
```

**Invite** [P3] — `{"id":"i_…","kind":"member","personId":"tanya","createdAt":"…","expiresAt":"…",
"url?":"https://homie.jobbyjobs.work/join#t=<token>"}`. `url` (with the token) appears only in the create response.

**Connection** [P3] — the same shape for both ChatGPT paths:
```json
{"id":"cn_…","personId":"tanya","kind":"site","state":"live","stale":false,"title":"Мой шалаш",
 "publishedAt?":"…","lastError?":"…",
 "prompt?":{"kind":"device_code","url":"https://auth.openai.com/codex/device","code":"ABCD-1234","expiresAt":"…"}}
```
- `state` ∈ `pending|needs_login|publishing|live|failed|revoked` (store), `stale` = live but published with another
  title, tool set or instructions than the hub has now.
- `prompt` is what the person must do next, present only in `needs_login`:
  - `device_code` (path B): open `url`, enter `code`;
  - `add_plugin` (path A): `{"kind":"add_plugin","url":"https://<domain>/mcp","name":"Мой шалаш"}`;
  - `oauth_consent` (path A): `{"kind":"oauth_consent","url":"/admin/oauth/consent?…"}`.

## 3. Endpoints

Paths in §3, §4 and §6 are relative to `/api/admin` (`GET /plugins` = `GET /api/admin/plugins`); §5 lists full paths.

### 3.1 Bootstrap and session
| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /bootstrap` | public | — | `200 {"homeName":"Мой шалаш","language":"ru","ownerReady":true,"features":{"people":false,"connections":false},"connectionKind?":"site"}` |
| `GET /session` | session | — | `200 Me`; `401 unauthenticated` |

- `ownerReady` = an owner with at least one passkey of the current relay domain (the WebAuthn RP ID) exists; passkeys
  of an old domain cannot sign in and do not count. When false, the login screen says how to finish the setup.
- `bootstrap` reveals the home name and nothing else; no version, no people.

### 3.2 Passkey ceremonies (`/auth`)
WebAuthn options travel in the WebAuthn Level 3 JSON form (binary fields as base64url strings) exactly as
`go-webauthn` marshals `protocol.CredentialCreation`/`CredentialAssertion`, under `publicKey`. The SPA passes them to
`@simplewebauthn/browser` (`startRegistration({optionsJSON})`, `startAuthentication({optionsJSON})`) and sends the
returned JSON back unchanged as `credential`.

Common to all ceremonies:
- RP ID = the relay domain, origin `https://<domain>`; `userVerification: "required"`; `residentKey: "required"`
  (discoverable credentials, so login needs no user name); attestation `none`.
- `ceremonyId` is a random id for the server-side challenge state. It is held in memory for 5 minutes and is single
  use: a second `finish` with the same id gets `400 webauthn_failed`.

| Method, path | Level | Request | Response |
|---|---|---|---|
| `POST /auth/token` | public | `{"token":"…"}` | `200 {"kind":"setup","homeName":"Мой шалаш","personName?":"Roman","inviterName?":"Roman","expiresAt":"…"}`; `404 token_invalid`, `410 token_expired`/`token_used` |
| `POST /auth/register/begin` | public | `{"token":"…","personName?":"Роман","homeName?":"Мой шалаш"}` | `200 {"ceremonyId":"…","publicKey":{creation options}}` |
| `POST /auth/register/finish` | public | `{"ceremonyId":"…","credential":{…},"passkeyName":"MacBook Pro"}` | `201 Me` + `Set-Cookie`; `400 webauthn_failed`, `409 passkey_exists`, `410 token_*` |
| `POST /auth/login/begin` | public | `{}` | `200 {"ceremonyId":"…","publicKey":{request options, no allowCredentials}}` |
| `POST /auth/login/finish` | public | `{"ceremonyId":"…","credential":{…}}` | `200 Me` + `Set-Cookie`; `401 login_failed` |
| `POST /auth/stepup/begin` | session | `{}` | `200 {"ceremonyId":"…","publicKey":{request options, allowCredentials = the person's passkeys}}` |
| `POST /auth/stepup/finish` | session | `{"ceremonyId":"…","credential":{…}}` | `200 {"stepUpUntil":"…"}`; `401 login_failed` (an assertion of another person counts as failed) |
| `POST /auth/logout` | session | `{}` | `204`, cookie cleared, session deleted |

Register rules:
- `token` is a setup link (`kind: "setup"`) or an invite (`kind: "member"`, [P3]). It is checked at `begin` and
  consumed at `finish`, inside one `store.InTx` with: creating the person (or using the one the token names), storing
  the passkey, saving `homeName` (setup only) and creating the session.
- `personName` and `homeName` are used only with a setup token. Without them, the existing person name and home name
  stay.
- Every display name (person, home, passkey; here and in `PATCH /me`, `PATCH /settings`, passkey renames) is 1–40
  characters after trimming, with no control characters, line or paragraph separators, bidirectional overrides,
  double quotes, backslashes or backticks: names reach the assistants' instructions and the MCP title (T2.F).
- `passkeyName` defaults from the User-Agent ("MacBook Pro", "iPhone").
- Setup and invite tokens are 32 random bytes, base64url; links carry them in the **fragment** (`#t=`), so neither
  Caddy's nor frps's logs nor a `Referer` ever contain them.

### 3.3 Me (any person, about themselves)
| Method, path | Level | Request | Response |
|---|---|---|---|
| `PATCH /me` | session | `{"name?":"Роман","language?":"en"}` | `200 Person`; a change is audited as `person.update` |
| `GET /me/passkeys` | session | — | `200 {"items":[Passkey…]}` |
| `POST /me/passkeys/begin` | self ✋ | `{}` | `200 {"ceremonyId","publicKey":{creation options, excludeCredentials = own passkeys}}` |
| `POST /me/passkeys/finish` | self ✋ | `{"ceremonyId","credential","name":"iPhone"}` | `201 Passkey`; `409 passkey_exists` |
| `PATCH /me/passkeys/{id}` | session | `{"name":"iPhone"}` | `200 Passkey` |
| `DELETE /me/passkeys/{id}` | self ✋ | — | `204`; `409 last_passkey` (it is the last one of the current relay domain; passkeys of an old domain can always go). Sessions signed in with it are deleted too. |
| `GET /me/sessions` | session | — | `200 {"items":[Session…]}` |
| `DELETE /me/sessions/{id}` | session | — | `204` (deleting `current` is a logout) |
| `POST /me/sessions/revoke-others` | session | `{}` | `200 {"revoked":2}` |

### 3.4 Home overview
| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /overview` | owner | — | `200` below |

```json
{"home":{"name":"Мой шалаш"},"relay":{RelayStatus},
 "plugins":[PluginSummary…],
 "attention":[{"kind":"plugin_failing","target":"tv"},{"kind":"plugin_update","target":"tv","version":"0.3.0"},
              {"kind":"hub_update","version":"0.3.0"},{"kind":"relay_offline"},{"kind":"relay_unreachable"},
              {"kind":"connections_stale","count":2},{"kind":"connection_failed","target":"tanya"}],
 "recent":[AuditItem…]}
```
- `attention` is ordered by severity; the SPA turns each `kind` into a row (DESIGN.md §8.3). `relay_unreachable` =
  connected but the self-check fails. `connections_stale` and `connection_failed` are [P3].
- `recent` = the last 5 `kind: "tool"` records with `via` `site` or `mcp`.

### 3.5 Plugins and tools
| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /plugins` | owner | — | `200 {"items":[PluginSummary…]}` |
| `GET /plugins/{id}` | owner | — | `200 PluginDetail` |
| `PATCH /plugins/{id}` | owner | `{"enabled":false}` | `200 PluginSummary`; disabling stops the process and hides its tools from everybody |
| `POST /plugins/{id}/restart` | owner | `{}` | `202 PluginSummary` (state `starting`) |
| `GET /plugins/{id}/inventory` | owner | — | `200 {"items":[InventoryItem…],"fetchedAt":"…"}`; `404 no_inventory`, `422 tool_failed`, `503 plugin_unavailable`, `504 timeout` |
| `GET /plugins/{id}/logs` | owner | `?tail=200` (1–1000) | `200 {"lines":["…"]}`, oldest first; live lines via SSE `?logs=<id>` |
| `GET /plugins/preview` | owner | `?repo=devXpro/homie-tv&version=latest` | `200` below; `404 not_found` (no repo or release), `502 upstream` |
| `POST /plugins` | owner ✋ | `{"repo":"devXpro/homie-tv","version?":"0.2.0","id?":"tv"}` | `202 Job` (`plugin.install`); `409 conflict` (id taken); `409 job_running` |
| `POST /plugins/{id}/update` | owner ✋ | `{"version?":"0.3.0","allowDowngrade?":true}` | `202 Job` (`plugin.update`); `409 job_running`; `409 downgrade`; `403 forbidden` (`allowDowngrade` from the web) |
| `DELETE /plugins/{id}` | owner ✋ | `?keepData=false` | `202 Job` (`plugin.remove`); `409 job_running`; data dir deleted unless `keepData=true` |
| `POST /plugins/check-updates` | owner | `{}` | `200 {"items":[PluginSummary…]}` with `update` refreshed (GitHub, cached 6 h otherwise) |
| `GET /tools` | owner | `?audience=admin\|assistant&plugin=tv` | `200 {"items":[Tool…]}`; `audience=admin` (default) lists every tool |
| `POST /tools/{name}/calls` | owner | `{"args":{…}}`, `?wait=1` optional | `202 Call` (running), or with `wait=1`: `200 Call` (finished) |
| `GET /calls` | owner | `?state=running` | `200 {"items":[Call…]}` (newest first) |
| `GET /calls/{id}` | owner | — | `200 Call` |
| `DELETE /calls/{id}` | owner | — | `202 Call` (cancel requested; ends as `cancelled`) |
| `GET /jobs` | owner | `?state=running` | `200 {"items":[Job…]}` |
| `GET /jobs/{id}` | owner | — | `200 Job` |

`GET /plugins/preview` answer (from the release's `homie-plugin.json`):
```json
{"repo":"devXpro/homie-tv","id":"tv","title":"TV","description":"LG webOS TVs: power, volume, inputs.",
 "version":"0.2.0","publishedAt":"…","homepage?":"https://github.com/devXpro/homie-tv","minHubVersion":"0.2.0",
 "compatible":true,"assetFound":true,"installed?":{"version":"0.1.0"}}
```
`homepage` is the plugin author's text: it is sent only when it is an absolute `https` URL with a host and no
credentials, and the SPA shows it as a link of its own; the source link is always `https://github.com/<repo>`.

Jobs and versions:
- One job at a time per plugin: an install, an update and a removal of the same plugin id refuse each other with
  `409 job_running` (an install by repo learns its id from the release, so the job itself may end `job_running`).
- Downgrades: an update to a release older than the installed one (the hub's: older than the running homed) is
  refused. The web never downgrades: an explicit older `version` answers `409 downgrade`, `allowDowngrade` answers
  `403 forbidden`, and a `"latest"` that resolves to an older release ends the job with `downgrade`. Only the socket
  may send `"allowDowngrade": true` (e.g. `curl --unix-socket <data_dir>/homed.sock`, DEV.md).

Tool call rules:
- Any tool may be called (admin sees both audiences). Arguments are validated against `inputSchema` with
  `github.com/google/jsonschema-go` (already in go.sum through go-sdk) before the call: `400 invalid_args` with
  `fields`. `args` omitted = `{}`.
- The call runs through `hub.ToolHost.Call` with `Caller{Person: <the person's name>, Via: "admin"}` (`"cli"` on the
  socket) and the tool's own timeout, and is audited like any call.
- It is detached from the HTTP request: closing the page does not cancel it; `DELETE /calls/{id}` does.
- One running call per tool: a second `POST` for the same tool answers `409 call_running` with `"callId"`.
- A plugin known to be down answers `503 plugin_unavailable` at once; a failure during the call ends as
  `state: "error"`.
- Progress: when the plugin sends MCP `notifications/progress` for the call, `Call.progress` follows it and SSE
  `call` events carry it (§4 and §8).
- `GET /plugins/{id}/inventory` calls the inventory tool synchronously (Via `admin`), caches the answer for 10 s per
  plugin and counts its items into `PluginSummary.devices`.

### 3.6 Audit
| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /audit` | owner | filters below, `cursor`, `limit` | `200 {"items":[AuditItem…],"nextCursor?":"…"}` |
| `GET /audit/{id}` | owner | — | `200 AuditItem` |

Filters (all optional, combined with AND): `kind=tool|admin`, `person=<id>`, `plugin=<id>`, `tool=<name>`,
`via=site,mcp` (comma list), `status=ok|failed`, `since`, `until` (RFC 3339). Newest first. Records older than 180 days
are pruned when homed starts and then every 24 hours.

### 3.7 People and keys
| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /people` | owner | — | `200 {"items":[Person…]}` (phase 2: the owner and phase 1 imports) |
| `GET /people/{id}` | owner | — | `200 Person` |
| `GET /keys` | owner | `?person=<id>` | `200 {"items":[Key…]}` |
| `POST /people/{id}/keys` | owner ✋ | `{"label":"ChatGPT Site"}` | `201 {"key":Key,"secret":"ghk_…"}`; the resolver is updated before the answer |
| `DELETE /keys/{id}` | owner ✋ | — | `204`; the resolver is updated before the answer |
| `POST /people` [P3] | owner ✋ | `{"name":"Таня","plugins":["*"]}` | `201 {"person":Person,"invite":Invite}` (invite create = step-up) |
| `PATCH /people/{id}` [P3] | owner | `{"name?":"…","plugins?":["tv"]}` | `200 Person`; scope changes reach keys at once and mark connections stale |
| `DELETE /people/{id}` [P3] | owner ✋ | — | `204`; revokes keys, sessions, invites and connections. The owner cannot delete themselves (`409 conflict`) |
| `POST /people/{id}/invite` [P3] | owner ✋ | `{}` | `201 Invite` (a new link; older ones of this person are revoked) |
| `DELETE /invites/{id}` [P3] | owner | — | `204` |

### 3.8 Connections [P3]
| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /connections` | owner | — | `200 {"items":[Connection…]}` |
| `GET /me/connection` | session | — | `200 Connection`; `404 not_found` when none |
| `POST /people/{id}/connections` | owner ✋, or self during `/join` | `{}` (kind = `settings.connectionKind`) | `202 Connection` (usually `needs_login` with a `prompt`) |
| `POST /connections/{id}/publish` | owner ✋ or self ✋ | `{}` | `202 Job` (`connection.publish`) |
| `POST /connections/republish` | owner ✋ | `{}` | `202 Job` (`connections.republish`, one item per person with a connection) |
| `DELETE /connections/{id}` | owner ✋ or self ✋ | — | `204`; revokes its key or OAuth tokens |

- The join wizard creates the member's own connection right after registration; the fresh registration counts as
  step-up.
- Path A has nothing to republish: its items end `skipped` with the message "ChatGPT picks up changes by itself".
- Every publish ends with the SPA note that ChatGPT may take about 10 minutes to see new tools.

### 3.9 Settings, relay, system
| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /settings` | owner | — | `200 Settings` |
| `PATCH /settings` | owner | `{"homeName?":"…","language?":"en","siteSignOut?":true}` | `200 Settings`; a new home name changes `/mcp` and the instructions at once and marks connections stale [P3] |
| `GET /relay` | owner | — | `200 RelayStatus` |
| `POST /relay/check` | owner | `{}` | `200 RelayStatus` after a self-check run now |
| `PUT /relay` | owner ✋ | `{"code":"ghr1.…"}` | `202 RelayStatus` (`connecting`); `400 invalid_args` (bad code); `409 domain_change_local_only` |
| `GET /system` | owner | — | `200 SystemInfo` |
| `POST /system/check-update` | owner | `{}` | `200 SystemInfo` (refreshed from GitHub) |
| `POST /system/update` | owner ✋ | `{"version?":"0.3.0","allowDowngrade?":true}` | `202 Job` (`hub.update`); `409 downgrade`; `403 forbidden` (`allowDowngrade` from the web, §3.5); homed exits after `replace`, the service manager restarts it, the SPA waits for SSE `hello` with the new version |

`PUT /relay` from the web accepts only a code for the **current** domain (a token rotation). A new domain would
change the WebAuthn RP ID and lock every passkey out, so it is done on the home box: `homed relay connect -`,
then `homed admin reset-access`.

## 4. Server-sent events — `GET /api/admin/events`
- Level: session (members get the subset below). `Content-Type: text/event-stream`; through Caddy and frps this works
  because the stream is a plain HTTP response (`stream_close_delay` covers reloads).
- Frames: `id: <seq>`, `event: <type>`, `data: <one line of JSON>`. The server sends `retry: 3000` first, a comment
  `: ping` every 20 s, and closes the stream when the session ends.
- `Last-Event-ID` resumes from a ring of the last 500 events. When the id is older than the ring, or from another
  hub run, the first event is `resync` and the SPA refetches everything.
- `?logs=<plugin id>` (owner) adds `plugin.log` events for that plugin.
- Every event carries the full current object, not a diff, so a client can simply replace what it holds.

| `event` | `data` | Sent when | Members |
|---|---|---|---|
| `hello` | `{"version":"0.2.0","serverTime":"…"}` | on connect | yes |
| `resync` | `{}` | resume impossible | yes |
| `relay` | `RelayStatus` | connected/disconnected, self-check result changed | no |
| `plugin` | `PluginSummary` | state, version, enabled or update info changed | no |
| `plugin.removed` | `{"id":"tv"}` | after a remove | no |
| `tools` | `{"pluginId":"tv"}` | the plugin's tool set or instructions changed (`ToolHost.Changed`) | no |
| `call` | `Call` | call started, progress (at most 4 per second per call), finished | no |
| `job` | `Job` | job started, step or item changed, finished | no |
| `audit` | `AuditItem` | a record was stored | no |
| `settings` | `Settings` | settings changed (another device) | `{"homeName"}` only |
| `person` [P3] | `Person` | created or changed | own only |
| `person.removed` [P3] | `{"id":"tanya"}` | removed | no |
| `connection` [P3] | `Connection` | state, prompt or stale flag changed | own only |
| `session.revoked` | `{}` | this session was revoked or expired; the stream closes | yes |
| `plugin.log` | `{"pluginId":"tv","lines":["…"]}` | new stderr lines (batched per 250 ms) | no |

## 5. First-run LAN page (setup listener, T2.2, hardened in T2.F)
Plain HTTP, so it is short-lived and narrow (docs/SECURITY.md):
- It listens on `setup_listen`; by default on the home network (`0.0.0.0:8090`) only while homed has no relay, and
  on `127.0.0.1:8090` once it has one (then the page only issues the link, which the home box opens or shows as a QR
  code).
- It opens when homed starts while no owner has a passkey of the current relay domain, and closes 30 minutes later,
  or for good after 20 wrong PINs from anyone; `homed admin firstrun` (socket `POST /local/firstrun`, §6) opens it
  again with a new PIN, and `homed admin setup-link` never needs it. Once an owner has such a passkey every path
  answers `410 setup_done`; while it is closed, `410 setup_closed`.
- The PIN has 8 digits (shown `1234 5678`; spaces and dashes are ignored) and changes every 10 minutes and after
  every issued link. homed prints it only to a terminal; under launchd or systemd it never reaches the log (the log
  says to run `homed admin firstrun` or `homed admin setup-link`).
- Same error format and `X-Homie: 1` rule; the cookie is `homie_firstrun` (`HttpOnly; SameSite=Strict`, not
  `Secure`: this is http on the LAN) and lives 30 minutes.

| Method, path | Level | Request | Response |
|---|---|---|---|
| `GET /api/firstrun/state` | public | — | `200 {"step":"pin"}`, or with the cookie `{"step":"relay\|connecting\|link","homeName":"…","relay?":RelayStatus}` |
| `POST /api/firstrun/pin` | public | `{"pin":"12345678"}` | `204` + cookie; `401 wrong_pin`; `429 rate_limited` (this device: 5 wrong PINs per 10 minutes); `410 setup_closed` (the 20th wrong PIN closes the page) |
| `POST /api/firstrun/relay` | cookie | `{"code":"ghr1.…"}` | `202 RelayStatus`; `409 conflict` when homed already has a relay (it is changed only on the home box or with step-up in the admin) |
| `GET /api/firstrun/events` | cookie | — | SSE with `relay` events only |
| `POST /api/firstrun/link` | cookie | `{}` | `200 {"url":"https://<domain>/admin/setup#t=<token>","expiresAt":"…"}`; `409 relay_not_ready` |

A link from this page is valid 3 minutes and once (the CLI's links: 15 minutes, PLAN §5.8); each `link` call revokes
the older setup links and changes the PIN. Every link and relay code from this page is logged and audited with the
client's LAN address.

## 6. Local socket (`<data_dir>/homed.sock`)
- HTTP/1.1 over the unix socket, mode 0600; homed also checks that the peer's uid is its own (`getpeereid` /
  `SO_PEERCRED`). The CLI talks to it with an `http.Client` whose transport dials the socket; the Host is `homed`.
- The same router and the same paths and shapes as the web. The principal is the owner (or, before one exists, a
  local principal named after the OS user), `Via: "cli"`. No cookie, no CSRF header, no step-up: access to the socket
  already means access to the data dir.
- Not served on the socket: `/auth/*`, `/me/*`, `/bootstrap`, `/session`. `GET /events` is served (the CLI follows a
  long `tv_pair` with it).
- Socket-only endpoints:

  | Method, path | Request | Response |
  |---|---|---|
  | `POST /local/setup-link` | `{}` | `200 {"url":"https://<domain>/admin/setup#t=…","expiresAt":"…"}` (`homed admin setup-link`) |
  | `POST /local/reset-access` | `{}` | `200 {"url","expiresAt","revokedPasskeys":2,"revokedSessions":3}` (`homed admin reset-access`) |
  | `POST /local/firstrun` | `{}` | `200 {"urls":["http://192.168.68.63:8090/firstrun",…],"pin":"12345678","pinUntil":"…","closesAt":"…"}` (`homed admin firstrun`): opens the first-run page when it is closed, else shows it; `409 owner_exists`; `409 conflict` (cannot listen). Running homed only. |
  | `POST /plugins` with `{"source":"local","id":"tv","command":["/…/homie-tv","serve"]}` | | `201 PluginSummary` (`homed plugin add`); the web can only install from GitHub |
  | `PUT /relay` with a code for any domain | | `202 RelayStatus` (`homed relay connect -`) |
  | `POST /plugins/{id}/update`, `POST /system/update` with `"allowDowngrade": true` | | `202 Job`: the only way to install an older release (§3.5) |

- **Phase 1 → phase 2.** T1.3's socket served `GET /v1/tools`, `POST /v1/tools/{name}`, `GET /v1/plugins`,
  `GET /v1/relay` and `POST /v1/reload`. Since T2.6 the socket serves only `/api/admin/*` and the CLI uses
  `GET /api/admin/tools?audience=admin`, `POST /api/admin/tools/{name}/calls` with `{"args":{…}}` (then polls
  `GET /calls/{id}` to show the progress), `GET /api/admin/plugins`, `GET /api/admin/relay`, and the key, plugin,
  relay and `/local/*` endpoints above; `/v1/reload` is gone because the store is read live (SIGHUP re-reads it).
  While homed is stopped, the CLI serves the same socket routes in-process over the data dir.

## 7. Admin audit actions
`kind: "admin"` records, `via` = `admin` or `cli`. `target` in brackets.

| Action | When |
|---|---|
| `auth.setup` | the owner registered through a setup link [person]; a failed finish (passkey not verified, link used or expired, passkey exists) is the same action with `ok: false` |
| `auth.join` [P3] | a member registered through an invite [person]; failures as for `auth.setup` |
| `auth.login` / `auth.login_failed` | sign-in; failures carry no person |
| `auth.step_up` | a step-up assertion succeeded, or failed (`ok: false`) |
| `auth.logout` | logout |
| `passkey.add` / `passkey.rename` / `passkey.remove` | [passkey name]; a failed `passkey.add` has `ok: false` |
| `session.revoke` | [session device] |
| `key.create` / `key.revoke` | [key id] |
| `person.update` | `PATCH /me` changed the person's name or language [person]; details = the changed keys |
| `person.create` / `person.update` / `person.remove` [P3] | [person] |
| `invite.create` / `invite.revoke` [P3] | [person] |
| `connection.create` / `connection.publish` / `connection.revoke` [P3] | [person] |
| `plugin.install` / `plugin.update` / `plugin.remove` / `plugin.enable` / `plugin.disable` / `plugin.restart` | [plugin] |
| `relay.change` | [domain] |
| `settings.update` | details = the changed keys |
| `hub.update` | [version] |
| `access.reset` | `homed admin reset-access` |
| `setup.link` | a setup link was made (LAN page or CLI) |

- Details `{"client":"…"}` name where a request came from: `auth.login_failed`, registrations (`auth.setup`,
  `auth.join`), failed `auth.step_up` and `passkey.add` (the address behind the relay, §1.7), and `setup.link` and
  `relay.change` from the first-run page (the device's LAN address).
- Arguments and details pass through `audit.Redact`; tokens, keys, codes and credentials never reach the log. The
  error text of a tool call is the gateway's (`gateway.AuditError`): a validation failure that may quote a redacted
  argument is stored as "invalid arguments", for the admin's calls as for the assistants'.

## 8. Notes for the implementers
- **Progress from plugins (T2.3).** `hub.ToolHost.Call` has no progress channel and `hub.go` is frozen. Proposal,
  without touching `hub.go`: `internal/plugins` gets `WithProgress(ctx, func(Progress)) context.Context`; `Host.Call`
  then sets a progress token on `CallToolParams` (`SetProgressToken`) and routes the client's
  `ProgressNotificationHandler` (go-sdk v1.8.0 `ClientOptions`) to that callback. `pluginkit` gets
  `Progress(ctx, req, progress, total float64, message string)`, and homie-tv's `tv_pair` sends
  "Press OK on the TV remote" once the prompt is on screen. Until then the SPA falls back to the tool description and
  an elapsed timer (DESIGN.md §9.5). These touch `internal/plugins`, `pluginkit` and homie-tv: the orchestrator assigns
  them.
- **`x-homie-ref`.** The inventory link of a tool argument (`Tool.target`) should be declared by plugins:
  `"tv": {"type":"string","x-homie-ref":"inventory",…}`. docs/PLUGINS.md and homie-tv need that line; until then the
  name heuristic of §2 covers homie-tv (`tv` = the plugin id).
- **Challenges** for ceremonies live in memory in `internal/auth` (no store table): one process, 5-minute TTL.
- **Keys:** after every key create, revoke or scope change, rebuild `keys.Resolver` from `store.Keys.List` plus the
  people's scopes (`Resolver.Update`).
- **Audit writer:** keep `hub.Auditor` non-blocking; batch into `store.Audit.Append`, then publish `audit` SSE events
  with the ids `Append` returned.
- **Instructions and `/mcp` title** read `settings.homeName` instead of `homed.yaml` from phase 2 on.
- **The SPA** is served at `/admin/*` and `/join` with an `index.html` fallback (T2.4); `/admin/setup` is a SPA route.
