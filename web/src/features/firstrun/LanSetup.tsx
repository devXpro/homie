import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2 } from 'lucide-react'
import { motion, useAnimationControls } from 'motion/react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { QRCodeView } from '@/components/qrcode'
import { Button } from '@/components/ui/button'
import { Disclosure, TextArea } from '@/components/ui/controls'
import { CopyField } from '@/components/ui/copy'
import { ErrorState } from '@/components/ui/feedback'
import { Spinner } from '@/components/ui/spinner'
import { StatusOrb } from '@/components/ui/status'
import { AppIcon } from '@/components/ui/tile'
import { setLanguage } from '@/i18n'
import { api, isApiError } from '@/lib/api'
import { cn } from '@/lib/cn'
import { errorText } from '@/lib/errors'
import { fmtClock } from '@/lib/format'
import { useNow } from '@/lib/hooks'
import { relayLook } from '@/lib/status'
import type { FirstRunState, RelayStatus, SetupLink } from '@/lib/types'

// The LAN first-run page (DESIGN.md §8.3, ADMIN-API §5), served by homed's setup listener on http://<lan ip>:8090
// until the home has an owner: PIN → server code → connecting → the one-time setup link. The page closes 30 minutes
// after homed opened it, or after too many wrong PINs (410 setup_closed); `homed admin firstrun` opens it again.

const FR = { base: '/api/firstrun' }
const STATE_KEY = ['firstrun', 'state'] as const
const PIN_LENGTH = 8

export function LanSetup() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  useEffect(() => {
    const langs = navigator.languages ?? [navigator.language]
    setLanguage(!langs.some((l) => l.startsWith('ru')) && langs.some((l) => l.startsWith('en')) ? 'en' : 'ru')
  }, [])
  const state = useQuery({
    queryKey: STATE_KEY,
    queryFn: () => api.get<FirstRunState>('/state', FR),
    retry: false,
    refetchOnWindowFocus: false,
  })
  const refresh = () => void qc.invalidateQueries({ queryKey: STATE_KEY })

  let content
  if (state.error) {
    content = isApiError(state.error, 'setup_done') ? (
      <Done />
    ) : isApiError(state.error, 'setup_closed') ? (
      <Closed />
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
    switch (state.data.step) {
      case 'pin':
        content = <PinStep onDone={refresh} />
        break
      case 'relay':
        content = <CodeStep onDone={refresh} />
        break
      case 'connecting':
      case 'link':
        content = <ConnectStep initial={state.data.relay} onGone={refresh} />
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
      <p className="type-callout text-label-2 text-balance">{t('lan.closed.text')}</p>
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
      await api.post('/pin', { pin: value }, FR)
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

function CodeStep({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation()
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const submit = async () => {
    const c = code.trim()
    if (!c) return setError(t('form.required'))
    setBusy(true)
    setError(undefined)
    try {
      await api.post<RelayStatus>('/relay', { code: c }, FR)
      onDone()
    } catch (err) {
      // conflict: homed has a server already (set from the home computer meanwhile); move on to it.
      if (isApiError(err, 'conflict')) onDone()
      else setError(isApiError(err, 'invalid_args') ? t('lan.code.invalid') : errorText(err, t))
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
      <h1 className="text-center type-title-1">{t('lan.code.title')}</h1>
      <p className="mt-2 mb-6 text-center type-callout text-label-2">{t('lan.code.text')}</p>
      <TextArea
        label={t('lan.code.label')}
        hideLabel
        rows={3}
        mono
        value={code}
        onChange={(e) => setCode(e.target.value)}
        error={error}
        placeholder="ghr1.…"
        spellCheck={false}
        autoCapitalize="none"
        autoCorrect="off"
        autoFocus
      />
      <Button type="submit" className="mt-6" variant="filled" size="lg" loading={busy}>
        {t('lan.code.button')}
      </Button>
    </form>
  )
}

/** ConnectStep waits for the relay (SSE), then shows the one-time link with its QR code and countdown. */
function ConnectStep({ initial, onGone }: { initial?: RelayStatus; onGone: () => void }) {
  const { t } = useTranslation()
  const [relay, setRelay] = useState<RelayStatus | undefined>(initial)
  const [link, setLink] = useState<SetupLink>()
  const [linkError, setLinkError] = useState<string>()
  const [startedAt] = useState(() => Date.now())
  const now = useNow(1000)
  const ready = !!relay && relayLook(relay) === 'connected'

  useEffect(() => {
    if (typeof EventSource === 'undefined') return
    const es = new EventSource('/api/firstrun/events')
    const onRelay = (e: MessageEvent) => {
      try {
        setRelay(JSON.parse(e.data as string) as RelayStatus)
      } catch {
        // ignore a broken frame
      }
    }
    es.addEventListener('relay', onRelay)
    return () => {
      es.removeEventListener('relay', onRelay)
      es.close()
    }
  }, [])

  const makeLink = async () => {
    setLinkError(undefined)
    try {
      setLink(await api.post<SetupLink>('/link', {}, FR))
    } catch (err) {
      if (isApiError(err, 'setup_done') || isApiError(err, 'setup_closed')) onGone()
      else if (!isApiError(err, 'relay_not_ready')) setLinkError(errorText(err, t))
    }
  }

  useEffect(() => {
    if (!ready || link) return
    let live = true
    api
      .post<SetupLink>('/link', {}, FR)
      .then((l) => live && setLink(l))
      .catch((err: unknown) => {
        if (!live) return
        if (isApiError(err, 'setup_done') || isApiError(err, 'setup_closed')) onGone()
        else if (!isApiError(err, 'relay_not_ready')) setLinkError(errorText(err, t))
      })
    return () => {
      live = false
    }
  }, [ready, link, onGone, t])

  if (!link) {
    const slow = now - startedAt > 60_000
    return (
      <div className="flex flex-col items-center" role="status">
        <StatusOrb tone="warning" breathing />
        <h1 className="mt-6 type-title-2 text-balance">{t('lan.connecting', { domain: relay?.domain ?? '…' })}</h1>
        {linkError && <p className="mt-4 type-footnote text-danger-text">{linkError}</p>}
        {slow && (
          <div className="mt-6 w-full text-left">
            <p className="type-callout text-label-2">{t('lan.connecting.slow')}</p>
            {(relay?.lastError || relay?.selfCheck?.error) && (
              <div className="mt-2">
                <Disclosure label={t('common.more')}>
                  <pre className="rounded-md bg-fill p-3 font-mono text-[12px] leading-4 whitespace-pre-wrap break-all text-label-2">
                    {[relay.lastError, relay.selfCheck?.error].filter(Boolean).join('\n')}
                  </pre>
                </Disclosure>
              </div>
            )}
          </div>
        )}
      </div>
    )
  }

  const left = new Date(link.expiresAt).getTime() - now
  const expired = left <= 0
  return (
    <div className="flex flex-col items-center">
      <h1 className="type-title-1 text-balance">{t('lan.link.title')}</h1>
      <div className={cn('mt-6 transition-opacity', expired && 'opacity-30')}>
        <QRCodeView value={link.url} label={t('lan.link.qr')} />
      </div>
      <Button className="mt-6" variant="filled" size="lg" disabled={expired} onClick={() => window.location.assign(link.url)}>
        {t('lan.link.open')}
      </Button>
      <CopyField className="mt-4 w-full text-left" value={link.url} />
      <p className={cn('mt-3 type-footnote tabular-nums', expired ? 'text-warning-text' : 'text-label-2')} aria-live="off">
        {expired ? t('lan.link.expired') : t('lan.link.expires', { time: fmtClock(left) })}
      </p>
      <Button className="mt-2" variant="plain" size="md" onClick={() => void makeLink()}>
        {t('lan.link.new')}
      </Button>
    </div>
  )
}
