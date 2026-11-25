
import { AppIcon, Button, ErrorState, Page, Row, Section, Skeleton, Spinner, toast, useDelayed, useStepUp } from '@devxpro/mcp-gatekeeper-ui'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ArrowDownCircle, ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useJobs } from '@/features/run/jobs'
import { OwnerOnly } from '@/features/shell/OwnerOnly'
import { homeApi, isHomeError } from '@/lib/api'
import { errorText } from '@/lib/errors'
import { hq, useSystem } from '@/lib/queries'
import type { Job, SystemInfo } from '@/lib/types'

// О программе (DESIGN.md §8.9): the version, a check for updates and the self-update (step-up, then a job; at
// "restart" the page waits for the new hub's hello and reloads itself).

export function AboutPage() {
  const { t } = useTranslation()
  return (
    <OwnerOnly title={t('settings.about')}>
      <AboutScreen />
    </OwnerOnly>
  )
}

function AboutScreen() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const system = useSystem()
  const { withStepUp } = useStepUp()
  const { openJob } = useJobs()
  const showSkeleton = useDelayed(system.isPending)
  const s = system.data

  const check = useMutation({
    mutationFn: () => homeApi.post<SystemInfo>('/system/check-update', {}),
    onSuccess: (info) => {
      qc.setQueryData(hq.system, info)
      if (!info.update) toast(t('settings.update.latest'))
    },
    onError: (err) => toast(errorText(err, t), 'warning'),
  })

  const update = async () => {
    try {
      const job = await withStepUp(() => homeApi.post<Job>('/system/update', s?.update ? { version: s.update.latest } : {}), t('stepup.action.hub_update'))
      if (!job) return
      qc.setQueryData(hq.job(job.id), job)
      openJob({ jobId: job.id, title: t('job.title.hub.update'), doneText: t('job.done.hub.update') })
    } catch (err) {
      if (isHomeError(err, 'job_running') && err.jobId) openJob({ jobId: err.jobId, title: t('job.title.hub.update') })
      else toast(errorText(err, t), 'warning')
    }
  }

  return (
    <Page title={t('settings.about')} back={{ to: '/settings', label: t('settings.title') }}>
      <div className="flex flex-col items-center pt-2 pb-8 text-center">
        <AppIcon size={64} />
        <h2 className="mt-3 type-title-2">Homie</h2>
        {s ? (
          <p className="mt-1 font-mono text-[13px] leading-[18px] text-label-2">
            {s.version} · {s.commit} · {s.os}/{s.arch}
          </p>
        ) : (
          showSkeleton && <Skeleton className="mt-2 h-4 w-48" />
        )}
      </div>
      {system.error && !s ? (
        <ErrorState text={errorText(system.error, t)} onRetry={() => void system.refetch()} />
      ) : (
        s && (
          <>
            {s.update ? (
              <>
                <Section>
                  <Row
                    iconInset
                    leading={<ArrowDownCircle aria-hidden className="size-[30px] text-accent" strokeWidth={1.75} />}
                    title={t('attention.hub_update.title')}
                    subtitle={s.update.latest}
                  />
                  <Row
                    title={
                      <a href={s.update.notesUrl} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1.5 text-accent-text">
                        {t('settings.update.notes')}
                        <ExternalLink aria-hidden className="size-4" />
                      </a>
                    }
                  />
                </Section>
                <Button className="mt-6" variant="filled" size="lg" onClick={() => void update()}>
                  {t('settings.update.button', { version: s.update.latest })}
                </Button>
              </>
            ) : (
              <Section>
                <Row
                  onClick={() => check.mutate()}
                  disabled={check.isPending}
                  accent
                  title={t('settings.update.check')}
                  trailing={check.isPending ? <Spinner size="sm" /> : check.isSuccess ? <span className="type-subheadline text-label-2">{t('settings.update.latest')}</span> : undefined}
                />
              </Section>
            )}
          </>
        )
      )}
    </Page>
  )
}
