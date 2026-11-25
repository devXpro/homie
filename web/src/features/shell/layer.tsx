import { currentLang, Spinner, spring, useLiveMode, useNow, type Me } from '@devxpro/mcp-gatekeeper-ui'
import { useQueryClient } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { AnimatePresence, motion } from 'motion/react'
import { useTranslation } from 'react-i18next'
import { Outlet } from 'react-router'
import { JobProvider, useJobs } from '@/features/run/jobs'
import { RunProvider, useRun } from '@/features/run/run'
import { useBootstrapSettled } from '@/lib/bootstrap'
import { useHomeEvents } from '@/lib/events'
import { fmtClock } from '@/lib/format'
import { hq, useOverview, useOwner, useRunningCalls, useRunningJobs } from '@/lib/queries'
import { needsOwner } from '@/lib/status'
import { moduleTitle, toolTitle } from '@/lib/tools'
import type { Call, Job, PluginDetail, PluginSummary } from '@/lib/types'

// Homie inside the gatekeeper's shell (STEP2 §3.1 web row). The package's AppShell owns the frame, the tabs, the
// sign-in redirect and step-up; Homie adds:
// - useHomieShell, its useBadges hook: the shell calls it on every render while someone is signed in, so it also
//   keeps the live events running (SSE or polling) and puts the red dots on Дом and Устройства;
// - HomieLayer, the layout route of Homie's own pages: the run and job sheets and the running-action pill.

/** useHomieShell follows the admin's events and marks the tabs that need the owner (DESIGN.md §5.2). */
export function useHomieShell(me: Me) {
  const qc = useQueryClient()
  const live = useLiveMode()
  const owner = me.person.role === 'owner'
  // The stream starts once the bootstrap said which way: SSE, or polling behind a quick tunnel.
  useHomeEvents(qc, live, useBootstrapSettled())
  const overview = useOverview(owner)
  return {
    // The Дом tab carries the id "connect": the package's connect page lives under it.
    connect: owner && needsOwner(overview.data?.attention).length > 0,
    devices: owner && (overview.data?.plugins ?? []).some((p) => p.state === 'failing'),
  }
}

/** HomieLayer wraps Homie's pages with the run and job sheets and the pill that reopens them. */
export function HomieLayer() {
  return (
    <JobProvider>
      <RunProvider>
        <Outlet />
        <RunningPill />
      </RunProvider>
    </JobProvider>
  )
}

function jobTitle(job: Job, plugins: PluginSummary[] | undefined, t: TFunction) {
  const p = plugins?.find((x) => x.id === job.target)
  const title = p ? moduleTitle(p, t) : (job.target ?? '')
  if (job.kind === 'plugin.install') return t('add.installing', { title })
  return t(`job.title.${job.kind}`, { title, defaultValue: title })
}

/**
 * RunningPill floats above the tab bar on phones and sits at the foot of the sidebar on desktop while an action
 * runs with its sheet closed; a tap reopens the sheet (DESIGN.md §5.2).
 */
function RunningPill() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const owner = useOwner()
  const { openCall, shownCallId } = useRun()
  const { openJob, shownJobId } = useJobs()
  const calls = (useRunningCalls(owner).data?.items ?? []).filter((c) => c.id !== shownCallId && c.state === 'running')
  const jobs = (useRunningJobs(owner).data?.items ?? []).filter((j) => j.id !== shownJobId && j.state === 'running')
  const overview = useOverview(owner)
  const now = useNow(1000)
  const count = calls.length + jobs.length
  const first = (calls[0] as Call | undefined) ?? (jobs[0] as Job | undefined)
  const lang = currentLang()
  let label = ''
  if (first && 'tool' in first) {
    const tool = qc.getQueryData<PluginDetail>(hq.plugin(first.pluginId))?.tools.find((x) => x.name === first.tool)
    label = tool ? toolTitle(tool, lang) : first.title
  } else if (first) {
    label = jobTitle(first, overview.data?.plugins, t)
  }
  const open = () => {
    if (!first) return
    if ('tool' in first) openCall(first)
    else openJob({ jobId: first.id, title: jobTitle(first, overview.data?.plugins, t) })
  }
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-[calc(var(--tab-h)+env(safe-area-inset-bottom)+8px)] z-40 flex justify-center px-4 lg:inset-x-auto lg:bottom-[72px] lg:left-3 lg:w-[calc(var(--sidebar-w)-24px)] lg:px-0">
      <AnimatePresence initial={false}>
        {first && (
          <motion.button
            type="button"
            onClick={open}
            aria-label={t('run.reopen', { title: count > 1 ? t('run.running_many', { count }) : label })}
            initial={{ opacity: 0, y: 8, scale: 0.98 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 8, scale: 0.98 }}
            transition={spring.snappy}
            whileTap={{ scale: 0.97 }}
            className="pointer-events-auto flex h-11 max-w-full items-center gap-2.5 rounded-full bg-dialog px-4 shadow-[var(--shadow-toast)] ring-[0.5px] ring-separator lg:h-10 lg:w-full lg:rounded-md lg:bg-accent-tint lg:px-3 lg:text-accent-tinted lg:shadow-none lg:ring-0"
          >
            <Spinner size="sm" className="text-accent lg:text-current" />
            <span className="min-w-0 flex-1 truncate text-left text-[15px] leading-5 font-semibold">
              {count > 1 ? t('run.running_many', { count }) : label}
            </span>
            {count === 1 && <span className="text-[13px] text-label-2 tabular-nums">{fmtClock(now - new Date(first.startedAt).getTime())}</span>}
          </motion.button>
        )}
      </AnimatePresence>
    </div>
  )
}
