import { useState } from 'react'
import { useHost } from '@/components/shell/host'
import { Dot, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Textarea } from '@/components/ui/Field'
import { ApiError, post } from '@/lib/api'
import { useT } from '@/lib/i18n'

type Source = 'caddyfile' | 'npm' | '1panel'
interface Item { key: string; type: string; title: string; detail?: string; ok: boolean; reason?: string; kind?: string; params?: unknown }
interface Done { status: string; error?: string; id?: string }

// Import from a Caddyfile, Nginx Proxy Manager or 1Panel (方案 §14). The scan only reads;
// what is ticked is then created as ordinary changes (each in the audit log, each undoable),
// and the other panel is left as it was.
export function ImportDialog({ onClose, initial = 'caddyfile' }: { onClose: () => void; initial?: Source }) {
  const t = useT()
  const { host } = useHost()
  const [src, setSrc] = useState<Source>(initial)
  const [text, setText] = useState('')
  const [path, setPath] = useState('')
  const [file, setFile] = useState<string>('')
  const [items, setItems] = useState<Item[] | null>(null)
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<Record<string, Done>>({})
  const scan = async () => {
    setErr(''); setBusy(true); setDone({})
    try {
      const r = await post<{ items: Item[]; path?: string }>(`/api/v1/hosts/${host}/import/scan`, { source: src, text, path, file })
      setItems(r.items)
      setSel(new Set(r.items.filter((i) => i.ok && i.reason !== 'import.disabled').map((i) => i.key)))
      if (r.path) setPath(r.path)
    } catch (e) { setErr((e as ApiError).message) } finally { setBusy(false) }
  }
  const run = async () => {
    setBusy(true)
    for (const it of (items ?? []).filter((i) => sel.has(i.key) && i.kind)) {
      setDone((d) => ({ ...d, [it.key]: { status: 'running' } }))
      try {
        const c = await post<{ id: string; status: string; result?: { error?: { message?: string } } }>(`/api/v1/hosts/${host}/changes/${it.kind}`, it.params)
        setDone((d) => ({ ...d, [it.key]: { status: c.status, id: c.id, error: c.result?.error?.message } }))
      } catch (e) { setDone((d) => ({ ...d, [it.key]: { status: 'failed', error: (e as ApiError).message } })) }
    }
    setBusy(false)
  }
  const upload = (f: File | undefined) => {
    if (!f) return
    const rd = new FileReader()
    rd.onload = () => setFile(String(rd.result).split(',')[1] ?? '')
    rd.readAsDataURL(f)
  }
  const chosen = (items ?? []).filter((i) => sel.has(i.key) && i.kind).length
  const finished = Object.keys(done).length > 0 && !busy
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('import.title')} wide
      footer={<>
        <Button onClick={onClose}>{finished ? t('common.close') : t('common.cancel')}</Button>
        {!items ? <Button variant="primary" disabled={busy || (src === 'caddyfile' && !text.trim() && !path.trim())} onClick={() => void scan()}>{busy ? t('import.scanning') : t('import.scan')}</Button>
          : !finished && <Button variant="primary" disabled={busy || chosen === 0} onClick={() => void run()}>{t('import.run', { n: chosen })}</Button>}
      </>}>
      <div className="space-y-3">
        {err && <Notice tone="bad">{err}</Notice>}
        {!items ? <>
          <Segmented value={src} onChange={(s) => { setSrc(s); setPath(''); setFile('') }} options={[{ value: 'caddyfile', label: 'Caddyfile' }, { value: 'npm', label: 'Nginx Proxy Manager' }, { value: '1panel', label: '1Panel' }]} />
          <p className="text-[12.5px] text-mute">{t('import.hint_' + src)}</p>
          {src === 'caddyfile' && <Field label={t('import.paste')}><Textarea className="num min-h-40 text-[12px]" value={text} onChange={(e) => setText(e.target.value)} placeholder={'app.example.com {\n  reverse_proxy 127.0.0.1:3000\n}'} /></Field>}
          <Field label={t('import.path')} hint={src === 'caddyfile' ? t('import.path_caddy') : t('import.path_auto')}><Input className="num" value={path} onChange={(e) => setPath(e.target.value)} placeholder={src === 'npm' ? '/data/database.sqlite' : src === '1panel' ? '/opt/1panel/db/1Panel.db' : '/etc/caddy/Caddyfile'} /></Field>
          {src !== 'caddyfile' && <Field label={t('import.upload')}><input type="file" className="text-[12.5px]" onChange={(e) => upload(e.target.files?.[0])} /></Field>}
        </> : <>
          {items.length === 0 && <div className="text-[13px] text-mute">{t('import.nothing')}</div>}
          <div className="max-h-[55vh] overflow-auto rounded-[var(--radius-box)] border border-line">
            {items.map((it) => {
              const d = done[it.key]
              return (
                <label key={it.key} className="flex items-start gap-3 border-b border-line px-3 py-2 text-[13px] last:border-b-0">
                  <input type="checkbox" className="mt-1" disabled={!it.kind || !!d || busy} checked={sel.has(it.key)}
                    onChange={(e) => { const n = new Set(sel); if (e.target.checked) n.add(it.key); else n.delete(it.key); setSel(n) }} />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2"><Pill>{t('import.type_' + it.type)}</Pill><span className="truncate font-semibold">{it.title}</span></div>
                    {it.detail && <div className="num truncate text-[12px] text-mute">{it.detail}</div>}
                    {it.reason && <div className={'text-[12px] ' + (it.ok ? 'text-warn' : 'text-mute')}>{t(it.reason)}</div>}
                    {d?.error && <div className="text-[12px] text-bad">{d.error}</div>}
                  </div>
                  {d && <span className="flex items-center gap-1 text-[12px]"><Dot state={d.status === 'done' ? 'ok' : d.status === 'running' || d.status === 'queued' ? 'busy' : d.status === 'pending_approval' ? 'warn' : 'bad'} />{t('status_change.' + d.status)}</span>}
                </label>
              )
            })}
          </div>
          <p className="text-[12px] text-mute">{t('import.after')}</p>
        </>}
      </div>
    </Dialog>
  )
}
