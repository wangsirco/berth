import { Download } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { ImportDialog } from '@/components/import/ImportDialog'
import { PublicAccessSettings } from '@/components/access/PublicAccess'
import { PanelAddress } from '@/components/access/PanelAddress'
import { AccessCard } from '@/components/access/AccessCard'
import { Box } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Field'
import { get } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { LOCALES, useT } from '@/lib/i18n'
import { PanelSnapshot } from './PanelSnapshot'
import { Updates } from './Updates'
import { useParams } from 'react-router-dom'
import { hostPath } from '@/components/shell/host'
import { TabBar } from '@/components/ui/Tabs'

interface S { values: Record<string, string>; public: boolean; ca_fingerprint: string; client_ip: string }

const TABS = ['', 'updates'] as const

// Panel settings: general things, and 版本与更新.
export default function SettingsPage() {
  const t = useT()
  const sub = (useParams()['*'] ?? '').split('/')[0]
  const tab = sub === 'updates' ? 'updates' : ''
  return (
    <div className="mx-auto max-w-3xl space-y-4 pb-16">
      <TabBar label={t('nav.settings')} value={tab} items={TABS.map((x) => ({ value: x, label: t('settings.tab_' + (x || 'general')), to: hostPath('local', 'settings' + (x ? '/' + x : '')) }))} />
      {tab === 'updates' ? <Updates /> : <General />}
    </div>
  )
}

function General() {
  const t = useT()
  const run = useChange()
  const s = useApi<S>('/api/v1/settings')
  const [v, setV] = useState<Record<string, string>>({})
  const [users, setUsers] = useState<string[]>([])
  const [importing, setImporting] = useState<'1panel' | 'npm' | 'caddyfile' | null>(null)
  useEffect(() => { if (s.data) setV({ ...s.data.values, 'access.allow_cidrs': (s.data.values['access.allow_cidrs'] ?? '').split(/[,\n]/).join('\n') }) }, [s.data])
  useEffect(() => { get<{ items: { name: string }[] }>('/api/v1/hosts/local/system/users').then((r) => setUsers(r.items.map((u) => u.name))).catch(() => {}) }, [])
  if (!s.data) return null
  const orig: Record<string, string> = { ...s.data.values, 'access.allow_cidrs': (s.data.values['access.allow_cidrs'] ?? '').split(/[,\n]/).join('\n') }
  const changed = Object.fromEntries(Object.entries(v).filter(([k, x]) => (orig[k] ?? '') !== x))
  const set = (k: string) => (e: { target: { value: string } }) => setV({ ...v, [k]: e.target.value })
  const list = (k: string) => (v[k] ?? '').split(',').map((x) => x.trim()).filter(Boolean)
  const has = (k: string, x: string) => list(k).includes(x)
  const toggle = (k: string, x: string, on: boolean) => setV({ ...v, [k]: (on ? [...list(k).filter((y) => y !== x), x] : list(k).filter((y) => y !== x)).join(',') })
  return (
    <div className="space-y-4">
      <Box title={t('settings.general')}>
        <Field label={t('welcome.panel_name')}><Input value={v['panel.name'] ?? ''} onChange={set('panel.name')} /></Field>
        <Field label={t('settings.default_locale')} className="pb-3">
          <Select value={v['panel.locale'] ?? ''} onChange={set('panel.locale')}><option value="">{t('settings.browser_locale')}</option>{LOCALES.map((l) => <option key={l} value={l}>{t('locale.' + l)}</option>)}</Select>
        </Field>
      </Box>
      <Box title={t('access.title')}>
        <div className="px-4 py-2"><AccessCard publicField={<PanelAddress publicOnly lan={v['panel.lan_host'] ?? ''} pub={v['panel.public_host'] ?? ''} savedPub={orig['panel.public_host'] ?? ''} onChange={(k, x) => setV({ ...v, [k]: x })} />} /></div>
      </Box>
      <Box title={t('settings.access')}>
        <Field label={t('welcome.public')}><PublicAccessSettings entry={orig['access.entry'] ?? ''} onSaved={() => void s.reload()} /></Field>
        <Field label={t('welcome.entry')} hint={t('welcome.entry_hint')}><Input className="num" value={v['access.entry'] ?? ''} onChange={set('access.entry')} placeholder={t('settings.entry_off')} /></Field>
        <Field label={t('welcome.cidrs')} hint={s.data.public ? t('pub.cidrs_off') : t('welcome.cidrs_hint', { ip: s.data.client_ip })}><Textarea className="num h-28 text-[12.5px]" value={v['access.allow_cidrs'] ?? ''} onChange={set('access.allow_cidrs')} /></Field>
        <Field label={t('settings.ca')} className="pb-3">
          <p className="num break-all text-[12px] text-mute">{s.data.ca_fingerprint}</p>
          <a href="/api/v1/ca.crt" download><Button size="sm" className="mt-2"><Download className="size-3.5" />{t('settings.ca_download')}</Button></a>
          <p className="mt-1.5 text-[12px] text-mute">{t('settings.ca_hint')}</p>
        </Field>
      </Box>
      <Box title={t('shell.terminal')}>
        <Field label={t('welcome.term_user')} hint={t('welcome.term_user_hint')}>
          <Select value={v['terminal.default_user'] ?? ''} onChange={set('terminal.default_user')}><option value="">{t('common.none')}</option>{users.map((u) => <option key={u}>{u}</option>)}</Select>
        </Field>
        <div className="px-4 py-3"><Switch checked={v['terminal.record_all'] === '1'} onChange={(x) => setV({ ...v, 'terminal.record_all': x ? '1' : '0' })} label={t('settings.record_all')} /></div>
      </Box>
      <Box title={t('settings.approval')}>
        <p className="px-4 pt-3 text-[12.5px] text-mute">{t('settings.approval_hint')}</p>
        <Field label={t('settings.approval_who')}>
          <div className="flex flex-wrap gap-4">{['admins', 'tokens'].map((w) => <Switch key={w} checked={has('approval.who', w)} onChange={(x) => toggle('approval.who', w, x)} label={t('settings.approval_who_' + w)} />)}</div>
        </Field>
        <Field label={t('settings.approval_risks')}>
          <div className="flex flex-wrap gap-4">{['destructive', 'high', 'medium'].map((r) => <Switch key={r} checked={has('approval.risks', r)} onChange={(x) => toggle('approval.risks', r, x)} label={t('risk.' + r)} />)}</div>
        </Field>
        <Field label={t('settings.approval_kinds')} hint={t('settings.approval_kinds_hint')}><Input className="num" value={v['approval.kinds'] ?? ''} onChange={set('approval.kinds')} placeholder="firewall.*, ssh.set, site.delete" /></Field>
        <Field label={t('settings.approval_hours')} hint="1–168" className="pb-3"><Input type="number" min={1} max={168} className="w-24" value={v['approval.hours'] || '24'} onChange={set('approval.hours')} /></Field>
      </Box>
      <Box title={t('import.title')}>
        <div className="flex flex-wrap items-center gap-2 px-4 py-3 text-[12.5px] text-mute">{t('welcome.import_from')}
          {(['1panel', 'npm', 'caddyfile'] as const).map((x) => <Button key={x} size="sm" onClick={() => setImporting(x)}>{x === '1panel' ? '1Panel' : x === 'npm' ? 'Nginx Proxy Manager' : 'Caddyfile'}</Button>)}
        </div>
      </Box>
      {importing && <ImportDialog initial={importing} onClose={() => setImporting(null)} />}
      <WallSites value={v['simple.sites'] ?? ''} onChange={(x) => setV({ ...v, 'simple.sites': x })} />
      <Box title={t('settings.housekeeping')}>
        <Field label={t('settings.heavy_parallel')} hint="1–4"><Input type="number" min={1} max={4} className="w-24" value={v['tasks.heavy_parallel'] || '1'} onChange={set('tasks.heavy_parallel')} /></Field>
        <Field label={t('settings.keep_min')} hint="1–90"><Input type="number" className="w-24" value={v['metrics.keep_min_days'] || '7'} onChange={set('metrics.keep_min_days')} /></Field>
        <Field label={t('settings.keep_hour')} hint="7–1095"><Input type="number" className="w-24" value={v['metrics.keep_hour_days'] || '365'} onChange={set('metrics.keep_hour_days')} /></Field>
        <Field label={t('settings.trash_days')} hint="1–365" className="pb-3"><Input type="number" className="w-24" value={v['files.trash_days'] || '30'} onChange={set('files.trash_days')} /></Field>
      </Box>
      <PanelSnapshot />
      <div className="sticky bottom-9 flex items-center gap-2 rounded-[var(--radius-box)] border border-line bg-canvas px-4 py-2.5 shadow-sm">
        <span className="text-[12.5px] text-mute">{Object.keys(changed).length ? t('settings.changed', { n: Object.keys(changed).length }) : t('settings.no_change')}</span>
        <Button className="ml-auto" disabled={!Object.keys(changed).length} onClick={() => setV(orig)}>{t('compose.discard')}</Button>
        <Button variant="primary" disabled={!Object.keys(changed).length} onClick={() => run('settings.update', { values: changed }, { url: '/api/v1/settings', onDone: () => void s.reload() })}>{t('action.settings.update')}</Button>
      </div>
    </div>
  )
}

// Which sites the app wall (简要模式) shows; none ticked means every enabled site.
function WallSites({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const t = useT()
  const hosts = useApi<{ items: { name: string }[] }>('/api/v1/hosts')
  const [sites, setSites] = useState<{ key: string; label: string }[]>([])
  useEffect(() => {
    const names = ['local', ...(hosts.data?.items.map((h) => h.name).filter((n) => n !== 'local') ?? [])]
    Promise.all(names.map((h) => get<{ items: { id: number; domains: string[]; enabled: boolean; options: { note?: string } }[] }>(`/api/v1/hosts/${h}/sites`)
      .then((r) => r.items.filter((s) => s.enabled && s.domains.length).map((s) => ({ key: `${h}:${s.id}`, label: (s.options?.note?.trim() || s.domains[0]) + (names.length > 1 ? ` · ${h}` : '') })))
      .catch(() => [] as { key: string; label: string }[]))).then((l) => setSites(l.flat()))
  }, [hosts.data])
  const sel = value.split(',').map((x) => x.trim()).filter(Boolean)
  return (
    <Box title={t('settings.wall')}>
      <p className="px-4 pt-3 text-[12.5px] text-mute">{t('settings.wall_hint')}</p>
      <div className="flex flex-wrap gap-x-4 gap-y-1.5 px-4 py-3 text-[13px]">
        {sites.length === 0 && <span className="text-mute">{t('settings.wall_none')}</span>}
        {sites.map((s) => (
          <label key={s.key} className="flex items-center gap-1.5"><input type="checkbox" checked={sel.includes(s.key)}
            onChange={(e) => onChange((e.target.checked ? [...sel, s.key] : sel.filter((x) => x !== s.key)).join(','))} />{s.label}</label>
        ))}
      </div>
      <div className="px-4 pb-3"><a className="text-[12.5px] text-acc hover:underline" href="/wall" target="_blank" rel="noreferrer">{t('settings.wall_open')}</a></div>
    </Box>
  )
}
