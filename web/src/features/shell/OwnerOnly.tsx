import { ButtonLink, ErrorState, Page, useMe } from '@devxpro/mcp-gatekeeper-ui'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

/**
 * OwnerOnly keeps the owners' pages (a module, its log, the home's settings) from members; the API refuses them too.
 * Owners are the superadmin and the co-owners (STEP2 §2.4). A member is pointed at the connect page instead.
 */
export function OwnerOnly({ title, children }: { title: string; children: ReactNode }) {
  const { t } = useTranslation()
  const me = useMe().data
  if (me && me.person.role !== 'owner') {
    return (
      <Page title={title} largeTitle={false}>
        <ErrorState
          title={t('owneronly.title')}
          text={t('owneronly.text')}
          action={
            <ButtonLink to="/connect" variant="tinted" size="lg">
              {t('owneronly.action')}
            </ButtonLink>
          }
        />
      </Page>
    )
  }
  return children
}
