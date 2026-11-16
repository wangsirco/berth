import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Select } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface SwapDev { name: string; type: string; size: number; used: number; prio: number }
export interface Tuning {
  sysctl: { values: Record<string, string>; file: Record<string, string>; available: Record<string, string> }
  swap: { devices: SwapDev[]; zram_size: string; zram_algo?: string; zram_custom: boolean; zram_supported: boolean; berth_swap_mb: number; mem_mb: number }
  resolver: { resolved: boolean; servers: string[] }
  auto_update: { family: string; installed: boolean; enabled: boolean; type: 'security' | 'all'; time: string }
}

// Choices for the kernel parameters that are not plain numbers.
const CHOICES: Record<string, string[]> = {
  'net.ipv4.icmp_echo_ignore_all': ['0', '1'], 'net.ipv4.ip_forward': ['0', '1'], 'net.ipv6.conf.all.forwarding': ['0', '1'], 'net.ipv4.tcp_tw_reuse': ['0', '1', '2'],
  'net.core.default_qdisc': ['fq_codel', 'fq', 'cake', 'pfifo_fast'], 'vm.overcommit_memory': ['0', '1', '2'],
}

// Kernel parameters, swap and zram (方案 §5.14).
export function Kernel() {
  const t = useT()
  const { host } = useHost()
  const d = useApi<Tuning>(`/api/v1/hosts/${host}/system/tuning`, [host])
  useEvents(['change.done'], () => void d.reload())
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  return (
    <div className="space-y-4">
      <SwapBox st={d.data.swap} />
      <SysctlBox st={d.data.sysctl} />
    </div>
  )
}

function SysctlBox({ st }: { st: Tuning['sysctl'] }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const [edit, setEdit] = useState<Record<string, string>>({})
  useEffect(() => setEdit({}), [st])
  const keys = Object.keys(st.values)
  const changed = Object.entries(edit).filter(([k, v]) => v !== st.values[k])
  const choices = (k: string) => CHOICES[k] ?? (st.available[k] ? st.available[k].split(/\s+/) : null)
  return (
    <Box title={t('kernel.sysctl')} right={<span className="text-[12px] text-mute">/etc/sysctl.d/90-berth.conf</span>}>
      {keys.map((k) => {
        const v = edit[k] ?? st.values[k]
        const ch = choices(k)
        return (
          <div key={k} className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-2 text-[13px] last:border-b-0">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2"><span className="font-semibold">{t('kernel.k.' + k)}</span>{st.file[k] !== undefined && <Pill tone="acc">{t('kernel.by_berth')}</Pill>}</div>
              <div className="text-[12px] text-mute"><span className="num">{k}</span> · {t('kernel.d.' + k)}</div>
            </div>
            {ch ? (
              <Select disabled={!manage} value={v} onChange={(e) => setEdit({ ...edit, [k]: e.target.value })}>
                {[...new Set([...ch, v])].map((c) => <option key={c} value={c}>{c}</option>)}
              </Select>
            ) : <Input disabled={!manage} className="num w-44" value={v} onChange={(e) => setEdit({ ...edit, [k]: e.target.value })} />}
            {manage && st.file[k] !== undefined && <button className="text-[12px] text-mute hover:underline" title={t('kernel.unset_hint')} onClick={() => run('sysctl.set', { values: { [k]: '' } }, { host })}>{t('kernel.unset')}</button>}
          </div>
        )
      })}
      {manage && <div className="flex items-center justify-end gap-2 border-t border-line px-4 py-2.5">
        <span className="mr-auto text-[12px] text-mute">{t('kernel.apply_hint')}</span>
        <Button size="sm" disabled={changed.length === 0} onClick={() => setEdit({})}>{t('common.reset')}</Button>
        <Button size="sm" variant="primary" disabled={changed.length === 0} onClick={() => run('sysctl.set', { values: Object.fromEntries(changed) }, { host })}>{t('kernel.apply', { n: changed.length })}</Button>
      </div>}
    </Box>
  )
}

const ZRAM = ['', 'ram', 'ram / 2', 'ram / 4', '0']

function SwapBox({ st }: { st: Tuning['swap'] }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const curZram = st.zram_custom ? st.zram_size || '0' : ''
  const [zram, setZram] = useState(curZram)
  const [mb, setMb] = useState(String(st.berth_swap_mb))
  useEffect(() => { setZram(curZram); setMb(String(st.berth_swap_mb)) }, [curZram, st.berth_swap_mb])
  const total = st.devices.reduce((s, d) => s + d.size, 0)
  const used = st.devices.reduce((s, d) => s + d.used, 0)
  const zramOpts = ZRAM.includes(zram) ? ZRAM : [...ZRAM, zram]
  return (
    <Box title={t('kernel.swap')} right={<span className="num text-[12px] text-mute">{bytes(used)} / {bytes(total)}</span>}>
      {st.devices.length === 0 && <div className="px-4 py-2.5 text-[12.5px] text-mute">{t('kernel.no_swap')}</div>}
      {st.devices.map((d) => (
        <div key={d.name} className="flex items-center gap-3 border-b border-line px-4 py-1.5 text-[12.5px]">
          <span className="num min-w-0 flex-1 truncate">{d.name}</span>
          <span className="w-24 text-mute">{d.name.startsWith('/dev/zram') ? 'zram' : d.type === 'file' ? t('kernel.swapfile') : t('kernel.partition')}</span>
          <span className="num w-32 text-right">{bytes(d.used)} / {bytes(d.size)}</span>
          <span className="num w-20 text-right text-mute">{t('kernel.prio', { n: d.prio })}</span>
        </div>
      ))}
      {manage && <div className="grid gap-3 px-4 py-3 sm:grid-cols-2">
        {st.zram_supported && <div className="flex items-center gap-2 text-[13px]">
          <span className="w-20 shrink-0">zram</span>
          <Select value={zram} onChange={(e) => setZram(e.target.value)}>
            {zramOpts.map((z) => <option key={z} value={z}>{t('kernel.zram_' + (z === '' ? 'default' : z === '0' ? 'off' : z === 'ram' ? 'ram' : z === 'ram / 2' ? 'half' : z === 'ram / 4' ? 'quarter' : 'custom'), { v: z })}</option>)}
          </Select>
          {zram !== curZram && <Button size="sm" variant="primary" onClick={() => run('swap.set', { zram }, { host })}>{t('common.apply')}</Button>}
        </div>}
        <div className="flex items-center gap-2 text-[13px]">
          <span className="w-20 shrink-0">{t('kernel.berth_file')}</span>
          <Input type="number" min={0} step={512} className="num w-28" value={mb} onChange={(e) => setMb(e.target.value)} />
          <span className="text-mute">MiB</span>
          {Number(mb) !== st.berth_swap_mb && <Button size="sm" variant="primary" onClick={() => run('swap.set', { file_mb: Number(mb) }, { host })}>{t('common.apply')}</Button>}
        </div>
        <div className="text-[12px] text-mute sm:col-span-2">{t('kernel.swap_hint', { mem: bytes(st.mem_mb * 1048576) })}</div>
      </div>}
    </Box>
  )
}
