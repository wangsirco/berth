import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Button } from '@/components/ui/Button'
import { Dot, Notice } from '@/components/ui/Box'
import { post } from '@/lib/api'
import { useT } from '@/lib/i18n'
import type { DiagLayer } from './types'

const dot = (s: DiagLayer['status']) => (s === 'ok' ? 'ok' : s === 'warn' ? 'warn' : s === 'fail' ? 'bad' : 'off') as 'ok' | 'warn' | 'bad' | 'off'

export function DiagnoseView({ host, siteId }: { host: string; siteId: number }) {
  const t = useT()
  const run = useChange()
  const [busy, setBusy] = useState(false)
  const [res, setRes] = useState<{ status: string; layers: DiagLayer[] } | null>(null)
  const [err, setErr] = useState('')
  const go = async () => {
    setBusy(true)
    setErr('')
    try { setRes(await post(`/api/v1/hosts/${host}/sites/${siteId}/diagnose`)) } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }
  return (
    <div className="space-y-3 p-4">
      <div className="flex items-center gap-2">
        <Button variant="primary" onClick={go} disabled={busy}>{busy ? t('diag.running') : res ? t('diag.again') : t('diag.run')}</Button>
        <span className="text-[12.5px] text-mute">{t('diag.hint')}</span>
      </div>
      {err && <Notice tone="bad">{err}</Notice>}
      {res && <ol className="space-y-1">
        {res.layers.map((l) => (
          <li key={l.layer} className="rounded-[var(--radius-box)] border border-line px-3 py-2">
            <div className="flex items-start gap-2">
              <Dot state={dot(l.status)} className="mt-1.5" />
              <div className="min-w-0 flex-1">
                <div className="text-[12px] text-mute">{t('diag.layer_' + l.layer)}</div>
                <div className="text-[13.5px]">{l.message}</div>
                {l.detail && l.detail.length > 0 && <pre className="num mt-1 max-h-28 overflow-auto whitespace-pre-wrap text-[11.5px] text-mute">{l.detail.join('\n')}</pre>}
              </div>
              {l.fix && <Button size="sm" onClick={() => run(l.fix!.kind, l.fix!.params, { host, onDone: go })}>{l.fix.label}</Button>}
            </div>
          </li>
        ))}
      </ol>}
    </div>
  )
}
