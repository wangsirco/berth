import { useState } from 'react'
import { Globe, MoreHorizontal, Plus } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Select, Textarea } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { can, useAuth } from '@/lib/auth'
import { ago, bytes } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { NewSiteDialog } from './NewSiteDialog'
import { cx } from '@/components/ui/cx'
import { BADGE, NativeDetail, NativeWizard, type NativeItem } from './native/Native'
import { langOf, type Kind as NKind } from './native/lang'

type Kind = 'node' | 'python' | 'go' | 'java' | 'custom'
interface Project { name: string; kind: Kind; version?: string; dir: string; command: string; install?: string; user: string; env?: Record<string, string>; port?: number; unit: string; active: string; mem_bytes?: number; sites: number[] }
interface Data { items: Project[]; defaults: Record<Kind, [string, string]>; users: { name: string }[] }

export type Lang = '' | 'node' | 'python' | 'go' | 'java' | 'other'
type Mode = 'managed' | 'run'

// One row of the merged list: a native project (built into releases) or a simple one (run in place).
interface Item { name: string; lang: Exclude<Lang, ''>; label: string; mode: Mode; state: 'ok' | 'bad' | 'off' | 'busy'; port?: number; sites: number; mem?: number; sub: string; native?: NativeItem; simple?: Project }

const langOfSimple = (k: Kind): Exclude<Lang, ''> => (k === 'custom' ? 'other' : k)

// Projects (宝塔/1Panel 的 Node、Python、Go、Java 项目): one list, whichever way each one is run.
export function ProjectsTab({ lang = '' }: { lang?: Lang }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'sites.manage')
  const d = useApi<Data>(`/api/v1/hosts/${host}/projects`, [host])
  const nat = useApi<{ items: NativeItem[] }>(`/api/v1/hosts/${host}/native`, [host])
  useEvents(['change.done', 'task.update'], () => { void d.reload(); void nat.reload() })
  const [edit, setEdit] = useState<Project | 'new' | null>(null)
  const [logs, setLogs] = useState<Project | null>(null)
  const [site, setSite] = useState<Project | null>(null)
  const [adding, setAdding] = useState(false)
  const [wizard, setWizard] = useState<NKind | 'any' | null>(null)
  const [newKind, setNewKind] = useState<Kind>('node')
  const [open, setOpen] = useState<string | null>(null)
  if (!d.data || !nat.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const svc = (p: Project, action: string) => run('system.service', { name: p.unit, action }, { host })
  const items: Item[] = [
    ...nat.data.items.map((p): Item => ({ name: p.name, lang: (p.kind || 'node') as NKind, label: `${langOf(p.kind)} ${p.version}`, mode: 'managed', state: !p.current ? 'off' : p.active ? 'ok' : 'bad', port: p.port,
      sites: (p.sites ?? []).length, sub: p.source.type === 'git' ? p.source.url + ' · ' + (p.source.branch || 'main') : p.source.path ?? '', native: p })),
    ...d.data.items.map((p): Item => ({ name: p.name, lang: langOfSimple(p.kind), label: t('proj.k_' + p.kind) + (p.version ? ' ' + p.version : ''), mode: 'run',
      state: p.active === 'active' ? 'ok' : p.active === 'failed' ? 'bad' : p.active === 'activating' ? 'busy' : 'off', port: p.port, sites: p.sites.length, mem: p.mem_bytes, sub: `${p.dir} $ ${p.command}`, simple: p })),
  ].filter((x) => !lang || x.lang === lang).sort((a, b) => a.name.localeCompare(b.name))
  const startNew = (k: Exclude<Lang, ''>, mode: Mode) => {
    setAdding(false)
    if (mode === 'managed' && k !== 'other') setWizard(k)
    else { setNewKind(k === 'other' ? 'custom' : k); setEdit('new') }
  }
  return (
    <>
      <Box title={lang ? t('projects.tab_' + lang) : t('projects.all')} count={items.length} right={manage && <Button size="sm" variant="primary" onClick={() => setAdding(true)}><Plus className="size-3.5" />{t('proj.add')}</Button>}>
        {items.length === 0 && <Empty title={t('projects.empty')}>{t('projects.empty_hint')}</Empty>}
        {items.map((x) => {
          const p = x.simple
          return (
            <Row key={x.mode + x.name} onClick={() => (x.native ? setOpen(x.name) : p && setLogs(p))}>
              <Dot state={x.state} />
              <span className={cx('grid size-8 shrink-0 place-items-center rounded-lg text-[12px] font-bold text-white', x.lang === 'other' ? 'bg-mute' : BADGE[x.lang][1])}>{x.lang === 'other' ? '$' : BADGE[x.lang][0]}</span>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2 text-[13.5px]"><b>{x.name}</b><Pill>{x.label}</Pill>
                  <Pill tone={x.mode === 'managed' ? 'acc' : undefined}>{x.mode === 'managed' ? (x.native?.source.type === 'git' ? t('projects.mode_git') : t('projects.mode_managed')) : t('projects.mode_run')}</Pill>
                  {x.port ? <span className="num text-[12px] text-mute">:{x.port}</span> : null}</div>
                <div className="num truncate text-[12px] text-mute">{x.sub}</div>
              </div>
              <span className="text-[12px] text-mute max-sm:hidden">{x.native ? (x.native.current ? t('native.deployed_ago', { ago: ago(x.native.deployed ?? 0, t) }) : t('native.not_deployed')) : x.mem ? bytes(x.mem) : ''}</span>
              {x.sites > 0 ? <Pill><Globe className="mr-1 size-3" />{x.sites}</Pill> : <span className="text-[12px] text-mute max-sm:hidden">{t('home.no_site')}</span>}
              {manage && p && p.port && p.sites.length === 0 ? <Button size="sm" onClick={(e) => { e.stopPropagation(); setSite(p) }}>{t('proj.make_site')}</Button> : null}
              {manage && p && <span onClick={(e) => e.stopPropagation()}><Menu items={[
                { label: t('system.svc_restart'), onClick: () => svc(p, 'restart') },
                p.active === 'active' ? { label: t('system.svc_stop'), onClick: () => svc(p, 'stop') } : { label: t('system.svc_start'), onClick: () => svc(p, 'start') },
                { label: t('common.edit'), onClick: () => setEdit(p) },
                { label: t('system.logs'), onClick: () => setLogs(p) },
                'sep',
                { label: t('action.project.delete'), danger: true, onClick: () => run('project.delete', { name: p.name }, { host }) },
              ]} trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} /></span>}
            </Row>
          )
        })}
      </Box>
      {adding && <NewProject lang={lang} onClose={() => setAdding(false)} onPick={startNew} />}
      {wizard && <NativeWizard kind={wizard === 'any' ? undefined : wizard} onClose={() => setWizard(null)} onOpen={(n) => { setWizard(null); setOpen(n) }} />}
      {open && <NativeDetail name={open} onClose={() => setOpen(null)} />}
      {edit && <ProjectDialog p={edit === 'new' ? null : edit} kind={newKind} data={d.data} onClose={() => setEdit(null)} />}
      {logs && <ProjectLogs p={logs} onClose={() => setLogs(null)} />}
      {site && <NewSiteDialog preset={{ type: 'address', upstreams: ['127.0.0.1:' + site.port] }} onClose={() => setSite(null)} onDone={() => void d.reload()} />}
    </>
  )
}

// NewProject: the language, then the way it runs; each opens its own form.
function NewProject({ lang, onClose, onPick }: { lang: Lang; onClose: () => void; onPick: (k: Exclude<Lang, ''>, m: Mode) => void }) {
  const t = useT()
  const [k, setK] = useState<Exclude<Lang, ''>>(lang || 'node')
  const [m, setM] = useState<Mode>(lang === 'other' ? 'run' : 'managed')
  const modes: Mode[] = k === 'other' ? ['run'] : ['managed', 'run']
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('proj.add')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => onPick(k, modes.includes(m) ? m : 'run')}>{t('common.continue')}</Button></>}>
      <div className="space-y-4 px-4 py-3">
        <Field label={t('projects.pick_lang')}>
          <Segmented value={k} onChange={setK} options={(['node', 'python', 'go', 'java', 'other'] as const).map((x) => ({ value: x, label: t('projects.tab_' + x) }))} />
        </Field>
        <Field label={t('projects.pick_mode')}>
          <div className="grid gap-2">
            {modes.map((x) => (
              <button key={x} type="button" onClick={() => setM(x)}
                className={cx('rounded-xl border px-3 py-2.5 text-left', (modes.includes(m) ? m : 'run') === x ? 'border-acc bg-tint' : 'border-line hover:border-mute')}>
                <div className="text-[13.5px] font-semibold">{t('projects.m_' + x)}</div>
                <div className="text-[12px] text-mute">{t('projects.m_' + x + '_hint')}</div>
              </button>
            ))}
          </div>
        </Field>
      </div>
    </Dialog>
  )
}

function ProjectDialog({ p, kind, data, onClose }: { p: Project | null; kind: Kind; data: Data; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [f, setF] = useState({ name: p?.name ?? '', kind: (p?.kind ?? kind) as Kind, version: p?.version ?? data.defaults[kind][0], dir: p?.dir ?? '/srv/', command: p?.command ?? data.defaults[kind][1],
    install: p?.install ?? '', user: p?.user ?? (data.users[0]?.name ?? ''), port: p?.port ? String(p.port) : '', env: Object.entries(p?.env ?? {}).map(([k, v]) => `${k}=${v}`).join('\n') })
  const setKind = (k: Kind) => setF({ ...f, kind: k, version: data.defaults[k][0], command: data.defaults[k][1] })
  const env = Object.fromEntries(f.env.split('\n').map((l) => l.trim()).filter((l) => l.includes('=')).map((l) => [l.slice(0, l.indexOf('=')), l.slice(l.indexOf('=') + 1)]))
  const ok = /^[a-z][a-z0-9-]{0,30}$/.test(f.name) && f.dir.startsWith('/') && f.command.trim() && f.user
  const save = () => run('project.set', { name: f.name, kind: f.kind, version: ['node', 'python', 'java'].includes(f.kind) ? f.version : '', dir: f.dir, command: f.command, install: f.install || undefined,
    user: f.user, port: Number(f.port) || undefined, env: Object.keys(env).length ? env : undefined }, { host, onDone: onClose })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={p ? t('proj.edit', { name: p.name }) : t('proj.add')} wide
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!ok} onClick={save}>{t('common.continue')}</Button></>}>
      <div className="space-y-3">
        <Segmented value={f.kind} onChange={setKind} options={(['node', 'python', 'go', 'java', 'custom'] as Kind[]).map((k) => ({ value: k, label: t('proj.k_' + k) }))} />
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('proj.name')}><Input value={f.name} disabled={!!p} onChange={(e) => setF({ ...f, name: e.target.value.toLowerCase() })} placeholder="my-api" /></Field>
          {['node', 'python', 'java'].includes(f.kind) && <Field label={t('proj.version')} hint={t('proj.version_hint')}><Input className="num" value={f.version} onChange={(e) => setF({ ...f, version: e.target.value })} /></Field>}
        </div>
        <Field label={t('proj.dir')}><Input className="num" value={f.dir} onChange={(e) => setF({ ...f, dir: e.target.value })} /></Field>
        <Field label={t('proj.command')} hint={t('proj.command_hint')}><Input className="num" value={f.command} onChange={(e) => setF({ ...f, command: e.target.value })} /></Field>
        <Field label={t('proj.install')} hint={t('proj.install_hint')}><Input className="num" value={f.install} placeholder={f.kind === 'node' ? 'npm ci' : f.kind === 'python' ? 'pip install -r requirements.txt' : ''} onChange={(e) => setF({ ...f, install: e.target.value })} /></Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('proj.user')} hint={t('proj.user_hint')}><Select value={f.user} onChange={(e) => setF({ ...f, user: e.target.value })}>{data.users.map((u) => <option key={u.name}>{u.name}</option>)}</Select></Field>
          <Field label={t('proj.port')} hint={t('proj.port_hint')}><Input type="number" className="num" value={f.port} onChange={(e) => setF({ ...f, port: e.target.value })} /></Field>
        </div>
        <Field label={t('proj.env')} hint={t('proj.env_hint')}><Textarea className="num min-h-20 text-[12px]" value={f.env} onChange={(e) => setF({ ...f, env: e.target.value })} placeholder={'NODE_ENV=production'} /></Field>
      </div>
    </Dialog>
  )
}

function ProjectLogs({ p, onClose }: { p: Project; onClose: () => void }) {
  const t = useT()
  const { host } = useHost()
  const d = useApi<{ text: string }>(`/api/v1/hosts/${host}/system/services/${encodeURIComponent(p.unit)}/logs?lines=400`, [p.unit])
  useInterval(() => void d.reload(), 4000)
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={p.name}>
      <pre className="num h-full overflow-auto whitespace-pre-wrap bg-term px-4 py-2 text-[11.5px] leading-relaxed text-term-fg">{d.data?.text || t('tasks.no_log')}</pre>
    </Drawer>
  )
}

interface RT { tool: string; version: string; bytes: number; used_by: string[] }

// Runtimes: the versions mise installed, their size and who uses them.
export function Runtimes({ manage }: { manage: boolean }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const r = useApi<{ items: RT[]; mise: boolean }>(`/api/v1/hosts/${host}/runtimes`, [host])
  useEvents(['change.done'], () => void r.reload())
  const [tool, setTool] = useState('node')
  const [ver, setVer] = useState('22')
  if (!r.data) return null
  const total = r.data.items.reduce((n, x) => n + x.bytes, 0)
  return (
    <Box title={t('rt.title')} count={r.data.items.length} right={total > 0 && <span className="num text-[12px] text-mute">{bytes(total)}</span>}>
      <p className="px-4 pt-2.5 text-[12.5px] text-mute">{t('rt.hint')}</p>
      {r.data.items.length === 0 && <p className="px-4 py-3 text-[12.5px] text-mute">{t('rt.empty')}</p>}
      {r.data.items.map((x) => (
        <Row key={x.tool + x.version}>
          <div className="min-w-0 flex-1 text-[13.5px]"><b>{t('proj.k_' + x.tool)}</b> <span className="num">{x.version}</span></div>
          <span className="min-w-0 flex-1 truncate text-[12px] text-mute">{x.used_by.length ? t('rt.used_by', { list: x.used_by.join(', ') }) : t('rt.unused')}</span>
          <span className="num w-20 text-right text-[12px] text-mute">{bytes(x.bytes)}</span>
          {manage && <Button size="sm" variant="ghost" disabled={x.used_by.length > 0} onClick={() => run('runtime.remove', { tool: x.tool, version: x.version }, { host })}>{t('action.runtime.remove')}</Button>}
        </Row>
      ))}
      {manage && <div className="flex flex-wrap items-center gap-2 border-t border-line px-4 py-2.5">
        <Select className="w-32" value={tool} onChange={(e) => { setTool(e.target.value); setVer({ node: '22', python: '3.12', java: '21' }[e.target.value] ?? '') }}>
          {['node', 'python', 'java'].map((k) => <option key={k} value={k}>{t('proj.k_' + k)}</option>)}
        </Select>
        <Input className="num w-28" value={ver} onChange={(e) => setVer(e.target.value.trim())} aria-label={t('proj.version')} />
        <Button size="sm" disabled={!/^[0-9][0-9a-z.-]{0,20}$|^lts$|^latest$/.test(ver)} onClick={() => run('runtime.install', { tool, version: ver }, { host })}>{t('rt.install')}</Button>
        <span className="text-[12px] text-mute">{t('rt.install_hint')}</span>
      </div>}
    </Box>
  )
}
