import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { msw } from 'msw/vite'
import type { Plugin } from 'vite'
import { defineConfig } from 'vite'

// The production CSP lives with the Go handler that sends it; the mock preview sends the same one, so the
// end-to-end tests run under the real policy.
const csp = readFileSync(fileURLToPath(new URL('../internal/web/csp.txt', import.meta.url)), 'utf8').trim()

const outDir = fileURLToPath(new URL('../internal/web/dist', import.meta.url))

// keepGitkeep restores internal/web/dist/.gitkeep after a build empties the directory, so `go:embed` always has a
// file to embed and git keeps the directory.
function keepGitkeep(): Plugin {
  return {
    name: 'homie-keep-gitkeep',
    apply: 'build',
    closeBundle() {
      writeFileSync(`${outDir}/.gitkeep`, '')
    },
  }
}

export default defineConfig(({ mode }) => {
  const mock = mode === 'mock'
  return {
    // The SPA lives at the root of homed (the gatekeeper's links are /setup#t=… and /join#t=…; homed
    // answers /admin* with 308 to /, see docs/ADMIN-API.md §8); Vite's SPA fallback serves index.html for its routes, /firstrun included.
    base: '/',
    resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
    plugins: [react(), tailwindcss(), mock && msw({ mode: 'worker-only' }), !mock && keepGitkeep()],
    build: {
      outDir: mock ? '.mock-dist' : outDir,
      emptyOutDir: true,
      assetsInlineLimit: 0,
      sourcemap: false,
      reportCompressedSize: false,
      // Libraries change less often than the app: separate chunks stay cached across homed updates.
      rolldownOptions: {
        output: {
          codeSplitting: {
            groups: [
              { name: 'react', test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/, priority: 30 },
              { name: 'router', test: /node_modules[\\/]react-router[\\/]/, priority: 30 },
              { name: 'motion', test: /node_modules[\\/](motion|framer-motion|motion-dom|motion-utils)[\\/]/, priority: 30 },
              // qrcode stays in the lazy chunks that show links (the LAN page, invites). The gatekeeper's UI package is
              // the app's own screens as sources: it is split like src/, so its lazy parts stay lazy.
              { name: 'vendor', test: /node_modules[\\/](?!qrcode|dijkstrajs|@devxpro[\\/]mcp-gatekeeper-ui[\\/])/, priority: 10 },
            ],
          },
        },
      },
    },
    server: {
      port: 5173,
      // Against a real homed started with `--dev-origin http://localhost:5173`. changeOrigin keeps homed's Host
      // allowlist happy; the Origin header stays the dev origin.
      proxy: mock ? undefined : { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: true } },
    },
    preview: {
      port: 4173,
      headers: { 'Content-Security-Policy': csp },
    },
  }
})
