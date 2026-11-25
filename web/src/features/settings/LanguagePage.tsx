import { CheckList, Page, Section, setLanguage, toast, useLang, type Lang } from '@devxpro/mcp-gatekeeper-ui'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { homeApi } from '@/lib/api'
import { updateBootstrap } from '@/lib/bootstrap'
import { errorText } from '@/lib/errors'
import { hq, useOwner } from '@/lib/queries'
import type { Settings } from '@/lib/types'

// Язык (DESIGN.md §8.9): applies at once in this browser. For an owner it is also the home's language, the default
// of anyone who has not chosen one (PATCH /api/home/settings).

export function LanguagePage() {
  const { t } = useTranslation()
  const lang = useLang()
  const qc = useQueryClient()
  const owner = useOwner()

  const choose = async (next: Lang) => {
    if (next === lang) return
    setLanguage(next)
    if (!owner) return
    try {
      const s = await homeApi.patch<Settings>('/settings', { language: next })
      qc.setQueryData(hq.settings, s)
      updateBootstrap({ language: s.language })
    } catch (err) {
      toast(errorText(err, t), 'warning')
    }
  }

  return (
    <Page title={t('settings.language')} back={{ to: '/settings', label: t('settings.title') }}>
      <Section footer={t('settings.language.footer')}>
        <CheckList<Lang>
          label={t('settings.language')}
          options={[
            { value: 'ru', label: 'Русский' },
            { value: 'en', label: 'English' },
          ]}
          selected={[lang]}
          onToggle={(v) => void choose(v)}
        />
      </Section>
    </Page>
  )
}
