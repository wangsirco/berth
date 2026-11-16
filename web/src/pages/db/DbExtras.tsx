import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Switch } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

interface Remote { container: string; port: number; bind: string; addrs: string[] }

// Remote access: where the instance's port listens.
export function DbAccess({ id, engine }: { id: number; engine: string }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'db.manage')
  const r = useApi<Remote>(`/api/v1/hosts/${host}/databases/${id}/remote`, [id])
  const [bind, setBind] = useState('')
  useEffect(() => { if (r.data) setBind(r.data.bind) }, [r.data])
  if (r.error) return <div className="p-4"><Notice tone="warn">{r.error.message}</Notice></div>
  if (!r.data) return null
  const d = r.data
  const opts = [
    { v: '127.0.0.1', label: t('dbx.bind_local'), hint: t('dbx.bind_local_hint') },
    ...d.addrs.map((a) => ({ v: a, label: t('dbx.bind_lan', { addr: a }), hint: t('dbx.bind_lan_hint') })),
    { v: '0.0.0.0', label: t('dbx.bind_all'), hint: t('dbx.bind_all_hint') },
  ]
  const shown = bind === '0.0.0.0' ? (host === 'local' ? location.hostname : host) : bind
  const cli = engine === 'postgres' ? `psql -h ${shown} -p ${d.port} -U <user> <db>` : `mysql -h ${shown} -P ${d.port} -u <user> -p`
  return (
    <div className="space-y-3 p-4">
      <p className="text-[12.5px] text-mute">{t('dbx.access_hint')}</p>
      <div className="space-y-1.5">
        {opts.map((o) => (
          <label key={o.v} className={cx('flex cursor-pointer items-start gap-2.5 rounded-[var(--radius-box)] border px-3 py-2', bind === o.v ? 'border-acc' : 'border-line')}>
            <input type="radio" className="mt-1" name="bind" checked={bind === o.v} disabled={!manage} onChange={() => setBind(o.v)} />
            <div><div className="text-[13.5px]">{o.label}{d.bind === o.v && <span className="ml-2 text-[12px] text-mute">{t('dbx.current')}</span>}</div><div className="text-[12px] text-mute">{o.hint}</div></div>
          </label>
        ))}
      </div>
      {bind === '0.0.0.0' && <Notice tone="warn">{t('dbx.all_warn', { port: d.port })} <Link className="text-acc hover:underline" to={hostPath(host, 'firewall')}>{t('dbx.to_firewall')}</Link></Notice>}
      {bind !== '127.0.0.1' && <p className="text-[12px] text-mute">{t('dbx.connect')} <code className="num">{cli}</code></p>}
      {manage && <Button variant="primary" disabled={bind === d.bind} onClick={() => run('db.remote', { instance: id, bind }, { host, onDone: () => void r.reload() })}>{t('action.db.remote')}</Button>}
    </div>
  )
}

interface Slow { supported: boolean; on: boolean; seconds: number; items: { at: string; ms: number; rows?: number; db?: string; sql: string }[] }

// Slow query log: switch, threshold and the recent slow statements.
export function DbSlow({ id }: { id: number }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const s = useApi<Slow>(`/api/v1/hosts/${host}/databases/${id}/slowlog`, [id])
  const [secs, setSecs] = useState('1')
  useEffect(() => { if (s.data?.seconds) setSecs(String(s.data.seconds)) }, [s.data])
  if (s.error) return <div className="p-4"><Notice tone="warn">{s.error.message}</Notice></div>
  if (!s.data) return null
  const d = s.data
  const apply = (on: boolean) => run('db.slowlog', { instance: id, on, seconds: Number(secs) || 1 }, { host, onDone: () => void s.reload() })
  return (
    <div>
      <div className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-2.5">
        <Switch checked={d.on} disabled={!can(user, 'db.manage')} onChange={(on) => apply(on)} label={t('dbx.slow_on')} />
        <span className="text-[12.5px] text-mute">{t('dbx.slow_threshold')}</span>
        <Input type="number" min={0.01} step={0.1} className="w-24" value={secs} onChange={(e) => setSecs(e.target.value)} />
        <span className="text-[12.5px] text-mute">{t('dbx.seconds')}</span>
        {d.on && Number(secs) !== d.seconds && <Button size="sm" onClick={() => apply(true)}>{t('action.db.slowlog')}</Button>}
        <Button size="sm" variant="ghost" className="ml-auto" onClick={() => void s.reload()}>{t('common.refresh')}</Button>
      </div>
      {d.items.length === 0 && <p className="px-4 py-3 text-[12.5px] text-mute">{d.on ? t('dbx.slow_empty') : t('dbx.slow_off_hint')}</p>}
      <div className="text-[12px]">
        {d.items.map((x, i) => (
          <div key={i} className="border-b border-line px-4 py-2">
            <div className="num flex gap-3 text-mute"><span>{x.at.replace('T', ' ').slice(0, 19)}</span><span className="text-fg">{x.ms >= 1000 ? (x.ms / 1000).toFixed(2) + ' s' : Math.round(x.ms) + ' ms'}</span>{x.rows != null && <span>{t('dbx.rows', { n: x.rows })}</span>}{x.db && <span>{x.db}</span>}</div>
            <pre className="num mt-1 whitespace-pre-wrap break-all text-[11.5px]">{x.sql}</pre>
          </div>
        ))}
      </div>
    </div>
  )
}

const genDbPassword = () => {
  const abc = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'
  return Array.from(crypto.getRandomValues(new Uint32Array(20)), (x) => abc[x % abc.length]).join('')
}

// Reset the admin password of a database in a container, without the old one.
export function DbResetButton({ id, user: admin }: { id: number; user: string }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [open, setOpen] = useState(false)
  const [pw, setPw] = useState('')
  const ok = /^[A-Za-z0-9!@#%^&*()_+=.,:;~-]{8,64}$/.test(pw)
  return <>
    <Button size="sm" variant="ghost" onClick={() => { setPw(genDbPassword()); setOpen(true) }}>{t('action.db.root_reset')}</Button>
    {open && <Dialog open onOpenChange={(o) => !o && setOpen(false)} title={t('action.db.root_reset')} description={t('dbx.reset_hint', { user: admin })}
      footer={<><Button className="ml-auto" onClick={() => setOpen(false)}>{t('common.cancel')}</Button><Button variant="primary" disabled={!ok} onClick={() => { setOpen(false); run('db.root_reset', { instance: id, password: pw }, { host }) }}>{t('common.next')}</Button></>}>
      <Field label={t('dbx.new_password')} hint={t('dbx.new_password_hint')}><Input className="num" value={pw} autoComplete="off" onChange={(e) => setPw(e.target.value)} /></Field>
    </Dialog>}
  </>
}
