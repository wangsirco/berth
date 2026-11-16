import { useState } from 'react'
import { useApi } from '@/lib/hooks'
import { Box, Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Field, Input, Segmented, Select, Switch } from '@/components/ui/Field'
import { ApiError, patch, post } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { LOCALES, useLocale, useT, type Locale } from '@/lib/i18n'
import { useTheme, type Theme } from '@/lib/theme'
import { ReauthProvider } from './reauth'
import { DeviceList, LoginModeCard, PasskeyList, TotpSetup } from './security'
import { SshKeys } from './SshKeys'
import { quickMode } from '@/components/change/ChangeDialog'

export default function AccountPage() {
  const t = useT()
  const { user, refresh } = useAuth()
  const [locale, setLocale] = useLocale()
  const { theme, setTheme } = useTheme()
  const [name, setName] = useState(user?.display_name ?? '')
  const [pw, setPw] = useState({ current: '', next: '' })
  const [note, setNote] = useState<{ tone: 'acc' | 'bad'; text: string } | null>(null)
  const saveProfile = async () => {
    await patch('/api/v1/me', { display_name: name, locale })
    await refresh()
    setNote({ tone: 'acc', text: t('account.saved') })
  }
  const changePw = async () => {
    try {
      await post('/api/v1/me/password', { current: pw.current, new: pw.next })
      setPw({ current: '', next: '' })
      setNote({ tone: 'acc', text: t('account.password_changed') })
    } catch (e) { setNote({ tone: 'bad', text: (e as ApiError).message }) }
  }
  return (
    <ReauthProvider>
      <div className="mx-auto grid max-w-5xl gap-4 lg:grid-cols-2">
        <div className="space-y-4">
          {note && <Notice tone={note.tone}>{note.text}</Notice>}
          <MyQuota />
          <AiSettings />
          <Box title={t('account.profile')}>
            <Field label={t('setup.display_name')}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
            <Field label={t('account.language')}>
              <Select value={locale} onChange={(e) => setLocale(e.target.value as Locale)}>{LOCALES.map((l) => <option key={l} value={l}>{t('locale.' + l)}</option>)}</Select>
            </Field>
            <Field label={t('account.theme')}>
              <Segmented<Theme> value={theme} onChange={setTheme} options={(['system', 'light', 'dark'] as Theme[]).map((v) => ({ value: v, label: t('theme.' + v) }))} />
            </Field>
            <div className="px-4 py-2"><QuickToggle /></div>
            <div className="px-4 py-3"><Button variant="primary" onClick={() => void saveProfile()}>{t('common.save')}</Button></div>
          </Box>
          <Box title={t('account.password')}>
            <Field label={t('account.current_password')}><Input type="password" autoComplete="current-password" value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} /></Field>
            <Field label={t('account.new_password')} hint={t('setup.password_hint')}><Input type="password" autoComplete="new-password" value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} /></Field>
            <div className="px-4 py-3"><Button onClick={() => void changePw()} disabled={!pw.current || !pw.next}>{t('account.change_password')}</Button></div>
          </Box>
          <DeviceList />
        </div>
        <div className="space-y-4">
          <PasskeyList onChange={refresh} />
          <TotpSetup onChange={refresh} />
          <LoginModeCard />
          <SshKeys />
        </div>
      </div>
    </ReauthProvider>
  )
}

// The person's own quota, shown only when one is set.
function MyQuota() {
  const t = useT()
  const d = useApi<{ quota: Record<string, number>; usage: Record<string, number>; unlimited: boolean }>('/api/v1/me/usage')
  const keys = Object.keys(d.data?.quota ?? {}).filter((k) => (d.data!.quota[k] ?? 0) > 0)
  if (!d.data || d.data.unlimited || keys.length === 0) return null
  return (
    <Box title={t('users.quota')}>
      <div className="space-y-2 px-4 py-3">
        {keys.map((k) => {
          const used = d.data!.usage[k] ?? 0
          const lim = d.data!.quota[k]
          return (
            <div key={k} className="text-[12.5px]">
              <div className="flex"><span className="flex-1">{t('users.q_' + k)}</span><span className="num">{used} / {lim}</span></div>
              <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-soft"><div className={'h-full rounded-full ' + (used >= lim ? 'bg-bad' : used / lim > 0.8 ? 'bg-warn' : 'bg-acc')} style={{ width: Math.min(100, (used / lim) * 100) + '%' }} /></div>
            </div>
          )
        })}
      </div>
    </Box>
  )
}

// The AI assistant (方案 §9.1): off until you save your own key. It only proposes changes.
function AiSettings() {
  const t = useT()
  const d = useApi<{ config: { provider: string; base_url: string; model: string; monthly_limit: number; has_key: boolean }; month: number; providers: Record<string, [string, string]> }>('/api/v1/ai')
  const [f, setF] = useState<{ provider: string; base_url: string; model: string; key: string; limit: string } | null>(null)
  const [note, setNote] = useState('')
  if (!d.data) return null
  const c = d.data.config
  const v = f ?? { provider: c.has_key ? c.provider : '', base_url: c.provider === 'openai' ? c.base_url : '', model: c.has_key ? c.model : '', key: '', limit: c.monthly_limit ? String(c.monthly_limit) : '' }
  const save = async () => {
    setNote('')
    try {
      await post('/api/v1/hosts/local/changes/ai.set', { provider: v.provider, base_url: v.provider === 'openai' ? v.base_url : '', model: v.model, key: v.key, monthly_limit: Number(v.limit || 0) })
      setF(null); await d.reload(); setNote(t('account.saved'))
    } catch (e) { setNote((e as ApiError).message) }
  }
  const def = d.data.providers[v.provider]
  return (
    <Box title={t('ai.settings')}>
      <p className="px-4 pt-3 text-[12.5px] text-mute">{t('ai.settings_hint')}</p>
      <Field label={t('ai.provider')}>
        <Select value={v.provider} onChange={(e) => setF({ ...v, provider: e.target.value, model: '' })}>
          <option value="">{t('ai.off')}</option><option value="anthropic">Anthropic Claude</option><option value="openai">{t('ai.openai')}</option><option value="xai">xAI Grok</option>
        </Select>
      </Field>
      {v.provider && <>
        {v.provider === 'openai' && <Field label={t('ai.base_url')} hint={t('ai.base_url_hint')}><Input className="num" value={v.base_url} placeholder={def?.[0]} onChange={(e) => setF({ ...v, base_url: e.target.value })} /></Field>}
        <Field label={t('ai.model')}><Input className="num" value={v.model} placeholder={def?.[1]} onChange={(e) => setF({ ...v, model: e.target.value })} /></Field>
        <Field label={t('ai.key')} hint={c.has_key ? t('ai.key_saved') : ''}><Input type="password" autoComplete="off" value={v.key} onChange={(e) => setF({ ...v, key: e.target.value })} /></Field>
        <Field label={t('ai.limit')} hint={t('ai.used', { n: d.data.month.toLocaleString() })}><Input type="number" min={0} value={v.limit} placeholder={t('users.no_limit')} onChange={(e) => setF({ ...v, limit: e.target.value })} /></Field>
      </>}
      <div className="flex items-center gap-2 px-4 pb-3">
        <span className="flex-1 text-[12.5px] text-mute">{note}</span>
        <Button size="sm" variant="primary" disabled={!f || (!!v.provider && !c.has_key && !v.key)} onClick={() => void save()}>{t('common.save')}</Button>
      </div>
    </Box>
  )
}

// QuickToggle: low-risk changes run without the preview dialog (stored in this browser).
function QuickToggle() {
  const t = useT()
  const [on, setOn] = useState(quickMode())
  return <div>
    <Switch checked={on} onChange={(v) => { localStorage.setItem('berth.quick', v ? '1' : '0'); setOn(v) }} label={t('account.quick')} />
    <p className="mt-1 text-[12px] text-mute">{t('account.quick_hint')}</p>
  </div>
}
