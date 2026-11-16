import { Check } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Box, Empty, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { ImportDialog } from '@/components/import/ImportDialog'
import { Field, Input, Select, Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { ApiError, get, patch, post, put } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { LOCALES, useLocale, useT, type Locale } from '@/lib/i18n'
import { AuthCard } from '../auth/AuthCard'
import { ReauthProvider } from '../account/reauth'
import { LoginModeCard, PasskeyList, TotpSetup } from '../account/security'
import { SoftwarePicker, type DesktopInfo } from '../remote/DesktopSoftware'

interface Proj { name: string; dir: string; services: string[]; running: number; total: number; adopted: boolean }

const randomEntry = () => '/b-' + Array.from(crypto.getRandomValues(new Uint8Array(6)), (b) => 'abcdefghjkmnpqrstuvwxyz23456789'[b % 31]).join('')

// Welcome: the first-login wizard (方案 §10.4, the parts phase 1 can do).
export default function Welcome() {
  const t = useT()
  const nav = useNavigate()
  const { user, refresh } = useAuth()
  const [locale, setLocale] = useLocale()
  const superUser = user?.role === 'super'
  const [desk, setDesk] = useState<DesktopInfo | null>(null)
  const [deskWant, setDeskWant] = useState<'later' | 'yes'>('later')
  const [deskPick, setDeskPick] = useState<string[]>([])
  const [deskStarted, setDeskStarted] = useState(false)
  const offerDesktop = !!desk && desk.status.supported && !desk.status.installed
  const steps = superUser ? ['basics', 'access', 'security', 'adopt', ...(offerDesktop ? ['desktop'] : []), 'later'] : ['basics', 'security']
  const [step, setStep] = useState(0)
  const [importing, setImporting] = useState<'1panel' | 'npm' | 'caddyfile' | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [s, setS] = useState({ name: '', entry: '', cidrs: '', termUser: '' })
  const [clientIP, setClientIP] = useState('')
  const [sysUsers, setSysUsers] = useState<string[]>([])
  const [projects, setProjects] = useState<Proj[]>([])
  const [pick, setPick] = useState<Record<string, boolean>>({})
  const [adopted, setAdopted] = useState<string[]>([])

  useEffect(() => {
    if (!superUser) return
    get<{ values: Record<string, string>; client_ip: string }>('/api/v1/settings').then((r) => {
      setS({ name: r.values['panel.name'] ?? '', entry: r.values['access.entry'] || randomEntry(), cidrs: (r.values['access.allow_cidrs'] ?? '').split(/[,\n]/).join('\n'), termUser: r.values['terminal.default_user'] ?? '' })
      setClientIP(r.client_ip)
    }).catch(() => {})
    get<{ items: { name: string }[] }>('/api/v1/hosts/local/system/users').then((r) => setSysUsers(r.items.map((u) => u.name))).catch(() => {})
    get<DesktopInfo>('/api/v1/desktop').then((r) => { setDesk(r); setDeskPick(r.presets.basic) }).catch(() => {})
    get<{ items: Proj[] }>('/api/v1/hosts/local/compose').then((r) => {
      setProjects(r.items)
      setPick(Object.fromEntries(r.items.filter((p) => !p.adopted).map((p) => [p.name, true])))
    }).catch(() => {})
  }, [superUser])

  const save = async (values: Record<string, string>) => {
    await put('/api/v1/settings', { values })
  }
  const next = async () => {
    setErr('')
    setBusy(true)
    try {
      const id = steps[step]
      if (id === 'basics') {
        await patch('/api/v1/me', { locale })
        if (superUser) await save({ 'panel.name': s.name, 'panel.locale': locale })
      }
      if (id === 'access') await save({ 'access.entry': s.entry, 'access.allow_cidrs': s.cidrs, ...(s.termUser ? { 'terminal.default_user': s.termUser } : {}) })
      if (id === 'adopt') {
        const done: string[] = []
        for (const p of projects.filter((p) => pick[p.name] && !p.adopted)) {
          await post('/api/v1/hosts/local/changes/compose.adopt', { name: p.name })
          done.push(p.name)
        }
        setAdopted(done)
      }
      if (id === 'desktop' && deskWant === 'yes') {
        await post('/api/v1/hosts/local/changes/desktop.install', { items: [...new Set([...deskPick, 'look'])] })
        setDeskStarted(true)
      }
      if (step === steps.length - 1) {
        await post('/api/v1/welcome/done')
        await refresh()
        nav('/', { replace: true })
        return
      }
      setStep(step + 1)
    } catch (e) {
      setErr((e as ApiError).message)
    } finally { setBusy(false) }
  }
  const id = steps[step]
  return (
    <ReauthProvider>
      <AuthCard wide title={t('welcome.step_' + id)} sub={t('welcome.step_' + id + '_sub')}>
        <ol className="mb-4 flex gap-1.5" aria-label={t('welcome.progress')}>
          {steps.map((x, i) => <li key={x} className={cx('h-1.5 flex-1 rounded-[1px]', i <= step ? 'bg-acc' : 'bg-line')} aria-current={i === step ? 'step' : undefined} />)}
        </ol>
        {err && <div className="mb-3"><Notice tone="bad">{err}</Notice></div>}
        <div className="-mx-5 space-y-3">
          {id === 'basics' && (
            <>
              {superUser && <Field label={t('welcome.panel_name')} hint={t('welcome.panel_name_hint')}><Input value={s.name} onChange={(e) => setS({ ...s, name: e.target.value })} placeholder="mini" /></Field>}
              <Field label={t('account.language')}>
                <Select value={locale} onChange={(e) => setLocale(e.target.value as Locale)}>{LOCALES.map((l) => <option key={l} value={l}>{t('locale.' + l)}</option>)}</Select>
              </Field>
            </>
          )}
          {id === 'access' && (
            <>
              <Field label={t('welcome.entry')} hint={t('welcome.entry_hint')}>
                <div className="flex gap-2"><Input className="num" aria-label={t("welcome.entry")} value={s.entry} onChange={(e) => setS({ ...s, entry: e.target.value })} /><Button onClick={() => setS({ ...s, entry: randomEntry() })}>{t('welcome.regenerate')}</Button></div>
                <p className="mt-1 text-[12px] text-mute">{t('welcome.entry_bookmark', { url: location.origin + s.entry })}</p>
              </Field>
              <Field label={t('welcome.public')}><p className="text-[13px] text-mute">{t('welcome.public_later')}</p></Field>
              <Field label={t('welcome.cidrs')} hint={t('welcome.cidrs_hint', { ip: clientIP })}>
                <Textarea className="num h-28 text-[12.5px]" value={s.cidrs} onChange={(e) => setS({ ...s, cidrs: e.target.value })} />
              </Field>
              <Field label={t('welcome.term_user')} hint={t('welcome.term_user_hint')}>
                <Select value={s.termUser} onChange={(e) => setS({ ...s, termUser: e.target.value })}>
                  <option value="">{t('common.none')}</option>
                  {sysUsers.map((u) => <option key={u} value={u}>{u}</option>)}
                </Select>
              </Field>
            </>
          )}
          {id === 'security' && <div className="space-y-3 px-5"><PasskeyList /><TotpSetup /><LoginModeCard /></div>}
          {id === 'adopt' && (
            <div className="space-y-3 px-5">
              <Box title={t('welcome.found')} count={projects.filter((p) => !p.adopted).length}>
                {projects.filter((p) => !p.adopted).length === 0 && <Empty title={t('welcome.nothing_found')}>{t('welcome.nothing_found_hint')}</Empty>}
                {projects.filter((p) => !p.adopted).map((p) => (
                  <label key={p.name} className="flex cursor-pointer items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-b-0">
                    <input type="checkbox" checked={!!pick[p.name]} onChange={(e) => setPick({ ...pick, [p.name]: e.target.checked })} />
                    <div className="min-w-0 flex-1">
                      <div className="text-[13.5px] font-semibold">{p.name}</div>
                      <div className="num truncate text-[12px] text-mute">{p.dir} · {p.services.join(', ') || '—'}</div>
                    </div>
                    <Pill>{p.running}/{p.total}</Pill>
                  </label>
                ))}
              </Box>
              <p className="text-[12.5px] text-mute">{t('welcome.adopt_note')}</p>
              <div className="flex flex-wrap items-center gap-2 text-[12.5px] text-mute">{t('welcome.import_from')}
                {(['1panel', 'npm', 'caddyfile'] as const).map((s) => <Button key={s} size="sm" onClick={() => setImporting(s)}>{s === '1panel' ? '1Panel' : s === 'npm' ? 'Nginx Proxy Manager' : 'Caddyfile'}</Button>)}
              </div>
              {importing && <ImportDialog initial={importing} onClose={() => setImporting(null)} />}
            </div>
          )}
          {id === 'desktop' && desk && (
            <div className="space-y-3 px-5">
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {(['later', 'yes'] as const).map((w) => (
                  <button key={w} type="button" onClick={() => setDeskWant(w)} className={cx('rounded-[var(--radius-box)] border p-2.5 text-left text-[13px]', deskWant === w ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
                    <div className="font-semibold">{t('welcome.desk_' + w)}</div>
                    <div className="text-[12px] text-mute">{t('welcome.desk_' + w + '_hint')}</div>
                  </button>
                ))}
              </div>
              {deskWant === 'yes' && <SoftwarePicker info={desk} pick={deskPick} onChange={setDeskPick} />}
            </div>
          )}
          {id === 'later' && (
            <div className="space-y-2 px-5 text-[13.5px]">
              {deskStarted && <Notice>{t('welcome.desk_started')}</Notice>}
              {adopted.length > 0 && <Notice><Check className="mr-1 inline size-4" />{t('welcome.adopted', { n: adopted.length, names: adopted.join('、') })}</Notice>}
              <p>{t('welcome.later_notify')}</p>
              <p>{t('welcome.later_integrations')}</p>
            </div>
          )}
        </div>
        <div className="mt-5 flex items-center gap-2">
          {step > 0 && <Button onClick={() => setStep(step - 1)} disabled={busy}>{t('common.back')}</Button>}
          {id === 'security' && <span className="text-[12px] text-mute">{t('welcome.security_optional')}</span>}
          <Button variant="primary" className="ml-auto" onClick={() => void next()} disabled={busy}>
            {step === steps.length - 1 ? t('welcome.finish') : t('common.next')}
          </Button>
        </div>
      </AuthCard>
    </ReauthProvider>
  )
}
