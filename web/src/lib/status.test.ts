import { describe, expect, it } from 'vitest'
import { hostOf, needsOwner, tunnelHost, tunnelLook, tunnelTone } from './status'
import type { Attention, TunnelStatus } from './types'

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

describe('needsOwner', () => {
  const items: Attention[] = [
    { kind: 'tunnel_offline' },
    { kind: 'hub_update', version: '0.3.0' },
    { kind: 'plugin_update', target: 'tv', version: '0.3.0' },
    { kind: 'plugin_failing', target: 'tv' },
    { kind: 'tunnel_quick' },
  ]

  it('leaves the quick tunnel to its banner', () => {
    expect(needsOwner(items).map((a) => a.kind)).toEqual(['tunnel_offline', 'hub_update', 'plugin_update', 'plugin_failing'])
  })

  it('does not call a co-owner for updates, which are the superadmin’s to run', () => {
    expect(needsOwner(items, false).map((a) => a.kind)).toEqual(['tunnel_offline', 'plugin_failing'])
  })
})
