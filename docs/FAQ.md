# FAQ

### What is Homie, in one sentence?

A small self-hosted hub that lets ChatGPT and Claude — in chat and in voice — control the devices in your home, through
a Cloudflare Tunnel that your home box opens.

### How is this different from Home Assistant?

Home Assistant is a home automation platform: dashboards, an entity database, automations, thousands of integrations.
Homie is deliberately the opposite: no dashboards, no state history, no automation engine. The interface is the
assistant; the hub only connects ChatGPT and Claude to devices, safely. It is one Go binary that runs comfortably on a
Raspberry Pi, and a device family is a small plugin. If you already run Home Assistant and love it, keep it; if you
want "talk to my TV" without running a platform, this is for you.

### Do I need a server? Why can't ChatGPT reach my home directly?

No server. ChatGPT and Claude run in the cloud and need a public HTTPS address; your home usually has none (no static
IP, carrier NAT, a router you would rather not open). The home box therefore connects **out** to Cloudflare with
`cloudflared`, which it runs itself, and Cloudflare gives your domain an address that leads back through that tunnel.
Nothing at home is exposed and no port is forwarded.

### What do I need for the Cloudflare part?

For the permanent setup, **a domain whose DNS is on Cloudflare** (a free Cloudflare account is enough; add the domain and
switch its nameservers). `homed cloudflare connect home.example.com` (or the installer's `--domain`) creates the tunnel
and the DNS record for you, with one login in your browser. To just try Homie you need nothing: a **quick tunnel**
gives a temporary `trycloudflare.com` address, but it is for testing — see below.

### What is a quick tunnel, and why can't I invite people on it?

Cloudflare's "try it" tunnel: no account, no domain, a random address. It changes whenever `cloudflared` starts again,
so at every restart of the hub, and a passkey belongs to one address. So on a quick tunnel only you can use the home, you
create a new passkey (from the home box) and add the connectors again after every restart, and invites are off. Cloudflare
also limits it (about 200 requests in flight, no Server-Sent Events, no uptime promise). It is for trying; move to your
domain with `homed cloudflare connect <host>`.

### Can I use ngrok or Tailscale Funnel instead of Cloudflare?

Not in v0.2. The Cloudflare path is the one Homie's installer, hub and admin support end to end.

### What does Cloudflare see?

Cloudflare terminates TLS for your domain, so it can read the requests passing through: OAuth tokens, tool arguments and
results, admin pages. It cannot forge your passkey, so it cannot start a protected action on its own, but whoever controls
your Cloudflare account could hijack a live session. Protect that account with two-factor authentication. Details and
what to do after a compromise: [SECURITY.md](SECURITY.md).

### What happens when the home box is off?

The assistant gets an error from Cloudflare and tells you it cannot reach the home. Everything resumes when the box is
back; `cloudflared` reconnects by itself.

### Which devices are supported?

Today: **LG webOS TVs** through [homie-tv](https://github.com/devXpro/homie-tv) — power on and off, volume, inputs
(tested on webOS 3.x; newer firmware that closes `ws://:3000` is not supported yet). Every other device family is a
plugin: [write one](PLUGINS.md) in about 15 minutes, or install someone else's by its GitHub address.

### Which assistants and plans work?

ChatGPT and Claude, through a **custom connector** (an MCP server added by URL with OAuth). Custom connectors need a
plan that offers them (and, on ChatGPT, today its developer mode); check your assistant's current rules. You add the same
one address in each assistant, and each person uses their own account.

### Does voice really work?

Yes, in the ChatGPT app's voice mode. One rule: choose **Always allow** for your home's connector once, in text, before
your first voice session — voice has no screen on which to approve an action, so without it nothing happens.

### Why does the assistant not see the device I just added?

The assistant keeps its own snapshot of a connector's tools. Start a new chat; if that is not enough, remove the
connector and add it again. See [USING.md](USING.md#when-tools-change).

### Can my family use it? Can a guest use it for a weekend?

Yes. **People → Invite**: a one-time link with a QR code. The person opens it on their phone, makes **their own
passkey**, and connects their own ChatGPT or Claude to the same address. You choose **which devices** (all, or ticked
ones) and **how long** (forever, a day, three days, a week, a month, or until a date). When a guest's term ends, their
access stops everywhere at once, and **Extend** brings it back. Your partner can be a **co-owner**. See
[USING.md](USING.md#people).

### Why passkeys and no password?

There is nothing to leak, guess or reset; Face ID or Touch ID is the whole login; and a passkey only works on your host
name, so phishing pages get nothing. The first passkey can only be created from the home box itself.

### I lost my phone. How do I get back in?

On the home box: `homed admin reset-access`. It removes every passkey and session and prints a new one-time setup link.
Remove the lost phone's assistant connections under **People**.

### Where is my data?

At home, in one directory: `~/Library/Application Support/homie` on macOS, `/var/lib/homie` for the Linux service, or
the Docker volume. Cloudflare holds your DNS record and the tunnel's identity, not your data, and there is no Homie
cloud account.

### Does it phone home?

No telemetry. The hub talks to Cloudflare (the tunnel), to GitHub when you check for updates or install a plugin, and to
your devices. Plugins may talk to whatever their devices need (homie-tv stays on the LAN).

### I used v0.1 with a relay and ChatGPT Sites. What now?

Update as usual: the first v0.2 start migrates your data dir and keeps your plugins, home name and language. The relay,
the Sites and the access keys are gone; passkeys do not move (they belonged to the old domain). See
[Upgrading from v0.1](INSTALL-HOME.md#upgrading-from-v01).

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

The assistants use one MCP endpoint (`/mcp`) with OAuth. The admin's API (`/api/home/*`, plus the gatekeeper's
`/api/account/*`) is documented in [ADMIN-API.md](ADMIN-API.md); the sign-in and OAuth parts are
[MCP Gatekeeper](https://github.com/devXpro/mcp-gatekeeper).

### What does it cost?

The software is free and MIT-licensed. A domain costs what a domain costs, Cloudflare's tunnel is free, and you probably
already have the assistant plan.
