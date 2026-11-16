import { CheckCircle2, Upload, X } from 'lucide-react'
import { useRef, useState } from 'react'
import { del, errText, get, post, put } from '@/lib/api'
import { bytes } from '@/lib/format'
import { useT } from '@/lib/i18n'
import { Button } from './Button'

export interface Uploaded { id: string; name: string; size: number; received: number; done: boolean; path: string }

const CHUNK = 16 << 20

// upload sends a file in 16 MB pieces; a piece that fails is retried from where the server stands.
export async function uploadFile(file: File, onProgress: (n: number) => void, signal: { stop: boolean }): Promise<Uploaded> {
  let u = await post<Uploaded>('/api/v1/uploads', { name: file.name, size: file.size })
  let off = 0
  let tries = 0
  while (off < file.size) {
    if (signal.stop) throw new Error('stopped')
    try {
      u = await put<Uploaded>(`/api/v1/uploads/${u.id}?offset=${off}`, file.slice(off, off + CHUNK))
      off = u.received
      tries = 0
      onProgress(off)
    } catch (e) {
      if (++tries > 5) throw e
      await new Promise((r) => setTimeout(r, 1000 * tries))
      off = (await get<Uploaded>(`/api/v1/uploads/${u.id}`)).received
    }
  }
  return u
}

// Uploader: pick a file, see it go up, get its id back.
export function Uploader({ accept, onDone, label }: { accept: string; onDone: (u: Uploaded | null) => void; label: string }) {
  const t = useT()
  const input = useRef<HTMLInputElement>(null)
  const signal = useRef({ stop: false })
  const [file, setFile] = useState<File | null>(null)
  const [sent, setSent] = useState(0)
  const [done, setDone] = useState<Uploaded | null>(null)
  const [err, setErr] = useState('')
  const start = async (f: File) => {
    setFile(f); setSent(0); setDone(null); setErr(''); onDone(null)
    signal.current = { stop: false }
    try {
      const u = await uploadFile(f, setSent, signal.current)
      setDone(u); onDone(u)
    } catch (e) { if (!signal.current.stop) setErr(errText(e)) }
  }
  const cancel = () => {
    signal.current.stop = true
    if (done) void del(`/api/v1/uploads/${done.id}`).catch(() => {})
    setFile(null); setDone(null); setSent(0); onDone(null)
  }
  const pct = file ? Math.floor((sent / Math.max(1, file.size)) * 100) : 0
  return (
    <div className="space-y-2">
      <input ref={input} type="file" accept={accept} className="hidden" aria-label={label} onChange={(e) => { const f = e.target.files?.[0]; if (f) void start(f); e.target.value = '' }} />
      {!file ? (
        <Button onClick={() => input.current?.click()}><Upload className="size-3.5" />{label}</Button>
      ) : (
        <div className="rounded-xl border border-line px-3 py-2">
          <div className="flex items-center gap-2 text-[13px]">
            {done ? <CheckCircle2 className="size-4 text-ok" /> : <Upload className="size-4 text-mute" />}
            <span className="num min-w-0 flex-1 truncate">{file.name}</span>
            <span className="num text-[12px] text-mute">{done ? bytes(file.size) : `${bytes(sent)} / ${bytes(file.size)} · ${pct}%`}</span>
            <button type="button" aria-label={t('common.cancel')} className="text-mute hover:text-fg" onClick={cancel}><X className="size-4" /></button>
          </div>
          {!done && <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-soft"><div className="h-full bg-acc transition-all" style={{ width: pct + '%' }} /></div>}
          {err && <p className="mt-1 text-[12px] text-bad">{err} <button className="text-acc hover:underline" onClick={() => void start(file)}>{t('upload.retry')}</button></p>}
        </div>
      )}
    </div>
  )
}
