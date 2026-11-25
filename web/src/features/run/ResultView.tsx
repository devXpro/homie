import { currentLang, Disclosure, Section, StatusDot, StatusPill } from '@devxpro/mcp-gatekeeper-ui'
import { Ellipsis } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { itemTone } from '@/lib/status'
import { humanize, toolTitle } from '@/lib/tools'
import type { InventoryItem, ItemState, JSONSchema, Tool } from '@/lib/types'

// ResultView renders a call's structured result by its shape (DESIGN.md §9.6) and offers the next step on each
// object (§9.7), without the person knowing any tool names.

type Obj = Record<string, unknown>

const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v)
const isScalar = (v: unknown) => v === null || ['string', 'number', 'boolean'].includes(typeof v)
const TITLE_KEYS = ['name', 'title', 'label', 'id', 'host']

function typeMatches(v: unknown, s: JSONSchema | undefined) {
  const type = Array.isArray(s?.type) ? s.type.find((x) => x !== 'null') : s?.type
  if (!type) return v !== undefined
  if (type === 'integer') return Number.isInteger(v)
  if (type === 'number') return typeof v === 'number'
  if (type === 'array') return Array.isArray(v)
  if (type === 'object') return isObj(v)
  return typeof v === type
}

/** prefillFrom copies the values of `o` that the tool's inputs accept. */
function prefillFrom(tool: Tool, o: Obj) {
  const out: Obj = {}
  for (const [k, s] of Object.entries(tool.inputSchema.properties ?? {})) {
    if (k in o && typeMatches(o[k], s)) out[k] = o[k]
  }
  return out
}

export interface FollowUpContext {
  /** the module's actions (no target) */
  moduleActions: Tool[]
  /** the module's item actions (with a target) */
  itemActions: Tool[]
  inventory: InventoryItem[]
  /** the tool that produced the result: never offered again */
  self: string
  onAction: (tool: Tool, prefill: Obj) => void
}

interface Offer {
  tool: Tool
  prefill: Obj
}

function offersFor(o: Obj, ctx: FollowUpContext): Offer[] {
  const offers: Offer[] = []
  for (const tool of ctx.moduleActions) {
    if (tool.name === ctx.self || tool.annotations?.destructiveHint) continue
    const req = tool.inputSchema.required ?? []
    if (req.length === 0) continue
    const props = tool.inputSchema.properties ?? {}
    if (req.every((r) => r in o && typeMatches(o[r], props[r]))) offers.push({ tool, prefill: prefillFrom(tool, o) })
  }
  const id = typeof o.id === 'string' ? o.id : undefined
  if (id && ctx.inventory.some((i) => i.id === id)) {
    for (const tool of ctx.itemActions) {
      if (tool.name === ctx.self || tool.annotations?.destructiveHint || !tool.target) continue
      offers.push({ tool, prefill: { [tool.target]: id } })
    }
  }
  return offers
}

function Offers({ offers, onAction }: { offers: Offer[]; onAction: FollowUpContext['onAction'] }) {
  const { t } = useTranslation()
  const lang = currentLang()
  const [all, setAll] = useState(false)
  if (offers.length === 0) return null
  const shown = all ? offers : offers.slice(0, 2)
  return (
    <div className="flex flex-wrap gap-2 pt-2">
      {shown.map((o) => (
        <button
          key={o.tool.name}
          type="button"
          onClick={() => onAction(o.tool, o.prefill)}
          className="inline-flex h-8 items-center rounded-full bg-accent-tint px-3.5 text-[15px] leading-5 font-semibold text-accent-tinted hover:brightness-110"
        >
          {toolTitle(o.tool, lang)}
        </button>
      ))}
      {!all && offers.length > 2 && (
        <button
          type="button"
          onClick={() => setAll(true)}
          aria-label={t('common.list_more')}
          className="inline-flex size-8 items-center justify-center rounded-full bg-fill text-label-2"
        >
          <Ellipsis aria-hidden className="size-4" />
        </button>
      )}
    </div>
  )
}

function scalarText(v: unknown, yes: string, no: string) {
  if (typeof v === 'boolean') return v ? yes : no
  if (v === null || v === undefined) return '—'
  return String(v)
}

function JsonDetails({ value }: { value: unknown }) {
  const { t } = useTranslation()
  return (
    <Disclosure label={t('common.more')}>
      <pre className="overflow-x-auto rounded-md bg-fill p-3 font-mono text-[12px] leading-4 whitespace-pre-wrap break-all text-label">
        {JSON.stringify(value, null, 2)}
      </pre>
    </Disclosure>
  )
}

function ObjectRows({ o, ctx }: { o: Obj; ctx?: FollowUpContext }) {
  const { t } = useTranslation()
  const scalars = Object.entries(o).filter(([, v]) => isScalar(v))
  const nested = Object.entries(o).filter(([, v]) => !isScalar(v))
  const offers = ctx ? offersFor(o, ctx) : []
  return (
    <>
      {scalars.length > 0 && (
        <Section className="mt-5">
          {scalars.map(([k, v]) => (
            <div key={k} className="homie-row flex min-h-11 items-center gap-3 px-4 py-[11px]">
              <span className="type-body">{humanize(k)}</span>
              <span className="ml-auto min-w-0 truncate text-right type-body text-label-2">{scalarText(v, t('common.yes'), t('common.no'))}</span>
            </div>
          ))}
        </Section>
      )}
      {ctx && <Offers offers={offers} onAction={ctx.onAction} />}
      {nested.length > 0 && (
        <div className="mt-3">
          <JsonDetails value={Object.fromEntries(nested)} />
        </div>
      )}
    </>
  )
}

function ListRows({ items, ctx }: { items: Obj[]; ctx?: FollowUpContext }) {
  const { t } = useTranslation()
  if (items.length === 0) return <p className="mt-4 type-body text-label-2">{t('run.nothing')}</p>
  return (
    <Section className="mt-5">
      {items.map((o, i) => {
        const titleKey = TITLE_KEYS.find((k) => typeof o[k] === 'string' || typeof o[k] === 'number')
        const title = titleKey ? String(o[titleKey]) : `#${i + 1}`
        const sub = Object.entries(o)
          .filter(([k, v]) => k !== titleKey && isScalar(v) && v !== '' && v !== null)
          .slice(0, 3)
          .map(([, v]) => scalarText(v, t('common.yes'), t('common.no')))
          .join(' · ')
        const offers = ctx ? offersFor(o, ctx) : []
        return (
          <div key={i} className="homie-row px-4 py-2.5">
            <div className="type-body">{title}</div>
            {sub && <div className="type-subheadline text-label-2">{sub}</div>}
            {ctx && <Offers offers={offers} onAction={ctx.onAction} />}
          </div>
        )
      })}
    </Section>
  )
}

function DeviceRows({ items }: { items: InventoryItem[] }) {
  const { t } = useTranslation()
  if (items.length === 0) return <p className="mt-4 type-body text-label-2">{t('run.nothing')}</p>
  return (
    <Section className="mt-5">
      {items.map((it) => (
        <div key={it.id} className="homie-row flex min-h-[60px] items-center gap-3 px-4 py-2.5">
          <StatusDot tone={itemTone(it.state)} />
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate type-body">{it.name}</span>
            {it.subtitle && <span className="truncate type-subheadline text-label-2">{it.subtitle}</span>}
          </span>
          {it.state && <StatusPill tone={itemTone(it.state)}>{t(`item.${it.state}`)}</StatusPill>}
        </div>
      ))}
    </Section>
  )
}

const ITEM_STATES = new Set<ItemState>(['on', 'off', 'offline', 'unknown', 'paired', 'unpaired'])

function isInventory(v: unknown): v is { items: InventoryItem[] } {
  return (
    isObj(v) &&
    Array.isArray(v.items) &&
    v.items.every((i) => isObj(i) && typeof i.id === 'string' && typeof i.name === 'string' && (i.state === undefined || ITEM_STATES.has(i.state as ItemState)))
  )
}

export function ResultView({ text, structured, ctx }: { text?: string; structured?: unknown; ctx?: FollowUpContext }) {
  let body: ReactNode
  let s = structured
  // A single wrapping key around one object ({"tv": {…}}) is unwrapped, so its fields read as rows.
  if (isObj(s) && !isInventory(s)) {
    const keys = Object.keys(s)
    if (keys.length === 1 && isObj(s[keys[0]])) s = s[keys[0]]
  }
  if (s === undefined || s === null) {
    body = null
  } else if (isInventory(s)) {
    body = <DeviceRows items={s.items} />
  } else if (Array.isArray(s)) {
    body = s.every(isObj) ? <ListRows items={s} ctx={ctx} /> : <JsonDetails value={s} />
  } else if (isObj(s)) {
    const arrays = Object.entries(s).filter(([, v]) => Array.isArray(v))
    if (arrays.length === 1 && (arrays[0][1] as unknown[]).every(isObj)) {
      body = <ListRows items={arrays[0][1] as Obj[]} ctx={ctx} />
    } else {
      body = <ObjectRows o={s} ctx={ctx} />
    }
  } else {
    body = <JsonDetails value={s} />
  }
  return (
    <div className="w-full text-left">
      {text && <p className="type-body whitespace-pre-line">{text}</p>}
      {body}
    </div>
  )
}
