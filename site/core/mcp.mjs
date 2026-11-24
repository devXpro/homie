// The Site's MCP server for Homie: a thin proxy. The tools, their descriptions and the rules for the model live in
// the home hub (GET /api/v1/tools, reached through the relay); this file speaks MCP to ChatGPT and forwards the calls.
// No dependencies, so it runs under node --test.

export const VERSION = "1.0.0";
const PROTOCOL_VERSIONS = ["2025-11-25", "2025-06-18", "2025-03-26"];
const CAPABILITIES = { tools: {} };
const MANIFEST_TTL_MS = 60_000;
const MANIFEST_DEADLINE_MS = 10_000;
const CALL_DEADLINE_MS = 45_000; // ChatGPT gives up on a tool call at about a minute
const TOOL_KEYS = ["name", "title", "description", "inputSchema", "outputSchema", "annotations"];

// Site settings -> Environment variables.
export function readConfig(env = {}) {
  return {
    apiUrl: String(env.HOME_API_URL ?? "").trim().replace(/\/+$/, ""),
    apiKey: String(env.HOME_API_KEY ?? ""),
    language: String(env.HOME_LANGUAGE ?? "").trim(),
  };
}

class BackendError extends Error {
  constructor(kind, { status = 0, detail = "" } = {}) {
    super(kind);
    // refused | bad_request | not_found | tool_error | rate_limited | plugin_down | hub_timeout | offline | unavailable
    // | timeout | unreachable
    this.kind = kind;
    this.status = status;
    this.detail = detail;
  }
}

async function backend(config, fetchImpl, path, { method = "GET", body, deadlineMs }) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), deadlineMs);
  try {
    const response = await fetchImpl(config.apiUrl + path, {
      method,
      headers: {
        authorization: `Bearer ${config.apiKey}`,
        accept: "application/json",
        ...(body === undefined ? {} : { "content-type": "application/json" }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: controller.signal,
    });
    let data = null;
    try {
      data = await response.json();
    } catch {
      // not JSON: the status below says what happened
    }
    if (response.ok) return data;
    const detail = typeof data?.error === "string" ? data.error : "";
    // The hub always answers 404 with JSON; a 404 that is not JSON is the relay's "home is offline" page.
    const offline = response.status === 404 && data === null;
    const kind = offline
      ? "offline"
      : { 400: "bad_request", 401: "refused", 404: "not_found", 422: "tool_error", 429: "rate_limited", 503: "plugin_down", 504: "hub_timeout" }[response.status] ?? "unavailable";
    throw new BackendError(kind, { status: response.status, detail });
  } catch (error) {
    if (error instanceof BackendError) throw error;
    throw new BackendError(error?.name === "AbortError" ? "timeout" : "unreachable", { detail: String(error?.message ?? "") });
  } finally {
    clearTimeout(timer);
  }
}

// FNV-1a, enough to fingerprint what the model reads.
function fingerprint(text) {
  let hash = 0x811c9dc5;
  for (let i = 0; i < text.length; i += 1) {
    hash ^= text.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16).padStart(8, "0");
}

const OFFLINE_TEXT = "The home hub is offline right now (the home computer may be asleep or without internet). Try again later.";

const pick = (tool) => Object.fromEntries(TOOL_KEYS.filter((key) => tool[key] !== undefined).map((key) => [key, tool[key]]));

// ChatGPT keeps its own snapshot of a plugin's tools and re-reads it when the server version moves: the version
// therefore carries a fingerprint of everything the model reads, so a change in the hub or its plugins shows up here.
function describeServer(manifest, config) {
  const tools = (manifest.tools ?? []).map(pick);
  const instructions = [manifest.instructions ?? "", config.language ? `Speak to the user in ${config.language}.` : ""].filter(Boolean).join(" ");
  const serverInfo = { name: "homie", version: `${VERSION}+${fingerprint(JSON.stringify(tools) + instructions)}` };
  return { tools, instructions, serverInfo };
}

function explain(error, name, isWrite) {
  const unknownEffect = isWrite ? " Unknown whether it took effect: never say it did." : "";
  switch (error.kind) {
    case "not_found":
      return `There is no tool "${name}": ChatGPT is using an outdated tool list for this plugin. Tell the user to start a new chat so it reloads.`;
    case "refused":
      return "The home hub refused this Site's key: HOME_API_KEY in the Site settings is wrong or was revoked. Tell the user to update it and redeploy.";
    case "bad_request":
      return `The request was refused: ${error.detail || "invalid arguments"}.`;
    case "tool_error":
      return error.detail || "The device reported a failure.";
    case "rate_limited":
      return "The home hub is rate limiting this key. Try again in a minute.";
    case "plugin_down":
      return `The device service on the home hub is not running right now.${unknownEffect || " Try again in a minute."}`;
    case "offline":
      return OFFLINE_TEXT;
    case "hub_timeout":
    case "timeout":
      return `The home hub did not answer in time.${unknownEffect || " Try again."}`;
    default:
      return `The home hub could not be reached right now.${unknownEffect || " Try again in a minute."}`;
  }
}

const scrub = (text, config) => (config.apiKey ? String(text).replaceAll(config.apiKey, "[redacted]") : String(text));

// Returns a JSON-RPC handler: (message) => response object, or null for notifications that need no body.
export function createMcpHandler({ config, fetchImpl = fetch, now = Date.now, cache = defaultCache }) {
  async function manifest() {
    if (cache.value && cache.url === config.apiUrl && now() - cache.at < MANIFEST_TTL_MS) return cache.value;
    const value = await backend(config, fetchImpl, "/api/v1/tools", { deadlineMs: MANIFEST_DEADLINE_MS });
    Object.assign(cache, { value, url: config.apiUrl, at: now() });
    return value;
  }

  return async function handle(rpc) {
    const id = rpc?.id ?? null;
    const reply = (result) => ({ jsonrpc: "2.0", id, result });
    const fail = (code, message) => ({ jsonrpc: "2.0", id, error: { code, message } });
    if (!rpc || rpc.jsonrpc !== "2.0" || typeof rpc.method !== "string") return fail(-32600, "Invalid request");
    if (rpc.method.startsWith("notifications/") && rpc.id === undefined) return null;
    if (rpc.method === "ping") return reply({});
    if (!config.apiUrl || !config.apiKey) {
      return fail(-32603, "Configure HOME_API_URL and HOME_API_KEY in the Site settings and redeploy.");
    }
    try {
      switch (rpc.method) {
        case "initialize": {
          const { instructions, serverInfo } = describeServer(await manifest(), config);
          const asked = rpc.params?.protocolVersion;
          const protocolVersion = PROTOCOL_VERSIONS.includes(asked) ? asked : PROTOCOL_VERSIONS[1];
          return reply({ protocolVersion, capabilities: CAPABILITIES, serverInfo, instructions });
        }
        case "tools/list":
          return reply({ tools: describeServer(await manifest(), config).tools });
        case "tools/call": {
          const name = rpc.params?.name;
          const args = rpc.params?.arguments ?? {};
          if (typeof name !== "string" || typeof args !== "object" || args === null || Array.isArray(args)) {
            return fail(-32602, "Invalid arguments");
          }
          try {
            const result = await backend(config, fetchImpl, `/api/v1/tools/${encodeURIComponent(name)}`, {
              method: "POST",
              body: args,
              deadlineMs: CALL_DEADLINE_MS,
            });
            // The hub wraps a plain text result as {"text": "..."}: hand that over as the text itself.
            const keys = Object.keys(result ?? {});
            const plain = keys.length === 1 && keys[0] === "text" && typeof result.text === "string";
            return reply({ content: [{ type: "text", text: plain ? result.text : JSON.stringify(result) }], structuredContent: result ?? {} });
          } catch (error) {
            if (!(error instanceof BackendError)) throw error;
            const known = (cache.value?.tools ?? []).find((tool) => tool.name === name);
            const isWrite = known ? known.annotations?.readOnlyHint === false : false;
            return reply({ content: [{ type: "text", text: scrub(explain(error, name, isWrite), config) }], isError: true });
          }
        }
        default:
          return fail(-32601, "Method not found");
      }
    } catch (error) {
      return fail(-32603, scrub(error instanceof BackendError ? explain(error, "", false) : "Internal error", config));
    }
  };
}

const defaultCache = {};
