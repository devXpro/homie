// What an invite or a member's detail can tick (STEP2 §2.4 Q1): a device is a plugin, its permission id is
// "plugin:<id>", and "all" (the package adds it) covers the plugins installed later too. homed's catalog has the
// plugins' own titles; the pickers show the names the admin uses for them (moduleTitle in lib/tools.ts), in both
// languages, so they follow a language switch.

import { i18n, type PermissionInfo } from '@devxpro/mcp-gatekeeper-ui'
import { moduleTitle } from './tools'

export const PLUGIN_PREFIX = 'plugin:'

export function pluginPermission(id: string) {
  return `${PLUGIN_PREFIX}${id}`
}

/** deviceTitle is a plugin's name in both languages, as the admin shows it. */
export function deviceTitle(id: string, title: string) {
  return { ru: moduleTitle({ id, title }, i18n.getFixedT('ru')), en: moduleTitle({ id, title }, i18n.getFixedT('en')) }
}

/** localCatalog keeps homed's catalog (ids, order, groups) and titles its plugins as the admin does. */
export function localCatalog(catalog: PermissionInfo[]): PermissionInfo[] {
  return catalog.map((p) => {
    if (!p.id.startsWith(PLUGIN_PREFIX) || typeof p.title !== 'string') return p
    const out: PermissionInfo = { id: p.id, title: deviceTitle(p.id.slice(PLUGIN_PREFIX.length), p.title) }
    if (p.group) out.group = p.group
    return out
  })
}
