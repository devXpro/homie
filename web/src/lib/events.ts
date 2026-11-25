// The admin's live stream feeds the TanStack Query cache (ADMIN-API §4): every event carries a whole object, so the
// cache simply replaces what it holds. It arrives as server-sent events (GET /api/home/events), or,
// behind a Cloudflare quick tunnel, by long-polling the same ring of events (GET /api/home/events/poll?after=<seq>,
// docs/ADMIN-API.md §6). GatekeeperApp's `live` option says which.

import { api, isApiError, qk, type Account, type Me } from '@devxpro/mcp-gatekeeper-ui'
import type { QueryClient } from '@tanstack/react-query'
import { useEffect, useSyncExternalStore } from 'react'
import { homeApi } from './api'
import { updateBootstrap } from './bootstrap'
import { hq } from './queries'
import type { ActivityItem, Call, Job, List, Overview, PluginDetail, PluginSummary, PollAnswer, Settings, TunnelStatus } from './types'

type Handler<T> = (value: T) => void

/** A Bus is a tiny event emitter for what the cache alone cannot say (a call ended, an entry arrived). */
export function createBus<T>() {
  const handlers = new Set<Handler<T>>()
  return {
    emit(value: T) {
      for (const h of handlers) h(value)
    },
    on(h: Handler<T>) {
      handlers.add(h)
      return () => {
        handlers.delete(h)
      }
    },
  }
}

export const callBus = createBus<Call>()
export const jobBus = createBus<Job>()
export const activityBus = createBus<ActivityItem>()
export const pluginRemovedBus = createBus<string>()

let connected = false
const connListeners = new Set<() => void>()
function setConnected(v: boolean) {
  if (connected === v) return
  connected = v
  for (const l of connListeners) l()
}

/** useEventsConnected is false while the stream is down; screens then poll what they watch. */
export function useEventsConnected() {
  return useSyncExternalStore(
    (l) => {
      connListeners.add(l)
      return () => {
        connListeners.delete(l)
      }
    },
    () => connected,
    () => false,
  )
}

function replaceIn<T extends { id: string }>(list: T[], item: T, addIfMissing = true) {
  const i = list.findIndex((x) => x.id === item.id)
  if (i === -1) return addIfMissing ? [...list, item] : list
  const next = list.slice()
  next[i] = item
  return next
}

/** An assistant's call: what Дом shows under «Недавно». */
const fromAssistant = (a: ActivityItem) => a.via === 'chatgpt' || a.via === 'claude'

/** applyEvent updates the cache for one event; exported for tests. */
export function applyEvent(qc: QueryClient, type: string, data: unknown) {
  switch (type) {
    case 'resync':
      void qc.invalidateQueries()
      break
    case 'tunnel': {
      const s = data as TunnelStatus
      qc.setQueryData(hq.tunnel, s)
      qc.setQueryData<Overview>(hq.overview, (o) => o && { ...o, tunnel: s })
      void qc.invalidateQueries({ queryKey: hq.overview, refetchType: 'active' })
      updateBootstrap({ tunnel: { mode: s.mode, url: s.url } })
      // The connect page reads the address from the account: a quick tunnel's new one shows there at once.
      void qc.invalidateQueries({ queryKey: qk.account, refetchType: 'active' })
      break
    }
    case 'plugin': {
      const p = data as PluginSummary
      qc.setQueryData<List<PluginSummary>>(hq.plugins, (l) => l && { items: replaceIn(l.items, p) })
      qc.setQueryData<Overview>(hq.overview, (o) => o && { ...o, plugins: replaceIn(o.plugins, p) })
      qc.setQueryData<PluginDetail>(hq.plugin(p.id), (d) => {
        if (!d) return d
        const { tools: _counts, ...summary } = p
        return { ...d, ...summary }
      })
      void qc.invalidateQueries({ queryKey: hq.overview, refetchType: 'active' })
      break
    }
    case 'plugin.removed': {
      const { id } = data as { id: string }
      qc.setQueryData<List<PluginSummary>>(hq.plugins, (l) => l && { items: l.items.filter((p) => p.id !== id) })
      qc.setQueryData<Overview>(hq.overview, (o) => o && { ...o, plugins: o.plugins.filter((p) => p.id !== id) })
      pluginRemovedBus.emit(id)
      break
    }
    case 'tools': {
      const { pluginId } = data as { pluginId: string }
      void qc.invalidateQueries({ queryKey: hq.plugin(pluginId) })
      void qc.invalidateQueries({ queryKey: hq.tools })
      break
    }
    case 'call': {
      const c = data as Call
      qc.setQueryData(hq.call(c.id), c)
      qc.setQueryData<List<Call>>(hq.runningCalls, (l) => {
        const items = l?.items ?? []
        return { items: c.state === 'running' ? replaceIn(items, c) : items.filter((x) => x.id !== c.id) }
      })
      if (c.state === 'ok') void qc.invalidateQueries({ queryKey: hq.inventory(c.pluginId) })
      callBus.emit(c)
      break
    }
    case 'job': {
      const j = data as Job
      qc.setQueryData(hq.job(j.id), j)
      qc.setQueryData<List<Job>>(hq.runningJobs, (l) => {
        const items = l?.items ?? []
        return { items: j.state === 'running' ? replaceIn(items, j) : items.filter((x) => x.id !== j.id) }
      })
      if (j.state !== 'running') {
        void qc.invalidateQueries({ queryKey: hq.plugins })
        void qc.invalidateQueries({ queryKey: hq.overview })
        if (j.target) void qc.invalidateQueries({ queryKey: hq.plugin(j.target) })
        // An install or a removal changes what invites can tick and what people may use.
        void qc.invalidateQueries({ queryKey: hq.permissions })
        void qc.invalidateQueries({ queryKey: qk.account })
        if (j.kind === 'hub.update') void qc.invalidateQueries({ queryKey: hq.system })
      }
      jobBus.emit(j)
      break
    }
    case 'activity': {
      const a = data as ActivityItem
      if (fromAssistant(a)) {
        qc.setQueryData<Overview>(hq.overview, (o) => o && { ...o, recent: [a, ...o.recent.filter((x) => x.id !== a.id)].slice(0, 5) })
      }
      activityBus.emit(a)
      break
    }
    case 'settings': {
      // Owners get the whole Settings, members only {"homeName"}. The home's name is the account's (docs/ADMIN-API.md §3, Settings).
      const s = data as Partial<Settings>
      qc.setQueryData<Settings>(hq.settings, (old) => old && { ...old, ...s })
      if (s.homeName) {
        const name = s.homeName
        qc.setQueryData<Overview>(hq.overview, (o) => o && { ...o, home: { name } })
        qc.setQueryData<Me>(qk.me, (m) => m && { ...m, account: { ...m.account, name } })
        qc.setQueryData<Account>(qk.account, (a) => a && { ...a, name })
        updateBootstrap({ homeName: name })
      }
      break
    }
    case 'attention':
      // An alert raised now (an access reset on the home box, GK-5): Дом lists it.
      void qc.invalidateQueries({ queryKey: hq.overview })
      break
    case 'session.revoked':
      // The package's client sees the 401 of /api/me and sends the person to sign in.
      void api.get('/me').catch(() => undefined)
      break
  }
}

const TYPES = ['hello', 'resync', 'tunnel', 'plugin', 'plugin.removed', 'tools', 'call', 'job', 'activity', 'settings', 'attention', 'session.revoked']

/** helloWatcher reloads into the new SPA after a self-update: a hello with another version. */
function helloWatcher() {
  let version: string | undefined
  return (data: unknown) => {
    const v = (data as { version?: string } | undefined)?.version
    if (version && v && v !== version) window.location.reload()
    version = v ?? version
  }
}

function parse(e: MessageEvent): unknown {
  try {
    return JSON.parse(e.data as string)
  } catch {
    return undefined
  }
}

function sleep(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve) => {
    const id = setTimeout(resolve, ms)
    signal.addEventListener('abort', () => {
      clearTimeout(id)
      resolve()
    })
  })
}

/** streamSSE keeps one EventSource open; the browser reconnects with Last-Event-ID by itself. */
function streamSSE(qc: QueryClient) {
  if (typeof EventSource === 'undefined') return () => undefined
  const hello = helloWatcher()
  const es = new EventSource('/api/home/events')
  es.onopen = () => setConnected(true)
  es.onerror = () => setConnected(false)
  const listeners = TYPES.map((type) => {
    const fn = (e: MessageEvent) => {
      const data = parse(e)
      if (type === 'hello') {
        hello(data)
        setConnected(true)
        return
      }
      applyEvent(qc, type, data)
      if (type === 'session.revoked') es.close()
    }
    es.addEventListener(type, fn)
    return [type, fn] as const
  })
  return () => {
    for (const [type, fn] of listeners) es.removeEventListener(type, fn)
    es.close()
    setConnected(false)
  }
}

/**
 * pollEvents long-polls the same events: each answer holds what came after `after` (the server waits up to 20 s
 * for one, and may answer none), and its `cursor` is the next `after`. Errors back off from 2 to 10 seconds; an ended
 * session stops it.
 */
export async function pollEvents(qc: QueryClient, signal: AbortSignal) {
  const hello = helloWatcher()
  let after: string | undefined
  let failures = 0
  while (!signal.aborted) {
    try {
      const res = await homeApi.get<PollAnswer>('/events/poll', { query: { after }, signal })
      failures = 0
      setConnected(true)
      for (const e of res.events ?? []) {
        if (e.event === 'hello') hello(e.data)
        else applyEvent(qc, e.event, e.data)
      }
      if (res.cursor) after = res.cursor
    } catch (err) {
      if (signal.aborted) return
      setConnected(false)
      if (isApiError(err, 'no_session')) return
      failures++
      await sleep(Math.min(10_000, 2000 * failures), signal)
    }
  }
}

/** useHomeEvents follows the admin's events while signed in, by SSE or by polling (`live`). */
export function useHomeEvents(qc: QueryClient, live: 'sse' | 'poll', enabled: boolean) {
  useEffect(() => {
    if (!enabled) return
    if (live === 'sse') return streamSSE(qc)
    const ctl = new AbortController()
    void pollEvents(qc, ctl.signal)
    return () => {
      ctl.abort()
      setConnected(false)
    }
  }, [qc, live, enabled])
}

/**
 * usePluginLogStream follows one plugin's log lines: by SSE (`?logs=<id>`, appended to the logs query), or behind a
 * quick tunnel by fetching the tail again every few seconds.
 */
export function usePluginLogStream(qc: QueryClient, pluginId: string, live: 'sse' | 'poll') {
  useEffect(() => {
    if (live === 'poll') {
      const id = setInterval(() => void qc.invalidateQueries({ queryKey: hq.logs(pluginId) }), 3000)
      return () => clearInterval(id)
    }
    if (typeof EventSource === 'undefined') return
    const es = new EventSource(`/api/home/events?logs=${encodeURIComponent(pluginId)}`)
    const fn = (e: MessageEvent) => {
      const d = parse(e) as { pluginId?: string; lines?: string[] } | undefined
      if (!d?.lines || d.pluginId !== pluginId) return
      const lines = d.lines
      qc.setQueryData<{ lines: string[] }>(hq.logs(pluginId), (old) => ({ lines: [...(old?.lines ?? []), ...lines].slice(-5000) }))
    }
    es.addEventListener('plugin.log', fn)
    return () => {
      es.removeEventListener('plugin.log', fn)
      es.close()
    }
  }, [qc, pluginId, live])
}
