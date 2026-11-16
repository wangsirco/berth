# 写 Berth 插件

这份文档说明 Berth 插件怎么写、怎么打包签名、怎么发布到插件市场，以及管理员安装时会看到什么。适用于插件接口版本 `api: 1`。

| 工具 | 说明 |
|---|---|
| `berth plugin init <目录>` | 生成可以直接打包的模板，`--native` 或 `--container` |
| `berth plugin check <目录>` | 检查清单和文件 |
| `berth plugin pack <目录> --key <钥匙>` | 打包成 `.berthplugin`，带 `--key` 时同时签名 |
| `berth plugin keygen <文件>` | 生成签名钥匙 |
| `berth plugin fingerprint <文件>` | 查看插件包或钥匙的指纹 |

示例（都在仓库的 `examples/plugins/` 里）：

- `iperf3`：网速测试。本地运行，以插件自己的用户运行；让 Berth 先装好 `iperf3` 软件包；在 5201 端口提供测速服务；页面上画出测速结果。
- `whoami-root`：最小的、以 root 运行的插件。

---

## 1. 两种运行方式

| | 容器沙盒（`container`） | 本地运行（`native`） |
|---|---|---|
| 跑在哪 | Docker 容器 | 宿主机上的 systemd 服务 |
| 隔离 | `no-new-privileges`、去掉所有特权（`cap_drop: ALL`）、内存上限、页面端口只绑本机 | 默认用插件自己的系统用户 `berth-p-<id>` 运行：系统文件只读，`/tmp` 独立，只能写自己的数据目录和申请到的目录 |
| 适合 | 网页工具、和 Berth 接口打交道的小应用 | 需要直接用系统工具或网络的东西：测速、抓包、诊断 |
| 交付 | 镜像名或 Dockerfile | 每个 CPU 架构一个可执行文件（`arm64` 必须有，`amd64` 可选） |

本地插件还可以申请**以 root 运行**（`run_as: root`）：

- 这样就没有任何沙盒限制，只保留内存和 CPU 上限。
- **以 root 运行的插件必须签名。** 安装时：
  - 整块红色警告；
  - 管理员要勾选「我完全信任这个插件的作者」，并输入插件 ID 确认。
- 签名钥匙变了的 root 插件，不能直接更新：要先卸载，确认是作者本人后再装。
- 能用插件用户解决的，就不要申请 root。

## 2. 快速开始

```bash
berth plugin init hello --native
cd hello && make                     # 编出 bin/hello-arm64、bin/hello-amd64
berth plugin keygen ~/berth-plugin.key
berth plugin pack . --key ~/berth-plugin.key
# → hello-0.1.0.berthplugin
```

在面板的「插件 → 手动安装」上传这个文件，就会进入安装向导。

## 3. 清单 `berth-plugin.yaml`

```yaml
api: 1                     # 接口版本
id: iperf3                 # 小写字母、数字、-，2–31 位，字母开头；装上后不能改
name: {zh-CN: 网速测试, zh-TW: 網速測試, en: Speed test}
version: 1.0.0             # 语义化版本
description: {zh-CN: …, en: …}
author: 你的名字
homepage: https://…        # 可选
icon: icon.svg             # 可选，包里的文件（svg/png）
screenshots: [shot1.png]   # 可选，最多显示 6 张

runtime:
  type: native             # native | container
  # native:
  binary: {arm64: bin/x-arm64, amd64: bin/x-amd64}
  args: [--flag]           # 可选
  run_as: plugin           # plugin（默认）| root
  page: true               # 在 $PORT 上提供页面
  # container:
  # image: ghcr.io/you/x:1.0.0   或   build: .   （包里带 Dockerfile 的目录）
  # port: 8080             # 容器里页面监听的端口
  mem_mb: 256              # 内存上限，默认 256
  cpus: 1                  # CPU 上限，默认 1

system:                    # 在这台服务器上需要什么（都会在安装时逐条列出）
  packages: [iperf3]       # Berth 先装好的系统软件包
  listen: [5201]           # 给别人连的端口
  firewall_open: true      # 这些端口需要对外开放（装完后面板提供一键放行）
  capabilities: [net_raw]  # 本地插件可申请：net_raw、net_admin、net_bind_service
  read_paths: [/var/log]   # 额外可读的目录
  write_paths: [/srv/x]    # 额外可写的目录（系统目录不允许）
  host_network: false      # 容器插件：直接用主机网络

permissions:               # Berth 接口权限，格式 资源:read|write
  - sites:read
events: [change.done]      # 可选，要接收的面板事件
webhook: /hooks/berth      # 有 events 时必填，事件以 POST {type, data} 送到这里

settings:                  # 安装向导和设置页都按这里生成表单
  - key: server_port       # 小写字母、数字、_
    label: {zh-CN: 服务端端口, en: Server port}
    hint: {zh-CN: …}
    type: number           # text | number | bool | select | secret
    default: "5201"
    options: []            # select 用
    required: false
```

可用的 Berth 资源：

- overview
- sites
- containers
- pipeline
- files
- terminal
- tasks
- audit
- users
- tokens
- settings
- system

可接收的事件：

- change.done
- alert
- uptime
- drift
- host.online
- host.offline
- backup.run

## 4. 插件运行时拿到什么

环境变量：

| 变量 | 内容 |
|---|---|
| `BERTH_URL` | 面板接口地址（本地插件是 `https://127.0.0.1:<端口>`，容器插件是 `https://host.docker.internal:<端口>`） |
| `BERTH_TOKEN` | 只带安装时授予的权限的令牌。用 `Authorization: Bearer …` 调接口；审计记录里显示成这个插件 |
| `BERTH_CA_FILE` | 面板证书的 CA，访问 `BERTH_URL` 时用它校验 |
| `BERTH_PLUGIN`、`BERTH_PLUGIN_VERSION` | 插件 ID 和版本 |
| `BERTH_DATA` | 可写的数据目录（本地：`/var/lib/berth-plugins/<id>/data`；容器：`/data`） |
| `PORT` | 本地插件和用主机网络的容器插件：页面要监听的端口，**只监听 127.0.0.1** |
| `BERTH_SETTING_<KEY>` | 每个设置项，键名转大写 |

- 页面通过 `/ext/<id>/<一次性钥匙>/` 显示在面板里，外面包一层 `sandbox` 的 iframe。
  - 页面拿不到看页面的人的登录状态。
  - 请求自己页面下的路径用相对地址（`fetch('run')`），不要写死 `/`。
- 面板会用 `postMessage` 告诉页面当前的主题、语言和主机：
  - 消息格式：`{type: 'berth.context', theme, locale, host}`。
  - 按它调整显示，看起来就和面板是一体的。
- 卸载后再装，数据目录还在：本地插件可以选「卸载但保留数据」；容器插件的数据在它的编排目录里。

## 5. 签名

```bash
berth plugin keygen ~/berth-plugin.key     # 只做一次，钥匙自己保管、备份
berth plugin pack . --key ~/berth-plugin.key
berth plugin fingerprint ~/berth-plugin.key # 把指纹公布在你的主页上
```

- **签了名：** 管理员安装时看到「开发者签了名」和你的指纹。
- **钥匙固定：** Berth 记住每个插件 ID 第一次见到的钥匙。以后的版本换了钥匙或没签名，会标红提醒；以 root 运行的插件直接拒绝。
- **签名的范围：** 签名覆盖包里的每个文件（文件名和 SHA-256）。改了任何文件，签名就对不上了。
- **签名不代表安全：** 签名只说明“是同一个人发布的”，不说明插件安全，沙盒和权限照样管着。

## 6. 发布到插件市场

插件市场是一个 JSON 索引：

- 默认地址：`https://raw.githubusercontent.com/wangsirco/berth-plugins/main/index.json`
- 在「设置」里可以用 `plugins.index` 改成自己的地址，比如公司内部的。

格式见 `examples/plugins/index.example.json`：

```json
{"plugins": [{
  "id": "iperf3", "version": "1.0.0", "author": "Berth",
  "name": {"zh-CN": "网速测试（iperf3）", "en": "Speed test (iperf3)"},
  "description": {"zh-CN": "…", "en": "…"},
  "runtime": "native", "run_as": "plugin",
  "url": "https://github.com/…/iperf3-1.0.0.berthplugin",
  "sha256": "<berth plugin pack 输出的 sha256>",
  "signer": "<可选，签名指纹>",
  "icon": "https://…/icon.svg", "homepage": "https://…"
}]}
```

- 面板下载插件包后，会核对 `sha256`。对不上就拒绝。
- 索引里写的只用于展示；装之前，面板仍然以插件包本身的清单和签名为准。

## 7. 管理员安装时看到什么

安装向导分四步：

1. **介绍：** 名称、作者、版本、描述、截图、运行方式，以及签名状态：
   - 已签名，显示指纹；
   - 未签名，标黄；
   - 签名不对或钥匙变了，标红。
2. **权限：**
   - **它能在 Berth 里做什么：** 每一项都能取消勾选，没给的权限调用时会被拒绝。
   - **它在这台服务器上需要什么：** 逐条用人话列出：运行身份、要装的软件包、端口、能力、可读写的目录、事件。

   要勾选同意后才能继续。以 root 运行的插件，这一步是红色的。
3. **设置：** 按 `settings` 生成的表单。
4. **确认：** 和 Berth 里所有改动一样，先预览：要下载什么、创建哪个用户和目录、写哪个 systemd 单元、开哪些端口。确认后执行，记入审计，卸载就是撤销。

**更新：** 插件市场有新版本时，「已安装」里会出现「更新」，同样走这四步。新版本多要的权限，会标「这次新增」。

## 8. 资源和限制

- **插件包：** 不超过 256 MB。
- **默认资源上限：** 内存 256 MB，CPU 1 核，清单里可以改。
- **本地插件：**
  - 不能写这些系统目录：`/`、`/etc`、`/usr`、`/boot`、`/var/lib/berth`。
  - 不能读写 Berth 自己的数据。
- **容器插件：** 去掉了所有 Linux 特权。需要特权的工具，请改做本地插件，并申请对应的能力。

## 9. 交给管理员之前

- [ ] `berth plugin check` 通过；
- [ ] 两个架构都编过（至少 `arm64`）；
- [ ] 只申请真正用到的权限；能用插件用户的，不要用 root；
- [ ] 页面只监听 `127.0.0.1:$PORT`；
- [ ] 签了名，指纹公布在你的主页上；
- [ ] 描述写清楚它会对服务器做什么。

## 10. 关于压力测试、发包类插件

这类插件只能对这个面板管理的主机，或管理员确认归属的地址做测试。请在插件里限制目标，并在描述里写明，否则不会被收进官方插件市场。
