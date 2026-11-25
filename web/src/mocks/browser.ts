// Starts the mock API in the browser (`npm run dev:mock`, `npm run preview:mock`). Only imported when MODE is
// "mock", so production builds contain none of it.

import { setupWorker } from 'msw/browser'
import { configureFromUrl, finishLogin, forgetPin, handlers, restartQuick, setOffline } from './handlers'

declare global {
  interface Window {
    /** Hooks for the screenshot and smoke tests (mock builds only). */
    __homieMock?: { setOffline: (v: boolean) => void; finishLogin: () => void; restartQuick: () => void; forgetPin: () => void }
  }
}

export async function startMocks() {
  configureFromUrl()
  const worker = setupWorker(...handlers)
  await worker.start({ onUnhandledFrame: 'bypass', quiet: true, serviceWorker: { url: '/mockServiceWorker.js' } })
  window.__homieMock = { setOffline, finishLogin, restartQuick, forgetPin }
}
