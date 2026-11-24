// The admin API client (docs/ADMIN-API.md §1): JSON both ways, the CSRF header on every write, error codes as
// ApiError, and "the home is offline" (a network error, or the relay's non-JSON 404 page) as OfflineError.

import type { ErrorBody, FieldError } from './types'

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly fields: FieldError[]
  readonly callId?: string
  readonly jobId?: string
  readonly retryAfter?: number

  constructor(
    status: number,
    code: string,
    message: string,
    extra: { fields?: FieldError[]; callId?: string; jobId?: string; retryAfter?: number } = {},
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = extra.fields ?? []
    this.callId = extra.callId
    this.jobId = extra.jobId
    this.retryAfter = extra.retryAfter
  }
}

export class OfflineError extends Error {
  constructor() {
    super('the home is offline')
    this.name = 'OfflineError'
  }
}

export function isApiError(err: unknown, code?: string): err is ApiError {
  return err instanceof ApiError && (code === undefined || err.code === code)
}

export function isOffline(err: unknown): err is OfflineError {
  return err instanceof OfflineError
}

type Listener = () => void

/** Connectivity tracks whether the home answers, and retries with backoff while it does not (DESIGN §8.12.1). */
class Connectivity {
  private offline = false
  private attempt = 0
  private timer: ReturnType<typeof setTimeout> | undefined
  private readonly listeners = new Set<Listener>()
  private readonly backListeners = new Set<Listener>()
  probeUrl = '/api/admin/bootstrap'

  subscribe = (listener: Listener) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  getSnapshot = () => this.offline

  /** onBack runs every time the home answers again after being offline. */
  onBack(listener: Listener) {
    this.backListeners.add(listener)
    return () => {
      this.backListeners.delete(listener)
    }
  }

  reportOffline() {
    if (this.offline) return
    this.offline = true
    this.attempt = 0
    this.emit()
    this.schedule()
  }

  reportOnline() {
    if (!this.offline) return
    this.offline = false
    clearTimeout(this.timer)
    this.emit()
    for (const l of this.backListeners) l()
  }

  /** retryNow probes at once: the Retry button, `online` and `visibilitychange`. */
  retryNow = () => {
    if (!this.offline) return
    clearTimeout(this.timer)
    void this.probe()
  }

  private schedule() {
    const delays = [2000, 5000, 10000]
    const delay = delays[this.attempt] ?? 30000
    this.attempt++
    clearTimeout(this.timer)
    this.timer = setTimeout(() => void this.probe(), delay)
  }

  private async probe() {
    try {
      const res = await fetch(this.probeUrl, { cache: 'no-store', credentials: 'same-origin' })
      if ((res.headers.get('content-type') ?? '').includes('application/json')) {
        this.reportOnline()
        return
      }
    } catch {
      // still offline
    }
    if (this.offline) this.schedule()
  }

  private emit() {
    for (const l of this.listeners) l()
  }
}

export const connectivity = new Connectivity()

if (typeof window !== 'undefined') {
  window.addEventListener('online', connectivity.retryNow)
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') connectivity.retryNow()
  })
}

const unauthenticatedListeners = new Set<Listener>()

/** onUnauthenticated runs when any admin request answers 401 unauthenticated (DESIGN §7). */
export function onUnauthenticated(listener: Listener) {
  unauthenticatedListeners.add(listener)
  return () => {
    unauthenticatedListeners.delete(listener)
  }
}

export type Method = 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'

export interface RequestOptions {
  /** API base; the LAN first-run page uses /api/firstrun. */
  base?: string
  query?: Record<string, string | number | boolean | undefined>
  signal?: AbortSignal
}

export async function request<T>(method: Method, path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  let url = (opts.base ?? '/api/admin') + path
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
    headers['X-Homie'] = '1'
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json'
      payload = JSON.stringify(body)
    }
  }

  let res: Response
  try {
    res = await fetch(url, {
      method,
      headers,
      body: payload,
      credentials: 'same-origin',
      cache: 'no-store',
      signal: opts.signal,
    })
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
    // The relay answers for an offline home with its own HTML page and status 404 (PLAN §3); a proxy in front of
    // a stopped relay answers 502–504 without JSON. Both mean the home cannot be reached.
    if (res.status === 404 || res.status >= 502) {
      connectivity.reportOffline()
      throw new OfflineError()
    }
    throw new ApiError(res.status, 'generic', res.statusText)
  }
  connectivity.reportOnline()
  const data = (await res.json().catch(() => null)) as ErrorBody | null
  const e = data?.error
  const retry = Number(res.headers.get('Retry-After'))
  const err = new ApiError(res.status, e?.code ?? 'generic', e?.message ?? res.statusText, {
    fields: e?.fields,
    callId: data?.callId ?? e?.callId,
    jobId: data?.jobId ?? e?.jobId,
    retryAfter: Number.isFinite(retry) && retry > 0 ? retry : undefined,
  })
  if (err.code === 'unauthenticated') {
    for (const l of unauthenticatedListeners) l()
  }
  throw err
}

export const api = {
  get: <T>(path: string, opts?: RequestOptions) => request<T>('GET', path, undefined, opts),
  post: <T>(path: string, body: unknown = {}, opts?: RequestOptions) => request<T>('POST', path, body, opts),
  patch: <T>(path: string, body: unknown, opts?: RequestOptions) => request<T>('PATCH', path, body, opts),
  put: <T>(path: string, body: unknown, opts?: RequestOptions) => request<T>('PUT', path, body, opts),
  del: <T>(path: string, opts?: RequestOptions) => request<T>('DELETE', path, undefined, opts),
}
