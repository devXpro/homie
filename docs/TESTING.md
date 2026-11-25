# Testing

Every suite is listed here with the exact command to run it. None of them talks to Cloudflare, ChatGPT or Claude, and
none touches a real install: **`cloudflared` is a fake everywhere** (built from
`internal/tunnel/testdata/fakecloudflared`), and the browser tests use the project's own Playwright and Chromium.

## Prerequisites

- Go (see `go.mod`; the `go` command fetches the pinned toolchain), golangci-lint v2.12.2, Node 22+ (only for `web/`),
  Docker (only for the integration tests and the Linux installer tests), shellcheck (installer tests).
- The module `github.com/devXpro/mcp-gatekeeper` is a normal dependency. To test against a local checkout of it, use a
  `go.work` (never committed): see [DEV.md](DEV.md#1-build).

## Go unit tests and lint

```sh
make lint   # golangci-lint run ./...
make test   # go test -race ./...
make vuln   # govulncheck v1.9.0 on the go.mod toolchain (go1.26.11): no known vulnerability reachable from the code
make privacy   # the generic privacy scan: local paths, private LAN addresses, real MAC addresses (CI runs it too)
```

`go.mod` pins the toolchain, so the `go` command fetches that patch release whatever Go is installed. Run govulncheck
from inside the module (`make vuln`, or `go -C <repo> run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`): it reads
`go env GOVERSION` from its working directory. CI has a `vuln` job in both repos.

## Integration tests

Tests that need Docker or the LAN are behind the build tag `integration`:

```sh
go test -race -tags integration ./...
```

## The fake cloudflared and the test hygiene

Tests that start a tunnel use `internal/tunnel/tunneltest`: it builds the fake once per test binary, copies it next to a
`control/` directory and steers it with files (`login-release`, `dns-exists`, `die`, …; see the header of
`internal/tunnel/testdata/fakecloudflared/main.go`). `tunneltest.Edge` is an HTTP client that plays Cloudflare: it sends
a request with the public Host and a `CF-Connecting-IP` to the hub's loopback listener. Rules every test follows:

- the data dir is a temp dir under `/tmp` (a unix socket path must fit in 103 bytes) and `HOMED_DATA_DIR` points into it;
- `PATH` holds only `go` and `git` (a real `cloudflared` may sit next to `go`), and every daemon test names the fake in
  `tunnel.cloudflared`;
- first-run pages run on free loopback ports, never 8090, never `0.0.0.0`;
- the browser opener is replaced; no test opens a browser;
- `tunneltest.Main` removes the built fake after the package's tests.

## Suites

### homed end to end (`cmd/homed`)

All in-process on temp data dirs, with the fake cloudflared, the fake plugins (`internal/plugins/testdata/fakeplugin`
and `pluginkit/example`), virtual passkeys (`github.com/descope/virtualwebauthn`) and `tunneltest.Edge`. About a
minute with `-race`.

- `TestGuestEndToEnd` is the acceptance of the whole Go side: a named tunnel (fake) → `homed admin setup-link` over the
  socket → the owner's passkey → `/api/account` (resources, devices) → `DELETE /api/account` refused → the permission
  catalog (the plugins, no `all`) → an invite for a guest with one plugin and a one-hour term → the guest's passkey →
  dynamic client registration, PKCE, consent and the code exchange as ChatGPT or Claude would → `tools/list` shows only
  that plugin's tools → a call reaches the plugin with the guest as caller, via `claude` → a tool of another plugin is
  refused → the owner sees all tools → `authorize` for another resource is `invalid_target`, scope `mcp:other` is
  `invalid_scope`, a store-made token of another resource is refused at `/mcp`, `/mcp/other` is 404, `/api/v1/tools` is
  404 → one hour later (fake clock): the guest's MCP call is 401 with a Bearer challenge, their refresh and session are
  refused, and the owner carries on.
- `TestMigrationFromV01`: a v0.1 data dir (a relay in `homed.yaml`, `relay-token`, `homed.db` from a neutral fixture)
  starts as v0.2: backups `0600`, the token gone, tunnel `off`, `setup_listen` on loopback, plugins running, the home
  name and language imported; a second start migrates nothing.
- `TestNamedConnectFromThePage` (a domain connected from the first-run page: the page moves to its loopback default with
  the same PIN and cookie, the link, the owner), `TestQuickHostChange` (the owner at address 1; a restart gives address
  2: the old Host is `421`, the old passkey does not count, the page that was closing stays open, its link adds a
  passkey to the same person; the next restart reopens the page by itself; an invite in quick mode is refused),
  `TestNoTunnel` (`503 home_not_ready`, the CLI says to connect first), `TestDevMode` (`--dev-origin`),
  `TestUnknownTunnelModeRefusesToStart`.
- `main_test.go`: the CLI contract (`TestCommandsNeedRunningHomed` exits 1 until the socket is up,
  `TestCloudflareConnectOffline` works with homed stopped, `TestPluginAddArgs`, `TestLinkOpener`), the Host guard and
  the dev origin parsing. `security_test.go`: the activity pruning, `TestFirstRunAddr` (never on the home network once a
  tunnel is configured), the terminal check and the public server's timeouts.
- `TestInstallerAdapter`: the admin's installer against an `httptest` TLS fake of GitHub with the real store and plugin
  host: install (steps, store row, plugin up), a second install refused before it touches the files, an update whose
  binary does not start (rolled back), a good update, a refused downgrade, the per-plugin lock, remove, and the hub
  update ending in a restart request.

```sh
go test -race ./cmd/homed/...
```

### Tunnel and first-run page (`internal/tunnel`, `internal/firstrun`)

`internal/tunnel` (the manager over the gatekeeper's supervisor): a named connect, an existing DNS record (`code`
`dns_exists`, then `overwriteDns`), a bad host name, the quick tunnel, a host that changes at every start, a child that
dies and is restarted, disconnect, the probe on `X-Homie-Instance`, connect while homed is stopped, `FindCloudflared`,
the child log rotation, and the adminsock command handlers. `internal/firstrun`: the PIN gate (8 digits, the cookie
only hashed, 30 minutes), five wrong PINs lock that device only, 20 close the page for everybody, the page closing and
the PIN rotating on a timer and after every link, no PIN without a terminal, CSRF, the Host guard against DNS rebinding,
the tunnel step (domain and quick), 3-minute links, the reconnect state, and re-serving on another listener.

```sh
go test -race ./internal/tunnel/... ./internal/firstrun/...
```

### Store and migration (`internal/store`, `internal/config`)

`internal/store` runs Homie's tables (plugins, settings, activity) next to the gatekeeper's on `sqlitestore`, in temp
dirs: file modes `0600`/`0700`, one transaction across both schemas, activity filters, cursor paging, limits and
pruning, the audit naming the actor. `ImportV01` runs against a fixture generated from the v0.1.1 schema with neutral
data (`internal/store/testdata/homed-v0.1.1.sql`): the import, no old database, no settings row, an existing home, a
failure inside the transaction (nothing changes), a crash before the rename (restartable), an earlier backup kept, and
another database refused. `internal/config`: loading and validating `homed.yaml` (unknown keys, loopback-only listen,
the `tunnel:` block, the host name rule), an old `relay:` ignored with a warning, `Update` rewriting only changed
sections, `MigrateV01` (also after a crash, and on a fresh dir), the instance id, the instance lock and the data dir
resolution.

```sh
go test -race ./internal/store/... ./internal/config/...
```

### Gateway, admin API and activity (`internal/gateway`, `internal/admin`, `internal/audit`)

`internal/gateway`: exactly `/mcp` (`/mcp/<x>` is 404), the OAuth challenge, `tools/list` and `tools/call` filtered by
the person's permissions, the per-grant rate limit (fake clock), the 90 s tool timeout cap, the public Host on the
loopback listener, one resource and its catalog, a home renamed while serving (the instructions, the title and the
fingerprint follow), a tool the MCP server refuses being skipped, and the activity entries without validation details.
`internal/admin` (`/api/home/*` with `httptest`): `TestRoles` runs every endpoint as anonymous, member, owner and
step-up; redaction, SSE and the long poll, events own-only for members, the tunnel endpoints, the settings rename, the
access-reset attention item, jobs, calls and the error form. `internal/audit`: redaction and the activity writer.

```sh
go test -race ./internal/gateway/... ./internal/admin/... ./internal/audit/...
```

### pluginkit and the plugin host (`pluginkit/`, `internal/plugins/`)

`pluginkit` is tested in memory against a go-sdk client: identity, instructions, tool `_meta`, the caller's request
`_meta` (`homie/caller`, `homie/via` with `chatgpt`, `claude`, `admin`, `cli`), the tool name rule, `DataDir` and `Serve`
shutdown. The host tests build two real plugins in `TestMain` (`internal/plugins/testdata/fakeplugin` and
`pluginkit/example`) and run them as subprocesses: listing and audiences, `_meta` parsing (timeouts, inventory, bad
values), invalid names skipped, calls with caller `_meta`, `isError`, the call timeout, a crash with last-known tools
and `ErrPluginDown`, restart with backoff, the failing state, a missing binary, `list_changed`, the stderr log (ring and
rotated file), the minimal env and data dir, translated tool texts and progress, run-time changes of the plugin set
(`Add`, `Restart`, `Remove`, `Sync`, `Await`), and shutdown (SIGTERM, and SIGKILL for a plugin that ignores it). About
5 s; no network.

```sh
go test -race ./pluginkit/... ./internal/plugins/...
```

### Plugin installer and homed self-update (`internal/install`)

Runs against an httptest TLS fake of the GitHub Releases API and the asset host (no real network, no Docker): release
resolving (latest, by version, prerelease and draft handling, rate limit and 404 mapping), asset choice by the
GoReleaser name (incl. armv7), checksum verification, the full install and update flows with their job steps, start
failure with rollback, pruning and removal, the size limits, https-only incl. a redirect to plain http, the token going
only to the API host and never into errors, the archive guard (`..`, absolute names, links of any kind, devices,
duplicates, entry and size caps, a gzip bomb, mode normalization), the manifest written through `os.Root` and parsed
strictly, and downgrades refused unless allowed. The homed self-update replaces a fake executable in a temp dir (swap,
`.prev` backup, rollback, failing self-test, read-only directory, symlinked executable). About 2 s.

```sh
go test -race ./internal/install/...
```

### Admin UI (`web/`, `internal/web`)

The SPA is built on the gatekeeper's UI package, and everything below runs against the mock API in `web/src/mocks`
(MSW, aligned to [ADMIN-API.md](ADMIN-API.md), including SSE, the long poll and the first-run API; the fixtures are
neutral: Alice, Bella, `home.example.com`, `192.0.2.x`). Scenarios are picked with `?scenario=<name>` (`default`,
`quick`, `fresh`, `trouble`, `unreachable`, `coowner`, `member`, `signed-out`, `join`, `consent`, `offline`,
`firstrun`, `firstrun-reconnect`, `firstrun-closed`, …). No homed, no network after `npm ci` and
`npx --prefix web playwright install chromium`.

- `npm --prefix web run typecheck` and `npm --prefix web run lint` (`--max-warnings 0`; the CI web job runs both, then
  the build).
- `npm --prefix web test` — Vitest (jsdom): the API client (`X-Homie`, the error forms, a proxy's 530 page as
  *offline*), events (SSE and the long-poll cursor, a rename, the tunnel changing the address), the routes (`/` is
  Homie's landing, `/connect` the package's page), the status model, the permission catalog, the host name check, the
  bootstrap's `live` choice (quick mode polls), and the JSON Schema → form rules of the device screens.
- `npm --prefix web run e2e` — Playwright smoke (Chromium, about 1.5 min). It builds the mock bundle and serves it with
  `vite preview` under the production CSP (`internal/web/csp.txt`); any CSP violation or page error fails a test.
  Covers: sign-in and the setup link, the Home tab and every tab, the Connect page, pairing a TV with progress, tool
  forms and failures, installing a plugin with a step-up, inviting a guest for a week (`permissions` and
  `access_until`), an expired guest and **Extend**, a co-owner invite, renaming the home, a connection check and a
  reconnect, quick mode (polling, *for trying*, the move to a domain, the address following a restart), the first-run
  page (PIN, my domain, the Cloudflare login, the taken address and *replace the record*, quick, a PIN that ran out,
  the page moved to localhost, closed), a member's view, the offline banner, and axe (WCAG 2.1 AA) on eight screens.
- `npm --prefix web run screenshots` — regenerates the screenshots (phone and desktop, dark and light, every changed
  screen); `SCREENSHOTS_DIR=<dir>` sets where they go and `SHOTS='^76'` narrows them.
- `npm --prefix web run e2e:real` (or `make e2e`, which builds the SPA first) — Playwright against a **real** homed,
  nothing mocked: `global-setup.ts` builds homed and the fake plugin into a fresh `/tmp/homie-e2e-*` dir and runs it
  with `--dev-origin`; one story with Chrome's virtual authenticator, from the first-run page to a tool call. About
  15 s after the builds; `E2E_KEEP=1` keeps the dir for a look.
- `go test -race ./internal/web/...` — the Go handler: SPA fallback for client routes, immutable hashed assets, 404 for
  missing assets, dotfiles never served, ETag/304, gzip, HEAD, 405, the CSP and security headers (HSTS included), the
  503 hint before `make web`, and the embedded build.

Manual: `npm --prefix web run dev:mock` serves the mock app at `http://localhost:5173/` (`/firstrun` for the first-run
page); `npm --prefix web run dev` proxies `/api` to a homed on `127.0.0.1:8080` started with `--dev-origin
http://localhost:5173`.

### Home installers (`install/test/`)

Tests of `install/home.sh` against a **fake release server** (`fakerelease.py`, stdlib only, run from a venv the script
makes in a temp dir), a **fake cloudflared**, a **fake `brew`** and a **fake `launchctl`**. Nothing talks to Cloudflare,
and the real `brew` is never run.

```sh
install/test/run.sh            # lint + mac + linux (all)
install/test/run.sh lint       # shellcheck, the privacy script's own tests, and the cloudflared pins
install/test/run.sh mac        # macOS, temp HOME
install/test/run.sh linux      # Docker: Debian containers homie-t41-*
NO_LAUNCHCTL=1 install/test/run.sh mac
PINS_ONLINE=1 install/test/run.sh lint
PLATFORMS="linux/amd64" SYSTEMD_PLATFORMS="" KEEP_IMAGES=1 install/test/run.sh linux
```

Environment variables:

| Variable | Effect |
|---|---|
| `NO_LAUNCHCTL=1` | skips the **real-launchd** test and every read of the live agent. The fake-`launchctl` tests still run. Use it whenever you must not touch launchd (CI runs the real one). |
| `PINS_ONLINE=1` | also re-reads the cloudflared release metadata on GitHub and the image digest on Docker Hub (read-only web) and compares them with the pins in `home.sh`, `Dockerfile` and `Dockerfile.dev`. Without it `lint` only checks that the three files agree. |
| `PLATFORMS` | the Linux container platforms for the `--no-start` flows (default: the host's CPU plus `linux/amd64`; `linux/arm/v7` is opt-in, its image takes 30+ minutes under qemu) |
| `SYSTEMD_PLATFORMS` | the platforms that also get a real systemd start (default the same; empty skips them) |
| `KEEP_IMAGES=1` | keeps the test images for the next run |

What the suite checks:

- **The fake release server** has the real shapes (`/latest` redirect, `/download/<tag>/...`, `/raw/<ref>/install/...`)
  and releases v0.0.1 (flat), v0.0.2, v0.0.3 (wrapped archives), v0.0.4 (checksum mismatch), v0.0.5 (an archive with a
  `../evil` entry), v0.0.6 (linux arm64 only). The "homed" in the archives is a shell script (`version`, `run`, `admin
  firstrun`, `cloudflare …`, `env`). cloudflared is a fake too, served with a matching test sha256
  (`--cloudflared-base`, `--cloudflared-sha256`; the flags are tests-only, and http needs `--allow-http`).
- **macOS:** `--no-start` in a temp HOME with a `launchctl` shim that fails the test on any call (files, modes, `plutil
  -lint`, every plist key, re-run no-op, update, `.prev`, refusals, a wrapped archive, `--prefix` with a space,
  uninstall, `--purge`); the Homebrew flows with a **fake brew** (its prefix is a temp dir; `install cloudflared` puts
  the fake there, and a file named `FAIL` in the prefix makes it fail, so the fall back to the pinned download is tested);
  `--domain` and `--quick-tunnel` with a fake launchctl and a fake homed; then, unless `NO_LAUNCHCTL=1`, the fake homed
  under real launchd with the TEST label `com.github.devxpro.homie.homed.t41` in a temp HOME, never the real label:
  first-run output, re-run no-op, update restart, exit 75 restart, uninstall.
- **cloudflared:** installed next to homed (mode, owner), skipped when current, replaced when older (`.prev` kept),
  refused on a wrong pinned sha256 and on an unsafe tarball, removed by uninstall, never run as root on Linux, the
  service `PATH` in the unit and the plist, a restart of the service when only cloudflared changed.
- **`--domain` / `--quick-tunnel`:** the connect runs after the start, as the service user, with the data dir and
  cloudflared on the `PATH`; a failing connect leaves the install and exits 1; `--no-start` prints the command to run
  later; the arguments are validated.
- **Linux:** `Dockerfile.linux` (Debian bookworm; target `base`, target `systemd` with systemd as PID 1) on every
  platform in `PLATFORMS`: `--no-start` flows (user, modes, owners, unit content, `systemd-analyze verify`, the wrapper
  as root / as `homie` / as an unprivileged user with and without sudo rights, idempotent re-run, update, refusals, a
  foreign file at the command path, files the service user plants in its program dir (a swapped binary or cloudflared,
  a symlinked `.prev`) never run or written as root, `curl | bash` with templates from the raw URL, `--prefix` with a
  space, uninstall, purge, the not-root refusal) and, on `SYSTEMD_PLATFORMS`, a real start, the process owner, the
  loaded hardening, UDP broadcast and multicast join from inside the sandbox, restart after exit 75, a no-op re-run keeps
  the PID, update, uninstall.
- **`.github/privacy-check.sh`** (`make privacy`) in a scratch git repo: fixtures pass, private values are caught, a
  line marked `privacy:ok` passes.

Release checks that `run.sh` does not run (do them before a release): a GoReleaser snapshot (`goreleaser release --snapshot --clean`: archives, the
generated formula with `depends_on "cloudflared"`, and the three images — amd64, arm64, arm/v7 — each running
`cloudflared --version`) and `docker build -f Dockerfile.dev`.

### Plugin template (`templates/plugin`)

A module of its own (`replace github.com/devXpro/homie => ../..`), so `./...` of the repository does not include it:

```sh
go -C templates/plugin test -race ./...
env -C templates/plugin golangci-lint run ./...
```

The tests drive the plugin through an in-memory MCP client against a fake Shelly device: the inventory tool, a switch
with a device argument, an admin action with progress and its Russian texts.
