import { AlertTriangle, Copy, FolderInput, Globe, Network, Plus, Router, Shield, Unplug } from 'lucide-react'
import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Select, Switch } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

export type NetMode = 'nat' | 'lan' | 'isolated' | 'custom'
export interface VmNet { mode: NetMode; parent?: string; egress: 'all' | 'lan' | 'none'; mac?: string }
export interface VmShare { id: string; source: string; path: string; readonly: boolean }
export interface VmPerms { autostart: boolean; nesting: boolean; privileged: boolean; shares: VmShare[] }
export interface Fwd { name: string; proto: string; host_port: number; target_port: number }
export interface NetInst { name: string; type: 'container' | 'virtual-machine'; status: string; ipv4: string[]; forwards: Fwd[]; net?: VmNet; perms?: VmPerms }
interface LanPort { name: string; wireless: boolean; bridge: boolean; up: boolean; address?: string; default: boolean }

const MODES: { mode: Exclude<NetMode, 'custom'>; Icon: typeof Router }[] = [{ mode: 'nat', Icon: Router }, { mode: 'lan', Icon: Network }, { mode: 'isolated', Icon: Unplug }]

// NetworkBox: how the machine is connected, what it may reach, and how others reach it.
export function NetworkBox({ inst: i, manage }: { inst: NetInst; manage: boolean }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const lan = useApi<{ ports: LanPort[]; host_ip: string }>(`/api/v1/hosts/${host}/vms-lan`, [host])
  const net = i.net ?? { mode: 'nat', egress: 'all' }
  const [want, setWant] = useState<Exclude<NetMode, 'custom'> | null>(null)
  const [fwd, setFwd] = useState(false)
  const hostIP = lan.data?.host_ip ?? location.hostname
  const openFw = (port: number, proto: string) => run('firewall.rule.set', { action: 'allow', proto, ports: String(port), comment: `${i.name} ${proto}/${port}`, enabled: true }, { host })
  return (
    <Box title={t('vmnet.title')}>
      <div className="space-y-4 px-4 py-3">
        <div className="grid gap-2 sm:grid-cols-3">
          {MODES.map(({ mode, Icon }) => (
            <button key={mode} type="button" disabled={!manage} onClick={() => mode !== net.mode && setWant(mode)}
              className={cx('rounded-xl border p-3 text-left', net.mode === mode ? 'border-acc bg-tint' : 'border-line hover:border-mute', !manage && 'cursor-default')}>
              <div className="flex items-center gap-1.5 text-[13px] font-semibold"><Icon className="size-4 text-acc" />{t('vmnet.m_' + mode)}{net.mode === mode && <Pill tone="acc">{t('vmnet.current')}</Pill>}</div>
              <div className="mt-0.5 text-[11.5px] leading-snug text-mute">{t('vmnet.m_' + mode + '_d')}</div>
            </button>
          ))}
        </div>
        {net.mode === 'custom' && <Notice>{t('vmnet.custom')}</Notice>}

        {net.mode === 'nat' && <>
          <div>
            <div className="mb-1 flex items-center gap-1.5 text-[13px] font-semibold"><Shield className="size-4 text-acc" />{t('vmnet.egress')}</div>
            <Segmented value={net.egress} onChange={(e) => manage && run('vm.network', { name: i.name, mode: 'nat', egress: e }, { host })}
              options={(['all', 'lan', 'none'] as const).map((e) => ({ value: e, label: t('vmnet.e_' + e) }))} />
            <p className="mt-1 text-[12px] text-mute">{t('vmnet.e_' + net.egress + '_d')}</p>
          </div>
          <div>
            <div className="mb-1 flex items-center gap-2">
              <span className="flex-1 text-[13px] font-semibold">{t('vms.forwards')} <span className="font-normal text-mute">{i.forwards.length || ''}</span></span>
              {manage && <Button size="sm" onClick={() => setFwd(true)}><Plus className="size-3.5" />{t('vms.add_forward')}</Button>}
            </div>
            <p className="mb-1.5 text-[12px] text-mute">{t('vmnet.forward_how')}</p>
            {i.forwards.length === 0 && <p className="text-[12.5px] text-mute">{t('vms.no_forwards')}</p>}
            {i.forwards.map((f) => (
              <Row key={f.name} className="!px-0">
                <span className="num min-w-0 flex-1 text-[12.5px]"><b>{hostIP}:{f.host_port}</b> → {i.name}:{f.target_port} <span className="text-mute">{f.proto.toUpperCase()}</span></span>
                <button type="button" aria-label={t('common.copy')} className="text-mute hover:text-fg" onClick={() => void navigator.clipboard?.writeText(`${hostIP}:${f.host_port}`)}><Copy className="size-3.5" /></button>
                {manage && <Button size="sm" variant="ghost" onClick={() => openFw(f.host_port, f.proto)}>{t('vmnet.open_fw')}</Button>}
                {manage && <Button size="sm" variant="ghost" onClick={() => run('vm.forward', { name: i.name, proto: f.proto, host_port: f.host_port, target_port: 0 }, { host })}>{t('common.delete')}</Button>}
              </Row>
            ))}
          </div>
        </>}

        {net.mode === 'lan' && (
          <div className="space-y-1 text-[12.5px]">
            <div><span className="text-mute">{t('vmnet.lan_port')}</span> <b className="num">{net.parent}</b></div>
            <div><span className="text-mute">{t('vms.address')}</span> <b className="num">{i.ipv4[0] ?? t('vmnet.lan_waiting')}</b></div>
            {net.mac && <div><span className="text-mute">MAC</span> <span className="num select-all">{net.mac}</span></div>}
            <p className="text-mute">{t('vmnet.lan_tip')}</p>
          </div>
        )}
        {net.mode === 'isolated' && <p className="text-[12.5px] text-mute">{t('vmnet.isolated_tip')}</p>}
      </div>
      {want && <NetDialog inst={i} mode={want} ports={lan.data?.ports ?? []} onClose={() => setWant(null)} />}
      {fwd && <ForwardDialog inst={i} hostIP={hostIP} onClose={() => setFwd(false)} />}
    </Box>
  )
}

function NetDialog({ inst: i, mode, ports, onClose }: { inst: NetInst; mode: Exclude<NetMode, 'custom'>; ports: LanPort[]; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const usable = ports.filter((p) => !p.wireless || p.bridge)
  const [parent, setParent] = useState(usable[0]?.name ?? '')
  const [egress, setEgress] = useState<'all' | 'lan' | 'none'>('all')
  const p = ports.find((x) => x.name === parent)
  const ok = mode !== 'lan' || !!parent
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('vmnet.switch_to', { mode: t('vmnet.m_' + mode) })}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!ok} onClick={() => run('vm.network', { name: i.name, mode, parent: mode === 'lan' ? parent : undefined, egress: mode === 'nat' ? egress : undefined }, { host, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <p className="text-mute">{t('vmnet.m_' + mode + '_long')}</p>
        {mode === 'lan' && (usable.length === 0
          ? <Notice tone="warn">{t('vmnet.no_wired')}</Notice>
          : <>
            <Field label={t('vmnet.lan_port')}>
              <Select value={parent} onChange={(e) => setParent(e.target.value)}>
                {usable.map((x) => <option key={x.name} value={x.name}>{x.name}{x.address ? ` · ${x.address}` : ''}{x.bridge ? ' · ' + t('vmnet.bridge') : ''}{x.default ? ' · ' + t('vmnet.default_route') : ''}</option>)}
              </Select>
            </Field>
            {p && !p.bridge && <Notice>{t('vmnet.macvlan_note')}</Notice>}
            {ports.some((x) => x.wireless && !x.bridge) && <p className="text-[12px] text-mute">{t('vmnet.wifi_note')}</p>}
          </>)}
        {mode === 'nat' && (
          <Field label={t('vmnet.egress')}>
            <Segmented value={egress} onChange={setEgress} options={(['all', 'lan', 'none'] as const).map((e) => ({ value: e, label: t('vmnet.e_' + e) }))} />
          </Field>
        )}
        {mode !== 'nat' && i.forwards.length > 0 && <Notice tone="warn">{t('vmnet.forwards_go', { n: i.forwards.length })}</Notice>}
        {i.status === 'Running' && <p className="text-[12px] text-mute">{t('vmnet.restart_note')}</p>}
      </div>
    </Dialog>
  )
}

const PRESETS = [{ k: 'ssh', host: 2222, target: 22 }, { k: 'web', host: 8080, target: 80 }, { k: 'https', host: 8443, target: 443 }, { k: 'rdp', host: 13389, target: 3389 }]

function ForwardDialog({ inst, hostIP, onClose }: { inst: NetInst; hostIP: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [v, setV] = useState({ proto: 'tcp', host_port: 2222, target_port: 22 })
  const [open, setOpen] = useState(true)
  const go = () => run('vm.forward', { name: inst.name, ...v }, { host, onDone: (c) => {
    onClose()
    if (open && c.status === 'done') run('firewall.rule.set', { action: 'allow', proto: v.proto, ports: String(v.host_port), comment: `${inst.name} ${v.proto}/${v.host_port}`, enabled: true }, { host })
  } })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('vms.add_forward')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={go}>{t('action.vm.forward')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <div className="flex flex-wrap gap-1.5">
          {PRESETS.map((p) => <button key={p.k} type="button" onClick={() => setV({ proto: 'tcp', host_port: p.host, target_port: p.target })}
            className={cx('rounded-full border px-2.5 py-0.5 text-[12px]', v.target_port === p.target ? 'border-acc text-acc' : 'border-line text-mute hover:border-mute')}>{t('vmnet.p_' + p.k)}</button>)}
        </div>
        <div className="grid grid-cols-3 gap-2">
          <Field label={t('network.proto')}><Select value={v.proto} onChange={(e) => setV({ ...v, proto: e.target.value })}><option value="tcp">TCP</option><option value="udp">UDP</option></Select></Field>
          <Field label={t('vms.host_port')}><Input type="number" value={v.host_port} onChange={(e) => setV({ ...v, host_port: Number(e.target.value) })} /></Field>
          <Field label={t('vms.target_port')}><Input type="number" value={v.target_port} onChange={(e) => setV({ ...v, target_port: Number(e.target.value) })} /></Field>
        </div>
        <p className="num rounded-lg bg-soft px-3 py-2 text-[12.5px]">{t('vmnet.will_reach', { addr: `${hostIP}:${v.host_port}`, name: inst.name, port: v.target_port })}</p>
        <Switch checked={open} onChange={setOpen} label={t('vmnet.also_open_fw')} />
      </div>
    </Dialog>
  )
}

// PermsBox: start on boot, Docker inside, privileged, shared folders.
export function PermsBox({ inst: i, manage }: { inst: NetInst; manage: boolean }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const p = i.perms ?? { autostart: false, nesting: false, privileged: false, shares: [] }
  const ct = i.type === 'container'
  const [v, setV] = useState({ autostart: p.autostart, nesting: p.nesting, privileged: p.privileged })
  const [share, setShare] = useState(false)
  const dirty = v.autostart !== p.autostart || v.nesting !== p.nesting || v.privileged !== p.privileged
  return (
    <Box title={t('vmperm.title')} right={manage && dirty && <Button size="sm" variant="primary" onClick={() => run('vm.perms', { name: i.name, ...v }, { host })}>{t('common.save')}</Button>}>
      <div className="space-y-2.5 px-4 py-3">
        <Perm label={t('vmperm.autostart')} hint={t('vmperm.autostart_d')} on={v.autostart} set={(x) => setV({ ...v, autostart: x })} disabled={!manage} />
        {ct && <Perm label={t('vmperm.nesting')} hint={t('vmperm.nesting_d')} on={v.nesting} set={(x) => setV({ ...v, nesting: x })} disabled={!manage} />}
        {ct && <Perm label={t('vmperm.privileged')} hint={t('vmperm.privileged_d')} on={v.privileged} set={(x) => setV({ ...v, privileged: x })} disabled={!manage} danger />}
        {!ct && <p className="text-[12px] text-mute">{t('vmperm.vm_note')}</p>}
        <div className="border-t border-line pt-2.5">
          <div className="mb-1 flex items-center gap-2">
            <span className="flex flex-1 items-center gap-1.5 text-[13px] font-semibold"><FolderInput className="size-4 text-acc" />{t('vmperm.shares')}</span>
            {manage && <Button size="sm" onClick={() => setShare(true)}><Plus className="size-3.5" />{t('vmperm.add_share')}</Button>}
          </div>
          {p.shares.length === 0 && <p className="text-[12.5px] text-mute">{t('vmperm.no_shares')}</p>}
          {p.shares.map((s) => (
            <Row key={s.id} className="!px-0">
              <span className="num min-w-0 flex-1 truncate text-[12.5px]">{s.source} → {s.path}</span>
              <Pill tone={s.readonly ? undefined : 'warn'}>{s.readonly ? t('vmperm.ro') : t('vmperm.rw')}</Pill>
              {manage && <Button size="sm" variant="ghost" onClick={() => run('vm.share', { name: i.name, id: s.id, source: '' }, { host })}>{t('common.delete')}</Button>}
            </Row>
          ))}
        </div>
      </div>
      {share && <ShareDialog inst={i} onClose={() => setShare(false)} />}
    </Box>
  )
}

function Perm({ label, hint, on, set, disabled, danger }: { label: string; hint: string; on: boolean; set: (v: boolean) => void; disabled?: boolean; danger?: boolean }) {
  return (
    <div className="flex items-start gap-3">
      <div className="min-w-0 flex-1">
        <div className={cx('flex items-center gap-1.5 text-[13px] font-medium', danger && on && 'text-bad')}>{danger && <AlertTriangle className="size-3.5 text-warn" />}{label}</div>
        <div className="text-[12px] text-mute">{hint}</div>
      </div>
      <Switch checked={on} onChange={set} disabled={disabled} label={<span className="sr-only">{label}</span>} />
    </div>
  )
}

function ShareDialog({ inst, onClose }: { inst: NetInst; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [src, setSrc] = useState('/srv/')
  const [dst, setDst] = useState('/mnt/share')
  const [ro, setRo] = useState(true)
  const id = Math.random().toString(36).slice(2, 8)
  const ok = src.startsWith('/') && src !== '/' && dst.startsWith('/') && dst !== '/'
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('vmperm.add_share')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!ok} onClick={() => run('vm.share', { name: inst.name, id, source: src, path: dst, readonly: ro }, { host, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <p className="text-[12.5px] text-mute">{t('vmperm.share_how')}</p>
        <Field label={t('vmperm.share_src')}><Input className="num" value={src} onChange={(e) => setSrc(e.target.value.trim())} aria-label={t('vmperm.share_src')} /></Field>
        <Field label={t('vmperm.share_dst')}><Input className="num" value={dst} onChange={(e) => setDst(e.target.value.trim())} aria-label={t('vmperm.share_dst')} /></Field>
        <Switch checked={ro} onChange={setRo} label={t('vmperm.readonly')} />
        {!ro && <Notice tone="warn">{t('vmperm.rw_warn')}</Notice>}
        {inst.type === 'virtual-machine' && <p className="text-[12px] text-mute">{t('vmperm.vm_share_note')}</p>}
      </div>
    </Dialog>
  )
}

export const netIcon = { nat: Router, lan: Network, isolated: Unplug, custom: Globe }
