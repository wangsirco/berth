// Site shapes as the panel API returns them (internal/panel/caddycfg).
export type TargetType = 'container' | 'address' | 'static' | 'php' | 'redirect' | 'project'
export type TLSMode = 'auto' | 'internal' | 'dns' | 'custom' | 'off'

export interface HeaderOp { op: 'set' | 'add' | 'del'; name: string; value?: string }
export interface PathRule { path: string; action: 'proxy' | 'rewrite' | 'redirect' | 'respond'; upstream?: string; strip?: boolean; to?: string; code?: number; body?: string }

export interface Site {
  id: number
  domains: string[]
  enabled: boolean
  target: {
    type: TargetType
    container?: string
    port?: number
    upstreams?: string[]
    scheme?: '' | 'http' | 'https' | 'h2c'
    insecure_tls?: boolean
    lb?: string
    health_path?: string
    spa?: boolean
    php?: string
    to?: string
    code?: number
    project?: string
    native?: boolean
    root_sub?: string
    php_ini?: Record<string, string>
    php_mem_mb?: number
    php_workers?: { name: string; command: string }[]
    schedule?: string
  }
  tls: { mode: TLSMode; dns?: string; cert_id?: number }
  options: {
    note?: string
    hotlink?: { exts: string[]; allow: string[]; allow_empty: boolean }
    no_https_redirect?: boolean
    www?: '' | 'to_www' | 'to_apex'
    redirect?: { hosts: string[]; code: number }
    group?: string
    hsts?: boolean
    compress?: boolean
    cache_static?: boolean
    timeouts?: { dial?: number; read?: number }
    req_headers?: HeaderOp[]
    resp_headers?: HeaderOp[]
    cors?: { origins: string[]; credentials?: boolean } | null
    paths?: PathRule[]
    basic_auth?: { user: string; hash: string }[]
    allow_ips?: string[]
    deny_ips?: string[]
    real_ip_cloudflare?: boolean
    log_days?: number
    rate_limit?: { requests: number; window: number } | null
    waf?: { mode: 'detect' | 'block'; paranoia?: number; exclude_rules?: number[]; exclude_paths?: string[] } | null
  }
}

export interface Candidate { container: string; project?: string; image: string; ports: number[]; loopback?: number[]; on_edge: boolean; used_by?: string[] }

export interface DiagFix { kind: string; params: unknown; label: string }
export interface DiagLayer { layer: string; status: 'ok' | 'warn' | 'fail' | 'skip'; code: string; message: string; detail?: string[]; fix?: DiagFix }

export interface Integration { id: string; kind: string; name: string; hint?: string; check?: { zones_read: boolean; dns_read: boolean; zone_count: number }; used_by?: string[] }
export interface CertRow { id: number; name: string; source: string; info: { domains: string[]; not_after: string; issuer: string; key_type: string; fingerprint: string; chain_len: number; self_signed: boolean }; auto_renew: boolean; used_by?: string[]; history: { fingerprint: string; not_after: string; replaced_at: number }[] }

export const targetText = (s: Site) => {
  const t = s.target
  switch (t.type) {
    case 'container': return `${t.container}:${t.port}`
    case 'project': return `project ${t.project}`
    case 'php': return `PHP ${t.php?.length === 2 ? t.php[0] + '.' + t.php[1] : t.php}${t.native ? ' · native' : ''}${t.root_sub ? ' · /' + t.root_sub : ''}`
    case 'address': return (t.upstreams ?? []).join(', ')
    case 'static': return 'static'
    case 'redirect': return t.to ?? ''
  }
}

export const daysLeft = (iso: string) => Math.floor((Date.parse(iso) - Date.now()) / 86400000)

// siteName: what a list shows for a site: its note, else its primary domain.
// domainsShort: the primary domain, and how many more there are.
export const domainsShort = (s: Site) => (s.domains[0] ?? '') + (s.domains.length > 1 ? ` +${s.domains.length - 1}` : '')

export const siteName = (s: Site) => s.options.note?.trim() || s.domains[0] || ''
