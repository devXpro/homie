// Wire types of the admin API (docs/ADMIN-API.md §2). Optional keys are omitted by the server, never null.

export type Lang = 'ru' | 'en'
export type ISOTime = string

export interface Features {
  people: boolean
  connections: boolean
}

export interface FieldError {
  path: string
  code: string
  message: string
}

export interface ErrorBody {
  error: { code: string; message: string; requestId?: string; fields?: FieldError[]; callId?: string; jobId?: string }
  callId?: string
  jobId?: string
}

export type Role = 'owner' | 'member'
export type ConnectionState = 'pending' | 'needs_login' | 'publishing' | 'live' | 'failed' | 'revoked'

export interface ConnectionSummary {
  id: string
  kind: 'site' | 'plugin'
  state: ConnectionState
  stale: boolean
}

export interface Person {
  id: string
  name: string
  role: Role
  plugins: string[]
  language?: Lang
  createdAt: ISOTime
  passkeys: number
  lastActiveAt?: ISOTime
  connection?: ConnectionSummary
}

export interface Me {
  person: Person
  session: { id: string; createdAt: ISOTime; expiresAt: ISOTime; idleExpiresAt: ISOTime; stepUpUntil?: ISOTime }
  home: { name: string; language: Lang }
  version: string
  features: Features
}

export interface Bootstrap {
  homeName: string
  language: Lang
  ownerReady: boolean
  features: Features
  connectionKind?: 'site' | 'plugin'
}

export interface Session {
  id: string
  device: string
  createdAt: ISOTime
  lastSeenAt: ISOTime
  current: boolean
}

export interface Passkey {
  id: string
  name: string
  provider?: string
  createdAt: ISOTime
  lastUsedAt?: ISOTime
  current: boolean
}

export interface Key {
  id: string
  personId: string
  label: string
  hint?: string
  connectionId?: string
  createdAt: ISOTime
  lastUsedAt?: ISOTime
}

export type RelayState = 'not_configured' | 'connected' | 'connecting' | 'offline'

export interface RelayStatus {
  configured: boolean
  domain?: string
  publicUrl?: string
  state: RelayState
  since?: ISOTime
  lastError?: string
  selfCheck?: { ok: boolean; checkedAt?: ISOTime; error?: string }
}

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

/** Translated tool texts from the plugin's _meta "homie/i18n" (PLAN §5.1), keyed by language. */
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
  /** Translations of `message` by language (ADMIN-API §2 Call), e.g. {"ru": "Нажмите OK на пульте телевизора"}. */
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

export type JobKind =
  | 'plugin.install'
  | 'plugin.update'
  | 'plugin.remove'
  | 'hub.update'
  | 'connection.publish'
  | 'connections.republish'

export interface JobItem {
  id: string
  label: string
  state: 'waiting' | 'running' | 'ok' | 'failed' | 'needs_login' | 'skipped'
  message?: string
}

export interface Job {
  id: string
  kind: JobKind
  target?: string
  state: 'running' | 'ok' | 'failed'
  steps: string[]
  step?: string
  progress?: { done: number; total?: number }
  message?: string
  items?: JobItem[]
  startedAt: ISOTime
  endedAt?: ISOTime
  error?: { code: string; message: string }
}

export type Via = 'site' | 'mcp' | 'admin' | 'cli'

export interface AuditItem {
  id: string
  at: ISOTime
  kind: 'tool' | 'admin'
  person?: { id?: string; name: string }
  via: Via
  tool?: string
  toolTitle?: string
  pluginId?: string
  action?: string
  target?: string
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

export interface Settings {
  homeName: string
  language: Lang
  connectionKind?: 'site' | 'plugin'
  siteSignOut?: boolean
}

export interface SystemInfo {
  version: string
  commit: string
  os: string
  arch: string
  startedAt: ISOTime
  update?: { latest: string; notesUrl: string; checkedAt: ISOTime }
}

export type Attention =
  | { kind: 'relay_offline' }
  | { kind: 'relay_unreachable' }
  | { kind: 'plugin_failing'; target: string }
  | { kind: 'plugin_update'; target: string; version: string }
  | { kind: 'hub_update'; version: string }
  | { kind: 'connections_stale'; count: number }
  | { kind: 'connection_failed'; target: string }

export interface Overview {
  home: { name: string }
  relay: RelayStatus
  plugins: PluginSummary[]
  attention: Attention[]
  recent: AuditItem[]
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

export interface TokenInfo {
  kind: 'setup' | 'member'
  homeName: string
  personName?: string
  inviterName?: string
  expiresAt: ISOTime
}

export interface Ceremony<T> {
  ceremonyId: string
  publicKey: T
}

export interface NewKey {
  key: Key
  secret: string
}

// First-run LAN page (ADMIN-API §5).
export interface FirstRunState {
  step: 'pin' | 'relay' | 'connecting' | 'link'
  homeName?: string
  relay?: RelayStatus
}

export interface SetupLink {
  url: string
  expiresAt: ISOTime
}
