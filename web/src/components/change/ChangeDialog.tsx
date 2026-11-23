import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import { ApiError, get, post } from '@/lib/api'
import { usePick, useT } from '@/lib/i18n'
import type { Change } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { AskAIButton } from '@/components/ai/AiDrawer'
import { Button } from '../ui/Button'
import { Dialog } from '../ui/Dialog'
import { Input } from '../ui/Field'
import { Notice } from '../ui/Box'
import { DiffView } from './DiffView'
import { EquivCommand } from './EquivCommand'
import { RiskBadge } from './RiskBadge'

// url: a special endpoint that turns form input into the change (e.g. hashing a password
// before it becomes a user.create change); it must accept ?plan=1 like the generic one.
// typeToConfirm: make the person type this word even when the change is not destructive (root plugins, restoring the panel)
type RunOpts = { host?: string; onDone?: (c: Change) => void; onStart?: (c: Change) => void /* accepted: running or done (the form that asked can close) */; confirmName?: string; typeToConfirm?: string; url?: string }
type State = { kind: string; params: unknown; opts: RunOpts; plan?: Change; error?: string; busy: boolean; result?: Change; seq: number; show: boolean }
type Toast = { key: number; text: string; change?: Change; undone?: boolean }

// quick mode: low-risk changes that can be undone run without the preview dialog; a toast
// says it is done and offers to undo. Turned off in the account page (berth.quick=0).
export const quickMode = () => localStorage.getItem('berth.quick') !== '0'
const quickOK = (c: Change, o: RunOpts) => quickMode() && c.plan.risk === 'low' && c.plan.undoable && !o.confirmName && !o.typeToConfirm && !(c.plan.files?.length)

const Ctx = createContext<{ run: (kind: string, params: unknown, opts?: RunOpts) => void }>({ run: () => {} })
export const useChange = () => useContext(Ctx).run

// openTasks is set by the shell so queued changes can show the task drawer.
let openTasks: (changeId: string) => void = () => {}
export const setOpenTasks = (fn: (id: string) => void) => { openTasks = fn }

export function ChangeProvider({ host: defaultHost = 'local', children }: { host?: string; children: ReactNode }) {
  const t = useT()
  const pick = usePick()
  const [st, setSt] = useState<State | null>(null)
  const [typed, setTyped] = useState('')
  const waiting = useRef<Map<string, (c: Change) => void>>(new Map())

  useEvents(['change.done'], (_t, c: Change) => {
    const cb = waiting.current.get(c.id)
    if (cb) { waiting.current.delete(c.id); cb(c) }
  })

  const url = (s: State, plan: boolean) => (s.opts.url ?? `/api/v1/hosts/${encodeURIComponent(s.opts.host ?? defaultHost)}/changes/${s.kind}`) + (plan ? (s.opts.url?.includes('?') ? '&plan=1' : '?plan=1') : '')

  // each dialog gets a number, so a request that finishes late only closes its own dialog
  const seq = useRef(0)
  const [toasts, setToasts] = useState<Toast[]>([])
  const toast = (x: Omit<Toast, 'key'>) => {
    const key = Date.now() + Math.random()
    setToasts((l) => [...l.slice(-3), { ...x, key }])
    setTimeout(() => setToasts((l) => l.filter((y) => y.key !== key)), 7000)
  }

  const apply = async (s: State, quiet: boolean) => {
    setSt({ ...s, busy: true, error: undefined })
    try {
      const c = await post<Change>(url(s, false), s.params)
      const mine = (cur: State | null) => cur?.seq === s.seq
      const label = s.plan ? actionLabel(pick(s.plan.action), s.plan) : s.kind
      if (c.status === 'running' || c.status === 'done') s.opts.onStart?.(c)
      const peerHost = (s.opts.host ?? defaultHost).includes('@') ? (s.opts.host ?? defaultHost) : '' // a linked panel's server: no live events from there
      if (c.status === 'running' && peerHost) {
        setSt((cur) => (mine(cur) ? null : cur))
        toast({ text: t('change.on_peer', { what: label }) })
        void (async () => {
          for (let i = 0; i < 900; i++) {
            await new Promise((r) => setTimeout(r, 2000))
            const cur = await get<Change>(`/api/v1/hosts/${encodeURIComponent(peerHost)}/changes/${c.id}`).catch(() => null)
            if (cur && cur.status !== 'running') {
              s.opts.onDone?.({ ...cur, host: peerHost })
              toast({ text: t(cur.status === 'done' ? 'change.done_toast' : 'change.failed_toast', { what: label }), change: cur.status === 'done' ? { ...cur, host: peerHost } : undefined })
              return
            }
          }
        })()
      } else if (c.status === 'running') {
        waiting.current.set(c.id, (done) => {
          s.opts.onDone?.(done)
          if (quiet && done.status === 'done') toast({ text: t('change.done_toast', { what: label }), change: done })
        })
        openTasks(c.id)
        setSt((cur) => (mine(cur) ? null : cur))
      } else if (c.status === 'pending_approval') {
        setSt((cur) => (mine(cur) ? { ...s, busy: false, result: c, show: true } : cur))
      } else {
        s.opts.onDone?.(c)
        setSt((cur) => (mine(cur) ? null : cur))
        if (quiet) toast({ text: t('change.done_toast', { what: label }), change: c })
      }
    } catch (e) {
      setSt((cur) => (cur?.seq === s.seq ? { ...s, busy: false, error: (e as Error).message, show: true } : cur))
    }
  }

  const run = useCallback((kind: string, params: unknown, opts: RunOpts = {}) => {
    const s: State = { kind, params, opts, busy: true, seq: ++seq.current, show: !quickMode() }
    setTyped('')
    setSt(s)
    // the dialog shows only if the change needs a look (in quick mode)
    const late = setTimeout(() => setSt((cur) => (cur && cur.seq === s.seq ? { ...cur, show: true } : cur)), 600)
    post<Change>(url(s, true), params)
      .then((plan) => {
        clearTimeout(late)
        if (quickOK(plan, opts)) void apply({ ...s, plan }, true)
        else setSt((cur) => (cur && cur.seq === s.seq ? { ...cur, plan, busy: false, show: true } : cur))
      })
      .catch((e: ApiError) => { clearTimeout(late); setSt((cur) => (cur && cur.seq === s.seq ? { ...cur, error: e.message, busy: false, show: true } : cur)) })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [defaultHost])

  const confirm = async () => {
    if (st) await apply(st, false)
  }

  const undo = async (x: Toast) => {
    if (!x.change) return
    try {
      const h = x.change.host ?? ''
      await post(h.includes('@') ? `/api/v1/hosts/${encodeURIComponent(h)}/changes/${x.change.id}/revert` : `/api/v1/changes/${x.change.id}/revert`)
      setToasts((l) => l.map((y) => (y.key === x.key ? { ...y, undone: true } : y)))
    } catch (e) {
      toast({ text: (e as Error).message })
    }
  }

  const plan = st?.plan
  const destructive = plan?.plan.risk === 'destructive' || !!st?.opts.typeToConfirm
  const confirmName = st?.opts.typeToConfirm ?? st?.opts.confirmName ?? plan?.plan.affected?.[0]?.name ?? plan?.plan.affected?.[0]?.id ?? ''
  const blocked = destructive && typed !== confirmName

  return (
    <Ctx.Provider value={{ run }}>
      {children}
      <Dialog open={!!st && st.show} onOpenChange={(o) => !o && !st?.busy && setSt(null)} wide={!!plan?.plan.files?.length}
        title={plan ? pick(plan.title) : t('change.preparing')}
        description={plan ? <span className="flex items-center gap-2"><RiskBadge risk={plan.plan.risk} />{plan.plan.undoable ? t('change.undoable') : t('change.not_undoable')}</span> : undefined}
        footer={st?.result ? (
          <Button className="ml-auto" onClick={() => setSt(null)}>{t('common.close')}</Button>
        ) : (
          <>
            <span className="text-[12.5px] text-mute">{plan?.plan.risk === 'high' || destructive ? t('change.recorded_high') : t('change.recorded')}</span>
            <Button className="ml-auto" onClick={() => setSt(null)} disabled={st?.busy}>{t('common.cancel')}</Button>
            <Button variant={destructive || plan?.plan.risk === 'high' ? 'danger' : 'primary'} onClick={confirm} disabled={!plan || st?.busy || blocked} data-testid="confirm">
              {plan ? actionLabel(pick(plan.action), plan) : t('common.loading')}
            </Button>
          </>
        )}>
        <div className="space-y-3 px-4 py-3 text-[13.5px]">
          {st?.error && <Notice tone="bad"><div className="flex items-start gap-2"><span className="flex-1">{st.error}</span>
            <AskAIButton question={t('ai.q_change')} context={`change ${st.kind} with ${JSON.stringify(st.params).slice(0, 600)} failed: ${st.error}`} /></div></Notice>}
          {st?.result && <Notice>{t('change.pending_approval', { id: st.result.id })}</Notice>}
          {plan && (
            <>
              <p className="leading-relaxed">{pick(plan.summary)}</p>
              {plan.plan.affected?.length > 0 && (
                <div>
                  <h3 className="mb-1 text-[12.5px] font-semibold">{t('change.affected')}</h3>
                  <ul className="flex flex-wrap gap-1.5">
                    {plan.plan.affected.map((o) => (
                      <li key={o.type + o.id} className="num rounded border border-line px-1.5 text-[12px]">
                        <span className="text-mute">{t('obj.' + o.type)}</span> {o.name || o.id}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {plan.plan.commands?.length > 0 && (
                <div>
                  <h3 className="mb-1 text-[12.5px] font-semibold">{t('change.commands')}</h3>
                  <pre className="num overflow-x-auto rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12px]">{plan.plan.commands.join('\n')}</pre>
                </div>
              )}
              {plan.plan.files?.filter((f) => f.unified).map((f) => <DiffView key={f.path} diff={f.unified} path={f.path} />)}
              {destructive && (
                <div>
                  <label className="mb-1 block text-[12.5px]">{t('change.type_to_confirm', { name: confirmName })}</label>
                  <Input value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus aria-label={t('change.type_to_confirm', { name: confirmName })} />
                </div>
              )}
              <EquivCommand command={plan.equivalent} />
            </>
          )}
        </div>
      </Dialog>
      {toasts.length > 0 && <div className="fixed bottom-10 right-4 z-[60] flex w-[min(360px,calc(100vw-2rem))] flex-col gap-2" role="status">
        {toasts.map((x) => (
          <div key={x.key} className="flex items-center gap-3 rounded-xl border border-line bg-card px-3.5 py-2.5 text-[13px] shadow-lg">
            <span className="min-w-0 flex-1">{x.undone ? t('change.undone_toast') : x.text}</span>
            {!!x.change?.undo && !x.undone && <button className="shrink-0 font-semibold text-acc hover:underline" onClick={() => void undo(x)}>{t('timeline.undo')}</button>}
          </div>
        ))}
      </div>}
    </Ctx.Provider>
  )
}

// actionLabel: "停止容器 n8n" — the verb from the server plus the object name.
function actionLabel(verb: string, c: Change) {
  const p = c.params as Record<string, unknown>
  const name = (p?.name ?? p?.username ?? p?.ref ?? (typeof p?.path === 'string' ? (p.path as string).split('/').pop() : '') ?? '') as string
  return name ? `${verb} ${name}` : verb
}
