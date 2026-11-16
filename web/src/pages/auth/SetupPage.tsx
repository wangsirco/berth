import { useState, type FormEvent } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { Notice } from '@/components/ui/Box'
import { Input, Label } from '@/components/ui/Field'
import { ApiError, post } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'
import type { User } from '@/lib/types'
import { AuthCard } from './AuthCard'

export default function SetupPage() {
  const t = useT()
  const nav = useNavigate()
  const { code = '' } = useParams()
  const { signedIn } = useAuth()
  const [f, setF] = useState({ username: '', display_name: '', password: '', again: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => setF({ ...f, [k]: e.target.value })
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (f.password !== f.again) { setErr(t('setup.mismatch')); return }
    setErr('')
    setBusy(true)
    try {
      const r = await post<{ user: User; csrf: string }>('/api/v1/setup', { code, username: f.username, display_name: f.display_name, password: f.password })
      signedIn(r.user, r.csrf)
      nav('/welcome', { replace: true })
    } catch (e) {
      setErr((e as ApiError).message)
    } finally { setBusy(false) }
  }
  return (
    <AuthCard title={t('setup.title')} sub={t('setup.sub')}>
      <form onSubmit={submit} className="space-y-3">
        {err && <Notice tone="bad">{err}</Notice>}
        <div><Label htmlFor="u" hint={t('setup.username_hint')}>{t('login.username')}</Label><Input id="u" value={f.username} onChange={set('username')} autoComplete="username" autoFocus /></div>
        <div><Label htmlFor="d">{t('setup.display_name')}</Label><Input id="d" value={f.display_name} onChange={set('display_name')} /></div>
        <div><Label htmlFor="p" hint={t('setup.password_hint')}>{t('login.password')}</Label><Input id="p" type="password" value={f.password} onChange={set('password')} autoComplete="new-password" /></div>
        <div><Label htmlFor="a">{t('setup.again')}</Label><Input id="a" type="password" value={f.again} onChange={set('again')} autoComplete="new-password" /></div>
        <Button type="submit" variant="primary" className="w-full" disabled={busy || !f.username || !f.password}>{t('setup.submit')}</Button>
      </form>
    </AuthCard>
  )
}
