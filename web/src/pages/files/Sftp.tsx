import { KeyRound, MoreHorizontal, Plus, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Dot, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Field, Input, Switch, Textarea } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

export interface SftpAccount { name: string; dir: string; readonly?: boolean; has_keys?: number; mounted: boolean }

const genPassword = () => {
  const abc = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'
  const r = crypto.getRandomValues(new Uint32Array(16))
  return Array.from(r, (x) => abc[x % abc.length]).join('')
}

// How to connect: the SSH port and this host's address.
function useConnect() {
  const { host } = useHost()
  const ssh = useApi<{ effective: { port: number } }>(`/api/v1/hosts/${host}/system/ssh`, [host])
  const port = ssh.data?.effective.port || 22
  const addr = host === 'local' ? location.hostname : host
  return (name: string) => `sftp ${port !== 22 ? `-P ${port} ` : ''}${name}@${addr}`
}

// SftpDialog creates or edits one account; dir is prefilled from where it was opened.
export function SftpDialog({ initial, edit, onClose, onDone }: { initial: Partial<SftpAccount>; edit?: boolean; onClose: () => void; onDone?: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const connect = useConnect()
  const [name, setName] = useState(initial.name ?? '')
  const [dir, setDir] = useState(initial.dir ?? '')
  const [ro, setRo] = useState(!!initial.readonly)
  const [pw, setPw] = useState(edit ? '' : genPassword())
  const [keys, setKeys] = useState('')
  const ok = /^[a-z][a-z0-9_-]{0,30}$/.test(name) && dir.startsWith('/') && (edit || pw.length >= 8 || keys.trim() !== '')
  const submit = () => {
    const p: Record<string, unknown> = { name, dir, readonly: ro }
    if (pw) p.password = pw
    if (keys.trim()) p.keys = keys.split('\n').map((k) => k.trim()).filter(Boolean)
    run('sftp.set', p, { host, onDone: () => { onDone?.(); onClose() } })
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={edit ? t('sftp.edit', { name }) : t('sftp.new')} description={t('sftp.dialog_hint')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!ok} onClick={submit}>{t('common.next')}</Button></>}>
      <div className="pb-2">
        <Field label={t('sftp.name')} hint={t('sftp.name_hint')}><Input className="num" value={name} disabled={edit} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <Field label={t('sftp.dir')} hint={t('sftp.dir_hint')}><Input className="num" value={dir} onChange={(e) => setDir(e.target.value)} /></Field>
        <div className="px-4 py-2"><Switch checked={ro} onChange={setRo} label={t('sftp.readonly')} /></div>
        <Field label={t('sftp.password')} hint={edit ? t('sftp.password_keep') : t('sftp.password_hint')}>
          <div className="flex gap-2">
            <Input className="num flex-1" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="off" />
            <Button size="sm" aria-label={t('sftp.generate')} onClick={() => setPw(genPassword())}><RefreshCw className="size-3.5" /></Button>
          </div>
        </Field>
        <Field label={t('sftp.keys')} hint={t('sftp.keys_hint')}><Textarea className="num h-20 text-[12px]" value={keys} onChange={(e) => setKeys(e.target.value)} placeholder="ssh-ed25519 AAAA… me@laptop" /></Field>
        {name && <p className="px-4 text-[12px] text-mute">{t('sftp.connect')} <code className="num">{connect(name)}</code></p>}
      </div>
    </Dialog>
  )
}

// SftpDrawer lists the accounts on this host.
export function SftpDrawer({ dir, onClose }: { dir: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const connect = useConnect()
  const d = useApi<{ items: SftpAccount[] }>(`/api/v1/hosts/${host}/sftp`, [host])
  const [form, setForm] = useState<{ initial: Partial<SftpAccount>; edit?: boolean } | null>(null)
  const items = d.data?.items ?? []
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={t('sftp.title')}
      right={<Button size="sm" variant="primary" onClick={() => setForm({ initial: { dir } })}><Plus className="size-3.5" />{t('sftp.new')}</Button>}>
      <p className="px-4 pt-3 text-[12.5px] text-mute">{t('sftp.hint')}</p>
      {items.length === 0 && d.data && <p className="px-4 py-3 text-[12.5px] text-mute">{t('sftp.empty')}</p>}
      <div className="mt-2">
        {items.map((a) => (
          <div key={a.name} className="flex items-center gap-3 border-b border-line px-4 py-2.5">
            <Dot state={a.mounted ? 'ok' : 'bad'} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 font-semibold">{a.name}{a.readonly && <Pill>{t('sftp.readonly_short')}</Pill>}{(a.has_keys ?? 0) > 0 && <Pill>{t('sftp.n_keys', { n: a.has_keys })}</Pill>}</div>
              <div className="num truncate text-[12px] text-mute">{a.dir}</div>
              <div className="num truncate text-[11.5px] text-mute">{connect(a.name)}</div>
              {!a.mounted && <Notice tone="warn">{t('sftp.not_mounted')}</Notice>}
            </div>
            <Menu trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} items={[
              { label: t('sftp.edit_short'), onClick: () => setForm({ initial: a, edit: true }) },
              { label: t('action.sftp.delete'), danger: true, onClick: () => run('sftp.delete', { name: a.name }, { host, onDone: () => void d.reload() }) },
            ]} />
          </div>
        ))}
      </div>
      {form && <SftpDialog initial={form.initial} edit={form.edit} onClose={() => setForm(null)} onDone={() => void d.reload()} />}
    </Drawer>
  )
}

export function SftpButton({ dir }: { dir: string }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  return <>
    <Button size="sm" onClick={() => setOpen(true)}><KeyRound className="size-3.5" /><span className="max-sm:hidden">{t('sftp.title')}</span></Button>
    {open && <SftpDrawer dir={dir} onClose={() => setOpen(false)} />}
  </>
}
