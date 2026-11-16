import { useState } from 'react'
import { Copy } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented } from '@/components/ui/Field'
import { datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Share { id: string; path: string; created_by: string; expires_at: number; max_downloads: number; downloads: number; password: boolean }

// A temporary public download link for one file (方案 §5.6), with the links already made.
export function ShareDialog({ path, onClose }: { path: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [hours, setHours] = useState('24')
  const [max, setMax] = useState('')
  const [pw, setPw] = useState('')
  const [made, setMade] = useState<string | null>(null)
  const list = useApi<{ items: Share[] }>(`/api/v1/hosts/${host}/shares`, [host])
  useEvents(['change.done'], () => void list.reload())
  const mine = (list.data?.items ?? []).filter((s) => s.path === path && s.expires_at > Date.now() / 1000)
  const create = () => run('file.share', { path, hours: Number(hours), max_downloads: Number(max || 0), password: pw || undefined }, {
    host, onDone: (c) => { const r = c.result as { url?: string } | undefined; if (r?.url) setMade(r.url); void list.reload() },
  })
  const copy = (s: string) => void navigator.clipboard?.writeText(s)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('share.title', { name: path.split('/').pop() ?? path })}
      footer={<><Button onClick={onClose}>{t('common.close')}</Button><Button variant="primary" onClick={create}>{t('action.file.share')}</Button></>}>
      <div className="space-y-3">
        {made && <Notice><div className="flex items-center gap-2"><span className="num min-w-0 flex-1 break-all text-[12.5px]">{made}</span><Button size="sm" onClick={() => copy(made)}><Copy className="size-3.5" />{t('share.copy')}</Button></div></Notice>}
        <Field label={t('share.valid')}>
          <Segmented value={hours} onChange={setHours} options={[['1', '1h'], ['24', '1d'], ['168', '7d'], ['720', '30d']].map(([v, l]) => ({ value: v, label: t('share.h_' + l) }))} />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('share.max')}><Input type="number" min={0} value={max} placeholder={t('users.no_limit')} onChange={(e) => setMax(e.target.value)} /></Field>
          <Field label={t('share.pw')}><Input type="password" autoComplete="new-password" value={pw} placeholder={t('share.pw_none')} onChange={(e) => setPw(e.target.value)} /></Field>
        </div>
        <p className="text-[12px] text-mute">{t('share.hint')}</p>
        {mine.length > 0 && <div className="space-y-1 border-t border-line pt-2">
          {mine.map((s) => (
            <div key={s.id} className="flex items-center gap-2 text-[12.5px]">
              <span className="flex-1">{t('share.until', { at: datetime(s.expires_at) })} · {s.max_downloads ? `${s.downloads}/${s.max_downloads}` : s.downloads}{s.password ? ' · ' + t('share.pw') : ''}</span>
              <button className="text-acc hover:underline" onClick={() => copy(location.origin + '/s/' + s.id)}>{t('share.copy')}</button>
              <button className="text-bad hover:underline" onClick={() => run('file.unshare', { id: s.id }, { host })}>{t('action.file.unshare')}</button>
            </div>
          ))}
        </div>}
      </div>
    </Dialog>
  )
}
