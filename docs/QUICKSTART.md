# Quick start

From nothing to "turn on the TV" by voice. You need a small Linux VPS with Docker, a domain you control, a box at home
that stays on (a Mac, a Raspberry Pi, a Linux box or a NAS), and ChatGPT.

The long versions: [INSTALL-SERVER.md](INSTALL-SERVER.md), [INSTALL-HOME.md](INSTALL-HOME.md),
[USING.md](USING.md).

## Server — 5 minutes

**1. DNS.** Create an A record `home.example.com → <your VPS's IPv4>`. DNS only: no CDN proxying (on Cloudflare, the
grey cloud).

**2. Install the relay** on the VPS:

```sh
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/server.sh \
  | sudo bash -s -- --domain home.example.com
```

It checks DNS, picks *standalone* (its own Caddy on 80/443) or *behind-proxy* (next to your existing Caddy, nginx or
Traefik), gets a certificate, and prints a **connect code**:

```
Connect code. It contains the relay token, so keep it secret:

ghr1.eyJ2IjoxLCJkb21haW4iOiJob21lLmV4YW1wbGUuY29tIiwidG9rZW4iOiI…
```

Keep it for the next step; treat it like a password.

**3. Check** from anywhere: `curl -sS https://home.example.com/healthz` shows the *"Home is offline"* page. That is
right — the home is not connected yet.

## Home — 5 minutes

**1. Install the hub.**

```sh
# macOS
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/home.sh | bash

# Raspberry Pi / Linux
curl -fsSL https://raw.githubusercontent.com/devXpro/homie/main/install/home.sh | sudo bash

# NAS / Docker on Linux (host networking is required)
docker run -d --name homie --restart unless-stopped --network host -v homie:/data ghcr.io/devxpro/homie:latest
```

The installer starts the hub as a service and prints a first-run address and an 8-digit PIN:

```
Set it up from a browser (the PIN is shown only here):
The first-run page is open until 19:12 on:
  http://192.168.1.20:8090/firstrun
PIN: 1234 5678 (it changes at 18:52 and after every setup link; run this again for the current one)
```

**2. Open the first-run page** on your phone or laptop (same home network), type the PIN, and paste the connect code.
Wait for *connected*.

<p>
  <img src="images/08-lan-pin-phone-dark.webp" width="240" alt="The first-run page: enter the PIN">
  <img src="images/05-setup-passkey-phone-dark.webp" width="240" alt="Create your passkey">
  <img src="images/20-home-phone-dark.webp" width="240" alt="Your home, online">
</p>

**3. Create your passkey.** Scan the QR code (or *Open on this computer*), name your home — this is the name ChatGPT
will show, e.g. «Мой шалаш» — enter your name, and confirm with Face ID or Touch ID. You are in the admin at
`https://home.example.com/admin`, and the status says **Online**.

Over SSH instead of a browser on the LAN:

```sh
homed relay connect -          # paste the connect code, press Enter
homed admin setup-link         # open the printed link on your phone
```

(macOS installer: the command is `"$HOME/Library/Application Support/homie/bin/homed"`. Docker:
`docker exec -i homie homed …`.)

**4. Add the TV.** Prepare an LG webOS TV once: *LG Connect Apps* on, *Mobile TV On → Turn on via Wi-Fi* on, a fixed IP
in your router. Then in the admin: **Devices → Add devices**, the TV module under *Recommended*, **Install**; then
**Find TVs**, **Add a TV**, **Pair a TV** and press **OK** on the remote.

## ChatGPT — 5 minutes

On a Mac with Node 22+, `git` and the [Codex CLI](https://github.com/openai/codex):

1. In the admin: **Settings → ChatGPT access → Access keys → Create a key**, copy it, and store it in the Keychain:

   ```sh
   security add-generic-password -U -s homie-key -a "$USER" -w
   ```

2. Publish your private ChatGPT Site:

   ```sh
   git clone https://github.com/devXpro/homie.git
   node homie/site/release/release.mjs --login
   HOME_API_URL=https://home.example.com \
     node homie/site/release/release.mjs --title "Мой шалаш" --slug moy-shalash
   ```

3. In ChatGPT: **Plugins → Created by you → connect** your home. Ask in text: *"Turn on the TV"* and choose **Always
   allow** — voice cannot approve actions, so this step is required once.

4. Start a new chat, switch to voice, and say: **«Включи телик»**.

Next: everything the admin can do, voice tips and adding your family — [USING.md](USING.md). Something off? —
[TROUBLESHOOTING.md](TROUBLESHOOTING.md).
