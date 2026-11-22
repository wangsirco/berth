import { useEffect, useRef, useState } from 'react'
import { useParams, useSearchParams } from 'react-router-dom'
import { BookMarked, RotateCcw, Square } from 'lucide-react'
import { CommandLibrary } from '@/components/snippets/CommandLibrary'
import { Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { ApiError, del, get, post } from '@/lib/api'
import { useT } from '@/lib/i18n'
import type { OpenRequest } from './store'
import type { Session } from './TerminalPanel'
import { TerminalView, type TerminalHandle } from './TerminalView'

// One terminal filling a browser tab. /term/:host/:id attaches a running session (moved out of
// the panel at the bottom: the same shell, not a new login); /term/:host/new?target=… makes
// one, then the address becomes its id so reloading the tab attaches it again.
const wsURL = (host: string, id: string) => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/hosts/${encodeURIComponent(host)}/terminals/${id}/ws`
const LIB_KEY = 'berth.term.tab.lib'

export default function TermWindow() {
  const t = useT()
  const { host = 'local', id: param = '' } = useParams()
  const [q] = useSearchParams()
  const [id, setId] = useState(param === 'new' ? '' : param)
  const [sess, setSess] = useState<Session | null>(null)
  const [err, setErr] = useState('')
  const [gone, setGone] = useState(false) // the socket closed
  const [n, setN] = useState(0) // reattach
  const [lib, setLib] = useState(() => localStorage.getItem(LIB_KEY) === '1')
  const h = useRef<TerminalHandle | null>(null)
  useEffect(() => { localStorage.setItem(LIB_KEY, lib ? '1' : '0') }, [lib])

  // a new session, made once
  const made = useRef(false)
  useEffect(() => {
    if (param !== 'new' || made.current) return
    made.current = true
    const req = Object.fromEntries(q.entries()) as unknown as OpenRequest
    post<Session>(`/api/v1/hosts/${host}/terminals`, { ...req, cols: 120, rows: 36 })
      .then((s) => { setSess(s); setId(s.id); history.replaceState(null, '', `/term/${encodeURIComponent(host)}/${encodeURIComponent(s.id)}`) })
      .catch((e: ApiError) => setErr(e.message))
  }, [host, param, q])
  const refresh = () => id && get<{ items: Session[] }>(`/api/v1/hosts/${host}/terminals`).then((r) => setSess(r.items.find((s) => s.id === id) ?? null)).catch(() => {})
  useEffect(() => { void refresh() }, [id, n]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { document.title = `${sess?.title || t('terminal.title')} · ${host} · Berth` }, [sess, host, t])

  const end = async () => { if (!id) return; await del(`/api/v1/hosts/${host}/terminals/${id}`).catch(() => {}); setGone(true); void refresh() }
  const exited = gone && (!sess || sess.exited)
  return (
    <div className="dark flex h-screen flex-col bg-[#010409] text-white">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-white/10 bg-neutral-900 px-3 text-[12.5px]">
        <span className={gone ? 'text-neutral-400' : 'text-emerald-400'}>●</span>
        <span className="font-semibold">{sess?.title || sess?.target || '…'}</span>
        <span className="text-neutral-400">{host}{sess?.user ? ` · ${sess.user}` : ''}</span>
        {sess?.recording && <Pill tone="bad" className="!leading-4">REC</Pill>}
        {gone && <span className="text-neutral-400">{exited ? t('term.ended') : t('term.disconnected')}</span>}
        <span className="ml-auto flex items-center gap-1">
          {gone && !exited && <Button size="sm" onClick={() => { setGone(false); setN(n + 1) }}><RotateCcw className="size-3.5" />{t('term.reattach')}</Button>}
          <Button size="sm" variant={lib ? 'primary' : 'ghost'} aria-pressed={lib} onClick={() => setLib(!lib)}><BookMarked className="size-3.5" />{t('snip.title')}</Button>
          {id && !exited && <Button size="sm" variant="ghost" title={t('term.end_hint')} onClick={() => void end()}><Square className="size-3.5" />{t('terminal.end')}</Button>}
        </span>
      </div>
      {err && <div className="p-3 text-[13px] text-red-300">{err}</div>}
      <div className="flex min-h-0 flex-1">
        <div className="min-w-0 flex-1">
          {id && <TerminalView key={n} ref={h} url={wsURL(host, id)} onExit={() => { setGone(true); void refresh() }} />}
        </div>
        {lib && (
          <div className="w-80 shrink-0 border-l border-white/10 bg-canvas">
            <CommandLibrary onClose={() => setLib(false)} selection={() => h.current?.selection() ?? ''}
              onInsert={(text, run) => { h.current?.send(text.replace(/\r?\n/g, '\r') + (run ? '\r' : '')); h.current?.focus() }} />
          </div>
        )}
      </div>
      <div className="shrink-0 border-t border-white/10 bg-neutral-900 px-3 py-1 text-[11.5px] text-neutral-400">{t('term.tab_note')}</div>
    </div>
  )
}
