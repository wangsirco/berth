import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Box, Dot, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input } from '@/components/ui/Field'
import { post } from '@/lib/api'
import { ago } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface HostRow { name: string; online: boolean; last_seen: number; remote?: boolean; version?: string; arch?: string; addr?: string; caps?: { OS: string; Arch: string }; upgrade?: string }

export default function HostsPage() {
  const t = useT()
  const run = useChange()
  const list = useApi<{ items: HostRow[] }>('/api/v1/hosts')
  const [adding, setAdding] = useState(false)
  useEvents(['host.online', 'host.offline', 'change.done'], () => void list.reload())
  return (
    <div className="space-y-3">
      <Box title={t('hosts.title')} count={list.data?.items.length} right={<Button size="sm" variant="primary" onClick={() => setAdding(true)}>＋ {t('hosts.add')}</Button>}>
        {list.data?.items.map((h) => (
          <Row key={h.name}>
            <Dot state={h.online ? 'ok' : 'bad'} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{h.name}</div>
              <div className="text-[12px] text-mute">
                {h.remote ? t('hosts.remote', { addr: h.addr ?? '' }) : t('hosts.local')}
                {h.caps ? ` · ${h.caps.OS} · ${h.caps.Arch}` : h.arch ? ` · ${h.arch}` : ''}{h.version ? ` · berth ${h.version}` : ''}
                {!h.online && h.last_seen > 0 ? ' · ' + t('hosts.last_seen', { ago: ago(h.last_seen, t) }) : ''}
              </div>
            </div>
            {h.online && <HostLoad host={h.name} />}
            {h.online ? <Pill tone="ok">{t('hosts.online')}</Pill> : <Pill tone="bad">{t('hosts.offline')}</Pill>}
            {h.remote && h.online && h.upgrade && <Button size="sm" onClick={() => run('host.agent_update', { name: h.name }, { host: 'local', onDone: () => void list.reload() })}>{t('action.host.agent_update')}</Button>}
            {h.remote && <Button size="sm" variant="ghost" onClick={() => run('host.remove', { name: h.name }, { host: 'local', onDone: () => void list.reload(), confirmName: h.name })}>{t('action.host.remove')}</Button>}
          </Row>
        ))}
      </Box>
      <p className="text-[12px] text-mute">{t('hosts.note')}</p>
      {adding && <JoinDialog onClose={() => { setAdding(false); void list.reload() }} />}
    </div>
  )
}

function JoinDialog({ onClose }: { onClose: () => void }) {
  const t = useT()
  const [name, setName] = useState('')
  const [res, setRes] = useState<{ command: string; expires_at: number } | null>(null)
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState(false)
  const create = async () => {
    setErr('')
    try { setRes(await post('/api/v1/hosts/join-codes', { name })) } catch (e) { setErr((e as Error).message) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('hosts.add')} wide
      footer={res ? <Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button> : <><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={create} disabled={!name}>{t('hosts.make_code')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        {err && <Notice tone="bad">{err}</Notice>}
        {!res ? <>
          <p className="text-[12.5px] text-mute">{t('hosts.add_hint')}</p>
          <Field label={t('hosts.name')} hint={t('hosts.name_hint')}><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="prod" autoFocus /></Field>
        </> : <>
          <p>{t('hosts.run_this', { name })}</p>
          <pre className="num whitespace-pre-wrap break-all rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12px]">{res.command}</pre>
          <div className="flex items-center gap-2">
            <Button size="sm" onClick={() => { void navigator.clipboard.writeText(res.command); setCopied(true) }}>{copied ? t('common.copied') : t('common.copy')}</Button>
            <span className="text-[12px] text-mute">{t('hosts.expires', { time: new Date(res.expires_at * 1000).toLocaleTimeString() })}</span>
          </div>
          <p className="text-[12px] text-mute">{t('hosts.join_note')}</p>
        </>}
      </div>
    </Dialog>
  )
}

// CPU, memory and disk at a glance, for every host on one page.
function HostLoad({ host }: { host: string }) {
  const d = useApi<{ host: { cpu: number; mem_used: number; mem_total: number; disks: { mount: string; used: number; total: number }[] } }>(`/api/v1/hosts/${encodeURIComponent(host)}/overview`, [host])
  useInterval(() => void d.reload(), 10000)
  const h = d.data?.host
  if (!h) return null
  const disk = h.disks?.find((x) => x.mount === '/') ?? h.disks?.[0]
  const bar = (label: string, v: number) => (
    <span className="flex w-20 flex-col gap-0.5 text-[11px] text-mute max-sm:hidden" title={`${label} ${v.toFixed(0)}%`}>
      <span className="flex justify-between"><span>{label}</span><span className="num">{v.toFixed(0)}%</span></span>
      <span className="h-1 overflow-hidden rounded-full bg-soft"><span className={'block h-full rounded-full ' + (v > 90 ? 'bg-bad' : v > 75 ? 'bg-warn' : 'bg-acc')} style={{ width: v + '%' }} /></span>
    </span>
  )
  return <span className="flex gap-3">{bar('CPU', h.cpu)}{bar('MEM', (h.mem_used / h.mem_total) * 100)}{disk && bar('DISK', (disk.used / disk.total) * 100)}</span>
}
