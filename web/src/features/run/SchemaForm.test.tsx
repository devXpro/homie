import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import i18n from '@/i18n'
import { buildFields, initialValues, validate, type FieldErrors, type FormValues } from '@/lib/schema'
import type { InventoryItem, JSONSchema, Tool } from '@/lib/types'
import { SchemaForm } from './SchemaForm'

const t = i18n.t.bind(i18n)

function tool(inputSchema: JSONSchema, over: Partial<Tool> = {}): Tool {
  return { name: 'tv_do', pluginId: 'tv', title: 'Do', description: '', audience: 'admin', inputSchema, timeoutMs: 30_000, inventory: false, ...over }
}

const inventory: InventoryItem[] = [
  { id: 'tv', name: 'Телевизор в гостиной', subtitle: '192.168.68.66', state: 'on' },
  { id: 'tv-2', name: 'Спальня', state: 'off' },
]

/** Harness renders the form like the run sheet does: values in state, validation on submit and on blur. */
function Harness({ tl, prefill, onArgs }: { tl: Tool; prefill?: FormValues; onArgs?: (a: Record<string, unknown>) => void }) {
  const fields = buildFields(tl, 'ru')
  const [values, setValues] = useState(() => initialValues(fields, prefill))
  const [errors, setErrors] = useState<FieldErrors>({})
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        const v = validate(tl, fields, values, t)
        setErrors(v.errors)
        if (Object.keys(v.errors).length === 0) onArgs?.(v.args)
      }}
    >
      <SchemaForm
        fields={fields}
        values={values}
        errors={errors}
        onChange={setValues}
        onBlur={(p) => {
          const v = validate(tl, fields, values, t)
          setErrors((old) => ({ ...Object.fromEntries(Object.entries(old).filter(([k]) => k !== p)), ...(v.errors[p] ? { [p]: v.errors[p] } : {}) }))
        }}
        inventory={inventory}
      />
      <button type="submit">Run</button>
    </form>
  )
}

describe('SchemaForm', () => {
  it('renders homie-tv volume: device picker, slider with its value, Russian labels', async () => {
    const user = userEvent.setup()
    let sent: Record<string, unknown> | undefined
    const tl = tool(
      {
        type: 'object',
        properties: {
          tv: { type: 'string', 'x-homie-ref': 'inventory', description: "The TV's id or name." },
          percent: { type: 'integer', minimum: 0, maximum: 100 },
        },
        required: ['percent'],
      },
      { target: 'tv', i18n: { ru: { properties: { tv: 'Телевизор', percent: 'Громкость, %' } } } },
    )
    render(<Harness tl={tl} onArgs={(a) => (sent = a)} />)
    const slider = screen.getByRole('slider', { name: 'Громкость, %' })
    expect(slider).toHaveValue('50')
    expect(screen.getByText('50')).toBeInTheDocument()
    const picker = screen.getByRole('combobox', { name: /Телевизор/ })
    expect(within(picker).getByRole('option', { name: 'Телевизор в гостиной' })).toBeInTheDocument()
    expect(within(picker).getByRole('option', { name: 'Другое…' })).toBeInTheDocument()
    expect(screen.getByText('необязательно')).toBeInTheDocument()
    await user.selectOptions(picker, 'tv-2')
    await user.click(screen.getByRole('button', { name: 'Run' }))
    expect(sent).toEqual({ tv: 'tv-2', percent: 50 })
  })

  it('"Другое…" opens a free field for a device that is not listed', async () => {
    const user = userEvent.setup()
    let sent: Record<string, unknown> | undefined
    const tl = tool({ type: 'object', properties: { tv: { type: 'string', 'x-homie-ref': 'inventory' } }, required: ['tv'] }, { target: 'tv' })
    render(<Harness tl={tl} onArgs={(a) => (sent = a)} />)
    await user.selectOptions(screen.getByRole('combobox'), '\u0000other')
    await user.type(screen.getByRole('textbox', { name: 'Своё значение' }), 'Kitchen TV')
    await user.click(screen.getByRole('button', { name: 'Run' }))
    expect(sent).toEqual({ tv: 'Kitchen TV' })
  })

  it('shows required and range errors under the fields, on submit and on blur', async () => {
    const user = userEvent.setup()
    const tl = tool({
      type: 'object',
      properties: {
        host: { type: 'string', format: 'ipv4', description: "The TV's IPv4 address." },
        id: { type: 'string', pattern: '^[a-z][a-z0-9-]{0,31}$' },
      },
      required: ['host'],
    }, { i18n: { ru: { properties: { host: 'IP-адрес', id: 'Короткое имя' } } } })
    render(<Harness tl={tl} />)
    await user.click(screen.getByRole('button', { name: 'Run' }))
    const host = screen.getByRole('textbox', { name: 'IP-адрес' })
    expect(host).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('alert')).toHaveTextContent('Заполните это поле')
    await user.type(host, '192.168.68.66')
    await user.type(screen.getByRole('textbox', { name: /Короткое имя/ }), 'Bad Id')
    await user.tab()
    expect(screen.getByRole('alert')).toHaveTextContent('Неверный формат')
    expect(host).not.toHaveAttribute('aria-invalid')
  })

  it('short enums are a segmented control, long ones a select', async () => {
    const user = userEvent.setup()
    let sent: Record<string, unknown> | undefined
    const tl = tool({
      type: 'object',
      properties: {
        input: { type: 'string', enum: ['tv', 'hdmi1', 'hdmi2'] },
        app: { type: 'string', enum: ['netflix', 'youtube', 'kinopoisk', 'ivi', 'okko'] },
      },
      required: ['input', 'app'],
    })
    render(<Harness tl={tl} onArgs={(a) => (sent = a)} />)
    const group = screen.getByRole('radiogroup', { name: 'Input' })
    await user.click(within(group).getByRole('radio', { name: 'hdmi1' }))
    expect(within(group).getByRole('radio', { name: 'hdmi1' })).toHaveAttribute('aria-checked', 'true')
    await user.keyboard('{ArrowRight}')
    expect(within(group).getByRole('radio', { name: 'hdmi2' })).toHaveAttribute('aria-checked', 'true')
    await user.selectOptions(screen.getByRole('combobox', { name: 'App' }), 'ivi')
    await user.click(screen.getByRole('button', { name: 'Run' }))
    expect(sent).toEqual({ input: 'hdmi2', app: 'ivi' })
  })

  it('secrets are password fields with a show toggle', async () => {
    const user = userEvent.setup()
    render(<Harness tl={tool({ type: 'object', properties: { pin: { type: 'string' } } })} />)
    const field = screen.getByLabelText('Pin')
    expect(field).toHaveAttribute('type', 'password')
    await user.click(screen.getByRole('button', { name: 'Показать' }))
    expect(field).toHaveAttribute('type', 'text')
  })

  it('booleans are switches, string arrays chips, enum arrays checklists, empty ones left out', async () => {
    const user = userEvent.setup()
    let sent: Record<string, unknown> | undefined
    const tl = tool({
      type: 'object',
      properties: {
        on: { type: 'boolean' },
        tags: { type: 'array', items: { type: 'string' } },
        days: { type: 'array', items: { type: 'string', enum: ['mon', 'tue'] } },
      },
    })
    render(<Harness tl={tl} onArgs={(a) => (sent = a)} />)
    // Three optional fields fold under "Ещё параметры".
    await user.click(screen.getByRole('button', { name: 'Ещё параметры' }))
    await user.click(screen.getByRole('switch', { name: 'On' }))
    await user.type(screen.getByRole('textbox', { name: /Tags/ }), 'kitchen{Enter}')
    expect(screen.getByRole('button', { name: 'Убрать kitchen' })).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: 'tue' }))
    await user.click(screen.getByRole('button', { name: 'Run' }))
    expect(sent).toEqual({ on: true, tags: ['kitchen'], days: ['tue'] })
  })

  it('edits anything else as JSON with a live parse error', async () => {
    const user = userEvent.setup()
    render(<Harness tl={tool({ type: 'object', properties: { rule: { anyOf: [{ type: 'string' }, { type: 'object' }] } } })} />)
    expect(screen.getByText('JSON')).toBeInTheDocument()
    await user.type(screen.getByRole('textbox', { name: /Rule/ }), '{{"a":')
    expect(screen.getByRole('alert')).toHaveTextContent('Неверный JSON')
  })

  it('nested objects get their own section', async () => {
    const user = userEvent.setup()
    let sent: Record<string, unknown> | undefined
    const tl = tool({
      type: 'object',
      properties: { wifi: { type: 'object', title: 'Wi-Fi', properties: { ssid: { type: 'string' } }, required: ['ssid'] } },
      required: ['wifi'],
    })
    render(<Harness tl={tl} onArgs={(a) => (sent = a)} />)
    expect(screen.getByRole('heading', { name: 'Wi-Fi' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Run' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Заполните это поле')
    await user.type(screen.getByRole('textbox', { name: 'Ssid' }), 'home')
    await user.click(screen.getByRole('button', { name: 'Run' }))
    expect(sent).toEqual({ wifi: { ssid: 'home' } })
  })
})
