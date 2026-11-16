import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Drawer } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Textarea } from '@/components/ui/Field'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { User } from '@/lib/types'
import { InvitesBox } from '@/components/invite/Invites'

interface PermDef { key: string; group: string; danger?: boolean; super_only?: boolean }
type Role = User['role']

export default function UsersPage() {
  const t = useT()
  const users = useApi<{ items: User[] }>('/api/v1/users')
  const perms = useApi<{ items: PermDef[] }>('/api/v1/permissions')
  const [edit, setEdit] = useState<User | 'new' | null>(null)
  return (
    <>
      <Box title={t('nav.users')} count={users.data?.items.length} right={<Button size="sm" variant="primary" onClick={() => setEdit('new')}>{t('users.new')}</Button>}>
        {users.data?.items.map((u) => (
          <Row key={u.id} onClick={() => setEdit(u)}>
            <Dot state={u.disabled ? 'off' : 'ok'} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{u.display_name} <span className="font-normal text-mute">@{u.username}</span></div>
              <div className="text-[12px] text-mute">{t('mode.' + u.login_mode)} · {u.last_login_at ? t('users.last_login', { when: ago(u.last_login_at, t) }) : t('users.never')}</div>
            </div>
            <Pill tone={u.role === 'super' ? 'acc' : undefined}>{t('role.' + u.role)}</Pill>
            {u.disabled && <Pill>{t('users.disabled')}</Pill>}
          </Row>
        ))}
      </Box>
      <div className="mt-4"><InvitesBox /></div>
      {edit && <UserDrawer user={edit === 'new' ? null : edit} perms={perms.data?.items ?? []} onClose={() => setEdit(null)} onDone={() => { setEdit(null); void users.reload() }} />}
    </>
  )
}

function UserDrawer({ user, perms, onClose, onDone }: { user: User | null; perms: PermDef[]; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [f, setF] = useState({
    username: user?.username ?? '', display_name: user?.display_name ?? '', password: '', role: (user?.role ?? 'admin') as Role,
    perms: new Set(user?.perms ?? []), sites: new Set(user?.scope.sites ?? []), hosts: (user?.scope.hosts ?? []).join('\n'), paths: (user?.scope.paths ?? []).join('\n'), disabled: user?.disabled ?? false, simple: user?.simple ?? false,
  })
  const [pw, setPw] = useState('')
  const sites = useApi<{ items: { id: number; domains: string[]; options?: { note?: string } }[] }>(`/api/v1/hosts/${host}/sites`, [host])
  const groups = [...new Set(perms.filter((p) => !p.super_only).map((p) => p.group))]
  const lines = (s: string) => s.split('\n').map((x) => x.trim()).filter(Boolean)
  const save = () => {
    const scope = { hosts: lines(f.hosts), paths: lines(f.paths), sites: [...f.sites] }
    if (!user) {
      // /api/v1/users hashes the password before it becomes a user.create change
      run('user.create', { username: f.username, display_name: f.display_name, password: f.password, role: f.role, perms: [...f.perms], scope, simple: f.simple }, { host, onDone, url: '/api/v1/users' })
      return
    }
    run('user.update', { id: user.id, display_name: f.display_name, role: f.role, perms: [...f.perms], scope, disabled: f.disabled, simple: f.simple }, { host, onDone })
  }
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={user ? user.display_name : t('users.new')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={save} disabled={!f.username || (!user && f.password.length < 10)}>{t('common.next')}</Button></>}>
      {!user && <Field label={t('login.username')}><Input value={f.username} onChange={(e) => setF({ ...f, username: e.target.value })} /></Field>}
      <Field label={t('setup.display_name')}><Input value={f.display_name} onChange={(e) => setF({ ...f, display_name: e.target.value })} /></Field>
      {!user && <Field label={t('login.password')} hint={t('setup.password_hint')}><Input type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>}
      <Field label={t('users.role')}>
        <Segmented<Role> value={f.role} onChange={(role) => setF({ ...f, role })} options={(['super', 'admin', 'viewer'] as Role[]).map((r) => ({ value: r, label: t('role.' + r) }))} />
        <p className="mt-1 text-[12px] text-mute">{t('role.' + f.role + '_hint')}</p>
      </Field>
      {f.role === 'admin' && (
        <Field label={t('users.perms')}>
          <div className="space-y-2">
            {groups.map((g) => (
              <div key={g} className="flex flex-wrap gap-x-4 gap-y-1 text-[13px]">
                <span className="w-24 text-mute">{t('permgroup.' + g)}</span>
                {perms.filter((p) => p.group === g && !p.super_only).map((p) => (
                  <label key={p.key} className="flex items-center gap-1.5">
                    <input type="checkbox" checked={f.perms.has(p.key)} onChange={(e) => { const n = new Set(f.perms); if (e.target.checked) n.add(p.key); else n.delete(p.key); setF({ ...f, perms: n }) }} />
                    {t('perm.' + p.key)}{p.danger && <Pill tone="bad">{t('users.danger')}</Pill>}
                  </label>
                ))}
              </div>
            ))}
          </div>
        </Field>
      )}
      {f.role !== 'super' && <>
        <Field label={t('users.scope_hosts')} hint={t('users.scope_hint')}><Textarea className="num h-16 text-[12.5px]" value={f.hosts} onChange={(e) => setF({ ...f, hosts: e.target.value })} /></Field>
        <Field label={t('users.scope_paths')} hint={t('users.scope_paths_hint')}><Textarea className="num h-16 text-[12.5px]" value={f.paths} onChange={(e) => setF({ ...f, paths: e.target.value })} /></Field>
        {f.perms.has('sites.manage') && sites.data && sites.data.items.length > 0 && (
          <Field label={t('users.scope_sites')} hint={t('users.scope_sites_hint')}>
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-[13px]">
              {sites.data.items.map((s) => (
                <label key={s.id} className="flex items-center gap-1.5"><input type="checkbox" checked={f.sites.has(s.id)}
                  onChange={(e) => { const n = new Set(f.sites); if (e.target.checked) n.add(s.id); else n.delete(s.id); setF({ ...f, sites: n }) }} />{s.options?.note?.trim() || s.domains[0]}</label>
              ))}
            </div>
          </Field>
        )}
      </>}
      {user && f.role !== 'super' && <QuotaField id={user.id} />}
      {f.role !== 'super' && <Field label={t('users.simple')} hint={t('users.simple_hint')}><label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={f.simple} onChange={(e) => setF({ ...f, simple: e.target.checked })} />{t('users.simple_on')}</label></Field>}
      {user && <Field label={t('users.status')}><label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={f.disabled} onChange={(e) => setF({ ...f, disabled: e.target.checked })} />{t('users.disable')}</label></Field>}
      {user && (
        <Field label={t('users.reset_password')} className="pb-4">
          <div className="flex gap-2"><Input type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} />
            <Button disabled={pw.length < 10} onClick={() => run('user.password', { password: pw }, { host, url: `/api/v1/users/${user.id}/password`, onDone: () => setPw('') })}>{t('action.user.password')}</Button></div>
        </Field>
      )}
    </Drawer>
  )
}

interface Quota { sites?: number; containers?: number; vms?: number; cpus?: number; mem_mb?: number; disk_gb?: number; traffic_gb?: number }
const QUOTA_KEYS: (keyof Quota)[] = ['sites', 'containers', 'vms', 'cpus', 'mem_mb', 'disk_gb', 'traffic_gb']

// What the user may create in total; blank means no limit. Usage counts what they made.
function QuotaField({ id }: { id: number }) {
  const t = useT()
  const run = useChange()
  const d = useApi<{ quota: Quota; usage: Required<Quota> & { traffic_bytes?: number } }>(`/api/v1/users/${id}/usage`, [id])
  const [q, setQ] = useState<Record<string, string> | null>(null)
  if (!d.data) return null
  const cur = q ?? Object.fromEntries(QUOTA_KEYS.map((k) => [k, d.data!.quota[k] ? String(d.data!.quota[k]) : '']))
  const dirty = QUOTA_KEYS.some((k) => (cur[k] || '') !== (d.data!.quota[k] ? String(d.data!.quota[k]) : ''))
  const save = () => run('user.quota', { id, quota: Object.fromEntries(QUOTA_KEYS.filter((k) => Number(cur[k]) > 0).map((k) => [k, Number(cur[k])])) }, { onDone: () => { setQ(null); void d.reload() } })
  return (
    <Field label={t('users.quota')} hint={t('users.quota_hint')}>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        {QUOTA_KEYS.map((k) => (
          <label key={k} className="text-[12.5px]">
            <span className="text-mute">{t('users.q_' + k)} · {t('users.used', { n: k === 'traffic_gb' ? ((d.data!.usage.traffic_bytes ?? 0) / 2 ** 30).toFixed(1) : d.data!.usage[k] })}</span>
            <Input type="number" min={0} className="num mt-0.5" placeholder={t('users.no_limit')} value={cur[k]} onChange={(e) => setQ({ ...cur, [k]: e.target.value })} />
          </label>
        ))}
      </div>
      {dirty && <Button size="sm" className="mt-2" onClick={save}>{t('action.user.quota')}</Button>}
    </Field>
  )
}
