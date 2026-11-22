import { useEffect, useRef, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { DiffView } from '@/components/change/DiffView'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Box, Notice, Pill } from '@/components/ui/Box'
import { Input, Segmented, Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { errText, get, post } from '@/lib/api'
import { ago, duration } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { openTerminal } from '@/pages/terminal/store'

interface FileStat { path: string; add: number; del: number; binary?: boolean }
interface Result { text: string; is_error?: boolean; cost_usd: number; turns: number; duration_ms: number; files: FileStat[]; diff_cut?: boolean; error?: { code: string; params?: Record<string, unknown> } }
export interface Task {
  id: number; workspace: string; prompt: string; mode: string; parent?: number; status: 'queued' | 'running' | 'done' | 'asking' | 'failed' | 'canceled'
  session?: string; created_by: string; created_at: number; started_at?: number; finished_at?: number; log?: string; result?: Result; before?: string; after?: string; reverted?: boolean
}
interface ClaudeData { enabled: boolean; mode: 'auto' | 'edits'; auth: 'login' | 'api_key' | 'token'; state: { version?: string; logged_in?: boolean; in_container?: boolean }; tasks: Task[] }

const TONE: Record<Task['status'], 'ok' | 'warn' | 'bad' | 'acc' | undefined> = { queued: undefined, running: 'acc', done: 'ok', asking: 'warn', failed: 'bad', canceled: undefined }

// openClaude: the workspace's resident Claude conversation in the panel terminal
export const openClaude = (name: string) => openTerminal({ target: 'dev', container: name, program: 'claude' })

export function ClaudeBox({ name, up, focusTask }: { name: string; up: boolean; focusTask?: number }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const d = useApi<ClaudeData>(`/api/v1/hosts/${host}/dev/${name}/claude`, [host, name])
  useEvents(['dev.task'], (_, ev) => { if (ev?.task?.workspace === name) void d.reload() })
  const busy = d.data?.tasks.some((x) => x.status === 'running' || x.status === 'queued')
  useInterval(() => void d.reload(), busy ? 3000 : null)
  const [prompt, setPrompt] = useState('')
  const [mode, setMode] = useState<'auto' | 'edits' | ''>('')
  const [sending, setSending] = useState(false)
  const [err, setErr] = useState('')
  const [auth, setAuth] = useState(false)
  const [openId, setOpenId] = useState<number | null>(focusTask ?? null)
  useEffect(() => { if (focusTask) setOpenId(focusTask) }, [focusTask])

  if (!d.data) return <Box title="Claude Code"><div className="p-4 text-[13px] text-mute">{d.error ? d.error.message : t('common.loading')}</div></Box>
  const data = d.data
  if (!data.enabled) return (
    <Box title="Claude Code">
      <div className="space-y-2 px-4 py-3 text-[13px]">
        <p className="text-mute">{t('claude.off_hint')}</p>
        <Button size="sm" variant="primary" onClick={() => run('dev.set', { name, claude: true }, { host })}>{t('claude.turn_on')}</Button>
      </div>
    </Box>
  )
  const send = async (text: string, replyTo?: number) => {
    setErr(''); setSending(true)
    try {
      await post(`/api/v1/hosts/${host}/changes/dev.task`, { name, prompt: text, mode: mode || undefined, reply_to: replyTo })
      if (!replyTo) setPrompt('')
      await d.reload()
    } catch (e) { setErr(errText(e)) } finally { setSending(false) }
  }
  const signedIn = data.auth !== 'login' || data.state.logged_in
  return (
    <Box title="Claude Code" right={<span className="text-[12px] text-mute num">{data.state.version ?? ''}</span>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {/* how it signs in */}
        <div className="flex flex-wrap items-center gap-2">
          {signedIn
            ? <Pill tone="ok">{t('claude.auth_' + data.auth)}</Pill>
            : <Pill tone="warn">{t('claude.not_signed_in')}</Pill>}
          <Button size="sm" disabled={!up} onClick={() => openClaude(name)}>{t('claude.chat')}</Button>
          <Button size="sm" variant="ghost" onClick={() => setAuth(true)}>{t('claude.auth_change')}</Button>
        </div>
        {!signedIn && <Notice tone="warn">{t('claude.sign_in_hint')}</Notice>}

        {/* a new task */}
        <div className="space-y-1.5">
          <Textarea rows={3} value={prompt} onChange={(e) => setPrompt(e.target.value)} aria-label={t('claude.task')} placeholder={t('claude.task_placeholder')}
            onKeyDown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && prompt.trim()) void send(prompt.trim()) }} />
          <div className="flex flex-wrap items-center gap-2">
            <Segmented value={mode || data.mode} onChange={(v) => setMode(v)} options={[{ value: 'auto', label: t('claude.mode_auto') }, { value: 'edits', label: t('claude.mode_edits') }]} />
            <span className="flex-1 text-[12px] text-mute">{t('claude.mode_' + (mode || data.mode) + '_hint')}</span>
            <Button size="sm" variant="primary" disabled={!prompt.trim() || sending || !up} onClick={() => void send(prompt.trim())}>{t('claude.send')}</Button>
          </div>
          {!up && <p className="text-[12px] text-warn">{t('claude.needs_running')}</p>}
          {err && <p className="text-[12px] text-bad">{err}</p>}
        </div>

        {/* the queue and history */}
        {data.tasks.length > 0 && <div className="divide-y divide-line rounded-[var(--radius-box)] border border-line">
          {data.tasks.map((x) => <TaskRow key={x.id} name={name} task={x} open={openId === x.id} onToggle={() => setOpenId(openId === x.id ? null : x.id)} onReply={(text) => send(text, x.id)} />)}
        </div>}
      </div>
      {auth && <AuthDialog name={name} current={data.auth} onClose={() => { setAuth(false); void d.reload() }} />}
    </Box>
  )
}

function TaskRow({ name, task: x, open, onToggle, onReply }: { name: string; task: Task; open: boolean; onToggle: () => void; onReply: (text: string) => Promise<void> }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const live = x.status === 'running' || x.status === 'queued'
  const full = useApi<Task>(open ? `/api/v1/hosts/${host}/dev/${name}/claude/tasks/${x.id}` : null, [host, name, x.id, open, x.status, x.reverted])
  useInterval(() => void full.reload(), open && live ? 2000 : null)
  const [diff, setDiff] = useState<{ diff: string; diff_cut: boolean } | null>(null)
  const [diffErr, setDiffErr] = useState('')
  const [reply, setReply] = useState('')
  const logRef = useRef<HTMLPreElement>(null)
  useEffect(() => { if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight }, [full.data?.log])
  const r = x.result
  const files = r?.files ?? []
  const adds = files.reduce((s, f) => s + f.add, 0), dels = files.reduce((s, f) => s + f.del, 0)
  const took = x.finished_at && x.started_at ? duration(x.finished_at - x.started_at, t) : ''
  const showDiff = async () => {
    setDiffErr('')
    try { setDiff(await get(`/api/v1/hosts/${host}/dev/${name}/claude/tasks/${x.id}/diff`)) } catch (e) { setDiffErr(errText(e)) }
  }
  return (
    <div data-task={x.id}>
      <button className="flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-soft" onClick={onToggle}>
        <Pill tone={TONE[x.status]} className="mt-0.5 shrink-0">{t('claude.status_' + x.status)}</Pill>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px]">{x.parent ? <span className="text-mute">↳ </span> : null}{x.prompt}</div>
          <div className="text-[11.5px] text-mute">
            #{x.id} · {x.created_by} · {ago(x.created_at, t)}{took && ' · ' + took}
            {r && r.cost_usd > 0 && <span className="num"> · ${r.cost_usd.toFixed(2)}</span>}
            {files.length > 0 && <span className="num"> · {t('claude.files_n', { n: files.length })} <span className="text-ok">+{adds}</span> <span className="text-bad">−{dels}</span></span>}
            {x.reverted && <span> · {t('claude.reverted')}</span>}
          </div>
        </div>
      </button>
      {open && <div className="space-y-2 border-t border-line bg-canvas px-3 py-2.5 text-[12.5px]">
        <div className="whitespace-pre-wrap rounded bg-soft px-2 py-1.5">{x.prompt}</div>
        {(live || (full.data?.log && !r?.text)) && <pre ref={logRef} className="num max-h-56 overflow-auto rounded bg-soft p-2 text-[11.5px] whitespace-pre-wrap">{full.data?.log || t('claude.waiting')}</pre>}
        {r?.error && <Notice tone="bad">{t(r.error.code, r.error.params as Record<string, string>)}</Notice>}
        {r?.text && <div className={cx('whitespace-pre-wrap rounded border px-2.5 py-2 leading-relaxed', x.status === 'asking' ? 'border-warn/60' : 'border-line')}>{r.text}</div>}
        {!live && full.data?.log && r?.text && <details><summary className="cursor-pointer text-mute">{t('claude.steps')}</summary>
          <pre className="num mt-1 max-h-56 overflow-auto rounded bg-soft p-2 text-[11.5px] whitespace-pre-wrap">{full.data.log}</pre></details>}
        {files.length > 0 && <div>
          <div className="mb-1 text-mute">{t('claude.changed_files')}</div>
          <div className="num space-y-0.5">{files.map((f) => <div key={f.path} className="flex gap-2"><span className="min-w-0 flex-1 truncate">{f.path}</span>
            {f.binary ? <span className="text-mute">bin</span> : <><span className="text-ok">+{f.add}</span><span className="text-bad">−{f.del}</span></>}</div>)}</div>
        </div>}
        <div className="flex flex-wrap gap-1.5">
          {live && <Button size="sm" onClick={() => run('dev.task_cancel', { name, task: x.id }, { host })}>{t('action.dev.task_cancel')}</Button>}
          {!live && files.length > 0 && !diff && <Button size="sm" onClick={() => void showDiff()}>{t('claude.show_diff')}</Button>}
          {!live && files.length > 0 && (x.reverted
            ? <Button size="sm" onClick={() => run('dev.task_revert', { name, task: x.id, reapply: true }, { host })}>{t('claude.reapply')}</Button>
            : <Button size="sm" onClick={() => run('dev.task_revert', { name, task: x.id }, { host })}>{t('action.dev.task_revert')}</Button>)}
        </div>
        {diffErr && <p className="text-bad">{diffErr}</p>}
        {diff && <div className="space-y-2">
          {diff.diff.split(/^(?=diff --git )/m).filter((s) => s.trim()).map((part, i) => {
            const path = part.match(/^diff --git a\/(.+?) b\//)?.[1] ?? ''
            return part.includes('@@') ? <DiffView key={i} diff={part} path={path} /> : <div key={i} className="num rounded border border-line px-3 py-1.5 text-mute">{path} · {t('claude.no_text_diff')}</div>
          })}
          {diff.diff_cut && <p className="text-mute">{t('claude.diff_cut')}</p>}
        </div>}
        {!live && x.session && <div className="flex gap-1.5">
          <Input className="flex-1" value={reply} onChange={(e) => setReply(e.target.value)} aria-label={t('claude.reply')}
            placeholder={x.status === 'asking' ? t('claude.answer_placeholder') : t('claude.reply_placeholder')}
            onKeyDown={(e) => { if (e.key === 'Enter' && reply.trim()) { void onReply(reply.trim()).then(() => setReply('')) } }} />
          <Button size="sm" variant={x.status === 'asking' ? 'primary' : undefined} disabled={!reply.trim()} onClick={() => void onReply(reply.trim()).then(() => setReply(''))}>{t('claude.reply')}</Button>
        </div>}
      </div>}
    </div>
  )
}

function AuthDialog({ name, current, onClose }: { name: string; current: ClaudeData['auth']; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [method, setMethod] = useState<ClaudeData['auth']>(current)
  const [secret, setSecret] = useState('')
  const ok = method === 'login' || secret.trim().length >= 8
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('claude.auth_title')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!ok} onClick={() => run('dev.claude_auth', { name, method, secret: secret.trim() }, { host, onDone: onClose })}>{t('action.dev.claude_auth')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <Segmented value={method} onChange={setMethod} options={[{ value: 'login', label: t('claude.auth_login') }, { value: 'token', label: t('claude.auth_token') }, { value: 'api_key', label: t('claude.auth_api_key') }]} />
        <p className="text-mute">{t('claude.auth_' + method + '_hint')}</p>
        {method !== 'login' && <Input type="password" autoComplete="off" className="num" value={secret} onChange={(e) => setSecret(e.target.value)} aria-label={t('claude.secret')}
          placeholder={method === 'api_key' ? 'sk-ant-api03-…' : 'sk-ant-oat01-…'} />}
        <p className="text-[12px] text-mute">{t('claude.auth_privacy')}</p>
      </div>
    </Dialog>
  )
}
