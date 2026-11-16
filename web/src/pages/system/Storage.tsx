import { useState } from 'react'
import { ChevronRight, HardDrive, MoreHorizontal, Plus, RefreshCw } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Select, Switch } from '@/components/ui/Field'
import { Menu, type MenuEntry } from '@/components/ui/Menu'
import { cx } from '@/components/ui/cx'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Smart { ok?: boolean; temp_c?: number; power_on_hours?: number; used_pct?: number; reallocated?: number; media_errors?: number; error?: string }
interface Part { name: string; path: string; size: number; fstype?: string; uuid?: string; label?: string; mounts?: string[]; used?: number; avail?: number; system: boolean; holders?: string[]; in_fstab: boolean; by_berth: boolean }
interface Disk { name: string; path: string; size: number; model?: string; tran?: string; rota: boolean; type: string; system: boolean; smart?: Smart; parts: Part[]; fstype?: string; uuid?: string; label?: string; mounts?: string[]; holders?: string[]; used?: number; avail?: number }
interface MD { name: string; path: string; level: string; state: string; size: number; devices: string[]; failed: string[]; sync?: string; degraded: boolean; fstype?: string; mounts?: string[] }
interface Btrfs { uuid: string; label?: string; devices: string[]; data: string; meta: string; mounts?: string[]; errors: number; missing: boolean; system: boolean }
interface Storage { disks: Disk[]; arrays: MD[]; btrfs: Btrfs[]; tools: string[] }

// A device that can be formatted, mounted or used in an array.
type Vol = { path: string; name: string; size: number; fstype?: string; label?: string; mounts?: string[]; used?: number; avail?: number; system: boolean; holders?: string[] }

const busy = (v: Vol) => v.system || !!v.mounts?.length || !!v.holders?.length

// Disks, RAID, space and clean-up (方案 §5.14 硬盘).
export function Disks() {
  const t = useT()
  const { host } = useHost()
  const d = useApi<Storage>(`/api/v1/hosts/${host}/system/storage`, [host])
  useEvents(['change.done', 'task.update'], () => void d.reload())
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  return (
    <div className="space-y-4">
      {d.data.disks.map((disk) => <DiskBox key={disk.path} disk={disk} />)}
      <RaidBox st={d.data} />
      <SpaceBox />
      <CleanupBox />
    </div>
  )
}

function SmartLine({ s }: { s: Smart }) {
  const t = useT()
  if (s.ok === undefined) return <span className="text-[12px] text-mute" title={s.error}>{t('disk.smart_unknown')}</span>
  return (
    <span className="flex flex-wrap items-center gap-2 text-[12px]">
      <span className="flex items-center gap-1"><Dot state={s.ok ? 'ok' : 'bad'} />{s.ok ? t('disk.healthy') : t('disk.failing')}</span>
      {!!s.temp_c && <span className="num text-mute">{s.temp_c}°C</span>}
      {s.used_pct !== undefined && s.used_pct > 0 && <span className="text-mute">{t('disk.wear', { n: s.used_pct })}</span>}
      {!!s.power_on_hours && <span className="text-mute">{t('disk.hours', { n: s.power_on_hours })}</span>}
      {!!s.reallocated && <Pill tone="warn">{t('disk.realloc', { n: s.reallocated })}</Pill>}
      {!!s.media_errors && <Pill tone="warn">{t('disk.media_errors', { n: s.media_errors })}</Pill>}
    </span>
  )
}

function DiskBox({ disk }: { disk: Disk }) {
  const t = useT()
  const vols: Vol[] = disk.parts.length ? disk.parts : [{ ...disk, system: disk.system }]
  return (
    <Box title={<span className="flex flex-wrap items-center gap-2"><HardDrive className="size-4 text-mute" /><span className="num">{disk.name}</span>
      <span className="font-normal text-mute">{[disk.model, bytes(disk.size), disk.tran?.toUpperCase(), disk.type === 'loop' ? 'loop' : disk.rota ? t('system.hdd') : 'SSD'].filter(Boolean).join(' · ')}</span>
      {disk.system && <Pill>{t('disk.system')}</Pill>}</span>}
      right={disk.smart && <SmartLine s={disk.smart} />}>
      {vols.map((v) => <VolRow key={v.path} v={v} wholeDisk={!disk.parts.length} />)}
    </Box>
  )
}

function VolRow({ v, wholeDisk }: { v: Vol; wholeDisk?: boolean }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'disk.manage')
  const [mount, setMount] = useState(false)
  const [format, setFormat] = useState(false)
  const total = (v.used ?? 0) + (v.avail ?? 0)
  const ratio = total ? (v.used ?? 0) / total : 0
  const mounted = (v.mounts ?? []).filter((m) => m !== '[SWAP]')
  const items: MenuEntry[] = []
  if (manage && !v.system) {
    if (mounted.length) items.push({ label: t('action.disk.unmount'), onClick: () => run('disk.unmount', { path: mounted[0] }, { host }) })
    else if (v.fstype && !v.holders?.length && v.fstype !== 'linux_raid_member' && v.fstype !== 'swap') items.push({ label: t('disk.mount_dots'), onClick: () => setMount(true) })
    if (!busy(v)) items.push('sep', { label: t('disk.format_dots'), danger: true, onClick: () => setFormat(true) })
  }
  return (
    <>
      <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 border-b border-line px-4 py-2 text-[12.5px] last:border-b-0 sm:grid-cols-[11rem_5.5rem_minmax(0,1fr)_minmax(0,1.3fr)_2rem]">
        <span className="num truncate font-semibold">{wholeDisk ? t('disk.whole') : v.name}{v.label && <span className="ml-1.5 font-normal text-mute">{v.label}</span>}</span>
        <span className="num text-mute max-sm:hidden">{bytes(v.size)}</span>
        <span className="flex min-w-0 items-center gap-1.5 truncate text-mute max-sm:hidden">
          {v.fstype === 'linux_raid_member' ? t('disk.raid_member') : v.fstype || t('disk.no_fs')}
          {mounted.length ? <span className="truncate">· {mounted.join(', ')}</span> : v.mounts?.includes('[SWAP]') ? '· swap' : v.fstype ? '· ' + t('system.not_mounted') : ''}
          {v.system && <Pill>{t('disk.system')}</Pill>}
        </span>
        {total > 0 ? (
          <span className="flex items-center gap-2 max-sm:hidden">
            <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-soft"><span className={cx('block h-full rounded-full', ratio >= 0.92 ? 'bg-bad' : ratio >= 0.8 ? 'bg-warn' : 'bg-acc')} style={{ width: ratio * 100 + '%' }} /></span>
            <span className="num w-32 text-right">{bytes(v.used)} / {bytes(total)}</span>
          </span>
        ) : <span className="max-sm:hidden" />}
        <span>{items.length > 0 && <Menu items={items} trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} />}</span>
      </div>
      {mount && <MountDialog v={v} onClose={() => setMount(false)} />}
      {format && <FormatDialog v={v} onClose={() => setFormat(false)} />}
    </>
  )
}

function MountDialog({ v, onClose }: { v: Vol; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [path, setPath] = useState('/mnt/' + (v.label || v.name).toLowerCase().replace(/[^a-z0-9_-]/g, '-'))
  const [persist, setPersist] = useState(true)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('disk.mount_title', { dev: v.name })}
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!/^\/[\w./-]+$/.test(path)} onClick={() => run('disk.mount', { device: v.path, path, persist }, { host, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3">
        <Field label={t('disk.mount_at')} hint={t('disk.mount_hint')}><Input autoFocus className="num" value={path} onChange={(e) => setPath(e.target.value)} /></Field>
        <Switch checked={persist} onChange={setPersist} label={t('disk.at_boot')} />
      </div>
    </Dialog>
  )
}

function FormatDialog({ v, onClose }: { v: Vol; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [fs, setFs] = useState('ext4')
  const [label, setLabel] = useState('')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('disk.format_title', { dev: v.name })}
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="danger" onClick={() => run('disk.format', { device: v.path, fstype: fs, label }, { host, confirmName: v.name, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3">
        <Notice tone="bad">{t('disk.format_warn', { dev: v.path, size: bytes(v.size) })}</Notice>
        <Field label={t('disk.fstype')} hint={t('disk.fs_hint_' + fs)}>
          <Segmented value={fs} onChange={setFs} options={['ext4', 'xfs', 'btrfs', 'exfat'].map((x) => ({ value: x, label: x }))} />
        </Field>
        <Field label={t('disk.label')}><Input maxLength={16} value={label} onChange={(e) => setLabel(e.target.value)} placeholder="data" /></Field>
      </div>
    </Dialog>
  )
}

function RaidBox({ st }: { st: Storage }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'disk.manage')
  const [create, setCreate] = useState(false)
  const [addTo, setAddTo] = useState<MD | null>(null)
  const multi = st.btrfs.filter((b) => b.devices.length > 1 || b.data.startsWith('raid'))
  const free: Vol[] = st.disks.flatMap((d): Vol[] => d.parts.length ? d.parts : [d]).filter((v) => !busy(v))
  const canMD = st.tools.includes('mdadm')
  return (
    <Box title={t('disk.raid')} count={st.arrays.length + multi.length} right={manage && (canMD || st.tools.includes('mkfs.btrfs')) && <Button size="sm" onClick={() => setCreate(true)}><Plus className="size-3.5" />{t('action.raid.create')}</Button>}>
      {st.arrays.length + multi.length === 0 && <div className="px-4 py-2.5 text-[12.5px] text-mute">{t('disk.no_raid')}</div>}
      {st.arrays.map((a) => (
        <div key={a.name} className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-2 text-[12.5px] last:border-b-0">
          <Dot state={a.degraded || a.failed.length ? 'bad' : a.sync ? 'busy' : 'ok'} />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2"><b className="num">{a.path.replace('/dev/md/', '')}</b><span className="text-mute">{a.level} · {bytes(a.size)}</span>
              {a.degraded && <Pill tone="bad">{t('disk.degraded')}</Pill>}{a.sync && <Pill tone="warn">{a.sync}</Pill>}</div>
            <div className="num truncate text-[12px] text-mute">{a.devices.join(' ')}{a.failed.length > 0 && <span className="text-bad"> · {t('disk.failed_members', { list: a.failed.join(' ') })}</span>}
              {a.fstype && ` · ${a.fstype}`}{a.mounts?.length ? ` · ${a.mounts.join(', ')}` : ''}</div>
          </div>
          {manage && <Menu items={[
            { label: t('action.raid.scrub'), onClick: () => run('raid.scrub', { target: a.path }, { host }) },
            { label: t('disk.add_member'), onClick: () => setAddTo(a) },
            ...[...a.failed, ...a.devices].map((m) => ({ label: t('disk.remove_member', { dev: m }), onClick: () => run('raid.repair', { array: a.path, op: 'remove', device: m }, { host }) })),
            'sep' as const,
            { label: t('action.raid.delete'), danger: true, disabled: !!a.mounts?.length, onClick: () => run('raid.delete', { array: a.path }, { host, confirmName: a.path.replace('/dev/md/', '') }) },
          ]} trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} />}
        </div>
      ))}
      {multi.map((b) => (
        <div key={b.uuid} className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-2 text-[12.5px] last:border-b-0">
          <Dot state={b.missing || b.errors ? 'bad' : 'ok'} />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2"><b>{b.label || 'Btrfs'}</b><span className="text-mute">Btrfs · {t('disk.btrfs_profile', { data: b.data || '?', meta: b.meta || '?' })}</span>
              {b.missing && <Pill tone="bad">{t('disk.missing_dev')}</Pill>}{b.errors > 0 && <Pill tone="warn">{t('disk.btrfs_errors', { n: b.errors })}</Pill>}</div>
            <div className="num truncate text-[12px] text-mute">{b.devices.join(' ')}{b.mounts?.length ? ` · ${b.mounts.join(', ')}` : ''}</div>
          </div>
          {manage && b.mounts?.length ? <Button size="sm" variant="ghost" onClick={() => run('raid.scrub', { target: b.mounts![0] }, { host })}>{t('action.raid.scrub')}</Button> : null}
        </div>
      ))}
      {create && <CreateRaid free={free} tools={st.tools} onClose={() => setCreate(false)} />}
      {addTo && <AddMember a={addTo} free={free} onClose={() => setAddTo(null)} />}
    </Box>
  )
}

function CreateRaid({ free, tools, onClose }: { free: Vol[]; tools: string[]; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [kind, setKind] = useState<'md' | 'btrfs'>(tools.includes('mdadm') ? 'md' : 'btrfs')
  const [level, setLevel] = useState('raid1')
  const [name, setName] = useState('data')
  const [sel, setSel] = useState<string[]>([])
  const levels = kind === 'md' ? ['raid1', 'raid0', 'raid5', 'raid6', 'raid10'] : ['raid1', 'raid0', 'raid10', 'raid1c3']
  const min = { raid0: 2, raid1: 2, raid5: 3, raid6: 4, raid10: 4, raid1c3: 3 }[level] ?? 2
  const ok = /^[a-z0-9][a-z0-9_-]{0,30}$/.test(name) && sel.length >= min
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('action.raid.create')} wide
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="danger" disabled={!ok} onClick={() => run('raid.create', { kind, name, level, devices: sel }, { host, confirmName: name, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('disk.raid_kind')}>
            <Segmented value={kind} onChange={(k) => { setKind(k as 'md' | 'btrfs'); setLevel('raid1') }} options={[{ value: 'md', label: 'mdadm' }, { value: 'btrfs', label: 'Btrfs' }].filter((o) => o.value === 'md' ? tools.includes('mdadm') : tools.includes('mkfs.btrfs'))} />
          </Field>
          <Field label={t('disk.raid_name')}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        </div>
        <Field label={t('disk.raid_level')} hint={t('disk.level_' + level)}>
          <Select value={level} onChange={(e) => setLevel(e.target.value)}>{levels.map((l) => <option key={l} value={l}>{l.toUpperCase()}</option>)}</Select>
        </Field>
        <Field label={t('disk.raid_devices', { n: min })} hint={t('disk.raid_wipe')}>
          {free.length === 0 ? <div className="text-[12.5px] text-mute">{t('disk.no_free')}</div> : <div className="space-y-1.5">
            {free.map((v) => <Switch key={v.path} checked={sel.includes(v.path)} onChange={(c) => setSel(c ? [...sel, v.path] : sel.filter((x) => x !== v.path))}
              label={<span className="num">{v.path} <span className="text-mute">· {bytes(v.size)}{v.fstype ? ' · ' + v.fstype : ''}</span></span>} />)}
          </div>}
        </Field>
      </div>
    </Dialog>
  )
}

function AddMember({ a, free, onClose }: { a: MD; free: Vol[]; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [dev, setDev] = useState(free[0]?.path ?? '')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('disk.add_member_to', { name: a.path.replace('/dev/md/', '') })}
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="danger" disabled={!dev} onClick={() => run('raid.repair', { array: a.path, op: 'add', device: dev }, { host, onDone: onClose })}>{t('common.continue')}</Button></>}>
      {free.length === 0 ? <div className="text-[13px] text-mute">{t('disk.no_free')}</div> :
        <Select value={dev} onChange={(e) => setDev(e.target.value)}>{free.map((v) => <option key={v.path} value={v.path}>{v.path} · {bytes(v.size)}</option>)}</Select>}
    </Dialog>
  )
}

interface DirUse { path: string; bytes: number; dir: boolean }
interface Space { path: string; total: number; items: DirUse[]; big: DirUse[]; at: number; partial?: boolean }

// Where the space went: one directory at a time, biggest first; the largest files below.
function SpaceBox() {
  const t = useT()
  const { host } = useHost()
  const [path, setPath] = useState<string | null>(null)
  const [data, setData] = useState<Space | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const load = (p: string, refresh = false) => {
    setPath(p); setLoading(true); setErr('')
    get<Space>(`/api/v1/hosts/${host}/system/space?path=${encodeURIComponent(p)}${refresh ? '&refresh=1' : ''}`).then(setData).catch((e: Error) => setErr(e.message)).finally(() => setLoading(false))
  }
  const crumbs = path ? path.split('/').filter(Boolean).reduce<string[]>((acc, s) => [...acc, (acc[acc.length - 1] ?? '') + '/' + s], ['/']) : []
  const max = data?.items[0]?.bytes ?? 1
  return (
    <Box title={t('disk.space')} right={path && <Button size="sm" variant="ghost" disabled={loading} onClick={() => load(path, true)}><RefreshCw className={cx('size-3.5', loading && 'animate-spin')} />{t('disk.rescan')}</Button>}>
      {!path ? <div className="flex items-center gap-3 px-4 py-3 text-[12.5px] text-mute"><span className="flex-1">{t('disk.space_hint')}</span><Button size="sm" onClick={() => load('/')}>{t('disk.space_scan')}</Button></div> : <>
        <div className="flex flex-wrap items-center gap-1 border-b border-line px-4 py-2 text-[12.5px]">
          {crumbs.map((c, i) => <span key={c} className="flex items-center gap-1">{i > 0 && <ChevronRight className="size-3 text-mute" />}<button className="num hover:underline" onClick={() => load(c)}>{i === 0 ? '/' : c.split('/').pop()}</button></span>)}
          {data && data.path === path && <span className="num ml-auto text-mute">{bytes(data.total)}{data.partial ? ' · ' + t('disk.partial') : ''}</span>}
        </div>
        {err && <div className="px-4 py-2 text-[12.5px] text-bad">{err}</div>}
        {loading && !data && <div className="px-4 py-3 text-[12.5px] text-mute">{t('disk.scanning')}</div>}
        {data && <div className={cx(loading && 'opacity-50')}>
          {data.items.slice(0, 25).map((it) => (
            <button key={it.path} disabled={!it.dir} onClick={() => load(it.path)} className="flex w-full items-center gap-3 border-b border-line px-4 py-1.5 text-left text-[12.5px] enabled:hover:bg-soft">
              <span className="num w-56 truncate">{it.path.split('/').pop()}{it.dir ? '/' : ''}</span>
              <span className="h-2 flex-1 overflow-hidden rounded-sm bg-soft"><span className="block h-full rounded-sm bg-acc/70" style={{ width: Math.max(0.5, (it.bytes / max) * 100) + '%' }} /></span>
              <span className="num w-20 text-right">{bytes(it.bytes)}</span>
            </button>
          ))}
          {data.big.length > 0 && <>
            <div className="bg-soft px-4 py-1.5 text-[12px] text-mute">{t('disk.big_files')}</div>
            {data.big.slice(0, 10).map((f) => (
              <div key={f.path} className="flex items-center gap-3 border-b border-line px-4 py-1 text-[12px] last:border-b-0">
                <span className="num min-w-0 flex-1 truncate" title={f.path}>{f.path}</span><span className="num w-20 text-right">{bytes(f.bytes)}</span>
              </div>
            ))}
          </>}
        </div>}
      </>}
    </Box>
  )
}

function CleanupBox() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const d = useApi<{ items: { key: string; bytes: number }[] }>(`/api/v1/hosts/${host}/system/cleanup`, [host])
  useEvents(['change.done'], () => void d.reload())
  const items = d.data?.items ?? []
  return (
    <Box title={t('disk.cleanup')}>
      {items.length === 0 && <Empty title={d.data ? t('disk.nothing_to_clean') : t('common.loading')} />}
      {items.map((it) => (
        <div key={it.key} className="flex items-center gap-3 border-b border-line px-4 py-2 text-[12.5px] last:border-b-0">
          <div className="min-w-0 flex-1"><div className="font-semibold">{t('disk.clean_' + it.key)}</div><div className="text-[12px] text-mute">{t('disk.clean_' + it.key + '_d')}</div></div>
          <span className="num w-24 text-right">{it.bytes > 0 ? bytes(it.bytes) : '—'}</span>
          {can(user, 'system.manage') && <Button size="sm" disabled={it.bytes <= 0} onClick={() => run('disk.cleanup', { key: it.key }, { host })}>{t('action.disk.cleanup')}</Button>}
        </div>
      ))}
    </Box>
  )
}
