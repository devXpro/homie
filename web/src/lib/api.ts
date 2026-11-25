// Homie's own APIs on the package's client: /api/home/* (the admin) and /api/firstrun/* (the LAN first-run page).
// They answer in the gatekeeper's error form {"error":"<code>","message":"…"} (STEP2 §2.4), so errors are the
// package's ApiError and every screen words them the same way. Homie's answers may carry a little more, which
// ApiError.details keeps: the invalid fields of a tool's form, and the running call or job that a 409 points at.

import { api, ApiError, type RequestOptions } from '@devxpro/mcp-gatekeeper-ui'
import type { FieldError } from './types'

function client(base: string) {
  return {
    get: <T>(path: string, opts?: RequestOptions) => api.get<T>(base + path, opts),
    post: <T>(path: string, body: unknown = {}, opts?: RequestOptions) => api.post<T>(base + path, body, opts),
    patch: <T>(path: string, body: unknown, opts?: RequestOptions) => api.patch<T>(base + path, body, opts),
    del: <T>(path: string, opts?: RequestOptions) => api.del<T>(base + path, undefined, opts),
  }
}

/** homeApi is /api/home/*: devices, tools, calls, jobs, the activity log, settings, the tunnel, the system. */
export const homeApi = client('/home')

/** firstrunApi is /api/firstrun/*, the LAN first-run page's own API (plain HTTP, a PIN cookie). */
export const firstrunApi = client('/firstrun')

/** isInvalid: a validation refusal, whichever name the hub gives it (the gatekeeper's `invalid`, or `invalid_args`). */
export function isInvalid(err: unknown): err is ApiError {
  return err instanceof ApiError && (err.code === 'invalid' || err.code === 'invalid_args')
}

/** invalidFields: the fields a 400 invalid_args marks (JSON Pointer paths), for the tool's form. */
export function invalidFields(err: unknown): FieldError[] {
  const fields = err instanceof ApiError ? err.details.fields : undefined
  return Array.isArray(fields) ? (fields as FieldError[]) : []
}

/** pointedAt: the running call or job that a 409 call_running or job_running names. */
export function pointedAt(err: unknown, key: 'callId' | 'jobId'): string | undefined {
  const id = err instanceof ApiError ? err.details[key] : undefined
  return typeof id === 'string' && id ? id : undefined
}
