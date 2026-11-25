import { useMe } from '@devxpro/mcp-gatekeeper-ui'
import { Navigate } from 'react-router'

/**
 * Landing is "/", the shell's index route: an owner lands on Дом, anyone else on the connect page, which is all a
 * guest needs. The shell has already signed the person in (or sent them to sign in and back).
 */
export function Landing() {
  const me = useMe().data
  if (!me) return null
  return <Navigate to={me.person.role === 'owner' ? '/home' : '/connect'} replace />
}
