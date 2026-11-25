import { ResourceTile, tileColor, type ResourceTileProps, type TileColor } from '@devxpro/mcp-gatekeeper-ui'
import { Blocks, CupSoda, Fan, House, Lightbulb, Speaker, Tv, type LucideIcon } from 'lucide-react'

/** moduleLook picks a module's glyph and tile colour by plugin id (DESIGN.md §3.6). */
function moduleLook(id: string): { icon: LucideIcon; color: TileColor } {
  if (id === 'tv') return { icon: Tv, color: 'indigo' }
  if (id.startsWith('light')) return { icon: Lightbulb, color: 'orange' }
  if (id === 'kettle') return { icon: CupSoda, color: 'teal' }
  if (id === 'vacuum') return { icon: Fan, color: 'green' }
  if (id === 'speaker') return { icon: Speaker, color: 'pink' }
  return { icon: Blocks, color: tileColor(id) }
}

/** ModuleTile is a plugin's icon: 30 px in rows, 44 px in lists, 64 px in the detail header. */
export function ModuleTile({ id, size = 'md', className }: ResourceTileProps) {
  const look = moduleLook(id)
  return <ResourceTile id={id} size={size} className={className} icon={look.icon} color={look.color} />
}

/** HomeTile is the home's own icon on the connect page: the one MCP resource of Homie. */
export function HomeTile({ id, size = 'md', className }: ResourceTileProps) {
  return <ResourceTile id={id} size={size} className={className} icon={House} color="blue" />
}
