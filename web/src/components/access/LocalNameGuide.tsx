import { useEffect, useState } from 'react'
import { Check, Copy, Download, ExternalLink, Laptop, Smartphone } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { get } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

// LocalNameGuide: what Passkey needs, in four steps — a name instead of an IP address, a way
// for devices to find the name (mDNS for .local, the router for other names), this device
// trusting the panel's certificate, and a check that the name opens the panel.

interface Status {
  hostname: string; suggested: string; current: string; port: string; ip: string; url?: string; opened_as: string; wharf_paired: boolean; entry?: string
  wharf_dns?: { suffixes: string[]; registered: string }
  no_port?: string[]
  mdns: { installed: boolean; active: boolean; hostname: string; aliases: string[]; firewall_open: boolean }
}

type OS = 'mac' | 'ios' | 'windows' | 'android' | 'linux'
const detectOS = (): OS => {
  const ua = navigator.userAgent
  if (/iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)) return 'ios'
  if (/Android/.test(ua)) return 'android'
  if (/Windows/.test(ua)) return 'windows'
  if (/Mac OS X/.test(ua)) return 'mac'
  return 'linux'
}
const NAME_RE = /^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+(local|lan|home\.arpa|internal)$/

export function LocalNameGuide({ embedded, wizard, onSaved }: { embedded?: boolean; wizard?: boolean; onSaved?: (on: boolean) => void }) {
  const t = useT()
  const run = useChange()
  const st = useApi<Status>('/api/v1/local-name', [])
  const [mode, setMode] = useState<'auto' | 'lan' | 'own'>('auto')
  const [own, setOwn] = useState('')
  const [os, setOS] = useState<OS>(detectOS)
  const [check, setCheck] = useState<'idle' | 'checking' | 'ok' | 'bad'>('idle')
  const [copied, setCopied] = useState(false)
  const s = st.data
  const lanName = (s?.hostname || 'berth') + '.lan'
  const name = (mode === 'auto' ? s?.suggested ?? '' : mode === 'lan' ? lanName : own.trim().toLowerCase())
  const valid = NAME_RE.test(name)
  const isLocal = name.endsWith('.local')
  const routerAuto = !!s?.wharf_dns && s.wharf_dns.suffixes.some((x) => name.endsWith('.' + x.replace(/^\./, '')))
  const saved = !!s && s.current === name && (routerAuto ? s.wharf_dns!.registered === name : true) && (!isLocal || (s.mdns.active && (name === s.mdns.hostname + '.local' || s.mdns.aliases.includes(name))))
  const url = s ? `https://${name}${s.port && s.port !== '443' && !s.no_port?.includes(name) ? ':' + s.port : ''}` : ''
  const onName = !!s && s.opened_as === name
  useEffect(() => { onSaved?.(saved) }, [saved])
  // with a Wharf that registers names, host.lan is what every device finds: start there
  useEffect(() => { if (s?.wharf_dns?.suffixes.includes('lan') && !s.current) setMode('lan') }, [!!s?.wharf_dns])
  // over to the name with a one-time link: the entry and the sign-in come along
  const goThere = async () => {
    const h = await get<{ url: string }>('/api/v1/access/hop?to=lan&path=' + encodeURIComponent(wizard ? '/welcome?step=localname' : location.pathname)).catch(() => null)
    location.href = h?.url ?? url + (s?.entry || '/login')
  }
  const apply = () => run('panel.local_name', { name }, { onDone: () => void st.reload() })
  const test = async () => {
    setCheck('checking')
    try { await fetch(url + '/api/v1/meta', { mode: 'no-cors', cache: 'no-store' }); setCheck('ok') } catch { setCheck('bad') }
  }
  if (!s) return <div className="p-4 text-[13px] text-mute">{t('common.loading')}</div>
  const num = (i: number, done: boolean) => <span className={cx('inline-flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold', done ? 'bg-ok text-white' : 'bg-acc text-white')}>{done ? <Check className="size-3" /> : i + 1}</span>
  return (
    <div className={cx('space-y-4 text-[13px]', !embedded && 'px-4 py-3')} data-local-name-guide>
      <p className="text-mute">{t('lname.why')}</p>
      {onName && <Notice tone="acc">{t('lname.already_on', { name })}</Notice>}

      {/* 1. the name */}
      <section className="space-y-2">
        <div className="flex items-center gap-2 font-semibold">{num(0, valid)}{t('lname.s1')}</div>
        <Segmented value={mode} onChange={setMode} options={[{ value: 'auto', label: t('lname.auto', { name: s.suggested }) }, ...(s.wharf_dns?.suffixes.includes('lan') ? [{ value: 'lan' as const, label: t('lname.auto_lan', { name: lanName }) }] : []), { value: 'own', label: t('lname.own') }]} />
        {mode === 'own' && <Field label={t('lname.own_label')} hint={t('lname.own_hint')}>
          <Input className="num" placeholder="nas.local / berth.lan" value={own} onChange={(e) => setOwn(e.target.value)} />
        </Field>}
        {mode === 'own' && own && !valid && <p className="text-[12px] text-warn">{t('lname.bad_name')}</p>}
      </section>

      {/* 2. devices find it */}
      <section className="space-y-2">
        <div className="flex items-center gap-2 font-semibold">{num(1, saved)}{t('lname.s2')}</div>
        {isLocal
          ? <p className="text-mute">{t('lname.mdns_d')}</p>
          : <>
            <p className="text-mute">{t(routerAuto ? 'lname.router_auto' : s.wharf_paired ? 'lname.router_wharf' : 'lname.router_d')}</p>
            {valid && !routerAuto && <div className="flex items-center gap-2 rounded bg-soft px-2 py-1.5">
              <code className="num flex-1 text-[12px]">{s.ip}  {name}</code>
              <button type="button" aria-label={t('common.copy')} className="text-mute hover:text-fg" onClick={() => { void navigator.clipboard?.writeText(`${s.ip} ${name}`); setCopied(true); setTimeout(() => setCopied(false), 1500) }}>{copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}</button>
            </div>}
          </>}
        {saved
          ? <Notice tone="acc">{t('lname.saved', { name })}</Notice>
          : <Button size="sm" variant="primary" disabled={!valid} onClick={apply}>{t(isLocal ? 'lname.enable_mdns' : 'lname.save_name')}</Button>}
      </section>

      {/* 3. this device trusts the certificate */}
      <section className="space-y-2">
        <div className="flex items-center gap-2 font-semibold">{num(2, check === 'ok')}{t('lname.s3')}</div>
        <p className="text-mute">{t('lname.trust_d')}</p>
        <div className="flex flex-wrap items-center gap-2">
          <Segmented value={os} onChange={setOS} options={(['mac', 'ios', 'windows', 'android', 'linux'] as OS[]).map((o) => ({ value: o, label: t('lname.os_' + o) }))} />
          <a href="/api/v1/ca.crt" download="berth-ca.crt"><Button size="sm"><Download className="size-3.5" />{t('lname.download_ca')}</Button></a>
        </div>
        <ol className="list-decimal space-y-0.5 pl-5 text-[12.5px]">
          {t('lname.steps_' + os).split('\n').map((l) => <li key={l}>{l}</li>)}
        </ol>
        {(os === 'mac' || os === 'windows' || os === 'linux') && <p className="flex items-center gap-1 text-[12px] text-mute"><Laptop className="size-3.5" />{t('lname.firefox_note')}</p>}
        {os === 'ios' && <p className="flex items-center gap-1 text-[12px] text-mute"><Smartphone className="size-3.5" />{t('lname.ios_note')}</p>}
      </section>

      {/* 4. check, then open it there */}
      <section className="space-y-2">
        <div className="flex items-center gap-2 font-semibold">{num(3, check === 'ok')}{t('lname.s4')}</div>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" disabled={!saved || check === 'checking'} onClick={() => void test()}>{t(check === 'checking' ? 'lname.checking' : 'lname.check', { url })}</Button>
          {check === 'ok' && !onName && <Button size="sm" variant="primary" onClick={() => void goThere()}><ExternalLink className="size-3.5" />{t('lname.open_there')}</Button>}
        </div>
        {check === 'ok' && <Notice tone="acc">{t(onName ? 'lname.ok_here' : 'lname.ok', { name })}</Notice>}
        {check === 'bad' && <Notice tone="warn">{t('lname.bad', { name })}</Notice>}
      </section>
    </div>
  )
}

export function LocalNameDialog({ onClose }: { onClose: () => void }) {
  const t = useT()
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('lname.title')} footer={<Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button>}>
      <LocalNameGuide />
    </Dialog>
  )
}
