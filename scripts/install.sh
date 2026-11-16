#!/bin/sh
# Berth 一键安装 / one-line installer
#
#   curl -fsSL https://raw.githubusercontent.com/wangsirco/berth/main/scripts/install.sh | sudo sh
#
# 自动完成：检查系统 → 安装依赖（Docker、nftables、tc、smartmontools、acl…）→ 下载并校验
# 安装包 → 创建用户和目录 → 安装并启动服务 → 放行面板端口（仅内网）→ 打印初始化链接。
# 已经装过时就地升级，数据保留。
# Does it all: checks the system, installs what is missing (Docker, nftables, tc, smartmontools,
# acl…), downloads and verifies the package, creates the user and folders, installs and starts
# the services, opens the panel port to private networks only and prints the setup link.
# Run it again to upgrade in place; your data stays.
#
# 选项 / options:
#   --ask           每一步先问 / ask before each step
#   --local DIR     从解压好的安装包安装 / install from an unpacked release
#   --version V     安装指定版本 / install version V (default: latest)
#   --no-start      只装文件不启动 / install files only
#
# 不想用 curl | sh？/ Rather not pipe to sh?
#   1. 在 Releases 页面下载 berth_<版本>_linux_arm64.tar.gz 和 SHA256SUMS
#      download berth_<version>_linux_arm64.tar.gz and SHA256SUMS from the Releases page
#   2. sha256sum -c --ignore-missing SHA256SUMS && tar xzf berth_*_linux_arm64.tar.gz && cd berth_*
#   3. sudo sh install.sh --local .
set -eu

BASE_URL=${BERTH_URL:-https://github.com/wangsirco/berth/releases}
YES=1 LOCAL=${BERTH_LOCAL:-} VERSION=latest START=1 SRC=""
while [ $# -gt 0 ]; do
  case "$1" in
    --yes|-y) YES=1 ;;
    --ask) YES=0 ;;
    --local) LOCAL=$2; shift ;;
    --version) VERSION=$2; shift ;;
    --no-start) START=0 ;;
    *) echo "未知选项 / unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

# every message twice: Chinese, then English
say()  { printf '\033[1;32m==>\033[0m %s\n    %s\n' "$1" "$2"; }
warn() { printf '\033[33m!!\033[0m %s\n   %s\n' "$1" "$2" >&2; }
die()  { printf '\033[31mxx\033[0m %s\n   %s\n' "$1" "$2" >&2; exit 1; }
ask() { # ask "中文" "English" -> 0 for yes
  [ "$YES" = 1 ] && return 0
  [ -r /dev/tty ] || return 0
  printf '%s / %s [Y/n] ' "$1" "$2"
  { read -r a </dev/tty; } 2>/dev/null || a=y
  case "$a" in ''|y|Y|yes) return 0 ;; *) return 1 ;; esac
}

printf '\n\033[1m  Berth — Linux 服务器面板 / a panel for your Linux server\033[0m\n\n'

[ "$(id -u)" = 0 ] || die "请用 root 运行：curl -fsSL … | sudo sh" "Please run as root: curl -fsSL … | sudo sh"

case "$(uname -m)" in
  aarch64|arm64) ARCH=arm64 ;;
  x86_64|amd64)
    die "目前只支持 ARM（arm64）架构。x86 版本正在开发中，敬请期待。" \
        "Berth supports ARM (arm64) for now. x86 is on the way — stay tuned." ;;
  *) die "不支持的架构：$(uname -m)" "Unsupported architecture: $(uname -m)" ;;
esac
[ -r /etc/os-release ] || die "读不到 /etc/os-release" "Cannot read /etc/os-release"
# shellcheck disable=SC1091
. /etc/os-release
# Asahi reports ID=fedora-asahi-remix, so ID_LIKE decides too
case " ${ID:-} ${ID_LIKE:-} " in
  *" fedora "*|*" rhel "*|*" rocky "*|*" almalinux "*|*" centos "*|*fedora*) PM=dnf ;;
  *" debian "*|*" ubuntu "*|*" raspbian "*) PM=apt ;;
  *) die "暂不支持这个系统：${ID:-?}（支持 Fedora / Asahi、Debian、Ubuntu、Rocky、Alma）" \
         "Unsupported system '${ID:-?}' (Fedora / Asahi, Debian, Ubuntu, Rocky, Alma)" ;;
esac
DOCKER_DISTRO=fedora
case " ${ID:-} " in *" rhel "*|*" rocky "*|*" almalinux "*|*" centos "*) DOCKER_DISTRO=rhel ;; esac
command -v systemctl >/dev/null 2>&1 || [ "$START" = 0 ] || die "需要 systemd" "systemd is required"
UPGRADE=0
[ -x /usr/local/bin/berth-panel ] && [ -d /var/lib/berth ] && UPGRADE=1
say "系统：${PRETTY_NAME:-$ID}（$ARCH）$( [ $UPGRADE = 1 ] && echo '，已安装，将就地升级（数据保留）')" \
    "System: ${PRETTY_NAME:-$ID} ($ARCH)$( [ $UPGRADE = 1 ] && echo ', already installed: upgrading in place (data kept)')"
case " ${ID:-} " in *asahi*) say "检测到 Asahi Linux：Berth 目前就是在 Apple M 系列芯片 + Fedora Asahi Remix 上开发和优化的。" \
  "Asahi Linux detected: Berth is developed and tuned on Apple M-series chips with Fedora Asahi Remix." ;; esac

# ---- dependencies ----
installed_by_berth=""
pkg() { # pkg <dnf name> <apt name>
  if [ "$PM" = dnf ]; then dnf install -y -q "$1" >/dev/null 2>&1; else DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$2" >/dev/null 2>&1; fi && installed_by_berth="$installed_by_berth $1"
}
[ "$PM" = apt ] && apt-get update -qq >/dev/null 2>&1 || true
for tool in curl:curl:curl tar:tar:tar sha256sum:coreutils:coreutils; do
  c=${tool%%:*}; rest=${tool#*:}
  command -v "$c" >/dev/null 2>&1 || pkg "${rest%%:*}" "${rest#*:}"
done
command -v update-ca-trust >/dev/null 2>&1 || command -v update-ca-certificates >/dev/null 2>&1 || pkg ca-certificates ca-certificates

if ! { command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; }; then
  if command -v podman >/dev/null 2>&1 && rpm -q podman-docker >/dev/null 2>&1; then
    warn "装着 podman-docker，Berth 需要 Docker CE。请先执行：dnf remove podman-docker" \
         "podman-docker is installed; Berth needs Docker CE. Remove it first: dnf remove podman-docker"
  elif ask "安装 Docker（官方源）？应用商店和容器功能需要它" "Install Docker CE from Docker's repository? Apps and containers need it"; then
    say "正在安装 Docker…" "Installing Docker…"
    if [ "$PM" = dnf ]; then
      dnf -y -q install dnf-plugins-core >/dev/null 2>&1
      dnf config-manager addrepo --overwrite --from-repofile="https://download.docker.com/linux/$DOCKER_DISTRO/docker-ce.repo" >/dev/null 2>&1 \
        || dnf config-manager --add-repo "https://download.docker.com/linux/$DOCKER_DISTRO/docker-ce.repo" >/dev/null 2>&1
      dnf -y -q install docker-ce docker-ce-cli containerd.io docker-compose-plugin docker-buildx-plugin >/dev/null
    else
      apt-get install -y -qq ca-certificates curl gnupg >/dev/null
      install -m 0755 -d /etc/apt/keyrings
      curl -fsSL "https://download.docker.com/linux/$ID/gpg" -o /etc/apt/keyrings/docker.asc
      echo "deb [signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$ID ${VERSION_CODENAME:-stable} stable" > /etc/apt/sources.list.d/docker.list
      apt-get update -qq && apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin docker-buildx-plugin >/dev/null
    fi
    systemctl enable --now docker >/dev/null 2>&1
    installed_by_berth="$installed_by_berth docker-ce docker-compose-plugin"
  else
    warn "先不装 Docker：容器相关功能会在装好 Docker 后自动可用" "Continuing without Docker: container features turn on once it is installed"
  fi
fi
# what the panel uses here and there: firewall rules, traffic limits, disk health, shared folders
for t in nft:nftables:nftables tc:iproute-tc:iproute2 smartctl:smartmontools:smartmontools setfacl:acl:acl; do
  c=${t%%:*}; rest=${t#*:}
  command -v "$c" >/dev/null 2>&1 && continue
  ask "安装 ${rest%%:*}？" "Install ${rest%%:*}?" && { say "正在安装 ${rest%%:*}" "Installing ${rest%%:*}"; pkg "${rest%%:*}" "${rest#*:}" || warn "${rest%%:*} 没装上，可以以后在面板里装" "${rest%%:*} did not install; you can add it later in the panel"; }
done

# ---- 16K memory pages (Apple silicon / Asahi): 4K compatibility layer for a few images ----
PAGE=$(getconf PAGESIZE 2>/dev/null || echo 4096)
if [ "$PAGE" -gt 4096 ] && [ -e /dev/kvm ] && [ "$PM" = dnf ] && command -v docker >/dev/null 2>&1 && ! command -v krun >/dev/null 2>&1; then
  say "这台机器用 $((PAGE / 1024))K 内存页，少数容器镜像只认 4K 会崩溃。" "This machine uses $((PAGE / 1024))K memory pages; a few container images expect 4K and crash."
  if ask "安装 4K 兼容层（libkrun：这类容器放进带 4K 内存页的小虚拟机里运行）？" "Install the 4K compatibility layer (libkrun runs such containers in a small 4K VM)?"; then
    if dnf -y -q install crun-krun libkrun >/dev/null 2>&1; then
      installed_by_berth="$installed_by_berth crun-krun libkrun"
      python3 - <<'PYEOF' 2>/dev/null || warn "没能写入 /etc/docker/daemon.json，可以以后在面板里开启" "Could not update /etc/docker/daemon.json; enable it later in the panel"
import json, os
p = "/etc/docker/daemon.json"
cfg = json.load(open(p)) if os.path.exists(p) and open(p).read().strip() else {}
cfg.setdefault("runtimes", {})["krun"] = {"path": "/usr/bin/krun"}
os.makedirs("/etc/docker", exist_ok=True)
json.dump(cfg, open(p, "w"), indent=2)
PYEOF
      systemctl reload docker 2>/dev/null || true
    else
      warn "libkrun 没装上，可以以后在面板里装" "libkrun did not install; you can add it later in the panel"
    fi
  fi
fi

# ---- the package ----
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
if [ -n "$LOCAL" ]; then
  if [ -f "$LOCAL/berth" ]; then SRC=$LOCAL
  else
    tb=""
    for f in "$LOCAL"/berth_*_linux_"$ARCH".tar.gz; do [ -f "$f" ] && tb=$f; done
    [ -n "$tb" ] || die "$LOCAL 里没有 berth_*_linux_$ARCH.tar.gz" "No berth_*_linux_$ARCH.tar.gz in $LOCAL"
    tar -C "$WORK" -xzf "$tb"
    for d in "$WORK"/berth_*; do [ -d "$d" ] && SRC=$d && break; done
  fi
else
  if [ "$VERSION" = latest ]; then url="$BASE_URL/latest/download"; else url="$BASE_URL/download/v$VERSION"; fi
  say "正在下载 Berth（$VERSION）" "Downloading Berth ($VERSION)"
  curl -fsSL "$url/berth-manifest.json" -o "$WORK/manifest.json" || die "下载不到版本清单：$url" "Cannot download the release manifest from $url"
  name=$(sed -n "s/.*\"name\": *\"\(berth_[^\"]*_linux_$ARCH\.tar\.gz\)\".*/\1/p" "$WORK/manifest.json" | head -n1)
  sum=$(grep -A2 "\"$name\"" "$WORK/manifest.json" | sed -n 's/.*"sha256": *"\([0-9a-f]*\)".*/\1/p' | head -n1)
  [ -n "$name" ] && [ -n "$sum" ] || die "这个版本没有 $ARCH 的安装包" "The release has no package for $ARCH"
  curl -fsSL "$url/$name" -o "$WORK/$name" || die "下载失败：$name" "Download failed: $name"
  echo "$sum  $WORK/$name" | sha256sum -c - >/dev/null 2>&1 || die "安装包校验不通过（SHA-256 不一致），已停止" "Checksum mismatch for $name; stopped"
  tar -C "$WORK" -xzf "$WORK/$name"
  for d in "$WORK"/berth_*; do [ -d "$d" ] && SRC=$d && break; done
fi
[ -x "$SRC/berth" ] && [ -x "$SRC/berth-panel" ] || die "安装包不完整" "The package is incomplete"
NEW=$("$SRC/berth-panel" -version 2>/dev/null | awk '{print $2}')

say "创建 berth 用户和目录" "Creating the berth user and folders"
getent group berth >/dev/null || groupadd --system berth
id berth >/dev/null 2>&1 || useradd --system --gid berth --shell /usr/sbin/nologin --home-dir /var/lib/berth berth 2>/dev/null \
  || useradd --system --gid berth --shell /sbin/nologin --home-dir /var/lib/berth berth
install -d -o root -g berth -m 0750 /etc/berth
install -d -o berth -g berth -m 0750 /var/lib/berth
install -d -m 0755 /srv/berth /srv/berth/stacks /srv/berth/sites /srv/berth/caddy /srv/berth/workspaces
install -d -m 0700 /srv/berth/backups

say "安装程序和服务（${NEW:-?}）" "Installing the program and services (${NEW:-?})"
install -m 0755 "$SRC/berth" "$SRC/berth-panel" /usr/local/bin/
install -d -m 0755 /etc/systemd/system
install -m 0644 "$SRC/berth-agent.service" "$SRC/berth-panel.service" /etc/systemd/system/
if [ -n "$installed_by_berth" ]; then echo "$installed_by_berth" | tr ' ' '\n' | sed '/^$/d' >> /etc/berth-installed-packages; fi

if [ "$START" = 0 ]; then
  say "已安装（未启动）。启动：systemctl enable --now berth-agent berth-panel" "Installed, not started. Start with: systemctl enable --now berth-agent berth-panel"
  exit 0
fi
systemctl daemon-reload
systemctl enable berth-agent berth-panel >/dev/null 2>&1
systemctl restart berth-agent
sleep 1
systemctl restart berth-panel

# ---- firewall: only private networks may reach the panel ----
PORT=7090
if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
  if ! firewall-cmd --list-rich-rules 2>/dev/null | grep -q "port=\"$PORT\""; then
    if ask "在防火墙里只对内网放行面板端口 $PORT（局域网、Tailscale）？" "Open port $PORT to private networks only (LAN, Tailscale)?"; then
      for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10; do
        firewall-cmd --permanent --add-rich-rule="rule family=ipv4 source address=$net port port=$PORT protocol=tcp accept" >/dev/null
      done
      firewall-cmd --reload >/dev/null
      say "已放行：只有内网能打开面板" "Opened: only private networks can reach the panel"
    fi
  fi
elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q active; then
  for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10; do ufw allow from "$net" to any port "$PORT" proto tcp >/dev/null; done
  say "已在 ufw 放行内网访问 $PORT" "ufw: port $PORT opened to private networks"
fi

say "等待面板启动…" "Waiting for the panel…"
i=0
while [ $i -lt 40 ]; do
  [ -s /var/lib/berth/setup-url ] && break
  curl -fsk "https://127.0.0.1:$PORT/api/v1/meta" >/dev/null 2>&1 && [ $UPGRADE = 1 ] && break
  sleep 1; i=$((i + 1))
done
systemctl is-active --quiet berth-panel || die "面板没有启动，看日志：journalctl -u berth-panel -n 50" "The panel did not start; see: journalctl -u berth-panel -n 50"

IP=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n1)
echo
printf '\033[1;32m  ✓ Berth %s %s\033[0m\n\n' "${NEW:-}" "$( [ $UPGRADE = 1 ] && echo '已升级 / upgraded' || echo '已安装 / installed')"
if [ -s /var/lib/berth/setup-url ]; then
  echo "  10 分钟内打开下面任意一个地址，创建超级管理员："
  echo "  Open one of these within 10 minutes to create the super admin:"
  sed 's/^/    /' /var/lib/berth/setup-url
else
  echo "  面板地址 / Panel: https://${IP:-<这台机器的 IP / this machine>}:$PORT（如设置了安全入口，请在后面加上 / add your security entry if set）"
fi
fp=$(journalctl -u berth-panel -n 80 --no-pager 2>/dev/null | sed -n 's/.*(CA \([0-9a-f]*\)).*/\1/p' | tail -n1)
[ -n "$fp" ] && echo "  证书指纹（浏览器提示不安全时核对）/ Certificate fingerprint: $fp"
echo
echo "  忘了安全入口或密码：sudo berth admin      Forgot the entry or password: sudo berth admin"
echo "  卸载（网站和容器继续运行）：sudo berth uninstall      Uninstall (sites keep running): sudo berth uninstall"
echo
