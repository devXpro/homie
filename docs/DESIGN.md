# Admin design brief

What the Homie admin looks like, how it moves and what it says. T2.4 (admin UI) and T3.4 (People and Join UI) build
every screen from this file without asking; the data comes from `docs/ADMIN-API.md` (called "API" below, with its
section numbers).

The bar, in Roman's words: «ахуенный UX, чтоб было всё понятно и не перегружено, как в айфоне», «тёмные ахуенные темы
обязательно». Read every rule below as a way to get there.

## 1. Principles
1. **It feels like iOS Settings, not like a dashboard.** Grouped lists on a dark canvas, large titles, sheets. No
   device control panels (PLAN §1 non-goal): the admin sets the home up; ChatGPT runs it.
2. **One primary action per screen.** At most one filled button is visible at a time. Everything else is a list row,
   a tinted or a plain button.
3. **Human words.** No "plugin", "relay", "MCP", "token", "JSON", "step-up", "SSE" in the UI. The glossary (§10) maps
   every technical state to words; technical detail lives behind "Подробнее".
4. **Few sections.** Four tabs in phase 2, five in phase 3. Nothing is more than two taps from its tab.
5. **Calm by default.** Status is green and quiet when all is well. Colour and motion appear only when something needs
   the person.
6. **Every screen has all its states:** loading, empty, error, offline home. They are specified per screen in §8.
7. **Dark first.** Dark is the default, designed first and checked first. Light and "follow the system" are equals in
   quality.
8. **Russian first.** RU is the default language, EN is complete. Copy is short; sentences end without a full stop in
   titles, with one in body text.

## 2. Information architecture

### 2.1 Tabs
| # | RU | EN | Icon (lucide) | Route | Phase |
|---|---|---|---|---|---|
| 1 | Дом | Home | `House` | `/admin` | 2 |
| 2 | Устройства | Devices | `Lamp` | `/admin/devices` | 2 |
| 3 | Люди | People | `Users` | `/admin/people` | 3 (shown when `features.people`) |
| 4 | Журнал | Activity | `History` | `/admin/activity` | 2 |
| 5 | Настройки | Settings | `Settings` | `/admin/settings` | 2 |

### 2.2 Routes
| Route | Screen | Section |
|---|---|---|
| `/admin/login` | Sign in | §8.1 |
| `/admin/setup#t=<token>` | First-run wizard (owner) | §8.2 |
| `/firstrun` (setup listener only, `http://<lan ip>:8090`) | LAN setup page | §8.3 |
| `/admin` | Дом | §8.4 |
| `/admin/devices` | Устройства | §8.5 |
| `/admin/devices/add` | Add devices (sheet over the list) | §8.6 |
| `/admin/devices/:pluginId` | Module detail | §8.7 |
| `/admin/devices/:pluginId/items/:itemId` | Device sheet | §8.7.3 |
| `/admin/devices/:pluginId/logs` | Module log | §8.7.6 |
| `/admin/activity` | Журнал | §8.8 |
| `/admin/activity/:id` | Entry sheet | §8.8 |
| `/admin/settings` | Настройки | §8.9 |
| `/admin/settings/home` · `/language` · `/appearance` · `/passkeys` · `/sessions` · `/server` · `/keys` · `/about` | Settings sub-screens | §8.9 |
| `/admin/people` · `/admin/people/:id` · `/admin/people/invite` | People [P3] | §8.10 |
| `/join#t=<token>` | Join wizard [P3] | §8.11 |

Sheets have their own route so the back gesture and the browser back button close them, and a reload reopens them.

### 2.3 Navigation model
- Tabs are siblings; switching keeps each tab's own stack and scroll position.
- Inside a tab: push (list → detail). The nav bar shows `‹ <previous title>` on the left.
- Focused tasks (add devices, run an action, invite, step-up) are sheets.
- Destructive confirmations are action sheets on phones, small alert dialogs on desktop.

## 3. Tokens

All tokens are CSS custom properties defined once in `web/src/styles/tokens.css` and exposed to Tailwind v4 through
`@theme inline`. Theme switching sets `data-theme="dark|light"` on `<html>`; "system" follows
`prefers-color-scheme`. The `<meta name="theme-color">` follows the `--bg` of the active theme.

### 3.1 Colour

| Token | Dark | Light | Use |
|---|---|---|---|
| `--bg` | `#000000` | `#F2F2F7` | app background (grouped) |
| `--surface-1` | `#1C1C1E` | `#FFFFFF` | grouped list cards, hero card |
| `--surface-2` | `#2C2C2E` | `#FFFFFF` | sheets, dialogs, popovers |
| `--surface-3` | `#3A3A3C` | `#E5E5EA` | pressed rows, input fill inside sheets |
| `--fill` | `rgba(118,118,128,0.24)` | `rgba(118,118,128,0.12)` | segmented control track, text field fill, chips |
| `--label` | `#FFFFFF` | `#000000` | primary text |
| `--label-2` | `rgba(235,235,245,0.60)` | `rgba(60,60,67,0.75)` | secondary text, footnotes, subtitles |
| `--label-3` | `rgba(235,235,245,0.30)` | `rgba(60,60,67,0.30)` | placeholders and disabled only (never information) |
| `--separator` | `rgba(84,84,88,0.60)` | `rgba(60,60,67,0.29)` | hairlines (`0.5px` on 2× screens, else `1px`) |
| `--accent` | `#0A84FF` | `#0066CC` | links, tinted buttons, selected tab, icons, focus ring |
| `--accent-fill` | `#0071E3` | `#0066CC` | filled button background (white label) |
| `--accent-tint` | `rgba(10,132,255,0.18)` | `rgba(0,102,204,0.12)` | tinted button background, selected row |
| `--success` | `#30D158` | `#34C759` | dots, orb, switch track, icon tiles |
| `--success-text` | `#30D158` | `#1E7B34` | success words |
| `--warning` | `#FF9F0A` | `#FF9500` | dots, orb, icon tiles |
| `--warning-text` | `#FF9F0A` | `#C93400` | warning words |
| `--danger` | `#FF453A` | `#FF3B30` | dots, destructive icon tiles |
| `--danger-text` | `#FF453A` | `#D70015` | destructive buttons and error words |
| `--gray` | `#8E8E93` | `#8E8E93` | neutral dots and tiles |
| `--material` | `rgba(22,22,24,0.72)` | `rgba(249,249,249,0.80)` | tab bar, nav bar, sidebar (with blur) |
| `--scrim` | `rgba(0,0,0,0.48)` | `rgba(0,0,0,0.24)` | behind sheets |

Contrast is checked: every text token passes WCAG AA (4.5:1) on `--bg`, `--surface-1` and `--surface-2` of its theme,
and white on `--accent-fill` passes in both. That is why `--accent` and the `*-text` colours are darker in light mode
than iOS's system colours. Vivid colours (`--success`, `--warning`, `--danger`) are for shapes, never for small text.

**Icon tile palette** (white glyph on a coloured squircle, like iOS Settings): blue `#0A84FF`, indigo `#5E5CE6`,
purple `#BF5AF2`, pink `#FF375F`, orange `#FF9F0A`, teal `#40C8E0`, green `#30D158`, gray `#8E8E93`; the same values
in both themes.

**Hero glow**: the status card of the home screen paints
`radial-gradient(120% 90% at 50% 0%, color-mix(in srgb, var(--status) 22%, transparent), transparent 70%)` over
`--surface-1`, where `--status` is the colour of the relay state (§10.1). In light mode the mix is 14 %.

shadcn/ui variables map onto these: `--background: var(--bg)`, `--foreground: var(--label)`,
`--card: var(--surface-1)`, `--popover: var(--surface-2)`, `--primary: var(--accent-fill)`,
`--primary-foreground: #FFFFFF`, `--secondary: var(--fill)`, `--muted: var(--fill)`,
`--muted-foreground: var(--label-2)`, `--accent: var(--accent-tint)`, `--accent-foreground: var(--accent)`,
`--destructive: var(--danger-text)`, `--border: var(--separator)`, `--input: var(--fill)`, `--ring: var(--accent)`.

### 3.2 Typography
Font stack (no web fonts are loaded):
```css
--font-sans: -apple-system, BlinkMacSystemFont, "SF Pro Text", "SF Pro", system-ui, "Segoe UI", Roboto,
             "Noto Sans", "Helvetica Neue", Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji";
--font-mono: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, monospace;
```
Scale (size / line height in px, weight, letter spacing). Phone values are iOS's; at ≥1024 px the rows marked change.

| Token | Phone | ≥1024 px | Use |
|---|---|---|---|
| `large-title` | 34/41, 700, 0.4px | 30/36, 700 | screen title |
| `title-1` | 28/34, 700, 0.36px | | wizard step title |
| `title-2` | 22/28, 700, 0.35px | | sheet title, hero status |
| `title-3` | 20/25, 600, 0.38px | | empty-state title, result title |
| `headline` | 17/22, 600, -0.43px | 15/20, 600 | row title emphasis, buttons |
| `body` | 17/22, 400, -0.43px | 15/20, 400 | row title, body text, inputs |
| `callout` | 16/21, 400, -0.31px | | wizard text |
| `subheadline` | 15/20, 400, -0.23px | 13/18, 400 | row subtitle, value text |
| `footnote` | 13/18, 400, -0.08px | 12/16, 400 | section header and footer, hints |
| `caption-1` | 12/16, 400, 0 | | timestamps, badges |
| `caption-2` | 11/13, 500, 0.06px | 10/12, 500 | tab bar labels |

- Section headers are `footnote`, `--label-2`, sentence case (iOS 17 style), not upper case.
- Times, durations, counters and codes use `font-variant-numeric: tabular-nums`.
- Inputs are at least 16 px on phones (iOS zooms into smaller inputs).
- Russian text is set with `hyphens: auto` in body paragraphs only, and `lang` on `<html>` follows the language.

### 3.3 Spacing and sizes
Tailwind's default 4 px scale. Named layout constants:

| Constant | Value |
|---|---|
| page gutter | 16 px (< 768), 20 px (768–1023), 32 px (≥ 1024) |
| content max width | 720 px for lists and forms, centered in the content area |
| section gap | 32 px between grouped sections; header 8 px above a card, footer 8 px below |
| row | min height 44 px (one line) or 60 px (with subtitle); padding 11 px × 16 px; separator inset 16 px, or 60 px when the row has an icon tile |
| icon tile | 30 px, radius 8, glyph 18 px stroke 2 |
| module tile | 44 px (list, radius 12), 64 px (detail header, radius 16), glyph 26 / 36 px |
| touch target | ≥ 44 × 44 px everywhere |
| nav bar | 44 px + `env(safe-area-inset-top)` |
| tab bar | 49 px + `env(safe-area-inset-bottom)` |
| sidebar | 260 px |
| sheet (phone) | full width, max height 92 dvh, grabber 36 × 5 px at 6 px from the top |
| dialog (≥ 768) | 480 px wide (forms), 360 px (alerts); max height 85 dvh |

### 3.4 Radii
| Token | px | Use |
|---|---|---|
| `--radius-xs` | 6 | badges, kbd, small chips |
| `--radius-sm` | 8 | icon tiles, segmented items |
| `--radius-md` | 12 | buttons, inputs, module tile in lists |
| `--radius-lg` | 16 | grouped list cards, result cards |
| `--radius-xl` | 20 | hero card, sheets (top corners), dialogs |
| `--radius-pill` | 9999 | capsule buttons, status pills, toasts |

shadcn's `--radius` is set to `12px`.

### 3.5 Elevation and materials
- Dark mode has no shadows: depth comes from `--surface-1` → `--surface-2` and the scrim.
- Light mode: cards have none; sheets and dialogs get `0 10px 40px rgba(0,0,0,0.12), 0 1px 3px rgba(0,0,0,0.06)`;
  toasts `0 8px 24px rgba(0,0,0,0.12)`.
- Bars (nav, tab, sidebar) use `--material` with `backdrop-filter: saturate(180%) blur(20px)`, and a hairline
  `--separator` border that appears only once content scrolls under the bar (nav) or always (tab bar).

### 3.6 Iconography
- lucide icons, stroke 2 (1.75 at ≥ 24 px), `currentColor`. Every icon-only button has an `aria-label`.
- Module icon by plugin id: `tv` → `Tv` on indigo; `light*` → `Lightbulb` on orange; `kettle` → `CupSoda` on teal;
  `vacuum` → `Fan` on green; `speaker` → `Speaker` on pink; anything else → `Blocks` on a tile colour picked by
  hashing the id (FNV-1a mod 8) into the tile palette.
- Platform passkey glyph: iPhone and iPad → `ScanFace` ("Face ID"); Mac → `Fingerprint` ("Touch ID"); others →
  `KeyRound` ("ключ входа"). Detect with `navigator.userAgentData?.platform` falling back to the User-Agent.

## 4. Motion

Library: `motion` (`motion/react`). Wrap the app in `<MotionConfig reducedMotion="user">`.

| Name | Spec | Use |
|---|---|---|
| `spring.snappy` | `{type:"spring", visualDuration:0.22, bounce:0.12}` | press feedback, switches, segmented thumb, chips |
| `spring.smooth` | `{type:"spring", visualDuration:0.32, bounce:0}` | push navigation, list insert/remove (`layout`) |
| `spring.sheet` | `{type:"spring", visualDuration:0.38, bounce:0.1}` | sheets and dialogs in and out |
| `spring.success` | `{type:"spring", visualDuration:0.5, bounce:0.35}` | the success checkmark only |
| `fade.fast` | `{duration:0.15, ease:"easeOut"}` | tab switch, scrim, banners, toasts |

Rules:
- **Push:** the incoming screen goes `x: 24 → 0`, `opacity: 0 → 1` (`spring.smooth`); the outgoing one fades to 0 in
  `fade.fast`. Pop is the mirror. No full-width slides: they look heavy in a browser.
- **Tabs:** cross-fade only (`fade.fast`), no movement.
- **Sheets (phone):** `y: 100% → 0` with `spring.sheet`, scrim fades. Drag down to dismiss when the offset passes
  30 % of the height or the velocity passes 500 px/s; otherwise spring back. Use shadcn's `Drawer` (vaul).
- **Dialogs (≥ 768):** `scale: 0.96 → 1`, `opacity: 0 → 1`, `spring.sheet`.
- **Press:** rows tint to `--surface-3` immediately on pointer down; buttons `scale: 0.97` (`whileTap`,
  `spring.snappy`).
- **Lists:** items entering or leaving animate height and opacity (`AnimatePresence` + `layout`, `spring.smooth`); a
  new audit entry arriving at the top flashes its row background with `--accent-tint` for 1.2 s.
- **Large title:** collapses into the inline nav title while scrolling, driven by scroll position, not time.
- **Status orb:** "connecting" breathes (`scale 1 → 1.08`, `opacity .7 → 1`, 2.4 s, ease-in-out, infinite). Every
  other state is still. Nothing else loops, apart from spinners.
- **Success:** a 56 px circle in `--success` scales in with `spring.success`, then the check path draws in 0.3 s.
- **Numbers and text** never animate (no counters, no typewriters).
- **No layout shift:** skeletons have the exact size of the content they stand for, bars have fixed heights, and route
  transitions animate transforms and opacity only (Lighthouse CLS = 0 on navigation).
- **Reduced motion** (`prefers-reduced-motion: reduce`): every transform becomes a 120 ms opacity fade; no bounce, no
  breathing, no flash, no drag spring-back animation (snap); spinners keep turning (they carry meaning).

## 5. Layout

### 5.1 Breakpoints
| Width | Shell |
|---|---|
| < 768 px (phone) | bottom tab bar; sheets are bottom drawers; large titles 34 px |
| 768–1023 px (tablet, small windows) | bottom tab bar; sheets become centered dialogs |
| ≥ 1024 px (desktop) | left sidebar; dialogs; content column max 720 px centered in the remaining width |

Mobile-first CSS: base styles are the phone; `md:` = 768, `lg:` = 1024.

### 5.2 Phone shell
- Nav bar (material, fixed): back button left, inline title centered (appears when the large title scrolls away),
  one trailing button at most (e.g. `+`).
- Large title in the content, under the nav bar, 16 px gutter.
- Tab bar (material, fixed): 4–5 items, icon 24 px over a `caption-2` label. Selected = `--accent` icon and label;
  others `--gray`. A red 8 px dot on Дом when `attention` is not empty; on Устройства when a module is failing.
- Running-action pill: when an admin action runs and its sheet is closed, a capsule floats 8 px above the tab bar
  (spinner, action title, elapsed time). Tap reopens the sheet. Two or more: "Выполняется: 2".
- Offline banner (§8.12) slides down under the nav bar.
- Safe areas: `viewport-fit=cover`; padding with `env(safe-area-inset-*)`.

### 5.3 Desktop shell
- Sidebar (material, full height): the home name in `headline` with the app icon at the top; the tab items as rows
  (icon + label, 36 px high, radius 8, selected = `--accent-tint` background and `--accent` text); at the bottom the
  person's name with "Выйти" in a menu, and the running-action pill.
- Content: large title (30 px) and the same grouped lists, max 720 px. Sheets open as dialogs; the device sheet and the
  activity entry open as dialogs too.
- Keyboard: `⌘K`/`Ctrl K` is not needed; `Esc` closes sheets; `Enter` submits forms; tab order follows the visual
  order.

## 6. Components

Build from shadcn/ui primitives where one exists (named in brackets); everything is restyled with the tokens above.

| Component | Spec |
|---|---|
| `AppShell` | §5; renders `TabBar` or `Sidebar` by breakpoint, the offline banner and the running-action pill |
| `NavBar` | §5.2; props: `title`, `back?`, `trailing?` |
| `LargeTitle` | `large-title`; collapses into `NavBar` on scroll (IntersectionObserver on a sentinel) |
| `Section` | optional `header` (footnote, `--label-2`, 16 px inset), a `--surface-1` card with `--radius-lg`, optional `footer` (footnote, `--label-2`) |
| `Row` | leading icon tile or avatar, title (`body`), subtitle (`subheadline`, `--label-2`, max 2 lines), trailing: value text (`--label-2`), `StatusPill`, `Switch`, badge or chevron (`ChevronRight` 16 px `--label-3`). Whole row is the hit target; `button` or `a` semantics |
| `StatusDot` | 8 px circle in a status colour; `aria-hidden`, always next to words |
| `StatusPill` | capsule, `caption-1` 600, 4 × 8 px padding, `--fill` background, dot + word |
| `Button` [Button] | heights 50 (`lg`, capsule, full width in sheets and wizards), 44 (`md`, radius 12), 32 (`sm`, capsule). Variants: `filled` (`--accent-fill`, white), `tinted` (`--accent-tint`, `--accent`), `plain` (text `--accent`), `destructive` (`--danger-text` text on `--fill`; filled red only inside a confirmation). Loading: label stays, a spinner replaces the leading icon, the button is disabled |
| `IconButton` | 44 × 44 hit area, 22 px glyph, `--accent` |
| `Sheet` [Drawer / Dialog] | responsive: drawer < 768, dialog ≥ 768. Header: title (`title-2`, or `headline` centered for small sheets), a close button (`X` in a 30 px `--fill` circle) top-right. Content scrolls; the primary button sticks to the bottom with the safe area |
| `ActionSheet` [AlertDialog] | destructive confirmation: title, one line of text, red action, "Отмена". Phone: bottom; desktop: alert |
| `SegmentedControl` [Tabs] | `--fill` track, radius 9; thumb `--surface-1` (light) or `#636366` (dark) with `spring.snappy` |
| `Switch` [Switch] | 51 × 31, on = `--success`, thumb white with a subtle shadow |
| `TextField` [Input] | 44 px, `--fill` background, radius 12, no border; focus = 2 px `--accent` ring; label above in `footnote` `--label-2`; hint under in `footnote`; error under in `footnote` `--danger-text` with `aria-describedby` |
| `Select` [Select] | phones: a sheet with a checkmarked list; desktop: shadcn select |
| `Stepper` | `−` and `+` 44 px buttons around a tabular number |
| `Slider` [Slider] | track 4 px `--fill`, filled `--accent`, thumb 28 px white; the value shows right of the label |
| `Spinner` | iOS-style 8 spokes, 20 px (`md`) or 28 px (`lg`), `--label-2` |
| `ProgressBar` [Progress] | 4 px, radius pill, `--fill` track, `--accent` value; animates width with `spring.smooth` |
| `StepList` | vertical list of job steps: pending (`Circle` `--label-3`), running (`Spinner`), done (`CheckCircle2` `--success`), failed (`XCircle` `--danger`) |
| `EmptyState` | 72 px `--fill` circle with a 32 px icon, `title-3`, one or two lines `subheadline` `--label-2`, at most one button |
| `ErrorState` | as `EmptyState` with `TriangleAlert` in `--warning`, the error copy (§10.6) and "Повторить" |
| `Skeleton` [Skeleton] | `--fill` blocks the size of the content; shimmer 1.6 s (none with reduced motion) |
| `Toast` [Sonner] | top-center capsule on phones, bottom-right on desktop; icon + one line; 3 s; never for errors that need action |
| `CopyField` | monospace value in a `--fill` box + "Скопировать" (`Copy` → `Check` for 1.5 s) |
| `SecretOnce` | `CopyField` plus the warning footnote "Сохраните сейчас — второй раз ключ не показать" and a "Готово" button that asks "Точно сохранили?" if nothing was copied |
| `AppIcon` | rounded square (radius 22 %), gradient `#0A84FF → #5E5CE6` at 135°, white `House` glyph |
| `StatusOrb` | 56 px circle in the status colour with an inner highlight and an outer glow `0 0 32px` of the status colour at 45 %; motion §4 |
| `Avatar` [P3] | circle 40 / 64 px, initials (`headline`), background = tile palette by person id hash |
| `QRCode` [P3] | `qrcode` package (bundled), rendered to SVG, quiet zone 4, `--label` on `--surface-1` |
| `PasskeyButton` | `Button lg filled` with the platform glyph (§3.6) and the label from the screen's copy |
| `SchemaForm` | §9 |
| `ResultView` | §9.6 |
| `LogView` | monospace 12/16, `--surface-1`, line numbers off, wraps, virtualized (> 300 lines), "Следить" switch auto-scrolls to the end |

## 7. Data behaviour (all screens)
- TanStack Query for every GET; `staleTime` 10 s; refetch on window focus; the SSE stream (API §4) updates the cache
  by replacing objects (`plugin` → the plugin queries, `relay` → relay and overview, `audit` → prepend, `call`/`job` →
  their stores) and invalidates on `tools` and `resync`.
- Mutations are optimistic only for switches (module on/off, person scopes); everything else waits for the answer
  with the button in its loading state.
- Every non-GET sends `X-Homie: 1` and JSON.
- `401 unauthenticated` anywhere → go to `/admin/login?next=<path>` and show the toast "Сеанс истёк — войдите снова".
- `403 step_up_required` → the step-up sheet (§8.12.3), then retry once.
- Error codes map to copy through §10.6; unknown codes use `error.generic`.
- Loading: show skeletons only after 150 ms (avoid flicker); keep previous data while refetching.

## 8. Screens

Copy keys are i18next keys in `web/src/i18n/{ru,en}.json`. Plurals use i18next's `_one/_few/_many/_other` (RU) and
`_one/_other` (EN); in the tables below RU plurals are written `устройство|устройства|устройств`.

### 8.1 Sign in — `/admin/login`
Layout: a centered column, max 360 px, vertically centered on phones (slightly above center). `AppIcon` 72 px; the
home name from `GET /bootstrap` in `title-1`; subtitle; `PasskeyButton`; footnote under it. No tab bar.

Flow: tap → `POST /auth/login/begin` → `startAuthentication` → `POST /auth/login/finish` → go to `next` or `/admin`.

| State | What shows |
|---|---|
| loading bootstrap | the icon and a skeleton line for the name |
| idle | button enabled |
| waiting | button loading, label `login.waiting` |
| cancelled (`NotAllowedError`) | back to idle with the footnote `login.cancelled` in `--label-2` (no red) |
| `login_failed` | footnote `login.failed` in `--danger-text` |
| `rate_limited` | button disabled for `Retry-After`, footnote `error.rate_limited` |
| `ownerReady: false` | no button; `EmptyState` with `login.not_set_up` |
| no WebAuthn in the browser | no button; `login.unsupported` |
| offline home | §8.12.1 |

| Key | RU | EN |
|---|---|---|
| `login.subtitle.faceid` | Войдите с Face ID | Sign in with Face ID |
| `login.subtitle.touchid` | Войдите с Touch ID | Sign in with Touch ID |
| `login.subtitle.passkey` | Войдите с ключом входа | Sign in with your passkey |
| `login.button` | Войти | Sign in |
| `login.waiting` | Ждём подтверждения… | Waiting for confirmation… |
| `login.cancelled` | Вход отменён. Нажмите «Войти», чтобы попробовать снова. | Sign-in cancelled. Tap Sign in to try again. |
| `login.failed` | Этот ключ не подходит к дому. Попробуйте другой или попросите новую ссылку у хозяина дома. | This passkey doesn't open this home. Try another one or ask the home's owner for a new link. |
| `login.not_set_up` | Дом ещё не настроен. Откройте ссылку настройки, которую показал домашний компьютер. | This home isn't set up yet. Open the setup link shown by the home computer. |
| `login.unsupported` | Этот браузер не умеет входить по ключу. Откройте страницу в Safari или Chrome. | This browser can't use passkeys. Open the page in Safari or Chrome. |
| `login.footnote` | Пароля нет — только ваш Face ID или Touch ID. | No password — just your Face ID or Touch ID. |

### 8.2 First-run wizard — `/admin/setup#t=<token>`
One step per screen, `title-1` title, `callout` text in `--label-2`, the primary button at the bottom (sticky, safe
area). Progress: 4 dots under the nav bar for steps 2–5. Back (`‹`) on steps 3 and 4. The token is read from the
fragment once, kept in memory, and the fragment is removed with `history.replaceState`.

1. **Checking** — `POST /auth/token`. Spinner centered, `setup.checking`. Errors replace the wizard with an
   `ErrorState`: `token_invalid` → `setup.invalid`, `token_expired` → `setup.expired`, `token_used` → `setup.used`;
   each has the footnote `setup.new_link_hint`.
2. **Home name** — `TextField` prefilled with `homeName` (or `setup.home.placeholder`), 1–40 characters, autofocus,
   `enterkeyhint="next"`. A live preview line under it: `setup.home.preview` with the name in quotes.
3. **Your name** — `TextField` prefilled with `personName`, 1–40 characters.
4. **Passkey** — a large platform glyph (64 px, `--accent`), `PasskeyButton`. `register/begin` (with both names) →
   `startRegistration` → `register/finish` with `passkeyName` from the platform ("MacBook Pro", "iPhone", "Mac").
   Cancelled → `setup.passkey.cancelled` footnote; `InvalidStateError`/`passkey_exists` → `setup.passkey.exists`;
   `token_expired` here → step 1's expired screen.
5. **Done** — success checkmark (§4), `setup.done.title`, `setup.done.text`, button `setup.done.button` → `/admin`.

| Key | RU | EN |
|---|---|---|
| `setup.checking` | Проверяем ссылку… | Checking the link… |
| `setup.invalid` | Ссылка не работает | This link doesn't work |
| `setup.expired` | Ссылка устарела | This link has expired |
| `setup.used` | Ссылкой уже воспользовались | This link has already been used |
| `setup.new_link_hint` | Получите новую на домашнем компьютере: страница настройки или команда `homed admin setup-link`. | Get a new one on the home computer: the setup page or `homed admin setup-link`. |
| `setup.home.title` | Как назовём дом? | What's your home called? |
| `setup.home.text` | Под этим именем дом появится в ChatGPT у всей семьи. | Your family will see this name in ChatGPT. |
| `setup.home.placeholder` | Мой дом | My home |
| `setup.home.preview` | В ChatGPT: «{{name}}» | In ChatGPT: “{{name}}” |
| `setup.name.title` | Как вас зовут? | What's your name? |
| `setup.name.text` | Так вас будет видно в журнале. | This is how you'll appear in the activity log. |
| `setup.passkey.title.faceid` | Вход по Face ID | Sign in with Face ID |
| `setup.passkey.title.touchid` | Вход по Touch ID | Sign in with Touch ID |
| `setup.passkey.title.passkey` | Ключ входа | Your passkey |
| `setup.passkey.text` | Пароля не будет. Ключ хранится в Связке ключей iCloud и сразу появится на ваших устройствах. | There's no password. The passkey lives in your iCloud Keychain and shows up on all your devices. |
| `setup.passkey.button` | Создать ключ | Create passkey |
| `setup.passkey.cancelled` | Не получилось создать ключ. Попробуйте ещё раз. | The passkey wasn't created. Try again. |
| `setup.passkey.exists` | На этом устройстве уже есть ключ для этого дома. Просто войдите. | This device already has a passkey for this home. Just sign in. |
| `setup.done.title` | Готово! Дом ваш | All set — it's your home |
| `setup.done.text` | Теперь добавьте устройства, а потом подключите ChatGPT. | Now add your devices, then connect ChatGPT. |
| `setup.done.button` | Открыть дом | Open my home |
| `common.next` | Дальше | Next |

### 8.3 LAN setup page — `http://<lan ip>:8090/firstrun`
Served by the setup listener (T2.2) from the same `internal/web` bundle (route `/firstrun`), until an owner exists.
Same look; no tab bar; centered column 420 px. Data: API §5.

1. **PIN** — `title-1` `lan.pin.title`; eight one-digit boxes in two groups of four, as homed prints the PIN
   (`inputmode="numeric"`, `autocomplete="one-time-code"`, paste fills all, a pasted space is ignored), auto-submit on
   the eighth digit. `wrong_pin` → shake (`x: [0,-8,8,-4,4,0]`, 0.3 s; reduced motion: none) and `lan.pin.wrong`;
   `rate_limited` → `lan.pin.locked` (this device waits; T2.F).
2. **Server code** (skipped when a relay is configured) — `TextField` (monospace, 3 rows) for the `ghr1.` code,
   `lan.code.text`, button `lan.code.button`. Bad code → `lan.code.invalid`.
3. **Connecting** — `StatusOrb` + `lan.connecting` with the domain; SSE `relay` events. After 60 s without
   `connected` and a `selfCheck.ok`, show `lan.connecting.slow` with "Подробнее" (lastError).
4. **Link** — `lan.link.title`; the link as a `QRCode` (220 px) to scan with the phone, a `filled` button
   `lan.link.open` (opens the link here, for a Mac with Touch ID), `CopyField`, and the footnote `lan.link.expires`
   with a live countdown. "Новая ссылка" (plain) makes another.
5. **Done** (`410 setup_done`) — `lan.done`.
6. **Closed** (`410 setup_closed`: 30 minutes passed or 20 wrong PINs) — `lan.closed.title` and `lan.closed.text`.

| Key | RU | EN |
|---|---|---|
| `lan.pin.title` | Введите PIN | Enter the PIN |
| `lan.pin.text` | Он в окне, где запущен homed, или выполните homed admin firstrun на домашнем компьютере. | It's in the window where homed runs, or run homed admin firstrun on the home computer. |
| `lan.pin.wrong` | PIN не тот. Проверьте ещё раз. | Wrong PIN. Check it again. |
| `lan.pin.locked` | Слишком много попыток с этого устройства. Попробуйте снова через {{time}}. | Too many tries from this device. Try again in {{time}}. |
| `lan.closed.title` | Страница настройки закрыта | The setup page is closed |
| `lan.closed.text` | На домашнем компьютере выполните homed admin firstrun, чтобы открыть её снова, или homed admin setup-link, чтобы получить ссылку. | On the home computer, run homed admin firstrun to open it again, or homed admin setup-link for a link. |
| `lan.code.title` | Код сервера | Server code |
| `lan.code.text` | Вставьте код, который выдал сервер при установке. Он начинается с ghr1. | Paste the code your server printed at install. It starts with ghr1. |
| `lan.code.button` | Подключить | Connect |
| `lan.code.invalid` | Код не подходит. Скопируйте его целиком. | That code doesn't work. Copy all of it. |
| `lan.connecting` | Подключаемся к {{domain}}… | Connecting to {{domain}}… |
| `lan.connecting.slow` | Долго не получается. Проверьте интернет и что сервер работает. | This is taking long. Check the internet and that the server is up. |
| `lan.link.title` | Откройте дом на телефоне или здесь | Open your home on your phone or here |
| `lan.link.open` | Открыть на этом компьютере | Open on this computer |
| `lan.link.expires` | Ссылка одноразовая и действует ещё {{time}}. | Single-use link, valid for {{time}}. |
| `lan.done` | Дом уже настроен. Эта страница больше не нужна. | Your home is set up. You don't need this page anymore. |

### 8.4 Дом — `/admin`
Large title = the home name. Data: `GET /overview`.

1. **Hero card** (`--surface-1`, `--radius-xl`, hero glow, 20 px padding): `StatusOrb` left, `title-2` status
   (`relay.*` words, §10.1) and a `subheadline` line under it (`home.hero.*`). Tap → `/admin/settings/server`.
2. **Требует внимания** (only when `attention` is not empty): one `Row` per item, icon tile in the item's colour,
   title and subtitle from the table below, chevron to the target. At most 3 rows, then "Ещё {{count}}".
3. **Устройства**: one `Row` per module (module tile 30 px, title, subtitle `"{{state}} · {{devices}}"`). Footer row
   `home.devices.add` (plain, `Plus`).
4. **Недавно**: the last 5 assistant actions as activity rows (§8.8 format); footer row `home.recent.all`.

States: loading → hero skeleton + two 3-row skeleton sections. No modules → hero, then `EmptyState`
`home.empty.*` whose button opens `/admin/devices/add` (the screen's one filled button). No recent actions →
the section shows one `--label-2` line `home.recent.empty`. Error → `ErrorState`.

| `attention.kind` | Tile | RU title / subtitle | EN title / subtitle |
|---|---|---|---|
| `relay_offline` | red `CloudOff` | Нет связи с сервером / ChatGPT сейчас не достучится до дома | Can't reach the server / ChatGPT can't reach your home right now |
| `relay_unreachable` | orange `Globe` | Дом не виден снаружи / Связь есть, но проверка не проходит | Home isn't reachable from outside / Connected, but the check fails |
| `plugin_failing` | red module icon | {{title}} не отвечает / Перезапустить или посмотреть журнал | {{title}} isn't responding / Restart it or check the log |
| `plugin_update` | blue `ArrowDownCircle` | Обновление для {{title}} / Версия {{version}} | Update for {{title}} / Version {{version}} |
| `hub_update` | blue `ArrowDownCircle` | Новая версия Homie / {{version}} | New Homie version / {{version}} |
| `connections_stale` [P3] | orange `RefreshCw` | Обновите ChatGPT у семьи / Изменились устройства: {{count}} подключение\|подключения\|подключений | Refresh ChatGPT for the family / Devices changed: {{count}} connection(s) |
| `connection_failed` [P3] | red `UserX` | Не удалось подключить {{name}} / Открыть | Couldn't connect {{name}} / Open |

| Key | RU | EN |
|---|---|---|
| `home.hero.connected` | ChatGPT может управлять домом | ChatGPT can run your home |
| `home.hero.connecting` | Восстанавливаем связь с сервером | Reconnecting to the server |
| `home.hero.offline` | Проверьте интернет дома и сервер | Check your home internet and the server |
| `home.hero.not_configured` | Подключите сервер в настройках | Connect a server in Settings |
| `home.hero.unreachable` | Связь есть, но снаружи дом не виден | Connected, but not reachable from outside |
| `home.attention` | Требует внимания | Needs attention |
| `home.attention.more` | Ещё {{count}} | {{count}} more |
| `home.devices` | Устройства | Devices |
| `home.devices.add` | Добавить устройства | Add devices |
| `home.recent` | Недавно | Recent |
| `home.recent.all` | Весь журнал | All activity |
| `home.recent.empty` | Пока никто ничего не просил. | Nobody has asked for anything yet. |
| `home.empty.title` | Пока нет устройств | No devices yet |
| `home.empty.text` | Добавьте первые — например, телевизор. | Add your first ones — a TV, for example. |
| `home.empty.button` | Добавить устройства | Add devices |

### 8.5 Устройства — `/admin/devices`
Large title `devices.title`; nav trailing `+` → `/admin/devices/add`. One `Section` with a `Row` per module: module
tile 44 px, title (§10.4 module names), subtitle: state word (coloured dot) + " · " + devices count when known +
" · v{{version}}" on desktop only. Failing modules sort first.

States: loading → 3 skeleton rows. Empty → `EmptyState` `devices.empty.*` with the filled button. Error →
`ErrorState`.

| Key | RU | EN |
|---|---|---|
| `devices.title` | Устройства | Devices |
| `devices.count` | {{count}} устройство\|устройства\|устройств | {{count}} device(s) |
| `devices.empty.title` | Здесь будут ваши устройства | Your devices will live here |
| `devices.empty.text` | Каждый тип устройств — отдельный модуль. Начните с телевизора. | Each kind of device is a module. Start with the TV. |
| `devices.empty.button` | Добавить устройства | Add devices |

### 8.6 Add devices — `/admin/devices/add` (sheet)
1. **Choose** — title `add.title`. Section `add.recommended`: built-in catalog in the SPA
   (`web/src/catalog.ts`: `[{repo:"devXpro/homie-tv", icon:"tv"}]`), each a `Row` with the module tile, title and
   description from `GET /plugins/preview` (fetched lazily; skeleton while loading); already installed ones show
   "Установлено" and are disabled. Section `add.other`: a `TextField` `owner/repo` (also accepts a full GitHub URL) and
   a tinted button `add.find`.
2. **Preview** — module tile 64 px, `title-2` title, description, rows: version, source (`github.com/owner/repo`, links
   out), "Требуется Homie {{minHubVersion}}" only when `compatible` is false. Primary `add.install` (step-up, then
   `POST /plugins`). `compatible: false` → button disabled + `add.incompatible`; `assetFound: false` →
   `add.no_build`.
3. **Installing** — `StepList` of the job steps (§10.5 words) with the download `ProgressBar` under the active step.
   Closing the sheet keeps it running (pill).
4. **Done** — success checkmark, `add.done` and the filled button `add.done.button` → the module page, which opens its
   setup action automatically (§9.2) when the inventory is empty.
5. **Failed** — the failed step in red, the job error copy (§10.5), buttons "Повторить" (filled) and "Закрыть".

| Key | RU | EN |
|---|---|---|
| `add.title` | Добавить устройства | Add devices |
| `add.recommended` | Рекомендуем | Recommended |
| `add.other` | Другой модуль с GitHub | Another module from GitHub |
| `add.repo.placeholder` | владелец/репозиторий | owner/repository |
| `add.find` | Найти | Find |
| `add.installed` | Установлено | Installed |
| `add.install` | Установить | Install |
| `add.incompatible` | Нужна более новая версия Homie. Сначала обновите её в Настройках. | This needs a newer Homie. Update it in Settings first. |
| `add.no_build` | Для этого компьютера нет сборки модуля. | There's no build of this module for this computer. |
| `add.not_found` | Такого модуля не нашлось. Проверьте адрес. | Couldn't find that module. Check the address. |
| `add.done` | {{title}} установлен | {{title}} is installed |
| `add.done.button` | Настроить | Set up |

### 8.7 Module detail — `/admin/devices/:pluginId`
Data: `GET /plugins/{id}` and `GET /plugins/{id}/inventory`.

#### 8.7.1 Layout, top to bottom
1. **Header** (no large title; the nav bar shows the module name once scrolled): module tile 64 px, `title-1` name,
   `StatusPill` with the state word, `footnote` "Версия {{version}}".
2. **Banner** when not `up` (a `Section` card with a coloured left icon):
   - `failing` → `module.failing.*`, buttons "Перезапустить" (tinted) and "Журнал" (plain);
   - `starting` → spinner + `module.starting`;
   - `stopped` → `module.stopped` + "Запустить" (tinted, `POST /restart`);
   - `disabled` → `module.disabled` + "Включить" (tinted, `PATCH enabled:true`).
3. **Устройства** (when the module has an inventory tool): `Row` per item — `StatusDot` + name, subtitle, trailing
   state word, chevron → device sheet. Section footer `module.items.footer`.
   - Empty inventory → inside the section, `EmptyState` (compact) `module.items.empty` with the module's **setup
     action** (§9.2) as the screen's filled button.
   - Inventory error → the section shows the error line (`tool_failed` text, or §10.6) and "Повторить".
4. **Действия**: the module-level admin actions (§9.2) as rows: icon tile (blue `Wand2`; destructive: red `Trash2`),
   tool title, chevron. Opens the run sheet (§9.4).
5. **Что умеет в ChatGPT**: the assistant tools as rows — title (or the humanized name) and the description
   (`--label-2`, 2 lines). Tap → a sheet with the full description and a tinted button `module.try` that opens the
   run sheet for that tool (for testing without ChatGPT).
6. **Обслуживание**: rows "Обновить до {{version}}" (only with an update, step-up → job sheet), "Журнал модуля" →
   logs, "Перезапустить", a switch row `module.enabled` (`PATCH`), and the red row "Удалить {{title}}" (action sheet
   `module.remove.confirm`, step-up, job).

#### 8.7.2 States
Loading → header skeleton and two skeleton sections. `404` → `ErrorState` `module.not_found` with "К устройствам".
Removed while open (SSE `plugin.removed`) → go back to `/admin/devices` with a toast.

#### 8.7.3 Device sheet — `/admin/devices/:pluginId/items/:itemId`
Header: name (`title-2`), `StatusPill`, subtitle. Rows: the item-targeted admin actions (§9.2) with the target field
filled and hidden, non-destructive first, destructive last in red. Empty → `module.item.no_actions`.

#### 8.7.4–5 Run sheet and progress — §9.4, §9.5.

#### 8.7.6 Module log — `/admin/devices/:pluginId/logs`
Full-height `LogView` from `GET /plugins/{id}/logs?tail=1000`, live through `GET /events?logs=<id>`. Nav trailing:
"Скопировать". The "Следить" switch is on by default and turns off when the person scrolls up. Empty →
`module.logs.empty`.

| Key | RU | EN |
|---|---|---|
| `module.version` | Версия {{version}} | Version {{version}} |
| `module.failing.title` | {{title}} не отвечает | {{title}} isn't responding |
| `module.failing.text` | Мы перезапускаем его сами, но пока не выходит. ChatGPT сейчас не может управлять этими устройствами. | We keep restarting it, but it doesn't come up. ChatGPT can't control these devices right now. |
| `module.starting` | Запускается… | Starting… |
| `module.stopped` | Остановлен | Stopped |
| `module.disabled` | Выключен — ChatGPT не видит эти устройства | Turned off — ChatGPT can't see these devices |
| `module.items` | Устройства | Devices |
| `module.items.footer` | Состояние обновляется при открытии страницы. | The state refreshes when you open this page. |
| `module.items.empty` | Пока ни одного устройства | No devices yet |
| `module.actions` | Действия | Actions |
| `module.abilities` | Что умеет в ChatGPT | What it does in ChatGPT |
| `module.try` | Проверить без ChatGPT | Try without ChatGPT |
| `module.maintenance` | Обслуживание | Maintenance |
| `module.update` | Обновить до {{version}} | Update to {{version}} |
| `module.logs` | Журнал модуля | Module log |
| `module.logs.empty` | Модуль пока ничего не писал. | The module hasn't logged anything yet. |
| `module.logs.follow` | Следить | Follow |
| `module.restart` | Перезапустить | Restart |
| `module.enabled` | Включён | Enabled |
| `module.remove` | Удалить {{title}} | Remove {{title}} |
| `module.remove.confirm` | Удалить {{title}}? Настройки устройств будут стёрты, ChatGPT перестанет их видеть. | Remove {{title}}? Its device settings will be erased and ChatGPT will stop seeing them. |
| `module.not_found` | Такого модуля нет | There's no such module |
| `module.removed` | {{title}} удалён | {{title}} removed |
| `module.item.no_actions` | Для этого устройства нет действий. | No actions for this device. |

### 8.8 Журнал — `/admin/activity`
Large title `activity.title`. Under it a `SegmentedControl`: `activity.seg.assistant` (default:
`kind=tool&via=site,mcp`), `activity.seg.all` (no filter), `activity.seg.failed` (`status=failed`). Nav trailing:
`SlidersHorizontal` → filter sheet (person, module, source: ChatGPT / Админка / Терминал, kind: Действия /
Вход и безопасность); an active filter shows as removable chips under the segmented control.

List: grouped by local day (`activity.today`, `activity.yesterday`, else `d MMMM` / `d MMMM yyyy` for other years).
Row: icon tile (module icon for tools, gray `Shield` for admin events), title = tool title (or the admin action words,
§10.7), subtitle `"{{person}} · {{via}} · {{time}}"`, trailing: `XCircle` in `--danger` when failed, else the
duration when ≥ 2 s. Infinite scroll by cursor; a bottom spinner while loading the next page.

New SSE entries: when scrolled to the top, they slide in with the flash (§4); when scrolled down, a capsule
`activity.new` appears under the nav bar and scrolls to the top on tap.

**Entry sheet** — `/admin/activity/:id`: title, status (`activity.ok` or `activity.failed` + the error text), rows:
who, when (full date and time), how (`via` words), action (`tool` in monospace), duration; "Аргументы" as pretty JSON
in a `--fill` monospace box (already redacted, shown as is).

States: loading → 8 skeleton rows; empty → `EmptyState` `activity.empty.*`; filtered empty → `activity.none` with
"Сбросить фильтры"; error → `ErrorState`.

| Key | RU | EN |
|---|---|---|
| `activity.title` | Журнал | Activity |
| `activity.seg.assistant` | ChatGPT | ChatGPT |
| `activity.seg.all` | Всё | All |
| `activity.seg.failed` | Ошибки | Failures |
| `activity.filters` | Фильтры | Filters |
| `activity.filter.person` | Кто | Who |
| `activity.filter.module` | Устройства | Devices |
| `activity.filter.via` | Откуда | From |
| `activity.filter.kind` | Что | What |
| `activity.kind.tool` | Действия | Actions |
| `activity.kind.admin` | Вход и безопасность | Sign-in and security |
| `activity.today` | Сегодня | Today |
| `activity.yesterday` | Вчера | Yesterday |
| `activity.new` | Новые записи: {{count}} | {{count}} new |
| `activity.ok` | Выполнено | Done |
| `activity.failed` | Не получилось | Failed |
| `activity.who` | Кто | Who |
| `activity.when` | Когда | When |
| `activity.how` | Откуда | From |
| `activity.action` | Команда | Command |
| `activity.duration` | Длительность | Duration |
| `activity.args` | Аргументы | Arguments |
| `activity.empty.title` | Здесь появятся действия | Actions will show up here |
| `activity.empty.text` | Когда кто-то попросит ChatGPT что-то сделать, это будет видно тут. | When someone asks ChatGPT to do something, you'll see it here. |
| `activity.none` | Ничего не нашлось | Nothing found |
| `activity.reset` | Сбросить фильтры | Reset filters |

### 8.9 Настройки — `/admin/settings`
Large title `settings.title`. Sections:

1. **Profile row** (60 px): avatar or initials, the person's name (`headline`), `settings.role.owner`; → `PATCH /me`
   name editor sheet.
2. **Дом**: "Название" → value = home name → `/settings/home` (a `TextField` and Save; footer
   `settings.home.footer`; [P3] after a change with live connections, offer "Перевыпустить"); "Язык" → `/language`
   (checkmarked list Русский / English; `PATCH /settings` for the home default and `PATCH /me` for the person);
   "Оформление" → `/appearance` (checkmarked list Тёмное / Светлое / Как в системе; stored in `localStorage`, applied
   before first paint by a tiny external classic script `theme.js` loaded in `<head>` without `defer`, so there is
   neither a flash nor an inline script).
3. **Безопасность**: "Ключи входа" → value count → `/passkeys`; "Где выполнен вход" → `/sessions`.
4. **Связь**: "Сервер" → value = relay state word → `/server`.
5. **Доступ для ChatGPT** (phase 2 only; in phase 3 keys live on the person page): "Ключи доступа" → `/keys`.
6. **О программе**: "Версия" → value → `/about`; a blue dot when an update exists.
7. A single red centered row `settings.logout`.

**Passkeys** (`/settings/passkeys`): rows with the platform glyph, name, subtitle "Создан {{date}} · Последний вход
{{relative}}", `settings.passkeys.current` badge on the one in use. Tap → sheet: rename (`TextField`), "Удалить"
(red; disabled with `settings.passkeys.last` when it's the only one; step-up + action sheet). Footer button
`settings.passkeys.add` (tinted; step-up → `/me/passkeys/begin|finish`).

**Sessions** (`/settings/sessions`): rows device + "Активен {{relative}}"; "Этот сеанс" badge; swipe or tap →
"Выйти на этом устройстве". Footer red button `settings.sessions.others` (hidden with one session).

**Server** (`/settings/server`): hero-like card with `StatusOrb`, the state word and the domain; rows: "Адрес"
(publicUrl, copy), "На связи с" (since), "Проверка снаружи" (`selfCheck`: "Проходит · {{relative}}" or the failure),
"Проверить сейчас" (`POST /relay/check`). "Подробнее" disclosure shows `lastError` and `selfCheck.error` in
monospace. Bottom: "Обновить код сервера" (plain; step-up; a sheet with a code field; `domain_change_local_only` →
`settings.server.other_domain`).

**Keys** (`/settings/keys`): rows label + hint (monospace) + "Создан {{date}} · Использован {{relative}}". Tap →
"Отозвать" (red, step-up, action sheet). Footer tinted `settings.keys.new` → sheet: label field (default
"ChatGPT Site") → step-up → `SecretOnce` with the key.

**About** (`/settings/about`): `AppIcon` 64, "Homie", version and commit (monospace footnote); rows "Проверить
обновления" (`POST /system/check-update`, inline spinner, then "У вас последняя версия" or the update row);
"Обновить до {{version}}" (filled, the screen's primary; step-up → job sheet with `StepList`; at `restart` the sheet
shows `settings.update.restarting` and waits for SSE `hello` with the new version, then reloads the page); "Что
нового" links to `notesUrl`.

| Key | RU | EN |
|---|---|---|
| `settings.title` | Настройки | Settings |
| `settings.role.owner` | Хозяин дома | Home owner |
| `settings.role.member` | Член семьи | Family member |
| `settings.home` | Дом | Home |
| `settings.home.name` | Название | Name |
| `settings.home.footer` | Так дом называется в ChatGPT у всей семьи. | Your family sees this name in ChatGPT. |
| `settings.language` | Язык | Language |
| `settings.appearance` | Оформление | Appearance |
| `settings.appearance.dark` | Тёмное | Dark |
| `settings.appearance.light` | Светлое | Light |
| `settings.appearance.system` | Как в системе | Match system |
| `settings.security` | Безопасность | Security |
| `settings.passkeys` | Ключи входа | Passkeys |
| `settings.passkeys.current` | Этот | This one |
| `settings.passkeys.add` | Добавить ключ входа | Add a passkey |
| `settings.passkeys.last` | Это единственный ключ — без него в дом не войти. | It's your only passkey — you can't get in without it. |
| `settings.passkeys.remove.confirm` | Удалить ключ «{{name}}»? На этом устройстве войти будет нельзя. | Remove the passkey “{{name}}”? You won't be able to sign in with it. |
| `settings.sessions` | Где выполнен вход | Where you're signed in |
| `settings.sessions.this` | Этот сеанс | This session |
| `settings.sessions.signout` | Выйти на этом устройстве | Sign out on this device |
| `settings.sessions.others` | Выйти на всех других | Sign out everywhere else |
| `settings.connection` | Связь | Connection |
| `settings.server` | Сервер | Server |
| `settings.server.address` | Адрес | Address |
| `settings.server.since` | На связи с | Connected since |
| `settings.server.check` | Проверка снаружи | Outside check |
| `settings.server.check.ok` | Проходит · {{time}} | Passing · {{time}} |
| `settings.server.check_now` | Проверить сейчас | Check now |
| `settings.server.recode` | Обновить код сервера | Update the server code |
| `settings.server.other_domain` | Это код другого сервера. Сменить сервер можно только на домашнем компьютере: `homed relay connect`. | This code is for another server. Switch servers on the home computer: `homed relay connect`. |
| `settings.keys.section` | Доступ для ChatGPT | ChatGPT access |
| `settings.keys` | Ключи доступа | Access keys |
| `settings.keys.new` | Создать ключ | Create a key |
| `settings.keys.label` | Для чего | What it's for |
| `settings.keys.once` | Сохраните сейчас — второй раз ключ не показать. | Save it now — it won't be shown again. |
| `settings.keys.revoke.confirm` | Отозвать ключ «{{label}}»? Всё, что им пользуется, перестанет работать. | Revoke the key “{{label}}”? Anything using it will stop working. |
| `settings.about` | О программе | About |
| `settings.version` | Версия | Version |
| `settings.update.check` | Проверить обновления | Check for updates |
| `settings.update.latest` | У вас последняя версия | You're up to date |
| `settings.update.button` | Обновить до {{version}} | Update to {{version}} |
| `settings.update.restarting` | Перезапускаемся… Страница обновится сама. | Restarting… The page will reload by itself. |
| `settings.update.notes` | Что нового | What's new |
| `settings.logout` | Выйти | Sign out |

### 8.10 Люди [P3] — `/admin/people`
Large title `people.title`; nav trailing `UserPlus` → invite sheet.

- **Republish banner** (when any connection is stale): a `Section` with an orange `RefreshCw` tile,
  `people.stale.title`, `people.stale.text` and the filled button `people.republish` (the screen's primary).
  Without stale connections, the same action is a plain row at the bottom of the list ("Перевыпустить для всех").
- **List**: `Row` per person — `Avatar` 40, name, subtitle = role word + " · " + connection word (§10.3) or
  `people.invited` with the expiry; trailing dot coloured by the connection state.
- Empty (only the owner) → under the owner's row, `EmptyState` (compact) `people.empty.*` with the invite button.

**Republish sheet**: title `people.republish`; a row per person with the item state (§10.3 words; spinner while
running, check, red cross with the reason, "Нужен вход" with a `people.relogin` button that shares that person's link
to sign in again); footnote `people.republish.note`. It is started by step-up → `POST /connections/republish`.

**Person** (`/admin/people/:id`): header `Avatar` 64, name `title-1`, role. Sections:
1. **Доступ**: a switch row "Все устройства" (`plugins: ["*"]`); when off, a switch row per module. Footer
   `people.access.footer`. Changes are optimistic `PATCH /people/{id}`.
2. **ChatGPT**: the connection row (kind words §10.3: "Сайт в ChatGPT" / "Плагин в ChatGPT", state, published
   relative time); rows "Перевыпустить" (step-up → job), "Отключить" (red, step-up). Without a connection: "Ждём,
   пока {{name}} подключится" + "Новая ссылка" (step-up → invite).
3. **Вход**: passkeys count (read-only for the owner), "Выйти на всех устройствах" (revokes the person's sessions).
4. **Ключи доступа**: as §8.9 keys, scoped to the person.
5. Red row "Удалить из дома" (action sheet `people.remove.confirm`, step-up). Hidden for the owner.

**Invite sheet** (`/admin/people/invite`): step 1 — `TextField` "Как зовут?" and the access switches (default "Все
устройства" on); button "Создать приглашение" (step-up → `POST /people`). Step 2 — `QRCode` 200 px, `CopyField` with
the link, filled `people.invite.share` (`navigator.share({title, text, url})`, hidden when unsupported), plain
"Скопировать"; footnote `people.invite.footnote`.

| Key | RU | EN |
|---|---|---|
| `people.title` | Люди | People |
| `people.invited` | Приглашение до {{time}} | Invited, link valid until {{time}} |
| `people.stale.title` | ChatGPT у семьи устарел | Family ChatGPT is out of date |
| `people.stale.text` | Устройства или название дома изменились. Перевыпустите, чтобы ChatGPT узнал новое. | Devices or the home name changed. Republish so ChatGPT learns about it. |
| `people.republish` | Перевыпустить | Republish |
| `people.republish.all` | Перевыпустить для всех | Republish for everyone |
| `people.republish.note` | ChatGPT подхватит изменения примерно за 10 минут. | ChatGPT picks up the changes in about 10 minutes. |
| `people.relogin` | Отправить ссылку для входа | Send a sign-in link |
| `people.empty.title` | Пригласите семью | Invite your family |
| `people.empty.text` | У каждого будет свой «{{home}}» в ChatGPT. | Everyone gets their own “{{home}}” in ChatGPT. |
| `people.invite` | Пригласить | Invite |
| `people.invite.name` | Как зовут? | Their name |
| `people.invite.create` | Создать приглашение | Create invite |
| `people.invite.share` | Отправить | Share |
| `people.invite.share_text` | Подключайся к «{{home}}» — откроется на телефоне за пару минут. | Join “{{home}}” — it takes a couple of minutes on your phone. |
| `people.invite.footnote` | Ссылка работает 24 часа и только один раз. | The link works once, for 24 hours. |
| `people.access` | Доступ | Access |
| `people.access.all` | Все устройства | All devices |
| `people.access.footer` | Что {{name}} может просить у ChatGPT. | What {{name}} can ask ChatGPT to do. |
| `people.chatgpt` | ChatGPT | ChatGPT |
| `people.waiting` | Ждём, пока {{name}} подключится | Waiting for {{name}} to connect |
| `people.new_link` | Новая ссылка | New link |
| `people.disconnect` | Отключить | Disconnect |
| `people.signout_all` | Выйти на всех устройствах | Sign out everywhere |
| `people.remove` | Удалить из дома | Remove from home |
| `people.remove.confirm` | Удалить {{name}}? ChatGPT {{name}} перестанет видеть дом. | Remove {{name}}? Their ChatGPT will stop seeing your home. |

**Member view** [P3]: a member who signs in to `/admin` gets no tabs, just one screen with the large title = the home
name and three sections: "Мой ChatGPT" (their connection row with "Перевыпустить" and, in `needs_login`, the prompt
of §8.11 step 3), "Ключи входа" and "Где выполнен вход" (as §8.9), then "Выйти". Owner routes redirect there.

### 8.11 Join wizard [P3] — `/join#t=<token>`
Phone-only layout (on desktop the same 420 px column). One step per screen, as §8.2, progress dots for steps 2–4.

1. **Welcome** — `AppIcon` 72, `join.welcome.title`, `join.welcome.text`, filled `join.welcome.button`. Token errors
   as §8.2 step 1 with `join.new_link_hint`.
2. **Passkey** — as §8.2 step 4 (`register/*` with the invite token; the name is already set by the inviter).
3. **ChatGPT** — `POST /people/{me}/connections`, then by `prompt.kind` (live through SSE `connection`):
   - `device_code` (path B): `join.chatgpt.code.text`; the code in 34 px monospace with letter spacing in a `--fill`
     card + `CopyField` behaviour; filled `join.chatgpt.open` (opens `prompt.url` in a new tab); under it a spinner
     with `join.chatgpt.waiting`; when the state becomes `publishing`, a `StepList` of the publish steps
     (`join.publish.*`); `failed` → the error and "Повторить".
   - `add_plugin` (path A): numbered instructions `join.plugin.steps` with `CopyField` for the URL and the name, then
     the OAuth consent screen opens by itself.
4. **Always allow** — an inline SVG illustration of a ChatGPT permission prompt with the "Always allow" button
   highlighted (`--accent` ring, a gentle pulse; none with reduced motion); `join.allow.title`, `join.allow.text`,
   filled `join.allow.button`.
5. **Done** — success checkmark, `join.done.title`, `join.done.text`; no button (the person goes to ChatGPT).

| Key | RU | EN |
|---|---|---|
| `join.welcome.title` | {{inviter}} зовёт вас в «{{home}}» | {{inviter}} invites you to “{{home}}” |
| `join.welcome.text` | Вы сможете управлять домом голосом и в чате ChatGPT. Это займёт пару минут. | You'll run the home by voice and chat in ChatGPT. It takes a couple of minutes. |
| `join.welcome.button` | Начать | Start |
| `join.new_link_hint` | Попросите {{inviter}} прислать новую ссылку. | Ask {{inviter}} for a new link. |
| `join.chatgpt.title` | Подключим ChatGPT | Let's connect ChatGPT |
| `join.chatgpt.code.text` | Откройте ChatGPT и введите этот код. Войдите в свой аккаунт, если попросит. | Open ChatGPT and enter this code. Sign in to your account if asked. |
| `join.chatgpt.open` | Открыть ChatGPT | Open ChatGPT |
| `join.chatgpt.waiting` | Ждём подтверждения в ChatGPT… | Waiting for ChatGPT… |
| `join.publish.title` | Добавляем «{{home}}» в ваш ChatGPT | Adding “{{home}}” to your ChatGPT |
| `join.plugin.steps` | 1. Откройте chatgpt.com → Плагины → Добавить. 2. Вставьте адрес. 3. Назовите «{{home}}». | 1. Open chatgpt.com → Plugins → Add. 2. Paste the address. 3. Name it “{{home}}”. |
| `join.allow.title` | Нажмите «Всегда разрешать» | Tap “Always allow” |
| `join.allow.text` | Когда ChatGPT впервые спросит разрешение, выберите «Всегда разрешать». Голосом это подтвердить нельзя — без этого шага голос не сработает. | The first time ChatGPT asks for permission, choose “Always allow”. You can't confirm by voice, so without this, voice won't work. |
| `join.allow.button` | Понятно | Got it |
| `join.done.title` | Готово! | You're all set! |
| `join.done.text` | Скажите в ChatGPT: «Включи телевизор» — он сам позовёт «{{home}}». | Say to ChatGPT: “Turn on the TV” — it will call “{{home}}” by itself. |

### 8.12 Global states and flows

#### 8.12.1 Offline home
When the home is offline before the SPA loads, the visitor gets the relay's own offline page (`relay/offline.html`);
nothing to build. Once loaded, the SPA treats as **offline**: a network error, or a `404` whose body is not JSON (the
relay page). Then:
- a banner slides under the nav bar (`--surface-2`, `CloudOff` in `--warning`): `offline.title` and `offline.text`,
  plain button `offline.retry`;
- cached content stays visible at 60 % opacity; every action is disabled;
- retries: 2 s, 5 s, 10 s, then every 30 s, and immediately on `online`/`visibilitychange`; on success the banner
  leaves and queries refetch.

#### 8.12.2 Session expired
§7; toast `session.expired`.

#### 8.12.3 Step-up sheet
A small sheet (dialog on desktop): the platform glyph 48 px, `stepup.title`, `stepup.text` with the action's name
(passed by the caller, e.g. "установить «TV»"), `PasskeyButton` `stepup.button`, plain "Отмена". It opens itself
only when the API answers `step_up_required`, and skips itself while `stepUpUntil` is in the future. Cancel → the
original action is dropped with no error. Failure → `stepup.failed`.

`{{action}}` comes from the caller:

| Key | RU | EN |
|---|---|---|
| `stepup.action.install` | установить «{{title}}» | install “{{title}}” |
| `stepup.action.update` | обновить «{{title}}» | update “{{title}}” |
| `stepup.action.remove_module` | удалить «{{title}}» | remove “{{title}}” |
| `stepup.action.hub_update` | обновить Homie | update Homie |
| `stepup.action.key_create` | создать ключ доступа | create an access key |
| `stepup.action.key_revoke` | отозвать ключ доступа | revoke an access key |
| `stepup.action.passkey_add` | добавить ключ входа | add a passkey |
| `stepup.action.passkey_remove` | удалить ключ входа | remove a passkey |
| `stepup.action.relay` | обновить код сервера | update the server code |
| `stepup.action.invite` [P3] | пригласить человека | invite someone |
| `stepup.action.republish` [P3] | перевыпустить ChatGPT | republish ChatGPT |
| `stepup.action.disconnect` [P3] | отключить ChatGPT | disconnect ChatGPT |
| `stepup.action.remove_person` [P3] | удалить человека из дома | remove someone from the home |

#### 8.12.4 Unknown route
`ErrorState` `notfound.title` with "На главную".

| Key | RU | EN |
|---|---|---|
| `offline.title` | Дом не на связи | Home is offline |
| `offline.text` | Возможно, домашний компьютер уснул или пропал интернет. Пробуем переподключиться. | The home computer may be asleep or offline. Trying to reconnect. |
| `offline.retry` | Повторить | Retry |
| `session.expired` | Сеанс истёк — войдите снова | Session expired — please sign in again |
| `stepup.title` | Подтвердите, что это вы | Confirm it's you |
| `stepup.text` | Чтобы {{action}}, нужен Face ID или Touch ID. | To {{action}}, confirm with Face ID or Touch ID. |
| `stepup.button` | Подтвердить | Confirm |
| `stepup.failed` | Не получилось подтвердить. Попробуйте ещё раз. | Couldn't confirm. Try again. |
| `notfound.title` | Такой страницы нет | This page doesn't exist |
| `common.home` | На главную | Go home |
| `common.cancel` | Отмена | Cancel |
| `common.close` | Закрыть | Close |
| `common.done` | Готово | Done |
| `common.save` | Сохранить | Save |
| `common.retry` | Повторить | Try again |
| `common.copy` | Скопировать | Copy |
| `common.copied` | Скопировано | Copied |
| `common.more` | Подробнее | Details |
| `common.optional` | необязательно | optional |
| `common.delete` | Удалить | Delete |

## 9. Admin actions: tools as forms

Plugins describe their admin tools only by a name, a title, an English description, a JSON Schema and annotations
(PLAN §5.1). The UI turns them into actions by these rules, the same for every plugin.

### 9.1 Words
- **Title** = `tool.title`, else the name without the plugin prefix, humanized (`tv_set_input` → "Set input").
- **Description** = `tool.description`, shown as given. Plugin strings are English today; the UI chrome around them is
  translated.

### 9.2 Classifying a module's admin tools
For each admin tool (`audience: "admin"`, inventory tool excluded):
1. **Item action** — it has a `target` (API §2, Tool): shown in the device sheet with the target filled from the
   item's `id` and hidden.
2. **Module action** — everything else: shown in "Действия".
3. **Setup action** — one module action: the first by name ending in `_discover`, else `_add`, else none. It becomes
   the filled button of the empty inventory and opens automatically right after installing (§8.6).

Order inside a group: the setup action, then non-destructive by title, then `destructiveHint: true` ones last in red.
Assistant tools are listed under "Что умеет" only; they run from there just for testing.

### 9.3 JSON Schema → form
The root must be `type: "object"`. Fields follow `properties` order (JSON object order as received), required ones
first. Each field: label = `title` or the humanized property name; hint = `description`; optional fields get the
`common.optional` badge; `default` pre-fills.

| Schema | Control |
|---|---|
| the tool's `target` property | hidden in the device sheet; elsewhere a `Select` of the inventory items (name + subtitle), plus "Другое…" for free text |
| `string` + `enum` (≤ 4 values, each ≤ 12 chars) | `SegmentedControl` |
| `string` + `enum` (more) | `Select`; option label = the value, or `x-enumNames`/`oneOf[].title` when given |
| `string`, `format: "ipv4"` | `TextField`, `inputmode="decimal"`, monospace |
| `string`, `format: "uri"` / `"email"` | `TextField`, `type="url"` / `type="email"` |
| `string` whose name matches `(?i)(token\|key\|secret\|password\|pin\|code)` or `writeOnly: true` | `TextField type="password"` with a show toggle; never echoed in results |
| `string`, `maxLength` > 200 or `format: "textarea"` | multi-line `TextField` (4 rows) |
| `string` | `TextField`; `pattern`, `minLength`, `maxLength` validated |
| `integer`/`number` with `minimum` and `maximum`, range ≤ 1000 | `Slider` with the value; integer step 1, number step `multipleOf` or range/100 |
| `integer`/`number` otherwise | `Stepper` (integer) or `TextField inputmode="decimal"` (number) |
| `boolean` | `Switch` row |
| `array` of `string` + `enum` | a checkmarked list (multi-select) |
| `array` of `string` | chips with an add field |
| `object` (nested) | a sub-`Section` with its own header (the property label), fields inside |
| anything else (`oneOf`, `anyOf`, `$ref`, arrays of objects, missing `type`) | a monospace JSON editor for that property with live parse errors, label badge "JSON" |

- More than 2 optional fields → they fold under a disclosure row `form.more` ("Ещё параметры").
- Empty optional fields are left out of `args` (never sent as `""`).
- Validation runs on blur and on submit with `@cfworker/json-schema` (it does not use `eval`, so the CSP holds).
  Messages are the generic §10.6 field words (`form.required`, `form.min`, `form.max`, `form.pattern`, `form.enum`).
- Server `invalid_args` → `fields[].path` (JSON Pointer) maps to the field; unknown paths show above the button.
- A tool with no fields has no form: it runs as soon as it is opened (§9.4), unless it is destructive, in which case
  the action sheet confirms first.

### 9.4 Run sheet
Title = the tool title; under it the description (`subheadline`, `--label-2`). States:

| State | Content | Buttons |
|---|---|---|
| form | the fields | filled = the tool title (destructive: red + action sheet `form.confirm` first) |
| running | §9.5 | plain `run.cancel` (`DELETE /calls/{id}`) |
| ok | success checkmark, `title-3` `run.ok`, `result.text`, `ResultView`, follow-up actions (§9.7) | filled `common.done` |
| failed | `TriangleAlert` 56 px in `--warning`, `title-3` `run.failed`, the plugin's text (it says what to do) | filled `common.retry` (back to the form with the values), plain `common.close` |
| error | as failed, the §10.6 words for `error.code` | the same |
| cancelled | `run.cancelled` | `common.close` |

- Submitting → `POST /tools/{name}/calls`; `409 call_running` → attach to the returned `callId`.
- Closing the sheet never cancels. The running-action pill (§5.2) reopens it; on reload, `GET /calls?state=running`
  restores the pills.
- When a call ends with `ok` and the module has an inventory, the inventory is refetched.
- A toast `run.done_bg` ("{{title}} — готово") appears when a call ends while its sheet is closed.

### 9.5 Progress for long actions
The running state shows, top to bottom:
1. a 28 px `Spinner`, or a `ProgressBar` with "{{progress}} из {{total}}" when `progress.total` is known;
2. the message: `progress.message` when the plugin sends one, else, for tools with `timeoutMs` ≥ 30 s, the tool
   description in a `--fill` card titled `run.meanwhile` ("Пока ждём") — for `tv_pair` that is the "press OK on the
   TV remote" instruction;
3. the elapsed time `m:ss`, and for `timeoutMs` ≥ 30 s "из {{timeout}}" after it;
4. at 80 % of the timeout, the footnote `run.slow`.

`aria-live="polite"` announces the message changes, not the timer.

### 9.6 Result view
`result.text` comes first, as `body`. `result.structured` renders by shape:
- `{"items":[…]}` in the inventory shape → device rows with state dots;
- an array of objects (at the root or in the only array-valued key) → a `Section` of rows: title = the first of
  `name|title|label|id|host`; subtitle = up to 3 other scalar values joined by " · "; booleans as "да/нет";
- an object of scalars → key/value rows (humanized keys, `--label-2` values; booleans as "да/нет");
- anything else, and any nested value beyond one level → a "Подробнее" disclosure with pretty JSON.
Empty arrays render `run.nothing` ("Ничего не найдено").

### 9.7 Follow-up actions
After `ok`, the result offers the next step without the person knowing tool names:
- For every object in a rendered result list, and for every **module action** whose required properties are all
  present in that object with matching types → a tinted button on that row with the action's title, opening its run
  sheet pre-filled from the object (`tv_discover` → each found TV gets "Add a TV").
- When the result object has an `id` equal to an inventory item (after refetch), its **item actions** are offered the
  same way (`tv_add` → "Pair a TV").
- At most 2 buttons per row; the rest go under "…".

| Key | RU | EN |
|---|---|---|
| `form.more` | Ещё параметры | More options |
| `form.other` | Другое… | Other… |
| `form.required` | Заполните это поле | Fill in this field |
| `form.min` | Не меньше {{min}} | At least {{min}} |
| `form.max` | Не больше {{max}} | At most {{max}} |
| `form.minLength` | Не короче {{min}} символов | At least {{min}} characters |
| `form.maxLength` | Не длиннее {{max}} символов | At most {{max}} characters |
| `form.pattern` | Неверный формат | Wrong format |
| `form.enum` | Выберите один из вариантов | Choose one of the options |
| `form.json` | Неверный JSON | Invalid JSON |
| `form.confirm` | Точно выполнить «{{title}}»? Это нельзя отменить. | Run “{{title}}”? This can't be undone. |
| `run.cancel` | Отменить | Cancel |
| `run.ok` | Готово | Done |
| `run.failed` | Не получилось | That didn't work |
| `run.cancelled` | Отменено | Cancelled |
| `run.meanwhile` | Пока ждём | While you wait |
| `run.of` | из {{total}} | of {{total}} |
| `run.slow` | Долго нет ответа. Можно подождать ещё или отменить. | No answer for a while. You can wait or cancel. |
| `run.nothing` | Ничего не найдено | Nothing found |
| `run.done_bg` | {{title}} — готово | {{title}} — done |
| `run.running_many` | Выполняется: {{count}} | Running: {{count}} |
| `common.yes` | да | yes |
| `common.no` | нет | no |

## 10. Glossary: technical states → words

### 10.1 Relay (`RelayStatus.state`, plus the self-check)
| State | Colour | RU | EN |
|---|---|---|---|
| `connected`, self-check ok | success | На связи | Online |
| `connected`, self-check failing | warning | Не виден снаружи | Not reachable from outside |
| `connecting` | warning | Подключаемся… | Connecting… |
| `offline` | danger | Нет связи с сервером | Can't reach the server |
| `not_configured` | gray | Сервер не подключён | No server yet |

### 10.2 Module (`PluginSummary.state`) and device (`InventoryItem.state`)
| State | Colour | RU | EN |
|---|---|---|---|
| `starting` | warning | Запускается… | Starting… |
| `up` | success | Работает | Running |
| `failing` | danger | Не отвечает | Not responding |
| `stopped` | gray | Остановлен | Stopped |
| `disabled` | gray | Выключен | Turned off |
| item `on` | success | Включено | On |
| item `off` | gray | Выключено | Off |
| item `offline` | danger | Не в сети | Offline |
| item `unknown` | gray | Нет данных | Unknown |
| item `paired` | success | Подключено | Paired |
| item `unpaired` | warning | Не подключено | Not paired |

### 10.3 People and connections [P3]
| Thing | RU | EN |
|---|---|---|
| role `owner` / `member` | Хозяин дома / Член семьи | Home owner / Family member |
| kind `site` / `plugin` | Сайт в ChatGPT / Плагин в ChatGPT | ChatGPT Site / ChatGPT plugin |
| `pending` | Не подключён | Not connected |
| `needs_login` | Нужен вход в ChatGPT | Needs a ChatGPT sign-in |
| `publishing` | Публикуем… | Publishing… |
| `live` | Подключён | Connected |
| `live` + `stale` | Нужно перевыпустить | Needs republishing |
| `failed` | Ошибка подключения | Connection failed |
| `revoked` | Отключён | Disconnected |
| republish item `waiting` / `running` / `ok` / `failed` / `needs_login` / `skipped` | Ждёт / Публикуем… / Готово / Ошибка / Нужен вход / Не нужно | Waiting / Publishing… / Done / Failed / Needs sign-in / Not needed |

### 10.4 Surfaces (`via`) and modules
| Value | RU | EN |
|---|---|---|
| `site`, `mcp` | через ChatGPT | via ChatGPT |
| `admin` | в админке | in the admin |
| `cli` | в терминале | in the terminal |
| module title | the plugin's `title`, except the first-party names: `tv` → Телевизоры / TVs | |
| generic word for a plugin (rare: add flow, logs) | модуль | module |

### 10.5 Jobs (`Job.step`, `Job.error.code`)
| Value | RU | EN |
|---|---|---|
| `resolve` | Ищем версию | Finding the version |
| `download` | Скачиваем | Downloading |
| `verify` | Проверяем файлы | Checking the files |
| `unpack` | Распаковываем | Unpacking |
| `switch` | Переключаем версию | Switching versions |
| `start` | Запускаем | Starting |
| `stop` | Останавливаем | Stopping |
| `remove` | Удаляем | Removing |
| `replace` | Устанавливаем | Installing |
| `restart` | Перезапускаемся | Restarting |
| `rollback` | Возвращаем прежнюю версию | Rolling back |
| `login` / `build` / `publish` / `deploy` / `refresh` [P3] | Вход в ChatGPT / Готовим / Публикуем / Выкладываем / Обновляем | Signing in / Preparing / Publishing / Deploying / Refreshing |
| `checksum_mismatch` | Файлы повреждены или подменены — установка остановлена. | The files are damaged or tampered with — install stopped. |
| `no_asset` | Нет сборки для этого компьютера. | No build for this computer. |
| `incompatible` | Нужна более новая версия Homie. | Needs a newer Homie. |
| `start_failed` | Новая версия не запустилась. | The new version didn't start. |
| `rolled_back` | Новая версия не запустилась, вернули прежнюю. | The new version didn't start; the previous one is back. |
| `upstream` | GitHub не отвечает. Попробуйте позже. | GitHub isn't answering. Try later. |

### 10.6 API error codes → words (`error.<code>`)
| Code | RU | EN |
|---|---|---|
| `generic` (any unknown) | Что-то пошло не так. Попробуйте ещё раз. | Something went wrong. Try again. |
| `invalid_args` | Проверьте поля формы. | Check the form fields. |
| `unauthenticated` | Сеанс истёк — войдите снова | Session expired — please sign in again |
| `forbidden` | Это может делать только хозяин дома. | Only the home owner can do this. |
| `not_found` | Этого уже нет. | This no longer exists. |
| `unknown_tool` | Такой команды больше нет. | That command no longer exists. |
| `conflict` | Такое уже есть. | That already exists. |
| `last_passkey` | Это единственный ключ — без него в дом не войти. | It's your only passkey — you can't get in without it. |
| `call_running` | Это действие уже выполняется. | This action is already running. |
| `job_running` | Это уже делается. | This is already in progress. |
| `rate_limited` | Слишком много попыток. Подождите минуту. | Too many tries. Wait a minute. |
| `plugin_unavailable` | Модуль сейчас не работает. Загляните в Устройства. | The module isn't running. Check Devices. |
| `timeout` | Не дождались ответа от устройства. | The device didn't answer in time. |
| `upstream` | GitHub не отвечает. Попробуйте позже. | GitHub isn't answering. Try later. |
| `home_not_ready` | Дом ещё запускается. Секунду… | The home is still starting. One moment… |
| `internal` | Ошибка в доме. Подробности — в журнале homed. | Something broke at home. See the homed log. |
| `cancelled` (call) | Отменено | Cancelled |

### 10.7 Admin actions in the Журнал
| Action | RU | EN |
|---|---|---|
| `auth.setup` | Дом настроен | Home set up |
| `auth.join` | Присоединился(ась) к дому | Joined the home |
| `auth.login` / `auth.login_failed` | Вход / Неудачный вход | Signed in / Failed sign-in |
| `auth.step_up` | Подтверждение личности | Identity confirmed |
| `auth.logout` | Выход | Signed out |
| `passkey.add` / `.rename` / `.remove` | Добавлен ключ входа / Ключ переименован / Ключ удалён | Passkey added / renamed / removed |
| `session.revoke` | Выход на устройстве | Signed out a device |
| `key.create` / `key.revoke` | Создан ключ доступа / Ключ доступа отозван | Access key created / revoked |
| `person.create` / `.update` / `.remove` | Добавлен человек / Изменён доступ / Человек удалён | Person added / Access changed / Person removed |
| `invite.create` / `invite.revoke` | Приглашение создано / отозвано | Invite created / revoked |
| `connection.create` / `.publish` / `.revoke` | Подключение ChatGPT / Публикация / Отключение | ChatGPT connected / Published / Disconnected |
| `plugin.install` / `.update` / `.remove` | Установлен модуль / Обновлён модуль / Удалён модуль | Module installed / updated / removed |
| `plugin.enable` / `.disable` / `.restart` | Модуль включён / выключен / перезапущен | Module enabled / disabled / restarted |
| `relay.change` | Обновлён код сервера | Server code updated |
| `settings.update` | Изменены настройки | Settings changed |
| `hub.update` | Homie обновлён | Homie updated |
| `access.reset` | Доступ сброшен с домашнего компьютера | Access reset on the home computer |
| `setup.link` | Создана ссылка настройки | Setup link created |

## 11. Language and formatting
- i18next with `ru` (default) and `en`; the language is the person's `language`, else the home's, else `ru`.
  Switching applies instantly without reload.
- Dates and times: `Intl.DateTimeFormat(locale)`: times `HH:mm` (24 h in both), dates `d MMMM` (`15 ноября`,
  `15 November`). Relative times with `Intl.RelativeTimeFormat` for under 7 days ("2 мин назад", "вчера").
- Durations: `< 1 s` → "0,4 с" / "0.4 s"; otherwise "12 с", "1 мин 5 с".
- Quotes: «ёлочки» in RU, “curly” in EN. The home name is always quoted when it sits in a sentence.
- RU copy uses «ё». No exclamation marks except in `setup.done.title`, `join.done.title`.

## 12. Quality bars (T2.4 acceptance)
- Lighthouse accessibility ≥ 95 on every screen, both themes.
- Keyboard: everything reachable, visible focus ring (2 px `--accent`, 2 px offset), sheets trap focus and return it.
- Screen readers: rows announce title, subtitle and state; status dots are `aria-hidden` next to words; progress
  messages use `aria-live="polite"`.
- No layout shift on navigation; no web fonts; no CDN; everything bundled.
- CSP-compatible: no inline scripts or styles, no `eval` (hence `@cfworker/json-schema`, not Ajv).
- Libraries: shadcn/ui (Radix), vaul (Drawer), sonner (Toast), `motion`, TanStack Query, react-router, i18next,
  lucide-react, `@simplewebauthn/browser`, `@cfworker/json-schema`, `qrcode` [P3].
- Screenshots of every screen and state that has data, dark and light, phone (390 × 844) and desktop (1440 × 900),
  saved to `docs/screenshots/` (PLAN T2.4).
