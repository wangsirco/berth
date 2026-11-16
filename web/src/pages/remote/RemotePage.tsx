import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Box, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented, Switch, Textarea } from '@/components/ui/Field'
import { useAuth } from '@/lib/auth'
import { bytes, datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import type { Change } from '@/lib/types'
import { SoftwarePicker, type DesktopInfo } from './DesktopSoftware'

export type Proto = 'rdp' | 'vnc' | 'ssh' | 'telnet'
export interface Conn {
  id: number; name: string; group: string; protocol: Proto; system?: string; host: string; port: number; username?: string; domain?: string
  clipboard: boolean; files: boolean; ignore_cert: boolean; users: number[]; has_secret: boolean; last_used?: number
}
interface Data { items: Conn[]; guacd: { running: boolean }; desktop?: unknown; manage: boolean }

const PORTS: Record<Proto, number> = { rdp: 3389, vnc: 5900, ssh: 22, telnet: 23 }
const PROTOS: Proto[] = ['rdp', 'vnc', 'ssh', 'telnet']

// The viewer lives on its own page so the desktop gets the whole browser window.
export const openRemote = (id: number) => window.open(`/remote/${id}`, `berth-remote-${id}`)

export default function RemotePage() {
  const t = useT()
  const run = useChange()
  const d = useApi<Data>('/api/v1/remote')
  useEvents(['change.done'], () => void d.reload())
  const [params, setParams] = useSearchParams()
  const [edit, setEdit] = useState<Partial<Conn> | null>(null)
  // "远程桌面" from a VM drawer arrives here with ?add=rdp&name=…&addr=…
  useEffect(() => {
    const add = params.get('add') as Proto | null
    if (!add || !d.data) return
    const addr = params.get('addr') ?? ''
    const known = d.data.items.find((c) => c.host === addr && c.protocol === add)
    if (known) openRemote(known.id)
    else setEdit({ protocol: add, name: params.get('name') ?? '', host: addr, group: params.get('group') ?? '', clipboard: true, ignore_cert: true })
    setParams({}, { replace: true })
  }, [params, d.data, setParams])
  const groups = useMemo(() => {
    const m = new Map<string, Conn[]>()
    for (const c of (d.data?.items ?? []).filter((c) => !c.system)) m.set(c.group, [...(m.get(c.group) ?? []), c])
    return [...m.entries()]
  }, [d.data])
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const { guacd, manage, desktop } = d.data
  const items = d.data.items.filter((c) => !c.system)
  const deskConn = d.data.items.find((c) => c.system === 'desktop')
  const install = <Button variant="primary" onClick={() => run('rd.install', {}, { host: 'local', onDone: () => void d.reload() })}>{t('action.rd.install')}</Button>
  return (
    <div className="space-y-4">
      {!guacd.running && <Notice tone="warn"><div className="flex flex-wrap items-center gap-2"><span className="flex-1">{t('rd.guacd_missing')}</span>{install}</div></Notice>}
      {desktop != null && <DesktopCard conn={deskConn} guacd={guacd.running} reload={() => void d.reload()} />}
      {items.length === 0 && (
        <Box title={t('nav.remote')}>
          <Empty title={t('rd.none')} action={<Button variant="primary" onClick={() => setEdit({ protocol: 'rdp', clipboard: true, ignore_cert: true })}>{t('rd.new')}</Button>}>{t('rd.none_hint')}</Empty>
        </Box>
      )}
      {groups.map(([g, cs], i) => (
        <Box key={g} title={g || t('nav.remote')} count={cs.length} right={i === 0 && <Button size="sm" variant="primary" onClick={() => setEdit({ protocol: 'rdp', clipboard: true, ignore_cert: true })}>{t('rd.new')}</Button>}>
          {cs.map((c) => (
            <Row key={c.id} onClick={() => openRemote(c.id)}>
              <Pill tone="acc" className="w-14 justify-center uppercase">{c.protocol}</Pill>
              <div className="min-w-0 flex-1">
                <div className="text-[13.5px] font-semibold">{c.name}</div>
                <div className="num truncate text-[12px] text-mute">{c.username ? c.username + '@' : ''}{c.host}:{c.port}{c.last_used ? ' · ' + t('rd.last_used', { v: datetime(c.last_used) }) : ''}</div>
              </div>
              <span className="flex gap-1" onClick={(e) => e.stopPropagation()}>
                <Button size="sm" variant="primary" disabled={!guacd.running} onClick={() => openRemote(c.id)}>{t('rd.connect')}</Button>
                <Button size="sm" variant="ghost" onClick={() => setEdit(c)}>{t('rd.edit')}</Button>
                <Button size="sm" variant="ghost" onClick={() => run('rd.connection.delete', { id: c.id }, { host: 'local' })}>{t('action.rd.connection.delete')}</Button>
              </span>
            </Row>
          ))}
        </Box>
      ))}
      {edit && <ConnDialog init={edit} manage={manage} groups={groups.map(([g]) => g).filter(Boolean)} onClose={() => setEdit(null)}
        onSaved={(id) => { setEdit(null); void d.reload(); if (!edit.id && id && guacd.running) openRemote(id) }} />}
    </div>
  )
}

function ConnDialog({ init, manage, groups, onClose, onSaved }: { init: Partial<Conn>; manage: boolean; groups: string[]; onClose: () => void; onSaved: (id?: number) => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const users = useApi<{ items: { id: number; username: string }[] }>(manage ? '/api/v1/users' : null)
  const [c, setC] = useState<Partial<Conn> & { password?: string; private_key?: string }>({ group: '', port: 0, users: [], ...init })
  const set = <K extends keyof typeof c>(k: K, v: (typeof c)[K]) => setC((o) => ({ ...o, [k]: v }))
  const proto = (c.protocol ?? 'rdp') as Proto
  const editing = !!c.id
  const save = () => {
    const p = { ...c, port: c.port || PORTS[proto] }
    run('rd.connection.set', p, { host: 'local', onDone: (r) => onSaved(r.result?.id) })
  }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={editing ? t('rd.edit_title', { name: init.name ?? '' }) : t('rd.new')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!c.name || !c.host} onClick={save}>{editing ? t('action.rd.connection.set') : t('rd.save_connect')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <Field label={t('rd.protocol')} hint={t('rd.proto_hint_' + proto)}>
          <Segmented value={proto} options={PROTOS.map((p) => ({ value: p, label: p.toUpperCase() }))} onChange={(v) => set('protocol', v)} />
        </Field>
        <div className="grid grid-cols-[1fr_7rem] gap-2">
          <Field label={t('rd.host')} hint={t('rd.host_hint')}><Input className="num" value={c.host ?? ''} onChange={(e) => set('host', e.target.value.trim())} placeholder="10.0.0.5" /></Field>
          <Field label={t('rd.port')}><Input className="num" type="number" value={c.port || ''} placeholder={String(PORTS[proto])} onChange={(e) => set('port', Number(e.target.value))} /></Field>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <Field label={t('rd.name')}><Input value={c.name ?? ''} onChange={(e) => set('name', e.target.value)} placeholder={c.host || ''} /></Field>
          <Field label={t('rd.group')} hint={t('rd.group_hint')}>
            <Input list="rd-groups" value={c.group ?? ''} onChange={(e) => set('group', e.target.value)} />
            <datalist id="rd-groups">{groups.map((g) => <option key={g} value={g} />)}</datalist>
          </Field>
        </div>
        <div className="grid grid-cols-2 gap-2">
          {proto !== 'vnc' && <Field label={t('rd.username')}><Input value={c.username ?? ''} onChange={(e) => set('username', e.target.value)} autoComplete="off" /></Field>}
          <Field label={t('rd.password')} hint={editing && init.has_secret ? t('rd.password_keep') : t('rd.password_hint')}>
            <Input type="password" value={c.password ?? ''} onChange={(e) => set('password', e.target.value)} autoComplete="new-password" />
          </Field>
        </div>
        {proto === 'rdp' && <Field label={t('rd.domain')} hint={t('rd.domain_hint')}><Input value={c.domain ?? ''} onChange={(e) => set('domain', e.target.value)} /></Field>}
        {proto === 'ssh' && <Field label={t('rd.private_key')} hint={editing && init.has_secret ? t('rd.password_keep') : t('rd.private_key_hint')}>
          <Textarea className="num h-20 text-[11.5px]" value={c.private_key ?? ''} onChange={(e) => set('private_key', e.target.value)} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" /></Field>}
        <div className="flex flex-wrap gap-x-5 gap-y-2">
          <Switch checked={!!c.clipboard} onChange={(v) => set('clipboard', v)} label={t('rd.clipboard')} />
          {(proto === 'rdp' || proto === 'ssh') && <Switch checked={!!c.files} onChange={(v) => set('files', v)} label={t('rd.files_' + proto)} />}
          {proto === 'rdp' && <Switch checked={!!c.ignore_cert} onChange={(v) => set('ignore_cert', v)} label={t('rd.ignore_cert')} />}
        </div>
        {manage && (users.data?.items.length ?? 0) > 1 && (
          <Field label={t('rd.share')} hint={t('rd.share_hint')}>
            <div className="flex flex-wrap gap-x-4 gap-y-1.5">
              {users.data!.items.filter((u) => u.id !== user?.id).map((u) => (
                <label key={u.id} className="flex items-center gap-1.5">
                  <input type="checkbox" checked={c.users?.includes(u.id) ?? false}
                    onChange={(e) => set('users', e.target.checked ? [...(c.users ?? []), u.id] : (c.users ?? []).filter((x) => x !== u.id))} />{u.username}
                </label>
              ))}
            </div>
          </Field>
        )}
      </div>
    </Dialog>
  )
}

function DesktopCard({ conn, guacd, reload }: { conn?: Conn; guacd: boolean; reload: () => void }) {
  const t = useT()
  const run = useChange()
  const d = useApi<DesktopInfo>('/api/v1/desktop')
  useEvents(['change.done'], () => void d.reload())
  const [dialog, setDialog] = useState<null | 'install' | 'more' | 'missing'>(null)
  if (!d.data) return null
  const st = d.data.status
  const done = (open: boolean) => (c: Change) => { reload(); void d.reload(); const id = c.result?.connection; if (open && id && c.status === 'done') openRemote(id) }
  const go = (kind: string, params = {}, open = false) => run(kind, params, { host: 'local', onDone: done(open) })
  // no browser yet: offer the software before opening an empty desktop
  const noBrowser = !d.data.items.some((i) => (i.id === 'firefox' || i.id === 'chromium') && i.installed)
  const state = !st.installed ? t('rd.desk_not_installed') : st.running ? t('rd.desk_running', { user: st.user ?? '', mem: st.mem_bytes ? bytes(st.mem_bytes) : '—' }) : t('rd.desk_off')
  return (
    <Box title={t('rd.desk_title')}>
      <div className="flex flex-wrap items-center gap-3 px-4 py-3 text-[13px]">
        <div className="min-w-0 flex-1">
          <div className="font-semibold">{state}</div>
          <div className="text-[12px] text-mute">{t('rd.desk_hint')}</div>
        </div>
        {!st.installed && (st.supported
          ? <Button variant="primary" onClick={() => setDialog('install')}>{t('action.desktop.install')}</Button>
          : <span className="text-mute">{t('rd.desk_unsupported')}</span>)}
        {st.installed && !st.running && <Button variant="primary" onClick={() => (noBrowser ? setDialog('missing') : go('desktop.enable', {}, true))}>{t('rd.desk_on_open')}</Button>}
        {st.running && <>
          {conn && <Button variant="primary" disabled={!guacd} onClick={() => openRemote(conn.id)}>{t('rd.connect')}</Button>}
          <Button onClick={() => go('desktop.disable')}>{t('action.desktop.disable')}</Button>
        </>}
        {st.installed && <Button variant="ghost" onClick={() => setDialog('more')}>{t('rd.desk_more')}</Button>}
        {st.installed && !st.running && <Button variant="ghost" onClick={() => go('desktop.uninstall')}>{t('action.desktop.uninstall')}</Button>}
      </div>
      {dialog && <SoftwareDialog info={d.data} mode={dialog} onClose={() => setDialog(null)}
        onInstall={(items, start) => { setDialog(null); go('desktop.install', { items, start }, start) }}
        onOpenAnyway={() => { setDialog(null); go('desktop.enable', {}, true) }} />}
    </Box>
  )
}

// install: first time; more: add software later; missing: opening a desktop without a browser
function SoftwareDialog({ info, mode, onClose, onInstall, onOpenAnyway }: {
  info: DesktopInfo; mode: 'install' | 'more' | 'missing'; onClose: () => void; onInstall: (items: string[], start: boolean) => void; onOpenAnyway: () => void
}) {
  const t = useT()
  const [pick, setPick] = useState<string[]>(mode === 'more' ? [] : info.presets.basic.filter((id) => !info.items.find((i) => i.id === id)?.installed))
  const items = [...new Set([...pick, 'look'])].filter((id) => !info.items.find((i) => i.id === id)?.installed)
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('desktop.dialog_' + mode)} description={t('desktop.dialog_' + mode + '_hint')}
      footer={<>
        {mode === 'missing' && <Button variant="ghost" onClick={onOpenAnyway}>{t('desktop.open_anyway')}</Button>}
        <Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={mode === 'more' && pick.length === 0} onClick={() => onInstall(items, mode !== 'more')}>
          {mode === 'more' ? t('desktop.install_these') : t('desktop.install_open')}
        </Button>
      </>}>
      <div className="max-h-[60vh] overflow-auto px-4 py-3"><SoftwarePicker info={info} pick={pick} onChange={setPick} /></div>
    </Dialog>
  )
}
