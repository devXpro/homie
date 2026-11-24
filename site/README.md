# Homie: the ChatGPT Site

A private [ChatGPT Site](https://help.openai.com/en/articles/20001339-creating-and-using-chatgpt-sites) whose MCP server
becomes a plugin in one person's own ChatGPT account, built the way the God Mode Site is. It is a thin proxy: the tools,
their descriptions and the rules for the model live in the home hub (`homed`, running at home), and this Site only
speaks MCP to ChatGPT and forwards the calls. This is path B of the plan (`../docs/PLAN.md`, section 3).

```
ChatGPT (voice or text)
  -> the plugin = this Site's MCP server (a Cloudflare Worker hosted by OpenAI), POST /mcp
  -> https://<your domain>/api/v1/tools[/<name>], bearer key of the person   (through Caddy and the frp relay)
  -> homed at home -> the device plugin (the TV, ...)
```

`core/mcp.mjs` has no dependencies and runs under `node --test`; `app/mcp/route.ts` only wires it to the Worker.

Every person gets their own Site in their own ChatGPT account (a personal plan cannot share a Site's plugin), with the
same code, the same hub and their own key. The Site's title is the name the person gave their home, for example
«Мой шалаш».

## Settings (Site settings -> Environment variables)

| Variable | Meaning |
| --- | --- |
| `HOME_API_URL` | Required. The public address of the home hub, e.g. `https://homie.example.com`. |
| `HOME_API_KEY` | Required, secret. This person's own key (`ghk_...`), created in the hub. The release script reads it from the Keychain item `homie-key`, or `homie-key-<profile>`. |
| `HOME_LANGUAGE` | Optional. The language ChatGPT speaks with you, e.g. `ru`. Unset: the language you use. |

## Errors the Site explains to the model

| Hub answer | What ChatGPT is told |
| --- | --- |
| `200` | the result (a plain `{"text": ...}` result becomes the text itself; structured results are passed on as they are) |
| `422` | the tool's own error text: the device was reached but could not do it (not paired, switched off, ...) |
| `404` with a JSON body | the tool is unknown or not allowed: ChatGPT holds an outdated tool list, start a new chat |
| `404` that is **not** JSON | the relay's "home is offline" page: "The home hub is offline right now ... Try again later." |
| `401` | the key in `HOME_API_KEY` is wrong or revoked |
| `400`, `429`, `503`, `504`, network errors | a short readable message; for a tool that changes something, "unknown whether it took effect" where it applies |

## Release

```sh
node release/release.mjs --title "Мой шалаш" --slug moy-shalash   # create the Site if there is none, build, push, publish
node release/release.mjs --title ... --slug ... --settings-only   # save the settings, redeploy the live version: ChatGPT re-reads the tool list
node release/release.mjs --dry-run --title Test --slug test       # stage, build and package in a scratch directory; the account is not touched
node release/release.mjs --check                                   # the live plugin as ChatGPT sees it: its tools and one read-only call
node release/release.mjs --list-sites-tools                        # read-only: names and parameters of the Codex sites.* tools
node release/release.mjs --login                                   # sign Codex in to a ChatGPT account by device code
```

Options: `--title` (the Site's name in ChatGPT), `--slug` (its unique address name; the Site is found by it),
`--description`, `--profile NAME` (see below), and for `--check` `--plugin NAME` (the name ChatGPT shows for the plugin,
when it cannot be found by itself).

The first release needs to know where the hub is: `HOME_API_URL=https://homie.example.com node release/release.mjs
--title ... --slug ...`. The address is remembered (`~/.homie-site/api-url`, or `~/.homie-site-NAME/api-url` for a
profile), so later runs need no setting; give `HOME_API_URL` again to change it. `HOME_LANGUAGE` is passed on when set.

It talks to the ChatGPT Sites tools through `codex app-server` with the Codex login of this Mac (the same route the
God Mode plugin takes), pushes the source to the Site's own repository with a short-lived credential (commits are made as
"Homie Site"), builds on this Mac (`npm ci`, `npm run check`, `npm run build`), and makes one model call to upload
the archive. The key is read from the Keychain and goes to the Site as a secret; it is never printed. Needs Node 22+,
git, npm and the Codex CLI signed in.

After the first release, in ChatGPT: **Plugins -> Created by you**, connect the plugin, and the first time it acts
choose **Always allow**. Then start a new chat: a chat keeps the tools it started with.

**Why "Always allow" matters.** Without it ChatGPT asks for approval on the screen before every action that changes
something (turn the TV on, set the volume). In voice mode there is no screen to approve on, so the call simply does not
happen. Choose it once, in text, before the first voice session.

When the hub gets a new tool or a new description (a new plugin, an updated one), run `--settings-only`: ChatGPT keeps
its own snapshot of a plugin's tools and re-reads it after the settings are saved and the Site is redeployed. The server
version this Site reports carries a fingerprint of everything the model reads, so ChatGPT can tell that it moved. The
snapshot is not instant: with the Librus Site it took about 11 minutes to catch up once. `--check` lists the tools as
ChatGPT shows them now, so it shows whether the refresh has arrived.

## Another person's Site (profiles)

Each person publishes into their own ChatGPT account. Everything is released from one Mac with `--profile NAME` (say
`tanya`), which keeps that account apart from yours:

| What | Where |
| --- | --- |
| state and the checkout | `~/.homie-site-NAME` |
| the Codex login (your own is never touched) | `~/.homie-site-NAME/codex` |
| the key | Keychain item `homie-key-NAME`, and the same person's key in the hub |

```sh
node release/release.mjs --profile tanya --login                       # prints an address and a code; they approve it on their phone
node release/release.mjs --profile tanya --title "Наш дом" --slug nash-dom   # create their Site, build, push, publish
node release/release.mjs --profile tanya --check                       # after they connected the plugin
node release/release.mjs --profile tanya --logout                      # sign the profile out again (never your own login)
```

Use THEIR ChatGPT account when approving the code (on their phone). If the code is approved with your own account, the
profile would publish the other person's key over your Site, so it compares the two logins, refuses to do anything and
signs itself out.

The code works once and only for a few minutes; the command waits 15 minutes and then gives up, and can be started
again. Then, in their ChatGPT: **Plugins -> Created by you**, connect the plugin, choose **Always allow**, and start a
new chat. Run `--settings-only` for every profile when tools or descriptions change.

Environment overrides for tests: `HOMIE_SITE_HOME` (state directory), `HOMIE_KEY_ITEM` (Keychain item),
`HOMIE_SITE_PROFILE` (profile).

## Voice

ChatGPT voice in a project reads the project's instructions. Create a ChatGPT project and paste something like:

```
I talk to you by voice, often on the move. Answer in one or two sentences unless I ask for details.
For anything about the devices at home (the TV and whatever else is connected) use the home plugin
with as few calls as possible. Do it when I ask, then confirm in one short sentence what was done.
If the plugin says the home is offline, say so plainly and do not retry more than once.
```

This does not change the language ChatGPT talks to you in. The hub's own instructions (the home's name, "act only when
asked") reach the model through the plugin; the project text only shapes the voice style.

## Troubleshooting

- "refused this Site's key": `HOME_API_KEY` in the Site settings differs from this person's key in the hub (or it was
  revoked). Fix and `--settings-only`.
- "The home hub is offline right now": the home computer is asleep or off the internet, or `homed` is not running. From
  anywhere: `curl https://<domain>/healthz` answers JSON `{"ok":true}` when the home is connected and the offline page
  (HTML, 404) when it is not.
- "Codex is not signed in ... run with --login first": the profile has no login yet, or it was signed out.
- "could not be reached": the relay server or its Caddy is down; check `https://<domain>/healthz`.
- ChatGPT calls a tool that no longer exists: it holds an older tool list; open a new chat.
- Which deployment answered: the Site's Worker log in ChatGPT Sites shows each MCP request.

## Develop

```sh
npm ci
node --test "core/*.test.mjs"   # unit tests, no network
npm run check                   # tests and typecheck
npm run lint
```

`SITES-STARTER.md` is the platform's own description of the starter this was generated from.
