import { KeyRound } from 'lucide-react'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { Notice } from '@/components/ui/Box'
import { Input, Label } from '@/components/ui/Field'
import { ApiError, get, post, setCsrf } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'
import { onIPAddress, PasskeyCancelled, passkeyLogin, passkeySupported } from '@/lib/passkey'
import type { User } from '@/lib/types'
import { AuthCard } from './AuthCard'

type Login = { stage: 'full' | 'second_step'; csrf: string; user?: User; methods?: string[] }

export default function LoginPage() {
  const t = useT()
  const nav = useNavigate()
  const { user, refresh } = useAuth()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [second, setSecond] = useState<string[] | null>(null)
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState(false)
  const [remember, setRemember] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const codeRef = useRef<HTMLInputElement>(null)
  const [needsSetup, setNeedsSetup] = useState(false)
  useEffect(() => { get<{ needs_setup: boolean }>('/api/v1/meta').then((m) => setNeedsSetup(m.needs_setup)).catch(() => {}) }, [])
  useEffect(() => { if (second) codeRef.current?.focus() }, [second])
  if (user) return <Navigate to="/" replace />

  const done = async () => { await refresh(); nav('/', { replace: true }) }
  const wrap = async (fn: () => Promise<void>) => {
    setErr('')
    setBusy(true)
    try { await fn() } catch (e) {
      if (!(e instanceof PasskeyCancelled)) setErr(e instanceof ApiError ? e.message : (e as Error).message)
    } finally { setBusy(false) }
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void wrap(async () => {
      const r = await post<Login>('/api/v1/auth/login', { username, password })
      setCsrf(r.csrf)
      if (r.stage === 'second_step') setSecond(r.methods ?? [])
      else await done()
    })
  }
  const sendCode = (c: string) => wrap(async () => {
    await post('/api/v1/auth/totp', recovery ? { recovery: c, remember } : { code: c, remember })
    await done()
  })
  const viaPasskey = () => wrap(async () => { await passkeyLogin(remember); await done() })
  const pkOK = passkeySupported() && !onIPAddress()

  if (second) {
    return (
      <AuthCard title={t('login.second_title')} sub={t('login.second_sub')}>
        <div className="space-y-3">
          {err && <Notice tone="bad">{err}</Notice>}
          {second.includes('totp') && (
            <form onSubmit={(e) => { e.preventDefault(); void sendCode(code) }}>
              <Label htmlFor="code">{recovery ? t('login.recovery_code') : t('login.totp_code')}</Label>
              <Input id="code" ref={codeRef} value={code} autoComplete="one-time-code" inputMode={recovery ? 'text' : 'numeric'}
                className="num text-center text-[18px] tracking-[.3em]"
                onChange={(e) => {
                  const v = recovery ? e.target.value : e.target.value.replace(/\D/g, '').slice(0, 6)
                  setCode(v)
                  if (!recovery && v.length === 6) void sendCode(v)
                }} />
              {recovery && <Button type="submit" variant="primary" className="mt-3 w-full" disabled={busy}>{t('login.continue')}</Button>}
            </form>
          )}
          <label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />{t('login.remember')}</label>
          {second.includes('passkey') && pkOK && <Button className="w-full" onClick={viaPasskey} disabled={busy}><KeyRound className="size-4" />{t('login.use_passkey_instead')}</Button>}
          {second.includes('recovery') && <button className="text-[12.5px] text-acc hover:underline" onClick={() => { setRecovery(!recovery); setCode('') }}>{recovery ? t('login.use_totp') : t('login.use_recovery')}</button>}
        </div>
      </AuthCard>
    )
  }
  return (
    <AuthCard title={t('login.title')} sub={needsSetup ? t('login.needs_setup') : undefined}>
      <form onSubmit={submit} className="space-y-3">
        {err && <Notice tone="bad">{err}</Notice>}
        <div><Label htmlFor="u">{t('login.username')}</Label><Input id="u" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username webauthn" autoFocus /></div>
        <div><Label htmlFor="p">{t('login.password')}</Label><Input id="p" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" /></div>
        <Button type="submit" variant="primary" className="w-full" disabled={busy || !username || !password}>{t('login.submit')}</Button>
      </form>
      <div className="my-3 flex items-center gap-2 text-[12px] text-mute"><span className="h-px flex-1 bg-line" />{t('login.or')}<span className="h-px flex-1 bg-line" /></div>
      <Button className="w-full" onClick={viaPasskey} disabled={busy || !pkOK}><KeyRound className="size-4" />{t('login.passkey')}</Button>
      {!pkOK && <p className="mt-2 text-[12px] text-mute">{onIPAddress() ? t('login.passkey_needs_name') : t('login.passkey_unsupported')}</p>}
    </AuthCard>
  )
}
