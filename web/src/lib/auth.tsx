import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { get, post, setCsrf, setUnauthorizedHandler } from './api'
import type { User } from './types'

type Me = User & { csrf: string }
const Ctx = createContext<{ user: User | null; loading: boolean; refresh: () => Promise<void>; signedIn: (u: User, csrf: string) => void; logout: () => Promise<void> }>({
  user: null, loading: true, refresh: async () => {}, signedIn: () => {}, logout: async () => {},
})

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [loading, setLoading] = useState(true)
  const refresh = useCallback(async () => {
    try {
      const me = await get<Me>('/api/v1/me')
      setCsrf(me.csrf)
      setUser(me)
    } catch {
      setUser(null)
    } finally {
      setLoading(false)
    }
  }, [])
  useEffect(() => {
    setUnauthorizedHandler(() => setUser(null))
    void refresh()
  }, [refresh])
  const signedIn = useCallback((u: User, csrf: string) => { setCsrf(csrf); setUser(u) }, [])
  const logout = useCallback(async () => {
    try { await post('/api/v1/auth/logout') } finally { setUser(null) }
  }, [])
  return <Ctx.Provider value={{ user, loading, refresh, signedIn, logout }}>{children}</Ctx.Provider>
}

export const useAuth = () => useContext(Ctx)

// can mirrors the server's User.Can (the server is the authority; this only hides UI).
const superOnly = new Set(['users.manage', 'tokens.manage', 'audit.view', 'extensions.manage', 'settings.manage'])
const danger = new Set(['terminal.use', 'remote.use', 'disk.manage'])
export function can(u: User | null, perm: string) {
  if (!u || u.disabled) return false
  if (u.role === 'super') return true
  if (superOnly.has(perm)) return false
  if (u.role === 'viewer') return perm.endsWith('.view') && !danger.has(perm)
  return u.perms.includes(perm)
}
