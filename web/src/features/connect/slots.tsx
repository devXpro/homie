import { Button, CardDivider, quote, SegmentedControl, StatusPill, useLang, type CardSlotProps } from '@devxpro/mcp-gatekeeper-ui'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DomainSheet } from '@/features/tunnel/QuickBanner'
import { useBootstrap } from '@/lib/bootstrap'
import { useTunnel } from '@/lib/queries'
import { tunnelLook, tunnelTone } from '@/lib/status'

// Homie's parts of the package's connect page (/connect): the home is the one resource, at https://<host>/mcp
// (STEP2 §2.1 #4). The card gets the connection's state, the trial-address note, and the steps for ChatGPT and Claude.

/** TunnelBadge: is the home reachable now (owners), and «для пробы» behind a quick tunnel. */
export function TunnelBadge({ owner }: CardSlotProps) {
  const { t } = useTranslation()
  const boot = useBootstrap()
  const tunnel = useTunnel(owner)
  const quick = (tunnel.data?.mode ?? boot?.tunnel.mode) === 'quick'
  if (!tunnel.data) return quick ? <StatusPill tone="warning">{t('quick.badge')}</StatusPill> : null
  const look = tunnelLook(tunnel.data)
  if (quick && look === 'connected') return <StatusPill tone="warning">{t('quick.badge')}</StatusPill>
  return <StatusPill tone={tunnelTone(look)}>{t(`tunnel.${look}`)}</StatusPill>
}

/** QuickNote warns that a trial address changes, and offers the move to an own domain (owners). */
export function QuickNote({ owner }: CardSlotProps) {
  const { t } = useTranslation()
  const boot = useBootstrap()
  const [moving, setMoving] = useState(false)
  if (boot?.tunnel.mode !== 'quick') return null
  return (
    <div className="mt-2">
      <p className="type-footnote text-warning-text">{t('connect.quick')}</p>
      {owner && (
        <>
          <Button className="mt-1.5 -ml-1" variant="plain" size="sm" onClick={() => setMoving(true)}>
            {t('quick.move')}
          </Button>
          <DomainSheet open={moving} onClose={() => setMoving(false)} />
        </>
      )}
    </div>
  )
}

type Assistant = 'chatgpt' | 'claude'

/** ConnectSteps lists what to do in each assistant, numbered, with the home's name as the connector's. */
export function ConnectSteps({ resource }: CardSlotProps) {
  const { t } = useTranslation()
  const lang = useLang()
  const [assistant, setAssistant] = useState<Assistant>('chatgpt')
  const home = quote(resource.name, lang)
  return (
    <>
      <CardDivider />
      <div className="px-4 pt-3 pb-4">
        <h3 className="px-1 pb-2 type-footnote text-label-2">{t('connect.steps')}</h3>
        <SegmentedControl<Assistant>
          label={t('connect.steps.label')}
          value={assistant}
          onChange={setAssistant}
          options={[
            { value: 'chatgpt', label: 'ChatGPT' },
            { value: 'claude', label: 'Claude' },
          ]}
        />
        <ol className="mt-4 flex flex-col gap-3" aria-label={t('connect.steps')}>
          {[1, 2, 3, 4].map((n) => (
            <li key={`${assistant}-${n}`} className="flex gap-3">
              <span aria-hidden className="inline-flex size-6 shrink-0 items-center justify-center rounded-full bg-fill text-[13px] font-semibold text-label-2 tabular-nums">
                {n}
              </span>
              <p className="min-w-0 flex-1 pt-0.5 type-subheadline">{t(`connect.steps.${assistant}.${n}`, { home })}</p>
            </li>
          ))}
        </ol>
      </div>
    </>
  )
}
