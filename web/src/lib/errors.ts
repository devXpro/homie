// Error codes → words. The package words the gatekeeper's codes; Homie adds its own (error.<code>)
// and lets a plugin's own text through for tool_failed, since it says what to do.

import { errorText as packageErrorText, isApiError } from '@devxpro/mcp-gatekeeper-ui'
import type { TFunction } from 'i18next'
import type { Job } from './types'

export function errorText(err: unknown, t: TFunction): string {
  if (isApiError(err, 'tool_failed') && err.message) return err.message
  return packageErrorText(err, t)
}

/** jobErrorText words a failed job. */
export function jobErrorText(job: Job, t: TFunction): string {
  const code = job.error?.code ?? 'internal'
  return t(`job.error.${code}`, { defaultValue: t(`error.${code}`, { defaultValue: t('error.generic') }) })
}
