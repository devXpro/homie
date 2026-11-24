// The mock home behind `npm run dev:mock`, the Playwright tests and the screenshots: one in-memory state per page
// load, shaped exactly like docs/ADMIN-API.md, seeded by a scenario (?mock=<name>).

import type {
  AuditItem,
  Call,
  Job,
  JSONSchema,
  Key,
  Lang,
  Passkey,
  Person,
  PluginSummary,
  RelayStatus,
  Session,
  SystemInfo,
  Tool,
} from '@/lib/types'

export const SCENARIOS = [
  'default',
  'fresh',
  'tv-new',
  'trouble',
  'unreachable',
  'signed-out',
  'not-set-up',
  'offline',
  'firstrun',
  'firstrun-norelay',
  'firstrun-done',
  'firstrun-closed',
] as const
export type Scenario = (typeof SCENARIOS)[number]

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

export interface MockState {
  scenario: Scenario
  lang: Lang
  slow: boolean
  home: { name: string; language: Lang }
  ownerReady: boolean
  signedIn: boolean
  stepUpUntil: number
  person: Person
  relay: RelayStatus
  plugins: MockPlugin[]
  audit: AuditItem[]
  calls: Map<string, Call>
  jobs: Map<string, Job>
  passkeys: Passkey[]
  sessions: Session[]
  keys: Key[]
  people: Person[]
  system: SystemInfo
  offline: boolean
  firstrun: { done: boolean; closed: boolean; relayConfigured: boolean; unlocked: boolean; wrongPins: number; lockedUntil: number }
  seq: number
}

const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR

export const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()

const inventoryRef = (description: string) => ({ type: 'string', description, 'x-homie-ref': 'inventory' })
const tvOptional = inventoryRef("The TV's id or name, as tv_list shows it. Leave it out when only one TV is set up.")
const tvRequired = inventoryRef("The TV's id or name, as tv_list shows it.")
const object = (properties: Record<string, JSONSchema>, required?: string[]): JSONSchema =>
  required ? { type: 'object', properties, required } : { type: 'object', properties }
const command = { destructiveHint: false, idempotentHint: true }
const tvLabel = { tv: 'Телевизор' }

/** The homie-tv tools as the hub lists them (homie-tv internal/tools, with T2.7's Russian texts). */
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
      description:
        'Adds the LG webOS TV at an IPv4 address and reads its model and MAC address. The TV must be on. Pair it afterwards with tv_pair.',
      audience: 'admin',
      inputSchema: object(
        {
          host: { type: 'string', description: "The TV's IPv4 address, e.g. 192.168.1.20." },
          name: { type: 'string', description: 'What people call the TV, e.g. Living room TV.' },
          id: { type: 'string', pattern: '^[a-z][a-z0-9-]{0,31}$', description: 'Short id: lowercase letters, digits and dashes. Default: tv, tv-2, ...' },
        },
        ['host'],
      ),
      annotations: { destructiveHint: false },
      i18n: {
        ru: {
          title: 'Добавить телевизор',
          description: 'Телевизор LG по его IP-адресу. Он должен быть включён.',
          properties: { host: 'IP-адрес', name: 'Название', id: 'Короткое имя' },
        },
      },
    }),
    tool({
      name: 'tv_discover',
      title: 'Find TVs',
      description: 'Searches the home network for LG webOS TVs that are switched on; takes up to 10 seconds.',
      audience: 'admin',
      inputSchema: object({}),
      annotations: { readOnlyHint: true },
      timeoutMs: 15_000,
      i18n: { ru: { title: 'Найти телевизоры', description: 'Ищет включённые телевизоры LG в домашней сети. Это займёт до 10 секунд.' } },
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
      i18n: {
        ru: {
          title: 'Подключить телевизор',
          description: 'На экране появится запрос — нажмите OK на пульте в течение 2 минут.',
          properties: tvLabel,
        },
      },
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
      inputSchema: object(
        { tv: tvOptional, percent: { type: 'integer', minimum: 0, maximum: 100, description: 'Volume in percent, 0 to 100.' } },
        ['percent'],
      ),
      annotations: command,
      target: 'tv',
      i18n: { ru: { title: 'Громкость', description: 'Громкость динамиков телевизора, от 0 до 100 %.', properties: { tv: 'Телевизор', percent: 'Громкость, %' } } },
    }),
  ]
}

export const LIVING_ROOM: MockTV = {
  id: 'tv',
  name: 'Телевизор в гостиной',
  host: '192.168.68.66',
  mac: '20:3d:bd:38:e1:c8',
  model: '49UJ634V-ZD',
  paired: true,
  power: 'on',
}

const TV_INSTRUCTIONS =
  'Controls the TVs at home: power, volume (0-100 %) and input (live TV or HDMI 1-4). Leave out `tv` when only one TV is set up. Turning a TV on can take up to 40 seconds.'

export function tvPlugin(tvs: MockTV[], over: Partial<PluginSummary> = {}): MockPlugin {
  return {
    summary: {
      id: 'tv',
      name: 'homie-tv',
      title: 'TV',
      description: 'LG webOS TVs: power, volume, inputs.',
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
      `${new Date(Date.now() - 2 * HOUR).toISOString()} INFO homie-tv 0.2.0 serving MCP over stdio`,
      `${new Date(Date.now() - 2 * HOUR).toISOString()} INFO store: 1 TV in tvs.json`,
      `${new Date(Date.now() - 47 * MIN).toISOString()} INFO tv_power_on tv=tv caller=Роман via=site`,
      `${new Date(Date.now() - 47 * MIN + 2400).toISOString()} INFO wake-on-lan sent to 20:3d:bd:38:e1:c8`,
      `${new Date(Date.now() - 47 * MIN + 6100).toISOString()} INFO tv answered hello after 6.1s`,
      `${new Date(Date.now() - 12 * MIN).toISOString()} INFO tv_set_volume tv=tv percent=18 caller=Таня via=site`,
    ],
  }
}

const roman = { id: 'roman', name: 'Роман' }
const tanya = { id: 'tanya', name: 'Таня' }

function auditSeed(): AuditItem[] {
  const out: AuditItem[] = []
  let id = 1100
  const tool = (ago: number, who: { id: string; name: string }, name: string, title: string, ok = true, ms = 800, args: Record<string, unknown> = {}, error?: string) => {
    out.push({
      id: String(id--),
      at: iso(ago),
      kind: 'tool',
      person: who,
      via: 'site',
      tool: name,
      toolTitle: title,
      pluginId: 'tv',
      args,
      ok,
      error,
      durationMs: ms,
    })
  }
  const admin = (ago: number, action: string, target?: string, ok = true, via: 'admin' | 'cli' = 'admin') => {
    out.push({ id: String(id--), at: iso(ago), kind: 'admin', person: ok ? roman : undefined, via, action, target, ok, durationMs: 0 })
  }
  tool(12 * MIN, tanya, 'tv_set_volume', 'Set TV volume', true, 640, { percent: 18 })
  tool(47 * MIN, roman, 'tv_power_on', 'Turn TV on', true, 6120, { tv: 'tv' })
  tool(3 * HOUR, roman, 'tv_set_input', 'Switch TV input', true, 930, { input: 'hdmi2' })
  admin(3 * HOUR + 5 * MIN, 'auth.login')
  tool(5 * HOUR, tanya, 'tv_power_off', 'Turn TV off', true, 1210)
  tool(DAY + 2 * HOUR, roman, 'tv_power_on', 'Turn TV on', false, 40_120, { tv: 'tv' },
    'Телевизор в гостиной did not wake up. Check that Mobile TV On → Turn on via Wi-Fi is enabled in the TV settings, or turn it on with the remote.')
  tool(DAY + 2 * HOUR + 4 * MIN, roman, 'tv_set_volume', 'Set TV volume', true, 702, { percent: 25 })
  tool(DAY + 6 * HOUR, tanya, 'tv_power_on', 'Turn TV on', true, 5400)
  admin(DAY + 7 * HOUR, 'key.create', 'k_7Hd2')
  admin(2 * DAY + 3 * HOUR, 'plugin.install', 'tv')
  admin(2 * DAY + 3 * HOUR + 2 * MIN, 'passkey.add', 'iPhone')
  tool(2 * DAY + 5 * HOUR, roman, 'tv_set_input', 'Switch TV input', true, 1104, { input: 'tv' })
  tool(3 * DAY + 1 * HOUR, tanya, 'tv_power_off', 'Turn TV off', true, 980)
  admin(3 * DAY + 2 * HOUR, 'auth.login_failed', undefined, false)
  tool(4 * DAY + 4 * HOUR, roman, 'tv_power_on', 'Turn TV on', true, 6900)
  tool(5 * DAY + 2 * HOUR, tanya, 'tv_set_volume', 'Set TV volume', true, 680, { percent: 12 })
  tool(6 * DAY + 3 * HOUR, roman, 'tv_power_off', 'Turn TV off', true, 1020)
  admin(9 * DAY, 'auth.setup', 'roman')
  admin(9 * DAY - 10 * MIN, 'setup.link', undefined, true, 'cli')
  return out
}

function relayConnected(): RelayStatus {
  return {
    configured: true,
    domain: 'homie.jobbyjobs.work',
    publicUrl: 'https://homie.jobbyjobs.work',
    state: 'connected',
    since: iso(2 * HOUR + 13 * MIN),
    selfCheck: { ok: true, checkedAt: iso(20_000) },
  }
}

export function seed(scenario: Scenario, lang: Lang, slow: boolean): MockState {
  const person: Person = {
    id: 'roman',
    name: lang === 'en' ? 'Roman' : 'Роман',
    role: 'owner',
    plugins: ['*'],
    language: lang,
    createdAt: iso(9 * DAY),
    passkeys: 2,
    lastActiveAt: iso(MIN),
  }
  const state: MockState = {
    scenario,
    lang,
    slow,
    home: { name: lang === 'en' ? 'Our place' : 'Мой шалаш', language: lang },
    ownerReady: true,
    signedIn: true,
    stepUpUntil: 0,
    person,
    relay: relayConnected(),
    plugins: [tvPlugin([{ ...LIVING_ROOM }])],
    audit: auditSeed(),
    calls: new Map(),
    jobs: new Map(),
    passkeys: [
      { id: 'cred-mbp', name: 'MacBook Pro', provider: 'iCloud Keychain', createdAt: iso(9 * DAY), lastUsedAt: iso(3 * HOUR), current: true },
      { id: 'cred-iphone', name: 'iPhone', provider: 'iCloud Keychain', createdAt: iso(2 * DAY + 3 * HOUR), lastUsedAt: iso(DAY + 5 * HOUR), current: false },
    ],
    sessions: [
      { id: 's_mac', device: 'Safari · Mac', createdAt: iso(3 * HOUR), lastSeenAt: iso(MIN), current: true },
      { id: 's_iphone', device: 'Safari · iPhone', createdAt: iso(2 * DAY), lastSeenAt: iso(DAY + 5 * HOUR), current: false },
    ],
    keys: [
      { id: 'k_7Hd2', personId: 'roman', label: 'ChatGPT Site', hint: 'ghk_AbCd', createdAt: iso(DAY + 7 * HOUR), lastUsedAt: iso(12 * MIN) },
      { id: 'k_2Lp9', personId: 'tanya', label: 'ChatGPT Site — Таня', hint: 'ghk_Q7rT', createdAt: iso(DAY + 7 * HOUR), lastUsedAt: iso(12 * MIN) },
    ],
    people: [],
    system: { version: '0.2.0', commit: '8cd45e2', os: 'darwin', arch: 'arm64', startedAt: iso(2 * HOUR + 15 * MIN) },
    offline: false,
    firstrun: { done: false, closed: false, relayConfigured: true, unlocked: false, wrongPins: 0, lockedUntil: 0 },
    seq: 0,
  }
  state.people = [
    person,
    { id: 'tanya', name: lang === 'en' ? 'Tanya' : 'Таня', role: 'member', plugins: ['*'], createdAt: iso(9 * DAY), passkeys: 0 },
  ]

  switch (scenario) {
    case 'fresh':
      state.plugins = []
      state.audit = state.audit.filter((a) => a.kind === 'admin' && (a.action === 'auth.setup' || a.action === 'setup.link'))
      state.keys = []
      state.passkeys = state.passkeys.slice(0, 1)
      state.sessions = state.sessions.slice(0, 1)
      break
    case 'tv-new':
      state.plugins = [tvPlugin([], { since: iso(30_000), devices: 0 })]
      state.audit = state.audit.filter((a) => a.kind === 'admin')
      break
    case 'trouble': {
      state.relay = { ...relayConnected(), state: 'offline', since: iso(25 * MIN), lastError: 'dial tcp 169.40.15.46:443: i/o timeout', selfCheck: { ok: false, checkedAt: iso(25 * MIN), error: 'Get "https://homie.jobbyjobs.work/healthz": context deadline exceeded' } }
      const tv = tvPlugin([{ ...LIVING_ROOM, power: 'offline' }], {
        state: 'failing',
        since: iso(9 * MIN),
        lastError: 'plugin exited: signal: killed (5 crashes in 5 minutes)',
        update: { latest: '0.3.0', checkedAt: iso(HOUR) },
      })
      tv.logs.push(`${new Date(Date.now() - 9 * MIN).toISOString()} ERROR lgwebos: ws dial 192.168.68.66:3001: connection refused`, `${new Date(Date.now() - 9 * MIN).toISOString()} FATAL panic: runtime error: invalid memory address`)
      state.plugins = [tv]
      state.system.update = { latest: '0.3.0', notesUrl: 'https://github.com/devXpro/homie/releases/tag/v0.3.0', checkedAt: iso(HOUR) }
      break
    }
    case 'unreachable':
      state.relay = { ...relayConnected(), selfCheck: { ok: false, checkedAt: iso(40_000), error: 'GET https://homie.jobbyjobs.work/healthz: 502 Bad Gateway' } }
      break
    case 'signed-out':
      state.signedIn = false
      break
    case 'not-set-up':
      state.signedIn = false
      state.ownerReady = false
      break
    case 'offline':
      state.offline = true
      break
    case 'firstrun':
      state.signedIn = false
      state.ownerReady = false
      state.relay = { ...relayConnected(), state: 'connecting', since: iso(1000), selfCheck: undefined }
      break
    case 'firstrun-norelay':
      state.signedIn = false
      state.ownerReady = false
      state.firstrun.relayConfigured = false
      state.relay = { configured: false, state: 'not_configured' }
      break
    case 'firstrun-closed':
      state.signedIn = false
      state.ownerReady = false
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

/**
 * inventoryItems is what tv_list returns: "model · host" and the power state. An unpaired TV that is on reports the
 * contract's `unpaired` state (PLAN §5.1); today's homie-tv appends an English "not paired" to the subtitle instead.
 */
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
