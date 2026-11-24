import { Check, ChevronDown, Eye, EyeOff, Minus, Plus } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import {
  useId,
  useRef,
  useState,
  type InputHTMLAttributes,
  type KeyboardEvent,
  type ReactNode,
  type TextareaHTMLAttributes,
} from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/cn'
import { useIsPhone } from '@/lib/hooks'
import { spring } from '@/lib/motion'
import { Row, Section } from './list'
import { Sheet } from './sheet'

// Form controls (DESIGN.md §6): TextField, Switch, SegmentedControl, Slider, Stepper, Select, Disclosure.

interface FieldChrome {
  label?: ReactNode
  hint?: ReactNode
  error?: ReactNode
  badge?: ReactNode
  hideLabel?: boolean
}

function FieldFrame({
  id,
  label,
  hint,
  error,
  badge,
  hideLabel,
  aside,
  children,
}: FieldChrome & { id: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-col">
      {label && (
        <div className={cn('flex items-baseline gap-2 px-1 pb-1.5', hideLabel && 'sr-only')}>
          <label htmlFor={id} className="type-footnote text-label-2">
            {label}
          </label>
          {badge}
          {aside && <span className="ml-auto">{aside}</span>}
        </div>
      )}
      {children}
      {error ? (
        <p id={`${id}-error`} className="px-1 pt-1.5 type-footnote text-danger-text" role="alert">
          {error}
        </p>
      ) : (
        hint && (
          <p id={`${id}-hint`} className="px-1 pt-1.5 type-footnote text-label-2">
            {hint}
          </p>
        )
      )}
    </div>
  )
}

/** Badge is the small "необязательно" / "JSON" mark next to a field label. */
export function Badge({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={cn('rounded-xs bg-fill px-1.5 py-px text-[11px] leading-4 font-medium text-label-2', className)}>
      {children}
    </span>
  )
}

const inputClass =
  'w-full rounded-md bg-fill px-3.5 text-label placeholder:text-label-3 outline-none focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-accent aria-invalid:outline-2 aria-invalid:outline-offset-0 aria-invalid:outline-danger disabled:opacity-50'

export interface TextFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'size'>, FieldChrome {
  mono?: boolean
  secret?: boolean
  autoFocusOnOpen?: boolean
}

export function TextField({ label, hint, error, badge, hideLabel, mono, secret, autoFocusOnOpen, id, className, type, ...props }: TextFieldProps) {
  const { t } = useTranslation()
  const auto = useId()
  const fieldId = id ?? auto
  const [shown, setShown] = useState(false)
  const describedBy = error ? `${fieldId}-error` : hint ? `${fieldId}-hint` : undefined
  return (
    <FieldFrame id={fieldId} label={label} hint={hint} error={error} badge={badge} hideLabel={hideLabel}>
      <div className="relative">
        <input
          id={fieldId}
          type={secret ? (shown ? 'text' : 'password') : (type ?? 'text')}
          aria-invalid={error ? true : undefined}
          aria-describedby={describedBy}
          data-autofocus={autoFocusOnOpen || undefined}
          className={cn(inputClass, 'h-11 type-body', mono && 'font-mono text-[15px]', secret && 'pr-12', className)}
          {...props}
        />
        {secret && (
          <button
            type="button"
            onClick={() => setShown((s) => !s)}
            aria-label={shown ? t('common.hide') : t('common.show')}
            aria-pressed={shown}
            className="absolute top-0 right-0 inline-flex size-11 items-center justify-center text-label-2 hover:text-label"
          >
            {shown ? <EyeOff aria-hidden className="size-5" /> : <Eye aria-hidden className="size-5" />}
          </button>
        )}
      </div>
    </FieldFrame>
  )
}

export interface TextAreaProps extends TextareaHTMLAttributes<HTMLTextAreaElement>, FieldChrome {
  mono?: boolean
  autoFocusOnOpen?: boolean
}

export function TextArea({ label, hint, error, badge, hideLabel, mono, autoFocusOnOpen, id, className, rows = 4, ...props }: TextAreaProps) {
  const auto = useId()
  const fieldId = id ?? auto
  const describedBy = error ? `${fieldId}-error` : hint ? `${fieldId}-hint` : undefined
  return (
    <FieldFrame id={fieldId} label={label} hint={hint} error={error} badge={badge} hideLabel={hideLabel}>
      <textarea
        id={fieldId}
        rows={rows}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy}
        data-autofocus={autoFocusOnOpen || undefined}
        className={cn(inputClass, 'resize-none py-2.5 type-body', mono && 'font-mono text-[14px] leading-5', className)}
        {...props}
      />
    </FieldFrame>
  )
}

export interface SwitchProps {
  checked: boolean
  onChange: (checked: boolean) => void
  disabled?: boolean
  id?: string
  'aria-label'?: string
  'aria-labelledby'?: string
  'aria-describedby'?: string
}

/** Switch is the iOS toggle: 51 × 31, green when on (DESIGN.md §6). */
export function Switch({ checked, onChange, disabled, ...aria }: SwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex h-[31px] w-[51px] shrink-0 items-center rounded-full p-0.5 transition-colors duration-200 disabled:opacity-40',
        checked ? 'bg-success' : 'bg-fill',
      )}
      {...aria}
    >
      <motion.span
        aria-hidden
        className="block size-[27px] rounded-full bg-white shadow-[0_3px_8px_rgba(0,0,0,0.15),0_1px_1px_rgba(0,0,0,0.16)]"
        initial={false}
        animate={{ x: checked ? 20 : 0 }}
        transition={spring.snappy}
      />
    </button>
  )
}

/** SwitchRow is a list row whose only control is the switch (the row itself is not a button). */
export function SwitchRow({
  title,
  subtitle,
  checked,
  onChange,
  disabled,
  leading,
}: {
  title: ReactNode
  subtitle?: ReactNode
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
  leading?: ReactNode
}) {
  const id = useId()
  return (
    <div className={cn('homie-row flex items-center gap-3 px-4', subtitle ? 'min-h-[60px] py-2.5' : 'min-h-11 py-1.5', leading && '[--row-inset:60px]')}>
      {leading}
      <span className="flex min-w-0 flex-1 flex-col">
        <span id={`${id}-t`} className="type-body">
          {title}
        </span>
        {subtitle && (
          <span id={`${id}-s`} className="type-subheadline text-label-2">
            {subtitle}
          </span>
        )}
      </span>
      <Switch
        checked={checked}
        onChange={onChange}
        disabled={disabled}
        aria-labelledby={`${id}-t`}
        aria-describedby={subtitle ? `${id}-s` : undefined}
      />
    </div>
  )
}

export interface Option<V extends string = string> {
  value: V
  label: ReactNode
  description?: ReactNode
}

/** SegmentedControl is a radio group with a sliding thumb (DESIGN.md §6); arrow keys move the choice. */
export function SegmentedControl<V extends string>({
  value,
  onChange,
  options,
  label,
  className,
}: {
  value: V
  onChange: (v: V) => void
  options: Option<V>[]
  label: string
  className?: string
}) {
  const id = useId()
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const onKey = (e: KeyboardEvent, i: number) => {
    const dir = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
    if (!dir) return
    e.preventDefault()
    const next = (i + dir + options.length) % options.length
    onChange(options[next].value)
    refs.current[next]?.focus()
  }
  return (
    <div role="radiogroup" aria-label={label} className={cn('flex h-8 rounded-[9px] bg-fill p-0.5', className)}>
      {options.map((o, i) => {
        const selected = o.value === value
        return (
          <button
            key={o.value}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            role="radio"
            aria-checked={selected}
            tabIndex={selected ? 0 : -1}
            onClick={() => onChange(o.value)}
            onKeyDown={(e) => onKey(e, i)}
            className="relative flex flex-1 items-center justify-center rounded-[7px] px-3 text-[13px] leading-[18px] font-semibold text-label outline-offset-0"
          >
            {selected && (
              <motion.span
                layoutId={`seg-${id}`}
                aria-hidden
                className="absolute inset-0 rounded-[7px] bg-segment-thumb shadow-[0_3px_8px_rgba(0,0,0,0.12),0_3px_1px_rgba(0,0,0,0.04)]"
                transition={spring.snappy}
              />
            )}
            <span className="relative truncate">{o.label}</span>
          </button>
        )
      })}
    </div>
  )
}

/** Slider is a styled native range input with its value right of the label (DESIGN.md §6). */
export function Slider({
  label,
  hint,
  badge,
  value,
  onChange,
  min,
  max,
  step,
  format = String,
}: FieldChrome & {
  value: number
  onChange: (v: number) => void
  min: number
  max: number
  step: number
  format?: (v: number) => string
}) {
  const id = useId()
  const pct = max > min ? ((value - min) / (max - min)) * 100 : 0
  return (
    <FieldFrame
      id={id}
      label={label}
      hint={hint}
      badge={badge}
      aside={
        <output htmlFor={id} className="type-subheadline font-semibold text-label tabular-nums">
          {format(value)}
        </output>
      }
    >
      <input
        id={id}
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        aria-describedby={hint ? `${id}-hint` : undefined}
        className="homie-slider"
        style={{ ['--value' as string]: `${pct}%` }}
      />
    </FieldFrame>
  )
}

/** Stepper is − and + around a tabular number (DESIGN.md §6). */
export function Stepper({
  label,
  hint,
  error,
  badge,
  value,
  onChange,
  min,
  max,
  step = 1,
}: FieldChrome & { value: number | undefined; onChange: (v: number | undefined) => void; min?: number; max?: number; step?: number }) {
  const { t } = useTranslation()
  const id = useId()
  const clamp = (v: number) => Math.min(max ?? Infinity, Math.max(min ?? -Infinity, v))
  const cur = value ?? 0
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error} badge={badge}>
      <div className="flex h-11 items-center gap-2">
        <button
          type="button"
          aria-label={t('form.decrease')}
          onClick={() => onChange(clamp(cur - step))}
          disabled={min !== undefined && cur <= min}
          className="inline-flex size-11 items-center justify-center rounded-md bg-fill text-accent disabled:opacity-40"
        >
          <Minus aria-hidden className="size-5" />
        </button>
        <input
          id={id}
          inputMode="numeric"
          value={value === undefined ? '' : String(value)}
          onChange={(e) => {
            const raw = e.target.value.trim()
            if (raw === '' || raw === '-') return onChange(undefined)
            const n = Number(raw)
            if (Number.isFinite(n)) onChange(Math.trunc(n))
          }}
          aria-invalid={error ? true : undefined}
          className={cn(inputClass, 'h-11 w-24 text-center type-body tabular-nums')}
        />
        <button
          type="button"
          aria-label={t('form.increase')}
          onClick={() => onChange(clamp(cur + step))}
          disabled={max !== undefined && cur >= max}
          className="inline-flex size-11 items-center justify-center rounded-md bg-fill text-accent disabled:opacity-40"
        >
          <Plus aria-hidden className="size-5" />
        </button>
      </div>
    </FieldFrame>
  )
}

/** Select is a checkmarked list in a sheet on phones and a native select on larger screens (DESIGN.md §6). */
export function Select<V extends string>({
  label,
  hint,
  error,
  badge,
  value,
  onChange,
  options,
  placeholder,
}: FieldChrome & {
  value: V | undefined
  onChange: (v: V) => void
  options: Option<V>[]
  placeholder?: string
}) {
  const { t } = useTranslation()
  const id = useId()
  const phone = useIsPhone()
  const [open, setOpen] = useState(false)
  const current = options.find((o) => o.value === value)
  const describedBy = error ? `${id}-error` : hint ? `${id}-hint` : undefined
  if (!phone) {
    return (
      <FieldFrame id={id} label={label} hint={hint} error={error} badge={badge}>
        <div className="relative">
          <select
            id={id}
            value={value ?? ''}
            onChange={(e) => onChange(e.target.value as V)}
            aria-invalid={error ? true : undefined}
            aria-describedby={describedBy}
            className={cn(inputClass, 'h-11 appearance-none pr-10 type-body')}
          >
            {value === undefined && (
              <option value="" disabled>
                {placeholder ?? t('form.choose')}
              </option>
            )}
            {options.map((o) => (
              <option key={o.value} value={o.value}>
                {typeof o.label === 'string' ? o.label : o.value}
              </option>
            ))}
          </select>
          <ChevronDown aria-hidden className="pointer-events-none absolute top-1/2 right-3.5 size-4 -translate-y-1/2 text-label-2" />
        </div>
      </FieldFrame>
    )
  }
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error} badge={badge}>
      <button
        id={id}
        type="button"
        onClick={() => setOpen(true)}
        aria-haspopup="dialog"
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy}
        className={cn(inputClass, 'flex h-11 items-center justify-between gap-2 text-left type-body')}
      >
        <span className={cn('truncate', !current && 'text-label-3')}>{current?.label ?? placeholder ?? t('form.choose')}</span>
        <ChevronDown aria-hidden className="size-4 shrink-0 text-label-2" />
      </button>
      <Sheet open={open} onClose={() => setOpen(false)} title={label} titleVariant="small">
        <Section>
          {options.map((o) => (
            <Row
              key={o.value}
              title={o.label}
              subtitle={o.description}
              onClick={() => {
                onChange(o.value)
                setOpen(false)
              }}
              aria-current={o.value === value ? 'true' : undefined}
              trailing={o.value === value ? <Check aria-hidden className="size-5 text-accent" strokeWidth={2.5} /> : undefined}
            />
          ))}
        </Section>
      </Sheet>
    </FieldFrame>
  )
}

/** CheckList is a checkmarked list: one choice (settings) or many (multi-select fields). */
export function CheckList<V extends string>({
  options,
  selected,
  onToggle,
  label,
  multiple,
}: {
  options: Option<V>[]
  selected: V[]
  onToggle: (v: V) => void
  label?: string
  multiple?: boolean
}) {
  return (
    <div role={multiple ? 'group' : 'radiogroup'} aria-label={label}>
      {options.map((o) => {
        const on = selected.includes(o.value)
        return (
          <button
            key={o.value}
            type="button"
            role={multiple ? 'checkbox' : 'radio'}
            aria-checked={on}
            onClick={() => onToggle(o.value)}
            className="homie-row flex min-h-11 w-full items-center gap-3 px-4 py-[11px] text-left transition-colors duration-75 active:bg-[var(--row-pressed)]"
          >
            <span className="flex min-w-0 flex-1 flex-col">
              <span className="type-body">{o.label}</span>
              {o.description && <span className="type-subheadline text-label-2">{o.description}</span>}
            </span>
            <Check aria-hidden className={cn('size-5 shrink-0 text-accent', !on && 'invisible')} strokeWidth={2.5} />
          </button>
        )
      })}
    </div>
  )
}

/** Disclosure is a "Подробнее"-style row that folds content open and shut. */
export function Disclosure({ label, children, defaultOpen = false }: { label: ReactNode; children: ReactNode; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  const id = useId()
  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-11 w-full items-center gap-2 px-1 text-left type-body text-accent-text"
      >
        <span className="flex-1">{label}</span>
        <motion.span animate={{ rotate: open ? 180 : 0 }} transition={spring.snappy} className="inline-flex">
          <ChevronDown aria-hidden className="size-4" />
        </motion.span>
      </button>
      <AnimatePresence initial={false}>
        {open && (
          <motion.div
            id={id}
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: 'auto', opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={spring.smooth}
            className="overflow-hidden"
          >
            <div className="pt-1 pb-2">{children}</div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}
