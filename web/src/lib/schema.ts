// JSON Schema → form (DESIGN.md §9.3): which control each property gets, the initial values, the arguments that
// are sent, and validation with @cfworker/json-schema (no eval, so the CSP holds).

import { Validator, type OutputUnit, type Schema } from '@cfworker/json-schema'
import type { TFunction } from 'i18next'
import { isSecretField, propLabel } from './tools'
import type { FieldError, JSONSchema, Lang, Tool } from './types'

export type FieldKind =
  | 'target'
  | 'segmented'
  | 'select'
  | 'ipv4'
  | 'url'
  | 'email'
  | 'secret'
  | 'textarea'
  | 'text'
  | 'slider'
  | 'stepper'
  | 'number'
  | 'switch'
  | 'multiselect'
  | 'chips'
  | 'object'
  | 'json'

export interface EnumOption {
  value: string
  label: string
}

export interface FieldSpec {
  name: string
  /** JSON Pointer of the value inside the arguments, e.g. "/percent" */
  pointer: string
  kind: FieldKind
  label: string
  hint?: string
  required: boolean
  schema: JSONSchema
  options?: EnumOption[]
  min?: number
  max?: number
  step?: number
  children?: FieldSpec[]
}

export type FormValues = Record<string, unknown>

function primaryType(s: JSONSchema): string | undefined {
  if (Array.isArray(s.type)) return s.type.find((t) => t !== 'null')
  return s.type
}

/** enumOptions reads string enums, with labels from x-enumNames or a oneOf of {const, title}. */
function enumOptions(s: JSONSchema): EnumOption[] | undefined {
  if (Array.isArray(s.enum) && s.enum.every((v) => typeof v === 'string')) {
    const names = s['x-enumNames']
    const titled = Array.isArray(s.oneOf) ? s.oneOf : undefined
    return (s.enum as string[]).map((v, i) => ({
      value: v,
      label:
        (Array.isArray(names) && typeof names[i] === 'string' ? names[i] : undefined) ??
        titled?.find((o) => o.const === v)?.title ??
        v,
    }))
  }
  if (Array.isArray(s.oneOf) && s.oneOf.length > 0 && s.oneOf.every((o) => typeof o.const === 'string')) {
    return s.oneOf.map((o) => ({ value: o.const as string, label: o.title ?? (o.const as string) }))
  }
  return undefined
}

function kindOf(name: string, s: JSONSchema, isTarget: boolean): FieldKind {
  const type = primaryType(s)
  if (isTarget) return 'target'
  if (s.$ref || s.anyOf || (s.oneOf && !enumOptions(s))) return 'json'
  if (type === 'string') {
    const opts = enumOptions(s)
    if (opts) return opts.length <= 4 && opts.every((o) => o.label.length <= 12) ? 'segmented' : 'select'
    if (s.format === 'ipv4') return 'ipv4'
    if (s.format === 'uri' || s.format === 'url') return 'url'
    if (s.format === 'email') return 'email'
    if (isSecretField(name, s)) return 'secret'
    if ((s.maxLength ?? 0) > 200 || s.format === 'textarea') return 'textarea'
    return 'text'
  }
  if (type === 'integer' || type === 'number') {
    if (typeof s.minimum === 'number' && typeof s.maximum === 'number' && s.maximum - s.minimum <= 1000 && s.maximum > s.minimum) {
      return 'slider'
    }
    return type === 'integer' ? 'stepper' : 'number'
  }
  if (type === 'boolean') return 'switch'
  if (type === 'array' && s.items && !Array.isArray(s.items) && primaryType(s.items) === 'string') {
    return enumOptions(s.items) ? 'multiselect' : 'chips'
  }
  if (type === 'object' && s.properties && Object.keys(s.properties).length > 0) return 'object'
  return 'json'
}

function escapePointer(s: string) {
  return s.replace(/~/g, '~0').replace(/\//g, '~1')
}

function fieldsOf(schema: JSONSchema, prefix: string, tool: Tool | undefined, lang: Lang, target: string | undefined, hide: string | undefined): FieldSpec[] {
  const props = schema.properties ?? {}
  const required = new Set(schema.required ?? [])
  const names = Object.keys(props).filter((n) => n !== hide)
  const ordered = [...names.filter((n) => required.has(n)), ...names.filter((n) => !required.has(n))]
  return ordered.map((name) => {
    const s = props[name] ?? {}
    const isTarget = prefix === '' && (name === target || s['x-homie-ref'] === 'inventory')
    const kind = kindOf(name, s, isTarget)
    const spec: FieldSpec = {
      name,
      pointer: `${prefix}/${escapePointer(name)}`,
      kind,
      label: propLabel(prefix === '' ? tool : undefined, name, s, lang),
      hint: s.description,
      required: required.has(name),
      schema: s,
    }
    if (kind === 'segmented' || kind === 'select') spec.options = enumOptions(s)
    if (kind === 'multiselect' && s.items && !Array.isArray(s.items)) spec.options = enumOptions(s.items)
    if (kind === 'slider' || kind === 'stepper') {
      spec.min = s.minimum
      spec.max = s.maximum
      const int = primaryType(s) === 'integer'
      spec.step = int ? 1 : (s.multipleOf ?? ((s.maximum ?? 1) - (s.minimum ?? 0)) / 100)
    }
    if (kind === 'object') spec.children = fieldsOf(s, spec.pointer, tool, lang, undefined, undefined)
    return spec
  })
}

/** buildFields lists a tool's form fields; the target is left out when the device sheet fills it in. */
export function buildFields(tool: Tool, lang: Lang, opts: { hideTarget?: boolean } = {}): FieldSpec[] {
  return fieldsOf(tool.inputSchema, '', tool, lang, tool.target, opts.hideTarget ? tool.target : undefined)
}

/** initialValues pre-fills defaults, then the given values (follow-ups, retries, a fixed target). */
export function initialValues(fields: FieldSpec[], prefill: FormValues = {}): FormValues {
  const out: FormValues = {}
  for (const f of fields) {
    const given = prefill[f.name]
    if (f.kind === 'object') {
      out[f.name] = initialValues(f.children ?? [], isRecord(given) ? given : isRecord(f.schema.default) ? f.schema.default : {})
      continue
    }
    let v: unknown = given !== undefined ? given : f.schema.default
    if (v === undefined && f.kind === 'slider' && f.required && f.min !== undefined && f.max !== undefined) {
      v = f.step === 1 ? Math.round((f.min + f.max) / 2) : (f.min + f.max) / 2
    }
    if (f.kind === 'json') {
      out[f.name] = v === undefined ? '' : JSON.stringify(v, null, 2)
    } else if (f.kind === 'number') {
      out[f.name] = v === undefined ? '' : String(v)
    } else if ((f.kind === 'chips' || f.kind === 'multiselect') && v === undefined) {
      out[f.name] = []
    } else {
      out[f.name] = v
    }
  }
  // Keep values for properties that have no visible field (a hidden target).
  for (const [k, v] of Object.entries(prefill)) {
    if (!(k in out)) out[k] = v
  }
  return out
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function isEmpty(v: unknown) {
  return v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)
}

export type FieldErrors = Record<string, string>
export type Translate = TFunction

/** buildArgs turns form values into tool arguments: empty optional fields are left out, JSON is parsed. */
export function buildArgs(fields: FieldSpec[], values: FormValues, t: Translate): { args: Record<string, unknown>; errors: FieldErrors } {
  const args: Record<string, unknown> = {}
  const errors: FieldErrors = {}
  const known = new Set(fields.map((f) => f.name))
  for (const f of fields) {
    const v = values[f.name]
    if (f.kind === 'object') {
      const inner = buildArgs(f.children ?? [], isRecord(v) ? v : {}, t)
      Object.assign(errors, inner.errors)
      if (Object.keys(inner.args).length > 0 || f.required) args[f.name] = inner.args
      continue
    }
    if (f.kind === 'json') {
      const text = typeof v === 'string' ? v.trim() : ''
      if (text === '') continue
      try {
        args[f.name] = JSON.parse(text)
      } catch {
        errors[f.pointer] = t('form.json')
      }
      continue
    }
    if (f.kind === 'number') {
      const text = typeof v === 'string' ? v.trim().replace(',', '.') : v === undefined ? '' : String(v)
      if (text === '') continue
      const n = Number(text)
      if (Number.isFinite(n)) args[f.name] = n
      else errors[f.pointer] = t('form.pattern')
      continue
    }
    if (f.kind === 'text' || f.kind === 'textarea' || f.kind === 'ipv4' || f.kind === 'url' || f.kind === 'email' || f.kind === 'target') {
      const s = typeof v === 'string' ? (f.kind === 'textarea' ? v : v.trim()) : v
      if (isEmpty(s)) continue
      args[f.name] = s
      continue
    }
    if (isEmpty(v)) continue
    args[f.name] = v
  }
  // Values without a field (a hidden target) pass through.
  for (const [k, v] of Object.entries(values)) {
    if (!known.has(k) && !isEmpty(v)) args[k] = v
  }
  return { args, errors }
}

const LEAF: Record<string, (s: JSONSchema, t: Translate) => string> = {
  minimum: (s, t) => t('form.min', { min: s.minimum }),
  exclusiveMinimum: (s, t) => t('form.min', { min: s.exclusiveMinimum }),
  maximum: (s, t) => t('form.max', { max: s.maximum }),
  exclusiveMaximum: (s, t) => t('form.max', { max: s.exclusiveMaximum }),
  minLength: (s, t) => t('form.minLength', { min: s.minLength }),
  maxLength: (s, t) => t('form.maxLength', { max: s.maxLength }),
  minItems: (s, t) => t('form.required', { min: s.minItems }),
  pattern: (_s, t) => t('form.pattern'),
  format: (_s, t) => t('form.pattern'),
  type: (_s, t) => t('form.pattern'),
  multipleOf: (_s, t) => t('form.pattern'),
  enum: (_s, t) => t('form.enum'),
  const: (_s, t) => t('form.enum'),
  required: (_s, t) => t('form.required'),
}

/** schemaAt follows a JSON Pointer through `properties`. */
export function schemaAt(root: JSONSchema, pointer: string): JSONSchema | undefined {
  let s: JSONSchema | undefined = root
  for (const part of pointer.split('/').slice(1)) {
    const key = part.replace(/~1/g, '/').replace(/~0/g, '~')
    s = s?.properties?.[key]
    if (!s) return undefined
  }
  return s
}

function requiredErrors(fields: FieldSpec[], values: FormValues, t: Translate, errors: FieldErrors) {
  for (const f of fields) {
    const v = values[f.name]
    if (f.kind === 'object') {
      requiredErrors(f.children ?? [], isRecord(v) ? v : {}, t, errors)
      continue
    }
    if (!f.required) continue
    const empty = f.kind === 'switch' ? v === undefined && f.schema.default === undefined ? false : false : isEmpty(typeof v === 'string' ? v.trim() : v)
    if (empty && !errors[f.pointer]) errors[f.pointer] = t('form.required')
  }
}

/** validate checks the form: required fields, JSON fields, then the tool's whole schema. */
export function validate(tool: Tool, fields: FieldSpec[], values: FormValues, t: Translate): { args: Record<string, unknown>; errors: FieldErrors } {
  const { args, errors } = buildArgs(fields, values, t)
  requiredErrors(fields, values, t, errors)
  let result: { valid: boolean; errors: OutputUnit[] }
  try {
    result = new Validator(tool.inputSchema as Schema, '2020-12', false).validate(args)
  } catch {
    return { args, errors }
  }
  for (const e of result.errors) {
    const make = LEAF[e.keyword]
    if (!make || e.keyword === 'required') continue
    const pointer = e.instanceLocation.replace(/^#/, '')
    if (!pointer || errors[pointer]) continue
    const s = schemaAt(tool.inputSchema, pointer) ?? {}
    errors[pointer] = make(s, t)
  }
  return { args, errors }
}

/** serverErrors maps the API's invalid_args fields onto form pointers; unknown paths come back separately. */
export function serverErrors(tool: Tool, fields: FieldSpec[], list: FieldError[], t: Translate): { errors: FieldErrors; other: string[] } {
  const pointers = new Set<string>()
  const walk = (fs: FieldSpec[]) => {
    for (const f of fs) {
      pointers.add(f.pointer)
      if (f.children) walk(f.children)
    }
  }
  walk(fields)
  const errors: FieldErrors = {}
  const other: string[] = []
  for (const fe of list) {
    const pointer = fe.path.replace(/^\/args(?=\/|$)/, '')
    const make = LEAF[fe.code]
    const s = schemaAt(tool.inputSchema, pointer) ?? {}
    const text = make ? make(s, t) : fe.message
    if (pointers.has(pointer)) errors[pointer] = errors[pointer] ?? text
    else other.push(fe.message)
  }
  return { errors, other }
}
