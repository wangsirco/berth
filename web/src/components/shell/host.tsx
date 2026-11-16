import { createContext, useContext } from 'react'
import type { HostInfo } from '@/lib/types'

export const HostCtx = createContext<{ host: string; hosts: HostInfo[] }>({ host: 'local', hosts: [] })
export const useHost = () => useContext(HostCtx)

// hostPath builds a link inside the current host.
export const hostPath = (host: string, p: string) => `/${encodeURIComponent(host)}/${p.replace(/^\//, '')}`
