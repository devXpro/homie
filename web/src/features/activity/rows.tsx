import { cn, currentLang, fmtRelative, fmtTime, IconTile, Row } from '@devxpro/mcp-gatekeeper-ui'
import { Wand2, XCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { ModuleTile } from '@/components/tile'
import { fmtDuration } from '@/lib/format'
import type { ActivityItem, Tool } from '@/lib/types'
import { activityTitle } from './toolIndex'

/** ActivityRow is one call: the module's icon, the action, "who · how · when", and a red cross or the duration (§8.8). */
export function ActivityRow({
  item,
  tools,
  to,
  relative,
  now,
  flash,
}: {
  item: ActivityItem
  tools: Map<string, Tool>
  to?: string
  relative?: boolean
  now: number
  flash?: boolean
}) {
  const { t } = useTranslation()
  const lang = currentLang()
  const time = relative ? fmtRelative(item.at, lang, now) : fmtTime(item.at, lang)
  // The wrapper carries the flash (the package's Row has none) and stays a separated row of its list.
  return (
    <div className={cn('fh-sep [--row-inset:60px]', flash && 'homie-flash')}>
      <Row
        to={to}
        iconInset
        leading={item.pluginId ? <ModuleTile id={item.pluginId} size="sm" /> : <IconTile icon={Wand2} color="gray" />}
        title={activityTitle(item, tools)}
        subtitle={t('activity.row', { person: item.person?.name ?? t('activity.someone'), via: t(`via.${item.via}`, { defaultValue: item.via }), time })}
        trailing={
          !item.ok ? (
            <XCircle role="img" aria-label={t('activity.failed_mark')} className="size-5 text-danger" />
          ) : item.durationMs >= 2000 ? (
            <span className="type-subheadline text-label-2 tabular-nums">{fmtDuration(item.durationMs, lang)}</span>
          ) : undefined
        }
        chevron={!!to}
      />
    </div>
  )
}
