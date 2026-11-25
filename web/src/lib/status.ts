// One glossary for every screen: technical states → a colour (tone) and an i18n key for the words. The tones and
// their classes are the package's; the tunnel replaces the relay (STEP2 §3.1).

import type { Tone } from '@devxpro/mcp-gatekeeper-ui'
import type { Attention, ItemState, PluginState, TunnelStatus } from './types'

export type { Tone }

/**
 * needsOwner is the attention that calls for the owner: everything but a quick tunnel, which Дом shows as its
 * «для пробы» banner rather than as a red dot that never goes away.
 */
export function needsOwner(items: Attention[] | undefined) {
  return (items ?? []).filter((a) => a.kind !== 'tunnel_quick')
}

/**
 * TunnelLook is how the home's reachability reads: online; reachable to cloudflared but failing the outside check;
 * connecting (the child starts or restarts); offline (the child keeps failing); stopped; or no tunnel yet.
 */
export type TunnelLook = 'connected' | 'unreachable' | 'connecting' | 'offline' | 'stopped' | 'not_configured'

export function tunnelLook(s: TunnelStatus | undefined): TunnelLook {
  if (!s) return 'connecting'
  if (s.mode === 'off') return 'not_configured'
  switch (s.state) {
    case 'connected':
      return 'connected'
    case 'probing':
      // The address is known; with an error the outside check failed, without one it has not run yet.
      return s.lastError ? 'unreachable' : 'connecting'
    case 'starting':
      return 'connecting'
    case 'failing':
      return 'offline'
    case 'stopped':
      return 'stopped'
  }
}

/** tunnelHost is the name the home is reachable at: the hostname, else the host of its address. */
export function tunnelHost(s: TunnelStatus | undefined) {
  return s?.hostname || hostOf(s?.url)
}

const TUNNEL_TONE: Record<TunnelLook, Tone> = {
  connected: 'success',
  unreachable: 'warning',
  connecting: 'warning',
  offline: 'danger',
  stopped: 'gray',
  not_configured: 'gray',
}

export function tunnelTone(look: TunnelLook): Tone {
  return TUNNEL_TONE[look]
}

/** hostOf is the bare host of a public URL, for the places that show it as a name. */
export function hostOf(url: string | undefined) {
  if (!url) return ''
  try {
    return new URL(url).host
  } catch {
    return url.replace(/^https?:\/\//, '').replace(/\/.*$/, '')
  }
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
