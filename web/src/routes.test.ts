import { CONNECT_TAB, ConnectPage, gatekeeperRoutes, tabVisible, type Me } from '@devxpro/mcp-gatekeeper-ui'
import { matchRoutes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { ActivityPage } from '@/features/activity/ActivityPage'
import { HomePage } from '@/features/home/HomePage'
import { Landing } from '@/features/shell/Landing'
import { indexRoute, publicRoutes, routes, tabs } from './routes'

// The route table GatekeeperApp builds from Homie's routes, and the tabs each role sees.

const table = gatekeeperRoutes({ routes, publicRoutes, indexRoute })
const leaf = (path: string) => {
  const m = matchRoutes(table, path)
  return m?.[m.length - 1]
}

describe('the route table', () => {
  it('sends "/" inside the shell to the landing', () => {
    const m = matchRoutes(table, '/')
    expect(m).toHaveLength(2)
    expect(m?.[1].route.Component).toBe(Landing)
  })

  it('keeps the package’s pages and adds Homie’s inside the shell', () => {
    expect(leaf('/connect')?.route.Component).toBe(ConnectPage)
    expect(leaf('/home')?.route.Component).toBe(HomePage)
    const entry = leaf('/activity/1099')
    expect(entry?.route.Component).toBe(ActivityPage)
    // Not "id": the shell would treat every entry as another page.
    expect(entry?.params).toEqual({ entryId: '1099' })
  })

  it('serves the LAN first-run page without the shell', () => {
    const m = matchRoutes(table, '/firstrun')
    expect(m).toHaveLength(1)
    expect(m?.[0].route.path).toBe('/firstrun')
  })
})

describe('the tabs', () => {
  const viewer = (role: 'owner' | 'member'): Me =>
    ({ person: { id: 'p', name: 'Alice', role, superadmin: false }, account: { id: 'a', name: 'Дом' } }) as unknown as Me
  const seen = (role: 'owner' | 'member') => tabs.filter((t) => tabVisible(t, viewer(role))).map((t) => t.root)

  it('give an owner Дом first, and anyone else the connect page there', () => {
    expect(seen('owner')).toEqual(['/home', '/devices', '/activity', '/account', '/settings'])
    expect(seen('member')).toEqual([CONNECT_TAB.root, '/devices', '/activity', '/account', '/settings'])
  })
})
