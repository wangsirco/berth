import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { TabBar } from '@/components/ui/Tabs'
import { useParams } from 'react-router-dom'
import { Migrate } from '../system/Migrate'
import { Restore } from './Restore'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented, Select, Switch, Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago, bytes, datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { ReauthProvider, useReauth } from '../account/reauth'

interface Target { id: number; name: string; kind: string; where: string }
interface Run { id: number; kind: string; status: string; snapshot?: string; bytes: number; message?: string; started: number; finished?: number }
interface Source { type: 'path' | 'app' | 'project' | 'database' | 'panel'; ref: string }
interface Job { id: number; name: string; host: string; target: number; target_name?: string; sources: Source[]; excludes?: string[]; cron: string; keep: { last?: number; daily?: number; weekly?: number; monthly?: number }; stop: boolean; limit_kbps: number; drill: boolean; enabled: boolean; last_run?: Run; last_drill?: Run }

const runDot = (r?: Run) => (!r ? 'off' : r.status === 'done' ? 'ok' : r.status === 'running' ? 'busy' : 'bad') as 'ok' | 'off' | 'busy' | 'bad'

const TABS = ['', 'restore', 'migrate'] as const

// 备份与还原: backup jobs, restoring (uploads, panel snapshots, other servers' repositories) and moving a whole server.
export default function BackupPage() {
  const t = useT()
  const { host } = useHost()
  const { user } = useAuth()
  const sub = (useParams()['*'] ?? '').split('/')[0]
  const tabs = TABS.filter((x) => x === '' || can(user, 'system.manage'))
  const tab = (tabs as readonly string[]).includes(sub) ? sub : ''
  return (
    <div className="space-y-4">
      <TabBar label={t('nav.backup')} value={tab} items={tabs.map((x) => ({ value: x, label: t('backup.tab_' + (x || 'jobs')), to: hostPath(host, 'backup' + (x ? '/' + x : '')) }))} />
      {tab === '' && <ReauthProvider><Backups /></ReauthProvider>}
      {tab === 'restore' && <Restore />}
      {tab === 'migrate' && <Migrate />}
    </div>
  )
}

function Backups() {
  const t = useT()
  const run = useChange()
  const reauth = useReauth()
  const { user } = useAuth()
  const { host } = useHost()
  const data = useApi<{ targets: Target[]; jobs: Job[] }>('/api/v1/backups')
  const tools = useApi<{ restic?: string; rclone?: string }>(`/api/v1/hosts/${host}/backup/tools`, [host])
  const [editTarget, setEditTarget] = useState<Target | 'new' | null>(null)
  const [editJob, setEditJob] = useState<Job | 'new' | null>(() => (new URLSearchParams(location.search).get('new') === '1' ? 'new' : null))
  const prefill = (() => { const q = new URLSearchParams(location.search); return q.get('path') ? { name: q.get('name') ?? '', sources: [{ type: 'path' as const, ref: q.get('path')! }] } : undefined })()
  const [open, setOpen] = useState<Job | null>(null)
  const [key, setKey] = useState<{ repository: string; password: string; rclone_config?: string } | null>(null)
  useEvents(['change.done', 'backup.run'], () => void data.reload())
  const manage = can(user, 'backup.manage')
  const reload = () => { void data.reload(); void tools.reload() }
  return (
    <div className="space-y-4">
      {tools.data && !tools.data.restic && (
        <Notice tone="warn"><div className="flex flex-wrap items-center gap-2"><span>{t('backup.no_tools')}</span>
          {manage && <Button size="sm" variant="primary" onClick={() => run('backup.tools.install', {}, { host, onDone: reload })}>{t('action.backup.tools.install')}</Button>}</div></Notice>
      )}
      <Box title={t('backup.jobs')} count={data.data?.jobs.length} right={manage && (data.data?.targets.length ?? 0) > 0 && <Button size="sm" variant="primary" onClick={() => setEditJob('new')}>{t('backup.new_job')}</Button>}>
        {data.data?.jobs.length === 0 && <Empty title={t('backup.no_jobs')}>{t('backup.no_jobs_hint')}</Empty>}
        {data.data?.jobs.map((j) => (
          <Row key={j.id} onClick={() => setOpen(j)}>
            <Dot state={runDot(j.last_run)} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{j.name} <span className="font-normal text-mute">→ {j.target_name} · {j.cron || t('backup.manual')}</span></div>
              <div className="truncate text-[12px] text-mute">
                {j.last_run ? t('backup.last', { ago: ago(j.last_run.started, t), status: t('backup.st_' + j.last_run.status) }) + (j.last_run.bytes ? ' · +' + bytes(j.last_run.bytes) : '') : t('backup.never')}
                {j.drill && ' · ' + (j.last_drill ? t('backup.last_drill', { ago: ago(j.last_drill.started, t), status: t('backup.st_' + j.last_drill.status) }) : t('backup.drill_monthly'))}
              </div>
            </div>
            {!j.enabled && <Pill>{t('sites.disabled')}</Pill>}
            {manage && <span onClick={(e) => e.stopPropagation()}><Button size="sm" onClick={() => run('backup.run', { id: j.id }, { host: j.host })}>{t('action.backup.run')}</Button></span>}
          </Row>
        ))}
      </Box>
      <Box title={t('backup.targets')} count={data.data?.targets.length} right={manage && <Button size="sm" onClick={() => setEditTarget('new')}>{t('backup.new_target')}</Button>}>
        {data.data?.targets.length === 0 && <Empty title={t('backup.no_targets')}>{t('backup.no_targets_hint')}</Empty>}
        {data.data?.targets.map((x) => (
          <Row key={x.id}>
            <div className="min-w-0 flex-1"><div className="text-[13.5px] font-semibold">{x.name} <span className="font-normal text-mute">· {t('backup.kind_' + x.kind)}</span></div><div className="num truncate text-[12px] text-mute">{x.where}</div></div>
            {manage && <>
              <Button size="sm" variant="ghost" onClick={async () => setKey(await reauth(() => post(`/api/v1/backups/targets/${x.id}/key`)))}>{t('backup.recovery_key')}</Button>
              <Button size="sm" variant="ghost" onClick={() => setEditTarget(x)}>{t('sites.edit')}</Button>
              <Button size="sm" variant="ghost" onClick={() => run('backup.target.delete', { id: x.id }, { host })}>{t('common.delete')}</Button>
            </>}
          </Row>
        ))}
      </Box>
      {editTarget && <TargetDialog target={editTarget === 'new' ? undefined : editTarget} onClose={() => setEditTarget(null)} onDone={reload} />}
      {editJob && <JobDialog job={editJob === 'new' ? undefined : editJob} targets={data.data?.targets ?? []} prefill={editJob === 'new' ? prefill : undefined} onClose={() => setEditJob(null)} onDone={reload} />}
      {open && <JobDrawer job={open} onClose={() => setOpen(null)} onEdit={(j) => { setOpen(null); setEditJob(j) }} />}
      {key && <Dialog open onOpenChange={(o) => !o && setKey(null)} title={t('backup.recovery_key')} footer={<Button className="ml-auto" onClick={() => setKey(null)}>{t('common.close')}</Button>}>
        <div className="space-y-2 px-4 py-3 text-[13px]">
          <Notice tone="warn">{t('backup.key_hint')}</Notice>
          <div className="num space-y-1 text-[12px]"><div><span className="text-mute">RESTIC_REPOSITORY=</span><span className="select-all">{key.repository}</span></div><div><span className="text-mute">RESTIC_PASSWORD=</span><span className="select-all font-semibold">{key.password}</span></div></div>
          {key.rclone_config && <pre className="num select-all whitespace-pre-wrap rounded-[var(--radius-box)] bg-soft p-2 text-[11.5px]">{key.rclone_config}</pre>}
        </div>
      </Dialog>}
    </div>
  )
}

const REMOTES = ['drive', 'onedrive', 'dropbox', 'webdav', 'box', 'pcloud', 'mega'] as const

function TargetDialog({ target, onClose, onDone }: { target?: Target; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [kind, setKind] = useState(target?.kind ?? 'local')
  const [f, setF] = useState({ name: target?.name ?? '', path: kind === 'local' ? '/srv/berth/backups/restic' : 'berth', endpoint: '', bucket: '', access: '', secret: '', remote: 'drive', token: '', host: '', user: '', pass: '', url: '', vendor: 'other' })
  const set = (k: keyof typeof f, v: string) => setF({ ...f, [k]: v })
  const submit = () => {
    const p: Record<string, unknown> = { id: target?.id, name: f.name, kind, path: f.path }
    if (kind === 's3') Object.assign(p, { endpoint: f.endpoint, bucket: f.bucket, access: f.access, secret: f.secret })
    if (kind === 'sftp') Object.assign(p, { options: { host: f.host, user: f.user, ...(f.pass ? { pass: f.pass } : {}) } })
    if (kind === 'rclone') {
      const opts: Record<string, string> = f.remote === 'drive' ? { scope: 'drive' } : f.remote === 'webdav' ? { url: f.url, vendor: f.vendor, user: f.user, ...(f.pass ? { pass: f.pass } : {}) } : {}
      Object.assign(p, { remote: f.remote, token: f.token.match(/\{[\s\S]*\}/)?.[0]?.replace(/\s+/g, ' ') ?? '', options: opts })
    }
    run('backup.target.set', p, { host, onDone: () => { onDone(); onClose() } })
  }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={target ? t('backup.edit_target', { name: target.name }) : t('backup.new_target')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={submit} disabled={!f.name}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        <Segmented value={kind} onChange={(v) => { setKind(v); if (v !== 'local' && f.path.startsWith('/')) set('path', 'berth') }} options={['local', 's3', 'sftp', 'rclone'].map((k) => ({ value: k, label: t('backup.kind_' + k) }))} />
        <Field label={t('backup.target_name')}><Input value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="gdrive" /></Field>
        {kind === 's3' && <div className="grid gap-2 sm:grid-cols-2">
          <Field label="Endpoint" hint={t('backup.s3_hint')}><Input value={f.endpoint} onChange={(e) => set('endpoint', e.target.value)} placeholder="https://<account>.r2.cloudflarestorage.com" /></Field>
          <Field label="Bucket"><Input value={f.bucket} onChange={(e) => set('bucket', e.target.value)} /></Field>
          <Field label="Access Key"><Input value={f.access} onChange={(e) => set('access', e.target.value)} autoComplete="off" /></Field>
          <Field label="Secret Key"><Input type="password" value={f.secret} onChange={(e) => set('secret', e.target.value)} autoComplete="off" /></Field>
        </div>}
        {kind === 'sftp' && <div className="grid gap-2 sm:grid-cols-3">
          <Field label={t('backup.sftp_host')}><Input value={f.host} onChange={(e) => set('host', e.target.value)} /></Field>
          <Field label={t('db.user')}><Input value={f.user} onChange={(e) => set('user', e.target.value)} /></Field>
          <Field label={t('backup.sftp_pass')} hint={t('backup.sftp_pass_hint')}><Input type="password" value={f.pass} onChange={(e) => set('pass', e.target.value)} autoComplete="off" /></Field>
        </div>}
        {kind === 'rclone' && <div className="space-y-2">
          <Field label={t('backup.remote')}><Select value={f.remote} onChange={(e) => set('remote', e.target.value)}>{REMOTES.map((r) => <option key={r} value={r}>{t('backup.remote_' + r)}</option>)}</Select></Field>
          {f.remote === 'webdav' ? <div className="grid gap-2 sm:grid-cols-4">
            <Field label="URL"><Input value={f.url} onChange={(e) => set('url', e.target.value)} placeholder="https://dav.example.com/remote.php/dav/files/me" /></Field>
            <Field label={t('db.user')}><Input value={f.user} onChange={(e) => set('user', e.target.value)} /></Field>
            <Field label={t('backup.sftp_pass')}><Input type="password" value={f.pass} onChange={(e) => set('pass', e.target.value)} autoComplete="off" /></Field>
            <Field label="Vendor"><Select value={f.vendor} onChange={(e) => set('vendor', e.target.value)}>{['other', 'nextcloud', 'owncloud', 'sharepoint'].map((v) => <option key={v}>{v}</option>)}</Select></Field>
          </div> : <>
            <Notice>{t('backup.paste_steps')}</Notice>
            <pre className="num select-all rounded-[var(--radius-box)] bg-soft px-3 py-2 text-[12.5px]">rclone authorize "{f.remote}"</pre>
            <Field label={t('backup.token')} hint={t('backup.token_hint')}><Textarea className="num h-24 text-[11.5px]" value={f.token} onChange={(e) => set('token', e.target.value)} placeholder='{"access_token":"…","token_type":"Bearer","refresh_token":"…","expiry":"…"}' /></Field>
          </>}
        </div>}
        <Field label={kind === 'local' ? t('backup.local_path') : t('backup.remote_path')} hint={kind === 'local' ? t('backup.local_hint') : undefined}><Input value={f.path} onChange={(e) => set('path', e.target.value)} /></Field>
        <p className="text-[12px] text-mute">{t('backup.encrypted_note')}</p>
      </div>
    </Dialog>
  )
}

function JobDialog({ job, targets, prefill, onClose, onDone }: { job?: Job; targets: Target[]; prefill?: { name: string; sources: Source[] }; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const apps = useApi<{ items: { name: string; app_id: string }[] }>(`/api/v1/hosts/${host}/apps`, [host])
  const projects = useApi<{ items: { name: string }[] }>(`/api/v1/hosts/${host}/compose`, [host])
  const dbs = useApi<{ items: { id: number; name: string; engine: string; container?: string }[] }>(`/api/v1/hosts/${host}/databases`, [host])
  const [j, setJ] = useState<Job>(() => job ?? { id: 0, name: prefill?.name ?? '', host, target: targets[0]?.id ?? 0, sources: prefill?.sources ?? [], cron: '0 3 * * *', keep: { daily: 7, weekly: 4, monthly: 6 }, stop: false, limit_kbps: 0, drill: true, enabled: true })
  const [path, setPath] = useState('')
  const [dbSel, setDbSel] = useState({ id: '', name: '' })
  const has = (s: Source) => j.sources.some((x) => x.type === s.type && x.ref === s.ref)
  const toggle = (s: Source) => setJ({ ...j, sources: has(s) ? j.sources.filter((x) => !(x.type === s.type && x.ref === s.ref)) : [...j.sources, s] })
  const chip = (s: Source, label: string) => (
    <button key={s.type + s.ref} type="button" onClick={() => toggle(s)} className={cx('rounded-full border px-2.5 py-0.5 text-[12.5px]', has(s) ? 'border-acc bg-tint' : 'border-line text-mute hover:text-fg')}>{label}</button>
  )
  const keep = (k: keyof Job['keep'], v: string) => setJ({ ...j, keep: { ...j.keep, [k]: Number(v) || 0 } })
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={job ? t('backup.edit_job', { name: job.name }) : t('backup.new_job')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!j.name || !j.sources.length} onClick={() => run('backup.job.set', { ...j, id: job?.id }, { host, onDone: () => { onDone(); onClose() } })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label={t('backup.job_name')}><Input value={j.name} onChange={(e) => setJ({ ...j, name: e.target.value })} placeholder="nightly" /></Field>
          <Field label={t('backup.target')}><Select value={String(j.target)} onChange={(e) => setJ({ ...j, target: Number(e.target.value) })}>{targets.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</Select></Field>
        </div>
        <div>
          <div className="mb-1 text-[12.5px] font-semibold">{t('backup.sources')}</div>
          <div className="space-y-2">
            <div className="flex flex-wrap gap-1">{chip({ type: 'panel', ref: '' }, t('backup.src_panel'))}</div>
            {(apps.data?.items.length ?? 0) > 0 && <div className="flex flex-wrap items-center gap-1"><span className="w-20 text-[12px] text-mute">{t('backup.src_apps')}</span>{apps.data!.items.map((a) => chip({ type: 'app', ref: a.name }, a.name))}</div>}
            {(projects.data?.items.length ?? 0) > 0 && <div className="flex flex-wrap items-center gap-1"><span className="w-20 text-[12px] text-mute">{t('backup.src_projects')}</span>{projects.data!.items.filter((p) => !apps.data?.items.some((a) => a.name === p.name)).map((p) => chip({ type: 'project', ref: p.name }, p.name))}</div>}
            <div className="flex flex-wrap items-center gap-1"><span className="w-20 text-[12px] text-mute">{t('backup.src_dbs')}</span>
              {j.sources.filter((s) => s.type === 'database').map((s) => chip(s, (dbs.data?.items.find((d) => String(d.id) === s.ref.split('/')[0])?.name ?? s.ref.split('/')[0]) + '/' + s.ref.split('/')[1]))}
              <Select value={dbSel.id} onChange={(e) => setDbSel({ ...dbSel, id: e.target.value })} aria-label={t('backup.src_dbs')}><option value="">—</option>{dbs.data?.items.filter((d) => d.container && d.engine !== 'redis').map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}</Select>
              <Input className="w-32" value={dbSel.name} onChange={(e) => setDbSel({ ...dbSel, name: e.target.value })} placeholder={t('db.name')} aria-label={t('db.name')} />
              <Button size="sm" disabled={!dbSel.id || !dbSel.name} onClick={() => { toggle({ type: 'database', ref: dbSel.id + '/' + dbSel.name }); setDbSel({ id: '', name: '' }) }}>＋</Button>
            </div>
            <div className="flex flex-wrap items-center gap-1"><span className="w-20 text-[12px] text-mute">{t('backup.src_paths')}</span>
              {j.sources.filter((s) => s.type === 'path').map((s) => chip(s, s.ref))}
              <Input className="w-64" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/srv/berth/sites" aria-label={t('backup.src_paths')} />
              <Button size="sm" disabled={!path.startsWith('/')} onClick={() => { toggle({ type: 'path', ref: path }); setPath('') }}>＋</Button>
            </div>
          </div>
        </div>
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label={t('pipeline.cron')} hint={t('pipeline.cron_hint')}><Input value={j.cron} onChange={(e) => setJ({ ...j, cron: e.target.value })} /></Field>
          <Field label={t('backup.limit')}><Input type="number" min={0} value={j.limit_kbps || ''} onChange={(e) => setJ({ ...j, limit_kbps: Number(e.target.value) || 0 })} placeholder={t('backup.unlimited')} /></Field>
        </div>
        <div>
          <div className="mb-1 text-[12.5px] font-semibold">{t('backup.keep')}</div>
          <div className="flex flex-wrap gap-2">{(['last', 'daily', 'weekly', 'monthly'] as const).map((k) => (
            <label key={k} className="flex items-center gap-1 text-[12.5px]">{t('backup.keep_' + k)}<Input className="w-16" type="number" min={0} value={j.keep[k] ?? 0} onChange={(e) => keep(k, e.target.value)} /></label>
          ))}</div>
        </div>
        <Switch checked={j.stop} onChange={(v) => setJ({ ...j, stop: v })} label={t('backup.stop')} />
        <Switch checked={j.drill} onChange={(v) => setJ({ ...j, drill: v })} label={t('backup.drill')} />
        <Switch checked={j.enabled} onChange={(v) => setJ({ ...j, enabled: v })} label={t('backup.enabled')} />
      </div>
    </Dialog>
  )
}

interface Snap { id: string; short_id: string; time: string; paths: string[]; tags: string[] }
interface Node { path: string; name: string; type: string; size: number }

function JobDrawer({ job, onClose, onEdit }: { job: Job; onClose: () => void; onEdit: (j: Job) => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const [tab, setTab] = useState<'runs' | 'snaps'>('snaps')
  const runs = useApi<{ items: Run[] }>(`/api/v1/backups/jobs/${job.id}/runs`, [job.id])
  const snaps = useApi<{ items: Snap[] }>(tab === 'snaps' ? `/api/v1/backups/jobs/${job.id}/snapshots` : null, [job.id, tab])
  const [snap, setSnap] = useState<Snap | null>(null)
  const [dir, setDir] = useState('/')
  const [nodes, setNodes] = useState<Node[] | null>(null)
  const [restoreTo, setRestoreTo] = useState('/tmp/berth-restore')
  const [toHost, setToHost] = useState(job.host)
  const { hosts } = useHost()
  useEvents(['backup.run'], () => { void runs.reload(); void snaps.reload() })
  const browse = async (s: Snap, d: string) => {
    setSnap(s); setDir(d)
    setNodes((await post<{ items: Node[] }>(`/api/v1/backups/jobs/${job.id}/ls`, { snapshot: s.id, dir: d })).items)
  }
  const manage = can(user, 'backup.manage')
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={job.name}
      right={manage && <div className="flex gap-1.5">
        <Button size="sm" variant="primary" onClick={() => run('backup.run', { id: job.id }, { host: job.host })}>{t('action.backup.run')}</Button>
        <Button size="sm" onClick={() => run('backup.drill', { id: job.id }, { host: job.host })}>{t('action.backup.drill')}</Button>
        <Button size="sm" onClick={() => onEdit(job)}>{t('sites.edit')}</Button>
        <Button size="sm" variant="ghost" onClick={() => run('backup.job.delete', { id: job.id }, { host: job.host, onDone: onClose })}>{t('common.delete')}</Button>
      </div>}>
      <div className="border-b border-line px-4 py-2"><Segmented value={tab} onChange={setTab} options={[{ value: 'snaps', label: t('backup.snapshots') }, { value: 'runs', label: t('backup.runs') }]} /></div>
      {tab === 'runs' && <div className="p-4">
        {runs.data?.items.map((r) => (
          <div key={r.id} className="flex items-center gap-2 border-b border-line py-1.5 text-[13px]">
            <Dot state={runDot(r)} /><span className="w-16">{t('backup.kind_run_' + r.kind)}</span>
            <span className="flex-1 text-mute">{datetime(r.started)}{r.finished ? ' · ' + Math.max(1, r.finished - r.started) + ' s' : ''}{r.snapshot ? ' · ' + r.snapshot.slice(0, 8) : ''}{r.bytes ? ' · +' + bytes(r.bytes) : ''}</span>
            {r.message && <span className="max-w-[45%] truncate text-[12px] text-bad" title={r.message}>{r.message}</span>}
          </div>
        ))}
      </div>}
      {tab === 'snaps' && <div className="space-y-2 p-4">
        {snaps.error && <Notice tone="bad">{snaps.error.message}</Notice>}
        {snaps.data?.items.length === 0 && <Empty title={t('backup.no_snapshots')} />}
        {[...(snaps.data?.items ?? [])].reverse().map((s) => (
          <div key={s.id} className={cx('rounded-[var(--radius-box)] border px-3 py-1.5 text-[13px]', snap?.id === s.id ? 'border-acc' : 'border-line')}>
            <div className="flex items-center gap-2">
              <span className="num">{s.short_id}</span><span className="text-mute">{new Date(s.time).toLocaleString()}</span>
              {s.tags.includes('pre-restore') && <Pill tone="warn">{t('backup.pre_restore')}</Pill>}
              <Button size="sm" variant="ghost" className="ml-auto" onClick={() => void browse(s, '/')}>{t('backup.browse')}</Button>
              {manage && can(user, 'backup.restore') && <Button size="sm" variant="ghost" onClick={() => run('backup.restore', { id: job.id, snapshot: s.id, target: '/' }, { host: job.host, confirmName: s.short_id })}>{t('backup.restore_original')}</Button>}
            </div>
            {snap?.id === s.id && nodes && <div className="mt-2 border-t border-line pt-2">
              <div className="num mb-1 flex items-center gap-1 text-[12px]">
                {dir !== '/' && <button className="text-acc hover:underline" onClick={() => void browse(s, dir.split('/').slice(0, -1).join('/') || '/')}>..</button>}<span className="text-mute">{dir}</span>
              </div>
              {nodes.map((n) => (
                <div key={n.path} className="num flex items-center gap-2 py-0.5 text-[12.5px]">
                  {n.type === 'dir' ? <button className="flex-1 truncate text-left text-acc hover:underline" onClick={() => void browse(s, n.path)}>{n.name}/</button> : <span className="flex-1 truncate">{n.name}</span>}
                  <span className="text-mute">{n.type === 'file' ? bytes(n.size) : ''}</span>
                  {manage && can(user, 'backup.restore') && <Button size="sm" variant="ghost" onClick={() => run('backup.restore', { id: job.id, snapshot: s.id, target: restoreTo, include: [n.path], ...(toHost !== job.host ? { to_host: toHost } : {}) }, { host: job.host })}>{t('backup.restore_here')}</Button>}
                </div>
              ))}
              <Field label={t('backup.restore_to')} hint={t('backup.restore_to_hint')}>
                <div className="flex gap-2">
                  {hosts.length > 1 && <Select value={toHost} onChange={(e) => setToHost(e.target.value)} aria-label={t('backup.restore_host')}>{hosts.map((h) => <option key={h.name} value={h.name}>{h.name}{h.name === job.host ? ' · ' + t('backup.same_host') : ''}</option>)}</Select>}
                  <Input className="num flex-1" value={restoreTo} onChange={(e) => setRestoreTo(e.target.value)} />
                </div>
              </Field>
            </div>}
          </div>
        ))}
      </div>}
    </Drawer>
  )
}
