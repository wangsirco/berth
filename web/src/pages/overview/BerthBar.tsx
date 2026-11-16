import { Tooltip } from '@base-ui/react/tooltip'
import { useNavigate } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { bytes, pct } from '@/lib/format'
import { useT } from '@/lib/i18n'
import type { Berth, Host } from '@/lib/types'

export interface Segment { key: string; label: string; bytes: number; pct: number; kind: 'pipeline' | 'vm' | 'app' | 'sys' | 'more' | 'free'; color: string; cpu: number; members: string[]; target?: string }

const APP_COLORS = ['var(--berth-app)', 'var(--berth-app-2)', 'var(--berth-app-3)']
const SMALL = 0.03

// layoutBerths turns the agent's berths into bar segments: one per compose project or
// standalone container (largest first), "…N" for the slivers under 3%, one "system" for
// services + kernel + sessions, and free memory last (方案 §5.1).
export function layoutBerths(items: Berth[], total: number): Segment[] {
  const apps = new Map<string, Segment>()
  const vms: Segment[] = []
  let sys: Segment | null = null
  let free: Segment | null = null
  for (const b of items) {
    if (b.type === 'container') {
      const key = b.project || b.name
      const s = apps.get(key) ?? { key: 'app:' + key, label: key, bytes: 0, pct: 0, kind: 'app' as const, color: '', cpu: 0, members: [], target: b.project ? undefined : b.name }
      s.bytes += b.mem_bytes
      s.cpu += b.cpu
      s.members.push(b.name)
      apps.set(key, s)
    } else if (b.type === 'vm') {
      vms.push({ key: 'vm:' + b.name, label: b.name, bytes: b.mem_bytes, pct: 0, kind: 'vm', color: 'var(--berth-vm)', cpu: b.cpu, members: [b.name] })
    } else if (b.type === 'free') {
      free = { key: 'free', label: 'free', bytes: b.mem_bytes, pct: 0, kind: 'free', color: '', cpu: 0, members: [] }
    } else {
      sys ??= { key: 'sys', label: 'system', bytes: 0, pct: 0, kind: 'sys', color: 'var(--berth-sys)', cpu: 0, members: [] }
      sys.bytes += b.mem_bytes
      sys.cpu += b.cpu
      if (b.type === 'service' || b.type === 'user') sys.members.push(b.name)
    }
  }
  const sum = total || items.reduce((a, b) => a + b.mem_bytes, 0) || 1
  const sorted = [...apps.values()].sort((a, b) => b.bytes - a.bytes)
  const big = sorted.filter((s) => s.bytes / sum >= SMALL)
  const small = sorted.filter((s) => s.bytes / sum < SMALL)
  const out: Segment[] = big.map((s, i) => ({ ...s, color: APP_COLORS[i % APP_COLORS.length] }))
  if (small.length) {
    out.push({ key: 'more', label: '…' + small.length, bytes: small.reduce((a, s) => a + s.bytes, 0), pct: 0, kind: 'more', color: 'var(--berth-app-3)', cpu: small.reduce((a, s) => a + s.cpu, 0), members: small.flatMap((s) => s.members) })
  }
  out.push(...vms.filter((v) => v.bytes / sum >= SMALL / 3))
  if (sys) out.push(sys)
  if (free) out.push(free)
  const all = out.reduce((a, s) => a + s.bytes, 0) || 1
  for (const s of out) s.pct = (s.bytes / all) * 100
  return out
}

export function BerthBar({ berths, host }: { berths: Berth[]; host?: Host }) {
  const t = useT()
  const nav = useNavigate()
  const { host: h } = useHost()
  const segs = layoutBerths(berths, host ? host.mem_used + host.mem_avail : 0)
  const label = (s: Segment) => (s.kind === 'more' ? t('berth.more', { n: s.members.length }) : s.kind === 'sys' ? t('berth.system') : s.kind === 'free' ? t('berth.free') : s.label)
  return (
    <section className="-mx-3 border-b border-line px-3 pb-3.5 pt-1 sm:-mx-6 sm:px-6" aria-label={t('berth.title')}>
      <div className="mb-2 flex flex-wrap justify-between gap-2 text-[13px] text-mute">
        <span><b className="font-semibold text-fg">{t('berth.title')}</b>　{t('berth.sub')}</span>
        {host && (
          <span className="num text-[12px]">
            {bytes(host.mem_used)} / {bytes(host.mem_total)}
            {host.swap_total > 0 && ` · ${t('berth.swap')} ${bytes(host.swap_used)} / ${bytes(host.swap_total)}`}
            {host.zram_total > 0 && ` · zram ${bytes(host.zram_orig)}→${bytes(host.zram_used)}`}
          </span>
        )}
      </div>
      <Tooltip.Provider delay={150}>
        <div className="flex h-[34px] overflow-hidden rounded-[var(--radius-box)] border border-line"
          style={{ background: 'repeating-linear-gradient(135deg, var(--berth-free-a) 0 6px, var(--berth-free-b) 6px 7px)' }}>
          {segs.filter((s) => s.kind !== 'free').map((s, i) => (
            <Tooltip.Root key={s.key}>
              <Tooltip.Trigger
                render={<button aria-label={`${label(s)} ${bytes(s.bytes)}`} />}
                onClick={() => (s.target ? nav(hostPath(h, 'containers/c/' + encodeURIComponent(s.target))) : s.kind === 'app' ? nav(hostPath(h, 'containers/compose/' + encodeURIComponent(s.label))) : s.kind === 'vm' ? nav(hostPath(h, 'vms')) : undefined)}
                style={{ width: s.pct + '%', background: s.color }}
                className={`flex min-w-0 flex-col justify-center overflow-hidden whitespace-nowrap px-2 text-left text-[11.5px] leading-tight ${i % 3 === 2 && s.kind === 'app' ? 'text-[#10302c] dark:text-white' : 'text-white'} ${s.kind === 'more' ? 'text-[#10302c] dark:text-white' : ''} border-r-2 border-canvas`}>
                {s.pct > 6 && <span className="truncate">{label(s)}</span>}
                {s.pct > 3 && <span className="num truncate text-[10.5px] opacity-85">{bytes(s.bytes)}</span>}
              </Tooltip.Trigger>
              <Tooltip.Portal>
                <Tooltip.Positioner sideOffset={6} className="z-50">
                  <Tooltip.Popup className="max-w-xs rounded-[var(--radius-box)] border border-line bg-canvas px-3 py-2 text-[12px] shadow-[0_8px_24px_rgba(66,74,83,.18)]">
                    <div className="font-semibold">{label(s)}</div>
                    <div className="num text-mute">{bytes(s.bytes)} · {pct(s.pct, 1)} · CPU {pct(s.cpu, 1)}</div>
                    {s.members.length > 1 && <div className="mt-1 text-mute">{s.members.slice(0, 12).join('、')}{s.members.length > 12 && ' …'}</div>}
                  </Tooltip.Popup>
                </Tooltip.Positioner>
              </Tooltip.Portal>
            </Tooltip.Root>
          ))}
        </div>
      </Tooltip.Provider>
      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-mute">
        <span><i className="mr-1.5 inline-block size-2.5 align-[-1px]" style={{ background: 'var(--berth-app)' }} />{t('berth.legend_app')}</span>
        <span><i className="mr-1.5 inline-block size-2.5 align-[-1px]" style={{ background: 'var(--berth-sys)' }} />{t('berth.legend_sys')}</span>
        <span><i className="mr-1.5 inline-block size-2.5 border border-line align-[-1px]" style={{ background: 'repeating-linear-gradient(135deg, var(--berth-free-a) 0 2px, var(--berth-free-b) 2px 3px)' }} />{t('berth.legend_free')}</span>
        <span><i className="mr-1.5 inline-block size-2.5 align-[-1px]" style={{ background: 'var(--berth-vm)' }} />{t('berth.legend_vm')}</span>
      </div>
    </section>
  )
}
