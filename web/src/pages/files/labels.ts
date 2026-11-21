// Plain words for the folders a newcomer meets on a Linux server, so a list of /etc, /var
// and /srv is not a wall of riddles. danger: system folders to leave alone.

export interface FolderLabel { key: string; danger?: boolean; arg?: string }

const EXACT: Record<string, FolderLabel> = {
  '/bin': { key: 'sys_programs', danger: true }, '/sbin': { key: 'sys_programs', danger: true },
  '/lib': { key: 'sys_libs', danger: true }, '/lib64': { key: 'sys_libs', danger: true },
  '/boot': { key: 'boot', danger: true }, '/dev': { key: 'dev', danger: true }, '/proc': { key: 'virtual', danger: true },
  '/sys': { key: 'virtual', danger: true }, '/run': { key: 'run', danger: true }, '/usr': { key: 'usr', danger: true },
  '/etc': { key: 'etc', danger: true }, '/var': { key: 'var' }, '/home': { key: 'home' }, '/root': { key: 'root' },
  '/tmp': { key: 'tmp' }, '/opt': { key: 'opt' }, '/srv': { key: 'srv' }, '/mnt': { key: 'mnt' }, '/media': { key: 'mnt' },
  '/snap': { key: 'snap' }, '/afs': { key: 'unused' }, '/lost+found': { key: 'lost_found', danger: true },
  '/usr/bin': { key: 'sys_programs', danger: true }, '/usr/sbin': { key: 'sys_programs', danger: true },
  '/usr/lib': { key: 'sys_libs', danger: true }, '/usr/lib64': { key: 'sys_libs', danger: true }, '/usr/share': { key: 'share', danger: true },
  '/usr/local': { key: 'local' }, '/usr/local/bin': { key: 'local_bin' },
  '/etc/ssh': { key: 'etc_ssh', danger: true }, '/etc/systemd': { key: 'etc_systemd', danger: true }, '/etc/nginx': { key: 'etc_nginx' },
  '/etc/caddy': { key: 'etc_caddy' }, '/etc/docker': { key: 'etc_docker' }, '/etc/berth': { key: 'etc_berth', danger: true },
  '/etc/cron.d': { key: 'etc_cron' }, '/etc/yum.repos.d': { key: 'etc_repos' }, '/etc/apt': { key: 'etc_repos' },
  '/etc/firewalld': { key: 'etc_firewall', danger: true }, '/etc/fstab': { key: 'etc_fstab', danger: true },
  '/var/log': { key: 'var_log' }, '/var/lib': { key: 'var_lib' }, '/var/cache': { key: 'var_cache' }, '/var/tmp': { key: 'tmp' },
  '/var/www': { key: 'var_www' }, '/var/spool': { key: 'var_spool' }, '/var/swap': { key: 'swap', danger: true },
  '/var/lib/docker': { key: 'docker', danger: true }, '/var/lib/containers': { key: 'docker', danger: true }, '/var/lib/incus': { key: 'incus', danger: true },
  '/var/lib/berth': { key: 'berth_data', danger: true }, '/var/lib/mysql': { key: 'mysql', danger: true }, '/var/lib/pgsql': { key: 'pgsql', danger: true },
  '/var/lib/postgresql': { key: 'pgsql', danger: true }, '/var/lib/valkey': { key: 'redis', danger: true }, '/var/lib/redis': { key: 'redis', danger: true },
  '/srv/berth': { key: 'berth' }, '/srv/berth/caddy': { key: 'caddy' }, '/srv/berth/caddy/sites': { key: 'sites' },
  '/srv/berth/caddy/logs': { key: 'site_logs' }, '/srv/berth/caddy/data': { key: 'caddy_data', danger: true }, '/srv/berth/caddy/config': { key: 'caddy_config', danger: true },
  '/srv/berth/stacks': { key: 'stacks' }, '/srv/berth/apps': { key: 'apps' }, '/srv/berth/backups': { key: 'backups' },
  '/srv/berth/workspaces': { key: 'workspaces' }, '/srv/berth/projects': { key: 'projects' },
}

// folderLabel: what a folder is for, by its full path.
export function folderLabel(path: string): FolderLabel | null {
  const p = path.replace(/\/+$/, '') || '/'
  if (EXACT[p]) return EXACT[p]
  const m = (re: RegExp) => p.match(re)?.[1]
  let x: string | undefined
  if ((x = m(/^\/srv\/berth\/caddy\/sites\/([^/]+)$/))) return { key: 'site_dir', arg: x }
  if ((x = m(/^\/srv\/berth\/stacks\/([^/]+)$/))) return { key: 'stack_dir', arg: x }
  if ((x = m(/^\/srv\/berth\/apps\/([^/]+)$/))) return { key: 'app_dir', arg: x }
  if ((x = m(/^\/home\/([^/]+)$/))) return { key: 'home_dir', arg: x }
  if ((x = m(/^\/var\/log\/([^/]+)$/))) return { key: 'log_of', arg: x }
  return null
}
