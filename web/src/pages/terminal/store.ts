// A request to open a terminal (e.g. "shell into container n8n") may come before the
// terminal panel is mounted; it waits here until the panel picks it up.
export interface OpenRequest { target: 'host' | 'container' | 'instance' | 'instance-console' | 'dev'; container?: string; program?: 'claude' }
let pending: OpenRequest[] = []
const listeners = new Set<() => void>()

export function requestTerminal(r: OpenRequest) {
  pending.push(r)
  listeners.forEach((l) => l())
}

export function takeRequests(): OpenRequest[] {
  const p = pending
  pending = []
  return p
}

export function onRequest(fn: () => void) {
  listeners.add(fn)
  return () => { listeners.delete(fn) }
}

// Where a terminal opens: the panel at the bottom of the page, or a browser tab of its own.
// Set once in the terminal's toolbar; every "Terminal" button follows it.
const WHERE_KEY = 'berth.term.where'
export const termWhere = (): 'panel' | 'tab' => (localStorage.getItem(WHERE_KEY) === 'tab' ? 'tab' : 'panel')
export const setTermWhere = (w: 'panel' | 'tab') => localStorage.setItem(WHERE_KEY, w)

// the host the page is on (/:host/…), for requests made outside the terminal panel
const pageHost = () => {
  const seg = decodeURIComponent(location.pathname.split('/')[1] ?? '')
  return seg && !['account', 'remote', 'vm-screen', 'pve-screen', 'term'].includes(seg) ? seg : 'local'
}

// openTermTab opens a session in its own tab: an existing one (moved out of the panel, still
// the same shell) or a new one made there.
export function openTermTab(host: string, id: string | OpenRequest) {
  const url = typeof id === 'string'
    ? `/term/${encodeURIComponent(host)}/${encodeURIComponent(id)}`
    : `/term/${encodeURIComponent(host)}/new?` + new URLSearchParams(Object.entries(id).filter(([, v]) => v) as [string, string][])
  return window.open(url, typeof id === 'string' ? `berth-term-${host}-${id}` : '_blank')
}

// openTerminal: what "Terminal" buttons call; follows the chosen place
export function openTerminal(r: OpenRequest) {
  if (termWhere() === 'tab' && openTermTab(pageHost(), r)) return
  requestTerminal(r)
}
