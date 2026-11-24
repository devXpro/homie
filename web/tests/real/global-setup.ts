import { type ChildProcess, execFileSync, spawn } from 'node:child_process'
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// Builds homed and the fake plugin into a fresh temporary directory, starts homed there and exports what the test
// needs (E2E_*). Nothing touches the real data dir: every command gets --data-dir, and HOMED_DATA_DIR points into the
// temporary directory too. The returned function stops homed and removes the directory.

const repo = fileURLToPath(new URL('../../..', import.meta.url))

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = createServer()
    srv.once('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address()
      const port = typeof addr === 'object' && addr ? addr.port : 0
      srv.close(() => resolve(port))
    })
  })
}

async function waitForHealth(url: string, homed: ChildProcess, log: string) {
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    if (homed.exitCode !== null) break
    try {
      const res = await fetch(url)
      if (res.ok) return
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`homed did not come up on ${url}:\n${readFileSync(log, 'utf8')}`)
}

export default async function globalSetup() {
  // Under /tmp and short: the unix socket path must fit in 103 bytes.
  const work = mkdtempSync('/tmp/homie-e2e-')
  const homedBin = path.join(work, 'homed')
  const fakeBin = path.join(work, 'fakeplugin')
  const data = path.join(work, 'data')
  const log = path.join(work, 'homed.log')
  const env = { ...process.env, HOMED_DATA_DIR: data }

  execFileSync('go', ['-C', repo, 'build', '-o', homedBin, './cmd/homed'], { stdio: 'inherit' })
  execFileSync('go', ['-C', repo, 'build', '-o', fakeBin, './internal/plugins/testdata/fakeplugin'], { stdio: 'inherit' })
  execFileSync(homedBin, ['plugin', 'add', '--data-dir', data, '--id', 'fake', '--', fakeBin, 'serve'], { env, stdio: 'inherit' })

  const port = await freePort()
  const setupPort = await freePort()
  const origin = `http://localhost:${port}`
  const out = openSync(log, 'a')
  const homed = spawn(
    homedBin,
    ['run', '--data-dir', data, '--listen', `127.0.0.1:${port}`, '--setup-listen', `127.0.0.1:${setupPort}`, '--dev-origin', origin],
    { env, stdio: ['ignore', out, out] },
  )
  closeSync(out)
  await waitForHealth(`http://127.0.0.1:${port}/healthz`, homed, log)
  const ui = await fetch(`${origin}/admin/`)
  if (ui.status !== 200) {
    homed.kill('SIGTERM')
    throw new Error(`homed serves no admin UI (${ui.status}): run \`make web\` first, or use \`make e2e\``)
  }

  process.env.E2E_HOMED = homedBin
  process.env.E2E_DATA_DIR = data
  process.env.E2E_ORIGIN = origin
  process.env.E2E_API = `http://127.0.0.1:${port}`
  process.env.E2E_SETUP = `http://localhost:${setupPort}`
  process.env.E2E_LOG = log

  return async () => {
    if (homed.exitCode === null) {
      const exited = new Promise((r) => homed.once('exit', r))
      homed.kill('SIGTERM')
      await Promise.race([exited, new Promise((r) => setTimeout(r, 10_000))])
    }
    if (process.env.E2E_KEEP) {
      console.warn(`kept ${work} (E2E_KEEP)`)
      return
    }
    rmSync(work, { recursive: true, force: true })
  }
}
