import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import RFB from '@novnc/novnc'
import { Camera, Maximize, MousePointerClick, PanelRight, PictureInPicture2, RotateCcw } from 'lucide-react'
import { ChangeProvider, useChange } from '@/components/change/ChangeDialog'
import { HostCtx } from '@/components/shell/host'
import { CommandLibrary } from '@/components/snippets/CommandLibrary'
import { Button } from '@/components/ui/Button'
import { Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { errText, post } from '@/lib/api'
import { bytes } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { ReauthProvider, useReauth } from '../account/reauth'

// A VM's screen in its own window, like a VMware or PVE console: QEMU's VNC through the agent
// and the panel, drawn by noVNC. Watching is the default; a click hands keyboard and mouse to
// the machine and a click outside takes them back, so passing the mouse over it does nothing.
// A side panel holds the machine's state and power, its login (typed in, never shown), the
// fix for a stopped incus-agent, keys the browser would keep, the command library and text to
// type. The screen can float above other windows (picture-in-picture) or go full screen.

export const openVmScreen = (host: string, name: string) =>
  window.open(`/vm-screen/${encodeURIComponent(host)}/${encodeURIComponent(name)}`, `berth-vm-${host}-${name}`, 'popup,width=1240,height=820')

interface Inst { name: string; type: string; status: string; ipv4: string[]; cpus?: string; memory?: string; mem_used?: number; agent?: boolean; booted_at?: number; net?: { mode: string; mac?: string } }
interface Data { items: Inst[]; has_password: Record<string, boolean>; agent_fix?: string }
type State = 'connecting' | 'connected' | 'closed' | 'error'

// keysyms
const K = { ctrl: 0xffe3, alt: 0xffe9, shift: 0xffe1, del: 0xffff, tab: 0xff09, esc: 0xff1b, enter: 0xff0d, bs: 0xff08, up: 0xff52, f1: 0xffbe }
const KEYS: { id: string; keys: number[] }[] = [
  { id: 'ctrl_alt_del', keys: [K.ctrl, K.alt, K.del] },
  { id: 'ctrl_c', keys: [K.ctrl, 0x63] },
  { id: 'ctrl_d', keys: [K.ctrl, 0x64] },
  { id: 'ctrl_z', keys: [K.ctrl, 0x7a] },
  { id: 'ctrl_l', keys: [K.ctrl, 0x6c] },
  { id: 'esc', keys: [K.esc] },
  { id: 'tab', keys: [K.tab] },
  { id: 'up', keys: [K.up] },
]
const SHIFTED = /[A-Z~!@#$%^&*()_+{}|:"<>?]/
const PANEL_KEY = 'berth.vmscreen.panel'

export default function VmScreenPage() {
  const { host = 'local', name = '' } = useParams()
  return (
    <HostCtx.Provider value={{ host, hosts: [] }}>
      <ChangeProvider host={host}><ReauthProvider><IncusScreen host={host} name={name} /></ReauthProvider></ChangeProvider>
    </HostCtx.Provider>
  )
}

// What a screen window needs from where the machine lives (Incus here, PVE elsewhere).
export interface ScreenIO { typeOut: (s: string) => void; connected: boolean; reconnect: () => void; flash: (s: string) => void }
export interface ScreenSource {
  name: string
  sub: string // shown after the name: kind, node, address
  connect: () => Promise<{ path: string; password?: string }>
  side: (io: ScreenIO) => React.ReactNode // the machine's own sections, above the common ones
}

function IncusScreen({ host, name }: { host: string; name: string }) {
  const t = useT()
  const run = useChange()
  const reauth = useReauth()
  const d = useApi<Data>(`/api/v1/hosts/${host}/vms`, [host])
  useInterval(() => void d.reload(), 5000)
  const inst = d.data?.items.find((i) => i.name === name)
  const running = inst?.status === 'Running'
  const ip = inst?.ipv4[0]
  const noAgent = !!inst && running && inst.agent === false && !!inst.booted_at && Date.now() / 1000 - inst.booted_at > 90
  const typePassword = async (io: ScreenIO) => {
    try {
      const p = await reauth(() => post<{ password: string }>(`/api/v1/hosts/${host}/vms/${name}/password`))
      io.typeOut(p.password + '\n')
      io.flash(t('vsc.typed_password'))
    } catch (e) { io.flash(errText(e)) }
  }
  return <ScreenWindow source={{
    name, sub: t('vsc.kind') + (ip ? ` · ${ip}` : ''),
    connect: () => post<{ key: string }>(`/api/v1/hosts/${encodeURIComponent(host)}/vms/${name}/screen`).then((k) => ({ path: `/api/v1/hosts/${encodeURIComponent(host)}/vm-screen/${k.key}` })),
    side: (io) => <>
      <Section title={t('vsc.machine')}>
        <Row k={t('vms.status')} v={inst ? t('vms.st_' + inst.status.toLowerCase()) : '—'} />
        <Row k={t('vms.address')} v={ip ? <>{ip} <span className="text-neutral-400">{t('vms.addr_' + (inst?.net?.mode === 'lan' ? 'lan' : 'nat'))}</span></> : (noAgent ? t('vms.addr_unknown') : '—')} />
        <Row k={t('vms.size')} v={inst ? `${inst.cpus ?? '—'} ${t('vms.cpu_unit')} · ${inst.memory ?? '—'}` : '—'} />
        {running && inst?.mem_used ? <Row k={t('vsc.mem_used')} v={bytes(inst.mem_used)} /> : null}
        {running && inst?.booted_at ? <Row k={t('vsc.up_for')} v={<Since from={inst.booted_at * 1000} />} /> : null}
        <div className="mt-2 flex flex-wrap gap-1">
          {running
            ? <>
              <Button size="sm" onClick={() => run('vm.restart', { name }, { host })}>{t('action.vm.restart')}</Button>
              <Button size="sm" onClick={() => run('vm.stop', { name }, { host })}>{t('action.vm.stop')}</Button>
              <Button size="sm" variant="ghost" onClick={() => run('vm.kill', { name }, { host })}>{t('action.vm.kill')}</Button>
            </>
            : inst && <Button size="sm" variant="primary" onClick={() => run('vm.start', { name }, { host, onDone: io.reconnect })}>{t('action.vm.start')}</Button>}
        </div>
        <div className="mt-1 text-[11.5px] text-neutral-400">{t('vsc.power_hint')}</div>
      </Section>
      <Section title={t('vsc.login')}>
        <div className="mb-1.5 text-[11.5px] text-neutral-400">{t('vsc.login_hint')}</div>
        <div className="flex flex-wrap gap-1">
          <Button size="sm" disabled={!io.connected} onClick={() => io.typeOut('root\n')}>{t('vsc.type_root')}</Button>
          {d.data?.has_password[name] && <Button size="sm" disabled={!io.connected} onClick={() => void typePassword(io)}>{t('vsc.type_password')}</Button>}
        </div>
      </Section>
      {noAgent && d.data?.agent_fix && (
        <Section title={t('vsc.fix')}>
          <div className="mb-1.5 text-[11.5px] text-amber-200/90">{t('vsc.fix_hint')}</div>
          <Button size="sm" variant="primary" disabled={!io.connected} onClick={() => { io.typeOut(d.data!.agent_fix! + '\n'); io.flash(t('vsc.typed_fix')) }}>{t('vsc.type_fix')}</Button>
        </Section>
      )}
    </>,
  }} />
}

// ScreenWindow: the screen filling a window, its toolbar and the side panel.
export function ScreenWindow({ source }: { source: ScreenSource }) {
  const t = useT()
  const name = source.name
  const box = useRef<HTMLDivElement>(null) // noVNC draws here
  const stage = useRef<HTMLDivElement>(null) // moves into the picture-in-picture window
  const rfb = useRef<RFB | null>(null)
  const [state, setState] = useState<State>('connecting')
  const [err, setErr] = useState('')
  const [attempt, setAttempt] = useState(0)
  const retries = useRef(0)
  const closing = useRef(false) // the person disconnected: do not come back by itself
  const [control, setControl] = useState(false)
  const controlRef = useRef(false)
  const [fit, setFit] = useState(true)
  const [panel, setPanel] = useState(() => localStorage.getItem(PANEL_KEY) !== '0')
  const [pip, setPip] = useState(false)
  const [text, setText] = useState('')
  const [note, setNote] = useState('')
  const canPip = 'documentPictureInPicture' in window

  useEffect(() => { document.title = `${name} · ${t('vsc.title')} · Berth` }, [name, t])
  useEffect(() => { localStorage.setItem(PANEL_KEY, panel ? '1' : '0') }, [panel])

  const take = (on: boolean) => {
    controlRef.current = on
    setControl(on)
    const r = rfb.current
    if (!r) return
    r.viewOnly = !on
    if (on) r.focus(); else r.blur()
  }

  // connect; after a restart (QEMU starts anew) or a dropped link, come back by itself
  useEffect(() => {
    if (!box.current || !name) return
    let gone = false
    closing.current = false
    setState('connecting')
    source.connect().then((k) => {
      if (gone || !box.current) return
      const url = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${k.path}`
      const r = new RFB(box.current, url, { wsProtocols: ['binary'], credentials: { password: k.password ?? '' } })
      r.addEventListener('credentialsrequired', () => r.sendCredentials({ password: k.password ?? '' }))
      r.viewOnly = !controlRef.current
      r.focusOnClick = false
      r.scaleViewport = fit
      r.background = '#000'
      r.addEventListener('connect', () => { retries.current = 0; setErr(''); setState('connected'); if (controlRef.current) r.focus() })
      r.addEventListener('disconnect', () => {
        rfb.current = null
        if (gone) return
        setState('closed')
        if (!closing.current && retries.current++ < 60) window.setTimeout(() => !gone && setAttempt((n) => n + 1), 3000)
      })
      rfb.current = r
    }).catch((e) => {
      if (gone) return
      setErr(errText(e)); setState('error')
      if (retries.current++ < 60) window.setTimeout(() => !gone && setAttempt((n) => n + 1), 3000) // a VM still starting
    })
    return () => { gone = true; rfb.current?.disconnect(); rfb.current = null }
  }, [name, attempt]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { if (rfb.current) { rfb.current.scaleViewport = fit; rfb.current.clipViewport = false } }, [fit])

  // a click on the screen takes keyboard and mouse; a click anywhere else gives them back
  // (native listeners: in picture-in-picture the screen lives in another document)
  useEffect(() => {
    const el = stage.current
    if (!el) return
    const onStage = () => { if (!controlRef.current) take(true) }
    const outside = (e: MouseEvent) => { if (controlRef.current && !el.contains(e.target as Node)) take(false) }
    // capture: noVNC stops the canvas's own mouse events before they bubble
    el.addEventListener('mousedown', onStage, true)
    document.addEventListener('mousedown', outside, true)
    return () => { el.removeEventListener('mousedown', onStage, true); document.removeEventListener('mousedown', outside, true) }
  }, [])

  // the panel's buttons send keys while the screen is only watched (clicking them handed the
  // keyboard back), and noVNC drops every key in view-only mode: lift it just while sending
  const withKeys = (fn: (r: RFB) => void) => {
    const r = rfb.current
    if (!r) return
    const was = r.viewOnly
    r.viewOnly = false
    try { fn(r) } finally { r.viewOnly = was }
  }
  const press = (keys: number[]) => withKeys((r) => {
    for (const k of keys) r.sendKey(k, null, true)
    for (const k of [...keys].reverse()) r.sendKey(k, null, false)
  })
  // type text key by key: consoles and login prompts take no paste
  const typeOut = (s: string) => withKeys((r) => {
    for (const ch of s) {
      if (ch === '\r') continue
      const cp = ch.codePointAt(0)!
      if (ch !== '\n' && ch !== '\t' && (cp < 0x20 || cp > 0x7e)) continue // a text console types ASCII only
      const k = ch === '\n' ? K.enter : ch === '\t' ? K.tab : cp
      const shift = SHIFTED.test(ch)
      if (shift) r.sendKey(K.shift, null, true)
      r.sendKey(k, null)
      if (shift) r.sendKey(K.shift, null, false)
    }
  })

  const flash = (s: string) => { setNote(s); window.setTimeout(() => setNote(''), 3000) }
  const shot = () => {
    const r = rfb.current
    if (!r) return
    const a = document.createElement('a')
    a.href = r.toDataURL('image/png')
    a.download = `${name}-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.png`
    a.click()
  }
  const full = () => { const el = stage.current; if (el) (document.fullscreenElement ? document.exitFullscreen() : el.requestFullscreen()).catch(() => {}) }
  const floatIt = async () => {
    const el = stage.current
    const home = el?.parentElement
    if (!el || !home) return
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const w: Window = await (window as any).documentPictureInPicture.requestWindow({ width: 720, height: 460 })
    for (const ss of Array.from(document.styleSheets)) {
      try {
        const s = w.document.createElement('style')
        s.textContent = Array.from(ss.cssRules).map((r) => r.cssText).join('\n')
        w.document.head.appendChild(s)
      } catch {
        if (ss.href) { const l = w.document.createElement('link'); l.rel = 'stylesheet'; l.href = ss.href; w.document.head.appendChild(l) }
      }
    }
    w.document.title = name
    w.document.body.style.cssText = 'margin:0;background:#000;height:100vh'
    w.document.body.append(el)
    const outside = (e: MouseEvent) => { if (controlRef.current && !el.contains(e.target as Node)) take(false) }
    w.document.addEventListener('mousedown', outside, true)
    setPip(true)
    w.addEventListener('pagehide', () => { home.append(el); setPip(false) })
  }
  const reconnect = () => { retries.current = 0; setErr(''); setAttempt((n) => n + 1) }
  const disconnect = () => { closing.current = true; take(false); rfb.current?.disconnect() }

  const io: ScreenIO = { typeOut, connected: state === 'connected', reconnect, flash }
  return (
    <div className="flex h-screen flex-col bg-black text-white">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-white/10 bg-neutral-900 px-3 text-[12.5px]">
        <span className={state === 'connected' ? 'text-emerald-400' : state === 'error' ? 'text-red-400' : 'text-neutral-400'}>●</span>
        <span className="font-semibold">{name}</span>
        <span className="hidden text-neutral-400 sm:inline">{source.sub}</span>
        <span className="text-neutral-400" data-screen-state={state}>{t('vsc.state_' + state)}</span>
        {state === 'connected' && (control
          ? <span className="rounded bg-emerald-500/20 px-1.5 py-0.5 text-[11.5px] text-emerald-300" data-control="on">{t('vsc.in_control')}</span>
          : <span className="rounded bg-white/10 px-1.5 py-0.5 text-[11.5px] text-neutral-300" data-control="off">{t('vsc.watching')}</span>)}
        {note && <span className="text-[11.5px] text-amber-300">{note}</span>}
        <span className="ml-auto flex items-center gap-1">
          <Button size="sm" variant="ghost" disabled={state !== 'connected'} onClick={() => press(KEYS[0].keys)}>Ctrl+Alt+Del</Button>
          <Button size="sm" variant="ghost" disabled={state !== 'connected'} title={t('vsc.shot')} aria-label={t('vsc.shot')} onClick={shot}><Camera className="size-4" /></Button>
          {canPip && <Button size="sm" variant="ghost" disabled={pip} title={t('vsc.pip')} aria-label={t('vsc.pip')} onClick={() => void floatIt()}><PictureInPicture2 className="size-4" /></Button>}
          <Button size="sm" variant="ghost" title={t('vsc.fullscreen')} aria-label={t('vsc.fullscreen')} onClick={full}><Maximize className="size-4" /></Button>
          {state === 'connected'
            ? <Button size="sm" variant="ghost" onClick={disconnect}>{t('vsc.disconnect')}</Button>
            : state !== 'connecting' && <Button size="sm" onClick={reconnect}><RotateCcw className="size-3.5" />{t('vsc.reconnect')}</Button>}
          <Button size="sm" variant="ghost" aria-pressed={panel} aria-label={t('rd.tools')} onClick={() => setPanel((p) => !p)}><PanelRight className="size-4" /></Button>
        </span>
      </div>
      <div className="flex min-h-0 flex-1">
        <div className="relative min-w-0 flex-1">
          {pip && <div className="absolute inset-0 flex items-center justify-center text-[13px] text-neutral-400">{t('vsc.in_pip')}</div>}
          <div ref={stage} className="absolute inset-0 bg-black" data-vnc-stage>
            <div ref={box} className={cx('absolute inset-0', !fit && 'overflow-auto')} data-vnc-screen />
            {state === 'connected' && !control && (
              <div className="pointer-events-none absolute inset-x-0 bottom-4 flex justify-center">
                <div className="flex items-center gap-2 rounded-full bg-neutral-900/85 px-3 py-1.5 text-[12px] text-neutral-200 shadow">
                  <MousePointerClick className="size-3.5" />{t('vsc.click_to_control')}
                </div>
              </div>
            )}
            {state !== 'connected' && (
              <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
                <div className="max-w-md rounded-lg bg-neutral-900/90 p-5 text-center text-[13px]">
                  <div className="font-semibold">{t('vsc.state_' + state)}</div>
                  {err && <div className="mt-2 text-red-300">{err}</div>}
                  {state !== 'connecting' && !closing.current && <div className="mt-2 text-neutral-400">{t('vsc.retrying')}</div>}
                </div>
              </div>
            )}
          </div>
        </div>
        {panel && (
          <aside className="w-80 shrink-0 space-y-4 overflow-y-auto border-l border-white/10 bg-neutral-900 p-3 text-[12.5px]">
            {source.side(io)}

            <Section title={t('rd.keys')}>
              <div className="grid grid-cols-4 gap-1">
                {KEYS.map((k) => (
                  <button key={k.id} type="button" disabled={state !== 'connected'} onClick={() => press(k.keys)} title={t('vsc.key_hint.' + k.id)}
                    className="num rounded border border-white/10 px-1 py-1 text-[11.5px] hover:border-white/30 disabled:opacity-40">{t('vsc.key.' + k.id)}</button>
                ))}
              </div>
              <div className="mt-2 mb-1 text-[11.5px] text-neutral-400">{t('vsc.tty')}</div>
              <div className="grid grid-cols-6 gap-1">
                {[1, 2, 3, 4, 5, 6].map((n) => (
                  <button key={n} type="button" disabled={state !== 'connected'} onClick={() => press([K.ctrl, K.alt, K.f1 + n - 1])} title={`Ctrl+Alt+F${n}`}
                    className="num rounded border border-white/10 py-1 text-[11.5px] hover:border-white/30 disabled:opacity-40">F{n}</button>
                ))}
              </div>
            </Section>

            <Section title={t('vsc.view')}>
              <label className="flex items-center gap-2"><input type="checkbox" checked={fit} onChange={(e) => setFit(e.target.checked)} />{t('rd.fit_window')}</label>
            </Section>

            <Section title={t('snip.title')}>
              <div className="dark h-96 overflow-hidden rounded-lg border border-white/10">
                <CommandLibrary onInsert={(s, go) => { typeOut(s + (go ? '\n' : '')); take(true) }} />
              </div>
            </Section>

            <Section title={t('rd.type_text')}>
              <div className="mb-1 text-[11.5px] text-neutral-400">{t('vsc.type_text_hint')}</div>
              <Textarea className="num h-16 bg-neutral-800 text-[12px] text-white" value={text} onChange={(e) => setText(e.target.value)} />
              <div className="mt-1.5 flex gap-1">
                <Button size="sm" disabled={!text || state !== 'connected'} onClick={() => typeOut(text)}>{t('rd.type_send')}</Button>
                <Button size="sm" variant="ghost" disabled={!text || state !== 'connected'} onClick={() => typeOut(text + '\n')}>{t('vsc.type_enter')}</Button>
              </div>
            </Section>
          </aside>
        )}
      </div>
    </div>
  )
}

export function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return <section><div className="mb-1.5 text-[11px] font-semibold uppercase tracking-wide text-neutral-400">{title}</div>{children}</section>
}
export function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return <div className="flex justify-between gap-3 py-0.5"><span className="shrink-0 text-neutral-400">{k}</span><span className="num truncate text-right">{v}</span></div>
}
function Since({ from }: { from: number }) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => { const i = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(i) }, [])
  const s = Math.max(0, Math.floor((now - from) / 1000))
  return <>{`${Math.floor(s / 3600)}:${String(Math.floor(s / 60) % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`}</>
}
