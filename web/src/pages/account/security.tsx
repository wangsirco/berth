import qrcode from 'qrcode-generator'
import { KeyRound, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { Box, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Field'
import { ApiError, del, patch, post, put } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { LocalNameDialog } from '@/components/access/LocalNameGuide'
import { ago, datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { addPasskey, defaultPasskeyName, onIPAddress, PasskeyCancelled, passkeySupported } from '@/lib/passkey'
import { useReauth } from './reauth'

const msg = (e: unknown) => (e instanceof PasskeyCancelled ? '' : (e as Error).message)

export function PasskeyList({ onChange, highlight }: { onChange?: () => void; highlight?: boolean }) {
  const t = useT()
  const reauth = useReauth()
  const list = useApi<{ items: { id: number; name: string; rp_id: string; created_at: number; last_used_at: number }[]; usable: boolean; here: string }>('/api/v1/me/passkeys')
  const [err, setErr] = useState('')
  const [guide, setGuide] = useState(false)
  const { user: authUser } = useAuth()
  const can = passkeySupported() && !onIPAddress() && list.data?.usable !== false
  const add = async () => {
    setErr('')
    try { await reauth(() => addPasskey(defaultPasskeyName())); await list.reload(); onChange?.() } catch (e) { setErr(msg(e)) }
  }
  const remove = async (id: number) => {
    setErr('')
    try { await reauth(() => del(`/api/v1/me/passkeys/${id}`)); await list.reload(); onChange?.() } catch (e) { setErr(msg(e)) }
  }
  const rename = async (id: number, name: string) => {
    const n = prompt(t('passkey.rename'), name)
    if (n && n !== name) { await patch(`/api/v1/me/passkeys/${id}`, { name: n }); await list.reload() }
  }
  return (
    <Box title="Passkey" right={<Button size="sm" variant={highlight ? 'primary' : 'default'} className={highlight ? 'ring-2 ring-acc ring-offset-2' : undefined} onClick={() => void add()} disabled={!can} data-passkey-add><KeyRound className="size-3.5" />{t('passkey.add')}</Button>}>
      {err && <div className="p-3"><Notice tone="bad">{err}</Notice></div>}
      {!can && <div className="px-3.5 py-2 text-[12.5px] text-mute">{onIPAddress() ? t('login.passkey_needs_name') : t('login.passkey_unsupported')}
        {onIPAddress() && authUser?.role === 'super' && <button type="button" className="ml-2 text-acc hover:underline" onClick={() => setGuide(true)}>{t('lname.title')}</button>}</div>}
      {guide && <LocalNameDialog onClose={() => setGuide(false)} />}
      {list.data?.items.length === 0 && <Empty title={t('passkey.none')}>{t('passkey.why')}</Empty>}
      {list.data?.items.map((p) => (
        <Row key={p.id}>
          <KeyRound className="size-4 text-mute" />
          <div className="min-w-0 flex-1">
            <button className="text-[13.5px] font-semibold hover:underline" onClick={() => void rename(p.id, p.name)}>{p.name}</button>
            <div className="text-[12px] text-mute">{p.rp_id} · {t('passkey.added', { when: datetime(p.created_at) })}{p.last_used_at ? ' · ' + t('passkey.used', { when: ago(p.last_used_at, t) }) : ''}</div>
          </div>
          <Button size="sm" variant="ghost" aria-label={t('passkey.remove')} onClick={() => void remove(p.id)}><Trash2 className="size-4" /></Button>
        </Row>
      ))}
    </Box>
  )
}

export function TotpSetup({ onChange }: { onChange?: () => void }) {
  const t = useT()
  const reauth = useReauth()
  const { user, refresh } = useAuth()
  const has = (user as unknown as { has_totp?: boolean })?.has_totp
  const [setup, setSetup] = useState<{ secret: string; otpauth: string } | null>(null)
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)
  const [saved, setSaved] = useState(false)
  const [err, setErr] = useState('')
  const begin = async () => {
    setErr('')
    try { setSetup(await reauth(() => post<{ secret: string; otpauth: string }>('/api/v1/me/totp/begin'))) } catch (e) { setErr(msg(e)) }
  }
  const confirm = async () => {
    setErr('')
    try {
      const r = await post<{ recovery: string[] }>('/api/v1/me/totp/confirm', { code })
      setCodes(r.recovery)
      setSetup(null)
    } catch (e) { setErr((e as ApiError).message) }
  }
  const off = async () => {
    setErr('')
    try { await reauth(() => del('/api/v1/me/totp')); await refresh(); onChange?.() } catch (e) { setErr(msg(e)) }
  }
  const qr = (text: string) => {
    const q = qrcode(0, 'M')
    q.addData(text)
    q.make()
    return q.createSvgTag({ cellSize: 4, margin: 2, scalable: true })
  }
  return (
    <Box title={t('totp.title')} right={has ? <><Pill tone="ok">{t('totp.on')}</Pill><Button size="sm" variant="danger" onClick={() => void off()}>{t('totp.off')}</Button></> : !setup && !codes && <Button size="sm" onClick={() => void begin()}>{t('totp.setup')}</Button>}>
      <div className="space-y-3 p-3.5 text-[13px]">
        {err && <Notice tone="bad">{err}</Notice>}
        {!has && !setup && !codes && <p className="text-mute">{t('totp.why')}</p>}
        {setup && (
          <div className="flex flex-wrap gap-4">
            <div className="size-40 shrink-0 rounded border border-line bg-white p-1" dangerouslySetInnerHTML={{ __html: qr(setup.otpauth) }} />
            <div className="min-w-0 flex-1 space-y-2">
              <p>{t('totp.scan')}</p>
              <p className="num break-all rounded bg-soft px-2 py-1 text-[12px]">{setup.secret}</p>
              <div className="flex gap-2">
                <Input className="num w-32 text-center tracking-[.2em]" value={code} inputMode="numeric" aria-label={t('login.totp_code')}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))} />
                <Button variant="primary" onClick={() => void confirm()} disabled={code.length !== 6}>{t('totp.confirm')}</Button>
              </div>
            </div>
          </div>
        )}
        {codes && (
          <div className="space-y-2">
            <Notice tone="warn">{t('totp.recovery_once')}</Notice>
            <pre className="num grid grid-cols-2 gap-1 rounded border border-line bg-soft p-3 text-[13px]">{codes.join('\n')}</pre>
            <div className="flex items-center gap-3">
              <Button size="sm" onClick={() => navigator.clipboard?.writeText(codes.join('\n'))}>{t('common.copy')}</Button>
              <label className="flex items-center gap-2"><input type="checkbox" checked={saved} onChange={(e) => setSaved(e.target.checked)} />{t('totp.saved')}</label>
              <Button size="sm" variant="primary" className="ml-auto" disabled={!saved} onClick={() => { setCodes(null); void refresh(); onChange?.() }}>{t('common.done')}</Button>
            </div>
          </div>
        )}
      </div>
    </Box>
  )
}

export function LoginModeCard() {
  const t = useT()
  const reauth = useReauth()
  const { user, refresh } = useAuth()
  const [err, setErr] = useState('')
  const set = async (mode: string) => {
    setErr('')
    try { await reauth(() => put('/api/v1/me/login-mode', { mode })); await refresh() } catch (e) { setErr(msg(e)) }
  }
  return (
    <Box title={t('mode.title')}>
      <div className="space-y-2 p-3.5 text-[13px]">
        {err && <Notice tone="bad">{err}</Notice>}
        {(['password', 'password_2fa', 'passkey_only'] as const).map((m) => (
          <label key={m} className="flex cursor-pointer gap-2">
            <input type="radio" name="mode" checked={user?.login_mode === m} onChange={() => void set(m)} className="mt-1" />
            <span><b className="font-semibold">{t('mode.' + m)}</b><span className="block text-mute">{t('mode.' + m + '_hint')}</span></span>
          </label>
        ))}
      </div>
    </Box>
  )
}

export function DeviceList() {
  const t = useT()
  const list = useApi<{ items: { id: string; label: string; ip: string; created_at: number; expires_at: number }[] }>('/api/v1/me/devices')
  const sessions = useApi<{ items: { id: string; ip: string; user_agent: string; method: string; seen_at: number; current: boolean }[] }>('/api/v1/me/sessions')
  return (
    <>
      <Box title={t('devices.title')}>
        {list.data?.items.length === 0 && <div className="px-3.5 py-3 text-[13px] text-mute">{t('devices.none')}</div>}
        {list.data?.items.map((d) => (
          <Row key={d.id}>
            <div className="min-w-0 flex-1"><div className="truncate text-[13px]">{d.label}</div><div className="text-[12px] text-mute">{d.ip} · {t('devices.until', { when: datetime(d.expires_at) })}</div></div>
            <Button size="sm" onClick={() => void del(`/api/v1/me/devices/${d.id}`).then(list.reload)}>{t('devices.revoke')}</Button>
          </Row>
        ))}
      </Box>
      <Box title={t('sessions.title')}>
        {sessions.data?.items.map((s) => (
          <Row key={s.id}>
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13px]">{s.user_agent || '?'} {s.current && <Pill tone="acc">{t('sessions.current')}</Pill>}</div>
              <div className="text-[12px] text-mute">{s.ip} · {t('login_method.' + s.method)} · {ago(s.seen_at, t)}</div>
            </div>
            {!s.current && <Button size="sm" onClick={() => void del(`/api/v1/me/sessions/${s.id}`).then(sessions.reload)}>{t('sessions.end')}</Button>}
          </Row>
        ))}
      </Box>
    </>
  )
}

