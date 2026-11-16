import { Check, Copy, Link2 } from 'lucide-react'
import { useState } from 'react'
import { Box, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select } from '@/components/ui/Field'
import { del, post } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

export interface Quota { sites?: number; containers?: number; vms?: number; cpus?: number; mem_mb?: number; disk_gb?: number; traffic_gb?: number }
interface Preset { key: string; role: 'admin' | 'viewer'; perms: string[]; quota: Quota; simple?: boolean }
interface Invite { id: number; created_by: string; preset: string; role: string; perms: string[]; quota: Quota; simple: boolean; note: string; max_uses: number; uses: number; accepted: string[]; expires_at: number; created_at: number; revoked: boolean }

// what an invitation (or a preset) lets someone do, in words: one line per area
export function PermSummary({ role, perms, quota, simple }: { role: string; perms: string[]; quota: Quota; simple?: boolean }) {
  const t = useT()
  if (simple) return <ul className="list-disc pl-5"><li>{t('inv.simple_d')}</li></ul>
  if (role === 'viewer') return <ul className="list-disc pl-5"><li>{t('inv.viewer_d')}</li></ul>
  const groups = new Map<string, string[]>()
  for (const p of perms) {
    const [g, a] = p.split('.')
    groups.set(g, [...(groups.get(g) ?? []), t('perm.' + g + '.' + a)])
  }
  const q = Object.entries(quota ?? {}).filter(([, v]) => v)
  return (
    <ul className="list-disc space-y-0.5 pl-5">
      {[...groups].map(([g, as]) => <li key={g}><b className="font-semibold">{t('permgroup.' + g)}</b>：{as.join('、')}</li>)}
      {q.length > 0 && <li>{t('inv.quota')}：{q.map(([k, v]) => t('inv.q_' + k, { n: v })).join('、')}</li>}
    </ul>
  )
}

export function InvitesBox() {
  const t = useT()
  const list = useApi<{ items: Invite[]; presets: Preset[]; public: boolean }>('/api/v1/invites')
  const [open, setOpen] = useState(false)
  const now = Date.now() / 1000
  const state = (i: Invite) => i.revoked ? 'revoked' : i.uses >= i.max_uses ? 'used' : i.expires_at <= now ? 'expired' : 'open'
  const items = list.data?.items ?? []
  return (
    <>
      <Box title={t('inv.title')} count={items.filter((i) => state(i) === 'open').length} right={<Button size="sm" onClick={() => setOpen(true)}><Link2 className="size-3.5" />{t('inv.new')}</Button>}>
        {items.length === 0 && <p className="px-4 py-3 text-[12.5px] text-mute">{t('inv.empty')}</p>}
        {items.slice(0, 20).map((i) => (
          <Row key={i.id}>
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{t('inv.preset_' + i.preset)}{i.note && <span className="font-normal text-mute"> · {i.note}</span>}</div>
              <div className="text-[12px] text-mute">{t('inv.row', { by: i.created_by, uses: i.uses, max: i.max_uses, at: new Date(i.expires_at * 1000).toLocaleString() })}{i.accepted.length > 0 && ' · ' + t('inv.joined', { names: i.accepted.join('、') })}</div>
            </div>
            <Pill tone={state(i) === 'open' ? 'ok' : undefined}>{t('inv.state_' + state(i))}</Pill>
            {state(i) === 'open' && <Button size="sm" variant="ghost" onClick={() => void del(`/api/v1/invites/${i.id}`).then(() => list.reload())}>{t('inv.revoke')}</Button>}
          </Row>
        ))}
      </Box>
      {open && list.data && <InviteDialog presets={list.data.presets} isPublic={list.data.public} onClose={() => { setOpen(false); void list.reload() }} />}
    </>
  )
}

function InviteDialog({ presets, isPublic, onClose }: { presets: Preset[]; isPublic: boolean; onClose: () => void }) {
  const t = useT()
  const [preset, setPreset] = useState('family')
  const [note, setNote] = useState('')
  const [days, setDays] = useState('7')
  const [max, setMax] = useState('1')
  const [res, setRes] = useState<{ url: string; invite: Invite } | null>(null)
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState('')
  const p = presets.find((x) => x.key === preset)!
  const create = async () => {
    setErr('')
    try { setRes(await post('/api/v1/invites', { preset, note, days: +days, max_uses: +max })) } catch (e) { setErr((e as Error).message) }
  }
  const copy = (k: string, s: string) => { void navigator.clipboard.writeText(s); setCopied(k) }
  const text = res ? t('inv.share_text', { what: t('inv.preset_' + preset), url: res.url, at: new Date(res.invite.expires_at * 1000).toLocaleString() }) : ''
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('inv.new')} wide
      footer={res ? <Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button> : <><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => void create()}>{t('inv.make')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {err && <Notice tone="bad">{err}</Notice>}
        {!res ? <>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {presets.map((x) => (
              <button key={x.key} type="button" aria-pressed={preset === x.key} onClick={() => setPreset(x.key)} className={cx('rounded-[var(--radius-box)] border p-2.5 text-left', preset === x.key ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
                <div className="font-semibold">{t('inv.preset_' + x.key)}</div>
                <div className="text-[12px] text-mute">{t('inv.preset_' + x.key + '_d')}</div>
              </button>
            ))}
          </div>
          <div className="rounded-[var(--radius-box)] border border-line bg-soft px-3.5 py-2.5 text-[12.5px]">
            <div className="mb-1 font-semibold">{t('inv.can')}</div>
            <PermSummary role={p.role} perms={p.perms} quota={p.quota} simple={p.simple} />
            <p className="mt-1.5 text-mute">{t('inv.change_later')}</p>
          </div>
          <div className="grid grid-cols-1 gap-x-3 sm:grid-cols-3">
            <Field label={t('inv.note')}><Input value={note} onChange={(e) => setNote(e.target.value)} placeholder={t('inv.note_ph')} /></Field>
            <Field label={t('inv.days')}><Select value={days} onChange={(e) => setDays(e.target.value)}>{['1', '3', '7', '30'].map((d) => <option key={d} value={d}>{t('inv.n_days', { n: d })}</option>)}</Select></Field>
            <Field label={t('inv.max')}><Select value={max} onChange={(e) => setMax(e.target.value)}>{['1', '3', '5', '10'].map((d) => <option key={d} value={d}>{t('inv.n_people', { n: d })}</option>)}</Select></Field>
          </div>
          {!isPublic && <Notice tone="warn">{t('inv.private_warn')}</Notice>}
        </> : <>
          {/* the invite card: what the friend will see, ready to screenshot or copy */}
          <div className="mx-auto max-w-md overflow-hidden rounded-[14px] border border-line bg-card shadow-sm">
            <div className="bg-acc/15 px-5 py-4">
              <div className="text-[12px] text-mute">{t('inv.card_from', { by: res.invite.created_by })}</div>
              <div className="mt-0.5 text-[17px] font-semibold">{t('inv.card_title', { what: t('inv.preset_' + preset) })}</div>
              {note && <div className="mt-1 text-[12.5px]">{note}</div>}
            </div>
            <div className="space-y-2 px-5 py-3.5 text-[12.5px]">
              <PermSummary role={res.invite.role} perms={res.invite.perms} quota={res.invite.quota} simple={res.invite.simple} />
              <div className="num break-all rounded-[8px] border border-line bg-soft px-2.5 py-1.5 text-[11.5px]">{res.url}</div>
              <div className="text-[11.5px] text-mute">{t('inv.card_valid', { at: new Date(res.invite.expires_at * 1000).toLocaleString(), n: res.invite.max_uses })}</div>
            </div>
          </div>
          <div className="flex flex-wrap justify-center gap-2">
            <Button size="sm" variant="primary" onClick={() => copy('url', res.url)}>{copied === 'url' ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}{t('inv.copy_link')}</Button>
            <Button size="sm" onClick={() => copy('text', text)}>{copied === 'text' ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}{t('inv.copy_text')}</Button>
          </div>
          <p className="text-center text-[12px] text-mute">{t('inv.once')}</p>
        </>}
      </div>
    </Dialog>
  )
}
