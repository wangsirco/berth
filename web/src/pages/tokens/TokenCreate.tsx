import { useState } from 'react'
import { EquivCommand } from '@/components/change/EquivCommand'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Select, Textarea } from '@/components/ui/Field'
import { ApiError, post } from '@/lib/api'
import { useT } from '@/lib/i18n'

export interface TokenRow {
  id: number; kind: 'pat' | 'agt' | 'cli'; name: string; prefix: string; scopes: string[]; ip_allow: string[]; danger_policy: string
  expires_at: number; last_used_at: number; last_ip: string; uses: number; revoked: boolean; note: string
}

const EXPIRY: Record<string, number> = { '30': 30, '90': 90, '365': 365, never: 0 }

export function TokenCreate({ resources, onClose, onCreated }: { resources: string[]; onClose: () => void; onCreated: (r: { token: string; item: TokenRow }) => void }) {
  const t = useT()
  const [kind, setKind] = useState<'agt' | 'pat'>('agt')
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<Record<string, '' | 'read' | 'write'>>({ containers: 'write', overview: 'read', audit: 'read', tasks: 'read' })
  const [expiry, setExpiry] = useState('90')
  const [ips, setIps] = useState('')
  const [policy, setPolicy] = useState('confirm')
  const [note, setNote] = useState('')
  const [err, setErr] = useState('')
  const create = async () => {
    setErr('')
    const days = EXPIRY[expiry]
    try {
      const r = await post<{ token: string; item: TokenRow }>('/api/v1/tokens', {
        kind, name, note, danger_policy: kind === 'agt' ? policy : undefined,
        scopes: Object.entries(scopes).filter(([, v]) => v).map(([k, v]) => `${k}:${v}`),
        ip_allow: ips.split(/[\s,]+/).filter(Boolean),
        expires_at: days ? Math.floor(Date.now() / 1000) + days * 86400 : 0,
      })
      onCreated(r)
    } catch (e) { setErr((e as ApiError).message) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} wide title={t('tokens.new')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!name} onClick={() => void create()}>{t('tokens.create')}</Button></>}>
      {err && <div className="px-4 pt-3"><Notice tone="bad">{err}</Notice></div>}
      <Field label={t('tokens.kind')}>
        <Segmented value={kind} onChange={setKind} options={[{ value: 'agt', label: t('tokens.kind_agt') }, { value: 'pat', label: t('tokens.kind_pat') }]} />
        <p className="mt-1 text-[12px] text-mute">{t('tokens.kind_' + kind + '_hint')}</p>
      </Field>
      <Field label={t('tokens.name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={kind === 'agt' ? 'claude-mac' : 'backup-script'} /></Field>
      <Field label={t('tokens.scopes')} hint={t('tokens.scopes_hint')}>
        <div className="grid gap-1.5 sm:grid-cols-2">
          {resources.map((r) => (
            <div key={r} className="flex items-center justify-between gap-2 rounded border border-line px-2 py-1 text-[13px]">
              <span>{t('res.' + r)}</span>
              <Segmented<'' | 'read' | 'write'> value={scopes[r] ?? ''} onChange={(v) => setScopes({ ...scopes, [r]: v })}
                options={[{ value: '', label: t('tokens.scope_none') }, { value: 'read', label: t('tokens.scope_read') }, { value: 'write', label: t('tokens.scope_write') }]} />
            </div>
          ))}
        </div>
      </Field>
      {kind === 'agt' && (
        <Field label={t('tokens.policy')}>
          <div className="space-y-1.5 text-[13px]">
            {['confirm', 'run', 'readonly'].map((p) => (
              <label key={p} className="flex gap-2"><input type="radio" name="policy" checked={policy === p} onChange={() => setPolicy(p)} className="mt-1" />
                <span><b className="font-semibold">{t('tokens.policy_' + p)}</b><span className="block text-mute">{t('tokens.policy_' + p + '_hint')}</span></span></label>
            ))}
          </div>
        </Field>
      )}
      <Field label={t('tokens.expiry')}>
        <Select value={expiry} onChange={(e) => setExpiry(e.target.value)} className="w-48">
          {Object.keys(EXPIRY).map((k) => <option key={k} value={k}>{t('tokens.expiry_' + k)}</option>)}
        </Select>
        {expiry === 'never' && <p className="mt-1.5 text-[12.5px] text-warn">{t('tokens.never_warning')}</p>}
      </Field>
      <Field label={t('tokens.ip_allow')} hint={t('tokens.ip_allow_hint')}><Input className="num" value={ips} onChange={(e) => setIps(e.target.value)} placeholder="10.0.2.0/24, 100.64.0.0/10" /></Field>
      <Field label={t('tokens.note')} className="pb-4"><Textarea className="h-14" value={note} onChange={(e) => setNote(e.target.value)} /></Field>
    </Dialog>
  )
}

// TokenShown displays a new token once, with how to use it.
export function TokenShown({ token, item, onClose }: { token: string; item: TokenRow; onClose: () => void }) {
  const t = useT()
  const origin = location.origin
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} wide title={t('tokens.created', { name: item.name })}
      footer={<Button variant="primary" className="ml-auto" onClick={onClose}>{t('tokens.saved_it')}</Button>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <Notice tone="warn">{t('tokens.once')}</Notice>
        <EquivCommand command={token} />
        {item.kind === 'agt' && (
          <>
            <h3 className="font-semibold">{t('tokens.mcp_title')}</h3>
            <EquivCommand command={`claude mcp add --transport http berth ${origin}/mcp --header "Authorization: Bearer ${token}"`} />
            <p className="text-mute">{t('tokens.mcp_ca')}</p>
            <EquivCommand command={`curl -fsSk ${origin}/api/v1/ca.crt -o berth-ca.crt && sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain berth-ca.crt`} />
            <p className="text-mute">{t('tokens.mcp_node')}</p>
            <EquivCommand command={`export NODE_EXTRA_CA_CERTS="$PWD/berth-ca.crt"`} />
          </>
        )}
        {item.kind === 'pat' && <EquivCommand command={`curl -H "Authorization: Bearer ${token}" ${origin}/api/v1/hosts/local/containers`} />}
      </div>
    </Dialog>
  )
}
