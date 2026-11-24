import { useQueryClient, type InfiniteData } from '@tanstack/react-query'
import { ArrowUp, History, SlidersHorizontal, X } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams } from 'react-router'
import { Page } from '@/components/page'
import { useCloseSheet } from '@/features/shell/useCloseSheet'
import { Button, IconButton } from '@/components/ui/button'
import { CheckList, SegmentedControl } from '@/components/ui/controls'
import { EmptyState, ErrorState, Skeleton, SkeletonRows } from '@/components/ui/feedback'
import { Row, Section } from '@/components/ui/list'
import { Sheet, SheetFooter } from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { currentLang } from '@/i18n'
import { errorText } from '@/lib/errors'
import { auditBus } from '@/lib/events'
import { dayDiff, fmtDate, fmtDateTime, fmtDuration } from '@/lib/format'
import { useDelayed, useNow, useSticky } from '@/lib/hooks'
import { spring } from '@/lib/motion'
import { qk, useAudit, useAuditItem, usePeople, usePlugins, type AuditFilter } from '@/lib/queries'
import { moduleTitle } from '@/lib/tools'
import type { AuditItem, Page as ApiPage } from '@/lib/types'
import { ActivityRow } from './rows'
import { auditTitle, useToolIndex } from './toolIndex'

// Журнал (DESIGN.md §8.8): ChatGPT / Всё / Ошибки, filters as removable chips, days, infinite scroll, and new
// entries arriving live (a flash at the top, or a capsule when scrolled down).

type Seg = 'assistant' | 'all' | 'failed'
type Via = 'chatgpt' | 'admin' | 'cli'
interface Extra {
  person?: string
  plugin?: string
  via?: Via
  kind?: 'tool' | 'admin'
}

// The filter survives leaving the tab and coming back (module state, not URL: the tab stack keeps paths only).
let saved: { seg: Seg; extra: Extra } = { seg: 'assistant', extra: {} }

const VIA_QUERY: Record<Via, string> = { chatgpt: 'site,mcp', admin: 'admin', cli: 'cli' }

function toFilter(seg: Seg, extra: Extra): AuditFilter {
  const f: AuditFilter = seg === 'assistant' ? { kind: 'tool', via: 'site,mcp' } : seg === 'failed' ? { status: 'failed' } : {}
  if (extra.kind) f.kind = extra.kind
  if (extra.via) f.via = VIA_QUERY[extra.via]
  if (extra.person) f.person = extra.person
  if (extra.plugin) f.plugin = extra.plugin
  return f
}

function matches(a: AuditItem, f: AuditFilter) {
  if (f.kind && a.kind !== f.kind) return false
  if (f.via && !f.via.split(',').includes(a.via)) return false
  if (f.status === 'failed' && a.ok) return false
  if (f.status === 'ok' && !a.ok) return false
  if (f.person && a.person?.id !== f.person) return false
  if (f.plugin && a.pluginId !== f.plugin) return false
  return true
}

export function ActivityPage() {
  const { t } = useTranslation()
  const lang = currentLang()
  const qc = useQueryClient()
  const { id: entryId } = useParams()
  const closeEntry = useCloseSheet('/admin/activity')
  const [seg, setSeg] = useState<Seg>(saved.seg)
  const [extra, setExtra] = useState<Extra>(saved.extra)
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [pending, setPending] = useState<AuditItem[]>([])
  const [flash, setFlash] = useState<Set<string>>(new Set())
  const filter = useMemo(() => toFilter(seg, extra), [seg, extra])
  const audit = useAudit(filter)
  const tools = useToolIndex()
  const plugins = usePlugins()
  const people = usePeople()
  const now = useNow()
  const showSkeleton = useDelayed(audit.isPending)
  const sentinel = useRef<HTMLDivElement>(null)

  useEffect(() => {
    saved = { seg, extra }
  }, [seg, extra])

  // New entries: at the top they slide in and flash; further down a capsule offers them (DESIGN.md §8.8).
  useEffect(
    () =>
      auditBus.on((a) => {
        if (!matches(a, filter)) {
          void qc.invalidateQueries({ queryKey: qk.auditAll, refetchType: 'none' })
          return
        }
        if (window.scrollY < 120) {
          insert(qc, filter, [a])
          setFlash((s) => new Set(s).add(a.id))
          setTimeout(() => setFlash((s) => {
            const n = new Set(s)
            n.delete(a.id)
            return n
          }), 1300)
        } else {
          setPending((p) => [a, ...p])
        }
      }),
    [filter, qc],
  )

  // Infinite scroll by cursor.
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = audit
  useEffect(() => {
    const el = sentinel.current
    if (!el) return
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting) && hasNextPage && !isFetchingNextPage) void fetchNextPage()
    }, { rootMargin: '400px' })
    io.observe(el)
    return () => io.disconnect()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])

  const items = useMemo(() => audit.data?.pages.flatMap((p) => p.items) ?? [], [audit.data])
  const groups = useMemo(() => {
    const out: { key: string; label: string; items: AuditItem[] }[] = []
    for (const a of items) {
      const d = dayDiff(new Date(a.at), new Date(now))
      const label = d === 0 ? t('activity.today') : d === -1 ? t('activity.yesterday') : fmtDate(a.at, lang, new Date(now))
      const last = out[out.length - 1]
      if (last && last.label === label) last.items.push(a)
      else out.push({ key: `${label}-${a.id}`, label, items: [a] })
    }
    return out
  }, [items, now, lang, t])

  const chips: { key: keyof Extra; label: string }[] = []
  if (extra.person) chips.push({ key: 'person', label: people.data?.items.find((p) => p.id === extra.person)?.name ?? extra.person })
  if (extra.plugin) {
    const p = plugins.data?.items.find((x) => x.id === extra.plugin)
    chips.push({ key: 'plugin', label: p ? moduleTitle(p, t) : extra.plugin })
  }
  if (extra.via) chips.push({ key: 'via', label: t(`activity.via.${extra.via}`) })
  if (extra.kind) chips.push({ key: 'kind', label: t(`activity.kind.${extra.kind}`) })
  // The default ChatGPT view counts as unfiltered: a new home is simply empty (DESIGN.md §8.8).
  const filtered = seg === 'failed' || chips.length > 0

  const showPending = () => {
    insert(qc, filter, pending)
    setPending([])
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  return (
    <Page
      title={t('activity.title')}
      trailing={
        <IconButton label={t('activity.filters')} onClick={() => setFiltersOpen(true)}>
          <SlidersHorizontal aria-hidden strokeWidth={2.2} />
        </IconButton>
      }
    >
      <SegmentedControl
        label={t('activity.title')}
        value={seg}
        onChange={(v) => {
          setSeg(v)
          setPending([])
        }}
        options={[
          { value: 'assistant', label: t('activity.seg.assistant') },
          { value: 'all', label: t('activity.seg.all') },
          { value: 'failed', label: t('activity.seg.failed') },
        ]}
      />
      {chips.length > 0 && (
        <ul className="mt-3 flex flex-wrap gap-1.5">
          {chips.map((c) => (
            <li key={c.key}>
              <button
                type="button"
                onClick={() => setExtra((e) => ({ ...e, [c.key]: undefined }))}
                aria-label={t('activity.remove_filter', { label: c.label })}
                className="inline-flex h-8 items-center gap-1 rounded-full bg-accent-tint pr-2 pl-3 text-[14px] font-semibold text-accent-tinted"
              >
                {c.label}
                <X aria-hidden className="size-3.5" strokeWidth={2.5} />
              </button>
            </li>
          ))}
        </ul>
      )}

      <AnimatePresence>
        {pending.length > 0 && (
          <motion.div
            className="pointer-events-none fixed inset-x-0 top-[calc(52px+env(safe-area-inset-top))] z-20 flex justify-center lg:left-[var(--sidebar-w)]"
            initial={{ opacity: 0, y: -8 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -8 }}
            transition={spring.snappy}
          >
            <button
              type="button"
              onClick={showPending}
              className="pointer-events-auto inline-flex h-9 items-center gap-1.5 rounded-full bg-accent-fill px-4 text-[14px] font-semibold text-white shadow-[var(--shadow-toast)]"
            >
              <ArrowUp aria-hidden className="size-4" strokeWidth={2.5} />
              {t('activity.new', { count: pending.length })}
            </button>
          </motion.div>
        )}
      </AnimatePresence>

      <div className="mt-6">
        {audit.error && !audit.data ? (
          <ErrorState text={errorText(audit.error, t)} onRetry={() => void audit.refetch()} />
        ) : !audit.data ? (
          showSkeleton && <SkeletonRows rows={8} />
        ) : items.length === 0 ? (
          filtered ? (
            <EmptyState
              icon={History}
              title={t('activity.none')}
              action={
                <Button
                  variant="tinted"
                  size="lg"
                  onClick={() => {
                    setSeg('all')
                    setExtra({})
                  }}
                >
                  {t('activity.reset')}
                </Button>
              }
            />
          ) : (
            <EmptyState icon={History} title={t('activity.empty.title')} text={t('activity.empty.text')} />
          )
        ) : (
          <div className={audit.isPlaceholderData ? 'opacity-60 transition-opacity' : 'transition-opacity'}>
            {groups.map((g) => (
              <Section key={g.key} header={g.label}>
                <AnimatePresence initial={false}>
                  {g.items.map((a) => (
                    <motion.div key={a.id} layout="position" initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: 'auto' }} transition={spring.smooth} className="homie-sep [--row-inset:60px]">
                      <ActivityRow item={a} tools={tools} to={`/admin/activity/${a.id}`} now={now} flash={flash.has(a.id)} />
                    </motion.div>
                  ))}
                </AnimatePresence>
              </Section>
            ))}
            <div ref={sentinel} className="flex h-16 items-center justify-center">
              {isFetchingNextPage && <Spinner />}
            </div>
          </div>
        )}
      </div>

      <FilterSheet
        open={filtersOpen}
        onClose={() => setFiltersOpen(false)}
        extra={extra}
        onChange={setExtra}
        people={(people.data?.items ?? []).map((p) => ({ value: p.id, label: p.name }))}
        modules={(plugins.data?.items ?? []).map((p) => ({ value: p.id, label: moduleTitle(p, t) }))}
      />
      <EntrySheet id={entryId} onClose={closeEntry} />
    </Page>
  )
}

function insert(qc: ReturnType<typeof useQueryClient>, filter: AuditFilter, entries: AuditItem[]) {
  qc.setQueryData<InfiniteData<ApiPage<AuditItem>>>(qk.audit(filter), (d) => {
    if (!d || d.pages.length === 0) return d
    const known = new Set(d.pages.flatMap((p) => p.items.map((i) => i.id)))
    const fresh = entries.filter((e) => !known.has(e.id))
    return { ...d, pages: [{ ...d.pages[0], items: [...fresh, ...d.pages[0].items] }, ...d.pages.slice(1)] }
  })
}

function FilterSheet({
  open,
  onClose,
  extra,
  onChange,
  people,
  modules,
}: {
  open: boolean
  onClose: () => void
  extra: Extra
  onChange: (e: Extra) => void
  people: { value: string; label: string }[]
  modules: { value: string; label: string }[]
}) {
  const { t } = useTranslation()
  const ANY = '*'
  const pick = <K extends keyof Extra>(key: K, v: string) => onChange({ ...extra, [key]: v === ANY ? undefined : v })
  const section = (key: keyof Extra, header: string, options: { value: string; label: string }[]) => (
    <Section header={header}>
      <CheckList
        label={header}
        options={[{ value: ANY, label: t('activity.filter.any') }, ...options]}
        selected={[extra[key] ?? ANY]}
        onToggle={(v) => pick(key, v)}
      />
    </Section>
  )
  return (
    <Sheet open={open} onClose={onClose} title={t('activity.filters')}>
      {people.length > 1 && section('person', t('activity.filter.person'), people)}
      {modules.length > 0 && section('plugin', t('activity.filter.module'), modules)}
      {section('via', t('activity.filter.via'), [
        { value: 'chatgpt', label: t('activity.via.chatgpt') },
        { value: 'admin', label: t('activity.via.admin') },
        { value: 'cli', label: t('activity.via.cli') },
      ])}
      {section('kind', t('activity.filter.kind'), [
        { value: 'tool', label: t('activity.kind.tool') },
        { value: 'admin', label: t('activity.kind.admin') },
      ])}
      <SheetFooter>
        <Button variant="filled" size="lg" onClick={onClose}>
          {t('common.done')}
        </Button>
      </SheetFooter>
    </Sheet>
  )
}

function EntrySheet({ id, onClose }: { id?: string; onClose: () => void }) {
  const { t } = useTranslation()
  const shownId = useSticky(id)
  return (
    <Sheet open={!!id} onClose={onClose} title={t('activity.title')} hiddenTitle>
      {shownId && <EntryBody id={shownId} />}
    </Sheet>
  )
}

function EntryBody({ id }: { id: string }) {
  const { t } = useTranslation()
  const lang = currentLang()
  const tools = useToolIndex()
  const entry = useAuditItem(id)
  const a = entry.data
  if (entry.error) return <ErrorState compact text={errorText(entry.error, t)} onRetry={() => void entry.refetch()} />
  if (!a) {
    return (
      <div className="flex flex-col gap-3 py-2" aria-busy="true">
        <Skeleton className="h-7 w-2/3" />
        <Skeleton className="h-4 w-1/3" />
        <SkeletonRows rows={4} icon={false} subtitle={false} />
      </div>
    )
  }
  const args = a.args && Object.keys(a.args).length > 0 ? JSON.stringify(a.args, null, 2) : undefined
  return (
    <div className="pb-2">
      <h2 className="type-title-2">{auditTitle(a, tools, t)}</h2>
      <p className={a.ok ? 'mt-1 type-subheadline text-success-text' : 'mt-1 type-subheadline text-danger-text'}>
        {a.ok ? t('activity.ok') : t('activity.failed')}
      </p>
      {!a.ok && a.error && <p className="mt-2 type-body">{a.error}</p>}
      <Section className="mt-5">
        <Row title={t('activity.who')} trailing={a.person?.name ?? t('activity.someone')} />
        <Row title={t('activity.when')} trailing={<span className="tabular-nums">{fmtDateTime(a.at, lang)}</span>} />
        <Row title={t('activity.how')} trailing={t(`via.${a.via}`)} />
        <Row title={t('activity.action')} trailing={<code className="font-mono text-[14px]">{a.tool ?? a.action}</code>} />
        {a.durationMs > 0 && <Row title={t('activity.duration')} trailing={<span className="tabular-nums">{fmtDuration(a.durationMs, lang)}</span>} />}
      </Section>
      {args && (
        <Section header={t('activity.args')}>
          <pre className="overflow-x-auto p-4 font-mono text-[13px] leading-[18px] whitespace-pre-wrap break-all text-label">{args}</pre>
        </Section>
      )}
    </div>
  )
}
