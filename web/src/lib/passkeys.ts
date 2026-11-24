// Passkey ceremonies in the browser (ADMIN-API §3.2): the server's options go to @simplewebauthn/browser as is,
// and its JSON answer goes back unchanged as `credential`.

import {
  browserSupportsWebAuthn,
  startAuthentication,
  startRegistration,
  WebAuthnError,
  type AuthenticationResponseJSON,
  type PublicKeyCredentialCreationOptionsJSON,
  type PublicKeyCredentialRequestOptionsJSON,
  type RegistrationResponseJSON,
} from '@simplewebauthn/browser'

export type CreationOptions = PublicKeyCredentialCreationOptionsJSON
export type RequestOptions = PublicKeyCredentialRequestOptionsJSON

/** What went wrong in a ceremony, in the words the screens use. */
export type CeremonyError = 'cancelled' | 'exists' | 'failed'

export function ceremonyError(err: unknown): CeremonyError {
  const name = err instanceof WebAuthnError || err instanceof DOMException || err instanceof Error ? err.name : ''
  if (err instanceof WebAuthnError) {
    if (err.code === 'ERROR_AUTHENTICATOR_PREVIOUSLY_REGISTERED') return 'exists'
    if (err.code === 'ERROR_CEREMONY_ABORTED') return 'cancelled'
  }
  if (name === 'NotAllowedError' || name === 'AbortError') return 'cancelled'
  if (name === 'InvalidStateError') return 'exists'
  return 'failed'
}

// The mock API (npm run dev:mock) answers with options no authenticator can sign for a real relying party, and a
// developer's Touch ID should not collect junk passkeys for localhost. So mock mode fakes the browser prompt unless
// localStorage "homie.mock.webauthn" is "real" (the Playwright smoke tests use Chrome's virtual authenticator with it).
// Production builds drop these branches: every call site tests MODE, a build-time constant, first.
function fakeMode(): 'off' | 'ok' | 'cancel' {
  if (import.meta.env.MODE !== 'mock') return 'off'
  const v = localStorage.getItem('homie.mock.webauthn')
  if (v === 'real') return 'off'
  return v === 'cancel' ? 'cancel' : 'ok'
}

async function fakePrompt<T>(value: T): Promise<T> {
  await new Promise((r) => setTimeout(r, 700))
  if (fakeMode() === 'cancel') throw new DOMException('The operation either timed out or was not allowed.', 'NotAllowedError')
  return value
}

const b64 = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')

export function webauthnSupported() {
  if (import.meta.env.MODE === 'mock' && fakeMode() !== 'off') return true
  return browserSupportsWebAuthn()
}

export async function getAssertion(optionsJSON: RequestOptions): Promise<AuthenticationResponseJSON> {
  if (import.meta.env.MODE === 'mock' && fakeMode() !== 'off') {
    return fakePrompt({
      id: b64('mock-credential'),
      rawId: b64('mock-credential'),
      type: 'public-key',
      response: { clientDataJSON: b64('{}'), authenticatorData: b64('a'), signature: b64('s'), userHandle: b64('u') },
      clientExtensionResults: {},
      authenticatorAttachment: 'platform',
    })
  }
  return startAuthentication({ optionsJSON })
}

export async function createCredential(optionsJSON: CreationOptions): Promise<RegistrationResponseJSON> {
  if (import.meta.env.MODE === 'mock' && fakeMode() !== 'off') {
    return fakePrompt({
      id: b64('mock-credential'),
      rawId: b64('mock-credential'),
      type: 'public-key',
      response: { clientDataJSON: b64('{}'), attestationObject: b64('o'), transports: ['internal', 'hybrid'] },
      clientExtensionResults: {},
      authenticatorAttachment: 'platform',
    })
  }
  return startRegistration({ optionsJSON })
}
