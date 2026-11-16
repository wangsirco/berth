import { AlertTriangle, BookMarked, Pencil, Play, Plus, Search, Star, Trash2, Users, CornerDownLeft, TextCursorInput } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Switch, Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { del, errText, get, post, put } from '@/lib/api'
import { useLocale, useT } from '@/lib/i18n'
import { fill, LIBRARY, placeholders } from './library'

export interface Snippet { id: number; name: string; command: string; note: string; folder: string; danger: boolean; shared: boolean; mine: boolean; owner?: string; uses: number }
type Draft = Omit<Snippet, 'id' | 'mine' | 'uses' | 'owner'> & { id?: number }

let cache: { items: Snippet[]; can_share: boolean } | null = null

// useSnippets: the saved commands, shared across open terminals; the old per-browser list moves in once.
export function useSnippets() {
  const [data, setData] = useState(cache)
  const reload = async () => { cache = await get<{ items: Snippet[]; can_share: boolean }>('/api/v1/snippets'); setData(cache) }
  useEffect(() => {
    void (async () => {
      const old = JSON.parse(localStorage.getItem('berth.snippets') ?? '[]') as string[]
      if (old.length) {
        for (const c of old) await post('/api/v1/snippets', { name: c.slice(0, 40), command: c }).catch(() => {})
        localStorage.removeItem('berth.snippets')
      }
      await reload().catch(() => {})
    })()
  }, [])
  return { data, reload }
}

// CommandLibrary: your commands, the team's, and a starter set. Insert puts the command at the
// prompt; Run also presses Enter. Commands marked dangerous ask first.
export function CommandLibrary({ onInsert, selection, onClose }: { onInsert: (text: string, run: boolean) => void; selection?: () => string; onClose?: () => void }) {
  const t = useT()
  const [locale] = useLocale()
  const { data, reload } = useSnippets()
  const [tab, setTab] = useState<'mine' | 'team' | 'lib'>('mine')
  const [q, setQ] = useState('')
  const [edit, setEdit] = useState<Draft | null>(null)
  const [ask, setAsk] = useState<{ cmd: string; run: boolean; danger: boolean; id?: number } | null>(null)
  const items = data?.items ?? []
  const mine = items.filter((x) => x.mine && !x.shared)
  const team = items.filter((x) => x.shared)
  const match = (s: string) => !q || s.toLowerCase().includes(q.toLowerCase())
  useEffect(() => { if (data && mine.length === 0 && tab === 'mine' && !q) setTab(team.length ? 'team' : 'lib') }, [data]) // eslint-disable-line react-hooks/exhaustive-deps

  const use = (cmd: string, run: boolean, danger: boolean, id?: number) => {
    if (placeholders(cmd).length || (run && danger)) { setAsk({ cmd, run, danger, id }); return }
    send(cmd, run, id)
  }
  const send = (text: string, run: boolean, id?: number) => {
    onInsert(text, run)
    if (id) void post(`/api/v1/snippets/${id}/used`).then(reload).catch(() => {})
  }
  const groups = useMemo(() => {
    const list = tab === 'mine' ? mine : team
    const m = new Map<string, Snippet[]>()
    for (const s of list.filter((x) => match(x.name) || match(x.command))) m.set(s.folder, [...(m.get(s.folder) ?? []), s])
    return [...m.entries()].sort(([a], [b]) => (a === '' ? -1 : b === '' ? 1 : a.localeCompare(b)))
  }, [tab, items, q]) // eslint-disable-line react-hooks/exhaustive-deps

  const row = (key: string, name: string, cmd: string, danger: boolean, extra: React.ReactNode, id?: number) => (
    <div key={key} className="group border-b border-line px-3 py-2 last:border-b-0">
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{name}</span>
        {danger && <AlertTriangle className="size-3.5 shrink-0 text-warn" aria-label={t('snip.danger')} />}
        <button type="button" title={t('snip.insert')} aria-label={t('snip.insert') + ' ' + name} className="rounded p-1 text-mute hover:bg-soft hover:text-fg" onClick={() => use(cmd, false, danger, id)}><TextCursorInput className="size-3.5" /></button>
        <button type="button" title={t('snip.run')} aria-label={t('snip.run') + ' ' + name} className="rounded p-1 text-acc hover:bg-soft" onClick={() => use(cmd, true, danger, id)}><Play className="size-3.5" /></button>
        {extra}
      </div>
      <code className="num mt-0.5 block truncate text-[11.5px] text-mute" title={cmd}>{cmd}</code>
    </div>
  )
  return (
    <div className="flex h-full min-h-0 flex-col bg-card text-fg">
      <div className="flex items-center gap-2 border-b border-line px-3 py-2">
        <BookMarked className="size-4 shrink-0 text-acc" /><b className="flex-1 whitespace-nowrap text-[13.5px]">{t('snip.title')}</b>
        {selection && <Button size="sm" variant="ghost" title={t('snip.from_selection_hint')} onClick={() => { const s = selection().trim(); if (s) setEdit({ name: s.split('\n')[0].slice(0, 40), command: s, note: '', folder: '', danger: false, shared: false }) }}><Star className="size-3.5" />{t('snip.from_selection_short')}</Button>}
        <Button size="sm" variant="primary" onClick={() => setEdit({ name: '', command: '', note: '', folder: '', danger: false, shared: false })}><Plus className="size-3.5" />{t('snip.new_short')}</Button>
        {onClose && <Button size="sm" variant="ghost" onClick={onClose} aria-label={t('common.close')}>×</Button>}
      </div>
      <div className="space-y-2 border-b border-line px-3 py-2">
        <Segmented value={tab} onChange={setTab} options={[
          { value: 'mine', label: `${t('snip.mine')} ${mine.length || ''}` },
          { value: 'team', label: <span className="inline-flex items-center gap-1"><Users className="size-3" />{t('snip.team')} {team.length || ''}</span> },
          { value: 'lib', label: t('snip.lib') },
        ]} />
        <div className="relative"><Search className="absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-mute" /><Input className="h-7 pl-7 text-[12.5px]" placeholder={t('snip.search')} value={q} onChange={(e) => setQ(e.target.value)} aria-label={t('snip.search')} /></div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {tab !== 'lib' && groups.length === 0 && (
          <p className="px-4 py-6 text-center text-[12.5px] text-mute">{tab === 'mine' ? t('snip.mine_empty') : t('snip.team_empty')}</p>
        )}
        {tab !== 'lib' && groups.map(([folder, list]) => (
          <div key={folder}>
            {folder && <div className="bg-soft px-3 py-1 text-[11.5px] font-semibold text-mute">{folder}</div>}
            {list.map((s) => row(String(s.id), s.name, s.command, s.danger, (s.mine || data?.can_share) && <>
              <button type="button" aria-label={t('common.edit')} className="rounded p-1 text-mute opacity-0 hover:bg-soft hover:text-fg group-hover:opacity-100" onClick={() => setEdit(s)}><Pencil className="size-3.5" /></button>
              <button type="button" aria-label={t('common.delete')} className="rounded p-1 text-mute opacity-0 hover:bg-soft hover:text-bad group-hover:opacity-100" onClick={() => { if (confirm(t('snip.delete_confirm', { name: s.name }))) void del(`/api/v1/snippets/${s.id}`).then(reload) }}><Trash2 className="size-3.5" /></button>
            </>, s.id))}
          </div>
        ))}
        {tab === 'lib' && LIBRARY.map((g) => {
          const list = g.items.filter((x) => match(x.name[locale as 'en'] ?? x.name.en) || match(x.command))
          if (!list.length) return null
          return (
            <div key={g.name.en}>
              <div className="bg-soft px-3 py-1 text-[11.5px] font-semibold text-mute">{g.name[locale as 'en'] ?? g.name.en}</div>
              {list.map((x) => {
                const name = x.name[locale as 'en'] ?? x.name.en
                const saved = items.some((s) => s.mine && s.command === x.command)
                return row(x.command, name, x.command, !!x.danger,
                  <button type="button" title={saved ? t('snip.saved') : t('snip.save')} aria-label={t('snip.save') + ' ' + name} disabled={saved} className={cx('rounded p-1 hover:bg-soft', saved ? 'text-warn' : 'text-mute hover:text-warn')}
                    onClick={() => void post('/api/v1/snippets', { name, command: x.command, folder: g.name[locale as 'en'] ?? g.name.en, danger: !!x.danger }).then(reload)}>
                    <Star className={cx('size-3.5', saved && 'fill-current')} /></button>)
              })}
            </div>
          )
        })}
      </div>
      <p className="border-t border-line px-3 py-1.5 text-[11px] text-mute">{t('snip.footer')}</p>
      {edit && <SnippetEditor draft={edit} canShare={!!data?.can_share} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); void reload() }} />}
      {ask && <FillDialog {...ask} onClose={() => setAsk(null)} onGo={(text) => { setAsk(null); send(text, ask.run, ask.id) }} />}
    </div>
  )
}

function FillDialog({ cmd, run, danger, onClose, onGo }: { cmd: string; run: boolean; danger: boolean; onClose: () => void; onGo: (text: string) => void }) {
  const t = useT()
  const ps = placeholders(cmd)
  const [vals, setVals] = useState<Record<string, string>>(() => Object.fromEntries(ps.map((p) => [p.key, p.def])))
  const text = fill(cmd, vals)
  const ok = ps.every((p) => (vals[p.key] ?? '').trim() !== '')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={run ? t('snip.run') : t('snip.insert')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant={danger && run ? 'danger' : 'primary'} disabled={!ok} onClick={() => onGo(text)}>{run ? <><CornerDownLeft className="size-3.5" />{t('snip.run')}</> : t('snip.insert')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        {danger && run && <Notice tone="warn">{t('snip.danger_hint')}</Notice>}
        {ps.map((p, i) => (
          <Field key={p.key} label={p.name}><Input className="num" autoFocus={i === 0} value={vals[p.key] ?? ''} onChange={(e) => setVals({ ...vals, [p.key]: e.target.value })}
            onKeyDown={(e) => { if (e.key === 'Enter' && ok) onGo(text) }} /></Field>
        ))}
        <pre className="num whitespace-pre-wrap break-all rounded-lg bg-soft px-3 py-2 text-[12px]">{text}</pre>
      </div>
    </Dialog>
  )
}

function SnippetEditor({ draft, canShare, onClose, onSaved }: { draft: Draft; canShare: boolean; onClose: () => void; onSaved: () => void }) {
  const t = useT()
  const [d, setD] = useState(draft)
  const [err, setErr] = useState('')
  const ps = placeholders(d.command)
  const save = async () => {
    setErr('')
    try { if (d.id) await put(`/api/v1/snippets/${d.id}`, d); else await post('/api/v1/snippets', d); onSaved() } catch (e) { setErr(errText(e)) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={d.id ? t('snip.edit') : t('snip.new')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!d.name.trim() || !d.command.trim()} onClick={() => void save()}>{t('common.save')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        {err && <Notice tone="bad">{err}</Notice>}
        <Field label={t('snip.name')}><Input autoFocus value={d.name} placeholder={t('snip.name_ph')} onChange={(e) => setD({ ...d, name: e.target.value })} aria-label={t('snip.name')} /></Field>
        <Field label={t('snip.command')} hint={t('snip.command_hint')}>
          <Textarea className="num min-h-24 text-[12.5px]" value={d.command} placeholder="systemctl restart {{服务:nginx}}" onChange={(e) => setD({ ...d, command: e.target.value })} aria-label={t('snip.command')} />
        </Field>
        {ps.length > 0 && <div className="flex flex-wrap gap-1.5 text-[12px] text-mute">{t('snip.will_ask')}{ps.map((p) => <Pill key={p.key}>{p.name}{p.def ? ' = ' + p.def : ''}</Pill>)}</div>}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('snip.folder')}><Input value={d.folder} placeholder={t('snip.folder_ph')} onChange={(e) => setD({ ...d, folder: e.target.value })} /></Field>
          <Field label={t('snip.note')}><Input value={d.note} onChange={(e) => setD({ ...d, note: e.target.value })} /></Field>
        </div>
        <Switch checked={d.danger} onChange={(x) => setD({ ...d, danger: x })} label={t('snip.mark_danger')} />
        {canShare && <Switch checked={d.shared} onChange={(x) => setD({ ...d, shared: x })} label={t('snip.share')} />}
      </div>
    </Dialog>
  )
}
