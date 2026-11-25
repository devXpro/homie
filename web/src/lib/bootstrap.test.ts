import { describe, expect, it } from 'vitest'
import { liveOf } from './bootstrap'

describe('liveOf', () => {
  it.each([
    ['quick', 'home.example.com', 'poll'],
    ['named', 'example.trycloudflare.com', 'sse'],
    ['off', 'localhost', 'sse'],
    [undefined, 'example.trycloudflare.com', 'poll'],
    [undefined, 'home.example.com', 'sse'],
    [undefined, 'trycloudflare.com.example.com', 'sse'],
  ] as const)('mode %s on %s follows by %s', (mode, host, want) => {
    expect(liveOf(mode, host)).toBe(want)
  })
})
