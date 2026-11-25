import { Button, CopyField, IconTile, Rich, Section, Sheet, SheetFooter, TextField, toast } from '@devxpro/mcp-gatekeeper-ui'
import { FlaskConical } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { hostnameOk } from '@/lib/hostname'

// Quick mode (docs/SECURITY.md, Quick mode): a Cloudflare quick tunnel is "for trying". Its address changes on every restart
// of homed, so the admin says so wherever the address matters and offers the way to a domain of one's own.

/** QuickBanner is the card that labels the address «для пробы» and opens the move to an own domain. */
export function QuickBanner({ className }: { className?: string }) {
  const { t } = useTranslation()
  const [moving, setMoving] = useState(false)
  return (
    <Section className={className} label={t('quick.title')}>
      <div className="flex gap-3 p-4" role="note">
        <IconTile icon={FlaskConical} color="orange" />
        <div className="min-w-0 flex-1">
          <p className="type-headline">{t('quick.title')}</p>
          <p className="mt-0.5 type-subheadline text-label-2">{t('quick.text')}</p>
          <Button className="mt-3" variant="tinted" size="sm" onClick={() => setMoving(true)}>
            {t('quick.move')}
          </Button>
        </div>
      </div>
      <DomainSheet open={moving} onClose={() => setMoving(false)} />
    </Section>
  )
}

/**
 * DomainSheet is the move to an own domain. It happens on the home computer: the Cloudflare login and the new first
 * passkey both belong there, and a new address changes the passkeys' RP ID anyway. The sheet builds the
 * command for the address the person types.
 */
export function DomainSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  const [host, setHost] = useState('')
  const typed = host.trim().toLowerCase()
  const command = `homed cloudflare connect ${hostnameOk(typed) ? typed : 'home.example.com'}`
  return (
    <Sheet open={open} onClose={onClose} title={t('domain.title')}>
      <p className="type-subheadline text-label-2">{t('domain.text')}</p>
      <div className="mt-5">
        <TextField
          label={t('domain.host.label')}
          hint={t('domain.host.hint')}
          placeholder="home.example.com"
          value={host}
          onChange={(e) => setHost(e.target.value)}
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          inputMode="url"
          mono
        />
      </div>
      <ol className="mt-6 flex flex-col gap-3">
        {[1, 2, 3, 4, 5].map((n) => (
          <li key={n} className="flex gap-3">
            <span aria-hidden className="inline-flex size-6 shrink-0 items-center justify-center rounded-full bg-fill text-[13px] font-semibold text-label-2 tabular-nums">
              {n}
            </span>
            <div className="min-w-0 flex-1 pt-0.5">
              <p className="type-body">
                <Rich text={t(`domain.step.${n}`)} />
              </p>
              {n === 2 && <CopyField className="mt-2" value={command} label={t('domain.command')} onCopied={() => toast(t('common.copied'))} />}
            </div>
          </li>
        ))}
      </ol>
      <SheetFooter>
        <Button variant="filled" size="lg" onClick={onClose}>
          {t('common.done')}
        </Button>
      </SheetFooter>
    </Sheet>
  )
}
