# 更新日志 / Changelog

简体中文在前，English below each version.

## 1.4.0 · 2026-11-23

**面板互联：两台都装了 Berth 面板的服务器，交给一个主面板统一管理**
- 侧栏新增「面板互联」，并排说明两种绑定方式：对方没装面板，用「添加主机」装 agent；对方已经装了面板，用「面板互联」。
- 配对码 + 两边确认：
  - 主面板生成配对码，先确认对方用哪些地址连过来（内网、公网域名、Tailscale 都行）；也可以发一个链接，在对方面板上打开直接落到确认页。
  - 对方看到谁要管、勾选交出哪些服务器、对方能做什么，以及一个 6 位校验码。
  - 主面板确认前看到同一个校验码，一样再确认。两边都要再验证一次身份。
- 生效后，对方的服务器在主机菜单里单独成一组（例如 `local@家里mini`），网站、容器、虚拟机、画面、终端、文件等页面都能直接用，变更可撤销。
- **安全**：
  - 互联密钥由两边各自算出（X25519），不在网上传；之后每个请求都签名，不能冒充、不能重放。
  - 主面板在对方那边只是管理员，只能动交出去的服务器，碰不到对方的账号、令牌和设置。
  - 每一步都记进对方面板的审计，写明是谁做的。
  - 对方随时可以断开，立即生效；一个面板同时只能交给一个主面板。

**内网互联与公网互联（面板之间、面板与 Wharf 都适用）**
- **证书两种都认**：直连时认对方面板自己的证书；经公网域名、反代、Cloudflare 或 Let's Encrypt 时，认对这个域名有效的公网证书，续期不会断。修复：用公网域名绑定 Wharf 时证书对不上。
- **地址**：内网优先、公网兜底，记住上次能用的那个。
- **显示**：
  - 内网只显示一个小标签「内网互联」；Tailscale 显示「Tailscale 互联（加密）」；
  - 公网显示「公网互联」，并注明直连（端到端加密）还是经 Cloudflare 等代理（会提示加密在代理处解开）；
  - 都显示延迟与抖动、对方 IP，公网时显示国家。

**虚拟机与终端**
- 新建时选「局域网独立 IP」一步完成：只有一次确认，网卡在第一次开机前接好；确认后「新建」窗口立即关掉，进度看任务日志。弹窗遮罩加深并轻微虚化，叠在一起时层次清楚。
- 修复：虚拟机里装了 Docker 时，地址显示成 docker0 的 172.17.0.1。现在显示它真正的局域网地址。
- 终端可以在新标签页打开（工具栏里勾一次，各处的「终端」按钮都跟着）；页面下方的会话可以「在新标签页继续」，是同一个会话，不用重新登录。
- 左上角主机菜单里加「添加主机」「面板互联」「管理主机」。
- 修复：已经装了 Berth 面板的服务器，运行「添加主机」的加入命令会被另一个面板抢走 agent。现在会停下来说明原因，什么也不改。

---

- **Linked panels**: hand the servers of one Berth panel to a main panel. Link code, check code and confirmation on both sides; the link key is never sent and every request is signed. The main panel acts as an admin on the handed-over servers only (no accounts, tokens or settings), audited on both sides; either side can unlink at once.
- LAN and internet links, for panels and Wharf alike:
  - the panel's own certificate or a valid public one (reverse proxy, Cloudflare, Let's Encrypt);
  - LAN / Tailscale / internet tags, direct or through a CDN;
  - latency, address and country.
  - Fix: pairing Wharf by its public name.
- New VMs on the LAN in one step; VMs running Docker show their real address.
- Terminals open in a new tab, and a session at the bottom can move to a tab and continue.
- The host menu offers adding a host and linking a panel.
- Fix: the add-host command no longer takes over a server that runs its own panel.

## 1.3.0 · 2026-11-22

**虚拟机「画面」：像 VMware / PVE 的控制台，开在单独的窗口里**
- 能看到虚拟机的屏幕：固件、开机过程、登录界面、网络没起来时的报错，全都看得到。虚拟机重启或开机时画面自动重连。
- 默认只看：鼠标划过不会被抓走；点一下画面才把键盘和鼠标交给虚拟机，点画面外面就交还，顶栏标明当前是「只看」还是「在操作」。
- 顶栏：Ctrl+Alt+Del、截图、画中画（浮在所有窗口上面，一边看一边干别的；Chrome / Edge）、全屏、断开 / 重连。
- 右侧工具栏：
  - 虚拟机状态、地址、规格、内存占用、已运行时长，重启 / 关机 / 强制关机。
  - 登录：「键入 root」「键入密码」，密码直接输进虚拟机，屏幕上不显示。
  - 常用按键（Ctrl+C / D / Z / L、Esc、Tab、↑）和 Ctrl+Alt+F1–F6 切换文字控制台。
  - 命令库（和终端同一个，「插入」或「运行」）；任意文字一个键一个键地输进去，登录界面和控制台也能用。
- PVE 的虚拟机和容器也用这个窗口：电源按 PVE 的接口操作，浏览器不用登录 PVE，也不用信任它的证书。
- 之前建的虚拟机点「启用画面」（会关机再开机一次）。ARM 上 Incus 的串口控制台本来就用不了，已去掉。

**虚拟机网络与登录**
- 修复：虚拟机改成「局域网独立 IP」后一直「等待路由器分配」、终端打不开。其实路由器早就分了地址，是虚拟机里的 SELinux 拦住了 incus-agent，面板读不到。新建的虚拟机自带开机自检，SELinux 一开就自动放行；已经这样的，详情和画面里都有一键修复。
- 地址后标注「NAT 内部」或「局域网」，10.x 的内部地址不会再看着像出错；读不到地址时直接说明并给出 MAC。
- 新建时可选 root 的登录方式：勾选账号里的公钥、另外粘贴一把、自动生成或自己设置密码、是否允许用密码 SSH 登录。
- 建好后弹出地址、用户和密码，只显示这一次。

**稳定性**
- 修复：面板查询虚拟机时偶尔把整个 Incus 卡死（所有 incus 命令都不返回，只能重启服务）。
- 启用画面时先正常关机（60 秒没关再强制），设置好再开回来；中途出错也会开回来。

---

- **VM screen, in its own window like a VMware / PVE console:** firmware, boot, login prompt and network errors all visible; reconnects by itself across restarts.
- Watch-only until you click the screen; a click outside gives keyboard and mouse back.
- **Toolbar:** Ctrl+Alt+Del, screenshot, picture-in-picture (floats above other windows), full screen.
- **Side panel:**
  - state, address and power;
  - type root / the password (never shown);
  - common keys and Ctrl+Alt+F1–F6;
  - the command library;
  - typing any text key by key.
- PVE guests open in the same window.
- Fix: a VM switched to its own LAN address stayed on "waiting for the router" and its shell would not open: SELinux in the VM blocked incus-agent. New VMs guard against it; existing ones get a one-click fix.
- Addresses are labelled NAT or LAN.
- Choose root's keys and password when creating a VM; the login is shown once afterwards.
- Fix: the panel's polling could freeze the whole Incus API.

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
