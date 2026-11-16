# Wharf 扩展：与 Berth 泊位联动

> 交给 Wharf 项目（<https://github.com/wangsirco/wharf>）实施。
> Berth 是同一作者的 Linux 服务器管理面板，和 Wharf 同一系列：Wharf 管路由器，Berth 管服务器。Berth 的总方案在 `specs/berth-design.md`，与本文相关的是它的 §12。
> 本文只描述 **Wharf 这边**要做的事。Berth 那边按总方案实施，两边以本文的接口约定为准。
> 日期：2026-11-10

---

## 1. 目标

让 Berth 和 Wharf 之间有一条安全、可审计的「内部通道」，做到：

1. **端口映射联动：**
   - Berth 新建站点时，自动在 Wharf 添加端口映射，或登记 443 SNI 分流；删除站点时自动撤销。
2. **DDNS 协调：** 同一个域名只由一方维护，避免两边互相覆盖。
3. **互相跳转免登录：** 在一个面板里点「打开另一个」，不用再登录一次。权限按各自的账号来。
4. **数据互看：**
   - Wharf 的设备列表能看到 Berth 主机上的容器、虚拟机、站点；
   - Berth 能看到路由器视角的公网流量和攻击来源。
5. **封禁同步：**
   - 默认只在本地；勾选后同步到另一边。
   - 可以对单条封禁追加或撤回同步，来源可追溯。
6. **统一告警：** 指定一方作为主通知方，另一方的告警转交给它，同一件事只收到一份通知。

**不做：**
- 不给 Wharf 加通用的第三方 API 令牌（以后可以在本文令牌机制的基础上扩展）；
- 不改变 Wharf 现有任何功能的默认行为。

---

## 2. 与现有 Wharf 代码的关系

已读过 Wharf 0.18.x 的代码，下面列出相关的现状：

- **鉴权：**
  - 现在只有登录会话（Cookie + CSRF），没有 API 令牌；
  - 面板 → agent 用 HMAC 签名（`internal/panel/agentclient.go`、`internal/sign`）。
- **权限：** `internal/panel/rbac.go` 里定义了 17 个权限键，例如 `forwards.view`、`forwards.manage`、`access.manage`；角色分超级管理员和管理员。
- **端口转发：** `internal/panel/forwards.go`。
  - 每条转发是共用同一个 id 的三段 uci 配置（`owpr_<hex>`）；
  - 支持按来源限制（国家、IP 段）；
  - 有 `Kind` 字段（现在用于标记代理节点的端口 `"proxy"`）；
  - 支持端口段授权和冲突检查。
- **443 SNI 分流：** `internal/proxyfront`。
  - 代理扩展开启时，WAN 443 由 proxy-front 接管：读取 TLS ClientHello，按 SNI 把节点域名交给 Xray，其他域名给伪装站点；
  - 现有的 1Panel 集成（`internal/panel/onepanel.go`，只读）用来列出「仍由 1Panel 提供的网站」，避免节点域名和它们冲突。
- **DDNS：** `internal/panel/ddns.go`，支持 Cloudflare、阿里云、DNSPod 等。
- **告警：** `internal/panel/alerts.go`，渠道有 Telegram、Server 酱。
- **审计：** `audit(...)`，可还原。
- **访问控制：** 设备断网、黑白名单（`policies`），以及面板访问控制（`access.go`）。

**Berth 集成的定位：**
- 它是 1Panel 集成的「读写版」，单独做成一个集成；
- 和 1Panel 集成共存，互不影响；
- 只由超级管理员启用。

---

## 3. 配对与对接令牌

### 3.1 配对流程

1. 在 Berth「设置 → 集成 → Wharf」填写 Wharf 地址。Berth 打开 Wharf 的配对页：
   ```
   https://<wharf>/pair/berth?berth_url=<berth 地址>&berth_fp=<berth 证书指纹>&code=<一次性码>&cb=<berth 回调>
   ```
2. Wharf 要求超级管理员登录。配对页显示：
   - Berth 的地址和证书指纹；
   - 这次配对会授予的权限（勾选列表，见 §3.2，默认勾全部，可取消）；
   - 有效期（默认永久，可选 30、90、365 天）；
   - Berth 主机在路由器上对应的设备：自动按 Berth 地址的 IP 和 MAC 匹配，可以手动改。
3. 超级管理员点「同意配对」后，Wharf 执行以下动作：
   - 生成 **Berth 对接令牌**（`wharf_bth_<随机>`，数据库只存哈希）；
   - 记录 Berth 的证书指纹（之后所有请求都校验）；
   - 浏览器带一次性码跳回 `cb`。
4. Berth 用一次性码向 Wharf 换取对接令牌（服务器之间直接请求，固定 Wharf 证书指纹）。同时 Berth 也为 Wharf 签发一个 Berth 侧的令牌（`berth_wrf_<随机>`），并通过同一次交换交给 Wharf。双方从此互相持有。
5. 两边的「集成」页都显示：
   - 对方地址、配对时间、权限范围、最后通信时间；
   - 操作：收窄权限、轮换令牌、解除配对。

**解除配对：**
- 任意一方都可以解除配对，另一方的令牌立即失效。
- 解除时询问：由 Berth 创建的映射和登记是保留（转为普通映射）还是删除。

### 3.2 令牌权限范围

| 范围 | 允许 Berth 做什么 |
|---|---|
| `forwards` | 在指定目标设备（配对时选定的 Berth 主机，可多个）上新建、修改、删除**由 Berth 创建的**端口映射；可读取全部映射，用于冲突检查 |
| `sni` | 在 proxy-front 登记或撤销「这些域名的 443 流量原样转给 Berth 主机」 |
| `ddns` | 读取 DDNS 配置与状态；协调域名归属；必要时让 Wharf 立即更新一次 |
| `devices.read` | 读取设备列表，用于在 Berth「指向 → 内网地址」里挑设备 |
| `traffic.read` | 读取 Berth 主机的流量、命中映射的外部连接、扫描与攻击来源 |
| `bans` | 下发或撤销同步过来的封禁；读取 Wharf 侧标记为可同步的封禁 |
| `alerts` | 向 Wharf 投递告警（Wharf 为主通知方时），或接收 Wharf 转交的告警 |
| `sso` | 申请一次性登录票据（§6） |

### 3.3 鉴权方式

- **Berth → Wharf：**
  - 请求头：`Authorization: Bearer wharf_bth_...`；
  - 签名头：`X-Berth-Sig`，对「方法 + 路径 + 时间戳 + nonce + body 哈希」做 HMAC，密钥是令牌派生的签名密钥，与 Wharf agent 签名同一套算法，可复用 `internal/sign`；
  - 另带 `X-Berth-Ts`、`X-Berth-Nonce`；
  - 时间窗 ±60 秒；nonce 去重。
- **TLS：** 两边都可能用本地 CA，所以服务器之间的请求按配对时记录的证书指纹校验，不依赖公共 CA。
- **审计：** 所有经对接令牌的操作，执行者都记为「Berth（<Berth 名称>）」。如果 Berth 在请求里带了 `X-Berth-Actor`（例如「王 · 新建站点 vault.example.net」），也一并记入审计摘要。

---

## 4. 接口

接口统一挂在 `/api/ext/berth/v1/`，只接受对接令牌，不接受登录会话。返回 JSON；出错时返回 `{"error":"给人看的中文"}`，与 Wharf 现有约定一致。

### 4.1 端口映射

```
GET    /api/ext/berth/v1/forwards                 全部映射（只读），用于冲突检查
POST   /api/ext/berth/v1/forwards?plan=1          只返回改动预览（会新增哪些 uci 段、会不会冲突）
POST   /api/ext/berth/v1/forwards                 新建映射
PUT    /api/ext/berth/v1/forwards/{id}            修改（只限 Berth 创建的）
DELETE /api/ext/berth/v1/forwards/{id}            删除（只限 Berth 创建的）
```

请求体沿用 Wharf 现有转发的字段，另加三个字段：

```json
{
  "name": "berth: vault.example.net",
  "proto": "tcp",
  "wan_port": "8443",
  "target_ip": "192.168.1.20",
  "target_port": "443",
  "wan": "",
  "mode": "restrict",
  "allow_countries": ["HK", "TW"],
  "berth_ref": "site:vault.example.net",
  "berth_host": "mini",
  "note": "由 Berth 新建站点时创建"
}
```

- Berth 创建的映射：`Kind = "berth"`；元数据里记录 `berth_ref` 和 `berth_host`。
- 映射列表上显示「Berth」徽标和来源站点，点击可跳转到 Berth 的对应站点（走 §6 的免登录）。
- **在 Wharf 界面上：**
  - 管理员可以查看这些映射，但修改和删除时提示「这是 Berth 管理的映射，建议在 Berth 里操作」；
  - 超级管理员仍可强制修改，Wharf 会通知 Berth，由 Berth 把对应站点标为「外部修改」。
- **目标限制：** `target_ip` 只能是配对时选定的 Berth 主机。
- **冲突检查：** 沿用现有的端口段和冲突检查逻辑。

### 4.2 443 SNI 分流

适用场景：代理扩展开启时，WAN 443 已由 proxy-front 接管。

```
GET    /api/ext/berth/v1/sni                       当前 Berth 登记的域名
PUT    /api/ext/berth/v1/sni                       整体替换 Berth 登记的域名列表（幂等）
       {"target": "192.168.1.20:443", "domains": ["vault.example.net", "*.home.example.net"], "proxy_protocol": true}
```

- **proxy-front 的行为：** SNI 命中 Berth 登记的域名时，**不终止 TLS**，原样转给 Berth 主机的 443。可选加 PROXY protocol v2 头，让 Berth 的 Caddy 拿到真实客户端 IP。
- **优先级：** 代理节点域名 > Berth 域名 > 1Panel 站点 > 伪装站点。冲突时拒绝登记，并返回冲突的域名和占用方。
- **证书：** proxy-front 不再为这些域名申请证书，由 Berth 负责。
- **代理扩展未开启时：** 这个接口返回「443 未被占用，请改用端口映射」。Berth 会据此改用 §4.1 映射 443。

### 4.3 DDNS 协调

```
GET  /api/ext/berth/v1/ddns                       Wharf 维护的 DDNS 条目与状态
POST /api/ext/berth/v1/ddns/claim                 {"domain": "...", "owner": "wharf" | "berth"}
POST /api/ext/berth/v1/ddns/{id}/run              让 Wharf 立即更新一次
```

- 每个域名有唯一的维护方，存在 Wharf 侧的 `ddns_owner` 表，Berth 侧也存一份副本。
- 配对时自动检测：两边配置了同一个域名，就在两边的界面提示，让用户选择维护方。
- 维护方为 Berth 的条目，在 Wharf 里显示为只读，并标「由 Berth 维护」，反之亦然。
- **推荐：** 路由器拿公网 IP 最准，所以默认建议由 Wharf 维护 DDNS。Berth 只需要在新建站点时，请求 Wharf 为新域名加一条 DDNS（走 `claim`，`owner = "wharf"`）。

### 4.4 设备与流量（只读）

```
GET /api/ext/berth/v1/devices                      设备列表（名称、IP、MAC、类型、在线状态）
GET /api/ext/berth/v1/hosts/{ip}/traffic?range=24h 指定主机的公网流量、外部连接、国家分布
GET /api/ext/berth/v1/hosts/{ip}/inbound?range=24h 命中映射的外部连接、服务识别、扫描与爆破检测结果
```

数据直接复用 Wharf 现有的流量和转发用量统计（`traffic.go`、`fwdstats.go`），只是按 Berth 主机过滤。

### 4.5 封禁同步

```
GET    /api/ext/berth/v1/bans                     Wharf 侧标记为「可同步」的封禁
POST   /api/ext/berth/v1/bans                     Berth 同步来的封禁
       {"cidr": "203.0.113.7/32", "reason": "SSH 爆破", "source": "berth:mini:fail2ban", "expires_at": "2026-11-17T00:00:00Z"}
DELETE /api/ext/berth/v1/bans/{id}                撤回（只限 Berth 同步来的）
```

- **Wharf 侧的实现：**
  - 新建 ipset `owp_berth_ban`（hash:net，带超时）；
  - 在 WAN 入站和转发链里 drop；
  - 不影响局域网内的访问。
- **界面：**
  - 「访问控制」新增「外部封禁」分组；
  - 每条显示来源（例如「Berth · mini · SSH 爆破」）、到期时间、生效层；
  - 可以单条撤销。撤销后通知 Berth，Berth 把该条的「已同步到 Wharf」取消。
- **反方向：**
  - Wharf 里的封禁（例如转发详情页对扫描 IP 的封禁）新增一个勾选项「同步到 Berth」，**默认不勾**；
  - 勾选后，Wharf 调用 Berth 侧的接口（使用 Berth 签发给 Wharf 的令牌）。
- **安全阈值：**
  - 拒绝封禁私有地址、Wharf 自身的公网 IP、面板访问者当前的 IP；
  - 单次最大网段为 /16（IPv4）、/32（IPv6）。

### 4.6 告警转交

```
POST /api/ext/berth/v1/alerts                     {"level": "warn", "title": "...", "body": "...", "link": "https://<berth>/...", "dedupe_key": "..."}
```

- **设置位置：** 在 Wharf 的「告警」页设置「主通知方」：Wharf 或 Berth。
  - **Wharf 为主时：** Berth 的告警投递到这个接口，由 Wharf 的渠道（Telegram、Server 酱）发出，标题前加「[Berth · mini]」。
  - **Berth 为主时：** Wharf 把自己的告警转交给 Berth（调用 Berth 的同类接口），Wharf 自己的渠道不再重复发送这些类别。
- **去重：** 按 `dedupe_key` 在 10 分钟内只发一次。
- **审批类通知**（Telegram 里的「批准 / 拒绝」按钮）：按钮回调发给发起审批的那个面板，不能由对方代为批准。

### 4.7 免登录跳转（SSO 票据）

```
POST /api/ext/berth/v1/sso/ticket                 {"berth_user": "wang", "return_to": "/forwards"}
→ {"url": "https://<wharf>/sso/berth?t=<票据>"}
```

- **票据：**
  - 60 秒有效，只能用一次，绑定 audience（Wharf 地址）；
  - 内容包括：Berth 用户名、映射到的 Wharf 用户、`return_to`（只允许站内路径）。
- **用户映射：**
  - 在 Wharf「集成 → Berth → 用户对应」里，由超级管理员设置「Berth 用户 → Wharf 用户」；
  - 没有设置对应的，拒绝签发票据。
  - 映射到超级管理员需要在 Wharf 侧二次确认。
- **登录后：** 会话按映射的 Wharf 用户的权限运行；登录方式在审计和访客记录里记为「经 Berth 跳转」。
- 反方向（Wharf → Berth）对称实现：Wharf 调用 Berth 的同名接口。Wharf 顶栏用户菜单里新增「打开 Berth」入口，配对后才显示。

---

## 5. Wharf 界面改动

1. **设置 → 集成：** 新增「Berth 泊位」卡片。
   - 未配对时：显示说明，以及「在 Berth 里发起配对」的提示；
   - 已配对时：显示对方地址、证书指纹、权限范围（可收窄）、最后通信时间、用户对应、解除配对。
2. **端口转发列表：** Berth 创建的映射带「Berth」徽标，点击可跳转到 Berth；编辑时有提示（§4.1）。
3. **代理扩展页：** 「443 分流」区域显示 Berth 登记的域名，与代理节点、1Panel 站点并列。
4. **设备页：**
   - Berth 主机带「Berth 主机」标记；
   - 设备画像里新增「Berth」标签页，显示容器、虚拟机、站点。数据通过 Berth 签发给 Wharf 的令牌，调用 Berth 的只读接口获取。
5. **DDNS 页：** 由 Berth 维护的条目只读显示，并说明维护方。
6. **访问控制：** 新增「外部封禁」分组；Wharf 自己的封禁操作新增「同步到 Berth」勾选项，默认不勾。
7. **告警：** 新增「主通知方」设置。
8. **审计：** 执行者为「Berth（名称）」的条目，可以按来源筛选。
9. **用户菜单：** 新增「打开 Berth」。

**文案：** 中文说人话，例如：
- 「这条映射由 Berth 管理：删除站点时会自动撤销」；
- 「同步到 Berth：Berth 主机也会拒绝这个 IP」。

---

## 6. 数据库迁移（追加）

```sql
-- 配对与令牌
CREATE TABLE berth_link (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,              -- Berth 面板名称
  url TEXT NOT NULL,
  cert_fp TEXT NOT NULL,           -- Berth 证书 SHA-256 指纹
  token_hash TEXT NOT NULL,        -- wharf_bth_ 令牌的哈希
  outbound_token_enc TEXT NOT NULL,-- Berth 签发给 Wharf 的令牌（加密存储）
  scopes TEXT NOT NULL,            -- JSON 数组
  target_devices TEXT NOT NULL,    -- JSON：允许作为映射目标的设备（IP/MAC）
  expires_at INTEGER,              -- NULL = 永久
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER
);
CREATE TABLE berth_user_map (berth_user TEXT PRIMARY KEY, wharf_user_id INTEGER NOT NULL);
CREATE TABLE berth_bans (
  id INTEGER PRIMARY KEY, cidr TEXT NOT NULL, reason TEXT, source TEXT, expires_at INTEGER, created_at INTEGER NOT NULL
);
CREATE TABLE ddns_owner (domain TEXT PRIMARY KEY, owner TEXT NOT NULL CHECK (owner IN ('wharf','berth')));
CREATE TABLE berth_nonce (nonce TEXT PRIMARY KEY, seen_at INTEGER NOT NULL);  -- 定期清理超过 5 分钟的
```

- 端口映射的 Berth 元数据：沿用现有的「元数据单独存放」配置，`Kind = "berth"`，另存 `berth_ref` 和 `berth_host`。
- 设置项：
  - `alerts.primary`：`wharf` 或 `berth`；
  - `bans.default_sync_to_berth`：默认 `false`。

---

## 7. 权限与安全要求

- 只有超级管理员能配对、解除配对、修改权限范围和用户对应。
- 新增权限键 `integrations.berth`，仅超级管理员持有。普通管理员能看到 Berth 徽标，但不能看到令牌和配置。
- 对接接口：
  - 一律校验令牌、签名、时间窗、nonce、证书指纹；
  - 校验失败的请求写入访客记录并计数，连续失败按现有限速规则封禁。
- Berth 只能动 `Kind = "berth"` 的映射，只能把映射指向配对时选定的设备。
- 所有经接口的写操作都写审计，并且可还原，与现有审计还原一致。
- 令牌过期或被吊销后：
  - 已经建好的映射保留；
  - 在两边界面显示「对接已失效」，并提示重新配对。

---

## 8. 测试

- 单元测试：
  - 签名校验：正常、过期、重放、错误指纹；
  - 令牌范围；
  - 只能操作 `berth` 类映射；
  - SNI 优先级与冲突；
  - 封禁安全阈值；
  - DDNS 归属冲突检测。
- `httptest` 模拟 Berth 端，覆盖：
  - 配对全流程（含一次性码换令牌）；
  - 解除配对时的两种处理；
  - SSO 票据（过期、重放、无用户对应）。
- 端到端：用 puppeteer-core 走一遍配对、看到 Berth 徽标、从 Wharf 免登录跳到 Berth（Berth 端用模拟服务）。

---

## 9. 交付顺序（建议）

1. 配对、令牌、签名、审计执行者（§3），加集成卡片界面。
2. 端口映射接口与界面徽标（§4.1）。
3. 443 SNI 分流（§4.2）。
4. DDNS 协调（§4.3）。
5. SSO 票据（§4.7）。
6. 设备与流量只读接口，设备页的 Berth 标签页（§4.4）。
7. 封禁同步（§4.5）。
8. 告警转交（§4.6）。

第 1、2 步完成后，Berth 第 4 期的「经 Wharf 映射」就可以开始联调。

---

## 10. 补充约定（Berth 一侧实现时确定，2026-11-12）

Berth 侧已按本文实现并用模拟的 Wharf 测试（`internal/panel/wharfapi/wharftest`）。下面是原文没写死、由 Berth 实现时确定的细节，Wharf 侧请照此实现。

### 10.1 配对：一次性码换令牌

§3.1 第 4 步的接口：

```
POST /api/ext/berth/v1/pair/exchange          （不带令牌；只认一次性码，码 15 分钟内有效、只能用一次）
请求  {"code": "<一次性码>", "berth_token": "berth_wrf_<随机>", "berth_name": "Berth 面板名称",
       "berth_url": "https://<berth>", "berth_fp": "<Berth 证书指纹>"}
返回  {"token": "wharf_bth_<随机>", "scopes": ["forwards","sni",...], "wharf_name": "Wharf 显示名",
       "expires_at": 0, "target_ips": ["192.168.1.20"]}
```

- `berth_token` 是 Berth 签发给 Wharf 的令牌，Wharf 加密保存（§6 的 `outbound_token_enc`）。
- `target_ips`：配对时选定的、映射可以指向的 Berth 主机地址；Berth 默认用第一个。
- `expires_at`：Unix 秒，0 表示永久。
- 码无效、过期或已用过：`403 {"error": "配对码无效或已用过"}`。

配对页地址里的 `cb` 是 `https://<berth>/wharf/paired`，Wharf 同意后让浏览器跳到 `cb?code=<同一个一次性码>`。

### 10.2 证书指纹

- 指纹 = 证书 DER 的 SHA-256，小写十六进制，64 个字符。
- 校验规则：对方出示的证书链里**任意一张**与记录的指纹相同即通过（这样叶子证书续期后不用重新配对）。
- Berth 提供的 `berth_fp` 是 Berth 本地 CA 的指纹；Berth 在配对前让管理员从 Wharf 出示的证书链里选一张固定（建议 CA）。

### 10.3 解除配对

```
DELETE /api/ext/berth/v1/pair?keep=true|false   Berth 发起：Wharf 作废两边令牌；keep=false 时删除 Kind=berth 的映射和 SNI 登记，true 时转为普通映射
DELETE /api/ext/wharf/v1/pair                   Wharf 发起：Berth 删除对接记录（Berth 建的映射由 Wharf 自行按用户选择处理）
```

### 10.4 Berth 提供给 Wharf 的接口

挂在 Berth 的 `/api/ext/wharf/v1/`。鉴权与 §3.3 对称：

- `Authorization: Bearer berth_wrf_...`；
- 签名头前缀为 `X-Wharf`：`X-Wharf-Ts`、`X-Wharf-Nonce`、`X-Wharf-Sig`；
- 签名密钥同样由令牌派生（`HMAC-SHA256(token, "wharf-berth-sign-v1")`），算法与 §3.3 相同；
- 时间窗 ±60 秒，nonce 去重。

| 方法 路径 | 说明 |
|---|---|
| `POST /bans` | 同步封禁到 Berth：`{"cidr","reason","source","expires_at"}`（`expires_at` 为 RFC 3339，空表示永久）。返回 `{"id"}`。Berth 拒绝私有地址、白名单地址，网段最大 IPv4 /16、IPv6 /32；在 Berth 管理的所有主机上生效 |
| `DELETE /bans/{id}` | 撤回 |
| `POST /alerts` | 告警转交（Berth 为主通知方时）：`{"level","title","body","link","dedupe_key"}`；同一 `dedupe_key` 10 分钟内只发一次。Berth 不是主通知方时返回 409 |
| `POST /sso/ticket` | `{"wharf_user": "<Wharf 用户名>", "return_to": "/local/sites"}` → `{"url": "https://<berth>/sso/wharf?t=<票据>"}`；票据 60 秒、一次性；没有用户对应返回 403 |
| `GET /host` | 设备页「Berth」标签页的数据：`{"name","containers":[{"name","image","state"}],"sites":[{"domains","enabled"}],"vms":[]}` |
| `DELETE /pair` | 见 10.3 |

出错一律返回 `{"error": "中文说明"}`，与 Wharf 的约定一致。

### 10.5 Berth 调 Wharf 时的字段

- `POST /sso/ticket` 请求体：`{"berth_user": "<Berth 用户名>", "return_to": "/forwards"}`，返回 `{"url"}`。
- `POST /bans` 返回 `{"id"}`；`source` 形如 `berth:<面板名>:<bruteforce|waf|manual>`；单个地址以 `/32` 或 `/128` 发送。
- `POST /forwards` 返回新建的映射（含 `id`）；`?plan=1` 返回任意 JSON 预览（Berth 原样显示在改动预览里）；端口冲突返回 409，`error` 写明被谁占用。
- `PUT /sni` 在 443 未被占用时返回 409 `{"error": "443 未被占用，请改用端口映射"}`。
- `X-Berth-Actor` 的值做了 URL 编码（可能含中文），Wharf 读取时请先解码。
- 所有请求出现 401 时，Berth 把对接标为「已失效」并提示重新配对。
