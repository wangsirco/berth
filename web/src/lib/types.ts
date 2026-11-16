export type Risk = 'low' | 'medium' | 'high' | 'destructive'
export type L10n = Record<string, string>

export interface User {
  id: number
  username: string
  display_name: string
  role: 'super' | 'admin' | 'viewer'
  perms: string[]
  scope: { hosts?: string[]; paths?: string[]; sites?: number[] }
  login_mode: 'password' | 'password_2fa' | 'passkey_only'
  locale: string
  disabled: boolean
  created_at: number
  last_login_at: number
  simple?: boolean
}

export interface Actor { type: string; id: string; name: string; via: string; ip: string; ai_session?: string; intent?: string }
export interface Obj { type: string; id: string; name?: string; host?: string }

export interface Change {
  id: string
  host: string
  kind: string
  status: 'planned' | 'pending_approval' | 'running' | 'done' | 'failed' | 'reverted' | 'rejected'
  actor: Actor
  params: Record<string, unknown>
  plan: { summary: string; files: { path: string; unified: string }[]; commands: string[]; affected: Obj[]; risk: Risk; undoable: boolean }
  undo?: unknown
  result?: any
  log: string
  reverts?: string
  reverted_by?: string
  ts: number
  finished_at?: number
  equivalent: string
  title: L10n
  summary: L10n
  action: L10n
}

export interface TaskView {
  id: string
  change_id: string
  host: string
  kind: string
  title: string
  status: 'waiting' | 'running' | 'done' | 'failed' | 'canceled'
  wait_reason?: string
  wait_params?: Record<string, unknown>
  steps: string[]
  step: number
  created_by: string
  created_at: number
  started_at?: number
  finished_at?: number
}

export interface Port { private: number; public?: number; proto: string; ip?: string }
export interface Container {
  id: string
  name: string
  image: string
  image_id: string
  state: string
  status: string
  created: number
  ports: Port[]
  project?: string
  service?: string
  networks: string[]
  mounts: { type: string; name?: string; source?: string; destination: string; rw: boolean }[]
  labels: Record<string, string>
  restart_count: number
  adopted: boolean
  exposure?: 'public' | 'open' | 'local' | 'internal' | 'edge'
  sites?: { id: number; domain: string; enabled: boolean }[]
  wan?: string[]
}

export interface Stat { id: string; cpu: number; mem_bytes: number; mem_limit: number; net_rx: number; net_tx: number }

export interface Host {
  ts: number
  cpu: number
  per_cpu: number[]
  cores: number
  load: [number, number, number]
  mem_total: number
  mem_used: number
  mem_avail: number
  swap_total: number
  swap_used: number
  zram_total: number
  zram_used: number
  zram_orig: number
  disks: { mount: string; fs: string; total: number; used: number }[]
  disk_r_bps: number
  disk_w_bps: number
  net: { name: string; rx_bps: number; tx_bps: number }[]
  temps: { label: string; c: number }[]
  uptime: number
}

export interface Berth { type: 'container' | 'service' | 'user' | 'kernel' | 'free'; id: string; name: string; mem_bytes: number; cpu: number; project?: string }

export interface Caps { OS: string; OSVersion: string; Kernel: string; Arch: string; DockerVersion: string; PageSize: number; Docker: boolean }
export interface HostInfo { name: string; online: boolean; last_seen: number; caps?: Caps }

export interface Point { t: number; v: number }
