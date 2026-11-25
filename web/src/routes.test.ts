import { ConnectPage, gatekeeperRoutes } from '@devxpro/mcp-gatekeeper-ui'
import { matchRoutes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { ActivityPage } from '@/features/activity/ActivityPage'
import { HomePage } from '@/features/home/HomePage'
import { Landing } from '@/features/shell/Landing'
import { publicRoutes, routes } from './routes'

// The route table GatekeeperApp builds from Homie's routes: "/" must reach Homie's landing, not the package's
// redirect to /connect (both rank the same; the application's public routes are listed first).

const table = gatekeeperRoutes({ routes, publicRoutes })
const leaf = (path: string) => {
  const m = matchRoutes(table, path)
  return m?.[m.length - 1]
}

describe('the route table', () => {
  it('sends "/" to the landing', () => {
    expect(leaf('/')?.route.Component).toBe(Landing)
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
