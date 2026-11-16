import Guacamole from 'guacamole-common-js'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Field'
import { ApiError, post } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { AuthCard } from '../auth/AuthCard'

interface Open { name: string; control: boolean; by: string; expires_at: number; password: boolean; ticket?: string }

// /shared#CODE: someone shared a remote desktop (or a VM's desktop) with a link. No account:
// the link (and its password, if set) is the key. Watch-only links send no input.
export default function SharedViewer() {
  const t = useT()
  const code = location.hash.slice(1)
  const [info, setInfo] = useState<Open | null>(null)
  const [bad, setBad] = useState(code === '' || code === 'invalid')
  const [pw, setPw] = useState('')
  const [err, setErr] = useState('')
  const [state, setState] = useState<'idle' | 'connecting' | 'connected' | 'closed' | 'error'>('idle')
  const box = useRef<HTMLDivElement>(null)
  const open = async (password = '') => {
    setErr('')
    try {
      const r = await post<Open>(`/api/v1/share/${encodeURIComponent(code)}/open`, { password })
      setInfo(r)
    } catch (e) {
      if ((e as ApiError).status === 404) setBad(true)
      else setErr((e as ApiError).message)
    }
  }
  useEffect(() => { if (!bad) void open() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (info) document.title = `${info.name} · Berth` }, [info])
  const ticket = info?.ticket
  useEffect(() => {
    const el = box.current
    if (!el || !ticket || !info) return
    setState('connecting')
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    // the tunnel appends "?" + the connect data to its URL, so the ticket travels in there
    const c = new Guacamole.Client(new Guacamole.WebSocketTunnel(`${proto}//${location.host}/api/v1/share/tunnel`))
    const display = c.getDisplay()
    el.innerHTML = ''
    el.appendChild(display.getElement())
    const fit = () => { const w = display.getWidth(), h = display.getHeight(); if (w && h) display.scale(Math.min(el.clientWidth / w, el.clientHeight / h, 1)) }
    display.onresize = fit
    c.onstatechange = (s: number) => { if (s === 3) setState('connected'); if (s === 5) setState((o) => (o === 'error' ? o : 'closed')) }
    c.onerror = (st: { message: string }) => { setErr(st.message); setState('error') }
    let kb: { onkeydown: unknown; onkeyup: unknown; reset: () => void } | null = null
    if (info.control) {
      const mouse = new Guacamole.Mouse(display.getElement())
      mouse.onEach(['mousedown', 'mouseup', 'mousemove'], (e: { state: unknown }) => c.sendMouseState(e.state, true))
      const k = new Guacamole.Keyboard(document)
      k.onkeydown = (code: number) => { c.sendKeyEvent(1, code); return false }
      k.onkeyup = (code: number) => c.sendKeyEvent(0, code)
      kb = k
    }
    const ro = new ResizeObserver(fit)
    ro.observe(el)
    c.connect(`ticket=${ticket}&width=${el.clientWidth}&height=${el.clientHeight}&dpi=${Math.round(96 * (window.devicePixelRatio || 1))}`)
    return () => { ro.disconnect(); if (kb) { kb.onkeydown = kb.onkeyup = null; kb.reset() } c.disconnect() }
  }, [ticket]) // eslint-disable-line react-hooks/exhaustive-deps

  if (bad) return <AuthCard title={t('shr.bad_title')} sub={t('shr.bad_sub')}><span /></AuthCard>
  if (info && info.password && !info.ticket) {
    const submit = (e: FormEvent) => { e.preventDefault(); void open(pw) }
    return (
      <AuthCard title={info.name} sub={t('shr.from', { by: info.by })}>
        <form onSubmit={submit} className="space-y-3">
          {err && <Notice tone="bad">{err}</Notice>}
          <Input type="password" aria-label={t('shr.password')} placeholder={t('shr.password')} value={pw} onChange={(e) => setPw(e.target.value)} autoFocus />
          <Button type="submit" variant="primary" className="w-full" disabled={!pw}>{t('shr.enter')}</Button>
        </form>
      </AuthCard>
    )
  }
  return (
    <div className="flex h-screen flex-col bg-black text-white">
      <div className="flex items-center gap-3 bg-[#111] px-3 py-1.5 text-[12.5px]">
        <b>{info?.name ?? '…'}</b>
        {info && <span className="text-white/60">{t('shr.from', { by: info.by })} · {t(info.control ? 'shr.control' : 'shr.watch')} · {t('shr.until', { at: new Date(info.expires_at * 1000).toLocaleString() })}</span>}
        <span className="ml-auto text-white/60">{t('shr.state_' + state)}</span>
        {(state === 'closed' || state === 'error') && <Button size="sm" onClick={() => void open()}>{t('shr.again')}</Button>}
      </div>
      {err && state === 'error' && <div className="bg-bad/30 px-3 py-1 text-[12.5px]">{err}</div>}
      <div ref={box} className="min-h-0 flex-1 overflow-hidden" />
    </div>
  )
}
