import { Check, Cloud, ExternalLink, Globe, Lock, ShieldCheck, Waypoints, BarChart3 } from 'lucide-react'
import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input } from '@/components/ui/Field'
import { useT } from '@/lib/i18n'

const TOKEN_URL = 'https://dash.cloudflare.com/profile/api-tokens'

// What a Berth token needs: [permission as Cloudflare names it, what it is for, required?]
const PERMS: [string, string, boolean][] = [
  ['区域 · Zone · 读取 / Zone · Zone · Read', 'cfw.p_zone', true],
  ['区域 · DNS · 编辑 / Zone · DNS · Edit', 'cfw.p_dns', true],
  ['账户 · Cloudflare Tunnel · 编辑 / Account · Cloudflare Tunnel · Edit', 'cfw.p_tunnel', false],
  ['账户 · Access: Apps and Policies · 编辑 / Account · Access: Apps and Policies · Edit', 'cfw.p_access', false],
  ['账户 · Account Firewall Access Rules · 编辑 / Account · Account Firewall Access Rules · Edit', 'cfw.p_firewall', false],
  ['区域 · Zone Settings · 编辑 / Zone · Zone Settings · Edit', 'cfw.p_settings', false],
  ['区域 · Analytics · 读取 / Zone · Analytics · Read', 'cfw.p_analytics', false],
]

const WHAT = [
  [Waypoints, 'tunnel'], [Globe, 'dns'], [BarChart3, 'ip'], [ShieldCheck, 'attack'], [Lock, 'access'], [Cloud, 'bans'],
] as const

// The Cloudflare page before any account: what it brings, then a token in three steps.
export function CloudflareWelcome({ onDone }: { onDone: () => void }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  return (
    <div className="space-y-4">
      <Box title={t('cfw.title')}>
        <div className="space-y-3 px-4 py-4 text-[13px]">
          <p className="text-[13.5px]">{t('cfw.intro')}</p>
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {WHAT.map(([Icon, k]) => (
              <div key={k} className="flex gap-2.5 rounded-[var(--radius-box)] border border-line p-3">
                <Icon className="mt-0.5 size-4 shrink-0 text-acc" aria-hidden />
                <div><div className="font-semibold">{t('cfw.w_' + k)}</div><div className="text-[12px] text-mute">{t('cfw.w_' + k + '_d')}</div></div>
              </div>
            ))}
          </div>
          <Button variant="primary" onClick={() => setOpen(true)}>{t('cfw.start')}</Button>
          <p className="text-[12px] text-mute">{t('cfw.free')}</p>
        </div>
      </Box>
      {open && <TokenDialog onClose={() => setOpen(false)} onDone={onDone} />}
    </div>
  )
}

// TokenDialog: make a token at Cloudflare (with exactly the permissions listed), paste it.
// Also used later to replace a token or add a second account.
export function TokenDialog({ existing, newId, onClose, onDone }: { existing?: { id: string; name: string }; newId?: string; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [step, setStep] = useState(existing ? 2 : 1)
  const [token, setToken] = useState('')
  const [name, setName] = useState(existing?.name ?? (newId && newId !== 'cf' ? 'Cloudflare ' + newId.slice(2) : 'Cloudflare'))
  const save = () => run('integration.set', { id: existing?.id ?? newId ?? 'cf', kind: 'cloudflare', name, credentials: { api_token: token.trim() } }, { host, onDone: () => { onDone(); onClose() } })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={existing ? t('cfw.replace_title') : t('cfw.connect_title')} wide
      footer={<>
        {step > 1 && !existing && <Button onClick={() => setStep(step - 1)}>{t('common.back')}</Button>}
        <Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        {step === 1 ? <Button variant="primary" onClick={() => setStep(2)}>{t('cfw.have_token')}</Button>
          : <Button variant="primary" disabled={token.trim().length < 20} onClick={save}>{t('cfw.save')}</Button>}
      </>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {!existing && <ol className="flex gap-1.5 text-[12px]">
          {[1, 2].map((n) => <li key={n} className={cx('flex items-center gap-1 rounded-full border px-2.5 py-0.5', step >= n ? 'border-acc text-acc' : 'border-line text-mute')}>{step > n ? <Check className="size-3" /> : n}. {t('cfw.step' + n)}</li>)}
        </ol>}
        {step === 1 && <>
          <p>{t('cfw.s1')}</p>
          <a href={TOKEN_URL} target="_blank" rel="noreferrer"><Button size="sm"><ExternalLink className="size-3.5" />{t('cfw.open_cf')}</Button></a>
          <p className="text-[12.5px] text-mute">{t('cfw.s1_how')}</p>
          <div className="overflow-hidden rounded-[var(--radius-box)] border border-line">
            {PERMS.map(([perm, use, req]) => (
              <div key={perm} className="grid grid-cols-[1fr_auto] gap-x-3 border-b border-line px-3 py-2 last:border-b-0 sm:grid-cols-[1.3fr_1fr_auto]">
                <span className="num text-[12px]">{perm}</span>
                <span className="text-[12px] text-mute max-sm:col-span-2 max-sm:row-start-2">{t(use)}</span>
                <span className={cx('text-[11.5px]', req ? 'text-acc' : 'text-mute')}>{req ? t('cfw.required') : t('cfw.optional')}</span>
              </div>
            ))}
          </div>
          <p className="text-[12.5px] text-mute">{t('cfw.s1_scope')}</p>
        </>}
        {step === 2 && <>
          <p>{existing ? t('cfw.replace_hint') : t('cfw.s2')}</p>
          <Field label={t('cfw.token')}><Input type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder="••••••••••••••••••••" autoFocus /></Field>
          <Field label={t('dns.account_name')} hint={t('cfw.name_hint')}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <Notice>{t('cfw.s2_after')}</Notice>
          <p className="text-[12px] text-mute">{t('dns.sealed_note')}</p>
        </>}
      </div>
    </Dialog>
  )
}
