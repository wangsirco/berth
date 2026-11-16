import { DiffView } from '@/components/change/DiffView'
import { EquivCommand } from '@/components/change/EquivCommand'
import { RiskBadge } from '@/components/change/RiskBadge'
import { Pill } from '@/components/ui/Box'
import { datetime } from '@/lib/format'
import { usePick, useT } from '@/lib/i18n'
import type { Change } from '@/lib/types'

export function ChangeDetail({ c }: { c: Change }) {
  const t = useT()
  const pick = usePick()
  const kv = (k: string, v: React.ReactNode) => (
    <div className="grid grid-cols-[110px_1fr] gap-2 border-b border-line px-4 py-2 text-[13px] last:border-b-0">
      <span className="text-mute">{k}</span><span className="min-w-0 break-words">{v}</span>
    </div>
  )
  return (
    <div className="space-y-4 pb-4">
      <div className="border-b border-line">
        {kv(t('audit.summary'), pick(c.summary))}
        {kv(t('audit.status'), <Pill tone={c.status === 'done' ? 'ok' : c.status === 'failed' ? 'bad' : undefined}>{t('status_change.' + c.status)}</Pill>)}
        {kv(t('audit.risk'), <RiskBadge risk={c.plan.risk} />)}
        {kv(t('audit.actor'), `${c.actor.name} · ${t('via.' + c.actor.via)} · ${c.actor.ip}`)}
        {c.actor.ai_session && kv(t('audit.ai_session'), <span className="num">{c.actor.ai_session}{c.actor.intent && ' — ' + c.actor.intent}</span>)}
        {kv(t('audit.time'), datetime(c.ts) + (c.finished_at ? ` → ${datetime(c.finished_at)}` : ''))}
        {kv(t('audit.host'), c.host)}
        {c.reverts && kv(t('audit.reverts'), <span className="num">{c.reverts}</span>)}
        {c.reverted_by && kv(t('audit.reverted_by'), <span className="num">{c.reverted_by}</span>)}
        {kv('ID', <span className="num">{c.id}</span>)}
      </div>
      <div className="space-y-3 px-4">
        {c.plan.affected?.length > 0 && <p className="text-[13px]"><b className="font-semibold">{t('change.affected')}：</b>{c.plan.affected.map((o) => `${t('obj.' + o.type)} ${o.name || o.id}`).join('、')}</p>}
        {c.plan.files?.filter((f) => f.unified).map((f) => <DiffView key={f.path} diff={f.unified} path={f.path} />)}
        {c.plan.commands?.length > 0 && <pre className="num overflow-auto rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12px]">{c.plan.commands.join('\n')}</pre>}
        <h3 className="text-[12.5px] font-semibold">{t('audit.params')}</h3>
        <pre className="num overflow-auto rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12px]">{JSON.stringify(c.params, null, 2)}</pre>
        {c.result !== undefined && c.result !== null && (<><h3 className="text-[12.5px] font-semibold">{t('audit.result')}</h3>
          <pre className="num overflow-auto rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12px]">{JSON.stringify(c.result, null, 2)}</pre></>)}
        {c.log && (<><h3 className="text-[12.5px] font-semibold">{t('tasks.log')}</h3>
          <pre className="num max-h-72 overflow-auto rounded-[var(--radius-box)] bg-term px-3 py-2 text-[12px] text-term-fg">{c.log}</pre></>)}
        <EquivCommand command={c.equivalent} />
      </div>
    </div>
  )
}
