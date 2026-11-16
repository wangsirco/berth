import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Field'
import { Notice } from '@/components/ui/Box'
import { ApiError, post } from '@/lib/api'
import { useT } from '@/lib/i18n'

// withReauth runs fn; if the server asks to confirm identity, it prompts for the password
// once and retries.
const Ctx = createContext<<T>(fn: () => Promise<T>) => Promise<T>>((fn) => fn())
export const useReauth = () => useContext(Ctx)

export function ReauthProvider({ children }: { children: ReactNode }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [pw, setPw] = useState('')
  const [err, setErr] = useState('')
  const waiter = useRef<{ ok: () => void; no: (e: Error) => void } | null>(null)
  const ask = () => new Promise<void>((ok, no) => { waiter.current = { ok, no }; setPw(''); setErr(''); setOpen(true) })
  const run = useCallback(async <T,>(fn: () => Promise<T>): Promise<T> => {
    try { return await fn() } catch (e) {
      if (!(e instanceof ApiError) || e.code !== 'auth.reauth_required') throw e
      await ask()
      return fn()
    }
  }, [])
  const submit = async () => {
    try {
      await post('/api/v1/me/reauth', { password: pw })
      setOpen(false)
      waiter.current?.ok()
    } catch (e) { setErr((e as ApiError).message) }
  }
  return (
    <Ctx.Provider value={run}>
      {children}
      <Dialog open={open} onOpenChange={(o) => { if (!o) { setOpen(false); waiter.current?.no(new Error(t('common.cancel'))) } }}
        title={t('reauth.title')} description={t('reauth.sub')}
        footer={<><Button className="ml-auto" onClick={() => { setOpen(false); waiter.current?.no(new Error(t('common.cancel'))) }}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => void submit()} disabled={!pw}>{t('reauth.confirm')}</Button></>}>
        <form className="space-y-2 px-4 py-3" onSubmit={(e) => { e.preventDefault(); void submit() }}>
          {err && <Notice tone="bad">{err}</Notice>}
          <Input type="password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} aria-label={t('login.password')} autoComplete="current-password" />
        </form>
      </Dialog>
    </Ctx.Provider>
  )
}
