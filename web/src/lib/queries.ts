// TanStack Query hooks for every GET of /api/home/* (DESIGN.md §7): staleTime 10 s, refetch on focus, previous data
// kept while refetching. The gatekeeper's queries (me, account, connections, passkeys, the audit) are the package's;
// the live stream (events.ts) keeps both current. The query client itself is the package's (GatekeeperApp).

import { seg, useMe } from '@devxpro/mcp-gatekeeper-ui'
import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { homeApi } from './api'
import type {
  ActivityItem,
  Call,
  Inventory,
  Job,
  List,
  Overview,
  Page,
  PluginDetail,
  PluginPreview,
  PluginSummary,
  Settings,
  SystemInfo,
  Tool,
  TunnelStatus,
} from './types'

/** hq are Homie's query keys, all under "home" so they never meet the package's (qk). */
export const hq = {
  all: ['home'] as const,
  overview: ['home', 'overview'] as const,
  plugins: ['home', 'plugins'] as const,
  plugin: (id: string) => ['home', 'plugin', id] as const,
  inventory: (id: string) => ['home', 'inventory', id] as const,
  logs: (id: string) => ['home', 'logs', id] as const,
  preview: (repo: string) => ['home', 'preview', repo] as const,
  runningCalls: ['home', 'calls', 'running'] as const,
  call: (id: string) => ['home', 'call', id] as const,
  runningJobs: ['home', 'jobs', 'running'] as const,
  job: (id: string) => ['home', 'job', id] as const,
  activity: (filter: ActivityFilter) => ['home', 'activity', filter] as const,
  activityAll: ['home', 'activity'] as const,
  activityItem: (id: string) => ['home', 'activityItem', id] as const,
  tools: ['home', 'tools', 'admin'] as const,
  /** GET /api/account/permissions: the catalog of the access pickers (a gatekeeper route, Homie's data) */
  permissions: ['home', 'permissions'] as const,
  settings: ['home', 'settings'] as const,
  tunnel: ['home', 'tunnel'] as const,
  system: ['home', 'system'] as const,
}

/** useOwner: the signed-in person is an owner (the superadmin or a co-owner); members get their own views. */
export function useOwner() {
  return useMe().data?.person.role === 'owner'
}

export function useOverview(enabled = true) {
  return useQuery({ queryKey: hq.overview, queryFn: () => homeApi.get<Overview>('/overview'), enabled })
}

export function usePlugins(enabled = true) {
  return useQuery({ queryKey: hq.plugins, queryFn: () => homeApi.get<List<PluginSummary>>('/plugins'), enabled })
}

export function usePlugin(id: string) {
  return useQuery({ queryKey: hq.plugin(id), queryFn: () => homeApi.get<PluginDetail>(`/plugins/${seg(id)}`) })
}

export function useInventory(id: string, enabled: boolean) {
  return useQuery({
    queryKey: hq.inventory(id),
    queryFn: () => homeApi.get<Inventory>(`/plugins/${seg(id)}/inventory`),
    enabled,
    staleTime: 0,
  })
}

export function usePluginLogs(id: string) {
  return useQuery({
    queryKey: hq.logs(id),
    queryFn: () => homeApi.get<{ lines: string[] }>(`/plugins/${seg(id)}/logs`, { query: { tail: 1000 } }),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })
}

export function usePreview(repo: string | undefined) {
  return useQuery({
    queryKey: hq.preview(repo ?? ''),
    queryFn: () => homeApi.get<PluginPreview>('/plugins/preview', { query: { repo, version: 'latest' } }),
    enabled: !!repo,
    staleTime: 5 * 60_000,
  })
}

export function useRunningCalls(enabled = true) {
  return useQuery({
    queryKey: hq.runningCalls,
    queryFn: () => homeApi.get<List<Call>>('/calls', { query: { state: 'running' } }),
    enabled,
    staleTime: Infinity,
  })
}

export function useCall(id: string | undefined) {
  return useQuery({
    queryKey: hq.call(id ?? ''),
    queryFn: () => homeApi.get<Call>(`/calls/${seg(id ?? '')}`),
    enabled: !!id,
    staleTime: Infinity,
  })
}

export function useRunningJobs(enabled = true) {
  return useQuery({
    queryKey: hq.runningJobs,
    queryFn: () => homeApi.get<List<Job>>('/jobs', { query: { state: 'running' } }),
    enabled,
    staleTime: Infinity,
  })
}

export function useJob(id: string | undefined) {
  return useQuery({
    queryKey: hq.job(id ?? ''),
    queryFn: () => homeApi.get<Job>(`/jobs/${seg(id ?? '')}`),
    enabled: !!id,
    staleTime: Infinity,
  })
}

/** ActivityFilter mirrors the GET /activity query: who, which devices, from where, failed only. */
export interface ActivityFilter {
  via?: string
  status?: 'ok' | 'failed'
  person?: string
  plugin?: string
}

export function useActivity(filter: ActivityFilter) {
  return useInfiniteQuery({
    queryKey: hq.activity(filter),
    queryFn: ({ pageParam }) => homeApi.get<Page<ActivityItem>>('/activity', { query: { ...filter, limit: 50, cursor: pageParam } }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    placeholderData: keepPreviousData,
  })
}

export function useActivityItem(id: string) {
  return useQuery({ queryKey: hq.activityItem(id), queryFn: () => homeApi.get<ActivityItem>(`/activity/${seg(id)}`) })
}

export function useSettings(enabled = true) {
  return useQuery({ queryKey: hq.settings, queryFn: () => homeApi.get<Settings>('/settings'), enabled })
}

export function useTunnel(enabled = true) {
  return useQuery({ queryKey: hq.tunnel, queryFn: () => homeApi.get<TunnelStatus>('/tunnel'), enabled })
}

export function useSystem(enabled = true) {
  return useQuery({ queryKey: hq.system, queryFn: () => homeApi.get<SystemInfo>('/system'), enabled })
}

/** useAllTools lists every tool once, for the translated titles in the activity log (owners only). */
export function useAllTools(enabled = true) {
  return useQuery({
    queryKey: hq.tools,
    queryFn: () => homeApi.get<List<Tool>>('/tools', { query: { audience: 'admin' } }),
    staleTime: 5 * 60_000,
    enabled,
  })
}
