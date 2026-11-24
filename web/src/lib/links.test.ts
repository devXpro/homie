import { describe, expect, it } from 'vitest'
import { httpsLink } from './links'

describe('httpsLink', () => {
  it('keeps plain https links', () => {
    expect(httpsLink('https://example.com/homie-tv')?.host).toBe('example.com')
    expect(httpsLink(' https://example.com ')?.href).toBe('https://example.com/')
  })

  it('drops everything else', () => {
    for (const raw of [undefined, '', 'javascript:alert(document.domain)', 'http://example.com', 'https://user:pw@example.com', '//example.com', '/admin', 'data:text/html,hi']) {
      expect(httpsLink(raw)).toBeUndefined()
    }
  })
})
