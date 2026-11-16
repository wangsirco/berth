import { Check, CircleAlert, CircleX } from 'lucide-react'
import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Box } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Select } from '@/components/ui/Field'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'

interface Fwd { id: string; name: string; proto: string; target_ip: string; target_port: string; berth_host?: string; mine: boolean; berth: boolean }
interface Result { wharf: string; target_ip: string; wan_port: number; local_port: number; proto: string; forwards: Fwd[]; issues: { level: string; msg: string }[]; ok: boolean; listens: boolean; listening?: string }

// Port check: "can port 8000 on the router reach this server?" — what Wharf already maps
// there, whether Wharf would accept a new mapping, and whether anything here listens.
export function PortCheck() {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const [port, setPort] = useState('')
  const [local, setLocal] = useState('')
  const [proto, setProto] = useState('tcp')
  const [res, setRes] = useState<Result | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const check = async () => {
    setBusy(true)
    setErr('')
    try {
      setRes(await get<Result>(`/api/v1/wharf/port-check?port=${encodeURIComponent(port)}&local_port=${encodeURIComponent(local)}&proto=${proto}`))
    } catch (e) { setErr((e as Error).message); setRes(null) } finally { setBusy(false) }
  }
  const mine = res?.forwards.find((f) => f.mine && f.proto === res.proto)
  const other = res?.forwards.find((f) => !f.mine && f.proto === res.proto)
  const verdict = !res ? null
    : mine ? { tone: 'ok', icon: Check, text: t('pc.mapped_here', { port: res.wan_port, ip: mine.target_ip, local: mine.target_port }) }
    : other ? { tone: 'bad', icon: CircleX, text: t('pc.taken', { port: res.wan_port, name: other.name, ip: other.target_ip }) }
    : res.ok ? { tone: 'ok', icon: Check, text: t('pc.free', { port: res.wan_port }) }
    : { tone: 'bad', icon: CircleX, text: t('pc.refused', { port: res.wan_port }) }
  const color = { ok: 'text-ok', bad: 'text-bad', warn: 'text-warn' } as Record<string, string>
  return (
    <Box title={t('pc.title')}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <p className="text-[12.5px] text-mute">{t('pc.hint')}</p>
        <div className="flex flex-wrap items-end gap-2">
          <label className="text-[12px] text-mute">{t('pc.router_port')}<Input className="num mt-1 w-28" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))} placeholder="8000" onKeyDown={(e) => e.key === 'Enter' && port && void check()} /></label>
          <label className="text-[12px] text-mute">{t('pc.local_port')}<Input className="num mt-1 w-28" inputMode="numeric" value={local} onChange={(e) => setLocal(e.target.value.replace(/\D/g, ''))} placeholder={port || t('pc.same')} /></label>
          <label className="text-[12px] text-mute">{t('pc.proto')}<Select className="mt-1 w-24" value={proto} onChange={(e) => setProto(e.target.value)}><option value="tcp">TCP</option><option value="udp">UDP</option></Select></label>
          <Button variant="primary" disabled={!port || busy} onClick={() => void check()}>{t('pc.check')}</Button>
        </div>
        {err && <p className="text-bad">{err}</p>}
        {res && verdict && (
          <div className="space-y-1.5 rounded-[var(--radius-box)] border border-line bg-soft px-3.5 py-2.5">
            <p className={'flex items-start gap-1.5 font-semibold ' + color[verdict.tone]}><verdict.icon className="mt-0.5 size-4 shrink-0" aria-hidden />{verdict.text}</p>
            {res.issues.filter((i) => !mine || i.level !== 'error').map((i, k) => <p key={k} className={'text-[12.5px] ' + (i.level === 'error' ? 'text-bad' : 'text-warn')}>{i.msg}</p>)}
            <p className={'flex items-start gap-1.5 text-[12.5px] ' + (res.listens ? 'text-mute' : 'text-warn')}>
              {res.listens ? <Check className="mt-0.5 size-3.5 shrink-0" aria-hidden /> : <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden />}
              {res.listens ? t('pc.listens', { port: res.local_port, proc: res.listening ?? '' }) : t('pc.not_listening', { port: res.local_port })}
            </p>
            {res.forwards.filter((f) => f.proto !== res.proto).map((f) => <p key={f.id} className="text-[12px] text-mute">{t('pc.other_proto', { proto: f.proto.toUpperCase(), name: f.name, ip: f.target_ip })}</p>)}
            {can(user, 'network.manage') && (
              <div className="flex gap-2 pt-1">
                {!mine && !other && res.ok && <Button size="sm" variant="primary" onClick={() => run('wharf.forward', { wan_port: String(res.wan_port), local_port: String(res.local_port), proto: res.proto }, { host: 'local', onDone: () => void check() })}>{t('pc.map', { port: res.wan_port, local: res.local_port })}</Button>}
                {mine?.berth && <Button size="sm" onClick={() => run('wharf.unforward', { id: mine.id, wan_port: String(res.wan_port), local_port: mine.target_port, proto: res.proto }, { host: 'local', onDone: () => void check() })}>{t('action.wharf.unforward')}</Button>}
              </div>
            )}
          </div>
        )}
      </div>
    </Box>
  )
}
