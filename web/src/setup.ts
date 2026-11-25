import { configure } from '@devxpro/mcp-gatekeeper-ui'
import { House } from 'lucide-react'
import en from './i18n/en.json'
import ru from './i18n/ru.json'

/**
 * setupHomie configures the gatekeeper's UI package for Homie before the first render (main.tsx, and the tests):
 * - the brand: core.Brand.CSRFHeader of homed ("X-Homie"), the localStorage prefix ("homie.theme", "homie.lang", as
 *   public/theme.js reads them) and the app icon's glyph;
 * - the people options of homed (STEP2 §2.4): one account, which is the home, and co-owners;
 * - Homie's strings on top of the package's.
 */
export function setupHomie() {
  configure({
    brand: { csrfHeader: 'X-Homie', storagePrefix: 'homie', icon: House },
    strings: { ru, en },
    singleAccount: true,
    coOwners: true,
  })
}
