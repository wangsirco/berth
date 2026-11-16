import { useEffect, useState } from 'react'
import { useHost } from '@/components/shell/host'
import { useChange } from '@/components/change/ChangeDialog'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented, Select, Switch, Textarea } from '@/components/ui/Field'
import { del, errText, post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago, datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { currentSubscription, pushSupported, subscribePush, unsubscribePush } from '@/lib/push'
import { TabBar } from '@/components/ui/Tabs'

interface Rule { key: string; category: string; severity: string; enabled: boolean; value?: number; minutes?: number; quiet?: string }
interface AlertEv { id: number; rule: string; category: string; severity: string; host: string; state: string; started: number; resolved_at?: number; title: string; message: string }
interface DayUp { day: string; ratio: number; avg_ms: number }
interface Check { id: number; name: string; kind: 'http' | 'tcp'; target: string; interval: number; expect?: number; keyword?: string; insecure?: boolean; up?: boolean; last_ms?: number; last_error?: string; last_at?: number; uptime_30d: number; days?: DayUp[] }
interface Page { slug: string; title: string; checks: number[]; note?: string }
interface Channel { id: number; kind: string; name: string; categories: string[]; last_sent?: number; last_error?: string; hint?: string }

type Tab = 'alerts' | 'uptime' | 'channels' | 'rules'

export default function MonitorPage() {
  const t = useT()
  const [tab, setTab] = useState<Tab>('alerts')
  const { user } = useAuth()
  const tabs: { value: Tab; label: string }[] = [
    { value: 'alerts', label: t('monitor.alerts') },
    { value: 'uptime', label: t('monitor.uptime') },
    { value: 'channels', label: t('monitor.channels') },
  ]
  if (can(user, 'monitor.manage')) tabs.push({ value: 'rules', label: t('monitor.rules') })
  return (
    <div className="space-y-4">
      <TabBar label={t('nav.monitor')} value={tab} items={tabs} onChange={setTab} />
      {tab === 'alerts' && <Alerts />}
      {tab === 'uptime' && <Uptime />}
      {tab === 'channels' && <Channels />}
      {tab === 'rules' && <Rules />}
    </div>
  )
}

const sevTone = (s: string) => (s === 'critical' ? 'bad' : s === 'warn' ? 'warn' : 'acc') as 'bad' | 'warn' | 'acc'

function Alerts() {
  const t = useT()
  const data = useApi<{ items: AlertEv[] }>('/api/v1/alerts')
  useEvents(['alert'], () => void data.reload())
  const items = data.data?.items ?? []
  const firing = items.filter((e) => e.state === 'firing')
  const past = items.filter((e) => e.state !== 'firing')
  const row = (e: AlertEv) => (
    <Row key={e.id}>
      <Dot state={e.state === 'firing' ? (e.severity === 'critical' ? 'bad' : 'warn') : 'off'} />
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] font-semibold">{e.title} {e.host && e.host !== 'local' && <span className="font-normal text-mute">· {e.host}</span>}</div>
        <div className="truncate text-[12px] text-mute">{e.message}</div>
      </div>
      <Pill tone={sevTone(e.severity)}>{t('monitor.sev_' + e.severity)}</Pill>
      <span className="num w-36 shrink-0 text-right text-[12px] text-mute">{e.state === 'firing' ? ago(e.started, t) : datetime(e.started) + (e.resolved_at ? ' → ' + datetime(e.resolved_at).slice(-5) : '')}</span>
    </Row>
  )
  return (
    <>
      <Box title={t('monitor.firing')} count={firing.length}>
        {firing.length === 0 ? <Empty title={t('monitor.all_good')}>{t('monitor.all_good_hint')}</Empty> : firing.map(row)}
      </Box>
      <Box title={t('monitor.history')} count={past.length}>
        {past.length === 0 ? <Empty title={t('monitor.no_history')} /> : past.map(row)}
      </Box>
    </>
  )
}

function Bars({ days }: { days?: DayUp[] }) {
  const t = useT()
  return (
    <div className="flex h-5 w-full max-w-[360px] gap-[2px]">
      {(days ?? []).map((d) => (
        <i key={d.day} title={`${d.day} · ${d.ratio < 0 ? t('monitor.no_data') : (d.ratio * 100).toFixed(2) + '% · ' + d.avg_ms + ' ms'}`}
          className={'flex-1 rounded-[2px] ' + (d.ratio < 0 ? 'bg-line' : d.ratio >= 0.999 ? 'bg-ok' : d.ratio >= 0.95 ? 'bg-warn' : 'bg-bad')} />
      ))}
    </div>
  )
}

function Uptime() {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const manage = can(user, 'monitor.manage')
  const data = useApi<{ items: Check[]; pages: Page[] }>('/api/v1/uptime')
  useEvents(['uptime', 'change.done'], () => void data.reload())
  const [edit, setEdit] = useState<Check | 'new' | null>(null)
  const [page, setPage] = useState<Page | 'new' | null>(null)
  const checks = data.data?.items ?? []
  return (
    <>
      <Box title={t('monitor.checks')} count={checks.length} right={manage && <Button size="sm" variant="primary" onClick={() => setEdit('new')}>{t('monitor.new_check')}</Button>}>
        {checks.length === 0 && <Empty title={t('monitor.no_checks')}>{t('monitor.no_checks_hint')}</Empty>}
        {checks.map((c) => (
          <Row key={c.id}>
            <Dot state={c.up === undefined ? 'off' : c.up ? 'ok' : 'bad'} />
            <div className="min-w-0 flex-1 space-y-1">
              <div className="text-[13.5px] font-semibold">{c.name} <span className="num font-normal text-mute">· {c.target}</span></div>
              <Bars days={c.days} />
              <div className="truncate text-[12px] text-mute">
                {c.uptime_30d < 0 ? t('monitor.no_data') : t('monitor.uptime_30d', { pct: c.uptime_30d.toFixed(2) })}
                {c.last_ms !== undefined && c.last_at ? ' · ' + c.last_ms + ' ms · ' + ago(c.last_at, t) : ''}
                {c.up === false && c.last_error ? ' · ' + c.last_error : ''}
              </div>
            </div>
            {manage && <>
              <Button size="sm" variant="ghost" onClick={() => setEdit(c)}>{t('sites.edit')}</Button>
              <Button size="sm" variant="ghost" onClick={() => run('uptime.delete', { id: c.id })}>{t('common.delete')}</Button>
            </>}
          </Row>
        ))}
      </Box>
      <Box title={t('monitor.status_pages')} count={data.data?.pages.length} right={manage && checks.length > 0 && <Button size="sm" onClick={() => setPage('new')}>{t('monitor.new_page')}</Button>}>
        {data.data?.pages.length === 0 && <Empty title={t('monitor.no_pages')}>{t('monitor.no_pages_hint')}</Empty>}
        {data.data?.pages.map((p) => (
          <Row key={p.slug}>
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{p.title}</div>
              <a className="num text-[12px] text-acc hover:underline" href={'/status/' + p.slug} target="_blank" rel="noreferrer">{location.origin}/status/{p.slug}</a>
            </div>
            <span className="text-[12px] text-mute">{t('monitor.page_checks', { n: p.checks.length })}</span>
            {manage && <>
              <Button size="sm" variant="ghost" onClick={() => setPage(p)}>{t('sites.edit')}</Button>
              <Button size="sm" variant="ghost" onClick={() => run('statuspage.delete', { slug: p.slug })}>{t('common.delete')}</Button>
            </>}
          </Row>
        ))}
      </Box>
      {edit && <CheckDialog check={edit === 'new' ? undefined : edit} onClose={() => setEdit(null)} />}
      {page && <PageDialog page={page === 'new' ? undefined : page} checks={checks} onClose={() => setPage(null)} />}
    </>
  )
}

function CheckDialog({ check, onClose }: { check?: Check; onClose: () => void }) {
  const { hosts } = useHost()
  const t = useT()
  const run = useChange()
  const [v, setV] = useState({ id: check?.id, name: check?.name ?? '', kind: check?.kind ?? 'http', target: check?.target ?? 'https://', interval: check?.interval ?? 60, expect: check?.expect ?? 0, keyword: check?.keyword ?? '', insecure: check?.insecure ?? false, from: (check as { from?: string } | undefined)?.from ?? '' })
  const set = <K extends keyof typeof v>(k: K) => (x: (typeof v)[K]) => setV({ ...v, [k]: x })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={check ? t('monitor.edit_check') : t('monitor.new_check')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!v.name || !v.target} onClick={() => run('uptime.set', v, { onDone: onClose })}>{t('action.uptime.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <Field label={t('monitor.check_name')}><Input value={v.name} onChange={(e) => set('name')(e.target.value)} placeholder={t('monitor.check_name_ph')} /></Field>
        <Field label={t('monitor.check_kind')}><Segmented value={v.kind} options={[{ value: 'http', label: 'HTTP(S)' }, { value: 'tcp', label: 'TCP' }]} onChange={(k) => setV({ ...v, kind: k, target: k === 'tcp' ? '' : 'https://' })} /></Field>
        <Field label={t('monitor.check_target')} hint={v.kind === 'tcp' ? t('monitor.target_tcp') : t('monitor.target_http')}><Input className="num" value={v.target} onChange={(e) => set('target')(e.target.value)} /></Field>
        {hosts.length > 1 && <Field label={t('monitor.from')} hint={t('monitor.from_hint')}>
          <Select value={v.from} onChange={(e) => set('from')(e.target.value)}><option value="">{t('monitor.from_panel')}</option>{hosts.filter((h) => h.name !== 'local').map((h) => <option key={h.name} value={h.name}>{h.name}</option>)}</Select>
        </Field>}
        <Field label={t('monitor.interval')}>
          <Select value={String(v.interval)} onChange={(e) => set('interval')(Number(e.target.value))}>
            {[30, 60, 120, 300, 600].map((s) => <option key={s} value={s}>{s < 60 ? s + ' s' : s / 60 + ' min'}</option>)}
          </Select>
        </Field>
        {v.kind === 'http' && <>
          <Field label={t('monitor.expect')} hint={t('monitor.expect_hint')}><Input type="number" className="w-28" value={v.expect || ''} onChange={(e) => set('expect')(Number(e.target.value) || 0)} /></Field>
          <Field label={t('monitor.keyword')} hint={t('monitor.keyword_hint')}><Input value={v.keyword} onChange={(e) => set('keyword')(e.target.value)} /></Field>
          <Switch checked={v.insecure} onChange={set('insecure')} label={t('monitor.insecure')} />
        </>}
      </div>
    </Dialog>
  )
}

function PageDialog({ page, checks, onClose }: { page?: Page; checks: Check[]; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const [v, setV] = useState<Page>(page ?? { slug: '', title: '', checks: checks.map((c) => c.id), note: '' })
  const toggle = (id: number) => setV({ ...v, checks: v.checks.includes(id) ? v.checks.filter((x) => x !== id) : [...v.checks, id] })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={page ? t('monitor.edit_page') : t('monitor.new_page')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!v.slug || !v.title} onClick={() => run('statuspage.set', v, { onDone: onClose })}>{t('action.statuspage.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <Notice tone="warn">{t('monitor.page_public')}</Notice>
        <Field label={t('monitor.page_slug')} hint={location.origin + '/status/' + (v.slug || '…')}><Input className="num" disabled={!!page} value={v.slug} onChange={(e) => setV({ ...v, slug: e.target.value.toLowerCase() })} /></Field>
        <Field label={t('monitor.page_title')}><Input value={v.title} onChange={(e) => setV({ ...v, title: e.target.value })} /></Field>
        <Field label={t('monitor.page_note')}><Textarea rows={2} value={v.note ?? ''} onChange={(e) => setV({ ...v, note: e.target.value })} /></Field>
        <Field label={t('monitor.page_pick')}>
          <div className="space-y-1">{checks.map((c) => <Switch key={c.id} checked={v.checks.includes(c.id)} onChange={() => toggle(c.id)} label={c.name} />)}</div>
        </Field>
      </div>
    </Dialog>
  )
}

const KINDS = ['webpush', 'telegram', 'serverchan', 'pushplus', 'wecom', 'dingtalk', 'feishu', 'email', 'bark', 'ntfy', 'webhook'] as const

function Channels() {
  const t = useT()
  const data = useApi<{ items: Channel[]; categories: string[]; vapid: string }>('/api/v1/me/channels')
  const [add, setAdd] = useState<string | null>(null)
  const [msg, setMsg] = useState<{ tone: 'acc' | 'bad'; text: string } | null>(null)
  const [here, setHere] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => { void currentSubscription().then((s) => setHere(s?.endpoint ?? null)) }, [data.data])
  const items = data.data?.items ?? []
  const enablePush = async () => {
    setBusy(true); setMsg(null)
    try {
      const id = await subscribePush(data.data!.vapid, browserName())
      await post(`/api/v1/me/channels/${id}/test`)
      setMsg({ tone: 'acc', text: t('monitor.push_on') })
    } catch (e) {
      setMsg({ tone: 'bad', text: errText(e) === 'denied' ? t('monitor.push_denied') : errText(e) })
    } finally { setBusy(false); void data.reload() }
  }
  const test = async (c: Channel) => {
    setMsg(null)
    try { await post(`/api/v1/me/channels/${c.id}/test`); setMsg({ tone: 'acc', text: t('monitor.test_sent', { name: c.name }) }) } catch (e) { setMsg({ tone: 'bad', text: errText(e) }) }
    void data.reload()
  }
  const remove = async (c: Channel) => {
    if (c.kind === 'webpush' && here && c.hint && here.includes(c.hint)) await unsubscribePush().catch(() => {})
    await del(`/api/v1/me/channels/${c.id}`)
    void data.reload()
  }
  const toggleCat = async (c: Channel, cat: string) => {
    const cats = c.categories.includes(cat) ? c.categories.filter((x) => x !== cat) : [...c.categories, cat]
    await post('/api/v1/me/channels', { id: c.id, name: c.name, categories: cats })
    void data.reload()
  }
  return (
    <>
      <Notice>{t('monitor.channels_hint')}</Notice>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      <Box title={t('monitor.my_channels')} count={items.length} right={
        <div className="flex gap-2">
          {pushSupported() ? <Button size="sm" variant="primary" disabled={busy || !data.data} onClick={enablePush}>{t('monitor.enable_push')}</Button>
            : <span className="text-[12px] text-mute">{t('monitor.push_unsupported')}</span>}
          <Select className="h-7 text-[12.5px]" value="" onChange={(e) => e.target.value && setAdd(e.target.value)}>
            <option value="">{t('monitor.add_channel')}</option>
            {KINDS.filter((k) => k !== 'webpush').map((k) => <option key={k} value={k}>{t('monitor.kind_' + k)}</option>)}
          </Select>
        </div>}>
        {items.length === 0 && <Empty title={t('monitor.no_channels')}>{t('monitor.no_channels_hint')}</Empty>}
        {items.map((c) => (
          <Row key={c.id}>
            <Dot state={c.last_error ? 'bad' : c.last_sent ? 'ok' : 'off'} />
            <div className="min-w-0 flex-1 space-y-1">
              <div className="text-[13.5px] font-semibold">{c.name} <span className="font-normal text-mute">· {t('monitor.kind_' + c.kind)}{c.hint ? ' · ' + c.hint : ''}</span>
                {c.kind === 'webpush' && here && c.hint && here.includes(c.hint) && <Pill tone="acc" className="ml-2">{t('monitor.this_browser')}</Pill>}</div>
              <div className="flex flex-wrap gap-1">
                {data.data?.categories.map((cat) => (
                  <button key={cat} onClick={() => void toggleCat(c, cat)} className={'rounded-full border px-2 py-[1px] text-[11.5px] ' + (c.categories.includes(cat) ? 'border-acc bg-acc/10 text-acc' : 'border-line text-mute')}>{t('monitor.cat_' + cat)}</button>
                ))}
              </div>
              {c.last_error && <div className="truncate text-[12px] text-bad">{c.last_error}</div>}
            </div>
            <Button size="sm" variant="ghost" onClick={() => void test(c)}>{t('monitor.test')}</Button>
            <Button size="sm" variant="ghost" onClick={() => void remove(c)}>{t('common.delete')}</Button>
          </Row>
        ))}
      </Box>
      {add && <ChannelDialog kind={add} onClose={() => setAdd(null)} onDone={() => { setAdd(null); void data.reload() }} />}
    </>
  )
}

function browserName() {
  const ua = navigator.userAgent
  const b = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : 'Browser'
  const os = /iPhone|iPad/.test(ua) ? 'iOS' : /Android/.test(ua) ? 'Android' : /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : ''
  return [b, os].filter(Boolean).join(' · ')
}

const FIELDS: Record<string, { key: string; secret?: boolean; ph?: string; area?: boolean }[]> = {
  telegram: [{ key: 'bot_token', secret: true, ph: '123456:ABC…' }, { key: 'chat_id', ph: '123456789' }],
  email: [{ key: 'smtp_host', ph: 'smtp.example.com:465' }, { key: 'smtp_user' }, { key: 'smtp_pass', secret: true }, { key: 'from', ph: 'berth@example.com' }, { key: 'to', ph: 'me@example.com' }],
  bark: [{ key: 'url', ph: 'https://api.day.app/<key>' }],
  serverchan: [{ key: 'key', secret: true, ph: 'SCT… / sctp…' }],
  pushplus: [{ key: 'key', secret: true, ph: 'token' }],
  wecom: [{ key: 'url', ph: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…' }],
  dingtalk: [{ key: 'url', ph: 'https://oapi.dingtalk.com/robot/send?access_token=…' }, { key: 'secret', secret: true, ph: 'SEC…' }],
  feishu: [{ key: 'url', ph: 'https://open.feishu.cn/open-apis/bot/v2/hook/…' }, { key: 'secret', secret: true }],
  ntfy: [{ key: 'url', ph: 'https://ntfy.sh/my-topic' }, { key: 'token', secret: true }],
  webhook: [{ key: 'url', ph: 'https://…' }, { key: 'template', area: true, ph: '{"title": {{json .Title}}, "text": {{json .Body}}}' }],
}

function ChannelDialog({ kind, onClose, onDone }: { kind: string; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const [name, setName] = useState(t('monitor.kind_' + kind))
  const [cfg, setCfg] = useState<Record<string, string>>({})
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true); setErr('')
    try {
      const r = await post<{ id: number }>('/api/v1/me/channels', { name, config: { kind, ...cfg } })
      try { await post(`/api/v1/me/channels/${r.id}/test`) } catch (e) { setErr(t('monitor.saved_test_failed', { error: errText(e) })); onDone(); return }
      onDone()
    } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('monitor.add_channel') + ' · ' + t('monitor.kind_' + kind)}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={busy} onClick={save}>{t('monitor.save_test')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        {err && <Notice tone="bad">{err}</Notice>}
        <Notice>{t('monitor.how_' + kind)}</Notice>
        <Field label={t('monitor.channel_name')}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        {FIELDS[kind]?.map((f) => (
          <Field key={f.key} label={t('monitor.f_' + f.key)}>
            {f.area ? <Textarea rows={3} className="num" placeholder={f.ph} value={cfg[f.key] ?? ''} onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value })} />
              : <Input className="num" type={f.secret ? 'password' : 'text'} autoComplete="off" placeholder={f.ph} value={cfg[f.key] ?? ''} onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value })} />}
          </Field>
        ))}
      </div>
    </Dialog>
  )
}

function Rules() {
  const t = useT()
  const run = useChange()
  const data = useApi<{ rules: Rule[] }>('/api/v1/alerts')
  const [edit, setEdit] = useState<Rule | null>(null)
  return (
    <>
      <Box title={t('monitor.rules')} count={data.data?.rules.length}>
        {data.data?.rules.map((r) => (
          <Row key={r.key} onClick={() => setEdit(r)}>
            <Dot state={r.enabled ? 'ok' : 'off'} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{t('monitor.rule_' + r.key)}</div>
              <div className="truncate text-[12px] text-mute">{t('monitor.rule_' + r.key + '_d', { value: r.value ?? '', minutes: r.minutes ?? '' })}{r.quiet ? ' · ' + t('monitor.quiet_at', { range: r.quiet }) : ''}</div>
            </div>
            <Pill tone={sevTone(r.severity)}>{t('monitor.sev_' + r.severity)}</Pill>
          </Row>
        ))}
      </Box>
      {edit && <RuleDialog rule={edit} onClose={() => setEdit(null)} onDone={() => { setEdit(null); void data.reload() }} run={run} />}
    </>
  )
}

function RuleDialog({ rule, onClose, onDone, run }: { rule: Rule; onClose: () => void; onDone: () => void; run: ReturnType<typeof useChange> }) {
  const t = useT()
  const [v, setV] = useState(rule)
  const hasValue = ['mem_high', 'disk_high', 'container_restarting', 'cert_expiring'].includes(rule.key)
  const hasMinutes = ['mem_high', 'container_restarting', 'host_offline'].includes(rule.key)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('monitor.rule_' + rule.key)}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => run('alert.rule.set', v, { onDone })}>{t('action.alert.rule.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <Switch checked={v.enabled} onChange={(x) => setV({ ...v, enabled: x })} label={t('monitor.rule_enabled')} />
        {hasValue && <Field label={t('monitor.rule_value_' + rule.key)}><Input type="number" className="w-28" value={v.value ?? ''} onChange={(e) => setV({ ...v, value: Number(e.target.value) })} /></Field>}
        {hasMinutes && <Field label={t('monitor.rule_minutes')}><Input type="number" className="w-28" value={v.minutes ?? ''} onChange={(e) => setV({ ...v, minutes: Number(e.target.value) })} /></Field>}
        <Field label={t('monitor.quiet')} hint={rule.severity === 'critical' ? t('monitor.quiet_critical') : t('monitor.quiet_hint')}>
          <Input className="num w-40" placeholder="22:00-07:00" value={v.quiet ?? ''} onChange={(e) => setV({ ...v, quiet: e.target.value })} />
        </Field>
      </div>
    </Dialog>
  )
}
