import { ArrowDownToLine, ArrowUpFromLine, Copy, FolderPlus, HardDrive, KeyRound, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Link } from 'react-router-dom'
import { Box, Dot, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Field, Input, Segmented, Select } from '@/components/ui/Field'
import { del, errText, post } from '@/lib/api'
import { bytes, datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { ReauthProvider, useReauth } from '../account/reauth'
import { Uploader, type Uploaded } from '@/components/ui/Uploader'

interface MigNeed { path: string; bytes: number }
interface MigItem { id: string; type: string; name: string; bytes: number; need: MigNeed[]; deps?: string[]; default: boolean; info?: Record<string, any> }
interface Manifest { host: string; created: number; items: MigItem[] }
interface Check { id: string; status: 'ok' | 'exists' | 'missing' | 'merge' | 'install'; note?: string; params?: Record<string, any> }
interface FS { mount: string; free: number; total: number; need: number; room: number; ok: boolean; items: string[] }
interface Preflight { manifest: Manifest; checks: Check[]; filesystems: FS[]; fits: boolean }
interface Inventory { manifest: Manifest; folders: string[]; code?: { expires: number; by: string } | null }

const ORDER = ['site', 'stack', 'project', 'appproject', 'db', 'btdb', 'vm', 'dev', 'certs', 'cron', 'btcron', 'firewall', 'sftp', 'folder', 'panel']

// Moving a whole server (R6): out of this host (a code or a pack file), or into it.
export function Migrate() {
  const t = useT()
  const [dir, setDir] = useState<'out' | 'in'>(() => (new URLSearchParams(location.search).get('in') ? 'in' : 'out'))
  return (
    <ReauthProvider>
      <div className="space-y-4">
        <Notice>{t('mig.intro')}</Notice>
        <Segmented value={dir} onChange={setDir} options={[
          { value: 'out', label: <span className="inline-flex items-center gap-1.5"><ArrowUpFromLine className="size-3.5" />{t('mig.out')}</span> },
          { value: 'in', label: <span className="inline-flex items-center gap-1.5"><ArrowDownToLine className="size-3.5" />{t('mig.in')}</span> },
        ]} />
        {dir === 'out' ? <MoveOut /> : <MoveIn />}
      </div>
    </ReauthProvider>
  )
}

function itemLabel(t: ReturnType<typeof useT>, it: MigItem) {
  switch (it.type) {
    case 'cron': return t('mig.cron_n', { n: it.info?.count ?? 0 })
    case 'btcron': return t('mig.btcron_n', { n: it.info?.count ?? 0, skipped: it.info?.skipped ?? 0 })
    case 'btdb': return `MySQL · ${t('mig.db_n', { n: Object.keys(it.info?.databases ?? {}).length })}`
    case 'firewall': return t('mig.fw_n', { n: it.info?.count ?? 0 })
    case 'certs': return t('mig.certs')
    case 'sftp': return t('mig.sftp_n', { n: it.info?.count ?? 0, names: (it.info?.names ?? []).join('、') })
    case 'dev': return `${it.name} · ${it.info?.dir ?? ''}`
    case 'panel': return t('mig.panel')
    case 'db': return `${it.info?.native ?? it.name} · ${t('mig.db_n', { n: Object.keys(it.info?.databases ?? {}).length })}`
  }
  return it.name
}

// ItemList: what can move, grouped by kind, with sizes; ticking one also ticks what it needs.
function ItemList({ items, chosen, setChosen, checks }: { items: MigItem[]; chosen: Set<string>; setChosen: (s: Set<string>) => void; checks?: Record<string, Check> }) {
  const t = useT()
  const byID = useMemo(() => Object.fromEntries(items.map((i) => [i.id, i])), [items])
  const toggle = (it: MigItem) => {
    const s = new Set(chosen)
    if (s.has(it.id)) {
      s.delete(it.id)
      for (const o of items) if (o.deps?.includes(it.id)) s.delete(o.id) // what needs it can't go without it
    } else {
      const add = (id: string) => { s.add(id); for (const d of byID[id]?.deps ?? []) if (!s.has(d)) add(d) }
      add(it.id)
    }
    setChosen(s)
  }
  const groups = ORDER.map((ty) => [ty, items.filter((i) => i.type === ty)] as const).filter(([, l]) => l.length)
  return (
    <div>
      {groups.map(([ty, l]) => (
        <div key={ty} className="border-b border-line last:border-b-0">
          <div className="bg-soft px-4 py-1 text-[11.5px] font-semibold text-mute">{t('mig.type_' + ty)}</div>
          {l.map((it) => {
            const c = checks?.[it.id]
            const off = c?.status === 'exists' || c?.status === 'missing'
            return (
              <label key={it.id} className="flex cursor-pointer items-center gap-3 px-4 py-1.5 text-[12.5px] hover:bg-soft">
                <input type="checkbox" checked={chosen.has(it.id)} onChange={() => toggle(it)} aria-label={itemLabel(t, it)} />
                <span className={'min-w-0 flex-1 truncate ' + (off && chosen.has(it.id) ? 'text-mute line-through' : '')}>{itemLabel(t, it)}
                  {it.deps?.length ? <span className="ml-2 text-[11.5px] text-mute">{t('mig.with', { n: it.deps.map((d) => byID[d]?.name ?? d).join('、') })}</span> : null}
                  {it.info?.bt && <Pill className="ml-2">{t('mig.from_bt')}</Pill>}
                  {it.type === 'site' && it.info?.bt && it.info.kind === 'php' && <span className="ml-2 text-[11.5px] text-mute">PHP {it.info.php}</span>}
                  {(it.info?.notes as string[] | undefined)?.map((n) => <span key={n} className="ml-2 text-[11.5px] text-warn">{t(n.split(':')[0], { v: n.split(':').slice(1).join(':') })}</span>)}
                  {it.type === 'vm' && it.info && it.info.limit > 0 && <span className="ml-2 text-[11.5px] text-mute">{t('mig.vm_disk', { used: bytes(it.info.used), limit: bytes(it.info.limit) })}</span>}
                </span>
                {c && c.status !== 'ok' && <Pill tone={c.status === 'exists' || c.status === 'missing' ? 'warn' : 'acc'}>{t('mig.st_' + c.status)}</Pill>}
                {c?.note && c.status !== 'ok' && <span className="max-w-[18rem] truncate text-[11.5px] text-mute max-md:hidden" title={t(c.note, c.params)}>{t(c.note, c.params)}</span>}
                <span className="num w-20 text-right text-mute">{it.bytes ? bytes(it.bytes) : '—'}</span>
              </label>
            )
          })}
        </div>
      ))}
    </div>
  )
}

function MoveOut() {
  const t = useT()
  const run = useChange()
  const reauth = useReauth()
  const { host } = useHost()
  const inv = useApi<Inventory>(`/api/v1/hosts/${host}/migrate/inventory`, [host])
  useEvents(['change.done'], () => void inv.reload())
  const [chosen, setChosen] = useState<Set<string> | null>(null)
  const [folder, setFolder] = useState('')
  const [code, setCode] = useState<{ code: string; expires: number } | null>(null)
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState(false)
  const items = inv.data?.manifest.items ?? []
  useEffect(() => { if (inv.data && chosen === null) setChosen(new Set(items.filter((i) => i.default).map((i) => i.id))) }, [inv.data])
  if (!inv.data) return inv.error ? <Notice tone="bad">{inv.error.message}</Notice> : <p className="p-6 text-[13px] text-mute">{t('mig.scanning')}</p>
  const sel = chosen ?? new Set<string>()
  const total = items.filter((i) => sel.has(i.id)).reduce((n, i) => n + i.bytes, 0)
  const folders = inv.data.folders
  const setFolders = (f: string[]) => run('migrate.folders', { folders: f }, { host })
  const makeCode = async () => {
    setErr('')
    try { setCode(await reauth(() => post<{ code: string; expires: number }>(`/api/v1/hosts/${host}/migrate/code`, {}))); void inv.reload() } catch (e) { setErr(errText(e)) }
  }
  const dl = async () => {
    setErr('')
    try {
      const r = await reauth(() => post<{ url: string }>(`/api/v1/hosts/${host}/migrate/pack`, { items: [...sel], password: pw }))
      window.location.href = r.url // the browser saves it to disk as it comes
      setPw(''); setPw2('')
    } catch (e) { setErr(errText(e)) }
  }
  return (
    <div className="space-y-4">
      {err && <Notice tone="bad">{err}</Notice>}
      <Box title={t('mig.what')} right={<span className="text-[12.5px] text-mute">{t('mig.total', { n: sel.size, size: bytes(total) })}</span>}>
        <p className="border-b border-line px-4 py-2 text-[12px] text-mute">{t('mig.what_hint')}</p>
        <ItemList items={items} chosen={sel} setChosen={setChosen} />
        <div className="flex flex-wrap items-center gap-2 border-t border-line px-4 py-2.5">
          <FolderPlus className="size-4 text-mute" />
          {folders.map((f) => (
            <span key={f} className="num inline-flex items-center gap-1 rounded-full border border-line px-2 py-0.5 text-[11.5px]">{f}
              <button aria-label={t('mig.folder_remove')} onClick={() => setFolders(folders.filter((x) => x !== f))}><X className="size-3" /></button></span>
          ))}
          <Input className="h-7 w-56" placeholder={t('mig.folder_ph')} value={folder} onChange={(e) => setFolder(e.target.value)} aria-label={t('mig.folder_add')} />
          <Button size="sm" disabled={!folder.startsWith('/')} onClick={() => { setFolders([...folders, folder.trim()]); setFolder('') }}>{t('mig.folder_add')}</Button>
        </div>
      </Box>
      <div className="grid gap-4 lg:grid-cols-2">
        <Box title={<span className="inline-flex items-center gap-1.5"><KeyRound className="size-4" />{t('mig.code_title')}</span>}>
          <p className="px-4 pt-3 text-[12.5px] text-mute">{t('mig.code_hint')}</p>
          <div className="space-y-2 px-4 py-3">
            {code ? (
              <>
                <div className="flex gap-2">
                  <Input readOnly className="num flex-1 text-[11.5px]" value={code.code} aria-label={t('mig.code_title')} onFocus={(e) => e.currentTarget.select()} />
                  <Button size="sm" onClick={() => { void navigator.clipboard?.writeText(code.code); setCopied(true) }}><Copy className="size-3.5" />{copied ? t('common.copied') : t('common.copy')}</Button>
                </div>
                <p className="text-[12px] text-mute">{t('mig.code_expires', { at: datetime(code.expires) })}</p>
              </>
            ) : inv.data.code && <p className="text-[12px] text-mute">{t('mig.code_live', { at: datetime(inv.data.code.expires) })}</p>}
            <div className="flex gap-2">
              <Button variant="primary" onClick={() => void makeCode()}>{code || inv.data.code ? t('mig.code_new') : t('mig.code_make')}</Button>
              {(code || inv.data.code) && <Button variant="ghost" onClick={() => { void del(`/api/v1/hosts/${host}/migrate/code`).then(() => { setCode(null); void inv.reload() }) }}>{t('mig.code_revoke')}</Button>}
            </div>
          </div>
        </Box>
        <Box title={<span className="inline-flex items-center gap-1.5"><ArrowDownToLine className="size-4" />{t('mig.pack_title')}</span>}>
          <p className="px-4 pt-3 text-[12.5px] text-mute">{t('mig.pack_hint')}</p>
          <Field label={t('snapshot.password')} hint={t('mig.pack_pw_hint')}><Input type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
          <Field label={t('snapshot.password2')}><Input type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} /></Field>
          <div className="px-4 pb-3"><Button disabled={pw.length < 8 || pw !== pw2 || sel.size === 0} onClick={() => void dl()}>{t('mig.pack_download', { size: bytes(total) })}</Button></div>
        </Box>
      </div>
    </div>
  )
}

type SrcType = 'upload' | 'code' | 'host' | 'url' | 'file'

function MoveIn() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const hosts = useApi<{ items: { name: string; online: boolean }[] }>('/api/v1/hosts', [])
  const [type, setType] = useState<SrcType>(() => (new URLSearchParams(location.search).get('in') === 'upload' ? 'upload' : 'code'))
  const [up, setUp] = useState<Uploaded | null>(null)
  const [code, setCode] = useState('')
  const [from, setFrom] = useState('')
  const [url, setUrl] = useState('')
  const [path, setPath] = useState('')
  const [pw, setPw] = useState('')
  const [pf, setPf] = useState<Preflight | null>(null)
  const [chosen, setChosen] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const others = (hosts.data?.items ?? []).filter((h) => h.name !== host)
  // an uploaded pack is read like one already on the server
  const source = () => ({ source: { type: type === 'upload' ? 'file' : type, from: type === 'host' ? from || others[0]?.name : undefined, url: type === 'url' ? url : undefined,
    path: type === 'file' ? path : type === 'upload' ? up?.path : undefined }, code: type === 'code' ? code : undefined, password: ['url', 'file', 'upload'].includes(type) ? pw : undefined })
  const ready = type === 'upload' ? !!up?.done && pw.length >= 8 : type === 'code' ? code.startsWith('berth-migrate:') : type === 'host' ? others.length > 0 : type === 'url' ? /^https?:\/\//.test(url) && pw.length >= 8 : path.startsWith('/') && pw.length >= 8
  const check = async (items?: string[]) => {
    setBusy(true); setErr('')
    try {
      const r = await post<Preflight>(`/api/v1/hosts/${host}/migrate/preflight`, { ...source(), items })
      setPf(r)
      if (!items) setChosen(new Set(r.manifest.items.filter((i) => i.default).map((i) => i.id)))
    } catch (e) { setErr(errText(e)); setPf(null) } finally { setBusy(false) }
  }
  // the disks are worked out again for what is ticked
  const first = useRef(true)
  useEffect(() => {
    if (!pf) return
    if (first.current) { first.current = false; return }
    const id = setTimeout(() => void check([...chosen]), 400)
    return () => clearTimeout(id)
  }, [chosen])
  const checks = Object.fromEntries((pf?.checks ?? []).map((c) => [c.id, c]))
  const items = pf?.manifest.items ?? []
  const sel = items.filter((i) => chosen.has(i.id))
  const total = sel.filter((i) => !['exists', 'missing'].includes(checks[i.id]?.status)).reduce((n, i) => n + i.bytes, 0)
  const reset = () => { setPf(null); first.current = true }
  // while the import runs: each item's state, from the @item lines of the task log
  const [phase, setPhase] = useState<'pick' | 'running' | 'done'>('pick')
  const [prog, setProg] = useState<Record<string, string>>({})
  const [report, setReport] = useState<MigReport | null>(null)
  useEvents(['task.log'], (_t, e: { line: string }) => {
    if (phase !== 'running' || !e?.line?.startsWith('@item ')) return
    const [, id, state] = e.line.split(' ')
    setProg((p) => ({ ...p, [id]: state }))
  })
  const start = () => {
    setProg({}); setReport(null)
    run('migrate.import', { ...source(), items: [...chosen] }, { host, onDone: (c) => { setReport((c.result as MigReport) ?? { done: [], skipped: [], failed: [], warnings: [], bytes: 0 }); setPhase('done') } })
    setPhase('running')
  }
  const step = phase === 'done' ? 4 : phase === 'running' ? 3 : pf ? 2 : busy ? 1 : 0
  if (phase === 'done' && report && pf) return <MigResult report={report} items={pf.manifest.items} onAgain={() => { setPhase('pick'); setReport(null); reset() }} />
  return (
    <div className="space-y-4">
      <Steps at={step} labels={['mig.st_source', 'mig.st_check', 'mig.st_pick', 'mig.st_move', 'mig.st_result'].map((k) => t(k))} />
      {phase === 'running' && pf && (
        <Box title={t('mig.moving')}>
          {sel.map((it) => {
            const st = prog[it.id]
            return (
              <Row key={it.id}>
                <Dot state={st === 'done' ? 'ok' : st === 'failed' ? 'bad' : st === 'skipped' ? 'warn' : st === 'start' ? 'busy' : 'off'} />
                <span className="min-w-0 flex-1 truncate text-[13px]">{itemLabel(t, it)}</span>
                <span className="num text-[12px] text-mute">{bytes(it.bytes)}</span>
                <span className="w-20 text-right text-[12px]">{t('mig.ist_' + (st ?? 'wait'))}</span>
              </Row>
            )
          })}
        </Box>
      )}
      <Box title={t('mig.source')}>
        <div className="space-y-3 px-4 py-3">
          <Segmented value={type} onChange={(v) => { setType(v); reset() }} options={(['upload', 'code', 'host', 'url', 'file'] as SrcType[]).map((v) => ({ value: v, label: t('mig.src_' + v) }))} />
          <p className="text-[12px] text-mute">{t('mig.src_' + type + '_hint')}</p>
          {type === 'host' && <p className="text-[12px] text-mute">{t('mig.bt_hint')}</p>}
          {type === 'code' && <Input className="num" placeholder="berth-migrate:https://…" value={code} onChange={(e) => { setCode(e.target.value.trim()); reset() }} aria-label={t('mig.src_code')} />}
          {type === 'host' && (others.length
            ? <Select className="w-64" value={from || others[0]?.name} onChange={(e) => { setFrom(e.target.value); reset() }} aria-label={t('mig.src_host')}>{others.map((h) => <option key={h.name} value={h.name}>{h.name}{h.online ? '' : ' (' + t('hosts.offline') + ')'}</option>)}</Select>
            : <Notice tone="warn">{t('mig.no_other_hosts')}</Notice>)}
          {type === 'upload' && <Uploader accept=".berthpack" label={t('mig.pick_pack')} onDone={(u) => { setUp(u); reset() }} />}
          {type === 'url' && <Input placeholder="https://…/berth-….berthpack" value={url} onChange={(e) => { setUrl(e.target.value); reset() }} aria-label={t('mig.src_url')} />}
          {type === 'file' && <Input className="num" placeholder="/root/berth-….berthpack" value={path} onChange={(e) => { setPath(e.target.value); reset() }} aria-label={t('mig.src_file')} />}
          {(type === 'url' || type === 'file' || type === 'upload') && <Input type="password" placeholder={t('mig.pack_pw')} value={pw} onChange={(e) => { setPw(e.target.value); reset() }} aria-label={t('mig.pack_pw')} />}
          <Button variant="primary" disabled={!ready || busy} onClick={() => { first.current = true; void check() }}>{busy && !pf ? t('mig.reading') : t('mig.read')}</Button>
        </div>
      </Box>
      {err && <Notice tone="bad">{err}</Notice>}
      {pf && (
        <>
          <Box title={t('mig.disks')} right={busy ? <span className="text-[12px] text-mute">{t('mig.rechecking')}</span> : pf.fits ? <Pill tone="ok">{t('mig.fits')}</Pill> : <Pill tone="bad">{t('mig.no_fit')}</Pill>}>
            {pf.filesystems.length === 0 && <p className="px-4 py-3 text-[12.5px] text-mute">{t('mig.no_data')}</p>}
            {pf.filesystems.map((f) => (
              <div key={f.mount} className="border-b border-line px-4 py-2.5 last:border-b-0">
                <div className="flex items-center gap-2 text-[12.5px]"><HardDrive className="size-4 text-mute" /><span className="num font-semibold">{f.mount}</span>
                  <span className="flex-1 text-mute">{t('mig.disk_line', { need: bytes(f.room), free: bytes(f.free), total: bytes(f.total) })}</span>
                  {f.ok ? <Pill tone="ok">{t('mig.enough')}</Pill> : <Pill tone="bad">{t('mig.short', { n: bytes(f.room - f.free) })}</Pill>}</div>
                <div className="mt-1.5 h-2 overflow-hidden rounded-full bg-soft" title={t('mig.disk_bar')}>
                  <div className={'h-full ' + (f.ok ? 'bg-acc' : 'bg-bad')} style={{ width: Math.min(100, (f.room / Math.max(1, f.free)) * 100) + '%' }} />
                </div>
                {!f.ok && <p className="mt-1 text-[12px] text-bad">{t('mig.short_hint', { items: f.items.map((id) => items.find((i) => i.id === id)?.name ?? id).join('、') })}</p>}
              </div>
            ))}
          </Box>
          <Box title={t('mig.from', { host: pf.manifest.host })} right={<span className="text-[12.5px] text-mute">{t('mig.total', { n: sel.length, size: bytes(total) })}</span>}>
            <ItemList items={items} chosen={chosen} setChosen={setChosen} checks={checks} />
          </Box>
          <p className="text-[12px] text-mute">{t('mig.left_behind')}</p>
          <Button variant="primary" disabled={!pf.fits || busy || sel.length === 0} onClick={start}>
            <ArrowDownToLine className="size-3.5" />{t('mig.start', { n: sel.length, size: bytes(total) })}</Button>
        </>
      )}
    </div>
  )
}

interface MigReport { done: string[]; skipped: { id: string; reason: string }[]; failed: { id: string; reason: string }[]; warnings: { id: string; reason: string }[]; bytes: number }

// Steps: where the migration stands.
function Steps({ at, labels }: { at: number; labels: string[] }) {
  return (
    <ol className="flex flex-wrap items-center gap-1.5 text-[12.5px]">
      {labels.map((l, i) => (
        <li key={l} className="flex items-center gap-1.5">
          <span className={'grid size-5 place-items-center rounded-full text-[11px] font-semibold ' + (i < at ? 'bg-acc text-white' : i === at ? 'border border-acc text-acc' : 'border border-line text-mute')}>{i + 1}</span>
          <span className={i === at ? 'font-semibold' : 'text-mute'}>{l}</span>
          {i < labels.length - 1 && <span className="mx-1 h-px w-6 bg-line" />}
        </li>
      ))}
    </ol>
  )
}

// MigResult: what came over, what did not and why, and what to do next (DNS still pointing at the old server).
function MigResult({ report, items, onAgain }: { report: MigReport; items: MigItem[]; onAgain: () => void }) {
  const t = useT()
  const { host } = useHost()
  const dom = useApi<{ items: { root: string; subs: { name: string; points: string; records?: string[] }[] }[] }>(`/api/v1/hosts/${host}/domains/overview`, [host])
  const name = (id: string) => { const it = items.find((i) => i.id === id); return it ? itemLabel(t, it) : id }
  const away = (dom.data?.items ?? []).flatMap((r) => r.subs.filter((s) => s.points === 'elsewhere' || s.points === 'none'))
  const group = (title: string, tone: 'ok' | 'warn' | 'bad', list: { id: string; reason?: string }[]) => list.length > 0 && (
    <Box title={<span className="inline-flex items-center gap-2">{title}<Pill tone={tone}>{list.length}</Pill></span>}>
      {list.map((x, i) => (
        <Row key={x.id + i}>
          <Dot state={tone === 'ok' ? 'ok' : tone === 'bad' ? 'bad' : 'warn'} />
          <span className="min-w-0 flex-1 text-[13px]">{name(x.id)}</span>
          {x.reason && <span className="max-w-[55%] truncate text-[12px] text-mute" title={x.reason}>{reasonText(t, x.reason)}</span>}
        </Row>
      ))}
    </Box>
  )
  return (
    <div className="space-y-4">
      <Steps at={4} labels={['mig.st_source', 'mig.st_check', 'mig.st_pick', 'mig.st_move', 'mig.st_result'].map((k) => t(k))} />
      <Notice tone={report.failed.length ? 'bad' : report.skipped.length || report.warnings.length ? 'warn' : 'acc'}>
        {t('mig.result_line', { done: report.done.length, skipped: report.skipped.length, failed: report.failed.length, size: bytes(report.bytes) })}
      </Notice>
      {group(t('mig.r_failed'), 'bad', report.failed)}
      {group(t('mig.r_skipped'), 'warn', report.skipped)}
      {group(t('mig.r_warnings'), 'warn', report.warnings)}
      {group(t('mig.r_done'), 'ok', report.done.map((id) => ({ id })))}
      <Box title={t('mig.next')}>
        <div className="space-y-2 px-4 py-3 text-[13px]">
          {away.length > 0
            ? <p>{t('mig.next_dns', { n: away.length, names: away.slice(0, 5).map((s) => s.name).join('、') })} <Link className="text-acc hover:underline" to={hostPath(host, 'domains')}>{t('mig.next_dns_go')}</Link></p>
            : dom.data && <p className="text-mute">{t('mig.next_dns_ok')}</p>}
          <p className="text-mute">{t('mig.next_check')}</p>
        </div>
      </Box>
      <Button onClick={onAgain}>{t('mig.again')}</Button>
    </div>
  )
}

// reasonText: "migrate.warn.db_exists:shop" → the translated sentence with "shop" in it; other text as it is.
export function reasonText(t: ReturnType<typeof useT>, r: string) {
  const m = /^(migrate\.[a-z_.]+)(?::(.*))?$/s.exec(r)
  if (!m) return r
  const s = t(m[1], { v: m[2] ?? '' })
  return s === m[1] ? r : s
}
