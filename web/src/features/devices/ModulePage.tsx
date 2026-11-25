import {
  ActionSheet,
  Button,
  currentLang,
  EmptyState,
  ErrorState,
  IconTile,
  isApiError,
  Page,
  Row,
  Section,
  seg,
  Sheet,
  SheetFooter,
  Skeleton,
  SkeletonRows,
  Spinner,
  StatusDot,
  StatusPill,
  SwitchRow,
  toast,
  useDelayed,
  usePageTitleRef,
  useStepUp,
  useSticky,
} from '@devxpro/mcp-gatekeeper-ui'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ArrowDownCircle, Lamp, RotateCw, ScrollText, TriangleAlert, Trash2, Wand2 } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation, useNavigate, useParams } from 'react-router'
import { SuperadminOnly } from '@/components/superadmin'
import { ModuleTile } from '@/components/tile'
import { useJobs } from '@/features/run/jobs'
import { useRun } from '@/features/run/run'
import { OwnerOnly } from '@/features/shell/OwnerOnly'
import { useCloseSheet } from '@/features/shell/useCloseSheet'
import { homeApi, pointedAt } from '@/lib/api'
import { errorText } from '@/lib/errors'
import { pluginRemovedBus } from '@/lib/events'
import { hq, useInventory, usePlugin, useSuperadmin } from '@/lib/queries'
import { itemTone, pluginTone } from '@/lib/status'
import { groupTools, isDestructive, moduleTitle, toolDescription, toolTitle } from '@/lib/tools'
import type { InventoryItem, Job, PluginDetail, PluginSummary, Tool } from '@/lib/types'

// Module detail: header, a banner when it is not running, its devices, its actions, what it does
// in ChatGPT and Claude, and maintenance. The device sheet is the route …/items/:itemId over this page.
// Owners only: a member's devices page lists what they may use, without details.

export function ModulePage() {
  const { t } = useTranslation()
  return (
    <OwnerOnly title={t('devices.title')}>
      <ModuleScreen />
    </OwnerOnly>
  )
}

function ModuleScreen() {
  const { t } = useTranslation()
  const { pluginId = '', itemId } = useParams()
  const navigate = useNavigate()
  const plugin = usePlugin(pluginId)
  const p = plugin.data
  const showSkeleton = useDelayed(plugin.isPending)
  const title = p ? moduleTitle(p, t) : ''

  // Removed while open (SSE plugin.removed): back to the list with a toast.
  useEffect(
    () =>
      pluginRemovedBus.on((id) => {
        if (id !== pluginId) return
        toast(t('module.removed', { title: title || pluginId }))
        navigate('/devices', { replace: true })
      }),
    [pluginId, navigate, t, title],
  )

  const back = { to: '/devices', label: t('devices.title') }
  if (plugin.error && !p) {
    const missing = isApiError(plugin.error, 'not_found')
    return (
      <Page title={t('devices.title')} back={back} largeTitle={false}>
        <ErrorState
          title={missing ? t('module.not_found') : undefined}
          text={missing ? undefined : errorText(plugin.error, t)}
          onRetry={missing ? undefined : () => void plugin.refetch()}
          action={
            missing ? (
              <Button variant="tinted" size="lg" onClick={() => navigate('/devices')}>
                {t('module.back')}
              </Button>
            ) : undefined
          }
        />
      </Page>
    )
  }
  return (
    <Page title={title} back={back} largeTitle={false}>
      {!p ? (
        showSkeleton && (
          <div aria-busy="true">
            <div className="flex flex-col items-center gap-3 pt-4 pb-6">
              <Skeleton className="size-16 rounded-lg" />
              <Skeleton className="h-7 w-44" />
              <Skeleton className="h-5 w-24 rounded-full" />
            </div>
            <SkeletonRows rows={2} />
            <div className="mt-8">
              <SkeletonRows rows={3} />
            </div>
          </div>
        )
      ) : (
        <ModuleBody plugin={p} itemId={itemId} />
      )}
    </Page>
  )
}

function Header({ p, title }: { p: PluginDetail; title: string }) {
  const { t } = useTranslation()
  const ref = usePageTitleRef()
  return (
    <div className="flex flex-col items-center pt-3 pb-7 text-center">
      <ModuleTile id={p.id} size="lg" />
      <h1 ref={ref as RefObject<HTMLHeadingElement>} className="mt-3 type-title-1">
        {title}
      </h1>
      <div className="mt-2 flex items-center gap-2">
        <StatusPill tone={pluginTone(p.state)}>{t(`state.${p.state}`)}</StatusPill>
        <span className="type-footnote text-label-2 tabular-nums">{t('module.version', { version: p.version })}</span>
      </div>
    </div>
  )
}

function ModuleBody({ plugin: p, itemId }: { plugin: PluginDetail; itemId?: string }) {
  const { t } = useTranslation()
  const lang = currentLang()
  const navigate = useNavigate()
  const location = useLocation()
  const qc = useQueryClient()
  const { openRun } = useRun()
  const { openJob } = useJobs()
  const { withStepUp } = useStepUp()
  const title = moduleTitle(p, t)
  const groups = useMemo(() => groupTools(p.tools, lang), [p.tools, lang])
  const setup = groups.setup
  const closeDevice = useCloseSheet(`/devices/${p.id}`)
  const hasInventory = !!p.inventoryTool
  const inventory = useInventory(p.id, hasInventory && p.state === 'up')
  const [ability, setAbility] = useState<Tool | null>(null)
  const [removing, setRemoving] = useState(false)
  const superadmin = useSuperadmin()
  const autoSetup = useRef(false)

  // Right after installing, the module page opens its setup action by itself.
  const wantsSetup = (location.state as { setup?: boolean } | null)?.setup === true
  useEffect(() => {
    if (!wantsSetup || autoSetup.current || !groups.setup || !inventory.data) return
    autoSetup.current = true
    navigate(location.pathname, { replace: true, state: null })
    if (inventory.data.items.length === 0) openRun({ tool: groups.setup })
  }, [wantsSetup, groups.setup, inventory.data, openRun, navigate, location.pathname])

  const summary = (patch: PluginSummary) => {
    qc.setQueryData<PluginDetail>(hq.plugin(p.id), (d) => {
      if (!d) return d
      const { tools: _counts, ...rest } = patch
      return { ...d, ...rest }
    })
  }
  const restart = useMutation({
    mutationFn: () => homeApi.post<PluginSummary>(`/plugins/${seg(p.id)}/restart`, {}),
    onSuccess: (s) => {
      summary(s)
      toast(t('module.restarted', { title }), 'info')
    },
    onError: (err) => toast(errorText(err, t), 'warning'),
  })
  const enable = useMutation({
    mutationFn: (enabled: boolean) => homeApi.patch<PluginSummary>(`/plugins/${seg(p.id)}`, { enabled }),
    // Switches are optimistic: the cache changes at once and goes back if homed refuses.
    onMutate: (enabled) => {
      const before = qc.getQueryData<PluginDetail>(hq.plugin(p.id))
      qc.setQueryData<PluginDetail>(hq.plugin(p.id), (d) => d && { ...d, enabled, state: enabled ? 'starting' : 'disabled' })
      return { before }
    },
    onError: (err, _v, ctx) => {
      if (ctx?.before) qc.setQueryData(hq.plugin(p.id), ctx.before)
      toast(errorText(err, t), 'warning')
    },
    onSuccess: summary,
  })

  const update = async () => {
    const version = p.update?.latest
    const job = await withStepUp(() => homeApi.post<Job>(`/plugins/${seg(p.id)}/update`, version ? { version } : {}), t('stepup.action.update', { title })).catch((err) => {
      const running = pointedAt(err, 'jobId')
      if (running) return { id: running } as Job
      toast(errorText(err, t), 'warning')
      return undefined
    })
    if (job) openJob({ jobId: job.id, title: t('job.title.plugin.update', { title }), doneText: t('job.done.plugin.update', { title }) })
  }
  const remove = async () => {
    setRemoving(false)
    const job = await withStepUp(() => homeApi.del<Job>(`/plugins/${seg(p.id)}`), t('stepup.action.remove_module', { title })).catch((err) => {
      toast(errorText(err, t), 'warning')
      return undefined
    })
    if (job) {
      qc.setQueryData(hq.job(job.id), job)
      openJob({ jobId: job.id, title: t('job.title.plugin.remove', { title }), doneText: t('module.removed', { title }) })
    }
  }

  const items = inventory.data?.items ?? []
  const item = items.find((i) => i.id === itemId)
  const shownItem = useSticky(item)

  return (
    <>
      <Header p={p} title={title} />
      <Banner p={p} title={title} onRestart={() => restart.mutate()} restarting={restart.isPending} onEnable={() => enable.mutate(true)} />

      {hasInventory && (
        <Section header={t('module.items')} footer={(inventory.data ? items.length > 0 : (p.devices ?? 1) > 0) ? t('module.items.footer') : undefined} id="module-items">
          {p.state !== 'up' ? (
            <p className="px-4 py-3 type-body text-label-2">{t(`state.${p.state}`)}</p>
          ) : inventory.error ? (
            <div className="flex items-center gap-3 px-4 py-3">
              <TriangleAlert aria-hidden className="size-5 shrink-0 text-warning" />
              <p className="flex-1 type-body">{errorText(inventory.error, t)}</p>
              <Button variant="plain" size="sm" onClick={() => void inventory.refetch()}>
                {t('common.retry')}
              </Button>
            </div>
          ) : !inventory.data && (p.devices ?? 1) > 0 ? (
            <div aria-busy="true">
              <SkeletonRows bare rows={Math.max(1, p.devices ?? 1)} icon={false} />
            </div>
          ) : items.length === 0 ? (
            <EmptyState
              compact
              icon={Lamp}
              title={t('module.items.empty')}
              action={
                setup && (
                  <Button variant="filled" size="lg" onClick={() => openRun({ tool: setup })}>
                    {toolTitle(setup, lang)}
                  </Button>
                )
              }
            />
          ) : (
            items.map((it) => (
              <Row
                key={it.id}
                to={`/devices/${p.id}/items/${encodeURIComponent(it.id)}`}
                leading={<StatusDot tone={itemTone(it.state)} />}
                title={it.name}
                subtitle={it.subtitle}
                subtitleLines={1}
                trailing={it.state && <span className="type-subheadline text-label-2">{t(`item.${it.state}`)}</span>}
                chevron
              />
            ))
          )}
        </Section>
      )}

      {groups.moduleActions.length > 0 && (
        <Section header={t('module.actions')} id="module-actions">
          {groups.moduleActions.map((tool) => (
            <Row
              key={tool.name}
              onClick={() => openRun({ tool })}
              iconInset
              leading={<IconTile icon={isDestructive(tool) ? Trash2 : Wand2} color={isDestructive(tool) ? 'red' : 'blue'} />}
              title={toolTitle(tool, lang)}
              destructive={isDestructive(tool)}
              chevron
            />
          ))}
        </Section>
      )}

      {groups.assistant.length > 0 && (
        <Section header={t('module.abilities')} id="module-abilities">
          {groups.assistant.map((tool) => (
            <Row key={tool.name} onClick={() => setAbility(tool)} title={toolTitle(tool, lang)} subtitle={toolDescription(tool, lang)} chevron />
          ))}
        </Section>
      )}

      <Section header={t('module.maintenance')} id="module-maintenance">
        {p.update &&
          (superadmin ? (
            <Row
              onClick={() => void update()}
              iconInset
              leading={<IconTile icon={ArrowDownCircle} color="blue" />}
              title={t('module.update', { version: p.update.latest })}
              chevron
            />
          ) : (
            <SuperadminOnly title={t('module.update', { version: p.update.latest })} leading={<IconTile icon={ArrowDownCircle} color="gray" />} />
          ))}
        <Row to={`/devices/${p.id}/logs`} iconInset leading={<IconTile icon={ScrollText} color="gray" />} title={t('module.logs')} chevron />
        <Row
          onClick={() => restart.mutate()}
          disabled={restart.isPending || !p.enabled}
          iconInset
          leading={<IconTile icon={RotateCw} color="gray" />}
          title={t('module.restart')}
          trailing={restart.isPending ? <Spinner size="sm" /> : undefined}
        />
        <SwitchRow title={t('module.enabled')} checked={p.enabled} onChange={(v) => enable.mutate(v)} />
        {superadmin ? (
          <Row onClick={() => setRemoving(true)} title={t('module.remove', { title })} destructive centered />
        ) : (
          <SuperadminOnly title={t('module.remove', { title })} />
        )}
      </Section>

      <ActionSheet
        open={removing}
        title={t('module.remove.confirm', { title })}
        action={t('common.delete')}
        onConfirm={() => void remove()}
        onCancel={() => setRemoving(false)}
      />
      <AbilitySheet tool={ability} onClose={() => setAbility(null)} onTry={(tool) => {
        setAbility(null)
        openRun({ tool })
      }} />
      <DeviceSheet
        open={!!item}
        item={shownItem}
        actions={groups.itemActions}
        onClose={closeDevice}
        onAction={(tool, it) => openRun({ tool, prefill: tool.target ? { [tool.target]: it.id } : {}, hideTarget: true })}
      />
    </>
  )
}

function Banner({ p, title, onRestart, restarting, onEnable }: { p: PluginDetail; title: string; onRestart: () => void; restarting: boolean; onEnable: () => void }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  if (p.state === 'up') return null
  return (
    <Section className="mb-8">
      <div className="flex gap-3 p-4" role={p.state === 'failing' ? 'alert' : 'status'}>
        {p.state === 'starting' ? (
          <Spinner size="md" className="mt-0.5" />
        ) : (
          <TriangleAlert aria-hidden className={p.state === 'failing' ? 'mt-0.5 size-6 shrink-0 text-danger' : 'mt-0.5 size-6 shrink-0 text-gray'} />
        )}
        <div className="min-w-0 flex-1">
          {p.state === 'failing' && (
            <>
              <p className="type-headline">{t('module.failing.title', { title })}</p>
              <p className="mt-0.5 type-subheadline text-label-2">{t('module.failing.text')}</p>
              {p.lastError && <p className="mt-2 font-mono text-[12px] leading-4 break-words text-label-2">{p.lastError}</p>}
              <div className="mt-3 flex flex-wrap gap-2">
                <Button variant="tinted" size="sm" loading={restarting} onClick={onRestart}>
                  {t('module.restart')}
                </Button>
                <Button variant="plain" size="sm" onClick={() => navigate(`/devices/${p.id}/logs`)}>
                  {t('module.logs')}
                </Button>
              </div>
            </>
          )}
          {p.state === 'starting' && <p className="type-body">{t('module.starting')}</p>}
          {p.state === 'stopped' && (
            <>
              <p className="type-body">{t('module.stopped')}</p>
              <Button className="mt-3" variant="tinted" size="sm" loading={restarting} onClick={onRestart}>
                {t('module.start')}
              </Button>
            </>
          )}
          {p.state === 'disabled' && (
            <>
              <p className="type-body">{t('module.disabled')}</p>
              <Button className="mt-3" variant="tinted" size="sm" onClick={onEnable}>
                {t('module.enable')}
              </Button>
            </>
          )}
        </div>
      </div>
    </Section>
  )
}

function AbilitySheet({ tool, onClose, onTry }: { tool: Tool | null; onClose: () => void; onTry: (t: Tool) => void }) {
  const { t } = useTranslation()
  const lang = currentLang()
  const shown = useSticky(tool)
  return (
    <Sheet open={!!tool} onClose={onClose} title={shown ? toolTitle(shown, lang) : ''}>
      {shown && (
        <>
          <p className="type-body">{toolDescription(shown, lang)}</p>
          {lang !== 'en' && shown.i18n?.[lang]?.description && <p className="mt-3 type-footnote text-label-2">{shown.description}</p>}
          <SheetFooter>
            <Button variant="tinted" size="lg" onClick={() => onTry(shown)}>
              {t('module.try')}
            </Button>
          </SheetFooter>
        </>
      )}
    </Sheet>
  )
}

function DeviceSheet({
  open,
  item,
  actions,
  onClose,
  onAction,
}: {
  open: boolean
  item?: InventoryItem
  actions: Tool[]
  onClose: () => void
  onAction: (tool: Tool, item: InventoryItem) => void
}) {
  const { t } = useTranslation()
  const lang = currentLang()
  return (
    <Sheet open={open} onClose={onClose} title={item?.name ?? ''}>
      {item && (
        <>
          <div className="-mt-1 mb-5 flex flex-wrap items-center gap-2">
            {item.state && <StatusPill tone={itemTone(item.state)}>{t(`item.${item.state}`)}</StatusPill>}
            {item.subtitle && <span className="type-subheadline text-label-2">{item.subtitle}</span>}
          </div>
          {actions.length === 0 ? (
            <p className="py-4 type-body text-label-2">{t('module.item.no_actions')}</p>
          ) : (
            <Section>
              {actions.map((tool) => (
                <Row
                  key={tool.name}
                  onClick={() => onAction(tool, item)}
                  iconInset
                  leading={<IconTile icon={isDestructive(tool) ? Trash2 : Wand2} color={isDestructive(tool) ? 'red' : 'blue'} />}
                  title={toolTitle(tool, lang)}
                  destructive={isDestructive(tool)}
                  chevron
                />
              ))}
            </Section>
          )}
        </>
      )}
    </Sheet>
  )
}
