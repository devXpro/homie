// MSW handlers for everything the admin calls: the gatekeeper's API (/api/me, /api/auth/*, /api/account/*, the
// consent API) and Homie's own (/api/home/*: plugins with a fake homie-tv, calls with progress, jobs, the activity
// log, settings, the tunnel, the event stream by SSE and by long-polling) and the LAN first-run API (/api/firstrun/*).
// They follow the rules the SPA can see: sessions, the X-Homie header, owners and the superadmin, step-up, invites
// with a term and devices, co-owners, the error form {"error","message"}.

import type { AuditEntry, Connection, ConsentTxn, Me } from '@devxpro/mcp-gatekeeper-ui'
import { Validator, type Schema } from '@cfworker/json-schema'
import { delay, http, HttpResponse, sse } from 'msw'
import type {
  ActivityItem,
  Bootstrap,
  Call,
  CallProgress,
  FirstRunState,
  HomeAccount,
  Job,
  JobKind,
  Lang,
  Overview,
  PluginDetail,
  PluginPreview,
  PluginSummary,
  TunnelStatus,
  Via,
} from '@/lib/types'
import {
  inventoryItems,
  iso,
  isoIn,
  livingRoom,
  LOGIN_URL,
  QUICK_URL,
  QUICK_URL_NEXT,
  SCENARIOS,
  seed,
  tunnelAt,
  tvInfo,
  type MockPerson,
  type MockPlugin,
  type MockState,
  type MockTV,
  type Scenario,
} from './data'

// ---------------------------------------------------------------- state and configuration

interface MockConfig {
  scenario: Scenario
  lang: Lang
  slow: boolean
}

const CONFIG_KEY = 'homie.mock'
const SESSION_KEY = 'homie.mock.session'

function readConfig(): MockConfig {
  const q = new URLSearchParams(window.location.search)
  let saved: Partial<MockConfig>
  try {
    saved = JSON.parse(sessionStorage.getItem(CONFIG_KEY) ?? '{}') as Partial<MockConfig>
  } catch {
    saved = {}
  }
  const name = q.get('mock') ?? saved.scenario ?? 'default'
  const scenario = (SCENARIOS as readonly string[]).includes(name) ? (name as Scenario) : 'default'
  const lang: Lang = (q.get('lang') ?? saved.lang) === 'en' ? 'en' : 'ru'
  const slow = q.has('slow') ? q.get('slow') !== '0' : (saved.slow ?? false)
  if (q.has('mock') && q.get('mock') !== saved.scenario) sessionStorage.removeItem(SESSION_KEY)
  const cfg = { scenario, lang, slow }
  sessionStorage.setItem(CONFIG_KEY, JSON.stringify(cfg))
  return cfg
}

let db: MockState = seed('default', 'ru', false)

/** configureFromUrl seeds a scenario from `?mock=` (and `?lang=`, `?slow=1`); a reload keeps the signed-in person. */
export function configureFromUrl() {
  const cfg = readConfig()
  db = seed(cfg.scenario, cfg.lang, cfg.slow)
  const saved = sessionStorage.getItem(SESSION_KEY)
  if (saved !== null) db.session = saved === '' ? null : saved
}

function setSession(id: string | null) {
  db.session = id
  sessionStorage.setItem(SESSION_KEY, id ?? '')
}

const RUN = Math.random().toString(36).slice(2, 8)
const rand = (n = 8) => Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => 'abcdefghijklmnopqrstuvwxyz234567'[b % 32]).join('')
const b64url = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const randomB64 = (n: number) => b64url(crypto.getRandomValues(new Uint8Array(n)))
const ms = (fast: number) => (db.slow ? fast * 6 : fast)
const sleep = (t: number) => new Promise((r) => setTimeout(r, t))
const nowIso = () => new Date().toISOString()
const STEP_UP_MS = 5 * 60_000

// ---------------------------------------------------------------- events: SSE clients, the ring, long-polls

interface SseClient {
  send: (m: { id?: string; event: string; data: unknown }) => void
  logs?: string
}

const clients = new Set<SseClient>()
const ring: { seq: number; type: string; data: unknown }[] = []
const waiters = new Set<() => void>()

function emit(type: string, data: unknown) {
  const seq = ++db.seq
  ring.push({ seq, type, data })
  if (ring.length > 500) ring.shift()
  for (const c of [...clients]) {
    try {
      c.send({ id: `${RUN}-${seq}`, event: type, data })
    } catch {
      clients.delete(c)
    }
  }
  for (const w of [...waiters]) w()
}

/** The test hooks of window.__homieMock (mock builds only). */
export function setOffline(v: boolean) {
  db.offline = v
}

/** finishLogin completes the Cloudflare login of the first-run connect, as if the person had signed in. */
export function finishLogin() {
  const c = db.firstrun.connect
  if (c && c.state === 'running' && c.step === 'login') void runConnect(c.hostname, true)
}

/** forgetPin ends the first-run page's PIN cookie, as its 30 minutes running out would. */
export function forgetPin() {
  db.firstrun.unlocked = false
}

/** restartQuick gives a quick tunnel a new address, as a restart of homed would (the LAN page then reopens). */
export function restartQuick() {
  db.tunnel = tunnelAt('quick', QUICK_URL_NEXT, 0)
  emit('tunnel', db.tunnel)
}

// ---------------------------------------------------------------- answers

function fail(status: number, error: string, message: string, extra: Record<string, unknown> = {}, headers?: Record<string, string>) {
  return HttpResponse.json({ error, message, ...extra }, { status, headers })
}

const noContent = () => new HttpResponse(null, { status: 204 })

/** Cloudflare's own page for a tunnel whose origin is down (error 1033 / 530): HTML, not JSON. */
function offlinePage() {
  return new HttpResponse('<!doctype html><title>Cloudflare Tunnel error</title><h1>Error 1033</h1>', {
    status: 530,
    headers: { 'Content-Type': 'text/html; charset=utf-8' },
  })
}

async function body<T>(request: Request): Promise<T> {
  try {
    return (await request.json()) as T
  } catch {
    return {} as T
  }
}

function viewer(): MockPerson | undefined {
  return db.session ? db.people.find((p) => p.id === db.session) : undefined
}

const isActive = (p: MockPerson) => !p.access_until || new Date(p.access_until).getTime() > Date.now()

type Guard = Response | undefined

/**
 * guard applies the gatekeeper's and homed's rules: the CSRF header on writes, a session, the superadmin (`superadmin`
 * names what only they may do; checked before the step-up, as homed does), an owner, step-up.
 */
function guard(request: Request, need: { owner?: boolean; superadmin?: string; stepUp?: boolean; session?: boolean } = {}): Guard {
  if (request.method !== 'GET' && request.headers.get('X-Homie') !== '1') return fail(403, 'forbidden', 'missing X-Homie header')
  if (need.session === false) return undefined
  const v = viewer()
  if (!v) return fail(401, 'no_session', 'no session')
  if (!isActive(v)) return fail(401, 'no_session', 'your access has ended')
  if (need.superadmin && !v.superadmin) return fail(403, 'forbidden', `only the home's superadmin may do this: ${need.superadmin}`)
  if (need.owner && v.role !== 'owner') return fail(403, 'forbidden', 'owner only')
  if (need.stepUp && db.stepUpUntil < Date.now()) return fail(403, 'step_up_required', 'this needs a fresh passkey')
  return undefined
}

function audit(action: string, extra: { target?: string; detail?: Record<string, string> } = {}) {
  db.audit.unshift({ id: `a_${rand()}`, at: nowIso(), actor: db.session ?? undefined, action, ip: '192.0.2.10', ...extra })
}

function me(v: MockPerson): Me {
  return {
    person: { id: v.id, name: v.name, role: v.role, superadmin: v.superadmin },
    account: { id: 'acc_home', name: db.home.name },
    step_up_until: db.stepUpUntil > Date.now() ? new Date(db.stepUpUntil).toISOString() : null,
  }
}

/**
 * account is GET /api/account: a member sees the term and devices of their own row only (K5). homed adds the home as
 * the one resource, the devices the viewer may use and the tunnel's mode (docs/ADMIN-API.md §5).
 */
function account(v: MockPerson): HomeAccount {
  const owner = v.role === 'owner'
  return {
    id: 'acc_home',
    name: db.home.name,
    resources: [{ id: 'home', name: db.home.name, description: 'Devices at home', mcp_url: db.tunnel.mcpUrl ?? `${window.location.origin}/mcp`, enabled: true }],
    devices: allowedFor(v).map((p) => ({ id: p.summary.id, title: p.summary.title, permission: `plugin:${p.summary.id}` })),
    tunnel: { mode: db.tunnel.mode },
    members: db.people.map((p) => {
      const own = owner || p.id === v.id
      return {
        id: p.id,
        name: p.name,
        role: p.role,
        joined_at: p.joined_at,
        you: p.id === v.id,
        ...(own ? { access_until: p.role === 'owner' ? null : (p.access_until ?? null), permissions: p.role === 'owner' ? ['all'] : (p.permissions ?? []), active: isActive(p) } : {}),
      }
    }),
    invites: owner
      ? db.invites
          .filter((i) => i.kind !== 'setup')
          .map((i) => ({ id: i.id, name: i.name, expires_at: i.expires_at, ...(i.role ? { role: i.role } : {}), access_until: i.access_until ?? null, permissions: i.permissions ?? null }))
      : [],
  }
}

/** The permission ids the catalog knows: "all" and one per plugin. */
function knownPermission(id: string) {
  return id === 'all' || db.plugins.some((p) => `plugin:${p.summary.id}` === id)
}

/** allowedFor: which plugins a person may use (owners all; members by their permissions). */
function allowedFor(v: MockPerson) {
  if (v.role === 'owner' || (v.permissions ?? []).includes('all')) return db.plugins
  return db.plugins.filter((p) => (v.permissions ?? []).includes(`plugin:${p.summary.id}`))
}

function connections(v: MockPerson): Connection[] {
  return db.grants
    .filter((g) => v.role === 'owner' || g.person === v.id)
    .filter((g) => db.people.some((p) => p.id === g.person))
    .map((g) => ({
      grant_id: g.grant_id,
      resource: 'home',
      assistant: g.assistant,
      client_name: g.client_name,
      person: { id: g.person, name: db.people.find((p) => p.id === g.person)?.name ?? '' },
      created_at: g.created_at,
      last_used_at: g.last_used_at ?? null,
    }))
}

function plugin(id: string) {
  return db.plugins.find((p) => p.summary.id === id)
}

function summary(p: MockPlugin): PluginSummary {
  const s = { ...p.summary }
  if (p.summary.inventoryTool) s.devices = p.tvs.length
  return s
}

function detail(p: MockPlugin): PluginDetail {
  const { tools: _counts, ...rest } = summary(p)
  return { ...rest, instructions: p.instructions, command: [`$HOME/.local/share/homie/plugins/${p.summary.id}/bin/homie-${p.summary.id}`, 'serve'], tools: p.tools }
}

function emitPlugin(p: MockPlugin) {
  emit('plugin', summary(p))
}

function logActivity(item: Omit<ActivityItem, 'id' | 'at'>) {
  const a: ActivityItem = { id: String(2000 + db.seq + Math.floor(Math.random() * 1000)), at: nowIso(), ...item }
  db.activity.unshift(a)
  emit('activity', a)
  return a
}

const REDACT = /(token|key|secret|password|pin|code)/i
function redact(args: Record<string, unknown>) {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(args)) out[k] = REDACT.test(k) ? '[redacted]' : v
  return out
}

function bootstrap(): Bootstrap {
  return { homeName: db.home.name, language: db.home.language, ownerReady: db.people.some((p) => p.role === 'owner'), tunnel: { mode: db.tunnel.mode } }
}

/** overview orders the attention by severity, as homed does (docs/ADMIN-API.md §3). */
function overview(): Overview {
  const attention: Overview['attention'] = []
  if (db.attentionReset) attention.push({ kind: 'access_reset', at: iso(40 * 60_000), peer: '/usr/local/bin/homed (pid 4242)' })
  if (db.tunnel.state === 'failing') attention.push({ kind: 'tunnel_offline' })
  else if (db.tunnel.state === 'probing' && db.tunnel.lastError) attention.push({ kind: 'tunnel_unreachable' })
  for (const p of db.plugins) if (p.summary.state === 'failing') attention.push({ kind: 'plugin_failing', target: p.summary.id })
  if (db.system.update) attention.push({ kind: 'hub_update', version: db.system.update.latest })
  for (const p of db.plugins) if (p.summary.update) attention.push({ kind: 'plugin_update', target: p.summary.id, version: p.summary.update.latest })
  if (db.tunnel.mode === 'quick') attention.push({ kind: 'tunnel_quick' })
  const recent = db.activity.filter((a) => a.via === 'chatgpt' || a.via === 'claude').slice(0, 5)
  return { home: { name: db.home.name }, tunnel: db.tunnel, plugins: db.plugins.map(summary), attention, recent }
}

// ---------------------------------------------------------------- the fake homie-tv

interface Ctx {
  progress: (message: string, ru: string, progress?: number, total?: number) => void
  sleep: (t: number) => Promise<void>
}

interface Outcome {
  ok: boolean
  text: string
  structured?: unknown
}

function pick(p: MockPlugin, ref: unknown): MockTV | Outcome {
  const r = typeof ref === 'string' ? ref.trim() : ''
  if (p.tvs.length === 0) return { ok: false, text: 'No TV is set up yet. Add one in the Homie admin.' }
  if (!r) {
    if (p.tvs.length === 1) return p.tvs[0]
    return { ok: false, text: `Which TV? Set tv to one of: ${p.tvs.map((t) => `"${t.id}" (${t.name})`).join(', ')}.` }
  }
  const t = p.tvs.find((x) => x.id === r) ?? p.tvs.find((x) => x.name.toLowerCase() === r.toLowerCase())
  return t ?? { ok: false, text: `There is no TV "${r}". Set tv to one of: ${p.tvs.map((x) => `"${x.id}" (${x.name})`).join(', ')}.` }
}

const isTV = (x: MockTV | Outcome): x is MockTV => 'host' in x

async function runTv(name: string, args: Record<string, unknown>, p: MockPlugin, ctx: Ctx): Promise<Outcome> {
  const home = livingRoom(db.lang)
  switch (name) {
    case 'tv_list':
      return { ok: true, text: p.tvs.map((t) => `${t.name} (${t.id}) is ${t.power}.`).join('\n') || 'No TV is set up yet.', structured: { items: inventoryItems(p) } }
    case 'tv_power_on': {
      const t = pick(p, args.tv)
      if (!isTV(t)) return t
      await ctx.sleep(ms(2200))
      if (t.power === 'offline') return { ok: false, text: `${t.name} did not wake up. Check that Turn on via Wi-Fi is enabled in the TV settings, or turn it on with the remote.` }
      t.power = 'on'
      return { ok: true, text: `${t.name} is on.`, structured: { tv: t.id, state: 'on' } }
    }
    case 'tv_power_off': {
      const t = pick(p, args.tv)
      if (!isTV(t)) return t
      await ctx.sleep(ms(700))
      if (!t.paired) return { ok: false, text: `${t.name} is not paired with Homie yet. Pair it in the Homie admin, then try again.` }
      t.power = 'off'
      return { ok: true, text: `${t.name} is off.`, structured: { tv: t.id, state: 'off' } }
    }
    case 'tv_set_volume': {
      const t = pick(p, args.tv)
      if (!isTV(t)) return t
      await ctx.sleep(ms(600))
      return { ok: true, text: `${t.name} volume is ${String(args.percent)}%.`, structured: { tv: t.id, volume: args.percent } }
    }
    case 'tv_set_input': {
      const t = pick(p, args.tv)
      if (!isTV(t)) return t
      await ctx.sleep(ms(800))
      const input = String(args.input)
      return { ok: true, text: `${t.name} is on ${input === 'tv' ? 'live TV' : `HDMI ${input.slice(4)}`}.`, structured: { tv: t.id, input } }
    }
    case 'tv_discover': {
      ctx.progress('Looking for TVs on the home network', 'Ищу телевизоры в домашней сети')
      await ctx.sleep(ms(2600))
      const added = p.tvs.find((t) => t.host === home.host)?.id
      const found = { host: home.host, name: '[TV] webOS TV', model: home.model, mac: home.mac, ...(added ? { added } : {}) }
      return { ok: true, text: `Found 1 TV(s): [TV] webOS TV at ${home.host} (${home.model})${added ? `, already added as ${added}` : ''}.`, structured: { tvs: [found] } }
    }
    case 'tv_add': {
      const host = String(args.host ?? '').trim()
      await ctx.sleep(ms(1200))
      const dup = p.tvs.find((t) => t.host === host)
      if (dup) return { ok: false, text: `The TV at ${host} is already added as "${dup.id}" (${dup.name}).` }
      if (host !== home.host) return { ok: false, text: `No webOS TV answered at ${host}. Turn the TV on, check the address and try again.` }
      let id = typeof args.id === 'string' && args.id ? args.id : 'tv'
      for (let n = 2; !args.id && p.tvs.some((t) => t.id === id); n++) id = `tv-${n}`
      const name = typeof args.name === 'string' && args.name.trim() ? args.name.trim() : 'webOS TV'
      const t: MockTV = { id, name, host, mac: home.mac, model: home.model, paired: false, power: 'on' }
      p.tvs.push(t)
      emitPlugin(p)
      return { ok: true, text: `Added ${t.name} (id ${t.id}) at ${t.host}. Pair it next: turn it on and run tv_pair.`, structured: { tv: tvInfo(t) } }
    }
    case 'tv_pair': {
      const t = pick(p, args.tv)
      if (!isTV(t)) return t
      await ctx.sleep(ms(900))
      if (t.power === 'offline') return { ok: false, text: `${t.name} is off or not on the network. Turn it on, then run tv_pair again.` }
      ctx.progress('Press OK on the TV remote', 'Нажмите OK на пульте телевизора')
      await ctx.sleep(ms(5200))
      t.paired = true
      emitPlugin(p)
      return { ok: true, text: `Paired with ${t.name}.`, structured: { tv: tvInfo(t) } }
    }
    case 'tv_remove': {
      const t = pick(p, args.tv)
      if (!isTV(t)) return t
      await ctx.sleep(ms(300))
      p.tvs = p.tvs.filter((x) => x !== t)
      emitPlugin(p)
      return { ok: true, text: `Removed ${t.name}.`, structured: { removed: t.id } }
    }
    case 'tv_rename': {
      const t = pick(p, args.tv)
      if (!isTV(t)) return t
      const nm = String(args.name ?? '').trim()
      await ctx.sleep(ms(300))
      if (!nm) return { ok: false, text: 'The new name is empty.' }
      const old = t.name
      t.name = nm
      return { ok: true, text: `Renamed ${old} to ${t.name}.`, structured: { tv: tvInfo(t) } }
    }
  }
  return { ok: false, text: `The mock has no ${name}.` }
}

const cancelled = new Set<string>()

function startCall(p: MockPlugin, toolName: string, args: Record<string, unknown>, v: MockPerson): Call {
  const tool = p.tools.find((t) => t.name === toolName) ?? p.tools[0]
  const call: Call = {
    id: `c_${rand(10)}`,
    tool: tool.name,
    pluginId: p.summary.id,
    title: tool.title,
    args: redact(args),
    state: 'running',
    startedAt: nowIso(),
    timeoutMs: tool.timeoutMs,
  }
  db.calls.set(call.id, call)
  emit('call', call)
  const update = (patch: Partial<Call>) => {
    if (call.state !== 'running') return
    Object.assign(call, patch)
    emit('call', { ...call })
  }
  const ctx: Ctx = {
    progress: (message, ru, progress = 1, total) => {
      const pr: CallProgress = { progress, message, messageI18n: { ru } }
      if (total) pr.total = total
      update({ progress: pr })
    },
    sleep: async (t) => {
      const end = Date.now() + t
      while (Date.now() < end) {
        if (cancelled.has(call.id)) throw new Error('cancelled')
        await sleep(Math.min(100, end - Date.now()))
      }
    },
  }
  void (async () => {
    try {
      const out = await runTv(toolName, args, p, ctx)
      if (call.state !== 'running') return
      const ended = new Date()
      Object.assign(call, {
        state: out.ok ? 'ok' : 'failed',
        endedAt: ended.toISOString(),
        durationMs: ended.getTime() - new Date(call.startedAt).getTime(),
        result: out.structured === undefined ? { text: out.text } : { text: out.text, structured: out.structured },
      })
      emit('call', { ...call })
      logActivity({ person: { id: v.id, name: v.name }, via: 'admin', tool: tool.name, toolTitle: tool.title, pluginId: p.summary.id, args: call.args, ok: out.ok, error: out.ok ? undefined : out.text, durationMs: call.durationMs ?? 0 })
    } catch {
      // cancelled: DELETE /calls/{id} already reported it
    }
  })()
  return { ...call }
}

// ---------------------------------------------------------------- jobs

const STEPS: Record<string, string[]> = {
  'plugin.install': ['resolve', 'download', 'verify', 'unpack', 'start'],
  'plugin.update': ['resolve', 'download', 'verify', 'unpack', 'switch', 'start'],
  'plugin.remove': ['stop', 'remove'],
  'hub.update': ['resolve', 'download', 'verify', 'replace', 'restart'],
}

function startJob(kind: JobKind, target: string | undefined, message: string | undefined, finish: () => void): Job {
  const job: Job = { id: `j_${rand(8)}`, kind, target, state: 'running', steps: STEPS[kind], step: STEPS[kind][0], startedAt: nowIso() }
  if (message) job.message = message
  db.jobs.set(job.id, job)
  emit('job', { ...job })
  void (async () => {
    for (const step of job.steps) {
      job.step = step
      delete job.progress
      emit('job', { ...job })
      if (step === 'download') {
        const total = 5_300_000
        for (let done = 0; done <= total; done += 1_060_000) {
          job.progress = { done: Math.min(done, total), total }
          emit('job', { ...job })
          await sleep(ms(260))
        }
      } else if (step === 'restart') {
        await sleep(ms(900))
        finish()
        emit('hello', { version: db.system.version, serverTime: nowIso() })
        return
      } else {
        await sleep(ms(step === 'start' ? 700 : 450))
      }
    }
    finish()
    job.state = 'ok'
    job.endedAt = nowIso()
    delete job.progress
    emit('job', { ...job })
  })()
  return { ...job }
}

function previewOf(repo: string): PluginPreview | undefined {
  const installed = (id: string) => {
    const p = plugin(id)
    return p ? { installed: { version: p.summary.version } } : {}
  }
  switch (repo.toLowerCase()) {
    case 'devxpro/homie-tv':
      return { repo: 'devXpro/homie-tv', id: 'tv', title: 'TV', description: 'webOS TVs: power, volume, inputs.', version: '0.2.0', publishedAt: iso(3 * 86_400_000), homepage: 'https://github.com/devXpro/homie-tv', minHubVersion: '0.2.0', compatible: true, assetFound: true, ...installed('tv') }
    case 'someone/homie-kettle':
      return { repo: 'someone/homie-kettle', id: 'kettle', title: 'Kettle', description: 'Smart kettles: boil, keep warm, temperature.', version: '1.4.0', publishedAt: iso(20 * 86_400_000), homepage: 'https://github.com/someone/homie-kettle', minHubVersion: '0.4.0', compatible: false, assetFound: true }
    case 'someone/homie-vacuum':
      return { repo: 'someone/homie-vacuum', id: 'vacuum', title: 'Vacuum', description: 'Robot vacuums: start, stop, clean a room.', version: '0.1.0', publishedAt: iso(40 * 86_400_000), minHubVersion: '0.2.0', compatible: true, assetFound: false }
  }
  return undefined
}

// ---------------------------------------------------------------- the tunnel

function setTunnel(next: TunnelStatus) {
  db.tunnel = next
  emit('tunnel', next)
}

/** bringUp walks a tunnel from starting through the outside check to connected, as the supervisor reports it. */
async function bringUp(mode: 'named' | 'quick', url: string | undefined) {
  // A quick tunnel learns its address from cloudflared once it runs: none while starting.
  setTunnel({ mode, state: 'starting', ...(mode === 'named' && url ? { url, hostname: new URL(url).host } : {}), since: nowIso() })
  await sleep(ms(900))
  const u = url ?? QUICK_URL
  setTunnel({ mode, state: 'probing', url: u, mcpUrl: `${u}/mcp`, hostname: new URL(u).host, since: nowIso() })
  await sleep(ms(1100))
  setTunnel(tunnelAt(mode, u, 0))
}

/** runConnect is the named connect of the first-run page: login (waits for the person), tunnel, DNS, config. */
let connects = 0

async function runConnect(hostname: string, loggedIn = false, overwrite = false) {
  const startedAt = nowIso()
  if (!loggedIn) {
    // A newer connect (another address, a retry) makes this one's later steps moot.
    const gen = ++connects
    overwrites.set(hostname, overwrite)
    db.firstrun.connect = { hostname, state: 'running', step: 'login', startedAt }
    await sleep(ms(500))
    if (gen !== connects) return
    db.firstrun.connect = { hostname, state: 'running', step: 'login', loginUrl: LOGIN_URL, startedAt }
    // Without a test driving it (?manual), the "person" signs in after a while.
    if (!new URLSearchParams(window.location.search).has('manual')) {
      await sleep(ms(6000))
      if (gen === connects && db.firstrun.connect.step === 'login' && db.firstrun.connect.state === 'running') void runConnect(hostname, true)
    }
    return
  }
  ++connects
  const started = db.firstrun.connect?.startedAt ?? startedAt
  for (const step of ['tunnel', 'dns', 'config'] as const) {
    db.firstrun.connect = { hostname, state: 'running', step, startedAt: started }
    await sleep(ms(700))
    // A taken address stops at DNS, as `cloudflared tunnel route dns` does without --overwrite-dns; homed names it
    // with the code dns_exists, the text stays the gatekeeper's.
    if (step === 'dns' && hostname.startsWith('taken.') && !overwrites.get(hostname)) {
      const error = `${hostname} already has a DNS record; run again with --overwrite-dns to point it at this tunnel`
      db.firstrun.connect = { hostname, state: 'failed', step: 'dns', code: 'dns_exists', error, startedAt: started, endedAt: nowIso() }
      return
    }
  }
  db.firstrun.connect = { hostname, state: 'ok', step: 'config', startedAt: started, endedAt: nowIso() }
  await bringUp('named', `https://${hostname}`)
}

/** Whether a connect may replace an existing DNS record (overwriteDns), by address. */
const overwrites = new Map<string, boolean>()

/** firstrunState follows homed (docs/ADMIN-API.md §7): a running connect → connecting; a ready tunnel → link; none → tunnel. */
function firstrunState(): FirstRunState {
  if (!db.firstrun.unlocked) return { step: 'pin' }
  const t = db.tunnel
  const job = db.firstrun.connect
  const base = { homeName: db.home.name, ownerExists: db.firstrun.ownerExists, tunnel: t, ...(job ? { connect: job } : {}) }
  if (job && job.state === 'running') return { step: 'connecting', ...base }
  if (t.mode !== 'off' && t.url && t.state === 'connected') return { step: 'link', ...base }
  if (t.mode === 'off') return { step: 'tunnel', ...base }
  return { step: 'connecting', ...base }
}

// ---------------------------------------------------------------- ceremonies

/** The names a registration sent to begin, applied at finish. */
let pending: { token: string; name: string; account_name?: string } | undefined

function creationOptions(name: string) {
  return {
    rp: { name: 'Homie', id: window.location.hostname },
    user: { id: randomB64(16), name: name || 'person', displayName: name || 'person' },
    challenge: randomB64(32),
    pubKeyCredParams: [
      { type: 'public-key', alg: -7 },
      { type: 'public-key', alg: -257 },
    ],
    timeout: 60000,
    authenticatorSelection: { residentKey: 'required', requireResidentKey: true, userVerification: 'required' },
    attestation: 'none',
  }
}

function requestOptions() {
  return { challenge: randomB64(32), rpId: window.location.hostname, timeout: 60000, userVerification: 'required', allowCredentials: [] }
}

/** inviteFor finds a usable invite: the seeded tokens, or (setup) any other token in a home without an owner. */
function inviteFor(token: string | undefined) {
  if (!token || token === 'invalid' || token === 'expired' || token === 'used') return undefined
  const known = db.invites.find((i) => i.token === token && new Date(i.expires_at).getTime() > Date.now())
  if (known) return known
  if (!db.people.some((p) => p.role === 'owner')) {
    const inv = { id: `inv_${rand()}`, token, kind: 'setup' as const, name: '', expires_at: isoIn(3 * 60_000) }
    db.invites.push(inv)
    return inv
  }
  return undefined
}

// ---------------------------------------------------------------- handlers

const H = '/api/home'
const F = '/api/firstrun'

export const handlers = [
  // The home is offline: Cloudflare's own HTML page for every call.
  http.all('/api/*', () => (db.offline ? offlinePage() : undefined)),

  // ------------------------------------------------ the gatekeeper: sign-in, setup, join
  http.get('/api/me', async () => {
    await delay(ms(120))
    const v = viewer()
    if (!v) return fail(401, 'no_session', 'no session')
    if (!isActive(v)) return fail(401, 'no_session', 'your access has ended')
    return HttpResponse.json(me(v))
  }),

  http.post('/api/auth/token', async ({ request }) => {
    await delay(ms(400))
    const g = guard(request, { session: false })
    if (g) return g
    const { token } = await body<{ token?: string }>(request)
    const inv = inviteFor(token)
    if (!inv) return fail(404, 'not_found', 'invalid or expired token')
    if (inv.kind === 'setup') return HttpResponse.json({ kind: 'setup', name: '', account_name: db.home.name })
    const coOwner = inv.role === 'owner'
    return HttpResponse.json({ kind: coOwner ? 'owner' : 'member', name: inv.name, account_name: db.home.name, ...(coOwner ? { co_owner: true } : {}) })
  }),

  http.post('/api/auth/register/begin', async ({ request }) => {
    await delay(ms(200))
    const g = guard(request, { session: false })
    if (g) return g
    const b = await body<{ token?: string; name?: string; account_name?: string }>(request)
    const inv = inviteFor(b.token)
    if (!inv) return fail(404, 'not_found', 'invalid or expired token')
    if (!b.name?.trim()) return fail(400, 'invalid', 'name is required')
    if (inv.kind === 'setup' && !b.account_name?.trim()) return fail(400, 'invalid', 'account_name is required')
    pending = { token: inv.token, name: b.name.trim(), account_name: b.account_name?.trim() }
    return HttpResponse.json({ options: creationOptions(b.name.trim()) })
  }),

  http.post('/api/auth/register/finish', async ({ request }) => {
    await delay(ms(300))
    const g = guard(request, { session: false })
    if (g) return g
    const p = pending
    const inv = p && db.invites.find((i) => i.token === p.token)
    if (!p || !inv) return fail(404, 'not_found', 'invalid or expired token')
    if (inv.kind === 'setup' && p.account_name) db.home.name = p.account_name
    const person: MockPerson = {
      id: `per_${rand()}`,
      name: p.name,
      role: inv.kind === 'member' && inv.role !== 'owner' ? 'member' : 'owner',
      superadmin: inv.kind === 'setup',
      joined_at: nowIso(),
      access_until: inv.access_until ?? null,
      permissions: inv.role === 'owner' || inv.kind === 'setup' ? ['all'] : (inv.permissions ?? ['all']),
    }
    db.people.push(person)
    db.passkeys.push({ id: `pk_${rand()}`, person: person.id, name: 'Mac', created_at: nowIso(), last_used_at: nowIso() })
    db.invites = db.invites.filter((i) => i !== inv)
    pending = undefined
    setSession(person.id)
    db.stepUpUntil = Date.now() + STEP_UP_MS
    audit(inv.kind === 'setup' ? 'auth.register' : 'auth.join')
    const m = me(person)
    return HttpResponse.json({ person: m.person, account: m.account })
  }),

  http.post('/api/auth/login/begin', async ({ request }) => {
    await delay(ms(150))
    const g = guard(request, { session: false })
    if (g) return g
    if (localStorage.getItem('homie.mock.loginRate') === '1') return fail(429, 'rate_limited', 'too many sign-ins', {}, { 'Retry-After': '30' })
    return HttpResponse.json({ options: requestOptions() })
  }),

  http.post('/api/auth/login/finish', async ({ request }) => {
    await delay(ms(250))
    const g = guard(request, { session: false })
    if (g) return g
    if (localStorage.getItem('homie.mock.loginFail') === '1') return fail(400, 'invalid', 'unknown passkey')
    // The mock does not verify signatures: any assertion signs in the scenario's person, else the first owner.
    const who = db.people.find((p) => p.id === (localStorage.getItem('homie.mock.as') ?? 'per_alice')) ?? db.people.find((p) => p.role === 'owner')
    if (!who) return fail(400, 'invalid', 'unknown passkey')
    if (!isActive(who)) return fail(403, 'access_ended', 'your access has ended')
    setSession(who.id)
    db.stepUpUntil = Date.now() + STEP_UP_MS
    audit('auth.login')
    const m = me(who)
    return HttpResponse.json({ person: m.person, account: m.account })
  }),

  http.post('/api/auth/stepup/begin', async ({ request }) => {
    await delay(ms(120))
    return guard(request) ?? HttpResponse.json({ options: requestOptions() })
  }),

  http.post('/api/auth/stepup/finish', async ({ request }) => {
    await delay(ms(200))
    const g = guard(request)
    if (g) return g
    db.stepUpUntil = Date.now() + STEP_UP_MS
    audit('auth.stepup')
    return HttpResponse.json({ step_up_until: new Date(db.stepUpUntil).toISOString() })
  }),

  http.post('/api/auth/logout', async ({ request }) => {
    await delay(ms(100))
    const g = guard(request, { session: false })
    if (g) return g
    setSession(null)
    db.stepUpUntil = 0
    for (const c of clients) clients.delete(c)
    return noContent()
  }),

  // ------------------------------------------------ the gatekeeper: passkeys
  http.get('/api/me/passkeys', async ({ request }) => {
    await delay(ms(150))
    return (
      guard(request) ??
      HttpResponse.json(db.passkeys.filter((k) => k.person === db.session).map((k) => ({ id: k.id, name: k.name, created_at: k.created_at, last_used_at: k.last_used_at ?? null })))
    )
  }),
  http.post('/api/me/passkeys/begin', async ({ request }) => {
    await delay(ms(150))
    return guard(request, { stepUp: true }) ?? HttpResponse.json({ options: creationOptions(viewer()?.name ?? '') })
  }),
  http.post('/api/me/passkeys/finish', async ({ request }) => {
    await delay(ms(250))
    const g = guard(request)
    if (g) return g
    const n = db.passkeys.filter((k) => k.person === db.session).length + 1
    const pk = { id: `pk_${rand()}`, person: db.session ?? '', name: `Passkey ${n}`, created_at: nowIso() }
    db.passkeys.push(pk)
    audit('auth.passkey.add')
    return HttpResponse.json({ id: pk.id, name: pk.name, created_at: pk.created_at, last_used_at: null }, { status: 201 })
  }),
  http.patch('/api/me/passkeys/:id', async ({ request, params }) => {
    await delay(ms(150))
    const g = guard(request)
    if (g) return g
    const pk = db.passkeys.find((k) => k.id === params.id && k.person === db.session)
    if (!pk) return fail(404, 'not_found', 'no such passkey')
    const { name } = await body<{ name?: string }>(request)
    if (!name?.trim()) return fail(400, 'invalid', 'name is required')
    pk.name = name.trim()
    return HttpResponse.json({ id: pk.id, name: pk.name, created_at: pk.created_at, last_used_at: pk.last_used_at ?? null })
  }),
  http.delete('/api/me/passkeys/:id', async ({ request, params }) => {
    await delay(ms(150))
    const g = guard(request, { stepUp: true })
    if (g) return g
    const mine = db.passkeys.filter((k) => k.person === db.session)
    if (!mine.some((k) => k.id === params.id)) return fail(404, 'not_found', 'no such passkey')
    if (mine.length <= 1) return fail(409, 'conflict', 'the last passkey cannot be deleted')
    db.passkeys = db.passkeys.filter((k) => k.id !== params.id)
    audit('auth.passkey.delete')
    return noContent()
  }),
  http.post('/api/me/sessions/revoke', async ({ request }) => {
    await delay(ms(200))
    const g = guard(request, { stepUp: true })
    if (g) return g
    audit('auth.sessions.revoke')
    return noContent()
  }),

  // ------------------------------------------------ the gatekeeper: consent (one transaction, ChatGPT asks)
  http.get('/api/oauth/txn/:txn', async ({ request, params }) => {
    await delay(ms(250))
    const g = guard(request)
    if (g) return g
    if (params.txn !== 'txn-chatgpt') return fail(404, 'not_found', 'no such transaction')
    const tools = db.plugins.flatMap((p) => p.tools.filter((x) => x.audience === 'assistant'))
    const out: ConsentTxn = {
      assistant: 'chatgpt',
      client_name: 'ChatGPT',
      redirect_host: 'chatgpt.com',
      resource: {
        id: 'home',
        name: db.home.name,
        description: 'Devices at home: TV.',
        tools: tools.map((x) => ({ name: x.name, title: x.i18n?.[db.lang]?.title ?? x.title, access: x.annotations?.readOnlyHint ? 'read' : 'write' })),
      },
      account: { id: 'acc_home', name: db.home.name },
      already_granted: false,
      started_from: 'assistant',
      client_kind: 'cimd',
    }
    return HttpResponse.json(out)
  }),
  http.post('/api/oauth/txn/:txn/:answer', async ({ request, params }) => {
    await delay(ms(300))
    const g = guard(request)
    if (g) return g
    const approve = params.answer === 'approve'
    return HttpResponse.json({ redirect_url: `https://chatgpt.com/connector/oauth/example?${approve ? 'code=c_example' : 'error=access_denied'}&state=example` })
  }),

  // ------------------------------------------------ the gatekeeper: the account (people, invites, connections)
  http.get('/api/account', async ({ request }) => {
    await delay(ms(200))
    return guard(request) ?? HttpResponse.json(account(viewer() as MockPerson))
  }),

  http.get('/api/account/permissions', async ({ request }) => {
    await delay(ms(120))
    return guard(request, { owner: true }) ?? HttpResponse.json(db.plugins.map((p) => ({ id: `plugin:${p.summary.id}`, title: p.summary.title, group: '' })))
  }),

  http.post('/api/account/invites', async ({ request }) => {
    await delay(ms(300))
    const g = guard(request, { owner: true, stepUp: true })
    if (g) return g
    const v = viewer() as MockPerson
    // Quick mode is the owner's alone: every restart changes the address and kills the guests' passkeys.
    if (db.tunnel.mode === 'quick') return fail(409, 'quick_mode', 'invites are off on a quick tunnel')
    const b = await body<{ name?: string; permissions?: string[]; access_until?: string; role?: string }>(request)
    if (!b.name?.trim()) return fail(400, 'invalid', 'name is required')
    if (b.role === 'owner') {
      if (!v.superadmin) return fail(403, 'forbidden', 'only the superadmin invites an owner')
      if (b.permissions || b.access_until) return fail(400, 'invalid', 'an owner has no term and no permissions')
    } else {
      if (!b.permissions?.length) return fail(400, 'invalid', 'permissions are required')
      if (!b.permissions.every(knownPermission)) return fail(400, 'invalid_permission', 'unknown permission')
      if (b.access_until) {
        const t = new Date(b.access_until).getTime()
        if (!(t > Date.now()) || t > Date.now() + 366 * 86_400_000) return fail(400, 'invalid', 'access_until must be in the future and at most a year away')
      }
    }
    const token = rand(24)
    const inv = {
      id: `inv_${rand()}`,
      token,
      kind: (b.role === 'owner' ? 'owner' : 'member') as 'owner' | 'member',
      name: b.name.trim(),
      expires_at: isoIn(24 * 3_600_000),
      ...(b.role === 'owner' ? { role: 'owner' as const } : { access_until: b.access_until ?? null, permissions: b.permissions }),
    }
    db.invites.push(inv)
    audit('account.invite.create', { target: inv.name })
    return HttpResponse.json({ id: inv.id, url: `${window.location.origin}/join#t=${token}`, expires_at: inv.expires_at })
  }),

  http.delete('/api/account/invites/:id', async ({ request, params }) => {
    await delay(ms(200))
    const g = guard(request, { owner: true })
    if (g) return g
    db.invites = db.invites.filter((i) => i.id !== params.id)
    audit('account.invite.revoke')
    return noContent()
  }),

  http.patch('/api/account/members/:id', async ({ request, params }) => {
    await delay(ms(250))
    const g = guard(request, { owner: true })
    if (g) return g
    const v = viewer() as MockPerson
    const m = db.people.find((p) => p.id === params.id)
    if (!m) return fail(404, 'not_found', 'no such member')
    if (m.role === 'owner' && !v.superadmin) return fail(403, 'forbidden', 'only the superadmin changes an owner')
    const b = await body<{ access_until?: string | null; permissions?: string[] }>(request)
    if (b.permissions && !b.permissions.every(knownPermission)) return fail(400, 'invalid_permission', 'unknown permission')
    // Widening: more permissions, or a later end, or no end. That needs a fresh passkey.
    const before = m.access_until ? new Date(m.access_until).getTime() : Infinity
    const after = b.access_until === undefined ? before : b.access_until === null ? Infinity : new Date(b.access_until).getTime()
    const morePerms = !!b.permissions && b.permissions.some((p) => !(m.permissions ?? []).includes(p) && !(m.permissions ?? []).includes('all'))
    if ((after > before || morePerms) && db.stepUpUntil < Date.now()) return fail(403, 'step_up_required', 'widening access needs a fresh passkey')
    if (b.access_until !== undefined) m.access_until = b.access_until
    if (b.permissions) m.permissions = b.permissions
    audit('account.member.update', { target: m.id })
    const row = account(v).members?.find((x) => x.id === m.id)
    return HttpResponse.json(row)
  }),

  http.delete('/api/account/members/:id', async ({ request, params }) => {
    await delay(ms(250))
    const g = guard(request, { owner: true, stepUp: true })
    if (g) return g
    const v = viewer() as MockPerson
    const m = db.people.find((p) => p.id === params.id)
    if (!m) return fail(404, 'not_found', 'no such member')
    if (m.role === 'owner' && !v.superadmin) return fail(403, 'forbidden', 'only the superadmin removes an owner')
    if (m.superadmin) return fail(409, 'conflict', 'the last superadmin cannot be removed')
    db.people = db.people.filter((p) => p !== m)
    db.grants = db.grants.filter((x) => x.person !== m.id)
    audit('account.member.remove', { target: m.id })
    return noContent()
  }),

  http.get('/api/account/connections', async ({ request }) => {
    await delay(ms(180))
    return guard(request) ?? HttpResponse.json(connections(viewer() as MockPerson))
  }),

  http.delete('/api/account/connections/:id', async ({ request, params }) => {
    await delay(ms(200))
    const g = guard(request)
    if (g) return g
    const v = viewer() as MockPerson
    const c = db.grants.find((x) => x.grant_id === params.id)
    if (!c || (v.role !== 'owner' && c.person !== v.id)) return fail(404, 'not_found', 'no such connection')
    db.grants = db.grants.filter((x) => x !== c)
    audit('oauth.grant.revoke', { target: c.grant_id })
    return noContent()
  }),

  http.get('/api/account/audit', async ({ request }) => {
    await delay(ms(200))
    const g = guard(request)
    if (g) return g
    const v = viewer() as MockPerson
    const names = new Map(db.people.map((p) => [p.id, p.name]))
    const list: AuditEntry[] = db.audit
      .filter((e) => v.role === 'owner' || e.actor === v.id)
      .map((e) => ({ ...e, actor_name: typeof e.actor === 'string' ? names.get(e.actor) : undefined }))
    return HttpResponse.json(list)
  }),

  // One home is one account: homed refuses to delete it (docs/ADMIN-API.md §5).
  http.delete('/api/account', async ({ request }) => {
    await delay(ms(200))
    return guard(request, { owner: true, stepUp: true }) ?? fail(403, 'forbidden', 'the home account cannot be deleted')
  }),

  // ------------------------------------------------ Homie: bootstrap, events
  http.get(`${H}/bootstrap`, async () => {
    await delay(ms(40))
    return HttpResponse.json(bootstrap())
  }),

  sse(`${H}/events`, ({ client, request }) => {
    if (db.offline || !viewer()) {
      client.error()
      return
    }
    const logs = new URL(request.url).searchParams.get('logs') ?? undefined
    const c: SseClient = { send: (m) => client.send(m as never), logs }
    clients.add(c)
    request.signal.addEventListener('abort', () => clients.delete(c))
    client.send({ retry: 3000 } as never)
    client.send({ id: `${RUN}-${db.seq}`, event: 'hello', data: { version: db.system.version, serverTime: nowIso() } } as never)
    if (logs) {
      const p = plugin(logs)
      const timer = setInterval(() => {
        if (!clients.has(c)) return clearInterval(timer)
        const line = `${nowIso()} DEBUG ssdp: no reply from 239.255.255.250:1900 within 3s`
        p?.logs.push(line)
        try {
          c.send({ id: `${RUN}-${++db.seq}`, event: 'plugin.log', data: { pluginId: logs, lines: [line] } })
        } catch {
          clearInterval(timer)
        }
      }, 4000)
    }
  }),

  // The long-poll of quick mode (docs/ADMIN-API.md §6): what came after the cursor `after`, waiting up to 20 s (the mock: 10 s)
  // for something new; an empty answer keeps the cursor. Cursors are "<run>-<seq>", like the SSE ids.
  http.get(`${H}/events/poll`, async ({ request }) => {
    const g = guard(request)
    if (g) return g
    const cursor = (seq: number) => `${RUN}-${seq}`
    const raw = new URL(request.url).searchParams.get('after')
    if (raw === null) return HttpResponse.json({ events: [{ event: 'hello', data: { version: db.system.version, serverTime: nowIso() } }], cursor: cursor(db.seq) })
    const [run, n] = raw.split('-')
    const after = Number(n)
    if (run !== RUN || !Number.isFinite(after) || (ring.length > 0 && after < ring[0].seq - 1)) {
      return HttpResponse.json({ events: [{ id: cursor(db.seq), event: 'resync', data: {} }], cursor: cursor(db.seq) })
    }
    if (db.seq <= after) {
      await new Promise<void>((resolve) => {
        const done = () => {
          waiters.delete(done)
          clearTimeout(timer)
          resolve()
        }
        const timer = setTimeout(done, 10_000)
        waiters.add(done)
        request.signal.addEventListener('abort', done)
      })
    }
    const events = ring.filter((e) => e.seq > after).map((e) => ({ id: cursor(e.seq), event: e.type, data: e.data }))
    return HttpResponse.json({ events, cursor: events.length > 0 ? cursor(db.seq) : raw })
  }),

  // ------------------------------------------------ Homie: the home
  http.get(`${H}/overview`, async ({ request }) => {
    await delay(ms(250))
    return guard(request, { owner: true }) ?? HttpResponse.json(overview())
  }),

  http.get(`${H}/settings`, async ({ request }) => {
    await delay(ms(120))
    return guard(request, { owner: true }) ?? HttpResponse.json({ homeName: db.home.name, language: db.home.language })
  }),

  http.patch(`${H}/settings`, async ({ request }) => {
    await delay(ms(250))
    const g = guard(request, { owner: true })
    if (g) return g
    const b = await body<{ homeName?: string; language?: Lang }>(request)
    if (b.homeName !== undefined) {
      const n = b.homeName.trim()
      if (n.length < 1 || n.length > 40) return fail(400, 'invalid', 'homeName: 1 to 40 characters', { fields: [{ path: '/homeName', code: 'maxLength', message: 'must be 1 to 40 characters' }] })
      db.home.name = n
    }
    if (b.language === 'ru' || b.language === 'en') db.home.language = b.language
    audit('settings.update', { detail: { keys: Object.keys(b).join(',') } })
    const s = { homeName: db.home.name, language: db.home.language }
    emit('settings', s)
    return HttpResponse.json(s)
  }),

  http.get(`${H}/tunnel`, async ({ request }) => {
    await delay(ms(120))
    return guard(request, { owner: true }) ?? HttpResponse.json(db.tunnel)
  }),

  // The self-check now: the unreachable scenario keeps failing it, a connected tunnel keeps passing.
  http.post(`${H}/tunnel/check`, async ({ request }) => {
    const g = guard(request, { owner: true })
    if (g) return g
    await delay(ms(900))
    emit('tunnel', db.tunnel)
    return HttpResponse.json(db.tunnel)
  }),

  http.post(`${H}/tunnel/reconnect`, async ({ request }) => {
    await delay(ms(300))
    const g = guard(request, { owner: true, superadmin: 'reconnect the tunnel', stepUp: true })
    if (g) return g
    if (db.tunnel.mode === 'off') return fail(409, 'conflict', 'no tunnel to reconnect')
    audit('tunnel.reconnect')
    const mode = db.tunnel.mode
    // A quick tunnel comes back with a new address; the passkeys of the old one stop working (docs/ADMIN-API.md §4).
    void bringUp(mode, mode === 'quick' ? QUICK_URL_NEXT : db.tunnel.url)
    return HttpResponse.json({ mode, state: 'starting', ...(mode === 'named' ? { url: db.tunnel.url, hostname: db.tunnel.hostname } : {}) }, { status: 202 })
  }),

  http.get(`${H}/system`, async ({ request }) => {
    await delay(ms(120))
    return guard(request, { owner: true }) ?? HttpResponse.json(db.system)
  }),

  http.post(`${H}/system/check-update`, async ({ request }) => {
    const g = guard(request, { owner: true })
    if (g) return g
    await delay(ms(900))
    return HttpResponse.json(db.system)
  }),

  http.post(`${H}/system/update`, async ({ request }) => {
    await delay(ms(200))
    const g = guard(request, { owner: true, superadmin: 'update the hub', stepUp: true })
    if (g) return g
    if (!db.system.update) return fail(409, 'conflict', 'no update')
    const latest = db.system.update.latest
    const job = startJob('hub.update', undefined, `Homie ${latest}`, () => {
      db.system = { ...db.system, version: latest, update: undefined }
      audit('hub.update', { target: latest })
    })
    return HttpResponse.json(job, { status: 202 })
  }),

  // ------------------------------------------------ Homie: plugins and tools
  http.get(`${H}/plugins/preview`, async ({ request }) => {
    const g = guard(request, { owner: true })
    if (g) return g
    await delay(ms(500))
    const repo = new URL(request.url).searchParams.get('repo') ?? ''
    const p = previewOf(repo)
    return p ? HttpResponse.json(p) : fail(404, 'not_found', 'no such repository or release')
  }),

  http.post(`${H}/plugins/check-updates`, async ({ request }) => {
    const g = guard(request, { owner: true })
    if (g) return g
    await delay(ms(700))
    return HttpResponse.json({ items: db.plugins.map(summary) })
  }),

  // Owners only: a member's devices come with GET /api/account (docs/ADMIN-API.md §4, §5).
  http.get(`${H}/plugins`, async ({ request }) => {
    await delay(ms(200))
    return guard(request, { owner: true }) ?? HttpResponse.json({ items: db.plugins.map(summary) })
  }),

  http.post(`${H}/plugins`, async ({ request }) => {
    await delay(ms(250))
    const g = guard(request, { owner: true, superadmin: 'install a plugin', stepUp: true })
    if (g) return g
    const b = await body<{ repo?: string; id?: string }>(request)
    const pre = previewOf(b.repo ?? '')
    if (!pre) return fail(404, 'not_found', 'no such repository')
    if (plugin(pre.id)) return fail(409, 'conflict', 'id taken')
    const job = startJob('plugin.install', pre.id, `${pre.repo} ${pre.version}`, () => {
      const p = newPlugin([], { since: nowIso(), devices: 0 })
      db.plugins.push(p)
      emitPlugin(p)
      audit('plugin.install', { target: pre.id })
    })
    return HttpResponse.json(job, { status: 202 })
  }),

  http.get(`${H}/plugins/:id`, async ({ request, params }) => {
    await delay(ms(200))
    const g = guard(request, { owner: true })
    if (g) return g
    const p = plugin(String(params.id))
    return p ? HttpResponse.json(detail(p)) : fail(404, 'not_found', 'no such plugin')
  }),

  http.patch(`${H}/plugins/:id`, async ({ request, params }) => {
    await delay(ms(250))
    const g = guard(request, { owner: true })
    if (g) return g
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    const b = await body<{ enabled?: boolean }>(request)
    if (typeof b.enabled === 'boolean') {
      p.summary.enabled = b.enabled
      p.summary.state = b.enabled ? 'up' : 'disabled'
      audit(b.enabled ? 'plugin.enable' : 'plugin.disable', { target: p.summary.id })
      emitPlugin(p)
    }
    return HttpResponse.json(summary(p))
  }),

  http.post(`${H}/plugins/:id/restart`, async ({ request, params }) => {
    await delay(ms(200))
    const g = guard(request, { owner: true })
    if (g) return g
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    p.summary.state = 'starting'
    emitPlugin(p)
    audit('plugin.restart', { target: p.summary.id })
    setTimeout(() => {
      p.summary.state = 'up'
      p.summary.lastError = undefined
      p.summary.since = nowIso()
      emitPlugin(p)
    }, ms(1500))
    return HttpResponse.json(summary(p), { status: 202 })
  }),

  http.post(`${H}/plugins/:id/update`, async ({ request, params }) => {
    await delay(ms(200))
    const g = guard(request, { owner: true, superadmin: 'update a plugin', stepUp: true })
    if (g) return g
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    if (!p.summary.update) return fail(409, 'conflict', 'no update')
    const latest = p.summary.update.latest
    const job = startJob('plugin.update', p.summary.id, `homie-${p.summary.id} ${latest}`, () => {
      p.summary = { ...p.summary, version: latest, update: undefined, state: 'up', lastError: undefined, since: nowIso() }
      emitPlugin(p)
      audit('plugin.update', { target: p.summary.id })
    })
    return HttpResponse.json(job, { status: 202 })
  }),

  http.delete(`${H}/plugins/:id`, async ({ request, params }) => {
    await delay(ms(200))
    const g = guard(request, { owner: true, superadmin: 'remove a plugin', stepUp: true })
    if (g) return g
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    const job = startJob('plugin.remove', p.summary.id, undefined, () => {
      db.plugins = db.plugins.filter((x) => x !== p)
      // As homed: the plugin's permission leaves everyone, and the open invites that give it are revoked.
      const perm = `plugin:${p.summary.id}`
      for (const person of db.people) if (person.permissions) person.permissions = person.permissions.filter((x) => x !== perm)
      db.invites = db.invites.filter((i) => !i.permissions?.includes(perm))
      emit('plugin.removed', { id: p.summary.id })
      audit('plugin.remove', { target: p.summary.id })
    })
    return HttpResponse.json(job, { status: 202 })
  }),

  http.get(`${H}/plugins/:id/inventory`, async ({ request, params }) => {
    await delay(ms(350))
    const g = guard(request, { owner: true })
    if (g) return g
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    if (!p.summary.inventoryTool) return fail(404, 'no_inventory', 'the plugin has no inventory tool')
    if (p.summary.state !== 'up') return fail(503, 'plugin_unavailable', 'the plugin is not running')
    return HttpResponse.json({ items: inventoryItems(p), fetchedAt: nowIso() })
  }),

  http.get(`${H}/plugins/:id/logs`, async ({ request, params }) => {
    await delay(ms(200))
    const g = guard(request, { owner: true })
    if (g) return g
    const p = plugin(String(params.id))
    return p ? HttpResponse.json({ lines: p.logs.slice(-1000) }) : fail(404, 'not_found', 'no such plugin')
  }),

  http.get(`${H}/tools`, async ({ request }) => {
    await delay(ms(150))
    const g = guard(request, { owner: true })
    if (g) return g
    return HttpResponse.json({ items: db.plugins.flatMap((p) => p.tools) })
  }),

  http.post(`${H}/tools/:name/calls`, async ({ request, params }) => {
    await delay(ms(200))
    const g = guard(request, { owner: true })
    if (g) return g
    const name = String(params.name)
    const p = db.plugins.find((x) => x.tools.some((t) => t.name === name))
    const tool = p?.tools.find((t) => t.name === name)
    if (!p || !tool) return fail(404, 'unknown_tool', 'no such tool')
    if (p.summary.state !== 'up') return fail(503, 'plugin_unavailable', 'the plugin is not running')
    const running = [...db.calls.values()].find((c) => c.tool === name && c.state === 'running')
    if (running) return fail(409, 'call_running', 'the tool already runs', { callId: running.id })
    const { args = {} } = await body<{ args?: Record<string, unknown> }>(request)
    const result = new Validator(tool.inputSchema as Schema, '2020-12', false).validate(args)
    if (!result.valid) {
      const fields = result.errors
        .filter((e) => e.keyword !== 'properties' && e.instanceLocation !== '#')
        .map((e) => ({ path: e.instanceLocation.replace(/^#/, ''), code: e.keyword, message: e.error }))
      return fail(400, 'invalid', 'invalid arguments', { fields })
    }
    return HttpResponse.json(startCall(p, name, args, viewer() as MockPerson), { status: 202 })
  }),

  http.get(`${H}/calls`, async ({ request }) => {
    await delay(ms(120))
    const g = guard(request, { owner: true })
    if (g) return g
    const state = new URL(request.url).searchParams.get('state')
    const items = [...db.calls.values()].filter((c) => !state || c.state === state).reverse()
    return HttpResponse.json({ items })
  }),

  http.get(`${H}/calls/:id`, async ({ request, params }) => {
    await delay(ms(80))
    const g = guard(request, { owner: true })
    if (g) return g
    const c = db.calls.get(String(params.id))
    return c ? HttpResponse.json(c) : fail(404, 'not_found', 'no such call')
  }),

  http.delete(`${H}/calls/:id`, async ({ request, params }) => {
    await delay(ms(150))
    const g = guard(request, { owner: true })
    if (g) return g
    const c = db.calls.get(String(params.id))
    if (!c) return fail(404, 'not_found', 'no such call')
    if (c.state === 'running') {
      cancelled.add(c.id)
      Object.assign(c, { state: 'cancelled', endedAt: nowIso(), durationMs: Date.now() - new Date(c.startedAt).getTime() })
      emit('call', { ...c })
    }
    return HttpResponse.json(c, { status: 202 })
  }),

  http.get(`${H}/jobs`, async ({ request }) => {
    await delay(ms(120))
    const g = guard(request, { owner: true })
    if (g) return g
    const state = new URL(request.url).searchParams.get('state')
    return HttpResponse.json({ items: [...db.jobs.values()].filter((j) => !state || j.state === state) })
  }),

  http.get(`${H}/jobs/:id`, async ({ request, params }) => {
    await delay(ms(80))
    const g = guard(request, { owner: true })
    if (g) return g
    const j = db.jobs.get(String(params.id))
    return j ? HttpResponse.json(j) : fail(404, 'not_found', 'no such job')
  }),

  // ------------------------------------------------ Homie: the activity log (a member sees their own calls)
  http.get(`${H}/activity`, async ({ request }) => {
    await delay(ms(250))
    const g = guard(request)
    if (g) return g
    const v = viewer() as MockPerson
    const q = new URL(request.url).searchParams
    const via = q.get('via')?.split(',') as Via[] | undefined
    const status = q.get('status')
    let list = db.activity.filter((a) => v.role === 'owner' || a.person?.id === v.id)
    if (via) list = list.filter((a) => via.includes(a.via))
    if (status === 'failed') list = list.filter((a) => !a.ok)
    if (status === 'ok') list = list.filter((a) => a.ok)
    if (q.get('person')) list = list.filter((a) => a.person?.id === q.get('person'))
    if (q.get('plugin')) list = list.filter((a) => a.pluginId === q.get('plugin'))
    const limit = Number(q.get('limit') ?? 50)
    const start = Number(q.get('cursor') ?? 0)
    const items = list.slice(start, start + limit)
    return HttpResponse.json({ items, ...(start + limit < list.length ? { nextCursor: String(start + limit) } : {}) })
  }),

  http.get(`${H}/activity/:id`, async ({ request, params }) => {
    await delay(ms(150))
    const g = guard(request)
    if (g) return g
    const v = viewer() as MockPerson
    const a = db.activity.find((x) => x.id === params.id && (v.role === 'owner' || x.person?.id === v.id))
    return a ? HttpResponse.json(a) : fail(404, 'not_found', 'no such entry')
  }),

  // ------------------------------------------------ the LAN first-run page (docs/ADMIN-API.md §7)
  http.all(`${F}/*`, ({ request }) => {
    if (request.method !== 'GET' && request.headers.get('X-Homie') !== '1') return fail(403, 'forbidden', 'missing X-Homie header')
    if (db.firstrun.done) return fail(410, 'setup_done', 'the home has an owner')
    if (db.firstrun.closed) return fail(410, 'setup_closed', 'the setup page is closed')
    return undefined
  }),

  http.get(`${F}/state`, async () => {
    await delay(ms(120))
    return HttpResponse.json(firstrunState())
  }),

  http.post(`${F}/pin`, async ({ request }) => {
    await delay(ms(250))
    const { pin } = await body<{ pin?: string }>(request)
    const digits = (pin ?? '').replace(/\D/g, '')
    if (digits === '00000000') return fail(429, 'rate_limited', 'this device: too many wrong PINs', {}, { 'Retry-After': '540' })
    if (digits !== '12345678') return fail(401, 'wrong_pin', 'wrong PIN')
    db.firstrun.unlocked = true
    return noContent()
  }),

  http.post(`${F}/tunnel`, async ({ request }) => {
    await delay(ms(250))
    if (!db.firstrun.unlocked) return fail(401, 'pin_required', 'enter the PIN first')
    const b = await body<{ mode?: string; hostname?: string; overwriteDns?: boolean }>(request)
    if (db.firstrun.connect?.state === 'running') return fail(409, 'conflict', 'a connect is running already')
    if (b.mode === 'quick') {
      if (db.tunnel.mode !== 'off') return fail(409, 'conflict', 'the home has a tunnel already')
      db.firstrun.connect = undefined
      // A quick tunnel is the owner's alone: homed revokes every open member and co-owner invite.
      db.invites = db.invites.filter((i) => i.kind === 'setup')
      void bringUp('quick', undefined)
      return HttpResponse.json({ tunnel: { mode: 'quick', state: 'starting' } }, { status: 202 })
    }
    const host = (b.hostname ?? '').trim().toLowerCase()
    if (b.mode !== 'named' || !/^[a-z0-9.-]+\.[a-z]{2,}$/.test(host)) {
      return fail(400, 'invalid_args', 'hostname: a DNS name', { fields: [{ path: '/hostname', code: 'format', message: 'a DNS name' }] })
    }
    if (db.tunnel.mode === 'named') return fail(409, 'conflict', 'a domain is connected: change it on the home computer')
    void runConnect(host, false, !!b.overwriteDns)
    return HttpResponse.json({ tunnel: db.tunnel, connect: { hostname: host, state: 'running', step: 'login', startedAt: nowIso() } }, { status: 202 })
  }),

  http.post(`${F}/link`, async () => {
    await delay(ms(300))
    if (!db.firstrun.unlocked) return fail(401, 'pin_required', 'enter the PIN first')
    if (db.tunnel.state !== 'connected' || !db.tunnel.url) return fail(409, 'tunnel_not_ready', 'the tunnel is not connected yet')
    // The link is for the public address: that is the passkey's relying party (docs/SECURITY.md, Setup links).
    return HttpResponse.json({ url: `${db.tunnel.url}/setup#t=${rand(24)}`, expiresAt: isoIn(3 * 60_000) })
  }),
]

/** newPlugin is a freshly installed homie-tv (no TVs yet). */
function newPlugin(tvs: MockTV[], over: Partial<PluginSummary>): MockPlugin {
  const base = seed('tv-new', db.lang, db.slow).plugins[0]
  return { ...base, tvs, summary: { ...base.summary, ...over } }
}
