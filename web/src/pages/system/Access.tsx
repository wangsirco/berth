import { useEffect, useState } from 'react'
import { KeyRound, MoreHorizontal, Plus } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface SSHSettings { port: number; password_auth: boolean; root_login: string; pubkey_auth: boolean }
interface AuthKey { type: string; comment: string; fingerprint: string }
interface SSHState { effective: SSHSettings; managed: boolean; pending: boolean; rollback_at?: number; socket: boolean; installed: boolean; keys: { user: string; keys: AuthKey[] }[] }
interface Account { name: string; uid: number; gecos?: string; home: string; shell: string; groups: string[]; system: boolean; nologin: boolean; locked: boolean; no_password: boolean }
interface Group { name: string; gid: number; members: string[]; system: boolean }
interface Accounts { users: Account[]; groups: Group[]; admin_group: string; shells: string[] }

// System users, their SSH keys, and the SSH server's login settings (方案 §5.14).
export function Access() {
  const t = useT()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const ssh = useApi<SSHState>(`/api/v1/hosts/${host}/system/ssh`, [host])
  const acc = useApi<Accounts>(`/api/v1/hosts/${host}/system/accounts`, [host])
  useEvents(['change.done', 'ssh.pending'], () => { void ssh.reload(); void acc.reload() })
  if (!ssh.data || !acc.data) return ssh.error || acc.error ? <Notice tone="bad">{(ssh.error ?? acc.error)!.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  return (
    <div className="space-y-4">
      {ssh.data.pending && ssh.data.rollback_at && <SSHPending at={ssh.data.rollback_at} onDone={() => void ssh.reload()} />}
      <SSHBox st={ssh.data} manage={manage} />
      <UsersBox acc={acc.data} keys={ssh.data.keys} manage={manage} />
    </div>
  )
}

function SSHPending({ at, onDone }: { at: number; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [now, setNow] = useState(Date.now() / 1000)
  useEffect(() => { const i = setInterval(() => setNow(Date.now() / 1000), 1000); return () => clearInterval(i) }, [])
  const left = Math.max(0, Math.round(at - now))
  useEffect(() => { if (left === 0) { const id = setTimeout(onDone, 3000); return () => clearTimeout(id) } }, [left, onDone])
  return (
    <Notice tone="warn">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex-1">{t('access.pending', { s: left })}</span>
        <Button size="sm" variant="primary" onClick={() => run('ssh.confirm', {}, { host, onDone })}>{t('action.ssh.confirm')}</Button>
        <Button size="sm" onClick={() => run('ssh.rollback', {}, { host, onDone })}>{t('action.ssh.rollback')}</Button>
      </div>
    </Notice>
  )
}

function SSHBox({ st, manage }: { st: SSHState; manage: boolean }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [s, setS] = useState<SSHSettings>(st.effective)
  useEffect(() => setS(st.effective), [st.effective])
  const dirty = JSON.stringify(s) !== JSON.stringify(st.effective)
  if (!st.installed) return <Box title="SSH"><Empty title={t('access.no_sshd')} /></Box>
  const rootKeys = st.keys.find((k) => k.user === 'root')?.keys.length ?? 0
  const risky = (!s.password_auth && st.effective.password_auth) || s.root_login !== st.effective.root_login && s.root_login === 'no'
  return (
    <Box title={t('access.ssh')} right={<span className="num text-[12px] text-mute">{t('access.listening', { port: st.effective.port })}</span>}>
      <div className="grid gap-4 px-4 py-3 sm:grid-cols-2">
        <Field label={t('access.port')} hint={st.socket ? t('access.port_socket') : t('access.port_hint')}>
          <Input type="number" min={1} max={65535} className="w-32" disabled={!manage || st.socket} value={s.port} onChange={(e) => setS({ ...s, port: Number(e.target.value) })} />
        </Field>
        <Field label={t('access.root_login')} hint={t('access.root_hint_' + s.root_login)}>
          <Select disabled={!manage} value={s.root_login} onChange={(e) => setS({ ...s, root_login: e.target.value })}>
            {['yes', 'prohibit-password', 'no'].map((v) => <option key={v} value={v}>{t('access.root_' + v)}</option>)}
          </Select>
        </Field>
        <div className="space-y-2">
          <Switch disabled={!manage} checked={s.password_auth} onChange={(v) => setS({ ...s, password_auth: v })} label={t('access.password_auth')} />
          <Switch disabled={!manage} checked={s.pubkey_auth} onChange={(v) => setS({ ...s, pubkey_auth: v })} label={t('access.pubkey_auth')} />
        </div>
        <div className="text-[12.5px] text-mute">
          {!s.password_auth && !s.pubkey_auth && <div className="text-bad">{t('access.no_method')}</div>}
          {risky && <div className="text-warn">{rootKeys === 0 && s.root_login !== 'no' ? t('access.no_root_keys') : t('access.risky')}</div>}
          <div>{t('access.rollback_hint')}</div>
        </div>
      </div>
      {manage && <div className="flex justify-end gap-2 border-t border-line px-4 py-2.5">
        <Button size="sm" disabled={!dirty} onClick={() => setS(st.effective)}>{t('common.reset')}</Button>
        <Button size="sm" variant="primary" disabled={!dirty || (!s.password_auth && !s.pubkey_auth)} onClick={() => run('ssh.set', { ...s, seconds: 60 }, { host })}>{t('action.ssh.set')}</Button>
      </div>}
    </Box>
  )
}

function UsersBox({ acc, keys, manage }: { acc: Accounts; keys: SSHState['keys']; manage: boolean }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [showSys, setShowSys] = useState(false)
  const [add, setAdd] = useState(false)
  const [keyFor, setKeyFor] = useState<string | null>(null)
  const [groupsFor, setGroupsFor] = useState<Account | null>(null)
  const [pwFor, setPwFor] = useState<string | null>(null)
  const keyMap = new Map(keys.map((k) => [k.user, k.keys]))
  const list = acc.users.filter((u) => showSys || !u.system || u.name === 'root')
  return (
    <>
      <Box title={t('access.users')} count={acc.users.filter((u) => !u.system).length} right={<div className="flex items-center gap-2">
        <Switch checked={showSys} onChange={setShowSys} label={t('access.show_system')} />
        {manage && <Button size="sm" variant="primary" onClick={() => setAdd(true)}><Plus className="size-3.5" />{t('action.sysuser.create')}</Button>}
      </div>}>
        {list.map((u) => {
          const ks = keyMap.get(u.name)
          const admin = acc.admin_group && (u.groups.includes(acc.admin_group) || u.name === 'root')
          return (
            <div key={u.name} className="border-b border-line last:border-b-0">
              <Row>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 text-[13px]">
                    <b className="num">{u.name}</b>{u.gecos && <span className="text-mute">{u.gecos}</span>}
                    {admin && <Pill tone="acc">{t('access.admin')}</Pill>}
                    {u.locked && <Pill tone="warn">{t('access.locked')}</Pill>}
                    {u.system && u.name !== 'root' && <Pill>{t('access.system')}</Pill>}
                    {!u.system && u.no_password && <Pill>{t('access.key_only')}</Pill>}
                  </div>
                  <div className="num truncate text-[12px] text-mute">{u.home} · {u.shell}{u.groups.length > 0 && ' · ' + u.groups.join(', ')}</div>
                </div>
                {ks && <span className="flex items-center gap-1 text-[12px] text-mute"><KeyRound className="size-3.5" />{t('access.keys', { n: ks.length })}</span>}
                {manage && (!u.system || u.name === 'root') && <Menu items={[
                  ...(ks ? [{ label: t('action.ssh.key_add'), onClick: () => setKeyFor(u.name) }] : []),
                  ...(!u.system ? [
                    { label: t('access.edit_groups'), onClick: () => setGroupsFor(u) },
                    { label: t('action.sysuser.password'), onClick: () => setPwFor(u.name) },
                    { label: u.locked ? t('access.unlock') : t('access.lock'), onClick: () => run('sysuser.update', { name: u.name, locked: !u.locked }, { host }) },
                    'sep' as const,
                    { label: t('action.sysuser.delete'), danger: true, onClick: () => run('sysuser.delete', { name: u.name, remove_home: false }, { host }) },
                    { label: t('access.delete_home'), danger: true, onClick: () => run('sysuser.delete', { name: u.name, remove_home: true }, { host, confirmName: u.name }) },
                  ] : []),
                ]} trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} />}
              </Row>
              {ks && ks.length > 0 && <div className="space-y-0.5 pb-2 pl-6 pr-4">
                {ks.map((k) => (
                  <div key={k.fingerprint} className="flex items-center gap-2 text-[12px]">
                    <span className="text-mute">{k.type.replace(/^ssh-/, '')}</span>
                    <span className="min-w-0 flex-1 truncate">{k.comment || <span className="num text-mute">{k.fingerprint}</span>}</span>
                    <span className="num truncate text-[11px] text-mute max-md:hidden">{k.fingerprint.slice(0, 22)}…</span>
                    {manage && <button className="text-[12px] text-bad hover:underline" onClick={() => run('ssh.key_remove', { user: u.name, fingerprint: k.fingerprint }, { host })}>{t('access.remove')}</button>}
                  </div>
                ))}
              </div>}
            </div>
          )
        })}
      </Box>
      <GroupsBox acc={acc} manage={manage} />
      {add && <AddUser acc={acc} onClose={() => setAdd(false)} />}
      {keyFor && <AddKey user={keyFor} onClose={() => setKeyFor(null)} />}
      {groupsFor && <EditGroups u={groupsFor} acc={acc} onClose={() => setGroupsFor(null)} />}
      {pwFor && <SetPassword user={pwFor} onClose={() => setPwFor(null)} />}
    </>
  )
}

function GroupsBox({ acc, manage }: { acc: Accounts; manage: boolean }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [name, setName] = useState('')
  const groups = acc.groups.filter((g) => !g.system)
  return (
    <Box title={t('access.groups')} count={groups.length} right={manage && <div className="flex items-center gap-2">
      <Input className="w-36" placeholder={t('access.group_name')} value={name} onChange={(e) => setName(e.target.value)} />
      <Button size="sm" disabled={!name.trim()} onClick={() => run('sysgroup.create', { name: name.trim() }, { host, onDone: () => setName('') })}>{t('action.sysgroup.create')}</Button>
    </div>}>
      {groups.length === 0 && <div className="px-4 py-2.5 text-[12.5px] text-mute">{t('access.no_groups')}</div>}
      {groups.map((g) => (
        <div key={g.name} className="flex items-center gap-3 border-b border-line px-4 py-1.5 text-[12.5px] last:border-b-0">
          <b className="num w-36 truncate">{g.name}</b>
          <span className="min-w-0 flex-1 truncate text-mute">{g.members.join(', ') || '—'}</span>
          {manage && <button className="text-bad hover:underline" onClick={() => run('sysgroup.delete', { name: g.name }, { host })}>{t('access.remove')}</button>}
        </div>
      ))}
    </Box>
  )
}

function AddUser({ acc, onClose }: { acc: Accounts; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [name, setName] = useState('')
  const [gecos, setGecos] = useState('')
  const [admin, setAdmin] = useState(false)
  const [pw, setPw] = useState('')
  const [key, setKey] = useState('')
  const ok = /^[a-z_][a-z0-9_-]{0,31}$/.test(name)
  const save = () => run('sysuser.create', { name, gecos, admin, password: pw || undefined }, {
    host, onDone: () => { if (key.trim()) run('ssh.key_add', { user: name, key: key.trim() }, { host }); onClose() },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('action.sysuser.create')}
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!ok} onClick={save}>{t('common.continue')}</Button></>}>
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('access.login_name')} hint={name && !ok ? <span className="text-bad">{t('access.bad_name')}</span> : ''}><Input autoFocus value={name} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
          <Field label={t('access.display_name')}><Input value={gecos} onChange={(e) => setGecos(e.target.value)} /></Field>
        </div>
        <Field label={t('access.pubkey')} hint={t('access.pubkey_hint')}><Textarea className="num min-h-20 text-[12px]" value={key} onChange={(e) => setKey(e.target.value)} placeholder="ssh-ed25519 AAAA… you@laptop" /></Field>
        <Field label={t('access.password')} hint={t('access.password_hint')}><Input type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
        {acc.admin_group && <Switch checked={admin} onChange={setAdmin} label={t('access.make_admin', { group: acc.admin_group })} />}
      </div>
    </Dialog>
  )
}

function AddKey({ user, onClose }: { user: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [key, setKey] = useState('')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('access.add_key_for', { user })}
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!/^(ssh-|ecdsa-|sk-)/.test(key.trim())} onClick={() => run('ssh.key_add', { user, key: key.trim() }, { host, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <Field label={t('access.pubkey')} hint={t('access.pubkey_hint')}><Textarea autoFocus className="num min-h-24 text-[12px]" value={key} onChange={(e) => setKey(e.target.value)} placeholder="ssh-ed25519 AAAA… you@laptop" /></Field>
    </Dialog>
  )
}

function EditGroups({ u, acc, onClose }: { u: Account; acc: Accounts; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [sel, setSel] = useState<string[]>(u.groups.filter((g) => g !== u.name))
  const choices = acc.groups.filter((g) => g.name !== u.name && (!g.system || ['wheel', 'sudo', 'docker', 'adm', 'systemd-journal', 'berth'].includes(g.name) || sel.includes(g.name)))
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('access.groups_of', { user: u.name })}
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => run('sysuser.update', { name: u.name, groups: sel }, { host, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <div className="grid grid-cols-2 gap-2">
        {choices.map((g) => (
          <Switch key={g.name} checked={sel.includes(g.name)} onChange={(v) => setSel(v ? [...sel, g.name] : sel.filter((x) => x !== g.name))}
            label={<span>{g.name}{g.name === acc.admin_group && <span className="text-mute"> · {t('access.admin')}</span>}{g.name === 'docker' && <span className="text-mute"> · Docker</span>}</span>} />
        ))}
      </div>
    </Dialog>
  )
}

function SetPassword({ user, onClose }: { user: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('access.password_for', { user })}
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={pw !== pw2} onClick={() => run('sysuser.password', { name: user, password: pw }, { host, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3">
        <Field label={t('access.password')} hint={t('access.password_empty')}><Input autoFocus type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
        <Field label={t('access.password_again')} hint={pw2 && pw !== pw2 ? <span className="text-bad">{t('access.password_mismatch')}</span> : ''}><Input type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} /></Field>
      </div>
    </Dialog>
  )
}
