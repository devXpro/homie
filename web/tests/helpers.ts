import { generateKeyPairSync, randomBytes } from 'node:crypto'
import { expect, type Page } from '@playwright/test'

/** open loads a mock scenario (src/mocks/data.ts SCENARIOS); state lives in the page, so every test starts fresh. */
export async function open(page: Page, path: string, scenario = 'default', extra = '') {
  const [base, hash] = path.split('#')
  const sep = base.includes('?') ? '&' : '?'
  await page.goto(`${base}${sep}mock=${scenario}${extra}${hash ? `#${hash}` : ''}`)
}

/**
 * useRealWebAuthn makes the mock app call navigator.credentials (instead of its fake prompt) and gives the page a
 * Chrome virtual authenticator with Touch ID-like user verification.
 */
export async function useRealWebAuthn(page: Page) {
  await page.addInitScript(() => localStorage.setItem('homie.mock.webauthn', 'real'))
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  })
  return {
    /** addPasskey stores a discoverable credential for localhost, as if the person had registered before. */
    async addPasskey() {
      const { privateKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' })
      await cdp.send('WebAuthn.addCredential', {
        authenticatorId,
        credential: {
          credentialId: randomBytes(16).toString('base64'),
          isResidentCredential: true,
          rpId: 'localhost',
          privateKey: privateKey.export({ format: 'der', type: 'pkcs8' }).toString('base64'),
          userHandle: Buffer.from('alice').toString('base64'),
          signCount: 0,
        },
      })
    },
    async credentials() {
      const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId })
      return credentials
    },
  }
}

/** guardCSP records Content-Security-Policy violations and uncaught errors; check them with expectClean. */
export async function guardCSP(page: Page) {
  const problems: string[] = []
  await page.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (e) => {
      const w = window as unknown as { __csp?: string[] }
      w.__csp = [...(w.__csp ?? []), `${e.violatedDirective} ${e.blockedURI}`]
    })
  })
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy|Refused to/i.test(m.text())) problems.push(m.text())
  })
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`))
  return {
    async expectClean() {
      const fromDom = await page.evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? []).catch(() => [])
      expect([...problems, ...fromDom]).toEqual([])
    },
  }
}

/** mock calls a test hook of the mock build (src/mocks/browser.ts). */
export async function mock(page: Page, hook: 'finishLogin' | 'restartQuick' | 'forgetPin' | 'offline' | 'online') {
  await page.evaluate((h) => {
    const m = (
      window as unknown as {
        __homieMock?: { setOffline: (v: boolean) => void; finishLogin: () => void; restartQuick: () => void; forgetPin: () => void }
      }
    ).__homieMock
    if (h === 'offline') m?.setOffline(true)
    else if (h === 'online') m?.setOffline(false)
    else m?.[h]()
  }, hook)
}
