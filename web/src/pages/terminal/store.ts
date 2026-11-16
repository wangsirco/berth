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
