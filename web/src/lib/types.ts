// Wire types of Homie's own API, /api/home/* (docs/ADMIN-API.md §2 without keys, the relay, Sites and connections),
// and of the LAN first-run page, /api/firstrun/*. People, passkeys, sessions, invites and connections are the
// gatekeeper's: their types come from @devxpro/mcp-gatekeeper-ui. Optional keys are omitted by the server.

import type { Account, Lang, Resource } from '@devxpro/mcp-gatekeeper-ui'

export type { Lang }
export type ISOTime = string

/** One field of a 400 invalid answer: a JSON Pointer into the request (ADMIN-API §1.2). */
export interface FieldError {
  path: string
  code: string
  message: string
}

// ---------------------------------------------------------------- the tunnel (STEP2 §4.5, §4.7)

export type TunnelMode = 'named' | 'quick' | 'off'

/** The supervisor's state: starting → probing → connected; failing while the child is down; stopped. */
export type TunnelState = 'starting' | 'probing' | 'connected' | 'failing' | 'stopped'

/**
 * The tunnel as homed reports it. `probing`: the address is known but the outside check (GET <url>/healthz must
 * carry this homed's X-Homie-Instance) does not pass yet; `lastError` then says why. `url` and `mcpUrl` are absent
 * while a quick tunnel has no hostname yet; a quick tunnel's change on every restart.
 */
export interface TunnelStatus {
  mode: TunnelMode
  state: TunnelState
  /** the public origin, https://<host> */
  url?: string
  /** the connector address, <url>/mcp */
  mcpUrl?: string
  hostname?: string
  since?: ISOTime
  /** English, for «Подробнее» */
  lastError?: string
}

/** GET /api/home/bootstrap (public): what the SPA needs before anything else. */
export interface Bootstrap {
  homeName: string
  language: Lang
  ownerReady?: boolean
  tunnel: { mode: TunnelMode; url?: string }
}

// ---------------------------------------------------------------- plugins and tools

export type PluginState = 'starting' | 'up' | 'failing' | 'stopped' | 'disabled'

export interface PluginSummary {
  id: string
  name: string
  title: string
  description?: string
  version: string
  state: PluginState
  since: ISOTime
  lastError?: string
  enabled: boolean
  source: { kind: 'github' | 'local'; repo?: string; homepage?: string }
  update?: { latest: string; checkedAt: ISOTime }
  tools: { assistant: number; admin: number }
  inventoryTool?: string
  devices?: number
}

export interface PluginDetail extends Omit<PluginSummary, 'tools'> {
  instructions?: string
  previousVersion?: string
  command?: string[]
  tools: Tool[]
}

/** A JSON Schema as plugins send it; only the keys the form reads are typed. */
export interface JSONSchema {
  type?: string | string[]
  title?: string
  description?: string
  properties?: Record<string, JSONSchema>
  required?: string[]
  enum?: unknown[]
  'x-enumNames'?: string[]
  oneOf?: JSONSchema[]
  anyOf?: JSONSchema[]
  const?: unknown
  $ref?: string
  items?: JSONSchema | JSONSchema[]
  default?: unknown
  format?: string
  pattern?: string
  minLength?: number
  maxLength?: number
  minimum?: number
  maximum?: number
  multipleOf?: number
  writeOnly?: boolean
  'x-homie-ref'?: string
  [key: string]: unknown
}

/** Translated tool texts from the plugin's _meta "homie/i18n", keyed by language. */
export interface ToolText {
  title?: string
  description?: string
  properties?: Record<string, string>
}

export interface ToolAnnotations {
  title?: string
  readOnlyHint?: boolean
  destructiveHint?: boolean
  idempotentHint?: boolean
  openWorldHint?: boolean
}

export interface Tool {
  name: string
  pluginId: string
  title: string
  description: string
  audience: 'admin' | 'assistant'
  inputSchema: JSONSchema
  outputSchema?: JSONSchema
  annotations?: ToolAnnotations
  timeoutMs: number
  inventory: boolean
  target?: string
  i18n?: Partial<Record<Lang, ToolText>>
}

export type ItemState = 'on' | 'off' | 'offline' | 'unknown' | 'paired' | 'unpaired'

export interface InventoryItem {
  id: string
  name: string
  subtitle?: string
  state?: ItemState
}

export interface Inventory {
  items: InventoryItem[]
  fetchedAt: ISOTime
}

export type CallState = 'running' | 'ok' | 'failed' | 'error' | 'cancelled'

export interface CallProgress {
  progress: number
  total?: number
  message?: string
  /** translations of `message` by language, e.g. {"ru": "Нажмите OK на пульте телевизора"} */
  messageI18n?: Partial<Record<Lang, string>>
}

export interface Call {
  id: string
  tool: string
  pluginId: string
  title: string
  args: Record<string, unknown>
  state: CallState
  startedAt: ISOTime
  endedAt?: ISOTime
  durationMs?: number
  timeoutMs: number
  progress?: CallProgress
  result?: { text: string; structured?: unknown }
  error?: { code: string; message: string }
}

export type JobKind = 'plugin.install' | 'plugin.update' | 'plugin.remove' | 'hub.update'

export interface Job {
  id: string
  kind: JobKind
  target?: string
  state: 'running' | 'ok' | 'failed'
  steps: string[]
  step?: string
  progress?: { done: number; total?: number }
  message?: string
  startedAt: ISOTime
  endedAt?: ISOTime
  error?: { code: string; message: string }
}

// ---------------------------------------------------------------- the activity log (tool calls)

/** Where a call came from: an assistant (by its OAuth client), the admin or the CLI. */
export type Via = 'chatgpt' | 'claude' | 'admin' | 'cli'

/** One tool call of the activity log (the Homie table `activity`), arguments redacted. */
export interface ActivityItem {
  id: string
  at: ISOTime
  person?: { id?: string; name: string }
  via: Via
  tool: string
  toolTitle?: string
  pluginId?: string
  args?: Record<string, unknown>
  ok: boolean
  error?: string
  durationMs: number
}

export interface Page<T> {
  items: T[]
  nextCursor?: string
}

export interface List<T> {
  items: T[]
}

// ---------------------------------------------------------------- the home

export interface Settings {
  homeName: string
  language: Lang
}

export interface SystemInfo {
  version: string
  commit: string
  os: string
  arch: string
  startedAt: ISOTime
  update?: { latest: string; notesUrl: string; checkedAt: ISOTime }
}

/** What needs the owner, by severity (HC-api §3). */
export type Attention =
  /** someone ran `homed admin reset-access` on the home box lately; `peer` is the program that asked (GK-5) */
  | { kind: 'access_reset'; at?: ISOTime; peer?: string }
  | { kind: 'tunnel_offline' }
  | { kind: 'tunnel_unreachable' }
  | { kind: 'plugin_failing'; target: string }
  | { kind: 'hub_update'; version: string }
  | { kind: 'plugin_update'; target: string; version: string }
  /** a quick tunnel: «для пробы»; Дом shows it as its banner */
  | { kind: 'tunnel_quick' }

export interface Overview {
  home: { name: string }
  tunnel: TunnelStatus
  plugins: PluginSummary[]
  attention: Attention[]
  recent: ActivityItem[]
}

export interface PluginPreview {
  repo: string
  id: string
  title: string
  description?: string
  version: string
  publishedAt?: ISOTime
  homepage?: string
  minHubVersion?: string
  compatible: boolean
  assetFound: boolean
  installed?: { version: string }
}

/** A device a person may use, as GET /api/account lists it for the viewer (a member sees only theirs). */
export interface AccountDevice {
  id: string
  title: string
  /** its permission id, "plugin:<id>" */
  permission: string
}

/** GET /api/account with Homie's fields (people.AccountFields, HC-api §5). */
export interface HomeAccount extends Account {
  /** the home, the one MCP resource: feeds the connect page */
  resources?: Resource[]
  devices?: AccountDevice[]
  tunnel?: { mode: TunnelMode }
}

/** One event of GET /api/home/events/poll: the same as an SSE frame's id, event and data. */
export interface PolledEvent {
  id?: string
  event: string
  data: unknown
}

export interface PollAnswer {
  events: PolledEvent[]
  /** where the next poll resumes: its `after` */
  cursor: string
}

// ---------------------------------------------------------------- the LAN first-run page (STEP2 §4.8)

/** The Cloudflare connect of "my domain": login link → tunnel → DNS → config (gatekeeper Connector.Start). */
export type ConnectStepName = 'login' | 'tunnel' | 'dns' | 'config'

/** The connect job of "my domain" (HC-api §7 ConnectJob). */
export interface ConnectStatus {
  hostname: string
  state: 'running' | 'ok' | 'failed'
  step?: ConnectStepName
  /** the Cloudflare link to sign in and pick the zone, as soon as cloudflared prints it */
  loginUrl?: string
  /** English; an existing DNS record says to retry with overwriteDns */
  error?: string
  startedAt?: ISOTime
  endedAt?: ISOTime
}

export interface FirstRunState {
  /** pin; tunnel: nothing connects the home yet; connecting: a connect runs or the tunnel comes up; link */
  step: 'pin' | 'tunnel' | 'connecting' | 'link'
  homeName?: string
  /** with "link": the owner is set up already, so this is the quick-mode Reconnect (the address changed) */
  ownerExists?: boolean
  tunnel?: TunnelStatus
  /** the last connect, also a failed one */
  connect?: ConnectStatus
}

export interface SetupLink {
  url: string
  expiresAt: ISOTime
}
