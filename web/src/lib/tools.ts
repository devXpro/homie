// How plugin tools become words and actions in the UI, the same for every plugin.

import type { TFunction } from 'i18next'
import type { CallProgress, JSONSchema, Lang, PluginSummary, Tool } from './types'

/** humanize turns "tv_set_input" (without the plugin prefix) or "percent" into "Set input" / "Percent". */
export function humanize(name: string, pluginId?: string) {
  let s = name
  if (pluginId && s.startsWith(`${pluginId}_`)) s = s.slice(pluginId.length + 1)
  s = s.replace(/([a-z0-9])([A-Z])/g, '$1 $2').replace(/[_-]+/g, ' ').trim().toLowerCase()
  return s.charAt(0).toUpperCase() + s.slice(1)
}

export function toolTitle(tool: Pick<Tool, 'name' | 'pluginId' | 'title' | 'i18n'>, lang: Lang) {
  return tool.i18n?.[lang]?.title || tool.title || humanize(tool.name, tool.pluginId)
}

export function toolDescription(tool: Pick<Tool, 'description' | 'i18n'>, lang: Lang) {
  return tool.i18n?.[lang]?.description || tool.description
}

export function propLabel(tool: Pick<Tool, 'i18n'> | undefined, prop: string, schema: JSONSchema, lang: Lang) {
  return tool?.i18n?.[lang]?.properties?.[prop] || schema.title || humanize(prop)
}

export function isDestructive(tool: Pick<Tool, 'annotations'>) {
  return tool.annotations?.destructiveHint === true
}

export function progressMessage(p: CallProgress | undefined, lang: Lang) {
  if (!p) return undefined
  return p.messageI18n?.[lang] || p.message || undefined
}

export interface ToolGroups {
  inventory?: Tool
  setup?: Tool
  moduleActions: Tool[]
  itemActions: Tool[]
  assistant: Tool[]
}

function byTitle(lang: Lang) {
  return (a: Tool, b: Tool) => toolTitle(a, lang).localeCompare(toolTitle(b, lang), lang)
}

/** orderActions puts the setup action first, then the others by title, destructive ones last. */
function orderActions(tools: Tool[], lang: Lang, setup?: Tool) {
  const rest = tools.filter((t) => t !== setup)
  const safe = rest.filter((t) => !isDestructive(t)).sort(byTitle(lang))
  const danger = rest.filter(isDestructive).sort(byTitle(lang))
  return [...(setup ? [setup] : []), ...safe, ...danger]
}

export function groupTools(tools: Tool[], lang: Lang): ToolGroups {
  const inventory = tools.find((t) => t.inventory)
  const admin = tools.filter((t) => t.audience === 'admin' && !t.inventory)
  const itemActions = admin.filter((t) => t.target)
  const moduleActions = admin.filter((t) => !t.target)
  const names = [...moduleActions].sort((a, b) => a.name.localeCompare(b.name))
  const setup = names.find((t) => t.name.endsWith('_discover')) ?? names.find((t) => t.name.endsWith('_add'))
  return {
    inventory,
    setup,
    moduleActions: orderActions(moduleActions, lang, setup),
    itemActions: orderActions(itemActions, lang),
    assistant: tools.filter((t) => t.audience === 'assistant').sort(byTitle(lang)),
  }
}

/** fieldCount counts the inputs a tool asks for; zero means it runs as soon as it opens. */
export function fieldCount(tool: Tool, hiddenTarget?: boolean) {
  const props = Object.keys(tool.inputSchema.properties ?? {})
  return props.filter((p) => !(hiddenTarget && p === tool.target)).length
}

const FIRST_PARTY = new Set(['tv'])

/** moduleTitle is the plugin's own title; first-party plugins get the admin's own name for them (module.name.<id>). */
export function moduleTitle(plugin: Pick<PluginSummary, 'id' | 'title'>, t: TFunction) {
  return FIRST_PARTY.has(plugin.id) ? t(`module.name.${plugin.id}`) : plugin.title || plugin.id
}

const SECRET_NAME = /(token|key|secret|password|pin|code)/i

export function isSecretField(name: string, schema: JSONSchema) {
  return schema.writeOnly === true || (SECRET_NAME.test(name) && schema.type === 'string')
}
