import { mkdirSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { expect, test, type Browser, type Page } from '@playwright/test'
import { mock } from './helpers'

// Screenshots of every screen and state, dark and light, phone (390 × 844, iPhone) and desktop (1440 × 900, Mac),
// against the mock API. Run: SCREENSHOTS_DIR=<dir> npm --prefix web run screenshots (default: test-results/screenshots).

const OUT = process.env.SCREENSHOTS_DIR ? `${process.env.SCREENSHOTS_DIR.replace(/\/+$/, '')}/` : fileURLToPath(new URL('../test-results/screenshots/', import.meta.url))
const ONLY = process.env.SHOTS ? new RegExp(process.env.SHOTS) : undefined
const IPHONE_UA =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 26_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.1 Mobile/15E148 Safari/604.1'
const MAC_UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.1 Safari/605.1.15'

type Device = 'phone' | 'desktop'
type Theme = 'dark' | 'light'

interface Shot {
  name: string
  path: string
  scenario?: string
  extra?: string
  /** a text that must be on screen before the picture */
  ready?: string
  prepare?: (page: Page, device: Device) => Promise<void>
  /** only for one form factor */
  only?: Device
}

const dialog = (page: Page, name: string | RegExp) => page.getByRole('dialog', { name })
const confirmStepUp = (page: Page) => dialog(page, 'Подтвердите, что это вы').getByRole('button', { name: 'Подтвердить' }).click()
const pin = async (page: Page) => page.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')

const SHOTS: Shot[] = [
  // Sign in, the first run, joining, consent (the gatekeeper's screens in Homie's words)
  { name: '01-login', path: '/login', scenario: 'signed-out', ready: 'Войти' },
  { name: '02-setup-welcome', path: '/setup#t=setup-token', scenario: 'not-set-up', ready: 'Добро пожаловать в Homie' },
  {
    name: '03-setup-home',
    path: '/setup#t=setup-token',
    scenario: 'not-set-up',
    ready: 'Добро пожаловать в Homie',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Начать' }).click()
      await p.getByRole('textbox', { name: 'Ваше имя' }).fill('Алиса')
      await p.getByRole('button', { name: 'Дальше' }).click()
      await expect(p.getByRole('heading', { name: 'Как назовём дом?' })).toBeVisible()
    },
  },
  {
    name: '04-setup-done',
    path: '/setup#t=setup-token',
    scenario: 'not-set-up',
    ready: 'Добро пожаловать в Homie',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Начать' }).click()
      await p.getByRole('textbox', { name: 'Ваше имя' }).fill('Алиса')
      await p.getByRole('button', { name: 'Дальше' }).click()
      await p.getByRole('button', { name: 'Дальше' }).click()
      await p.getByRole('button', { name: 'Создать ключ' }).click()
      await expect(p.getByRole('heading', { name: 'Готово, Алиса!' })).toBeVisible()
    },
  },
  { name: '05-join-welcome', path: '/join#t=invite-token', scenario: 'join', ready: 'Вас зовут в дом «Дом»' },
  { name: '06-consent', path: '/consent?txn=txn-chatgpt', scenario: 'consent', ready: 'ChatGPT просит доступ' },

  // The LAN first-run page
  { name: '10-lan-pin', path: '/firstrun', scenario: 'firstrun', ready: 'Введите PIN' },
  {
    name: '11-lan-tunnel',
    path: '/firstrun',
    scenario: 'firstrun',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await expect(p.getByRole('heading', { name: 'Как дом будет виден из интернета?' })).toBeVisible()
    },
  },
  {
    name: '12-lan-domain',
    path: '/firstrun',
    scenario: 'firstrun',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await p.getByRole('button', { name: /Мой домен/ }).click()
      await p.getByRole('textbox', { name: 'Адрес дома' }).fill('home.example.com')
    },
  },
  {
    name: '13-lan-login',
    path: '/firstrun',
    scenario: 'firstrun',
    extra: '&manual=1',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await p.getByRole('button', { name: /Мой домен/ }).click()
      await p.getByRole('textbox', { name: 'Адрес дома' }).fill('home.example.com')
      await p.getByRole('button', { name: 'Подключить' }).click()
      await expect(p.getByRole('img', { name: 'QR-код ссылки для входа в Cloudflare' })).toBeVisible()
    },
  },
  {
    name: '14-lan-connecting',
    path: '/firstrun',
    scenario: 'firstrun',
    extra: '&manual=1&slow=1',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await p.getByRole('button', { name: /Мой домен/ }).click()
      await p.getByRole('textbox', { name: 'Адрес дома' }).fill('home.example.com')
      await p.getByRole('button', { name: 'Подключить' }).click()
      await expect(p.getByRole('heading', { name: 'Войдите в Cloudflare' })).toBeVisible({ timeout: 15_000 })
      await mock(p, 'finishLogin')
      await expect(p.getByRole('heading', { name: 'Подключаем home.example.com' })).toBeVisible({ timeout: 15_000 })
    },
  },
  {
    name: '15-lan-failed',
    path: '/firstrun',
    scenario: 'firstrun',
    extra: '&manual=1',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await p.getByRole('button', { name: /Мой домен/ }).click()
      await p.getByRole('textbox', { name: 'Адрес дома' }).fill('taken.example.com')
      await p.getByRole('button', { name: 'Подключить' }).click()
      await expect(p.getByRole('heading', { name: 'Войдите в Cloudflare' })).toBeVisible()
      await mock(p, 'finishLogin')
      await expect(p.getByRole('heading', { name: 'Не получилось подключить taken.example.com' })).toBeVisible({ timeout: 10_000 })
    },
  },
  {
    name: '16-lan-quick',
    path: '/firstrun',
    scenario: 'firstrun',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await p.getByRole('button', { name: /Попробовать без домена/ }).click()
      await expect(p.getByRole('heading', { name: 'Адрес для пробы' })).toBeVisible()
    },
  },
  {
    name: '17-lan-link',
    path: '/firstrun',
    scenario: 'firstrun',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await p.getByRole('button', { name: /Попробовать без домена/ }).click()
      await p.getByRole('button', { name: 'Попробовать' }).click()
      await expect(p.getByRole('img', { name: 'QR-код ссылки для телефона' })).toBeVisible({ timeout: 15_000 })
    },
  },
  {
    name: '18-lan-reconnect',
    path: '/firstrun',
    scenario: 'firstrun-reconnect',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await expect(p.getByRole('heading', { name: 'Адрес сменился' })).toBeVisible()
    },
  },
  {
    name: '19-lan-reconnect-link',
    path: '/firstrun',
    scenario: 'firstrun-reconnect',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await pin(p)
      await p.getByRole('button', { name: 'Переподключить' }).click()
      await expect(p.getByRole('img', { name: 'QR-код ссылки для телефона' })).toBeVisible()
    },
  },
  { name: '19b-lan-closed', path: '/firstrun', scenario: 'firstrun-closed', ready: 'Страница настройки закрыта' },

  // Дом
  { name: '20-home', path: '/home', ready: 'ChatGPT и Claude могут управлять домом' },
  { name: '21-home-quick', path: '/home', scenario: 'quick', ready: 'Адрес для пробы' },
  { name: '22-home-trouble', path: '/home', scenario: 'trouble', ready: 'Требует внимания' },
  { name: '23-home-empty', path: '/home', scenario: 'fresh', ready: 'Пока нет устройств' },
  {
    name: '24-domain-sheet',
    path: '/home',
    scenario: 'quick',
    ready: 'Адрес для пробы',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Перейти на свой домен' }).click()
      await dialog(p, 'Свой домен').getByRole('textbox', { name: 'Адрес дома' }).fill('home.example.com')
    },
  },

  // Подключить (the package's connect page with Homie's parts)
  { name: '30-connect', path: '/connect', ready: 'https://home.example.com/mcp' },
  {
    name: '31-connect-claude',
    path: '/connect',
    ready: 'https://home.example.com/mcp',
    prepare: async (p) => {
      await p.getByRole('radio', { name: 'Claude' }).click()
      await p.getByRole('list', { name: 'Как подключить' }).scrollIntoViewIfNeeded()
    },
  },
  { name: '32-connect-quick', path: '/connect', scenario: 'quick', ready: 'https://example.trycloudflare.com/mcp' },
  { name: '33-connect-member', path: '/connect', scenario: 'member', ready: 'https://home.example.com/mcp' },

  // Устройства
  { name: '40-devices', path: '/devices', ready: 'Телевизоры' },
  { name: '41-devices-add', path: '/devices/add', ready: 'Рекомендуем' },
  { name: '42-module', path: '/devices/tv', ready: 'Телевизор в гостиной' },
  { name: '43-module-device', path: '/devices/tv/items/tv', ready: 'Подключить телевизор' },
  {
    name: '44-run-progress',
    path: '/devices/tv/items/tv',
    extra: '&slow=1',
    ready: 'Подключить телевизор',
    prepare: async (p) => {
      await dialog(p, 'Телевизор в гостиной').getByRole('button', { name: 'Подключить телевизор' }).click()
      await expect(dialog(p, 'Подключить телевизор').getByText('Нажмите OK на пульте телевизора')).toBeVisible({ timeout: 15_000 })
    },
  },
  { name: '45-module-failing', path: '/devices/tv', scenario: 'trouble', ready: 'не отвечает' },
  { name: '46-module-logs', path: '/devices/tv/logs', ready: 'serving MCP over stdio' },
  { name: '47-devices-member', path: '/devices', scenario: 'member', ready: 'Телевизоры' },

  // Журнал
  { name: '50-activity', path: '/activity', ready: 'Сегодня' },
  { name: '51-activity-entry', path: '/activity/1099', ready: 'Аргументы' },
  {
    name: '52-activity-filters',
    path: '/activity',
    ready: 'Сегодня',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Фильтры' }).click()
      await expect(dialog(p, 'Фильтры')).toBeVisible()
    },
  },

  // Люди (the package's account page and sheets, with devices and terms)
  { name: '60-people', path: '/account', ready: 'Белла' },
  { name: '60b-people-quick', path: '/account', scenario: 'quick', ready: 'нужен свой домен' },
  {
    name: '61-invite',
    path: '/account',
    ready: 'Белла',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Пригласить в дом' }).click()
      const s = dialog(p, 'Пригласить')
      await s.getByRole('textbox', { name: 'Как зовут?' }).fill('Ева')
      await s.getByRole('radio', { name: 'Только выбранные' }).click()
      await s.getByRole('checkbox', { name: 'Телевизоры' }).click()
      await s.getByRole('radio', { name: 'Неделя' }).click()
    },
  },
  {
    name: '62-invite-coowner',
    path: '/account',
    ready: 'Белла',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Пригласить в дом' }).click()
      const s = dialog(p, 'Пригласить')
      await s.getByRole('textbox', { name: 'Как зовут?' }).fill('Ева')
      await s.getByRole('radio', { name: 'Совладелец' }).click()
    },
  },
  {
    name: '63-invite-link',
    path: '/account',
    ready: 'Белла',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Пригласить в дом' }).click()
      const s = dialog(p, 'Пригласить')
      await s.getByRole('textbox', { name: 'Как зовут?' }).fill('Ева')
      await s.getByRole('button', { name: 'Создать приглашение' }).click()
      await confirmStepUp(p)
      await expect(s.getByLabel('Ссылка-приглашение')).toBeVisible()
    },
  },
  {
    name: '64-member-detail',
    path: '/account',
    ready: 'Белла',
    prepare: async (p) => {
      await p.getByRole('button', { name: /^Борис/ }).click()
      await expect(p.getByRole('dialog')).toBeVisible()
    },
  },
  {
    name: '65-member-extend',
    path: '/account',
    ready: 'Белла',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Продлить доступ: Карина' }).click()
      await expect(p.getByRole('dialog')).toBeVisible()
    },
  },
  { name: '66-audit', path: '/account/audit', ready: 'Изменения и входы' },

  // Настройки
  { name: '70-settings', path: '/settings', ready: 'Адрес дома' },
  { name: '71-settings-home', path: '/settings/home', ready: 'Название дома' },
  { name: '72-settings-language', path: '/settings/language', ready: 'English' },
  { name: '73-connection', path: '/settings/connection', ready: 'Проверка снаружи' },
  { name: '74-connection-quick', path: '/settings/connection', scenario: 'quick', ready: 'Адрес для пробы' },
  { name: '75-connection-trouble', path: '/settings/connection', scenario: 'trouble', ready: 'Нет связи' },
  {
    name: '76-connection-reconnect-quick',
    path: '/settings/connection',
    scenario: 'quick',
    ready: 'Адрес для пробы',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Переподключить' }).click()
      await expect(p.getByRole('alertdialog')).toBeVisible()
    },
  },
  {
    name: '76b-connection-quick-moved',
    path: '/settings/connection',
    scenario: 'quick',
    ready: 'Адрес для пробы',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Переподключить' }).click()
      await p.getByRole('alertdialog').getByRole('button', { name: 'Переподключить' }).click()
      await confirmStepUp(p)
      await expect(p.getByText('Дальше — на домашнем компьютере')).toBeVisible()
    },
  },
  { name: '77-about-update', path: '/settings/about', scenario: 'trouble', ready: 'Обновить до 0.3.0' },
  { name: '78-settings-member', path: '/settings', scenario: 'member', ready: 'Язык' },

  // Global states
  { name: '90-offline-start', path: '/home', scenario: 'offline', ready: 'Дом не на связи' },
  {
    name: '91-offline-banner',
    path: '/home',
    ready: 'ChatGPT и Claude могут управлять домом',
    prepare: async (p) => {
      // Visit a module (it stays cached), go home, lose the home, come back: cached content dims under the banner.
      await p.getByRole('link', { name: /Телевизоры/ }).first().click()
      await expect(p.getByText('Телевизор в гостиной')).toBeVisible()
      await p.goBack()
      await expect(p.getByText('ChatGPT и Claude могут управлять домом')).toBeVisible()
      await p.waitForTimeout(800)
      await mock(p, 'offline')
      await p.getByRole('link', { name: /Телевизоры/ }).first().click()
      await expect(p.getByRole('status').filter({ hasText: 'Дом не на связи' })).toBeVisible()
    },
  },
  { name: '92-not-found', path: '/nowhere', ready: 'Такой страницы нет' },
  { name: '93-owner-only', path: '/devices/tv', scenario: 'member', ready: 'Это раздел владельца' },
]

async function newPage(browser: Browser, device: Device, theme: Theme) {
  const ctx = await browser.newContext({
    viewport: device === 'phone' ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    isMobile: device === 'phone',
    hasTouch: device === 'phone',
    userAgent: device === 'phone' ? IPHONE_UA : MAC_UA,
    locale: 'ru-RU',
    timezoneId: 'Europe/Warsaw',
    colorScheme: theme,
    reducedMotion: 'reduce',
  })
  const page = await ctx.newPage()
  await page.addInitScript((t) => localStorage.setItem('homie.theme', t), theme)
  return page
}

/**
 * toWebP re-encodes a PNG screenshot as WebP (quality 0.92) in a blank page of the same browser: a third of the
 * size, no visible loss on UI.
 */
async function toWebP(page: Page, png: Buffer) {
  const blank = await page.context().newPage()
  const b64 = await blank.evaluate(async (src) => {
    const bytes = Uint8Array.from(atob(src), (c) => c.charCodeAt(0))
    const bitmap = await createImageBitmap(new Blob([bytes], { type: 'image/png' }))
    const canvas = new OffscreenCanvas(bitmap.width, bitmap.height)
    canvas.getContext('2d')?.drawImage(bitmap, 0, 0)
    const out = new Uint8Array(await (await canvas.convertToBlob({ type: 'image/webp', quality: 0.92 })).arrayBuffer())
    let bin = ''
    for (let i = 0; i < out.length; i += 0x8000) bin += String.fromCharCode(...out.subarray(i, i + 0x8000))
    return btoa(bin)
  }, png.toString('base64'))
  await blank.close()
  return Buffer.from(b64, 'base64')
}

mkdirSync(OUT, { recursive: true })

for (const device of ['phone', 'desktop'] as const) {
  for (const theme of ['dark', 'light'] as const) {
    test.describe(`${device} ${theme}`, () => {
      for (const shot of SHOTS) {
        if (shot.only && shot.only !== device) continue
        if (ONLY && !ONLY.test(shot.name)) continue
        test(shot.name, async ({ browser }) => {
          const page = await newPage(browser, device, theme)
          const [base, hash] = shot.path.split('#')
          const sep = base.includes('?') ? '&' : '?'
          await page.goto(`${base}${sep}mock=${shot.scenario ?? 'default'}${shot.extra ?? ''}${hash ? `#${hash}` : ''}`)
          if (shot.ready) await expect(page.getByText(shot.ready).first()).toBeVisible({ timeout: 15_000 })
          if (shot.prepare) await shot.prepare(page, device)
          await page.waitForTimeout(700)
          const png = await page.screenshot({ animations: 'disabled', caret: 'hide' })
          writeFileSync(`${OUT}${shot.name}-${device}-${theme}.webp`, await toWebP(page, png))
          await page.context().close()
        })
      }
    })
  }
}
