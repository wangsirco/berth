import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { Notice } from '@/components/ui/Box'
import { ApiError, post } from '@/lib/api'
import { datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { AuthCard } from './AuthCard'

export default function CliAuthPage() {
  const t = useT()
  const code = useSearchParams()[0].get('code') ?? ''
  const pending = useApi<{ device: string; ip: string; created: number }>(`/api/v1/cli/pending?code=${encodeURIComponent(code)}`)
  const [done, setDone] = useState<'allowed' | 'denied' | null>(null)
  const [err, setErr] = useState('')
  const answer = async (allow: boolean) => {
    try { await post('/api/v1/cli/approve', { code, allow }); setDone(allow ? 'allowed' : 'denied') } catch (e) { setErr((e as ApiError).message) }
  }
  return (
    <AuthCard title={t('cli.title')} sub={<>{t('cli.code')} <b className="num text-fg">{code}</b></>}>
      {err && <Notice tone="bad">{err}</Notice>}
      {pending.error && <Notice tone="bad">{pending.error.message}</Notice>}
      {done ? <Notice>{t(done === 'allowed' ? 'cli.allowed' : 'cli.denied')}</Notice> : pending.data && (
        <div className="space-y-3 text-[13.5px]">
          <p>{t('cli.request', { device: pending.data.device || '?', ip: pending.data.ip, time: datetime(pending.data.created) })}</p>
          <p className="text-mute">{t('cli.scope')}</p>
          <div className="flex gap-2">
            <Button onClick={() => void answer(false)}>{t('cli.deny')}</Button>
            <Button variant="primary" className="ml-auto" onClick={() => void answer(true)}>{t('cli.allow')}</Button>
          </div>
        </div>
      )}
    </AuthCard>
  )
}
