#!/bin/sh
# Build for linux/arm64 and install on the dev server (default: the Mac mini).
set -eu
HOST=${BERTH_DEV_HOST:-root@192.0.2.10}
ARCH=${BERTH_DEV_ARCH:-arm64}
scp -q "bin/linux-$ARCH/berth" "bin/linux-$ARCH/berth-panel" packaging/*.service "$HOST":/tmp/
ssh "$HOST" 'set -e
  install -m 0755 /tmp/berth /tmp/berth-panel /usr/local/bin/
  install -m 0644 /tmp/berth-agent.service /tmp/berth-panel.service /etc/systemd/system/
  getent group berth >/dev/null || groupadd --system berth
  id berth >/dev/null 2>&1 || useradd --system --gid berth --shell /sbin/nologin --home-dir /var/lib/berth berth
  install -d -o root -g berth -m 0750 /etc/berth
  install -d -m 0755 /srv/berth /srv/berth/stacks /srv/berth/sites /srv/berth/backups /srv/berth/workspaces
  systemctl daemon-reload
  systemctl enable -q berth-agent berth-panel
  systemctl restart berth-agent; sleep 1; systemctl restart berth-panel; sleep 2
  systemctl --no-pager --lines=8 status berth-agent berth-panel | grep -E "Active|berth-panel\[|berth\[" || true
  cat /var/lib/berth/setup-url 2>/dev/null || true'
