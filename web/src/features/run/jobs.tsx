import { Button, currentLang, ProgressBar, Sheet, SheetFooter, StepList, SuccessCheck, toast, type StepItem } from '@devxpro/mcp-gatekeeper-ui'
import { useQueryClient } from '@tanstack/react-query'
import { TriangleAlert } from 'lucide-react'
import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router'
import { jobErrorText } from '@/lib/errors'
import { jobBus, useEventsConnected } from '@/lib/events'
import { fmtBytes } from '@/lib/format'
import { hq, useJob } from '@/lib/queries'
import type { Job } from '@/lib/types'

// Jobs (ADMIN-API §2 Job): installs, updates, removals and the hub's self-update, shown as a StepList. Closing the
// sheet keeps the job running; the pill reopens it.

/** jobSteps turns a job into StepList rows, with the download bar under the active download step. */
export function JobSteps({ job }: { job: Job }) {
  const { t } = useTranslation()
  const lang = currentLang()
  const current = job.step ? job.steps.indexOf(job.step) : -1
  const steps: StepItem[] = job.steps.map((s, i) => {
    let state: StepItem['state'] = 'pending'
    if (job.state === 'ok' || i < current) state = 'done'
    else if (i === current) state = job.state === 'failed' ? 'failed' : 'running'
    let detail: ReactNode
    if (state === 'running' && s === 'download' && job.progress?.total) {
      detail = (
        <div className="flex flex-col gap-1.5">
          <ProgressBar value={job.progress.done / job.progress.total} />
          <span className="type-footnote text-label-2 tabular-nums">
            {t('job.downloaded', { done: fmtBytes(job.progress.done, lang), total: fmtBytes(job.progress.total, lang) })}
          </span>
        </div>
      )
    }
    if (state === 'running' && s === 'restart') detail = <span className="type-footnote text-label-2">{t('settings.update.restarting')}</span>
    return { key: s, label: t(`job.step.${s}`, { defaultValue: s }), state, detail }
  })
  return <StepList steps={steps} />
}

export interface JobRequest {
  jobId: string
  title: string
  doneText?: string
  /** runs once when the job ends well (navigate away after a removal, …) */
  onDone?: (job: Job) => void
  /** starts the same job again after a failure */
  retry?: () => Promise<Job | undefined>
}

// The one job sheet of the admin, kept outside React: pages open it (useJobs), and the shell's overlay shows it on
// every page (JobSheet), so it survives navigation.
interface JobSheetState {
  req: JobRequest
  open: boolean
  key: number
  /** the page it was opened on: like the run sheet, it belongs there; the pill reopens it elsewhere */
  path: string
}

let sheet: JobSheetState | null = null
let seq = 0
const listeners = new Set<() => void>()
/** the callbacks of running jobs, run once when each ends well */
const onDone = new Map<string, NonNullable<JobRequest['onDone']>>()

function setSheet(next: JobSheetState | null) {
  sheet = next
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

const getSheet = () => sheet

/** openJob shows a job's sheet on the current page. */
function openJob(req: JobRequest) {
  if (req.onDone) onDone.set(req.jobId, req.onDone)
  setSheet({ req, open: true, key: ++seq, path: window.location.pathname })
}

function closeJob() {
  if (sheet) setSheet({ ...sheet, open: false })
}

/** useOpenJobSheet is the job whose sheet is open on this page, if any. */
function useOpenJobSheet(): JobSheetState | undefined {
  const s = useSyncExternalStore(subscribe, getSheet, getSheet)
  const { pathname } = useLocation()
  return s?.open && s.path === pathname ? s : undefined
}

/** useJobs: open a job's sheet; the job the open sheet shows, so the pill can leave it out. */
export function useJobs() {
  const shownJobId = useOpenJobSheet()?.req.jobId
  return { openJob, shownJobId }
}

/** JobSheet is the job sheet itself, mounted once by the shell's overlay; a job that ends unseen gets a toast. */
export function JobSheet() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  const shown = useOpenJobSheet()
  const s = useSyncExternalStore(subscribe, getSheet, getSheet)
  const shownId = useRef<string | undefined>(undefined)
  useEffect(() => {
    shownId.current = shown?.req.jobId
  }, [shown])

  useEffect(
    () =>
      jobBus.on((j) => {
        if (j.state === 'running') return
        const done = onDone.get(j.id)
        if (!done) return
        onDone.delete(j.id)
        if (j.state === 'ok') done(j)
        if (shownId.current !== j.id) toast(t(j.state === 'ok' ? 'common.done' : 'run.failed'), j.state === 'ok' ? 'success' : 'warning')
      }),
    [t],
  )

  return (
    <Sheet open={!!shown} onClose={closeJob} title={s?.req.title}>
      {s && (
        <JobContent
          key={s.key}
          req={s.req}
          onClose={closeJob}
          onRetry={async () => {
            const next = await s.req.retry?.()
            if (next) {
              qc.setQueryData(hq.job(next.id), next)
              openJob({ ...s.req, jobId: next.id })
            }
          }}
        />
      )}
    </Sheet>
  )
}

function JobContent({ req, onClose, onRetry }: { req: JobRequest; onClose: () => void; onRetry: () => Promise<void> }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const job = useJob(req.jobId).data
  const live = useEventsConnected()
  const [retrying, setRetrying] = useState(false)
  // While the event stream is down, poll the job instead.
  useEffect(() => {
    if (live || job?.state !== 'running') return
    const id = setInterval(() => void qc.invalidateQueries({ queryKey: hq.job(req.jobId) }), 1500)
    return () => clearInterval(id)
  }, [live, job?.state, qc, req.jobId])

  if (!job) return null
  if (job.state === 'ok') {
    return (
      <>
        <div className="flex flex-col items-center pt-4 pb-2 text-center">
          <SuccessCheck />
          <h3 className="mt-4 type-title-3">{req.doneText ?? t('common.done')}</h3>
        </div>
        <SheetFooter>
          <Button variant="filled" size="lg" onClick={onClose}>
            {t('common.done')}
          </Button>
        </SheetFooter>
      </>
    )
  }
  return (
    <>
      <div className="pt-2 pb-2">
        <JobSteps job={job} />
        {job.state === 'failed' && (
          <div role="alert" className="mt-6 flex gap-3 rounded-lg bg-fill p-4">
            <TriangleAlert aria-hidden className="size-6 shrink-0 text-warning" />
            <p className="type-body">{jobErrorText(job, t)}</p>
          </div>
        )}
      </div>
      {job.state === 'failed' && (
        <SheetFooter>
          {req.retry && (
            <Button
              variant="filled"
              size="lg"
              loading={retrying}
              onClick={async () => {
                setRetrying(true)
                try {
                  await onRetry()
                } finally {
                  setRetrying(false)
                }
              }}
            >
              {t('common.retry')}
            </Button>
          )}
          <Button variant="plain" size="lg" onClick={onClose}>
            {t('common.close')}
          </Button>
        </SheetFooter>
      )}
    </>
  )
}
