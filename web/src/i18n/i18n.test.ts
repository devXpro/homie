import { ApiError, errorText, i18n } from '@devxpro/mcp-gatekeeper-ui'
import { describe, expect, it } from 'vitest'
import en from './en.json'
import ru from './ru.json'

// Plural forms differ by language (RU _one/_few/_many/_other, EN _one/_other), so keys are compared without them.
const base = (keys: string[]) => new Set(keys.map((k) => k.replace(/_(zero|one|two|few|many|other)$/, '')))

describe('translations', () => {
  it('RU and EN have the same keys', () => {
    const r = base(Object.keys(ru))
    const e = base(Object.keys(en))
    expect([...r].filter((k) => !e.has(k))).toEqual([])
    expect([...e].filter((k) => !r.has(k))).toEqual([])
  })

  it('RU plurals have every form', () => {
    const plurals = Object.keys(ru).filter((k) => k.endsWith('_one')).map((k) => k.slice(0, -4))
    for (const p of plurals) {
      for (const form of ['one', 'few', 'many', 'other']) expect(ru).toHaveProperty([`${p}_${form}`])
    }
  })

  it('words homed’s own refusals, wherever the package shows an error', () => {
    const t = i18n.getFixedT('ru')
    expect(errorText(new ApiError(409, 'quick_mode', 'invites need the home’s own domain'), t)).toBe(ru['error.quick_mode'])
    expect(errorText(new ApiError(421, 'misdirected', 'not the public host'), t)).toBe(ru['error.misdirected'])
  })

  it('every interpolation in RU exists in EN', () => {
    const vars = (s: string) => [...s.matchAll(/{{(\w+)}}/g)].map((m) => m[1]).sort()
    const enMap = en as Record<string, string>
    for (const [k, v] of Object.entries(ru as Record<string, string>)) {
      const other = enMap[k] ?? enMap[k.replace(/_(few|many)$/, '_other')]
      if (other !== undefined) expect([k, vars(other)]).toEqual([k, vars(v)])
    }
  })
})
