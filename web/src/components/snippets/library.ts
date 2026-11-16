// A starter set of commands most people retype on a Linux server, by topic. Saving one copies it
// into your own list, where it can be changed. {{name:default}} parts are asked for on insert.

type L = { 'zh-CN': string; 'zh-TW': string; en: string }
export interface LibItem { name: L; command: string; danger?: boolean }
export interface LibGroup { name: L; items: LibItem[] }

const l = (zh: string, tw: string, en: string): L => ({ 'zh-CN': zh, 'zh-TW': tw, en })

export const LIBRARY: LibGroup[] = [
  { name: l('看状态', '看狀態', 'Status'), items: [
    { name: l('资源一览（CPU、内存、进程）', '資源一覽（CPU、記憶體、程序）', 'Resources at a glance'), command: 'top -o %MEM' },
    { name: l('内存', '記憶體', 'Memory'), command: 'free -h' },
    { name: l('磁盘空间', '磁碟空間', 'Disk space'), command: 'df -h -x tmpfs -x devtmpfs' },
    { name: l('哪个目录最占空间', '哪個目錄最佔空間', 'What uses the space'), command: 'du -xh --max-depth=1 {{目录:/}} 2>/dev/null | sort -h | tail -15' },
    { name: l('最占内存的进程', '最佔記憶體的程序', 'Biggest processes'), command: 'ps aux --sort=-%mem | head -15' },
    { name: l('开机多久、负载', '開機多久、負載', 'Uptime and load'), command: 'uptime' },
  ] },
  { name: l('服务与日志', '服務與日誌', 'Services & logs'), items: [
    { name: l('服务状态', '服務狀態', 'Service status'), command: 'systemctl status {{服务:nginx}} --no-pager' },
    { name: l('重启服务', '重新啟動服務', 'Restart a service'), command: 'systemctl restart {{服务:nginx}}', danger: true },
    { name: l('实时看服务日志', '即時看服務日誌', 'Follow a service log'), command: 'journalctl -fu {{服务:nginx}}' },
    { name: l('最近的错误日志', '最近的錯誤日誌', 'Recent errors'), command: 'journalctl -p err -b --no-pager | tail -50' },
    { name: l('启动失败的服务', '啟動失敗的服務', 'Failed services'), command: 'systemctl --failed' },
  ] },
  { name: l('网络', '網路', 'Network'), items: [
    { name: l('谁在监听哪些端口', '誰在監聽哪些連接埠', 'Listening ports'), command: 'ss -tulpn' },
    { name: l('某个端口被谁占用', '某個連接埠被誰佔用', 'Who uses a port'), command: 'ss -tulpn | grep :{{端口:80}}' },
    { name: l('本机 IP 地址', '本機 IP 位址', 'IP addresses'), command: 'ip -br addr' },
    { name: l('公网 IP', '公網 IP', 'Public IP'), command: 'curl -s https://ifconfig.me; echo' },
    { name: l('测试能否连到某地址', '測試能否連到某位址', 'Can I reach it'), command: 'curl -sv -o /dev/null --max-time 10 {{地址:https://example.com}}' },
    { name: l('当前连接数最多的 IP', '目前連線數最多的 IP', 'Most connections by IP'), command: "ss -Hnt state established | awk '{print $4}' | sed 's/:[0-9]*$//' | sort | uniq -c | sort -rn | head" },
  ] },
  { name: l('Docker', 'Docker', 'Docker'), items: [
    { name: l('运行中的容器', '執行中的容器', 'Running containers'), command: 'docker ps --format "table {{.Names}}\\t{{.Status}}\\t{{.Ports}}"' },
    { name: l('容器占用的资源', '容器佔用的資源', 'Container resources'), command: 'docker stats --no-stream' },
    { name: l('看容器日志', '看容器日誌', 'Container logs'), command: 'docker logs -f --tail 200 {{容器}}' },
    { name: l('进入容器', '進入容器', 'Shell into a container'), command: 'docker exec -it {{容器}} sh' },
    { name: l('清理不用的镜像和缓存', '清理不用的映像和快取', 'Clean unused images and cache'), command: 'docker system prune -f', danger: true },
  ] },
  { name: l('软件与系统', '軟體與系統', 'Packages & system'), items: [
    { name: l('更新软件（Debian/Ubuntu）', '更新軟體（Debian/Ubuntu）', 'Update packages (Debian/Ubuntu)'), command: 'apt update && apt upgrade -y', danger: true },
    { name: l('更新软件（Fedora/RHEL）', '更新軟體（Fedora/RHEL）', 'Update packages (Fedora/RHEL)'), command: 'dnf upgrade -y', danger: true },
    { name: l('清理旧日志（保留 7 天）', '清理舊日誌（保留 7 天）', 'Trim old logs (keep 7 days)'), command: 'journalctl --vacuum-time=7d', danger: true },
    { name: l('最近登录过的人', '最近登入過的人', 'Recent logins'), command: 'last -n 20' },
    { name: l('SSH 登录失败记录', 'SSH 登入失敗紀錄', 'Failed SSH logins'), command: "journalctl -u sshd -u ssh --since today --no-pager | grep -i 'failed' | tail -30" },
  ] },
]

// placeholders: {{name}} or {{name:default}}; Docker's own {{.Field}} templates are left alone.
export function placeholders(cmd: string): { key: string; name: string; def: string }[] {
  const out: { key: string; name: string; def: string }[] = []
  for (const m of cmd.matchAll(/\{\{([^{}.][^{}]*?)\}\}/g)) {
    if (out.some((x) => x.key === m[0])) continue
    const [name, ...rest] = m[1].split(':')
    out.push({ key: m[0], name: name.trim(), def: rest.join(':').trim() })
  }
  return out
}

export function fill(cmd: string, vals: Record<string, string>): string {
  let s = cmd
  for (const p of placeholders(cmd)) s = s.replaceAll(p.key, vals[p.key] ?? p.def)
  return s
}
