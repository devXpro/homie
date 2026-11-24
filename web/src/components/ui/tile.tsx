import { Blocks, CupSoda, Fan, House, Lightbulb, Speaker, Tv, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/cn'

export type TileColor = 'blue' | 'indigo' | 'purple' | 'pink' | 'orange' | 'teal' | 'green' | 'gray' | 'red'

const TILE_BG: Record<TileColor, string> = {
  blue: 'bg-tile-blue',
  indigo: 'bg-tile-indigo',
  purple: 'bg-tile-purple',
  pink: 'bg-tile-pink',
  orange: 'bg-tile-orange',
  teal: 'bg-tile-teal',
  green: 'bg-tile-green',
  gray: 'bg-tile-gray',
  red: 'bg-danger',
}

const PALETTE: TileColor[] = ['blue', 'indigo', 'purple', 'pink', 'orange', 'teal', 'green', 'gray']

/** IconTile is a white glyph on a coloured squircle, like iOS Settings (30 px, radius 8). */
export function IconTile({ icon: Icon, color, className }: { icon: LucideIcon; color: TileColor; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn('inline-flex size-[30px] shrink-0 items-center justify-center rounded-sm text-white', TILE_BG[color], className)}
    >
      <Icon className="size-[18px]" strokeWidth={2} />
    </span>
  )
}

function fnv1a(s: string) {
  let h = 0x811c9dc5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h >>> 0
}

/** moduleLook picks a module's glyph and tile colour by plugin id (DESIGN.md §3.6). */
function moduleLook(id: string): { icon: LucideIcon; color: TileColor } {
  if (id === 'tv') return { icon: Tv, color: 'indigo' }
  if (id.startsWith('light')) return { icon: Lightbulb, color: 'orange' }
  if (id === 'kettle') return { icon: CupSoda, color: 'teal' }
  if (id === 'vacuum') return { icon: Fan, color: 'green' }
  if (id === 'speaker') return { icon: Speaker, color: 'pink' }
  return { icon: Blocks, color: PALETTE[fnv1a(id) % PALETTE.length] }
}

const MODULE_SIZE = {
  sm: { box: 'size-[30px] rounded-sm', glyph: 'size-[18px]', stroke: 2 },
  md: { box: 'size-11 rounded-md', glyph: 'size-[26px]', stroke: 2 },
  lg: { box: 'size-16 rounded-lg', glyph: 'size-9', stroke: 1.75 },
} as const

/** ModuleTile is a plugin's icon: 30 px in home rows, 44 px in lists, 64 px in the detail header. */
export function ModuleTile({ id, size = 'md', className }: { id: string; size?: keyof typeof MODULE_SIZE; className?: string }) {
  const { icon: Icon, color } = moduleLook(id)
  const s = MODULE_SIZE[size]
  return (
    <span aria-hidden className={cn('inline-flex shrink-0 items-center justify-center text-white', s.box, TILE_BG[color], className)}>
      <Icon className={s.glyph} strokeWidth={s.stroke} />
    </span>
  )
}

/** AppIcon is the Homie icon: a blue-to-indigo squircle with a white house (DESIGN.md §6). */
export function AppIcon({ size = 72, className }: { size?: number; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        'inline-flex shrink-0 items-center justify-center bg-linear-135 from-tile-blue to-tile-indigo text-white',
        className,
      )}
      style={{ width: size, height: size, borderRadius: size * 0.22 }}
    >
      <House style={{ width: size * 0.52, height: size * 0.52 }} strokeWidth={size >= 48 ? 1.9 : 2.2} />
    </span>
  )
}
