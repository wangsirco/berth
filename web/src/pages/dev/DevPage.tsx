import { useEffect, useMemo, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Box, Dot, Empty, Notice, Pill } from '@/components/ui/Box'
import { Field, Input, Segmented, Switch, Textarea } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { cx } from '@/components/ui/cx'
import { errText, get, post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { bytes } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { openTerminal } from '@/pages/terminal/store'
import { NewSiteDialog } from '@/pages/sites/NewSiteDialog'
import { FlowsView } from '@/components/FlowsView'
import { useSearchParams } from 'react-router-dom'
import { ClaudeBox, openClaude } from './ClaudePanel'

interface DevEnv { image?: string; build?: { context: string; dockerfile: string }; user?: string; folder: string; extensions?: string[]; forward_ports?: number[]; notes?: string[]; hooks?: Record<string, string>; min_cpus?: number; min_mem_mb?: number }
interface Spec {
  name: string; source: 'new' | 'git' | 'dir'; git_url?: string; dir: string; made: boolean; env: 'auto' | 'preset'; preset: string; devcontainer_path?: string
  environment: DevEnv; cpus: number; memory_mb: number; code: boolean; access: 'lan' | 'public'; domains?: string[]; ssh: boolean; ssh_port: number; web_port: number
  claude?: boolean; no_lan?: boolean
}
interface SSHInfo { host: string; port: number; user: string; alias: string; command: string; folder: string }
interface Item { spec: Spec; state: string; container: string; id?: string; user: string; ssh?: SSHInfo; web?: string; sites?: { id: number; domain: string; port: number; enabled: boolean }[]; claude?: { running: number; queued: number; asking: number } }
interface Preset { id: string; image: string; size_mb: number }
interface Data {
  items: Item[]; presets: Preset[]; sizes: { id: string; cpus: number; memory_mb: number }[]; lan_ip: string; tools?: { code_server: string; want: string; claude?: string }
  my_keys: number; root: string; host: { cores: number; mem_total: number }
}
interface Stat { id: string; cpu: number; mem_bytes: number; mem_limit: number }
interface Status { user: string; hooks: Record<string, { status: string; log: string }>; code_log: string; extensions_log: string }

const PRESET_ICON: Record<string, string> = { base: '🐧', node: '🟩', python: '🐍', go: '🐹', rust: '🦀', java: '☕' }
const memText = (mb: number) => (mb >= 1024 ? `${+(mb / 1024).toFixed(1)} GB` : `${mb} MB`)

// copyText: navigator.clipboard where allowed, else a hidden textarea (plain http)
async function copyText(s: string) {
  try { await navigator.clipboard.writeText(s); return true } catch {
    const ta = document.createElement('textarea'); ta.value = s; document.body.appendChild(ta); ta.select()
    const ok = document.execCommand('copy'); ta.remove(); return ok
  }
}

export default function DevPage() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const d = useApi<Data>(`/api/v1/hosts/${host}/dev`, [host])
  const stats = useApi<{ items: Stat[] }>(`/api/v1/hosts/${host}/containers/stats`, [host])
  useEvents(['change.done', 'task.update', 'dev.task'], () => void d.reload())
  useInterval(() => { void stats.reload() }, 5000)
  // a fresh container installs SSH and starts the editor in its first seconds: refresh meanwhile
  useInterval(() => void d.reload(), d.data?.items.some((i) => i.state === 'running' && i.user === 'root' && i.spec.environment.user) ? 4000 : null)
  const [creating, setCreating] = useState(false)
  // a notification links to ?ws=<name>&task=<id>
  const [q] = useSearchParams()
  const [open, setOpen] = useState<string | null>(q.get('ws'))
  const focusTask = Number(q.get('task')) || undefined
  if (!can(user, 'dev.use')) return <Notice tone="warn">{t('auth.forbidden')}</Notice>
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const data = d.data
  const statOf = (it: Item) => (it.id ? stats.data?.items.find((s) => s.id === it.id || it.id!.startsWith(s.id) || s.id.startsWith(it.id!)) : undefined)
  const cur = data.items.find((i) => i.spec.name === open)
  return (
    <div className="space-y-4">
      <Box title={t('dev.title')} count={data.items.length} right={<Button size="sm" variant="primary" onClick={() => setCreating(true)}>{t('dev.new')}</Button>}>
        {data.items.length === 0 && <Empty title={t('dev.none')} action={<Button variant="primary" onClick={() => setCreating(true)}>{t('dev.new')}</Button>}>
          <div className="mx-auto mt-2 grid max-w-2xl gap-2 text-left sm:grid-cols-3">
            {(['web', 'ssh', 'iso'] as const).map((k) => <div key={k} className="rounded-[var(--radius-box)] border border-line p-3">
              <div className="font-semibold text-ink">{t('dev.intro_' + k)}</div><div className="mt-1 text-[12px]">{t('dev.intro_' + k + '_hint')}</div></div>)}
          </div>
        </Empty>}
        <div className="grid gap-3 p-3 lg:grid-cols-2">
          {data.items.map((it) => <Card key={it.spec.name} it={it} stat={statOf(it)} onOpen={() => setOpen(it.spec.name)} onRun={(k, p) => run(k, { name: it.spec.name, ...p }, { host })} />)}
        </div>
      </Box>
      {creating && <CreateDialog data={data} onClose={() => setCreating(false)} onDone={(name) => { setCreating(false); void d.reload(); setOpen(name) }} reload={() => void d.reload()} />}
      {cur && <DevDrawer it={cur} data={data} focusTask={cur.spec.name === q.get('ws') ? focusTask : undefined} onClose={() => setOpen(null)} />}
    </div>
  )
}

function envLabel(s: Spec, t: ReturnType<typeof useT>) {
  if (s.devcontainer_path) return t('dev.env_devcontainer', { path: s.devcontainer_path })
  return t('dev.preset_' + s.preset)
}

// openEditor: the tab opens at once (no popup blocker), the password goes to the clipboard
function useOpenEditor() {
  const t = useT()
  const { host } = useHost()
  const [msg, setMsg] = useState('')
  const openEditor = async (it: Item) => {
    if (!it.web) return
    window.open(it.web, '_blank', 'noopener')
    try {
      const r = await get<{ password: string }>(`/api/v1/hosts/${host}/dev/${it.spec.name}/password`)
      setMsg((await copyText(r.password)) ? t('dev.password_copied') : t('dev.password_is', { pw: r.password }))
    } catch (e) { setMsg(errText(e)) }
    setTimeout(() => setMsg(''), 8000)
  }
  return { openEditor, msg }
}

function Card({ it, stat, onOpen, onRun }: { it: Item; stat?: Stat; onOpen: () => void; onRun: (kind: string, p?: object) => void }) {
  const t = useT()
  const { openEditor, msg } = useOpenEditor()
  const s = it.spec
  const up = it.state === 'running'
  const memPct = stat && stat.mem_limit ? Math.min(100, (stat.mem_bytes / stat.mem_limit) * 100) : 0
  const cpuPct = stat ? Math.min(100, stat.cpu / s.cpus) : 0
  const bar = (pct: number, label: string) => (
    <div className="flex-1">
      <div className="flex justify-between text-[11px] text-mute"><span>{label}</span><span className="num">{pct.toFixed(0)}%</span></div>
      <div className="mt-0.5 h-1.5 rounded-full bg-soft"><div className={cx('h-1.5 rounded-full', pct > 85 ? 'bg-warn' : 'bg-acc')} style={{ width: `${Math.max(2, pct)}%` }} /></div>
    </div>
  )
  return (
    <div className="rounded-[var(--radius-box)] border border-line bg-panel p-3" data-dev={s.name}>
      <div className="flex items-start gap-2">
        <Dot state={up ? 'ok' : it.state === 'missing' ? 'bad' : 'off'} className="mt-1.5" />
        <button className="min-w-0 flex-1 text-left" onClick={onOpen}>
          <div className="text-[14px] font-semibold hover:text-acc">{s.name}</div>
          <div className="truncate text-[12px] text-mute">{envLabel(s, t)} · <span className="num">{s.dir}</span></div>
        </button>
        {it.claude && (it.claude.running + it.claude.queued + it.claude.asking > 0) && <button onClick={onOpen} className="flex shrink-0 gap-1">
          {it.claude.running > 0 && <Pill tone="acc">{t('claude.badge_running')}</Pill>}
          {it.claude.queued > 0 && <Pill>{t('claude.badge_queued', { n: it.claude.queued })}</Pill>}
          {it.claude.asking > 0 && <Pill tone="warn">{t('claude.badge_asking')}</Pill>}
        </button>}
        <Menu trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}>⋯</Button>} items={[
          { label: t('dev.details'), onClick: onOpen },
          up ? { label: t('action.dev.restart'), onClick: () => onRun('dev.restart') } : { label: t('action.dev.start'), onClick: () => onRun('dev.start') },
          ...(up ? [{ label: t('action.dev.stop'), onClick: () => onRun('dev.stop') }] : []),
          { label: t('action.dev.rebuild'), onClick: () => onRun('dev.rebuild') },
          'sep' as const,
          { label: t('action.dev.delete'), danger: true, onClick: onOpen },
        ]} />
      </div>
      {up && <div className="mt-2.5 flex gap-3">
        {bar(cpuPct, t('dev.cpu_of', { n: s.cpus }))}
        {bar(memPct, t('dev.mem_of', { v: memText(s.memory_mb) }) + (stat ? ' · ' + bytes(stat.mem_bytes) : ''))}
      </div>}
      {!up && <div className="mt-2 text-[12px] text-mute">{it.state === 'missing' ? t('dev.state_missing') : t('dev.state_stopped')}</div>}
      <div className="mt-3 flex flex-wrap gap-1.5">
        {up && s.code && it.web && <Button size="sm" variant="primary" onClick={() => void openEditor(it)}>{t('dev.open_web')}</Button>}
        {up && it.ssh && <a className="inline-flex" href={`vscode://vscode-remote/ssh-remote+${it.ssh.user}@${it.ssh.host}:${it.ssh.port}${it.ssh.folder}`}><Button size="sm">{t('dev.open_vscode')}</Button></a>}
        {up && s.claude && <Button size="sm" onClick={() => openClaude(s.name)}>{t('claude.chat')}</Button>}
        {up && s.claude && <Button size="sm" onClick={onOpen}>{t('claude.new_task')}</Button>}
        {up && <Button size="sm" onClick={() => openTerminal({ target: 'dev', container: s.name })}>{t('dev.terminal')}</Button>}
        {!up && <Button size="sm" variant="primary" onClick={() => onRun('dev.start')}>{t('action.dev.start')}</Button>}
      </div>
      {msg && <div className="mt-2 text-[12px] text-ok">{msg}</div>}
    </div>
  )
}

function CopyLine({ label, text }: { label: string; text: string }) {
  const t = useT()
  const [ok, setOk] = useState(false)
  return (
    <div className="flex items-center gap-2 py-1 text-[12.5px]">
      <span className="w-28 shrink-0 text-mute">{label}</span>
      <code className="num min-w-0 flex-1 truncate rounded bg-soft px-1.5 py-0.5 select-all">{text}</code>
      <Button size="sm" variant="ghost" onClick={() => void copyText(text).then((v) => { setOk(v); setTimeout(() => setOk(false), 1500) })}>{ok ? t('dev.copied') : t('dev.copy')}</Button>
    </div>
  )
}

function DevDrawer({ it, data, focusTask, onClose }: { it: Item; data: Data; focusTask?: number; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const s = it.spec
  const up = it.state === 'running'
  const st = useApi<{ status: Status }>(`/api/v1/hosts/${host}/dev/${s.name}`, [host, s.name])
  useInterval(() => void st.reload(), up ? 5000 : null)
  const { openEditor, msg } = useOpenEditor()
  const [pw, setPw] = useState('')
  const [edit, setEdit] = useState(false)
  const [site, setSite] = useState<number | null>(null)
  const [del, setDel] = useState(false)
  const status = st.data?.status
  const sshCfg = it.ssh ? `Host ${it.ssh.alias}\n  HostName ${it.ssh.host}\n  Port ${it.ssh.port}\n  User ${it.ssh.user}` : ''
  const kv = (k: string, v: React.ReactNode) => <div className="flex gap-3 py-1 text-[13px]"><span className="w-28 shrink-0 text-mute">{k}</span><span className="min-w-0 flex-1">{v}</span></div>
  const notes = (s.environment.notes ?? []).map((n) => { const [code, arg] = n.split(/:(.*)/s); return t(code, { v: arg ?? '' }) })
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={<span className="flex items-center gap-2"><Dot state={up ? 'ok' : 'off'} />{s.name}</span>}>
      <div className="space-y-4 p-4">
        <div className="flex flex-wrap gap-2">
          {up ? <>
            {s.code && it.web && <Button size="sm" variant="primary" onClick={() => void openEditor(it)}>{t('dev.open_web')}</Button>}
            <Button size="sm" onClick={() => openTerminal({ target: 'dev', container: s.name })}>{t('dev.terminal')}</Button>
            <Button size="sm" onClick={() => run('dev.restart', { name: s.name }, { host })}>{t('action.dev.restart')}</Button>
            <Button size="sm" onClick={() => run('dev.stop', { name: s.name }, { host })}>{t('action.dev.stop')}</Button>
          </> : <Button size="sm" variant="primary" onClick={() => run('dev.start', { name: s.name }, { host })}>{t('action.dev.start')}</Button>}
          <Button size="sm" onClick={() => setEdit(true)}>{t('dev.change')}</Button>
          <Button size="sm" onClick={() => run('dev.rebuild', { name: s.name }, { host })}>{t('action.dev.rebuild')}</Button>
        </div>
        {msg && <Notice>{msg}</Notice>}
        {notes.length > 0 && <Notice tone="warn"><div className="space-y-1">{notes.map((n, i) => <div key={i}>{n}</div>)}</div></Notice>}

        <ClaudeBox name={s.name} up={up} focusTask={focusTask} />

        <Box title={t('dev.connect')}>
          <div className="px-4 py-2">
            {s.code && it.web && <>
              <CopyLine label={t('dev.web_url')} text={it.web} />
              <div className="flex items-center gap-2 py-1 text-[12.5px]"><span className="w-28 shrink-0 text-mute">{t('dev.password')}</span>
                {pw ? <code className="num flex-1 select-all rounded bg-soft px-1.5 py-0.5">{pw}</code>
                  : <button className="text-acc hover:underline" onClick={() => void get<{ password: string }>(`/api/v1/hosts/${host}/dev/${s.name}/password`).then((r) => setPw(r.password))}>{t('dev.show_password')}</button>}
              </div>
              {s.access === 'lan' && <p className="py-1 text-[12px] text-mute">{t('dev.ca_hint')} <a className="text-acc hover:underline" href="/api/v1/ca.crt">{t('dev.ca_download')}</a></p>}
            </>}
            {it.ssh && <>
              <CopyLine label="SSH" text={it.ssh.command} />
              <div className="py-1 text-[12.5px]">
                <div className="mb-1 text-mute">{t('dev.ssh_config')}</div>
                <pre className="num relative rounded bg-soft px-2 py-1.5 text-[12px] select-all">{sshCfg}</pre>
                <div className="mt-1.5 flex flex-wrap gap-1.5">
                  <a href={`vscode://vscode-remote/ssh-remote+${it.ssh.user}@${it.ssh.host}:${it.ssh.port}${it.ssh.folder}`}><Button size="sm">{t('dev.open_vscode')}</Button></a>
                  <a href={`cursor://vscode-remote/ssh-remote+${it.ssh.user}@${it.ssh.host}:${it.ssh.port}${it.ssh.folder}`}><Button size="sm">{t('dev.open_cursor')}</Button></a>
                  <Button size="sm" variant="ghost" onClick={() => void copyText(sshCfg)}>{t('dev.copy_config')}</Button>
                </div>
                <p className="mt-1.5 text-[12px] text-mute">{data.my_keys > 0 ? t('dev.ssh_keys_hint', { n: data.my_keys }) : t('dev.ssh_no_keys')}</p>
              </div>
            </>}
          </div>
        </Box>

        <div>
          {kv(t('dev.environment'), <span>{envLabel(s, t)}<span className="num text-mute"> · {s.environment.image ?? 'Dockerfile'}</span></span>)}
          {kv(t('dev.folder'), <span className="num">{s.dir} → {s.environment.folder}</span>)}
          {s.git_url && kv('Git', <span className="num">{s.git_url}</span>)}
          {kv(t('dev.size'), <span className="num">{t('dev.size_line', { cpus: s.cpus, mem: memText(s.memory_mb) })}</span>)}
          {kv(t('dev.user'), <span className="num">{it.user}</span>)}
          {kv(t('dev.network'), s.no_lan ? <Pill tone="ok">{t('dev.no_lan_on')}</Pill> : <span className="text-mute">{t('dev.lan_open')}</span>)}
        </div>

        {status && Object.keys(status.hooks).length > 0 && <Box title={t('dev.lifecycle')}>
          {Object.entries(status.hooks).map(([k, h]) => (
            <details key={k} className="border-b border-line px-4 py-2 text-[12.5px] last:border-0">
              <summary className="cursor-pointer"><span className="font-semibold">{k}</span> <Pill tone={h.status === 'done' ? 'ok' : h.status === 'failed' ? 'bad' : 'warn'}>{t('dev.hook_' + (h.status || 'waiting'))}</Pill></summary>
              <pre className="num mt-1 max-h-60 overflow-auto rounded bg-soft p-2 text-[11.5px] whitespace-pre-wrap">{h.log || '—'}</pre>
            </details>
          ))}
        </Box>}

        <Box title={t('dev.ports')}>
          <div className="px-4 py-2 text-[12.5px]">
            <p className="text-mute">{t('dev.ports_hint')}</p>
            <div className="mt-2 flex flex-wrap gap-1.5">
              {[...new Set([...(s.environment.forward_ports ?? []), 3000, 5173, 8000, 8080])].slice(0, 8).map((p) => (
                <Button key={p} size="sm" disabled={!can(user, 'sites.manage')} onClick={() => setSite(p)}>{t('dev.make_site', { port: p })}</Button>
              ))}
            </div>
            {(it.sites ?? []).filter((x) => x.port !== 8443).map((x) => <div key={x.id} className="mt-2"><a className="text-acc hover:underline" href={`https://${x.domain}`} target="_blank" rel="noreferrer">{x.domain}</a> <span className="num text-mute">→ :{x.port}</span></div>)}
          </div>
        </Box>

        <Box title={t('flows.tab')}><FlowsView obj={'container:' + it.container} /></Box>

        <div className="border-t border-line pt-3">
          <Button size="sm" variant="ghost" onClick={() => setDel(true)}>{t('action.dev.delete')}</Button>
        </div>
      </div>
      {edit && <EditDialog it={it} data={data} onClose={() => setEdit(false)} />}
      {site !== null && <NewSiteDialog preset={{ type: 'container', container: it.container, port: site }} onClose={() => setSite(null)} onDone={() => setSite(null)} />}
      {del && <DeleteDialog it={it} onClose={() => setDel(false)} onDone={onClose} />}
    </Drawer>
  )
}

function DeleteDialog({ it, onClose, onDone }: { it: Item; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [files, setFiles] = useState(false)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('action.dev.delete') + ' ' + it.spec.name}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="danger" onClick={() => { onClose(); run('dev.delete', { name: it.spec.name, files }, { host, confirmName: it.spec.name, onDone }) }}>{t('action.dev.delete')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <p>{t('dev.delete_keeps', { dir: it.spec.dir })}</p>
        {it.spec.made ? <Switch checked={files} onChange={setFiles} label={t('dev.delete_files')} /> : <p className="text-mute">{t('dev.delete_not_made')}</p>}
      </div>
    </Dialog>
  )
}

function SizePicker({ data, cpus, mem, onChange, min }: { data: Data; cpus: number; mem: number; onChange: (c: number, m: number) => void; min?: { cpus?: number; mem?: number } }) {
  const t = useT()
  const hostMB = Math.floor(data.host.mem_total / 1048576)
  const preset = data.sizes.find((z) => z.cpus === cpus && z.memory_mb === mem)?.id ?? 'custom'
  const [custom, setCustom] = useState(preset === 'custom')
  return (
    <div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {data.sizes.map((z) => (
          <button key={z.id} type="button" onClick={() => { setCustom(false); onChange(z.cpus, z.memory_mb) }}
            className={cx('rounded-[var(--radius-box)] border p-2 text-left', !custom && preset === z.id ? 'border-acc bg-tint' : 'border-line hover:border-acc', hostMB > 0 && z.memory_mb > hostMB * 0.8 && 'opacity-60')}>
            <div className="font-semibold">{t('dev.size_' + z.id)}</div>
            <div className="num text-[11.5px] text-mute">{z.cpus} {t('dev.cores')} · {memText(z.memory_mb)}</div>
          </button>
        ))}
        <button type="button" onClick={() => setCustom(true)} className={cx('rounded-[var(--radius-box)] border p-2 text-left', custom ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
          <div className="font-semibold">{t('dev.size_custom')}</div><div className="text-[11.5px] text-mute">{t('dev.size_custom_hint')}</div>
        </button>
      </div>
      {custom && <div className="mt-2 grid grid-cols-2 gap-2">
        <Field label={t('dev.cpus')}><Input type="number" min={0.5} step={0.5} max={data.host.cores || 64} value={cpus} onChange={(e) => onChange(Number(e.target.value), mem)} /></Field>
        <Field label={t('dev.memory_mb')}><Input type="number" min={256} step={256} value={mem} onChange={(e) => onChange(cpus, Number(e.target.value))} /></Field>
      </div>}
      <p className="mt-1.5 text-[12px] text-mute">{t('dev.size_hint', { cores: data.host.cores, mem: memText(hostMB) })}
        {min && ((min.cpus ?? 0) > cpus || (min.mem ?? 0) > mem) && <span className="text-warn"> {t('dev.size_below_req', { cpus: min.cpus ?? '—', mem: min.mem ? memText(min.mem) : '—' })}</span>}</p>
    </div>
  )
}

function AccessPicker({ code, access, domain, onChange }: { code: boolean; access: 'lan' | 'public'; domain: string; onChange: (v: { code?: boolean; access?: 'lan' | 'public'; domain?: string }) => void }) {
  const t = useT()
  return (
    <div className="space-y-2">
      <Switch checked={code} onChange={(v) => onChange({ code: v })} label={t('dev.web_vscode')} />
      {code && <div className="grid gap-2 sm:grid-cols-2">
        {(['lan', 'public'] as const).map((a) => (
          <button key={a} type="button" onClick={() => onChange({ access: a })} className={cx('rounded-[var(--radius-box)] border p-2 text-left', access === a ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
            <div className="font-semibold">{t('dev.access_' + a)}{a === 'lan' && <span className="ml-1 text-[11px] font-normal text-acc">{t('dev.recommended')}</span>}</div>
            <div className="text-[11.5px] text-mute">{t('dev.access_' + a + '_hint')}</div>
          </button>
        ))}
      </div>}
      {code && access === 'public' && <Field label={t('dev.domain')} hint={t('dev.domain_hint')}><Input className="num" placeholder="code.example.com" value={domain} onChange={(e) => onChange({ domain: e.target.value.trim().toLowerCase() })} /></Field>}
    </div>
  )
}

function CreateDialog({ data, onClose, onDone, reload }: { data: Data; onClose: () => void; onDone: (name: string) => void; reload: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [source, setSource] = useState<'new' | 'git' | 'dir'>('new')
  const [git, setGit] = useState({ url: '', ref: '' })
  const [dir, setDir] = useState('')
  const [inspect, setInspect] = useState<{ devcontainer_path?: string; environment?: DevEnv; error?: string } | null>(null)
  const [inspectErr, setInspectErr] = useState('')
  const [useDC, setUseDC] = useState(true)
  const [preset, setPreset] = useState('base')
  const [size, setSize] = useState({ cpus: 2, mem: 2048 })
  const [web, setWeb] = useState<{ code: boolean; access: 'lan' | 'public'; domain: string }>({ code: true, access: 'lan', domain: '' })
  const [ssh, setSsh] = useState(true)
  const [claude, setClaude] = useState(false)
  const [noLan, setNoLan] = useState<boolean | null>(null) // follows Claude until touched
  const [key, setKey] = useState('')
  const [keyErr, setKeyErr] = useState('')
  const [keys, setKeys] = useState(data.my_keys)
  const [moreKey, setMoreKey] = useState(false)
  const used = useMemo(() => new Set(data.items.map((i) => i.spec.name)), [data.items])
  const [name, setName] = useState('')
  const [touched, setTouched] = useState(false)
  const slug = (s: string) => s.toLowerCase().replace(/\.git$/, '').replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 31)
  const suggest = (base: string) => { const b = slug(base) || 'project'; if (!used.has(b)) return b; for (let n = 2; ; n++) if (!used.has(`${b}-${n}`)) return `${b}-${n}` }
  useEffect(() => {
    if (touched) return
    const base = source === 'git' ? git.url.split(/[/:]/).pop() ?? '' : source === 'dir' ? dir.split('/').filter(Boolean).pop() ?? '' : 'project'
    setName(suggest(base))
  }, [source, git.url, dir]) // eslint-disable-line react-hooks/exhaustive-deps
  // an existing folder: look for its devcontainer.json as soon as the path is typed
  useEffect(() => {
    setInspect(null); setInspectErr('')
    if (source !== 'dir' || !dir.startsWith('/')) return
    const h = setTimeout(() => {
      get<typeof inspect>(`/api/v1/hosts/${host}/dev/inspect?dir=${encodeURIComponent(dir)}`).then(setInspect).catch((e) => setInspectErr(errText(e)))
    }, 400)
    return () => clearTimeout(h)
  }, [source, dir, host])
  const dc = source === 'dir' && inspect?.environment
  const envMode = source === 'new' ? 'preset' : useDC ? 'auto' : 'preset'
  const addKey = async () => {
    setKeyErr('')
    try { await post('/api/v1/me/ssh-keys', { name: 'workspace', key: key.trim() }); setKeys(keys + 1); setKey(''); setMoreKey(false); reload() } catch (e) { setKeyErr(errText(e)) }
  }
  const params = {
    name, source, git_url: git.url, git_ref: git.ref, dir, env: envMode, preset, cpus: size.cpus, memory_mb: size.mem,
    code: web.code, access: web.access, domains: web.access === 'public' && web.domain ? [web.domain] : [], ssh, claude, no_lan: noLan ?? claude,
  }
  const ready = name && (source !== 'git' || git.url) && (source !== 'dir' || (dir && !inspectErr)) && (!web.code || web.access === 'lan' || web.domain) && (web.code || ssh)
  const step = (n: number, title: string, body: React.ReactNode) => (
    <section className="flex gap-3">
      <div className="num mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-tint text-[11px] font-semibold text-acc">{n}</div>
      <div className="min-w-0 flex-1"><div className="mb-1.5 font-semibold">{title}</div>{body}</div>
    </section>
  )
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('dev.new')}
      footer={<><span className="text-[12px] text-mute">{t('dev.create_note')}</span><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!ready} onClick={() => run('dev.create', params, { host, onDone: () => onDone(name) })}>{t('action.dev.create')}</Button></>}>
      <div className="space-y-5 px-4 py-3 text-[13px]">
        {step(1, t('dev.step_source'), <>
          <Segmented value={source} onChange={setSource} options={[{ value: 'new', label: t('dev.source_new') }, { value: 'git', label: t('dev.source_git') }, { value: 'dir', label: t('dev.source_dir') }]} />
          {source === 'new' && <p className="mt-1.5 text-[12px] text-mute">{t('dev.source_new_hint', { dir: `${data.root}/${name || '…'}` })}</p>}
          {source === 'git' && <div className="mt-2 grid gap-2 sm:grid-cols-[1fr_10rem]">
            <Input className="num" placeholder="https://github.com/you/project.git" value={git.url} onChange={(e) => setGit({ ...git, url: e.target.value.trim() })} aria-label={t('dev.git_url')} />
            <Input className="num" placeholder={t('dev.git_ref')} value={git.ref} onChange={(e) => setGit({ ...git, ref: e.target.value.trim() })} aria-label={t('dev.git_ref')} />
            <p className="text-[12px] text-mute sm:col-span-2">{t('dev.git_hint', { dir: `${data.root}/${name || '…'}` })}</p>
          </div>}
          {source === 'dir' && <div className="mt-2">
            <Input className="num" placeholder="/srv/www/my-site" value={dir} onChange={(e) => setDir(e.target.value.trim())} aria-label={t('dev.dir')} />
            {inspectErr ? <p className="mt-1 text-[12px] text-bad">{inspectErr}</p> : <p className="mt-1 text-[12px] text-mute">{t('dev.dir_hint')}</p>}
          </div>}
        </>)}

        {step(2, t('dev.step_env'), <>
          {source !== 'new' && <div className="mb-2">
            {dc ? <Notice><div className="space-y-1">
              <div>{t('dev.dc_found', { path: inspect?.devcontainer_path ?? '' })}</div>
              <div className="num text-[12px] text-mute">{inspect?.environment?.image ?? 'Dockerfile'}{inspect?.environment?.forward_ports?.length ? ' · ' + t('dev.dc_ports', { ports: inspect.environment.forward_ports.join(', ') }) : ''}</div>
              <Switch checked={useDC} onChange={setUseDC} label={t('dev.dc_use')} />
            </div></Notice>
              : source === 'dir' && inspect?.error ? <Notice tone="warn">{t('dev.dc_bad', { err: inspect.error })}</Notice>
              : source === 'git' ? <Switch checked={useDC} onChange={setUseDC} label={t('dev.dc_auto')} />
              : source === 'dir' && inspect ? <p className="text-[12px] text-mute">{t('dev.dc_none')}</p> : null}
          </div>}
          {!(dc && useDC) && <>
            {source === 'git' && useDC && <p className="mb-1.5 text-[12px] text-mute">{t('dev.dc_fallback')}</p>}
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {data.presets.map((p) => (
                <button key={p.id} type="button" onClick={() => setPreset(p.id)} className={cx('rounded-[var(--radius-box)] border p-2 text-left', preset === p.id ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
                  <div className="font-semibold"><span className="mr-1">{PRESET_ICON[p.id]}</span>{t('dev.preset_' + p.id)}</div>
                  <div className="text-[11.5px] text-mute">{t('dev.preset_' + p.id + '_hint')} · ~{memText(p.size_mb)}</div>
                </button>
              ))}
            </div>
          </>}
        </>)}

        {step(3, t('dev.step_size'), <SizePicker data={data} cpus={size.cpus} mem={size.mem} onChange={(c, m) => setSize({ cpus: c, mem: m })}
          min={dc && useDC ? { cpus: inspect?.environment?.min_cpus, mem: inspect?.environment?.min_mem_mb } : undefined} />)}

        {step(4, t('dev.step_access'), <div className="space-y-3">
          <AccessPicker code={web.code} access={web.access} domain={web.domain} onChange={(v) => setWeb({ ...web, ...v })} />
          <div>
            <Switch checked={ssh} onChange={setSsh} label={t('dev.ssh_for_desktop')} />
            {ssh && keys > 0 && <p className="mt-1 text-[12px] text-mute">{t('dev.ssh_keys_hint', { n: keys })}
              {!moreKey && <> <button type="button" className="text-accent hover:underline" onClick={() => setMoreKey(true)}>{t('dev.ssh_another')}</button></>}</p>}
            {ssh && (keys === 0 || moreKey) && <div className="mt-1.5 space-y-1.5">
              {keys === 0 && <p className="text-[12px] text-warn">{t('dev.ssh_add_key')}</p>}
              <Textarea rows={2} className="num text-[12px]" placeholder="ssh-ed25519 AAAA… you@laptop" value={key} onChange={(e) => setKey(e.target.value)} aria-label={t('dev.ssh_key')} />
              <div className="flex items-center gap-2"><Button size="sm" disabled={!key.trim()} onClick={() => void addKey()}>{t('dev.ssh_save_key')}</Button>
                <span className="text-[12px] text-mute">{t('dev.ssh_key_how')}</span></div>
              {keyErr && <p className="text-[12px] text-bad">{keyErr}</p>}
            </div>}
          </div>
          <div>
            <Switch checked={claude} onChange={setClaude} label={t('claude.in_workspace')} />
            <p className="mt-1 text-[12px] text-mute">{claude ? t('claude.in_workspace_on', { size: data.tools?.claude ? '' : t('claude.first_download') }) : t('claude.in_workspace_hint')}</p>
            {claude && size.mem < 1024 && <p className="mt-1 text-[12px] text-warn">{t('claude.mem_low')}</p>}
          </div>
          <div>
            <Switch checked={noLan ?? claude} onChange={setNoLan} label={t('dev.no_lan')} />
            <p className="mt-1 text-[12px] text-mute">{t('dev.no_lan_hint')}</p>
          </div>
        </div>)}

        {step(5, t('dev.step_name'), <Input className="num max-w-xs" value={name} onChange={(e) => { setTouched(true); setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, '').slice(0, 31)) }} aria-label={t('dev.name')} />)}
      </div>
    </Dialog>
  )
}

function EditDialog({ it, data, onClose }: { it: Item; data: Data; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const s = it.spec
  const [size, setSize] = useState({ cpus: s.cpus, mem: s.memory_mb })
  const [web, setWeb] = useState<{ code: boolean; access: 'lan' | 'public'; domain: string }>({ code: s.code, access: s.access, domain: s.domains?.[0] ?? '' })
  const [ssh, setSsh] = useState(s.ssh)
  const [claude, setClaude] = useState(!!s.claude)
  const [noLan, setNoLan] = useState(!!s.no_lan)
  const p = { no_lan: noLan, name: s.name, cpus: size.cpus, memory_mb: size.mem, code: web.code, access: web.access, domains: web.domain ? [web.domain] : [], ssh, claude }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('dev.change') + ' ' + s.name}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => run('dev.set', p, { host, onDone: onClose })}>{t('action.dev.set')}</Button></>}>
      <div className="space-y-4 px-4 py-3 text-[13px]">
        <div><div className="mb-1.5 font-semibold">{t('dev.step_size')}</div><SizePicker data={data} cpus={size.cpus} mem={size.mem} onChange={(c, m) => setSize({ cpus: c, mem: m })} /></div>
        <div><div className="mb-1.5 font-semibold">{t('dev.step_access')}</div>
          <AccessPicker code={web.code} access={web.access} domain={web.domain} onChange={(v) => setWeb({ ...web, ...v })} />
          <div className="mt-2"><Switch checked={ssh} onChange={setSsh} label={t('dev.ssh_for_desktop')} /></div>
          <div className="mt-2"><Switch checked={claude} onChange={setClaude} label={t('claude.in_workspace')} /></div>
          <div className="mt-2"><Switch checked={noLan} onChange={setNoLan} label={t('dev.no_lan')} /></div></div>
        <p className="text-[12px] text-mute">{t('dev.change_restarts')}</p>
      </div>
    </Dialog>
  )
}
