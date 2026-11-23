import { useEffect, useRef, useState } from 'react'
import { ExternalLink, Globe, House } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { useT } from '@/lib/i18n'
import { reachable } from './MainName'

// OpenPeer: open the other panel (Wharf, a linked panel) on the right name. Opened here by a
// LAN name or address: its LAN name. Opened by the public name: its LAN name only if this
// device reaches it (at home), else the public one. When that is not clear, the person picks,
// and can have this device remember it.

type Via = 'lan' | 'public'

const onLAN = () => {
  const h = location.hostname
  return /\.(local|lan|home\.arpa|internal)$/.test(h) || /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|127\.|\[?fd|\[?fe80)/.test(h) || h === 'localhost'
}

export function OpenPeer({ id, lan, pub, open, label, size = 'sm', variant = 'primary' }: {
  id: string // remembers the choice per peer on this device
  lan?: string
  pub?: string
  open: (via: Via, base: string) => Promise<string | void> | string | void // a (sign-in) URL for that name; nothing: the name itself
  label?: string
  size?: 'sm' | 'md'
  variant?: 'primary' | 'default' | 'ghost'
}) {
  const t = useT()
  const [menu, setMenu] = useState<Via | null>(null) // the recommended one, when asking
  const [remember, setRemember] = useState(false)
  const [busy, setBusy] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const key = 'berth.open.' + id
  useEffect(() => {
    if (!menu) return
    const off = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setMenu(null) }
    document.addEventListener('mousedown', off)
    return () => document.removeEventListener('mousedown', off)
  }, [menu])
  const go = async (via: Via) => {
    const base = via === 'lan' ? lan : pub
    if (!base) return
    if (remember) localStorage.setItem(key, via)
    setMenu(null)
    const w = window.open('about:blank', '_blank') // before the await: not a blocked pop-up
    try {
      const u = (await open(via, base)) || base
      if (w) w.location.href = u
      else location.href = u
    } catch { w?.close() }
  }
  const click = async () => {
    const saved = localStorage.getItem(key) as Via | null
    if (saved && (saved === 'lan' ? lan : pub)) return go(saved)
    if (!lan || !pub) return go(lan ? 'lan' : 'public') // only one name: that one
    if (onLAN()) return go('lan')
    setBusy(true)
    const home = await reachable(lan) // opened from outside: at home after all?
    setBusy(false)
    if (!home) return go('public')
    setMenu('lan') // outside by the address bar, inside by the network: ask
  }
  const opt = (via: Via) => {
    const base = via === 'lan' ? lan : pub
    if (!base) return null
    return (
      <button type="button" onClick={() => void go(via)} className={cx('flex w-full items-start gap-2 rounded-[var(--radius-box)] px-2.5 py-2 text-left hover:bg-soft', menu === via && 'bg-acc/10')} data-open-via={via}>
        {via === 'lan' ? <House className="mt-0.5 size-4 shrink-0 text-acc" /> : <Globe className="mt-0.5 size-4 shrink-0 text-acc" />}
        <span className="min-w-0">
          <span className="block text-[13px] font-semibold">{t(via === 'lan' ? 'openpeer.lan' : 'openpeer.public')}{menu === via && <span className="ml-1.5 text-[11px] font-normal text-acc">{t('openpeer.recommended')}</span>}</span>
          <span className="num block truncate text-[12px] text-mute">{base}</span>
        </span>
      </button>
    )
  }
  return (
    <div ref={box} className="relative inline-block" data-open-peer={id}>
      <Button size={size} variant={variant} disabled={busy} onClick={() => void click()}><ExternalLink className="size-3.5" />{label ?? t('openpeer.open')}</Button>
      {menu && (
        <div role="menu" className="absolute right-0 z-30 mt-1 w-80 rounded-[var(--radius-box)] border border-line bg-card p-1.5 shadow-lg">
          <p className="px-2.5 pb-1 pt-1 text-[12px] text-mute">{t('openpeer.ask')}</p>
          {menu === 'lan' ? <>{opt('lan')}{opt('public')}</> : <>{opt('public')}{opt('lan')}</>}
          <label className="flex items-center gap-2 px-2.5 py-1.5 text-[12px] text-mute"><input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />{t('openpeer.remember')}</label>
        </div>
      )}
    </div>
  )
}
