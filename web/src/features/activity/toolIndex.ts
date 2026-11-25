import { currentLang, useMe } from '@devxpro/mcp-gatekeeper-ui'
import { useMemo } from 'react'
import { useAllTools } from '@/lib/queries'
import { humanize, toolTitle } from '@/lib/tools'
import type { ActivityItem, Tool } from '@/lib/types'

/** useToolIndex maps tool names to tools, for translated titles of past calls (owners read the tool list). */
export function useToolIndex() {
  const owner = useMe().data?.person.role === 'owner'
  const tools = useAllTools(owner)
  return useMemo(() => new Map((tools.data?.items ?? []).map((t) => [t.name, t] as const)), [tools.data])
}

/** activityTitle is a call's title: the tool's title in the UI language, else what the log kept. */
export function activityTitle(item: ActivityItem, tools: Map<string, Tool>) {
  const tool = tools.get(item.tool)
  if (tool) return toolTitle(tool, currentLang())
  return item.toolTitle ?? humanize(item.tool, item.pluginId)
}
