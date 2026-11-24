import { mkdirSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { expect, test, type Browser, type Page } from '@playwright/test'

// Screenshots of every screen and state for docs/screenshots (PLAN T2.4 acceptance): dark and light, phone
// (390 × 844, iPhone) and desktop (1440 × 900, Mac), against the mock API. Run: npm --prefix web run screenshots

const OUT = fileURLToPath(new URL('../../docs/screenshots/', import.meta.url))
const IPHONE_UA =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 26_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.1 Mobile/15E148 Safari/604.1'
const MAC_UA =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.1 Safari/605.1.15'

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

type Device = 'phone' | 'desktop'
type Theme = 'dark' | 'light'

const dialog = (page: Page, name: string | RegExp) => page.getByRole('dialog', { name })

const SHOTS: Shot[] = [
  // Sign in and the first run
  { name: '01-login', path: '/admin/login', scenario: 'signed-out', ready: 'Войти' },
  { name: '02-login-not-set-up', path: '/admin/login', scenario: 'not-set-up', ready: 'Дом ещё не настроен' },
  { name: '03-setup-home', path: '/admin/setup#t=demo', scenario: 'not-set-up', ready: 'Как назовём дом?' },
  {
    name: '04-setup-name',
    path: '/admin/setup#t=demo',
    scenario: 'not-set-up',
    ready: 'Как назовём дом?',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Дальше' }).click()
      await expect(p.getByRole('heading', { name: 'Как вас зовут?' })).toBeVisible()
    },
  },
  {
    name: '05-setup-passkey',
    path: '/admin/setup#t=demo',
    scenario: 'not-set-up',
    ready: 'Как назовём дом?',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Дальше' }).click()
      await p.getByRole('button', { name: 'Дальше' }).click()
      await expect(p.getByRole('button', { name: 'Создать ключ' })).toBeVisible()
    },
  },
  {
    name: '06-setup-done',
    path: '/admin/setup#t=demo',
    scenario: 'not-set-up',
    ready: 'Как назовём дом?',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Дальше' }).click()
      await p.getByRole('button', { name: 'Дальше' }).click()
      await p.getByRole('button', { name: 'Создать ключ' }).click()
      await expect(p.getByRole('heading', { name: 'Готово! Дом ваш' })).toBeVisible()
    },
  },
  { name: '07-setup-expired', path: '/admin/setup#t=expired', scenario: 'not-set-up', ready: 'Ссылка устарела' },
  { name: '08-lan-pin', path: '/firstrun', scenario: 'firstrun', ready: 'Введите PIN' },
  {
    name: '09-lan-code',
    path: '/firstrun',
    scenario: 'firstrun-norelay',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await p.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
      await expect(p.getByRole('heading', { name: 'Код сервера' })).toBeVisible()
    },
  },
  {
    name: '10-lan-connecting',
    path: '/firstrun',
    scenario: 'firstrun',
    extra: '&slow=1',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await p.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
      await expect(p.getByRole('heading', { name: /Подключаемся/ })).toBeVisible()
    },
  },
  {
    name: '11-lan-link',
    path: '/firstrun',
    scenario: 'firstrun',
    ready: 'Введите PIN',
    prepare: async (p) => {
      await p.getByRole('textbox', { name: 'Введите PIN' }).fill('12345678')
      await expect(p.getByRole('heading', { name: 'Откройте дом на телефоне или здесь' })).toBeVisible({ timeout: 15_000 })
    },
  },
  { name: '12-lan-done', path: '/firstrun', scenario: 'firstrun-done', ready: 'Дом уже настроен' },

  // Дом
  { name: '20-home', path: '/admin/', ready: 'ChatGPT может управлять домом' },
  { name: '21-home-attention', path: '/admin/', scenario: 'trouble', ready: 'Требует внимания' },
  { name: '22-home-empty', path: '/admin/', scenario: 'fresh', ready: 'Пока нет устройств' },
  { name: '23-home-unreachable', path: '/admin/', scenario: 'unreachable', ready: 'Не виден снаружи' },

  // Устройства
  { name: '30-devices', path: '/admin/devices', ready: 'Работает' },
  { name: '31-devices-empty', path: '/admin/devices', scenario: 'fresh', ready: 'Здесь будут ваши устройства' },
  { name: '32-add-choose', path: '/admin/devices/add', scenario: 'fresh', ready: 'LG webOS TVs: power, volume, inputs.' },
  {
    name: '33-add-preview',
    path: '/admin/devices/add',
    scenario: 'fresh',
    ready: 'LG webOS TVs: power, volume, inputs.',
    prepare: async (p) => {
      await dialog(p, 'Добавить устройства').getByRole('button', { name: /Телевизоры/ }).click()
      await expect(p.getByRole('button', { name: 'Установить' })).toBeEnabled()
    },
  },
  {
    name: '34-add-incompatible',
    path: '/admin/devices/add',
    scenario: 'fresh',
    ready: 'LG webOS TVs: power, volume, inputs.',
    prepare: async (p) => {
      await p.getByRole('textbox', { name: 'Адрес на GitHub' }).fill('someone/homie-kettle')
      await p.getByRole('button', { name: 'Найти' }).click()
      await expect(p.getByText('Нужна более новая версия Homie. Сначала обновите её в Настройках.')).toBeVisible()
    },
  },
  {
    name: '35-stepup',
    path: '/admin/devices/add',
    scenario: 'fresh',
    ready: 'LG webOS TVs: power, volume, inputs.',
    prepare: async (p) => {
      await dialog(p, 'Добавить устройства').getByRole('button', { name: /Телевизоры/ }).click()
      await p.getByRole('button', { name: 'Установить' }).click()
      await expect(dialog(p, 'Подтвердите, что это вы')).toBeVisible()
    },
  },
  {
    name: '36-add-installing',
    path: '/admin/devices/add',
    scenario: 'fresh',
    extra: '&slow=1',
    ready: 'LG webOS TVs: power, volume, inputs.',
    prepare: async (p) => {
      await dialog(p, 'Добавить устройства').getByRole('button', { name: /Телевизоры/ }).click()
      await p.getByRole('button', { name: 'Установить' }).click()
      await dialog(p, 'Подтвердите, что это вы').getByRole('button', { name: 'Подтвердить' }).click()
      await expect(p.getByText(/из 5,3 МБ/)).toBeVisible({ timeout: 15_000 })
    },
  },
  {
    name: '37-add-done',
    path: '/admin/devices/add',
    scenario: 'fresh',
    ready: 'LG webOS TVs: power, volume, inputs.',
    prepare: async (p) => {
      await dialog(p, 'Добавить устройства').getByRole('button', { name: /Телевизоры/ }).click()
      await p.getByRole('button', { name: 'Установить' }).click()
      await dialog(p, 'Подтвердите, что это вы').getByRole('button', { name: 'Подтвердить' }).click()
      await expect(p.getByText('Модуль «Телевизоры» установлен')).toBeVisible({ timeout: 15_000 })
    },
  },
  { name: '40-module', path: '/admin/devices/tv', ready: 'Телевизор в гостиной' },
  {
    name: '41-module-lower',
    path: '/admin/devices/tv',
    ready: 'Телевизор в гостиной',
    prepare: async (p) => {
      await p.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
    },
  },
  { name: '42-module-failing', path: '/admin/devices/tv', scenario: 'trouble', ready: 'Мы перезапускаем его сами' },
  { name: '43-module-empty', path: '/admin/devices/tv', scenario: 'tv-new', ready: 'Пока ни одного устройства' },
  { name: '44-device-sheet', path: '/admin/devices/tv/items/tv', ready: 'Подключить телевизор' },
  {
    name: '45-run-form',
    path: '/admin/devices/tv',
    ready: 'Добавить телевизор',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Добавить телевизор' }).click()
      await expect(dialog(p, 'Добавить телевизор').getByRole('textbox', { name: 'IP-адрес' })).toBeVisible()
    },
  },
  {
    name: '46-ability',
    path: '/admin/devices/tv',
    ready: 'Громкость',
    prepare: async (p) => {
      await p.getByRole('button', { name: /^Громкость/ }).click()
      await expect(p.getByRole('button', { name: 'Проверить без ChatGPT' })).toBeVisible()
    },
  },
  {
    name: '47-run-form-volume',
    path: '/admin/devices/tv',
    ready: 'Громкость',
    prepare: async (p) => {
      await p.getByRole('button', { name: /^Громкость/ }).click()
      await p.getByRole('button', { name: 'Проверить без ChatGPT' }).click()
      await expect(p.getByRole('slider', { name: 'Громкость, %' })).toBeVisible()
    },
  },
  {
    name: '48-run-progress',
    path: '/admin/devices/tv/items/tv',
    extra: '&slow=1',
    ready: 'Подключить телевизор',
    prepare: async (p) => {
      await dialog(p, 'Телевизор в гостиной').getByRole('button', { name: 'Подключить телевизор' }).click()
      await expect(p.getByText('Нажмите OK на пульте телевизора')).toBeVisible({ timeout: 15_000 })
      await p.waitForTimeout(2200)
    },
  },
  {
    name: '49-run-result',
    path: '/admin/devices/tv',
    ready: 'Найти телевизоры',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Найти телевизоры' }).click()
      await expect(dialog(p, 'Найти телевизоры').getByRole('heading', { name: 'Готово' })).toBeVisible({ timeout: 10_000 })
    },
  },
  {
    name: '50-run-failed',
    path: '/admin/devices/tv',
    ready: 'Добавить телевизор',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Добавить телевизор' }).click()
      const d = dialog(p, 'Добавить телевизор')
      await d.getByRole('textbox', { name: 'IP-адрес' }).fill('192.168.68.70')
      await d.getByRole('button', { name: 'Добавить телевизор' }).click()
      await expect(d.getByRole('heading', { name: 'Не получилось' })).toBeVisible({ timeout: 10_000 })
    },
  },
  {
    name: '51-confirm-destructive',
    path: '/admin/devices/tv/items/tv',
    ready: 'Удалить телевизор',
    prepare: async (p) => {
      await dialog(p, 'Телевизор в гостиной').getByRole('button', { name: 'Удалить телевизор' }).click()
      await expect(p.getByRole('alertdialog')).toBeVisible()
    },
  },
  {
    name: '52-running-pill',
    path: '/admin/devices/tv/items/tv',
    extra: '&slow=1',
    ready: 'Подключить телевизор',
    prepare: async (p) => {
      await dialog(p, 'Телевизор в гостиной').getByRole('button', { name: 'Подключить телевизор' }).click()
      await expect(p.getByText('Нажмите OK на пульте телевизора')).toBeVisible({ timeout: 15_000 })
      await p.keyboard.press('Escape')
      await p.waitForTimeout(800)
      await p.keyboard.press('Escape')
      await expect(p.getByRole('button', { name: /Открыть действие/ }).first()).toBeVisible()
    },
  },
  { name: '53-module-log', path: '/admin/devices/tv/logs', ready: 'serving MCP over stdio' },

  // Люди (placeholder until phase 3)
  { name: '55-people', path: '/admin/people', ready: 'Скоро здесь будет семья' },

  // Журнал
  { name: '60-activity', path: '/admin/activity', ready: 'Сегодня' },
  {
    name: '61-activity-all',
    path: '/admin/activity',
    ready: 'Сегодня',
    prepare: async (p) => {
      await p.getByRole('radio', { name: 'Всё' }).click()
      await expect(p.getByText('Вход').first()).toBeVisible()
    },
  },
  {
    name: '62-activity-filters',
    path: '/admin/activity',
    ready: 'Сегодня',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Фильтры' }).click()
      await expect(dialog(p, 'Фильтры')).toBeVisible()
    },
  },
  { name: '63-activity-entry', path: '/admin/activity/1095', ready: 'Аргументы' },
  { name: '64-activity-empty', path: '/admin/activity', scenario: 'fresh', ready: 'Здесь появятся действия' },

  // Настройки
  { name: '70-settings', path: '/admin/settings', ready: 'Ключи входа' },
  {
    name: '71-settings-profile',
    path: '/admin/settings',
    ready: 'Хозяин дома',
    prepare: async (p) => {
      await p.getByRole('button', { name: /Хозяин дома/ }).click()
      await expect(dialog(p, 'Ваше имя')).toBeVisible()
    },
  },
  { name: '72-settings-home-name', path: '/admin/settings/home', ready: 'Так дом называется в ChatGPT у всей семьи.' },
  { name: '73-settings-language', path: '/admin/settings/language', ready: 'English' },
  { name: '74-settings-appearance', path: '/admin/settings/appearance', ready: 'Как в системе' },
  { name: '75-settings-passkeys', path: '/admin/settings/passkeys', ready: 'MacBook Pro' },
  {
    name: '76-settings-passkey-sheet',
    path: '/admin/settings/passkeys',
    ready: 'MacBook Pro',
    prepare: async (p) => {
      await p.getByRole('button', { name: /iPhone/ }).click()
      await expect(dialog(p, 'iPhone')).toBeVisible()
    },
  },
  { name: '77-settings-sessions', path: '/admin/settings/sessions', ready: 'Safari · Mac' },
  { name: '78-settings-server', path: '/admin/settings/server', ready: 'Проверка снаружи' },
  { name: '79-settings-server-offline', path: '/admin/settings/server', scenario: 'trouble', ready: 'Нет связи с сервером' },
  {
    name: '80-settings-recode',
    path: '/admin/settings/server',
    ready: 'Проверка снаружи',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Обновить код сервера' }).click()
      await expect(dialog(p, 'Обновить код сервера')).toBeVisible()
    },
  },
  { name: '81-settings-keys', path: '/admin/settings/keys', ready: 'ChatGPT Site' },
  {
    name: '82-settings-key-created',
    path: '/admin/settings/keys',
    ready: 'ChatGPT Site',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Создать ключ' }).click()
      await dialog(p, 'Создать ключ').getByRole('button', { name: 'Создать ключ' }).click()
      await dialog(p, 'Подтвердите, что это вы').getByRole('button', { name: 'Подтвердить' }).click()
      await expect(dialog(p, 'Создать ключ').locator('code')).toContainText('ghk_')
    },
  },
  { name: '83-settings-about', path: '/admin/settings/about', ready: 'Проверить обновления' },
  { name: '84-settings-about-update', path: '/admin/settings/about', scenario: 'trouble', ready: 'Обновить до 0.3.0' },
  {
    name: '85-settings-update-job',
    path: '/admin/settings/about',
    scenario: 'trouble',
    extra: '&slow=1',
    ready: 'Обновить до 0.3.0',
    prepare: async (p) => {
      await p.getByRole('button', { name: 'Обновить до 0.3.0' }).click()
      await dialog(p, 'Подтвердите, что это вы').getByRole('button', { name: 'Подтвердить' }).click()
      await expect(dialog(p, 'Обновляем Homie').getByText(/из 5,3 МБ/)).toBeVisible({ timeout: 15_000 })
    },
  },

  // Global states
  { name: '90-offline-start', path: '/admin/', scenario: 'offline', ready: 'Дом не на связи' },
  {
    name: '91-offline-banner',
    path: '/admin/',
    ready: 'ChatGPT может управлять домом',
    prepare: async (p) => {
      // Visit a module (it stays cached), go home, lose the home, come back: cached content dims under the banner.
      await p.getByRole('link', { name: /Телевизоры/ }).first().click()
      await expect(p.getByText('Телевизор в гостиной')).toBeVisible()
      await p.getByRole('link', { name: 'Дом' }).first().click()
      await expect(p.getByText('ChatGPT может управлять домом')).toBeVisible()
      await p.waitForTimeout(800)
      await p.evaluate(() => (window as unknown as { __homieMock?: { setOffline: (v: boolean) => void } }).__homieMock?.setOffline(true))
      await p.getByRole('link', { name: /Телевизоры/ }).first().click()
      await expect(p.getByRole('status').filter({ hasText: 'Дом не на связи' })).toBeVisible()
    },
  },
  { name: '92-not-found', path: '/admin/nowhere', ready: 'Такой страницы нет' },
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
 * size, no visible loss on UI, and GitHub and macOS show it.
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
