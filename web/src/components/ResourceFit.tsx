import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { cx } from '@/components/ui/cx'

interface Host { cores: number; mem_total: number; mem_avail: number; disks: { mount: string; total: number; used: number }[] | null }

type Level = 'ok' | 'tight' | 'low'

// ResourceFit: before installing something heavy (the desktop, a plugin, an app), what it
// would like, what this server has, and what installing it does to everything else. It
// never blocks: a small machine is allowed to be slow, as long as nobody is surprised.
export function ResourceFit({ host = 'local', cores, memMB, diskMB, runMB, impacts }: {
  host?: string
  cores?: number // recommended CPU cores
  memMB: number // recommended total memory for it to feel smooth
  diskMB: number // what it needs on disk
  runMB?: number // what it keeps using while it runs
  impacts: string[] // i18n keys: what installing / running it means for the rest
}) {
  const t = useT()
  const d = useApi<{ host: Host }>(`/api/v1/hosts/${encodeURIComponent(host)}/overview`, [host])
  const h = d.data?.host
  if (!h) return null
  const MB = 1 << 20
  const total = Math.round(h.mem_total / MB)
  const avail = Math.round(h.mem_avail / MB)
  const root = (h.disks ?? []).find((x) => x.mount === '/') ?? (h.disks ?? [])[0]
  const free = root ? Math.round((root.total - root.used) / MB) : 0
  const lvl = {
    cpu: !cores || h.cores >= cores ? 'ok' : 'tight',
    mem: total >= memMB && avail >= (runMB ?? 0) + 256 ? 'ok' : total >= memMB / 2 ? 'tight' : 'low',
    disk: !root || free >= diskMB * 1.5 + 1024 ? 'ok' : free >= diskMB ? 'tight' : 'low',
  } as Record<string, Level>
  const worst: Level = Object.values(lvl).includes('low') ? 'low' : Object.values(lvl).includes('tight') ? 'tight' : 'ok'
  const tone = { ok: 'text-ok', tight: 'text-warn', low: 'text-bad' }
  const gb = (mb: number) => (mb >= 1024 ? `${(mb / 1024).toFixed(mb % 1024 ? 1 : 0)} GB` : `${mb} MB`)
  const rows: [string, string, string, Level][] = [
    ...(cores ? [[t('fit.cpu'), t('fit.cores', { n: cores }), t('fit.cores', { n: h.cores }), lvl.cpu] as [string, string, string, Level]] : []),
    [t('fit.mem'), gb(memMB), t('fit.mem_have', { total: gb(total), avail: gb(avail) }), lvl.mem],
    [t('fit.disk'), gb(diskMB), root ? t('fit.disk_free', { free: gb(free) }) : '—', lvl.disk],
  ]
  return (
    <div className="rounded-[var(--radius-box)] border border-line bg-soft px-3.5 py-2.5 text-[12.5px]">
      <div className={cx('mb-1.5 font-semibold', tone[worst])}>{t('fit.verdict_' + worst)}</div>
      <table className="w-full">
        <thead><tr className="text-left text-[11.5px] text-mute"><th className="font-normal" /><th className="font-normal">{t('fit.recommended')}</th><th className="font-normal">{t('fit.this_server')}</th></tr></thead>
        <tbody>
          {rows.map(([k, want, have, l]) => (
            <tr key={k}><td className="py-0.5 pr-3 text-mute">{k}</td><td className="num pr-3">{want}</td><td className={cx('num', tone[l])}>{have}</td></tr>
          ))}
        </tbody>
      </table>
      <ul className="mt-1.5 list-disc space-y-0.5 pl-5 text-mute">
        {runMB ? <li>{t('fit.run', { mb: gb(runMB) })}</li> : null}
        {impacts.map((k) => <li key={k}>{t(k)}</li>)}
        {worst !== 'ok' && <li className={tone[worst]}>{t('fit.still_ok_' + worst)}</li>}
      </ul>
    </div>
  )
}
