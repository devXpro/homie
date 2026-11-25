import {
  ActionSheet,
  cn,
  CopyField,
  copyText,
  currentLang,
  Disclosure,
  ErrorState,
  fmtDateTime,
  Page,
  Rich,
  Row,
  Section,
  Skeleton,
  SkeletonRows,
  Spinner,
  toast,
  TONE_STATUS,
  useDelayed,
  useStepUp,
} from '@devxpro/mcp-gatekeeper-ui'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Copy } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { StatusOrb } from '@/components/status'
import { SuperadminOnly } from '@/components/superadmin'
import { OwnerOnly } from '@/features/shell/OwnerOnly'
import { QuickBanner } from '@/features/tunnel/QuickBanner'
import { homeApi } from '@/lib/api'
import { updateBootstrap } from '@/lib/bootstrap'
import { errorText } from '@/lib/errors'
import { hq, useSuperadmin, useTunnel } from '@/lib/queries'
import { tunnelHost, tunnelLook, tunnelTone } from '@/lib/status'
import type { Overview, TunnelStatus } from '@/lib/types'

// Связь: how the home reaches the internet through Cloudflare — the state, the address, the outside
// check — and «Переподключить», which restarts cloudflared. A quick tunnel gets a new address when it restarts, so
// there the button asks first, the page says the address is for trying, and after the restart it sends the owner to
// the home computer's first-run page.

export function TunnelPage() {
  const { t } = useTranslation()
  return (
    <OwnerOnly title={t('tunnel.title')}>
      <TunnelScreen />
    </OwnerOnly>
  )
}

function TunnelScreen() {
  const { t } = useTranslation()
  const lang = currentLang()
  const qc = useQueryClient()
  const tunnel = useTunnel()
  const { withStepUp } = useStepUp()
  const showSkeleton = useDelayed(tunnel.isPending)
  const [confirming, setConfirming] = useState(false)
  const [moved, setMoved] = useState(false)
  const superadmin = useSuperadmin()
  const s = tunnel.data

  const apply = (next: TunnelStatus) => {
    qc.setQueryData(hq.tunnel, next)
    qc.setQueryData<Overview>(hq.overview, (o) => o && { ...o, tunnel: next })
    updateBootstrap({ tunnel: { mode: next.mode, url: next.url } })
  }
  const check = useMutation({
    mutationFn: () => homeApi.post<TunnelStatus>('/tunnel/check', {}),
    onSuccess: apply,
    onError: (err) => toast(errorText(err, t), 'warning'),
  })
  const reconnect = useMutation({
    mutationFn: () => withStepUp(() => homeApi.post<TunnelStatus>('/tunnel/reconnect', {}), t('stepup.action.reconnect')),
    onSuccess: (next) => {
      if (!next) return
      apply(next)
      // A quick tunnel comes back at a new address, where this sign-in does not count: the owner continues on the
      // home computer's first-run page, which reopens for it (docs/ADMIN-API.md §4).
      if (next.mode === 'quick') setMoved(true)
      else toast(t('tunnel.reconnect.done'), 'info')
    },
    onError: (err) => toast(errorText(err, t), 'warning'),
  })

  const back = { to: '/settings', label: t('settings.title') }
  if (moved) {
    return (
      <Page title={t('tunnel.title')} back={back}>
        <div className="flex flex-col items-center gap-4 py-6 text-center" role="status">
          <StatusOrb tone="warning" breathing />
          <p className="type-title-3 text-balance">{t('lan.moved.title')}</p>
          <p className="type-callout text-balance text-label-2">{t('tunnel.reconnect.quick.after')}</p>
          <CopyField className="w-full text-left" value="http://localhost:8090/firstrun" label={t('lan.moved.address')} />
        </div>
      </Page>
    )
  }
  if (tunnel.error && !s) {
    return (
      <Page title={t('tunnel.title')} back={back}>
        <ErrorState text={errorText(tunnel.error, t)} onRetry={() => void tunnel.refetch()} />
      </Page>
    )
  }
  if (!s) {
    return (
      <Page title={t('tunnel.title')} back={back}>
        {showSkeleton && (
          <>
            <Skeleton className="h-[132px] rounded-xl" />
            <div className="mt-8">
              <SkeletonRows rows={4} icon={false} subtitle={false} />
            </div>
          </>
        )}
      </Page>
    )
  }

  const look = tunnelLook(s)
  const tone = tunnelTone(look)
  const quick = s.mode === 'quick'
  const host = tunnelHost(s)
  // The outside check: passing once connected; failing while cloudflared is down or the probe reports an error; else
  // it is still on its way.
  const outside =
    s.state === 'connected' ? (
      <span className="text-success-text">{t('tunnel.check.ok')}</span>
    ) : s.state === 'failing' || s.state === 'stopped' || (s.state === 'probing' && s.lastError) ? (
      <span className="text-warning-text">{t('tunnel.check.failed')}</span>
    ) : (
      t('tunnel.check.pending')
    )
  return (
    <Page title={t('tunnel.title')} back={back}>
      <div className={cn('fh-hero flex flex-col items-center rounded-xl px-5 py-6 text-center', TONE_STATUS[tone])}>
        <StatusOrb tone={tone} breathing={look === 'connecting'} />
        <p className="mt-3 type-title-2">{t(`tunnel.${look}`)}</p>
        {host && <p className="mt-0.5 type-subheadline break-all text-label-2">{host}</p>}
      </div>

      {quick && <QuickBanner className="mt-4" />}

      {s.mode === 'off' ? (
        <p className="mt-6 px-1 type-body text-label-2">
          <Rich text={t('tunnel.not_configured.text')} />
        </p>
      ) : (
        <>
          <Section className="mt-8">
            {s.url && (
              <Row
                title={t('tunnel.address')}
                trailing={
                  <button
                    type="button"
                    onClick={async () => {
                      if (await copyText(s.url ?? '')) toast(t('common.copied'))
                    }}
                    className="inline-flex max-w-[14rem] items-center gap-1.5 text-accent-text"
                    aria-label={`${t('common.copy')}: ${s.url}`}
                  >
                    <span className="truncate">{host}</span>
                    <Copy aria-hidden className="size-4 shrink-0" />
                  </button>
                }
              />
            )}
            <Row title={t('tunnel.mode')} trailing={<span className="type-body text-label-2">{t(`tunnel.mode.${s.mode}`)}</span>} />
            {s.since && s.state === 'connected' && (
              <Row title={t('tunnel.since')} trailing={<span className="tabular-nums text-label-2">{fmtDateTime(s.since, lang)}</span>} />
            )}
            <Row title={t('tunnel.check')} subtitle={outside} />
            <Row
              onClick={() => check.mutate()}
              disabled={check.isPending}
              accent
              title={t('tunnel.check_now')}
              trailing={check.isPending ? <Spinner size="sm" /> : undefined}
            />
          </Section>
          {s.lastError && (
            <div className="mt-4">
              <Disclosure label={t('common.more')}>
                <pre className="rounded-md bg-fill p-3 font-mono text-[12px] leading-4 whitespace-pre-wrap break-all text-label-2">{s.lastError}</pre>
              </Disclosure>
            </div>
          )}
          {superadmin ? (
            <Section footer={t('tunnel.reconnect.footer')}>
              <Row
                onClick={() => (quick ? setConfirming(true) : reconnect.mutate())}
                disabled={reconnect.isPending}
                accent
                title={t('tunnel.reconnect')}
                trailing={reconnect.isPending ? <Spinner size="sm" /> : undefined}
              />
            </Section>
          ) : (
            <Section>
              <SuperadminOnly title={t('tunnel.reconnect')} />
            </Section>
          )}
        </>
      )}
      <ActionSheet
        open={confirming}
        title={t('tunnel.reconnect.quick.confirm')}
        text={t('tunnel.reconnect.quick.text')}
        action={t('tunnel.reconnect')}
        onConfirm={() => {
          setConfirming(false)
          reconnect.mutate()
        }}
        onCancel={() => setConfirming(false)}
      />
    </Page>
  )
}
