# FAQ

### What is Homie, in one sentence?

A small self-hosted hub that lets ChatGPT — in chat and in voice — control the devices in your home, through a relay
server you own.

### How is this different from Home Assistant?

Home Assistant is a home automation platform: dashboards, an entity database, automations, thousands of integrations.
Homie is deliberately the opposite: no dashboards, no state history, no automation engine. The interface is the
assistant; the hub only connects ChatGPT to devices, safely. It is one Go binary that runs comfortably on a Raspberry
Pi, and a device family is a small plugin. If you already run Home Assistant and love it, keep it; if you want "talk
to my TV" without running a platform, this is for you.

### Do I need a server? Why can't ChatGPT reach my home directly?

ChatGPT runs in the cloud and needs a public HTTPS address. Your home usually has none: no static IP, carrier NAT, a
router you would rather not open. The relay gives you a stable domain with a real certificate, and your home box
connects **out** to it, so nothing at home is exposed. The server side is two containers (Caddy and frps); any $4 VPS
does it.

### Can I use Cloudflare Tunnel, ngrok or Tailscale Funnel instead?

Not out of the box. The design keeps every party under your control: a CDN or tunnel service that terminates TLS
would see your access keys and admin traffic. The relay is small enough to own.

### What does the server see?

It terminates TLS, so it can read requests passing through: access keys in headers, tool arguments and results, admin
pages. It cannot forge your passkey, so it cannot start a protected action on its own, but a compromised server could
hijack a live admin session. Run it on a server you control. Details: [SECURITY.md](SECURITY.md).

### What happens when the home box is off?

ChatGPT gets a clear "the home hub is offline right now" and tells you so. Everything resumes when the box is back;
the hub reconnects by itself.

### Which devices are supported?

Today: **LG webOS TVs** through [homie-tv](https://github.com/devXpro/homie-tv) — power on and off, volume, inputs
(tested on webOS 3.x; newer firmware that closes `ws://:3000` is not supported yet). Every other device family is a
plugin: [write one](PLUGINS.md) in about 15 minutes, or install someone else's by its GitHub address.

### Which ChatGPT plans work?

You need a plan with ChatGPT Sites to publish the private Site that becomes your home's plugin. Each person uses their
own account; a personal plan cannot share one Site's plugin with another account.

### Does voice really work?

Yes, in the ChatGPT app's voice mode. One rule: choose **Always allow** for your home plugin once, in text, before
your first voice session — voice has no screen on which to approve an action, so without it nothing happens.

### Why does ChatGPT not see the device I just added?

ChatGPT keeps its own snapshot of a plugin's tools. Republish the Site settings (`--settings-only`) and give it about
ten minutes; a new chat picks up the new tools. See [USING.md](USING.md#when-tools-change).

### Can my family use it?

Yes: each person gets their own Site in their own ChatGPT account with their own access key. Today you publish it for
them from your Mac (`--profile`); phase 3 adds invites — a link they open on their phone, with their own passkey and
their own name in the activity log. See [USING.md](USING.md#a-family-member).

### Why passkeys and no password?

There is nothing to leak, guess or reset; Face ID or Touch ID is the whole login; and a passkey only works on your
domain, so phishing pages get nothing. The first passkey can only be created from the home box itself.

### I lost my phone. How do I get back in?

On the home box: `homed admin reset-access`. It removes every passkey and session and prints a new one-time setup
link. ChatGPT keeps working: access keys are not touched.

### Where is my data?

At home, in one directory: `~/Library/Application Support/homie` on macOS, `/var/lib/homie` for the Linux
service, or the Docker volume. Nothing is stored on the relay server except its token, and there is no Homie cloud
account.

### Does it phone home?

No telemetry. The hub talks to your relay, to GitHub when you check for updates or install a plugin, and to your
devices. Plugins may talk to whatever their devices need (homie-tv stays on the LAN).

### Are plugins sandboxed?

No. A plugin runs as the hub's OS user and can read what that user can. Install plugins only from authors you trust as
much as the hub itself. The hub verifies checksums, refuses unsafe archives and rolls back failed updates, but that
protects against corruption, not against a malicious author.

### Can I run the hub on a Mac laptop?

Yes, while it is awake and online. For a home that answers at 3 a.m., use something that never sleeps: a Mac mini, a
Raspberry Pi or a NAS.

### Can I run the hub in Docker on my Mac?

No. Docker Desktop's "host network" is a Linux VM, not your LAN, so plugins cannot find or wake devices. Use the
installer or Homebrew on a Mac; Docker is for Linux hosts.

### Can I write a plugin in Python or JavaScript?

Yes. A plugin is any executable that speaks MCP over stdio; use an official MCP SDK and follow the
[protocol reference](PLUGINS.md#protocol-reference). Go authors get `pluginkit` and a [template](../templates/plugin).

### Is there an API?

The hub exposes the REST façade (`/api/v1/tools`) and an MCP endpoint (`/mcp`), both with a person's bearer access
key — that is what the ChatGPT Site calls. The admin's API is documented in [ADMIN-API.md](ADMIN-API.md).

### What does it cost?

The software is free and MIT-licensed. You pay for a small VPS and a domain, and for a ChatGPT plan you probably
already have.
