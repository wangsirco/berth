import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Notice } from '@/components/ui/Box'
import { errText, post } from '@/lib/api'
import { useT } from '@/lib/i18n'

// Wharf's approval page sends the browser back here with the one-time code.
export default function WharfPaired() {
  const t = useT()
  const nav = useNavigate()
  const [err, setErr] = useState('')
  useEffect(() => {
    const code = new URLSearchParams(location.search).get('code') ?? ''
    post<{ name: string }>('/api/v1/wharf/pair/finish', { code })
      .then(() => nav('/local/wharf', { replace: true }))
      .catch((e) => setErr(errText(e)))
  }, [nav])
  return <div className="mx-auto max-w-md p-8">{err ? <Notice tone="bad">{err}</Notice> : <p className="text-mute">{t('wharf.finishing')}</p>}</div>
}
