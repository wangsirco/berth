import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented, Select, Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { DbAccess, DbResetButton, DbSlow } from './DbExtras'
import { CopyDbDialog, DbOverview, engineName, NativeAccess, NativeInstallDialog, SqliteFound, SqliteOverview, type Instance } from './DbNative'

interface Candidate { container: string; image: string; engine: string; user?: string; has_password: boolean; path?: string; name?: string }
interface Result { columns: string[]; rows: unknown[][]; affected: number; truncated: boolean; ms: number }

const genPassword = () => Array.from(crypto.getRandomValues(new Uint8Array(18)), (b) => 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789'[b % 57]).join('')

export default function DatabasesPage() {
  const t = useT()
  const { user } = useAuth()
  const { host } = useHost()
  const list = useApi<{ items: Instance[]; candidates: Candidate[] }>(`/api/v1/hosts/${host}/databases`, [host])
  const [add, setAdd] = useState<Partial<Candidate> | null>(null)
  const [open, setOpen] = useState<Instance | null>(null)
  const [install, setInstall] = useState(false)
  useEvents(['change.done'], () => void list.reload())
  const manage = can(user, 'db.manage')
  return (
    <div className="space-y-3">
      {list.error && <Notice tone="bad">{list.error.message}</Notice>}
      <Box title={t('nav.db')} count={list.data?.items.length} right={manage && <div className="flex gap-1.5">
        <Button size="sm" variant="primary" onClick={() => setInstall(true)}>{t('dbn.install_btn')}</Button>
        <Button size="sm" onClick={() => setAdd({ engine: 'sqlite' })}>{t('sqlite.add')}</Button>
        <Button size="sm" variant="ghost" onClick={() => setAdd({})}>{t('db.add_external')}</Button></div>}>
        {list.data?.items.length === 0 && <Empty title={t('db.none')}>{t('db.none_hint')}</Empty>}
        {list.data?.items.map((d) => (
          <Row key={d.id} onClick={() => setOpen(d)}>
            <Dot state={d.status?.error ? 'bad' : 'ok'} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 text-[13.5px] font-semibold">{d.name} <span className="font-normal text-mute">· {engineName(d.engine)}</span>
                {d.native && <Pill tone="acc">{t('dbn.native')}</Pill>}{d.container && <Pill>{t('dbn.container')}</Pill>}</div>
              <div className="num truncate text-[12px] text-mute">{d.container ? t('db.in_container', { c: d.container }) : d.addr}{d.status?.version ? ' · ' + String(d.status.version).slice(0, 40) : ''}{d.status?.connections !== undefined ? ' · ' + t('db.connections', { n: d.status.connections }) : ''}</div>
            </div>
          </Row>
        ))}
      </Box>
      {(list.data?.candidates.length ?? 0) > 0 && (
        <Box title={t('db.found')} count={list.data!.candidates.length}>
          {list.data!.candidates.map((c) => (
            <Row key={c.container}>
              <Dot state="off" />
              <div className="min-w-0 flex-1"><div className="text-[13.5px] font-semibold">{c.container} <span className="font-normal text-mute">· {c.engine}</span></div><div className="truncate text-[12px] text-mute">{c.image}{c.has_password ? ' · ' + t('db.from_store') : ''}</div></div>
              {manage && <Button size="sm" onClick={() => setAdd(c)}>{t('action.db.instance.add')}</Button>}
            </Row>
          ))}
        </Box>
      )}
      <SqliteFound onAdd={(path, name) => setAdd({ engine: 'sqlite', container: '', path, name } as Partial<Candidate>)} />
      {add && <AddDialog c={add} onClose={() => setAdd(null)} onDone={() => void list.reload()} />}
      {install && <NativeInstallDialog onClose={() => setInstall(false)} />}
      {open && <InstanceDrawer d={open} all={list.data?.items ?? []} onClose={() => setOpen(null)} />}
    </div>
  )
}

function AddDialog({ c, onClose, onDone }: { c: Partial<Candidate>; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [f, setF] = useState({ name: c.name ?? (c.container ?? '').replace(/[^A-Za-z0-9_-]/g, '-'), engine: c.engine ?? 'postgres', addr: c.path ?? '', user: c.user ?? (c.engine === 'postgres' ? 'postgres' : c.engine === 'redis' || c.engine === 'sqlite' ? '' : 'root'), password: '' })
  const sqlite = f.engine === 'sqlite'
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('action.db.instance.add')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" onClick={() => run('db.instance.add', { ...f, container: c.container, addr: c.container ? '' : f.addr }, { host, onDone: () => { onDone(); onClose() } })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label={t('db.name')}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
          <Field label={t('db.engine')}><Select value={f.engine} disabled={!!c.engine} onChange={(e) => setF({ ...f, engine: e.target.value })}>{['postgres', 'mysql', 'mariadb', 'redis', 'sqlite'].map((x) => <option key={x} value={x}>{engineName(x)}</option>)}</Select></Field>
        </div>
        {!c.container && (sqlite
          ? <Field label={t('sqlite.path')} hint={t('sqlite.path_hint')}><Input className="num" value={f.addr} onChange={(e) => setF({ ...f, addr: e.target.value })} placeholder="/srv/berth/projects/web/shared/data/app.db" /></Field>
          : <Field label={t('db.addr')} hint={t('db.addr_hint')}><Input value={f.addr} onChange={(e) => setF({ ...f, addr: e.target.value })} placeholder="10.0.2.20:5432" /></Field>)}
        {!sqlite && <div className="grid gap-2 sm:grid-cols-2">
          <Field label={t('db.admin_user')}><Input value={f.user} onChange={(e) => setF({ ...f, user: e.target.value })} /></Field>
          <Field label={t('db.admin_password')} hint={c.has_password ? t('db.store_pw_hint') : undefined}><Input type="password" autoComplete="off" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
        </div>}
        <p className="text-[12px] text-mute">{sqlite ? t('sqlite.add_note') : t('db.sealed_note')}</p>
      </div>
    </Dialog>
  )
}

type Tab = 'overview' | 'dbs' | 'users' | 'query' | 'access' | 'slow'

function InstanceDrawer({ d, all, onClose }: { d: Instance; all: Instance[]; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const redis = d.engine === 'redis'
  const sqlite = d.engine === 'sqlite'
  const [tab, setTab] = useState<Tab>(redis ? 'query' : sqlite ? 'overview' : 'dbs')
  const tabs: { value: Tab; label: string }[] = redis
    ? [{ value: 'query', label: t('db.tab_query') }, { value: 'overview', label: t('dbn.tab_overview') }, ...(d.native ? [{ value: 'access' as const, label: t('dbx.tab_access') }] : [])]
    : sqlite
    ? [{ value: 'overview', label: t('dbn.tab_overview') }, { value: 'query', label: t('db.tab_query') }]
    : [{ value: 'overview', label: t('dbn.tab_overview') }, { value: 'dbs', label: t('db.tab_dbs') }, { value: 'users', label: t('db.tab_users') }, { value: 'query', label: t('db.tab_query') },
      ...(d.container || d.native ? [{ value: 'access' as const, label: t('dbx.tab_access') }] : []), { value: 'slow', label: t('dbx.tab_slow') }]
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={`${d.name} · ${engineName(d.engine)}${d.native ? ' ' + d.native.split(' ')[1] : ''}`} className="sm:w-[min(980px,95vw)]"
      right={can(user, 'db.manage') && <>{(d.container || d.native) && !redis && !sqlite && <DbResetButton id={d.id} user={d.user} />}
        {d.native
          ? <Button size="sm" variant="ghost" onClick={() => run('db.native.remove', { instance: d.id }, { host, onDone: onClose })}>{t('action.db.native.remove')}</Button>
          : <Button size="sm" variant="ghost" onClick={() => run('db.instance.remove', { instance: d.id }, { host, onDone: onClose })}>{t('action.db.instance.remove')}</Button>}</>}>
      {(!redis || d.native) && <div className="border-b border-line px-4 py-2"><Segmented value={tab} onChange={setTab} options={tabs} /></div>}
      {tab === 'overview' && (sqlite ? <SqliteOverview d={d} /> : <DbOverview d={d} />)}
      {tab === 'dbs' && <DbList d={d} all={all} />}
      {tab === 'users' && <Users d={d} />}
      {tab === 'query' && (redis ? <RedisView d={d} /> : <Query d={d} />)}
      {tab === 'access' && (d.native ? <NativeAccess d={d} /> : <DbAccess id={d.id} engine={d.engine} />)}
      {tab === 'slow' && <DbSlow id={d.id} />}
    </Drawer>
  )
}

function useMeta(d: Instance, what: string, database = '', table = '') {
  const { host } = useHost()
  const [res, setRes] = useState<Result | null>(null)
  const [err, setErr] = useState('')
  const load = () => post<Result>(`/api/v1/hosts/${host}/databases/${d.id}/meta`, { database, what, table }).then((r) => { setRes(r); setErr('') }).catch((e) => setErr(e.message))
  useEffect(() => { void load() }, [d.id, what, database, table]) // eslint-disable-line react-hooks/exhaustive-deps
  return { res, err, reload: load }
}

function DbList({ d, all }: { d: Instance; all: Instance[] }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const m = useMeta(d, 'databases')
  const [name, setName] = useState('')
  const [imp, setImp] = useState('')
  const [impPath, setImpPath] = useState('')
  const [exported, setExported] = useState('')
  const [copy, setCopy] = useState('')
  useEvents(['change.done'], () => void m.reload())
  return (
    <div className="space-y-2 p-4">
      {m.err && <Notice tone="bad">{m.err}</Notice>}
      {m.res?.rows.map((r) => (
        <div key={String(r[0])} className="flex items-center gap-2 border-b border-line py-1 text-[13px]">
          <span className="num flex-1">{String(r[0])}</span><span className="num text-mute">{bytes(Number(r[1] ?? 0))}</span>
          {(d.container || d.native) && d.engine !== 'redis' && <>
            <Button size="sm" variant="ghost" onClick={() => setCopy(String(r[0]))}>{t('dbn.copy')}</Button>
            <Button size="sm" variant="ghost" onClick={() => setImp(String(r[0]))}>{t('db.import')}</Button>
            <Button size="sm" variant="ghost" onClick={() => run('db.export', { instance: d.id, database: String(r[0]) }, { host, onDone: (c) => setExported((c.result as { path?: string })?.path ?? '') })}>{t('db.export')}</Button>
          </>}
          <Button size="sm" variant="ghost" onClick={() => run('db.drop', { instance: d.id, name: String(r[0]) }, { host, confirmName: String(r[0]) })}>{t('common.delete')}</Button>
        </div>
      ))}
      {copy && <CopyDbDialog d={d} db={copy} all={all} onClose={() => setCopy('')} />}
      {exported && <Notice><span className="num break-all">{exported}</span> <a className="text-acc hover:underline" href={`/api/v1/hosts/${host}/files/download?path=${encodeURIComponent(exported)}`}>{t('db.download')}</a></Notice>}
      {imp && <div className="flex flex-wrap items-center gap-2 rounded-[var(--radius-box)] border border-line p-2 text-[13px]">
        <span>{t('db.import_into', { db: imp })}</span>
        <Input className="num flex-1" value={impPath} onChange={(e) => setImpPath(e.target.value)} placeholder="/root/backup.sql.gz" aria-label={t('db.import_path')} />
        <Button size="sm" variant="primary" disabled={!/\.sql(\.gz)?$/.test(impPath)} onClick={() => run('db.import', { instance: d.id, database: imp, path: impPath }, { host, onDone: () => setImp('') })}>{t('common.continue')}</Button>
        <Button size="sm" onClick={() => setImp('')}>{t('common.cancel')}</Button>
        <p className="w-full text-[12px] text-mute">{t('db.import_hint')}</p>
      </div>}
      <div className="flex gap-2 pt-2">
        <Input className="w-56" value={name} onChange={(e) => setName(e.target.value)} placeholder="app_db" aria-label={t('db.name')} />
        <Button onClick={() => run('db.create', { instance: d.id, name }, { host })} disabled={!name}>{t('action.db.create')}</Button>
      </div>
    </div>
  )
}

function Users({ d }: { d: Instance }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const m = useMeta(d, 'users')
  const dbs = useMeta(d, 'databases')
  const [nu, setNu] = useState({ user: '', name: '', password: genPassword() })
  const [shown, setShown] = useState<{ user: string; password: string } | null>(null)
  useEvents(['change.done'], () => void m.reload())
  return (
    <div className="space-y-2 p-4">
      {m.res?.rows.map((r) => {
        const u = String(r[0]).split('@')[0]
        return (
          <div key={String(r[0])} className="flex items-center gap-2 border-b border-line py-1 text-[13px]">
            <span className="num flex-1">{String(r[0])}</span>
            <Button size="sm" variant="ghost" onClick={() => { const pw = genPassword(); run('db.user.password', { instance: d.id, user: u, password: pw }, { host, onDone: () => setShown({ user: u, password: pw }) }) }}>{t('action.db.user.password')}</Button>
            <Button size="sm" variant="ghost" onClick={() => run('db.user.drop', { instance: d.id, user: u }, { host })}>{t('common.delete')}</Button>
          </div>
        )
      })}
      <div className="flex flex-wrap items-end gap-2 pt-2">
        <Field label={t('db.user')}><Input className="w-40" value={nu.user} onChange={(e) => setNu({ ...nu, user: e.target.value })} /></Field>
        <Field label={t('db.grant')}><Select value={nu.name} onChange={(e) => setNu({ ...nu, name: e.target.value })}><option value="">—</option>{dbs.res?.rows.map((r) => <option key={String(r[0])}>{String(r[0])}</option>)}</Select></Field>
        <Field label={t('db.password')}><Input className="num w-56" value={nu.password} onChange={(e) => setNu({ ...nu, password: e.target.value })} /></Field>
        <Button onClick={() => run('db.user.create', { instance: d.id, ...nu }, { host, onDone: () => { setShown({ user: nu.user, password: nu.password }); setNu({ user: '', name: '', password: genPassword() }) } })} disabled={!nu.user}>{t('action.db.user.create')}</Button>
      </div>
      {shown && <Notice>{t('db.password_shown', { user: shown.user })} <span className="num select-all font-semibold">{shown.password}</span></Notice>}
    </div>
  )
}

function toCSV(r: Result) {
  const esc = (v: unknown) => { const s = v === null || v === undefined ? '' : String(v); return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s }
  return [r.columns.map(esc).join(','), ...r.rows.map((row) => row.map(esc).join(','))].join('\n')
}

function Query({ d }: { d: Instance }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const dbs = useMeta(d, 'databases')
  const [db, setDb] = useState('')
  const tables = useMeta(d, 'tables', db)
  const [table, setTable] = useState('')
  const cols = useMeta(d, 'columns', db, table)
  const [sql, setSql] = useState('')
  const [res, setRes] = useState<Result | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (!db && dbs.res?.rows.length) setDb(String(dbs.res.rows.find((r) => !['postgres', 'information_schema', 'mysql', 'performance_schema', 'sys'].includes(String(r[0])))?.[0] ?? dbs.res.rows[0][0])) }, [dbs.res, db])
  const exec = async (q = sql) => {
    setBusy(true); setErr('')
    try { setRes(await post<Result>(`/api/v1/hosts/${host}/databases/${d.id}/query`, { database: db, sql: q, limit: 500 })) } catch (e) { setErr((e as Error).message); setRes(null) } finally { setBusy(false) }
  }
  const quote = (n: string) => (d.engine === 'postgres' ? n.split('.').map((p) => `"${p}"`).join('.') : d.engine === 'sqlite' ? `"${n}"` : '`' + n + '`')
  return (
    <div className="grid h-full gap-0 sm:grid-cols-[220px_1fr]">
      <div className="space-y-2 overflow-auto border-r border-line p-3 text-[12.5px]">
        <Select value={db} onChange={(e) => { setDb(e.target.value); setTable('') }} aria-label={t('db.name')}>{dbs.res?.rows.map((r) => <option key={String(r[0])}>{String(r[0])}</option>)}</Select>
        {tables.res?.rows.map((r) => (
          <button key={String(r[0])} onClick={() => { const n = String(r[0]); setTable(n); const q = `SELECT * FROM ${quote(n)} LIMIT 100`; setSql(q); void exec(q) }}
            className={cx('num block w-full truncate rounded px-1.5 py-0.5 text-left', table === String(r[0]) ? 'bg-tint' : 'hover:bg-soft')}>{String(r[0])}</button>
        ))}
        {table && cols.res && <div className="border-t border-line pt-2">{cols.res.rows.map((c) => <div key={String(c[0])} className="num flex justify-between gap-1"><span className="truncate">{String(c[0])}</span><span className="text-mute">{String(c[1])}</span></div>)}</div>}
      </div>
      <div className="min-w-0 space-y-2 p-3">
        <Textarea className="num h-28 text-[12.5px]" value={sql} onChange={(e) => setSql(e.target.value)} onKeyDown={(e) => { if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') void exec() }} placeholder="SELECT …" aria-label="SQL" />
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="primary" onClick={() => void exec()} disabled={busy || !sql.trim()}>{t('db.run_read')}</Button>
          <Button onClick={() => run('db.exec', { instance: d.id, database: db, sql }, { host, confirmName: db, onDone: () => void tables.reload() })} disabled={!sql.trim()}>{t('db.run_change')}</Button>
          {res && <Button variant="ghost" onClick={() => { const a = document.createElement('a'); a.href = URL.createObjectURL(new Blob([toCSV(res)], { type: 'text/csv' })); a.download = (table || 'query') + '.csv'; a.click() }}>CSV</Button>}
          <span className="text-[12px] text-mute">{t('db.read_hint')}</span>
        </div>
        {err && <Notice tone="bad">{err}</Notice>}
        {res && <>
          <div className="text-[12px] text-mute">{t('db.rows', { n: res.rows.length, ms: res.ms })}{res.truncated ? ' · ' + t('db.truncated') : ''}</div>
          <div className="max-h-[55vh] overflow-auto rounded-[var(--radius-box)] border border-line">
            <table className="num w-full text-[12px]">
              <thead className="sticky top-0 bg-soft"><tr>{res.columns.map((c) => <th key={c} className="border-b border-line px-2 py-1 text-left font-semibold">{c}</th>)}</tr></thead>
              <tbody>{res.rows.map((r, i) => <tr key={i} className="border-b border-line last:border-0">{r.map((v, j) => <td key={j} className="max-w-[320px] truncate px-2 py-0.5" title={v === null ? 'NULL' : String(v)}>{v === null ? <span className="text-mute">NULL</span> : String(v)}</td>)}</tr>)}</tbody>
            </table>
          </div>
        </>}
      </div>
    </div>
  )
}

function RedisView({ d }: { d: Instance }) {
  const t = useT()
  const { host } = useHost()
  const [cmd, setCmd] = useState('SCAN 0 COUNT 100')
  const [out, setOut] = useState<unknown>(null)
  const [err, setErr] = useState('')
  const exec = async (c = cmd) => {
    setErr('')
    try { const r = await post<{ value: unknown }>(`/api/v1/hosts/${host}/databases/${d.id}/redis`, { args: c.match(/"[^"]*"|\S+/g)?.map((x) => x.replace(/^"|"$/g, '')) ?? [] }); setOut(r.value) } catch (e) { setErr((e as Error).message) }
  }
  useEffect(() => { void exec('INFO server') }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="space-y-2 p-4">
      <div className="flex gap-2"><Input className="num" value={cmd} onChange={(e) => setCmd(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && void exec()} aria-label={t('db.redis_cmd')} /><Button variant="primary" onClick={() => void exec()}>{t('db.run_read')}</Button></div>
      <p className="text-[12px] text-mute">{t('db.redis_hint')}</p>
      {err && <Notice tone="bad">{err}</Notice>}
      {out !== null && <pre className="num max-h-[60vh] overflow-auto rounded-[var(--radius-box)] border border-line bg-soft p-3 text-[12px]">{typeof out === 'string' ? out : JSON.stringify(out, null, 2)}</pre>}
    </div>
  )
}
