import '@testing-library/jest-dom/vitest'
import { setLanguage } from '@devxpro/mcp-gatekeeper-ui'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'
import { setupHomie } from '@/setup'

// jsdom has no matchMedia; tests run in the desktop layout (native selects, dialogs instead of drawers).
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: query.includes('min-width'),
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  }),
})

// The brand, the people options and the strings, as main.tsx sets them; the tests read Russian.
setupHomie()
setLanguage('ru')

afterEach(() => cleanup())

// jsdom does not scroll.
window.scrollTo = () => undefined
