// Adapted from github.com/wangsirco/wharf (web/src/lib/api.ts), Copyright (c) Wharf authors, MIT License.
// Thin fetch wrapper: JSON in/out, CSRF header on writes, typed errors carrying the
// server's translated message.
let csrf = ''
export const setCsrf = (t: string) => { csrf = t }

export class ApiError extends Error {
  status: number
  code: string
  params: Record<string, unknown>
  constructor(status: number, code: string, message: string, params: Record<string, unknown> = {}) {
    super(message)
    this.status = status
    this.code = code
    this.params = params
  }
}

let onUnauthorized: () => void = () => {}
export const setUnauthorizedHandler = (fn: () => void) => { onUnauthorized = fn }
let locale = 'zh-CN'
export const setApiLocale = (l: string) => { locale = l }

export async function api<T = unknown>(method: string, path: string, body?: unknown, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = { 'Accept-Language': locale }
  const raw = body instanceof Blob || body instanceof ArrayBuffer
  if (body !== undefined && !raw) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && method !== 'HEAD') headers['X-CSRF-Token'] = csrf
  const res = await fetch(path, {
    method, headers, credentials: 'same-origin', ...init,
    body: body === undefined ? undefined : raw ? (body as BodyInit) : JSON.stringify(body),
  })
  const text = await res.text()
  let data: any = null
  try { data = text ? JSON.parse(text) : null } catch { data = { error: { code: 'http', message: text } } }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/api/v1/auth/')) onUnauthorized()
    const e = data?.error ?? {}
    throw new ApiError(res.status, e.code ?? 'http', e.message ?? res.statusText, e.params ?? {})
  }
  return data as T
}

export const get = <T,>(p: string) => api<T>('GET', p)
export const post = <T,>(p: string, b: unknown = {}) => api<T>('POST', p, b)
export const put = <T,>(p: string, b: unknown) => api<T>('PUT', p, b)
export const patch = <T,>(p: string, b: unknown) => api<T>('PATCH', p, b)
export const del = <T,>(p: string) => api<T>('DELETE', p)

export const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

// download POSTs and saves the response as a file (errors still come back as ApiError).
export async function download(path: string, body: unknown, fallbackName: string) {
  const res = await fetch(path, {
    method: 'POST', credentials: 'same-origin', body: JSON.stringify(body),
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf, 'Accept-Language': locale },
  })
  if (!res.ok) {
    let e: any = {}
    try { e = (await res.json())?.error ?? {} } catch { /* not json */ }
    if (res.status === 401) onUnauthorized()
    throw new ApiError(res.status, e.code ?? 'http', e.message ?? res.statusText, e.params ?? {})
  }
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? fallbackName
  const url = URL.createObjectURL(await res.blob())
  const a = Object.assign(document.createElement('a'), { href: url, download: name })
  document.body.appendChild(a); a.click(); a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}
