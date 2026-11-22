import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { ChangeProvider, useChange } from '@/components/change/ChangeDialog'
import { Button } from '@/components/ui/Button'
import { post } from '@/lib/api'
import { bytes } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { ReauthProvider } from '../account/reauth'
import type { Guest } from './PvePanel'
import { Row, ScreenWindow, Section } from './ScreenPage'

// A PVE guest's screen in the same window as Berth's own VMs: PVE's VNC ticket, relayed by the
// panel (the browser needs no PVE login and no trust in its certificate), with the toolbar,
// keys, command library and power of the PVE API.

export const openPveScreen = (cluster: string, g: { node: string; type: string; vmid: number }) =>
  window.open(`/pve-screen/${encodeURIComponent(cluster)}/${encodeURIComponent(g.node)}/${g.type}/${g.vmid}`, `berth-pve-${cluster}-${g.vmid}`, 'popup,width=1240,height=820')

interface Item { cluster: { id: string; name: string }; guests?: Guest[] }

export default function PveScreenPage() {
  return <ChangeProvider host="local"><ReauthProvider><PveScreen /></ReauthProvider></ChangeProvider>
}

function PveScreen() {
  const t = useT()
  const run = useChange()
  const { cluster = '', node = '', type = 'qemu', vmid = '0' } = useParams()
  const list = useApi<{ items: Item[] }>('/api/v1/pve', [])
  useInterval(() => void list.reload(), 5000)
  const it = list.data?.items.find((x) => x.cluster.id === cluster)
  const g = it?.guests?.find((x) => x.node === node && String(x.vmid) === vmid)
  const detail = useApi<{ ips: string[] }>(`/api/v1/pve/${cluster}/guests/${node}/${type}/${vmid}`, [cluster, node, type, vmid, g?.status])
  const ips = detail.data?.ips ?? []
  const name = g?.name || vmid
  const on = g?.status === 'running'
  const r = { cluster, node, type, vmid: Number(vmid), name: g?.name ?? '' }
  return <ScreenWindow source={{
    name,
    sub: `PVE ${type === 'lxc' ? t('pve.lxc') : t('pve.qemu')} · ${it?.cluster.name ?? cluster} · ${node} · #${vmid}${ips[0] ? ' · ' + ips[0] : ''}`,
    connect: () => post<{ key: string; password: string }>(`/api/v1/pve/${cluster}/guests/${node}/${type}/${vmid}/console`, {})
      .then((tk) => ({ path: `/api/v1/pve/console/${tk.key}`, password: tk.password })),
    side: (io) => (
      <Section title={t('vsc.machine')}>
        <Row k={t('vms.status')} v={g ? t('vms.st_' + (g.status === 'paused' ? 'frozen' : g.status)) : '—'} />
        <Row k={t('vms.address')} v={ips.length ? ips.join(', ') : '—'} />
        {g && <Row k={t('vms.size')} v={`${g.maxcpu} ${t('vms.cpu_unit')} · ${bytes(g.maxmem)}`} />}
        {g && on && <Row k={t('vsc.cpu_used')} v={`${Math.round(g.cpu * 100)}%`} />}
        {g && on && <Row k={t('vsc.mem_used')} v={bytes(g.mem)} />}
        {g && on && g.uptime > 0 && <Row k={t('vsc.up_for')} v={<Uptime seconds={g.uptime} />} />}
        <div className="mt-2 flex flex-wrap gap-1">
          {on
            ? <>
              <Button size="sm" onClick={() => run('pve.power', { ...r, action: 'reboot' })}>{t('pve.reboot')}</Button>
              <Button size="sm" onClick={() => run('pve.power', { ...r, action: 'shutdown' })}>{t('pve.shutdown')}</Button>
              <Button size="sm" variant="ghost" onClick={() => run('pve.power', { ...r, action: 'stop' })}>{t('pve.stop')}</Button>
            </>
            : g && <Button size="sm" variant="primary" onClick={() => run('pve.power', { ...r, action: g.status === 'paused' ? 'resume' : 'start' }, { onDone: io.reconnect })}>{t('pve.start')}</Button>}
        </div>
        <div className="mt-1 text-[11.5px] text-neutral-400">{t('vsc.power_hint')}</div>
      </Section>
    ),
  }} />
}

// PVE reports uptime in seconds at each refresh; count on between them
function Uptime({ seconds }: { seconds: number }) {
  const [base, setBase] = useState({ s: seconds, at: Date.now() })
  const [now, setNow] = useState(Date.now())
  useEffect(() => setBase({ s: seconds, at: Date.now() }), [seconds])
  useEffect(() => { const i = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(i) }, [])
  const s = base.s + Math.max(0, Math.floor((now - base.at) / 1000))
  const d = Math.floor(s / 86400)
  return <>{`${d ? d + 'd ' : ''}${Math.floor(s / 3600) % 24}:${String(Math.floor(s / 60) % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`}</>
}
