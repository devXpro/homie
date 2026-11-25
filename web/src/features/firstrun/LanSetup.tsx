import {
  ActionSheet,
  AppIcon,
  Button,
  ButtonAnchor,
  CopyField,
  cn,
  Disclosure,
  ErrorState,
  IconTile,
  isApiError,
  isOffline,
  Row,
  Section,
  Skeleton,
  Spinner,
  StepList,
  TextField,
  useNow,
  type StepItem,
} from '@devxpro/mcp-gatekeeper-ui'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { CheckCircle2, ExternalLink, FlaskConical, Globe, TriangleAlert } from 'lucide-react'
import { motion, useAnimationControls } from 'motion/react'
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { StatusOrb } from '@/components/status'
import { firstrunApi } from '@/lib/api'
import { errorText } from '@/lib/errors'
import { fmtClock } from '@/lib/format'
import { hostnameOk } from '@/lib/hostname'
import { tunnelHost, tunnelLook } from '@/lib/status'
import type { ConnectStatus, ConnectStepName, FirstRunState, SetupLink, TunnelStatus } from '@/lib/types'

// The LAN first-run page (STEP2 §4.8), served by homed's setup listener on http://<lan ip>:8090 while
// no tunnel is configured (then on 127.0.0.1:8090 only) and until an owner has a passkey of the current address:
// PIN → "my domain" (the Cloudflare login link, then the connect's progress) or "try without a domain" (a quick
// tunnel) → the probe → the one-time setup link. Behind a quick tunnel the address changes on every restart; the page
// then opens again and offers «Переподключить»: a new setup link for the same owner and the steps to re-add the
// connector. The PIN rules stay as T2.F made them: 8 digits, 5 wrong per device per 10 minutes, 20 close the page.

const QRCodeView = lazy(() => import('@/components/qrcode').then((m) => ({ default: m.QRCodeView })))

const STATE_KEY = ['firstrun', 'state'] as const
const PIN_LENGTH = 8

/**
 * followed: the page moved on without this request, so it asks for the state again — a connect runs already or the
 * home got a tunnel meanwhile (conflict), or the PIN's cookie is gone (pin_required: back to the PIN).
 */
const followed = (err: unknown) => isApiError(err, 'conflict') || isApiError(err, 'pin_required')

export function LanSetup() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const state = useQuery({
    queryKey: STATE_KEY,
    queryFn: () => firstrunApi.get<FirstRunState>('/state'),
    retry: false,
    refetchOnWindowFocus: false,
    // While the tunnel comes up the page follows it by asking again (plain HTTP on the LAN, nothing to stream).
    refetchInterval: (q) => (q.state.data?.step === 'connecting' ? 1500 : false),
  })
  const refresh = () => void qc.invalidateQueries({ queryKey: STATE_KEY })

  let content
  if (state.error) {
    content = isApiError(state.error, 'setup_done') ? (
      <Done />
    ) : isApiError(state.error, 'setup_closed') ? (
      <Closed />
    ) : isOffline(state.error) && state.data && state.data.step !== 'pin' ? (
      // Once a tunnel is configured homed serves this page on 127.0.0.1 only: a page opened from the LAN loses it.
      <Moved />
    ) : (
      <ErrorState text={errorText(state.error, t)} onRetry={refresh} />
    )
  } else if (!state.data) {
    content = (
      <div className="flex justify-center py-16">
        <Spinner size="lg" />
      </div>
    )
  } else {
    const s = state.data
    switch (s.step) {
      case 'pin':
        content = <PinStep onDone={refresh} />
        break
      case 'tunnel':
        // Nothing connects the home yet; a connect that just failed says why first.
        content = s.connect?.state === 'failed' ? <ConnectFailed connect={s.connect} onDone={refresh} /> : <TunnelStep onDone={refresh} />
        break
      case 'connecting':
        content = <ConnectingStep state={s} />
        break
      case 'link':
        // An owner exists, yet the page is open: the quick address changed and their passkey no longer fits.
        content = s.ownerExists ? <ReconnectStep tunnel={s.tunnel} onGone={refresh} /> : <LinkStep onGone={refresh} />
        break
    }
  }
  return (
    <main className="flex min-h-dvh flex-col items-center px-6 pt-[max(48px,env(safe-area-inset-top))] pb-[max(32px,env(safe-area-inset-bottom))]">
      <div className="flex w-full max-w-[420px] flex-col items-center text-center">
        <AppIcon size={56} />
        <p className="mt-3 type-footnote font-semibold tracking-wide text-label-2">Homie</p>
        <div className="mt-8 w-full">{content}</div>
      </div>
    </main>
  )
}

function Done() {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col items-center gap-4 py-6">
      <CheckCircle2 aria-hidden className="size-14 text-success" strokeWidth={1.75} />
      <p className="type-title-3 text-balance">{t('lan.done')}</p>
    </div>
  )
}

/** Closed: the page timed out or saw too many wrong PINs; only the home computer can open it again. */
function Closed() {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col items-center gap-4 py-6">
      <StatusOrb tone="warning" />
      <p className="type-title-3 text-balance">{t('lan.closed.title')}</p>
      <p className="type-callout text-balance text-label-2">{t('lan.closed.text')}</p>
    </div>
  )
}

/** Moved: the home is on the internet now, so the setup page left the LAN for the home computer itself. */
function Moved() {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col items-center gap-4 py-6" role="status">
      <CheckCircle2 aria-hidden className="size-14 text-success" strokeWidth={1.75} />
      <p className="type-title-3 text-balance">{t('lan.moved.title')}</p>
      <p className="type-callout text-balance text-label-2">{t('lan.moved.text')}</p>
      <CopyField className="w-full text-left" value="http://localhost:8090/firstrun" label={t('lan.moved.address')} />
    </div>
  )
}

function PinStep({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation()
  const [pin, setPin] = useState('')
  const [busy, setBusy] = useState(false)
  const [wrong, setWrong] = useState(false)
  const [error, setError] = useState<string>()
  const [lockedUntil, setLockedUntil] = useState(0)
  const [focused, setFocused] = useState(false)
  const now = useNow(1000)
  const shake = useAnimationControls()
  const input = useRef<HTMLInputElement>(null)
  const locked = lockedUntil > now

  // When the lock ends the boxes come back to life (a new PIN was printed meanwhile).
  useEffect(() => {
    if (!lockedUntil) return
    const id = setTimeout(() => input.current?.focus(), Math.max(0, lockedUntil - Date.now()) + 50)
    return () => clearTimeout(id)
  }, [lockedUntil])

  const submit = async (value: string) => {
    setBusy(true)
    setError(undefined)
    try {
      await firstrunApi.post('/pin', { pin: value })
      onDone()
    } catch (err) {
      setPin('')
      if (isApiError(err, 'wrong_pin')) {
        setWrong(true)
        void shake.start({ x: [0, -8, 8, -4, 4, 0], transition: { duration: 0.3 } })
      } else if (isApiError(err, 'setup_closed')) {
        onDone()
      } else if (isApiError(err, 'rate_limited')) {
        setWrong(false)
        setLockedUntil(Date.now() + (err.retryAfter ?? 600) * 1000)
      } else {
        setError(errorText(err, t))
      }
    } finally {
      setBusy(false)
      requestAnimationFrame(() => input.current?.focus())
    }
  }

  return (
    <div className="flex flex-col items-center">
      <h1 className="type-title-1">{t('lan.pin.title')}</h1>
      <p className="mt-2 type-callout text-label-2">{t('lan.pin.text')}</p>
      <motion.div animate={shake} className="relative mt-8">
        <input
          ref={input}
          value={pin}
          onChange={(e) => {
            const v = e.target.value.replace(/\D/g, '').slice(0, PIN_LENGTH)
            setPin(v)
            setWrong(false)
            if (v.length === PIN_LENGTH && !busy) void submit(v)
          }}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          inputMode="numeric"
          autoComplete="one-time-code"
          pattern="[0-9]*"
          maxLength={PIN_LENGTH + 1} // room for a pasted "1234 5678"; onChange keeps the digits
          disabled={busy || locked}
          aria-label={t('lan.pin.title')}
          aria-invalid={wrong || undefined}
          aria-describedby="pin-note"
          autoFocus
          className="absolute inset-0 z-10 h-full w-full cursor-text text-transparent caret-transparent opacity-0 outline-none disabled:cursor-not-allowed"
        />
        {/* Eight boxes in two groups of four, as homed prints the PIN ("1234 5678"); narrow enough for a phone. */}
        <div aria-hidden className={cn('flex gap-3 sm:gap-4', locked && 'opacity-40')}>
          {[0, 4].map((start) => (
            <div key={start} className="flex gap-1.5 sm:gap-2">
              {Array.from({ length: 4 }, (_, j) => {
                const i = start + j
                const active = focused && !locked && i === Math.min(pin.length, PIN_LENGTH - 1)
                return (
                  <span
                    key={i}
                    className={cn(
                      'flex h-14 w-8 items-center justify-center rounded-md bg-fill font-mono text-[24px] font-semibold tabular-nums transition-[box-shadow] sm:w-10 sm:text-[28px]',
                      active && 'shadow-[0_0_0_2px_var(--accent)]',
                      wrong && 'shadow-[0_0_0_2px_var(--danger)]',
                    )}
                  >
                    {pin[i] ?? ''}
                  </span>
                )
              })}
            </div>
          ))}
        </div>
      </motion.div>
      <div id="pin-note" className="mt-5 min-h-10" role="status" aria-live="polite">
        {busy ? (
          <Spinner />
        ) : locked ? (
          <p className="type-footnote text-warning-text tabular-nums">{t('lan.pin.locked', { time: fmtClock(lockedUntil - now) })}</p>
        ) : wrong ? (
          <p className="type-footnote text-danger-text">{t('lan.pin.wrong')}</p>
        ) : (
          error && <p className="type-footnote text-danger-text">{error}</p>
        )}
      </div>
    </div>
  )
}

/** TunnelStep: "my domain" or "try without a domain", then the form of the chosen way. */
function TunnelStep({ onDone, initial = 'choose', host }: { onDone: () => void; initial?: 'choose' | 'domain' | 'quick'; host?: string }) {
  const { t } = useTranslation()
  const [way, setWay] = useState(initial)
  if (way === 'domain') return <DomainForm onDone={onDone} onBack={() => setWay('choose')} initial={host} />
  if (way === 'quick') return <QuickForm onDone={onDone} onBack={() => setWay('choose')} />
  return (
    <div className="flex flex-col">
      <h1 className="type-title-1 text-balance">{t('lan.tunnel.title')}</h1>
      <p className="mt-2 type-callout text-balance text-label-2">{t('lan.tunnel.text')}</p>
      <Section className="mt-8 text-left">
        <Row
          onClick={() => setWay('domain')}
          className="min-h-[72px]"
          iconInset
          leading={<IconTile icon={Globe} color="green" />}
          title={<span className="type-headline">{t('lan.tunnel.domain')}</span>}
          subtitle={t('lan.tunnel.domain.text')}
          chevron
        />
        <Row
          onClick={() => setWay('quick')}
          className="min-h-[72px]"
          iconInset
          leading={<IconTile icon={FlaskConical} color="orange" />}
          title={<span className="type-headline">{t('lan.tunnel.quick')}</span>}
          subtitle={t('lan.tunnel.quick.text')}
          chevron
        />
      </Section>
    </div>
  )
}

function DomainForm({ onDone, onBack, initial = '' }: { onDone: () => void; onBack: () => void; initial?: string }) {
  const { t } = useTranslation()
  const [host, setHost] = useState(initial)
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    const h = host.trim().toLowerCase()
    if (!hostnameOk(h)) return setError(t('lan.domain.invalid'))
    setBusy(true)
    setError(undefined)
    try {
      await firstrunApi.post('/tunnel', { mode: 'named', hostname: h })
      onDone()
    } catch (err) {
      if (followed(err)) onDone()
      else setError(isApiError(err, 'invalid') || isApiError(err, 'invalid_args') ? t('lan.domain.invalid') : errorText(err, t))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      className="flex flex-col text-left"
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <h1 className="text-center type-title-1">{t('lan.domain.title')}</h1>
      <p className="mt-2 mb-6 text-center type-callout text-balance text-label-2">{t('lan.domain.text')}</p>
      <TextField
        label={t('lan.domain.label')}
        placeholder="home.example.com"
        value={host}
        onChange={(e) => {
          setHost(e.target.value)
          setError(undefined)
        }}
        error={error}
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        inputMode="url"
        mono
        autoFocus
      />
      <Button type="submit" className="mt-6" variant="filled" size="lg" loading={busy}>
        {t('lan.domain.button')}
      </Button>
      <Button type="button" className="mt-2" variant="plain" size="lg" onClick={onBack}>
        {t('common.back')}
      </Button>
    </form>
  )
}

function QuickForm({ onDone, onBack }: { onDone: () => void; onBack: () => void }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const start = async () => {
    setBusy(true)
    setError(undefined)
    try {
      await firstrunApi.post('/tunnel', { mode: 'quick' })
      onDone()
    } catch (err) {
      if (followed(err)) onDone()
      else setError(errorText(err, t))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="flex flex-col">
      <h1 className="type-title-1">{t('lan.quick.title')}</h1>
      <p className="mt-2 type-callout text-balance text-label-2">{t('lan.quick.text')}</p>
      <div role="note" className="mt-6 flex gap-3 rounded-lg bg-surface-1 p-4 text-left">
        <TriangleAlert aria-hidden className="mt-0.5 size-5 shrink-0 text-warning" />
        <p className="type-subheadline">{t('lan.quick.warning')}</p>
      </div>
      {error && (
        <p role="alert" className="mt-4 type-footnote text-danger-text">
          {error}
        </p>
      )}
      <Button className="mt-6" variant="filled" size="lg" loading={busy} onClick={() => void start()}>
        {t('lan.quick.button')}
      </Button>
      <Button className="mt-2" variant="plain" size="lg" onClick={onBack}>
        {t('common.back')}
      </Button>
    </div>
  )
}

const CONNECT_STEPS: ConnectStepName[] = ['login', 'tunnel', 'dns', 'config']

/** zoneOf guesses the zone a host belongs to for the login hint: its last two labels. */
function zoneOf(host: string) {
  return host.split('.').slice(-2).join('.')
}

/** connectSteps shows a connect's steps: done before the current one, the current one running or failed. */
function connectSteps(c: ConnectStatus, t: TFunction): StepItem[] {
  const current = c.step ? CONNECT_STEPS.indexOf(c.step) : 0
  return CONNECT_STEPS.map((s, i) => ({
    key: s,
    label: t(`lan.connect.step.${s}`),
    state: c.state === 'ok' || i < current ? 'done' : i === current ? (c.state === 'failed' ? 'failed' : 'running') : 'pending',
  }))
}

/**
 * ConnectFailed is a connect of "my domain" that stopped: the step it stopped at and why. An existing DNS record can
 * be replaced, after a confirmation (STEP2 §3.4 asks before --overwrite-dns); any other failure goes back to the form.
 */
function ConnectFailed({ connect: c, onDone }: { connect: ConnectStatus; onDone: () => void }) {
  const { t } = useTranslation()
  const [again, setAgain] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  // Cloudflare has a record for the address already: it can be replaced with overwriteDns.
  const exists = c.code === 'dns_exists'

  if (again) {
    return (
      <TunnelStep
        initial="domain"
        host={c.hostname}
        onDone={() => {
          setAgain(false)
          onDone()
        }}
      />
    )
  }

  const overwrite = async () => {
    setBusy(true)
    setError(undefined)
    try {
      await firstrunApi.post('/tunnel', { mode: 'named', hostname: c.hostname, overwriteDns: true })
      onDone()
    } catch (err) {
      if (followed(err)) onDone()
      else setError(errorText(err, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col text-left">
      <h1 className="text-center type-title-2 text-balance">{t('lan.connect.failed', { host: c.hostname })}</h1>
      <div className="mt-6">
        <StepList steps={connectSteps(c, t)} />
      </div>
      <p role="alert" className="mt-6 rounded-lg bg-fill p-4 type-body">
        {exists ? t('lan.connect.error.dns_exists', { host: c.hostname }) : t('lan.connect.failed.text')}
      </p>
      {c.error && (
        <div className="mt-3">
          <Disclosure label={t('common.more')}>
            <pre className="rounded-md bg-fill p-3 font-mono text-[12px] leading-4 whitespace-pre-wrap break-all text-label-2">{c.error}</pre>
          </Disclosure>
        </div>
      )}
      {error && <p className="mt-3 type-footnote text-danger-text">{error}</p>}
      {exists ? (
        <>
          <Button className="mt-6" variant="filled" size="lg" loading={busy} onClick={() => setConfirming(true)}>
            {t('lan.connect.overwrite')}
          </Button>
          <Button className="mt-2" variant="plain" size="lg" onClick={() => setAgain(true)}>
            {t('lan.connect.other')}
          </Button>
        </>
      ) : (
        <Button className="mt-6" variant="filled" size="lg" onClick={() => setAgain(true)}>
          {t('lan.connect.retry')}
        </Button>
      )}
      <ActionSheet
        open={confirming}
        title={t('lan.connect.overwrite.confirm', { host: c.hostname })}
        text={t('lan.connect.overwrite.text')}
        action={t('lan.connect.overwrite')}
        onConfirm={() => {
          setConfirming(false)
          void overwrite()
        }}
        onCancel={() => setConfirming(false)}
      />
    </div>
  )
}

/**
 * ConnectingStep follows the connect of "my domain" (the Cloudflare login link as a button and a QR code, then the
 * steps), and then the tunnel until the outside check passes; homed then moves the page on to the link.
 */
function ConnectingStep({ state }: { state: FirstRunState }) {
  const { t } = useTranslation()
  const [startedAt] = useState(() => Date.now())
  const now = useNow(1000)
  const c = state.connect

  if (c && c.state === 'running') {
    const steps = connectSteps(c, t)
    if (c.step === 'login' && c.loginUrl) {
      return (
        <div className="flex flex-col items-center">
          <h1 className="type-title-1">{t('lan.login.title')}</h1>
          <p className="mt-2 type-callout text-balance text-label-2">{t('lan.login.text', { zone: zoneOf(c.hostname) })}</p>
          <ButtonAnchor href={c.loginUrl} className="mt-6 w-full" variant="filled" size="lg" icon={<ExternalLink aria-hidden className="size-5" />}>
            {t('lan.login.open')}
          </ButtonAnchor>
          <div className="mt-6">
            <Suspense fallback={<Skeleton className="size-[196px] rounded-lg" />}>
              <QRCodeView value={c.loginUrl} size={180} label={t('lan.login.qr')} />
            </Suspense>
          </div>
          <div className="mt-8 w-full text-left">
            <StepList steps={steps} />
          </div>
        </div>
      )
    }
    return (
      <div className="flex flex-col text-left" role="status">
        <h1 className="text-center type-title-2 text-balance">{t('lan.connect.title', { host: c.hostname })}</h1>
        <div className="mt-6">
          <StepList steps={steps} />
        </div>
      </div>
    )
  }

  // The connect is done (or it was a quick tunnel): the child starts, then the outside check.
  const tunnel = state.tunnel
  const look = tunnelLook(tunnel)
  const host = tunnelHost(tunnel) || c?.hostname
  const title =
    tunnel?.mode === 'quick' && !tunnel.url
      ? t('lan.connecting.quick')
      : tunnel?.state === 'probing'
        ? t('lan.connecting.probe')
        : t('lan.connecting', { domain: host || '…' })
  const slow = now - startedAt > 60_000
  return (
    <div className="flex flex-col items-center" role="status">
      <StatusOrb tone={look === 'offline' ? 'danger' : 'warning'} breathing />
      <h1 className="mt-6 type-title-2 text-balance">{title}</h1>
      {tunnel?.state === 'probing' && host && <p className="mt-1 type-subheadline text-label-2">{host}</p>}
      {(slow || look === 'offline' || look === 'unreachable') && (
        <div className="mt-6 w-full text-left">
          <p className="type-callout text-label-2">{t('lan.connecting.slow')}</p>
          {tunnel?.lastError && (
            <div className="mt-2">
              <Disclosure label={t('common.more')}>
                <pre className="rounded-md bg-fill p-3 font-mono text-[12px] leading-4 whitespace-pre-wrap break-all text-label-2">{tunnel.lastError}</pre>
              </Disclosure>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/** useSetupLink asks for the one-time setup link (each call revokes the older ones and changes the PIN). */
function useSetupLink(onGone: () => void, auto: boolean) {
  const { t } = useTranslation()
  const [link, setLink] = useState<SetupLink>()
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const make = useCallback(async () => {
    setError(undefined)
    setBusy(true)
    try {
      setLink(await firstrunApi.post<SetupLink>('/link', {}))
    } catch (err) {
      if (isApiError(err, 'setup_done') || isApiError(err, 'setup_closed') || isApiError(err, 'pin_required')) onGone()
      else if (!isApiError(err, 'tunnel_not_ready')) setError(errorText(err, t))
    } finally {
      setBusy(false)
    }
  }, [onGone, t])
  // The plain link step asks for its link at once, and only once.
  const started = useRef(false)
  useEffect(() => {
    if (!auto || started.current) return
    started.current = true
    void make()
  }, [auto, make])
  return { link, error, busy, make }
}

/** LinkView is the one-time setup link: a QR code for the phone, "open on this computer", copy, countdown. */
function LinkView({ link, onNew }: { link: SetupLink; onNew: () => void }) {
  const { t } = useTranslation()
  const now = useNow(1000)
  const left = new Date(link.expiresAt).getTime() - now
  const expired = left <= 0
  return (
    <div className="flex flex-col items-center">
      <div className={cn('transition-opacity', expired && 'opacity-30')}>
        <Suspense fallback={<Skeleton className="size-[236px] rounded-lg" />}>
          <QRCodeView value={link.url} label={t('lan.link.qr')} />
        </Suspense>
      </div>
      <Button className="mt-6" variant="filled" size="lg" disabled={expired} onClick={() => window.location.assign(link.url)}>
        {t('lan.link.open')}
      </Button>
      <CopyField className="mt-4 w-full text-left" value={link.url} />
      <p className={cn('mt-3 type-footnote tabular-nums', expired ? 'text-warning-text' : 'text-label-2')} aria-live="off">
        {expired ? t('lan.link.expired') : t('lan.link.expires', { time: fmtClock(left) })}
      </p>
      <Button className="mt-2" variant="plain" size="md" onClick={onNew}>
        {t('lan.link.new')}
      </Button>
    </div>
  )
}

function LinkStep({ onGone }: { onGone: () => void }) {
  const { t } = useTranslation()
  const { link, error, make } = useSetupLink(onGone, true)
  return (
    <div className="flex flex-col items-center">
      <h1 className="type-title-1 text-balance">{t('lan.link.title')}</h1>
      {error && <p className="mt-4 type-footnote text-danger-text">{error}</p>}
      <div className="mt-6 w-full">
        {link ? (
          <LinkView link={link} onNew={() => void make()} />
        ) : (
          <div className="flex justify-center py-10">
            <Spinner size="lg" />
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * ReconnectStep: a quick tunnel came back with a new address, so the owner's passkey no longer fits. «Переподключить»
 * issues a new setup link for the same owner; the new connector address and the steps to re-add it follow.
 */
function ReconnectStep({ tunnel, onGone }: { tunnel?: TunnelStatus; onGone: () => void }) {
  const { t } = useTranslation()
  const { link, error, busy, make } = useSetupLink(onGone, false)
  const mcp = tunnel?.mcpUrl ?? (tunnel?.url ? `${tunnel.url.replace(/\/+$/, '')}/mcp` : undefined)
  return (
    <div className="flex flex-col items-center">
      <StatusOrb tone="warning" />
      <h1 className="mt-5 type-title-1 text-balance">{t('lan.reconnect.title')}</h1>
      <p className="mt-2 type-callout text-balance text-label-2">{t('lan.reconnect.text')}</p>
      {mcp && (
        <div className="mt-6 w-full text-left">
          <p className="px-1 pb-1.5 type-footnote text-label-2">{t('lan.reconnect.address')}</p>
          <CopyField value={mcp} label={t('lan.reconnect.address')} />
        </div>
      )}
      {error && <p className="mt-4 type-footnote text-danger-text">{error}</p>}
      {link ? (
        <div className="mt-8 w-full">
          <LinkView link={link} onNew={() => void make()} />
        </div>
      ) : (
        <Button className="mt-6" variant="filled" size="lg" loading={busy} onClick={() => void make()}>
          {t('lan.reconnect.button')}
        </Button>
      )}
      <div className="mt-8 w-full text-left">
        <p className="px-1 pb-2 type-footnote text-label-2">{t('lan.reconnect.steps')}</p>
        <ol className="flex flex-col gap-3">
          {[1, 2, 3].map((n) => (
            <li key={n} className="flex gap-3">
              <span aria-hidden className="inline-flex size-6 shrink-0 items-center justify-center rounded-full bg-fill text-[13px] font-semibold text-label-2 tabular-nums">
                {n}
              </span>
              <p className="min-w-0 flex-1 pt-0.5 type-subheadline">{t(`lan.reconnect.step.${n}`)}</p>
            </li>
          ))}
        </ol>
      </div>
    </div>
  )
}
