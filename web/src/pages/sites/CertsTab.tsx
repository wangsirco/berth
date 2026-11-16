import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Box, Empty, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented, Select, Switch, Textarea } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT, type TFn } from '@/lib/i18n'
import { useReauth } from '../account/reauth'
import { daysLeft, type CertRow } from './types'

interface Acme { host: string; domain: string; issuer: string; not_after: string }

function download(name: string, data: BlobPart, type = 'application/octet-stream') {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([data], { type }))
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}

const expiry = (iso: string, t: TFn) => {
  const d = daysLeft(iso)
  return <Pill tone={d < 0 ? 'bad' : d < 14 ? 'warn' : 'ok'}>{d < 0 ? t('certs.expired') : t('certs.days_left', { n: d })}</Pill>
}

export function CertsTab() {
  const t = useT()
  const run = useChange()
  const reauth = useReauth()
  const { user } = useAuth()
  const { host } = useHost()
  const list = useApi<{ items: CertRow[]; acme: Acme[]; renew_days: number }>('/api/v1/certs')
  const [create, setCreate] = useState<null | CertRow | 'new'>(null)
  const manage = can(user, 'certs.manage')
  const reload = () => void list.reload()
  const exportCert = async (c: CertRow, format: 'pem' | 'pfx') => {
    const password = format === 'pfx' ? prompt(t('certs.pfx_password')) ?? '' : ''
    const r = await reauth(() => post<{ pfx?: string; cert_pem?: string; key_pem?: string; filename: string }>(`/api/v1/certs/${c.id}/export`, { format, password }))
    if (r.pfx) download(r.filename, Uint8Array.from(atob(r.pfx), (ch) => ch.charCodeAt(0)))
    else { download(r.filename + '.crt', r.cert_pem!, 'text/plain'); download(r.filename + '.key', r.key_pem!, 'text/plain') }
  }
  return (
    <div className="space-y-4">
      <Box title={t('certs.managed')} count={list.data?.items.length} right={manage && <Button size="sm" variant="primary" onClick={() => setCreate('new')}>{t('certs.add')}</Button>}>
        {list.data?.items.length === 0 && <Empty title={t('certs.none')}>{t('certs.none_hint')}</Empty>}
        {list.data?.items.map((c) => (
          <Row key={c.id}>
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13.5px] font-semibold">{c.name}</div>
              <div className="truncate text-[12px] text-mute">{t('certs.src_' + c.source)} · {c.info.domains.join(', ')} · {c.info.key_type}{c.used_by?.length ? ' · ' + t('certs.used_by', { sites: c.used_by.join(', ') }) : ''}</div>
            </div>
            {c.auto_renew && <Pill>{t('certs.auto_renew')}</Pill>}
            {expiry(c.info.not_after, t)}
            {manage && <Menu trigger={<Button size="sm" variant="ghost">⋯</Button>} items={[
              ...(c.source === 'self' || c.source === 'internal' ? [{ label: t('action.cert.reissue'), onClick: () => setCreate(c) }] : []),
              ...c.history.map((h) => ({ label: t('certs.switch_back', { date: h.not_after.slice(0, 10) }), onClick: () => run('cert.rollback', { id: c.id, fingerprint: h.fingerprint }, { host, onDone: reload }) })),
              { label: t('certs.export_pem'), onClick: () => void exportCert(c, 'pem') },
              { label: t('certs.export_pfx'), onClick: () => void exportCert(c, 'pfx') },
              'sep',
              { label: t('action.cert.delete'), danger: true, onClick: () => run('cert.delete', { id: c.id }, { host, onDone: reload }) },
            ]} />}
          </Row>
        ))}
      </Box>
      <Box title={t('certs.acme')} count={list.data?.acme.length}>
        {list.data?.acme.length === 0 && <Empty title={t('certs.acme_none')}>{t('certs.acme_hint')}</Empty>}
        {list.data?.acme.map((c) => (
          <Row key={c.host + c.domain + c.issuer}>
            <div className="min-w-0 flex-1"><div className="truncate text-[13.5px] font-semibold">{c.domain}</div>
              <div className="truncate text-[12px] text-mute">{c.issuer === 'local' ? t('certs.caddy_local') : c.issuer.replace(/-directory$/, '')} · {c.host}</div></div>
            {c.issuer === 'local' ? <Pill>{t('certs.caddy_local_rotate')}</Pill> : expiry(c.not_after, t)}
          </Row>
        ))}
        <p className="px-4 py-2 text-[12px] text-mute">{t('certs.acme_renew')}</p>
      </Box>
      {create && <CertDialog cert={create === 'new' ? undefined : create} onClose={() => setCreate(null)} onDone={reload} />}
    </div>
  )
}

function CertDialog({ cert, onClose, onDone }: { cert?: CertRow; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [source, setSource] = useState<'internal' | 'self' | 'import'>((cert?.source as 'self') ?? 'internal')
  const [name, setName] = useState(cert?.name ?? '')
  const [dns, setDns] = useState((cert?.info.domains ?? []).join(' '))
  const [ips, setIps] = useState('')
  const [f, setF] = useState({ cn: '', o: '', ou: '', c: '', days: 397, key_type: 'ec256', client: false, auto: cert?.auto_renew ?? true })
  const [pem, setPem] = useState('')
  const [key, setKey] = useState('')
  const [pfx, setPfx] = useState('')
  const [pw, setPw] = useState('')
  const split = (s: string) => s.split(/[\s,]+/).filter(Boolean)
  const fields = { cn: f.cn, o: f.o, ou: f.ou, c: f.c, dns: split(dns), ips: split(ips), days: Number(f.days), key_type: f.key_type, usage: f.client ? ['server', 'client'] : ['server'] }
  const submit = () => {
    if (cert) return run('cert.reissue', { id: cert.id, name, fields, auto_renew: f.auto }, { host, onDone: () => { onDone(); onClose() } })
    const p = source === 'import' ? { source, name, cert_pem: pem, key_pem: key, pfx, password: pw } : { source, name, fields, auto_renew: f.auto }
    run('cert.create', p, { host, onDone: () => { onDone(); onClose() } })
  }
  const readFile = (file: File) => {
    const r = new FileReader()
    if (/\.(pfx|p12)$/i.test(file.name)) { r.onload = () => setPfx(btoa(String.fromCharCode(...new Uint8Array(r.result as ArrayBuffer)))); r.readAsArrayBuffer(file) }
    else { r.onload = () => setPem((cur) => cur + (r.result as string)); r.readAsText(file) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={cert ? t('certs.reissue_title', { name: cert.name }) : t('certs.add')} wide
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={submit}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        {!cert && <Segmented value={source} onChange={setSource} options={[{ value: 'internal', label: t('certs.src_internal') }, { value: 'self', label: t('certs.src_self') }, { value: 'import', label: t('certs.src_import') }]} />}
        <p className="text-[12.5px] text-mute">{t('certs.src_hint_' + source)}</p>
        <Field label={t('certs.name')}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        {source === 'import' ? <>
          <Field label={t('certs.file')} hint={t('certs.file_hint')}><input type="file" accept=".pem,.crt,.key,.pfx,.p12,.cer" multiple onChange={(e) => Array.from(e.target.files ?? []).forEach(readFile)} /></Field>
          <Field label={t('certs.pem')}><Textarea className="num h-28 text-[11.5px]" value={pem} onChange={(e) => setPem(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" /></Field>
          <Field label={t('certs.key')}><Textarea className="num h-20 text-[11.5px]" value={key} onChange={(e) => setKey(e.target.value)} placeholder="-----BEGIN PRIVATE KEY-----" /></Field>
          {pfx && <Field label={t('certs.pfx_password')}><Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>}
        </> : <>
          <Field label={t('certs.dns_names')} hint={source === 'internal' ? t('certs.internal_names') : undefined}><Input value={dns} onChange={(e) => setDns(e.target.value)} placeholder="nas.lan *.home.lan" /></Field>
          <Field label={t('certs.ips')}><Input value={ips} onChange={(e) => setIps(e.target.value)} placeholder="192.168.1.20" /></Field>
          <div className="grid gap-2 sm:grid-cols-4">
            <Field label="CN"><Input value={f.cn} onChange={(e) => setF({ ...f, cn: e.target.value })} /></Field>
            <Field label="O"><Input value={f.o} onChange={(e) => setF({ ...f, o: e.target.value })} /></Field>
            <Field label="OU"><Input value={f.ou} onChange={(e) => setF({ ...f, ou: e.target.value })} /></Field>
            <Field label="C"><Input value={f.c} maxLength={2} onChange={(e) => setF({ ...f, c: e.target.value })} /></Field>
          </div>
          <div className="grid gap-2 sm:grid-cols-2">
            <Field label={t('certs.days')}><Input type="number" min={1} max={3650} value={f.days} onChange={(e) => setF({ ...f, days: Number(e.target.value) })} /></Field>
            <Field label={t('certs.key_type')}><Select value={f.key_type} onChange={(e) => setF({ ...f, key_type: e.target.value })}>
              <option value="ec256">ECDSA P-256</option><option value="ec384">ECDSA P-384</option><option value="rsa2048">RSA 2048</option><option value="rsa4096">RSA 4096</option>
            </Select></Field>
          </div>
          <Switch checked={f.client} onChange={(v) => setF({ ...f, client: v })} label={t('certs.client_auth')} />
          <Switch checked={f.auto} onChange={(v) => setF({ ...f, auto: v })} label={t('certs.auto_renew_label')} />
        </>}
      </div>
    </Dialog>
  )
}
