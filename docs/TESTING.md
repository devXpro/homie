# Testing

Every suite is listed here with the exact command to run it. Each task appends its own suite.

## Prerequisites

- Go (see `go.mod`), golangci-lint v2.12.2, Node 22+ (only for `web/` and `site/`), Docker (only for integration tests).

## Go unit tests and lint

```sh
make lint   # golangci-lint run ./...
make test   # go test -race ./...
make vuln   # govulncheck v1.9.0 on the go.mod toolchain (go1.26.11): no known vulnerability reachable from the code
```

`go.mod` pins the toolchain (`toolchain go1.26.11`), so the `go` command fetches that patch release whatever Go is
installed; `go -C <repo> version` shows it. Run govulncheck from inside the module (`make vuln`, or `go -C <repo> run
golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`): it reads `go env GOVERSION` from its working directory, so
`govulncheck -C <repo>` started elsewhere checks the wrong standard library. CI has a `vuln` job in both repos.

## Integration tests

Tests that need Docker or the LAN are behind the build tag `integration`:

```sh
go test -race -tags integration ./...
```

## Site tests

```sh
make site-test   # node --test "site/core/*.test.mjs" (no-op until site/ exists)
```

The quotes matter: Node 22+ takes a glob after `--test`, and a bare directory such as `site/core` is not run.

Typecheck, lint and the release dry run (the Site's own checks; they need `npm ci` first):

```sh
npm --prefix site ci
npm --prefix site run check   # unit tests + tsc --noEmit
npm --prefix site run lint
HOMIE_SITE_HOME=/tmp/homie-site-dryrun node site/release/release.mjs --dry-run --title Test --slug test
```

The dry run stages the source, builds it and packs the archive in `$HOMIE_SITE_HOME` (default `~/.homie-site`); it
makes no ChatGPT or Codex call. Never run `release.mjs` without `--dry-run` as a test: it publishes to a real account.

## Web tests

```sh
make web   # npm ci + vite build into internal/web/dist (embedded by homed)
```

The admin SPA's own suites (typecheck, lint, Vitest, Playwright) are described under "Admin UI" below.

## Suites

<!-- Append one section per suite below: what it covers, the command, prerequisites. -->

### Site template (`site/core`)

Covers `site/core/mcp.mjs`, the thin MCP proxy between ChatGPT and the hub's REST façade (PLAN §5.4, §5.7): protocol
negotiation, the tool-list fingerprint, forwarding with the bearer key, and the error mapping (401, 404 unknown tool,
422 tool failure, non-JSON 404 = home offline, 429, 503, 504, timeouts, network errors). No network, no account.

```sh
make site-test
```

### Gateway, keys and audit (`internal/gateway`, `internal/keys`, `internal/audit`)

Covers PLAN §5.4 with `httptest` and a fake `ToolHost`: bearer auth (missing, wrong, right), per-person plugin scopes on
the list and on calls, every row of the REST status table, the `/api/v1/tools` golden manifest
(`internal/gateway/testdata/tools.golden.json`, checked against the keys `librus-gpt/site/core/mcp.mjs` reads), MCP
list and call through a go-sdk client over HTTP, the `serverInfo.version` fingerprint following tool changes, the
per-key rate limit (fake clock), audit entries and redaction, key generation and constant-time matching, and the JSONL
audit files (monthly names, 0600, drop-on-overflow), and a home name changed while serving (`TestSetHomeName`: the
instructions, the MCP title and the fingerprint follow). No network.

```sh
go test -race ./internal/gateway/... ./internal/keys/... ./internal/audit/...
go test ./internal/gateway/ -run TestManifestGolden -update   # rewrite the golden after an intended change
```

### Relay docker smoke test (`relay/test/run.sh`)

Covers the server side of PLAN §5.5 with real software: `install/server.sh` (files, token, connect code, rerun keeps the
token, `--rotate-token`, behind-proxy files), then the standalone compose stack (Caddy `tls internal` for the fake domain
`homie.test` + frps) and a real `frpc` v0.72.0 over `wss` on 443. It asserts, all through Caddy on one domain and one
frps port: GET and POST reach the echo backend; the channel survives 3 forced Caddy reloads (also with
`grace_period 3s`), while the same reload without `stream_close_delay` drops it (control); with frpc stopped the bilingual
offline page comes back with 404; frpc reconnects by itself; a wrong token and an untrusted CA are rejected. Everything is
named `homie-test-*` and removed on exit (about 80 s).

```sh
bash relay/test/run.sh
shellcheck -x install/server.sh relay/test/run.sh
```

Prerequisites: Docker with compose v2 and the images `ghcr.io/fatedier/frps:v0.72.0`, `ghcr.io/fatedier/frpc:v0.72.0`,
`caddy:2.11.4-alpine`, `alpine:3.21`, `curlimages/curl:8.11.1` (pulled on first run), Go (builds `relay/test/echo`),
openssl. No root, no network except image pulls, nothing on the host ports.

### Relay client (`internal/relay`)

Unit tests (no network, no Docker): connect code round trip and every parse error (tokens never in errors); the frp
client settings of PLAN §5.5 (wss on 443, TLS name = domain, system roots or only the CA file, `loginFailExit = false`,
one `http` proxy `homed` → `127.0.0.1:<port>`); the guard that refuses frp's default of not verifying the server
certificate; the frp log bridge; the real embedded client retrying against a closed port and stopping cleanly;
`SelfCheck` against an `httptest` TLS server (our id, another hub, not a hub, home offline, untrusted certificate).
(The phase 0 spike package and its tests were removed in T1.3; `homed relay connect` is covered by the homed suite.)

```sh
go test -race ./internal/relay/...
```

Integration test (Docker): the embedded client against the real `ghcr.io/fatedier/frps:v0.72.0` with the §5.5 config,
behind an in-process TLS terminator for `homie.test` with a throwaway CA. It asserts the offline 404 page before
connecting and after stopping, requests reaching the local hub, `SelfCheck`, an automatic reconnect after the channel is
cut (with `Status.Since` moving), and that an untrusted certificate and a wrong token never connect and show up in
`Status.LastError`. The frps container is named `homie-t03-frps-*` and removed at the end (about 3 s).

```sh
go test -race -tags integration -run Integration ./internal/relay/
```

The manual check behind real Caddy is in `docs/research/frp-embedding.md` ("Local acceptance run"); since T1.3 run
`homed run` without `--spike` there and call a plugin tool with a person key instead of `home_ping`.

### pluginkit and the plugin host (`pluginkit/`, `internal/plugins/`)

`pluginkit` is tested in memory against a go-sdk client: identity, instructions, tool `_meta`, the caller's request
`_meta`, the tool name rule, `DataDir` and `Serve` shutdown. The host tests build two real plugins in `TestMain`
(`internal/plugins/testdata/fakeplugin` and `pluginkit/example`) and run them as subprocesses: listing and audiences,
`_meta` parsing (timeouts, inventory, bad values), invalid names skipped, calls with caller `_meta`, `isError`, the call
timeout, a crash with last-known tools and `ErrPluginDown`, restart with backoff, the failing state, a missing binary,
`list_changed` → `Changed()` (on the 2026-07-28 protocol and on the legacy 2025-11-25 handshake), the stderr log (ring
and rotated file), the minimal env and data dir, and shutdown (SIGTERM, and SIGKILL for a plugin that ignores it).
About 5 s; no network.

```sh
go test -race ./pluginkit/... ./internal/plugins/...
```

### homed and its config (`cmd/homed`, `internal/config`)

`internal/config`: loading and validating `homed.yaml` (unknown keys, loopback-only listen, `setup_listen`, relay
domain, the phase 1 sections' ids, scopes, key hashes, duplicates), `Update` rewriting only changed top-level sections
(comments and untouched sections survive; failed updates write nothing; 0600), `ArchivePhase1` (the imported sections
leave homed.yaml for `homed.yaml.phase1`, no backup is ever overwritten), the relay token (0600, loose modes refused),
the instance id, the instance lock and the data dir resolution.

`cmd/homed` (T2.6), all on temp data dirs under `/tmp` (a unix socket path must fit in 103 bytes) with
`HOMED_DATA_DIR` pointing into a temp dir, so nothing touches the real data dir; no network, no Docker; about 25 s
with `-race`:
- `TestDaemon` is the acceptance: a phase 1 data dir (relay on a closed local port, the fake plugin, Roman with a key)
  starts as a phase 2 homed. The import into homed.db and the archived homed.yaml; Roman's old key on the REST façade
  (also with the relay's Host; a foreign Host gets 421); `/` → `/admin/`, the SPA (or its 503 hint before `make web`),
  bootstrap and session; the CLI over the socket (`key list/new`, `tool list/call` as the owner via `cli`, arguments
  from stdin with `-`, a failing and an unknown tool, `plugin list`, `relay status`); the first-run page (the 8-digit
  PIN from the terminal, cookie, `connecting`, `relay_not_ready`); the owner's passkey from `homed admin setup-link`, registered with a virtual authenticator
  (`descope/virtualwebauthn`) through the relay's origin; the new home name reaching the gateway at once and the
  first-run page answering 410; a key created in the admin, used, revoked; an admin tool call with progress released
  from the CLI; a rename of the home; the `echo` plugin added and removed while homed runs; a second homed refused; a
  clean shutdown; the audit in homed.db with redaction.
- `TestRelayConnectAtRunTime` (Host guard, passkey relying party and admin origin follow a relay connected while homed
  runs), `TestDevMode` (`--dev-origin`: refused with a relay, localhost passkeys and links, no relay from the web, the
  first-run page or the CLI), `TestRestartAfterHubUpdate` (serve returns `errRestart` → exit 75),
  `TestRelayConfigErrorInStatus`.
- `TestInstallerAdapter`: the admin's installer against an `httptest` TLS fake of GitHub with the real store and plugin
  host: install (steps, store row, plugin up), a second install of the same plugin refused before it touches the
  files, an update whose binary does not start (rolled back to the old row and binary), a good update, a refused
  downgrade, the per-plugin lock (install, update and remove answer `job_running`), remove, and the hub update ending
  in a restart request.
- T2.F security fixes (`security_test.go`): `TestKeyRebuildCannotResurrectARevokedKey` (a stalled SIGHUP rebuild and
  a revoke's rebuild land in order), `TestPruneAudit` (180 days, at start), `TestFirstRunWithoutATerminal` (no PIN in
  the log; `homed admin firstrun` shows the page, reopens it after it closed, refuses once the owner exists),
  `TestFirstRunAddr` (0.0.0.0:8090 only without a relay), `TestTerminalOf`, `TestPublicServerTimeouts` (ReadTimeout cuts a
  stalled body, never a long answer); `TestRelayConnectErrors` checks that a code on the command line is refused
  without being echoed.
- The CLI without a running homed (`TestOfflineCommands`: keys, plugins, `admin setup-link`/`reset-access`, through the
  in-process admin API), `TestPhase1Import` (Roman's homed.yaml shape), `relay connect` (stdin, comments kept, the
  domain-change hint, audit), `key new --keychain` against a fake `security` that must get the key on stdin and never in
  argv (and a Keychain failure revoking the key), the Host guard, `--dev-origin` parsing, key id matching.

```sh
go test -race ./cmd/homed/... ./internal/config/...
```

### Real-homed end to end (`web/tests/real`, T2.6)

Playwright against a **real** homed, nothing mocked. `global-setup.ts` builds homed (with the SPA that `make web`
embedded) and the fake plugin into a fresh `/tmp/homie-e2e-*` dir, adds the plugin, and runs `homed run --dev-origin
http://localhost:<port>` on a data dir there (127.0.0.1 only, free ports). One story, in Chromium with Chrome's
virtual authenticator (real WebAuthn ceremonies): the LAN first-run page (the PIN from `homed admin firstrun`, never in
homed's log since it runs without a terminal; then the server code step, as there is no relay in development) →
`homed admin setup-link` → the wizard and a passkey for `localhost` → the
home → Устройства → Fake → a tool run from the admin with live progress, released by `homed tool call fake_release` →
Настройки → Ключи доступа: a new key → `GET /api/v1/tools` and `POST /api/v1/tools/fake_echo` with it, as a Site would
→ the call in Журнал → revoke → the key gets 401. CSP violations and page errors fail it. About 15 s after the builds;
homed is stopped and the temp dir removed afterwards (`E2E_KEEP=1` keeps it for a look).

```sh
make e2e                          # make web, then the suite
npm --prefix web run e2e:real     # the suite alone, when internal/web/dist is already built
```

Prerequisites: Go, Node 22+, `npx --prefix web playwright install chromium` once. No relay, no network after `npm ci`.

### SQLite store (`internal/store`)

Covers the T2.1 implementation of the `store.Store` contract on throw-away databases in `t.TempDir()` with a fake clock:
file mode 0600 (also `-wal` and `-shm`), WAL, foreign keys and busy timeout, embedded migrations (run once, newer schema
refused), every repository (people, passkeys, sessions, person keys, invites, connections, plugins, audit, settings) with
its errors (`ErrNotFound`, `ErrConflict`, `ErrLastPasskey`, `ErrTokenExpired`, `ErrTokenUsed`), cascade delete of a
person, audit filters, cursor paging, limit caps and prune, `InTx` (commit, rollback, nesting, the first-run flow), the
races that must have one winner (the last two passkeys, one setup token), concurrent writers, and `ImportPhase1`
(owner first, once per database, merge into an existing home, bad input changes nothing). No network, no Docker.

```sh
go test -race ./internal/store/...
```

### Plugin installer and homed self-update (`internal/install`)

Runs against an httptest TLS fake of the GitHub Releases API and the asset host (no real network, no Docker): release
resolving (latest, by version, `v` and bare tags, prerelease and draft handling, rate limit and 404 mapping), asset
choice by the GoReleaser name (incl. armv7), checksum verification (mismatch, missing file, missing line), the full
install and update flows with their job steps, start failure with rollback, same-version replacement, pruning and
removal, the size limits (advertised and streamed), https-only incl. a redirect to plain http, the token going only to
the API host and never into errors, and the archive guard (`..`, absolute and backslash names, links of any kind incl.
the chained-symlink escape of the T2 review, devices, duplicates, entry and size caps, a gzip bomb, mode
normalization), the manifest written through `os.Root` (`TestWriteManifest`) and parsed strictly (no trailing data),
and downgrades refused unless allowed (`TestUpdateRefusesDowngrade`, hub too). The homed self-update replaces a
fake executable in a temp dir (swap, `.prev` backup, rollback, failing self-test, read-only directory, symlinked
executable). About 2 s.

```sh
go test -race ./internal/install/...
```

- The plugin host tests also cover translated tool texts and progress (`TestToolI18n`, `TestCallProgress`, `TestParseI18n`, `TestDeliverProgress`; T2.7).
- And run-time changes of the plugin set (T2.6): `Add`, `Restart`, `Remove` (the process has exited before it
  returns, its tools are gone), `Sync` (unchanged plugins keep running, a new command restarts, invalid sets change
  nothing), `Await` (up, a run that failed since the start, the context), adding before `Start` and after `Close`
  (`TestAddRestartRemove`, `TestAddBeforeStartAndAfterClose`, `TestSync`, `TestAwait`).

### Admin API backend (`internal/admin`)

Runs the admin JSON API (T2.3) on `httptest` servers for the web and the socket handler, with in-memory fakes of the
store, the plugin host, auth (cookie → principal), the relay, the installer and the key resolver; no network, no
Docker. Covers the error shape and common headers, JSON 404/405, auth levels (public, session, owner, member 403, socket
principal), the CSRF rule (header, Origin, Sec-Fetch-Site; socket exempt), content type and size limits, step-up
(missing, expired, per session; socket exempt), key create/revoke with the resolver updated before the answer and no
secret in lists or audit, passkeys and sessions of "me", tools/plugins/inventory cache/logs, admin tool calls (schema
validation into `fields`, 409 `call_running`, progress with `message_i18n`, `wait=1`, cancel, audit with redaction),
audit paging with filter-bound cursors, relay state and the domain-change rule, jobs (409 `job_running`, step reports,
coded failures), and SSE (hello, replay after `Last-Event-ID`, `resync` for another run or an id that left the
500-event ring, member filtering, `plugin.log`, `plugin`, `tools`, `audit`, `session.revoked` on revoke and on expiry),
and the CSRF origin read per request (`TestOriginFunc`, T2.6). T2.F adds: downgrades only on the socket with the
flag (`TestDowngradeOnlyOnTheSocket`), one job per plugin (`TestOneJobPerPlugin`), the https-only preview homepage
(`TestPreviewHomepage`), passkeys of another relay domain (`TestPasskeysOfAnotherDomain`), the shared name rule and
the `person.update` audit of `PATCH /me` (`TestNamesAndPatchMe`), and the gateway's audit text for admin tool calls
(`TestToolCallAuditText`).
About 2 s.

```sh
go test -race ./internal/admin/...
```

### Passkeys, sessions and the first-run page (`internal/auth`, `internal/firstrun`)

Runs real WebAuthn ceremonies (go-webauthn) against a virtual authenticator (`github.com/descope/virtualwebauthn`,
EC2 passkeys, synced and hardware-key flags) on an in-memory store (`internal/auth/authtest`) and a fake clock; no
network, no Docker, no browser. `internal/auth`: setup link → registration → Me and the `__Host-homie_session` cookie →
discoverable sign-in → logout; an imported owner, a family invite, renames and the home name; registration and sign-in
rejected for a wrong origin (incl. http), a wrong or parent RP ID, missing user verification or presence, bad backup
flags, unknown passkeys and user handles; replayed and expired ceremonies, an answer to another challenge, a ceremony
finished at the wrong endpoint; setup tokens single use (also two registrations racing), expired (also between begin
and finish, with rollback), revoked by a newer link, unknown; step-up (5-minute window, renewal, another person's
passkey, another session's ceremony), add passkey behind step-up (`excludeCredentials`, `passkey_exists`), clone
detection by the sign counter, idle and absolute session expiry, touch throttling and event streams, the hourly sweep,
the CSRF rule, request format errors, the rate limits per class (`TestRateLimitTokens`: per client from the Caddy hop
of `X-Forwarded-For` and in total, no address = own bucket; `TestRateLimitLogin`: begins and failed finishes per
address, no shared bucket; `TestRateLimitStepUpPerSession`: an anonymous flood never touches a session's step-up;
`TestCeremonyPools`), the audit of failures with the client's address (`TestFailuresAreAudited`), `OwnerReady` by
relying party, setup links with a TTL and a client (`TestSetupLinkTTLAndClient`), `homed admin setup-link` and
`reset-access` over the local handler, and the name (incl. quotes, separators, bidi overrides), slug, device-label and
credential-storage helpers. `internal/firstrun`: the PIN gate (8 digits, cookie only hashed, 30 minutes), five wrong
PINs lock that device only (`TestPINLockoutPerClient`), 20 close the page for everybody with `410 setup_closed`
(`TestPINFailureCapClosesThePage`, `TestServeClosesAfterTheCap`), the page closing after its time and the PIN
changing on a timer and after every link (`TestServeClosesAndRotates`, `TestFirstRunEndToEnd`), no PIN without a
terminal (`TestNoTerminalNoPIN`), the relay code step (bad codes, no token in errors, `Connect`, `409 conflict` once a
relay exists, the client in the audit), `relay_not_ready` until connected with a passing self-check, 3-minute links →
passkey registration end to end → every path `410 setup_done`, the Host guard against DNS rebinding, CSRF, routes, the
SSE `relay` stream, and `Serve` printing the URL and PIN. About 3 s.

```sh
go test -race ./internal/auth/... ./internal/firstrun/...
```

### Admin UI (`web/`, `internal/web`)

Everything runs against the mock API in `web/src/mocks` (MSW 3, the whole of `docs/ADMIN-API.md` including SSE, the
LAN first-run API and a fake homie-tv with progress); scenarios are picked with `?mock=<name>`
(`web/src/mocks/data.ts`). No homed, no network after `npm ci` and `npx playwright install chromium`.

- `npm --prefix web run typecheck` and `npm --prefix web run lint` (the CI web job runs both, then the build).
- `npm --prefix web test` — Vitest (jsdom, about 1 s): the JSON Schema → form rules of DESIGN.md §9.3 (every control,
  ordering, labels from `i18n`, defaults, arguments, validation words, server `invalid_args`), the SchemaForm
  component (device picker with "Другое…", slider, segmented control with arrow keys, select, secret toggle, switch,
  chips, checklist, JSON editor, nested object, folding), tool grouping (§9.2), RU/EN key and placeholder parity, and
  the https-only link of a plugin homepage (`lib/links.test.ts`).
- `npm --prefix web run e2e` — Playwright smoke (Chromium, about 1 min). It builds the mock bundle and serves it with
  `vite preview` under the production CSP (`internal/web/csp.txt`); any CSP violation or page error fails a test.
  Sign-in and the first-run wizard use Chrome's virtual authenticator (real WebAuthn ceremonies); also an admin tool
  with progress (`tv_pair`), form validation and a failed run with retry, discovery → add from the result, step-up
  before creating a key, the LAN page (8-digit PIN, this device waiting after five wrong PINs, connecting, QR link, the
  closed page), the offline home, a destructive action's confirmation, no layout shift while navigating, and axe
  (WCAG 2.1 AA) on eight screens in both themes.
- `npm --prefix web run screenshots` — regenerates `docs/screenshots/*.webp` (git-ignored; a curated set is committed in `docs/images/`) (63 screens × dark/light ×
  phone 390×844 / desktop 1440×900, about 10 min). `-- --grep "phone dark"` narrows it.
- `go test -race ./internal/web/...` — the Go handler: SPA fallback for client routes (`/admin/...`, `/firstrun`,
  `/join`), immutable hashed assets, 404 (not HTML) for missing assets, dotfiles never served, ETag/304, gzip and
  `q=0`, HEAD, 405, the CSP and security headers (HSTS included), the 503 hint before `make web`, and the embedded build.

Manual: `npm --prefix web run dev:mock` serves the mock app at http://localhost:5173/admin/ (`/firstrun` for the LAN
page, PIN 1234 5678); `npm --prefix web run dev` proxies `/api` to a homed on 127.0.0.1:8080 started with
`--dev-origin http://localhost:5173`.

### Home installers (`install/test/`)

```sh
install/test/run.sh            # lint + mac + linux (all)
install/test/run.sh lint       # shellcheck of home.sh, the wrapper and the tests
install/test/run.sh mac        # macOS, temp HOME
install/test/run.sh linux      # Docker: Debian containers homie-t41-*
PLATFORMS="linux/amd64" SYSTEMD_PLATFORMS="" KEEP_IMAGES=1 install/test/run.sh linux
```

- `fakerelease.py` (stdlib only, run from a venv the script makes in a temp dir): a fake GitHub release server with the
  real shapes (`/latest` redirect, `/download/<tag>/...`, `/raw/<ref>/install/...`) and releases v0.0.1 (flat),
  v0.0.2, v0.0.3 (wrapped archives), v0.0.4 (checksum mismatch), v0.0.5 (archive with a `../evil` entry), v0.0.6
  (linux arm64 only). The "homed" in the archives is a shell script (`version`, `run`, `admin firstrun`, `env`).
- mac: `--no-start` in a temp HOME with a `launchctl` shim that fails the test on any call (files, modes,
  `plutil -lint`, every plist key, re-run no-op, update, `.prev`, refusals, wrapped archive, `--prefix` with a space,
  uninstall, `--purge`; the real LaunchAgent's checksum and pid are compared before and after), then the fake homed
  under real launchd with the TEST label `com.github.devxpro.homie.homed.t41` in a temp HOME, never the real label:
  first-run output, re-run no-op, update restart, exit 75 restart, uninstall.
- linux: `Dockerfile.linux` (Debian bookworm; target `base`, target `systemd` with systemd as PID 1) on every platform
  in `PLATFORMS` (default native + amd64; arm/v7 is opt-in, its image takes 30+ minutes under qemu): `--no-start` flows
  (user, modes, owners, unit content, `systemd-analyze verify`, the wrapper as root / as homie / as an unprivileged
  user with and without sudo rights, idempotent re-run, update, refusals, a foreign file at the command path, files
  the service user plants in its program dir (a swapped binary, a `homed.prev` symlink) never run or written as root,
  curl | bash with templates from the raw URL, `--prefix` with a space, uninstall, purge, not-root refusal) and, on
  `SYSTEMD_PLATFORMS`, a real start, process owner, loaded hardening, UDP broadcast and multicast join from inside
  the sandbox, restart after exit 75, no-op re-run keeps the PID, update, uninstall.

### Plugin template (`templates/plugin`)

A module of its own (`replace github.com/devXpro/homie => ../..`), so `./...` of the repository does not include it:

```sh
go -C templates/plugin test -race ./...
env -C templates/plugin golangci-lint run ./...
```

The tests drive the plugin through an in-memory MCP client against a fake Shelly device: the inventory tool, a switch
with a device argument, an admin action with progress and its Russian texts.
