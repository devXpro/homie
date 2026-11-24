# homie-plug — a Homie plugin template

A small, complete [Homie](https://github.com/devXpro/homie) plugin you can copy and turn into your own. It
switches [Shelly](https://shelly.cloud) Gen2+ smart plugs over their local HTTP API (no cloud), and shows every part
of the plugin protocol on one page:

| Tool | Audience | Shows |
|---|---|---|
| `plug_list` | assistant + admin | the **inventory** tool: the admin's device list, with `on`/`off`/`offline` states |
| `plug_switch` | assistant | a command ChatGPT calls ("turn on the desk lamp"), with a device argument (`x-homie-ref`) |
| `plug_add` | admin only | an admin action with a form, a timeout, **progress** reports and Russian texts |

Plus: the data dir (`plugs.json`), human results with `isError` hints, tests with an in-memory MCP client and a fake
device, lint, GoReleaser and GitHub Actions that publish exactly the release Homie installs from.

The step-by-step tutorial that builds this plugin is in
[docs/PLUGINS.md](https://github.com/devXpro/homie/blob/main/docs/PLUGINS.md).

## Make it yours

```sh
cp -R homie/templates/plugin ~/src/homie-lamp     # from a Homie checkout
cd ~/src/homie-lamp
go mod edit -module github.com/alice/homie-lamp      # your repository
go mod edit -dropreplace github.com/devXpro/homie
go get github.com/devXpro/homie@latest
go mod tidy
go test ./...
```

`go.mod` carries `replace github.com/devXpro/homie => ../..` only so the template builds and tests inside the
homie repository. **Remove it in your own repository** (the second command above), or nothing will build there.

Then rename, in this order:

1. **The plugin id** (`plug`): `ID` in `main.go`, the `plug_` prefix of every tool name, `id` in
   `homie-plugin.json`. Ids match `^[a-z][a-z0-9]{1,15}$`; tool names `^<id>_[a-z][a-z0-9_]*$`.
2. **The binary** (`homie-plug`): `Name` in `main.go`, `project_name` and `binary` in `.goreleaser.yaml`, `binary` in
   `homie-plugin.json`.
3. **The texts**: `Title`, `Instructions`, every tool's `Description` (English, for the model) and its `ru` texts (for
   the admin).
4. **The device**: replace `Shelly` in `plug.go` with your own client behind the `Device` interface.
5. `homepage` in `homie-plugin.json`, and the copyright holder in `LICENSE`.

## Develop

```sh
go build -o bin/homie-plug .
go test -race ./...
golangci-lint run ./...                  # v2.12.x, see .golangci.yml
bin/homie-plug serve                        # MCP on stdin/stdout; state goes to ./data outside the hub
```

Try it under a real hub (a scratch data dir, never your real one):

```sh
W=/tmp/plug-dev
homed plugin add --data-dir $W/data --id plug -- "$PWD/bin/homie-plug" serve
homed run --data-dir $W/data --listen 127.0.0.1:8080 --setup-listen 127.0.0.1:8090 --dev-origin http://localhost:8080 &
homed tool list --data-dir $W/data
homed tool call --data-dir $W/data plug_add '{"host":"192.168.1.30","name":"Desk lamp"}'
homed tool call --data-dir $W/data plug_switch '{"on":true}'
```

## Release

```sh
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` runs GoReleaser, which publishes:

- `homie-plug_0.1.0_darwin_arm64.tar.gz`, `…_darwin_amd64`, `…_linux_amd64`, `…_linux_arm64`, `…_linux_armv7`;
- `checksums.txt`;
- `homie-plugin.json`, uploaded as is.

Then, in the Homie admin: **Devices → Add devices → Another module from GitHub**, type `alice/homie-lamp`, **Find**,
**Install**.

## License

MIT, see [LICENSE](LICENSE). Change the copyright holder to yours.
