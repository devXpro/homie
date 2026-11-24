// Starts the mock API in the browser (`npm run dev:mock`, `npm run preview:mock`). Only imported when MODE is
// "mock", so production builds contain none of it.

import { setupWorker } from 'msw/browser'
import { configureFromUrl, handlers, setOffline } from './handlers'

declare global {
  interface Window {
    /** Hooks for the screenshot and smoke tests (mock builds only). */
    __homieMock?: { setOffline: (v: boolean) => void }
  }
}

export async function startMocks() {
  configureFromUrl()
  const worker = setupWorker(...handlers)
  // The worker is served under /admin/ with Service-Worker-Allowed: /, so it also controls /firstrun.
  await worker.start({
    onUnhandledFrame: 'bypass',
    quiet: true,
    serviceWorker: { url: '/admin/mockServiceWorker.js', options: { scope: '/' } },
  })
  window.__homieMock = { setOffline }
}
