import {
  Button,
  CheckList,
  currentLang,
  dayDiff,
  EmptyState,
  ErrorState,
  fmtDate,
  fmtDateTime,
  IconButton,
  Page,
  Row,
  Section,
  SegmentedControl,
  Sheet,
  SheetFooter,
  Skeleton,
  SkeletonRows,
  Spinner,
  spring,
  useAccount,
  useDelayed,
  useNow,
  useSticky,
} from '@devxpro/mcp-gatekeeper-ui'
import { useQueryClient, type InfiniteData } from '@tanstack/react-query'
import { ArrowUp, History, SlidersHorizontal, X } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams } from 'react-router'
import { useCloseSheet } from '@/features/shell/useCloseSheet'
import { errorText } from '@/lib/errors'
import { activityBus } from '@/lib/events'
import { fmtDuration } from '@/lib/format'
import { hq, useActivity, useActivityItem, useOwner, usePlugins, type ActivityFilter } from '@/lib/queries'
import { moduleTitle } from '@/lib/tools'
import type { ActivityItem, Page as ApiPage, HomeAccount, Via } from '@/lib/types'
import { ActivityRow } from './rows'
import { activityTitle, useToolIndex } from './toolIndex'

// Журнал (DESIGN.md §8.8): every tool call — by the assistants, from the admin and the terminal — with filters as
// removable chips, days, infinite scroll, and new calls arriving live (a flash at the top, or a capsule when scrolled
// down). Sign-ins and admin actions are the gatekeeper's audit: People → Changes and sign-ins. A member sees their
// own calls only (the API filters).

type Seg = 'assistant' | 'all' | 'failed'
interface Extra {
  person?: string
  plugin?: string
  via?: Via
}

// The filter survives leaving the tab and coming back (module state, not URL: the tab stack keeps paths only).
let saved: { seg: Seg; extra: Extra } = { seg: 'assistant', extra: {} }

const ASSISTANTS = 'chatgpt,claude'

function toFilter(seg: Seg, extra: Extra): ActivityFilter {
  const f: ActivityFilter = seg === 'assistant' ? { via: ASSISTANTS } : seg === 'failed' ? { status: 'failed' } : {}
  if (extra.via) f.via = extra.via
  if (extra.person) f.person = extra.person
  if (extra.plugin) f.plugin = extra.plugin
  return f
}

function matches(a: ActivityItem, f: ActivityFilter) {
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
  const owner = useOwner()
  const { entryId } = useParams()
  const closeEntry = useCloseSheet('/activity')
  const [seg, setSeg] = useState<Seg>(saved.seg)
  const [extra, setExtra] = useState<Extra>(saved.extra)
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [pending, setPending] = useState<ActivityItem[]>([])
  const [flash, setFlash] = useState<Set<string>>(new Set())
  const filter = useMemo(() => toFilter(seg, extra), [seg, extra])
  const activity = useActivity(filter)
  const tools = useToolIndex()
  // The devices to filter by: an owner's installed plugins, a member's own devices (GET /api/account).
  const plugins = usePlugins(owner)
  const account = useAccount<HomeAccount>()
  const devices = owner ? (plugins.data?.items ?? []) : (account.data?.devices ?? [])
  const now = useNow()
  const showSkeleton = useDelayed(activity.isPending)
  const sentinel = useRef<HTMLDivElement>(null)

  useEffect(() => {
    saved = { seg, extra }
  }, [seg, extra])

  // New calls: at the top they slide in and flash; further down a capsule offers them (DESIGN.md §8.8).
  useEffect(
    () =>
      activityBus.on((a) => {
        if (!matches(a, filter)) {
          void qc.invalidateQueries({ queryKey: hq.activityAll, refetchType: 'none' })
          return
        }
        if (window.scrollY < 120) {
          insert(qc, filter, [a])
          setFlash((s) => new Set(s).add(a.id))
          setTimeout(
            () =>
              setFlash((s) => {
                const n = new Set(s)
                n.delete(a.id)
                return n
              }),
            1300,
          )
        } else {
          setPending((p) => [a, ...p])
        }
      }),
    [filter, qc],
  )

  // Infinite scroll by cursor.
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = activity
  useEffect(() => {
    const el = sentinel.current
    if (!el) return
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting) && hasNextPage && !isFetchingNextPage) void fetchNextPage()
      },
      { rootMargin: '400px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])

  const items = useMemo(() => activity.data?.pages.flatMap((p) => p.items) ?? [], [activity.data])
  const groups = useMemo(() => {
    const out: { key: string; label: string; items: ActivityItem[] }[] = []
    for (const a of items) {
      const d = dayDiff(new Date(a.at), new Date(now))
      const label = d === 0 ? t('activity.today') : d === -1 ? t('activity.yesterday') : fmtDate(a.at, lang, new Date(now))
      const last = out[out.length - 1]
      if (last && last.label === label) last.items.push(a)
      else out.push({ key: `${label}-${a.id}`, label, items: [a] })
    }
    return out
  }, [items, now, lang, t])

  const people = (account.data?.members ?? []).map((m) => ({ value: m.id, label: m.name }))
  const chips: { key: keyof Extra; label: string }[] = []
  if (extra.person) chips.push({ key: 'person', label: people.find((p) => p.value === extra.person)?.label ?? extra.person })
  if (extra.plugin) {
    const p = devices.find((x) => x.id === extra.plugin)
    chips.push({ key: 'plugin', label: p ? moduleTitle(p, t) : extra.plugin })
  }
  if (extra.via) chips.push({ key: 'via', label: t(`activity.via.${extra.via}`) })
  // The default Ассистенты view counts as unfiltered: a new home is simply empty (DESIGN.md §8.8).
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
        {activity.error && !activity.data ? (
          <ErrorState text={errorText(activity.error, t)} onRetry={() => void activity.refetch()} />
        ) : !activity.data ? (
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
            <EmptyState icon={History} title={t('activity.empty.title')} text={t(owner ? 'activity.empty.text' : 'activity.empty.mine')} />
          )
        ) : (
          <div className={activity.isPlaceholderData ? 'opacity-60 transition-opacity' : 'transition-opacity'}>
            {groups.map((g) => (
              <Section key={g.key} header={g.label}>
                <AnimatePresence initial={false}>
                  {g.items.map((a) => (
                    <motion.div
                      key={a.id}
                      layout="position"
                      initial={{ opacity: 0, height: 0 }}
                      animate={{ opacity: 1, height: 'auto' }}
                      transition={spring.smooth}
                      className="fh-sep [--row-inset:60px]"
                    >
                      <ActivityRow item={a} tools={tools} to={`/activity/${a.id}`} now={now} flash={flash.has(a.id)} />
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
        {owner && <p className="mt-2 px-1 type-footnote text-label-2">{t('activity.security')}</p>}
      </div>

      <FilterSheet
        open={filtersOpen}
        onClose={() => setFiltersOpen(false)}
        extra={extra}
        onChange={setExtra}
        people={owner ? people : []}
        modules={devices.map((p) => ({ value: p.id, label: moduleTitle(p, t) }))}
      />
      <EntrySheet id={entryId} onClose={closeEntry} />
    </Page>
  )
}

function insert(qc: ReturnType<typeof useQueryClient>, filter: ActivityFilter, entries: ActivityItem[]) {
  qc.setQueryData<InfiniteData<ApiPage<ActivityItem>>>(hq.activity(filter), (d) => {
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
        { value: 'claude', label: t('activity.via.claude') },
        { value: 'admin', label: t('activity.via.admin') },
        { value: 'cli', label: t('activity.via.cli') },
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
  const entry = useActivityItem(id)
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
      <h2 className="type-title-2">{activityTitle(a, tools)}</h2>
      <p className={a.ok ? 'mt-1 type-subheadline text-success-text' : 'mt-1 type-subheadline text-danger-text'}>
        {a.ok ? t('activity.ok') : t('activity.failed')}
      </p>
      {!a.ok && a.error && <p className="mt-2 type-body">{a.error}</p>}
      <Section className="mt-5">
        <Row title={t('activity.who')} trailing={a.person?.name ?? t('activity.someone')} />
        <Row title={t('activity.when')} trailing={<span className="tabular-nums">{fmtDateTime(a.at, lang)}</span>} />
        <Row title={t('activity.how')} trailing={t(`via.${a.via}`, { defaultValue: a.via })} />
        <Row title={t('activity.action')} trailing={<code className="font-mono text-[14px]">{a.tool}</code>} />
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
