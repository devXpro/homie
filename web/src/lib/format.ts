// Durations, clocks and sizes; dates, times and quotes are the package's (fmtDate, fmtRelative, …).

import type { Lang } from './types'

const LOCALE: Record<Lang, string> = { ru: 'ru-RU', en: 'en-GB' }

/** fmtDuration is "0,4 с", "12 с", "1 мин 5 с". */
export function fmtDuration(ms: number, lang: Lang) {
  const nf = new Intl.NumberFormat(LOCALE[lang], { maximumFractionDigits: 1 })
  const unit = lang === 'ru' ? { s: 'с', m: 'мин' } : { s: 's', m: 'min' }
  if (ms < 1000) return `${nf.format(Math.max(ms, 0) / 1000)} ${unit.s}`
  const total = Math.round(ms / 1000)
  if (total < 60) return `${total} ${unit.s}`
  const m = Math.floor(total / 60)
  const s = total % 60
  return s === 0 ? `${m} ${unit.m}` : `${m} ${unit.m} ${s} ${unit.s}`
}

/** fmtClock is an elapsed time as m:ss. */
export function fmtClock(ms: number) {
  const total = Math.max(0, Math.floor(ms / 1000))
  const m = Math.floor(total / 60)
  const s = total % 60
  return `${m}:${s.toString().padStart(2, '0')}`
}

export function fmtBytes(n: number, lang: Lang) {
  const mb = n / 1_000_000
  return new Intl.NumberFormat(LOCALE[lang], {
    style: 'unit',
    unit: mb >= 1 ? 'megabyte' : 'kilobyte',
    maximumFractionDigits: 1,
  }).format(mb >= 1 ? mb : n / 1000)
}
