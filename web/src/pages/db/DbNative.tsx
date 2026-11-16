import { Database, HardDrive, ShieldCheck, Sparkles } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select, Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago, bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

export interface Instance { id: number; name: string; engine: string; container?: string; addr?: string; native?: string; user: string; status?: { version?: string; connections?: number; error?: boolean } }
interface Native { engine: string; version: string; port: number; service: string; active: boolean; data_dir: string; allow: string[]; values: Record<string, string>; mem_mb: number }

const ENGINE_NAME: Record<string, string> = { mariadb: 'MariaDB', mysql: 'MySQL', postgres: 'PostgreSQL', redis: 'Redis', sqlite: 'SQLite' }
const VERSION_NAME: Record<string, string> = { valkey: 'Valkey' }
export const engineName = (e: string) => ENGINE_NAME[e] ?? e

// ---- install ----
export function NativeInstallDialog({ onClose }: { onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const o = useApi<{ items: Native[]; versions: Record<string, string[]>; busy: Record<string, string>; mem_mb: number }>(`/api/v1/hosts/${host}/databases/native`, [host])
  const [engine, setEngine] = useState('mariadb')
  const [version, setVersion] = useState('')
  const vs = o.data?.versions[engine] ?? []
  useEffect(() => { setVersion(vs[0] ?? '') }, [engine, o.data]) // eslint-disable-line react-hooks/exhaustive-deps
  const fam = (e: string) => (e === 'mariadb' || e === 'mysql' ? 'my' : e)
  const have = o.data?.items.find((n) => fam(n.engine) === fam(engine))
  const port = engine === 'postgres' ? '5432' : engine === 'redis' ? '6379' : '3306'
  const busy = o.data?.busy[port]
  const cards = [
    { e: 'mariadb', hint: t('dbn.hint_mariadb') },
    { e: 'mysql', hint: t('dbn.hint_mysql') },
    { e: 'postgres', hint: t('dbn.hint_postgres') },
    { e: 'redis', hint: t('dbn.hint_redis') },
  ]
  return (
    <Dialog open onOpenChange={(x) => !x && onClose()} title={t('dbn.install_title')} description={t('dbn.install_desc')} wide
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!version || !!have || !!busy} onClick={() => run('db.native.install', { engine, version }, { host, onDone: onClose })}>{t('action.db.native.install')}</Button></>}>
      <div className="space-y-3 px-4 pb-3">
        <div className="grid gap-2 sm:grid-cols-2">
          {cards.map((c) => (
            <button key={c.e} onClick={() => setEngine(c.e)} className={cx('rounded-xl border p-3 text-left transition', engine === c.e ? 'border-acc bg-tint' : 'border-line hover:bg-soft')}>
              <div className="flex items-center gap-2 text-[14px] font-semibold"><Database className="size-4" />{engineName(c.e)}{c.e === 'mariadb' && <Pill tone="acc">{t('dbn.recommended')}</Pill>}</div>
              <div className="mt-1 text-[12px] text-mute">{c.hint}</div>
            </button>
          ))}
        </div>
        <Field label={t('dbn.version')}>
          <Select value={version} onChange={(e) => setVersion(e.target.value)} aria-label={t('dbn.version')}>
            {vs.map((v, i) => <option key={v} value={v}>{VERSION_NAME[v] ?? v}{i === 0 && vs.length > 1 ? ' · ' + t('dbn.default') : ''}</option>)}
          </Select>
        </Field>
        {have && <Notice tone="warn">{t('dbn.have', { engine: engineName(have.engine), version: have.version })}</Notice>}
        {busy && <Notice tone="warn">{t('dbn.port_taken', { port, by: busy })}</Notice>}
        <ul className="space-y-1 text-[12.5px] text-mute">
          <li className="flex gap-2"><Sparkles className="mt-0.5 size-3.5 shrink-0" />{t('dbn.point_native')}</li>
          <li className="flex gap-2"><ShieldCheck className="mt-0.5 size-3.5 shrink-0" />{t('dbn.point_safe', { port })}</li>
          <li className="flex gap-2"><HardDrive className="mt-0.5 size-3.5 shrink-0" />{t('dbn.point_tune')}</li>
        </ul>
      </div>
    </Dialog>
  )
}

// ---- overview: status and tuning ----
interface Stats { uptime?: number; connections?: number; max_connections?: number; qps?: number; hit_pct?: number; databases?: { rows: unknown[][] } }
interface Tune { native: Native; live: Record<string, string>; mem_mb: number; host_mem_mb: number; recommend: Record<string, string> }

const humanVal = (v?: string) => {
  if (v === undefined || v === '') return '—'
  if (/^\d{7,}$/.test(v)) return bytes(Number(v))
  return v
}
const dur = (s: number) => (s >= 86400 ? Math.floor(s / 86400) + 'd ' : '') + Math.floor((s % 86400) / 3600) + 'h ' + Math.floor((s % 3600) / 60) + 'm'

export function DbOverview({ d }: { d: Instance }) {
  const t = useT()
  const { host } = useHost()
  const s = useApi<Stats>(`/api/v1/hosts/${host}/databases/${d.id}/stats`, [d.id])
  useEvents(['change.done'], () => void s.reload())
  const st = s.data
  const tile = (label: string, v: React.ReactNode) => <div className="rounded-xl border border-line bg-canvas px-3 py-2"><div className="text-[12px] text-mute">{label}</div><div className="num text-[17px] font-semibold">{v}</div></div>
  return (
    <div className="space-y-4 p-4">
      {s.error && <Notice tone="warn">{s.error.message}</Notice>}
      {st && <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {tile(t('dbn.uptime'), st.uptime ? dur(Number(st.uptime)) : '—')}
        {tile(t('dbn.conns'), `${st.connections ?? '—'} / ${st.max_connections ?? '—'}`)}
        {tile(t('dbn.qps'), st.qps ?? '—')}
        {tile(t('dbn.hit'), st.hit_pct != null ? st.hit_pct + '%' : '—')}
      </div>}
      {d.native ? <TunePanel d={d} /> : <p className="text-[12.5px] text-mute">{t('dbn.tune_native_only')}</p>}
    </div>
  )
}

// friendly order of the settings; the rest follow alphabetically
const ORDER = ['maxmemory', 'maxmemory-policy', 'appendonly', 'innodb_buffer_pool_size', 'shared_buffers', 'effective_cache_size', 'max_connections', 'work_mem', 'maintenance_work_mem', 'innodb_log_file_size', 'innodb_redo_log_capacity',
  'tmp_table_size', 'max_heap_table_size', 'table_open_cache', 'thread_cache_size', 'max_allowed_packet', 'random_page_cost', 'slow_query_log', 'long_query_time', 'log_min_duration_statement']

function TunePanel({ d }: { d: Instance }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'db.manage')
  const [mem, setMem] = useState(0)
  const r = useApi<Tune>(`/api/v1/hosts/${host}/databases/${d.id}/tune${mem ? '?mem=' + mem : ''}`, [d.id, mem])
  const [vals, setVals] = useState<Record<string, string>>({})
  const [memText, setMemText] = useState('')
  useEvents(['change.done'], () => void r.reload())
  useEffect(() => {
    if (!r.data) return
    if (!memText) setMemText(String(r.data.mem_mb))
    if (Object.keys(vals).length === 0) setVals(Object.keys(r.data.native.values).length ? r.data.native.values : r.data.recommend)
  }, [r.data]) // eslint-disable-line react-hooks/exhaustive-deps
  if (r.error) return <Notice tone="warn">{r.error.message}</Notice>
  if (!r.data) return null
  const tn = r.data
  const keys = [...new Set([...Object.keys(tn.recommend), ...Object.keys(vals)])].sort((a, b) => (ORDER.indexOf(a) + 1 || 99) - (ORDER.indexOf(b) + 1 || 99) || a.localeCompare(b))
  const changed = JSON.stringify(vals) !== JSON.stringify(tn.native.values) || Number(memText) !== tn.native.mem_mb
  const recommend = async () => {
    const m = Number(memText) || tn.mem_mb
    setMem(m)
    const x = await get<Tune>(`/api/v1/hosts/${host}/databases/${d.id}/tune?mem=${m}`)
    setVals(x.recommend)
  }
  return (
    <Box title={t('dbn.tune')} right={<span className="text-[12px] text-mute">{engineName(tn.native.engine)} {tn.native.version} · {tn.native.active ? t('home.running') : t('dbn.stopped')}</span>}>
      <div className="flex flex-wrap items-end gap-2 border-b border-line px-4 py-3">
        <Field label={t('dbn.mem_for_db')} hint={t('dbn.mem_hint', { total: tn.host_mem_mb })}>
          <Input type="number" className="num w-32" min={256} step={256} value={memText} disabled={!manage} onChange={(e) => setMemText(e.target.value)} aria-label={t('dbn.mem_for_db')} />
        </Field>
        {manage && <Button onClick={() => void recommend()}><Sparkles className="size-3.5" />{t('dbn.fill_recommended')}</Button>}
      </div>
      <div className="grid grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(0,1fr)] gap-x-3 px-4 py-2 text-[12px] text-mute"><span>{t('dbn.setting')}</span><span>{t('dbn.running_now')}</span><span>{t('dbn.new_value')}</span></div>
      {keys.map((k) => (
        <div key={k} className="grid grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(0,1fr)] items-center gap-x-3 border-t border-line px-4 py-1.5 text-[13px]">
          <div className="min-w-0"><div className="truncate">{t('dbt.' + k)}</div><div className="num truncate text-[11.5px] text-mute">{k}</div></div>
          <span className="num truncate text-mute">{humanVal(tn.live[k])}</span>
          <Input className={cx('num h-8', vals[k] !== tn.recommend[k] && tn.recommend[k] && 'border-warn')} disabled={!manage} value={vals[k] ?? ''} placeholder={tn.recommend[k]} aria-label={k}
            onChange={(e) => setVals({ ...vals, [k]: e.target.value.trim() })} />
        </div>
      ))}
      {manage && <div className="flex items-center gap-2 border-t border-line px-4 py-2.5">
        <span className="text-[12px] text-mute">{t('dbn.tune_hint')}</span>
        <Button className="ml-auto" variant="primary" disabled={!changed} onClick={() => run('db.tune', { instance: d.id, mem_mb: Number(memText) || tn.mem_mb, values: Object.fromEntries(Object.entries(vals).filter(([, v]) => v)) }, { host })}>{t('action.db.tune')}</Button>
      </div>}
    </Box>
  )
}

// ---- remote access for a native server ----
export function NativeAccess({ d }: { d: Instance }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const r = useApi<Tune>(`/api/v1/hosts/${host}/databases/${d.id}/tune`, [d.id])
  const [text, setText] = useState('')
  useEffect(() => { if (r.data) setText(r.data.native.allow.join('\n')) }, [r.data])
  if (r.error) return <div className="p-4"><Notice tone="warn">{r.error.message}</Notice></div>
  if (!r.data) return null
  const n = r.data.native
  const list = text.split(/[\s,]+/).filter(Boolean)
  const ok = list.every((s) => /^[0-9a-fA-F:.]+(\/\d{1,3})?$/.test(s))
  const cli = n.engine === 'postgres' ? `psql -h <${t('dbn.this_server')}> -p ${n.port} -U <user> <db>` : n.engine === 'redis' ? `redis-cli -h <${t('dbn.this_server')}> -p ${n.port} --askpass` : `mysql -h <${t('dbn.this_server')}> -P ${n.port} -u <user> -p`
  return (
    <div className="space-y-3 p-4">
      <p className="text-[12.5px] text-mute">{t('dbn.access_hint', { port: n.port })}</p>
      <Field label={t('dbn.allow')} hint={t('dbn.allow_hint')}>
        <Textarea className="num h-24 text-[12.5px]" disabled={!can(user, 'db.manage')} value={text} onChange={(e) => setText(e.target.value)} placeholder={'10.0.2.20\n192.168.1.0/24'} aria-label={t('dbn.allow')} />
      </Field>
      {!ok && <p className="text-[12px] text-bad">{t('dbn.allow_bad')}</p>}
      {list.length > 0 && <p className="text-[12px] text-mute">{t('dbx.connect')} <code className="num">{cli}</code></p>}
      {can(user, 'db.manage') && <Button variant="primary" disabled={!ok || list.join(',') === n.allow.join(',')} onClick={() => run('db.native.allow', { instance: d.id, sources: list }, { host })}>{t('action.db.native.allow')}</Button>}
    </div>
  )
}

// ---- copy a database to another instance ----
export function CopyDbDialog({ d, db, all, onClose }: { d: Instance; db: string; all: Instance[]; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const fam = (e: string) => (e === 'mariadb' ? 'mysql' : e)
  const targets = all.filter((x) => x.id !== d.id && fam(x.engine) === fam(d.engine) && (x.container || x.native))
  const [to, setTo] = useState(targets.find((x) => x.native)?.id ?? targets[0]?.id ?? 0)
  const [as, setAs] = useState(db)
  return (
    <Dialog open onOpenChange={(x) => !x && onClose()} title={t('dbn.copy_title', { db })} description={t('dbn.copy_desc')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!to || !/^[A-Za-z0-9_]{1,63}$/.test(as)} onClick={() => run('db.copy', { from: d.id, to, database: db, as }, { host, onDone: onClose })}>{t('action.db.copy')}</Button></>}>
      <div className="space-y-3 px-4 pb-3">
        {targets.length === 0 ? <Notice tone="warn">{t('dbn.copy_none')}</Notice> : <>
          <Field label={t('dbn.copy_to')}>
            <Select value={to} onChange={(e) => setTo(Number(e.target.value))} aria-label={t('dbn.copy_to')}>
              {targets.map((x) => <option key={x.id} value={x.id}>{x.name} · {engineName(x.engine)}{x.native ? ' · ' + t('dbn.native') : ''}</option>)}
            </Select>
          </Field>
          <Field label={t('dbn.copy_as')}><Input className="num" value={as} onChange={(e) => setAs(e.target.value)} aria-label={t('dbn.copy_as')} /></Field>
        </>}
      </div>
    </Dialog>
  )
}

// ---- SQLite ----
interface Found { path: string; bytes: number; modified: number; owner: string; registered: boolean; project?: string; site?: string }

export function SqliteFound({ onAdd }: { onAdd: (path: string, name: string) => void }) {
  const t = useT()
  const { host } = useHost()
  const { user } = useAuth()
  const f = useApi<{ items: Found[] }>(`/api/v1/hosts/${host}/databases/sqlite/discover`, [host])
  useEvents(['change.done'], () => void f.reload())
  const items = (f.data?.items ?? []).filter((x) => !x.registered)
  if (items.length === 0) return null
  const nameOf = (x: Found) => ((x.project || x.site || '') + '-' + x.path.split('/').pop()!.replace(/\.[^.]+$/, '')).replace(/^-/, '').replace(/[^A-Za-z0-9_-]/g, '-').slice(0, 60)
  return (
    <Box title={t('sqlite.found')} count={items.length}>
      <p className="border-b border-line px-4 py-2 text-[12.5px] text-mute">{t('sqlite.found_hint')}</p>
      {items.map((x) => (
        <Row key={x.path}>
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-[#3a6f8f] text-[11px] font-bold text-white">SQL</span>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 text-[13.5px] font-semibold">{x.path.split('/').pop()}
              {x.project && <Pill>{t('sqlite.of_project', { p: x.project })}</Pill>}{x.site && <Pill>{t('sqlite.of_site', { s: x.site })}</Pill>}</div>
            <div className="num truncate text-[12px] text-mute">{x.path} · {bytes(x.bytes)} · {x.owner} · {ago(x.modified, t)}</div>
          </div>
          {can(user, 'db.manage') && <Button size="sm" onClick={() => onAdd(x.path, nameOf(x))}>{t('sqlite.register')}</Button>}
        </Row>
      ))}
    </Box>
  )
}

interface SqInfo { path: string; bytes: number; wal_bytes: number; page_size: number; journal_mode: string; version: string; owner: string; modified: number; tables: { name: string; rows: number }[]; backups: { name: string; bytes: number; at: number }[] }

export function SqliteOverview({ d }: { d: Instance }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'db.manage')
  const i = useApi<SqInfo>(`/api/v1/hosts/${host}/databases/${d.id}/sqlite`, [d.id])
  useEvents(['change.done'], () => void i.reload())
  if (i.error) return <div className="p-4"><Notice tone="bad">{i.error.message}</Notice></div>
  if (!i.data) return null
  const x = i.data
  const act = (action: string) => run('db.sqlite.maintain', { instance: d.id, action }, { host })
  return (
    <div className="space-y-4 p-4">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {[[t('sqlite.size'), bytes(x.bytes) + (x.wal_bytes ? ' + WAL ' + bytes(x.wal_bytes) : '')], [t('sqlite.journal'), x.journal_mode.toUpperCase()], [t('sqlite.owner'), x.owner], [t('sqlite.changed'), ago(x.modified, t)]].map(([k, v]) => (
          <div key={k} className="rounded-xl border border-line bg-canvas px-3 py-2"><div className="text-[12px] text-mute">{k}</div><div className="num truncate text-[15px] font-semibold">{v}</div></div>
        ))}
      </div>
      <p className="num break-all text-[12px] text-mute">{x.path} · SQLite {x.version}</p>
      {manage && <div className="flex flex-wrap gap-2">
        <Button onClick={() => act('check')}>{t('sqlite.check')}</Button>
        <Button onClick={() => act('backup')}>{t('sqlite.backup')}</Button>
        <Button onClick={() => act('vacuum')}>{t('sqlite.vacuum')}</Button>
        {x.journal_mode !== 'wal' && <Button onClick={() => act('wal')}>{t('sqlite.to_wal')}</Button>}
      </div>}
      <Box title={t('sqlite.tables')} count={x.tables.length}>
        {x.tables.map((tb) => <div key={tb.name} className="flex justify-between border-t border-line px-4 py-1.5 text-[13px] first:border-t-0"><span className="num">{tb.name}</span><span className="num text-mute">{t('sqlite.rows', { n: tb.rows })}</span></div>)}
      </Box>
      <Box title={t('sqlite.backups')} count={x.backups.length}>
        <p className="border-b border-line px-4 py-2 text-[12px] text-mute">{t('sqlite.backups_hint')}</p>
        {x.backups.map((b) => (
          <div key={b.name} className="flex items-center gap-2 border-t border-line px-4 py-1.5 text-[13px] first:border-t-0">
            <span className="num flex-1">{b.name.replace(/\.db$/, '')}</span><span className="num text-mute">{bytes(b.bytes)}</span>
            {manage && <Button size="sm" variant="ghost" onClick={() => run('db.sqlite.restore', { instance: d.id, backup: b.name }, { host, confirmName: d.name })}>{t('action.db.sqlite.restore')}</Button>}
          </div>
        ))}
      </Box>
    </div>
  )
}
