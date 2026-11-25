import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './styles/index.css'
import { App } from './App'
import { loadBootstrap } from './lib/bootstrap'
import { setupHomie } from './setup'

setupHomie()

// The mock API runs only in `--mode mock` builds; MODE is a build-time constant, so production drops this branch.
if (import.meta.env.MODE === 'mock') {
  const { startMocks } = await import('./mocks/browser')
  await startMocks()
}

// The tunnel's mode decides SSE or polling before the router starts; the page renders either way.
void loadBootstrap()

const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
}
