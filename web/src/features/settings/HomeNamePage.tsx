import { Button, Page, qk, TextField, toast, useMe, type Account, type Me } from '@devxpro/mcp-gatekeeper-ui'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { OwnerOnly } from '@/features/shell/OwnerOnly'
import { homeApi, isInvalid } from '@/lib/api'
import { updateBootstrap } from '@/lib/bootstrap'
import { errorText } from '@/lib/errors'
import { hq, useSettings } from '@/lib/queries'
import type { Overview, Settings } from '@/lib/types'

// The home's name: what ChatGPT and Claude call the home. It is the gatekeeper account's name too
// (docs/ADMIN-API.md §3, Settings), so a rename here renames the account: the sidebar, the people page and the invites follow.

export function HomeNamePage() {
  const { t } = useTranslation()
  const me = useMe().data
  const settings = useSettings(me?.person.role === 'owner')
  const current = settings.data?.homeName ?? me?.account.name
  return (
    <OwnerOnly title={t('settings.home.name')}>
      <Page title={t('settings.home.name')} back={{ to: '/settings', label: t('settings.title') }}>
        {current !== undefined && <HomeNameForm current={current} />}
      </Page>
    </OwnerOnly>
  )
}

function HomeNameForm({ current }: { current: string }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [name, setName] = useState(current)
  const [error, setError] = useState<string>()
  const trimmed = name.trim()
  const save = useMutation({
    mutationFn: () => homeApi.patch<Settings>('/settings', { homeName: trimmed }),
    onSuccess: (s) => {
      qc.setQueryData(hq.settings, s)
      qc.setQueryData<Overview>(hq.overview, (o) => o && { ...o, home: { name: s.homeName } })
      qc.setQueryData<Me>(qk.me, (m) => m && { ...m, account: { ...m.account, name: s.homeName } })
      qc.setQueryData<Account>(qk.account, (a) => a && { ...a, name: s.homeName })
      updateBootstrap({ homeName: s.homeName })
      toast(t('common.saved'))
      navigate('/settings')
    },
    onError: (err) => setError(isInvalid(err) ? t('form.maxLength', { max: 40 }) : errorText(err, t)),
  })
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (!trimmed) return setError(t('form.required'))
        save.mutate()
      }}
      className="flex flex-col gap-6"
    >
      <TextField
        label={t('settings.home.label')}
        hint={trimmed ? t('settings.home.preview', { name: trimmed }) : undefined}
        error={error}
        value={name}
        onChange={(e) => {
          setName(e.target.value)
          setError(undefined)
        }}
        maxLength={40}
        autoComplete="off"
        enterKeyHint="done"
      />
      <p className="-mt-3 px-1 type-footnote text-label-2">{t('settings.home.footer')}</p>
      <Button type="submit" variant="filled" size="lg" loading={save.isPending} disabled={trimmed === current || !trimmed}>
        {t('common.save')}
      </Button>
    </form>
  )
}
