import { Download, Pause, Play } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Button } from '@/components/ui/Button'
import { Input, Segmented } from '@/components/ui/Field'
import { useT } from '@/lib/i18n'

type L = { t: string; s: 'out' | 'err'; l: string }

// LogsView follows a container's logs over SSE: search highlight, stdout/stderr filter,
// pause, and a download of the last lines.
export function LogsView({ host, name }: { host: string; name: string }) {
  const t = useT()
  const [lines, setLines] = useState<L[]>([])
  const [paused, setPaused] = useState(false)
  const [q, setQ] = useState('')
  const [stream, setStream] = useState<'all' | 'out' | 'err'>('all')
  const box = useRef<HTMLDivElement>(null)
  const pausedRef = useRef(paused)
  pausedRef.current = paused
  const base = `/api/v1/hosts/${encodeURIComponent(host)}/containers/${encodeURIComponent(name)}/logs`
  useEffect(() => {
    setLines([])
    const es = new EventSource(`${base}?tail=500&follow=1`, { withCredentials: true })
    let buf: L[] = []
    const flush = setInterval(() => {
      if (!buf.length) return
      const b = buf
      buf = []
      setLines((l) => [...l, ...b].slice(-5000))
    }, 250)
    es.addEventListener('log', (e) => { try { buf.push(JSON.parse((e as MessageEvent).data)) } catch { /* ignore */ } })
    es.addEventListener('end', () => es.close())
    return () => { es.close(); clearInterval(flush) }
  }, [base])
  useEffect(() => { if (!pausedRef.current && box.current) box.current.scrollTop = box.current.scrollHeight }, [lines])
  const shown = useMemo(() => lines.filter((l) => (stream === 'all' || l.s === stream) && (!q || l.l.toLowerCase().includes(q.toLowerCase()))), [lines, stream, q])
  const mark = (text: string) => {
    if (!q) return text
    const i = text.toLowerCase().indexOf(q.toLowerCase())
    return i < 0 ? text : <>{text.slice(0, i)}<mark className="bg-[#f2c94c] text-black">{text.slice(i, i + q.length)}</mark>{text.slice(i + q.length)}</>
  }
  return (
    <div className="flex h-full min-h-[360px] flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2">
        <Input className="h-7 w-44 text-[13px]" placeholder={t('logs.search')} value={q} onChange={(e) => setQ(e.target.value)} />
        <Segmented value={stream} onChange={setStream} options={[{ value: 'all', label: t('logs.all') }, { value: 'out', label: 'stdout' }, { value: 'err', label: 'stderr' }]} />
        <Button size="sm" onClick={() => setPaused(!paused)}>{paused ? <Play className="size-3.5" /> : <Pause className="size-3.5" />}{paused ? t('logs.resume') : t('logs.pause')}</Button>
        <a className="ml-auto" href={`${base}?tail=5000&format=text`} download><Button size="sm"><Download className="size-3.5" />{t('logs.download')}</Button></a>
      </div>
      <div ref={box} className="num min-h-0 flex-1 overflow-auto bg-term px-3 py-2 text-[12px] leading-[18px] text-term-fg">
        {shown.length === 0 && <span className="text-[#8c959f]">{t('logs.empty')}</span>}
        {shown.map((l, i) => (
          <div key={i} className={l.s === 'err' ? 'text-[#ffa198]' : ''}>
            <span className="select-none text-[#6e7781]">{l.t.slice(11, 19)} </span>{mark(l.l)}
          </div>
        ))}
      </div>
    </div>
  )
}
