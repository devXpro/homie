// MSW handlers for the whole admin API (docs/ADMIN-API.md §3–§5): sessions, step-up, CSRF, plugins with a fake
// homie-tv, calls with progress, jobs, the audit log, settings, relay, the SSE stream and the LAN first-run API.

import { Validator, type Schema } from '@cfworker/json-schema'
import { delay, http, HttpResponse, sse } from 'msw'
import type {
  AuditItem,
  Call,
  CallProgress,
  Job,
  JobKind,
  Lang,
  List,
  Me,
  PluginDetail,
  PluginPreview,
  PluginSummary,
  RelayStatus,
} from '@/lib/types'
import {
  inventoryItems,
  iso,
  LIVING_ROOM,
  seed,
  SCENARIOS,
  tvInfo,
  tvPlugin,
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
const SIGNED_KEY = 'homie.mock.signedIn'

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
  if (q.has('mock') && q.get('mock') !== saved.scenario) sessionStorage.removeItem(SIGNED_KEY)
  const cfg = { scenario, lang, slow }
  sessionStorage.setItem(CONFIG_KEY, JSON.stringify(cfg))
  return cfg
}

let db: MockState = seed('default', 'ru', false)

export function configureFromUrl() {
  const cfg = readConfig()
  db = seed(cfg.scenario, cfg.lang, cfg.slow)
  const signed = sessionStorage.getItem(SIGNED_KEY)
  if (signed !== null) db.signedIn = signed === '1'
  if (db.signedIn) db.ownerReady = true
}

function setSignedIn(v: boolean) {
  db.signedIn = v
  sessionStorage.setItem(SIGNED_KEY, v ? '1' : '0')
}

const RUN = Math.random().toString(36).slice(2, 8)
const rand = (n = 8) => Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => b.toString(36).padStart(2, '0')).join('').slice(0, n)
const b64url = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const randomB64 = (n: number) => b64url(crypto.getRandomValues(new Uint8Array(n)))
const ms = (fast: number) => (db.slow ? fast * 6 : fast)
const sleep = (t: number) => new Promise((r) => setTimeout(r, t))

// ---------------------------------------------------------------- SSE

interface SseClient {
  send: (m: { id?: string; event: string; data: unknown }) => void
  logs?: string
  firstrun?: boolean
}

const clients = new Set<SseClient>()

function emit(event: string, data: unknown, filter: (c: SseClient) => boolean = (c) => !c.firstrun) {
  const id = `${RUN}-${++db.seq}`
  for (const c of [...clients]) {
    if (!filter(c)) continue
    try {
      c.send({ id, event, data })
    } catch {
      clients.delete(c)
    }
  }
}

// ---------------------------------------------------------------- helpers

function fail(status: number, code: string, message: string, extra: Record<string, unknown> = {}, headers?: Record<string, string>) {
  return HttpResponse.json({ error: { code, message, requestId: `r_${rand(6)}`, ...extra } }, { status, headers })
}

function offlinePage() {
  return new HttpResponse('<!doctype html><title>Home is offline</title><h1>Home is offline</h1>', {
    status: 404,
    headers: { 'Content-Type': 'text/html; charset=utf-8' },
  })
}

const needSession = () => (db.signedIn ? undefined : fail(401, 'unauthenticated', 'no session'))
const needStepUp = () => (db.stepUpUntil > Date.now() ? undefined : fail(403, 'step_up_required', 'this needs a fresh passkey'))

async function body<T>(request: Request): Promise<T> {
  try {
    return (await request.json()) as T
  } catch {
    return {} as T
  }
}

function me(): Me {
  return {
    person: { ...db.person, passkeys: db.passkeys.length },
    session: {
      id: 's_mac',
      createdAt: iso(3 * 3600_000),
      expiresAt: new Date(Date.now() + 6 * 86400_000).toISOString(),
      idleExpiresAt: new Date(Date.now() + 12 * 3600_000).toISOString(),
      ...(db.stepUpUntil > Date.now() ? { stepUpUntil: new Date(db.stepUpUntil).toISOString() } : {}),
    },
    home: { ...db.home },
    version: db.system.version,
    features: { people: false, connections: false },
  }
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
  return { ...rest, instructions: p.instructions, command: [`/Users/devxpro/Library/Application Support/homie/plugins/${p.summary.id}/bin/${p.summary.version}/homie-${p.summary.id}`, 'serve'], tools: p.tools }
}

function emitPlugin(p: MockPlugin) {
  emit('plugin', summary(p))
}

function audit(item: Omit<AuditItem, 'id' | 'at'>) {
  const a: AuditItem = { id: String(2000 + db.seq + Math.floor(Math.random() * 1000)), at: new Date().toISOString(), ...item }
  db.audit.unshift(a)
  emit('audit', a)
  return a
}

function relayLookOk(r: RelayStatus) {
  return r.state === 'connected' && (!r.selfCheck || r.selfCheck.ok)
}

const REDACT = /(token|key|secret|password|pin|code)/i
function redact(args: Record<string, unknown>) {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(args)) out[k] = REDACT.test(k) ? '[redacted]' : v
  return out
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
  switch (name) {
    case 'tv_list':
      return { ok: true, text: p.tvs.map((t) => `${t.name} (${t.id}) is ${t.power}.`).join('\n') || 'No TV is set up yet.', structured: { items: inventoryItems(p) } }
    case 'tv_power_on': {
      const t = pick(p, args.tv)
      if (!isTV(t)) return t
      await ctx.sleep(ms(2200))
      if (t.power === 'offline') {
        return { ok: false, text: `${t.name} did not wake up. Check that Mobile TV On → Turn on via Wi-Fi is enabled in the TV settings, or turn it on with the remote.` }
      }
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
      const found = [{ host: LIVING_ROOM.host, name: '[LG] webOS TV UJ634V', model: LIVING_ROOM.model, mac: LIVING_ROOM.mac, added: p.tvs.find((t) => t.host === LIVING_ROOM.host)?.id }]
      const tvs = found.map((f) => (f.added ? f : { host: f.host, name: f.name, model: f.model, mac: f.mac }))
      return {
        ok: true,
        text: `Found 1 TV(s): [LG] webOS TV UJ634V at ${LIVING_ROOM.host} (${LIVING_ROOM.model})${found[0].added ? `, already added as ${found[0].added}` : ''}.`,
        structured: { tvs },
      }
    }
    case 'tv_add': {
      const host = String(args.host ?? '').trim()
      await ctx.sleep(ms(1200))
      const dup = p.tvs.find((t) => t.host === host)
      if (dup) return { ok: false, text: `The TV at ${host} is already added as "${dup.id}" (${dup.name}).` }
      if (host !== LIVING_ROOM.host) return { ok: false, text: `No LG webOS TV answered at ${host}. Turn the TV on, check the address and try again.` }
      let id = typeof args.id === 'string' && args.id ? args.id : 'tv'
      for (let n = 2; !args.id && p.tvs.some((t) => t.id === id); n++) id = `tv-${n}`
      const name = typeof args.name === 'string' && args.name.trim() ? args.name.trim() : 'webOS TV UJ634V'
      const t: MockTV = { id, name, host, mac: LIVING_ROOM.mac, model: LIVING_ROOM.model, paired: false, power: 'on' }
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

async function startCall(p: MockPlugin, toolName: string, args: Record<string, unknown>): Promise<Call> {
  const tool = p.tools.find((t) => t.name === toolName) ?? p.tools[0]
  const call: Call = {
    id: `c_${rand(10)}`,
    tool: tool.name,
    pluginId: p.summary.id,
    title: tool.title,
    args: redact(args),
    state: 'running',
    startedAt: new Date().toISOString(),
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
  const finished = (async () => {
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
      audit({ kind: 'tool', person: { id: db.person.id, name: db.person.name }, via: 'admin', tool: tool.name, toolTitle: tool.title, pluginId: p.summary.id, args: call.args, ok: out.ok, error: out.ok ? undefined : out.text, durationMs: call.durationMs ?? 0 })
    } catch {
      // cancelled: DELETE /calls/{id} already reported it
    }
  })()
  void finished
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
  const job: Job = { id: `j_${rand(8)}`, kind, target, state: 'running', steps: STEPS[kind], step: STEPS[kind][0], startedAt: new Date().toISOString() }
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
        emit('hello', { version: db.system.version, serverTime: new Date().toISOString() })
        return
      } else {
        await sleep(ms(step === 'start' ? 700 : 450))
      }
    }
    finish()
    job.state = 'ok'
    job.endedAt = new Date().toISOString()
    delete job.progress
    emit('job', { ...job })
  })()
  return { ...job }
}

// ---------------------------------------------------------------- preview catalog

function previewOf(repo: string): PluginPreview | undefined {
  const installed = (id: string) => {
    const p = plugin(id)
    return p ? { installed: { version: p.summary.version } } : {}
  }
  switch (repo.toLowerCase()) {
    case 'devxpro/homie-tv':
      return { repo: 'devXpro/homie-tv', id: 'tv', title: 'TV', description: 'LG webOS TVs: power, volume, inputs.', version: '0.2.0', publishedAt: iso(3 * 86400_000), homepage: 'https://github.com/devXpro/homie-tv', minHubVersion: '0.2.0', compatible: true, assetFound: true, ...installed('tv') }
    case 'someone/homie-kettle':
      return { repo: 'someone/homie-kettle', id: 'kettle', title: 'Kettle', description: 'Xiaomi smart kettles: boil, keep warm, temperature.', version: '1.4.0', publishedAt: iso(20 * 86400_000), homepage: 'https://github.com/someone/homie-kettle', minHubVersion: '0.4.0', compatible: false, assetFound: true }
    case 'someone/homie-vacuum':
      return { repo: 'someone/homie-vacuum', id: 'vacuum', title: 'Vacuum', description: 'Robot vacuums: start, stop, clean a room.', version: '0.1.0', publishedAt: iso(40 * 86400_000), minHubVersion: '0.2.0', compatible: true, assetFound: false }
  }
  return undefined
}

// ---------------------------------------------------------------- ceremonies

/** The names a setup registration sent to begin, applied at finish (one store.InTx in the real hub). */
const pendingNames = new Map<string, { personName?: string; homeName?: string }>()

function creationOptions(name: string) {
  return {
    rp: { name: 'Homie', id: window.location.hostname },
    user: { id: randomB64(16), name, displayName: name },
    challenge: randomB64(32),
    pubKeyCredParams: [
      { type: 'public-key', alg: -7 },
      { type: 'public-key', alg: -257 },
    ],
    timeout: 300000,
    attestation: 'none',
    authenticatorSelection: { residentKey: 'required', requireResidentKey: true, userVerification: 'required' },
    excludeCredentials: [],
  }
}

function requestOptions() {
  return { challenge: randomB64(32), timeout: 300000, rpId: window.location.hostname, userVerification: 'required' }
}

// ---------------------------------------------------------------- handlers

const A = '/api/admin'

export const handlers = [
  // The home is offline: the relay's HTML 404 for every admin call (DESIGN.md §8.12.1).
  http.all(`${A}/*`, ({ request }) => {
    if (db.offline) return offlinePage()
    if (request.method !== 'GET' && request.headers.get('X-Homie') !== '1') {
      return fail(403, 'csrf_failed', 'missing X-Homie header')
    }
    return undefined
  }),

  sse(`${A}/events`, ({ client, request }) => {
    if (db.offline || !db.signedIn) {
      client.error()
      return
    }
    const logs = new URL(request.url).searchParams.get('logs') ?? undefined
    const c: SseClient = { send: (m) => client.send(m as never), logs }
    clients.add(c)
    request.signal.addEventListener('abort', () => clients.delete(c))
    client.send({ retry: 3000 } as never)
    client.send({ id: `${RUN}-${db.seq}`, event: 'hello', data: { version: db.system.version, serverTime: new Date().toISOString() } } as never)
    if (logs) {
      const p = plugin(logs)
      const timer = setInterval(() => {
        if (!clients.has(c)) return clearInterval(timer)
        const line = `${new Date().toISOString()} DEBUG ssdp: no reply from 239.255.255.250:1900 within 3s`
        p?.logs.push(line)
        try {
          c.send({ id: `${RUN}-${++db.seq}`, event: 'plugin.log', data: { pluginId: logs, lines: [line] } })
        } catch {
          clearInterval(timer)
        }
      }, 4000)
    }
  }),

  http.get(`${A}/bootstrap`, async () => {
    await delay(60)
    return HttpResponse.json({ homeName: db.home.name, language: db.home.language, ownerReady: db.ownerReady, features: { people: false, connections: false } })
  }),

  http.get(`${A}/session`, async () => {
    await delay(60)
    return needSession() ?? HttpResponse.json(me())
  }),

  // ------------------------------------------------ ceremonies (§3.2)
  http.post(`${A}/auth/token`, async ({ request }) => {
    await delay(500)
    const { token } = await body<{ token?: string }>(request)
    if (!token || token === 'invalid') return fail(404, 'token_invalid', 'unknown token')
    if (token === 'expired') return fail(410, 'token_expired', 'the link expired')
    if (token === 'used') return fail(410, 'token_used', 'the link was used')
    return HttpResponse.json({ kind: 'setup', homeName: db.home.name, personName: db.person.name, expiresAt: new Date(Date.now() + 14 * 60_000).toISOString() })
  }),
  http.post(`${A}/auth/register/begin`, async ({ request }) => {
    await delay(150)
    const b = await body<{ token?: string; personName?: string; homeName?: string }>(request)
    const fields = []
    if (b.personName !== undefined && (b.personName.trim().length < 1 || b.personName.trim().length > 40)) fields.push({ path: '/personName', code: 'maxLength', message: 'must be 1 to 40 characters' })
    if (b.homeName !== undefined && (b.homeName.trim().length < 1 || b.homeName.trim().length > 40)) fields.push({ path: '/homeName', code: 'maxLength', message: 'must be 1 to 40 characters' })
    if (fields.length) return fail(400, 'invalid_args', 'bad names', { fields })
    if (b.token === 'expired') return fail(410, 'token_expired', 'the link expired')
    if (b.token === 'exists') return fail(409, 'passkey_exists', 'this authenticator is registered')
    const ceremonyId = `cer_${rand()}`
    pendingNames.set(ceremonyId, { personName: b.personName?.trim(), homeName: b.homeName?.trim() })
    return HttpResponse.json({ ceremonyId, publicKey: creationOptions(b.personName ?? db.person.name) })
  }),
  http.post(`${A}/auth/register/finish`, async ({ request }) => {
    await delay(300)
    const b = await body<{ ceremonyId?: string; passkeyName?: string }>(request)
    const pending = pendingNames.get(b.ceremonyId ?? '')
    if (!pending) return fail(400, 'webauthn_failed', 'unknown or used ceremony')
    pendingNames.delete(b.ceremonyId ?? '')
    if (pending.homeName) db.home.name = pending.homeName
    if (pending.personName) db.person.name = pending.personName
    db.ownerReady = true
    setSignedIn(true)
    db.stepUpUntil = Date.now() + 5 * 60_000
    db.passkeys = [{ id: `cred-${rand()}`, name: b.passkeyName ?? 'Mac', provider: 'iCloud Keychain', createdAt: new Date().toISOString(), current: true }]
    return HttpResponse.json(me(), { status: 201 })
  }),
  http.post(`${A}/auth/login/begin`, async () => {
    await delay(120)
    if (localStorage.getItem('homie.mock.loginRate') === '1') return fail(429, 'rate_limited', 'too many sign-ins', {}, { 'Retry-After': '30' })
    return HttpResponse.json({ ceremonyId: `cer_${rand()}`, publicKey: requestOptions() })
  }),
  http.post(`${A}/auth/login/finish`, async () => {
    await delay(250)
    if (localStorage.getItem('homie.mock.loginFail') === '1') return fail(401, 'login_failed', 'no such passkey')
    setSignedIn(true)
    db.ownerReady = true
    db.stepUpUntil = Date.now() + 5 * 60_000
    audit({ kind: 'admin', person: { id: db.person.id, name: db.person.name }, via: 'admin', action: 'auth.login', ok: true, durationMs: 0 })
    return HttpResponse.json(me())
  }),
  http.post(`${A}/auth/stepup/begin`, async () => {
    await delay(120)
    return needSession() ?? HttpResponse.json({ ceremonyId: `cer_${rand()}`, publicKey: { ...requestOptions(), allowCredentials: [] } })
  }),
  http.post(`${A}/auth/stepup/finish`, async () => {
    await delay(200)
    const no = needSession()
    if (no) return no
    db.stepUpUntil = Date.now() + 5 * 60_000
    return HttpResponse.json({ stepUpUntil: new Date(db.stepUpUntil).toISOString() })
  }),
  http.post(`${A}/auth/logout`, async () => {
    await delay(100)
    setSignedIn(false)
    for (const c of clients) clients.delete(c)
    return new HttpResponse(null, { status: 204 })
  }),

  // ------------------------------------------------ me (§3.3)
  http.patch(`${A}/me`, async ({ request }) => {
    const no = needSession()
    if (no) return no
    const b = await body<{ name?: string; language?: Lang }>(request)
    await delay(200)
    if (b.name !== undefined) {
      const n = b.name.trim()
      if (n.length < 1 || n.length > 40) return fail(400, 'invalid_args', 'name: 1 to 40 characters', { fields: [{ path: '/name', code: 'maxLength', message: 'must be 1 to 40 characters' }] })
      db.person.name = n
    }
    if (b.language) db.person.language = b.language
    return HttpResponse.json({ ...db.person, passkeys: db.passkeys.length })
  }),
  http.get(`${A}/me/passkeys`, async () => {
    await delay(120)
    return needSession() ?? HttpResponse.json({ items: db.passkeys })
  }),
  http.post(`${A}/me/passkeys/begin`, async () => {
    await delay(120)
    return needSession() ?? needStepUp() ?? HttpResponse.json({ ceremonyId: `cer_${rand()}`, publicKey: creationOptions(db.person.name) })
  }),
  http.post(`${A}/me/passkeys/finish`, async ({ request }) => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    const b = await body<{ name?: string }>(request)
    await delay(250)
    const pk = { id: `cred-${rand()}`, name: b.name ?? 'Passkey', provider: 'iCloud Keychain', createdAt: new Date().toISOString(), current: false }
    db.passkeys.push(pk)
    return HttpResponse.json(pk, { status: 201 })
  }),
  http.patch(`${A}/me/passkeys/:id`, async ({ request, params }) => {
    const no = needSession()
    if (no) return no
    const pk = db.passkeys.find((p) => p.id === params.id)
    if (!pk) return fail(404, 'not_found', 'no such passkey')
    const b = await body<{ name?: string }>(request)
    await delay(150)
    if (b.name) pk.name = b.name.trim()
    return HttpResponse.json(pk)
  }),
  http.delete(`${A}/me/passkeys/:id`, async ({ params }) => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    await delay(200)
    if (db.passkeys.length <= 1) return fail(409, 'last_passkey', 'cannot remove the last passkey')
    db.passkeys = db.passkeys.filter((p) => p.id !== params.id)
    return new HttpResponse(null, { status: 204 })
  }),
  http.get(`${A}/me/sessions`, async () => {
    await delay(120)
    return needSession() ?? HttpResponse.json({ items: db.sessions })
  }),
  http.delete(`${A}/me/sessions/:id`, async ({ params }) => {
    const no = needSession()
    if (no) return no
    await delay(150)
    const s = db.sessions.find((x) => x.id === params.id)
    db.sessions = db.sessions.filter((x) => x.id !== params.id)
    if (s?.current) setSignedIn(false)
    return new HttpResponse(null, { status: 204 })
  }),
  http.post(`${A}/me/sessions/revoke-others`, async () => {
    const no = needSession()
    if (no) return no
    await delay(200)
    const revoked = db.sessions.filter((s) => !s.current).length
    db.sessions = db.sessions.filter((s) => s.current)
    return HttpResponse.json({ revoked })
  }),

  // ------------------------------------------------ overview (§3.4)
  http.get(`${A}/overview`, async () => {
    await delay(140)
    const no = needSession()
    if (no) return no
    const attention: unknown[] = []
    if (db.relay.state === 'offline') attention.push({ kind: 'relay_offline' })
    else if (db.relay.state === 'connected' && db.relay.selfCheck && !db.relay.selfCheck.ok) attention.push({ kind: 'relay_unreachable' })
    for (const p of db.plugins) if (p.summary.state === 'failing') attention.push({ kind: 'plugin_failing', target: p.summary.id })
    for (const p of db.plugins) if (p.summary.update) attention.push({ kind: 'plugin_update', target: p.summary.id, version: p.summary.update.latest })
    if (db.system.update) attention.push({ kind: 'hub_update', version: db.system.update.latest })
    return HttpResponse.json({
      home: { name: db.home.name },
      relay: db.relay,
      plugins: db.plugins.map(summary),
      attention,
      recent: db.audit.filter((a) => a.kind === 'tool' && (a.via === 'site' || a.via === 'mcp')).slice(0, 5),
    })
  }),

  // ------------------------------------------------ plugins (§3.5)
  http.get(`${A}/plugins/preview`, async ({ request }) => {
    const no = needSession()
    if (no) return no
    await delay(700)
    const repo = new URL(request.url).searchParams.get('repo') ?? ''
    const p = previewOf(repo)
    return p ? HttpResponse.json(p) : fail(404, 'not_found', `no release of ${repo}`)
  }),
  http.post(`${A}/plugins/check-updates`, async () => {
    const no = needSession()
    if (no) return no
    await delay(800)
    return HttpResponse.json({ items: db.plugins.map(summary) } satisfies List<PluginSummary>)
  }),
  http.get(`${A}/plugins`, async () => {
    await delay(120)
    return needSession() ?? HttpResponse.json({ items: db.plugins.map(summary) })
  }),
  http.post(`${A}/plugins`, async ({ request }) => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    const b = await body<{ repo?: string }>(request)
    const pv = previewOf(b.repo ?? '')
    await delay(200)
    if (!pv) return fail(404, 'not_found', 'no such repo')
    if (plugin(pv.id)) return fail(409, 'conflict', `plugin id ${pv.id} is taken`)
    const job = startJob('plugin.install', pv.id, `homie-${pv.id} ${pv.version}`, () => {
      const p = pv.id === 'tv' ? tvPlugin([], { since: new Date().toISOString(), devices: 0 }) : tvPlugin([], { id: pv.id, name: `homie-${pv.id}`, title: pv.title })
      db.plugins.push(p)
      emitPlugin(p)
      audit({ kind: 'admin', person: { id: db.person.id, name: db.person.name }, via: 'admin', action: 'plugin.install', target: pv.id, ok: true, durationMs: 0 })
    })
    return HttpResponse.json(job, { status: 202 })
  }),
  http.get(`${A}/plugins/:id`, async ({ params }) => {
    await delay(130)
    const no = needSession()
    if (no) return no
    const p = plugin(String(params.id))
    return p ? HttpResponse.json(detail(p)) : fail(404, 'not_found', 'no such plugin')
  }),
  http.patch(`${A}/plugins/:id`, async ({ request, params }) => {
    const no = needSession()
    if (no) return no
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    const b = await body<{ enabled?: boolean }>(request)
    await delay(250)
    if (b.enabled !== undefined) {
      p.summary.enabled = b.enabled
      p.summary.state = b.enabled ? 'up' : 'disabled'
      p.summary.since = new Date().toISOString()
      emitPlugin(p)
    }
    return HttpResponse.json(summary(p))
  }),
  http.post(`${A}/plugins/:id/restart`, async ({ params }) => {
    const no = needSession()
    if (no) return no
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    await delay(150)
    p.summary.state = 'starting'
    p.summary.since = new Date().toISOString()
    delete p.summary.lastError
    emitPlugin(p)
    setTimeout(() => {
      p.summary.state = 'up'
      p.summary.since = new Date().toISOString()
      emitPlugin(p)
    }, ms(1400))
    return HttpResponse.json(summary(p), { status: 202 })
  }),
  http.get(`${A}/plugins/:id/inventory`, async ({ params }) => {
    const no = needSession()
    if (no) return no
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    await delay(ms(450))
    if (!p.summary.inventoryTool) return fail(404, 'no_inventory', 'no inventory tool')
    if (p.summary.state !== 'up') return fail(503, 'plugin_unavailable', 'plugin is not running')
    return HttpResponse.json({ items: inventoryItems(p), fetchedAt: new Date().toISOString() })
  }),
  http.get(`${A}/plugins/:id/logs`, async ({ params }) => {
    const no = needSession()
    if (no) return no
    const p = plugin(String(params.id))
    await delay(150)
    return p ? HttpResponse.json({ lines: p.logs.slice(-1000) }) : fail(404, 'not_found', 'no such plugin')
  }),
  http.post(`${A}/plugins/:id/update`, async ({ params }) => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    const p = plugin(String(params.id))
    if (!p) return fail(404, 'not_found', 'no such plugin')
    const latest = p.summary.update?.latest ?? p.summary.version
    await delay(200)
    const job = startJob('plugin.update', p.summary.id, `homie-${p.summary.id} ${latest}`, () => {
      p.summary.version = latest
      delete p.summary.update
      p.summary.state = 'up'
      delete p.summary.lastError
      emitPlugin(p)
    })
    return HttpResponse.json(job, { status: 202 })
  }),
  http.delete(`${A}/plugins/:id`, async ({ params }) => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    const id = String(params.id)
    if (!plugin(id)) return fail(404, 'not_found', 'no such plugin')
    await delay(200)
    const job = startJob('plugin.remove', id, undefined, () => {
      db.plugins = db.plugins.filter((p) => p.summary.id !== id)
      emit('plugin.removed', { id })
    })
    return HttpResponse.json(job, { status: 202 })
  }),

  // ------------------------------------------------ tools and calls
  http.get(`${A}/tools`, async ({ request }) => {
    const no = needSession()
    if (no) return no
    const q = new URL(request.url).searchParams
    const audience = q.get('audience') ?? 'admin'
    const only = q.get('plugin')
    const items = db.plugins
      .filter((p) => !only || p.summary.id === only)
      .flatMap((p) => p.tools)
      .filter((t) => audience === 'admin' || t.audience === 'assistant')
    return HttpResponse.json({ items })
  }),
  http.post(`${A}/tools/:name/calls`, async ({ request, params }) => {
    const no = needSession()
    if (no) return no
    const name = String(params.name)
    const p = db.plugins.find((x) => x.tools.some((t) => t.name === name))
    if (!p) return fail(404, 'unknown_tool', 'unknown tool')
    if (p.summary.state !== 'up') return fail(503, 'plugin_unavailable', 'plugin is not running')
    const running = [...db.calls.values()].find((c) => c.tool === name && c.state === 'running')
    if (running) return fail(409, 'call_running', 'this tool is already running', { callId: running.id })
    const b = await body<{ args?: Record<string, unknown> }>(request)
    const args = b.args ?? {}
    const tool = p.tools.find((t) => t.name === name)
    if (!tool) return fail(404, 'unknown_tool', 'unknown tool')
    const v = new Validator(tool.inputSchema as Schema, '2020-12', false).validate(args)
    const fields = v.errors
      .filter((e) => !['properties', 'items', 'allOf', 'anyOf', 'oneOf', '$ref'].includes(e.keyword))
      .map((e) => ({ path: e.instanceLocation.replace(/^#/, '') || '/', code: e.keyword, message: e.error }))
    if (fields.length) return fail(400, 'invalid_args', fields.map((f) => f.message).join('; '), { fields })
    await delay(120)
    const call = await startCall(p, name, args)
    if (new URL(request.url).searchParams.get('wait') === '1') {
      while (db.calls.get(call.id)?.state === 'running') await sleep(100)
      return HttpResponse.json(db.calls.get(call.id))
    }
    return HttpResponse.json(call, { status: 202 })
  }),
  http.get(`${A}/calls`, async ({ request }) => {
    const no = needSession()
    if (no) return no
    const state = new URL(request.url).searchParams.get('state')
    const items = [...db.calls.values()].filter((c) => !state || c.state === state).reverse()
    return HttpResponse.json({ items })
  }),
  http.get(`${A}/calls/:id`, async ({ params }) => {
    const no = needSession()
    if (no) return no
    const c = db.calls.get(String(params.id))
    return c ? HttpResponse.json(c) : fail(404, 'not_found', 'no such call')
  }),
  http.delete(`${A}/calls/:id`, async ({ params }) => {
    const no = needSession()
    if (no) return no
    const c = db.calls.get(String(params.id))
    if (!c) return fail(404, 'not_found', 'no such call')
    if (c.state === 'running') {
      cancelled.add(c.id)
      const ended = new Date()
      Object.assign(c, { state: 'cancelled', endedAt: ended.toISOString(), durationMs: ended.getTime() - new Date(c.startedAt).getTime(), error: { code: 'cancelled', message: 'cancelled by the owner' } })
      emit('call', { ...c })
    }
    return HttpResponse.json(c, { status: 202 })
  }),
  http.get(`${A}/jobs`, async ({ request }) => {
    const no = needSession()
    if (no) return no
    const state = new URL(request.url).searchParams.get('state')
    return HttpResponse.json({ items: [...db.jobs.values()].filter((j) => !state || j.state === state).reverse() })
  }),
  http.get(`${A}/jobs/:id`, async ({ params }) => {
    const no = needSession()
    if (no) return no
    const j = db.jobs.get(String(params.id))
    return j ? HttpResponse.json(j) : fail(404, 'not_found', 'no such job')
  }),

  // ------------------------------------------------ audit (§3.6)
  http.get(`${A}/audit`, async ({ request }) => {
    const no = needSession()
    if (no) return no
    await delay(ms(220))
    const q = new URL(request.url).searchParams
    const via = q.get('via')?.split(',')
    const filtered = db.audit.filter(
      (a) =>
        (!q.get('kind') || a.kind === q.get('kind')) &&
        (!q.get('person') || a.person?.id === q.get('person')) &&
        (!q.get('plugin') || a.pluginId === q.get('plugin')) &&
        (!q.get('tool') || a.tool === q.get('tool')) &&
        (!via || via.includes(a.via)) &&
        (!q.get('status') || (q.get('status') === 'ok' ? a.ok : !a.ok)),
    )
    const limit = Math.min(200, Math.max(1, Number(q.get('limit') ?? 50)))
    const cursor = q.get('cursor')
    const offset = cursor ? Number(atob(cursor)) : 0
    const items = filtered.slice(offset, offset + limit)
    const next = offset + limit < filtered.length ? btoa(String(offset + limit)) : undefined
    return HttpResponse.json(next ? { items, nextCursor: next } : { items })
  }),
  http.get(`${A}/audit/:id`, async ({ params }) => {
    const no = needSession()
    if (no) return no
    await delay(100)
    const a = db.audit.find((x) => x.id === params.id)
    return a ? HttpResponse.json(a) : fail(404, 'not_found', 'no such entry')
  }),

  // ------------------------------------------------ people and keys (§3.7)
  http.get(`${A}/people`, async () => needSession() ?? HttpResponse.json({ items: db.people })),
  http.get(`${A}/keys`, async () => {
    await delay(120)
    return needSession() ?? HttpResponse.json({ items: db.keys })
  }),
  http.post(`${A}/people/:id/keys`, async ({ request, params }) => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    const b = await body<{ label?: string }>(request)
    await delay(250)
    const secret = `ghk_${randomB64(32)}`
    const key = { id: `k_${rand(4)}`, personId: String(params.id), label: (b.label ?? 'ChatGPT Site').trim() || 'ChatGPT Site', hint: secret.slice(0, 8), createdAt: new Date().toISOString() }
    db.keys.push(key)
    audit({ kind: 'admin', person: { id: db.person.id, name: db.person.name }, via: 'admin', action: 'key.create', target: key.id, ok: true, durationMs: 0 })
    return HttpResponse.json({ key, secret }, { status: 201 })
  }),
  http.delete(`${A}/keys/:id`, async ({ params }) => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    await delay(200)
    db.keys = db.keys.filter((k) => k.id !== params.id)
    return new HttpResponse(null, { status: 204 })
  }),

  // ------------------------------------------------ settings, relay, system (§3.9)
  http.get(`${A}/settings`, async () => needSession() ?? HttpResponse.json({ homeName: db.home.name, language: db.home.language })),
  http.patch(`${A}/settings`, async ({ request }) => {
    const no = needSession()
    if (no) return no
    const b = await body<{ homeName?: string; language?: Lang }>(request)
    await delay(250)
    if (b.homeName !== undefined) {
      const n = b.homeName.trim()
      if (n.length < 1 || n.length > 40) return fail(400, 'invalid_args', 'homeName: 1 to 40 characters', { fields: [{ path: '/homeName', code: 'maxLength', message: 'must be 1 to 40 characters' }] })
      db.home.name = n
    }
    if (b.language) db.home.language = b.language
    const s = { homeName: db.home.name, language: db.home.language }
    emit('settings', s)
    return HttpResponse.json(s)
  }),
  http.get(`${A}/relay`, async () => {
    await delay(120)
    return needSession() ?? HttpResponse.json(db.relay)
  }),
  http.post(`${A}/relay/check`, async () => {
    const no = needSession()
    if (no) return no
    await delay(ms(1100))
    if (db.relay.state === 'connected') db.relay.selfCheck = { ...db.relay.selfCheck, ok: db.relay.selfCheck?.ok ?? true, checkedAt: new Date().toISOString() }
    emit('relay', db.relay)
    return HttpResponse.json(db.relay)
  }),
  http.put(`${A}/relay`, async ({ request }) => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    const { code } = await body<{ code?: string }>(request)
    await delay(400)
    if (!code?.startsWith('ghr1.') || code.length < 20) return fail(400, 'invalid_args', 'not a connect code', { fields: [{ path: '/code', code: 'pattern', message: 'not a connect code' }] })
    if (code.includes('other')) return fail(409, 'domain_change_local_only', 'a code for another domain')
    db.relay = { ...db.relay, state: 'connecting', since: new Date().toISOString() }
    emit('relay', db.relay)
    setTimeout(() => {
      db.relay = { ...db.relay, state: 'connected', since: new Date().toISOString(), selfCheck: { ok: true, checkedAt: new Date().toISOString() } }
      delete db.relay.lastError
      emit('relay', db.relay)
    }, ms(1500))
    return HttpResponse.json(db.relay, { status: 202 })
  }),
  http.get(`${A}/system`, async () => {
    await delay(120)
    return needSession() ?? HttpResponse.json(db.system)
  }),
  http.post(`${A}/system/check-update`, async () => {
    const no = needSession()
    if (no) return no
    await delay(ms(1200))
    return HttpResponse.json(db.system)
  }),
  http.post(`${A}/system/update`, async () => {
    const no = needSession() ?? needStepUp()
    if (no) return no
    const latest = db.system.update?.latest
    if (!latest) return fail(409, 'conflict', 'no update')
    await delay(200)
    const job = startJob('hub.update', undefined, `homie ${latest}`, () => {
      db.system = { ...db.system, version: latest, startedAt: new Date().toISOString() }
      delete db.system.update
    })
    return HttpResponse.json(job, { status: 202 })
  }),

  http.all(`${A}/*`, () => fail(404, 'not_found', 'the mock has no such endpoint')),

  // ------------------------------------------------ first-run LAN page (§5)
  http.all('/api/firstrun/*', ({ request }) => {
    if (db.firstrun.done) return fail(410, 'setup_done', 'the home has an owner')
    if (db.firstrun.closed) return fail(410, 'setup_closed', 'the setup page is closed; open it again with homed admin firstrun')
    if (request.method !== 'GET' && request.headers.get('X-Homie') !== '1') return fail(403, 'csrf_failed', 'missing X-Homie header')
    return undefined
  }),
  http.get('/api/firstrun/state', async () => {
    await delay(150)
    const f = db.firstrun
    if (!f.unlocked) return HttpResponse.json({ step: 'pin' })
    if (!f.relayConfigured) return HttpResponse.json({ step: 'relay', homeName: db.home.name })
    return HttpResponse.json({ step: relayLookOk(db.relay) ? 'link' : 'connecting', homeName: db.home.name, relay: db.relay })
  }),
  http.post('/api/firstrun/pin', async ({ request }) => {
    await delay(350)
    const { pin } = await body<{ pin?: string }>(request)
    // 5 wrong PINs lock this device until its 10-minute window ends (T2.F: per client; 20 in total close the page).
    const locked = () =>
      fail(429, 'rate_limited', 'too many wrong PINs from this device; wait, then try again', {}, {
        'Retry-After': String(Math.max(1, Math.ceil((db.firstrun.lockedUntil - Date.now()) / 1000))),
      })
    if (db.firstrun.lockedUntil > Date.now()) return locked()
    if (pin?.replace(/[\s-]/g, '') === '12345678') {
      db.firstrun.unlocked = true
      db.firstrun.wrongPins = 0
      if (db.firstrun.relayConfigured && !relayLookOk(db.relay)) connectRelaySoon()
      return new HttpResponse(null, { status: 204 })
    }
    db.firstrun.wrongPins++
    if (db.firstrun.wrongPins >= 5) {
      db.firstrun.wrongPins = 0
      db.firstrun.lockedUntil = Date.now() + 10 * 60_000
      return locked()
    }
    return fail(401, 'wrong_pin', 'wrong PIN')
  }),
  http.post('/api/firstrun/relay', async ({ request }) => {
    if (!db.firstrun.unlocked) return fail(401, 'unauthenticated', 'enter the PIN first')
    const { code } = await body<{ code?: string }>(request)
    await delay(500)
    if (!code?.trim().startsWith('ghr1.') || code.trim().length < 20) return fail(400, 'invalid_args', 'not a connect code', { fields: [{ path: '/code', code: 'pattern', message: 'not a connect code' }] })
    db.firstrun.relayConfigured = true
    db.relay = { configured: true, domain: 'homie.jobbyjobs.work', publicUrl: 'https://homie.jobbyjobs.work', state: 'connecting', since: new Date().toISOString() }
    connectRelaySoon()
    return HttpResponse.json(db.relay, { status: 202 })
  }),
  sse('/api/firstrun/events', ({ client, request }) => {
    const c: SseClient = { send: (m) => client.send(m as never), firstrun: true }
    clients.add(c)
    request.signal.addEventListener('abort', () => clients.delete(c))
    client.send({ retry: 3000 } as never)
    client.send({ id: `${RUN}-${db.seq}`, event: 'relay', data: db.relay } as never)
  }),
  http.post('/api/firstrun/link', async () => {
    if (!db.firstrun.unlocked) return fail(401, 'unauthenticated', 'enter the PIN first')
    await delay(300)
    if (!relayLookOk(db.relay)) return fail(409, 'relay_not_ready', 'the relay is not connected yet')
    return HttpResponse.json({ url: `https://homie.jobbyjobs.work/admin/setup#t=${randomB64(32)}`, expiresAt: new Date(Date.now() + 3 * 60_000).toISOString() }) // LAN links live 3 minutes
  }),
]

function connectRelaySoon() {
  setTimeout(() => {
    db.relay = { ...db.relay, state: 'connected', since: new Date().toISOString() }
    emit('relay', db.relay, (c) => !!c.firstrun)
    setTimeout(() => {
      db.relay = { ...db.relay, selfCheck: { ok: true, checkedAt: new Date().toISOString() } }
      emit('relay', db.relay, (c) => !!c.firstrun)
    }, ms(1200))
  }, ms(1800))
}

/** setOffline makes every admin call answer like the relay does for a sleeping home. */
export function setOffline(v: boolean) {
  db.offline = v
}
