import { makeQueryClient, qk, type Account, type Me } from '@devxpro/mcp-gatekeeper-ui'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { getBootstrap, loadBootstrap } from './bootstrap'
import { activityBus, applyEvent, pollEvents } from './events'
import { hq } from './queries'
import type { ActivityItem, Overview, PollAnswer, TunnelStatus } from './types'

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => vi.unstubAllGlobals())

const overview = (): Overview => ({
  home: { name: 'Дом' },
  tunnel: { mode: 'named', state: 'connected', url: 'https://home.example.com' },
  plugins: [],
  attention: [],
  recent: [],
})

const call = (id: string, via: ActivityItem['via']): ActivityItem => ({ id, at: '2026-11-25T10:00:00.000Z', via, tool: 'tv_power_on', ok: true, durationMs: 10 })

describe('applyEvent', () => {
  it('a renamed home renames the account the package shows', () => {
    const qc = makeQueryClient()
    qc.setQueryData<Me>(qk.me, { person: { id: 'per_alice', name: 'Alice', role: 'owner', superadmin: true }, account: { id: 'acc', name: 'Дом' } })
    qc.setQueryData<Account>(qk.account, { id: 'acc', name: 'Дом', members: [], invites: [] })
    qc.setQueryData(hq.overview, overview())
    applyEvent(qc, 'settings', { homeName: 'Дача' })
    expect(qc.getQueryData<Me>(qk.me)?.account.name).toBe('Дача')
    expect(qc.getQueryData<Account>(qk.account)?.name).toBe('Дача')
    expect(qc.getQueryData<Overview>(hq.overview)?.home.name).toBe('Дача')
  })

  it('a new tunnel address reaches the status and the connector address', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(200, { homeName: 'Дом', language: 'ru', ownerReady: true, tunnel: { mode: 'quick' } })))
    await loadBootstrap()
    const qc = makeQueryClient()
    qc.setQueryData(hq.overview, overview())
    const next: TunnelStatus = { mode: 'quick', state: 'connected', url: 'https://example-two.trycloudflare.com' }
    applyEvent(qc, 'tunnel', next)
    expect(qc.getQueryData(hq.tunnel)).toEqual(next)
    expect(qc.getQueryData<Overview>(hq.overview)?.tunnel.url).toBe('https://example-two.trycloudflare.com')
    expect(getBootstrap()?.tunnel.url).toBe('https://example-two.trycloudflare.com')
  })

  it('an assistant’s call goes to «Недавно»; every call reaches the log', () => {
    const qc = makeQueryClient()
    qc.setQueryData(hq.overview, overview())
    const seen: string[] = []
    const off = activityBus.on((a) => seen.push(a.id))
    applyEvent(qc, 'activity', call('1', 'claude'))
    applyEvent(qc, 'activity', call('2', 'admin'))
    off()
    expect(qc.getQueryData<Overview>(hq.overview)?.recent.map((a) => a.id)).toEqual(['1'])
    expect(seen).toEqual(['1', '2'])
  })
})

describe('pollEvents (quick mode)', () => {
  it('asks for what came after the cursor and applies it; an empty answer keeps the cursor', async () => {
    const answers: PollAnswer[] = [
      { events: [{ event: 'hello', data: { version: '0.2.0' } }], cursor: 'r1-4' },
      { events: [], cursor: 'r1-4' },
      { events: [{ id: 'r1-5', event: 'settings', data: { homeName: 'Дача' } }], cursor: 'r1-5' },
    ]
    const ctl = new AbortController()
    const urls: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        urls.push(url)
        const next = answers.shift()
        if (!next) {
          ctl.abort()
          throw new DOMException('aborted', 'AbortError')
        }
        return json(200, next)
      }),
    )
    const qc = makeQueryClient()
    qc.setQueryData(hq.overview, overview())
    await pollEvents(qc, ctl.signal)
    expect(urls).toEqual(['/api/home/events/poll', '/api/home/events/poll?after=r1-4', '/api/home/events/poll?after=r1-4', '/api/home/events/poll?after=r1-5'])
    expect(qc.getQueryData<Overview>(hq.overview)?.home.name).toBe('Дача')
  })

  it('stops when the session ends', async () => {
    const f = vi.fn(async () => json(401, { error: 'no_session', message: 'no session' }))
    vi.stubGlobal('fetch', f)
    await pollEvents(makeQueryClient(), new AbortController().signal)
    // One poll, then the package's own check of /api/me; no retry loop.
    expect(f.mock.calls.filter((c) => String((c as unknown as [string])[0]).includes('/events/poll'))).toHaveLength(1)
  })
})
