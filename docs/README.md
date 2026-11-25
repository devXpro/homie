# Homie documentation

## Start here

| Guide | For |
|---|---|
| [Quick start](QUICKSTART.md) | home and ChatGPT or Claude in two short blocks |
| [Install the home hub](INSTALL-HOME.md) | where it runs (Mac, Raspberry Pi, Linux, NAS), installer, Homebrew, Docker, from source, connecting Cloudflare (your domain or a quick tunnel), first run with a passkey, upgrading from v0.1, service, updates |
| [Using Homie](USING.md) | the admin tour, setting up a TV, connecting ChatGPT and Claude, voice, inviting people with devices and a term |
| [Troubleshooting](TROUBLESHOOTING.md) | the tunnel's states, the assistant that cannot reach the home, quick-tunnel limits, 524, passkeys, Local Network permission, TVs that do not wake, logs |
| [FAQ](FAQ.md) | short answers: why a tunnel, what Cloudflare sees, what a quick tunnel is, which devices, which plans |

## Build on it

| Guide | For |
|---|---|
| [Writing a plugin](PLUGINS.md) | a 15-minute tutorial, tool design, testing, releasing, and the full hub ⇄ plugin protocol |
| [Plugin template](../templates/plugin) | a complete Go plugin to copy: tools, tests, lint, GoReleaser, CI |
| [Architecture](ARCHITECTURE.md) | components, request flows, trust boundaries, the data dir, and why Cloudflare Tunnel, the gatekeeper, MCP and passkeys |
| [Security model](SECURITY.md) | every secret, what it unlocks, Cloudflare in the middle, quick mode, guests and terms, co-owners, the admin socket on a Mac |
| [Admin API](ADMIN-API.md) | the `/api/home` contract the admin UI uses, the first-run page's API, and where the gatekeeper's endpoints fit |
| [Running homed locally](DEV.md) | building, a scratch hub with a fake cloudflared, a development hub without a tunnel, the local socket, launchd |
| [Testing](TESTING.md) | every test suite and how to run it |

## Related

| Project | What |
|---|---|
| [MCP Gatekeeper](https://github.com/devXpro/mcp-gatekeeper) | the public library behind Homie's sign-in, OAuth server for the assistants, people and invites, and the Cloudflare tunnel supervisor |
| [homie-tv](https://github.com/devXpro/homie-tv) | the first plugin: LG webOS TVs |
