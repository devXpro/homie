import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import { guardCSP } from '../helpers'

// One story against a real homed, nothing mocked: the LAN first-run page behind its PIN, the owner's passkey from a
// setup link (real WebAuthn ceremonies with Chrome's virtual authenticator) on the gatekeeper's onboarding, Дом, a
// plugin tool run from the admin with live progress, the call in the activity log, and an invite for a guest with a
// device and a term. global-setup.ts starts homed in development mode (no tunnel); run it with `make e2e`.

function env(name: string): string {
  const v = process.env[name]
  if (!v) throw new Error(`${name} is not set: run this suite with \`make e2e\``)
  return v
}

/** homed runs the CLI against the test's homed, over its socket. */
function homed(...args: string[]): string {
  return execFileSync(env('E2E_HOMED'), [...args, '--data-dir', env('E2E_DATA_DIR')], {
    encoding: 'utf8',
    env: { ...process.env, HOMED_DATA_DIR: env('E2E_DATA_DIR') },
  })
}

test('first-run page → passkey → Дом → tool with progress → activity → invite', async ({ page, request }) => {
  const csp = await guardCSP(page)
  const origin = env('E2E_ORIGIN')
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

  await test.step('the first-run page: the PIN from `homed admin firstrun`, then the choice of a tunnel', async () => {
    await page.goto(`${env('E2E_SETUP')}/firstrun`)
    await expect(page.getByRole('heading', { name: 'Введите PIN' })).toBeVisible()
    // homed runs without a terminal here (its stderr is a log file, as under launchd): it never prints the PIN.
    const pin = /PIN: (\d{4} \d{4})/.exec(homed('admin', 'firstrun'))?.[1] ?? ''
    expect(pin).toMatch(/^\d{4} \d{4}$/)
    const log = readFileSync(env('E2E_LOG'), 'utf8')
    expect(log).not.toContain(pin.replace(' ', ''))
    await page.getByRole('textbox', { name: 'Введите PIN' }).fill(pin) // pasted as printed, with the space
    // No cloudflared in this test: the page stops at the choice; the setup link comes from the CLI.
    await expect(page.getByRole('heading', { name: 'Как дом будет виден из интернета?' })).toBeVisible()
  })

  await test.step('`homed admin setup-link` → the onboarding → a real passkey makes Alice the owner', async () => {
    const link = homed('admin', 'setup-link').trim()
    expect(link.startsWith(`${origin}/setup#t=`)).toBe(true)
    await page.goto(link)
    await expect(page.getByRole('heading', { name: 'Добро пожаловать в Homie' })).toBeVisible()
    expect(page.url()).not.toContain('#t=')
    await page.getByRole('button', { name: 'Начать' }).click()
    await page.getByRole('textbox', { name: 'Ваше имя' }).fill('Алиса')
    await page.getByRole('button', { name: 'Дальше' }).click()
    await page.getByRole('textbox', { name: 'Название дома' }).fill('Дом')
    await page.getByRole('button', { name: 'Дальше' }).click()
    await page.getByRole('button', { name: 'Создать ключ' }).click()
    await expect(page.getByRole('heading', { name: 'Готово, Алиса!' })).toBeVisible()
    const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId })
    expect(credentials.map((c) => c.rpId)).toEqual(['localhost'])
    await page.getByRole('button', { name: 'Открыть дом' }).click()
    await expect(page.getByRole('heading', { name: 'Дом', level: 1 })).toBeVisible()
    // The first-run page closes once the home has an owner with a passkey.
    expect((await request.get(`${env('E2E_SETUP')}/api/firstrun/state`)).status()).toBe(410)
  })

  await test.step('Устройства → Fake: a tool runs from the admin and shows its live progress', async () => {
    const nav = page.getByRole('navigation', { name: 'Разделы' })
    await nav.getByRole('link', { name: 'Устройства' }).click()
    await page.getByRole('link', { name: /Fake/ }).click()
    // Assistant tools run from "what it can do" just for testing (DESIGN.md §9.2); untitled tools carry their name.
    await page.getByRole('region', { name: 'Что умеет в ChatGPT и Claude' }).getByRole('button', { name: /^fake_progress/ }).click()
    await page.getByRole('dialog', { name: 'fake_progress' }).getByRole('button', { name: 'Проверить без ассистента' }).click()
    await expect(page.getByText(/Нажмите OK|Almost done/)).toBeVisible()
    // fake_progress waits for fake_release, the way a TV pairing waits for OK on the remote.
    expect(homed('tool', 'call', 'fake_release').trim()).toBe('ok')
    await expect(page.getByRole('heading', { name: 'Готово' })).toBeVisible()
    await expect(page.getByText('released')).toBeVisible()
    await page.getByRole('button', { name: 'Готово' }).click()
    await expect(page.getByText('released')).toBeHidden()
  })

  await test.step('the call is in the activity log', async () => {
    await page.getByRole('navigation', { name: 'Разделы' }).getByRole('link', { name: 'Журнал' }).click()
    await page.getByRole('radio', { name: 'Всё' }).click()
    await expect(page.getByRole('link').filter({ hasText: 'Алиса · в админке' }).first()).toBeVisible()
  })

  await test.step('Люди: an invite for a guest who may use the fake device for a week', async () => {
    await page.getByRole('navigation', { name: 'Разделы' }).getByRole('link', { name: 'Люди' }).click()
    await page.getByRole('button', { name: 'Пригласить в дом' }).click()
    const sheet = page.getByRole('dialog', { name: 'Пригласить' })
    await sheet.getByRole('textbox', { name: 'Как зовут?' }).fill('Борис')
    await sheet.getByRole('radio', { name: 'Только выбранные' }).click()
    await sheet.getByRole('checkbox').first().click()
    await sheet.getByRole('radio', { name: 'Неделя' }).click()
    await sheet.getByRole('button', { name: 'Создать приглашение' }).click()
    // Signing up a moment ago counts as step-up; otherwise the passkey sheet asks.
    const stepUp = page.getByRole('dialog', { name: 'Подтвердите, что это вы' })
    const link = sheet.getByLabel('Ссылка-приглашение')
    await expect(link.or(stepUp).first()).toBeVisible()
    if (await stepUp.isVisible()) await stepUp.getByRole('button', { name: 'Подтвердить' }).click()
    await expect(link).toContainText(`${origin}/join#t=`)
  })

  await csp.expectClean()
})
