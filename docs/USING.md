# Using Homie

A tour of the admin, then the part that matters most: connecting ChatGPT so the family can talk to the home.

The admin lives at **`https://<your domain>/admin`** and works the same on a phone (bottom tab bar) and a computer
(sidebar). It speaks English and Russian, follows the system's dark or light mode, and has no password: you sign in
with a passkey — Face ID, Touch ID or your device's screen lock. The screenshots below are from a real home called
«Мой шалаш», in Russian.

- [Sign in](#sign-in)
- [Home](#home)
- [Devices](#devices) — add a plugin, set up a TV, run actions
- [Activity](#activity)
- [Settings](#settings) — passkeys, access keys, the server, updates
- [Connect ChatGPT](#connect-chatgpt) — a ChatGPT Site, "Always allow", voice
- [A family member](#a-family-member)
- [When tools change](#when-tools-change)

## Sign in

<img src="images/01-login-phone-dark.webp" width="280" alt="The sign-in screen: a single Sign in button with Face ID">

Open `https://<your domain>/admin` and tap **Sign in**. Your device asks for Face ID or Touch ID; that is all. A
session lasts up to 7 days, or 12 hours without use.

The very first passkey can only be created from the home box itself — the first-run page on your home network or
`homed admin setup-link` in a terminal there (see [INSTALL-HOME.md](INSTALL-HOME.md#first-run-create-your-passkey)).
Passkeys live in iCloud Keychain (or your password manager), so the one you made on the Mac also works on your iPhone.

## Home

<p>
  <img src="images/20-home-phone-dark.webp" width="260" alt="Home tab on a phone, dark">
  <img src="images/20-home-phone-light.webp" width="260" alt="Home tab on a phone, light">
</p>

The first screen answers one question: *can ChatGPT run my home right now?*

- **The status card.** Green *Online — ChatGPT can run your home* when the relay is up and the outside check
  passes. Otherwise it says what is wrong: *Reconnecting to the server*, *Check your home internet and the server*,
  *Connected, but not reachable from outside*, or *Connect a server in Settings*. Tap it for the details.
- **Needs attention** appears only when something does: the server is unreachable, a plugin is not responding, or an
  update is available for a plugin or for Homie itself.
- **Devices** lists your plugins (modules) with their state and device count.
- **Recent** shows the last actions: what, who, through which path, and how long it took. A red mark is a failure.

## Devices

<p>
  <img src="images/30-devices-phone-dark.webp" width="260" alt="Devices tab">
  <img src="images/44-device-sheet-phone-dark.webp" width="260" alt="A TV's sheet with its actions">
  <img src="images/48-run-progress-phone-dark.webp" width="260" alt="Pairing a TV: press OK on the remote">
</p>

Each kind of device is a **module** (a plugin). Open one to see:

| Section | What it is |
|---|---|
| **Devices** | the module's devices and their state (on, off, offline, paired…). The state refreshes when you open the page. Tap a device for the actions that apply to it. |
| **Actions** | setup actions only you see: find, add, pair, rename, remove. Each opens a short form; long ones show progress. |
| **What it does in ChatGPT** | the commands ChatGPT can use, in plain words. **Try without ChatGPT** runs one right here. |
| **Maintenance** | version and update, the module's log, restart, turn off (ChatGPT stops seeing it), remove. |

<img src="images/40-module-desktop-dark.webp" alt="The TV module on a desktop: devices, actions and what it does in ChatGPT">

### Add a plugin

**Devices → Add devices.** *Recommended* lists known modules (the TV plugin first); *Another module from GitHub* takes
any `owner/repository` that publishes Homie releases. Tap **Find**, check the name, version and source, then
**Install**. Installing, updating and removing a module ask you to confirm with Face ID or Touch ID if your last
confirmation is older than five minutes. If a new version fails to start, the previous one comes back by itself.

### Set up a TV (LG webOS)

Prepare the TV once — LG Connect Apps on, Mobile TV On → Turn on via Wi-Fi, Quick Start+ on, and a fixed IP in your
router ([details](https://github.com/devXpro/homie-tv/blob/main/docs/families/lg-webos.md#tv-setup)). Then, with the TV
switched on:

1. **Devices → Add devices**, pick the TV module under *Recommended*, **Install**.
2. Open **TVs** → **Find TVs**. The search takes up to 10 seconds and lists the TVs that answered.
3. **Add a TV**: its IP address and a name people will say, e.g. *Living room TV*. The model and MAC address are read
   from the TV.
4. Tap the TV → **Pair a TV**. The TV shows a prompt: **press OK on the remote** within two minutes. The admin shows
   the countdown.
5. **What it does in ChatGPT → Turn TV on → Try without ChatGPT.** If the TV turns on, ChatGPT can do it too.

### Actions with progress

An action that takes a while (pairing, a network search) shows what the device is doing right now — "Press OK on
the TV remote" — and a timer. You can close the sheet; the action keeps running, a pill shows it, and a notice tells
you when it is done. **Cancel** stops it.

## Activity

<img src="images/60-activity-phone-dark.webp" width="280" alt="Activity: a log of every action">

Every tool call through every path — ChatGPT, the admin, the terminal — plus sign-ins and security events. The
segments switch between **ChatGPT**, **All** and **Failures**; **Filters** narrow it by person, module, path and
kind. Tap an entry for the arguments (secrets are redacted), the result and how long it took. Entries are kept for
180 days.

## Settings

<img src="images/70-settings-phone-dark.webp" width="280" alt="Settings">

| Section | What you can do |
|---|---|
| **Your name** | how you appear in the activity log |
| **Home** | the home's name (the name your family sees in ChatGPT), the admin's language, dark / light / match system |
| **Security → Passkeys** | add a passkey on another device, rename or remove one. The last one cannot be removed. |
| **Security → Where you're signed in** | every session; sign out on this device or everywhere else |
| **Connection → Server** | the relay address, connected since, the outside check (*Check now*), and *Update the server code* after you rotated the token on the server |
| **ChatGPT access → Access keys** | create a key for a ChatGPT Site (shown once — save it right away), see when each was last used, revoke one |
| **About** | the version, *Check for updates* and *What's new*. Updating restarts the hub; the page reloads by itself. |

Lost a phone? On the home box: `homed admin reset-access` removes every passkey and session and prints a new setup
link. Access keys (ChatGPT's access) survive a reset.

## Connect ChatGPT

Each person talks to the home through **their own ChatGPT account**, with a private ChatGPT **Site** that becomes a
plugin there, named after your home. The Site is a thin proxy: the tools and their descriptions come from the hub,
the Site forwards the calls with that person's access key.

You need:

- a **Mac** with Node 22+, `git`, `npm` and the [Codex CLI](https://github.com/openai/codex) (the release script uses
  Codex to publish the Site, and the macOS Keychain for the key);
- a ChatGPT plan with Sites;
- your home connected (the status card is green).

### 1. Create an access key

**Settings → ChatGPT access → Access keys → Create a key**, label it *ChatGPT Site*, copy it. Store it in the
Keychain of the Mac you publish from (the command asks for the key, so it never lands in your shell history):

```sh
security add-generic-password -U -s homie-key -a "$USER" -w
```

If that Mac is the home box, one command does both and never shows the key:

```sh
homed key new --label "ChatGPT Site" --keychain homie-key
```

### 2. Publish your Site

```sh
git clone https://github.com/devXpro/homie.git
node homie/site/release/release.mjs --login                     # sign Codex in to YOUR ChatGPT account (device code)
HOME_API_URL=https://home.example.com \
  node homie/site/release/release.mjs --title "Мой шалаш" --slug moy-shalash
```

- `--title` is the name ChatGPT shows for the plugin — your home's name. `--slug` is the Site's unique address name.
- The first run creates the Site, builds it on your Mac, sets `HOME_API_URL` and the key (as a secret) and publishes.
  The address is remembered; later runs need no `HOME_API_URL`.
- `--dry-run` builds everything without touching your account; `--check` shows the live plugin's tools as ChatGPT
  sees them. All options: [site/README.md](../site/README.md).

### 3. Connect it in ChatGPT

1. In ChatGPT: **Plugins → Created by you**, find your home, **Connect**.
2. Ask for something in **text** first: "Turn on the TV". When ChatGPT asks for permission, choose **Always allow**.
3. Start a **new chat**: a chat keeps the tools it started with.

**Why "Always allow" matters.** Without it ChatGPT asks for approval on the screen before every action that changes
something. In voice mode there is no screen to approve on, so the action silently does not happen. Choose it once, in
text, before your first voice session.

### 4. Voice

Voice works with the plugin like text does: «Включи телик», «Громкость двадцать», «Переключи на HDMI 2», «Выключи
телик». Tips:

- Name devices the way you say them: *Living room TV*, *Desk lamp*. One TV? Then just "the TV".
- For short, voice-friendly answers, create a ChatGPT project and give it instructions like:

  ```
  I talk to you by voice, often on the move. Answer in one or two sentences unless I ask for details.
  For anything about the devices at home use the home plugin with as few calls as possible.
  Do it when I ask, then confirm in one short sentence what was done.
  If the plugin says the home is offline, say so plainly and do not retry more than once.
  ```
- "The home hub is offline right now" means what it says: the home box is asleep or without internet.

## A family member

**Today.** Every person needs their own Site in their own ChatGPT account (a personal plan cannot share a Site's
plugin). Create a separate access key for them (label it with their name), then publish their Site from your Mac with
a **profile**, which keeps their Codex login and key apart from yours:

```sh
security add-generic-password -U -s homie-key-tanya -a "$USER" -w          # paste THEIR key
node homie/site/release/release.mjs --profile tanya --login                   # they approve the code on THEIR phone
node homie/site/release/release.mjs --profile tanya --title "Наш дом" --slug nash-dom
```

They approve the device code with **their** ChatGPT account (the script refuses and signs out if it sees yours), then
connect the plugin, choose **Always allow** and start a new chat — steps 3 and 4 above. Their calls appear in Activity
under the key's person (today, the owner) with the key's label.

**Coming in phase 3: invites.** **People → Invite** makes a single-use link with a QR code. The family member opens it
on their phone, creates their own passkey, connects ChatGPT in a guided step and chooses *Always allow* — no terminal,
no Mac. Each person gets their own name in Activity and their own set of modules.

## When tools change

ChatGPT keeps its own snapshot of a plugin's tools. After you install a module, update one, or change the home's name,
republish each person's Site settings so ChatGPT re-reads them:

```sh
node homie/site/release/release.mjs --title "Мой шалаш" --slug moy-shalash --settings-only
node homie/site/release/release.mjs --profile tanya --title "Наш дом" --slug nash-dom --settings-only
```

The refresh is not instant (about ten minutes). `--check` shows the tools ChatGPT sees now; a new chat picks them up.
