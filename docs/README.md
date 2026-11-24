# Homie documentation

## Start here

| Guide | For |
|---|---|
| [Quick start](QUICKSTART.md) | server, home and ChatGPT in three five-minute blocks |
| [Install the relay server](INSTALL-SERVER.md) | DNS, the one-line installer, standalone or behind Caddy / nginx / Traefik, the connect code, firewall, rotation, updates |
| [Install the home hub](INSTALL-HOME.md) | where it runs (Mac, Raspberry Pi, Linux, NAS), installer, Homebrew, Docker, from source, first run with a passkey, service, updates |
| [Using Homie](USING.md) | the admin tour, setting up a TV, connecting ChatGPT with a Site, voice, family members |
| [Troubleshooting](TROUBLESHOOTING.md) | the offline page, certificates, passkeys, Local Network permission, TVs that do not wake, logs |
| [FAQ](FAQ.md) | short answers: why a relay, what the server sees, which devices, which plans |

## Build on it

| Guide | For |
|---|---|
| [Writing a plugin](PLUGINS.md) | a 15-minute tutorial, tool design, testing, releasing, and the full hub ⇄ plugin protocol |
| [Plugin template](../templates/plugin) | a complete Go plugin to copy: tools, tests, lint, GoReleaser, CI |
| [Architecture](ARCHITECTURE.md) | components, request flows, trust boundaries, the data dir, and why frp, MCP and passkeys |
| [Security model](SECURITY.md) | every secret, what it unlocks, and what each part can see |
| [Relay internals](RELAY.md) | frps on one port, `stream_close_delay`, the home client's TLS settings |
| [Admin API](ADMIN-API.md) | the `/api/admin` contract the admin UI uses |
| [Running homed locally](DEV.md) | building, a development hub without a relay, the local socket, launchd |
| [Testing](TESTING.md) | every test suite and how to run it |
| [Admin design](DESIGN.md) | the admin's design system: tokens, components, motion, copy |
| [ChatGPT Site](../site/README.md) | the Site template, release script, profiles |

## Project

| Document | What |
|---|---|
| [Plan](PLAN.md) | the implementation plan and the locked decisions |
| [research/](research) | notes behind the decisions: [embedding frp](research/frp-embedding.md), [ChatGPT Sites tools](research/sites-tools.md) |
| [homie-tv](https://github.com/devXpro/homie-tv) | the first plugin: LG webOS TVs |
