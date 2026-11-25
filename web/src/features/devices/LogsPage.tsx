import { Button, copyText, EmptyState, ErrorState, Page, SkeletonRows, Switch, toast, useDelayed, useLiveMode } from '@devxpro/mcp-gatekeeper-ui'
import { useQueryClient } from '@tanstack/react-query'
import { useWindowVirtualizer } from '@tanstack/react-virtual'
import { ScrollText } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams } from 'react-router'
import { OwnerOnly } from '@/features/shell/OwnerOnly'
import { errorText } from '@/lib/errors'
import { usePluginLogStream } from '@/lib/events'
import { usePlugin, usePluginLogs } from '@/lib/queries'
import { moduleTitle } from '@/lib/tools'

// Module log: the last 1000 stderr lines, live; "Следить" keeps the end in view and turns off
// when the person scrolls up. Behind a quick tunnel the lines are fetched again every few seconds instead.

export function LogsPage() {
  const { t } = useTranslation()
  return (
    <OwnerOnly title={t('module.logs')}>
      <LogsScreen />
    </OwnerOnly>
  )
}

function LogsScreen() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const live = useLiveMode()
  const { pluginId = '' } = useParams()
  const plugin = usePlugin(pluginId)
  const logs = usePluginLogs(pluginId)
  const [follow, setFollow] = useState(true)
  const showSkeleton = useDelayed(logs.isPending)
  const lines = logs.data?.lines ?? []
  const title = plugin.data ? moduleTitle(plugin.data, t) : pluginId
  usePluginLogStream(qc, pluginId, live)

  return (
    <Page
      title={t('module.logs')}
      back={{ to: `/devices/${pluginId}`, label: title }}
      trailing={
        <Button
          variant="plain"
          size="sm"
          disabled={lines.length === 0}
          onClick={async () => {
            if (await copyText(lines.join('\n'))) toast(t('common.copied'))
          }}
        >
          {t('common.copy')}
        </Button>
      }
    >
      <div className="mb-4 flex min-h-11 items-center justify-between rounded-lg bg-surface-1 px-4">
        <span id="follow-label" className="type-body">
          {t('module.logs.follow')}
        </span>
        <Switch checked={follow} onChange={setFollow} aria-labelledby="follow-label" />
      </div>
      {logs.error && !logs.data ? (
        <ErrorState text={errorText(logs.error, t)} onRetry={() => void logs.refetch()} />
      ) : !logs.data ? (
        showSkeleton && <SkeletonRows rows={6} icon={false} subtitle={false} />
      ) : lines.length === 0 ? (
        <EmptyState icon={ScrollText} title={t('module.logs.empty')} />
      ) : (
        <LogView lines={lines} follow={follow} onUserScrollUp={() => setFollow(false)} />
      )}
    </Page>
  )
}

/** LogView is monospace 12/16 on --surface-1, wrapped, virtualized above 300 lines. */
function LogView({ lines, follow, onUserScrollUp }: { lines: string[]; follow: boolean; onUserScrollUp: () => void }) {
  const listRef = useRef<HTMLOListElement>(null)
  const [offset, setOffset] = useState(0)
  useLayoutEffect(() => {
    const el = listRef.current
    if (!el) return
    const measure = () => setOffset(el.getBoundingClientRect().top + window.scrollY)
    measure()
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
  }, [])
  const virtual = lines.length > 300
  const v = useWindowVirtualizer({ count: lines.length, estimateSize: () => 16, overscan: 30, scrollMargin: offset, enabled: virtual })

  // Following: keep the newest line in view.
  useEffect(() => {
    if (!follow) return
    window.scrollTo({ top: document.documentElement.scrollHeight })
  }, [follow, lines.length])

  // Scrolling up turns following off.
  useEffect(() => {
    let last = window.scrollY
    const onScroll = () => {
      const y = window.scrollY
      if (y < last - 4) onUserScrollUp()
      last = y
    }
    window.addEventListener('scroll', onScroll, { passive: true })
    return () => window.removeEventListener('scroll', onScroll)
  }, [onUserScrollUp])

  const cls = 'px-3 font-mono text-[12px] leading-4 whitespace-pre-wrap break-all text-label'
  return (
    <div className="rounded-lg bg-surface-1 py-3">
      {virtual ? (
        <ol ref={listRef} className="relative" style={{ height: v.getTotalSize() }}>
          {v.getVirtualItems().map((row) => (
            <li
              key={row.key}
              data-index={row.index}
              ref={v.measureElement}
              className={`absolute inset-x-0 top-0 ${cls}`}
              style={{ transform: `translateY(${row.start - v.options.scrollMargin}px)` }}
            >
              {lines[row.index]}
            </li>
          ))}
        </ol>
      ) : (
        <ol ref={listRef}>
          {lines.map((l, i) => (
            <li key={i} className={cls}>
              {l}
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}
