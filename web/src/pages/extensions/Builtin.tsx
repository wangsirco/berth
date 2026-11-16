import { Bug, ShieldCheck } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

export interface BuiltinState {
  waf: { installed: boolean; edge: boolean; image?: string; sites: { id: number; domain: string }[] }
  av: { installed: boolean; db_date?: string; jobs: string[] }
}

// useBuiltin: whether this host has the WAF and virus-scanning extensions.
export function useBuiltin() {
  const { host } = useHost()
  const d = useApi<BuiltinState>(`/api/v1/hosts/${host}/extensions/builtin`, [host])
  useEvents(['change.done'], () => void d.reload())
  return d
}

// Berth's own optional extensions, as cards on the Extensions page.
export function BuiltinExtensions() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const d = useBuiltin()
  const st = d.data
  const cards = [
    {
      id: 'waf', icon: ShieldCheck, title: t('bext.waf'), desc: t('bext.waf_desc'), cost: t('bext.waf_cost'), on: !!st?.waf.installed, perm: 'sites.manage',
      note: !st ? '' : st.waf.installed ? t('bext.waf_used', { n: st.waf.sites.length }) : !st.waf.edge ? t('bext.waf_no_edge') : '', blocked: !!st && !st.waf.edge && !st.waf.installed,
    },
    {
      id: 'av', icon: Bug, title: t('bext.av'), desc: t('bext.av_desc'), cost: t('bext.av_cost'), on: !!st?.av.installed, perm: 'system.manage',
      note: st?.av.installed && st.av.db_date ? t('bext.av_db', { d: st.av.db_date.slice(0, 10) }) : '', blocked: false,
    },
  ]
  return (
    <Box title={t('bext.title')}>
      <p className="border-b border-line px-4 py-2 text-[12.5px] text-mute">{t('bext.hint')}</p>
      <div className="grid gap-3 p-4 sm:grid-cols-2">
        {cards.map((c) => (
          <div key={c.id} className="flex flex-col rounded-xl border border-line bg-canvas p-3.5">
            <div className="flex items-center gap-2 text-[14px] font-semibold"><c.icon className="size-4" />{c.title}
              {st && (c.on ? <Pill tone="ok">{t('bext.installed')}</Pill> : <Pill>{t('bext.not_installed')}</Pill>)}</div>
            <p className="mt-1.5 text-[12.5px]">{c.desc}</p>
            <p className="mt-1 text-[12px] text-mute">{c.cost}</p>
            {c.note && <p className="mt-1 text-[12px] text-mute">{c.note}</p>}
            {st && can(user, c.perm) && <div className="mt-3">
              {c.on
                ? <Button size="sm" variant="ghost" onClick={() => run(`ext.${c.id}.remove`, {}, { host })}>{t('action.ext.' + c.id + '.remove')}</Button>
                : <Button size="sm" variant="primary" disabled={c.blocked} onClick={() => run(`ext.${c.id}.install`, {}, { host })}>{t('action.ext.' + c.id + '.install')}</Button>}
            </div>}
          </div>
        ))}
      </div>
    </Box>
  )
}

// NeedsExtension: where a feature needs an extension that is not installed — what it is and one
// button to install it.
export function NeedsExtension({ id, onDone }: { id: 'waf' | 'av'; onDone?: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-xl border border-dashed border-line bg-canvas px-3 py-2.5 text-[12.5px]">
      <span className="min-w-0 flex-1 text-mute">{t('bext.need_' + id)}</span>
      {can(user, id === 'waf' ? 'sites.manage' : 'system.manage') && <Button size="sm" variant="primary" onClick={() => run(`ext.${id}.install`, {}, { host, onDone })}>{t('action.ext.' + id + '.install')}</Button>}
    </div>
  )
}
