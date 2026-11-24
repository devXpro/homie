import { describe, expect, it } from 'vitest'
import i18n from '@/i18n'
import { buildArgs, buildFields, initialValues, serverErrors, validate } from './schema'
import { groupTools } from './tools'
import type { JSONSchema, Tool } from './types'

const t = i18n.t.bind(i18n)

function tool(inputSchema: JSONSchema, over: Partial<Tool> = {}): Tool {
  return {
    name: 'x_do',
    pluginId: 'x',
    title: 'Do',
    description: '',
    audience: 'admin',
    inputSchema,
    timeoutMs: 30_000,
    inventory: false,
    ...over,
  }
}

describe('buildFields: JSON Schema → controls (DESIGN.md §9.3)', () => {
  const schema: JSONSchema = {
    type: 'object',
    properties: {
      note: { type: 'string', maxLength: 500 },
      tv: { type: 'string', 'x-homie-ref': 'inventory' },
      input: { type: 'string', enum: ['tv', 'hdmi1', 'hdmi2', 'hdmi3'] },
      mode: { type: 'string', enum: ['a', 'b', 'c', 'd', 'e'] },
      long: { type: 'string', enum: ['short', 'a-very-long-option-name'] },
      named: { type: 'string', enum: ['h1', 'h2'], 'x-enumNames': ['HDMI 1', 'HDMI 2'] },
      titled: { type: 'string', oneOf: [{ const: 'on', title: 'On' }, { const: 'off', title: 'Off' }] },
      host: { type: 'string', format: 'ipv4' },
      url: { type: 'string', format: 'uri' },
      mail: { type: 'string', format: 'email' },
      pin: { type: 'string' },
      apiKey: { type: 'string' },
      blob: { type: 'string', writeOnly: true },
      name: { type: 'string', minLength: 1 },
      percent: { type: 'integer', minimum: 0, maximum: 100 },
      count: { type: 'integer' },
      ratio: { type: 'number' },
      fine: { type: 'number', minimum: 0, maximum: 1 },
      on: { type: 'boolean' },
      days: { type: 'array', items: { type: 'string', enum: ['mon', 'tue'] } },
      tags: { type: 'array', items: { type: 'string' } },
      nested: { type: 'object', properties: { a: { type: 'string' } }, required: ['a'] },
      anyThing: { anyOf: [{ type: 'string' }, { type: 'number' }] },
      ref: { $ref: '#/$defs/x' },
      untyped: {},
      rows: { type: 'array', items: { type: 'object' } },
    },
    required: ['percent', 'name'],
  }
  const fields = buildFields(tool(schema, { target: 'tv' }), 'en')
  const kind = (name: string) => fields.find((f) => f.name === name)?.kind

  it.each([
    ['note', 'textarea'],
    ['tv', 'target'],
    ['input', 'segmented'],
    ['mode', 'select'],
    ['long', 'select'],
    ['named', 'segmented'],
    ['titled', 'segmented'],
    ['host', 'ipv4'],
    ['url', 'url'],
    ['mail', 'email'],
    ['pin', 'secret'],
    ['apiKey', 'secret'],
    ['blob', 'secret'],
    ['name', 'text'],
    ['percent', 'slider'],
    ['count', 'stepper'],
    ['ratio', 'number'],
    ['fine', 'slider'],
    ['on', 'switch'],
    ['days', 'multiselect'],
    ['tags', 'chips'],
    ['nested', 'object'],
    ['anyThing', 'json'],
    ['ref', 'json'],
    ['untyped', 'json'],
    ['rows', 'json'],
  ])('%s → %s', (name, want) => {
    expect(kind(name)).toBe(want)
  })

  it('puts required fields first and keeps the received order otherwise', () => {
    expect(fields.slice(0, 2).map((f) => f.name)).toEqual(['name', 'percent'])
    expect(fields[2].name).toBe('note')
  })

  it('labels enums from x-enumNames and oneOf titles', () => {
    expect(fields.find((f) => f.name === 'named')?.options).toEqual([
      { value: 'h1', label: 'HDMI 1' },
      { value: 'h2', label: 'HDMI 2' },
    ])
    expect(fields.find((f) => f.name === 'titled')?.options?.map((o) => o.label)).toEqual(['On', 'Off'])
  })

  it('steps sliders by 1 for integers and by multipleOf or range/100 for numbers', () => {
    expect(fields.find((f) => f.name === 'percent')?.step).toBe(1)
    expect(fields.find((f) => f.name === 'fine')?.step).toBe(0.01)
    const stepped = buildFields(tool({ type: 'object', properties: { v: { type: 'number', minimum: 0, maximum: 10, multipleOf: 0.5 } } }), 'en')
    expect(stepped[0].step).toBe(0.5)
  })

  it('uses translated labels when the language matches, else title, else the humanized name', () => {
    const tl = tool(
      { type: 'object', properties: { percent: { type: 'integer' }, host_name: { type: 'string' }, t: { type: 'string', title: 'Titled' } } },
      { i18n: { ru: { properties: { percent: 'Громкость, %' } } } },
    )
    expect(buildFields(tl, 'ru').map((f) => f.label)).toEqual(['Громкость, %', 'Host name', 'Titled'])
    expect(buildFields(tl, 'en')[0].label).toBe('Percent')
  })

  it('hides the target in the device sheet', () => {
    const tl = tool({ type: 'object', properties: { tv: { type: 'string' }, name: { type: 'string' } }, required: ['tv', 'name'] }, { target: 'tv' })
    expect(buildFields(tl, 'en', { hideTarget: true }).map((f) => f.name)).toEqual(['name'])
  })
})

describe('initialValues and buildArgs', () => {
  const tl = tool({
    type: 'object',
    properties: {
      percent: { type: 'integer', minimum: 0, maximum: 100 },
      name: { type: 'string', default: 'Living room' },
      note: { type: 'string' },
      ratio: { type: 'number' },
      raw: { oneOf: [{ type: 'string' }, { type: 'object' }] },
      tags: { type: 'array', items: { type: 'string' } },
      on: { type: 'boolean' },
    },
    required: ['percent'],
  })
  const fields = buildFields(tl, 'en')

  it('pre-fills defaults, the slider middle for a required range, and the given values', () => {
    const v = initialValues(fields, { tv: 'tv' })
    expect(v).toMatchObject({ percent: 50, name: 'Living room', note: undefined, ratio: '', raw: '', tags: [], tv: 'tv' })
  })

  it('leaves empty optional fields out and parses numbers and JSON', () => {
    const { args, errors } = buildArgs(fields, { percent: 30, name: '  Kitchen ', note: '', ratio: '0,5', raw: '{"a":1}', tags: [], on: undefined, tv: 'tv' }, t)
    expect(errors).toEqual({})
    expect(args).toEqual({ percent: 30, name: 'Kitchen', ratio: 0.5, raw: { a: 1 }, tv: 'tv' })
  })

  it('reports broken JSON and numbers on their own fields', () => {
    const { errors } = buildArgs(fields, { percent: 1, raw: '{oops', ratio: 'abc' }, t)
    expect(errors).toEqual({ '/raw': 'Неверный JSON', '/ratio': 'Неверный формат' })
  })
})

describe('validate (no eval: @cfworker/json-schema)', () => {
  const tl = tool({
    type: 'object',
    properties: {
      percent: { type: 'integer', minimum: 0, maximum: 100 },
      host: { type: 'string', format: 'ipv4' },
      id: { type: 'string', pattern: '^[a-z][a-z0-9-]{0,31}$' },
      name: { type: 'string', minLength: 2, maxLength: 5 },
      input: { type: 'string', enum: ['tv', 'hdmi1'] },
    },
    required: ['host'],
  })
  const fields = buildFields(tl, 'ru')

  it('accepts valid values', () => {
    expect(validate(tl, fields, { percent: 40, host: '192.168.68.66', id: 'tv-2', name: 'abc', input: 'tv' }, t).errors).toEqual({})
  })

  it('maps every keyword to the field words of DESIGN.md §9.3', () => {
    const { errors } = validate(tl, fields, { percent: 140, host: '', id: 'Bad id', name: 'a', input: 'vga' }, t)
    expect(errors).toEqual({
      '/host': 'Заполните это поле',
      '/percent': 'Не больше 100',
      '/id': 'Неверный формат',
      '/name': 'Не короче 2 символов',
      '/input': 'Выберите один из вариантов',
    })
  })

  it('checks formats', () => {
    expect(validate(tl, fields, { host: '999.1.1.1' }, t).errors).toEqual({ '/host': 'Неверный формат' })
  })

  it('maps server invalid_args onto fields, and unknown paths above the button', () => {
    const mapped = serverErrors(tl, fields, [
      { path: '/args/percent', code: 'maximum', message: 'must be at most 100' },
      { path: '/host', code: 'format', message: 'bad ip' },
      { path: '/elsewhere', code: 'x', message: 'Something else is wrong.' },
    ], t)
    expect(mapped.errors).toEqual({ '/percent': 'Не больше 100', '/host': 'Неверный формат' })
    expect(mapped.other).toEqual(['Something else is wrong.'])
  })
})

describe('groupTools (DESIGN.md §9.2)', () => {
  const mk = (name: string, over: Partial<Tool> = {}) => tool({ type: 'object', properties: {} }, { name, title: name, pluginId: 'tv', ...over })
  const groups = groupTools(
    [
      mk('tv_list', { inventory: true, audience: 'assistant' }),
      mk('tv_power_on', { audience: 'assistant' }),
      mk('tv_rename', { target: 'tv' }),
      mk('tv_remove', { target: 'tv', annotations: { destructiveHint: true } }),
      mk('tv_pair', { target: 'tv' }),
      mk('tv_add'),
      mk('tv_discover'),
      mk('tv_wipe', { annotations: { destructiveHint: true } }),
      mk('tv_backup'),
    ],
    'en',
  )

  it('finds the inventory and the setup action (_discover before _add)', () => {
    expect(groups.inventory?.name).toBe('tv_list')
    expect(groups.setup?.name).toBe('tv_discover')
  })

  it('orders actions: setup, then by title, destructive last', () => {
    expect(groups.moduleActions.map((x) => x.name)).toEqual(['tv_discover', 'tv_add', 'tv_backup', 'tv_wipe'])
    expect(groups.itemActions.map((x) => x.name)).toEqual(['tv_pair', 'tv_rename', 'tv_remove'])
  })

  it('lists assistant tools apart (the inventory tool included)', () => {
    expect(groups.assistant.map((x) => x.name)).toEqual(['tv_list', 'tv_power_on'])
  })
})
