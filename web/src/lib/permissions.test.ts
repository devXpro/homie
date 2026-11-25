import { label } from '@devxpro/mcp-gatekeeper-ui'
import { describe, expect, it } from 'vitest'
import { localCatalog, pluginPermission } from './permissions'

describe('the device catalog of the access pickers', () => {
  it('keeps homed’s ids and order and titles the plugins in both languages as the admin names them', () => {
    const catalog = localCatalog([
      { id: 'plugin:tv', title: 'TV', group: '' },
      { id: 'plugin:lights', title: 'Lights' },
      { id: 'scenes', title: 'Scenes' },
    ])
    expect(catalog.map((p) => p.id)).toEqual(['plugin:tv', 'plugin:lights', 'scenes'])
    expect(label(catalog[0].title, 'ru')).toBe('Телевизоры')
    expect(label(catalog[0].title, 'en')).toBe('TVs')
    expect(catalog[0].group).toBeUndefined()
    expect(label(catalog[1].title, 'ru')).toBe('Lights')
    // Not a plugin: kept as homed sends it.
    expect(catalog[2]).toEqual({ id: 'scenes', title: 'Scenes' })
  })

  it('names a plugin’s permission as homed does', () => {
    expect(pluginPermission('tv')).toBe('plugin:tv')
  })
})
