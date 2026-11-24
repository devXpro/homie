import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { guardCSP, open, useRealWebAuthn } from './helpers'

// Smoke tests against the mock API (PLAN T2.4 §5): sign-in, the first-run wizard, admin tools with progress,
// step-up, the LAN page, the offline banner, and accessibility. Every test also fails on a CSP violation.

test('sign in with a passkey', async ({ page }) => {
  const csp = await guardCSP(page)
  const auth = await useRealWebAuthn(page)
  await auth.addPasskey()
  await open(page, '/admin/login', 'signed-out', '&lang=ru')
  await expect(page.getByRole('heading', { name: 'Мой шалаш' })).toBeVisible()
  await expect(page.getByText('Пароля нет — только ваш Face ID или Touch ID.')).toBeVisible()
  await page.getByRole('button', { name: 'Войти' }).click()
  await expect(page).toHaveURL(/\/admin\/?$/)
  await expect(page.getByRole('heading', { name: 'Мой шалаш', level: 1 })).toBeVisible()
  await expect(page.getByText('ChatGPT может управлять домом')).toBeVisible()
  await csp.expectClean()
})

test('first run: setup link, names, passkey', async ({ page }) => {
  const csp = await guardCSP(page)
  const auth = await useRealWebAuthn(page)
  await open(page, '/admin/setup#t=one-time-token', 'not-set-up')
  await expect(page.getByRole('heading', { name: 'Как назовём дом?' })).toBeVisible()
  // The token leaves the address bar at once (ADMIN-API §3.2).
  expect(page.url()).not.toContain('#t=')
  const home = page.getByRole('textbox', { name: 'Название дома' })
  await home.fill('Дача')
  await expect(page.getByText('В ChatGPT: «Дача»')).toBeVisible()
  await page.getByRole('button', { name: 'Дальше' }).click()
  await expect(page.getByRole('heading', { name: 'Как вас зовут?' })).toBeVisible()
  await page.getByRole('textbox', { name: 'Ваше имя' }).fill('Роман')
  await page.getByRole('button', { name: 'Дальше' }).click()
  await page.getByRole('button', { name: 'Создать ключ' }).click()
  await expect(page.getByRole('heading', { name: 'Готово! Дом ваш' })).toBeVisible()
  expect(await auth.credentials()).toHaveLength(1)
  await page.getByRole('button', { name: 'Открыть дом' }).click()
  await expect(page.getByRole('heading', { name: 'Дача', level: 1 })).toBeVisible()
  await csp.expectClean()
})

test('first run: an expired link says what to do', async ({ page }) => {
  await open(page, '/admin/setup#t=expired', 'not-set-up')
  await expect(page.getByRole('heading', { name: 'Ссылка устарела' })).toBeVisible()
  await expect(page.getByText('homed admin setup-link')).toBeVisible()
})

test('call an admin tool: pair a TV with progress', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/admin/devices/tv')
  await page.getByRole('link', { name: /Телевизор в гостиной/ }).click()
  await expect(page).toHaveURL(/\/admin\/devices\/tv\/items\/tv$/)
  const sheet = page.getByRole('dialog', { name: 'Телевизор в гостиной' })
  await sheet.getByRole('button', { name: 'Подключить телевизор' }).click()
  // The target is filled and hidden; the tool has no other field, so it runs at once and reports progress.
  const run = page.getByRole('dialog', { name: 'Подключить телевизор' })
  await expect(run.getByText('Нажмите OK на пульте телевизора')).toBeVisible()
  await expect(run.getByText(/из 3:00/)).toBeVisible()
  await expect(run.getByRole('heading', { name: 'Готово' })).toBeVisible({ timeout: 15_000 })
  await expect(run.getByText('Paired with Телевизор в гостиной.')).toBeVisible()
  await run.getByRole('button', { name: 'Готово' }).click()
  await expect(run).toBeHidden()
  await csp.expectClean()
})

test('admin tool form: validation, a failed run, retry keeps the values', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/admin/devices/tv')
  await page.getByRole('button', { name: 'Добавить телевизор' }).click()
  const run = page.getByRole('dialog', { name: 'Добавить телевизор' })
  await run.getByRole('button', { name: 'Добавить телевизор' }).click()
  await expect(run.getByText('Заполните это поле')).toBeVisible()
  await run.getByRole('textbox', { name: 'IP-адрес' }).fill('192.168.68.70')
  await run.getByRole('button', { name: 'Добавить телевизор' }).click()
  await expect(run.getByRole('heading', { name: 'Не получилось' })).toBeVisible({ timeout: 10_000 })
  await expect(run.getByText(/No LG webOS TV answered at 192\.168\.68\.70/)).toBeVisible()
  await run.getByRole('button', { name: 'Повторить' }).click()
  await expect(run.getByRole('textbox', { name: 'IP-адрес' })).toHaveValue('192.168.68.70')
  await csp.expectClean()
})

test('find TVs, then add the one found from the result', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/admin/devices/tv', 'tv-new')
  // An empty module offers its setup action as the one filled button.
  await page.getByRole('button', { name: 'Найти телевизоры' }).first().click()
  const run = page.getByRole('dialog', { name: 'Найти телевизоры' })
  await expect(run.getByText('Ищу телевизоры в домашней сети')).toBeVisible()
  await expect(run.getByRole('heading', { name: 'Готово' })).toBeVisible({ timeout: 10_000 })
  await run.getByRole('button', { name: 'Добавить телевизор' }).click()
  const add = page.getByRole('dialog', { name: 'Добавить телевизор' })
  await expect(add.getByRole('textbox', { name: 'IP-адрес' })).toHaveValue('192.168.68.66')
  await add.getByRole('button', { name: 'Добавить телевизор' }).click()
  await expect(add.getByRole('heading', { name: 'Готово' })).toBeVisible({ timeout: 10_000 })
  // The new TV is in the inventory, so its item actions are offered next.
  await expect(add.getByRole('button', { name: 'Подключить телевизор' })).toBeVisible()
  await csp.expectClean()
})

test('step-up before creating an access key', async ({ page }) => {
  const csp = await guardCSP(page)
  const auth = await useRealWebAuthn(page)
  await auth.addPasskey()
  await open(page, '/admin/settings/keys')
  await page.getByRole('button', { name: 'Создать ключ' }).click()
  const sheet = page.getByRole('dialog', { name: 'Создать ключ' })
  await sheet.getByRole('button', { name: 'Создать ключ' }).click()
  const stepUp = page.getByRole('dialog', { name: 'Подтвердите, что это вы' })
  await expect(stepUp.getByText('Чтобы создать ключ доступа, нужен Face ID или Touch ID.')).toBeVisible()
  await stepUp.getByRole('button', { name: 'Подтвердить' }).click()
  await expect(sheet.locator('code')).toContainText('ghk_')
  await expect(sheet.getByText('Сохраните сейчас — второй раз ключ не показать.')).toBeVisible()
  await csp.expectClean()
})

test('LAN first run: PIN, connecting, one-time link', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/firstrun', 'firstrun')
  await expect(page.getByRole('heading', { name: 'Введите PIN' })).toBeVisible()
  await page.getByRole('textbox', { name: 'Введите PIN' }).fill('11111111')
  await expect(page.getByText('PIN не тот. Проверьте ещё раз.')).toBeVisible()
  await page.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
  await expect(page.getByRole('heading', { name: /Подключаемся к homie\.jobbyjobs\.work/ })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Откройте дом на телефоне или здесь' })).toBeVisible({ timeout: 15_000 })
  await expect(page.getByRole('img', { name: 'QR-код ссылки для телефона' })).toBeVisible()
  await expect(page.locator('code')).toContainText('https://homie.jobbyjobs.work/admin/setup#t=')
  await csp.expectClean()
})

test('LAN first run: five wrong PINs make this device wait, with a countdown', async ({ page }) => {
  await open(page, '/firstrun', 'firstrun')
  const pin = page.getByRole('textbox', { name: 'Введите PIN' })
  for (let i = 0; i < 5; i++) {
    await expect(pin).toBeEnabled()
    await pin.fill(`0000000${i}`)
    await expect(page.getByRole('status').filter({ hasText: /PIN|попыток/ })).toBeVisible()
  }
  await expect(page.getByText(/с этого устройства\. Попробуйте снова через \d+:\d\d/)).toBeVisible()
  await expect(pin).toBeDisabled()
})

test('LAN first run: a closed page says how to open it again', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/firstrun', 'firstrun-closed')
  await expect(page.getByText('Страница настройки закрыта')).toBeVisible()
  await expect(page.getByText(/homed admin firstrun/)).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Введите PIN' })).toHaveCount(0)
  await csp.expectClean()
})

test('offline home: the banner, cached content, retry', async ({ page }) => {
  await open(page, '/admin', 'offline')
  await expect(page.getByRole('heading', { name: 'Дом не на связи' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Повторить' })).toBeVisible()
})

test('a destructive tool asks first', async ({ page }) => {
  await open(page, '/admin/devices/tv/items/tv')
  await page.getByRole('dialog', { name: 'Телевизор в гостиной' }).getByRole('button', { name: 'Удалить телевизор' }).click()
  const confirm = page.getByRole('alertdialog')
  await expect(confirm).toContainText('Точно выполнить «Удалить телевизор»? Это нельзя отменить.')
  await confirm.getByRole('button', { name: 'Отмена' }).click()
  await expect(confirm).toBeHidden()
})

test('no layout shift while navigating (CLS = 0)', async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __shift: number; __shifts: string[] }
    w.__shift = 0
    w.__shifts = []
    new PerformanceObserver((list) => {
      for (const e of list.getEntries() as (PerformanceEntry & { value: number; hadRecentInput: boolean; sources?: { node?: Node }[] })[]) {
        if (e.hadRecentInput) continue
        w.__shift += e.value
        w.__shifts.push(`${location.pathname} ${e.value.toFixed(4)} ${e.sources?.map((s) => (s.node as Element | undefined)?.className ?? '?').join(',')}`)
      }
    }).observe({ type: 'layout-shift', buffered: true })
  })
  await open(page, '/admin/')
  await expect(page.getByText('ChatGPT может управлять домом')).toBeVisible()
  const nav = page.getByRole('navigation', { name: 'Разделы' })
  for (const tab of ['Устройства', 'Журнал', 'Настройки', 'Люди', 'Дом']) {
    await nav.getByRole('link', { name: tab }).click()
    await page.waitForTimeout(600)
  }
  await nav.getByRole('link', { name: 'Устройства' }).click()
  await page.getByRole('link', { name: /Телевизоры/ }).click()
  await expect(page.getByText('Телевизор в гостиной')).toBeVisible()
  await page.waitForTimeout(600)
  await page.getByRole('button', { name: 'Назад: Устройства' }).click()
  await page.waitForTimeout(600)
  const { shift, shifts } = await page.evaluate(() => {
    const w = window as unknown as { __shift: number; __shifts: string[] }
    return { shift: w.__shift, shifts: w.__shifts }
  })
  expect(shifts).toEqual([])
  expect(shift).toBe(0)
})

for (const theme of ['dark', 'light'] as const) {
  test(`accessibility: no serious axe violations (${theme})`, async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('homie.theme', t), theme)
    const screens: [string, string, string][] = [
      ['/admin/login', 'signed-out', 'Войти'],
      ['/admin', 'default', 'ChatGPT может управлять домом'],
      ['/admin/devices', 'default', 'Телевизоры'],
      ['/admin/devices/tv', 'default', 'Что умеет в ChatGPT'],
      ['/admin/activity', 'default', 'Сегодня'],
      ['/admin/settings', 'default', 'Ключи входа'],
      ['/admin/settings/server', 'default', 'Проверка снаружи'],
      ['/admin', 'trouble', 'Требует внимания'],
    ]
    for (const [path, scenario, text] of screens) {
      await open(page, path, scenario)
      await expect(page.getByText(text).first()).toBeVisible()
      await page.waitForTimeout(400)
      const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      const serious = result.violations
        .filter((v) => v.impact === 'serious' || v.impact === 'critical')
        .map((v) => `${path} [${scenario}] ${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)
      expect(serious).toEqual([])
    }
  })
}
