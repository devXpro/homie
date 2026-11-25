// The mock home behind `npm run dev:mock`, the Playwright tests and the screenshots: one in-memory state per page
// load, seeded by a scenario (?mock=<name>). Everything in it is a neutral fixture, no real data: the people Alice,
// Bella, Bob and Carol, the home «Дом», the hosts home.example.com and example.trycloudflare.com, the LAN 192.0.2.0/24
// and a locally administered MAC address.

import type { AuditEntry, Lang, Role } from '@devxpro/mcp-gatekeeper-ui'
import type { ActivityItem, Call, ConnectStatus, Job, JSONSchema, PluginSummary, SystemInfo, Tool, TunnelStatus, Via } from '@/lib/types'

export const SCENARIOS = [
  'default',
  'quick',
  'fresh',
  'tv-new',
  'trouble',
  'unreachable',
  'coowner',
  'member',
  'signed-out',
  'not-set-up',
  'join',
  'consent',
  'offline',
  'firstrun',
  'firstrun-reconnect',
  'firstrun-closed',
  'firstrun-done',
] as const
export type Scenario = (typeof SCENARIOS)[number]

export const NAMED_URL = 'https://home.example.com'
export const QUICK_URL = 'https://example.trycloudflare.com'
export const QUICK_URL_NEXT = 'https://example-two.trycloudflare.com'
export const LOGIN_URL = 'https://dash.example.com/argotunnel?callback=example'

export interface MockTV {
  id: string
  name: string
  host: string
  mac: string
  model: string
  paired: boolean
  power: 'on' | 'off' | 'offline'
}

export interface MockPlugin {
  summary: PluginSummary
  tools: Tool[]
  instructions?: string
  tvs: MockTV[]
  logs: string[]
}

export interface MockPerson {
  id: string
  name: string
  role: Role
  superadmin: boolean
  joined_at: string
  access_until?: string | null
  permissions?: string[]
}

export interface MockInvite {
  id: string
  token: string
  kind: 'setup' | 'member' | 'owner'
  name: string
  expires_at: string
  role?: Role
  access_until?: string | null
  permissions?: string[]
}

export interface MockGrant {
  grant_id: string
  person: string
  assistant: string
  client_name: string
  created_at: string
  last_used_at?: string
}

export interface MockPasskey {
  id: string
  person: string
  name: string
  created_at: string
  last_used_at?: string
}

export interface MockState {
  scenario: Scenario
  lang: Lang
  slow: boolean
  home: { name: string; language: Lang }
  session: string | null
  stepUpUntil: number
  people: MockPerson[]
  invites: MockInvite[]
  grants: MockGrant[]
  passkeys: MockPasskey[]
  audit: AuditEntry[]
  tunnel: TunnelStatus
  plugins: MockPlugin[]
  activity: ActivityItem[]
  calls: Map<string, Call>
  jobs: Map<string, Job>
  system: SystemInfo
  attentionReset: boolean
  offline: boolean
  firstrun: {
    done: boolean
    closed: boolean
    unlocked: boolean
    /** the owner exists already (the quick-mode Reconnect case once the address changed) */
    ownerExists: boolean
    connect?: ConnectStatus
  }
  seq: number
}

const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR

export const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()
export const isoIn = (msAhead: number) => new Date(Date.now() + msAhead).toISOString()

/** endOfDay is 23:59:59 local, n days from today: how the invite sheet ends a term. */
function endOfDayIn(n: number) {
  const d = new Date()
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n, 23, 59, 59).toISOString()
}

const inventoryRef = (description: string) => ({ type: 'string', description, 'x-homie-ref': 'inventory' })
const tvOptional = inventoryRef("The TV's id or name, as tv_list shows it. Leave it out when only one TV is set up.")
const tvRequired = inventoryRef("The TV's id or name, as tv_list shows it.")
const object = (properties: Record<string, JSONSchema>, required?: string[]): JSONSchema =>
  required ? { type: 'object', properties, required } : { type: 'object', properties }
const command = { destructiveHint: false, idempotentHint: true }
const tvLabel = { tv: 'Телевизор' }

/** The homie-tv tools as the hub lists them, with their Russian texts. */
export function tvTools(): Tool[] {
  const tool = (t: Omit<Tool, 'pluginId' | 'timeoutMs' | 'inventory'> & Partial<Pick<Tool, 'timeoutMs' | 'inventory'>>): Tool => ({
    pluginId: 'tv',
    timeoutMs: 30_000,
    inventory: false,
    ...t,
  })
  return [
    tool({
      name: 'tv_add',
      title: 'Add a TV',
      description: 'Adds the TV at an IPv4 address and reads its model and MAC address. The TV must be on. Pair it afterwards with tv_pair.',
      audience: 'admin',
      inputSchema: object(
        {
          host: { type: 'string', description: "The TV's IPv4 address, e.g. 192.0.2.20." },
          name: { type: 'string', description: 'What people call the TV, e.g. Living room TV.' },
          id: { type: 'string', pattern: '^[a-z][a-z0-9-]{0,31}$', description: 'Short id: lowercase letters, digits and dashes. Default: tv, tv-2, ...' },
        },
        ['host'],
      ),
      annotations: { destructiveHint: false },
      i18n: {
        ru: {
          title: 'Добавить телевизор',
          description: 'Телевизор по его IP-адресу. Он должен быть включён.',
          properties: { host: 'IP-адрес', name: 'Название', id: 'Короткое имя' },
        },
      },
    }),
    tool({
      name: 'tv_discover',
      title: 'Find TVs',
      description: 'Searches the home network for TVs that are switched on; takes up to 10 seconds.',
      audience: 'admin',
      inputSchema: object({}),
      annotations: { readOnlyHint: true },
      timeoutMs: 15_000,
      i18n: { ru: { title: 'Найти телевизоры', description: 'Ищет включённые телевизоры в домашней сети. Это займёт до 10 секунд.' } },
    }),
    tool({
      name: 'tv_list',
      title: 'List TVs',
      description: 'Lists the TVs at home with their id, name and power state (on, off or offline).',
      audience: 'assistant',
      inputSchema: object({}),
      annotations: { readOnlyHint: true },
      inventory: true,
      i18n: { ru: { title: 'Телевизоры', description: 'Все телевизоры дома и включены ли они.' } },
    }),
    tool({
      name: 'tv_pair',
      title: 'Pair a TV',
      description:
        'Pairs with a TV that is on: the TV shows a prompt, and someone must press OK on the TV remote within 2 minutes. Tell the user to do that before calling.',
      audience: 'admin',
      inputSchema: object({ tv: tvRequired }, ['tv']),
      annotations: command,
      timeoutMs: 180_000,
      target: 'tv',
      i18n: { ru: { title: 'Подключить телевизор', description: 'На экране появится запрос — нажмите OK на пульте в течение 2 минут.', properties: tvLabel } },
    }),
    tool({
      name: 'tv_power_off',
      title: 'Turn TV off',
      description: 'Turns a TV off (standby).',
      audience: 'assistant',
      inputSchema: object({ tv: tvOptional }),
      annotations: command,
      target: 'tv',
      i18n: { ru: { title: 'Выключить телевизор', description: 'Телевизор перейдёт в режим ожидания.', properties: tvLabel } },
    }),
    tool({
      name: 'tv_power_on',
      title: 'Turn TV on',
      description: 'Turns a TV on and waits until it is up, which can take up to 40 seconds.',
      audience: 'assistant',
      inputSchema: object({ tv: tvOptional }),
      annotations: command,
      timeoutMs: 60_000,
      target: 'tv',
      i18n: { ru: { title: 'Включить телевизор', description: 'Включение может занять до 40 секунд.', properties: tvLabel } },
    }),
    tool({
      name: 'tv_remove',
      title: 'Remove a TV',
      description: 'Forgets a TV and its pairing.',
      audience: 'admin',
      inputSchema: object({ tv: tvRequired }, ['tv']),
      annotations: { destructiveHint: true, idempotentHint: true },
      target: 'tv',
      i18n: { ru: { title: 'Удалить телевизор', description: 'Телевизор пропадёт из списка, подключение забудется.', properties: tvLabel } },
    }),
    tool({
      name: 'tv_rename',
      title: 'Rename a TV',
      description: 'Gives a TV a new name; its id stays the same.',
      audience: 'admin',
      inputSchema: object({ tv: tvRequired, name: { type: 'string', minLength: 1, description: 'The new name.' } }, ['tv', 'name']),
      annotations: command,
      target: 'tv',
      i18n: { ru: { title: 'Переименовать телевизор', description: 'Новое название для телевизора.', properties: { tv: 'Телевизор', name: 'Новое название' } } },
    }),
    tool({
      name: 'tv_set_input',
      title: 'Switch TV input',
      description: 'Switches a TV to live TV ("tv") or to an HDMI port ("hdmi1" to "hdmi4").',
      audience: 'assistant',
      inputSchema: object(
        {
          tv: tvOptional,
          input: { type: 'string', enum: ['tv', 'hdmi1', 'hdmi2', 'hdmi3', 'hdmi4'], description: 'The source: tv for live TV, or hdmi1..hdmi4.' },
        },
        ['input'],
      ),
      annotations: command,
      target: 'tv',
      i18n: { ru: { title: 'Источник', description: 'Эфирное ТВ или один из входов HDMI.', properties: { tv: 'Телевизор', input: 'Источник' } } },
    }),
    tool({
      name: 'tv_set_volume',
      title: 'Set TV volume',
      description: "Sets the volume of a TV's own speakers, in percent from 0 (silent) to 100 (loudest).",
      audience: 'assistant',
      inputSchema: object({ tv: tvOptional, percent: { type: 'integer', minimum: 0, maximum: 100, description: 'Volume in percent, 0 to 100.' } }, ['percent']),
      annotations: command,
      target: 'tv',
      i18n: { ru: { title: 'Громкость', description: 'Громкость динамиков телевизора, от 0 до 100 %.', properties: { tv: 'Телевизор', percent: 'Громкость, %' } } },
    }),
  ]
}

/** The one TV of the fixtures, on the documentation LAN with a locally administered MAC address. */
export function livingRoom(lang: Lang): MockTV {
  return { id: 'tv', name: lang === 'en' ? 'Living room TV' : 'Телевизор в гостиной', host: '192.0.2.20', mac: '02:00:00:00:00:01', model: 'webOS TV', paired: true, power: 'on' }
}

const TV_INSTRUCTIONS =
  'Controls the TVs at home: power, volume (0-100 %) and input (live TV or HDMI 1-4). Leave out `tv` when only one TV is set up. Turning a TV on can take up to 40 seconds.'

export function tvPlugin(tvs: MockTV[], over: Partial<PluginSummary> = {}): MockPlugin {
  return {
    summary: {
      id: 'tv',
      name: 'homie-tv',
      title: 'TV',
      description: 'webOS TVs: power, volume, inputs.',
      version: '0.2.0',
      state: 'up',
      since: iso(2 * HOUR + 14 * MIN),
      enabled: true,
      source: { kind: 'github', repo: 'devXpro/homie-tv', homepage: 'https://github.com/devXpro/homie-tv' },
      tools: { assistant: 5, admin: 5 },
      inventoryTool: 'tv_list',
      devices: tvs.length,
      ...over,
    },
    tools: tvTools(),
    instructions: TV_INSTRUCTIONS,
    tvs: tvs.map((t) => ({ ...t })),
    logs: [
      `${iso(2 * HOUR)} INFO homie-tv 0.2.0 serving MCP over stdio`,
      `${iso(2 * HOUR)} INFO store: 1 TV in tvs.json`,
      `${iso(47 * MIN)} INFO tv_power_on tv=tv caller=Alice via=chatgpt`,
      `${iso(47 * MIN - 2400)} INFO wake-on-lan sent to 02:00:00:00:00:01`,
      `${iso(47 * MIN - 6100)} INFO tv answered hello after 6.1s`,
      `${iso(12 * MIN)} INFO tv_set_volume tv=tv percent=18 caller=Bella via=claude`,
    ],
  }
}

/** The people of the fixtures: the owner (superadmin), a co-owner, a guest with a term and a guest whose term ended. */
function people(lang: Lang): MockPerson[] {
  const en = lang === 'en'
  return [
    { id: 'per_alice', name: en ? 'Alice' : 'Алиса', role: 'owner', superadmin: true, joined_at: iso(9 * DAY), permissions: ['all'] },
    { id: 'per_bella', name: en ? 'Bella' : 'Белла', role: 'owner', superadmin: false, joined_at: iso(8 * DAY), permissions: ['all'] },
    { id: 'per_bob', name: en ? 'Bob' : 'Борис', role: 'member', superadmin: false, joined_at: iso(DAY), access_until: endOfDayIn(3), permissions: ['plugin:tv'] },
    { id: 'per_carol', name: en ? 'Carol' : 'Карина', role: 'member', superadmin: false, joined_at: iso(12 * DAY), access_until: iso(2 * DAY), permissions: ['plugin:tv'] },
  ]
}

function activitySeed(lang: Lang): ActivityItem[] {
  const [alice, bella, bob] = people(lang)
  const out: ActivityItem[] = []
  let id = 1100
  const call = (ago: number, who: MockPerson, via: Via, tool: string, title: string, ok = true, ms = 800, args: Record<string, unknown> = {}, error?: string) => {
    out.push({ id: String(id--), at: iso(ago), person: { id: who.id, name: who.name }, via, tool, toolTitle: title, pluginId: 'tv', args, ok, error, durationMs: ms })
  }
  const tv = livingRoom(lang).name
  call(12 * MIN, bella, 'claude', 'tv_set_volume', 'Set TV volume', true, 640, { percent: 18 })
  call(47 * MIN, alice, 'chatgpt', 'tv_power_on', 'Turn TV on', true, 6120, { tv: 'tv' })
  call(3 * HOUR, alice, 'chatgpt', 'tv_set_input', 'Switch TV input', true, 930, { input: 'hdmi2' })
  call(5 * HOUR, bob, 'claude', 'tv_power_off', 'Turn TV off', true, 1210)
  call(DAY + 2 * HOUR, alice, 'chatgpt', 'tv_power_on', 'Turn TV on', false, 40_120, { tv: 'tv' }, `${tv} did not wake up. Check that Turn on via Wi-Fi is enabled in the TV settings, or turn it on with the remote.`)
  call(DAY + 2 * HOUR + 4 * MIN, alice, 'admin', 'tv_set_volume', 'Set TV volume', true, 702, { percent: 25 })
  call(DAY + 6 * HOUR, bella, 'chatgpt', 'tv_power_on', 'Turn TV on', true, 5400)
  call(2 * DAY + 5 * HOUR, alice, 'cli', 'tv_set_input', 'Switch TV input', true, 1104, { input: 'tv' })
  call(3 * DAY + 1 * HOUR, bella, 'claude', 'tv_power_off', 'Turn TV off', true, 980)
  call(4 * DAY + 4 * HOUR, alice, 'chatgpt', 'tv_power_on', 'Turn TV on', true, 6900)
  call(5 * DAY + 2 * HOUR, bob, 'chatgpt', 'tv_set_volume', 'Set TV volume', true, 680, { percent: 12 })
  call(6 * DAY + 3 * HOUR, alice, 'chatgpt', 'tv_power_off', 'Turn TV off', true, 1020)
  return out
}

function auditSeed(): AuditEntry[] {
  const entry = (ago: number, action: string, actor: string | undefined, target?: string, detail?: Record<string, string>): AuditEntry => ({
    id: `a_${Math.round(ago / 1000)}`,
    at: iso(ago),
    action,
    actor,
    target,
    detail,
    ip: '192.0.2.10',
  })
  return [
    entry(12 * MIN, 'oauth.grant.create', 'per_bob', 'home', { assistant: 'claude' }),
    entry(3 * HOUR + 5 * MIN, 'auth.login', 'per_alice'),
    entry(DAY + 7 * HOUR, 'account.member.update', 'per_alice', 'per_bob', { access_until: endOfDayIn(3), permissions: 'plugin:tv' }),
    entry(DAY + 8 * HOUR, 'auth.join', 'per_bob'),
    entry(DAY + 9 * HOUR, 'account.invite.create', 'per_alice', 'Bob'),
    entry(2 * DAY + 3 * HOUR, 'plugin.install', 'per_alice', 'tv'),
    entry(3 * DAY + 2 * HOUR, 'auth.login.fail', undefined, undefined, { count: '1' }),
    entry(8 * DAY, 'auth.join', 'per_bella'),
    entry(9 * DAY, 'auth.register', 'per_alice'),
    entry(9 * DAY + 10 * MIN, 'admin.setup-link', undefined),
  ]
}

/** tunnelAt is a connected tunnel at a public origin, as homed reports it (docs/ADMIN-API.md §3, TunnelStatus). */
export function tunnelAt(mode: 'named' | 'quick', url: string, msAgo: number): TunnelStatus {
  return { mode, state: 'connected', url, mcpUrl: `${url}/mcp`, hostname: new URL(url).host, since: iso(msAgo) }
}

export function namedTunnel(): TunnelStatus {
  return tunnelAt('named', NAMED_URL, 2 * HOUR + 13 * MIN)
}

export function quickTunnel(): TunnelStatus {
  return tunnelAt('quick', QUICK_URL, 25 * MIN)
}

export function seed(scenario: Scenario, lang: Lang, slow: boolean): MockState {
  const all = people(lang)
  const state: MockState = {
    scenario,
    lang,
    slow,
    home: { name: lang === 'en' ? 'Home' : 'Дом', language: lang },
    session: 'per_alice',
    stepUpUntil: 0,
    people: all,
    invites: [{ id: 'inv_carol', token: 'invite-token', kind: 'member', name: lang === 'en' ? 'Dan' : 'Дмитрий', expires_at: isoIn(20 * HOUR), access_until: endOfDayIn(7), permissions: ['plugin:tv'] }],
    grants: [
      { grant_id: 'grt_alice_chatgpt', person: 'per_alice', assistant: 'chatgpt', client_name: 'ChatGPT', created_at: iso(9 * DAY), last_used_at: iso(47 * MIN) },
      { grant_id: 'grt_alice_claude', person: 'per_alice', assistant: 'claude', client_name: 'Claude', created_at: iso(6 * DAY), last_used_at: iso(DAY) },
      { grant_id: 'grt_bella_claude', person: 'per_bella', assistant: 'claude', client_name: 'Claude', created_at: iso(8 * DAY), last_used_at: iso(12 * MIN) },
      { grant_id: 'grt_bob_chatgpt', person: 'per_bob', assistant: 'chatgpt', client_name: 'ChatGPT', created_at: iso(DAY), last_used_at: iso(5 * HOUR) },
    ],
    passkeys: [
      { id: 'pk_mac', person: 'per_alice', name: 'MacBook', created_at: iso(9 * DAY), last_used_at: iso(3 * HOUR) },
      { id: 'pk_phone', person: 'per_alice', name: 'iPhone', created_at: iso(2 * DAY + 3 * HOUR), last_used_at: iso(DAY + 5 * HOUR) },
      { id: 'pk_bella', person: 'per_bella', name: 'iPhone', created_at: iso(8 * DAY), last_used_at: iso(DAY) },
      { id: 'pk_bob', person: 'per_bob', name: 'iPhone', created_at: iso(DAY), last_used_at: iso(5 * HOUR) },
    ],
    audit: auditSeed(),
    tunnel: namedTunnel(),
    plugins: [tvPlugin([livingRoom(lang)])],
    activity: activitySeed(lang),
    calls: new Map(),
    jobs: new Map(),
    system: { version: '0.2.0', commit: '1a2b3c4', os: 'darwin', arch: 'arm64', startedAt: iso(2 * HOUR + 15 * MIN) },
    attentionReset: false,
    offline: false,
    firstrun: { done: false, closed: false, unlocked: false, ownerExists: true },
    seq: 0,
  }

  switch (scenario) {
    case 'quick':
      // Trying Homie out: the owner alone (a quick tunnel takes no invites: docs/SECURITY.md, Quick mode).
      state.tunnel = quickTunnel()
      state.invites = []
      state.people = all.slice(0, 1)
      state.grants = state.grants.filter((g) => g.person === 'per_alice')
      state.activity = state.activity.filter((a) => a.person?.id === 'per_alice')
      state.passkeys = state.passkeys.filter((k) => k.person === 'per_alice')
      break
    case 'fresh':
      state.plugins = []
      state.activity = []
      state.people = all.slice(0, 1)
      state.invites = []
      state.grants = []
      state.passkeys = state.passkeys.slice(0, 1)
      state.audit = state.audit.filter((a) => a.action === 'auth.register' || a.action === 'admin.setup-link')
      break
    case 'tv-new':
      state.plugins = [tvPlugin([], { since: iso(30_000), devices: 0 })]
      state.activity = []
      break
    case 'trouble': {
      state.tunnel = {
        ...namedTunnel(),
        state: 'failing',
        since: iso(25 * MIN),
        lastError: 'cloudflared exited: failed to dial to edge with quic: timeout: no recent network activity',
      }
      const tv = tvPlugin([{ ...livingRoom(lang), power: 'offline' }], {
        state: 'failing',
        since: iso(9 * MIN),
        lastError: 'plugin exited: signal: killed (5 crashes in 5 minutes)',
        update: { latest: '0.3.0', checkedAt: iso(HOUR) },
      })
      tv.logs.push(`${iso(9 * MIN)} ERROR webos: ws dial 192.0.2.20:3001: connection refused`, `${iso(9 * MIN)} FATAL panic: runtime error: invalid memory address`)
      state.plugins = [tv]
      state.system.update = { latest: '0.3.0', notesUrl: 'https://github.com/devXpro/homie/releases/tag/v0.3.0', checkedAt: iso(HOUR) }
      state.attentionReset = true
      break
    }
    case 'unreachable':
      state.tunnel = { ...namedTunnel(), state: 'probing', lastError: 'GET https://home.example.com/healthz: 502 Bad Gateway' }
      break
    case 'coowner':
      state.session = 'per_bella'
      break
    case 'member':
      state.session = 'per_bob'
      break
    case 'signed-out':
      state.session = null
      break
    case 'not-set-up':
      state.session = null
      state.people = []
      state.grants = []
      state.passkeys = []
      state.invites = [{ id: 'inv_setup', token: 'setup-token', kind: 'setup', name: '', expires_at: isoIn(3 * MIN) }]
      break
    case 'join':
      state.session = null
      break
    case 'consent':
      break
    case 'offline':
      state.offline = true
      break
    case 'firstrun':
      state.session = null
      state.people = []
      state.tunnel = { mode: 'off', state: 'stopped' }
      state.firstrun.ownerExists = false
      break
    case 'firstrun-reconnect':
      // A quick tunnel came back with a new address: the owner exists, their passkey is for the old one.
      state.session = null
      state.tunnel = tunnelAt('quick', QUICK_URL_NEXT, 40_000)
      break
    case 'firstrun-closed':
      state.session = null
      state.firstrun.closed = true
      break
    case 'firstrun-done':
      state.firstrun.done = true
      break
    default:
      break
  }
  return state
}

/** inventoryItems is what tv_list returns: "model · host" and the power state; an unpaired TV that is on says so. */
export function inventoryItems(p: MockPlugin) {
  return p.tvs.map((t) => ({
    id: t.id,
    name: t.name,
    subtitle: [t.model, t.host].filter(Boolean).join(' · '),
    state: t.paired ? t.power : t.power === 'offline' ? 'offline' : 'unpaired',
  }))
}

export function tvInfo(t: MockTV) {
  return { id: t.id, name: t.name, host: t.host, mac: t.mac, model: t.model, paired: t.paired }
}
