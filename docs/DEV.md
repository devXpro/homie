# Running homed locally

How to build and run the hub (`homed`) and the TV plugin (`homie-tv`) on a Mac or Linux box for development: a scratch
data dir, a **fake `cloudflared`** so nothing touches Cloudflare, the first passkey, the CLI over the local socket,
and starting homed at login with launchd. Nothing here prints a secret, except the one-time links you ask for.

Paths below assume working copies side by side; change them to yours.

```sh
G="$HOME/src/homie"
T="$HOME/src/homie-tv"
```

## 1. Build

```sh
make -C "$G" build    # builds the admin SPA (make web), then $G/bin/homed with it embedded
make -C "$T" build    # → $T/bin/homie-tv
"$G/bin/homed" version
```

`make build` runs `make web` first (`npm ci` and the Vite build into `internal/web/dist`). A homed built without it
answers `/` with 503 and a hint.

`homed` builds on the public MCP Gatekeeper module (`github.com/devXpro/mcp-gatekeeper`). To work on both at once,
put a `go.work` that uses the two checkouts next to them and **never commit it**:

```sh
cat > "$G/go.work" <<'EOF'
go 1.26.11

use (
	.
	../mcp-gatekeeper
)
EOF
```

and the admin's UI package (`web/package.json`'s `@devxpro/mcp-gatekeeper-ui`) comes from the gatekeeper's git tag, or
from a local checkout while developing both.

## 2. The data dir

`homed` keeps everything in one directory (mode 0700). It defaults to `~/Library/Application Support/homie` on macOS
and `$XDG_DATA_HOME/homie` on Linux; every command takes `--data-dir D`, and `HOMED_DATA_DIR` overrides the default.
**Never point a development homed at the real data dir.**

| File | What |
|---|---|
| `homed.yaml` | what homed needs before its database opens: `listen`, `setup_listen`, `tunnel:` (0600) |
| `instance-id` | random id sent as `X-Homie-Instance`; the tunnel self-check looks for it |
| `homie.db` | SQLite (0600): people, passkeys, sessions, OAuth clients and grants, invites, plugins, settings, activity, audit |
| `homed.sock` | local admin socket of the running homed (0600, own user only); the CLI talks to it |
| `tunnel/` | `config.yml`, `credentials.json`, `tunnel.json` of the named tunnel, and an empty private `HOME` for cloudflared |
| `logs/<plugin>.log`, `logs/cloudflared.log` | stderr of each plugin and of cloudflared (rotated at 5 MB) |
| `plugins/<id>/data/` | each plugin's own data (`HOMIE_DATA_DIR`), e.g. homie-tv's `tvs.json` with TV pairing keys |
| `plugins/<id>/bin/<version>/` | plugins installed from GitHub releases in the admin |
| `homed.yaml.v0.1`, `homed.db.v0.1` | after the upgrade from v0.1: the old files, kept (0600) |

```yaml
# homed.yaml
listen: 127.0.0.1:8080          # loopback only; cloudflared forwards here
setup_listen: 127.0.0.1:8090    # optional: the first-run page; default 0.0.0.0:8090 without a tunnel, 127.0.0.1:8090 with one
tunnel:
  mode: named                   # named | quick | off (written by `homed cloudflare …`)
  hostname: home.example.com    # named mode only
  cloudflared: /path/to/cloudflared   # optional; default: "cloudflared" on PATH, then next to homed
```

Unknown keys are an error, so a typo is caught at start. People, plugins and the home name are changed in the admin or
with the CLI, never in homed.yaml. A leftover `relay:` section of v0.1 is ignored with a warning and removed by the
first start's migration.

## 3. A scratch hub with a fake cloudflared

Never use the real `cloudflared` against Cloudflare while developing. The repository builds a fake that plays the CLI
(`tunnel login`, `create`, `route dns`, `run`, and the quick tunnel's `/quicktunnel` endpoint) and is steered by files
in a `control/` directory next to it:

```sh
W=/tmp/homed-scratch
mkdir -p "$W/bin/control" "$W/data" && chmod 700 "$W/data"
go -C "$G" build -o "$W/bin/homed" ./cmd/homed
go -C "$G" build -o "$W/bin/cloudflared" ./internal/tunnel/testdata/fakecloudflared
cat > "$W/data/homed.yaml" <<EOF
listen: 127.0.0.1:18080
setup_listen: 127.0.0.1:18090
tunnel:
  cloudflared: $W/bin/cloudflared
EOF
chmod 600 "$W/data/homed.yaml"
touch "$W/bin/control/login-release"      # lets the fake's "Cloudflare login" finish at once
export HOMED_DATA_DIR="$W/data"
```

- `control/login-release` lets the fake login finish; `login-fail` fails it. `dns-exists` makes `route dns` fail as when
  the record exists (until `--overwrite-dns`). `die` makes a running tunnel exit once (so you see `failing` and the
  restart); `exit-now` makes it exit at once; `no-register` holds back "Registered tunnel connection". The fake's quick
  tunnel answers on `example-<n>.trycloudflare.com`, a new host at every start.
- The hub's self-check fetches `https://<host>/healthz` for real. Against the fake that name does not exist, so the
  state stays `probing`; to keep the check off the network, run homed with `HTTPS_PROXY=http://127.0.0.1:9` (a dead
  local proxy). The status then shows *the public URL does not answer yet: … proxyconnect … connection refused*.
- **Hide the real cloudflared.** If the configured `tunnel.cloudflared` does not exist, homed warns and falls back to
  `cloudflared` on `PATH`, which on a developer's box is the real one. Run scratch hubs with a `PATH` without it:
  `env PATH=/usr/bin:/bin "$W/bin/homed" run …`.

Run it in the foreground (logs go to stderr; Ctrl-C stops it cleanly):

```sh
env PATH=/usr/bin:/bin HTTPS_PROXY=http://127.0.0.1:9 "$W/bin/homed" run
```

Expect `homed listening addr=127.0.0.1:18080 … tunnel=off`, and the first-run page's line. `HOMED_LOG_LEVEL=debug` adds
detail. Only one homed can run per data dir: a second one exits with "homed is already running".

In another terminal (the same `HOMED_DATA_DIR`):

```sh
H="$W/bin/homed"
"$H" cloudflare connect home.example.com     # prints the (fake) login link, then Done: tunnel homie-home-example-com (…)
"$H" cloudflare status                       # mode named, state probing, the address and the MCP address
"$H" cloudflare quick                        # a quick tunnel: a new example-<n>.trycloudflare.com address
"$H" cloudflare disconnect
"$H" admin firstrun                          # the first-run page's address and the PIN
"$H" admin setup-link                        # the link alone on stdout, the explanation on stderr
```

`homed cloudflare connect` also works while homed is **stopped**: it writes `tunnel/` and the `tunnel:` block, and the
next `homed run` starts the tunnel.

## 4. Add a plugin

The commands that talk to the hub need it running (§3). Flags go **before** `--`; everything after it is the plugin's
command (a `--data-dir` there would be passed to the plugin):

```sh
go -C "$G" build -o "$W/bin/echo" ./pluginkit/example               # a test plugin with id "echo"
"$H" plugin add --id echo -- "$W/bin/echo" serve
"$H" plugin list
"$H" plugin add --id tv -- "$T/bin/homie-tv" serve                  # the real TV plugin
```

The executable is stored as an absolute path, and the hub starts it at once. `homed plugin remove echo` stops and
removes it; its data dir (the paired TVs) stays unless `--delete-data`. Plugins released on GitHub are installed,
updated (with a rollback when the new version does not start) and removed in the admin: Devices → Add devices.

## 5. Give the owner a passkey

Until an owner can sign in, homed also serves the **first-run page** and, when it runs in a terminal, prints its address
and an 8-digit PIN there:

```
homed first run: open http://localhost:18090/firstrun and enter the PIN 4821 0937 (the page closes at 19:45)
```

Open it, type the PIN, choose a domain or a quick tunnel, wait for *connected*, then open the one-time link (a QR code
for the phone, or "open on this computer"). Passkeys need a real HTTPS host, so with the fake tunnel use `--dev-origin`
(§7) for the browser part. The page's rules (docs/SECURITY.md):

- it listens on the home network (`0.0.0.0:8090`) only while homed has no tunnel — macOS may then ask once whether
  homed may accept incoming connections — and on `127.0.0.1:8090` once it has one (`setup_listen` in homed.yaml or
  `--setup-listen` overrides both);
- the PIN changes every 10 minutes and after every link (a terminal shows the new one);
- the page closes 30 minutes after it opened, after 20 wrong PINs, and shortly after the owner registered.

Without the page, from a terminal on the home box:

```sh
"$H" admin setup-link     # prints https://<host>/setup#t=… (valid 15 minutes, once)
"$H" admin reset-access   # a lost or stolen device: removes every passkey and session, prints a new link
```

The link is a secret until it is used. A reset leaves the plugins, the tunnel and the assistants' connections alone;
remove the connections you do not trust in the admin.

## 6. Call tools by hand

`homed tool call <name> ['<json>' | -]` calls any tool through the running homed as the owner, via `cli`, and writes
the activity entry. It prints the plugin's progress to stderr while it runs, then the tool's text and its structured
result. A tool that ran and failed prints its message to stderr and exits with status 1. With `-` the arguments come
from stdin: use it for anything secret (a pairing PIN), which must stay out of the shell history and the process list.

```sh
"$H" tool list
"$H" tool call echo_say '{"text":"hi"}'
"$H" tool call tv_discover
"$H" tool call tv_add '{"host":"192.0.2.20","name":"Living room TV"}'
"$H" tool call tv_pair '{"tv":"<id from tv_add>"}'    # … Press OK on the TV remote
printf '%s' '{"tv":"<id>","pin":"<from the TV>"}' | "$H" tool call tv_pair -   # a PIN, via stdin
```

The same actions are in the admin: Devices → TVs.

## 7. Development without a tunnel or a TV

`--dev-origin http://localhost:<port>` runs homed with no tunnel, with passkeys for `localhost` (browsers allow WebAuthn
and the `Secure` session cookie on `http://localhost`):

```sh
D=/tmp/homed-dev && mkdir -p "$D" && chmod 700 "$D"
go -C "$G" build -o "$D/homed" ./cmd/homed
go -C "$G" build -o "$D/echo" ./pluginkit/example                   # plugin id "echo"
env PATH=/usr/bin:/bin "$D/homed" run --data-dir "$D/data" --listen 127.0.0.1:8080 --setup-listen 127.0.0.1:8090 \
  --dev-origin http://localhost:8080 &
"$D/homed" plugin add --data-dir "$D/data" --id echo -- "$D/echo" serve
"$D/homed" admin setup-link --data-dir "$D/data"       # open it, create the passkey, then http://localhost:8080/
"$D/homed" tool list --data-dir "$D/data"
```

In this mode the tunnel is `off` and its `url` is the development origin. homed refuses `--dev-origin` on a data dir
that configures a tunnel, so use a separate one. For the SPA with hot reload, start homed with
`--dev-origin http://localhost:5173` and run `npm --prefix "$G/web" run dev` (Vite proxies `/api` to
`127.0.0.1:8080`); `npm --prefix "$G/web" run dev:mock` serves the SPA against the mock API with no homed at all (see
[TESTING.md](TESTING.md)). Keep `--data-dir` paths short: the socket path must fit in 103 bytes.

## 8. Run at login with launchd

A LaunchAgent runs homed as you whenever you are logged in and restarts it if it stops: after a crash, and after a hub
update from the admin, where homed replaces its binary, shuts down cleanly and exits with status 75 on purpose
(`KeepAlive` starts the new binary). Save as `~/Library/LaunchAgents/com.github.devxpro.homie.homed.plist`, with your
paths (a plist does not expand `$HOME`: replace `/PATH/TO/HOME` with your home directory and `/PATH/TO/homie` with your checkout):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.github.devxpro.homie.homed</string>
	<key>ProgramArguments</key>
	<array>
		<string>/PATH/TO/homie/bin/homed</string>
		<string>run</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/bin:/bin</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>/PATH/TO/HOME/Library/Logs/homie/homed.log</string>
	<key>StandardErrorPath</key>
	<string>/PATH/TO/HOME/Library/Logs/homie/homed.log</string>
</dict>
</plist>
```

The `PATH` matters: homed starts `cloudflared` as its child and finds it there (the installer's own plist points it at
its bin dir first, then Homebrew's).

```sh
L=com.github.devxpro.homie.homed
mkdir -p ~/Library/Logs/homie
plutil -lint ~/Library/LaunchAgents/$L.plist
# Stop a homed running in a terminal first: only one may run per data dir.
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/$L.plist   # start now and at every login
launchctl print gui/$(id -u)/$L | grep -E 'state|pid'               # running, pid …
launchctl kickstart -k gui/$(id -u)/$L                               # restart (after a rebuild)
launchctl bootout gui/$(id -u)/$L                                    # stop and unload
tail -f ~/Library/Logs/homie/homed.log
```

launchd gives the agent `HOME`, so the default data dir is the same as in a terminal. The log file is not rotated; it
stays small at the info level. Under launchd homed has no terminal, so it never prints the first-run PIN (the log says
so instead): run `homed admin firstrun` in a terminal for the PIN, or `homed admin setup-link` for a link straight
away. On Linux the systemd unit needs `Restart=always` (or `on-failure`, with 75 kept out of `SuccessExitStatus=` and
`RestartPreventExitStatus=`).

**Going back to an older version.** The admin never installs a release older than the one running. On the home box:

```sh
"$H" update --version 0.2.0 --allow-downgrade
```

**Local Network permission.** macOS asks once whether homed may find devices on the local network, the first time the
TV plugin (a child of homed) talks to the LAN: discovery, wake, or any TV command. Click **Allow**. A homed started from
a terminal runs under the terminal's permission, so the prompt comes again the first time homed runs from launchd; a
rebuilt binary may also be asked again. If the prompt was missed or denied, LAN calls fail at once (tools report the TV
as offline): turn homed on in System Settings → Privacy & Security → Local Network, then `launchctl kickstart -k`.

## 9. When something is wrong

| Symptom | Look at |
|---|---|
| a tool says the plugin is not running | `homed plugin list` (state and last error), `$HOMED_DATA_DIR/logs/<id>.log`, the admin's module log |
| `cloudflare status` says `probing` with a *does not answer yet* error | expected with a fake cloudflared; against a real one see [TROUBLESHOOTING.md](TROUBLESHOOTING.md#the-tunnel-states-and-errors) |
| `cloudflared was not found` in the log | `tunnel.cloudflared` in homed.yaml, or put cloudflared next to homed or on `PATH` |
| `421 misdirected` | the request's Host is neither the current public host nor `127.0.0.1:<port>`/`localhost:<port>` |
| the admin says the passkey does not open the home | the public host changed: `homed admin reset-access` (§5) |
| `cannot reach the homed on …` from a CLI command | start it (§3 or §8); only `run`, `version` and `cloudflare connect` work without it |
| `/` answers 503 | homed was built without the SPA: `make -C "$G" build` |
| the first-run page does not answer, or says it is closed | it closes after 30 minutes or 20 wrong PINs: `homed admin firstrun` |
