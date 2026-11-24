// The glossary of DESIGN.md §10: technical states → a colour (tone) and an i18n key for the words.

import type { ItemState, PluginState, RelayStatus } from './types'

export type Tone = 'success' | 'warning' | 'danger' | 'gray'

export type RelayLook = 'connected' | 'unreachable' | 'connecting' | 'offline' | 'not_configured'

export function relayLook(r: RelayStatus | undefined): RelayLook {
  if (!r) return 'connecting'
  if (r.state === 'connected') return r.selfCheck && !r.selfCheck.ok ? 'unreachable' : 'connected'
  return r.state
}

const RELAY_TONE: Record<RelayLook, Tone> = {
  connected: 'success',
  unreachable: 'warning',
  connecting: 'warning',
  offline: 'danger',
  not_configured: 'gray',
}

export function relayTone(look: RelayLook): Tone {
  return RELAY_TONE[look]
}

const PLUGIN_TONE: Record<PluginState, Tone> = {
  starting: 'warning',
  up: 'success',
  failing: 'danger',
  stopped: 'gray',
  disabled: 'gray',
}

export function pluginTone(s: PluginState): Tone {
  return PLUGIN_TONE[s]
}

const ITEM_TONE: Record<ItemState, Tone> = {
  on: 'success',
  off: 'gray',
  offline: 'danger',
  unknown: 'gray',
  paired: 'success',
  unpaired: 'warning',
}

export function itemTone(s: ItemState | undefined): Tone {
  return ITEM_TONE[s ?? 'unknown']
}

export const TONE_BG: Record<Tone, string> = {
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
  gray: 'bg-gray',
}

export const TONE_TEXT: Record<Tone, string> = {
  success: 'text-success-text',
  warning: 'text-warning-text',
  danger: 'text-danger-text',
  gray: 'text-label-2',
}

/** TONE_STATUS sets --status for the hero glow and the orb (index.css). */
export const TONE_STATUS: Record<Tone, string> = {
  success: 'homie-status-success',
  warning: 'homie-status-warning',
  danger: 'homie-status-danger',
  gray: 'homie-status-gray',
}
