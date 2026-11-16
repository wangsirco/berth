import { Archive, ArrowDownToLine, DatabaseBackup, FolderOpen, Loader2, Package, ShieldAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Field, Input, Segmented } from '@/components/ui/Field'
import { Uploader, type Uploaded } from '@/components/ui/Uploader'
import { errText, get, post } from '@/lib/api'
import { bytes, datetime } from '@/lib/format'
import { useT } from '@/lib/i18n'

type Src = 'pack' | 'snap' | 'repo'

// 还原: put things back on a panel that is already installed — a migration pack, the panel
// itself from a snapshot, or files from a backup repository another server wrote.
export function Restore() {
  const t = useT()
  const { host } = useHost()
  const [src, setSrc] = useState<Src>(() => (new URLSearchParams(location.search).get('src') as Src) || 'pack')
  const card = (k: Src, Icon: typeof Archive) => (
    <button key={k} type="button" onClick={() => setSrc(k)}
      className={'flex flex-1 items-start gap-3 rounded-xl border px-3.5 py-3 text-left ' + (src === k ? 'border-acc bg-tint' : 'border-line bg-card hover:border-mute')}>
      <Icon className="mt-0.5 size-5 shrink-0 text-acc" />
      <span><span className="block text-[13.5px] font-semibold">{t('restore.src_' + k)}</span><span className="block text-[12px] text-mute">{t('restore.src_' + k + '_hint')}</span></span>
    </button>
  )
  return (
    <div className="space-y-4">
      <div className="grid gap-2.5 sm:grid-cols-3">{card('pack', Package)}{card('snap', ShieldAlert)}{card('repo', DatabaseBackup)}</div>
      {src === 'pack' && <Notice>{t('restore.pack_how')} <Link className="text-acc hover:underline" to={hostPath(host, 'backup/migrate') + '?in=upload'}>{t('restore.to_migrate')}</Link></Notice>}
      {src === 'snap' && <SnapshotRestore />}
      {src === 'repo' && <RepoRestore />}
      <p className="text-[12px] text-mute">{t('restore.local_jobs')} <Link className="text-acc hover:underline" to={hostPath(host, 'backup')}>{t('backup.tab_jobs')}</Link></p>
    </div>
  )
}

// SnapshotRestore: upload a .berthsnap, check its password, then swap the panel's data for it.
function SnapshotRestore() {
  const t = useT()
  const run = useChange()
  const [up, setUp] = useState<Uploaded | null>(null)
  const [pw, setPw] = useState('')
  const [checked, setChecked] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const check = async () => {
    setBusy(true); setErr(''); setChecked(null)
    try { const r = await post<{ detail: string }>('/api/v1/panel/restore/check', { upload: up!.id, password: pw }); setChecked(r.detail) } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  // after the restore the panel goes away and comes back with the snapshot's accounts
  useEffect(() => {
    if (!restarting) return
    let gone = false
    const id = setInterval(async () => {
      try { await get('/api/v1/me'); if (gone) location.href = '/login' } catch { gone = true }
    }, 2000)
    const stop = setTimeout(() => { location.href = '/login' }, 120000)
    return () => { clearInterval(id); clearTimeout(stop) }
  }, [restarting])
  if (restarting) return <Box><div className="flex items-center gap-3 px-4 py-6 text-[13.5px]"><Loader2 className="size-5 animate-spin text-acc" />{t('restore.snap_restarting')}</div></Box>
  return (
    <Box title={t('restore.src_snap')}>
      <div className="space-y-3 px-4 py-3">
        <Notice tone="warn">{t('restore.snap_warn')}</Notice>
        <Uploader accept=".berthsnap" label={t('restore.pick_snap')} onDone={(u) => { setUp(u); setChecked(null) }} />
        {up && <Field label={t('restore.snap_password')}><Input type="password" value={pw} onChange={(e) => { setPw(e.target.value); setChecked(null) }} aria-label={t('restore.snap_password')} /></Field>}
        {up && !checked && <Button disabled={pw.length < 8 || busy} onClick={() => void check()}>{busy ? t('restore.checking') : t('restore.check')}</Button>}
        {checked && <Notice>{t('restore.snap_ok', { detail: checked })}</Notice>}
        {err && <Notice tone="bad">{err}</Notice>}
        {checked && <Button variant="danger" onClick={() => run('panel.restore', { upload: up!.id, password: pw }, { host: 'local', typeToConfirm: t('restore.confirm_word'), onDone: (c) => { if (c.status === 'done') setRestarting(true) } })}>
          <ArrowDownToLine className="size-3.5" />{t('action.panel.restore')}</Button>}
      </div>
    </Box>
  )
}

interface Snap { id: string; short_id: string; time: string; paths: string[]; tags: string[]; hostname: string }
interface Node { path: string; name: string; type: string; size: number }
type Kind = 'local' | 's3' | 'sftp'

// RepoRestore: a restic repository written by another server (its own backups), read here.
function RepoRestore() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [kind, setKind] = useState<Kind>('s3')
  const [f, setF] = useState<Record<string, string>>({ path: '', endpoint: '', bucket: '', access: '', secret: '', sftp_host: '', sftp_user: '', sftp_pass: '', password: '' })
  const [snaps, setSnaps] = useState<Snap[] | null>(null)
  const [pick, setPick] = useState<Snap | null>(null)
  const [dir, setDir] = useState('/')
  const [nodes, setNodes] = useState<Node[] | null>(null)
  const [to, setTo] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const target = () => kind === 'local' ? { kind, path: f.path, password: f.password }
    : kind === 's3' ? { kind, endpoint: f.endpoint, bucket: f.bucket, path: f.path, access: f.access, secret: f.secret, password: f.password }
      : { kind, path: f.path, options: { host: f.sftp_host, user: f.sftp_user, pass: f.sftp_pass }, password: f.password }
  const set = (k: string) => (e: { target: { value: string } }) => { setF({ ...f, [k]: e.target.value }); setSnaps(null) }
  const read = async () => {
    setBusy(true); setErr(''); setPick(null)
    try { setSnaps((await post<{ items: Snap[] }>(`/api/v1/hosts/${host}/backup/external/snapshots`, { target: target() })).items.slice().reverse()) } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  const ls = async (s: Snap, d: string) => {
    setDir(d); setNodes(null)
    try { setNodes((await post<{ items: Node[] }>(`/api/v1/hosts/${host}/backup/external/ls`, { target: target(), snapshot: s.id, dir: d })).items) } catch (e) { setErr(errText(e)) }
  }
  useEffect(() => { if (pick) { setTo('/srv/restore/' + pick.short_id); void ls(pick, pick.paths[0] ? pick.paths[0].replace(/\/[^/]*$/, '') || '/' : '/') } }, [pick])
  const ready = f.password.length > 0 && (kind === 'local' ? f.path.startsWith('/') : kind === 's3' ? !!(f.endpoint && f.bucket && f.access && f.secret) : !!(f.sftp_host && f.sftp_user))
  return (
    <div className="space-y-4">
      <Box title={t('restore.repo_where')}>
        <div className="space-y-3 px-4 py-3">
          <p className="text-[12.5px] text-mute">{t('restore.repo_hint')}</p>
          <Segmented value={kind} onChange={(k) => { setKind(k); setSnaps(null) }} options={(['s3', 'sftp', 'local'] as Kind[]).map((k) => ({ value: k, label: t('restore.kind_' + k) }))} />
          {kind === 's3' && <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Endpoint"><Input className="num" value={f.endpoint} placeholder="https://s3.amazonaws.com" onChange={set('endpoint')} /></Field>
            <Field label="Bucket"><Input className="num" value={f.bucket} onChange={set('bucket')} /></Field>
            <Field label="Access Key"><Input className="num" value={f.access} onChange={set('access')} /></Field>
            <Field label="Secret Key"><Input type="password" value={f.secret} onChange={set('secret')} /></Field>
          </div>}
          {kind === 'sftp' && <div className="grid gap-3 sm:grid-cols-3">
            <Field label={t('restore.sftp_host')}><Input className="num" value={f.sftp_host} placeholder="backup.example.com" onChange={set('sftp_host')} /></Field>
            <Field label={t('restore.sftp_user')}><Input className="num" value={f.sftp_user} onChange={set('sftp_user')} /></Field>
            <Field label={t('restore.sftp_pass')}><Input type="password" value={f.sftp_pass} onChange={set('sftp_pass')} /></Field>
          </div>}
          <Field label={kind === 'local' ? t('restore.repo_path') : t('restore.repo_prefix')}><Input className="num" value={f.path} placeholder={kind === 'local' ? '/mnt/usb/berth-backups' : 'berth'} onChange={set('path')} /></Field>
          <Field label={t('restore.repo_password')} hint={t('restore.repo_password_hint')}><Input type="password" value={f.password} onChange={set('password')} aria-label={t('restore.repo_password')} /></Field>
          <Button variant="primary" disabled={!ready || busy} onClick={() => void read()}>{busy ? t('restore.reading') : t('restore.read_repo')}</Button>
          {err && <Notice tone="bad">{err}</Notice>}
        </div>
      </Box>
      {snaps && (
        <Box title={t('restore.snapshots')} count={snaps.length}>
          {snaps.length === 0 && <p className="px-4 py-3 text-[12.5px] text-mute">{t('restore.no_snaps')}</p>}
          {snaps.map((s) => (
            <Row key={s.id} onClick={() => setPick(s)} className={pick?.id === s.id ? 'bg-tint' : ''}>
              <Archive className="size-4 text-mute" />
              <div className="min-w-0 flex-1">
                <div className="text-[13px]"><b className="num">{s.short_id}</b> · {datetime(Date.parse(s.time) / 1000)} · {s.hostname}</div>
                <div className="num truncate text-[12px] text-mute">{s.paths.join('  ')}</div>
              </div>
              {(s.tags ?? []).filter((x) => x.startsWith('job:')).map((x) => <Pill key={x}>{x.slice(4)}</Pill>)}
            </Row>
          ))}
        </Box>
      )}
      {pick && (
        <Box title={t('restore.restore_snap', { id: pick.short_id })}>
          <div className="space-y-3 px-4 py-3">
            <div className="rounded-lg border border-line">
              <div className="num flex items-center gap-2 border-b border-line px-3 py-1.5 text-[12px] text-mute"><FolderOpen className="size-3.5" />{dir}
                {dir !== '/' && <button className="ml-auto text-acc hover:underline" onClick={() => void ls(pick, dir.replace(/\/[^/]*$/, '') || '/')}>{t('restore.up')}</button>}</div>
              <div className="max-h-56 overflow-auto">
                {!nodes && <p className="px-3 py-2 text-[12px] text-mute">{t('common.loading')}</p>}
                {nodes?.filter((n) => n.path !== dir).map((n) => (
                  <div key={n.path} className="num flex items-center gap-2 px-3 py-1 text-[12.5px]">
                    {n.type === 'dir' ? <button className="text-acc hover:underline" onClick={() => void ls(pick, n.path)}>{n.name}/</button> : <span>{n.name}</span>}
                    <span className="ml-auto text-mute">{n.type === 'dir' ? '' : bytes(n.size)}</span>
                  </div>
                ))}
              </div>
            </div>
            <Field label={t('restore.to')} hint={t('restore.to_hint')}><Input className="num" value={to} onChange={(e) => setTo(e.target.value.trim())} aria-label={t('restore.to')} /></Field>
            <Button variant="primary" disabled={!to.startsWith('/')} onClick={() => run('backup.restore_external', { target: target(), snapshot: pick.id, to, include: dir !== '/' && dir !== (pick.paths[0] ?? '') ? [dir] : undefined }, { host })}>
              <ArrowDownToLine className="size-3.5" />{dir !== '/' ? t('restore.restore_dir', { dir }) : t('restore.restore_all')}</Button>
          </div>
        </Box>
      )}
    </div>
  )
}
