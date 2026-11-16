import { ResourceFit } from '@/components/ResourceFit'
import { Check } from 'lucide-react'
import { cx } from '@/components/ui/cx'
import { useT } from '@/lib/i18n'

export interface DeskItem { id: string; group: 'look' | 'apps' | 'dev' | 'net'; preset: string; size_mb: number; installed: boolean }
export interface DesktopInfo {
  status: { supported: boolean; installed: boolean; enabled: boolean; running: boolean; user?: string; port: number; mem_bytes?: number }
  items: DeskItem[]; presets: { basic: string[]; dev: string[] }
}

const GROUPS = ['apps', 'dev', 'net'] as const
export const BASE_MB = 300 // Xfce + VNC

// the size of what still has to be downloaded
export function pickSize(info: DesktopInfo, pick: string[]) {
  return (info.status.installed ? 0 : BASE_MB) + info.items.filter((i) => pick.includes(i.id) && !i.installed).reduce((n, i) => n + i.size_mb, 0)
}

// Preset buttons, then every item with what it contains and its size. Installed items are
// shown ticked and cannot be unticked (removing single programs is not offered).
export function SoftwarePicker({ info, pick, onChange }: { info: DesktopInfo; pick: string[]; onChange: (ids: string[]) => void }) {
  const t = useT()
  const has = (id: string) => pick.includes(id) || info.items.find((i) => i.id === id)?.installed
  const toggle = (id: string) => onChange(pick.includes(id) ? pick.filter((x) => x !== id) : [...pick, id])
  const preset = (p: 'basic' | 'dev') => onChange(info.presets[p])
  const isPreset = (p: 'basic' | 'dev') => info.presets[p].length === pick.length && info.presets[p].every((x) => pick.includes(x))
  return (
    <div className="space-y-3 text-[13px]">
      <ResourceFit cores={2} memMB={2048} runMB={info.status.installed ? undefined : 400} diskMB={Math.round(pickSize(info, pick) * 2.5)} impacts={['fit.desk_install', 'fit.desk_run', 'fit.desk_browser']} />
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {(['basic', 'dev'] as const).map((p) => (
          <button key={p} type="button" onClick={() => preset(p)} className={cx('rounded-[var(--radius-box)] border p-2.5 text-left', isPreset(p) ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
            <div className="font-semibold">{t('desktop.preset_' + p)}</div>
            <div className="text-[12px] text-mute">{t('desktop.preset_' + p + '_hint')}</div>
          </button>
        ))}
      </div>
      {GROUPS.map((g) => (
        <div key={g}>
          <div className="mb-1 text-[12px] font-semibold text-mute">{t('desktop.group_' + g)}</div>
          <div className="grid grid-cols-1 gap-1 sm:grid-cols-2">
            {info.items.filter((i) => i.group === g).map((i) => (
              <label key={i.id} className={cx('flex cursor-pointer items-start gap-2 rounded-[var(--radius-box)] border px-2.5 py-1.5', has(i.id) ? 'border-acc/60' : 'border-line')}>
                <input type="checkbox" className="mt-0.5" checked={!!has(i.id)} disabled={i.installed} onChange={() => toggle(i.id)} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5 font-semibold">{t('desktop.item.' + i.id)}
                    {i.installed && <span className="inline-flex items-center gap-0.5 text-[11px] font-normal text-ok"><Check className="size-3" />{t('desktop.installed')}</span>}
                  </div>
                  <div className="text-[11.5px] text-mute">{t('desktop.item_hint.' + i.id)}</div>
                </div>
                <span className="num shrink-0 text-[11px] text-mute">{i.size_mb} MB</span>
              </label>
            ))}
          </div>
        </div>
      ))}
      <div className="text-[12px] text-mute">{t('desktop.look_note')} · {t('desktop.total', { mb: pickSize(info, pick) })}</div>
    </div>
  )
}
