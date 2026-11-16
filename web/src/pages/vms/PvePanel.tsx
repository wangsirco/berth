import { useEffect, useRef, useState } from 'react'
import RFB from '@novnc/novnc'
import { useChange } from '@/components/change/ChangeDialog'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { cx } from '@/components/ui/cx'
import { errText, post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { bytes, datetime, duration } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { NewSiteDialog } from '@/pages/sites/NewSiteDialog'

interface ErrE { code: string; params?: Record<string, unknown> }
interface Cluster { id: string; name: string; url: string; token_id: string; fingerprint?: string }
interface Node { node: string; status: string; cpu: number; maxcpu: number; mem: number; maxmem: number; uptime: number }
export interface Guest {
  vmid: number; name: string; node: string; type: 'qemu' | 'lxc'; status: string; cpu: number; maxcpu: number
  mem: number; maxmem: number; maxdisk: number; uptime: number; tags?: string; template?: number; lock?: string
}
interface Item { cluster: Cluster; fingerprint_text: string; version?: string; nodes?: Node[]; guests?: Guest[]; error?: ErrE }
interface Snap { name: string; description?: string; snaptime?: number; vmstate?: number }
interface Detail { config: Record<string, unknown>; snapshots: Snap[]; ips: string[]; snapshots_error?: ErrE }

const pct = (a: number, b: number) => (b ? Math.min(100, (a / b) * 100) : 0)
function Bar({ value, label }: { value: number; label: string }) {
  return (
    <div className="min-w-0 flex-1">
      <div className="flex justify-between text-[11px] text-mute"><span className="truncate">{label}</span><span className="num">{value.toFixed(0)}%</span></div>
      <div className="mt-0.5 h-1.5 rounded-full bg-soft"><div className={cx('h-1.5 rounded-full', value > 85 ? 'bg-warn' : 'bg-acc')} style={{ width: `${Math.max(2, value)}%` }} /></div>
    </div>
  )
}

// PveSection: every connected Proxmox VE, on the 虚拟机 page.
export function PveSection() {
  const t = useT()
  const { user } = useAuth()
  const admin = can(user, 'settings.manage')
  const change = useChange()
  const d = useApi<{ items: Item[] }>('/api/v1/pve', [])
  const run = (kind: string, p: object) => change(kind, p, { onDone: () => void d.reload() })
  useEvents(['change.done'], (_, c) => { if (String(c?.kind ?? '').startsWith('pve.')) void d.reload() })
  const busy = d.data?.items.some((i) => i.guests?.some((g) => g.lock))
  useInterval(() => void d.reload(), busy ? 3000 : 15000)
  const [connect, setConnect] = useState<Cluster | null | undefined>(undefined) // null: a new one
  const [open, setOpen] = useState<{ cluster: string; vmid: number } | null>(null)
  if (!d.data) return null
  const items = d.data.items
  const cur = open && items.find((i) => i.cluster.id === open.cluster)
  const guest = cur && cur.guests?.find((g) => g.vmid === open!.vmid)
  return (
    <>
      {items.length === 0 && (
        <Box title="Proxmox VE">
          <Empty title={t('pve.none')} action={admin ? <Button onClick={() => setConnect(null)}>{t('pve.connect')}</Button> : undefined}>{t('pve.none_hint')}</Empty>
        </Box>
      )}
      {items.map((it) => (
        <Box key={it.cluster.id} title={<span className="flex items-center gap-2">Proxmox VE · {it.cluster.name}{it.version && <Pill>{it.version}</Pill>}</span>}
          count={it.guests?.length}
          right={admin && <Menu trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}>⋯</Button>} items={[
            { label: t('pve.reconnect'), onClick: () => setConnect(it.cluster) },
            { label: t('pve.add_another'), onClick: () => setConnect(null) },
            'sep' as const,
            { label: t('action.pve.delete'), danger: true, onClick: () => run('pve.delete', { id: it.cluster.id }) },
          ]} />}>
          {it.error && <div className="p-3"><Notice tone="bad"><div className="flex flex-wrap items-center gap-2"><span>{t(it.error.code, it.error.params as Record<string, string>)}</span>
            {admin && <Button size="sm" onClick={() => setConnect(it.cluster)}>{t('pve.reconnect')}</Button>}</div></Notice></div>}
          {(it.nodes?.length ?? 0) > 0 && <div className="grid gap-3 border-b border-line px-4 py-2.5 sm:grid-cols-2 lg:grid-cols-3">
            {it.nodes!.map((n) => (
              <div key={n.node} className="flex items-center gap-2" data-pve-node={n.node}>
                <Dot state={n.status === 'online' ? 'ok' : 'bad'} />
                <span className="w-14 truncate text-[12.5px] font-semibold">{n.node}</span>
                <Bar value={n.cpu * 100} label={`CPU · ${n.maxcpu}`} />
                <Bar value={pct(n.mem, n.maxmem)} label={`${t('pve.mem')} · ${bytes(n.maxmem)}`} />
              </div>
            ))}
          </div>}
          {it.guests?.filter((g) => !g.template).map((g) => <GuestRow key={g.vmid} c={it.cluster} g={g} onOpen={() => setOpen({ cluster: it.cluster.id, vmid: g.vmid })} />)}
        </Box>
      ))}
      {connect !== undefined && <ConnectDialog existing={connect} onClose={() => setConnect(undefined)} onDone={() => { setConnect(undefined); void d.reload() }} />}
      {cur && guest && <GuestDrawer c={cur.cluster} g={guest} onClose={() => setOpen(null)} />}
    </>
  )
}

const ref = (c: Cluster, g: Guest) => ({ cluster: c.id, node: g.node, type: g.type, vmid: g.vmid, name: g.name })

function GuestRow({ c, g, onOpen }: { c: Cluster; g: Guest; onOpen: () => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const on = g.status === 'running'
  return (
    <div data-pve-guest={g.vmid}>
    <Row onClick={onOpen}>
        <Dot state={g.lock ? 'busy' : on ? 'ok' : g.status === 'paused' ? 'warn' : 'off'} />
        <div className="min-w-0 flex-1">
          <div className="text-[13.5px] font-semibold">{g.name || g.vmid} <span className="num font-normal text-mute">· {g.vmid} · {g.node}</span></div>
          <div className="num truncate text-[12px] text-mute">
            {g.maxcpu} {t('vms.cpu_unit')} · {bytes(g.maxmem)}{on ? ` · CPU ${(g.cpu * 100).toFixed(0)}% · ${t('vms.mem_used', { v: bytes(g.mem) })} · ${duration(g.uptime, t)}` : ''}
            {g.tags ? ` · ${g.tags}` : ''}
          </div>
        </div>
        <Pill tone={g.type === 'qemu' ? 'acc' : undefined}>{g.type === 'qemu' ? t('vms.type_vm') : t('vms.type_ct')}</Pill>
        {can(user, 'vms.manage') && <span className="flex gap-1" onClick={(e) => e.stopPropagation()}>
          {on
            ? <Button size="sm" variant="ghost" onClick={() => run('pve.power', { ...ref(c, g), action: 'shutdown' })}>{t('pve.shutdown')}</Button>
            : <Button size="sm" onClick={() => run('pve.power', { ...ref(c, g), action: g.status === 'paused' ? 'resume' : 'start' })}>{t('pve.start')}</Button>}
        </span>}
    </Row>
    </div>
  )
}

function GuestDrawer({ c, g, onClose }: { c: Cluster; g: Guest; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const manage = can(user, 'vms.manage')
  const d = useApi<Detail>(`/api/v1/pve/${c.id}/guests/${g.node}/${g.type}/${g.vmid}`, [c.id, g.vmid, g.status])
  useEvents(['change.done'], (_, ch) => { if (String(ch?.kind ?? '').startsWith('pve.')) void d.reload() })
  const [showConsole, setConsole] = useState(false)
  const [site, setSite] = useState<string | null>(null)
  const stamp = new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '').replace(/^(\d{8})(\d{4})$/, '$1-$2')
  const [snap, setSnap] = useState('berth-' + stamp)
  const [ram, setRam] = useState(false)
  const on = g.status === 'running'
  const r = ref(c, g)
  const kv = (k: string, v: React.ReactNode) => <div className="flex gap-3 py-1 text-[13px]"><span className="w-24 shrink-0 text-mute">{k}</span><span className="num min-w-0 flex-1">{v}</span></div>
  const cfg = d.data?.config ?? {}
  const ips = d.data?.ips ?? []
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={<span className="flex items-center gap-2"><Dot state={on ? 'ok' : 'off'} />{g.name || g.vmid}<span className="text-[12px] font-normal text-mute">· {c.name}</span></span>}>
      <div className="space-y-4 p-4">
        {manage && <div className="flex flex-wrap gap-2">
          {on ? <>
            <Button size="sm" variant="primary" onClick={() => setConsole(true)}>{t('pve.console')}</Button>
            <Button size="sm" onClick={() => run('pve.power', { ...r, action: 'shutdown' })}>{t('pve.shutdown')}</Button>
            <Button size="sm" onClick={() => run('pve.power', { ...r, action: 'reboot' })}>{t('pve.reboot')}</Button>
            <Button size="sm" variant="ghost" onClick={() => run('pve.power', { ...r, action: 'stop' })}>{t('pve.stop')}</Button>
          </> : <Button size="sm" variant="primary" onClick={() => run('pve.power', { ...r, action: g.status === 'paused' ? 'resume' : 'start' })}>{t('pve.start')}</Button>}
        </div>}
        <div>
          {kv('VMID', `${g.vmid} · ${g.node}`)}
          {kv(t('pve.kind'), g.type === 'qemu' ? t('vms.type_vm') : t('vms.type_ct'))}
          {kv(t('pve.size'), `${g.maxcpu} ${t('vms.cpu_unit')} · ${bytes(g.maxmem)} · ${t('pve.disk')} ${bytes(g.maxdisk)}`)}
          {cfg.ostype ? kv(t('pve.os'), String(cfg.ostype)) : null}
          {on && kv(t('pve.uptime'), duration(g.uptime, t))}
          {kv(t('pve.address'), d.data ? (ips.length ? ips.join(' · ') : <span className="text-mute">{on ? (g.type === 'qemu' ? t('pve.no_ip_agent') : t('pve.no_ip')) : t('pve.no_ip_off')}</span>) : '…')}
        </div>
        {ips.length > 0 && can(user, 'sites.manage') && <Box title={t('pve.site')}>
          <div className="px-4 py-2 text-[12.5px]">
            <p className="text-mute">{t('pve.site_hint')}</p>
            <div className="mt-2 flex flex-wrap gap-1.5">{[80, 443, 3000, 8080].map((p) => <Button key={p} size="sm" onClick={() => setSite(`${ips[0]}:${p}`)}>{t('pve.make_site', { port: p })}</Button>)}</div>
          </div>
        </Box>}
        <Box title={t('pve.snapshots')} count={d.data?.snapshots.length}>
          <div className="px-4 py-2 text-[12.5px]">
            {d.data?.snapshots_error && <Notice tone="bad">{t(d.data.snapshots_error.code, d.data.snapshots_error.params as Record<string, string>)}</Notice>}
            {manage && <div className="flex flex-wrap items-center gap-2 py-1">
              <Input className="num max-w-[14rem]" value={snap} onChange={(e) => setSnap(e.target.value)} aria-label={t('pve.snapshot_name')} />
              {g.type === 'qemu' && on && <label className="flex items-center gap-1 text-mute"><input type="checkbox" checked={ram} onChange={(e) => setRam(e.target.checked)} />{t('pve.with_ram')}</label>}
              <Button size="sm" onClick={() => run('pve.snapshot', { ...r, snapshot: snap, vmstate: ram })}>{t('action.pve.snapshot')}</Button>
            </div>}
            {(d.data?.snapshots ?? []).map((s) => (
              <div key={s.name} className="flex items-center gap-2 border-t border-line py-1.5 first:border-0" data-snap={s.name}>
                <div className="min-w-0 flex-1"><span className="num font-semibold">{s.name}</span>
                  <span className="text-mute">{s.snaptime ? ' · ' + datetime(s.snaptime) : ''}{s.description ? ' · ' + s.description : ''}{s.vmstate ? ' · ' + t('pve.with_ram') : ''}</span></div>
                {manage && <><Button size="sm" variant="ghost" onClick={() => run('pve.rollback', { ...r, snapshot: s.name })}>{t('action.pve.rollback')}</Button>
                  <Button size="sm" variant="ghost" onClick={() => run('pve.snapshot_delete', { ...r, snapshot: s.name })}>{t('common.delete')}</Button></>}
              </div>
            ))}
            {d.data && (d.data.snapshots ?? []).length === 0 && <p className="py-1 text-mute">{t('pve.no_snapshots')}</p>}
          </div>
        </Box>
      </div>
      {showConsole && <ConsoleDialog c={c} g={g} onClose={() => setConsole(false)} />}
      {site && <NewSiteDialog preset={{ type: 'address', upstreams: [site] }} onClose={() => setSite(null)} onDone={() => setSite(null)} />}
    </Drawer>
  )
}

// ConsoleDialog: noVNC, through the panel (the browser needs no PVE login and no trust in its certificate)
function ConsoleDialog({ c, g, onClose }: { c: Cluster; g: Guest; onClose: () => void }) {
  const t = useT()
  const box = useRef<HTMLDivElement>(null)
  const rfb = useRef<RFB | null>(null)
  const [state, setState] = useState<'connecting' | 'connected' | 'closed'>('connecting')
  const [err, setErr] = useState('')
  useEffect(() => {
    let gone = false
    post<{ key: string; password: string }>(`/api/v1/pve/${c.id}/guests/${g.node}/${g.type}/${g.vmid}/console`, {}).then((tk) => {
      if (gone || !box.current) return
      const url = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/pve/console/${tk.key}`
      const r = new RFB(box.current, url, { credentials: { password: tk.password }, wsProtocols: ['binary'] })
      r.scaleViewport = true
      r.background = '#000'
      r.addEventListener('connect', () => setState('connected'))
      r.addEventListener('disconnect', () => setState('closed'))
      r.addEventListener('credentialsrequired', () => r.sendCredentials({ password: tk.password }))
      rfb.current = r
    }).catch((e) => setErr(errText(e)))
    return () => { gone = true; rfb.current?.disconnect() }
  }, [c.id, g.node, g.type, g.vmid])
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={`${t('pve.console')} · ${g.name || g.vmid}`}
      footer={<><span className="text-[12px] text-mute" data-console-state={state}>{t('pve.console_' + state)}</span>
        <Button className="ml-auto" size="sm" disabled={state !== 'connected'} onClick={() => rfb.current?.sendCtrlAltDel()}>Ctrl+Alt+Del</Button>
        <Button size="sm" onClick={onClose}>{t('common.close')}</Button></>}>
      {err && <div className="p-3"><Notice tone="bad">{err}</Notice></div>}
      <div ref={box} className="h-[60vh] w-full bg-black" data-pve-console />
    </Dialog>
  )
}

const TOKEN_HELP = `pveum role add BerthOperator -privs "VM.Audit VM.PowerMgmt VM.Snapshot VM.Snapshot.Rollback VM.Console VM.GuestAgent.Audit Sys.Audit Datastore.Audit"
pveum user add berth@pve
pveum aclmod / -user berth@pve -role BerthOperator
pveum user token add berth@pve panel --privsep 0`

function ConnectDialog({ existing, onClose, onDone }: { existing: Cluster | null; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const [url, setUrl] = useState(existing?.url ?? '')
  const [probe, setProbe] = useState<{ url: string; fingerprint: string; fingerprint_text: string; subject: string; trusted: boolean } | null>(null)
  const [probeErr, setProbeErr] = useState('')
  const [checking, setChecking] = useState(false)
  const [same, setSame] = useState(false)
  const [tokenId, setTokenId] = useState(existing?.token_id ?? 'berth@pve!panel')
  const [secret, setSecret] = useState('')
  const check = async () => {
    setProbeErr(''); setProbe(null); setSame(false); setChecking(true)
    try { setProbe(await post('/api/v1/pve/probe', { url })) } catch (e) { setProbeErr(errText(e)) } finally { setChecking(false) }
  }
  const ok = probe && (probe.trusted || same) && tokenId.includes('!') && (secret.trim() || existing)
  const save = () => run('pve.set', { id: existing?.id, name: existing?.name, url: probe!.url, token_id: tokenId.trim(), secret: secret.trim(), fingerprint: probe!.trusted ? '' : probe!.fingerprint },
    { onDone })
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={existing ? t('pve.reconnect') + ' · ' + existing.name : t('pve.connect')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!ok} onClick={save}>{t('pve.connect_save')}</Button></>}>
      <div className="space-y-4 px-4 py-3 text-[13px]">
        <section>
          <div className="mb-1.5 font-semibold">1 · {t('pve.step_address')}</div>
          <div className="flex gap-2">
            <Input className="num flex-1" value={url} onChange={(e) => { setUrl(e.target.value); setProbe(null) }} placeholder="https://pve.lan:8006" aria-label={t('pve.address_label')}
              onKeyDown={(e) => { if (e.key === 'Enter' && url.trim()) void check() }} />
            <Button disabled={!url.trim() || checking} onClick={() => void check()}>{checking ? t('common.loading') : t('pve.check')}</Button>
          </div>
          {probeErr && <p className="mt-1 text-[12px] text-bad">{probeErr}</p>}
          {probe && (probe.trusted
            ? <p className="mt-1.5 text-[12px] text-ok">{t('pve.cert_trusted', { subject: probe.subject })}</p>
            : <div className="mt-2 rounded-[var(--radius-box)] border border-warn/50 p-2.5">
              <p className="text-[12px]">{t('pve.cert_selfsigned')}</p>
              <code className="num mt-1 block break-all rounded bg-soft px-1.5 py-1 text-[11.5px]" data-fingerprint>{probe.fingerprint_text}</code>
              <p className="mt-1 text-[12px] text-mute">{t('pve.cert_where')}</p>
              <label className="mt-1.5 flex items-center gap-1.5"><input type="checkbox" checked={same} onChange={(e) => setSame(e.target.checked)} />{t('pve.cert_same')}</label>
            </div>)}
        </section>
        <section className={cx(!probe && 'opacity-50')}>
          <div className="mb-1.5 font-semibold">2 · {t('pve.step_token')}</div>
          <details className="mb-2 text-[12px]">
            <summary className="cursor-pointer text-acc">{t('pve.token_how')}</summary>
            <p className="mt-1 text-mute">{t('pve.token_how_text')}</p>
            <pre className="num mt-1 overflow-x-auto rounded bg-soft p-2 text-[11.5px] select-all">{TOKEN_HELP}</pre>
            <p className="mt-1 text-mute">{t('pve.token_pve8')}</p>
          </details>
          <div className="grid gap-2 sm:grid-cols-2">
            <Field label={t('pve.token_id')}><Input className="num" value={tokenId} onChange={(e) => setTokenId(e.target.value)} disabled={!probe} /></Field>
            <Field label={t('pve.token_secret')}><Input className="num" type="password" autoComplete="off" value={secret} onChange={(e) => setSecret(e.target.value)} disabled={!probe}
              placeholder={existing ? t('pve.secret_keep') : 'xxxxxxxx-xxxx-…'} /></Field>
          </div>
          <p className="mt-1.5 text-[12px] text-mute">{t('pve.token_privacy')}</p>
        </section>
      </div>
    </Dialog>
  )
}
