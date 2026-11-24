import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Copy } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Page } from '@/components/page'
import { Rich } from '@/components/rich'
import { Button } from '@/components/ui/button'
import { Disclosure, TextArea } from '@/components/ui/controls'
import { ErrorState, Skeleton, SkeletonRows } from '@/components/ui/feedback'
import { Row, Section } from '@/components/ui/list'
import { Sheet, SheetFooter } from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { StatusOrb } from '@/components/ui/status'
import { useStepUp } from '@/features/auth/stepup'
import { currentLang } from '@/i18n'
import { api, isApiError } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { cn } from '@/lib/cn'
import { errorText } from '@/lib/errors'
import { fmtDateTime, fmtRelative } from '@/lib/format'
import { useDelayed, useNow } from '@/lib/hooks'
import { qk, useRelay } from '@/lib/queries'
import { relayLook, relayTone, TONE_STATUS } from '@/lib/status'
import { toast } from '@/lib/toast'
import type { Overview, RelayStatus } from '@/lib/types'

// Сервер (DESIGN.md §8.9): is the home reachable, since when, does the outside check pass; a new code for the same
// server (a token rotation). Switching servers is done on the home computer (ADMIN-API §3.9).

export function ServerPage() {
  const { t } = useTranslation()
  const lang = currentLang()
  const qc = useQueryClient()
  const relay = useRelay()
  const now = useNow(15_000)
  const showSkeleton = useDelayed(relay.isPending)
  const [recode, setRecode] = useState(false)
  const r = relay.data

  const check = useMutation({
    mutationFn: () => api.post<RelayStatus>('/relay/check', {}),
    onSuccess: (s) => {
      qc.setQueryData(qk.relay, s)
      qc.setQueryData<Overview>(qk.overview, (o) => o && { ...o, relay: s })
    },
    onError: (err) => toast(errorText(err, t), 'warning'),
  })

  const back = { to: '/admin/settings', label: t('settings.title') }
  if (relay.error && !r) {
    return (
      <Page title={t('settings.server')} back={back}>
        <ErrorState text={errorText(relay.error, t)} onRetry={() => void relay.refetch()} />
      </Page>
    )
  }
  if (!r) {
    return (
      <Page title={t('settings.server')} back={back}>
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
  const look = relayLook(r)
  const tone = relayTone(look)
  return (
    <Page title={t('settings.server')} back={back}>
      <div className={cn('homie-hero flex flex-col items-center rounded-xl px-5 py-6 text-center', TONE_STATUS[tone])}>
        <StatusOrb tone={tone} breathing={look === 'connecting'} />
        <p className="mt-3 type-title-2">{t(`relay.${look}`)}</p>
        {r.domain && <p className="mt-0.5 type-subheadline text-label-2">{r.domain}</p>}
      </div>

      {r.configured ? (
        <>
          <Section className="mt-8">
            {r.publicUrl && (
              <Row
                title={t('settings.server.address')}
                trailing={
                  <button
                    type="button"
                    onClick={async () => {
                      if (await copyText(r.publicUrl ?? '')) toast(t('common.copied'))
                    }}
                    className="inline-flex max-w-[14rem] items-center gap-1.5 text-accent-text"
                    aria-label={`${t('common.copy')}: ${r.publicUrl}`}
                  >
                    <span className="truncate">{r.publicUrl.replace(/^https:\/\//, '')}</span>
                    <Copy aria-hidden className="size-4 shrink-0" />
                  </button>
                }
              />
            )}
            {r.since && r.state === 'connected' && (
              <Row title={t('settings.server.since')} trailing={<span className="tabular-nums">{fmtDateTime(r.since, lang)}</span>} />
            )}
            <Row
              title={t('settings.server.check')}
              subtitle={
                !r.selfCheck?.checkedAt ? (
                  t('settings.server.check.none')
                ) : r.selfCheck.ok ? (
                  <span className="text-success-text">{t('settings.server.check.ok', { time: fmtRelative(r.selfCheck.checkedAt, lang, now) })}</span>
                ) : (
                  <span className="text-warning-text">{t('settings.server.check.failed', { time: fmtRelative(r.selfCheck.checkedAt, lang, now) })}</span>
                )
              }
            />
            <Row
              onClick={() => check.mutate()}
              disabled={check.isPending}
              accent
              title={t('settings.server.check_now')}
              trailing={check.isPending ? <Spinner size="sm" /> : undefined}
            />
          </Section>
          {(r.lastError || r.selfCheck?.error) && (
            <div className="mt-4">
              <Disclosure label={t('common.more')}>
                <pre className="rounded-md bg-fill p-3 font-mono text-[12px] leading-4 whitespace-pre-wrap break-all text-label-2">
                  {[r.lastError, r.selfCheck?.error].filter(Boolean).join('\n')}
                </pre>
              </Disclosure>
            </div>
          )}
          <Button className="mt-6 w-full" variant="plain" size="md" onClick={() => setRecode(true)}>
            {t('settings.server.recode')}
          </Button>
        </>
      ) : (
        <p className="mt-6 px-1 type-body text-label-2">
          <Rich text={t('settings.server.not_configured')} />
        </p>
      )}
      <Sheet open={recode} onClose={() => setRecode(false)} title={t('settings.server.recode')}>
        <RecodeForm onDone={() => setRecode(false)} />
      </Sheet>
    </Page>
  )
}

function RecodeForm({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const { withStepUp } = useStepUp()
  const [code, setCode] = useState('')
  const [error, setError] = useState<ReactNode>()
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    const c = code.trim()
    if (!c) return setError(t('form.required'))
    setBusy(true)
    setError(undefined)
    try {
      const s = await withStepUp(() => api.put<RelayStatus>('/relay', { code: c }), t('stepup.action.relay'))
      if (!s) return
      qc.setQueryData(qk.relay, s)
      toast(t('settings.server.recode.done'))
      onDone()
    } catch (err) {
      if (isApiError(err, 'domain_change_local_only')) setError(<Rich text={t('settings.server.other_domain')} />)
      else if (isApiError(err, 'invalid_args')) setError(t('lan.code.invalid'))
      else setError(errorText(err, t))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      id="recode-form"
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <p className="mb-4 type-subheadline text-label-2">{t('settings.server.recode.text')}</p>
      <TextArea
        label={t('lan.code.label')}
        hideLabel
        value={code}
        onChange={(e) => setCode(e.target.value)}
        error={error}
        rows={3}
        mono
        placeholder="ghr1.…"
        spellCheck={false}
        autoCapitalize="none"
        autoCorrect="off"
        autoFocusOnOpen
      />
      <SheetFooter>
        <Button type="submit" form="recode-form" variant="filled" size="lg" loading={busy}>
          {t('lan.code.button')}
        </Button>
      </SheetFooter>
    </form>
  )
}
