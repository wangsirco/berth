import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Dot, Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Field, Input, Switch } from '@/components/ui/Field'
import { bytes, clock } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { can, useAuth } from '@/lib/auth'
import { AvScanButton } from '../files/AvScan'
import { SftpDialog } from '../files/Sftp'
import type { Site } from './types'

interface Line { ts: number; ip: string; method: string; uri: string; status: number; size: number; ms: number; ua?: string; referer?: string }

// The site's access log, newest first, with a filter.
export function SiteLog({ id }: { id: number }) {
  const t = useT()
  const { host } = useHost()
  const [q, setQ] = useState('')
  const [live, setLive] = useState(true)
  const d = useApi<{ items: Line[] }>(`/api/v1/hosts/${host}/sites/${id}/log?lines=300&q=${encodeURIComponent(q)}`, [id, q])
  useInterval(() => void d.reload(), live ? 5000 : null)
  const items = d.data?.items ?? []
  return (
    <div>
      <div className="flex items-center gap-2 border-b border-line px-4 py-2">
        <Input className="flex-1" placeholder={t('sitelog.filter')} value={q} onChange={(e) => setQ(e.target.value)} />
        <Switch checked={live} onChange={setLive} label={t('system.live')} />
      </div>
      {items.length === 0 && <p className="px-4 py-3 text-[12.5px] text-mute">{t('sitelog.empty')}</p>}
      <div className="num text-[11.5px]">
        {items.map((l, i) => (
          <div key={i} className="flex items-center gap-2 border-b border-line px-4 py-1" title={[l.ua, l.referer].filter(Boolean).join('\n')}>
            <span className="w-14 shrink-0 text-mute">{clock(l.ts)}</span>
            <Dot state={l.status >= 500 ? 'bad' : l.status >= 400 ? 'warn' : 'ok'} />
            <span className="w-8 shrink-0">{l.status}</span>
            <span className="w-11 shrink-0 text-mute">{l.method}</span>
            <span className="min-w-0 flex-1 truncate">{l.uri}</span>
            <span className="w-28 shrink-0 truncate text-mute max-sm:hidden">{l.ip}</span>
            <span className="w-14 shrink-0 text-right text-mute max-sm:hidden">{bytes(l.size)}</span>
            <span className="w-12 shrink-0 text-right text-mute">{l.ms}ms</span>
          </div>
        ))}
      </div>
    </div>
  )
}

// Protection: hotlink, tamper protection, and a one-click backup.
export function SiteProtect({ s, onDone }: { s: Site; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const nav = useNavigate()
  const { host } = useHost()
  const files = s.target.type === 'static' || s.target.type === 'php'
  const { user } = useAuth()
  const sftpOK = files && can(user, 'system.manage')
  const [sftp, setSftp] = useState(false)
  const h = s.options.hotlink
  const [hl, setHl] = useState({ on: !!h, exts: (h?.exts ?? ['jpg', 'jpeg', 'png', 'gif', 'webp', 'mp4']).join(' '), allow: (h?.allow ?? []).join(' '), empty: h?.allow_empty ?? true })
  const tp = useApi<{ on: boolean; writable: string }>(files ? `/api/v1/hosts/${host}/sites/${s.id}/tamper` : null, [s.id])
  const [writable, setWritable] = useState('')
  useEffect(() => {
    if (!tp.data) return
    let w: string[] = []
    try { w = JSON.parse(tp.data.writable || '[]') } catch { /* none yet */ }
    setWritable(w.length ? w.join(' ') : s.target.type === 'php' ? 'wp-content/uploads wp-content/cache' : '')
  }, [tp.data, s.target.type])
  const saveHotlink = () => run('site.update', { ...s, options: { ...s.options, hotlink: hl.on ? { exts: hl.exts.split(/[\s,]+/).filter(Boolean), allow: hl.allow.split(/[\s,]+/).filter(Boolean), allow_empty: hl.empty } : undefined } }, { host, onDone })
  const words = (x: string) => x.split(/[\s,]+/).filter(Boolean)
  return (
    <div className="space-y-5 p-4 text-[13px]">
      <section className="space-y-2">
        <h3 className="font-semibold">{t('protect.hotlink')}</h3>
        <p className="text-[12px] text-mute">{t('protect.hotlink_hint')}</p>
        <Switch checked={hl.on} onChange={(v) => setHl({ ...hl, on: v })} label={t('protect.hotlink_on')} />
        {hl.on && <>
          <Field label={t('protect.exts')}><Input className="num" value={hl.exts} onChange={(e) => setHl({ ...hl, exts: e.target.value })} /></Field>
          <Field label={t('protect.allow')} hint={t('protect.allow_hint')}><Input className="num" value={hl.allow} placeholder="*.example.com" onChange={(e) => setHl({ ...hl, allow: e.target.value })} /></Field>
          <Switch checked={hl.empty} onChange={(v) => setHl({ ...hl, empty: v })} label={t('protect.allow_empty')} />
        </>}
        <Button size="sm" onClick={saveHotlink}>{t('common.apply')}</Button>
      </section>
      {files && <section className="space-y-2">
        <h3 className="flex items-center gap-2 font-semibold">{t('protect.tamper')}{tp.data?.on && <Dot state="ok" />}</h3>
        <p className="text-[12px] text-mute">{t('protect.tamper_hint')}</p>
        <Field label={t('protect.writable')} hint={t('protect.writable_hint')}><Input className="num" value={writable} onChange={(e) => setWritable(e.target.value)} /></Field>
        <div className="flex gap-2">
          <Button size="sm" variant={tp.data?.on ? 'default' : 'primary'} onClick={() => run('site.tamper', { id: s.id, on: true, writable: words(writable) }, { host, onDone: () => void tp.reload() })}>{tp.data?.on ? t('protect.tamper_update') : t('protect.tamper_on')}</Button>
          {tp.data?.on && <Button size="sm" onClick={() => run('site.tamper', { id: s.id, on: false }, { host, onDone: () => void tp.reload() })}>{t('protect.tamper_off')}</Button>}
        </div>
        {tp.data?.on && <Notice tone="warn">{t('protect.tamper_note')}</Notice>}
      </section>}
      {sftpOK && <section className="space-y-2">
        <h3 className="font-semibold">{t('protect.av')}</h3>
        <p className="text-[12px] text-mute">{t('protect.av_hint')}</p>
        <AvScanButton paths={['/srv/berth/caddy/sites/' + s.domains[0]]} label={t('protect.av_btn')} />
      </section>}
      {sftpOK && <section className="space-y-2">
        <h3 className="font-semibold">{t('protect.sftp')}</h3>
        <p className="text-[12px] text-mute">{t('protect.sftp_hint')}</p>
        <Button size="sm" onClick={() => setSftp(true)}>{t('protect.sftp_btn')}</Button>
        {sftp && <SftpDialog initial={{ dir: '/srv/berth/caddy/sites/' + s.domains[0], name: s.domains[0].split('.')[0].toLowerCase().replace(/[^a-z0-9_-]/g, '').replace(/^[^a-z]+/, '').slice(0, 30) }} onClose={() => setSftp(false)} />}
      </section>}
      {files && <section className="space-y-2">
        <h3 className="font-semibold">{t('protect.backup')}</h3>
        <p className="text-[12px] text-mute">{t('protect.backup_hint')}</p>
        <Button size="sm" onClick={() => nav(hostPath(host, 'backup') + '?new=1&name=' + encodeURIComponent('site-' + s.domains[0]) + '&path=' + encodeURIComponent('/srv/berth/caddy/sites/' + s.domains[0]))}>{t('protect.backup_btn')}</Button>
      </section>}
    </div>
  )
}
