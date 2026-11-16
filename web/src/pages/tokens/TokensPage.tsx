import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Empty, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Menu } from '@/components/ui/Menu'
import { get, post } from '@/lib/api'
import { ago, datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { TokenCreate, TokenShown, type TokenRow } from './TokenCreate'
import { Drawer } from '@/components/ui/Dialog'
import { ActivityList, useTokenActivity, type Activity } from '@/components/ai/Activity'

export default function TokensPage() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const list = useApi<{ items: TokenRow[]; resources: string[] }>('/api/v1/tokens')
  const [creating, setCreating] = useState(false)
  const [shown, setShown] = useState<{ token: string; item: TokenRow } | null>(null)
  const act = useTokenActivity()
  const working = new Set((act.data?.items ?? []).filter((x) => x.working).map((x) => x.id))
  const [timeline, setTimeline] = useState<TokenRow | null>(null)
  const want = Number(new URLSearchParams(location.search).get('t') || 0)
  if (want && !timeline && list.data) { const x = list.data.items.find((i) => i.id === want); if (x) { history.replaceState(null, '', location.pathname); setTimeout(() => setTimeline(x)) } }
  const now = Date.now() / 1000
  const active = (x: TokenRow) => !x.revoked && (!x.expires_at || x.expires_at > now)
  return (
    <div className="space-y-4">
      {shown && <TokenShown token={shown.token} item={shown.item} onClose={() => setShown(null)} />}
      <Box title={t('nav.tokens')} count={list.data?.items.filter(active).length} right={<Button size="sm" variant="primary" onClick={() => setCreating(true)}>{t('tokens.new')}</Button>}>
        {list.data?.items.length === 0 && <Empty title={t('tokens.none')}>{t('tokens.none_hint')}</Empty>}
        {list.data?.items.map((x) => (
          <Row key={x.id} className={active(x) ? '' : 'opacity-60'} onClick={() => setTimeline(x)}>
            <span className={'size-2 shrink-0 rounded-full ' + (working.has(x.id) ? 'animate-pulse bg-acc' : x.last_used_at && now - x.last_used_at < 86400 ? 'bg-ok' : 'bg-line')} title={working.has(x.id) ? t('ai.working') : undefined} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{x.name} <span className="num font-normal text-mute">{x.prefix}…</span>{working.has(x.id) && <span className="ml-2 text-[12px] font-normal text-acc">{t('ai.working')}</span>}</div>
              <div className="text-[12px] text-mute">
                {x.scopes.length ? x.scopes.join(' ') : t('tokens.no_scopes')}
                {' · '}{x.expires_at ? t('tokens.expires', { when: datetime(x.expires_at) }) : t('tokens.forever')}
                {' · '}{x.last_used_at ? t('tokens.used', { when: ago(x.last_used_at, t), ip: x.last_ip, n: x.uses }) : t('tokens.unused')}
              </div>
            </div>
            <Pill tone={x.kind === 'agt' ? 'acc' : undefined}>{t('tokens.kind_' + x.kind)}</Pill>
            {x.kind === 'agt' && <Pill>{t('tokens.policy_' + x.danger_policy)}</Pill>}
            {x.revoked ? <Pill>{t('tokens.revoked')}</Pill> : !active(x) ? <Pill tone="warn">{t('tokens.expired')}</Pill> : (
              <span onClick={(e) => e.stopPropagation()}><Menu trigger={<Button size="sm">{t('common.more')}</Button>} items={[
                { label: t('tokens.rotate'), onClick: () => void post<{ token: string; item: TokenRow }>(`/api/v1/tokens/${x.id}/rotate`).then((r) => { setShown(r); void list.reload() }) },
                { label: t('action.token.revoke'), danger: true, onClick: () => run('token.revoke', {}, { host, url: `/api/v1/tokens/${x.id}/revoke`, onDone: () => void list.reload() }) },
              ]} /></span>
            )}
          </Row>
        ))}
      </Box>
      {timeline && <Timeline tok={timeline} onClose={() => setTimeline(null)} />}
      {creating && <TokenCreate resources={list.data?.resources ?? []} onClose={() => setCreating(false)} onCreated={(r) => { setCreating(false); setShown(r); void list.reload() }} />}
    </div>
  )
}

// Timeline: everything one token did, newest first (seven days are kept).
function Timeline({ tok, onClose }: { tok: TokenRow; onClose: () => void }) {
  const t = useT()
  const d = useApi<{ items: Activity[] }>(`/api/v1/tokens/${tok.id}/activity`, [tok.id])
  const [older, setOlder] = useState<Activity[]>([])
  const items = [...(d.data?.items ?? []), ...older]
  const more = async () => {
    const last = items[items.length - 1]
    if (last) { const r = await get<{ items: Activity[] }>(`/api/v1/tokens/${tok.id}/activity?before=${last.id}`); setOlder([...older, ...r.items]) }
  }
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={t('ai.timeline_of', { name: tok.name })}>
      <div className="px-4 py-3">
        {d.data && items.length === 0 && <p className="text-[13px] text-mute">{t('ai.no_activity')}</p>}
        <ActivityList items={items} />
        {items.length >= 200 && items.length % 200 === 0 && <Button size="sm" className="mt-3" onClick={() => void more()}>{t('ai.more')}</Button>}
      </div>
    </Drawer>
  )
}
