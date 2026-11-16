import { useState } from 'react'
import { useEvents } from './useEvents'

export interface HostSample {
  ts: number; cpu: number; per_cpu: number[]; cores: number; load: [number, number, number]
  mem_total: number; mem_used: number; mem_avail: number; buffers: number; cached: number
  swap_total: number; swap_used: number; zram_total: number; zram_used: number; zram_orig: number
  disks: { mount: string; fs: string; total: number; used: number }[]; disk_r_bps: number; disk_w_bps: number
  net: { name: string; rx_bps: number; tx_bps: number }[]; temps: { label: string; c: number }[]; uptime: number
}

// virtual interfaces would count the same traffic twice
const VIRTUAL = /^(lo|veth|docker|br-|incusbr|tap|virbr|cni|flannel|tun|wg|vnet)/
export const realNet = (s: HostSample) => s.net.filter((n) => !VIRTUAL.test(n.name))
export const netRx = (s: HostSample) => realNet(s).reduce((n, i) => n + i.rx_bps, 0)
export const netTx = (s: HostSample) => realNet(s).reduce((n, i) => n + i.tx_bps, 0)
export const rootDisk = (s: HostSample) => s.disks.find((d) => d.mount === '/') ?? s.disks[0]

// useHostLive keeps the last `keep` one-second samples of a host from metrics.tick.
export function useHostLive(host: string, keep = 60) {
  const [samples, setSamples] = useState<HostSample[]>([])
  useEvents(['metrics.tick'], (_t, e) => {
    if (e?.host !== host || !e.sample) return
    setSamples((l) => [...l.slice(-(keep - 1)), e.sample as HostSample])
  })
  return { samples, last: samples[samples.length - 1] as HostSample | undefined }
}
