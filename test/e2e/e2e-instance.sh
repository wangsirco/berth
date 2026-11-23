#!/bin/sh
# Start a fresh, throwaway panel for end-to-end tests on the dev server (port 7092, data
# wiped every run), sharing the real agent. Prints the setup code.
set -eu
HOST=${BERTH_DEV_HOST:-root@192.0.2.10}
cd "$(dirname "$0")/../.."
scp -q bin/linux-arm64/berth-panel "$HOST":/tmp/berth-panel-e2e
ssh "$HOST" 'set -e
  systemctl stop berth-panel-e2e 2>/dev/null || true
  systemctl reset-failed berth-panel-e2e 2>/dev/null || true
  install -m 0755 /tmp/berth-panel-e2e /usr/local/bin/berth-panel-e2e
  rm -rf /var/lib/berth-e2e && install -d -o berth -g berth -m 0750 /var/lib/berth-e2e
  install -d -o root -g berth -m 0750 /etc/berth-e2e
  cp /etc/berth/panel-agent.key /etc/berth-e2e/ && chgrp berth /etc/berth-e2e/panel-agent.key && chmod 0640 /etc/berth-e2e/panel-agent.key
  printf "{\"listen\":\":7092\",\"data_dir\":\"/var/lib/berth-e2e\",\"no_edge_entry\":true}\n" > /etc/berth-e2e/panel.json
  systemd-run --unit berth-panel-e2e --uid berth --gid berth -p MemoryMax=128M /usr/local/bin/berth-panel-e2e -etc /etc/berth-e2e >/dev/null
  for i in $(seq 1 20); do [ -s /var/lib/berth-e2e/setup-url ] && break; sleep 0.5; done
  head -n1 /var/lib/berth-e2e/setup-url | sed "s#.*/setup/##"'
