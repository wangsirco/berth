import { useEffect, useRef, useState } from 'react'
import { Sparkles } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Drawer } from '@/components/ui/Dialog'
import { Textarea } from '@/components/ui/Field'
import { ApiError, post } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'
import type { Change } from '@/lib/types'

interface Msg { role: 'user' | 'assistant'; content: string; proposals?: { change: Change & { summary: Record<string, string> }; why?: string }[] }

// askAI opens the assistant from anywhere ("让 AI 看看"), with what the person was looking at.
export function askAI(question: string, context: string) {
  window.dispatchEvent(new CustomEvent('berth:ai', { detail: { question, context } }))
}

export function useAIReady() {
  const d = useApi<{ config: { has_key: boolean } }>('/api/v1/ai')
  return !!d.data?.config?.has_key
}

export function AskAIButton({ question, context }: { question: string; context: string }) {
  const t = useT()
  if (!useAIReady()) return null
  return <Button size="sm" onClick={() => askAI(question, context)}><Sparkles className="size-3.5" />{t('ai.look')}</Button>
}

// The assistant's side panel: a conversation, and the changes it proposes — each opens the
// usual preview dialog; nothing runs without the person confirming it there.
export function AiDrawer() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [locale] = useLocale()
  const [open, setOpen] = useState(false)
  const [msgs, setMsgs] = useState<Msg[]>([])
  const [ctx, setCtx] = useState('')
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [usage, setUsage] = useState<{ month: number; limit: number } | null>(null)
  const end = useRef<HTMLDivElement>(null)
  const send = async (text: string, context = ctx, history = msgs) => {
    if (!text.trim()) return
    const next: Msg[] = [...history, { role: 'user', content: text }]
    setMsgs(next); setInput(''); setBusy(true); setErr('')
    try {
      const r = await post<{ reply: string; proposals: Msg['proposals']; month: number; limit: number }>('/api/v1/ai/chat',
        { messages: next.map(({ role, content }) => ({ role, content })), host, context, locale })
      setMsgs([...next, { role: 'assistant', content: r.reply, proposals: r.proposals }])
      setUsage({ month: r.month, limit: r.limit })
    } catch (e) { setErr((e as ApiError).message) } finally { setBusy(false) }
  }
  useEffect(() => {
    const on = (e: Event) => {
      const d = (e as CustomEvent<{ question: string; context: string }>).detail
      setOpen(true)
      if (!d.question) return // just open it
      setCtx(d.context); setMsgs([])
      void send(d.question, d.context, [])
    }
    window.addEventListener('berth:ai', on)
    return () => window.removeEventListener('berth:ai', on)
  }) // re-bound each render so send sees the current host and locale
  useEffect(() => { end.current?.scrollIntoView({ block: 'end' }) }, [msgs, busy])
  return (
    <>
      <Drawer open={open} onOpenChange={setOpen} title={<span className="flex items-center gap-2"><Sparkles className="size-4" />{t('ai.title')}</span>}
        right={msgs.length > 0 && <Button size="sm" variant="ghost" onClick={() => { setMsgs([]); setCtx('') }}>{t('ai.new')}</Button>}
        footer={<form className="flex w-full items-end gap-2" onSubmit={(e) => { e.preventDefault(); void send(input) }}>
          <Textarea className="min-h-10 flex-1 text-[13px]" rows={2} value={input} placeholder={t('ai.placeholder')} onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void send(input) } }} />
          <Button type="submit" variant="primary" disabled={busy || !input.trim()}>{t('ai.send')}</Button>
        </form>}>
        <div className="space-y-3 p-4 text-[13.5px]">
          {ctx && <div className="rounded border border-line bg-soft px-3 py-2 text-[12px] text-mute">{t('ai.about')} {ctx.slice(0, 200)}</div>}
          {msgs.length === 0 && !busy && <p className="text-mute">{t('ai.hint')}</p>}
          {msgs.map((m, i) => (
            <div key={i} className={m.role === 'user' ? 'ml-8 rounded-lg bg-soft px-3 py-2' : ''}>
              <div className="whitespace-pre-wrap leading-relaxed">{m.content}</div>
              {m.proposals?.map((p) => (
                <div key={p.change.id} className="mt-2 rounded-[var(--radius-box)] border border-line p-3">
                  <div className="flex items-center gap-2"><Pill>{t('risk.' + p.change.plan.risk)}</Pill><span className="font-semibold">{p.change.summary?.[locale] ?? p.change.kind}</span></div>
                  {p.why && <div className="mt-1 text-[12.5px] text-mute">{p.why}</div>}
                  <Button size="sm" className="mt-2" onClick={() => run(p.change.kind, p.change.params, { host: p.change.host })}>{t('ai.review')}</Button>
                </div>
              ))}
            </div>
          ))}
          {busy && <div className="text-mute">{t('ai.thinking')}</div>}
          {err && <Notice tone="bad">{err}</Notice>}
          {usage && <div className="text-right text-[11.5px] text-mute">{t('ai.usage', { n: usage.month.toLocaleString() })}{usage.limit ? ' / ' + usage.limit.toLocaleString() : ''}</div>}
          <div ref={end} />
        </div>
      </Drawer>
    </>
  )
}
