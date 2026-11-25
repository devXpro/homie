import { Row } from '@devxpro/mcp-gatekeeper-ui'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

/**
 * SuperadminOnly stands where a co-owner would look for what only the home's superadmin may run on the home computer
 * (installing, updating or removing modules, updating the hub, reconnecting the tunnel): the action greyed out, and
 * who can do it.
 */
export function SuperadminOnly({ title, leading }: { title: string; leading?: ReactNode }) {
  const { t } = useTranslation()
  return <Row iconInset={!!leading} leading={leading} title={title} subtitle={t('superadmin.only')} disabled />
}
