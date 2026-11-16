import { BookOpen, Download, ExternalLink, Link2, MoreHorizontal, RefreshCw, Store, Upload } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { TabBar } from '@/components/ui/Tabs'
import { Uploader, type Uploaded } from '@/components/ui/Uploader'
import { errText, post } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { BuiltinExtensions } from '../extensions/Builtin'
import { LegacyInstall } from '../extensions/ExtensionsPage'
import { InstallWizard, PluginIcon, scopeText, tx, type Inspect, type PManifest } from './InstallWizard'

interface Installed {
  id: string; name: Record<string, string>; version: string; description?: Record<string, string>; permissions: string[]; scopes: string[]
  installed_by: string; installed_at: number; running: boolean; kind: 'container' | 'native' | 'extension'; plugin?: PManifest; fingerprint?: string; has_page: boolean
}
interface MarketItem { id: string; name: Record<string, string>; description: Record<string, string>; author: string; version: string; icon?: string; runtime: string; run_as?: string; homepage?: string; signer?: string; tags?: string[] }

const TABS = ['', 'market', 'official'] as const

// 插件: what is installed, the market, and Berth's own optional plugins.
export default function PluginsPage() {
  const t = useT()
  const { host } = useHost()
  const sub = (useParams()['*'] ?? '').split('/')[0]
  const tab = (TABS as readonly string[]).includes(sub) ? sub : ''
  const [wizard, setWizard] = useState<Inspect | null>(null)
  const [manual, setManual] = useState(false)
  const [after, setAfter] = useState<{ ports?: number[]; id: string } | null>(null)
  const { user } = useAuth()
  const manage = user?.role === 'super'
  return (
    <div className="space-y-4">
      <TabBar label={t('nav.plugins')} value={tab} items={TABS.map((x) => ({ value: x, label: t('plugin.tab_' + (x || 'installed')), to: hostPath(host, 'plugins' + (x ? '/' + x : '')) }))}
        right={manage && <div className="flex gap-1.5">
          <a href="https://github.com/wangsirco/berth-plugins/blob/main/docs/PLUGIN-GUIDE.md" target="_blank" rel="noreferrer"><Button size="sm" variant="ghost"><BookOpen className="size-3.5" />{t('plugin.dev_guide')}</Button></a>
          <Button size="sm" variant="primary" onClick={() => setManual(true)}><Upload className="size-3.5" />{t('plugin.manual')}</Button>
        </div>} />
      {after && <AfterInstall id={after.id} ports={after.ports} onClose={() => setAfter(null)} />}
      {tab === '' && <InstalledList onInspect={setWizard} />}
      {tab === 'market' && <Market onInspect={setWizard} />}
      {tab === 'official' && <BuiltinExtensions />}
      {manual && <ManualSource onClose={() => setManual(false)} onInspect={(i) => { setManual(false); setWizard(i) }} />}
      {wizard && <InstallWizard info={wizard} onClose={() => setWizard(null)} onDone={(r) => { setWizard(null); setAfter({ id: String(r.id ?? ''), ports: r.open_ports as number[] | undefined }) }} />}
    </div>
  )
}

function AfterInstall({ id, ports, onClose }: { id: string; ports?: number[]; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const nav = useNavigate()
  const { host } = useHost()
  return (
    <Notice>
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex-1">{t('plugin.installed_ok')}{ports?.length ? ' ' + t('plugin.open_ports_hint', { ports: ports.join(', ') }) : ''}</span>
        {ports?.length ? <Button size="sm" onClick={() => run('firewall.rule.set', { action: 'allow', proto: 'tcp', ports: ports.join(','), comment: 'plugin ' + id, enabled: true }, { host: 'local' })}>{t('plugin.open_ports')}</Button> : null}
        <Button size="sm" variant="primary" onClick={() => nav(hostPath(host, 'ext/' + id))}>{t('plugin.open')}</Button>
        <Button size="sm" variant="ghost" onClick={onClose}>{t('common.close')}</Button>
      </div>
    </Notice>
  )
}

function InstalledList({ onInspect }: { onInspect: (i: Inspect) => void }) {
  const t = useT()
  const run = useChange()
  const nav = useNavigate()
  const { host } = useHost()
  const { user } = useAuth()
  const [locale] = useLocale()
  const d = useApi<{ items: Installed[] }>('/api/v1/plugins')
  const mk = useApi<{ items: MarketItem[]; installed: Record<string, string> }>(user?.role === 'super' ? '/api/v1/plugins/market' : null)
  useEvents(['change.done'], () => void d.reload())
  const manage = user?.role === 'super'
  const newer = (id: string, v: string) => { const m = mk.data?.items.find((x) => x.id === id); return m && m.version !== v ? m.version : '' }
  const update = async (id: string) => { try { onInspect(await post<Inspect>('/api/v1/plugins/inspect', { market: id })) } catch (e) { alert(errText(e)) } }
  return (
    <Box title={t('plugin.installed')} count={d.data?.items.length}>
      {d.data?.items.length === 0 && <Empty title={t('plugin.none')} action={<Link to={hostPath(host, 'plugins/market')}><Button size="sm" variant="primary"><Store className="size-3.5" />{t('plugin.go_market')}</Button></Link>}>{t('plugin.none_hint')}</Empty>}
      {d.data?.items.map((e) => {
        const up = newer(e.id, e.version)
        const root = e.plugin?.runtime.run_as === 'root'
        return (
          <Row key={e.id} onClick={e.has_page ? () => nav(hostPath(host, 'ext/' + e.id)) : undefined}>
            <Dot state={e.running ? 'ok' : 'bad'} />
            <PluginIcon size={34} />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-1.5 text-[13.5px]"><b>{tx(e.name, locale) || e.id}</b><span className="num text-mute">{e.version}</span>
                <Pill>{e.kind === 'native' ? (root ? t('plugin.rt_root') : t('plugin.rt_native')) : e.kind === 'container' ? t('plugin.rt_container') : t('plugin.rt_legacy')}</Pill>
                {e.fingerprint ? <Pill tone="ok">{t('plugin.signed_short')}</Pill> : e.kind !== 'extension' && <Pill tone="warn">{t('plugin.unsigned_short')}</Pill>}
                {up && <Pill tone="acc">{t('plugin.update_available', { v: up })}</Pill>}</div>
              <div className="truncate text-[12px] text-mute">{tx(e.description, locale)}</div>
              <div className="mt-1 flex flex-wrap gap-1">{(e.scopes ?? []).map((s) => <Pill key={s}>{scopeText(t, s)}</Pill>)}
                <span className="text-[11.5px] text-mute">{t('plugin.installed_by', { who: e.installed_by, ago: ago(e.installed_at, t) })}</span></div>
            </div>
            {e.has_page && <Button size="sm" onClick={(ev) => { ev.stopPropagation(); nav(hostPath(host, 'ext/' + e.id)) }}><ExternalLink className="size-3.5" />{t('plugin.open')}</Button>}
            {manage && up && <Button size="sm" variant="primary" onClick={(ev) => { ev.stopPropagation(); void update(e.id) }}>{t('plugin.update')}</Button>}
            {manage && <span onClick={(ev) => ev.stopPropagation()}><Menu trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} items={[
              ...(e.kind === 'native' ? [{ label: t('system.svc_restart'), onClick: () => run('system.service', { name: 'berth-plugin-' + e.id, action: 'restart' }, { host: 'local' }) }] : []),
              ...(e.kind === 'native' ? [{ label: t('plugin.remove_keep'), onClick: () => run('plugin.remove', { id: e.id, keep_data: true }, { host: 'local' }) }] : []),
              { label: t('action.plugin.remove'), danger: true, onClick: () => e.kind === 'extension' ? run('ext.remove', { id: e.id }, { host: 'local' }) : run('plugin.remove', { id: e.id }, { host: 'local' }) },
            ]} /></span>}
          </Row>
        )
      })}
    </Box>
  )
}

function Market({ onInspect }: { onInspect: (i: Inspect) => void }) {
  const t = useT()
  const [locale] = useLocale()
  const [q, setQ] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const d = useApi<{ items: MarketItem[]; installed: Record<string, string>; index: string }>('/api/v1/plugins/market')
  const open = async (id: string) => {
    setBusy(id); setErr('')
    try { onInspect(await post<Inspect>('/api/v1/plugins/inspect', { market: id })) } catch (e) { setErr(errText(e)) } finally { setBusy('') }
  }
  if (d.error) return <Notice tone="warn">{t('plugin.market_down', { detail: d.error.message })}</Notice>
  const items = (d.data?.items ?? []).filter((x) => !q || x.id.includes(q) || tx(x.name, locale).toLowerCase().includes(q) || tx(x.description, locale).toLowerCase().includes(q))
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input className="h-8 w-64" placeholder={t('plugin.search')} value={q} onChange={(e) => setQ(e.target.value.trim().toLowerCase())} aria-label={t('plugin.search')} />
        <Button size="sm" variant="ghost" onClick={() => void d.reload()}><RefreshCw className="size-3.5" /></Button>
        <span className="num truncate text-[12px] text-mute">{d.data?.index}</span>
      </div>
      {err && <Notice tone="bad">{err}</Notice>}
      {d.data && items.length === 0 && <Empty title={t('plugin.market_empty')}>{t('plugin.market_empty_hint')}</Empty>}
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {items.map((x) => {
          const inst = d.data?.installed[x.id]
          return (
            <div key={x.id} className="flex flex-col gap-2 rounded-[var(--radius-box)] border border-line bg-card p-3.5">
              <div className="flex items-start gap-3">
                <PluginIcon src={x.icon} />
                <div className="min-w-0 flex-1"><div className="text-[14px] font-semibold">{tx(x.name, locale)}</div><div className="text-[12px] text-mute">{x.author} · {x.version}</div></div>
              </div>
              <p className="line-clamp-3 flex-1 text-[12.5px] text-mute">{tx(x.description, locale)}</p>
              <div className="flex flex-wrap items-center gap-1.5">
                <Pill>{x.runtime === 'container' ? t('plugin.rt_container') : x.run_as === 'root' ? t('plugin.rt_root') : t('plugin.rt_native')}</Pill>
                {x.signer ? <Pill tone="ok">{t('plugin.signed_short')}</Pill> : <Pill tone="warn">{t('plugin.unsigned_short')}</Pill>}
                <span className="flex-1" />
                {inst === x.version ? <Pill tone="ok">{t('plugin.is_installed')}</Pill>
                  : <Button size="sm" variant="primary" disabled={!!busy} onClick={() => void open(x.id)}><Download className="size-3.5" />{busy === x.id ? t('plugin.fetching') : inst ? t('plugin.update') : t('plugin.get')}</Button>}
              </div>
            </div>
          )
        })}
      </div>
      <p className="text-[12px] text-mute">{t('plugin.market_note')}</p>
    </div>
  )
}

function ManualSource({ onClose, onInspect }: { onClose: () => void; onInspect: (i: Inspect) => void }) {
  const t = useT()
  const [src, setSrc] = useState<'upload' | 'url' | 'dir'>('upload')
  const [up, setUp] = useState<Uploaded | null>(null)
  const [url, setUrl] = useState('')
  const [sha, setSha] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  if (src === 'dir') return <LegacyInstall onClose={onClose} onBack={() => setSrc('upload')} />
  const go = async () => {
    setBusy(true); setErr('')
    try { onInspect(await post<Inspect>('/api/v1/plugins/inspect', src === 'upload' ? { upload: up!.id } : { url, sha256: sha || undefined })) } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('plugin.manual')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={busy || (src === 'upload' ? !up?.done : !/^https:\/\//.test(url))} onClick={() => void go()}>{busy ? t('plugin.reading') : t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <Segmented value={src} onChange={setSrc} options={[{ value: 'upload', label: <span className="inline-flex items-center gap-1"><Upload className="size-3.5" />{t('plugin.src_upload')}</span> },
          { value: 'url', label: <span className="inline-flex items-center gap-1"><Link2 className="size-3.5" />{t('plugin.src_url')}</span> }, { value: 'dir', label: t('plugin.src_dir') }]} />
        {src === 'upload' && <Uploader accept=".berthplugin" label={t('plugin.pick_file')} onDone={setUp} />}
        {src === 'url' && <>
          <Field label={t('plugin.url')}><Input className="num" value={url} placeholder="https://example.com/my-plugin-1.0.0.berthplugin" onChange={(e) => setUrl(e.target.value.trim())} aria-label={t('plugin.url')} /></Field>
          <Field label="SHA-256" hint={t('plugin.sha_hint')}><Input className="num" value={sha} onChange={(e) => setSha(e.target.value.trim())} /></Field>
        </>}
        <p className="text-[12px] text-mute">{t('plugin.manual_hint')}</p>
        {err && <Notice tone="bad">{err}</Notice>}
      </div>
    </Dialog>
  )
}
