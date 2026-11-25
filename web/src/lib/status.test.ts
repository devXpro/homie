import { describe, expect, it } from 'vitest'
import { hostOf, tunnelHost, tunnelLook, tunnelTone } from './status'
import type { TunnelStatus } from './types'

const t = (s: Partial<TunnelStatus>): TunnelStatus => ({ mode: 'named', state: 'connected', url: 'https://home.example.com', ...s })

describe('tunnelLook', () => {
  it('reads the supervisor state and the outside check as words', () => {
    const cases: [TunnelStatus | undefined, string][] = [
      [undefined, 'connecting'],
      [t({ mode: 'off', state: 'stopped', url: undefined }), 'not_configured'],
      [t({}), 'connected'],
      [t({ state: 'probing' }), 'connecting'],
      [t({ state: 'probing', lastError: 'GET /healthz: 530' }), 'unreachable'],
      [t({ state: 'starting', url: undefined }), 'connecting'],
      [t({ state: 'failing', lastError: 'cloudflared exited' }), 'offline'],
      [t({ state: 'stopped' }), 'stopped'],
      [t({ mode: 'quick', url: 'https://example.trycloudflare.com' }), 'connected'],
    ]
    for (const [s, look] of cases) expect([s?.state, s?.lastError, tunnelLook(s)]).toEqual([s?.state, s?.lastError, look])
  })

  it('colours the looks', () => {
    expect(tunnelTone('connected')).toBe('success')
    expect(tunnelTone('unreachable')).toBe('warning')
    expect(tunnelTone('offline')).toBe('danger')
    expect(tunnelTone('not_configured')).toBe('gray')
  })
})

describe('hosts', () => {
  it('keeps the host of a public URL', () => {
    expect(hostOf('https://home.example.com')).toBe('home.example.com')
    expect(hostOf('https://example.trycloudflare.com/')).toBe('example.trycloudflare.com')
    expect(hostOf(undefined)).toBe('')
    expect(hostOf('not a url/path')).toBe('not a url')
  })

  it('prefers the tunnel’s hostname', () => {
    expect(tunnelHost(t({ hostname: 'home.example.com', url: 'https://other.example.com' }))).toBe('home.example.com')
    expect(tunnelHost(t({}))).toBe('home.example.com')
    expect(tunnelHost(undefined)).toBe('')
  })
})
