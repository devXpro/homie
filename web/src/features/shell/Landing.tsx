import { ErrorState, isApiError, isOffline, OfflineScreen, Splash, useMe } from '@devxpro/mcp-gatekeeper-ui'
import { useTranslation } from 'react-i18next'
import { Navigate } from 'react-router'
import { errorText } from '@/lib/errors'

/**
 * Landing is "/": an owner lands on Дом, anyone else on the connect page, which is all a guest needs. Without a session
 * it goes to sign in and comes back here, so the role decides again. (The package's own "/" leads to /connect; this
 * route is matched first because the application's public routes come before the shell.)
 */
export function Landing() {
  const { t } = useTranslation()
  const me = useMe()
  if (me.isPending) return <Splash />
  if (me.error) {
    if (isApiError(me.error, 'no_session')) return <Navigate to="/login?next=%2F" replace />
    if (isOffline(me.error)) return <OfflineScreen onRetry={() => void me.refetch()} />
    // e.g. misdirected: a quick tunnel's old address, after the home got a new one.
    return (
      <main className="flex min-h-dvh items-center justify-center">
        <ErrorState text={errorText(me.error, t)} onRetry={() => void me.refetch()} />
      </main>
    )
  }
  return <Navigate to={me.data.person.role === 'owner' ? '/home' : '/connect'} replace />
}
