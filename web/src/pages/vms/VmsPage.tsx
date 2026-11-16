import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { errText, post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { bytes, datetime } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { requestTerminal } from '@/pages/terminal/store'
import { NewSiteDialog } from '@/pages/sites/NewSiteDialog'
import { FlowsView } from '@/components/FlowsView'
import { ReauthProvider, useReauth } from '../account/reauth'
import { PveSection } from './PvePanel'
import { NetworkBox, PermsBox, type VmNet, type VmPerms } from './VmNetwork'

interface VmStatus { installed: boolean; running: boolean; kvm: boolean; vms: boolean; containers: boolean; network: boolean; page_size: number; arch: string; supported: boolean; version?: string; problems?: string[] }
interface Img { alias: string; os: string; name: string; vm: boolean; cloud: boolean; default?: boolean }
interface Inst {
  name: string; type: 'container' | 'virtual-machine'; status: string; description?: string; created_at: string; image?: string
  cpus?: string; memory?: string; disk?: string; ipv4: string[]; mem_used?: number; disk_used?: number
  snapshots: { name: string; created_at: string }[]; forwards: { name: string; proto: string; host_port: number; target_port: number }[]
  net?: VmNet; perms?: VmPerms
}
interface Data { status: VmStatus; images: Img[]; items: Inst[]; has_password: Record<string, boolean>; sizes: Record<string, [number, number, number]>; my_keys: number }

const running = (i: Inst) => i.status === 'Running'
const kindLabel = (i: Inst, t: ReturnType<typeof useT>) => (i.type === 'virtual-machine' ? t('vms.type_vm') : t('vms.type_ct'))

export default function VmsPage() {
  return <ReauthProvider><Vms /></ReauthProvider>
}

function Vms() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'vms.manage')
  const d = useApi<Data>(`/api/v1/hosts/${host}/vms`, [host])
  useEvents(['change.done', 'task.update'], () => void d.reload())
  // a machine that just started gets its address a few seconds later: keep looking until it has one
  const waiting = d.data?.items.some((i) => running(i) && i.ipv4.length === 0) ?? false
  useInterval(() => void d.reload(), waiting ? 5000 : null)
  const [creating, setCreating] = useState(false)
  const [open, setOpen] = useState<string | null>(null)
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const st = d.data.status
  if (!st.running) {
    return (
      <div className="space-y-4">
      <Box title={t('nav.vms')}>
        <Empty title={st.installed ? t('vms.not_running') : t('vms.not_installed')} action={manage && st.supported ? (
          <Button variant="primary" onClick={() => run('vm.install', {}, { host, onDone: () => void d.reload() })}>{t('action.vm.install')}</Button>) : undefined}>
          <span className="block max-w-xl">{t('vms.install_hint')}</span>
          {!st.kvm && <span className="mt-2 block text-warn">{t('vms.no_kvm_hint')}</span>}
        </Empty>
      </Box>
      <PveSection />
      </div>
    )
  }
  const items = d.data.items
  const inst = items.find((i) => i.name === open)
  return (
    <div className="space-y-4">
      {(st.problems?.length ?? 0) > 0 && manage && <Notice tone="warn"><div className="flex flex-wrap items-center gap-2"><span>{t('vms.problems')}</span>
        <Button size="sm" onClick={() => run('vm.install', {}, { host, onDone: () => void d.reload() })}>{t('vms.fix')}</Button></div></Notice>}
      <Box title={t('nav.vms')} count={items.length} right={manage && <Button size="sm" variant="primary" onClick={() => setCreating(true)}>{t('vms.new')}</Button>}>
        {items.length === 0 && <Empty title={t('vms.none')} action={manage ? <Button variant="primary" onClick={() => setCreating(true)}>{t('vms.new')}</Button> : undefined}>{t('vms.none_hint')}</Empty>}
        {items.map((i) => (
          <Row key={i.name} onClick={() => setOpen(i.name)}>
            <Dot state={running(i) ? 'ok' : i.status === 'Frozen' ? 'warn' : 'off'} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{i.name} <span className="font-normal text-mute">· {i.image || '—'}</span></div>
              <div className="num truncate text-[12px] text-mute">
                {i.ipv4[0] ?? t('vms.no_ip')}{i.cpus ? ` · ${i.cpus} ${t('vms.cpu_unit')}` : ''}{i.memory ? ` · ${i.memory}` : ''}
                {running(i) && i.mem_used ? ` · ${t('vms.mem_used', { v: bytes(i.mem_used) })}` : ''}
              </div>
            </div>
            <Pill tone={i.type === 'virtual-machine' ? 'acc' : undefined}>{kindLabel(i, t)}</Pill>
            {manage && <span className="flex gap-1" onClick={(e) => e.stopPropagation()}>
              {running(i)
                ? <>{can(user, 'terminal.use') && <Button size="sm" variant="ghost" onClick={() => requestTerminal({ target: 'instance', container: i.name })}>{t('vms.shell')}</Button>}
                  <Button size="sm" variant="ghost" onClick={() => run('vm.stop', { name: i.name }, { host })}>{t('action.vm.stop')}</Button></>
                : <Button size="sm" onClick={() => run('vm.start', { name: i.name }, { host })}>{t('action.vm.start')}</Button>}
            </span>}
          </Row>
        ))}
      </Box>
      <PveSection />
      {creating && <CreateDialog data={d.data} onClose={() => setCreating(false)} onDone={(name) => { setCreating(false); void d.reload(); setOpen(name) }} />}
      {inst && <VmDrawer inst={inst} hasPassword={!!d.data.has_password[inst.name]} onClose={() => setOpen(null)} />}
    </div>
  )
}

function CreateDialog({ data, onClose, onDone }: { data: Data; onClose: () => void; onDone: (name: string) => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const def = data.images.find((i) => i.default) ?? data.images[0]
  const [img, setImg] = useState(def)
  const [vm, setVm] = useState(false)
  const [size, setSize] = useState('small')
  const [custom, setCustom] = useState({ cpus: 2, memory_mb: 2048, disk_gb: 20 })
  const used = useMemo(() => new Set(data.items.map((i) => i.name)), [data.items])
  const suggest = (os: string) => { for (let n = 1; ; n++) { const c = `${os}-${n}`; if (!used.has(c)) return c } }
  const [name, setName] = useState(suggest(def.os))
  const [touched, setTouched] = useState(false)
  const pick = (i: Img) => { setImg(i); if (!i.vm) setVm(false); if (!touched) setName(suggest(i.os)) }
  const canVM = data.status.vms && img.vm
  const params = { name, image: img.alias, vm, size, ...(size === 'custom' ? custom : {}) }
  const lan = useApi<{ ports: { name: string; wireless: boolean; bridge: boolean; default: boolean }[] }>(`/api/v1/hosts/${host}/vms-lan`, [host])
  const lanPort = (lan.data?.ports ?? []).find((p) => !p.wireless || p.bridge)
  const [netMode, setNetMode] = useState<'nat' | 'lan'>('nat')
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('vms.new')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!name} onClick={() => run('vm.create', params, { host, onDone: (c) => {
          if (c.status === 'done' && netMode === 'lan' && lanPort) run('vm.network', { name, mode: 'lan', parent: lanPort.name }, { host, onDone: () => onDone(name) })
          else onDone(name)
        } })}>{t('action.vm.create')}</Button></>}>
      <div className="space-y-4 px-4 py-3 text-[13px]">
        <div>
          <div className="mb-1.5 font-semibold">{t('vms.system')}</div>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            {data.images.map((i) => (
              <button key={i.alias} onClick={() => pick(i)} className={cx('rounded-[var(--radius-box)] border p-2 text-left', img.alias === i.alias ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
                <div className="font-semibold">{i.name}</div>
                <div className="text-[11.5px] text-mute">{i.vm ? t('vms.ct_and_vm') : t('vms.ct_only')}</div>
              </button>
            ))}
          </div>
        </div>
        <Field label={t('vms.kind')} hint={vm ? t('vms.kind_vm_hint') : t('vms.kind_ct_hint')}>
          <Segmented value={vm ? 'vm' : 'ct'} options={[{ value: 'ct', label: t('vms.type_ct') }, ...(canVM ? [{ value: 'vm', label: t('vms.type_vm') }] : [])]} onChange={(v) => setVm(v === 'vm')} />
        </Field>
        <div>
          <div className="mb-1.5 font-semibold">{t('vms.size')}</div>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            {(['small', 'medium', 'large'] as const).map((s) => {
              const [c, m, g] = data.sizes[s]
              return (
                <button key={s} onClick={() => setSize(s)} className={cx('rounded-[var(--radius-box)] border p-2 text-left', size === s ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
                  <div className="font-semibold">{t('vms.size_' + s)}</div>
                  <div className="num text-[11.5px] text-mute">{c} {t('vms.cpu_unit')} · {m >= 1024 ? m / 1024 + ' GB' : m + ' MB'} · {g} GB</div>
                </button>
              )
            })}
            <button onClick={() => setSize('custom')} className={cx('rounded-[var(--radius-box)] border p-2 text-left', size === 'custom' ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
              <div className="font-semibold">{t('vms.size_custom')}</div><div className="text-[11.5px] text-mute">{t('vms.size_custom_hint')}</div>
            </button>
          </div>
          {size === 'custom' && <div className="mt-2 grid grid-cols-3 gap-2">
            <Field label={t('vms.cpus')}><Input type="number" min={1} value={custom.cpus} onChange={(e) => setCustom({ ...custom, cpus: Number(e.target.value) })} /></Field>
            <Field label={t('vms.memory_mb')}><Input type="number" min={128} step={128} value={custom.memory_mb} onChange={(e) => setCustom({ ...custom, memory_mb: Number(e.target.value) })} /></Field>
            <Field label={t('vms.disk_gb')}><Input type="number" min={2} value={custom.disk_gb} onChange={(e) => setCustom({ ...custom, disk_gb: Number(e.target.value) })} /></Field>
          </div>}
        </div>
        <Field label={t('vmnet.title')} hint={netMode === 'lan' ? t('vmnet.create_lan_hint', { port: lanPort?.name ?? '' }) : t('vmnet.create_nat_hint')}>
          <Segmented value={netMode} onChange={setNetMode} options={[{ value: 'nat', label: t('vmnet.m_nat') }, ...(lanPort ? [{ value: 'lan' as const, label: t('vmnet.m_lan') }] : [])]} />
        </Field>
        <Field label={t('vms.name')} hint={t('vms.name_hint')}><Input className="num" value={name} onChange={(e) => { setTouched(true); setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, '')) }} /></Field>
        <Notice>{img.cloud ? (data.my_keys > 0 ? t('vms.login_keys', { n: data.my_keys }) : t('vms.login_password')) : t('vms.login_console')}</Notice>
      </div>
    </Dialog>
  )
}

function VmDrawer({ inst: i, hasPassword, onClose }: { inst: Inst; hasPassword: boolean; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const reauth = useReauth()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'vms.manage')
  const [pw, setPw] = useState('')
  const [err, setErr] = useState('')
  const [site, setSite] = useState(false)
  const [resize, setResize] = useState(false)
  const [clone, setClone] = useState(false)
  const nav = useNavigate()
  const ip = i.ipv4[0]
  const isVM = i.type === 'virtual-machine'
  const kv = (k: string, v: React.ReactNode) => <div className="flex gap-3 py-1 text-[13px]"><span className="w-24 shrink-0 text-mute">{k}</span><span className="min-w-0 flex-1">{v}</span></div>
  const showPw = async () => {
    setErr('')
    try { const r = await reauth(() => post<{ password: string }>(`/api/v1/hosts/${host}/vms/${i.name}/password`)); setPw(r.password) } catch (e) { setErr(errText(e)) }
  }
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={<span className="flex items-center gap-2">{i.name}<Pill tone={isVM ? 'acc' : undefined}>{kindLabel(i, t)}</Pill></span>}>
      <div className="space-y-4 p-4">
        {err && <Notice tone="bad">{err}</Notice>}
        {manage && <div className="flex flex-wrap gap-2">
          {running(i) ? <>
            {can(user, 'terminal.use') && <Button size="sm" variant="primary" onClick={() => requestTerminal({ target: 'instance', container: i.name })}>{t('vms.shell')}</Button>}
            {isVM && can(user, 'terminal.use') && <Button size="sm" onClick={() => requestTerminal({ target: 'instance-console', container: i.name })}>{t('vms.console')}</Button>}
            {ip && can(user, 'sites.manage') && <Button size="sm" onClick={() => setSite(true)}>{t('vms.make_site')}</Button>}
            {ip && host === 'local' && can(user, 'remote.use') && <Button size="sm" onClick={() => nav(`/${host}/remote?` + new URLSearchParams({ add: 'rdp', name: i.name, addr: ip, group: t('nav.vms') }))}>{t('vms.remote_desktop')}</Button>}
            <Button size="sm" onClick={() => run('vm.restart', { name: i.name }, { host })}>{t('action.vm.restart')}</Button>
            <Button size="sm" onClick={() => run('vm.stop', { name: i.name }, { host })}>{t('action.vm.stop')}</Button>
            <Button size="sm" variant="ghost" onClick={() => run('vm.kill', { name: i.name }, { host })}>{t('action.vm.kill')}</Button>
          </> : <Button size="sm" variant="primary" onClick={() => run('vm.start', { name: i.name }, { host })}>{t('action.vm.start')}</Button>}
        </div>}
        <div>
          {kv(t('vms.status'), <span className="flex items-center gap-1.5"><Dot state={running(i) ? 'ok' : 'off'} />{t('vms.st_' + i.status.toLowerCase())}</span>)}
          {kv(t('vms.system'), i.image || '—')}
          {kv(t('vms.size'), <span className="num">{i.cpus ?? '—'} {t('vms.cpu_unit')} · {i.memory ?? '—'} · {i.disk ?? '—'}{manage && <button className="ml-2 text-acc hover:underline" onClick={() => setResize(true)}>{t('vms.change')}</button>}</span>)}
          {running(i) && kv(t('vms.usage'), <span className="num">{i.mem_used ? t('vms.mem_used', { v: bytes(i.mem_used) }) : '—'}{(i.disk_used ?? 0) > 1 << 20 ? ' · ' + t('vms.disk_used', { v: bytes(i.disk_used) }) : '' /* btrfs pools report next to nothing: better no number than a wrong one */}</span>)}
          {kv(t('vms.address'), <span className="num">{ip ?? t('vms.no_ip')}</span>)}
          {ip && kv('SSH', <div className="num space-y-1 text-[12px]">
            <div><span className="text-mute">{t('vms.ssh_here')}</span> <span className="select-all">ssh root@{ip}</span></div>
            {i.net?.mode !== 'lan' && <div><span className="text-mute">{t('vms.ssh_from_pc')}</span> <span className="select-all">ssh -J root@{location.hostname} root@{ip}</span></div>}
          </div>)}
          {hasPassword && kv(t('vms.password'), pw ? <span className="num select-all">{pw}</span> : <button className="text-acc hover:underline" onClick={() => void showPw()}>{t('vms.show_password')}</button>)}
          {kv(t('vms.created'), datetime(Date.parse(i.created_at) / 1000))}
        </div>

        <NetworkBox inst={i} manage={manage} />
        <Box title={t('flows.tab')}><FlowsView obj={'vm:' + i.name} /></Box>
        <Box title={t('vms.snapshots')} count={i.snapshots.length} right={manage && <Button size="sm" onClick={() => run('vm.snapshot', { name: i.name }, { host })}>{t('action.vm.snapshot')}</Button>}>
          {i.snapshots.length === 0 && <div className="px-4 py-3 text-[12.5px] text-mute">{t('vms.no_snapshots')}</div>}
          {[...i.snapshots].reverse().map((s) => (
            <Row key={s.name}>
              <span className="num min-w-0 flex-1 truncate text-[13px]">{s.name}</span>
              <span className="num text-[12px] text-mute">{datetime(Date.parse(s.created_at) / 1000)}</span>
              {manage && <><Button size="sm" variant="ghost" onClick={() => run('vm.restore', { name: i.name, snapshot: s.name }, { host })}>{t('action.vm.restore')}</Button>
                <Button size="sm" variant="ghost" onClick={() => run('vm.snapshot.delete', { name: i.name, snapshot: s.name }, { host })}>{t('common.delete')}</Button></>}
            </Row>
          ))}
        </Box>

        <PermsBox inst={i} manage={manage} />

        {manage && <div className="flex flex-wrap gap-2 border-t border-line pt-3">
          <Button size="sm" variant="ghost" onClick={() => setClone(true)}>{t('action.vm.clone')}</Button>
          <Button size="sm" variant="ghost" onClick={() => run('vm.delete', { name: i.name }, { host, confirmName: i.name, onDone: onClose })}>{t('action.vm.delete')}</Button>
        </div>}
      </div>
      {site && ip && <NewSiteDialog preset={{ type: 'address', upstreams: [ip + ':80'] }} onClose={() => setSite(false)} onDone={() => setSite(false)} />}
      {resize && <ResizeDialog inst={i} onClose={() => setResize(false)} />}
      {clone && <CloneDialog inst={i} onClose={() => setClone(false)} />}
    </Drawer>
  )
}

const num = (s?: string) => Number((s ?? '').replace(/[^0-9]/g, '')) || 0
const memMB = (s?: string) => (!s ? 0 : s.endsWith('GiB') ? num(s) * 1024 : num(s))

function ResizeDialog({ inst, onClose }: { inst: Inst; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [v, setV] = useState({ cpus: num(inst.cpus) || 1, memory_mb: memMB(inst.memory) || 512, disk_gb: num(inst.disk) || 10 })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('kind.vm.resize')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => run('vm.resize', { name: inst.name, ...v }, { host, onDone: onClose })}>{t('action.vm.resize')}</Button></>}>
      <div className="grid grid-cols-3 gap-2 px-4 py-3">
        <Field label={t('vms.cpus')}><Input type="number" min={1} value={v.cpus} onChange={(e) => setV({ ...v, cpus: Number(e.target.value) })} /></Field>
        <Field label={t('vms.memory_mb')}><Input type="number" min={128} step={128} value={v.memory_mb} onChange={(e) => setV({ ...v, memory_mb: Number(e.target.value) })} /></Field>
        <Field label={t('vms.disk_gb')} hint={t('vms.disk_grow_only')}><Input type="number" min={num(inst.disk) || 2} value={v.disk_gb} onChange={(e) => setV({ ...v, disk_gb: Number(e.target.value) })} /></Field>
      </div>
    </Dialog>
  )
}

function CloneDialog({ inst, onClose }: { inst: Inst; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [to, setTo] = useState(inst.name + '-copy')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('action.vm.clone')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => run('vm.clone', { name: inst.name, to }, { host, onDone: onClose })}>{t('action.vm.clone')}</Button></>}>
      <div className="px-4 py-3"><Field label={t('vms.clone_name')}><Input className="num" value={to} onChange={(e) => setTo(e.target.value.toLowerCase())} /></Field></div>
    </Dialog>
  )
}
