import { isApiError, isOffline, onNoSession } from '@devxpro/mcp-gatekeeper-ui'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { firstrunApi, homeApi, invalidFields, isInvalid, pointedAt } from './api'

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

  it('reads the gatekeeper error form and keeps what a 409 points at', async () => {
    stub(
      json(409, { error: 'call_running', message: 'the tool already runs', callId: 'c_1' }),
      json(409, { error: 'job_running', message: 'busy', jobId: 'j_1' }),
      json(409, { error: 'conflict', message: 'no id' }),
    )
    const call = await homeApi.post('/tools/tv_pair/calls', { args: {} }).catch((e: unknown) => e)
    expect(isApiError(call, 'call_running')).toBe(true)
    expect(pointedAt(call, 'callId')).toBe('c_1')
    const job = await homeApi.post('/plugins/tv/update').catch((e: unknown) => e)
    expect(pointedAt(job, 'jobId')).toBe('j_1')
    expect(pointedAt(await homeApi.post('/plugins').catch((e: unknown) => e), 'jobId')).toBeUndefined()
  })

  it('keeps the invalid fields of a tool’s form', async () => {
    stub(json(400, { error: 'invalid_args', message: 'bad', fields: [{ path: '/percent', code: 'maximum', message: 'too big' }] }))
    const bad = await homeApi.post('/tools/tv_set_volume/calls').catch((e: unknown) => e)
    expect(isInvalid(bad)).toBe(true)
    expect(invalidFields(bad).map((f) => f.path)).toEqual(['/percent'])
    expect(invalidFields(new Error('x'))).toEqual([])
  })

  it('treats Cloudflare’s HTML error page as the home being offline', async () => {
    stub(new Response('<h1>Error 1033</h1>', { status: 530, headers: { 'Content-Type': 'text/html' } }))
    expect(isOffline(await homeApi.get('/overview').catch((e: unknown) => e))).toBe(true)
  })

  it('tells the shell about an ended session', async () => {
    stub(json(401, { error: 'no_session', message: 'no session' }))
    const listener = vi.fn()
    const off = onNoSession(listener)
    await homeApi.get('/overview').catch(() => undefined)
    expect(listener).toHaveBeenCalledOnce()
    off()
  })
})

describe('firstrunApi', () => {
  it('talks to /api/firstrun and keeps Retry-After', async () => {
    const f = stub(new Response(JSON.stringify({ error: 'rate_limited', message: 'wait' }), { status: 429, headers: { 'Content-Type': 'application/json', 'Retry-After': '540' } }))
    const err = await firstrunApi.post('/pin', { pin: '00000000' }).catch((e: unknown) => e)
    expect((f.mock.calls[0] as unknown as [string])[0]).toBe('/api/firstrun/pin')
    expect(isApiError(err, 'rate_limited') && err.retryAfter).toBe(540)
  })
})
