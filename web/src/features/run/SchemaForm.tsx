import { Badge, CheckList, cn, Section, SegmentedControl, spring, SwitchRow, TextField } from '@devxpro/mcp-gatekeeper-ui'
import { ChevronDown, X } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import { useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Select, Slider, Stepper, TextArea } from '@/components/controls'
import type { FieldErrors, FieldSpec, FormValues } from '@/lib/schema'
import type { InventoryItem } from '@/lib/types'

// SchemaForm renders a tool's fields by the rules of DESIGN.md §9.3. The fields and their validation come from
// lib/schema.ts; this file only draws them.

export interface SchemaFormProps {
  fields: FieldSpec[]
  values: FormValues
  errors: FieldErrors
  onChange: (values: FormValues) => void
  /** validate one field when it loses focus */
  onBlur?: (pointer: string) => void
  /** the module's devices, for target fields ("x-homie-ref": "inventory") */
  inventory?: InventoryItem[]
}

export function SchemaForm({ fields, values, errors, onChange, onBlur, inventory }: SchemaFormProps) {
  const { t } = useTranslation()
  const [more, setMore] = useState(false)
  const moreId = useId()
  const required = fields.filter((f) => f.required)
  const optional = fields.filter((f) => !f.required)
  // More than 2 optional fields fold under "Ещё параметры" (unless one of them already has a value or an error).
  const fold = optional.length > 2 && !optional.some((f) => errors[f.pointer])
  const set = (name: string, v: unknown) => onChange({ ...values, [name]: v })
  const render = (f: FieldSpec) => (
    <Field key={f.pointer} field={f} value={values[f.name]} errors={errors} onChange={(v) => set(f.name, v)} onBlur={onBlur} inventory={inventory} />
  )
  return (
    <div className="flex flex-col gap-5">
      {(fold ? required : fields).map(render)}
      {fold && (
        <div>
          <button
            type="button"
            aria-expanded={more}
            aria-controls={moreId}
            onClick={() => setMore((m) => !m)}
            className="flex min-h-11 w-full items-center gap-2 px-1 text-left type-body text-accent-text"
          >
            <span className="flex-1">{t('form.more')}</span>
            <motion.span animate={{ rotate: more ? 180 : 0 }} transition={spring.snappy} className="inline-flex">
              <ChevronDown aria-hidden className="size-4" />
            </motion.span>
          </button>
          <AnimatePresence initial={false}>
            {more && (
              <motion.div
                id={moreId}
                initial={{ height: 0, opacity: 0 }}
                animate={{ height: 'auto', opacity: 1 }}
                exit={{ height: 0, opacity: 0 }}
                transition={spring.smooth}
                className="overflow-hidden"
              >
                <div className="flex flex-col gap-5 pt-3">{optional.map(render)}</div>
              </motion.div>
            )}
          </AnimatePresence>
        </div>
      )}
    </div>
  )
}

function Field({
  field: f,
  value,
  errors,
  onChange,
  onBlur,
  inventory,
}: {
  field: FieldSpec
  value: unknown
  errors: FieldErrors
  onChange: (v: unknown) => void
  onBlur?: (pointer: string) => void
  inventory?: InventoryItem[]
}) {
  const { t } = useTranslation()
  const error = errors[f.pointer]
  const badge = f.kind === 'json' ? <Badge>{t('form.json.badge')}</Badge> : !f.required ? <Badge>{t('common.optional')}</Badge> : undefined
  const common = { label: f.label, hint: f.hint, error, badge }
  const blur = () => onBlur?.(f.pointer)
  const str = typeof value === 'string' ? value : value === undefined || value === null ? '' : String(value)

  switch (f.kind) {
    case 'target':
      return <TargetField field={f} value={str} onChange={onChange} error={error} badge={badge} inventory={inventory} onBlur={blur} />
    case 'segmented':
      return (
        <FieldGroup label={f.label} hint={f.hint} error={error} badge={badge}>
          <SegmentedControl
            label={f.label}
            value={str}
            onChange={(v) => {
              onChange(v)
            }}
            options={(f.options ?? []).map((o) => ({ value: o.value, label: o.label }))}
          />
        </FieldGroup>
      )
    case 'select':
      return <Select {...common} value={str || undefined} onChange={onChange} options={(f.options ?? []).map((o) => ({ value: o.value, label: o.label }))} />
    case 'ipv4':
      return <TextField {...common} value={str} onChange={(e) => onChange(e.target.value)} onBlur={blur} inputMode="decimal" mono autoComplete="off" spellCheck={false} placeholder="192.0.2.20" />
    case 'url':
      return <TextField {...common} value={str} onChange={(e) => onChange(e.target.value)} onBlur={blur} type="url" autoComplete="off" spellCheck={false} />
    case 'email':
      return <TextField {...common} value={str} onChange={(e) => onChange(e.target.value)} onBlur={blur} type="email" autoComplete="off" />
    case 'secret':
      return <TextField {...common} value={str} onChange={(e) => onChange(e.target.value)} onBlur={blur} secret autoComplete="off" spellCheck={false} />
    case 'textarea':
      return <TextArea {...common} value={str} onChange={(e) => onChange(e.target.value)} onBlur={blur} rows={4} />
    case 'text':
      return (
        <TextField
          {...common}
          value={str}
          onChange={(e) => onChange(e.target.value)}
          onBlur={blur}
          minLength={f.schema.minLength}
          maxLength={f.schema.maxLength}
          autoComplete="off"
        />
      )
    case 'slider': {
      const num = typeof value === 'number' ? value : (f.min ?? 0)
      return (
        <Slider
          {...common}
          value={num}
          onChange={onChange}
          min={f.min ?? 0}
          max={f.max ?? 100}
          step={f.step ?? 1}
          format={(v) => (value === undefined ? '—' : String(v))}
        />
      )
    }
    case 'stepper':
      return <Stepper {...common} value={typeof value === 'number' ? value : undefined} onChange={onChange} min={f.min} max={f.max} />
    case 'number':
      return <TextField {...common} value={str} onChange={(e) => onChange(e.target.value)} onBlur={blur} inputMode="decimal" className="tabular-nums" />
    case 'switch':
      return (
        <FieldGroup hint={f.hint} error={error}>
          <Section>
            <SwitchRow title={f.label} checked={value === true} onChange={onChange} />
          </Section>
        </FieldGroup>
      )
    case 'multiselect': {
      const selected = Array.isArray(value) ? (value as string[]) : []
      return (
        <FieldGroup label={f.label} hint={f.hint} error={error} badge={badge}>
          <Section>
            <CheckList
              multiple
              label={f.label}
              options={(f.options ?? []).map((o) => ({ value: o.value, label: o.label }))}
              selected={selected}
              onToggle={(v) => onChange(selected.includes(v) ? selected.filter((x) => x !== v) : [...selected, v])}
            />
          </Section>
        </FieldGroup>
      )
    }
    case 'chips':
      return <ChipsField field={f} value={Array.isArray(value) ? (value as string[]) : []} onChange={onChange} error={error} badge={badge} />
    case 'object':
      return (
        <section className="flex flex-col gap-4 rounded-lg bg-fill/40 p-3 ring-[0.5px] ring-separator">
          <h3 className="px-1 type-footnote font-semibold text-label-2">{f.label}</h3>
          {(f.children ?? []).map((c) => (
            <Field
              key={c.pointer}
              field={c}
              value={(value as Record<string, unknown> | undefined)?.[c.name]}
              errors={errors}
              onChange={(v) => onChange({ ...((value as Record<string, unknown> | undefined) ?? {}), [c.name]: v })}
              onBlur={onBlur}
            />
          ))}
        </section>
      )
    case 'json':
      return <JsonField field={f} value={str} onChange={onChange} error={error} badge={badge} onBlur={blur} />
  }
}

/** FieldGroup gives a label, hint and error to controls that are not a single input. */
function FieldGroup({ label, hint, error, badge, children }: { label?: string; hint?: string; error?: string; badge?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-col">
      {label && (
        <div className="flex items-baseline gap-2 px-1 pb-1.5">
          <span className="type-footnote text-label-2">{label}</span>
          {badge}
        </div>
      )}
      {children}
      {error ? (
        <p role="alert" className="px-1 pt-1.5 type-footnote text-danger-text">
          {error}
        </p>
      ) : (
        hint && <p className="px-1 pt-1.5 type-footnote text-label-2">{hint}</p>
      )}
    </div>
  )
}

const OTHER = '\u0000other'

/** TargetField picks one of the module's devices, with "Другое…" for a free value (DESIGN.md §9.3). */
function TargetField({
  field: f,
  value,
  onChange,
  error,
  badge,
  inventory = [],
  onBlur,
}: {
  field: FieldSpec
  value: string
  onChange: (v: unknown) => void
  error?: string
  badge?: ReactNode
  inventory?: InventoryItem[]
  onBlur: () => void
}) {
  const { t } = useTranslation()
  const known = inventory.some((i) => i.id === value)
  const [other, setOther] = useState(!known && value !== '')
  const options = [
    ...inventory.map((i) => ({ value: i.id, label: i.name, description: i.subtitle })),
    { value: OTHER, label: t('form.other') },
  ]
  return (
    <div className="flex flex-col gap-3">
      <Select
        label={f.label}
        hint={other ? undefined : f.hint}
        error={other ? undefined : error}
        badge={badge}
        value={other ? OTHER : value || undefined}
        onChange={(v) => {
          if (v === OTHER) {
            setOther(true)
            onChange('')
          } else {
            setOther(false)
            onChange(v)
          }
        }}
        options={options}
      />
      {other && (
        <TextField
          label={t('form.other.label')}
          hint={f.hint}
          error={error}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur}
          autoComplete="off"
          spellCheck={false}
        />
      )}
    </div>
  )
}

function ChipsField({
  field: f,
  value,
  onChange,
  error,
  badge,
}: {
  field: FieldSpec
  value: string[]
  onChange: (v: unknown) => void
  error?: string
  badge?: ReactNode
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState('')
  const add = () => {
    const v = draft.trim()
    if (v && !value.includes(v)) onChange([...value, v])
    setDraft('')
  }
  return (
    <FieldGroup label={f.label} hint={f.hint} error={error} badge={badge}>
      {value.length > 0 && (
        <ul className="mb-2 flex flex-wrap gap-1.5">
          {value.map((v) => (
            <li key={v} className="inline-flex h-8 items-center gap-1 rounded-full bg-fill pr-1 pl-3 text-[15px]">
              {v}
              <button
                type="button"
                onClick={() => onChange(value.filter((x) => x !== v))}
                aria-label={`${t('common.remove')} ${v}`}
                className="inline-flex size-6 items-center justify-center rounded-full text-label-2 hover:bg-fill"
              >
                <X aria-hidden className="size-3.5" strokeWidth={2.5} />
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex gap-2">
        <input
          aria-label={`${f.label}: ${t('form.add_value')}`}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add()
            }
          }}
          className="h-11 min-w-0 flex-1 rounded-md bg-fill px-3.5 type-body outline-none placeholder:text-label-3 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-accent"
          placeholder={t('form.add_value')}
        />
        <button type="button" onClick={add} className="h-11 rounded-md bg-accent-tint px-4 type-headline text-accent-tinted">
          {t('common.add')}
        </button>
      </div>
    </FieldGroup>
  )
}

/** JsonField edits a property the form has no control for, with live parse errors. */
function JsonField({
  field: f,
  value,
  onChange,
  error,
  badge,
  onBlur,
}: {
  field: FieldSpec
  value: string
  onChange: (v: unknown) => void
  error?: string
  badge?: ReactNode
  onBlur: () => void
}) {
  const { t } = useTranslation()
  let parseError: string | undefined
  if (value.trim()) {
    try {
      JSON.parse(value)
    } catch {
      parseError = t('form.json')
    }
  }
  return (
    <TextArea
      label={f.label}
      hint={f.hint}
      badge={badge}
      error={parseError ?? error}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      onBlur={onBlur}
      mono
      rows={5}
      spellCheck={false}
      className={cn('whitespace-pre')}
    />
  )
}
