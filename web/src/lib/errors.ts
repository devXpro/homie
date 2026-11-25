// Error codes → words. The package words the gatekeeper's codes; Homie adds its own (error.<code>)
// and lets two texts of homed through as they are: a plugin's for tool_failed, since it says what to do, and the
// reason of a 403 forbidden ("only the home's superadmin may do this: …").

import { errorText as packageErrorText, isApiError } from '@devxpro/mcp-gatekeeper-ui'
import type { TFunction } from 'i18next'
import type { Job } from './types'

export function errorText(err: unknown, t: TFunction): string {
  if ((isApiError(err, 'tool_failed') || isApiError(err, 'forbidden')) && err.message) return err.message
  return packageErrorText(err, t)
}

/** jobErrorText words a failed job. */
export function jobErrorText(job: Job, t: TFunction): string {
  const code = job.error?.code ?? 'internal'
  return t(`job.error.${code}`, { defaultValue: t(`error.${code}`, { defaultValue: t('error.generic') }) })
}
