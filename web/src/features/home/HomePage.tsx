import type { TFunction } from 'i18next'
import { ArrowDownCircle, ChevronRight, CloudOff, Globe, Lamp, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { Page } from '@/components/page'
import { ButtonLink } from '@/components/ui/button'
import { EmptyState, ErrorState, Skeleton, SkeletonRows } from '@/components/ui/feedback'
import { Row, Section } from '@/components/ui/list'
import { StatusDot, StatusOrb } from '@/components/ui/status'
import { IconTile, ModuleTile } from '@/components/ui/tile'
import { ActivityRow } from '@/features/activity/rows'
import { useToolIndex } from '@/features/activity/toolIndex'
import { cn } from '@/lib/cn'
import { useDelayed, useNow } from '@/lib/hooks'
import { useOverview } from '@/lib/queries'
import { pluginTone, relayLook, relayTone, TONE_STATUS } from '@/lib/status'
import { moduleTitle } from '@/lib/tools'
import type { Attention, PluginSummary, RelayStatus } from '@/lib/types'

// Дом (DESIGN.md §8.4): how the home is, what needs the person, the modules, and what ChatGPT did lately.

export function HomePage() {
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
      <Hero relay={data.relay} />
      {data.attention.length > 0 && <AttentionSection items={data.attention} plugins={data.plugins} />}
      {data.plugins.length === 0 ? (
        <EmptyState
          icon={Lamp}
          title={t('home.empty.title')}
          text={t('home.empty.text')}
          action={
            <ButtonLink to="/admin/devices/add" preventScrollReset variant="filled" size="lg">
              {t('home.empty.button')}
            </ButtonLink>
          }
        />
      ) : (
        <Section header={t('home.devices')} id="home-devices">
          {data.plugins.map((p) => (
            <Row
              key={p.id}
              to={`/admin/devices/${p.id}`}
              iconInset
              leading={<ModuleTile id={p.id} size="sm" />}
              title={moduleTitle(p, t)}
              subtitle={<ModuleSubtitle p={p} />}
              chevron
            />
          ))}
          <Row to="/admin/devices/add" iconInset accent leading={<AddGlyph />} title={t('home.devices.add')} />
        </Section>
      )}
      {data.plugins.length > 0 && (
        <Section header={t('home.recent')} id="home-recent">
          {data.recent.length === 0 ? (
            <p className="px-4 py-3 type-body text-label-2">{t('home.recent.empty')}</p>
          ) : (
            <>
              {data.recent.map((a) => (
                <ActivityRow key={a.id} item={a} tools={tools} to={`/admin/activity/${a.id}`} relative now={now} />
              ))}
              <Row to="/admin/activity" title={t('home.recent.all')} accent chevron />
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

function Hero({ relay }: { relay: RelayStatus }) {
  const { t } = useTranslation()
  const look = relayLook(relay)
  const tone = relayTone(look)
  return (
    <Link
      to="/admin/settings/server"
      className={cn(
        'homie-hero flex items-center gap-4 rounded-xl p-5 transition-[filter] active:brightness-95',
        TONE_STATUS[tone],
      )}
    >
      <StatusOrb tone={tone} breathing={look === 'connecting'} />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="type-title-2">{t(`relay.${look}`)}</span>
        <span className="type-subheadline text-label-2">{t(`home.hero.${look}`)}</span>
      </span>
      <ChevronRight aria-hidden className="size-5 shrink-0 text-label-3" strokeWidth={2.5} />
    </Link>
  )
}

function attentionRow(a: Attention, plugins: PluginSummary[], t: TFunction) {
  const plugin = 'target' in a ? plugins.find((p) => p.id === a.target) : undefined
  const title = plugin ? moduleTitle(plugin, t) : 'target' in a ? a.target : ''
  switch (a.kind) {
    case 'relay_offline':
      return { key: a.kind, to: '/admin/settings/server', icon: <IconTile icon={CloudOff} color="red" />, title: t('attention.relay_offline.title'), subtitle: t('attention.relay_offline.subtitle') }
    case 'relay_unreachable':
      return { key: a.kind, to: '/admin/settings/server', icon: <IconTile icon={Globe} color="orange" />, title: t('attention.relay_unreachable.title'), subtitle: t('attention.relay_unreachable.subtitle') }
    case 'plugin_failing':
      return { key: `${a.kind}-${a.target}`, to: `/admin/devices/${a.target}`, icon: <ModuleTile id={a.target} size="sm" className="bg-danger" />, title: t('attention.plugin_failing.title', { title }), subtitle: t('attention.plugin_failing.subtitle') }
    case 'plugin_update':
      return { key: `${a.kind}-${a.target}`, to: `/admin/devices/${a.target}`, icon: <IconTile icon={ArrowDownCircle} color="blue" />, title: t('attention.plugin_update.title', { title }), subtitle: t('attention.plugin_update.subtitle', { version: a.version }) }
    case 'hub_update':
      return { key: a.kind, to: '/admin/settings/about', icon: <IconTile icon={ArrowDownCircle} color="blue" />, title: t('attention.hub_update.title'), subtitle: t('attention.hub_update.subtitle', { version: a.version }) }
    default:
      return undefined
  }
}

function AttentionSection({ items, plugins }: { items: Attention[]; plugins: PluginSummary[] }) {
  const { t } = useTranslation()
  const [all, setAll] = useState(false)
  const rows = items.map((a) => attentionRow(a, plugins, t)).filter((r) => r !== undefined)
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
