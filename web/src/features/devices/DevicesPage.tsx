import {
  Button,
  ButtonLink,
  EmptyState,
  ErrorState,
  fade,
  IconButton,
  isApiError,
  Page,
  Row,
  Section,
  Sheet,
  SheetFooter,
  Skeleton,
  SkeletonRows,
  StatusDot,
  SuccessCheck,
  TextField,
  useAccount,
  useDelayed,
  useIsDesktop,
  useStepUp,
} from '@devxpro/mcp-gatekeeper-ui'
import { useQueryClient } from '@tanstack/react-query'
import { ExternalLink, Lamp, Plus, Search } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMatch, useNavigate } from 'react-router'
import { catalog, parseRepo } from '@/catalog'
import { ModuleTile } from '@/components/tile'
import { JobSteps } from '@/features/run/jobs'
import { useCloseSheet } from '@/features/shell/useCloseSheet'
import { homeApi } from '@/lib/api'
import { errorText, jobErrorText } from '@/lib/errors'
import { httpsLink } from '@/lib/links'
import { hq, useJob, useOwner, usePlugins, usePreview } from '@/lib/queries'
import { pluginTone } from '@/lib/status'
import { moduleTitle } from '@/lib/tools'
import type { HomeAccount, Job, PluginPreview, PluginSummary } from '@/lib/types'

// Устройства: one row per module, failing ones first; "+" opens Add devices. A member sees
// the devices the owner opened to them, read-only: GET /api/account lists them (HC-api §5).

const ORDER: Record<PluginSummary['state'], number> = { failing: 0, starting: 1, stopped: 2, disabled: 3, up: 4 }

export function DevicesPage() {
  const owner = useOwner()
  return owner ? <OwnerDevices /> : <MemberDevices />
}

function MemberDevices() {
  const { t } = useTranslation()
  const account = useAccount<HomeAccount>()
  const showSkeleton = useDelayed(account.isPending)
  const items = [...(account.data?.devices ?? [])].sort((a, b) => moduleTitle(a, t).localeCompare(moduleTitle(b, t)))
  return (
    <Page title={t('devices.title')}>
      {account.error && !account.data ? (
        <ErrorState text={errorText(account.error, t)} onRetry={() => void account.refetch()} />
      ) : !account.data ? (
        showSkeleton && <SkeletonRows rows={2} subtitle={false} />
      ) : items.length === 0 ? (
        <EmptyState icon={Lamp} title={t('devices.title')} text={t('devices.mine.empty')} />
      ) : (
        <Section footer={t('devices.mine.footer')}>
          {items.map((d) => (
            <Row key={d.id} leading={<ModuleTile id={d.id} size="md" />} className="min-h-[68px] [--row-inset:72px]" title={moduleTitle(d, t)} />
          ))}
        </Section>
      )}
    </Page>
  )
}

function OwnerDevices() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const plugins = usePlugins()
  const desktop = useIsDesktop()
  const adding = !!useMatch('/devices/add')
  const closeAdd = useCloseSheet('/devices')
  const showSkeleton = useDelayed(plugins.isPending)
  const items = [...(plugins.data?.items ?? [])].sort((a, b) => ORDER[a.state] - ORDER[b.state] || moduleTitle(a, t).localeCompare(moduleTitle(b, t)))

  return (
    <Page
      title={t('devices.title')}
      trailing={
        <IconButton label={t('devices.add')} onClick={() => navigate('/devices/add', { preventScrollReset: true })}>
          <Plus aria-hidden strokeWidth={2.2} />
        </IconButton>
      }
    >
      {plugins.error && !plugins.data ? (
        <ErrorState text={errorText(plugins.error, t)} onRetry={() => void plugins.refetch()} />
      ) : !plugins.data ? (
        showSkeleton && <SkeletonRows rows={3} />
      ) : items.length === 0 ? (
        <EmptyState
          icon={Lamp}
          title={t('devices.empty.title')}
          text={t('devices.empty.text')}
          action={
            <ButtonLink to="/devices/add" preventScrollReset variant="filled" size="lg">
              {t('devices.empty.button')}
            </ButtonLink>
          }
        />
      ) : (
        <Section>
          {items.map((p) => (
            <Row
              key={p.id}
              to={`/devices/${p.id}`}
              leading={<ModuleTile id={p.id} size="md" />}
              className="min-h-[68px] [--row-inset:72px]"
              title={moduleTitle(p, t)}
              subtitle={
                <span className="inline-flex items-center gap-1.5">
                  <StatusDot tone={pluginTone(p.state)} />
                  <span>
                    {t(`state.${p.state}`)}
                    {p.devices !== undefined && p.devices > 0 && ` · ${t('devices.count', { count: p.devices })}`}
                    {desktop && ` · v${p.version}`}
                  </span>
                </span>
              }
              chevron
            />
          ))}
        </Section>
      )}
      <AddSheet open={adding} onClose={closeAdd} installed={plugins.data?.items ?? []} />
    </Page>
  )
}

type Step = { kind: 'choose' } | { kind: 'preview'; repo: string } | { kind: 'job'; jobId: string; preview: PluginPreview }

function AddSheet({ open, onClose, installed }: { open: boolean; onClose: () => void; installed: PluginSummary[] }) {
  const { t } = useTranslation()
  return (
    <Sheet open={open} onClose={onClose} title={t('add.title')}>
      <AddFlow onClose={onClose} installed={installed} />
    </Sheet>
  )
}

function AddFlow({ onClose, installed }: { onClose: () => void; installed: PluginSummary[] }) {
  const [step, setStep] = useState<Step>({ kind: 'choose' })
  return (
    <AnimatePresence mode="wait" initial={false}>
      <motion.div
        key={step.kind}
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        exit={{ opacity: 0 }}
        transition={fade.fast}
      >
        {step.kind === 'choose' && <Choose installed={installed} onPick={(repo) => setStep({ kind: 'preview', repo })} />}
        {step.kind === 'preview' && (
          <Preview repo={step.repo} onBack={() => setStep({ kind: 'choose' })} onStarted={(jobId, preview) => setStep({ kind: 'job', jobId, preview })} />
        )}
        {step.kind === 'job' && <Installing jobId={step.jobId} preview={step.preview} onClose={onClose} onRetry={() => setStep({ kind: 'preview', repo: step.preview.repo })} />}
      </motion.div>
    </AnimatePresence>
  )
}

function Choose({ installed, onPick }: { installed: PluginSummary[]; onPick: (repo: string) => void }) {
  const { t } = useTranslation()
  const [repo, setRepo] = useState('')
  const [error, setError] = useState<string>()
  const find = () => {
    const r = parseRepo(repo)
    if (!r) return setError(t('add.not_found'))
    setError(undefined)
    onPick(r)
  }
  return (
    <div>
      <Section header={t('add.recommended')} className="mt-1">
        {catalog.map((c) => (
          <CatalogRow key={c.repo} repo={c.repo} installed={installed} onPick={onPick} />
        ))}
      </Section>
      <Section header={t('add.other')} cardClassName="bg-transparent overflow-visible">
        <form
          className="flex items-start gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            find()
          }}
        >
          <div className="flex-1">
            <TextField
              label={t('add.repo.label')}
              hideLabel
              placeholder={t('add.repo.placeholder')}
              value={repo}
              onChange={(e) => setRepo(e.target.value)}
              error={error}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              mono
            />
          </div>
          <Button type="submit" variant="tinted" size="md" icon={<Search aria-hidden className="size-4" />}>
            {t('add.find')}
          </Button>
        </form>
      </Section>
    </div>
  )
}

function CatalogRow({ repo, installed, onPick }: { repo: string; installed: PluginSummary[]; onPick: (repo: string) => void }) {
  const { t } = useTranslation()
  const preview = usePreview(repo)
  const p = preview.data
  const isInstalled = !!p?.installed || installed.some((i) => i.source.repo?.toLowerCase() === repo.toLowerCase())
  if (!p) {
    return (
      <div className="flex min-h-[68px] items-center gap-3 px-4 py-2.5" aria-busy={preview.isPending || undefined}>
        <Skeleton className="size-11 rounded-md" />
        <span className="flex flex-1 flex-col gap-1.5">
          <Skeleton className="h-3.5 w-1/3" />
          <Skeleton className="h-3 w-2/3" />
        </span>
      </div>
    )
  }
  return (
    <Row
      onClick={isInstalled ? undefined : () => onPick(repo)}
      disabled={isInstalled}
      leading={<ModuleTile id={p.id} size="md" />}
      className="min-h-[68px] [--row-inset:72px]"
      title={moduleTitle(p, t)}
      subtitle={p.description}
      trailing={isInstalled ? <span className="type-subheadline text-label-2">{t('add.installed')}</span> : undefined}
      chevron={!isInstalled}
    />
  )
}

function Preview({ repo, onBack, onStarted }: { repo: string; onBack: () => void; onStarted: (jobId: string, p: PluginPreview) => void }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const { withStepUp } = useStepUp()
  const preview = usePreview(repo)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const p = preview.data

  if (preview.error) {
    return (
      <ErrorState
        compact
        text={isApiError(preview.error, 'not_found') ? t('add.not_found') : errorText(preview.error, t)}
        action={
          <Button variant="tinted" size="lg" onClick={onBack}>
            {t('nav.back')}
          </Button>
        }
      />
    )
  }
  if (!p) {
    return (
      <div className="flex flex-col items-center gap-3 py-6" aria-busy="true">
        <Skeleton className="size-16 rounded-lg" />
        <Skeleton className="h-6 w-40" />
        <Skeleton className="h-4 w-64" />
      </div>
    )
  }
  const title = moduleTitle(p, t)
  const website = httpsLink(p.homepage)
  const install = async () => {
    setBusy(true)
    setError(undefined)
    try {
      // The id from the preview lets homed refuse at once a job for a plugin that another job is changing.
      const job = await withStepUp(() => homeApi.post<Job>('/plugins', { repo: p.repo, id: p.id }), t('stepup.action.install', { title }))
      if (job) {
        qc.setQueryData(hq.job(job.id), job)
        onStarted(job.id, p)
      }
    } catch (err) {
      setError(errorText(err, t))
    } finally {
      setBusy(false)
    }
  }
  const blocked = !p.compatible || !p.assetFound || !!p.installed
  return (
    <div className="pb-2">
      <div className="flex flex-col items-center pt-1 text-center">
        <ModuleTile id={p.id} size="lg" />
        <h3 className="mt-3 type-title-2">{title}</h3>
        {p.description && <p className="mt-1 max-w-sm type-subheadline text-label-2">{p.description}</p>}
      </div>
      <Section className="mt-6">
        <Row title={t('add.version')} trailing={<span className="tabular-nums">{p.version}</span>} />
        {/* The source is always the repo the files come from; the author's homepage is a link of its own. */}
        <Row
          title={t('add.source')}
          subtitle={
            <a
              href={`https://github.com/${p.repo}`}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex max-w-full items-center gap-1 text-accent-text hover:underline"
            >
              <span className="truncate">{`github.com/${p.repo}`}</span>
              <ExternalLink aria-hidden className="size-3.5 shrink-0" />
            </a>
          }
        />
        {website && (
          <Row
            title={t('add.website')}
            subtitle={
              <a
                href={website.href}
                target="_blank"
                rel="noreferrer noopener"
                className="inline-flex max-w-full items-center gap-1 text-accent-text hover:underline"
              >
                <span className="truncate">{website.host}</span>
                <ExternalLink aria-hidden className="size-3.5 shrink-0" />
              </a>
            }
          />
        )}
        {!p.compatible && p.minHubVersion && <Row title={t('add.requires', { version: p.minHubVersion })} titleClassName="text-warning-text" />}
      </Section>
      {!p.compatible && <p className="mt-3 px-1 type-footnote text-warning-text">{t('add.incompatible')}</p>}
      {p.compatible && !p.assetFound && <p className="mt-3 px-1 type-footnote text-warning-text">{t('add.no_build')}</p>}
      {error && (
        <p role="alert" className="mt-3 px-1 type-footnote text-danger-text">
          {error}
        </p>
      )}
      <SheetFooter>
        <Button variant="filled" size="lg" loading={busy} disabled={blocked} onClick={() => void install()}>
          {p.installed ? t('add.installed') : t('add.install')}
        </Button>
        <Button variant="plain" size="lg" onClick={onBack}>
          {t('nav.back')}
        </Button>
      </SheetFooter>
    </div>
  )
}

function Installing({ jobId, preview, onClose, onRetry }: { jobId: string; preview: PluginPreview; onClose: () => void; onRetry: () => void }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const job = useJob(jobId).data
  const title = moduleTitle(preview, t)
  if (!job) return null
  if (job.state === 'ok') {
    return (
      <div className="flex flex-col items-center pt-4 pb-2 text-center">
        <SuccessCheck />
        <h3 className="mt-4 type-title-3">{t('add.done', { title })}</h3>
        <SheetFooter>
          <Button
            variant="filled"
            size="lg"
            onClick={() => {
              // Replace the sheet's entry, so back from the module returns to where Add devices was opened.
              navigate(`/devices/${preview.id}`, { replace: true, state: { setup: true } })
            }}
          >
            {t('add.done.button')}
          </Button>
        </SheetFooter>
      </div>
    )
  }
  return (
    <div className="pt-1 pb-2">
      <h3 className="mb-5 type-headline">{job.state === 'failed' ? t('add.failed', { title }) : t('add.installing', { title })}</h3>
      <JobSteps job={job} />
      {job.state === 'failed' && (
        <>
          <p role="alert" className="mt-5 rounded-lg bg-fill p-4 type-body">
            {jobErrorText(job, t)}
          </p>
          <SheetFooter>
            <Button variant="filled" size="lg" onClick={onRetry}>
              {t('common.retry')}
            </Button>
            <Button variant="plain" size="lg" onClick={onClose}>
              {t('common.close')}
            </Button>
          </SheetFooter>
        </>
      )}
    </div>
  )
}
