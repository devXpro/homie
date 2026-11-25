import { api, GatekeeperApp, i18n, useAccount, useMe, type ConnectSlots, type PermissionInfo, type Resource } from '@devxpro/mcp-gatekeeper-ui'
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { HomeTile } from '@/components/tile'
import { ConnectSteps, QuickNote, TunnelBadge } from '@/features/connect/slots'
import { HomieOverlay, useHomieShell } from '@/features/shell/layer'
import { liveOf, mcpUrl, useBootstrap } from '@/lib/bootstrap'
import { localCatalog } from '@/lib/permissions'
import { hq } from '@/lib/queries'
import type { HomeAccount } from '@/lib/types'
import { indexRoute, publicRoutes, routes, tabs } from '@/routes'

// Homie on the gatekeeper's UI package (@devxpro/mcp-gatekeeper-ui, see its README): the package owns sign-in, setup and join, consent, the
// connect page, people and passkeys, and the shell; Homie adds its tabs and pages (routes.ts), the LAN first-run
// page, its parts of the connect page, the device catalog of the access pickers, and how it follows changes.

/**
 * resources: the home is the one MCP resource, at https://<host>/mcp, always on. homed lists it on
 * GET /api/account (docs/ADMIN-API.md §5); the description is the admin's own words in the UI language.
 */
function resources(a: HomeAccount): Resource[] {
  const description = i18n.t('connect.home.description')
  const listed = a.resources?.filter((r) => r.id === 'home') ?? []
  if (listed.length > 0) return listed.map((r) => ({ ...r, description, mcp_url: mcpUrl(r.mcp_url) }))
  return [{ id: 'home', name: a.name, description, mcp_url: mcpUrl(), enabled: true }]
}

const connect: ConnectSlots = { Badge: TunnelBadge, Note: QuickNote, BodyEnd: ConnectSteps }

const NO_CATALOG: PermissionInfo[] = []

/**
 * usePluginCatalog is what an invite or a member's detail can tick (owners only): homed's catalog (GET
 * /api/account/permissions: "plugin:<id>" per installed plugin), titled as the admin names the devices.
 */
function usePluginCatalog(): PermissionInfo[] | undefined {
  const owner = useMe().data?.person.role === 'owner'
  const remote = useQuery({
    queryKey: hq.permissions,
    queryFn: async () => (await api.get<PermissionInfo[] | null>('/account/permissions')) ?? [],
    enabled: owner,
    staleTime: 60_000,
  })
  const catalog = useMemo(() => (remote.data ? localCatalog(remote.data) : undefined), [remote.data])
  return owner ? catalog : NO_CATALOG
}

/**
 * useQuickInvitesOff: a quick tunnel is for trying, the owner's alone: its address changes on every
 * restart, and every guest's passkey with it. The people list says so where «Пригласить» was.
 */
function useQuickInvitesOff(): string | undefined {
  const { t } = useTranslation()
  const account = useAccount<HomeAccount>().data
  const boot = useBootstrap()
  return (account?.tunnel?.mode ?? boot?.tunnel.mode) === 'quick' ? t('invite.off.quick') : undefined
}

export function App() {
  const boot = useBootstrap()
  return (
    <GatekeeperApp
      tabs={tabs}
      routes={routes}
      publicRoutes={publicRoutes}
      indexRoute={indexRoute}
      resources={(a) => resources(a as HomeAccount)}
      ResourceTile={HomeTile}
      useBadges={useHomieShell}
      connect={connect}
      setupDoneTo="/home"
      usePermissions={usePluginCatalog}
      useInvitesDisabled={useQuickInvitesOff}
      Overlay={HomieOverlay}
      live={liveOf(boot?.tunnel.mode)}
    />
  )
}
