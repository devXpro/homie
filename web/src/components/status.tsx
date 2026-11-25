import { cn, TONE_STATUS, type Tone } from '@devxpro/mcp-gatekeeper-ui'

/** StatusOrb is the 56 px glowing circle of the home's status; it breathes only while connecting (DESIGN.md §4). */
export function StatusOrb({ tone, breathing, size = 56 }: { tone: Tone; breathing?: boolean; size?: number }) {
  return (
    <span
      aria-hidden
      className={cn('homie-orb inline-block shrink-0 rounded-full', TONE_STATUS[tone], breathing && 'homie-breathe')}
      style={{ width: size, height: size }}
    />
  )
}
