# Homie — implementation plan

Architecture approved by Roman on 2026-11-15. This file is the execution plan for the orchestrator (the main Claude
session) and the subagents it launches. Every task card in §7 is self-contained once sections 2–6 and 9 are read.

## 0. How to use this plan

**Orchestrator**
- Run the waves of §7.0 in order and launch one subagent per task card, with the model and effort the card names.
- Give each agent the prompt template of §10. Agents read this file themselves.
- Keep the live actions of §8 for itself: jobby, ChatGPT accounts, GitHub, TV pairing and commands.
- Commit and push on its own at sensible checkpoints: before risky changes and after accepted tasks (Roman's order,
  2026-11-15). No Co-Authored-By trailers, no force-push to `main`.
- At every phase gate (§7.6): tests and lint green, review findings triaged, then a demo to Roman. Otherwise come
  back to Roman only with a finished result or a decision he must make.

**Agents**
- Read §2 (decisions), §3 (architecture), §4 (layout), §5 (contracts), §6 (rules) and §9 (environment), then your
  card.
- Do only your card, inside its "Owns" paths.
- Report in the format of §6.10.

## 1. Goal and non-goals

Homie is an LLM-first home hub. ChatGPT (voice and chat) controls home devices through a self-hosted relay. Every
device family is a plugin. The admin is iPhone-grade, and the whole thing installs in minutes. The first device is an
LG webOS TV.

It must stay light, so these are **non-goals**:
- dashboards of device cards;
- a global entity, state or history model in the core;
- device code in the core (it all lives in plugins);
- an automation engine in the core;
- any cloud account of our own;
- Home Assistant, Tailscale or Cloudflare proxying.

## 2. Locked decisions

| Topic | Decision |
|---|---|
| Relay | frp v0.72.0, self-hosted. `frps` on the server behind Caddy. The home side dials out over `wss` on 443. |
| Domain | Mandatory, with an A record. Roman's is `homie.jobbyjobs.work` → `169.40.15.46`, DNS-only (grey cloud), already in place. One name serves both the frpc channel (`/~!frp`) and the public traffic. |
| Server role | A dumb relay: Caddy + frps only. No keys, no admin, no plugins, no database. |
| Home role | `homed` (the hub) holds everything: admin, passkeys, people and keys, audit, plugins, the relay client. |
| Plugins | Separate executables at home. They speak MCP over stdio to the hub, can be in any language, and are released from their own repos. The first one is `homie-tv`. |
| ChatGPT side | Every person gets their own "home" plugin, named by Roman (e.g. «Мой шалаш»). Path **B** is a ChatGPT Site in that person's account calling the hub's REST façade; it is the proven pattern from librus-gpt. Path **A** is a custom MCP plugin added by URL, plus OAuth. T0.5 picks the primary path; the other stays the fallback. |
| Admin | React + Vite + TypeScript + Tailwind + shadcn/ui + motion. Dark by default. The UX is iPhone-like: few sections, one action per screen, human words. It ships in Russian and English, is embedded in the `homed` binary and is served at `https://<domain>/admin`. |
| Admin login | Passkeys (WebAuthn: Touch ID / Face ID), verified at home. The first passkey comes only from a one-time link the home box shows locally. Dangerous actions need a fresh passkey assertion (step-up). |
| Repos | `devXpro/homie` (public: hub, relay, admin, Site template, installers, docs) and `devXpro/homie-tv` (public: plugin). No secrets in either. |
| Install | Server: one command on a Docker VPS. One question (the domain); standalone, or behind an existing proxy. Home: Homebrew (Mac), a one-line script (Raspberry Pi / Linux), or a Docker image (NAS). A browser wizard follows. |

## 3. Architecture

```
ChatGPT (Roman / Tanya; voice and chat)
  │  path B: the person's Site → REST /api/v1/tools[/{name}] + Bearer person key
  │  path A: custom plugin   → MCP /mcp + OAuth (phase 3, only if chosen)
  ▼
https://homie.jobbyjobs.work
jobby: shared Caddy (TLS, Let's Encrypt) → homie-frps:7000
                                            ├─ "GET /~!frp…" → the frpc channel from home (token auth)
                                            └─ everything else → over that channel to homed
  ┆ one persistent wss connection, opened BY the home box
home box (Mac now, Raspberry Pi later): homed, 127.0.0.1:8080
  ├─ /api/v1/tools…   REST façade for Sites (bearer → person → scopes → audit)
  ├─ /mcp             MCP endpoint (same auth model)
  ├─ /admin, /api/admin/…  admin SPA and API (passkey sessions)      [phase 2]
  ├─ /join/{token}    invite wizard for family members               [phase 3]
  ├─ /healthz         {"ok":true}
  └─ plugin host ── stdio MCP ──▶ homie-tv serve ──LAN──▶ LG TV
                               └▶ homie-kettle serve … (future)
```

**Assistant call (path B)**
1. ChatGPT calls tool `tv_power_on` on the person's Site.
2. The Site sends `POST https://homie…/api/v1/tools/tv_power_on` with the person's bearer key.
3. The request goes Caddy → frps → channel → homed.
4. homed checks the key and the person's scopes, calls the plugin over stdio and writes the audit entry.
5. The plugin talks to the TV and returns its result. The reply travels back the same way.

**Home offline.** No frpc is connected, so frps answers with its `custom404Page`: an HTML "home is offline" page with
status 404. The Site recognizes a non-JSON 404 and tells the user the home is offline.

**Trust.** The server terminates TLS, so it sees request contents. It cannot forge passkey assertions, so on its own
it cannot start a step-up action. It is still trusted like the domain itself: a compromised server could steal a
fresh setup link or a live session, or serve tampered admin pages while the admin is using them (docs/SECURITY.md).
Plugins are not reachable from the network, only through the hub.

## 4. Repositories and layout

Local working copies live in `/Users/devxpro/projects/family/`.

```
homie/                      module github.com/devXpro/homie
  cmd/homed/                   hub daemon + CLI
  internal/hub/                shared types and interfaces (§5.3), written in T0.0, changed only by the orchestrator
  internal/config/             homed.yaml, data dir (§5.6)
  internal/relay/              embedded frp client, status, self-check (§5.5)
  internal/plugins/            plugin host: spawn, MCP stdio client, tool registry, restarts
  internal/gateway/            REST façade + MCP endpoint + auth + scopes + rate limit (§5.4)
  internal/audit/              audit log
  internal/keys/               person keys (phase 1: in homed.yaml)
  internal/store/              SQLite (phase 2)
  internal/auth/               passkeys, sessions, step-up (phase 2)
  internal/admin/              admin JSON API (phase 2)
  internal/web/                go:embed of the built admin SPA (phase 2)
  internal/sites/              ChatGPT Site publisher (phase 3, path B)
  internal/oauth/              OAuth 2.1 server (phase 3, path A only)
  pluginkit/                   public Go helper for plugin authors (§5.2)
  web/                         admin SPA source (phase 2)
  site/                        ChatGPT Site template, from librus-gpt/site (§5.7)
  relay/                       server side: compose files, frps.toml, offline page, proxy snippets, docker test harness
  install/                     server.sh (phase 0), home.sh + service files (phase 4)
  docs/                        PLAN.md (this), PLUGINS.md, RELAY.md, ADMIN-API.md, DESIGN.md, SECURITY.md, QUICKSTART.md,
                               TESTING.md, research/
  .github/workflows/  .golangci.yml  .goreleaser.yaml  Makefile  LICENSE  README.md

homie-tv/                        module github.com/devXpro/homie-tv  (GitHub repo exists, empty; local code uncommitted)
  cmd/homie-tv/                  `homie-tv serve` (MCP stdio), `homie-tv discover` (debug)
  tv/                          brand-neutral TV interface + Input type (exists, tested)
  provider/lgwebos/            LG webOS SSAP client (exists, NOT compiled yet, no tests yet)
  internal/wol/                Wake-on-LAN (exists, tested)
  internal/store/              tvs.json in HOMIE_DATA_DIR
  internal/discovery/          SSDP + UPnP description + ARP
  internal/config, internal/keychain   (exist; to be DELETED, replaced by the store)
  docs/families/lg-webos.md
```

## 5. Contracts

### 5.1 Plugin protocol (hub ⇄ plugin)

**Process**
- The hub starts `command[0] command[1:]…` (default args `["serve"]`) with these settings:
  - cwd = the plugin data dir;
  - a minimal env (`PATH`, `HOME`, `LANG`, `TZ`) plus `HOMIE_PLUGIN_ID`, `HOMIE_DATA_DIR` (per plugin,
    persistent, mode 0700), `HOMIE_LOG_LEVEL` (`debug|info|warn|error`) and `HOMIE_HUB_VERSION`.
- stdin/stdout carry MCP over stdio: newline-delimited JSON-RPC, the protocol version the official go-sdk v1.8.0
  negotiates.
- stderr is the plugin's log. The hub keeps the last 1000 lines in memory and in `<data_dir>/logs/<id>.log`
  (rotated at 5 MB, 3 files).

**Identity**
- Plugin id: `^[a-z][a-z0-9]{1,15}$`, e.g. `tv`, `kettle`.
- `initialize` → `serverInfo.name` = the repo or binary name (`homie-tv`), `serverInfo.title` = a human name ("TV"),
  `serverInfo.version` = semver.
- Optional `instructions`: short English guidance for the model. The hub merges them into its own instructions.

**Tools**
- Name `^<id>_[a-z][a-z0-9_]*$`. The hub skips any other name and logs a warning.
- `description` is English, written for an LLM, one or two sentences, with units and ranges stated.
- `inputSchema` is a JSON Schema object. `outputSchema` is optional.
- Annotations follow MCP: `readOnlyHint: true` on every tool that changes nothing; `destructiveHint` and
  `idempotentHint` where they apply.
- `_meta` keys the hub understands (all optional):
  - `"homie/audience": "admin"` — hidden from assistants; shown in the admin UI and the CLI. The default is
    `"assistant"`.
  - `"homie/timeout": <seconds>` — the hub-side call timeout. Default 30, maximum 300. Pairing flows use 180.
  - `"homie/role": "inventory"` — on at most one read-only tool. It returns
    `{"items":[{"id","name","subtitle"?,"state"?}]}`, where `state` is one of
    `on|off|offline|unknown|paired|unpaired`. The admin renders it as the device list.
  - `"homie/i18n": {"ru": {"title", "description", "properties": {"<input property>": "<label>"}}}` —
    translated texts for the admin UI (→ `hub.Tool.I18n`). Assistants always get the English texts. Added
    2026-11-15 by the orchestrator.
- An input property schema may carry `"x-homie-ref": "inventory"`. It means the value is a device id from this
  plugin's inventory tool, and the admin renders a device picker for it.
- **Progress.** Long-running tools (pairing) may send MCP progress notifications for the hub's `progressToken`, with
  an English `message` and an optional notification `_meta` `"homie/i18n": {"ru": "<message>"}`. The hub forwards
  them to the admin UI (T2.7, ADMIN-API §8).

**Calls**
- The hub adds request `_meta` `{"homie/caller":"<person name>","homie/via":"site|mcp|admin|cli"}`. Plugins may
  log it and must not trust it for security.
- Results:
  - text content: short, English, human, e.g. "Living room TV is on.";
  - optional `structuredContent`;
  - a tool that ran but failed (device unreachable, not paired) returns `isError: true` with a text that says what to
    do;
  - JSON-RPC errors mean "could not run": bad arguments, internal error.

**Lifecycle**
- The hub calls `tools/list` after `initialize` and again on `notifications/tools/list_changed`.
- A crashed plugin is restarted with backoff 1, 2, 4… up to 60 s. After 5 crashes in 5 minutes its state becomes
  `failing` and it is retried every 5 minutes.
- Shutdown: the hub closes the session, sends SIGTERM, then SIGKILL after 5 s.

**Release assets (phase 2 installs)**
- GoReleaser archives `<binary>_<version>_<os>_<arch>.tar.gz` plus `checksums.txt`.
- `homie-plugin.json`: `{"id","title","description","binary","args":["serve"],"minHubVersion","homepage"}`.

### 5.2 `pluginkit` (Go helper; T1.1 builds it, T1.4 consumes it)

```go
package pluginkit // github.com/devXpro/homie/pluginkit

type Plugin struct {
	ID           string // "tv"
	Name         string // "homie-tv" (serverInfo.name)
	Title        string // "TV"
	Version      string
	Instructions string // optional, English
}

func New(p Plugin) *mcp.Server                             // go-sdk server with identity and instructions set
func Admin(t *mcp.Tool) *mcp.Tool                          // sets _meta "homie/audience": "admin"
func Timeout(t *mcp.Tool, d time.Duration) *mcp.Tool       // sets _meta "homie/timeout"
func Inventory(t *mcp.Tool) *mcp.Tool                      // sets _meta "homie/role": "inventory"
func DataDir() (string, error)                             // HOMIE_DATA_DIR, or ./data (0700) in development
func Caller(req *mcp.CallToolRequest) (person, via string) // reads the hub's request _meta
func Serve(ctx context.Context, s *mcp.Server) error       // stdio until ctx is done or stdin closes
func CheckToolName(pluginID, tool string) error            // the §5.1 rule; the hub uses it too
```

The helpers return the same `*mcp.Tool`, so they chain: `pluginkit.Admin(pluginkit.Timeout(tool, 3*time.Minute))`.
Verify every go-sdk name against `/Users/devxpro/go/pkg/mod/github.com/modelcontextprotocol/go-sdk@v1.8.0` before
relying on it. If the SDK forces a different shape, keep the intent and document the change in your report.

### 5.3 Shared hub types — `internal/hub/hub.go` (written verbatim by T0.0)

```go
// Package hub holds the types the hub's packages share, so they can be built in parallel.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Audience says who may see a tool.
type Audience string

const (
	AudienceAssistant Audience = "assistant" // ChatGPT and other assistants
	AudienceAdmin     Audience = "admin"     // the admin UI and the homed CLI only
)

// Tool is one plugin tool as the hub exposes it.
type Tool struct {
	PluginID     string
	Name         string          // "<plugin id>_<verb>", unique across the hub
	Title        string
	Description  string
	InputSchema  json.RawMessage // JSON Schema object
	OutputSchema json.RawMessage // optional
	Annotations  json.RawMessage // MCP tool annotations, passed through unchanged; optional
	Audience     Audience
	Inventory    bool          // _meta "homie/role": "inventory"
	Timeout      time.Duration // from _meta "homie/timeout", default 30s, max 300s
}

// Result is the outcome of a tool that ran.
type Result struct {
	Text       string          // the plugin's text content, joined with newlines
	Structured json.RawMessage // structuredContent, if any
	IsError    bool            // the tool ran and reported a failure
}

// Caller says who asked; it goes to the audit log and to the plugin as request _meta.
type Caller struct {
	Person string // display name, e.g. "Roman"
	Via    string // "site", "mcp", "admin" or "cli"
}

// PluginState is the life state of one plugin process.
type PluginState string

const (
	PluginStarting PluginState = "starting"
	PluginUp       PluginState = "up"
	PluginFailing  PluginState = "failing"
	PluginStopped  PluginState = "stopped"
)

// PluginStatus reports one plugin for health checks and the admin.
type PluginStatus struct {
	ID        string
	Title     string
	Version   string
	State     PluginState
	LastError string
	Since     time.Time
}

// ToolHost runs the plugins and calls their tools.
type ToolHost interface {
	// Tools returns a snapshot of the tools visible to the audience, sorted by name.
	// AudienceAdmin sees every tool; AudienceAssistant sees only assistant tools.
	Tools(audience Audience) []Tool
	// Call runs a tool. err is non-nil when the call could not be made (ErrUnknownTool, ErrPluginDown,
	// context deadline). A tool that ran and failed returns Result.IsError with a nil err.
	Call(ctx context.Context, caller Caller, name string, args json.RawMessage) (Result, error)
	// Changed returns a channel that is closed at the next change of the tool set.
	Changed() <-chan struct{}
	// Status reports every plugin.
	Status() []PluginStatus
}

// Person may use assistant tools through their own keys.
type Person struct {
	ID      string   // stable, e.g. "roman"
	Name    string   // display name, e.g. "Roman"
	Plugins []string // plugin ids this person may use; ["*"] means all
}

// KeyResolver maps a bearer key to its person.
type KeyResolver interface {
	ResolveKey(ctx context.Context, key string) (Person, error) // ErrUnknownKey when nothing matches
}

// AuditEntry is one tool call, as stored.
type AuditEntry struct {
	At       time.Time
	Caller   Caller
	Tool     string
	Args     json.RawMessage // already redacted
	OK       bool            // false when err != nil or Result.IsError
	Error    string
	Duration time.Duration
}

// Auditor records tool calls; it must not block the call path for long.
type Auditor interface {
	Record(ctx context.Context, e AuditEntry)
}

var (
	ErrUnknownTool = errors.New("unknown tool")
	ErrPluginDown  = errors.New("plugin is not running")
	ErrUnknownKey  = errors.New("unknown key")
)
```

**Key format.** `ghk_` + base64url (no padding) of 32 random bytes. Only `sha256(key)` in hex is stored. Comparison
is constant-time.

### 5.4 Hub public HTTP surface (served on `127.0.0.1:8080`, reached through the relay)

Every response carries `X-Homie-Instance: <random id persisted in the data dir>`. The relay self-check uses it to
prove the public URL reaches *this* hub.

**`GET /healthz`** → `200 {"ok":true}`. No auth, no details.

**REST façade for Sites (path B).** The shapes are exactly what `librus-gpt/site/core/mcp.mjs` already consumes.
- Auth: `Authorization: Bearer <person key>`.
- `GET /api/v1/tools` → 200:
  ```json
  {"instructions":"…","tools":[{"name":"tv_power_on","title":"…","description":"…","inputSchema":{…},
    "outputSchema":{…},"annotations":{…}}]}
  ```
  It lists only assistant tools of the plugins the person may use. Absent optional keys are omitted, never `null`.
- `POST /api/v1/tools/{name}`, body = the JSON arguments object:

  | Status | Body | When |
  |---|---|---|
  | 200 | `Result.Structured` if present, else `{"text":"<Result.Text>"}` | the tool ran |
  | 422 | `{"error":"<Result.Text>"}` | the tool ran and failed (`IsError`) |
  | 400 | `{"error":"…"}` | invalid JSON, or the body is not an object |
  | 401 | `{"error":"unauthorized"}` | missing or unknown key |
  | 404 | `{"error":"unknown tool"}` | no such tool, or not allowed for this person (do not reveal which) |
  | 429 | `{"error":"rate limited"}` | over 120 calls per minute per key (token bucket) |
  | 503 | `{"error":"plugin unavailable"}` | `ErrPluginDown` |
  | 504 | `{"error":"timeout"}` | the tool timed out |

**`/mcp`** is MCP Streamable HTTP through go-sdk, stateless, with the same bearer auth and scopes.
- tools = the person's assistant tools.
- `serverInfo` = `{name:"homie", title:<home_name>, version:"<hub version>+<fingerprint>"}`, where the fingerprint
  is FNV-1a of the tools JSON plus the instructions (the same trick as the Librus Site, so ChatGPT notices tool
  changes).
- OAuth for path A is added in T3.3 only if path A wins.

**Instructions** = the hub preamble plus every enabled plugin's instructions. The hub preamble names the home
(`home_name`), says to act only when asked, to confirm what was done in one short sentence, and to say plainly when a
device is offline.

**Audit**
- Every call through any surface is recorded.
- Arguments are redacted: values of keys matching `(?i)(token|key|secret|password|pin|code)` become `"[redacted]"`.
- Phase 1 storage: JSON lines in `<data_dir>/audit/audit-YYYY-MM.jsonl`. Phase 2 moves it to SQLite.

### 5.5 Relay

**Server (`relay/`)**
- Image `ghcr.io/fatedier/frps:v0.72.0`, verified to exist for amd64, arm and arm64.
- `frps.toml`:
  ```toml
  bindPort = 7000
  vhostHTTPPort = 7000            # same port: frps muxes "GET /~!frp" (frpc wss) and vhost HTTP
  auth.method = "token"
  auth.tokenSource.type = "file"
  auth.tokenSource.file.path = "/etc/frp/token"
  custom404Page = "/etc/frp/offline.html"
  log.to = "console"
  log.level = "info"
  ```
- No frps port is published to the host.
- Behind an existing proxy, frps joins that proxy's docker network as container `homie-frps`.
- Standalone, the compose file adds `caddy:2.11.4-alpine`, which owns 80 and 443.
- Caddy block (shared Caddy on jobby, and the standalone Caddyfile template):
  ```
  homie.jobbyjobs.work {
  	reverse_proxy homie-frps:7000 {
  		stream_close_delay 30m
  	}
  }
  ```

**Connect code.** `ghr1.` + base64url (no padding) of `{"v":1,"domain":"<domain>","token":"<64 hex chars>"}`.
`install/server.sh` prints it once; `homed relay connect -` consumes it from stdin (never argv).

**Home client** (embedded `github.com/fatedier/frp/client` v0.72.0 if viable, else a supervised `frpc` binary)
- `serverAddr = <domain>`, `serverPort = 443`, `transport.protocol = "wss"`.
- The outer TLS is verified against system roots. A CA file is used for the docker test harness.
- Token auth; `loginFailExit = false`, so it retries forever.
- One proxy: `{name:"homed", type:"http", customDomains:[<domain>], localIP:"127.0.0.1", localPort:<listen port>}`.
- The status (`connected`, `since`, `lastError`) is exposed.
- Self-check every 30 s: `GET https://<domain>/healthz` must carry our `X-Homie-Instance`.

### 5.6 Config and data dir (home)

The data dir defaults to `~/Library/Application Support/homie` on macOS and `$XDG_DATA_HOME/homie` on Linux,
or `/var/lib/homie` when run as a system service. It is overridden by `--data-dir` or `HOMED_DATA_DIR`. Mode 0700.

```
<data_dir>/homed.yaml        config (phase 1 also holds people and key hashes)
<data_dir>/relay-token       0600
<data_dir>/instance-id
<data_dir>/homed.sock        local admin socket for the CLI, 0600 (phase 1)
<data_dir>/audit/            phase 1 JSONL
<data_dir>/logs/<plugin>.log
<data_dir>/plugins/<id>/data/   the plugin's HOMIE_DATA_DIR (0700)
<data_dir>/plugins/<id>/bin/<version>/   installed releases (phase 2)
<data_dir>/homed.db          SQLite (phase 2)
<data_dir>/sites/<person>/   per-person Codex home and Site checkout (phase 3, path B)
```

```yaml
# homed.yaml (phase 1)
home_name: "Мой шалаш"
listen: 127.0.0.1:8080
relay:
  domain: homie.jobbyjobs.work        # the token lives in relay-token
plugins:
  - id: tv
    command: ["/Users/devxpro/projects/family/homie-tv/bin/homie-tv", "serve"]
people:
  - id: roman
    name: Roman
    plugins: ["*"]
    keys:
      - sha256: "<hex>"
        label: "ChatGPT Site"
        created: 2026-11-15T12:00:00Z
```

### 5.7 Site template (`site/`, from `librus-gpt/site`)

- The same thin-proxy design as Librus. ChatGPT talks MCP to the Site, and the Site calls `GET /api/v1/tools` and
  `POST /api/v1/tools/{name}` on the hub.
- Environment: `HOME_API_URL`, `HOME_API_KEY` (secret), `HOME_LANGUAGE` (optional).
- `serverInfo.name` is `homie`. The Site title is a release parameter (e.g. «Мой шалаш»).
- Error mapping adds two cases to Librus:
  - `422` → the tool's own error text (`isError`);
  - a non-JSON `404` (the frps offline page) → "The home hub is offline right now (the home computer may be asleep
    or without internet). Try again later."
- `release/release.mjs` takes `--title`, `--slug`, `--description` and `--profile NAME`.
  - State goes to `~/.homie-site[-NAME]`; the key to Keychain item `homie-key[-NAME]`.
  - It keeps Librus's `--login`, `--logout`, `--check`, `--settings-only` and `--dry-run`.
  - New read-only `--list-sites-tools` dumps the `codex_apps` `sites.*` tool names and schemas.

### 5.8 Security rules (all phases)

- Secrets are never printed, logged, committed or put in chat. Files holding them are 0600. On the Mac they go in the
  Keychain. Logs redact them.
- Person keys and session ids are stored hashed.
- Admin (phase 2):
  - passkeys only; the RP ID is the relay domain; origin `https://<domain>`;
  - session cookie `HttpOnly; Secure; SameSite=Strict`, 12 h idle and 7 d absolute;
  - mutating admin requests also need the header `X-Homie: 1`;
  - step-up (an assertion no older than 5 min) for: plugin install, update or remove; key create or revoke; invite
    create; Site publish; passkey add or remove; relay change.
- Plugins never get the caller's keys. They are not network-reachable; the hub speaks to them over stdio only.
- The first passkey can only be created through a one-time link from the home box: shown on the LAN setup page
  behind a PIN, or by `homed admin setup-link` (valid 15 min). Since T2.F the LAN page lives 30 min (reopen with
  `homed admin firstrun`), listens on 0.0.0.0 only while no relay is configured (else 127.0.0.1), uses an 8-digit
  PIN that rotates every 10 min and after each link, caps failures per device (5) and in total (20), prints the PIN
  only to a terminal (never a log), and its links live 3 min.

## 6. Rules for every agent

1. **Language.** Code, comments, docs and commit-ready text are in English. UI strings come in Russian and English.
   Match the surrounding code's style and comment density.
2. **Go.**
   - Go 1.26 (frp v0.72.0 requires it; the `go` command fetches the go1.26 toolchain itself). golangci-lint v2.12.2,
     rebuilt with go1.26 in `~/go/bin`, must be clean with the repo's `.golangci.yml`. `go test -race ./...` must be
     green.
   - Library sources are in `/Users/devxpro/go/pkg/mod/`: read them instead of guessing APIs.
   - Never run `go mod tidy` unless your card says so. If you need a new module, `go get` it and list it in your
     report.
3. **Frontend.** Node 22+ (the Mac has 25). Use npm. Typecheck, lint and build must pass. No CDN assets at runtime:
   everything is bundled.
4. **Shell.** bash with `set -euo pipefail`, shellcheck clean, wrapped in `main "$@"`. No GNU-only flags in scripts
   that also run on macOS.
5. **Tool hygiene on this Mac.**
   - Use absolute paths, `git -C <dir>` and `go -C <dir>`. Never `cd X && …` compounds, and no `rm -rf` of computed
     paths.
   - No Perl.
   - Python only as `python3` inside a venv.
   - Downloaded or third-party files go in their own empty directory; never run interpreters from inside it.
6. **Ownership.** Edit only the paths under your card's "Owns". `internal/hub/hub.go` is frozen: if it blocks you,
   stop and report.
7. **No live actions** unless your card says "Live allowed":
   - no git commits or pushes, no GitHub repo or settings changes;
   - no ssh to jobby;
   - no ChatGPT, Codex or Sites account calls;
   - no TV commands or pairing.
8. **Tests are part of the task.** Fakes over mocks; table tests; no sleeps where a channel or a deadline will do.
   Integration tests that need Docker or the LAN go behind the build tag `integration`.
9. **Docs.** Update the doc your card names. `docs/TESTING.md` says how to run every test suite; append your suite.
10. **Report**, in this order:
    1. summary in three sentences;
    2. files created or changed;
    3. how you verified it (exact commands and their results);
    4. deviations from this plan and why;
    5. open issues and risks;
    6. what the next task needs to know.

## 7. Phases, waves and task cards

### 7.0 Waves

| Wave | Runs in parallel | Needs |
|---|---|---|
| 1 | T0.0 | — |
| 2 | T0.1, T0.3, T0.4, T1.1, T1.2, T1.4 | T0.0 |
| 3 | T0.2 (O) after T0.1 · T1.3 after T0.3+T1.1+T1.2 | — |
| 4 | T0.5 (O + Roman) after T0.2+T0.3+T0.4 · T1.5 after T0.5 | — |
| 5 | T1.6 (O + Roman) · T1.R | T1.3, T1.4, T1.5 |
| 6 | T2.0 | phase 1 gate |
| 7 | T2.1, T2.2, T2.3, T2.4, T2.5, T2.7 (started 2026-11-15 in parallel with T1.3) | T2.0 |
| 8 | T2.6 → T2.R | wave 7 |
| 9 | T3.0 (O) → T3.1 + (T3.2 or T3.3) + T3.4 → T3.5 (O + Roman + Tanya) → T3.R | phase 2 gate |
| 10 | T4.1, T4.2, T4.3, T4.4 → T4.5 (O) | phase 3 gate |

(O) means the orchestrator only (§8).

**Models.** Opus goes to security-critical work and what Roman sees; Sonnet to well-scoped engineering and chores.

| Task | Model | Effort | Why |
|---|---|---|---|
| T0.0 scaffold | Sonnet | medium | mechanical |
| T0.1 relay + server.sh + harness | Sonnet | high | well-specified infra, must be tested end to end |
| T0.3 relay client spike | Opus | high | unknown library internals |
| T0.4 Site template | Sonnet | high | adaptation of proven code |
| T1.1 pluginkit + plugin host | Opus | high | concurrency, process lifecycle, protocol |
| T1.2 gateway | Opus | high | auth, scopes, exact contracts |
| T1.3 homed wiring | Opus | high | integration |
| T1.4 homie-tv plugin | Opus | high | protocol, LAN discovery, quality of tool design |
| T1.5 Site finalize | Sonnet | medium | small follow-up |
| T1.R review | Opus | high | review |
| T2.0 admin API + design brief | Opus | high | contracts the UI and backend share |
| T2.1 SQLite store | Sonnet | high | standard |
| T2.2 passkeys + sessions | Opus | max | security-critical |
| T2.3 admin API backend | Opus | high | broad |
| T2.4 admin UI | Opus | max | the face of the product |
| T2.5 plugin installer + self-update | Sonnet | high | standard, needs care |
| T2.7 progress + plugin i18n | Opus | high | protocol across hub, pluginkit and homie-tv |
| T2.6 integration + e2e | Opus | high | integration |
| T2.R security review | Opus | max | security |
| T3.1 people + invites | Opus | high | auth flows |
| T3.2 Site publisher in Go (path B) | Opus | high | fragile external flow |
| T3.3 OAuth 2.1 server (path A) | Opus | max | security-critical |
| T3.4 People + Join UI | Opus | max | key UX |
| T3.R review | Opus | high | review |
| T4.1 home installers | Sonnet | high | standard, many platforms |
| T4.2 release pipeline | Sonnet | medium | standard |
| T4.3 installer CI | Sonnet | medium | standard |
| T4.4 docs | Sonnet | medium | writing |

### 7.1 Phase 0 — relay and the connection test

#### T0.0 — Scaffold Homie  · Sonnet / medium
**Owns:** `homie/` root files, `internal/hub/hub.go`, `docs/TESTING.md`, `.github/workflows/ci.yml`.

**Do**
1. Run `git init -b main` in `/Users/devxpro/projects/family/homie` (it contains `docs/PLAN.md`) and add the
   remote `git@github.com:devXpro/homie.git`. Do not push; the orchestrator creates the GitHub repo.
2. Set up `go.mod`: module `github.com/devXpro/homie`, `go 1.26` (done: frp v0.72.0 forced it). Run
   `go get github.com/modelcontextprotocol/go-sdk@v1.8.0 github.com/fatedier/frp@v0.72.0 go.yaml.in/yaml/v3@v3.0.6`.
   Report if frp v0.72.0 cannot be required as a library.
3. Write `internal/hub/hub.go` exactly as in §5.3, plus a trivial test so the package builds and lint runs.
4. Write `.golangci.yml` (v2):
   - linters: default + bodyclose, errorlint, gocritic, gosec, misspell, nolintlint, revive, unconvert, unparam,
     usestdlibvars;
   - exclusions preset `std-error-handling`;
   - formatters: gofmt, goimports.
5. Write `.gitignore`: `/bin/`, `/dist/`, `node_modules/`, `/internal/web/dist/*` (except `.gitkeep`), `*.db`,
   `/data/`, `.env*`, `*token*`, `coverage.out`, `site/.openai/hosting.json`, `.DS_Store`.
6. Write a `Makefile` with targets `lint`, `test` (`go test -race ./...`), `build` (`bin/homed`), `web` (a no-op until
   `web/` exists) and `site-test` (`node --test site/core`, a no-op until `site/` exists).
7. Write CI (`.github/workflows/ci.yml`):
   - a Go job: setup-go from `go.mod`, golangci-lint action pinned to v2.12.x, tests;
   - jobs for `web/` and `site/` that run only when those directories exist.
8. Add the MIT `LICENSE` with the holder "Homie contributors", and a skeleton `README.md`: pitch, status "work in
   progress", link to `docs/PLAN.md`.
9. Write `docs/TESTING.md` as the skeleton every later card appends to.

**Acceptance:** `make lint test` passes. `golangci-lint config verify` passes. The tree matches §4 for the files made.

#### T0.1 — Relay server package, installer and docker test harness  · Sonnet / high
**Owns:** `relay/**`, `install/server.sh`, `docs/RELAY.md`.

**Do**
1. `relay/docker-compose.yml` (standalone):
   - services `caddy` (`caddy:2.11.4-alpine`; ports 80, 443 and 443/udp; volumes for Caddyfile, data and config)
     and `frps` (`ghcr.io/fatedier/frps:v0.72.0`, `-c /etc/frp/frps.toml`, read-only mounts for `frps.toml`, `token`
     and `offline.html`, container_name `homie-frps`);
   - `restart: unless-stopped`; no frps ports published.
2. `relay/docker-compose.behind-proxy.yml`: frps only, on the external network `${HOMIE_PROXY_NETWORK:-proxy}`.
3. `relay/frps.toml` per §5.5. Confirm every key against
   `/Users/devxpro/go/pkg/mod/github.com/fatedier/frp@v0.72.0` (run `go mod download` in homie first).
4. `relay/offline.html`: bilingual (EN/RU) "Home is offline / Дом не на связи", dark, self-contained, no external
   assets.
5. `relay/Caddyfile.tmpl` (standalone) and `relay/snippets/{caddy,nginx,traefik}.md` for the behind-proxy mode.
   - nginx: WebSocket upgrade headers, `proxy_read_timeout 1h`, `proxy_buffering off`.
   - Caddy: `stream_close_delay 30m`.
6. `install/server.sh`:
   - flags: `--domain D` (asked for if missing; required), `--mode auto|standalone|behind-proxy`,
     `--network NAME` (behind-proxy; default `proxy`), `--dir` (default `/opt/homie-relay`), `--rotate-token`,
     `--print-code`, `--uninstall`, `--yes`, `--skip-dns-check` (tests only).
   - Preflight: root or sudo; `docker`; `docker compose version` v2; `curl`; `ss` or `netstat`.
   - DNS check: the domain's A records must include a public address of this server. Compare local interface
     addresses plus a best-effort `curl -fsS https://api.ipify.org`. On a mismatch, explain and ask.
   - Mode `auto`: standalone if nothing listens on 80/443, else behind-proxy.
   - Idempotent files: the token (64 hex chars from `/dev/urandom`, 0600) is kept unless `--rotate-token`.
   - `docker compose up -d`.
   - Standalone: wait up to 120 s for a TLS handshake on `https://<domain>`. Behind-proxy: print the right snippet
     and stop.
   - Print the connect code (§5.5) and the next step for the home side.
   - Must be safe for `curl … | bash`.
7. `relay/test/run.sh` is the docker smoke test. Run it on this Mac; Docker 29 is running.
   - Bring up the standalone stack with a test Caddyfile using `tls internal` for `homie.test`.
   - Start an `frpc` container (`ghcr.io/fatedier/frpc:v0.72.0`) using `wss` to `homie.test:443` (docker network
     alias or `--add-host`), trusting Caddy's internal root CA, and proxying to a tiny HTTP echo container.
   - Assert a request to `https://homie.test/anything` (curl `--resolve`, `--cacert`) reaches the echo backend.
   - Assert that with frpc stopped the same request returns 404 with the offline page body.
   - Assert a Caddy reload does not drop the frpc channel.
   - Clean up everything.
8. `docs/RELAY.md`: how it works, both modes, rotating the token, uninstall, troubleshooting.

**Acceptance:** shellcheck is clean. `relay/test/run.sh` passes. `install/server.sh --mode behind-proxy --dir
/tmp/homie-relay-test --domain homie.test --skip-dns-check --yes` writes the files and prints a valid connect code
on this Mac (run it without root via a `--no-root-check` test flag, or document how you ran it). Nothing touches jobby.

#### T0.2 — Deploy the relay on jobby  · Orchestrator
After T0.1 is accepted:
1. Copy `relay/` and `install/server.sh` to jobby.
2. Run `server.sh --mode behind-proxy --network proxy --domain homie.jobbyjobs.work --yes`.
3. Add the §5.5 Caddy block to `/opt/proxy/Caddyfile` the safe way:
   - backup `Caddyfile.bak-<date>-homie`;
   - write in place (single-file bind mount);
   - `caddy validate`, then one `caddy reload`;
   - check that the other WebSocket routes keep their `stream_close_delay`.
4. Check that `curl -sS https://homie.jobbyjobs.work/` returns the offline page with a valid Let's Encrypt
   certificate.
5. Store the connect code in the Mac Keychain as `homie-relay-code`. Never print it.

#### T0.3 — homed spike: embedded relay client and stub endpoints  · Opus / high
**Owns:** `cmd/homed/**`, `internal/relay/**`, `internal/spike/**` (temporary), `docs/research/frp-embedding.md`.

**Do**
1. Study `github.com/fatedier/frp@v0.72.0` (`client/`, `pkg/config/v1/`) in the module cache. Find out how to run the
   client in-process with: the §5.5 settings; `wss` with outer TLS verified (system roots, or a CA file); token auth;
   no exit on login failure; one HTTP proxy. Also find out how to observe login, reconnects and errors. Write the
   findings to `docs/research/frp-embedding.md`.
2. `internal/relay`:
   - `Config{Domain, Token string; LocalPort int; CAFile string}`;
   - `Run(ctx, cfg, *slog.Logger) error` — blocks and reconnects;
   - `Status() Status{Connected bool; Since time.Time; LastError string}`;
   - `SelfCheck(ctx, domain, instanceID) error`;
   - `ParseCode`/`FormatCode` for the connect code.
   If embedding is not viable, supervise an external `frpc` (generated TOML in the data dir) and say why in the
   research doc.
3. `internal/spike`:
   - `GET /healthz`;
   - the §5.4 REST façade with exactly one tool, `home_ping`: returns `{"message":"pong","at":"<RFC3339>"}`,
     read-only, guarded by the bearer key in env `HOMED_SPIKE_KEY`;
   - `/mcp` through go-sdk Streamable HTTP exposing `home_ping` **without auth, only with
     `--spike-authless-mcp`**;
   - every response carries `X-Homie-Instance`.
4. `cmd/homed`:
   - `homed run --data-dir D --listen 127.0.0.1:8080 --spike [--spike-authless-mcp]`;
   - `homed relay connect <code>` writes `relay.domain` into `homed.yaml` and the token into `relay-token` (0600);
   - `homed version`.
5. Tests:
   - connect code round trip;
   - the spike handlers via httptest;
   - an `integration`-tagged test that runs the client against the T0.1 harness, or against an frps binary from the
     module cache if T0.1 is not ready.

**Acceptance:** unit tests and lint are green. Locally, `homed run --spike` behind the T0.1 harness answers
`/healthz` and `/api/v1/tools` through `https://homie.test`. The research doc lists the exact commands the
orchestrator should run against the real domain.

#### T0.4 — Site template from Librus  · Sonnet / high
**Owns:** `site/**`, `docs/research/sites-tools.md` (template only; the orchestrator fills it).

**Context:** `/Users/devxpro/projects/family/librus-gpt/site/` — `README.md`, `SITES-STARTER.md`, `core/mcp.mjs`,
`core/mcp.test.mjs`, `app/mcp/route.ts` and `release/release.mjs`.

**Do**
1. Copy the Librus Site into `homie/site/`. Leave out `node_modules`, `dist`, `build`, `.openai/hosting.json`,
   state and keys.
2. Apply §5.7:
   - env names, `serverInfo.name`, wording ("home hub" instead of "school service");
   - 422 and offline-404 handling;
   - the tests updated and extended for both.
3. `release/release.mjs` per §5.7, including `--list-sites-tools`. The commit identity becomes "Homie Site".
4. `site/README.md`: settings, release, profiles, voice notes. Keep the "Always allow" step and say why: voice cannot
   approve write actions.

**Acceptance:** `node --test site/core` and `npm run check` pass. `node release/release.mjs --dry-run --title "Test"
--slug test` builds the archive without any account call.

**Not allowed:** any release run without `--dry-run`.

#### T0.5 — Live connection test  · Orchestrator + Roman
1. On the Mac:
   - `homed relay connect` with the code from the Keychain;
   - `homed run --spike`, with `HOMED_SPIKE_KEY` from Keychain item `homie-spike-key`;
   - check `https://homie.jobbyjobs.work/healthz` from outside.
2. Run `node site/release/release.mjs --list-sites-tools` and fill `docs/research/sites-tools.md`.
3. **Path B**
   - Publish «Мой шалаш (тест)» (`--slug moy-shalash-test`) into Roman's account. `HOME_API_URL` =
     `https://homie.jobbyjobs.work`; the key is the spike key.
   - Roman, on the iPhone ChatGPT app: Plugins → Created by you → connect it, then Always allow.
   - Live voice: «Спроси у Моего шалаша пинг».
   - Record:
     - the name ChatGPT shows;
     - whether connecting works in the iOS app;
     - whether voice works.
4. **Path A**
   - Restart with `--spike-authless-mcp`.
   - Roman, in iPhone Safari: chatgpt.com/plugins → Add → custom MCP server → `https://homie.jobbyjobs.work/mcp`,
     name «Мой шалаш A».
   - Record:
     - whether adding from the phone works;
     - whether the plugin appears in the iOS app;
     - whether voice works.
5. Write `docs/research/connection-paths.md`.
   - **Decision:** A is primary only if all three of its checks pass; otherwise B.
   - Remove the authless mode. Delete the test plugin A. Keep or delete the test Site as Roman prefers.

### 7.2 Phase 1 — "turn on the TV" by voice, no admin yet

#### T1.1 — pluginkit and plugin host  · Opus / high
**Owns:** `pluginkit/**`, `internal/plugins/**`, `docs/PLUGINS.md`.

**Do**
1. `pluginkit` per §5.2, with a runnable example in `pluginkit/example/` (an `echo` plugin with an assistant tool, an
   admin tool and an inventory tool).
2. `internal/plugins` implements `hub.ToolHost` per §5.1 and §5.3:
   - spawn with the env, per-plugin data dir and stderr capture;
   - go-sdk stdio client (verify the names in the module cache);
   - tool snapshot with name, audience and timeout parsing;
   - `Call` with timeout and request `_meta`;
   - restart and backoff, the failing state, `list_changed`, `Changed()`, graceful shutdown.
   - Constructor: `New(specs []Spec, dataDir string, log *slog.Logger) (*Host, error)` with
     `Spec{ID string; Command []string}` plus `Start(ctx)` and `Close()`.
3. Tests use a fake plugin built in `TestMain` from `internal/plugins/testdata/fakeplugin`. Cover:
   - listing and audiences;
   - a call;
   - the timeout;
   - a crash and restart;
   - invalid names skipped;
   - `Changed()` firing;
   - the stderr log.
4. `docs/PLUGINS.md`: the §5.1 contract for authors, with a minimal Go plugin built on pluginkit.

**Acceptance:** `go test -race ./pluginkit/... ./internal/plugins/...` is green, lint is clean, and the example runs
under the host in a test.

#### T1.2 — Gateway: REST façade, MCP endpoint, keys, audit  · Opus / high
**Owns:** `internal/gateway/**`, `internal/audit/**`, `internal/keys/**`.

**Do**
1. `internal/keys`:
   - `Generate() (key, sha256hex string)`;
   - a `hub.KeyResolver` over the people from config (§5.6);
   - constant-time matching.
2. `internal/audit`: JSONL monthly files, redaction (§5.4), a non-blocking `Record` (buffered channel; drop and count
   on overflow).
3. `internal/gateway.New(host hub.ToolHost, keys hub.KeyResolver, audit hub.Auditor, opts Options) http.Handler`
   implements §5.4 exactly.
   - `Options`: `HomeName`, `Instructions`, `InstanceID`, `Version`, `RateLimit`.
   - The MCP server is rebuilt from the host snapshot when `Changed()` fires, one per scope set.
4. Tests (httptest plus a fake `ToolHost`):
   - auth (missing, wrong, right);
   - scope filtering (list and call);
   - every status row of the §5.4 table;
   - golden JSON for `/api/v1/tools`, checked against what `librus-gpt/site/core/mcp.mjs` reads;
   - MCP list and call through a go-sdk client over HTTP;
   - the version fingerprint changes when tools change;
   - audit lines and redaction;
   - the rate limit.

**Acceptance:** green with `-race`, lint clean.

#### T1.3 — Wire homed  · Opus / high
**Needs:** T0.3, T1.1, T1.2.
**Owns:** `cmd/homed/**`, `internal/config/**`, `internal/relay/**`. It deletes `internal/spike/**`.

**Do**
1. `internal/config`: load, validate and save `homed.yaml` (§5.6), resolve the data dir, generate the instance id.
2. `homed run` starts:
   - the gateway on `listen`;
   - the plugin host from `plugins`;
   - the relay client and self-check, if `relay` is set;
   - structured slog logs;
   - graceful shutdown on SIGINT and SIGTERM.
3. Local admin socket `<data_dir>/homed.sock` (unix, 0600) with a tiny JSON API: list all tools (admin audience),
   call any tool, plugin status, relay status.
4. CLI:
   - `homed relay connect <code>`, `homed relay status`;
   - `homed key new --person roman --name Roman --label "ChatGPT Site" [--plugins tv]` prints the key once;
   - `homed key list`, `homed key revoke <prefix>`;
   - `homed plugin add --id tv -- <command…>`, `homed plugin list`, `homed plugin remove`;
   - `homed tool list`, `homed tool call <name> '<json>'` (through the socket, `Via:"cli"`);
   - `homed version`.
5. `docs/DEV.md`: running homed and homie-tv locally, plus a launchd plist example for running at login.

**Acceptance:** an integration test with the fake plugin passes:
- `homed run`;
- `key new`;
- `curl /api/v1/tools`;
- `tool call`.

Lint clean. The orchestrator can run the T1.6 steps from `docs/DEV.md` alone.

#### T1.4 — homie-tv plugin  · Opus / high
**Owns:** all of `homie-tv/`. The existing uncommitted code is yours to finish.

**Context**
- The TV facts in §9.
- Existing code:
  - `tv/` and `internal/wol/` are tested;
  - `provider/lgwebos/{lgwebos.go,manifest.go}` were never compiled and have no tests. The manifest comes from
    home-assistant-libs/aiowebostv `handshake.py`.
  - webOS quirk: the `hello` reply carries **no id**.
  - The registration flow: a `response` with `pairingType: PROMPT`, then `registered` with `client-key`, or `error`.

**Order:** T1.1 builds `pluginkit` in parallel with you. Do the provider, store and discovery first. If `pluginkit`
is still missing when you reach the tools, call go-sdk directly behind a tiny local adapter with the §5.2 names, and
say so in your report.

**Do**
1. `go.mod`:
   - module `github.com/devXpro/homie-tv`;
   - require `github.com/devXpro/homie` (pluginkit) with `replace github.com/devXpro/homie => ../homie`
     (development only, noted in the README);
   - go-sdk v1.8.0; `github.com/coder/websocket`.
2. Delete `internal/config` and `internal/keychain`.
3. `internal/store`: `tvs.json` in `pluginkit.DataDir()`, mode 0600, atomic writes. Each entry is
   `{id, name, family:"lg-webos", host, mac, model, key}`.
4. `internal/discovery`:
   - SSDP M-SEARCH for `urn:lge-com:service:webos-second-screen:1`, with fallback `ssdp:all` filtered by `SERVER`
     containing `WebOS`;
   - bind the multicast to the default-route interface;
   - fetch the UPnP description (friendlyName, modelNumber);
   - MAC from the ARP table after a unicast probe (`arp -n <ip>` on macOS, `/proc/net/arp` on Linux).
5. Finish `provider/lgwebos` and add its tests with a fake SSAP WebSocket server:
   - register with a known key;
   - a stale key → `ErrNotPaired`;
   - pairing PROMPT → registered;
   - request error;
   - every command's URI and payload;
   - PowerOn sending WoL to a local UDP listener;
   - a wake timeout.
6. Tools through pluginkit (descriptions in English):
   - **Assistant**
     - `tv_list` — read-only, inventory; items carry state `on|off|offline`, probed cheaply by `hello` with a 1.5 s
       timeout.
     - `tv_power_on(tv?)`, `tv_power_off(tv?)`.
     - `tv_set_volume(tv?, percent 0..100)`.
     - `tv_set_input(tv?, input: "tv"|"hdmi1".."hdmi4")`.
     - `tv` is optional when exactly one TV is configured. Otherwise an unknown or missing `tv` returns
       `isError: true` listing the known TVs.
   - **Admin**
     - `tv_discover` (timeout 15 s);
     - `tv_add(host, name?, id?)` fills MAC and model;
     - `tv_pair(tv)` (timeout 180 s; the text tells the user to press OK on the remote);
     - `tv_remove(tv)`, `tv_rename(tv, name)`.
   - Results are short human texts plus `structuredContent`.
   - Not-paired and offline cases are `isError: true` with a fix hint, e.g. "Turn on Mobile TV On in the TV settings"
     for a failed wake.
7. `cmd/homie-tv`: `serve` (default) and `discover` (prints the TVs found).
8. Copy `.golangci.yml` from homie. Write the `README.md`, `docs/families/lg-webos.md` and `docs/TESTING.md`. The
   lg-webos doc covers the TV setup: LG Connect Apps, Mobile TV On → Turn on via Wi-Fi, Quick Start+, DHCP
   reservation; plus the limitations: ws:3000 only, newer firmware needs wss:3001.

**Live allowed:** read-only discovery (SSDP and webOS `hello`) on the LAN. No pairing, no commands.

**Acceptance:**
- green with `-race`, lint clean;
- `homie-tv discover` finds `192.168.68.66`, model `49UJ634V-ZD`, MAC `20:3d:bd:38:e1:c8`;
- an in-memory MCP client test lists the 5 assistant tools and 5 admin tools with the right `_meta`.

#### T1.5 — Site finalize  · Sonnet / medium
**Needs:** T0.5. **Owns:** `site/**`.
Apply what T0.5 learned: naming, the offline text, the `--check` tool, README steps. If path B is primary, add a
`--home-api-url` flag so the hub can drive the script later.

#### T1.6 — Integration on Roman's Mac  · Orchestrator + Roman
1. Build homed and homie-tv.
2. `homed relay connect`, then `homed plugin add --id tv -- …/homie-tv serve`.
3. `homed tool call tv_discover` → `tv_add` → `tv_pair`; Roman presses OK on the TV.
4. `homed key new --person roman --name Roman` → Keychain `homie-key`.
5. Publish «Мой шалаш» by the chosen path.
6. Voice checks: «включи телик», «громкость двадцать», «переключи на HDMI 2», «выключи телик»; and the TV offline
   case.
7. Write the results into `docs/research/phase1-demo.md`.
8. Set up launchd so homed runs at login. Note: macOS asks for Local Network access once.

#### T1.R — Phase 1 review  · Opus / high
Review Homie phase 1 and homie-tv for correctness and security:
- auth and scope bypass, key handling, redaction;
- process handling, races, timeouts;
- the frp client config (certificate verification on!).

Output a findings list with severity and a proposed fix; no edits. The orchestrator triages.

### 7.3 Phase 2 — admin and login

#### T2.0 — Admin API contract and design brief  · Opus / high
**Owns:** `docs/ADMIN-API.md`, `docs/DESIGN.md`, `internal/store/store.go` (interfaces only).

**Do**
1. `ADMIN-API.md`:
   - every endpoint under `/api/admin/…`: method, path, request and response JSON, errors, which ones need step-up;
   - the SSE stream `/api/admin/events` (relay status, plugin states, tool progress, new audit entries);
   - the passkey endpoints (register begin/finish, login begin/finish, step-up begin/finish), the setup-link flow,
     logout;
   - plugins: list, detail, assistant tools, admin tools, call admin tool, inventory, install from GitHub, update,
     remove, enable/disable, logs;
   - audit: paged, filtered;
   - settings: home name, language, relay status, passkeys, version and update check.
2. `DESIGN.md`: iPhone-grade principles and the information architecture.
   - Tabs: Дом / Устройства / Люди (phase 3) / Журнал / Настройки.
   - Dark-first tokens; the iOS system font stack; large titles; 16–20 px rounded cards; spring motion with
     reduced-motion support.
   - One primary action per screen; human copy in RU and EN, with a glossary mapping technical states to words.
   - Empty states; error states; the first-run wizard; the JSON-Schema-to-form rules for admin tools; mobile-first
     breakpoints.
3. `store.go`: repository interfaces for people, passkeys, sessions, keys, invites, plugins, audit and settings.

#### T2.1 — SQLite store  · Sonnet / high
**Owns:** `internal/store/**` except the `store.go` interfaces.
`modernc.org/sqlite` (no cgo), embedded migrations, the repositories from T2.0, and a one-time import of the phase 1
people, keys and plugins from `homed.yaml`. Tests run on temp DBs.

#### T2.2 — Passkeys, sessions, first run  · Opus / max
**Owns:** `internal/auth/**`, `internal/firstrun/**`.

**Do**
1. `github.com/go-webauthn/webauthn`: RP ID = the relay domain; origin `https://<domain>`.
2. Flows:
   - register with a one-time setup or invite token;
   - login;
   - step-up;
   - add and remove a passkey; the last passkey cannot be removed.
3. Sessions per §5.8; rate limits on the auth endpoints; admin audit events.
4. First run:
   - a LAN page on `setup_listen` protected by a PIN (hardened in T2.F: see §5.8);
   - steps: enter the connect code → wait for "relay connected" → show the one-time link to `https://<domain>/admin/setup#<token>`.
   - The page serves nothing once an owner exists.
5. CLI: `homed admin setup-link`, `homed admin reset-access` (local only; it revokes all sessions and passkeys and
   prints a new setup link).
6. Tests with a virtual authenticator (e.g. `github.com/descope/virtualwebauthn`):
   - registration and login;
   - wrong origin and RP ID rejected;
   - replayed challenge rejected;
   - step-up expiry;
   - token single-use and expiry.

#### T2.3 — Admin API backend  · Opus / high
**Owns:** `internal/admin/**`.
Implement `ADMIN-API.md` on `hub.ToolHost`, the store and auth. Admin tool calls validate their arguments against the
JSON Schema before calling and stream progress over SSE. Tests run with httptest and fakes.

#### T2.4 — Admin UI  · Opus / max
**Owns:** `web/**`, `internal/web/**` (`embed.go` + `dist/.gitkeep`).

**Do**
1. Stack: Vite, React 19, TypeScript strict, Tailwind v4, shadcn/ui, `motion`, TanStack Query, react-router, i18next
   (RU/EN, RU default for Roman), lucide icons. Dark default, light, and follow-system.
2. The screens of `DESIGN.md`:
   - login (passkey);
   - the first-run wizard (setup link landing → create passkey);
   - Дом;
   - Устройства — plugin cards → detail with the inventory list, actions from admin tools as auto-forms with
     progress, and "what it can do" (assistant tools);
   - Журнал;
   - Настройки.
3. Mobile-first, with a bottom tab bar on phones and a sidebar on desktop.
4. Build into `internal/web/dist`. `internal/web` serves the SPA with an `index.html` fallback, long-cache hashed
   assets and a CSP.
5. Tests:
   - typecheck and lint;
   - component tests for the schema form;
   - Playwright smoke tests against a mocked API (MSW): login, first run, call an admin tool.

**Acceptance:**
- Lighthouse accessibility ≥ 95.
- No layout shift on navigation.
- Screenshots of every screen, dark and light, phone and desktop, saved to `docs/screenshots/` for Roman.

#### T2.5 — Plugin installer and self-update  · Sonnet / high
**Owns:** `internal/install/**`.
- GitHub Releases client (unauthenticated, with an optional token).
- Pick the asset by GOOS/GOARCH and the GoReleaser naming.
- Verify `checksums.txt`; unpack to `plugins/<id>/bin/<version>/`; read `homie-plugin.json`.
- Atomic switch, with a rollback if the new version fails to start.
- `homed` self-update: download, verify, replace, then exit and let the service manager restart it.
- Tests run against an httptest fake of the GitHub API.

#### T2.7 — Progress plumbing and plugin i18n  · Opus / high
**Owns:** `internal/plugins/**`, `pluginkit/**`, `docs/PLUGINS.md`, and in homie-tv `internal/tools/**` (+ its tests).

**Do**
1. `internal/plugins`:
   - parse `_meta "homie/i18n"` into `hub.Tool.I18n` (lenient: skip malformed entries with a warning);
   - progress through the context, without changing `hub.ToolHost`: `plugins.WithProgress(ctx, func(Progress))`. If
     the callback is present, `Call` sends a `progressToken` and forwards every progress notification (progress,
     total, message, i18n) to it. See ADMIN-API §8.
2. `pluginkit`:
   - `I18n(t *mcp.Tool, lang string, text Text) *mcp.Tool` with its own exported `Text` type. Plugins outside this
     module cannot import `internal/hub`.
   - `Progress(ctx, req, progress, total float64, message string, i18n map[string]string) error`.
   - A helper to set `x-homie-ref: "inventory"` on a property schema.
3. `docs/PLUGINS.md`: document i18n, progress and `x-homie-ref`.
4. homie-tv `internal/tools`:
   - Russian texts for all 10 tools and their input labels;
   - `x-homie-ref: "inventory"` on every `tv` argument;
   - `tv_pair` sends progress "Press OK on the TV remote" / «Нажмите OK на пульте телевизора»;
   - `tv_discover` sends progress while it searches.
5. Tests for all of the above. homie-tv tests must still pass with the `replace ../homie`.

#### T2.6 — Integration and e2e  · Opus / high
Wire the store (and the config import), auth, admin, web and installer into `homed run`. Playwright e2e runs against
a real homed with the fake plugin and a virtual authenticator. `docs/DEV.md` is updated.

#### T2.R — Security review  · Opus / max
Findings only:
- auth, sessions, step-up, CSRF, first run, setup links;
- plugin install integrity;
- SSE leaks; headers and CSP; the admin socket.

### 7.4 Phase 3 — people and family onboarding

#### T3.0 — Path decision  · Orchestrator
Use `connection-paths.md` to choose T3.2 (path B) or T3.3 (path A), and update this plan.

#### T3.1 — People and invites backend  · Opus / high
**Owns:** `internal/people/**`, plus the related admin API additions agreed in T2.0's doc.
- Invites: 24 h, single use, with a scope set.
- `/join/{token}`: the invitee registers their own passkey (role `member`).
- Per-person scopes, connections (`site` | `plugin`) with status, and revoke (keys, sessions, connections).

#### T3.2 — Site publisher in Go (path B)  · Opus / high
**Owns:** `internal/sites/**`.
1. Port `librus-gpt/site/release/release.mjs`:
   - the `codex app-server` JSON-RPC client;
   - a per-person `CODEX_HOME` under `<data_dir>/sites/<person>/`;
   - device-code login whose `verificationUrl` and `userCode` are surfaced to the join page;
   - the guard that refuses a second profile on the same account.
2. Find or create the Site by slug with Roman's title; set the env (`HOME_API_URL`, `HOME_API_KEY` secret).
3. Full publish uses a CI-built Site archive (a release asset) plus the embedded source for the git push, then the
   `codex exec` upload step and deploy status. Settings-only republish needs no build.
4. Republish-all; a logout policy option (keep the login or sign out after publishing).
5. Requirements on the home box: `codex` and `git`. Report them clearly in the UI when missing.

#### T3.3 — OAuth 2.1 server (path A)  · Opus / max
**Owns:** `internal/oauth/**`.
- RFC 8414 server metadata and RFC 9728 protected-resource metadata.
- DCR and CIMD per OpenAI's custom-MCP docs; the redirect allow-list per those docs.
- Authorize = passkey login of the person, then a consent screen in the SPA.
- PKCE S256; opaque hashed tokens with rotation; revoke. `/mcp` accepts these tokens.
- Tests run the full flow with a fake client.

#### T3.4 — People and Join UI  · Opus / max
**Owns:** `web/src/features/people/**`, `web/src/features/join/**`.
- People list and detail: scopes as toggles, connection status, republish, revoke.
- Invite sheet: link, QR, system share.
- `/join` wizard, mobile-only, one step per screen:
  1. welcome;
  2. Face ID passkey;
  3. ChatGPT step (path B: device code with a big "Open" button and the code; path A: add the plugin, then OAuth);
  4. the "Always allow" step with an illustration;
  5. done.
- "Перевыпустить" shows per-person progress and notes ChatGPT's ~10 min refresh.

#### T3.5 — Onboard Tanya  · Orchestrator + Roman + Tanya
Roman invites from the admin. Tanya completes everything on her phone. Then a voice check from her account.

#### T3.R — Review  · Opus / high
The invite and publisher (or OAuth) flows: correctness and security.

### 7.5 Phase 4 — install for everyone

#### T4.1 — Home installers  · Sonnet / high
**Owns:** `install/home.sh`, `install/launchd/**`, `install/systemd/**`, `Dockerfile`.
- `home.sh` (Linux and Raspberry Pi):
  - detect amd64, arm64 or armv7;
  - download the release and verify the checksum;
  - create user `homie` and `/var/lib/homie`;
  - install and start a systemd unit;
  - print the setup URL and PIN.
- macOS: a LaunchAgent plist used by the Homebrew formula (`brew services`).
- `Dockerfile`: multi-arch and minimal; document `--network host` and why the Mac cannot use it.

#### T4.2 — Release pipeline  · Sonnet / medium
**Owns:** `.goreleaser.yaml`, `.github/workflows/release.yml` in both repos.
- homie:
  - homed for darwin amd64/arm64 and linux amd64/arm64/armv7;
  - a `web` build hook; the Site archive asset; checksums;
  - Homebrew formula to `devXpro/homebrew-tap` (created by the orchestrator);
  - multi-arch images on ghcr.
- homie-tv: the binaries plus the `homie-plugin.json` asset.

#### T4.3 — Installer CI  · Sonnet / medium
GitHub Actions jobs:
- `relay/test/run.sh`;
- `server.sh` standalone on ubuntu-latest, with a `--test-tls-internal` flag added to the script;
- `home.sh` in a Debian arm64 container through qemu (a smoke test of the service start).

#### T4.4 — Docs  · Sonnet / medium
- `QUICKSTART.md` ("Server — 5 minutes", "Home — 5 minutes", with screenshots).
- `PLUGINS.md` (final), `SECURITY.md`, `TROUBLESHOOTING.md`, `ARCHITECTURE.md`.
- README polish.

#### T4.5 — Dogfood and tag  · Orchestrator
Re-run the official `server.sh` behind-proxy on jobby (it must be a no-op). Clean-install the home side from the
release on the Mac. Tag `v0.1.0` when Roman says so.

### 7.6 Phase gates

At the end of every phase:
1. All test suites and lint are green in both repos.
2. Review findings are triaged: fixed or written down.
3. A demo to Roman.
4. The orchestrator commits and pushes (no approval needed).

## 8. Orchestrator-only actions

These are never delegated:
- creating the GitHub repos and the tap repo, commits and pushes (no approval needed since 2026-11-15);
- anything on jobby (T0.2, T4.5) — a Caddy reload is announced first;
- every ChatGPT, Codex or Sites account action (T0.5, T1.6, T3.5);
- TV pairing and commands (T1.6);
- Keychain items: `homie-relay-code`, `homie-spike-key`, `homie-key`, `homie-key-tanya`.

## 9. Environment facts

**Mac (home box for now)**
- MacBook Pro, M4 Pro, macOS 27, Touch ID.
- Go 1.25.9 installed; both go.mod files pin `toolchain go1.26.11` (T2.F — without it the go command picked exactly
  go1.26.0, which has reachable stdlib CVEs; `make vuln` runs govulncheck); golangci-lint 2.12.2 rebuilt with
  go1.26.11 in `~/go/bin`, Node 25.3, Docker 29.9 (running), codex-cli 0.163.0, gh logged in as `devXpro`.
- Claude runs under Paseo.app, which now has Local Network permission. Before it did, LAN traffic failed instantly and
  only `dns-sd` worked.

**Home LAN:** 192.168.68.0/22; router TP-Link Deco at .1; the Mac is .63.

**TV**
- LG 49UJ634V-ZD (2017), webOS release 3.9.3.
- 192.168.68.66, MAC `20:3d:bd:38:e1:c8`, Wi-Fi.
- SSAP `ws://:3000` (3001 also open); pairing types PIN, PROMPT, COMBINED.
- UPnP name "[LG] webOS TV UJ634V". Sound comes from the TV speakers.
- A PS5 sits on HDMI (LAN .74) as a possible HDMI-CEC wake fallback.

**jobby**
- Ubuntu 24.04, Docker 29, Compose v5, 169.40.15.46.
- The shared Caddy `proxy-caddy` (2.11.4) runs in `/opt/proxy`. Its Caddyfile is a single-file bind mount: write in
  place and back up first.
- Other stacks share the docker network `proxy`.
- Only ports 22, 80 and 443 are public.

**Domain:** `homie.jobbyjobs.work` A → 169.40.15.46, DNS-only.

**Prior art to reuse:** `/Users/devxpro/projects/family/librus-gpt/site/` (Site, release script, profiles).

## 10. Agent prompt template (orchestrator)

```
You are a subagent implementing task <ID> of /Users/devxpro/projects/family/homie/docs/PLAN.md.
Read §2, §3, §4, §5, §6 and §9 of that file, then the card <ID> in §7. Do exactly that card, inside its "Owns"
paths, following §6. <Extra context from earlier reports, if any.>
When done, reply with the report of §6.10. Do not commit, push, or perform any live action the card does not allow.
```

## 11. Risks and fallbacks

| Risk | Fallback |
|---|---|
| frp client cannot be embedded cleanly | supervise the official `frpc` binary (T0.3 decides) |
| Caddy alters the `/~!frp` request line | separate frps ports via two Caddy matchers, or a second hostname |
| Sites publishing (undocumented Codex tools, beta, region) breaks | path A as the primary path, or manual publishing with `site/release` |
| Custom plugins cannot be added on a phone or used by voice | path B |
| Voice cannot approve writes | "Always allow" is a mandatory onboarding step |
| The Mac sleeps, so the home is offline | Raspberry Pi as the 24/7 home box; the offline page tells the user |
| macOS Local Network prompt for homed | wizard hint; run as a LaunchAgent and accept once |
| LG wake over Wi-Fi is flaky | Mobile TV On + Quick Start+; HDMI-CEC wake through the PS5 as a later plugin feature |
| Too many parallel Opus agents exhaust the weekly limit | the orchestrator may serialize waves or move high tasks to Sonnet high |

## 12. Open decisions

1. License: MIT is proposed.
2. Commits: decided — the orchestrator commits and pushes on its own (Roman, 2026-11-15).
3. Primary ChatGPT path: decided in T0.5.
