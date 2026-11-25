import { isOffline, onNoSession } from '@devxpro/mcp-gatekeeper-ui'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { firstrunApi, homeApi, isHomeError, isInvalid } from './api'

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json; charset=utf-8' } })

function stub(...answers: Response[]) {
  const fetchMock = vi.fn(async () => answers.shift() ?? json(500, { error: 'internal', message: 'no more answers' }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => vi.unstubAllGlobals())

describe('homeApi', () => {
  it('talks to /api/home with JSON and the X-Homie header on writes', async () => {
    const f = stub(json(200, { homeName: 'Дом', language: 'ru' }))
    await homeApi.patch('/settings', { homeName: 'Дом' })
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/home/settings')
    expect(init.method).toBe('PATCH')
    expect((init.headers as Record<string, string>)['X-Homie']).toBe('1')
    expect(init.body).toBe('{"homeName":"Дом"}')
  })

  it('reads the gatekeeper error form and keeps the call a 409 points at', async () => {
    stub(json(409, { error: 'call_running', message: 'the tool already runs', callId: 'c_1' }))
    const err = await homeApi.post('/tools/tv_pair/calls', { args: {} }).catch((e: unknown) => e)
    expect(isHomeError(err, 'call_running')).toBe(true)
    expect(isHomeError(err) && err.callId).toBe('c_1')
  })

  it('accepts snake_case extras and the older nested form', async () => {
    stub(json(409, { error: 'job_running', message: 'busy', job_id: 'j_1' }), json(400, { error: { code: 'invalid_args', message: 'bad', fields: [{ path: '/percent', code: 'maximum', message: 'too big' }] } }))
    const busy = await homeApi.post('/plugins/tv/update').catch((e: unknown) => e)
    expect(isHomeError(busy) && busy.jobId).toBe('j_1')
    const bad = await homeApi.post('/tools/tv_set_volume/calls').catch((e: unknown) => e)
    expect(isInvalid(bad)).toBe(true)
    expect(isHomeError(bad) && bad.fields.map((f) => f.path)).toEqual(['/percent'])
  })

  it('treats Cloudflare’s HTML error page as the home being offline', async () => {
    stub(new Response('<h1>Error 1033</h1>', { status: 530, headers: { 'Content-Type': 'text/html' } }))
    expect(isOffline(await homeApi.get('/overview').catch((e: unknown) => e))).toBe(true)
  })

  it('lets the shell notice an ended session through the package’s own client', async () => {
    const f = stub(json(401, { error: 'no_session', message: 'no session' }), json(401, { error: 'no_session', message: 'no session' }))
    const listener = vi.fn()
    const off = onNoSession(listener)
    await homeApi.get('/overview').catch(() => undefined)
    await vi.waitFor(() => expect(listener).toHaveBeenCalled())
    expect((f.mock.calls[1] as unknown as [string])[0]).toBe('/api/me')
    off()
  })
})

describe('firstrunApi', () => {
  it('talks to /api/firstrun and keeps Retry-After', async () => {
    const f = stub(new Response(JSON.stringify({ error: 'rate_limited', message: 'wait' }), { status: 429, headers: { 'Content-Type': 'application/json', 'Retry-After': '540' } }))
    const err = await firstrunApi.post('/pin', { pin: '00000000' }).catch((e: unknown) => e)
    expect((f.mock.calls[0] as unknown as [string])[0]).toBe('/api/firstrun/pin')
    expect(isHomeError(err, 'rate_limited') && err.retryAfter).toBe(540)
  })
})
