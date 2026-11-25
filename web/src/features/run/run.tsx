import { ActionSheet, Button, currentLang, ProgressBar, seg, Sheet, SheetFooter, Spinner, SuccessCheck, toast, useNow } from '@devxpro/mcp-gatekeeper-ui'
import { useQueryClient } from '@tanstack/react-query'
import { TriangleAlert } from 'lucide-react'
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router'
import { homeApi, isHomeError, isInvalid } from '@/lib/api'
import { errorText } from '@/lib/errors'
import { callBus, useEventsConnected } from '@/lib/events'
import { fmtClock } from '@/lib/format'
import { hq, useCall, useInventory, usePlugin } from '@/lib/queries'
import { buildFields, initialValues, serverErrors, validate, type FieldErrors, type FormValues } from '@/lib/schema'
import { fieldCount, groupTools, isDestructive, progressMessage, toolDescription, toolTitle } from '@/lib/tools'
import type { Call, List, PluginDetail, Tool } from '@/lib/types'
import { ResultView, type FollowUpContext } from './ResultView'
import { SchemaForm } from './SchemaForm'

// The run sheet (DESIGN.md §9.4–§9.7): an admin tool as a form, then the call with its progress, then the result
// with follow-up actions. Calls are detached from the page: closing the sheet never cancels; the running-action
// pill reopens it, and a toast says when a call ends while its sheet is closed.

export interface RunRequest {
  tool: Tool
  prefill?: FormValues
  /** the device sheet fills the target and hides it */
  hideTarget?: boolean
  /** attach to a call that already runs (the pill, a reload, 409 call_running) */
  callId?: string
}

interface RunApi {
  openRun: (r: RunRequest) => void
  openCall: (call: Pick<Call, 'id' | 'tool' | 'pluginId' | 'title' | 'timeoutMs'>) => void
  /** the call the open sheet shows, so the pill can leave it out */
  shownCallId?: string
}

const RunContext = createContext<RunApi | null>(null)

export function useRun() {
  const ctx = useContext(RunContext)
  if (!ctx) throw new Error('useRun outside RunProvider')
  return ctx
}

/** Calls started from this page; only they get the "done" toast. */
const myCalls = new Set<string>()

function toolFromCache(qc: ReturnType<typeof useQueryClient>, pluginId: string, name: string) {
  return qc.getQueryData<PluginDetail>(hq.plugin(pluginId))?.tools.find((t) => t.name === name)
}

let seq = 0

export function RunProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const [state, setState] = useState<{ req: RunRequest; key: number; path: string; open: boolean } | null>(null)
  const [shown, setShown] = useState<string | undefined>()
  // The sheet belongs to the page it was opened on; navigating away closes it (the call keeps running).
  const open = !!state?.open && state.path === pathname
  const openRef = useRef({ open, shown })
  useEffect(() => {
    openRef.current = { open, shown }
  }, [open, shown])

  const openRun = useCallback(
    (req: RunRequest) => {
      setShown(req.callId)
      setState({ req, key: ++seq, path: window.location.pathname, open: true })
    },
    [],
  )

  const openCall = useCallback<RunApi['openCall']>(
    (call) => {
      const show = (tool: Tool) => openRun({ tool, callId: call.id })
      const cached = toolFromCache(qc, call.pluginId, call.tool)
      if (cached) return show(cached)
      void qc
        .fetchQuery({ queryKey: hq.plugin(call.pluginId), queryFn: () => homeApi.get<PluginDetail>(`/plugins/${seg(call.pluginId)}`) })
        .then((d) => d.tools.find((x) => x.name === call.tool))
        .catch(() => undefined)
        .then((tool) =>
          show(
            tool ?? {
              name: call.tool,
              pluginId: call.pluginId,
              title: call.title,
              description: '',
              audience: 'admin',
              inputSchema: { type: 'object', properties: {} },
              timeoutMs: call.timeoutMs,
              inventory: false,
            },
          ),
        )
    },
    [qc, openRun],
  )

  // A call that ends while its sheet is closed gets a toast (DESIGN.md §9.4).
  useEffect(
    () =>
      callBus.on((c) => {
        if (c.state === 'running' || !myCalls.has(c.id)) return
        myCalls.delete(c.id)
        if (openRef.current.open && openRef.current.shown === c.id) return
        const tool = toolFromCache(qc, c.pluginId, c.tool)
        const title = tool ? toolTitle(tool, currentLang()) : c.title
        if (c.state === 'ok') toast(t('run.done_bg', { title }), 'success')
        else if (c.state !== 'cancelled') toast(t('run.failed_bg', { title }), 'warning')
      }),
    [qc, t],
  )

  const value = useMemo(() => ({ openRun, openCall, shownCallId: open ? shown : undefined }), [openRun, openCall, open, shown])
  const close = () => setState((s) => s && { ...s, open: false })
  const lang = currentLang()

  return (
    <RunContext.Provider value={value}>
      {children}
      <Sheet open={open} onClose={close} title={state ? toolTitle(state.req.tool, lang) : ''}>
        {state && <RunContent key={state.key} req={state.req} onClose={close} onCallShown={setShown} onFollowUp={openRun} />}
      </Sheet>
    </RunContext.Provider>
  )
}

function RunContent({
  req,
  onClose,
  onCallShown,
  onFollowUp,
}: {
  req: RunRequest
  onClose: () => void
  onCallShown: (id: string | undefined) => void
  onFollowUp: (r: RunRequest) => void
}) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const lang = currentLang()
  const tool = req.tool
  const fields = useMemo(() => buildFields(tool, lang, { hideTarget: req.hideTarget }), [tool, lang, req.hideTarget])
  const [values, setValues] = useState<FormValues>(() => initialValues(fields, req.prefill))
  const [errors, setErrors] = useState<FieldErrors>({})
  const [otherErrors, setOtherErrors] = useState<string[]>([])
  const [callId, setCallId] = useState<string | undefined>(req.callId)
  const [submitError, setSubmitError] = useState<unknown>(null)
  const [submitting, setSubmitting] = useState(false)
  // A tool with no fields runs as soon as it opens; a destructive one asks first (DESIGN.md §9.3).
  const noFields = fieldCount(tool, req.hideTarget) === 0
  const [confirming, setConfirming] = useState(() => noFields && isDestructive(tool) && !req.callId)
  const autoRan = useRef(false)
  const sse = useEventsConnected()
  const call = useCall(callId)
  const c = call.data
  // While the event stream is down, poll the running call instead.
  useEffect(() => {
    if (!callId || sse || c?.state !== 'running') return
    const id = setInterval(() => void qc.invalidateQueries({ queryKey: hq.call(callId) }), 1500)
    return () => clearInterval(id)
  }, [callId, sse, c?.state, qc])

  const plugin = usePlugin(tool.pluginId)
  const groups = useMemo(() => groupTools(plugin.data?.tools ?? [], lang), [plugin.data, lang])
  const hasInventory = !!plugin.data?.inventoryTool
  const inventory = useInventory(tool.pluginId, hasInventory)
  const needsInventory = fields.some((f) => f.kind === 'target')

  const start = useCallback(
    async (args: Record<string, unknown>) => {
      setSubmitting(true)
      setSubmitError(null)
      setOtherErrors([])
      try {
        const created = await homeApi.post<Call>(`/tools/${seg(tool.name)}/calls`, { args })
        qc.setQueryData(hq.call(created.id), created)
        myCalls.add(created.id)
        setCallId(created.id)
        onCallShown(created.id)
      } catch (err) {
        // The same tool runs already: attach to that call (409 names it; else find it among the running ones).
        const running = isHomeError(err, 'call_running') ? (err.callId ?? (await runningCallOf(tool.name))) : undefined
        if (running) {
          myCalls.add(running)
          setCallId(running)
          onCallShown(running)
        } else if (isInvalid(err)) {
          const mapped = serverErrors(tool, fields, isHomeError(err) ? err.fields : [], t)
          setErrors(mapped.errors)
          setOtherErrors(mapped.other.length ? mapped.other : Object.keys(mapped.errors).length ? [] : [errorText(err, t)])
        } else {
          setSubmitError(err)
        }
      } finally {
        setSubmitting(false)
      }
    },
    [tool, fields, qc, t, onCallShown],
  )

  const submit = (confirmed = false) => {
    const v = validate(tool, fields, values, t)
    setErrors(v.errors)
    if (Object.keys(v.errors).length > 0) {
      requestAnimationFrame(() => document.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus())
      return
    }
    if (isDestructive(tool) && !confirmed) {
      setConfirming(true)
      return
    }
    void start(v.args)
  }

  useEffect(() => {
    if (autoRan.current || callId || !noFields || isDestructive(tool)) return
    autoRan.current = true
    void start(buildArgsForEmpty(values))
  }, [noFields, callId, tool, start, values])

  const retry = () => {
    setCallId(undefined)
    onCallShown(undefined)
    setSubmitError(null)
    if (noFields) {
      autoRan.current = false
      if (isDestructive(tool)) setConfirming(true)
    }
  }

  const ctx: FollowUpContext = {
    moduleActions: groups.moduleActions,
    itemActions: groups.itemActions,
    inventory: inventory.data?.items ?? [],
    self: tool.name,
    onAction: (next, prefill) => onFollowUp({ tool: next, prefill }),
  }

  const description = toolDescription(tool, lang)
  let body: ReactNode
  let footer: ReactNode

  if (!callId && submitError) {
    body = <Outcome icon="warning" title={t('run.failed')} text={errorText(submitError, t)} />
    footer = (
      <>
        <Button variant="filled" size="lg" onClick={retry}>
          {t('common.retry')}
        </Button>
        <Button variant="plain" size="lg" onClick={onClose}>
          {t('common.close')}
        </Button>
      </>
    )
  } else if (!callId) {
    body = (
      <form
        id="run-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        {description && <p className="mb-5 type-subheadline text-label-2">{description}</p>}
        {noFields ? (
          !isDestructive(tool) && (
            <div className="flex justify-center py-8">
              <Spinner size="lg" />
            </div>
          )
        ) : (
          <SchemaForm
            fields={fields}
            values={values}
            errors={errors}
            onChange={(v) => setValues(v)}
            onBlur={(pointer) => {
              const v = validate(tool, fields, values, t)
              setErrors((old) => {
                const rest = Object.fromEntries(Object.entries(old).filter(([k]) => k !== pointer))
                return v.errors[pointer] ? { ...rest, [pointer]: v.errors[pointer] } : rest
              })
            }}
            inventory={needsInventory ? inventory.data?.items : undefined}
          />
        )}
        {otherErrors.length > 0 && (
          <p role="alert" className="mt-4 type-footnote text-danger-text">
            {otherErrors.join(' ')}
          </p>
        )}
      </form>
    )
    footer = !noFields && (
      <Button
        type="submit"
        form="run-form"
        variant={isDestructive(tool) ? 'destructive-filled' : 'filled'}
        size="lg"
        loading={submitting}
      >
        {toolTitle(tool, lang)}
      </Button>
    )
  } else if (!c || c.state === 'running') {
    body = <Running call={c} tool={tool} />
    footer = (
      <Button
        variant="plain"
        size="lg"
        disabled={!c}
        onClick={() => {
          if (!c) return
          void homeApi.del<Call>(`/calls/${seg(c.id)}`).then((x) => x && qc.setQueryData(hq.call(x.id), x)).catch(() => undefined)
        }}
      >
        {t('run.cancel')}
      </Button>
    )
  } else if (c.state === 'ok') {
    body = (
      <div className="flex flex-col items-center pt-2 text-center">
        <SuccessCheck />
        <h3 className="mt-4 mb-4 type-title-3">{t('run.ok')}</h3>
        <ResultView text={c.result?.text} structured={c.result?.structured} ctx={ctx} />
      </div>
    )
    footer = (
      <Button variant="filled" size="lg" onClick={onClose}>
        {t('common.done')}
      </Button>
    )
  } else if (c.state === 'cancelled') {
    body = <Outcome icon="none" title={t('run.cancelled')} />
    footer = (
      <Button variant="filled" size="lg" onClick={onClose}>
        {t('common.close')}
      </Button>
    )
  } else {
    const text = c.state === 'failed' ? c.result?.text : c.error ? t(`error.${c.error.code}`, { defaultValue: t('error.generic') }) : t('error.generic')
    body = <Outcome icon="warning" title={t('run.failed')} text={text} />
    footer = (
      <>
        <Button variant="filled" size="lg" onClick={retry}>
          {t('common.retry')}
        </Button>
        <Button variant="plain" size="lg" onClick={onClose}>
          {t('common.close')}
        </Button>
      </>
    )
  }

  return (
    <>
      {body}
      {footer && <SheetFooter>{footer}</SheetFooter>}
      <ActionSheet
        open={confirming}
        title={t('form.confirm', { title: toolTitle(tool, lang) })}
        action={toolTitle(tool, lang)}
        onConfirm={() => {
          setConfirming(false)
          submit(true)
        }}
        onCancel={() => {
          setConfirming(false)
          if (noFields) onClose()
        }}
      />
    </>
  )
}

/** runningCallOf finds the running call of a tool, for a 409 call_running that did not name it. */
async function runningCallOf(tool: string): Promise<string | undefined> {
  try {
    const list = await homeApi.get<List<Call>>('/calls', { query: { state: 'running' } })
    return list.items.find((c) => c.tool === tool)?.id
  } catch {
    return undefined
  }
}

function buildArgsForEmpty(values: FormValues) {
  const args: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(values)) if (v !== undefined && v !== '') args[k] = v
  return args
}

function Outcome({ icon, title, text }: { icon: 'warning' | 'none'; title: string; text?: string }) {
  return (
    <div className="flex flex-col items-center pt-2 pb-2 text-center">
      {icon === 'warning' && <TriangleAlert aria-hidden className="size-14 text-warning" strokeWidth={1.75} />}
      <h3 className="mt-3 type-title-3">{title}</h3>
      {text && <p className="mt-2 max-w-md type-body whitespace-pre-line text-label-2">{text}</p>}
    </div>
  )
}

/** Running shows a call's progress (DESIGN.md §9.5): spinner or bar, the message, the elapsed time, "slow". */
function Running({ call, tool }: { call?: Call; tool: Tool }) {
  const { t } = useTranslation()
  const lang = currentLang()
  const now = useNow(250)
  const started = call ? new Date(call.startedAt).getTime() : now
  const elapsed = Math.max(0, now - started)
  const timeout = call?.timeoutMs ?? tool.timeoutMs
  const long = timeout >= 30_000
  const message = progressMessage(call?.progress, lang)
  const total = call?.progress?.total
  return (
    <div className="flex flex-col items-center gap-4 pt-4 pb-2 text-center">
      {total ? (
        <div className="flex w-full max-w-xs flex-col gap-2">
          <ProgressBar value={(call?.progress?.progress ?? 0) / total} />
          <span className="type-footnote text-label-2 tabular-nums">
            {t('run.progress', { progress: call?.progress?.progress ?? 0, total })}
          </span>
        </div>
      ) : (
        <Spinner size="lg" />
      )}
      <div aria-live="polite" className="w-full">
        {message ? (
          <p className="type-title-3 text-balance">{message}</p>
        ) : (
          long && (
            <div className="w-full rounded-lg bg-fill px-4 py-3 text-left">
              <div className="type-footnote font-semibold text-label-2">{t('run.meanwhile')}</div>
              <p className="mt-1 type-body">{toolDescription(tool, lang)}</p>
            </div>
          )
        )}
      </div>
      <p className="type-subheadline text-label-2 tabular-nums">
        {fmtClock(elapsed)}
        {long && ` ${t('run.of', { total: fmtClock(timeout) })}`}
      </p>
      {elapsed >= timeout * 0.8 && <p className="type-footnote text-warning-text">{t('run.slow')}</p>}
    </div>
  )
}
