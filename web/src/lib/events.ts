// One shared EventSource for the whole app; reconnects with backoff.
type Handler = (type: string, data: any) => void
const handlers = new Set<Handler>()
let es: EventSource | null = null
let retry = 1000
// every type the server publishes (internal/panel/events_types_test.go keeps this in step)
const TYPES = ['task.update', 'task.log', 'change.done', 'change.pending', 'link.pending', 'token.activity', 'metrics.tick', 'host.online', 'host.offline',
  'pipeline.run', 'release', 'backup.run', 'alert', 'uptime', 'firewall.pending', 'ban', 'waf', 'dev.task', 'cron.run', 'ssh.pending', 'drift']

function connect() {
  if (es || handlers.size === 0) return
  es = new EventSource('/api/v1/events', { withCredentials: true })
  es.onopen = () => { retry = 1000 }
  for (const t of TYPES) {
    es.addEventListener(t, (ev) => {
      let d: unknown = null
      try { d = JSON.parse((ev as MessageEvent).data) } catch { return }
      handlers.forEach((h) => h(t, d))
    })
  }
  es.onerror = () => {
    es?.close()
    es = null
    if (handlers.size > 0) setTimeout(connect, retry)
    retry = Math.min(retry * 2, 30000)
  }
}

export function subscribe(h: Handler): () => void {
  handlers.add(h)
  connect()
  return () => {
    handlers.delete(h)
    if (handlers.size === 0) { es?.close(); es = null }
  }
}
