import { ChevronRight } from 'lucide-react'
import type { ComponentProps, ReactNode } from 'react'
import { Link } from 'react-router'
import { cn } from '@/lib/cn'
import { useSurface } from './surface'

// Grouped lists (DESIGN.md §6): a Section is an optional header, a rounded card of Rows, and an optional footer.

export function Section({
  header,
  footer,
  children,
  className,
  cardClassName,
  id,
}: {
  header?: ReactNode
  footer?: ReactNode
  children: ReactNode
  className?: string
  cardClassName?: string
  id?: string
}) {
  const surface = useSurface()
  return (
    <section className={cn('mt-8 first:mt-0', className)} aria-labelledby={header && id ? `${id}-h` : undefined}>
      {header && (
        <h2 id={id ? `${id}-h` : undefined} className="px-4 pb-2 type-footnote text-label-2">
          {header}
        </h2>
      )}
      <div
        className={cn(
          'overflow-hidden rounded-lg',
          surface === 'sheet' ? 'bg-sheet-card [--row-pressed:var(--sheet-pressed)]' : 'bg-surface-1 [--row-pressed:var(--surface-3)]',
          cardClassName,
        )}
      >
        {children}
      </div>
      {footer && <div className="px-4 pt-2 type-footnote text-label-2">{footer}</div>}
    </section>
  )
}

export interface RowProps {
  title: ReactNode
  subtitle?: ReactNode
  leading?: ReactNode
  trailing?: ReactNode
  chevron?: boolean
  to?: string
  onClick?: () => void
  destructive?: boolean
  accent?: boolean
  centered?: boolean
  disabled?: boolean
  /** the leading element is a 30 px icon tile, so the separator starts at the title (60 px) */
  iconInset?: boolean
  className?: string
  titleClassName?: string
  'aria-label'?: string
  'aria-current'?: ComponentProps<'a'>['aria-current']
  /** subtitle lines before an ellipsis; 1 keeps rows a fixed 60 px (no layout shift when a list loads) */
  subtitleLines?: 1 | 2
  /** a flash of --accent-tint when a new audit entry arrives (DESIGN.md §4) */
  flash?: boolean
}

/** Row is one line of a grouped list; the whole row is the hit target, a link or a button. */
export function Row({
  title,
  subtitle,
  leading,
  trailing,
  chevron,
  to,
  onClick,
  destructive,
  accent,
  centered,
  disabled,
  iconInset,
  className,
  titleClassName,
  flash,
  subtitleLines = 2,
  ...aria
}: RowProps) {
  const interactive = !!to || !!onClick
  const body = (
    <>
      {leading && <span className="flex shrink-0 items-center">{leading}</span>}
      <span className={cn('flex min-w-0 flex-1 flex-col', centered && 'items-center text-center')}>
        <span
          className={cn(
            'type-body',
            destructive && 'text-danger-text',
            accent && 'text-accent-text',
            !subtitle && 'truncate',
            titleClassName,
          )}
        >
          {title}
        </span>
        {subtitle && <span className={cn('type-subheadline text-label-2', subtitleLines === 1 ? 'truncate' : 'line-clamp-2')}>{subtitle}</span>}
      </span>
      {trailing && <span className="flex max-w-[62%] min-w-0 shrink-0 items-center justify-end gap-2 type-body text-label-2">{trailing}</span>}
      {chevron && <ChevronRight aria-hidden className="size-4 shrink-0 text-label-3" strokeWidth={2.5} />}
    </>
  )
  const classes = cn(
    'homie-row flex w-full items-center gap-3 px-4 text-left outline-offset-[-2px]',
    subtitle ? 'min-h-[60px] py-2.5' : 'min-h-11 py-[11px]',
    iconInset && '[--row-inset:60px]',
    interactive && !disabled && 'transition-colors duration-75 active:bg-[var(--row-pressed)] hover:bg-[color-mix(in_srgb,var(--row-pressed)_45%,transparent)]',
    disabled && 'opacity-40',
    flash && 'homie-flash',
    className,
  )
  if (to && !disabled) {
    return (
      <Link to={to} className={classes} {...aria}>
        {body}
      </Link>
    )
  }
  if (onClick) {
    return (
      <button type="button" onClick={onClick} disabled={disabled} className={classes} {...aria}>
        {body}
      </button>
    )
  }
  return (
    <div className={classes} {...aria}>
      {body}
    </div>
  )
}

/** RowValue is the --label-2 value text on the right of a row. */
export function RowValue({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn('max-w-[12rem] truncate type-body text-label-2', className)}>{children}</span>
}
