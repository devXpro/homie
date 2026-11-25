import { cn, Row, Section, Sheet, useIsPhone, type Option } from '@devxpro/mcp-gatekeeper-ui'
import { Check, ChevronDown, Minus, Plus } from 'lucide-react'
import { useId, useState, type ReactNode, type TextareaHTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'

// The form controls Homie needs beyond those of @devxpro/mcp-gatekeeper-ui: TextArea, Slider, Stepper and Select, for
// the forms built from plugin schemas (lib/schema.ts). They share the package's look: the same field frame and input
// fill.

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

const inputClass =
  'w-full rounded-md bg-fill px-3.5 text-label placeholder:text-label-3 outline-none focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-accent aria-invalid:outline-2 aria-invalid:outline-offset-0 aria-invalid:outline-danger disabled:opacity-50'

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

/** Slider is a styled native range input with its value right of the label. */
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

/** Stepper is − and + around a tabular number. */
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

/** Select is a checkmarked list in a sheet on phones and a native select on larger screens. */
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
