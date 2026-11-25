import { ApiError, i18n } from '@devxpro/mcp-gatekeeper-ui'
import { describe, expect, it } from 'vitest'
import { errorText } from './errors'

describe('errorText', () => {
  const t = i18n.getFixedT('ru')

  it('shows homed’s reason when only the superadmin may do something', () => {
    const err = new ApiError(403, 'forbidden', 'only the home’s superadmin may do this: install a plugin')
    expect(errorText(err, t)).toBe('only the home’s superadmin may do this: install a plugin')
  })

  it('lets a plugin’s own failure text through', () => {
    expect(errorText(new ApiError(422, 'tool_failed', 'No webOS TV answered at 192.0.2.70.'), t)).toBe('No webOS TV answered at 192.0.2.70.')
  })

  it('words the other codes by their strings', () => {
    expect(errorText(new ApiError(409, 'quick_mode', 'invites need the home’s own domain'), t)).toBe(t('error.quick_mode'))
    expect(errorText(new ApiError(403, 'forbidden', ''), t)).toBe(t('error.forbidden'))
  })
})
