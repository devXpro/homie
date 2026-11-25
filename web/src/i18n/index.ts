// The strings live in the package's i18next instance: configure() (src/setup.ts) loads the package's strings and
// Homie's (ru.json, en.json) on top of them. This module only re-exports it for the code that imports '@/i18n'.
export { currentLang, i18n as default, setLanguage, useLang } from '@devxpro/mcp-gatekeeper-ui'
