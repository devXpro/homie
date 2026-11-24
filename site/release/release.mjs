#!/usr/bin/env node
// Publishes this Site to a person's own ChatGPT account, the way the God Mode plugin releases its Site (see the
// paseo-god-mode-plugin: server/release). It talks to the ChatGPT Sites tools through `codex app-server` with the Codex
// login of this Mac, so no model is involved except for the one upload call.
//
//   node release/release.mjs --title T --slug S   create the Site if there is none, then build, push and publish
//   node release/release.mjs --settings-only      save the settings and redeploy the live version (tool list refresh)
//   node release/release.mjs --dry-run            stage, build and package locally; touch nothing in the account
//   node release/release.mjs --check              as ChatGPT sees the live plugin: its tools, and a read-only call
//   node release/release.mjs --list-sites-tools   read-only: dump the names and schemas of the sites.* tools of Codex
//   node release/release.mjs --login              sign this Codex in to a ChatGPT account by device code (see profiles)
//   node release/release.mjs --logout             sign a PROFILE out again (never your own login)
//
// Options: --title (the Site's name in ChatGPT, e.g. "Мой шалаш"), --slug (its unique address name), --description,
// --profile NAME, --plugin PREFIX (--check: the name ChatGPT shows for the plugin; found by itself when a tool of the
// spike answers).
//
// Settings: HOME_API_URL, the home hub (given once, then remembered in ~/.homie-site/api-url), the key from the
// Keychain item `homie-key` (never printed), HOME_LANGUAGE (optional). Needs Node 22+, git, npm, and the Codex CLI
// signed in.
//
// Profiles: another person's Site lives in their own ChatGPT account, so it needs its own Codex login. With
// `--profile NAME` (or HOMIE_SITE_PROFILE=NAME) everything above runs against ~/.homie-site-NAME: its own state,
// its own Codex home (so your login is never touched) and its own Keychain item `homie-key-NAME`. First `--login`,
// then release as usual. A profile that turns out to be signed in to the SAME account as your own Codex is refused (the
// code was approved with the wrong account). Without a profile nothing changes.
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { copyFile, cp, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import readline from "node:readline";
import { fileURLToPath } from "node:url";

const args = process.argv.slice(2);
const flags = new Set(args);
// An option value is given as `--name value` or `--name=value`.
function option(name) {
  const at = args.findIndex((arg) => arg === `--${name}` || arg.startsWith(`--${name}=`));
  if (at < 0) return undefined;
  if (args[at].includes("=")) return args[at].slice(name.length + 3);
  return args[at + 1]?.startsWith("--") ? "" : (args[at + 1] ?? "");
}
const fatal = (message) => {
  console.error(`[release] ${message}`);
  process.exit(2);
};
const PROFILE = (option("profile") ?? process.env.HOMIE_SITE_PROFILE ?? "").trim();
if (PROFILE && !/^[a-z][a-z0-9-]{0,19}$/.test(PROFILE)) fatal("A profile name is lowercase letters, digits and dashes, up to 20 characters, e.g. --profile tanya.");
const SITE_DIR = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const HOME_DIR = process.env.HOMIE_SITE_HOME ?? join(homedir(), PROFILE ? `.homie-site-${PROFILE}` : ".homie-site");
const CHECKOUT = join(HOME_DIR, "checkout");
const KEY_ITEM = process.env.HOMIE_KEY_ITEM ?? (PROFILE ? `homie-key-${PROFILE}` : "homie-key");
const CODEX_HOME = PROFILE ? join(HOME_DIR, "codex") : null;
const CODEX_ENV = CODEX_HOME ? { ...process.env, CODEX_HOME } : process.env; // a profile never inherits another login
const TITLE = (option("title") ?? "").trim();
const SLUG = (option("slug") ?? "").trim();
const DESCRIPTION = (option("description") ?? "").trim() || "Private MCP tools for ChatGPT over my own home hub (Homie).";
// The Site is found by its slug, so every command that publishes to it needs the title and the slug.
const touchesSite = !["--dry-run", "--check", "--list-sites-tools", "--login", "--logout"].some((flag) => flags.has(flag));
if (touchesSite) {
  if (!TITLE || !SLUG) fatal('Name the Site: --title "Мой шалаш" --slug moy-shalash (the slug is its unique address name).');
  if (!/^[a-z0-9][a-z0-9-]*$/.test(SLUG)) fatal("A slug is lowercase letters, digits and dashes, e.g. --slug moy-shalash.");
}
const API_URL_FILE = join(HOME_DIR, "api-url");
const LOGIN_WAIT_MINUTES = 15;
const log = (line) => console.log(`[release] ${line}`);
const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

// ---------------------------------------------------------------- processes
function run(command, args, { cwd, env = process.env, timeoutMs = 120_000, input } = {}) {
  return new Promise((done) => {
    const child = spawn(command, args, { cwd, env, stdio: ["pipe", "pipe", "pipe"] });
    let out = "";
    let err = "";
    const timer = setTimeout(() => child.kill("SIGKILL"), timeoutMs);
    child.stdout.on("data", (chunk) => (out += chunk));
    child.stderr.on("data", (chunk) => (err += chunk));
    child.on("error", (error) => done({ ok: false, code: -1, out, err: String(error) }));
    child.on("close", (code) => {
      clearTimeout(timer);
      done({ ok: code === 0, code, out, err });
    });
    child.stdin.on("error", () => {}); // a child that exits without reading (`which`) closes stdin first: EPIPE, not a failure
    child.stdin.end(input ?? "");
  });
}

async function runOrThrow(command, args, options) {
  const result = await run(command, args, options);
  if (!result.ok) throw new Error(`${command} ${args[0] ?? ""} failed (${result.code}):\n${(result.out + result.err).split("\n").slice(-15).join("\n")}`);
  return result.out.trim();
}

// ---------------------------------------------------------------- the Codex app-server (JSON-RPC over stdio)
class AppServer {
  constructor(bin, cwd, env) {
    this.pending = new Map();
    this.notifications = new Map(); // the latest params of every notification, by method
    this.nextId = 1;
    this.threadId = null;
    this.child = spawn(bin, ["app-server"], { cwd, env, stdio: ["pipe", "pipe", "pipe"] });
    this.child.stderr.on("data", () => {});
    this.child.on("exit", () => this.fail(new Error("codex app-server exited")));
    process.once("exit", () => this.child.kill());
    readline.createInterface({ input: this.child.stdout }).on("line", (line) => this.receive(line));
  }

  static async start(bin, cwd, env = process.env) {
    const server = new AppServer(bin, cwd, env);
    await server.request("initialize", { clientInfo: { name: "homie-site-release", version: "1.0.0" } });
    server.write({ method: "initialized" });
    return server;
  }

  write(message) {
    this.child.stdin.write(`${JSON.stringify(message)}\n`);
  }

  receive(line) {
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      return;
    }
    if (message.method) {
      if (message.id === undefined) this.notifications.set(message.method, message.params ?? {}); // requests of the server stay unanswered, as before
      return;
    }
    if (message.id === undefined) return;
    const waiter = this.pending.get(message.id);
    if (!waiter) return;
    this.pending.delete(message.id);
    clearTimeout(waiter.timer);
    if (message.error) waiter.reject(new Error(`${message.error.message ?? "app-server error"} (${message.error.code ?? "?"})`));
    else waiter.resolve(message.result);
  }

  fail(error) {
    this.failure ??= error;
    for (const { reject, timer } of this.pending.values()) {
      clearTimeout(timer);
      reject(error);
    }
    this.pending.clear();
  }

  request(method, params = {}, timeoutMs = 60_000) {
    if (this.failure) return Promise.reject(this.failure);
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`${method} timed out`));
      }, timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
      this.write({ id, method, params });
    });
  }

  // Polls instead of subscribing, so a notification that arrives before anyone waits for it is not lost.
  async waitForNotification(method, timeoutMs) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      if (this.notifications.has(method)) return this.notifications.get(method);
      if (this.failure) throw this.failure;
      if (Date.now() > deadline) throw new Error(`${method} did not arrive in ${Math.round(timeoutMs / 60_000)} minutes`);
      await sleep(500);
    }
  }

  async callTool(tool, args = {}, timeoutMs) {
    if (!this.threadId) this.threadId = (await this.request("thread/start", { cwd: SITE_DIR, ephemeral: true })).thread.id;
    const result = await this.request("mcpServer/tool/call", { threadId: this.threadId, server: "codex_apps", tool, arguments: args }, timeoutMs);
    const text = (result?.content ?? []).filter((part) => part.type === "text").map((part) => part.text ?? "").join("\n");
    if (result?.isError || !result?.structuredContent) throw new Error(`${tool}: ${text.slice(0, 300) || "no result"}`);
    return result.structuredContent;
  }

  async listTools() {
    const status = await this.request("mcpServerStatus/list", {});
    return status?.data?.find((server) => server.name === "codex_apps")?.tools ?? {};
  }

  close() {
    this.child.kill();
  }
}

// ---------------------------------------------------------------- the Site
async function findOrCreateSite(server) {
  const { items } = await server.callTool("sites.list_sites", { limit: 50, role: "owner" });
  const existing = items.find((site) => site.slug === SLUG);
  if (existing) return existing;
  const slug = await server.callTool("sites.check_slug_availability", { slug: SLUG });
  if (slug.status !== "available") throw new Error(`The slug ${SLUG} is ${slug.status}; pick another one with --slug.`);
  return server.callTool("sites.create_site", { title: TITLE, slug: SLUG, description: DESCRIPTION, creation_intent: "user_requested" });
}

async function keychain(service) {
  const result = await run("security", ["find-generic-password", "-s", service, "-w"]);
  if (!result.ok || !result.out.trim()) throw new Error(`No Keychain item "${service}" (see site/README.md: the person's key).`);
  return result.out.trim();
}

// The home hub the Site calls: given once as HOME_API_URL, then remembered next to this profile's state.
async function apiUrl() {
  const given = (process.env.HOME_API_URL ?? "").trim().replace(/\/+$/, "");
  if (given) {
    if (!given.startsWith("https://")) throw new Error("HOME_API_URL must be an https:// address.");
    await mkdir(HOME_DIR, { recursive: true });
    await writeFile(API_URL_FILE, `${given}\n`);
    return given;
  }
  if (existsSync(API_URL_FILE)) return (await readFile(API_URL_FILE, "utf8")).trim();
  throw new Error(`Say once where the home hub is: HOME_API_URL=https://homie.example.com node release/release.mjs (it is remembered in ${API_URL_FILE}).`);
}

async function saveSettings(server, projectId, api) {
  const set = [
    { key: "HOME_API_URL", value: api },
    { key: "HOME_API_KEY", value: await keychain(KEY_ITEM), is_secret: true },
    ...(process.env.HOME_LANGUAGE ? [{ key: "HOME_LANGUAGE", value: process.env.HOME_LANGUAGE }] : []),
  ];
  const { revision } = await server.callTool("sites.update_environment_variables", { project_id: projectId, set_values: set });
  log(`settings saved (revision ${revision}): ${set.map((entry) => entry.key).join(", ")}`); // keys only, never values
}

// ---------------------------------------------------------------- git (the token travels in the environment only)
function gitAuth(credential) {
  return {
    env: { ...process.env, SITES_GIT_AUTHORIZATION: `Authorization: Bearer ${credential.token}` },
    config: ["-c", "credential.helper=", "-c", "http.extraHeader=", "-c", "http.followRedirects=false",
      `--config-env=http.${credential.remote_url}.extraHeader=SITES_GIT_AUTHORIZATION`],
  };
}

const git = (args) => runOrThrow("git", ["-C", CHECKOUT, ...args]);

async function syncCheckout(credential) {
  const { env, config } = gitAuth(credential);
  const probe = await run("git", [...config, "ls-remote", "--exit-code", credential.remote_url, `refs/heads/${credential.branch}`], { env });
  if (!probe.ok && probe.code !== 2) throw new Error(`git ls-remote failed:\n${probe.err.slice(-300)}`);
  if (!probe.ok) {
    // A new Site starts with an empty repository: the first release commits on an unborn branch and its push creates it.
    await rm(join(CHECKOUT, ".git"), { recursive: true, force: true });
    await mkdir(CHECKOUT, { recursive: true, mode: 0o700 });
    await git(["init", "--quiet"]);
    await git(["symbolic-ref", "HEAD", `refs/heads/${credential.branch}`]);
  } else if (!existsSync(join(CHECKOUT, ".git"))) {
    await mkdir(dirname(CHECKOUT), { recursive: true, mode: 0o700 });
    await runOrThrow("git", [...config, "clone", "--quiet", "--branch", credential.branch, credential.remote_url, CHECKOUT], { env, timeoutMs: 300_000 });
  } else {
    await runOrThrow("git", ["-C", CHECKOUT, ...config, "fetch", "--quiet", "--no-tags", credential.remote_url, `refs/heads/${credential.branch}`], { env, timeoutMs: 300_000 });
    await git(["checkout", "--quiet", "--force", "-B", credential.branch, "FETCH_HEAD"]);
  }
}

async function stageSource(projectId) {
  await mkdir(CHECKOUT, { recursive: true, mode: 0o700 });
  for (const entry of await readdir(CHECKOUT)) {
    if (![".git", "node_modules"].includes(entry)) await rm(join(CHECKOUT, entry), { recursive: true, force: true });
  }
  const listing = await runOrThrow("git", ["-C", SITE_DIR, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "."]);
  const files = [...new Set(listing.split("\0").filter(Boolean))].filter((file) => existsSync(join(SITE_DIR, file)) && !file.startsWith("release/"));
  for (const file of files) {
    await mkdir(dirname(join(CHECKOUT, file)), { recursive: true });
    await copyFile(join(SITE_DIR, file), join(CHECKOUT, file));
  }
  const template = JSON.parse(await readFile(join(CHECKOUT, ".openai", "hosting.template.json"), "utf8"));
  await writeFile(join(CHECKOUT, ".openai", "hosting.json"), `${JSON.stringify({ ...template, ...(projectId ? { project_id: projectId } : {}) }, null, 2)}\n`);
  return files.length;
}

async function commitRelease(message) {
  await git(["add", "--all"]);
  await git(["add", "--force", ".openai/hosting.json"]); // ignored for development, but every commit Sites builds from needs it
  if ((await run("git", ["-C", CHECKOUT, "diff", "--cached", "--quiet"])).ok) return null;
  // Always the Site's own identity: the checkout is the hosted repository of the Site, not a personal one.
  const identity = ["-c", "user.name=Homie Site", "-c", "user.email=homie-site@users.noreply.github.com"];
  await runOrThrow("git", ["-C", CHECKOUT, ...identity, "commit", "--quiet", "-m", message]);
  return git(["rev-parse", "HEAD"]);
}

// ---------------------------------------------------------------- build, package, publish
async function build() {
  const npm = (args) => runOrThrow("npm", args, { cwd: CHECKOUT, timeoutMs: 600_000 });
  log("npm ci");
  await npm(["ci", "--no-audit", "--no-fund", "--loglevel=error"]);
  log("npm run check (tests and typecheck)");
  await npm(["run", "check"]);
  log("npm run build");
  await npm(["run", "build"]);
}

async function packageWorker(archive) {
  if (!existsSync(join(CHECKOUT, "dist", "server", "index.js"))) throw new Error("The build left no dist/server/index.js.");
  const hosting = JSON.parse(await readFile(join(CHECKOUT, ".openai", "hosting.json"), "utf8"));
  const builtFile = join(CHECKOUT, "dist", ".openai", "hosting.json");
  const built = existsSync(builtFile) ? JSON.parse(await readFile(builtFile, "utf8")) : {};
  if (built.artifact_metadata != null) hosting.artifact_metadata = built.artifact_metadata;
  const stage = await mkdtemp(join(tmpdir(), "homie-package-"));
  try {
    await cp(join(CHECKOUT, "dist"), join(stage, "dist"), { recursive: true });
    await mkdir(join(stage, "dist", ".openai"), { recursive: true });
    await writeFile(join(stage, "dist", ".openai", "hosting.json"), `${JSON.stringify(hosting, null, 2)}\n`);
    await mkdir(dirname(archive), { recursive: true });
    await runOrThrow("tar", ["-C", stage, "-czf", archive, "dist"]);
  } finally {
    await rm(stage, { recursive: true, force: true });
  }
  const entries = (await runOrThrow("tar", ["-tzf", archive])).split("\n");
  if (!entries.includes("dist/.openai/hosting.json")) throw new Error("The archive has no dist/.openai/hosting.json.");
  log(`archive ${((await stat(archive)).size / 1024 / 1024).toFixed(1)} MB, ${entries.length} entries`);
}

// The one model-driven step: only Codex's own tool layer uploads a local file to the Sites connector.
async function publish(codex, projectId, commitSha, archive) {
  const work = await mkdtemp(join(tmpdir(), "homie-publish-"));
  try {
    const schema = join(work, "schema.json");
    const result = join(work, "result.json");
    await writeFile(schema, JSON.stringify({
      type: "object",
      properties: { status: { type: "string", enum: ["deployed", "failed"] }, version_number: { type: ["integer", "null"] }, deployment_id: { type: ["string", "null"] }, message: { type: "string" } },
      required: ["status", "version_number", "deployment_id", "message"], additionalProperties: false,
    }));
    const prompt = [
      "You are one step of an automated release. Make exactly one tool call and nothing else.",
      `Call the Sites tool save_version_and_deploy_private with project_id "${projectId}", commit_sha "${commitSha}" and archive "${archive}".`,
      "The commit is already pushed and the archive is already built from it.",
      "Do not run commands, read or edit files, push, build, or call any other tool, even if the call fails.",
      "Then return only the JSON result: status is deployed when the call returned a deployment, otherwise failed; message is one sentence.",
    ].join("\n");
    log("codex exec: save_version_and_deploy_private");
    await runOrThrow(codex, ["exec", "--ephemeral", "--approve-for-me", "--output-schema", schema, "-o", result, prompt], { cwd: CHECKOUT, env: CODEX_ENV, timeoutMs: 15 * 60_000 });
    const report = JSON.parse(await readFile(result, "utf8"));
    log(`Codex: ${report.status}, version ${report.version_number ?? "?"}: ${report.message}`);
    if (report.status !== "deployed") throw new Error(`Publishing failed: ${report.message}`);
    return report;
  } finally {
    await rm(work, { recursive: true, force: true });
  }
}

async function waitForDeployment(server, projectId, deploymentId) {
  for (const attempt of Array.from({ length: 120 }, (_, i) => i)) {
    const deployment = await server.callTool("sites.get_deployment_status", { project_id: projectId, deployment_id: deploymentId });
    if (deployment.status === "succeeded" || deployment.status === "failed") return deployment;
    if (attempt % 6 === 0) log(`deployment ${deployment.status}…`);
    await sleep(5_000);
  }
  throw new Error("The deployment did not finish in 10 minutes.");
}

// ---------------------------------------------------------------- the live plugin as ChatGPT sees it
// ChatGPT names a Site plugin's tools "<plugin>.<tool>". A brand-new plugin shows up only after you connect it
// in ChatGPT (Plugins -> Created by you). The plugin's name is whatever ChatGPT shows, so it is found by a tool of the
// connection test (home_ping), or given with --plugin.
async function check(server) {
  const visible = await server.listTools();
  const marker = ".home_ping";
  const prefix = option("plugin")?.trim() || Object.keys(visible).find((name) => name.endsWith(marker))?.slice(0, -marker.length);
  if (!prefix) {
    log("ChatGPT does not show the home plugin yet (or its name is unknown): connect it in ChatGPT (Plugins -> Created by you), or pass --plugin NAME.");
    return false;
  }
  const mine = Object.keys(visible).filter((name) => name.startsWith(`${prefix}.`));
  if (!mine.length) {
    log(`ChatGPT lists no tools for a plugin named "${prefix}".`);
    return false;
  }
  log(`plugin "${prefix}" shows ${mine.length} tools: ${mine.map((name) => name.slice(prefix.length + 1)).join(", ")}`);
  // A read-only round trip through the whole chain: ChatGPT -> the Site -> the relay -> the hub.
  if (visible[`${prefix}${marker}`]) {
    const ping = await server.callTool(`${prefix}${marker}`, {});
    log(`home_ping: ${JSON.stringify(ping)}`);
  }
  return true;
}

// Read-only: what the Sites tools of Codex are called and which parameters they take, for the notes in
// docs/research/sites-tools.md. If no tool looks like sites.*, every tool name is listed, so the naming can be seen.
async function listSitesTools(server) {
  const visible = await server.listTools();
  const names = Object.keys(visible).sort();
  const sites = names.filter((name) => /^sites[._]/.test(name));
  if (!sites.length) log(`no sites.* tools among codex_apps; all ${names.length} tool names: ${names.join(", ")}`);
  const dump = Object.fromEntries(sites.map((name) => [name, { description: visible[name]?.description ?? "", inputSchema: visible[name]?.inputSchema ?? null }]));
  console.log(JSON.stringify(dump, null, 2));
  return sites.length > 0;
}

// ---------------------------------------------------------------- the Codex login
const maskEmail = (email) => (email ? email.replace(/^(.).*(@.*)$/, "$1***$2") : "(no email)");

// A profile's Codex home starts empty. The checkout it publishes from is marked trusted, as it is in the first login's
// home, so that the one `codex exec` call may work there.
async function prepareCodexHome() {
  if (!CODEX_HOME) return;
  await mkdir(CODEX_HOME, { recursive: true, mode: 0o700 });
  const config = join(CODEX_HOME, "config.toml");
  if (!existsSync(config)) await writeFile(config, `[projects.${JSON.stringify(CHECKOUT)}]\ntrust_level = "trusted"\n`, { mode: 0o600 });
}

async function signedInAccount(server) {
  const { account } = await server.request("account/read", { refreshToken: false });
  return account?.type === "chatgpt" ? account : null;
}

// A profile exists for a SECOND ChatGPT account. If its code was approved with the first one, a release would put the
// other person's key into the first account's Site, so the two logins are compared by e-mail (never printed in full).
async function sharesTheFirstAccount(codex, server) {
  if (!PROFILE) return false;
  const mine = await signedInAccount(server);
  const first = await AppServer.start(codex, SITE_DIR, process.env);
  try {
    const theirs = await signedInAccount(first);
    return Boolean(mine?.email && theirs?.email && mine.email.toLowerCase() === theirs.email.toLowerCase());
  } finally {
    first.close();
  }
}

// Device-code login: whoever holds the ChatGPT account opens the address on any device where it is signed in and types
// the code. Nothing secret is printed: the code works once, for a few minutes, and only for that account's sign-in.
async function login(codex, server) {
  let account = await signedInAccount(server);
  if (account) {
    log(`already signed in to ChatGPT: plan ${account.planType}, ${maskEmail(account.email)}`);
  } else {
    const started = await server.request("account/login/start", { type: "chatgptDeviceCode" });
    console.log([
      "",
      "Sign in with the ChatGPT account that should own the Site:",
      `  1. open ${started.verificationUrl}`,
      `  2. enter the code ${started.userCode}`,
      `Waiting up to ${LOGIN_WAIT_MINUTES} minutes for the approval...`,
      "",
    ].join("\n"));
    let done;
    try {
      done = await server.waitForNotification("account/login/completed", LOGIN_WAIT_MINUTES * 60_000);
    } catch (error) {
      await server.request("account/login/cancel", { loginId: started.loginId }).catch(() => {});
      throw error;
    }
    if (!done.success) throw new Error(`The login failed: ${done.error ?? "no reason given"}`);
    account = await signedInAccount(server);
    log(`signed in: plan ${account?.planType ?? "?"}, ${maskEmail(account?.email)}`);
  }
  if (await sharesTheFirstAccount(codex, server)) {
    await server.request("account/logout", null).catch(() => {});
    throw new Error(`That is the SAME ChatGPT account as your own Codex (${maskEmail(account?.email)}); the profile is signed out again. Log in with the other person's account.`);
  }
}

// ---------------------------------------------------------------- main
async function main() {
  const codex = (await runOrThrow("which", ["codex"]).catch(() => "")) || "/opt/homebrew/bin/codex";
  if (PROFILE) log(`profile "${PROFILE}": state in ${HOME_DIR}, Codex home ${CODEX_HOME}, key from the Keychain item ${KEY_ITEM}`);
  if (flags.has("--dry-run") && ["--login", "--logout", "--check", "--list-sites-tools", "--settings-only"].some((flag) => flags.has(flag))) {
    throw new Error("--login, --logout, --check, --list-sites-tools and --settings-only need the account, so they cannot be a dry run.");
  }
  if (flags.has("--logout") && !PROFILE) throw new Error("--logout works on a profile only; it never signs out your own Codex.");
  await prepareCodexHome();
  const server = flags.has("--dry-run") ? null : await AppServer.start(codex, SITE_DIR, CODEX_ENV);
  try {
    if (flags.has("--login")) return void (await login(codex, server));
    if (flags.has("--logout")) {
      await server.request("account/logout", null);
      return log(`profile "${PROFILE}" is signed out`);
    }
    if (server && !(await signedInAccount(server))) {
      log(`Codex is not signed in to ChatGPT${PROFILE ? ` for the profile "${PROFILE}"` : ""}: run with --login first.`);
      return void (process.exitCode = 3);
    }
    if (server && (await sharesTheFirstAccount(codex, server))) {
      log(`The profile "${PROFILE}" is signed in to the SAME ChatGPT account as your own Codex, so nothing was done: run with --logout, then --login with the other person's account.`);
      return void (process.exitCode = 3);
    }
    if (flags.has("--check")) return void (process.exitCode = (await check(server)) ? 0 : 3);
    if (flags.has("--list-sites-tools")) return void (process.exitCode = (await listSitesTools(server)) ? 0 : 3);
    const api = server ? await apiUrl() : null; // before anything changes in the account
    if (api) log(`home hub ${api}`);
    let site = null;
    if (server) {
      site = await findOrCreateSite(server);
      log(`Site "${site.title}" (${site.slug}), id ${site.id}`);
    }
    if (flags.has("--settings-only")) {
      await saveSettings(server, site.id, api);
      const versions = await server.callTool("sites.list_site_versions", { project_id: site.id, limit: 1 });
      const live = versions.items[0];
      if (live) {
        const deployment = await server.callTool("sites.deploy_private_site_version", { project_id: site.id, version_id: live.id });
        log(`redeploying version ${live.version_number}: ${(await waitForDeployment(server, site.id, deployment.id)).status}`);
      }
      return;
    }
    if (server) await syncCheckout(await server.callTool("sites.create_source_repository_write_credential", { project_id: site.id }));
    log(`${await stageSource(site?.id)} files staged`);
    if (!server) await runOrThrow("git", ["-C", CHECKOUT, "init", "--quiet"]).catch(() => {});
    const sha = (await commitRelease("Homie Site")) ?? (await git(["rev-parse", "HEAD"]).catch(() => null));
    log(sha ? `commit ${sha.slice(0, 7)}` : "nothing to commit");
    await build();
    const archive = join(dirname(CHECKOUT), "site.tgz");
    await packageWorker(archive);
    if (!server) return log("dry run: stopped before the account was touched");
    const credential = await server.callTool("sites.create_source_repository_write_credential", { project_id: site.id });
    const { env, config } = gitAuth(credential);
    await runOrThrow("git", ["-C", CHECKOUT, ...config, "push", "--quiet", credential.remote_url, `${sha}:refs/heads/${credential.branch}`], { env, timeoutMs: 300_000 });
    log(`pushed ${sha.slice(0, 7)}`);
    await saveSettings(server, site.id, api);
    const report = await publish(codex, site.id, sha, archive);
    const deployment = await waitForDeployment(server, site.id, report.deployment_id);
    if (deployment.status !== "succeeded") throw new Error(`The deployment failed: ${deployment.failure_message ?? "no reason given"}`);
    const fresh = (await server.callTool("sites.list_sites", { limit: 50, role: "owner" })).items.find((item) => item.id === site.id);
    log(`live: ${fresh?.current_live_url ?? deployment.url ?? "(no url reported)"}`);
    log("Next, in ChatGPT: Plugins -> Created by you -> connect it; the first time it acts choose Always allow.");
  } finally {
    server?.close();
  }
}

main().catch((error) => {
  console.error(`[release] ${String(error.message ?? error).replaceAll(process.env.HOME_API_KEY ?? "\u0000", "[redacted]")}`);
  process.exit(1);
});
