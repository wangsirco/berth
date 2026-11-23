# 更新日志 / Changelog

简体中文在前，English below each version.

## 1.6.5 · 2026-11-24

**开了邮箱验证（Cloudflare Access），面板之间照样能连**
- 经 Cloudflare 隧道开放面板并勾选邮箱验证时，面板会同时建一条放行规则，只放行机器之间调用的接口：Wharf、互联的面板、远程主机加入和连接、部署钩子、迁移、订阅链接、状态页、CA 证书下载。这些接口本来就各自校验签名或一次性码。人打开面板仍然要先过邮箱验证。
- 用 1.6.3 / 1.6.4 开过邮箱验证的面板，升级后会自动补上这条放行规则。
- 对方的公网名字开了 Access、但没放行接口时，会明确说是 Cloudflare Access 挡住了，不再只报「连不上」。把 Wharf 改为公网互联前会先试一次，被挡住就不切换。

---

- With Cloudflare Access on the panel's tunnel name, a second Access app lets the machine endpoints through (Wharf, linked panels, remote hosts, hooks, migration, subscriptions, status pages, the CA download); people still get the email check. Panels published with Access by 1.6.3/1.6.4 get it added after the upgrade.
- A peer whose public name is behind Access without such a bypass is reported as such instead of "unreachable"; switching the Wharf link to the internet checks this first.

## 1.6.4 · 2026-11-24

**经隧道 / 公网名字登录：Passkey 跟着名字走，不再卡在「还差一步」**
- **修复**：面板经 Cloudflare 隧道开放后，在公网名字（如 `berth.你的域名`）上用密码登录，会要求用 Passkey 做二步验证。可是 Passkey 只在 `mini.lan` 上，这个名字上用不了，于是登录卡住。现在只算当前名字上能用的 Passkey：这个名字上还没有时，先用密码登录，登录后马上提示「在这里添加 Passkey」。
- **开放之后马上提醒**：隧道开放后，设置页的隧道卡片会说明「`berth.你的域名` 上还没有你的 Passkey」，一个按钮带着登录状态过去，直接停在添加 Passkey。
- 账号自己选了「密码 + 二步验证」、但这个名字上没有可用的验证方式时，登录会明确说 Passkey 在哪个地址、怎么补上，不再只显示没法完成的一步。

---

- Passkeys belong to a name: on a name where the account has none (e.g. the tunnel name), the forced second step no longer asks for a Passkey that cannot work there; sign in with the password and the panel asks you to add one on that name.
- After publishing through the tunnel, the card offers to add a Passkey on the public name (opened signed in).
- An account that requires two steps itself is told where its Passkeys are when this name has none.

## 1.6.3 · 2026-11-24

**Cloudflare 隧道：在设置页一步开放面板**
- 「设置 → 访问 → 公网访问」选 **Cloudflare 隧道** 时，下面直接展开要做的事，不用再去别的页面：
  - 还没有 Cloudflare 账号：说明怎么做，「连接 Cloudflare」就在这里，粘贴令牌即可；
  - 令牌失效、看不到域名或缺少 Tunnel 权限：直接说缺什么，旁边就是「更换令牌」；
  - 令牌可用：选一个名字（如 `panel.你的域名`），点「通过隧道开放面板」，面板会创建隧道（已有就共用）、添加 DNS 记录和隧道路由，并把面板的公网名字设成它。不开放任何端口，证书用 DNS 验证申请。
- **打开前先验证邮箱（Cloudflare Access）**：默认勾选，只有你填的邮箱收到验证码后才能看到面板，别人连登录页都看不到。令牌没有 Access 权限时会说明并提示更换。
- **隧道上的安全入口可以选**：默认仍然要带安全入口；也可以不勾，不勾又不验证邮箱时会明确提示风险。
- 经隧道打开时，和公网直连一样每个账号都要二步验证；停止开放或撤销这次改动会删掉 DNS 记录、路由和 Access。
- **安全入口可以关掉，但要确认两次**：清空安全入口保存时，先弹出说明风险的确认框，要勾选「我了解风险」才能继续。开启公网访问时没有安全入口，也不再直接挡住，而是同样提醒并确认。

---

- Settings → Access: choosing Cloudflare Tunnel now does it in place — connect a Cloudflare account there, see what a token lacks, or pick `panel.your-domain` and publish the panel through the tunnel in one change (tunnel, DNS record, route, public name; certificate by DNS validation; no port opened).
- Cloudflare Access (an email code before the panel shows) is ticked by default; the security entry on the tunnel name is optional (on by default), with a warning when both are off. Two-step verification is required through the tunnel, as for public access.
- The security entry can be turned off, after a confirmation that spells out the risk; public access without an entry asks the same way instead of refusing.

## 1.6.2 · 2026-11-23

**和路由器绑定，从头到尾都说清楚**
- **Wharf 页直接内网发现**：左侧「Wharf 码头」的第 2 步会自己在网关上找路由器的 Wharf（优先用 `wharf.lan`），找到就能在这里「申请绑定」，不用再绕到「面板互联」。手动填地址配对收在下面，需要时展开。
- **换名字之前先告诉你**：配对前明确写着「配对后，面板改用 `mini.lan` 打开」，默认勾选（推荐），也可以不改。你的 Passkey 在旧名字上时，会提前说明之后要在新名字上再添加一次。
- **配对完一步步带你迁移**：
  - 马上弹出说明：新地址是什么、Passkey 跟着名字走、旧地址还能用；
  - 一个按钮带着登录状态去新地址，直接停在「添加 Passkey」。
- **旧地址不再把你挡在外面**：
  - Passkey 还在旧地址上时，登录前不会把你跳走，先在旧地址用 Passkey 登录，登录后再带你去新地址；
  - 新地址的登录页如果还没有你的 Passkey，会给出「回到旧地址用 Passkey 登录」。
- **不再只给一个 404**：在局域网里用面板自己的名字打开、但没带安全入口时，会显示一页说明：
  - 面板的新地址；
  - 要用安全入口打开；
  - 忘了安全入口或密码时，在服务器上运行 `sudo berth admin`。

  页面不会透露安全入口本身；公网来的访问照旧只回 404。

---

- The Wharf page finds the router's Wharf by itself and binds it there (pairing by address folded below).
- Before pairing it says the panel will move to host.lan (on by default, can be kept off).
- Afterwards a dialog walks you over: the new address, Passkeys belong to a name, go there signed in and add one.
- The old name lets you sign in with your existing Passkey first and then takes you over; the new name's login page links back when it has no Passkey yet.
- On the LAN, the panel's own name without the security entry shows an explanation instead of a bare 404 (never the entry itself).

## 1.6.1 · 2026-11-23

**修复：用 `https://mini.local`（不带端口）打开面板，进了安全入口马上跳到 404**
- 原因：经网站入口转给面板时，面板看到的地址是入口内部用的名字，不是你打开的 `mini.local`。于是面板以为你不在主名字上，把你送去一个跳转链接；跳转链接又因为同样的原因认不出来，就显示了 404。
- 现在入口会把你打开的名字原样转给面板：不再乱跳，Passkey 也认得这个名字。
- 页面这边也加了保护：已经在主名字上时，绝不再跳。
- 升级后入口会自动重新加载，不用做什么。

---

- Fix: opening the panel at https://mini.local (no port) bounced to a 404 right after the security entry. The edge now passes the browser's host name to the panel (also what Passkey checks), and the page never moves when it is already on the main name.

## 1.6.0 · 2026-11-23

**一个面板，一个名字，不用记 IP 和端口**
- 面板直接用名字打开，不带端口：例如 `https://mini.local`；和路由器的 Wharf 配对后，换成 `https://mini.lan`。
  - 网站入口（443 端口）按名字把面板的名字转给面板，其他名字照旧给网站。
  - 证书用面板自己的本地证书，设备信任一次就好。
  - 原来的「IP + 端口」保留作后备地址。
- **只认一个名字**：用 IP 或别的名字打开时，只要这台设备打得开面板的名字，就会自动换过去，并带着安全入口和登录状态。这样从哪里进来都落在同一个名字上，Passkey（指纹 / 面容登录）就一直能用。
  - 这台设备暂时打不开时，页面顶部会说明，并给出「让这台设备能用」的步骤。
- **配对了能登记名字的 Wharf**：面板会自动改用 `主机名.lan`，家里所有设备都能找到它，包括不支持 `.local` 的设备。
  - 改名后页面顶部会提醒：Passkey 跟着名字走，需要在新名字上再添加一次。
- **安装向导**：「内网域名」一步标为「推荐」，跳过按钮改成次要样式。
- **设置 → 访问地址**：一张卡片列出内网地址、公网地址和后备地址，并标明这台设备能不能打开。

**内网互联 / 公网互联：二选一，分得清清楚楚**
- 和 Wharf 配对、生成面板之间的配对码时，先回答「两台在同一个局域网吗？」：
  - **内网互联**：两边用内网名字；
  - **公网互联**：两边用公网名字。
  - 选定后，这条连接只用这一对名字，连不上也不会悄悄换成另一种。
- 用 IP 配对的 Wharf，如果 `wharf.lan` 就是同一台路由器，会改成用名字记住。
- Wharf 页顶部同时显示对方的内网名字和公网名字，可以「改为公网互联 / 改为内网互联」，不用重新配对。
- **打开 Wharf 时判断你在家还是在外**：
  - 用内网打开的 Berth，打开 Wharf 的内网名字；
  - 用公网打开的 Berth，先试一下内网通不通；
  - 没把握时，让你选「打开内网」还是「打开公网」，可以记住这台设备的选择。
  - 都是免登录跳过去，从 Wharf 打开 Berth 也一样。

**Berth 里的 Wharf 页：图形化，直接看路由器的统计**（需要 Wharf 0.25，配对时勾选「查看统计」）
- **总览**：
  - 四个数字：在线设备、今日流量、外网访客、拦截；
  - 24 小时外网流量图；
  - 流量最多的设备、访客来自哪些国家；
  - 这台服务器在路由器上的分流、映射和封禁。
- 另外几个标签页：
  - **流量**：下载 / 上传曲线，按设备、按服务排行；
  - **流向世界**：家里在和哪些国家通信；
  - **设备**：在家 / 离开，点开看这台设备的流量；
  - **访问记录**：可以一键在服务器上封禁；
  - **DNS**：请求、拦截和拦截最多的域名；
  - **联动设置**：原来的设置收进这里。
- 每个数字、每一行都能点进去；每块都有「在 Wharf 中打开」，会落在 Wharf 同一页、同样的筛选上。

**修复**
- 网站入口：上传的证书以前会被用到所有网站上，现在只给它自己的网站。
- 从 Wharf 免登录进入 Berth 时，设了安全入口的面板会显示 404。
- Wharf 回复「对接过期」「签名过期」时，不会再退回带令牌的旧签名。

---

- **One name per panel, no port**:
  - `https://mini.local` (or `https://mini.lan` once paired with Wharf) opens the panel through the site entry on 443, on the panel's own certificate.
  - Any other address moves to that name, keeping the entry and the sign-in, so Passkeys keep working.
  - Settings → Where the panel opens shows the LAN, public and fallback addresses; the setup wizard recommends the local name.
- **LAN or internet links**:
  - Pairing with Wharf or another panel asks which; the link uses that pair of names only and can be switched without pairing again.
  - Opening the other side checks whether you are at home and asks when unsure.
- **Wharf page with charts** (Wharf 0.25, scope Statistics): overview tiles, 24 h traffic, busiest devices, visitor countries, traffic, world, devices, access log, DNS. Everything clicks through, and each panel opens the same page in Wharf.
- Fixes:
  - an uploaded certificate was served for every site;
  - the Wharf sign-in link failed behind a security entry;
  - Berth no longer falls back to sending the token on expired-link errors.

## 1.5.1 · 2026-11-23

**修复：和真实的 Wharf 路由器面板对接时的几处格式问题**
- **在内网发现里申请绑定 Wharf**：Wharf 点「允许」后，Berth 只显示「wharf.error」，配对没有完成。原因是交换令牌时字段名和 Wharf 的不一致。手动填地址配对也受影响。现在两种方式都能完成配对。出错时也会显示翻译好的原因和 Wharf 的原话，不再显示代号。
- **路由器视角**：设备列表、DDNS 列表和端口占用检查读不出 Wharf 的数据；「用 Wharf 的公网 IP 更新 DDNS」也取不到地址。现在都能正常显示和使用。
- **封禁同步**：
  - 同步到 Wharf 的封禁现在能正常记住，解封时会一起撤回；以前自动封禁还会被反复重发。
  - 在 Wharf 里撤销一条 Berth 同步过去的封禁后，Berth 现在也会更新。
  - IPv6 封禁不再推给 Wharf，因为 Wharf 只能封禁 IPv4 地址。
- **其他**：Berth 建的端口映射在 Wharf 里显示站点名；Wharf 发来的紧急告警按「紧急」处理。

---

- Fix: pairing with a real Wharf from LAN discovery (and by address) failed after Wharf allowed it; errors now read in words.
- Fix: the router view (devices, DDNS, port checks) and the DDNS "Wharf's public IP" source read Wharf's list format.
- Fix: bans synced to Wharf are remembered and withdrawn, carry Berth's own number, and IPv6 bans stay on the server; Wharf's urgent alerts count as critical.

## 1.5.0 · 2026-11-23

**内网发现：像连蓝牙一样绑定**
- 「面板互联」页新增「内网发现」：扫描本机所在的局域网，列出开放了发现的 Berth 面板，以及路由器上的 Wharf（按证书认出）。
- **绑定另一台 Berth 面板**：
  - 对方点「让别人发现我」，开放 10 分钟。默认关闭，只回答局域网里的请求。
  - 这边点「申请管理它」，对方页面顶部会弹出申请。对方点「查看并允许」，选好交出哪些服务器，就完成了，这边自动确认。
- **绑定路由器的 Wharf**（需要 Wharf 0.24.0 及以上）：
  - 先在 Wharf 的「Berth 服务器面板」页点「允许 Berth 申请绑定（10 分钟）」，再在 Berth 里点「申请绑定」。
  - 两边会显示同一个 6 位校验码，一样再在 Wharf 里点「允许」。Berth 会自动完成配对，不用来回跳转页面。
  - 校验码由两边各自算出：中间要是有人换了证书，两边的数字就对不上，Berth 会直接停止申请。
- 跨网的情况仍然用配对码（面板之间）或填地址配对（Wharf）。

**本地域名与 Passkey**
- Passkey（指纹 / 面容登录）不能在 IP 地址上用。新增引导，四步设好：
  1. **起个名字**：默认用「主机名.local」，也可以自己取。
  2. **让设备找得到它**：`.local` 名字由服务器在局域网里广播（mDNS），需要时自动装好 avahi 并放行防火墙，执行前先给你预览。
  3. **让这台设备信任面板证书**：按 Mac、iPhone / iPad、Windows、安卓、Linux 分别给出步骤，Firefox 单独说明。
  4. **检查后用新名字打开**：检查通过后，直接带着安全入口用新名字打开面板。
- 已和 Wharf 配对时，可以直接选「主机名.lan」：保存时会自动在路由器上登记，局域网里所有设备都能解析这个名字，包括不支持 `.local` 的设备。换名字或清空时，路由器上的旧记录会一起删掉；解除配对时，路由器会把这些记录清理掉。
- 新装面板的欢迎向导里多了可以跳过的一步「本地域名」；在「设置」和 Passkey 卡片里也能随时打开这个引导。
- 名字会签进面板证书。这项设置可以预览，也可以撤销。

**Wharf 对接升级（协议 v2，配合 Wharf 0.24.0）**
- **签名不带令牌**：两边都升级后，请求里只带令牌的标识，令牌本身不再出现在网上。即使经 Cloudflare 等代理，代理也看不到它。自动切换，不用重新配对；Wharf 页会显示「签名不带令牌」。
- **配对更稳**：
  - Berth 把自己的多个地址一起交给 Wharf，内网地址优先。Wharf 连不上其中一个时，会自动换下一个。
  - 配对结束后的回跳由 Berth 自己完成，不再依赖浏览器里的登录状态，也不需要再输入安全入口。
- **补齐 Wharf 会调用的接口**：
  - 互联状态测速（ping）；
  - 令牌轮换；
  - Wharf 收窄权限或改主机后同步过来；
  - 在 Wharf 里撤销封禁后同步过来；
  - Wharf 设备页里的「Berth」标签页（这台主机的网站和容器）；
  - DDNS 列表。
- **修复**：
  - 在 Berth 里解除和 Wharf 的配对时，Wharf 那边没有收到通知；
  - 用公网域名配对 Wharf 时，说明里让人比对对不上的指纹。

**其他**
- 公开仓库只发布图标、截图和插件开发指南，开发中的方案和交接文档不再公开。

---

- **LAN discovery**, Bluetooth-style:
  - Find Berth panels that opened discovery (10 minutes, LAN only) and the router's Wharf.
  - Ask to manage a panel; it allows at the top of its page.
  - Ask Wharf (0.24.0+) to pair: both show the same six-digit check code, and Berth finishes the pairing on its own once Wharf's admin allows.
- **Local name and Passkey guide**:
  - pick a name: `hostname.local` via mDNS (avahi installed and the firewall opened when needed), or `hostname.lan` registered on the router through Wharf;
  - trust the panel certificate, with steps for each OS;
  - check the name, then open the panel by it.
  - Available in the setup wizard and in Settings.
- **Wharf protocol v2**:
  - Requests are signed with a key id, so the token never travels.
  - Berth hands Wharf several addresses, LAN first.
  - The pairing callback is finished server-side.
  - Berth now serves ping, rotate, events, hosts and DDNS for Wharf.
  - Fix: unpairing from Berth now tells Wharf.

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
