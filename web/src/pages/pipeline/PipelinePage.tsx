import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { CodeEditor } from '@/components/code/CodeEditor'
import { hostPath, useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented, Select } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago, duration } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { RunLog } from './RunLog'
import { TabBar } from '@/components/ui/Tabs'

interface Step { name: string; status: 'ok' | 'failed' | 'skipped'; ms: number }
interface Run { id: number; number: number; status: string; trigger: string; actor: string; ref?: string; commit?: string; version: string; image?: string; steps: Step[]; error?: string; change_id?: string; started_at: number; finished_at?: number }
interface Pipeline { id: number; name: string; host: string; source: { type: string; url?: string; ref?: string }; spec: string; cron?: string; has_token: boolean; last_run?: Run }
interface Release { id: number; version: string; host: string; service: string; image: string; old_image: string; status: string; actor: string; error?: string; started_at: number; ms: number }

const runDot = (s: string) => (s === 'done' ? 'ok' : s === 'running' ? 'busy' : s === 'failed' ? 'bad' : 'off') as 'ok' | 'busy' | 'bad' | 'off'

const TEMPLATE = `name: my-app
runner: { memory: 2g, cpus: 2 }
steps:
  - name: test
    image: node:24-bookworm
    cache: [node_modules]
    run: npm ci && npm test
  - name: image
    docker_build: { context: ., platforms: [linux/arm64], push: registry.berth.lan/my-app/web }
deploy:
  - host: local
    compose: /srv/berth/stacks/my-app/compose.yaml
    service: web
    healthcheck: { url: http://127.0.0.1:3000/, timeout: 120s }
    rollback: auto
`

export default function PipelinePage() {
  const t = useT()
  const nav = useNavigate()
  const { user } = useAuth()
  const { host } = useHost()
  const rest = (useParams()['*'] ?? '').split('/')
  const tab = rest[0] === 'p' ? '' : rest[0]
  const open = rest[0] === 'p' ? decodeURIComponent(rest[1] ?? '') : ''
  const list = useApi<{ items: Pipeline[] }>('/api/v1/pipelines')
  const [edit, setEdit] = useState<Pipeline | 'new' | null>(null)
  useEvents(['pipeline.run', 'change.done'], () => void list.reload())
  const manage = can(user, 'pipeline.manage')
  return (
    <div className="space-y-3">
      <TabBar label={t('nav.pipeline')} value={rest[0] === 'p' ? '' : tab} items={['', 'registry'].map((x) => ({ value: x, label: t('pipeline.tab_' + (x || 'pipelines')), to: hostPath(host, 'pipeline' + (x ? '/' + x : '')) }))}
        right={tab === '' && manage && <Button size="sm" variant="primary" onClick={() => setEdit('new')}>＋ {t('action.pipeline.create')}</Button>} />
      {tab === '' && <Box title={t('nav.pipeline')} count={list.data?.items.length}>
        {list.error && <Notice tone="bad">{list.error.message}</Notice>}
        {list.data?.items.length === 0 && <Empty title={t('pipeline.none')}>{t('pipeline.none_hint')}</Empty>}
        {list.data?.items.map((p) => (
          <Row key={p.id} onClick={() => nav(hostPath(host, 'pipeline/p/' + encodeURIComponent(p.name)))}>
            <Dot state={p.last_run ? runDot(p.last_run.status) : 'off'} />
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13.5px] font-semibold">{p.name}</div>
              <div className="truncate text-[12px] text-mute">{p.source.type === 'git' ? `${p.source.url} · ${p.source.ref || 'HEAD'}` : t('pipeline.src_upload')} · {p.host}{p.cron ? ' · ⏱ ' + p.cron : ''}</div>
            </div>
            {p.last_run && <span className="text-[12px] text-mute">#{p.last_run.number} · {t('pipeline.status_' + p.last_run.status)} · {ago(p.last_run.started_at, t)}</span>}
          </Row>
        ))}
      </Box>}
      {tab === 'registry' && <RegistryTab />}
      {open && <PipelineDrawer name={open} onClose={() => nav(hostPath(host, 'pipeline'))} onEdit={(p) => setEdit(p)} />}
      {edit && <PipelineDialog p={edit === 'new' ? undefined : edit} onClose={() => setEdit(null)} onDone={() => void list.reload()} />}
    </div>
  )
}

function PipelineDialog({ p, onClose, onDone }: { p?: Pipeline; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host, hosts } = useHost()
  const [name, setName] = useState(p?.name ?? '')
  const [runner, setRunner] = useState(p?.host ?? host)
  const [type, setType] = useState<'git' | 'upload'>((p?.source.type as 'git') ?? 'upload')
  const [url, setUrl] = useState(p?.source.url ?? '')
  const [ref, setRef] = useState(p?.source.ref ?? 'main')
  const [token, setToken] = useState('')
  const [specIn, setSpecIn] = useState<'panel' | 'repo'>(p && !p.spec ? 'repo' : 'panel')
  const [spec, setSpec] = useState(p?.spec || TEMPLATE)
  const [cron, setCron] = useState(p?.cron ?? '')
  const submit = () => {
    const body = { id: p?.id, name, host: runner, source: { type, url: type === 'git' ? url : '', ref: type === 'git' ? ref : '' }, token: token || undefined,
      spec: specIn === 'panel' ? spec : '', cron }
    run(p ? 'pipeline.update' : 'pipeline.create', body, { host: runner, onDone: () => { onDone(); onClose() } })
  }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={p ? t('pipeline.edit', { name: p.name }) : t('action.pipeline.create')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={submit} disabled={!name}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label={t('pipeline.name')} hint={t('pipeline.name_hint')}><Input value={name} onChange={(e) => { setName(e.target.value); if (!p) setSpec((s) => s.replace(/^name: .*$/m, 'name: ' + e.target.value)) }} disabled={!!p} /></Field>
          <Field label={t('pipeline.runner')} hint={t('pipeline.runner_hint')}><Select value={runner} onChange={(e) => setRunner(e.target.value)}>{(hosts.length ? hosts.map((h) => h.name) : [host]).map((h) => <option key={h}>{h}</option>)}</Select></Field>
        </div>
        <div>
          <div className="mb-1 text-[12.5px] font-semibold">{t('pipeline.source')}</div>
          <Segmented value={type} onChange={setType} options={[{ value: 'upload', label: t('pipeline.src_upload') }, { value: 'git', label: 'Git' }]} />
          {type === 'upload' ? <p className="mt-1 text-[12.5px] text-mute">{t('pipeline.upload_hint', { name: name || '<name>' })}</p> : (
            <div className="mt-2 grid gap-2 sm:grid-cols-[1fr_140px]">
              <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://github.com/me/app.git" aria-label={t('pipeline.git_url')} />
              <Input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="main" aria-label={t('pipeline.git_ref')} />
              <Input className="sm:col-span-2" type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder={p?.has_token ? t('pipeline.token_kept') : t('pipeline.token')} aria-label={t('pipeline.token')} autoComplete="off" />
            </div>
          )}
        </div>
        <div>
          <div className="mb-1 flex items-center gap-2 text-[12.5px] font-semibold">{t('pipeline.definition')}
            <Segmented value={specIn} onChange={setSpecIn} options={[{ value: 'panel', label: t('pipeline.spec_panel') }, { value: 'repo', label: t('pipeline.spec_repo') }]} /></div>
          {specIn === 'panel' ? <div className="overflow-hidden rounded-[var(--radius-box)] border border-line"><CodeEditor value={spec} onChange={setSpec} language="yaml" height="320px" /></div>
            : <p className="text-[12.5px] text-mute">{t('pipeline.spec_repo_hint')}</p>}
        </div>
        <Field label={t('pipeline.cron')} hint={t('pipeline.cron_hint')}><Input value={cron} onChange={(e) => setCron(e.target.value)} placeholder="@daily" /></Field>
      </div>
    </Dialog>
  )
}

function PipelineDrawer({ name, onClose, onEdit }: { name: string; onClose: () => void; onEdit: (p: Pipeline) => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const d = useApi<{ pipeline: Pipeline; runs: Run[] }>(`/api/v1/pipelines/${encodeURIComponent(name)}`, [name])
  const rel = useApi<{ items: Release[] }>(`/api/v1/pipelines/${encodeURIComponent(name)}/releases`, [name])
  const [tab, setTab] = useState<'runs' | 'releases' | 'trigger'>('runs')
  const [sel, setSel] = useState<number | null>(null)
  const [hook, setHook] = useState<{ url: string; secret: string } | null>(null)
  useEvents(['pipeline.run', 'release'], () => { void d.reload(); void rel.reload() })
  const p = d.data?.pipeline
  const runs = d.data?.runs ?? []
  const cur = runs.find((r) => r.id === sel) ?? runs[0]
  const now = Date.now() / 1000
  const manage = can(user, 'pipeline.manage')
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={name}
      right={p && <div className="flex gap-1.5">
        <Button size="sm" variant="primary" onClick={() => run('pipeline.run', { name }, { host: p.host })}>{t('action.pipeline.run')}</Button>
        {manage && <Button size="sm" onClick={() => onEdit(p)}>{t('sites.edit')}</Button>}
        {manage && <Button size="sm" variant="danger" onClick={() => run('pipeline.delete', { id: p.id }, { host: p.host, onDone: onClose })}>{t('common.delete')}</Button>}
      </div>}>
      <div className="border-b border-line px-4 py-2">
        <Segmented value={tab} onChange={setTab} options={[{ value: 'runs', label: t('pipeline.runs') }, { value: 'releases', label: t('pipeline.releases') }, { value: 'trigger', label: t('pipeline.triggers') }]} />
      </div>
      {tab === 'runs' && <div className="space-y-3 p-4">
        {runs.length === 0 && <Empty title={t('pipeline.no_runs')} />}
        <div className="space-y-0.5">
          {runs.slice(0, 15).map((r) => (
            <button key={r.id} onClick={() => setSel(r.id)} className={cx('flex w-full items-center gap-2 rounded px-2 py-1 text-left text-[13px]', cur?.id === r.id ? 'bg-tint' : 'hover:bg-soft')}>
              <Dot state={runDot(r.status)} />
              <span className="num w-10">#{r.number}</span>
              <span className="min-w-0 flex-1 truncate text-mute">{t('pipeline.trigger_' + r.trigger)} · {r.actor}{r.commit ? ' · ' + r.commit.slice(0, 8) : ''}</span>
              <span className="flex gap-[2px]">{r.steps.map((s) => <i key={s.name} title={s.name} className={cx('h-1.5 w-4 rounded-[1px]', s.status === 'ok' ? 'bg-acc' : s.status === 'failed' ? 'bg-bad' : 'bg-line')} />)}</span>
              <span className="num w-16 text-right text-[12px] text-mute">{duration((r.finished_at || now) - r.started_at, t)}</span>
            </button>
          ))}
        </div>
        {cur && <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
            <b>#{cur.number}</b><Pill tone={cur.status === 'done' ? 'ok' : cur.status === 'failed' ? 'bad' : undefined}>{t('pipeline.status_' + cur.status)}</Pill>
            {cur.image && <span className="num text-mute">{cur.image}</span>}
            {cur.status === 'running' && <Button size="sm" onClick={() => run('pipeline.cancel', { run_id: cur.id }, { host: p?.host })}>{t('action.pipeline.cancel')}</Button>}
          </div>
          {cur.error && <Notice tone="bad">{cur.error}</Notice>}
          {cur.change_id && <RunLog changeId={cur.change_id} />}
        </div>}
      </div>}
      {tab === 'releases' && <div className="p-4">
        {rel.data?.items.length === 0 && <Empty title={t('pipeline.no_releases')}>{t('pipeline.no_releases_hint')}</Empty>}
        {rel.data?.items.map((r, i) => (
          <div key={r.id} className="flex items-center gap-2 border-b border-line py-1.5 text-[13px]">
            <Dot state={r.status === 'done' ? 'ok' : r.status === 'rolled_back' ? 'warn' : r.status === 'running' ? 'busy' : 'bad'} />
            <b className="num w-12">{r.version}</b>
            <div className="min-w-0 flex-1">
              <div className="truncate">{r.service} @ {r.host} · {t('pipeline.rel_' + r.status)}</div>
              <div className="truncate text-[12px] text-mute">{r.actor} · {ago(r.started_at, t)} · {(r.ms / 1000).toFixed(0)} s{r.error ? ' · ' + r.error : ''}</div>
            </div>
            {i > 0 && r.status === 'done' && <Button size="sm" onClick={() => run('release.deploy', { pipeline: String(d.data?.pipeline.id), release_id: r.id }, { host: r.host })}>{t('pipeline.redeploy')}</Button>}
          </div>
        ))}
      </div>}
      {tab === 'trigger' && p && <div className="space-y-3 p-4 text-[13px]">
        <div><h4 className="mb-1 font-semibold">berth push</h4>
          <pre className="num rounded-[var(--radius-box)] bg-soft px-3 py-2 text-[12px]">cd ~/code/{p.name}{'\n'}berth push --pipeline {p.name}</pre>
          <p className="mt-1 text-[12px] text-mute">{t('pipeline.push_hint')}</p></div>
        {p.source.type === 'git' && <div><h4 className="mb-1 font-semibold">Webhook</h4>
          {hook ? <div className="num space-y-1 text-[12px]"><div>URL: {hook.url}</div><div>Secret: {hook.secret}</div><div className="font-sans text-mute">{t('pipeline.webhook_hint')}</div></div>
            : manage && <Button size="sm" onClick={() => get<{ url: string; secret: string }>(`/api/v1/pipelines/${p.id}/webhook`).then(setHook)}>{t('pipeline.show_webhook')}</Button>}</div>}
        <div><h4 className="mb-1 font-semibold">{t('pipeline.cron')}</h4><p className="text-mute">{p.cron || t('pipeline.no_cron')}</p></div>
        <div><h4 className="mb-1 font-semibold">MCP / API</h4><p className="num text-[12px] text-mute">POST /api/v1/hosts/{p.host}/changes/pipeline.run {'{"name":"' + p.name + '"}'}</p></div>
      </div>}
    </Drawer>
  )
}

function RegistryTab() {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const r = useApi<{ installed: boolean; registry: { host: string; addr: string }; tags?: { repo: string; tag: string; digest: string; pushed_at?: number }[] }>('/api/v1/registry?tags=1')
  const manage = can(user, 'pipeline.manage')
  const reload = () => void r.reload()
  if (r.error && r.error.message) return <Notice tone="bad">{r.error.message}</Notice>
  if (r.data && !r.data.installed) return (
    <Empty title={t('registry.none')} action={manage && <Button variant="primary" onClick={() => run('registry.install', {}, { host, onDone: reload })}>{t('action.registry.install')}</Button>}>{t('registry.none_hint')}</Empty>
  )
  const repos = new Map<string, { tag: string; digest: string; pushed_at?: number }[]>()
  r.data?.tags?.forEach((x) => repos.set(x.repo, [...(repos.get(x.repo) ?? []), x]))
  return (
    <div className="space-y-3">
      {r.data && <Notice>{t('registry.at', { addr: r.data.registry.addr, host: r.data.registry.host })}</Notice>}
      <Box title={t('registry.repos')} count={repos.size} right={manage && <div className="flex gap-1.5">
        <Button size="sm" onClick={() => run('registry.prune', { keep: 10 }, { host: r.data?.registry.host, onDone: reload })}>{t('action.registry.prune')}</Button>
        <Button size="sm" variant="ghost" onClick={() => run('registry.remove', {}, { host: r.data?.registry.host, onDone: reload })}>{t('action.registry.remove')}</Button></div>}>
        {repos.size === 0 && <Empty title={t('registry.empty')}>{t('registry.empty_hint', { addr: r.data?.registry.addr ?? '' })}</Empty>}
        {[...repos.entries()].map(([repo, tags]) => (
          <Row key={repo}>
            <span className="num min-w-0 flex-1 truncate text-[13px] font-semibold">{repo}</span>
            <span className="flex flex-wrap justify-end gap-1">{tags.sort((a, b) => (b.pushed_at ?? 0) - (a.pushed_at ?? 0)).slice(0, 8).map((x) => <Pill key={x.tag} className="num">{x.tag}</Pill>)}</span>
          </Row>
        ))}
      </Box>
      <p className="text-[12px] text-mute">{t('registry.note')}</p>
    </div>
  )
}

