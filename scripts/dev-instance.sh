#!/bin/sh
# A throwaway second panel on the dev server (port 7091, data in /var/lib/berth-dev) that
# shares the real agent. Used for UI checks without touching the real panel's accounts.
set -eu
HOST=${BERTH_DEV_HOST:-root@192.0.2.10}
scp -q bin/linux-arm64/berth-panel "$HOST":/tmp/berth-panel-dev
ssh "$HOST" 'set -e
  install -m 0755 /tmp/berth-panel-dev /usr/local/bin/berth-panel-dev.new && mv -f /usr/local/bin/berth-panel-dev.new /usr/local/bin/berth-panel-dev
  install -d -o root -g berth -m 0750 /etc/berth-dev
  install -d -o berth -g berth -m 0750 /var/lib/berth-dev
  cp /etc/berth/panel-agent.key /etc/berth-dev/ && chgrp berth /etc/berth-dev/panel-agent.key && chmod 0640 /etc/berth-dev/panel-agent.key
  printf "{\"listen\":\":7091\",\"data_dir\":\"/var/lib/berth-dev\"}\n" > /etc/berth-dev/panel.json
  systemctl stop berth-panel-dev 2>/dev/null || true
  systemctl reset-failed berth-panel-dev 2>/dev/null || true
  systemd-run --unit berth-panel-dev --uid berth --gid berth -p MemoryMax=128M /usr/local/bin/berth-panel-dev -etc /etc/berth-dev >/dev/null
  sleep 2; systemctl is-active berth-panel-dev; cat /var/lib/berth-dev/setup-url 2>/dev/null | head -1 || true'
