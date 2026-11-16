import { ArrowUpCircle, CheckCircle2, RefreshCw, RotateCcw, ShieldCheck } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Box, Dot, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch } from '@/components/ui/Field'
import { errText, get } from '@/lib/api'
import { ago, bytes, date } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

export interface UpdateInfo { current: string; latest: string; available: boolean; web_only: boolean; released: number; channel: string; notes?: Record<string, string>; web_bytes: number; core_bytes: number; checked: number }
export interface UpdateStatus { current: string; enabled: boolean; web_override: boolean; channel: string; auto: boolean; window: string; check: boolean; url: string; default_url: string; last?: UpdateInfo }
interface HostRow { name: string; online: boolean; remote: boolean; version?: string; upgrade?: boolean }

// 版本与更新 (like Wharf's): what runs, what is out, what changed; one click to update.
// The web UI alone is swapped in without a restart; the program restarts in about ten seconds
// and rolls itself back if the new one does not come up.
export function Updates() {
  const t = useT()
  const run = useChange()
  const [locale] = useLocale()
  const st = useApi<UpdateStatus>('/api/v1/update/status')
  const hosts = useApi<{ items: HostRow[] }>('/api/v1/hosts')
  useEvents(['change.done', 'host.online'], () => { void st.reload(); void hosts.reload() })
  const [info, setInfo] = useState<UpdateInfo | null>(null)
  const [checking, setChecking] = useState(false)
  const [err, setErr] = useState('')
  const [v, setV] = useState<Record<string, string>>({})
  useEffect(() => {
    if (st.data) {
      setInfo((i) => i ?? st.data!.last ?? null)
      setV({ 'update.channel': st.data.channel, 'update.auto': st.data.auto ? '1' : '0', 'update.window': st.data.window || '03:00-05:00', 'update.check': st.data.check ? '1' : '0', 'update.url': st.data.url })
    }
  }, [st.data])
  const check = async () => {
    setChecking(true); setErr('')
    try { setInfo(await get<UpdateInfo>('/api/v1/update/check')); void st.reload() } catch (e) { setErr(errText(e)) } finally { setChecking(false) }
  }
  if (!st.data) return null
  const s = st.data
  const orig: Record<string, string> = { 'update.channel': s.channel, 'update.auto': s.auto ? '1' : '0', 'update.window': s.window || '03:00-05:00', 'update.check': s.check ? '1' : '0', 'update.url': s.url }
  const changed = Object.fromEntries(Object.entries(v).filter(([k, x]) => (orig[k] ?? '') !== x))
  const notes = info?.notes?.[locale] ?? info?.notes?.en ?? info?.notes?.['zh-CN']
  const agents = (hosts.data?.items ?? []).filter((h) => h.remote)
  const stale = agents.filter((h) => h.online && h.upgrade)
  return (
    <div className="space-y-4">
      {!s.enabled && <Notice tone="warn">{t('upd.disabled')}</Notice>}
      <Box title={t('upd.versions')} right={s.enabled && <Button size="sm" disabled={checking} onClick={() => void check()}><RefreshCw className={'size-3.5 ' + (checking ? 'animate-spin' : '')} />{checking ? t('upd.checking_panel') : t('upd.check')}</Button>}>
        <Row>
          <ShieldCheck className="size-4 text-acc" />
          <div className="min-w-0 flex-1">
            <div className="text-[13.5px] font-semibold">Berth <span className="num font-normal">{s.current}</span>
              {s.web_override && <Pill className="ml-2">{t('upd.web_override')}</Pill>}</div>
            <div className="text-[12px] text-mute">{info ? t('upd.checked', { ago: ago(info.checked, t), channel: t('upd.ch_' + info.channel) }) : t('upd.never_checked')}</div>
          </div>
          {info && (info.available
            ? <Pill tone="acc"><ArrowUpCircle className="mr-1 size-3" />{t('upd.new', { v: info.latest })}</Pill>
            : <Pill tone="ok"><CheckCircle2 className="mr-1 size-3" />{t('upd.latest')}</Pill>)}
        </Row>
        {info?.available && (
          <div className="space-y-3 border-t border-line px-4 py-3">
            <div className="flex flex-wrap items-center gap-2 text-[13px]">
              <b>{t('upd.whats_new', { v: info.latest })}</b>
              <span className="text-mute">{info.released ? date(info.released) : ''}</span>
              <span className="text-mute">· {info.web_only ? t('upd.web_only', { size: bytes(info.web_bytes) }) : t('upd.full', { size: bytes(info.core_bytes) })}</span>
            </div>
            {notes ? <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-lg bg-soft px-3 py-2 text-[12.5px] leading-relaxed">{notes}</pre>
              : <p className="text-[12.5px] text-mute">{t('upd.no_notes')}</p>}
            <p className="text-[12px] text-mute">{info.web_only ? t('upd.web_only_hint') : t('upd.full_hint')}</p>
            <Button variant="primary" onClick={() => run('panel.update', { version: info.latest }, { host: 'local', onDone: () => { void st.reload(); if (info.web_only) setTimeout(() => location.reload(), 1500) } })}>
              <ArrowUpCircle className="size-4" />{t('upd.update_to', { v: info.latest })}</Button>
          </div>
        )}
        {s.web_override && (
          <div className="flex flex-wrap items-center gap-2 border-t border-line px-4 py-2.5 text-[12.5px] text-mute">
            <span className="flex-1">{t('upd.web_override_hint')}</span>
            <Button size="sm" variant="ghost" onClick={() => run('panel.web_reset', {}, { host: 'local', onDone: () => setTimeout(() => location.reload(), 800) })}><RotateCcw className="size-3.5" />{t('action.panel.web_reset')}</Button>
          </div>
        )}
        {err && <div className="px-4 pb-3"><Notice tone="bad">{err}</Notice></div>}
      </Box>

      {agents.length > 0 && (
        <Box title={t('upd.agents')} count={agents.length} right={stale.length > 1 && <Button size="sm" onClick={() => stale.forEach((h) => run('host.agent_update', { name: h.name }, { host: 'local' }))}>{t('upd.update_all', { n: stale.length })}</Button>}>
          {agents.map((h) => (
            <Row key={h.name}>
              <Dot state={h.online ? 'ok' : 'off'} />
              <div className="min-w-0 flex-1 text-[13.5px]"><b>{h.name}</b> <span className="num text-mute">{h.version ? 'berth ' + h.version : ''}</span></div>
              {h.online && (h.upgrade
                ? <Button size="sm" onClick={() => run('host.agent_update', { name: h.name }, { host: 'local' })}>{t('action.host.agent_update')}</Button>
                : <Pill tone="ok">{t('upd.same')}</Pill>)}
              {!h.online && <Pill>{t('hosts.offline')}</Pill>}
            </Row>
          ))}
        </Box>
      )}

      <Box title={t('upd.settings')}>
        <div className="space-y-3 px-4 py-3">
          <Switch checked={v['update.check'] === '1'} onChange={(x) => setV({ ...v, 'update.check': x ? '1' : '0' })} label={t('upd.auto_check')} />
          <Field label={t('upd.channel')} hint={t('upd.channel_hint')}>
            <Select className="w-48" value={v['update.channel']} onChange={(e) => setV({ ...v, 'update.channel': e.target.value })}>
              <option value="stable">{t('upd.ch_stable')}</option><option value="beta">{t('upd.ch_beta')}</option>
            </Select>
          </Field>
          <div className="flex flex-wrap items-center gap-3">
            <Switch checked={v['update.auto'] === '1'} onChange={(x) => setV({ ...v, 'update.auto': x ? '1' : '0' })} label={t('upd.auto')} />
            {v['update.auto'] === '1' && <Input className="num w-36" value={v['update.window']} onChange={(e) => setV({ ...v, 'update.window': e.target.value.trim() })} aria-label={t('upd.window')} placeholder="03:00-05:00" />}
            {v['update.auto'] === '1' && <span className="text-[12px] text-mute">{t('upd.window_hint')}</span>}
          </div>
          <Field label={t('upd.url')} hint={t('upd.url_hint', { url: s.default_url })}>
            <Input className="num" value={v['update.url']} placeholder={s.default_url} onChange={(e) => setV({ ...v, 'update.url': e.target.value.trim() })} />
          </Field>
          <Button variant="primary" disabled={!Object.keys(changed).length} onClick={() => run('settings.update', { values: changed }, { url: '/api/v1/settings', onDone: () => void st.reload() })}>{t('action.settings.update')}</Button>
        </div>
      </Box>
      <p className="text-[12px] text-mute">{t('upd.safety')}</p>
    </div>
  )
}
