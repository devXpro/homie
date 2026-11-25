// The bootstrap (GET /api/home/bootstrap, public) is read once, before the router: the home's name and language and
// the tunnel's mode. The mode decides how the admin follows changes, `live` of GatekeeperApp: server-sent events, or
// long-polling behind a Cloudflare quick tunnel, which carries no SSE (docs/SECURITY.md, Quick mode). Live tunnel events keep it current.

import { i18n, storageKey } from '@devxpro/mcp-gatekeeper-ui'
import { useSyncExternalStore } from 'react'
import { homeApi } from './api'
import type { Bootstrap, Lang, TunnelMode } from './types'

let current: Bootstrap | undefined
/** settled: the first read answered or failed; the live stream waits for it, so a quick tunnel never sees SSE. */
let settled = false
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** getBootstrap is the last bootstrap read, or undefined before the first answer. */
export function getBootstrap() {
  return current
}

/** updateBootstrap merges what a live event says (a new tunnel address, a new home name). */
export function updateBootstrap(patch: Partial<Bootstrap>) {
  if (!current) return
  current = { ...current, ...patch, tunnel: { ...current.tunnel, ...patch.tunnel } }
  emit()
}

/** savedLang is the language this browser chose, if any (the package keeps it under "<prefix>.lang"). */
function savedLang(): string | null {
  try {
    return localStorage.getItem(storageKey('lang'))
  } catch {
    return null
  }
}

/**
 * loadBootstrap reads the bootstrap and applies the home's language when this browser has not chosen one: the
 * person's choice first, then the home's. It never throws: without an answer the admin starts with
 * SSE and the browser's language.
 */
export async function loadBootstrap() {
  try {
    current = await homeApi.get<Bootstrap>('/bootstrap')
    const lang: Lang | undefined = current.language === 'en' || current.language === 'ru' ? current.language : undefined
    if (lang && !savedLang() && i18n.language !== lang) {
      void i18n.changeLanguage(lang)
      document.documentElement.lang = lang
    }
  } catch {
    // no bootstrap: SSE and the browser's language
  } finally {
    settled = true
    emit()
  }
}

export function useBootstrap(): Bootstrap | undefined {
  return useSyncExternalStore(subscribe, getBootstrap, getBootstrap)
}

const getSettled = () => settled

/** useBootstrapSettled is true once the first bootstrap read answered or failed. */
export function useBootstrapSettled(): boolean {
  return useSyncExternalStore(subscribe, getSettled, getSettled)
}

/**
 * liveOf: quick tunnels carry no server-sent events, so the admin polls there. Without a bootstrap (its first read
 * failed) the page's own host tells: a quick tunnel's is always under trycloudflare.com.
 */
export function liveOf(mode: TunnelMode | undefined, host = window.location.hostname): 'sse' | 'poll' {
  if (mode) return mode === 'quick' ? 'poll' : 'sse'
  return host.endsWith('.trycloudflare.com') ? 'poll' : 'sse'
}

/** mcpUrl is the one connector address of the home: what the hub says, else the tunnel's, else this page's origin. */
export function mcpUrl(fromAccount?: string) {
  if (fromAccount) return fromAccount
  const base = current?.tunnel.url ?? window.location.origin
  return `${base.replace(/\/+$/, '')}/mcp`
}
