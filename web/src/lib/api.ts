// Homie's own API clients: /api/home/* (the admin) and /api/firstrun/* (the LAN first-run page). They answer in the
// gatekeeper's error form {"error":"<code>","message":"…"} (STEP2 §2.4), so errors are the package's ApiError and
// every screen words them the same way. Homie's answers may carry a little more, which this client keeps: the
// invalid fields of a form, and the running call or job that a 409 points at.

import { api, ApiError, brand, connectivity, OfflineError, type Method } from '@devxpro/mcp-gatekeeper-ui'
import type { FieldError } from './types'

/** HomeError is an ApiError with Homie's extras. */
export class HomeError extends ApiError {
  readonly fields: FieldError[]
  readonly callId?: string
  readonly jobId?: string

  constructor(status: number, code: string, message: string, extra: { fields?: FieldError[]; callId?: string; jobId?: string; retryAfter?: number } = {}) {
    super(status, code, message, { retryAfter: extra.retryAfter })
    this.fields = extra.fields ?? []
    this.callId = extra.callId
    this.jobId = extra.jobId
  }
}

/** isHomeError narrows an error to HomeError, optionally of one code. */
export function isHomeError(err: unknown, code?: string): err is HomeError {
  return err instanceof HomeError && (code === undefined || err.code === code)
}

/** isInvalid: a validation refusal, whichever name the hub gives it (the gatekeeper's `invalid`, or `invalid_args`). */
export function isInvalid(err: unknown): err is ApiError {
  return err instanceof ApiError && (err.code === 'invalid' || err.code === 'invalid_args')
}

/** A proxy in front of a stopped homed (cloudflared, Cloudflare's edge) answers these with its own HTML page. */
const UNREACHABLE = new Set([502, 503, 504, 520, 521, 522, 523, 524, 530])

let askingMe = false

/**
 * noSession lets the signed-in shell notice an ended session: the package's own client reports a 401 no_session to
 * the shell, so one cheap GET /api/me through it does (the shell then goes to the sign-in page with its toast).
 */
function noSession() {
  if (askingMe) return
  askingMe = true
  void api
    .get('/me')
    .catch(() => undefined)
    .finally(() => {
      askingMe = false
    })
}

const str = (v: unknown) => (typeof v === 'string' && v ? v : undefined)

/** errorOf reads an error body: the gatekeeper's flat form, or an older nested {"error":{"code",…}} one. */
function errorOf(status: number, statusText: string, data: unknown, retryAfter?: number): HomeError {
  const o = (data && typeof data === 'object' ? data : {}) as Record<string, unknown>
  const nested = o.error && typeof o.error === 'object' ? (o.error as Record<string, unknown>) : undefined
  const src = nested ?? o
  const code = str(nested ? nested.code : o.error) ?? 'generic'
  const fields = Array.isArray(src.fields) ? (src.fields as FieldError[]) : undefined
  return new HomeError(status, code, str(src.message) ?? statusText, {
    fields,
    callId: str(o.callId) ?? str(o.call_id) ?? str(src.callId),
    jobId: str(o.jobId) ?? str(o.job_id) ?? str(src.jobId),
    retryAfter,
  })
}

export interface RequestOptions {
  query?: Record<string, string | number | boolean | undefined>
  signal?: AbortSignal
}

async function request<T>(base: string, method: Method, path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  let url = base + path
  if (opts.query) {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(opts.query)) {
      if (v !== undefined && v !== '') q.set(k, String(v))
    }
    const s = q.toString()
    if (s) url += (url.includes('?') ? '&' : '?') + s
  }
  const headers: Record<string, string> = { Accept: 'application/json' }
  let payload: string | undefined
  if (method !== 'GET') {
    headers[brand().csrfHeader] = '1'
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json'
      payload = JSON.stringify(body)
    }
  }

  let res: Response
  try {
    res = await fetch(url, { method, headers, body: payload, credentials: 'same-origin', cache: 'no-store', signal: opts.signal })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    connectivity.reportOffline()
    throw new OfflineError()
  }

  const json = (res.headers.get('content-type') ?? '').includes('application/json')
  if (res.ok) {
    connectivity.reportOnline()
    if (res.status === 204 || !json) return undefined as T
    return (await res.json()) as T
  }
  if (!json) {
    if (UNREACHABLE.has(res.status)) {
      connectivity.reportOffline()
      throw new OfflineError()
    }
    throw new HomeError(res.status, res.status === 404 ? 'not_found' : 'generic', res.statusText)
  }
  connectivity.reportOnline()
  const retry = Number(res.headers.get('Retry-After'))
  const err = errorOf(res.status, res.statusText, await res.json().catch(() => null), Number.isFinite(retry) && retry > 0 ? retry : undefined)
  if (err.code === 'no_session') noSession()
  throw err
}

function client(base: string) {
  return {
    get: <T>(path: string, opts?: RequestOptions) => request<T>(base, 'GET', path, undefined, opts),
    post: <T>(path: string, body: unknown = {}, opts?: RequestOptions) => request<T>(base, 'POST', path, body, opts),
    patch: <T>(path: string, body: unknown, opts?: RequestOptions) => request<T>(base, 'PATCH', path, body, opts),
    put: <T>(path: string, body: unknown, opts?: RequestOptions) => request<T>(base, 'PUT', path, body, opts),
    del: <T>(path: string, opts?: RequestOptions) => request<T>(base, 'DELETE', path, undefined, opts),
  }
}

/** homeApi is /api/home/*: devices, tools, calls, jobs, the activity log, settings, the tunnel, the system. */
export const homeApi = client('/api/home')

/** firstrunApi is /api/firstrun/*, the LAN first-run page's own API (plain HTTP, a PIN cookie). */
export const firstrunApi = client('/api/firstrun')
