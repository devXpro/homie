/** hostnameOk accepts a DNS name with at least one dot, like home.example.com: the address a home can live at. */
export function hostnameOk(v: string) {
  return /^(?=.{4,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/i.test(v.trim())
}
