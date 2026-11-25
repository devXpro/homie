// Renders the app icon (the house glyph on the blue-to-indigo tile, as the AppIcon of @devxpro/mcp-gatekeeper-ui draws
// it with brand.icon = House) to the PNGs iOS and the web manifest need: node scripts/icons.mjs
// Full-bleed squares: iOS rounds the corners itself.
import { writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { chromium } from '@playwright/test'

const out = (name) => fileURLToPath(new URL(`../public/${name}`, import.meta.url))
const house =
  '<path d="M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8"/><path d="M3 10a2 2 0 0 1 .709-1.528l7-6a2 2 0 0 1 2.582 0l7 6A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>'
const svg = (size) => `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 64 64">
  <defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#0A84FF"/><stop offset="1" stop-color="#5E5CE6"/></linearGradient></defs>
  <rect width="64" height="64" fill="url(#g)"/>
  <g fill="none" stroke="#fff" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" transform="translate(15.36 15.36) scale(1.387)">${house}</g>
</svg>`

const browser = await chromium.launch()
const page = await browser.newPage()
for (const [name, size] of [['apple-touch-icon.png', 180], ['icon-192.png', 192], ['icon-512.png', 512]]) {
  await page.setViewportSize({ width: size, height: size })
  await page.setContent(`<html><body style="margin:0;background:#000">${svg(size)}</body></html>`)
  writeFileSync(out(name), await page.screenshot({ type: 'png', clip: { x: 0, y: 0, width: size, height: size } }))
}
await browser.close()
