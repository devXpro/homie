import { ACCOUNT_TAB, type PageHandle, type TabDef } from '@devxpro/mcp-gatekeeper-ui'
import { History, House, Lamp, Settings } from 'lucide-react'
import type { ComponentType } from 'react'
import type { RouteObject } from 'react-router'
import { ActivityPage } from '@/features/activity/ActivityPage'
import { DevicesPage } from '@/features/devices/DevicesPage'
import { LogsPage } from '@/features/devices/LogsPage'
import { ModulePage } from '@/features/devices/ModulePage'
import { HomePage } from '@/features/home/HomePage'
import { AboutPage } from '@/features/settings/AboutPage'
import { HomeNamePage } from '@/features/settings/HomeNamePage'
import { LanguagePage } from '@/features/settings/LanguagePage'
import { SettingsPage } from '@/features/settings/SettingsPage'
import { TunnelPage } from '@/features/settings/TunnelPage'
import { Landing } from '@/features/shell/Landing'
import { HomieLayer } from '@/features/shell/layer'

// Homie's tabs and routes inside the gatekeeper's shell (STEP2 §3.1 web row). The package owns /login, /setup,
// /join, /consent, /connect and /account/*; these are Homie's own.

const page = (path: string, Component: ComponentType, handle: PageHandle): RouteObject => ({ path, Component, handle })

/**
 * The tabs. Дом carries the id "connect" because the package's connect page (/connect) belongs to it: there the tab
 * stays lit, and tapping Дом goes back to /home. Люди is the package's account page.
 */
export const tabs: TabDef[] = [
  { id: 'connect', root: '/home', icon: House, label: 'tab.home' },
  { id: 'devices', root: '/devices', icon: Lamp, label: 'tab.devices' },
  { id: 'activity', root: '/activity', icon: History, label: 'tab.activity' },
  ACCOUNT_TAB,
  { id: 'settings', root: '/settings', icon: Settings, label: 'tab.settings' },
]

/**
 * Homie's pages, inside the shell, under one layout (the run and job sheets, the running-action pill). Sheets have
 * their own URL, served by the page under them, so back closes them. The activity entry's parameter is not "id": the
 * shell keys its page transitions by the handle and an "id" parameter, and opening an entry keeps the list's page.
 */
export const routes: RouteObject[] = [
  {
    Component: HomieLayer,
    children: [
      page('home', HomePage, { page: 'home', tab: 'connect', depth: 0 }),
      page('devices', DevicesPage, { page: 'devices', tab: 'devices', depth: 0 }),
      page('devices/add', DevicesPage, { page: 'devices', tab: 'devices', depth: 0 }),
      page('devices/:pluginId', ModulePage, { page: 'module', tab: 'devices', depth: 1 }),
      page('devices/:pluginId/items/:itemId', ModulePage, { page: 'module', tab: 'devices', depth: 1 }),
      page('devices/:pluginId/logs', LogsPage, { page: 'logs', tab: 'devices', depth: 2 }),
      page('activity', ActivityPage, { page: 'activity', tab: 'activity', depth: 0 }),
      page('activity/:entryId', ActivityPage, { page: 'activity', tab: 'activity', depth: 0 }),
      page('settings', SettingsPage, { page: 'settings', tab: 'settings', depth: 0 }),
      page('settings/home', HomeNamePage, { page: 'settings/home', tab: 'settings', depth: 1 }),
      page('settings/language', LanguagePage, { page: 'settings/language', tab: 'settings', depth: 1 }),
      page('settings/connection', TunnelPage, { page: 'settings/connection', tab: 'settings', depth: 1 }),
      page('settings/about', AboutPage, { page: 'settings/about', tab: 'settings', depth: 1 }),
    ],
  },
]

/**
 * Outside the shell: "/" picks the landing by role, and the LAN first-run page of the setup listener works without a
 * session. The package's shell has an index route of its own (to /connect); an application's public routes come
 * first in its table and rank the same, so this "/" wins (routes.test.ts pins that).
 */
export const publicRoutes: RouteObject[] = [
  { index: true, Component: Landing },
  { path: '/firstrun', lazy: async () => ({ Component: (await import('@/features/firstrun/LanSetup')).LanSetup }) },
]
