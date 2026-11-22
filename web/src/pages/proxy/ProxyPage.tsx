import qrcode from 'qrcode-generator'
import { Check, Copy, QrCode } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Box, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Field'
import { TabBar } from '@/components/ui/Tabs'
import { bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Params { target?: string; server_names?: string[]; public_key?: string; short_ids?: string[]; fingerprint?: string; cert?: string; path?: string; xhttp_mode?: string; ss_method?: string }
interface Node { id: number; name: string; enabled: boolean; protocol: string; transport: string; security: string; port: number; domain: string; params: Params; note: string; pin?: string }
interface PUser { id: number; name: string; nodes: number[]; limit_bytes: number; reset_day: number; expire_at: number; allow_lan: boolean; enabled: boolean; stopped: string; used_up: number; used_down: number; note: string; sub_url: string; links: Record<string, string> }
interface Policy { block_bt: boolean; block_ports: number[]; block_hosts: string[] }
interface Data { installed: boolean; host: string; host_set: string; policy: Policy; nodes: Node[]; users: PUser[]; status?: { running: boolean; state: string; log?: string; version?: string }; firewall?: { firewalld: boolean; open_ports?: string[] } }

const qr = (s: string) => { const q = qrcode(0, 'M'); q.addData(s); q.make(); return q.createSvgTag({ cellSize: 4, margin: 2, scalable: true }) }
const protoName: Record<string, string> = { vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan', shadowsocks: 'Shadowsocks 2022', hysteria: 'Hysteria 2' }

// Proxy (代理): Xray on this server, nodes and the people who use them.
export default function ProxyPage() {
  const t = useT()
  const run = useChange()
  const d = useApi<Data>('/api/v1/proxy')
  useEvents(['change.done'], () => void d.reload())
  const [tab, setTab] = useState<'nodes' | 'users' | 'activity' | 'settings'>('nodes')
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : null
  const data = d.data
  if (!data.installed) {
    return (
      <Box title={t('px.intro_title')}>
        <div className="space-y-2 px-4 py-3 text-[13px]">
          <p>{t('px.intro')}</p>
          <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-mute">
            <li>{t('px.intro_protocols')}</li>
            <li>{t('px.intro_users')}</li>
            <li>{t('px.intro_sub')}</li>
          </ul>
          <p className="text-[12px] text-mute">{t('px.intro_law')}</p>
          <Button variant="primary" onClick={() => run('proxy.install', {}, { host: 'local' })}>{t('px.install')}</Button>
        </div>
      </Box>
    )
  }
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2 rounded-[var(--radius-box)] border border-line bg-card px-4 py-2.5 text-[13px]">
        <Pill tone={data.status?.running ? 'ok' : 'bad'}>{data.status?.running ? t('px.running') : t('px.stopped')}</Pill>
        <span className="text-mute">{data.status?.version || data.status?.state}</span>
        <span className="ml-auto text-[12px] text-mute">{t('px.link_host', { host: data.host })}</span>
      </div>
      {data.status && !data.status.running && data.status.log && <pre className="num max-h-40 overflow-auto whitespace-pre-wrap rounded-[var(--radius-box)] border border-bad bg-soft px-3 py-2 text-[11.5px]">{data.status.log}</pre>}
      <TabBar label={t('nav.proxy')} value={tab} onChange={setTab} items={[{ value: 'nodes', label: t('px.nodes') }, { value: 'users', label: t('px.users') }, { value: 'activity', label: t('px.activity') }, { value: 'settings', label: t('px.settings') }]} />
      {tab === 'nodes' && <Nodes data={data} />}
      {tab === 'users' && <Users data={data} />}
      {tab === 'activity' && <Activity data={data} />}
      {tab === 'settings' && <Settings data={data} />}
    </div>
  )
}

// ---- nodes ----

const PRESETS: { key: string; node: Partial<Node> }[] = [
  { key: 'reality', node: { protocol: 'vless', transport: 'raw', security: 'reality' } },
  { key: 'reality_xhttp', node: { protocol: 'vless', transport: 'xhttp', security: 'reality' } },
  { key: 'trojan_tls', node: { protocol: 'trojan', transport: 'raw', security: 'tls' } },
  { key: 'vless_ws', node: { protocol: 'vless', transport: 'ws', security: 'tls' } },
  { key: 'ss', node: { protocol: 'shadowsocks', transport: 'raw', security: 'none' } },
  { key: 'hy2', node: { protocol: 'hysteria', transport: 'hysteria', security: 'tls' } },
]

function portOpen(fw: Data['firewall'], n: Node) {
  if (!fw?.firewalld) return true
  const need = n.protocol === 'hysteria' ? ['udp'] : n.protocol === 'shadowsocks' ? ['tcp', 'udp'] : ['tcp']
  return need.every((p) => fw.open_ports?.includes(`${n.port}/${p}`))
}

function Nodes({ data }: { data: Data }) {
  const t = useT()
  const run = useChange()
  const wharf = useApi<{ paired: boolean }>('/api/v1/wharf')
  const [edit, setEdit] = useState<Partial<Node> | null>(null)
  return (
    <>
      <Box title={t('px.nodes')} count={data.nodes.length} right={<Button size="sm" variant="primary" onClick={() => setEdit({})}>{t('px.new_node')}</Button>}>
        {data.nodes.length === 0 && <Empty title={t('px.no_nodes')}>{t('px.no_nodes_hint')}</Empty>}
        {data.nodes.map((n) => (
          <Row key={n.id}>
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{n.name} {!n.enabled && <Pill>{t('px.off')}</Pill>}</div>
              <div className="num truncate text-[12px] text-mute">{protoName[n.protocol]} · {n.transport.toUpperCase()} · {n.security.toUpperCase()} · {t('px.port')} {n.port}{n.domain ? ' · ' + n.domain : ''}</div>
              {!portOpen(data.firewall, n) && <div className="mt-0.5 flex flex-wrap items-center gap-2 text-[12px] text-warn">{t('px.fw_closed', { port: n.port })}
                {(n.protocol === 'hysteria' ? ['udp'] : n.protocol === 'shadowsocks' ? ['tcp', 'udp'] : ['tcp']).map((p) => (
                  <Button key={p} size="sm" onClick={() => run('firewall.rule.set', { action: 'allow', proto: p, ports: String(n.port), source: 'any', comment: 'Berth proxy: ' + n.name, enabled: true }, { host: 'local' })}>{t('px.fw_open', { proto: p.toUpperCase() })}</Button>
                ))}</div>}
            </div>
            <span className="flex gap-1">
              {wharf.data?.paired && <Button size="sm" variant="ghost" title={t('px.map_wharf_d')} onClick={() => {
                const ps = n.protocol === 'hysteria' ? ['udp'] : n.protocol === 'shadowsocks' ? ['tcp', 'udp'] : ['tcp']
                const next = (i: number) => { if (i < ps.length) run('wharf.forward', { wan_port: String(n.port), local_port: String(n.port), proto: ps[i], note: 'Berth proxy: ' + n.name }, { host: 'local', onDone: () => next(i + 1) }) }
                next(0)
              }}>{t('px.map_wharf')}</Button>}
              <Button size="sm" variant="ghost" onClick={() => run('proxy.node.set', { ...n, enabled: !n.enabled }, { host: 'local' })}>{n.enabled ? t('px.disable') : t('px.enable')}</Button>
              <Button size="sm" variant="ghost" onClick={() => setEdit(n)}>{t('common.edit')}</Button>
              <Button size="sm" variant="ghost" onClick={() => run('proxy.node.delete', { id: n.id }, { host: 'local' })}>{t('action.proxy.node.delete')}</Button>
            </span>
          </Row>
        ))}
      </Box>
      <p className="px-1 text-[12px] text-mute">{t('px.cloud_sg')}</p>
      {edit && <NodeDialog init={edit} used={data.nodes.filter((x) => x.id !== edit.id).map((x) => x.port)} onClose={() => setEdit(null)} />}
    </>
  )
}

function NodeDialog({ init, used, onClose }: { init: Partial<Node>; used: number[]; onClose: () => void }) {
  const fw = useApi<{ state: { firewalld: boolean; open_ports?: string[] }; listening: { proto: string; port: number; process?: string; public: boolean }[] }>('/api/v1/hosts/local/firewall')
  const wharf = useApi<{ paired: boolean; link?: { name: string } }>('/api/v1/wharf')
  const [openFw, setOpenFw] = useState(true)
  const [mapWharf, setMapWharf] = useState(true)
  const t = useT()
  const run = useChange()
  const free = () => { let p = 0; do { p = 20000 + Math.floor(Math.random() * 40000) } while (used.includes(p) || p === 20085); return p }
  const [n, setN] = useState<Partial<Node>>(() => init.id ? init : { name: t('px.preset_reality'), protocol: 'vless', transport: 'raw', security: 'reality', port: free(), enabled: true, params: {} })
  const p = n.params ?? {}
  const setP = (x: Partial<Params>) => setN({ ...n, params: { ...p, ...x } })
  const reality = n.security === 'reality'
  const tls = n.security === 'tls'
  const fixed = n.protocol === 'shadowsocks' || n.protocol === 'hysteria'
  const protos = n.protocol === 'hysteria' ? ['udp'] : n.protocol === 'shadowsocks' ? ['tcp', 'udp'] : ['tcp']
  // the port: taken by another program here? open in the firewall? (the node's own xray does not count)
  const taken = (fw.data?.listening ?? []).find((l) => l.port === n.port && protos.includes(l.proto) && l.process !== 'xray')
  const fwClosed = !!fw.data?.state.firewalld && protos.some((p) => !fw.data!.state.open_ports?.includes(`${n.port}/${p}`))
  const paired = !!wharf.data?.paired
  // save the node, then (each with its own preview) open the firewall and map the router port
  const save = () => run('proxy.node.set', n, { host: 'local', onDone: () => {
    const steps: [string, unknown][] = []
    if (fwClosed && openFw) for (const p of protos) steps.push(['firewall.rule.set', { action: 'allow', proto: p, ports: String(n.port), source: 'any', comment: 'Berth proxy: ' + n.name, enabled: true }])
    if (paired && mapWharf) for (const p of protos) steps.push(['wharf.forward', { wan_port: String(n.port), local_port: String(n.port), proto: p, note: 'Berth proxy: ' + n.name }])
    const next = (i: number) => { if (i >= steps.length) { onClose(); return } run(steps[i][0], steps[i][1], { host: 'local', onDone: () => next(i + 1) }) }
    next(0)
  } })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={n.id ? t('px.edit_node') : t('px.new_node')} wide
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!n.name || !n.port || (tls && !n.domain)} onClick={save}>{t('action.proxy.node.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {!n.id && (
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
            {PRESETS.map((x) => {
              const on = x.node.protocol === n.protocol && x.node.transport === n.transport && x.node.security === n.security
              return (
                <button key={x.key} type="button" aria-pressed={on} onClick={() => setN({ ...n, ...x.node, name: t('px.preset_' + x.key), params: {} })} className={cx('rounded-[var(--radius-box)] border p-2 text-left', on ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
                  <div className="text-[12.5px] font-semibold">{t('px.preset_' + x.key)}</div>
                  <div className="text-[11.5px] text-mute">{t('px.preset_' + x.key + '_d')}</div>
                </button>
              )
            })}
          </div>
        )}
        <div className="grid grid-cols-1 gap-x-3 sm:grid-cols-2">
          <Field label={t('px.name')}><Input value={n.name ?? ''} onChange={(e) => setN({ ...n, name: e.target.value })} /></Field>
          <Field label={t('px.port')} hint={t('px.port_hint')}><Input className="num" inputMode="numeric" value={n.port ?? ''} onChange={(e) => setN({ ...n, port: +e.target.value.replace(/\D/g, '') })} /></Field>
          <Field label={t('px.protocol')}>
            <Select value={n.protocol} onChange={(e) => setN({ ...n, protocol: e.target.value, ...(e.target.value === 'hysteria' ? { transport: 'hysteria', security: 'tls' } : e.target.value === 'shadowsocks' ? { transport: 'raw', security: 'none' } : n.transport === 'hysteria' ? { transport: 'raw' } : {}) })}>
              {Object.entries(protoName).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
            </Select>
          </Field>
          {!fixed && <>
            <Field label={t('px.transport')}>
              <Select value={n.transport} onChange={(e) => setN({ ...n, transport: e.target.value })}>
                {['raw', 'xhttp', 'ws', 'grpc', 'httpupgrade'].map((x) => <option key={x} value={x}>{x === 'raw' ? 'RAW (TCP)' : x.toUpperCase()}</option>)}
              </Select>
            </Field>
            <Field label={t('px.security')}>
              <Select value={n.security} onChange={(e) => setN({ ...n, security: e.target.value })}>
                <option value="reality">REALITY</option><option value="tls">TLS</option><option value="none">{t('px.sec_none')}</option>
              </Select>
            </Field>
          </>}
          {(tls || n.protocol === 'hysteria') && <>
            <Field label={t('px.domain')} hint={t('px.domain_hint')}><Input className="num" value={n.domain ?? ''} onChange={(e) => setN({ ...n, domain: e.target.value.trim() })} placeholder="proxy.example.com" /></Field>
            <Field label={t('px.cert')}>
              <Select value={p.cert || 'self'} onChange={(e) => setP({ cert: e.target.value })}><option value="self">{t('px.cert_self')}</option><option value="site">{t('px.cert_site')}</option></Select>
            </Field>
          </>}
          {reality && <>
            <Field label={t('px.target')} hint={t('px.target_hint')}><Input className="num" value={p.target ?? ''} onChange={(e) => setP({ target: e.target.value })} placeholder="www.cloudflare.com:443" /></Field>
            <Field label="SNI" hint={t('px.sni_hint')}><Input className="num" value={(p.server_names ?? []).join(', ')} onChange={(e) => setP({ server_names: e.target.value.split(/[,\s]+/).filter(Boolean) })} /></Field>
          </>}
          {n.protocol === 'shadowsocks' && <Field label={t('px.ss_method')}><Select value={p.ss_method || '2022-blake3-aes-128-gcm'} onChange={(e) => setP({ ss_method: e.target.value })}>{['2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305'].map((m) => <option key={m}>{m}</option>)}</Select></Field>}
          {['ws', 'xhttp', 'grpc', 'httpupgrade'].includes(n.transport ?? '') && <Field label={n.transport === 'grpc' ? 'serviceName' : t('px.path')} hint={t('px.path_hint')}><Input className="num" value={p.path ?? ''} onChange={(e) => setP({ path: e.target.value })} /></Field>}
        </div>
        {['vmess', 'shadowsocks'].includes(n.protocol ?? '') || n.transport === 'httpupgrade' ? <Notice tone="warn">{t('px.deprecated')}</Notice> : null}
        <div className="space-y-1.5 rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12.5px]">
          <div className="font-semibold">{t('px.port_title', { port: n.port ?? '', proto: protos.join(' + ').toUpperCase() })}</div>
          {taken ? <p className="text-bad">{t('px.port_taken', { port: n.port ?? '', proc: taken.process ?? '?' })}</p> : <p className="text-ok">{t('px.port_free', { port: n.port ?? '' })}</p>}
          {fwClosed ? <Switch checked={openFw} onChange={setOpenFw} label={t('px.then_fw', { port: n.port ?? '' })} /> : fw.data?.state.firewalld && <p className="text-mute">{t('px.fw_ok')}</p>}
          {paired ? <Switch checked={mapWharf} onChange={setMapWharf} label={t('px.then_wharf', { name: wharf.data?.link?.name ?? 'Wharf', port: n.port ?? '' })} /> : <p className="text-mute">{t('px.no_wharf')}</p>}
          {(fwClosed && openFw) || (paired && mapWharf) ? <p className="text-mute">{t('px.then_confirm')}</p> : null}
        </div>
        {n.security === 'none' && n.protocol !== 'shadowsocks' && <Notice tone="warn">{t('px.plain_warn')}</Notice>}
        <Field label={t('px.note')}><Textarea className="h-16" value={n.note ?? ''} onChange={(e) => setN({ ...n, note: e.target.value })} /></Field>
      </div>
    </Dialog>
  )
}

// ---- users ----

function Users({ data }: { data: Data }) {
  const t = useT()
  const run = useChange()
  const [edit, setEdit] = useState<Partial<PUser> | null>(null)
  const [links, setLinks] = useState<PUser | null>(null)
  const now = Date.now() / 1000
  const state = (u: PUser) => !u.enabled ? 'off' : u.stopped === 'quota' ? 'quota' : u.stopped === 'expired' || (u.expire_at && u.expire_at <= now) ? 'expired' : 'ok'
  return (
    <>
      <Box title={t('px.users')} count={data.users.length} right={<Button size="sm" variant="primary" disabled={data.nodes.length === 0} onClick={() => setEdit({ nodes: data.nodes.map((n) => n.id), enabled: true })}>{t('px.new_user')}</Button>}>
        {data.users.length === 0 && <Empty title={t('px.no_users')}>{t(data.nodes.length ? 'px.no_users_hint' : 'px.nodes_first')}</Empty>}
        {data.users.map((u) => {
          const used = u.used_up + u.used_down
          return (
            <Row key={u.id}>
              <div className="min-w-0 flex-1">
                <div className="text-[13.5px] font-semibold">{u.name} <Pill tone={state(u) === 'ok' ? 'ok' : 'warn'}>{t('px.u_' + state(u))}</Pill></div>
                <div className="text-[12px] text-mute">{t('px.used', { used: bytes(used), limit: u.limit_bytes ? bytes(u.limit_bytes) : t('px.unlimited') })}{u.reset_day ? ' · ' + t('px.resets', { d: u.reset_day }) : ''}{u.expire_at ? ' · ' + t('px.expires', { at: new Date(u.expire_at * 1000).toLocaleDateString() }) : ''} · {t('px.n_nodes', { n: u.nodes.length })}</div>
                {u.limit_bytes > 0 && <div className="mt-1 h-1 w-48 overflow-hidden rounded-full bg-soft"><div className={cx('h-full rounded-full', used >= u.limit_bytes ? 'bg-bad' : used > u.limit_bytes * 0.8 ? 'bg-warn' : 'bg-acc')} style={{ width: Math.min(100, (used / u.limit_bytes) * 100) + '%' }} /></div>}
              </div>
              <span className="flex gap-1">
                <Button size="sm" onClick={() => setLinks(u)}><QrCode className="size-3.5" />{t('px.links')}</Button>
                <Button size="sm" variant="ghost" onClick={() => setEdit(u)}>{t('common.edit')}</Button>
                <Button size="sm" variant="ghost" onClick={() => run('proxy.user.delete', { id: u.id }, { host: 'local' })}>{t('action.proxy.user.delete')}</Button>
              </span>
            </Row>
          )
        })}
      </Box>
      {edit && <UserDialog init={edit} nodes={data.nodes} onClose={() => setEdit(null)} />}
      {links && <LinksDialog u={links} nodes={data.nodes} onClose={() => setLinks(null)} />}
    </>
  )
}

function UserDialog({ init, nodes, onClose }: { init: Partial<PUser>; nodes: Node[]; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const [f, setF] = useState({
    name: init.name ?? '', nodes: init.nodes ?? [], limit_gb: init.limit_bytes ? String(+(init.limit_bytes / 2 ** 30).toFixed(2)) : '',
    reset_day: String(init.reset_day ?? 1), expire: init.expire_at ? new Date(init.expire_at * 1000).toISOString().slice(0, 10) : '',
    allow_lan: init.allow_lan ?? false, enabled: init.enabled ?? true, note: init.note ?? '', new_secret: false, reset_usage: false,
  })
  const save = () => run('proxy.user.set', {
    id: init.id, name: f.name, nodes: f.nodes, limit_gb: +f.limit_gb || 0, reset_day: +f.reset_day, allow_lan: f.allow_lan, enabled: f.enabled, note: f.note,
    expire_at: f.expire ? Math.floor(new Date(f.expire + 'T23:59:59').getTime() / 1000) : 0, new_secret: f.new_secret, reset_usage: f.reset_usage,
  }, { host: 'local', onDone: onClose })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={init.id ? t('px.edit_user') : t('px.new_user')} wide
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!f.name} onClick={save}>{t('action.proxy.user.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <div className="grid grid-cols-1 gap-x-3 sm:grid-cols-2">
          <Field label={t('px.name')}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder={t('px.user_ph')} /></Field>
          <Field label={t('px.limit')} hint={t('px.limit_hint')}><Input className="num" inputMode="decimal" value={f.limit_gb} onChange={(e) => setF({ ...f, limit_gb: e.target.value.replace(/[^\d.]/g, '') })} placeholder={t('px.unlimited')} /></Field>
          <Field label={t('px.reset_day')}><Select value={f.reset_day} onChange={(e) => setF({ ...f, reset_day: e.target.value })}><option value="0">{t('px.never_reset')}</option>{Array.from({ length: 28 }, (_, i) => <option key={i + 1} value={i + 1}>{t('px.day_n', { n: i + 1 })}</option>)}</Select></Field>
          <Field label={t('px.expire')} hint={t('px.expire_hint')}><Input type="date" value={f.expire} onChange={(e) => setF({ ...f, expire: e.target.value })} /></Field>
        </div>
        <Field label={t('px.user_nodes')}>
          <div className="flex flex-wrap gap-x-4 gap-y-1">{nodes.map((n) => (
            <label key={n.id} className="flex items-center gap-1.5"><input type="checkbox" checked={f.nodes.includes(n.id)} onChange={(e) => setF({ ...f, nodes: e.target.checked ? [...f.nodes, n.id] : f.nodes.filter((x) => x !== n.id) })} />{n.name}</label>
          ))}</div>
        </Field>
        <div className="space-y-2 px-0.5">
          <Switch checked={f.enabled} onChange={(x) => setF({ ...f, enabled: x })} label={t('px.enabled')} />
          <Switch checked={f.allow_lan} onChange={(x) => setF({ ...f, allow_lan: x })} label={t('px.allow_lan')} />
          {init.id && <Switch checked={f.reset_usage} onChange={(x) => setF({ ...f, reset_usage: x })} label={t('px.reset_usage')} />}
          {init.id && <Switch checked={f.new_secret} onChange={(x) => setF({ ...f, new_secret: x })} label={t('px.new_secret')} />}
        </div>
        {f.allow_lan && <Notice tone="warn">{t('px.allow_lan_warn')}</Notice>}
        <Field label={t('px.note')}><Input value={f.note} onChange={(e) => setF({ ...f, note: e.target.value })} /></Field>
      </div>
    </Dialog>
  )
}

function LinksDialog({ u, nodes, onClose }: { u: PUser; nodes: Node[]; onClose: () => void }) {
  const t = useT()
  const [show, setShow] = useState(u.sub_url)
  const [copied, setCopied] = useState('')
  const copy = (s: string) => { void navigator.clipboard.writeText(s); setCopied(s) }
  const items = [{ name: t('px.subscription'), link: u.sub_url }, ...nodes.filter((n) => u.links[n.id]).map((n) => ({ name: n.name, link: u.links[n.id] }))]
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('px.links_of', { name: u.name })} wide footer={<Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button>}>
      <div className="grid gap-4 px-4 py-3 text-[13px] sm:grid-cols-[180px_1fr]">
        <div className="size-44 rounded border border-line bg-white p-1" dangerouslySetInnerHTML={{ __html: qr(show) }} />
        <div className="min-w-0 space-y-2">
          <p className="text-[12.5px] text-mute">{t('px.links_hint')}</p>
          {items.map((x) => (
            <div key={x.link} className={cx('rounded-[var(--radius-box)] border px-2.5 py-1.5', show === x.link ? 'border-acc' : 'border-line')}>
              <div className="flex items-center gap-2">
                <button type="button" className="min-w-0 flex-1 text-left font-semibold" onClick={() => setShow(x.link)}>{x.name}</button>
                <Button size="sm" variant="ghost" onClick={() => copy(x.link)}>{copied === x.link ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}</Button>
              </div>
              <div className="num truncate text-[11px] text-mute">{x.link}</div>
            </div>
          ))}
          {Object.keys(u.links).length === 0 && <Notice tone="warn">{t('px.no_links')}</Notice>}
        </div>
      </div>
    </Dialog>
  )
}

// ---- settings ----

function Settings({ data }: { data: Data }) {
  const t = useT()
  const run = useChange()
  const [host, setHost] = useState(data.host_set)
  const [bt, setBt] = useState(data.policy.block_bt)
  const [ports, setPorts] = useState((data.policy.block_ports ?? []).join(', '))
  const [hosts, setHosts] = useState((data.policy.block_hosts ?? []).join('\n'))
  return (
    <>
      <Box title={t('px.settings')}>
        <Field label={t('px.host')} hint={t('px.host_hint', { host: data.host })}><Input className="num" value={host} onChange={(e) => setHost(e.target.value.trim())} placeholder={data.host} /></Field>
        <div className="px-4 py-2"><Switch checked={bt} onChange={setBt} label={t('px.block_bt')} /></div>
        <Field label={t('px.block_ports')} hint={t('px.block_ports_hint')}><Input className="num" value={ports} onChange={(e) => setPorts(e.target.value)} placeholder="25, 465" /></Field>
        <Field label={t('px.block_hosts')} hint={t('px.block_hosts_hint')}><Textarea className="num h-20" value={hosts} onChange={(e) => setHosts(e.target.value)} /></Field>
        <div className="px-4 pb-3"><Button variant="primary" onClick={() => run('proxy.settings', { host, policy: { block_bt: bt, block_ports: ports.split(/[,\s]+/).map(Number).filter((x) => x > 0 && x < 65536), block_hosts: hosts.split(/\s+/).filter(Boolean) } }, { host: 'local' })}>{t('action.proxy.settings')}</Button></div>
      </Box>
      <XrayVersion running={data.status?.version} />
      <Box title={t('px.uninstall_title')}>
        <div className="flex items-center gap-3 px-4 py-3 text-[13px]"><span className="flex-1 text-mute">{t('px.uninstall_hint')}</span><Button onClick={() => run('proxy.uninstall', {}, { host: 'local' })}>{t('action.proxy.uninstall')}</Button></div>
      </Box>
    </>
  )
}

interface Rel { tag: string; prerelease: boolean; date: string }

// XrayVersion: which Xray runs; switch to another release (and back) like any change.
function XrayVersion({ running }: { running?: string }) {
  const t = useT()
  const run = useChange()
  const v = useApi<{ items: Rel[]; current: string }>('/api/v1/proxy/versions')
  const [pick, setPick] = useState('')
  const cur = v.data?.current ?? 'latest'
  const sel = pick || cur
  const newest = v.data?.items.find((x) => !x.prerelease)
  return (
    <Box title={t('px.version')}>
      <div className="space-y-2 px-4 py-3 text-[13px]">
        <p>{t('px.version_now', { v: running || '…', pinned: cur === 'latest' ? t('px.v_latest') : cur })}</p>
        {newest && cur !== 'latest' && cur !== newest.tag && <Notice>{t('px.v_newer', { v: newest.tag, date: newest.date })}</Notice>}
        <div className="flex flex-wrap items-center gap-2">
          <Select className="w-64" value={sel} onChange={(e) => setPick(e.target.value)} aria-label={t('px.version')}>
            <option value="latest">{t('px.v_latest')}</option>
            {(v.data?.items ?? []).map((x) => <option key={x.tag} value={x.tag}>{x.tag} · {x.date}{x.prerelease ? ' · ' + t('px.v_pre') : ''}</option>)}
          </Select>
          <Button variant="primary" disabled={sel === cur} onClick={() => run('proxy.version', { version: sel }, { host: 'local', onDone: () => { setPick(''); void v.reload() } })}>{t('px.v_switch')}</Button>
        </div>
        <p className="text-[12px] text-mute">{t('px.v_hint')}</p>
      </div>
    </Box>
  )
}

interface OnlineRow { user_id: number; user: string; nodes: number[]; ips: { ip: string; last_seen: number; country?: string }[] }
interface AccessRec { id: number; ts: number; last: number; user: string; node: string; ip: string; country: string; dest: string; port: number; net: string; status: string; count: number }

// Activity: who is online now and which sites people reached (from Xray's access log).
function Activity({ data }: { data: Data }) {
  const t = useT()
  const online = useApi<{ items: OnlineRow[]; at: number }>('/api/v1/proxy/online')
  const [user, setUser] = useState('')
  const [q, setQ] = useState('')
  const [blocked, setBlocked] = useState(false)
  const access = useApi<{ items: AccessRec[]; top: { k: string; n: number }[] }>(`/api/v1/proxy/access?user=${user}&q=${encodeURIComponent(q)}&status=${blocked ? 'blocked' : ''}`, [user, q, blocked])
  useEffect(() => { const id = setInterval(() => { void online.reload(); void access.reload() }, 20000); return () => clearInterval(id) }, [user, q, blocked]) // eslint-disable-line react-hooks/exhaustive-deps
  const nodeName = (id: number) => data.nodes.find((n) => n.id === id)?.name ?? '#' + id
  const ago = (ts: number) => { const s = Math.max(0, Math.round(Date.now() / 1000 - ts)); return s < 60 ? t('px.s_ago', { n: s }) : s < 3600 ? t('px.m_ago', { n: Math.round(s / 60) }) : new Date(ts * 1000).toLocaleString() }
  const items = online.data?.items ?? []
  return (
    <>
      <Box title={t('px.online')} count={items.length}>
        {items.length === 0 && <Empty title={t('px.nobody')}>{t('px.nobody_hint')}</Empty>}
        {items.map((u) => (
          <Row key={u.user_id}>
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{u.user || '#' + u.user_id} <span className="font-normal text-mute">· {u.nodes.map(nodeName).join('、')}</span></div>
              <div className="num flex flex-wrap gap-x-3 text-[12px] text-mute">{u.ips.map((x) => <span key={x.ip}>{x.ip}{x.country ? ' · ' + x.country : ''} · {ago(x.last_seen)}</span>)}</div>
            </div>
            {u.ips.length > 1 && <Pill tone="warn">{t('px.n_ips', { n: u.ips.length })}</Pill>}
          </Row>
        ))}
      </Box>
      {(access.data?.top.length ?? 0) > 0 && <Box title={t('px.top_sites')}>
        <div className="grid gap-x-6 px-4 py-3 text-[12.5px] sm:grid-cols-2">
          {access.data!.top.map((x) => <div key={x.k} className="flex justify-between gap-2 border-b border-line py-1"><span className="num truncate">{x.k}</span><span className="num text-mute">{x.n}</span></div>)}
        </div>
      </Box>}
      <Box title={t('px.access')} count={access.data?.items.length}>
        <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2">
          <Select className="w-44" value={user} onChange={(e) => setUser(e.target.value)} aria-label={t('px.users')}>
            <option value="">{t('px.all_users')}</option>
            {data.users.map((u) => <option key={u.id} value={u.id}>{u.name}</option>)}
          </Select>
          <Input className="w-56" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('px.search_dest')} aria-label={t('px.search_dest')} />
          <Switch checked={blocked} onChange={setBlocked} label={t('px.only_blocked')} />
        </div>
        {(access.data?.items ?? []).length === 0 && <p className="px-4 py-3 text-[12.5px] text-mute">{t('px.no_access')}</p>}
        {(access.data?.items ?? []).map((x) => (
          <div key={x.id} className="grid grid-cols-[1fr_auto] gap-x-3 border-b border-line px-4 py-1.5 text-[12.5px] last:border-b-0 sm:grid-cols-[110px_100px_1fr_auto_auto]">
            <span className="text-mute">{ago(x.last)}</span>
            <span className="truncate">{x.user || '—'}</span>
            <span className="num min-w-0 truncate">{x.dest}:{x.port} <span className="text-mute">· {x.ip}{x.country ? ' ' + x.country : ''} · {x.node}</span></span>
            <span className="num text-mute">×{x.count}</span>
            <Pill tone={x.status === 'accepted' ? undefined : 'bad'}>{t('px.st_' + x.status)}</Pill>
          </div>
        ))}
        <p className="px-4 py-2 text-[11.5px] text-mute">{t('px.access_note')}</p>
      </Box>
    </>
  )
}
