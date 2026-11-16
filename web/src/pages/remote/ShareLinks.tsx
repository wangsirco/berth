import { Check, Copy } from 'lucide-react'
import { useState } from 'react'
import { Box, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select, Switch } from '@/components/ui/Field'
import { del, post } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

interface Share { id: number; by: string; name: string; control: boolean; password: boolean; uses: number; last_used: number; expires_at: number }

// Share a remote desktop (a VM's desktop too) with a link: no account needed.
export function ShareDialog({ conn, onClose }: { conn: { id: number; name: string }; onClose: () => void }) {
  const t = useT()
  const [control, setControl] = useState(false)
  const [hours, setHours] = useState('24')
  const [pw, setPw] = useState('')
  const [url, setUrl] = useState('')
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState(false)
  const make = async () => {
    setErr('')
    try { setUrl((await post<{ url: string }>('/api/v1/shares', { kind: 'rd', target_id: conn.id, control, hours: +hours, password: pw })).url) } catch (e) { setErr((e as Error).message) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('shr.title', { name: conn.name })}
      footer={url ? <Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button> : <><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => void make()}>{t('shr.make')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {err && <Notice tone="bad">{err}</Notice>}
        {!url ? <>
          <p className="text-[12.5px] text-mute">{t('shr.hint')}</p>
          <div className="px-0.5"><Switch checked={control} onChange={setControl} label={t('shr.allow_control')} /></div>
          {control && <Notice tone="warn">{t('shr.control_warn')}</Notice>}
          <Field label={t('shr.valid')}><Select value={hours} onChange={(e) => setHours(e.target.value)}>{['1', '4', '24', '72', '168'].map((h) => <option key={h} value={h}>{t('shr.h_' + h)}</option>)}</Select></Field>
          <Field label={t('shr.password_opt')} hint={t('shr.password_hint')}><Input type="text" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
        </> : <>
          <p>{t('shr.ready')}</p>
          <div className="num break-all rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12px]">{url}</div>
          <Button size="sm" variant="primary" onClick={() => { void navigator.clipboard.writeText(url); setCopied(true) }}>{copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}{t('common.copy')}</Button>
          {pw && <p className="text-[12px] text-mute">{t('shr.tell_pw')}</p>}
        </>}
      </div>
    </Dialog>
  )
}

export function ShareList() {
  const t = useT()
  const l = useApi<{ items: Share[] }>('/api/v1/shares')
  if (!l.data?.items.length) return null
  return (
    <Box title={t('shr.list')} count={l.data.items.length}>
      {l.data.items.map((s) => (
        <Row key={s.id}>
          <div className="min-w-0 flex-1">
            <div className="text-[13.5px] font-semibold">{s.name}</div>
            <div className="text-[12px] text-mute">{t('shr.row', { by: s.by, at: new Date(s.expires_at * 1000).toLocaleString(), n: s.uses })}</div>
          </div>
          <Pill tone={s.control ? 'warn' : undefined}>{t(s.control ? 'shr.control' : 'shr.watch')}</Pill>
          {s.password && <Pill>{t('shr.has_pw')}</Pill>}
          <Button size="sm" variant="ghost" onClick={() => void del(`/api/v1/shares/${s.id}`).then(() => l.reload())}>{t('shr.revoke')}</Button>
        </Row>
      ))}
    </Box>
  )
}
