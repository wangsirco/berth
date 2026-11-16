import { CheckCircle2, CircleAlert, Globe, MoreHorizontal, Plus, Rocket, TriangleAlert } from 'lucide-react'
import { ObjectActivity } from '@/components/ai/Activity'
import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Textarea } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { cx } from '@/components/ui/cx'
import { get, post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { NewSiteDialog } from '../NewSiteDialog'
import { KINDS, LANG, langOf, type Kind } from './lang'

interface Source { type: 'dir' | 'git'; path?: string; url?: string; branch?: string }
export interface NativeSpec {
  name: string; kind: Kind; version: string; source: Source; install: string; build?: string; release?: string; start: string; health: string
  shared: string[]; mem_mb?: number; cpus?: number; build_mem_mb?: number; wait_secs?: number; workers?: { name: string; command: string }[]
}
export interface NativeItem extends NativeSpec { port?: number; current?: string; deployed?: number; env_keys: string[]; sites: number[]; active: boolean }
interface Finding { level: 'ok' | 'warn' | 'block'; code: string; params?: Record<string, string> }
interface Report { kind?: Kind; version?: string; release?: string; framework: string; package_manager: string; node: string; install: string; build: string; start: string; health: string; shared: string[]; findings: Finding[] }
interface Rel { id: string; at: number; info?: Record<string, string>; node?: string; kind?: string }
interface Status { exists: boolean; release: string; port: number; active: boolean; releases: Rel[]; ports: [number, number]; shared_mb: number; user: string; workers?: Record<string, string> }

export const BADGE: Record<Kind, [string, string]> = { node: ['N', 'bg-[#111]'], java: ['J', 'bg-[#b5502a]'], go: ['Go', 'bg-[#00758f]'], python: ['Py', 'bg-[#2f5f8a]'] }
// what each language usually needs: run memory, build memory (MB)
const MEM: Record<Kind, [number, number]> = { node: [512, 2048], java: [1024, 3072], go: [256, 2048], python: [512, 1024] }

const WORKER_PH: Record<Kind, string> = { node: 'node worker.js', java: 'java -cp app.jar com.example.Worker', go: './.berth/app worker', python: 'celery -A proj worker -l info' }

const relTime = (id: string) => id.replace(/^(\d{4})(\d\d)(\d\d)-(\d\d)(\d\d)(\d\d)$/, '$1-$2-$3 $4:$5')

// parseEnv reads KEY=VALUE lines (blank lines and # comments are skipped).
export function parseEnv(text: string): Record<string, string> | null {
  const out: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const l = raw.trim()
    if (!l || l.startsWith('#')) continue
    const i = l.indexOf('=')
    if (i < 1) return null
    const k = l.slice(0, i).trim()
    if (!/^[A-Z_][A-Z0-9_]{0,63}$/.test(k)) return null
    out[k] = l.slice(i + 1).trim().replace(/^"(.*)"$/, '$1')
  }
  return out
}

export function FindingList({ findings }: { findings: Finding[] }) {
  const t = useT()
  return (
    <div>
      {findings.map((f, i) => (
        <div key={i} className="flex items-start gap-2 border-t border-line py-1.5 text-[13px] first:border-t-0">
          {f.level === 'ok' ? <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-ok" /> : f.level === 'warn' ? <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn" /> : <CircleAlert className="mt-0.5 size-4 shrink-0 text-bad" />}
          <span>{t('compat.' + f.code, f.params ?? {})}</span>
        </div>
      ))}
    </div>
  )
}

// ---- list ----
export function NativeProjects() {
  const t = useT()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'sites.manage')
  const d = useApi<{ items: NativeItem[] }>(`/api/v1/hosts/${host}/native`, [host])
  useEvents(['change.done'], () => void d.reload())
  const [wizard, setWizard] = useState(false)
  const [open, setOpen] = useState<string | null>(null)
  const items = d.data?.items ?? []
  return (
    <>
      <Box title={t('native.title')} count={items.length} right={manage && <Button size="sm" variant="primary" onClick={() => setWizard(true)}><Plus className="size-3.5" />{t('native.add')}</Button>}>
        <p className="border-b border-line px-4 py-2 text-[12.5px] text-mute">{t('native.hint')}</p>
        {d.data && items.length === 0 && <Empty title={t('native.empty')}>{t('native.empty_hint')}</Empty>}
        {items.map((p) => (
          <Row key={p.name} onClick={() => setOpen(p.name)}>
            <Dot state={!p.current ? 'off' : p.active ? 'ok' : 'bad'} />
            <span className={cx('grid size-8 shrink-0 place-items-center rounded-lg text-[12px] font-bold text-white', BADGE[p.kind || 'node'][1])}>{BADGE[p.kind || 'node'][0]}</span>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 text-[13.5px]"><b>{p.name}</b><Pill tone="acc">{t('home.tag_native')}</Pill><Pill>{langOf(p.kind)} {p.version}</Pill></div>
              <div className="num truncate text-[12px] text-mute">{p.source.type === 'git' ? p.source.url + ' · ' + (p.source.branch || 'main') : p.source.path}</div>
            </div>
            <span className="text-[12px] text-mute max-sm:hidden">{p.current ? t('native.deployed_ago', { ago: ago(p.deployed ?? 0, t) }) : t('native.not_deployed')}</span>
            {(p.sites ?? []).length > 0 && <Pill><Globe className="mr-1 size-3" />{p.sites.length}</Pill>}
          </Row>
        ))}
      </Box>
      {wizard && <NativeWizard onClose={() => setWizard(false)} onOpen={(n) => { setWizard(false); setOpen(n) }} />}
      {open && <NativeDetail name={open} onClose={() => setOpen(null)} />}
    </>
  )
}

// ---- new-project wizard: source → check → settings → deploy ----
export function NativeWizard({ onClose, onOpen, kind: want }: { onClose: () => void; onOpen: (name: string) => void; kind?: Kind }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [step, setStep] = useState<1 | 2>(1)
  const [name, setName] = useState('')
  const [src, setSrc] = useState<Source>({ type: 'dir', path: '', branch: 'main' })
  const [checking, setChecking] = useState(false)
  const [err, setErr] = useState('')
  const [rep, setRep] = useState<Report | null>(null)
  const [spec, setSpec] = useState<NativeSpec | null>(null)
  const [envText, setEnvText] = useState('')
  const nameOK = /^[a-z][a-z0-9-]{0,25}$/.test(name)
  const srcOK = src.type === 'dir' ? (src.path ?? '').startsWith('/') : /^(https:\/\/|git@)/.test(src.url ?? '')
  // kind '' lets the server tell the language from the project's files
  const check = async (kind: Kind | '' = '') => {
    setChecking(true); setErr('')
    try {
      const r = await post<Report>(`/api/v1/hosts/${host}/native/check`, { spec: { name, kind, version: 'lts', source: src, start: '-', install: '', health: '/', shared: [] } })
      const k: Kind = r.kind || kind || 'node'
      const version = k === 'node' ? (r.node || '24').split('.')[0] : (r.version || '')
      const next = r.framework === 'next'
      setRep(r)
      setSpec({ name, kind: k, version, source: src, install: r.install ?? '', build: r.build ?? '', release: r.release ?? '', start: r.start ?? '', health: r.health || '/', shared: r.shared ?? [], mem_mb: next ? 1024 : MEM[k][0], build_mem_mb: next ? 4096 : MEM[k][1] })
      setStep(2)
    } catch (e) { setErr((e as Error).message) } finally { setChecking(false) }
  }
  const env = parseEnv(envText)
  const blocked = rep?.findings.some((f) => f.level === 'block')
  const save = () => {
    if (!spec || !env) return
    run('native.set', { ...spec, env }, { host, onDone: () => run('native.deploy', { name: spec.name }, { host, onDone: () => onOpen(spec.name) }) })
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('native.add')} description={step === 1 ? t('native.wiz_hint1') : t('native.wiz_hint2')} wide
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        {step === 1 ? <Button variant="primary" disabled={!nameOK || !srcOK || checking} onClick={() => void check(want ?? '')}>{checking ? t('native.checking') : t('native.check')}</Button>
          : <Button variant="primary" disabled={!env || !!blocked || !spec?.start} onClick={save}><Rocket className="size-4" />{t('native.save_deploy')}</Button>}</>}>
      {step === 1 && <div className="pb-2">
        <Field label={t('native.name')} hint={t('native.name_hint')}><Input className="num" value={name} onChange={(e) => setName(e.target.value.toLowerCase())} aria-label={t('native.name')} /></Field>
        <div className="px-4 pb-2"><Segmented value={src.type} onChange={(v) => setSrc({ ...src, type: v })} options={[{ value: 'dir', label: t('native.src_dir') }, { value: 'git', label: t('native.src_git') }]} /></div>
        {src.type === 'dir'
          ? <Field label={t('native.src_path')} hint={t('native.src_path_hint')}><Input className="num" value={src.path} placeholder="/srv/code/my-site" onChange={(e) => setSrc({ ...src, path: e.target.value.trim() })} aria-label={t('native.src_path')} /></Field>
          : <>
            <Field label={t('native.src_url')} hint={t('native.src_url_hint')}><Input className="num" value={src.url ?? ''} placeholder="https://github.com/me/site.git" onChange={(e) => setSrc({ ...src, url: e.target.value.trim() })} aria-label={t('native.src_url')} /></Field>
            <Field label={t('native.branch')}><Input className="num w-48" value={src.branch ?? 'main'} onChange={(e) => setSrc({ ...src, branch: e.target.value.trim() })} /></Field>
          </>}
        {err && <div className="px-4"><Notice tone="bad">{err}</Notice></div>}
      </div>}
      {step === 2 && rep && spec && <div className="space-y-3 px-4 pb-3">
        <div className="rounded-xl border border-line bg-canvas px-3 py-2">
          <div className="mb-1.5 flex flex-wrap items-center gap-2">
            <span className="text-[13px] font-semibold">{spec.kind === 'node'
              ? t('native.found', { fw: rep.framework ? t('native.fw_' + rep.framework) : t('native.fw_none'), pm: rep.package_manager })
              : t('native.found_lang', { lang: LANG[spec.kind], fw: rep.framework ? t('native.fw_' + rep.framework) : t('native.fw_none') })}</span>
            <div className="ml-auto"><Segmented value={spec.kind} onChange={(v) => void check(v)} options={KINDS.map((k) => ({ value: k, label: LANG[k] }))} /></div>
          </div>
          <FindingList findings={rep.findings} />
        </div>
        <SpecForm spec={spec} onChange={setSpec} />
        <Field label={t('native.env')} hint={t('native.env_hint')}><Textarea className="num h-24 text-[12px]" value={envText} onChange={(e) => setEnvText(e.target.value)} placeholder={'DATABASE_URL=file:./data/app.db\nSECRET_KEY=…'} /></Field>
        {!env && <p className="text-[12px] text-bad">{t('native.env_bad')}</p>}
      </div>}
    </Dialog>
  )
}

function SpecForm({ spec, onChange }: { spec: NativeSpec; onChange: (s: NativeSpec) => void }) {
  const t = useT()
  const set = (k: keyof NativeSpec) => (e: React.ChangeEvent<HTMLInputElement>) => onChange({ ...spec, [k]: e.target.value })
  const num = (k: keyof NativeSpec) => (e: React.ChangeEvent<HTMLInputElement>) => onChange({ ...spec, [k]: Number(e.target.value) || 0 })
  const row = (label: string, k: keyof NativeSpec, ph = '') => (
    <label className="grid grid-cols-[110px_1fr] items-center gap-2 text-[13px]"><span className="text-mute">{label}</span>
      <Input className="num h-8" value={String(spec[k] ?? '')} placeholder={ph} onChange={set(k)} aria-label={label} /></label>
  )
  return (
    <div className="space-y-1.5">
      {row(t('native.version', { lang: langOf(spec.kind) }), 'version')}
      {row(t('native.install'), 'install')}
      {row(t('native.build'), 'build')}
      {row(t('native.release'), 'release', t('native.release_ph'))}
      {row(t('native.start'), 'start')}
      {row(t('native.health'), 'health')}
      <label className="grid grid-cols-[110px_1fr] items-center gap-2 text-[13px]"><span className="text-mute">{t('native.shared')}</span>
        <Input className="num h-8" value={(spec.shared ?? []).join(', ')} onChange={(e) => onChange({ ...spec, shared: e.target.value.split(',').map((x) => x.trim()).filter(Boolean) })} aria-label={t('native.shared')} /></label>
      <div className="grid grid-cols-[110px_1fr] gap-2 text-[13px]"><span className="pt-1.5 text-mute">{t('native.workers')}</span>
        <div className="space-y-1.5">
          {(spec.workers ?? []).map((w, i) => (
            <div key={i} className="flex gap-1.5">
              <Input className="num h-8 w-28" value={w.name} placeholder="worker" aria-label={t('native.worker_name')} onChange={(e) => onChange({ ...spec, workers: spec.workers!.map((x, j) => (j === i ? { ...x, name: e.target.value.toLowerCase() } : x)) })} />
              <Input className="num h-8 flex-1" value={w.command} placeholder={WORKER_PH[spec.kind]} aria-label={t('native.worker_cmd')} onChange={(e) => onChange({ ...spec, workers: spec.workers!.map((x, j) => (j === i ? { ...x, command: e.target.value } : x)) })} />
              <Button size="sm" variant="ghost" onClick={() => onChange({ ...spec, workers: spec.workers!.filter((_, j) => j !== i) })}>{t('common.delete')}</Button>
            </div>
          ))}
          <Button size="sm" variant="ghost" onClick={() => onChange({ ...spec, workers: [...(spec.workers ?? []), { name: '', command: '' }] })}><Plus className="size-3.5" />{t('native.add_worker')}</Button>
          <p className="text-[12px] text-mute">{t('native.workers_hint')}</p>
        </div>
      </div>
      <div className="grid grid-cols-3 gap-2 pt-1 text-[12.5px]">
        <label><span className="text-mute">{t('native.mem')}</span><Input type="number" className="num h-8" value={spec.mem_mb ?? 0} onChange={num('mem_mb')} /></label>
        <label><span className="text-mute">{t('native.cpus')}</span><Input type="number" step={0.5} className="num h-8" value={spec.cpus ?? 0} onChange={num('cpus')} /></label>
        <label><span className="text-mute">{t('native.build_mem')}</span><Input type="number" className="num h-8" value={spec.build_mem_mb ?? 0} onChange={num('build_mem_mb')} /></label>
      </div>
    </div>
  )
}

// ---- detail: overview, releases, logs, environment, settings ----
type Tab = 'overview' | 'releases' | 'logs' | 'env' | 'settings'

export function NativeDetail({ name, onClose }: { name: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'sites.manage')
  const d = useApi<{ project: NativeItem; status: Status; env_keys: string[]; sites: number[] }>(`/api/v1/hosts/${host}/native/${name}`, [name])
  useEvents(['change.done'], () => void d.reload())
  const [tab, setTab] = useState<Tab>('overview')
  const [site, setSite] = useState(false)
  const [spec, setSpec] = useState<NativeSpec | null>(null)
  const [envText, setEnvText] = useState('')
  const p = d.data?.project
  const st = d.data?.status
  const deploy = () => run('native.deploy', { name }, { host })
  const kv = (k: string, v: React.ReactNode) => <><dt className="text-mute">{k}</dt><dd className="m-0 min-w-0 break-all">{v}</dd></>
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={name} className="sm:w-[min(900px,95vw)]"
      right={manage && p && <div className="flex gap-1.5">
        {d.data && d.data.sites.length === 0 && <Button size="sm" onClick={() => setSite(true)}><Globe className="size-3.5" />{t('native.bind')}</Button>}
        <Button size="sm" variant="primary" onClick={deploy}><Rocket className="size-3.5" />{t('action.native.deploy')}</Button>
        <Menu items={[{ label: t('action.native.delete'), danger: true, onClick: () => run('native.delete', { name }, { host, onDone: onClose }) }]}
          trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} />
      </div>}>
      <ObjectActivity type="project" id={name} className="mx-4 mt-3" />
      {!p || !st ? <p className="p-4 text-mute">{d.error?.message ?? t('common.loading')}</p> : <>
        <div className="border-b border-line px-4 py-2">
          <Segmented<Tab> value={tab} onChange={setTab} options={(['overview', 'releases', 'logs', 'env', 'settings'] as Tab[]).map((x) => ({ value: x, label: t('native.tab_' + x) }))} />
        </div>
        {tab === 'overview' && <div className="space-y-4 p-4">
          <div className="flex items-center gap-2 text-[13.5px]">
            <Dot state={!st.release ? 'off' : st.active ? 'ok' : 'bad'} />
            <b>{!st.release ? t('native.not_deployed') : st.active ? t('home.running') : t('native.down')}</b>
            {st.release && <span className="text-mute">{t('native.release_at', { r: relTime(st.release), port: st.port })}</span>}
          </div>
          <dl className="grid grid-cols-[110px_1fr] gap-x-4 gap-y-1.5 text-[13px]">
            {kv(t('native.source'), <code className="num">{p.source.type === 'git' ? `${p.source.url} (${p.source.branch || 'main'})` : p.source.path}</code>)}
            {kv(t('native.version', { lang: langOf(p.kind) }), langOf(p.kind) + ' ' + p.version)}
            {kv(t('native.start'), <code className="num">{p.start}</code>)}
            {kv(t('native.user'), <><code className="num">{st.user}</code> · {t('native.sandbox')}</>)}
            {kv(t('native.dir'), <code className="num">/srv/berth/projects/{name}</code>)}
            {kv(t('native.shared'), <>{(p.shared ?? []).map((s) => <code key={s} className="num mr-1">{s}</code>)} · {st.shared_mb} MB</>)}
            {kv(t('native.limits'), [p.mem_mb ? `${p.mem_mb} MB` : t('native.no_limit'), p.cpus ? t('home.cores', { n: p.cpus }) : ''].filter(Boolean).join(' · '))}
            {kv(t('native.sites'), d.data!.sites.length ? t('native.sites_n', { n: d.data!.sites.length }) : t('native.no_sites'))}
            {(p.workers ?? []).length > 0 && kv(t('native.workers'), <div className="space-y-1">{p.workers!.map((w) => (
              <div key={w.name} className="flex items-center gap-2"><Dot state={st.workers?.[w.name] === 'active' ? 'ok' : st.workers?.[w.name] ? 'bad' : 'off'} /><b className="num">{w.name}</b><code className="num truncate text-mute">{w.command}</code></div>))}</div>)}
          </dl>
          {p.source.type === 'git' && manage && <PushHook name={name} />}
        </div>}
        {tab === 'releases' && <div>
          {st.releases.length === 0 && <Empty title={t('native.no_releases')} />}
          {st.releases.map((r) => (
            <Row key={r.id}>
              <Dot state={r.id === st.release ? 'ok' : 'off'} />
              <div className="min-w-0 flex-1">
                <div className="num text-[13px] font-semibold">{relTime(r.id)}{r.id === st.release && <Pill tone="ok" className="ml-2">{t('native.live')}</Pill>}</div>
                <div className="truncate text-[12px] text-mute">{r.info?.commit ? `${r.info.commit} ${r.info.message ?? ''}` : r.info?.path ?? ''}{r.node ? ' · ' + langOf(r.kind) + ' ' + r.node : ''}</div>
              </div>
              {manage && r.id !== st.release && <Button size="sm" onClick={() => run('native.activate', { name, release: r.id }, { host })}>{t('action.native.activate')}</Button>}
            </Row>
          ))}
          <p className="px-4 py-2 text-[12px] text-mute">{t('native.releases_hint')}</p>
        </div>}
        {tab === 'logs' && st.port > 0 && <Logs unit={`berth-proj-${name}@${st.port}.service`} />}
        {tab === 'logs' && !st.port && <p className="p-4 text-mute">{t('native.not_deployed')}</p>}
        {tab === 'env' && <div className="space-y-3 p-4">
          <div className="flex flex-wrap gap-1.5">{d.data!.env_keys.length ? d.data!.env_keys.map((k) => <code key={k} className="num rounded bg-soft px-2 py-0.5 text-[12px]">{k}</code>) : <span className="text-[13px] text-mute">{t('native.env_none')}</span>}</div>
          <p className="text-[12.5px] text-mute">{t('native.env_edit_hint')}</p>
          <Textarea className="num h-28 text-[12px]" value={envText} onChange={(e) => setEnvText(e.target.value)} placeholder={'NEW_KEY=value\nOLD_KEY='} />
          {manage && <Button disabled={!parseEnv(envText) || !envText.trim()} onClick={() => run('native.set', { ...p, env: parseEnv(envText), env_merge: true }, { host, onDone: () => setEnvText('') })}>{t('native.env_save')}</Button>}
        </div>}
        {tab === 'settings' && <div className="space-y-3 p-4">
          <SpecForm spec={spec ?? p} onChange={setSpec} />
          {manage && <Button variant="primary" disabled={!spec} onClick={() => spec && run('native.set', spec, { host, onDone: () => setSpec(null) })}>{t('action.native.set')}</Button>}
          <p className="text-[12px] text-mute">{t('native.settings_hint')}</p>
        </div>}
      </>}
      {site && <NewSiteDialog preset={{ type: 'project', project: name }} onClose={() => setSite(false)} onDone={() => void d.reload()} />}
    </Drawer>
  )
}

function Logs({ unit }: { unit: string }) {
  const t = useT()
  const { host } = useHost()
  const d = useApi<{ text: string }>(`/api/v1/hosts/${host}/system/services/${encodeURIComponent(unit)}/logs?lines=400`, [unit])
  useInterval(() => void d.reload(), 4000)
  return <pre className={cx('num h-[60vh] overflow-auto whitespace-pre-wrap bg-term px-4 py-2 text-[11.5px] leading-relaxed text-term-fg')}>{d.data?.text || t('tasks.no_log')}</pre>
}

// PushHook: the webhook that deploys the project when its branch is pushed.
function PushHook({ name }: { name: string }) {
  const t = useT()
  const { host } = useHost()
  const [h, setH] = useState<{ url: string; secret: string; branch: string } | null>(null)
  const [err, setErr] = useState('')
  return (
    <div className="rounded-xl border border-line bg-canvas px-3.5 py-3 text-[13px]">
      <div className="flex items-center gap-2"><b>{t('native.hook')}</b>
        {!h && <Button size="sm" className="ml-auto" onClick={() => get<{ url: string; secret: string; branch: string }>(`/api/v1/hosts/${host}/native/${name}/webhook`).then(setH).catch((e) => setErr(e.message))}>{t('native.hook_show')}</Button>}</div>
      <p className="mt-1 text-[12.5px] text-mute">{t('native.hook_hint')}</p>
      {err && <Notice tone="bad">{err}</Notice>}
      {h && <dl className="mt-2 grid grid-cols-[110px_1fr] gap-x-3 gap-y-1">
        <dt className="text-mute">{t('native.hook_url')}</dt><dd className="num m-0 select-all break-all">{h.url}</dd>
        <dt className="text-mute">Secret</dt><dd className="num m-0 select-all break-all">{h.secret}</dd>
        <dt className="text-mute">Content type</dt><dd className="num m-0">application/json</dd>
        <dt className="text-mute">{t('native.branch')}</dt><dd className="num m-0">{h.branch}</dd>
      </dl>}
    </div>
  )
}
