import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input } from '@/components/ui/Field'
import { ApiError, get, post } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'
import { useTheme } from '@/lib/theme'
import { useEvents } from '@/lib/useEvents'
import { BuiltinExtensions } from './Builtin'

export interface Ext {
  id: string; name: Record<string, string>; version: string; description?: Record<string, string>; permissions: string[]; events?: string[]
  source: string; scopes: string[]; installed_by: string; installed_at: number; running: boolean
}

export const extName = (e: Pick<Ext, 'id' | 'name'>, locale: string) => e.name?.[locale] || e.name?.en || e.id

// Third-party extensions (方案 §9): what is installed, with the scopes each was granted.
export default function ExtensionsPage() {
  const t = useT()
  const run = useChange()
  const nav = useNavigate()
  const { host } = useHost()
  const { user } = useAuth()
  const [locale] = useLocale()
  const d = useApi<{ items: Ext[] }>('/api/v1/extensions')
  useEvents(['change.done'], () => void d.reload())
  const [installing, setInstalling] = useState(false)
  const manage = user?.role === 'super'
  return (
    <div className="space-y-4">
      <BuiltinExtensions />
      <Box title={t('ext.third_party')} count={d.data?.items.length} right={manage && <Button size="sm" variant="primary" onClick={() => setInstalling(true)}>{t('ext.install')}</Button>}>
        {d.data?.items.length === 0 && <Empty title={t('ext.empty')}>{t('ext.empty_hint')}</Empty>}
        {d.data?.items.map((e) => (
          <Row key={e.id} onClick={() => nav(hostPath(host, 'ext/' + e.id))}>
            <Dot state={e.running ? 'ok' : 'bad'} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{extName(e, locale)} <span className="font-normal text-mute">{e.version}</span></div>
              <div className="truncate text-[12px] text-mute">{e.description?.[locale] || e.description?.en}</div>
              <div className="mt-1 flex flex-wrap gap-1">{e.scopes.map((s) => <Pill key={s}>{s}</Pill>)}{e.permissions.filter((p) => !e.scopes.includes(p)).map((s) => <span key={s} className="rounded-full border border-dashed border-line px-2 text-[11.5px] text-mute line-through">{s}</span>)}</div>
            </div>
            {manage && <Button size="sm" variant="ghost" onClick={(ev) => { ev.stopPropagation(); run('ext.remove', { id: e.id }, { host: 'local' }) }}>{t('action.ext.remove')}</Button>}
          </Row>
        ))}
      </Box>
      {installing && <LegacyInstall onClose={() => setInstalling(false)} />}
    </div>
  )
}

// LegacyInstall: an older extension (berth-extension.yaml) from a folder on the server.
export function LegacyInstall({ onClose, onBack }: { onClose: () => void; onBack?: () => void }) {
  const t = useT()
  const run = useChange()
  const [locale] = useLocale()
  const [source, setSource] = useState('')
  const [m, setM] = useState<Ext | null>(null)
  const [grant, setGrant] = useState<Set<string>>(new Set())
  const [err, setErr] = useState('')
  const read = async () => {
    setErr('')
    try { const r = await post<Ext>('/api/v1/extensions/manifest', { source }); setM(r); setGrant(new Set(r.permissions)) } catch (e) { setErr((e as ApiError).message) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('ext.install')}
      footer={<>{onBack && <Button variant="ghost" onClick={onBack}>{t('common.back')}</Button>}<Button onClick={onClose}>{t('common.cancel')}</Button>
        {!m ? <Button variant="primary" disabled={!source.startsWith('/')} onClick={() => void read()}>{t('import.scan')}</Button>
          : <Button variant="primary" onClick={() => run('ext.install', { source, grant: [...grant] }, { host: 'local', onDone: onClose })}>{t('common.continue')}</Button>}</>}>
      <div className="space-y-3">
        {err && <Notice tone="bad">{err}</Notice>}
        <Field label={t('ext.source')} hint={t('ext.source_hint')}><Input className="num" value={source} disabled={!!m} onChange={(e) => setSource(e.target.value)} placeholder="/srv/berth/extensions/hello" /></Field>
        {m && <>
          <div className="text-[13.5px]"><b>{extName(m, locale)}</b> <span className="text-mute">{m.version}</span><div className="text-[12.5px] text-mute">{m.description?.[locale] || m.description?.en}</div></div>
          <Field label={t('ext.asks')} hint={t('ext.asks_hint')}>
            <div className="space-y-1.5">{m.permissions.map((p) => (
              <label key={p} className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={grant.has(p)} onChange={(e) => { const n = new Set(grant); if (e.target.checked) n.add(p); else n.delete(p); setGrant(n) }} />
                <span className="num">{p}</span><span className="text-mute">{t('ext.scope_' + p.split(':')[0])} · {t('ext.' + p.split(':')[1])}</span></label>
            ))}</div>
          </Field>
          {m.events && m.events.length > 0 && <p className="text-[12.5px] text-mute">{t('ext.events', { list: m.events.join(', ') })}</p>}
        </>}
      </div>
    </Dialog>
  )
}

// One extension's page, in a sandboxed frame; it is told the theme, language and host.
export function ExtensionView() {
  const t = useT()
  const { id = '' } = useParams()
  const { host } = useHost()
  const [locale] = useLocale()
  const { dark } = useTheme()
  const resolved = dark ? 'dark' : 'light'
  const [url, setUrl] = useState('')
  const [err, setErr] = useState('')
  const frame = useRef<HTMLIFrameElement>(null)
  useEffect(() => { get<{ url: string }>(`/api/v1/extensions/${encodeURIComponent(id)}/open`).then((r) => setUrl(r.url)).catch((e: ApiError) => setErr(e.message)) }, [id])
  const tell = () => frame.current?.contentWindow?.postMessage({ type: 'berth.context', theme: resolved, locale, host }, '*')
  useEffect(tell, [resolved, locale, host]) // eslint-disable-line react-hooks/exhaustive-deps
  if (err) return <Notice tone="bad">{err}</Notice>
  if (!url) return <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  return <iframe ref={frame} title={id} src={url} onLoad={tell} sandbox="allow-scripts allow-forms allow-popups allow-downloads" className="h-[calc(100vh-170px)] w-full rounded-[var(--radius-box)] border border-line bg-canvas" />
}
