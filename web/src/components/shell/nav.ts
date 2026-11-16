// Navigation: the sidebar's groups (我的东西 / 网络 / 开发 / 保护 / 服务器), laid out like 1Panel and 宝塔. Routes are unchanged.
export interface NavItem { key: string; path: string; perm?: string; phase?: number }
export interface NavGroup { key: string; items: NavItem[] }

export const GROUPS: NavGroup[] = [
  // Wharf, the sister product for routers, sits right under the home page
  { key: 'home', items: [{ key: 'overview', path: 'overview', perm: 'overview.view' }, { key: 'wharf', path: 'wharf', perm: 'network.view' }] },
  { key: 'mine', items: [
    { key: 'sites', path: 'sites', perm: 'sites.view' },
    { key: 'projects', path: 'projects', perm: 'sites.view' },
    { key: 'store', path: 'store', perm: 'store.install' },
    { key: 'containers', path: 'containers', perm: 'containers.view' },
    { key: 'vms', path: 'vms', perm: 'vms.view' },
    { key: 'db', path: 'db', perm: 'db.view' },
    { key: 'files', path: 'files', perm: 'files.read' },
    { key: 'cron', path: 'cron', perm: 'system.view' },
  ] },
  { key: 'net', items: [
    { key: 'domains', path: 'domains', perm: 'sites.view' },
    { key: 'cloudflare', path: 'cloudflare', perm: 'network.view' },
    { key: 'network', path: 'network', perm: 'network.view' },
  ] },
  { key: 'build', items: [
    { key: 'pipeline', path: 'pipeline', perm: 'pipeline.run' },
    { key: 'dev', path: 'dev', perm: 'dev.use' },
    { key: 'remote', path: 'remote', perm: 'remote.use' },
  ] },
  { key: 'protect', items: [
    { key: 'backup', path: 'backup', perm: 'backup.view' },
    { key: 'monitor', path: 'monitor' },
    { key: 'audit', path: 'audit', perm: 'audit.view' },
  ] },
  { key: 'server', items: [
    { key: 'system', path: 'system', perm: 'system.view' },
    { key: 'hosts', path: 'hosts', perm: 'settings.manage' },
    { key: 'plugins', path: 'plugins', perm: 'extensions.use' },
    { key: 'users', path: 'users', perm: 'users.manage' },
    { key: 'tokens', path: 'tokens' },
    { key: 'settings', path: 'settings', perm: 'settings.manage' },
  ] },
]

// ALL is every page, flat (search, the "later" page).
export const ALL: NavItem[] = GROUPS.flatMap((g) => g.items)

// navGroups keeps what the user may see and drops groups left empty.
export function navGroups(visible: (perm?: string) => boolean): NavGroup[] {
  return GROUPS.map((g) => ({ ...g, items: g.items.filter((i) => visible(i.perm)) })).filter((g) => g.items.length > 0)
}
