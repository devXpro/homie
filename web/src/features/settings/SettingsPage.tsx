import { IconTile, Page, Row, RowValue, Section, StatusDot, useLang, useMe } from '@devxpro/mcp-gatekeeper-ui'
import { FlaskConical, Globe, House, Info, Languages, Sparkles } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useBootstrap } from '@/lib/bootstrap'
import { useOwner, useSettings, useSystem, useTunnel } from '@/lib/queries'
import { tunnelHost, tunnelLook, tunnelTone } from '@/lib/status'

// Настройки (STEP2 §3.1): the home's name and language, how the home reaches the internet, the
// assistants, and updates. The person's own things (passkeys, appearance, sign out) are under Люди, the package's
// account page. A member sees the home's name and the language only.

export function SettingsPage() {
  const { t } = useTranslation()
  const lang = useLang()
  const owner = useOwner()
  const me = useMe().data
  const boot = useBootstrap()
  const settings = useSettings(owner)
  const tunnel = useTunnel(owner)
  const system = useSystem(owner)
  const homeName = settings.data?.homeName ?? me?.account.name ?? boot?.homeName ?? ''
  const look = tunnel.data ? tunnelLook(tunnel.data) : undefined
  const quick = (tunnel.data?.mode ?? boot?.tunnel.mode) === 'quick'

  return (
    <Page title={t('settings.title')}>
      <Section header={t('settings.home')} id="settings-home">
        <Row
          to={owner ? '/settings/home' : undefined}
          iconInset
          leading={<IconTile icon={House} color="blue" />}
          title={t('settings.home.name')}
          trailing={<RowValue>{homeName}</RowValue>}
          chevron={owner}
        />
        <Row
          to="/settings/language"
          iconInset
          leading={<IconTile icon={Languages} color="indigo" />}
          title={t('settings.language')}
          trailing={<RowValue>{lang === 'en' ? 'English' : 'Русский'}</RowValue>}
          chevron
        />
      </Section>

      {owner && (
        <Section header={t('settings.connection')} id="settings-connection">
          <Row
            to="/settings/connection"
            iconInset
            leading={<IconTile icon={quick ? FlaskConical : Globe} color={quick ? 'orange' : 'green'} />}
            title={t('settings.connection.address')}
            subtitle={tunnelHost(tunnel.data) || undefined}
            subtitleLines={1}
            trailing={
              look && (
                <span className="inline-flex items-center gap-1.5">
                  <StatusDot tone={tunnelTone(look)} />
                  <RowValue>{quick && look === 'connected' ? t('quick.badge') : t(`tunnel.${look}`)}</RowValue>
                </span>
              )
            }
            chevron
          />
          <Row to="/connect" iconInset leading={<IconTile icon={Sparkles} color="purple" />} title={t('settings.connect')} chevron />
        </Section>
      )}

      {owner && (
        <Section header={t('settings.updates')} id="settings-updates">
          <Row
            to="/settings/about"
            iconInset
            leading={<IconTile icon={Info} color="gray" />}
            title={t('settings.version')}
            trailing={
              <span className="inline-flex items-center gap-2">
                {system.data?.update && <span aria-hidden className="size-2.5 rounded-full bg-accent" />}
                <RowValue className="tabular-nums">{system.data?.version}</RowValue>
              </span>
            }
            chevron
          />
        </Section>
      )}

      {!owner && (
        <Section id="settings-connect">
          <Row to="/connect" iconInset leading={<IconTile icon={Sparkles} color="purple" />} title={t('settings.connect')} chevron />
        </Section>
      )}
    </Page>
  )
}
