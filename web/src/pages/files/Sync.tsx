import { ArrowRightLeft, MoreHorizontal, Plus } from 'lucide-react'
import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Dot, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Field, Input, Select, Switch } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface SyncItem { name: string; path: string; target: string; target_path: string; cron: string; delete?: boolean; address?: string; port?: number; job_id: number; enabled: boolean; last_run?: { status: string; started: number } }

const CRONS = [['0 * * * *', 'sync.every_hour'], ['0 3 * * *', 'sync.every_day'], ['0 3 * * 0', 'sync.every_week'], ['*/10 * * * *', 'sync.every_10min']] as const

function SyncDialog({ path, onClose }: { path: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const hosts = useApi<{ items: { name: string; online: boolean }[] }>('/api/v1/hosts')
  const others = (hosts.data?.items ?? []).filter((h) => h.name !== host)
  const [name, setName] = useState(path.split('/').filter(Boolean).pop()?.toLowerCase().replace(/[^a-z0-9_-]/g, '-') ?? '')
  const [target, setTarget] = useState('')
  const [tpath, setTpath] = useState(path)
  const [cron, setCron] = useState('0 3 * * *')
  const [del, setDel] = useState(false)
  const tgt = target || others.find((h) => h.online)?.name || others[0]?.name || ''
  const ok = /^[a-z0-9][a-z0-9_-]{0,40}$/.test(name) && tgt !== '' && tpath.startsWith('/')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('action.sync.set')} description={t('sync.dialog_hint')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!ok} onClick={() => { onClose(); run('sync.set', { name, path, target: tgt, target_path: tpath, cron, delete: del }, { host }) }}>{t('common.next')}</Button></>}>
      <div className="pb-2">
        <Field label={t('sync.name')}><Input className="num" value={name} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <Field label={t('sync.from')}><Input className="num" value={path} disabled /></Field>
        <Field label={t('sync.target')} hint={others.length === 0 ? t('sync.no_hosts') : undefined}>
          <Select value={tgt} onChange={(e) => setTarget(e.target.value)} aria-label={t('sync.target')}>
            {others.map((h) => <option key={h.name} value={h.name} disabled={!h.online}>{h.name}{h.online ? '' : ' · ' + t('hosts.offline')}</option>)}
          </Select>
        </Field>
        <Field label={t('sync.target_path')}><Input className="num" value={tpath} onChange={(e) => setTpath(e.target.value)} /></Field>
        <Field label={t('sync.when')}>
          <Select value={cron} onChange={(e) => setCron(e.target.value)} aria-label={t('sync.when')}>
            {CRONS.map(([c, k]) => <option key={c} value={c}>{t(k)}</option>)}
          </Select>
        </Field>
        <div className="px-4 py-2"><Switch checked={del} onChange={setDel} label={t('sync.delete')} /></div>
        {del && <p className="px-4 text-[12px] text-warn">{t('sync.delete_warn')}</p>}
      </div>
    </Dialog>
  )
}

export function SyncButton({ path }: { path: string }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState(false)
  const d = useApi<{ items: SyncItem[] }>(open ? `/api/v1/hosts/${host}/syncs` : null, [host, open])
  useEvents(['change.done', 'cron.run'], () => { if (open) void d.reload() })
  const items = d.data?.items ?? []
  return <>
    <Button size="sm" onClick={() => setOpen(true)}><ArrowRightLeft className="size-3.5" /><span className="max-sm:hidden">{t('sync.title')}</span></Button>
    {open && <Drawer open onOpenChange={(o) => !o && setOpen(false)} title={t('sync.title')}
      right={<Button size="sm" variant="primary" onClick={() => setForm(true)}><Plus className="size-3.5" />{t('action.sync.set')}</Button>}>
      <p className="px-4 pt-3 text-[12.5px] text-mute">{t('sync.hint')}</p>
      {items.length === 0 && d.data && <p className="px-4 py-3 text-[12.5px] text-mute">{t('sync.empty')}</p>}
      <div className="mt-2">
        {items.map((s) => (
          <div key={s.job_id} className="flex items-center gap-3 border-b border-line px-4 py-2.5">
            <Dot state={!s.last_run ? 'off' : s.last_run.status === 'ok' ? 'ok' : s.last_run.status === 'running' ? 'busy' : 'bad'} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 font-semibold">{s.name}{s.delete && <Pill tone="warn">{t('sync.mirror')}</Pill>}</div>
              <div className="num truncate text-[12px] text-mute">{s.path} → {s.target}:{s.target_path}</div>
              <div className="num text-[11.5px] text-mute">{s.cron}{s.last_run ? ' · ' + t('sync.last', { ago: ago(s.last_run.started, t), status: s.last_run.status }) : ''}</div>
            </div>
            <Menu trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} items={[
              { label: t('sync.run_now'), onClick: () => run('cron.run', { id: s.job_id }, { host }) },
              { label: t('action.sync.delete'), danger: true, onClick: () => run('sync.delete', { job: s.job_id }, { host }) },
            ]} />
          </div>
        ))}
      </div>
      {form && <SyncDialog path={path} onClose={() => setForm(false)} />}
    </Drawer>}
  </>
}
