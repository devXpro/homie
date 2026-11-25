import { describe, expect, it } from 'vitest'
import { hostnameOk } from './hostname'

describe('hostnameOk', () => {
  it('accepts the address a home can live at', () => {
    for (const h of ['home.example.com', 'a.example.co', ' home.example.com ', 'x-1.home.example.net']) expect([h, hostnameOk(h)]).toEqual([h, true])
  })

  it('refuses everything else', () => {
    for (const h of ['', 'home', 'example', 'https://home.example.com', 'home.example.com/mcp', '-a.example.com', 'home..example.com', 'home.example.c0m']) {
      expect([h, hostnameOk(h)]).toEqual([h, false])
    }
  })
})
