import { BookMarked, Columns2, Plus, X } from 'lucide-react'
import { CommandLibrary } from '@/components/snippets/CommandLibrary'
import { Drawer } from '@/components/ui/Dialog'
import { useCallback, useEffect, useRef, useState } from 'react'
import { hostPath, useHost } from '@/components/shell/host'
import { useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { Menu } from '@/components/ui/Menu'
import { Notice, Pill } from '@/components/ui/Box'
import { cx } from '@/components/ui/cx'
import { ApiError, del, get, post } from '@/lib/api'
import { ago } from '@/lib/format'
import { useMedia } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Container } from '@/lib/types'
import { onRequest, takeRequests, type OpenRequest } from './store'
import { TerminalView, type TerminalHandle } from './TerminalView'

export interface Session { id: string; target: string; container?: string; user: string; title: string; created: number; last_active: number; clients: number; recording: boolean; exited: boolean }

const wsURL = (host: string, id: string) => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/hosts/${encodeURIComponent(host)}/terminals/${id}/ws`

// SessionList shows the caller's sessions that keep running with no window attached.
export function SessionList({ sessions, open, onAttach, onKill }: { sessions: Session[]; open: string[]; onAttach: (s: Session) => void; onKill: (s: Session) => void }) {
  const t = useT()
  const detached = sessions.filter((s) => !open.includes(s.id) && !s.exited)
  if (!detached.length) return null
  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-line bg-soft px-3 py-1.5 text-[12.5px]">
      {detached.map((s) => (
        <span key={s.id} className="inline-flex items-center gap-1.5 rounded-[var(--radius-box)] border border-line bg-canvas px-2 py-0.5">
          <b className="font-medium">{s.title || s.target}</b>
          <span className="text-mute">{t('terminal.background')} · {ago(s.last_active, t)}</span>
          <button className="text-acc hover:underline" onClick={() => onAttach(s)}>{t('terminal.attach')}</button>
          <button aria-label={t('terminal.end')} className="text-mute hover:text-bad" onClick={() => onKill(s)}><X className="size-3.5" /></button>
        </span>
      ))}
    </div>
  )
}

const KEYS: [string, string][] = [['Esc', '\x1b'], ['Tab', '\t'], ['Ctrl-C', '\x03'], ['Ctrl-D', '\x04'], ['←', '\x1b[D'], ['↑', '\x1b[A'], ['↓', '\x1b[B'], ['→', '\x1b[C']]

export default function TerminalPanel({ compact }: { compact?: boolean }) {
  const t = useT()
  const { host, hosts } = useHost()
  const nav = useNavigate()
  const phone = useMedia('(max-width: 640px)')
  const [sessions, setSessions] = useState<Session[]>([])
  const [tabs, setTabs] = useState<string[]>([]) // open session ids
  const [active, setActive] = useState<string>()
  const [split, setSplit] = useState<string>()
  const [record, setRecord] = useState(false)
  const [containers, setContainers] = useState<Container[]>([])
  const [err, setErr] = useState('')
  const [lib, setLib] = useState(() => localStorage.getItem('berth.cmdlib') === '1')
  useEffect(() => { localStorage.setItem('berth.cmdlib', lib ? '1' : '0') }, [lib])
  const handles = useRef<Record<string, TerminalHandle | null>>({})
  const refresh = useCallback(() => get<{ items: Session[] }>(`/api/v1/hosts/${host}/terminals`).then((r) => setSessions(r.items)).catch(() => {}), [host])
  const openNew = useCallback(async (req: OpenRequest) => {
    setErr('')
    try {
      if (req.program === 'claude') { // one resident conversation per workspace: go back to it
        const list = await get<{ items: Session[] }>(`/api/v1/hosts/${host}/terminals`).then((r) => r.items).catch(() => [] as Session[])
        const ex = list.find((s) => s.title === `${req.container} · Claude` && !s.exited)
        if (ex) {
          setSessions(list)
          setTabs((l) => (l.includes(ex.id) ? l : [...l, ex.id]))
          setActive(ex.id)
          return
        }
      }
      const s = await post<Session>(`/api/v1/hosts/${host}/terminals`, { ...req, record, cols: 100, rows: 30 })
      setSessions((l) => [...l, s])
      setTabs((l) => [...l, s.id])
      setActive(s.id)
    } catch (e) { setErr((e as ApiError).message) }
  }, [host, record])
  useEffect(() => {
    void refresh()
    get<{ items: Container[] }>(`/api/v1/hosts/${host}/containers`).then((r) => setContainers(r.items.filter((c) => c.state === 'running'))).catch(() => {})
  }, [host, refresh])
  useEffect(() => {
    const consume = () => { for (const r of takeRequests()) void openNew(r) }
    consume()
    return onRequest(consume)
  }, [openNew])
  const close = (id: string, kill: boolean) => {
    setTabs((l) => l.filter((x) => x !== id))
    if (split === id) setSplit(undefined)
    if (active === id) setActive(tabs.filter((x) => x !== id)[0])
    if (kill) void del(`/api/v1/hosts/${host}/terminals/${id}`).then(refresh)
    else void refresh()
  }
  const byId = (id: string) => sessions.find((s) => s.id === id)
  const panes = [active, split].filter(Boolean) as string[]
  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-1 border-b border-line px-2 py-1">
        <div className="flex min-w-0 flex-1 gap-1 overflow-x-auto">
          {tabs.map((id) => (
            <span key={id} className={cx('inline-flex shrink-0 items-center gap-1 rounded-[var(--radius-box)] px-2 py-0.5 text-[12.5px]', id === active || id === split ? 'bg-soft font-semibold' : 'text-mute')}>
              <button onClick={() => setActive(id)}>{byId(id)?.title || id}</button>
              {byId(id)?.recording && <Pill tone="bad" className="!leading-4">REC</Pill>}
              <button aria-label={t('terminal.detach')} title={t('terminal.detach')} onClick={() => close(id, false)} className="text-mute hover:text-fg"><X className="size-3" /></button>
            </span>
          ))}
        </div>
        {tabs.length > 0 && <Button size="sm" variant={lib ? 'primary' : 'ghost'} aria-pressed={lib} onClick={() => setLib(!lib)}><BookMarked className="size-3.5" />{t('snip.title')}</Button>}
        <label className="hidden items-center gap-1 text-[12px] text-mute sm:flex"><input type="checkbox" checked={record} onChange={(e) => setRecord(e.target.checked)} />{t('terminal.record')}</label>
        {!phone && tabs.length > 1 && (
          <Button size="sm" variant="ghost" aria-label={t('terminal.split')} onClick={() => setSplit(split ? undefined : tabs.find((x) => x !== active))}><Columns2 className="size-4" /></Button>
        )}
        <Menu trigger={<Button size="sm"><Plus className="size-3.5" />{t('terminal.new')}</Button>} items={[
          { label: t('terminal.on_host', { host }), onClick: () => void openNew({ target: 'host' }) },
          ...hosts.filter((h) => h.name !== host && h.online).map((h) => ({ label: t('terminal.go_host', { host: h.name }), onClick: () => nav(hostPath(h.name, 'terminal')) })),
          ...(containers.length ? ['sep' as const] : []),
          ...containers.map((c) => ({ label: t('terminal.in_container', { name: c.name }), onClick: () => void openNew({ target: 'container', container: c.name }) })),
        ]} />
      </div>
      <SessionList sessions={sessions} open={tabs} onAttach={(s) => { setTabs((l) => [...l, s.id]); setActive(s.id) }} onKill={(s) => void del(`/api/v1/hosts/${host}/terminals/${s.id}`).then(refresh)} />
      {err && <div className="p-2"><Notice tone="bad">{err}</Notice></div>}
      {tabs.length === 0 ? (
        <div className="flex flex-1 items-center justify-center p-6 text-center text-[13px] text-mute">
          <div>
            <p>{t('terminal.empty')}</p>
            <Button className="mt-3" variant="primary" onClick={() => void openNew({ target: 'host' })}>{t('terminal.on_host', { host })}</Button>
            {!compact && <p className="mt-3 text-[12px]">{t('terminal.persist_note')}</p>}
          </div>
        </div>
      ) : (
        <div className="flex min-h-0 flex-1">
          {tabs.map((id) => (
            <div key={id} className={cx('min-w-0 flex-1 border-line', !panes.includes(id) && 'hidden', split && id === split && 'border-l')}>
              <TerminalView ref={(h) => { handles.current[id] = h }} url={wsURL(host, id)} onExit={() => void refresh()} />
            </div>
          ))}
          {lib && !phone && (
            <div className="w-80 shrink-0 border-l border-line">
              <CommandLibrary onClose={() => setLib(false)} selection={() => handles.current[active!]?.selection() ?? ''}
                onInsert={(text, run) => { const h = handles.current[active!]; h?.send(text.replace(/\r?\n/g, '\r') + (run ? '\r' : '')); h?.focus() }} />
            </div>
          )}
        </div>
      )}
      {lib && phone && (
        <Drawer open onOpenChange={(o) => !o && setLib(false)} title={t('snip.title')}>
          <CommandLibrary onInsert={(text, run) => { const h = handles.current[active!]; h?.send(text.replace(/\r?\n/g, '\r') + (run ? '\r' : '')); setLib(false); h?.focus() }} />
        </Drawer>
      )}
      {phone && tabs.length > 0 && (
        <div className="flex gap-1 overflow-x-auto border-t border-line bg-soft p-1">
          {KEYS.map(([label, seq]) => <Button key={label} size="sm" onClick={() => { handles.current[active!]?.send(seq); handles.current[active!]?.focus() }}>{label}</Button>)}
        </div>
      )}
    </div>
  )
}
