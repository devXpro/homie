import { defineConfig, devices } from '@playwright/test'

// End-to-end against a REAL homed (PLAN T2.6), not the mock API: global-setup.ts builds homed with the admin
// embedded by `make web`, adds the fake plugin, runs homed on a temporary data dir with --dev-origin (passkeys for
// localhost over plain http, no tunnel) and hands its addresses to the test through the environment. Run it with
// `make e2e` from the repository root.
export default defineConfig({
  testDir: '.',
  testMatch: /real\.spec\.ts/,
  globalSetup: './global-setup.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 120_000,
  expect: { timeout: 15_000 },
  outputDir: '../../test-results/real',
  use: {
    ...devices['Desktop Chrome'],
    viewport: { width: 1280, height: 860 },
    locale: 'ru-RU',
    timezoneId: 'Europe/Warsaw',
    trace: 'retain-on-failure',
  },
})
