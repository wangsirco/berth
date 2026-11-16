import { ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { useT } from '@/lib/i18n'
import { useBuiltin } from '../extensions/Builtin'

interface Res { paths: string[]; files: number; infected: { path: string; sig: string }[]; seconds: number; db_date?: string }

// AvScanButton scans the given paths with ClamAV and shows what it found.
export function AvScanButton({ paths, label, size = 'sm' }: { paths: string[]; label?: string; size?: 'sm' }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [res, setRes] = useState<Res | null>(null)
  const [moved, setMoved] = useState(false)
  const bx = useBuiltin()
  const scan = () => { setMoved(false); run('av.scan', { paths }, { host, onDone: (c) => setRes(c.result as Res) }) }
  return <>
    <Button size={size} onClick={() => (bx.data && !bx.data.av.installed ? run('ext.av.install', {}, { host, onDone: scan }) : scan())}>
      <ShieldCheck className="size-3.5" /><span className="max-sm:hidden">{label ?? t('av.scan')}</span>
    </Button>
    {res && <Dialog open onOpenChange={(o) => !o && setRes(null)} title={t('av.result')} wide
      description={t('av.summary', { files: res.files, n: res.infected.length, s: res.seconds })}
      footer={<>
        {res.infected.length > 0 && !moved && <Button variant="danger" onClick={() => run('file.trash', { paths: res.infected.map((x) => x.path) }, { host, onDone: () => setMoved(true) })}>{t('av.trash_all', { n: res.infected.length })}</Button>}
        <Button className="ml-auto" onClick={() => setRes(null)}>{t('common.close')}</Button>
      </>}>
      <div className="space-y-2 px-4 py-3">
        {res.infected.length === 0 && <Notice>{t('av.clean')}</Notice>}
        {moved && <Notice>{t('av.moved')}</Notice>}
        {res.infected.map((x) => (
          <div key={x.path} className="rounded-[var(--radius-box)] border border-line px-3 py-2">
            <div className="num break-all text-[12.5px]">{x.path}</div>
            <div className="text-[12px] text-bad">{x.sig}</div>
          </div>
        ))}
        {res.infected.length > 0 && <p className="text-[12px] text-mute">{t('av.note')}</p>}
      </div>
    </Dialog>}
  </>
}
