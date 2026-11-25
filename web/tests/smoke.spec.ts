import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { guardCSP, mock, open, useRealWebAuthn } from './helpers'

// Smoke tests against the mock API: sign-in and the first run on the gatekeeper's screens, Homie's tabs, admin tools
// with progress, step-up, people with devices and a term, the connect page, quick mode (polling, the trial banner),
// the LAN first-run page with both tunnel ways and the quick-mode reconnect, a member's view, the offline banner and
// accessibility. Every test also fails on a CSP violation or an uncaught error.

const nav = (page: Page) => page.getByRole('navigation', { name: 'Разделы' })
const dialog = (page: Page, name: string | RegExp) => page.getByRole('dialog', { name })

test('sign in with a passkey lands an owner on Дом', async ({ page }) => {
  const csp = await guardCSP(page)
  const auth = await useRealWebAuthn(page)
  await auth.addPasskey()
  await open(page, '/home', 'signed-out')
  await expect(page).toHaveURL(/\/login\?next=%2Fhome/)
  await expect(page.getByRole('heading', { name: 'Homie' })).toBeVisible()
  await page.getByRole('button', { name: 'Войти' }).click()
  await expect(page).toHaveURL(/\/home(\?|$)/)
  await expect(page.getByRole('heading', { name: 'Дом', level: 1 })).toBeVisible()
  await expect(page.getByText('ChatGPT и Claude могут управлять домом')).toBeVisible()
  await csp.expectClean()
})

test('first run: the setup link names the home and makes the owner', async ({ page }) => {
  const csp = await guardCSP(page)
  const auth = await useRealWebAuthn(page)
  await open(page, '/setup#t=setup-token', 'not-set-up')
  await expect(page.getByRole('heading', { name: 'Добро пожаловать в Homie' })).toBeVisible()
  // The token leaves the address bar at once.
  expect(page.url()).not.toContain('#t=')
  await page.getByRole('button', { name: 'Начать' }).click()
  await page.getByRole('textbox', { name: 'Ваше имя' }).fill('Алиса')
  await page.getByRole('button', { name: 'Дальше' }).click()
  await expect(page.getByRole('heading', { name: 'Как назовём дом?' })).toBeVisible()
  // The imported name of the home is offered.
  await expect(page.getByRole('textbox', { name: 'Название дома' })).toHaveValue('Дом')
  await page.getByRole('textbox', { name: 'Название дома' }).fill('Дача')
  await expect(page.getByText('В ChatGPT: «Дача»')).toBeVisible()
  await page.getByRole('button', { name: 'Дальше' }).click()
  await page.getByRole('button', { name: 'Создать ключ' }).click()
  await expect(page.getByRole('heading', { name: 'Готово, Алиса!' })).toBeVisible()
  expect(await auth.credentials()).toHaveLength(1)
  await page.getByRole('button', { name: 'Открыть дом' }).click()
  await expect(page).toHaveURL(/\/home$/)
  await expect(page.getByRole('heading', { name: 'Дача', level: 1 })).toBeVisible()
  await csp.expectClean()
})

test('first run: a used link says where to get a new one', async ({ page }) => {
  await open(page, '/setup#t=expired', 'not-set-up')
  await expect(page.getByRole('heading', { name: 'Ссылка не работает' })).toBeVisible()
  await expect(page.getByText('homed admin setup-link')).toBeVisible()
})

test('Дом shows the status, the devices and the assistants; the tabs reach every section', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/')
  await expect(page).toHaveURL(/\/home$/)
  await expect(page.getByRole('region', { name: 'Устройства' })).toContainText('Телевизоры')
  await expect(page.getByRole('region', { name: 'Недавно' })).toContainText('Белла · через Claude')
  await page.getByRole('link', { name: /Подключить ChatGPT и Claude/ }).click()
  await expect(page).toHaveURL(/\/connect$/)
  // The connect page belongs to the Дом tab.
  await expect(nav(page).getByRole('link', { name: 'Дом' })).toHaveAttribute('aria-current', 'page')
  for (const [tab, heading] of [
    ['Устройства', 'Устройства'],
    ['Журнал', 'Журнал'],
    ['Люди', 'Дом'],
    ['Настройки', 'Настройки'],
  ] as const) {
    await nav(page).getByRole('link', { name: tab }).click()
    await expect(page.getByRole('heading', { name: heading, level: 1 })).toBeVisible()
  }
  await csp.expectClean()
})

test('the connect page: one address, copied, and the steps for ChatGPT and Claude', async ({ page, context }) => {
  const csp = await guardCSP(page)
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await open(page, '/connect')
  await expect(page.getByRole('heading', { name: 'Подключить', level: 1 })).toBeVisible()
  await expect(page.getByLabel('Адрес для подключения')).toHaveText('https://home.example.com/mcp')
  await page.getByRole('button', { name: 'Скопировать' }).first().click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('https://home.example.com/mcp')
  const steps = page.getByRole('list', { name: 'Как подключить' })
  await expect(steps).toContainText('плагины ChatGPT')
  await page.getByRole('radio', { name: 'Claude' }).click()
  await expect(steps).toContainText('Добавить свой коннектор')
  await expect(steps).toContainText('назовите его «Дом»')
  await csp.expectClean()
})

test('call an admin tool: pair a TV with progress', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/devices/tv')
  await page.getByRole('link', { name: /Телевизор в гостиной/ }).click()
  await expect(page).toHaveURL(/\/devices\/tv\/items\/tv$/)
  const sheet = dialog(page, 'Телевизор в гостиной')
  await sheet.getByRole('button', { name: 'Подключить телевизор' }).click()
  // The target is filled and hidden; the tool has no other field, so it runs at once and reports progress.
  const run = dialog(page, 'Подключить телевизор')
  await expect(run.getByText('Нажмите OK на пульте телевизора')).toBeVisible()
  await expect(run.getByText(/из 3:00/)).toBeVisible()
  await expect(run.getByRole('heading', { name: 'Готово' })).toBeVisible({ timeout: 15_000 })
  await expect(run.getByText('Paired with Телевизор в гостиной.')).toBeVisible()
  await run.getByRole('button', { name: 'Готово' }).click()
  await expect(run).toBeHidden()
  await csp.expectClean()
})

test('a running action follows the owner to the package’s pages: the pill reopens its sheet there', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/devices/tv/items/tv', 'default', '&slow=1')
  await dialog(page, 'Телевизор в гостиной').getByRole('button', { name: 'Подключить телевизор' }).click()
  const run = dialog(page, 'Подключить телевизор')
  await expect(run.getByText('Нажмите OK на пульте телевизора')).toBeVisible({ timeout: 15_000 })
  // Closing the sheets never cancels the call.
  await run.getByRole('button', { name: 'Закрыть' }).click()
  await dialog(page, 'Телевизор в гостиной').getByRole('button', { name: 'Закрыть' }).click()
  await nav(page).getByRole('link', { name: 'Люди' }).click()
  await expect(page).toHaveURL(/\/account$/)
  await page.getByRole('button', { name: 'Открыть действие «Подключить телевизор»' }).click()
  await expect(run.getByText('Нажмите OK на пульте телевизора')).toBeVisible()
  await csp.expectClean()
})

test('admin tool form: validation, a failed run, retry keeps the values', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/devices/tv')
  await page.getByRole('button', { name: 'Добавить телевизор' }).click()
  const run = dialog(page, 'Добавить телевизор')
  await run.getByRole('button', { name: 'Добавить телевизор' }).click()
  await expect(run.getByText('Заполните это поле')).toBeVisible()
  await run.getByRole('textbox', { name: 'IP-адрес' }).fill('192.0.2.70')
  await run.getByRole('button', { name: 'Добавить телевизор' }).click()
  await expect(run.getByRole('heading', { name: 'Не получилось' })).toBeVisible({ timeout: 10_000 })
  await expect(run.getByText(/No webOS TV answered at 192\.0\.2\.70/)).toBeVisible()
  await run.getByRole('button', { name: 'Повторить' }).click()
  await expect(run.getByRole('textbox', { name: 'IP-адрес' })).toHaveValue('192.0.2.70')
  await csp.expectClean()
})

test('installing a module asks for Face ID first, then shows the job', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/devices/add', 'fresh')
  const add = dialog(page, 'Добавить устройства')
  await add.getByRole('textbox', { name: 'Адрес на GitHub' }).fill('devXpro/homie-tv')
  await add.getByRole('button', { name: 'Найти' }).click()
  await add.getByRole('button', { name: 'Установить' }).click()
  const stepUp = dialog(page, 'Подтвердите, что это вы')
  await expect(stepUp).toContainText('установить «Телевизоры»')
  await stepUp.getByRole('button', { name: 'Подтвердить' }).click()
  await expect(add.getByText('Модуль «Телевизоры» установлен')).toBeVisible({ timeout: 15_000 })
  await add.getByRole('button', { name: 'Настроить' }).click()
  await expect(page).toHaveURL(/\/devices\/tv$/)
  await csp.expectClean()
})

test('people: invite a guest to the TV for a week', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/account')
  const people = page.getByRole('region', { name: 'Люди' })
  await expect(people).toContainText('Белла')
  await people.getByRole('button', { name: 'Пригласить в дом' }).click()
  const sheet = dialog(page, 'Пригласить')
  await sheet.getByRole('textbox', { name: 'Как зовут?' }).fill('Ева')
  await sheet.getByRole('radio', { name: 'Только выбранные' }).click()
  await sheet.getByRole('checkbox', { name: 'Телевизоры' }).click()
  await sheet.getByRole('radio', { name: 'Неделя' }).click()
  const sent = page.waitForRequest((r) => r.url().endsWith('/api/account/invites') && r.method() === 'POST')
  await sheet.getByRole('button', { name: 'Создать приглашение' }).click()
  const body = (await sent).postDataJSON() as { name: string; permissions: string[]; access_until?: string }
  expect(body.permissions).toEqual(['plugin:tv'])
  expect(body.access_until).toBeTruthy()
  await dialog(page, 'Подтвердите, что это вы').getByRole('button', { name: 'Подтвердить' }).click()
  await expect(sheet.getByLabel('Ссылка-приглашение')).toContainText('/join#t=')
  await csp.expectClean()
})

test('people: a guest whose term ended is greyed with «Продлить»; the superadmin can invite a co-owner', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/account')
  await expect(page.getByText(/доступ закончился/)).toBeVisible()
  // homed refuses to delete the home's one account: the package leaves «Удалить всё» out (accountDelete: false).
  await expect(page.locator('a[href="/account/delete"]')).toHaveCount(0)
  await page.getByRole('button', { name: 'Продлить доступ: Карина' }).click()
  await expect(page.getByRole('dialog')).toContainText('Неделя')
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: 'Пригласить в дом' }).click()
  const sheet = dialog(page, 'Пригласить')
  await sheet.getByRole('radio', { name: 'Совладелец' }).click()
  await expect(sheet.getByText('Совладельцу доступны все устройства')).toBeVisible()
  await expect(sheet.getByRole('heading', { name: 'Срок' })).toBeHidden()
  await csp.expectClean()
})

test('a co-owner keeps the tools, but installing, updating, removing, reconnecting and the hub update are the superadmin’s', async ({ page }) => {
  const csp = await guardCSP(page)
  const only = (title: string) => page.getByText(title, { exact: true }).locator('..').getByText('Это может только хозяин хаба')
  await open(page, '/home', 'coowner')
  await expect(page.getByRole('heading', { name: 'Дом', level: 1 })).toBeVisible()
  // Updates wait for the superadmin: Дом does not call a co-owner for them.
  await expect(page.getByText('Новая версия Homie')).toHaveCount(0)
  await expect(only('Добавить устройства')).toBeVisible()
  await nav(page).getByRole('link', { name: 'Устройства' }).click()
  await expect(page.getByRole('button', { name: 'Добавить устройства' })).toHaveCount(0)
  await expect(only('Добавить устройства')).toBeVisible()
  await page.getByRole('link', { name: /Телевизоры/ }).click()
  await expect(page.getByRole('button', { name: 'Найти телевизоры' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Обновить до 0.3.0' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Удалить модуль «Телевизоры»' })).toHaveCount(0)
  await expect(only('Обновить до 0.3.0')).toBeVisible()
  await expect(only('Удалить модуль «Телевизоры»')).toBeVisible()
  await page.goto('/settings/connection')
  await expect(page.getByRole('button', { name: 'Проверить сейчас' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Переподключить' })).toHaveCount(0)
  await expect(only('Переподключить')).toBeVisible()
  await page.goto('/settings/about')
  await expect(page.getByText('Новая версия Homie')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Обновить до 0.3.0' })).toHaveCount(0)
  await expect(only('Обновить до 0.3.0')).toBeVisible()
  await csp.expectClean()
})

test('the superadmin has them all, and removing a module says what else goes', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/devices/tv', 'trouble')
  await expect(page.getByText('Это может только хозяин хаба')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Обновить до 0.3.0' })).toBeVisible()
  await page.getByRole('button', { name: 'Удалить модуль «Телевизоры»' }).click()
  await expect(page.getByRole('alertdialog')).toContainText('У людей пропадёт доступ к нему, а приглашения, которые его дают, будут отозваны.')
  await page.getByRole('alertdialog').getByRole('button', { name: 'Отмена' }).click()
  await nav(page).getByRole('link', { name: 'Устройства' }).click()
  await expect(page.getByRole('button', { name: 'Добавить устройства' })).toBeVisible()
  await page.goto('/settings/connection')
  await expect(page.getByRole('button', { name: 'Переподключить' })).toBeVisible()
  await page.goto('/settings/about')
  await expect(page.getByRole('button', { name: 'Обновить до 0.3.0' })).toBeVisible()
  await csp.expectClean()
})

test('settings: renaming the home renames the account', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/settings/home')
  await page.getByRole('textbox', { name: 'Название дома' }).fill('Дача')
  await page.getByRole('button', { name: 'Сохранить' }).click()
  await expect(page).toHaveURL(/\/settings$/)
  await nav(page).getByRole('link', { name: 'Люди' }).click()
  await expect(page.getByRole('heading', { name: 'Дача', level: 1 })).toBeVisible()
  await csp.expectClean()
})

test('settings: the connection page checks from outside and reconnects after Face ID', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/settings/connection')
  await expect(page.getByText('home.example.com').first()).toBeVisible()
  await page.getByRole('button', { name: 'Проверить сейчас' }).click()
  await expect(page.getByText('Проходит', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Переподключить' }).click()
  await dialog(page, 'Подтвердите, что это вы').getByRole('button', { name: 'Подтвердить' }).click()
  await expect(page.getByText('Переподключаемся…').first()).toBeVisible()
  await csp.expectClean()
})

test('quick mode: the admin polls instead of SSE, says «для пробы» and builds the move to a domain', async ({ page }) => {
  const csp = await guardCSP(page)
  const sse: string[] = []
  const polls: string[] = []
  page.on('request', (r) => {
    if (r.url().includes('/api/home/events/poll')) polls.push(r.url())
    else if (r.url().includes('/api/home/events')) sse.push(r.url())
  })
  await open(page, '/home', 'quick')
  const banner = page.getByRole('note').filter({ hasText: 'Адрес для пробы' })
  await expect(banner).toBeVisible()
  await expect.poll(() => polls.length).toBeGreaterThan(0)
  expect(sse).toEqual([])
  await banner.getByRole('button', { name: 'Перейти на свой домен' }).click()
  const sheet = dialog(page, 'Свой домен')
  await sheet.getByRole('textbox', { name: 'Адрес дома' }).fill('home.example.com')
  await expect(sheet.getByLabel('Команда для домашнего компьютера')).toHaveText('homed cloudflare connect home.example.com')
  await sheet.getByRole('button', { name: 'Готово' }).click()
  // The tunnel's address changes behind the poll: the connect page follows.
  await mock(page, 'restartQuick')
  await page.getByRole('link', { name: /Подключить ChatGPT и Claude/ }).click()
  await expect(page.getByLabel('Адрес для подключения')).toHaveText('https://example-two.trycloudflare.com/mcp')
  await csp.expectClean()
})

test('people: in quick mode invites are off, and the list says why', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/account', 'quick')
  const people = page.getByRole('region', { name: 'Люди' })
  await expect(people).toContainText('С адресом для пробы приглашать нельзя — нужен свой домен.')
  await expect(people.getByRole('button', { name: 'Пригласить в дом' })).toHaveCount(0)
  await csp.expectClean()
})

test('settings: in quick mode «Переподключить» warns that the address changes, then sends the owner to the home computer', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/settings/connection', 'quick')
  await page.getByRole('button', { name: 'Переподключить' }).click()
  const confirm = page.getByRole('alertdialog')
  await expect(confirm).toContainText('Переподключить с новым адресом?')
  await confirm.getByRole('button', { name: 'Переподключить' }).click()
  await dialog(page, 'Подтвердите, что это вы').getByRole('button', { name: 'Подтвердить' }).click()
  await expect(page.getByText('Дальше — на домашнем компьютере')).toBeVisible()
  await expect(page.getByLabel('Адрес страницы на домашнем компьютере')).toHaveText('http://localhost:8090/firstrun')
  await csp.expectClean()
})

test('LAN page: PIN, my domain, the Cloudflare login, then the setup link', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/firstrun', 'firstrun', '&manual=1')
  const pin = page.getByRole('textbox', { name: 'Введите PIN' })
  await pin.fill('11112222')
  await expect(page.getByText('PIN не тот. Проверьте ещё раз.')).toBeVisible()
  await pin.fill('1234 5678')
  await expect(page.getByRole('heading', { name: 'Как дом будет виден из интернета?' })).toBeVisible()
  await page.getByRole('button', { name: /Мой домен/ }).click()
  await page.getByRole('textbox', { name: 'Адрес дома' }).fill('home')
  await page.getByRole('button', { name: 'Подключить' }).click()
  await expect(page.getByText('Нужен адрес вида home.example.com')).toBeVisible()
  await page.getByRole('textbox', { name: 'Адрес дома' }).fill('home.example.com')
  await page.getByRole('button', { name: 'Подключить' }).click()
  await expect(page.getByRole('heading', { name: 'Войдите в Cloudflare' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Открыть Cloudflare' })).toHaveAttribute('href', /^https:\/\/dash\.example\.com\//)
  await expect(page.getByRole('img', { name: 'QR-код ссылки для входа в Cloudflare' })).toBeVisible()
  await mock(page, 'finishLogin')
  await expect(page.getByRole('heading', { name: 'Откройте дом на телефоне или здесь' })).toBeVisible({ timeout: 15_000 })
  await expect(page.getByRole('img', { name: 'QR-код ссылки для телефона' })).toBeVisible()
  await expect(page.getByText(/Ссылка одноразовая и действует ещё/)).toBeVisible()
  await csp.expectClean()
})

test('LAN page: a taken address stops at DNS; the record is replaced only after a yes', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/firstrun', 'firstrun', '&manual=1')
  await page.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
  await page.getByRole('button', { name: /Мой домен/ }).click()
  await page.getByRole('textbox', { name: 'Адрес дома' }).fill('taken.example.com')
  await page.getByRole('button', { name: 'Подключить' }).click()
  await expect(page.getByRole('heading', { name: 'Войдите в Cloudflare' })).toBeVisible()
  await mock(page, 'finishLogin')
  await expect(page.getByRole('heading', { name: 'Не получилось подключить taken.example.com' })).toBeVisible({ timeout: 10_000 })
  await expect(page.getByRole('alert')).toHaveText('Для taken.example.com в Cloudflare уже есть запись. Её можно заменить на дом или выбрать другой адрес.')
  await expect(page.getByRole('button', { name: 'Другой адрес' })).toBeVisible()
  // «Заменить запись» asks first, then connects again with overwriteDns.
  const overwrite = page.waitForRequest((r) => r.url().endsWith('/api/firstrun/tunnel') && r.method() === 'POST')
  await page.getByRole('button', { name: 'Заменить запись' }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Заменить запись' }).click()
  expect((await overwrite).postDataJSON()).toEqual({ mode: 'named', hostname: 'taken.example.com', overwriteDns: true })
  await expect(page.getByRole('heading', { name: 'Войдите в Cloudflare' })).toBeVisible()
  await mock(page, 'finishLogin')
  await expect(page.getByRole('heading', { name: 'Откройте дом на телефоне или здесь' })).toBeVisible({ timeout: 15_000 })
  await csp.expectClean()
})

test('LAN page: try without a domain, with the warning, then the link', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/firstrun', 'firstrun')
  await page.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
  await page.getByRole('button', { name: /Попробовать без домена/ }).click()
  await expect(page.getByRole('note')).toContainText('Приглашать людей в этом режиме нельзя. Открытые приглашения будут отозваны.')
  await page.getByRole('button', { name: 'Попробовать' }).click()
  await expect(page.getByRole('heading', { name: 'Откройте дом на телефоне или здесь' })).toBeVisible({ timeout: 15_000 })
  await csp.expectClean()
})

test('LAN page: once the PIN has run out, the next step goes back to the PIN', async ({ page }) => {
  await open(page, '/firstrun', 'firstrun')
  await page.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
  await page.getByRole('button', { name: /Попробовать без домена/ }).click()
  await mock(page, 'forgetPin')
  await page.getByRole('button', { name: 'Попробовать' }).click()
  await expect(page.getByRole('heading', { name: 'Введите PIN' })).toBeVisible()
})

test('LAN page: when the page leaves the LAN for the home computer, it says where to go', async ({ page }) => {
  await open(page, '/firstrun', 'firstrun', '&slow=1')
  await page.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
  await page.getByRole('button', { name: /Попробовать без домена/ }).click()
  await page.getByRole('button', { name: 'Попробовать' }).click()
  await expect(page.getByRole('heading', { name: 'Получаем адрес у Cloudflare…' })).toBeVisible({ timeout: 15_000 })
  // homed moved the listener to 127.0.0.1: this page's next poll finds nobody.
  await mock(page, 'offline')
  await expect(page.getByText('Дальше — на домашнем компьютере')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByLabel('Адрес страницы на домашнем компьютере')).toHaveText('http://localhost:8090/firstrun')
})

test('LAN page: after a quick restart it offers «Переподключить» with the new address', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/firstrun', 'firstrun-reconnect')
  await page.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
  await expect(page.getByRole('heading', { name: 'Адрес сменился' })).toBeVisible()
  await expect(page.getByLabel('Новый адрес для ChatGPT и Claude')).toHaveText('https://example-two.trycloudflare.com/mcp')
  await page.getByRole('button', { name: 'Переподключить' }).click()
  await expect(page.getByRole('img', { name: 'QR-код ссылки для телефона' })).toBeVisible()
  await expect(page.getByText('Удалите старое подключение дома.')).toBeVisible()
  await csp.expectClean()
})

test('LAN page: after a move to an own domain it offers «Переподключить» with the new address', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/firstrun', 'firstrun-domain')
  await page.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
  await expect(page.getByRole('heading', { name: 'Адрес сменился' })).toBeVisible()
  await expect(page.getByText('Теперь дом на своём адресе home.example.com.')).toBeVisible()
  await expect(page.getByLabel('Новый адрес для ChatGPT и Claude')).toHaveText('https://home.example.com/mcp')
  await page.getByRole('button', { name: 'Переподключить' }).click()
  await expect(page.getByRole('img', { name: 'QR-код ссылки для телефона' })).toBeVisible()
  await csp.expectClean()
})

test('LAN page: closed after too many PINs says how to open it again', async ({ page }) => {
  await open(page, '/firstrun', 'firstrun-closed')
  await expect(page.getByText('Страница настройки закрыта')).toBeVisible()
  await expect(page.getByText(/homed admin firstrun/)).toBeVisible()
})

test('a member lands on the connect page and sees only their devices', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/', 'member')
  await expect(page).toHaveURL(/\/connect$/)
  await expect(page.getByText('https://home.example.com/mcp')).toBeVisible()
  // Дом is the owner's tab: a member has the connect page in its place.
  await expect(nav(page).getByRole('link', { name: 'Подключить' })).toHaveAttribute('aria-current', 'page')
  await expect(nav(page).getByRole('link', { name: 'Дом' })).toHaveCount(0)
  await nav(page).getByRole('link', { name: 'Устройства' }).click()
  await expect(page.getByText('Телевизоры')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Добавить устройства' })).toBeHidden()
  await page.goto('/devices/tv')
  await expect(page.getByRole('heading', { name: 'Это раздел владельца' })).toBeVisible()
  await csp.expectClean()
})

test('the home goes offline: a banner, cached content dims, and it comes back', async ({ page }) => {
  const csp = await guardCSP(page)
  await open(page, '/home')
  await expect(page.getByText('ChatGPT и Claude могут управлять домом')).toBeVisible()
  await mock(page, 'offline')
  await nav(page).getByRole('link', { name: 'Устройства' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Дом не на связи' })).toBeVisible()
  await mock(page, 'online')
  await page.getByRole('status').getByRole('button', { name: 'Повторить' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Дом не на связи' })).toBeHidden()
  await csp.expectClean()
})

test('accessibility: no serious axe violations on the main screens', async ({ page }) => {
  for (const [path, scenario, ready] of [
    ['/home', 'default', 'ChatGPT и Claude могут управлять домом'],
    ['/devices/tv', 'default', 'Телевизор в гостиной'],
    ['/activity', 'default', 'Сегодня'],
    ['/connect', 'default', 'https://home.example.com/mcp'],
    ['/account', 'default', 'Белла'],
    ['/settings', 'default', 'Адрес дома'],
    ['/settings/connection', 'quick', 'Адрес для пробы'],
    ['/firstrun', 'firstrun', 'Введите PIN'],
  ] as const) {
    await open(page, path, scenario)
    await expect(page.getByText(ready).first()).toBeVisible()
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()
    const serious = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
    expect(serious.map((v) => `${path}: ${v.id} ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([])
  }
})
