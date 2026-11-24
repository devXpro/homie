// The built-in catalog of the Add devices sheet (DESIGN.md §8.6): recommended modules, previewed lazily.
export const catalog: { repo: string; icon: string }[] = [{ repo: 'devXpro/homie-tv', icon: 'tv' }]

/** parseRepo accepts "owner/repo" or a GitHub URL and returns "owner/repo", or undefined. */
export function parseRepo(input: string): string | undefined {
  const s = input.trim().replace(/\.git$/, '').replace(/\/+$/, '')
  const m = /^(?:https?:\/\/)?(?:www\.)?github\.com\/([\w.-]+)\/([\w.-]+)/i.exec(s) ?? /^([\w.-]+)\/([\w.-]+)$/.exec(s)
  return m ? `${m[1]}/${m[2]}` : undefined
}
