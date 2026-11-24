# Running homed locally

How to build and run the hub (`homed`) and the TV plugin (`homie-tv`) on a Mac, connect the relay, set up the admin
(passkeys at `https://<domain>/admin`), issue a person key into the Keychain, call tools by hand, and start homed at
login with launchd. Nothing here prints a secret, except the one-time links and keys you ask for.

Paths below assume the working copies in `/Users/devxpro/projects/family/`.

```sh
G=/Users/devxpro/projects/family/homie
T=/Users/devxpro/projects/family/homie-tv
D="$HOME/Library/Application Support/homie"   # the default data dir on macOS
```

## 1. Build

```sh
make -C "$G" build    # builds the admin SPA (make web), then $G/bin/homed with it embedded
make -C "$T" build    # → $T/bin/homie-tv
"$G/bin/homed" version
```

`make build` runs `make web` first (`npm ci` and the Vite build into `internal/web/dist`). A homed built without it
answers `/admin` with 503 and a hint.

## 2. The data dir

`homed` keeps everything in one directory (mode 0700). It defaults to `~/Library/Application Support/homie` on macOS
and `$XDG_DATA_HOME/homie` on Linux; every command takes `--data-dir D`, and `HOMED_DATA_DIR` overrides the default.

| File | What |
|---|---|
| `homed.yaml` | what homed needs before its database opens: `listen`, `setup_listen`, the relay domain (0600) |
| `relay-token` | the frps token (0600) |
| `instance-id` | random id sent as `X-Homie-Instance`; the relay self-check looks for it |
| `homed.db` | SQLite (0600): people, passkeys, sessions, person key hashes, plugins, audit, the home name and language |
| `homed.sock` | local admin socket of the running homed (0600, own user only); the CLI talks to it |
| `logs/<plugin>.log` | each plugin's stderr (rotated at 5 MB) |
| `plugins/<id>/data/` | each plugin's own data (`HOMIE_DATA_DIR`), e.g. homie-tv's `tvs.json` with TV pairing keys |
| `plugins/<id>/bin/<version>/` | plugins installed from GitHub releases in the admin |
| `homed.yaml.phase1` | after the upgrade from phase 1: the old homed.yaml, as it was (§3) |
| `audit/audit-YYYY-MM.jsonl` | the phase 1 audit log; kept, no longer written (the audit is in `homed.db`) |

```yaml
# homed.yaml (phase 2)
listen: 127.0.0.1:8080            # loopback only; the relay forwards here
setup_listen: 127.0.0.1:8090      # optional: the first-run page; default 0.0.0.0:8090 without a relay, 127.0.0.1:8090 with one
relay:
  domain: homie.jobbyjobs.work    # the token lives in relay-token
```

Unknown keys are an error, so a typo is caught at start. People, keys, plugins and the home name are changed in the
admin or with the CLI, never in homed.yaml.

## 3. Upgrading a phase 1 data dir

A phase 1 homed.yaml also holds `home_name`, `plugins` and `people` with their key hashes (Roman's has the relay, the
`tv` plugin and his key). The first phase 2 start (or the first CLI command) imports them into `homed.db` once:

- the first person becomes the **owner** (still without a passkey), their keys keep working on the REST façade and
  `/mcp`, the plugins are kept as local plugins, the home name becomes the admin's;
- then those three sections leave homed.yaml; the whole old file is kept as `homed.yaml.phase1` (0600). `listen` and
  `relay` stay. The log says `imported people, keys, plugins and the home name…` with the counts;
- the import runs once per database: sections added to homed.yaml later are not imported (homed warns and moves them
  to a new backup). Do it in the admin or with `homed key …` / `homed plugin …` instead;
- the old `audit/*.jsonl` files stay where they are; the new audit starts empty in `homed.db`.

Then give the owner a passkey (§6). To go back to the phase 1 binary, stop homed and restore `homed.yaml.phase1` as
`homed.yaml`.

## 4. Connect the relay

The connect code goes through stdin, never through argv (`homed relay connect <code>` is refused: the code holds the
relay token and would land in the shell history and the process list):

```sh
security find-generic-password -s homie-relay-code -a roman -w | "$G/bin/homed" relay connect -
```

It writes `relay.domain` into `homed.yaml` and the token into `relay-token` (0600). A running homed switches to the
relay at once (no restart). The first-run page (§6) takes the same code in a browser. A code for a **new domain** also
changes the passkeys' domain (the WebAuthn relying party), so existing passkeys stop working: run
`homed admin reset-access` afterwards (§6) and register again.

## 5. Add the TV plugin

Flags go **before** `--`; everything after it is the plugin's command (a `--data-dir` there would be passed to the
plugin):

```sh
"$G/bin/homed" plugin add --id tv -- "$T/bin/homie-tv" serve
"$G/bin/homed" plugin list
```

The executable is stored as an absolute path. A running homed starts it at once. `homed plugin remove tv` stops and
removes it; its data dir (the paired TVs) stays unless `--delete-data`. Plugins released on GitHub are installed,
updated (with a rollback when the new version does not start) and removed in the admin: Устройства → +.

## 6. Run homed and give the owner a passkey

In a terminal, in the foreground (logs go to stderr; Ctrl-C stops it cleanly):

```sh
"$G/bin/homed" run
```

Expect `homed listening addr=127.0.0.1:8080 … admin=https://homie.jobbyjobs.work/admin`, `plugin up plugin=tv`,
`relay connected` within seconds and `relay self-check passed` right after. `HOMED_LOG_LEVEL=debug` adds detail. Only
one homed can run per data dir: a second one exits with "homed is already running".

Until the owner has a passkey, homed also serves the **first-run page** and, when it runs in a terminal, prints its
address and an 8-digit PIN there:

```
homed first run: open http://localhost:8090/firstrun and enter the PIN 4821 0937 (the page closes at 19:45)
```

Open it, type the PIN, paste the relay code if homed has none yet, wait for "connected", then open the one-time link
(a QR code for the phone, or "open on this computer" for Touch ID). It leads to `https://<domain>/admin/setup#t=…`:
name the home, your name, create the passkey. A link from this page works once and for 3 minutes; "Новая ссылка"
makes another. The page is plain http, so it is kept short-lived (docs/SECURITY.md):

- it listens on the home network (`0.0.0.0:8090`) only while homed has no relay — macOS may then ask once whether
  homed may accept incoming connections — and on `127.0.0.1:8090` once it has one (`setup_listen` in homed.yaml or
  `--setup-listen` overrides both);
- the PIN changes every 10 minutes and after every link (a terminal shows the new one);
- the page closes 30 minutes after it opened, after 20 wrong PINs, and two minutes after the owner registered.

`homed admin firstrun` (in a terminal on the home box, with homed running) prints the page's addresses and the current
PIN, and opens the page again when it has closed:

```sh
"$G/bin/homed" admin firstrun       # The first-run page is open until 19:45 on: … PIN: 4821 0937
```

Without the page, from a terminal on the home box:

```sh
"$G/bin/homed" admin setup-link     # prints https://<domain>/admin/setup#t=… (valid 15 minutes, once)
"$G/bin/homed" admin reset-access   # a lost or stolen device: removes every passkey and session, prints a new link
```

The link is a secret until it is used. Both commands work with or without a running homed. Person keys (ChatGPT's
access) and the relay token survive a reset; rotate them too after a compromise (docs/SECURITY.md).

## 7. The admin

`https://<domain>/admin` (Roman: https://homie.jobbyjobs.work/admin), with the passkey: the home and the relay state,
Устройства (plugins, their devices and actions, logs), Журнал (every tool call and admin action), Настройки (home
name, language, server, passkeys, sessions, access keys, updates). Dangerous actions (install, remove, keys, relay,
hub update) ask for Face ID or Touch ID again when the last one is older than 5 minutes.

## 8. Set up the TV by hand (tool calls through the socket)

`homed tool call <name> ['<json>' | -]` calls any tool through the running homed as the owner, via `cli`, and writes
the audit entry. It prints the plugin's progress to stderr while it runs, then the tool's text and its structured
result. A tool that ran and failed prints its message to stderr and exits with status 1. With `-` the arguments come
from stdin: use it for anything secret (a pairing PIN), which must stay out of the shell history and the process list.

```sh
"$G/bin/homed" tool call tv_discover
"$G/bin/homed" tool call tv_add '{"host":"192.168.68.66","name":"Living room TV"}'
"$G/bin/homed" tool call tv_pair '{"tv":"<id from tv_add>"}'    # … Press OK on the TV remote
"$G/bin/homed" tool call tv_list
printf '%s' '{"tv":"<id>","pin":"<from the TV>"}' | "$G/bin/homed" tool call tv_pair -   # a PIN, via stdin
```

The same actions are in the admin: Устройства → Телевизоры.

## 9. Issue Roman's key straight into the Keychain

```sh
"$G/bin/homed" key new --label "ChatGPT Site" --keychain homie-key
```

This saves a new key for the owner (`--person ID` for someone else) as Keychain item `homie-key`, account = the
person id (an existing item is replaced); homed stores only its sha256. The key reaches `/usr/bin/security` on stdin,
so it is in no process list and never on the terminal. Without `--keychain` the key is printed once to stdout (and
only the key, so it can be piped). If the Keychain refuses it, the key is revoked again.

A running homed accepts new keys and refuses revoked ones at once. Keys made in the admin (Настройки → Ключи доступа)
are the same keys.

```sh
"$G/bin/homed" key list                   # KEY ID  PERSON  LABEL  STARTS WITH  CREATED  LAST USED
"$G/bin/homed" key revoke <key id>        # or a unique start of it, at least 6 characters
```

Check the key end to end without printing it (curl reads the header from stdin):

```sh
security find-generic-password -s homie-key -w | sed 's/^/Authorization: Bearer /' \
  | curl -sS -H @- https://homie.jobbyjobs.work/api/v1/tools | head -c 300; echo
```

The Site release (`site/release/release.mjs`) reads the same Keychain item.

## 10. Run at login with launchd

A LaunchAgent runs homed as Roman whenever he is logged in and restarts it if it stops: after a crash, and after a
hub update from the admin, where homed replaces its binary, shuts down cleanly and exits with status 75 on purpose
(`KeepAlive` starts the new binary). Save as `~/Library/LaunchAgents/com.github.devxpro.homie.homed.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.github.devxpro.homie.homed</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Users/devxpro/projects/family/homie/bin/homed</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>/Users/devxpro/Library/Logs/homie/homed.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/devxpro/Library/Logs/homie/homed.log</string>
</dict>
</plist>
```

```sh
L=com.github.devxpro.homie.homed
mkdir -p ~/Library/Logs/homie
plutil -lint ~/Library/LaunchAgents/$L.plist
# Stop a homed running in a terminal first: only one may run per data dir.
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/$L.plist   # start now and at every login
launchctl print gui/$(id -u)/$L | grep -E 'state|pid'               # running, pid …
launchctl kickstart -k gui/$(id -u)/$L                               # restart (after a rebuild)
launchctl kill HUP gui/$(id -u)/$L                                   # re-read keys, plugins and the home name from homed.db
launchctl bootout gui/$(id -u)/$L                                    # stop and unload
tail -f ~/Library/Logs/homie/homed.log
```

launchd gives the agent `HOME`, so the default data dir is the same as in a terminal. The log file is not rotated; it
stays small at the info level. Under launchd homed has no terminal, so it never prints the first-run PIN (the log says
so instead): run `homed admin firstrun` in a terminal for the PIN, or `homed admin setup-link` for a link straight
away. On Linux the systemd unit needs `Restart=always` (or `on-failure`, with 75 kept out of `SuccessExitStatus=` and
`RestartPreventExitStatus=`).

**Going back to an older version.** The admin never installs a release older than the one installed (plugins) or
running (the hub). On the home box the socket can, with an explicit flag:

```sh
curl -sS --unix-socket "$D/homed.sock" -H 'Content-Type: application/json' \
  -d '{"version":"0.1.0","allowDowngrade":true}' http://homed/api/admin/plugins/tv/update
```

**Local Network permission.** macOS asks once whether homed may find devices on the local network, the first time the
TV plugin (a child of homed) talks to the LAN: discovery, wake, or any TV command. Click **Allow**. A homed started from
a terminal runs under the terminal's permission, so the prompt comes again the first time homed runs from launchd; a
rebuilt binary may also be asked again. If the prompt was missed or denied, LAN calls fail at once (tools report the TV
as offline): turn homed on in System Settings → Privacy & Security → Local Network, then `launchctl kickstart -k`.

## 11. When something is wrong

| Symptom | Look at |
|---|---|
| a tool says the plugin is not running | `homed plugin list` (state and last error), `$D/logs/tv.log`, the admin's module log |
| `relay status` offline, `last error: … token …` | the code was rotated: run §4 again |
| `last error: … x509 …` | the relay's certificate (Caddy and Let's Encrypt on the server) |
| `self-check: FAILED … another Homie hub` | a second homed is connected to the same relay |
| the Site says the home is offline | homed is not running, the Mac sleeps, or the relay is down |
| `421 misdirected request` | the request's Host is neither the relay domain nor `127.0.0.1:<port>`/`localhost:<port>` |
| the admin says the passkey does not open the home | the relay domain changed: `homed admin reset-access` (§6) |
| `homed is not running` from a CLI command | start it (§6 or §10); `tool …` and `relay status` need it, the rest work without |
| `/admin` answers 503 | homed was built without the SPA: `make -C "$G" build` |
| the first-run page does not answer, or says it is closed | it closes after 30 minutes or 20 wrong PINs: `homed admin firstrun` |
| `homed relay connect` says the code never goes on the command line | pipe it in: `… \| homed relay connect -` (§4) |

## Development without a relay or a TV

`--dev-origin http://localhost:<port>` runs homed without a relay, with passkeys for `localhost` (browsers allow
WebAuthn and the `Secure` session cookie on `http://localhost`). It refuses a data dir that has a relay, so use a
separate one. The test plugins work as plugins:

```sh
W=/tmp/homed-dev
go -C "$G" build -o $W/homed ./cmd/homed                              # after make -C "$G" web, for the admin
go -C "$G" build -o $W/fake ./internal/plugins/testdata/fakeplugin   # plugin id "fake"
go -C "$G" build -o $W/echo ./pluginkit/example                      # plugin id "echo"
$W/homed plugin add --data-dir $W/data --id echo -- $W/echo serve
$W/homed run --data-dir $W/data --listen 127.0.0.1:8080 --setup-listen 127.0.0.1:8090 \
  --dev-origin http://localhost:8080 &
$W/homed admin setup-link --data-dir $W/data      # open it, create the passkey, then http://localhost:8080/admin
KEY=$($W/homed key new --data-dir $W/data)
printf 'Authorization: Bearer %s\n' "$KEY" | curl -sS -H @- http://127.0.0.1:8080/api/v1/tools
$W/homed tool list --data-dir $W/data
```

For the SPA with hot reload, start homed with `--dev-origin http://localhost:5173` and run `npm --prefix "$G/web" run
dev` (Vite proxies `/api` to `127.0.0.1:8080`). Keep `--data-dir` paths short: the socket path must fit in 103 bytes.
Never point a development homed at the real data dir.

`--relay-server-addr host:port` and `--relay-ca-file ca.pem` point `homed run` at a local relay test rig (see
`docs/research/frp-embedding.md`, "Local acceptance run"); never use them against the real relay.
