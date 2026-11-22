# 更新日志 / Changelog

简体中文在前，English below each version.

## 1.2.1 · 2026-11-22

**代理**
- 新增「在线与访问」：在线用户从哪些地址、哪个国家、经哪个节点连进来；访问记录（谁访问了哪个网站和端口、几次、是否被规则拦截），可按用户和关键词筛选或只看被拦截的；最近 24 小时访问最多的网站。访问记录保留 30 天。
- Xray 版本可以选择和切换（GitHub 正式版 / 测试版，或总是用最新版）；切换先预览，可撤销。
- 新建或修改节点时检查端口是否被占用、本机防火墙是否放行；保存后可顺手放行防火墙，并在已配对的 Wharf 路由器上映射端口，每一步先预览、确认后才执行。节点列表也可一键「在路由器上映射」。
- 流量统计与在线状态改为每 20 秒更新。

---

- Proxy: new Online & access tab — who is connected from where and through which node, what sites they visited (with rule blocks), top destinations in 24 h; kept 30 days.
- Pick and switch the Xray version (stable / pre-release / always latest), with preview and undo.
- Node ports are checked for conflicts and firewall; after saving, open the firewall and map the port on a paired Wharf router, each step previewed first.
- Traffic and online status refresh every 20 seconds.

## 1.2.0 · 2026-11-22

**网站**
- 网站详情改为整页设置，左侧分类：概览、域名与跳转、目标、HTTPS、访问控制与防护、访客统计、访问日志、诊断、关联（PHP 网站另有 PHP）。顶部一键打开网站、项目、容器或网站文件夹。
- 域名可以随时增删，指定主域名；其他域名可「同样访问」或「301 / 302 跳转到主域名」（保留路径）。一键加上 www / 不带 www 并跳转。改主域名时网站文件夹一起改名。
- 网站列表按主域名自动归类，也可以自定义分组；每行显示目标（可直接跳过去）、隧道 / 直连、证书、真人访客和打开按钮。
- 建站或改域名时检查每个域名的 DNS：已指向本机 / 隧道 / 别处 / 没有解析；在 DNS 账号里的域名可一键创建或修改解析，先预览、确认后才改。

**访客统计**
- 修复：经 Cloudflare 隧道或橙云代理的访客被记成内网地址。现在统计、日志和 IP 黑白名单都按真实访客 IP。
- 统计区分真人访客、新访客 / 回访，以及搜索引擎、AI 爬虫、社交预览、监控、脚本、扫描器。

**Cloudflare**
- Cloudflare 页面首次进入是连接向导：能做什么、令牌需要哪些权限（逐项说明用途）、粘贴并检查。令牌在 Cloudflare 页面添加和更换。

**文件管理**
- 常见文件夹旁标注用途（系统配置、系统日志、网站文件、应用、Docker 数据……），系统目录标「勿动」，进入时说明里面是什么。

---

- Site details are now a full page with sections on the left; shortcuts to open the site, its project, container or folder.
- Edit a site's names any time: pick the primary; others serve the site too or redirect to it (301 / 302, path kept). Changing the primary renames the site folder.
- The site list groups sites by registered domain or a group you name, and shows target, tunnel / direct, certificate and real visitors.
- DNS is checked for every name when adding or editing a site; records in a DNS account can be created or fixed in one click, after a preview.
- Fix: visitors through a Cloudflare Tunnel or proxy were logged with an internal address. Stats, logs and IP rules now use the real visitor.
- Visitor stats separate people, new and returning visitors, and search, AI, social, monitoring, script and scanner traffic.
- The Cloudflare page starts with a connect wizard explaining each token permission; tokens are added and replaced there.
- Files labels common folders in plain words and marks system ones to leave alone.

## 1.1.1 · 2026-11-17

- 修复：内存很小、没有 swap 的机器安装桌面时 dnf 被系统杀掉。安装桌面、软件包、原生数据库、原生 PHP、虚拟机组件时临时加 swap，装完删除；仍不够时说明原因。

---

- Fix: on small machines without swap, dnf was killed while installing the desktop. Installs now add temporary swap and explain when memory still runs out.

## 1.1.0 · 2026-11-17

- 公网访问：安装时检测公网 IP 并询问是否对公网开放；向导和设置里可选公网 / Tailscale / Cloudflare 隧道 / 只在局域网；`sudo berth admin public on|off`。
- 面板内网 / 外网地址；添加主机命令按地址给出。
- 邀请朋友（权限预设、配额）；远程桌面和虚拟机桌面的免注册分享链接。
- Xray 代理扩展；Wharf 端口检查。
- 安装前显示建议配置与本机配置；内存紧张时不再一直等待。
- 文案全面修正；修复设了安全入口时文件分享打不开。

---

- Public access chosen at install (internet / Tailscale / Cloudflare Tunnel / LAN); `sudo berth admin public on|off`.
- Panel internal / external addresses; invitations with permission presets; share links for remote and VM desktops.
- Xray proxy extension; Wharf port check; specs shown before installs; texts fixed throughout.

## 1.0.0 · 2026-11-16

- 第一个公开版本。/ First public release.
