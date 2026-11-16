import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import Guacamole from 'guacamole-common-js'
import { CommandLibrary } from '@/components/snippets/CommandLibrary'
import { PanelRight } from 'lucide-react'
import { Spark } from '@/components/charts/Spark'
import { RingMeter } from '@/components/charts/Ring'
import { Button } from '@/components/ui/Button'
import { Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { netRx, netTx, rootDisk, useHostLive } from '@/lib/useHostLive'
import type { Conn } from './RemotePage'

// One remote desktop filling the browser window (方案 §5.8). The panel relays the Guacamole
// protocol from guacd; this page draws it and sends keyboard, mouse and clipboard back.
// A side panel (like a cloud console's) carries the server's load, the session, keys the
// browser would otherwise swallow, the clipboard and "type this text".
type State = 'connecting' | 'connected' | 'closed' | 'error'
type Client = any // eslint-disable-line @typescript-eslint/no-explicit-any

// keysyms
const K = { ctrl: 0xffe3, alt: 0xffe9, shift: 0xffe1, del: 0xffff, tab: 0xff09, esc: 0xff1b, f4: 0xffc1, win: 0xffeb, print: 0xff61, enter: 0xff0d, bs: 0xff08, t: 0x74, l: 0x6c, r: 0x72, e: 0x65 }
const SHORTCUTS: { id: string; keys: number[]; os?: 'win' | 'linux' }[] = [
  { id: 'ctrl_alt_del', keys: [K.ctrl, K.alt, K.del] },
  { id: 'alt_tab', keys: [K.alt, K.tab] },
  { id: 'alt_f4', keys: [K.alt, K.f4] },
  { id: 'win', keys: [K.win] },
  { id: 'ctrl_esc', keys: [K.ctrl, K.esc] },
  { id: 'print', keys: [K.print] },
  { id: 'ctrl_shift_esc', keys: [K.ctrl, K.shift, K.esc], os: 'win' },
  { id: 'win_r', keys: [K.win, K.r], os: 'win' },
  { id: 'win_e', keys: [K.win, K.e], os: 'win' },
  { id: 'win_l', keys: [K.win, K.l], os: 'win' },
  { id: 'ctrl_alt_t', keys: [K.ctrl, K.alt, K.t], os: 'linux' },
]
const PANEL_KEY = 'berth.remote.panel'

export default function ViewerPage() {
  const t = useT()
  const { id } = useParams()
  const list = useApi<{ items: Conn[] }>('/api/v1/remote')
  const conn = list.data?.items.find((c) => String(c.id) === id)
  const box = useRef<HTMLDivElement>(null)
  const client = useRef<Client>(null)
  const [state, setState] = useState<State>('connecting')
  const [error, setError] = useState('')
  const [clip, setClip] = useState('')
  const [typeText, setTypeText] = useState('')
  const [panel, setPanel] = useState(() => localStorage.getItem(PANEL_KEY) !== '0')
  const [fit, setFit] = useState(true)
  const [attempt, setAttempt] = useState(0)
  const [since, setSince] = useState(0)
  const [size, setSize] = useState<[number, number]>([0, 0])
  const [rate, setRate] = useState<number[]>([]) // screen data per second
  const counted = useRef(0)
  const fitRef = useRef(fit)
  fitRef.current = fit
  const typing = useRef(false) // keys go to a text box in the panel, not the remote machine

  useEffect(() => { if (conn) document.title = `${conn.name} · Berth` }, [conn])
  useEffect(() => { localStorage.setItem(PANEL_KEY, panel ? '1' : '0') }, [panel])

  useEffect(() => {
    const el = box.current
    if (!el || !id) return
    setState('connecting'); setError('')
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const tunnel = new Guacamole.WebSocketTunnel(`${proto}//${location.host}/api/v1/remote/${id}/tunnel`)
    const c = new Guacamole.Client(tunnel)
    client.current = c
    // count what the remote screen sends (for the session panel)
    const orig = tunnel.oninstruction
    tunnel.oninstruction = (op: string, args: string[]) => {
      if (op === 'blob') counted.current += (args[1]?.length ?? 0) * 0.75
      orig?.(op, args)
    }
    const display = c.getDisplay()
    const view = display.getElement()
    el.innerHTML = ''
    el.appendChild(view)
    const doFit = () => {
      const w = display.getWidth(), h = display.getHeight()
      setSize([w, h])
      if (w && h) display.scale(fitRef.current ? Math.min(el.clientWidth / w, el.clientHeight / h, 1) : 1)
    }
    display.onresize = doFit
    c.onstatechange = (s: number) => {
      if (s === 3) { setState('connected'); setSince(Date.now()) }
      if (s === 5) setState((o) => (o === 'error' ? o : 'closed'))
    }
    c.onerror = (st: { code: number; message: string }) => { setError(st.message || String(st.code)); setState('error') }
    tunnel.onerror = (st: { code: number; message: string }) => { setError(st.message || String(st.code)); setState('error') }
    c.onclipboard = (stream: unknown, mime: string) => {
      if (!/^text\//.test(mime)) return
      const r = new Guacamole.StringReader(stream)
      let text = ''
      r.ontext = (s: string) => { text += s }
      r.onend = () => { setClip(text); navigator.clipboard?.writeText(text).catch(() => {}) }
    }
    const mouse = new Guacamole.Mouse(view)
    mouse.onEach(['mousedown', 'mouseup', 'mousemove'], (e: { state: unknown }) => c.sendMouseState(e.state, true))
    const kb = new Guacamole.Keyboard(document)
    kb.onkeydown = (k: number) => { if (typing.current) return true; c.sendKeyEvent(1, k); return false }
    kb.onkeyup = (k: number) => { if (!typing.current) c.sendKeyEvent(0, k) }
    // the local clipboard goes over when the window gets focus again (after copying elsewhere)
    const pushClip = () => {
      navigator.clipboard?.readText().then((s) => { if (s) sendClip(c, s) }).catch(() => {})
    }
    window.addEventListener('focus', pushClip)
    // the box changes size with the window and with the side panel
    let timer = 0
    const ro = new ResizeObserver(() => {
      doFit()
      window.clearTimeout(timer)
      timer = window.setTimeout(() => c.sendSize(el.clientWidth, el.clientHeight), 300)
    })
    ro.observe(el)
    const tick = window.setInterval(() => { setRate((l) => [...l.slice(-59), counted.current]); counted.current = 0 }, 1000)
    const dpi = Math.round(96 * (window.devicePixelRatio || 1))
    const tz = encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || '')
    c.connect(`width=${el.clientWidth}&height=${el.clientHeight}&dpi=${dpi}&tz=${tz}`)
    return () => {
      window.removeEventListener('focus', pushClip)
      ro.disconnect()
      window.clearInterval(tick)
      kb.onkeydown = kb.onkeyup = null
      kb.reset()
      c.disconnect()
    }
  }, [id, attempt])

  useEffect(() => { // re-apply scaling when "fit" changes
    const c = client.current, el = box.current
    if (!c || !el) return
    const d = c.getDisplay()
    if (d.getWidth()) d.scale(fit ? Math.min(el.clientWidth / d.getWidth(), el.clientHeight / d.getHeight(), 1) : 1)
  }, [fit])

  const press = (keys: number[]) => {
    const c = client.current
    if (!c) return
    for (const k of keys) c.sendKeyEvent(1, k)
    for (const k of [...keys].reverse()) c.sendKeyEvent(0, k)
  }
  // type text key by key: works where pasting does not (login screens, consoles)
  const typeOut = (s: string) => {
    const c = client.current
    if (!c) return
    for (const ch of s) {
      const cp = ch.codePointAt(0)!
      const k = ch === '\n' ? K.enter : ch === '\t' ? K.tab : cp < 0x100 ? cp : 0x01000000 | cp
      c.sendKeyEvent(1, k)
      c.sendKeyEvent(0, k)
    }
  }
  const full = () => (document.fullscreenElement ? document.exitFullscreen() : document.documentElement.requestFullscreen()).catch(() => {})
  const graphical = conn ? conn.protocol === 'rdp' || conn.protocol === 'vnc' : true
  const os = conn?.protocol === 'rdp' && conn.system !== 'desktop' ? 'win' : 'linux'
  return (
    <div className="flex h-screen flex-col bg-black text-white">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-white/10 bg-neutral-900 px-3 text-[12.5px]">
        <span className={state === 'connected' ? 'text-emerald-400' : state === 'error' ? 'text-red-400' : 'text-neutral-400'}>●</span>
        <span className="font-semibold">{conn?.name ?? '…'}</span>
        <span className="num hidden text-neutral-400 sm:inline">{conn ? `${conn.protocol.toUpperCase()} · ${conn.host}:${conn.port}` : ''}</span>
        <span className="text-neutral-400">{t('rd.state_' + state)}</span>
        <span className="ml-auto flex gap-1">
          {graphical && <Button size="sm" variant="ghost" onClick={() => press(SHORTCUTS[0].keys)}>Ctrl+Alt+Del</Button>}
          <Button size="sm" variant="ghost" onClick={full}>{t('rd.fullscreen')}</Button>
          {state === 'connected'
            ? <Button size="sm" variant="ghost" onClick={() => client.current?.disconnect()}>{t('rd.disconnect')}</Button>
            : state !== 'connecting' && <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>{t('rd.reconnect')}</Button>}
          <Button size="sm" variant="ghost" aria-pressed={panel} aria-label={t('rd.tools')} onClick={() => setPanel((p) => !p)}><PanelRight className="size-4" /></Button>
        </span>
      </div>
      <div className="flex min-h-0 flex-1">
        <div className="relative min-w-0 flex-1">
          <div ref={box} className={cx('absolute inset-0 flex', fit ? 'items-center justify-center overflow-hidden' : 'overflow-auto')} onMouseDown={() => { typing.current = false }} />
          {state !== 'connected' && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
              <div className="max-w-md rounded-lg bg-neutral-900/90 p-5 text-center text-[13px]">
                <div className="font-semibold">{t('rd.state_' + state)}</div>
                {error && <div className="mt-2 text-red-300">{error}</div>}
                {state === 'error' && <div className="mt-2 text-neutral-400">{t('rd.error_hint')}</div>}
              </div>
            </div>
          )}
        </div>
        {panel && (
          <aside className="w-72 shrink-0 space-y-4 overflow-y-auto border-l border-white/10 bg-neutral-900 p-3 text-[12.5px]"
            onFocus={(e) => { typing.current = e.target instanceof HTMLTextAreaElement }} onBlur={() => { typing.current = false }}
            onClick={(e) => { if (e.target instanceof HTMLElement && e.target.closest('button')) (document.activeElement as HTMLElement | null)?.blur() }}>
            {conn?.system === 'desktop' && <HostLoad />}
            <Section title={t('rd.session')}>
              <Row k={t('rd.connected_for')} v={since && state === 'connected' ? <Since from={since} /> : '—'} />
              <Row k={t('rd.resolution')} v={size[0] ? `${size[0]} × ${size[1]}` : '—'} />
              <Row k={t('rd.screen_data')} v={bytes(rate[rate.length - 1] ?? 0) + '/s'} />
              <div className="mt-1 text-white/80"><Spark values={rate} height={24} label={t('rd.screen_data')} /></div>
              {graphical && <label className="mt-2 flex items-center gap-2"><input type="checkbox" checked={fit} onChange={(e) => setFit(e.target.checked)} />{t('rd.fit_window')}</label>}
            </Section>
            {graphical && (
              <Section title={t('rd.keys')}>
                <div className="grid grid-cols-2 gap-1">
                  {SHORTCUTS.filter((s) => !s.os || s.os === os).map((s) => (
                    <button key={s.id} type="button" onClick={() => press(s.keys)} className="rounded border border-white/10 px-2 py-1 text-left hover:border-white/30">
                      <div className="num text-[11.5px]">{t('rd.key.' + s.id)}</div>
                      <div className="text-[10.5px] text-neutral-400">{t('rd.key_hint.' + s.id)}</div>
                    </button>
                  ))}
                </div>
              </Section>
            )}
            {conn?.clipboard !== false && (
              <Section title={t('rd.clipboard')}>
                <div className="mb-1 text-[11.5px] text-neutral-400">{t('rd.clipboard_hint')}</div>
                <Textarea className="num h-20 bg-neutral-800 text-[12px] text-white" value={clip} onChange={(e) => setClip(e.target.value)} />
                <Button size="sm" variant="primary" className="mt-1.5" onClick={() => { if (client.current) sendClip(client.current, clip) }}>{t('rd.clipboard_send')}</Button>
              </Section>
            )}
            <Section title={t('snip.title')}>
              <div className="dark h-96 overflow-hidden rounded-lg border border-white/10">
                <CommandLibrary onInsert={(text, run) => typeOut(text + (run ? '\n' : ''))} />
              </div>
            </Section>
            <Section title={t('rd.type_text')}>
              <div className="mb-1 text-[11.5px] text-neutral-400">{t('rd.type_text_hint')}</div>
              <Textarea className="num h-16 bg-neutral-800 text-[12px] text-white" value={typeText} onChange={(e) => setTypeText(e.target.value)} />
              <Button size="sm" className="mt-1.5" disabled={!typeText} onClick={() => typeOut(typeText)}>{t('rd.type_send')}</Button>
            </Section>
          </aside>
        )}
      </div>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return <section><div className="mb-1.5 text-[11px] font-semibold uppercase tracking-wide text-neutral-400">{title}</div>{children}</section>
}
function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return <div className="flex justify-between py-0.5"><span className="text-neutral-400">{k}</span><span className="num">{v}</span></div>
}
function Since({ from }: { from: number }) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => { const i = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(i) }, [])
  const s = Math.floor((now - from) / 1000)
  return <>{`${Math.floor(s / 3600)}:${String(Math.floor(s / 60) % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`}</>
}

// this server's load (the desktop runs on it), live
function HostLoad() {
  const t = useT()
  const { samples, last } = useHostLive('local')
  const disk = last && rootDisk(last)
  return (
    <Section title={t('rd.server_load')}>
      <div className="grid grid-cols-3 gap-1">
        <RingMeter size={64} value={last ? last.cpu / 100 : null} label={t('overview.cpu')} />
        <RingMeter size={64} value={last ? last.mem_used / last.mem_total : null} label={t('overview.memory')} sub={last ? bytes(last.mem_used) : undefined} />
        <RingMeter size={64} value={disk ? disk.used / disk.total : null} label={t('overview.disk')} sub={disk ? bytes(disk.used) : undefined} />
      </div>
      {last && <>
        <Row k={t('rd.load_avg')} v={last.load.map((x) => x.toFixed(2)).join(' · ')} />
        <Row k={t('rd.net_down')} v={bytes(netRx(last)) + '/s'} />
        <div className="text-white/80"><Spark values={samples.map(netRx)} height={20} label={t('rd.net_down')} /></div>
        <Row k={t('rd.net_up')} v={bytes(netTx(last)) + '/s'} />
        <div className="text-white/80"><Spark values={samples.map(netTx)} height={20} label={t('rd.net_up')} /></div>
      </>}
      {!last && <div className="text-neutral-400">{t('common.loading')}</div>}
    </Section>
  )
}

function sendClip(c: Client, text: string) {
  const w = new Guacamole.StringWriter(c.createClipboardStream('text/plain'))
  w.sendText(text)
  w.sendEnd()
}
