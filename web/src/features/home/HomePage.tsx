import {
  ButtonLink,
  cn,
  currentLang,
  EmptyState,
  ErrorState,
  fmtRelative,
  IconTile,
  Page,
  Row,
  Section,
  Skeleton,
  SkeletonRows,
  StatusDot,
  TONE_STATUS,
  useDelayed,
  useMe,
  useNow,
} from '@devxpro/mcp-gatekeeper-ui'
import type { TFunction } from 'i18next'
import { ArrowDownCircle, ChevronRight, CloudOff, Globe, Lamp, Plus, ShieldAlert, Sparkles } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, Navigate } from 'react-router'
import { StatusOrb } from '@/components/status'
import { ModuleTile } from '@/components/tile'
import { ActivityRow } from '@/features/activity/rows'
import { useToolIndex } from '@/features/activity/toolIndex'
import { QuickBanner } from '@/features/tunnel/QuickBanner'
import { useOverview } from '@/lib/queries'
import { needsOwner, pluginTone, tunnelLook, tunnelTone } from '@/lib/status'
import { moduleTitle } from '@/lib/tools'
import type { Attention, PluginSummary, TunnelStatus } from '@/lib/types'

// Дом (DESIGN.md §8.4): how the home is reachable, what needs the owner, the modules, the assistants, and what
// ChatGPT and Claude did lately. A member has no home page of their own: the connect page is theirs.

export function HomePage() {
  const me = useMe().data
  if (me && me.person.role !== 'owner') return <Navigate to="/connect" replace />
  return <OwnerHome />
}

function OwnerHome() {
  const { t } = useTranslation()
  const overview = useOverview()
  const tools = useToolIndex()
  const now = useNow()
  const showSkeleton = useDelayed(overview.isPending)
  const data = overview.data
  const title = data?.home.name ?? ''

  if (overview.error && !data) {
    return (
      <Page title={t('tab.home')}>
        <ErrorState text={t('error.generic')} onRetry={() => void overview.refetch()} />
      </Page>
    )
  }
  if (!data) {
    return (
      <Page title={title || t('tab.home')}>
        {showSkeleton && (
          <div aria-busy="true">
            <Skeleton className="h-[116px] rounded-xl" />
            <div className="mt-8">
              <SkeletonRows rows={3} />
            </div>
            <div className="mt-8">
              <SkeletonRows rows={3} />
            </div>
          </div>
        )}
      </Page>
    )
  }

  return (
    <Page title={title}>
      <Hero tunnel={data.tunnel} />
      {data.tunnel.mode === 'quick' && <QuickBanner className="mt-4" />}
      {needsOwner(data.attention).length > 0 && <AttentionSection items={data.attention} plugins={data.plugins} />}
      {data.plugins.length === 0 ? (
        <EmptyState
          icon={Lamp}
          title={t('home.empty.title')}
          text={t('home.empty.text')}
          action={
            <ButtonLink to="/devices/add" preventScrollReset variant="filled" size="lg">
              {t('home.empty.button')}
            </ButtonLink>
          }
        />
      ) : (
        <Section header={t('home.devices')} id="home-devices">
          {data.plugins.map((p) => (
            <Row
              key={p.id}
              to={`/devices/${p.id}`}
              iconInset
              leading={<ModuleTile id={p.id} size="sm" />}
              title={moduleTitle(p, t)}
              subtitle={<ModuleSubtitle p={p} />}
              chevron
            />
          ))}
          <Row to="/devices/add" iconInset accent leading={<AddGlyph />} title={t('home.devices.add')} />
        </Section>
      )}
      <Section header={t('home.assistants')} id="home-assistants">
        <Row
          to="/connect"
          iconInset
          leading={<IconTile icon={Sparkles} color="purple" />}
          title={t('home.connect')}
          subtitle={t('home.connect.subtitle')}
          chevron
        />
      </Section>
      {data.plugins.length > 0 && (
        <Section header={t('home.recent')} id="home-recent">
          {data.recent.length === 0 ? (
            <p className="px-4 py-3 type-body text-label-2">{t('home.recent.empty')}</p>
          ) : (
            <>
              {data.recent.map((a) => (
                <ActivityRow key={a.id} item={a} tools={tools} to={`/activity/${a.id}`} relative now={now} />
              ))}
              <Row to="/activity" title={t('home.recent.all')} accent chevron />
            </>
          )}
        </Section>
      )}
    </Page>
  )
}

function AddGlyph() {
  return (
    <span aria-hidden className="inline-flex size-[30px] items-center justify-center text-accent">
      <Plus className="size-5" strokeWidth={2.4} />
    </span>
  )
}

export function ModuleSubtitle({ p }: { p: PluginSummary }) {
  const { t } = useTranslation()
  return (
    <span className="inline-flex items-center gap-1.5">
      <StatusDot tone={pluginTone(p.state)} />
      <span>
        {t(`state.${p.state}`)}
        {p.devices !== undefined && p.devices > 0 && ` · ${t('devices.count', { count: p.devices })}`}
      </span>
    </span>
  )
}

/** Hero is the status card: is the home reachable from outside, and what that means (DESIGN.md §8.4). */
function Hero({ tunnel }: { tunnel: TunnelStatus }) {
  const { t } = useTranslation()
  const look = tunnelLook(tunnel)
  const tone = tunnelTone(look)
  return (
    <Link
      to="/settings/connection"
      className={cn('fh-hero flex items-center gap-4 rounded-xl p-5 transition-[filter] active:brightness-95', TONE_STATUS[tone])}
    >
      <StatusOrb tone={tone} breathing={look === 'connecting'} />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="type-title-2">{t(`tunnel.${look}`)}</span>
        <span className="type-subheadline text-label-2">{t(`home.hero.${look}`)}</span>
      </span>
      <ChevronRight aria-hidden className="size-5 shrink-0 text-label-3" strokeWidth={2.5} />
    </Link>
  )
}

function attentionRow(a: Attention, plugins: PluginSummary[], t: TFunction, now: number) {
  const plugin = 'target' in a ? plugins.find((p) => p.id === a.target) : undefined
  const title = plugin ? moduleTitle(plugin, t) : 'target' in a ? a.target : ''
  switch (a.kind) {
    case 'tunnel_offline':
      return { key: a.kind, to: '/settings/connection', icon: <IconTile icon={CloudOff} color="red" />, title: t('attention.tunnel_offline.title'), subtitle: t('attention.tunnel_offline.subtitle') }
    case 'tunnel_unreachable':
      return { key: a.kind, to: '/settings/connection', icon: <IconTile icon={Globe} color="orange" />, title: t('attention.tunnel_unreachable.title'), subtitle: t('attention.tunnel_unreachable.subtitle') }
    case 'access_reset': {
      const subtitle = a.at ? t('attention.access_reset.subtitle.at', { time: fmtRelative(a.at, currentLang(), now) }) : t('attention.access_reset.subtitle')
      return { key: a.kind, to: '/account/audit', icon: <IconTile icon={ShieldAlert} color="red" />, title: t('attention.access_reset.title'), subtitle }
    }
    case 'plugin_failing':
      return { key: `${a.kind}-${a.target}`, to: `/devices/${a.target}`, icon: <ModuleTile id={a.target} size="sm" className="bg-danger" />, title: t('attention.plugin_failing.title', { title }), subtitle: t('attention.plugin_failing.subtitle') }
    case 'plugin_update':
      return { key: `${a.kind}-${a.target}`, to: `/devices/${a.target}`, icon: <IconTile icon={ArrowDownCircle} color="blue" />, title: t('attention.plugin_update.title', { title }), subtitle: t('attention.plugin_update.subtitle', { version: a.version }) }
    case 'hub_update':
      return { key: a.kind, to: '/settings/about', icon: <IconTile icon={ArrowDownCircle} color="blue" />, title: t('attention.hub_update.title'), subtitle: t('attention.hub_update.subtitle', { version: a.version }) }
    default:
      return undefined
  }
}

function AttentionSection({ items, plugins }: { items: Attention[]; plugins: PluginSummary[] }) {
  const { t } = useTranslation()
  const now = useNow()
  const [all, setAll] = useState(false)
  // tunnel_quick is the trial banner above, not a row.
  const rows = items.map((a) => attentionRow(a, plugins, t, now)).filter((r) => r !== undefined)
  const shown = all ? rows : rows.slice(0, 3)
  if (rows.length === 0) return null
  return (
    <Section header={t('home.attention')} id="home-attention" className="mt-8">
      {shown.map((r) => (
        <Row key={r.key} to={r.to} iconInset leading={r.icon} title={r.title} subtitle={r.subtitle} chevron />
      ))}
      {!all && rows.length > 3 && <Row onClick={() => setAll(true)} accent title={t('home.attention.more', { count: rows.length - 3 })} />}
    </Section>
  )
}
