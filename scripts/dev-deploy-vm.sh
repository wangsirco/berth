#!/bin/sh
# Install the dev build into an Incus VM on the dev server (default: fedora-1), as dev-deploy.sh
# does for the server itself. Used for the second panel in the panel-link end-to-end test.
set -eu
HOST=${BERTH_DEV_HOST:-root@192.0.2.10}
VM=${1:-fedora-1}
ssh "$HOST" 'mkdir -p /tmp/vmdeploy'
scp -q bin/linux-arm64/berth bin/linux-arm64/berth-panel packaging/berth-agent.service packaging/berth-panel.service "$HOST":/tmp/vmdeploy/
[ -f bin/linux-arm64/devuser ] && scp -q bin/linux-arm64/devuser "$HOST":/tmp/vmdeploy/
ssh "$HOST" "cd /tmp/vmdeploy && for f in *; do incus file push \$f $VM/tmp/\$f >/dev/null </dev/null; done
  incus exec $VM -- sh -c 'set -e; install -m 0755 /tmp/berth /tmp/berth-panel /usr/local/bin/; [ -f /tmp/devuser ] && install -m 0755 /tmp/devuser /root/devuser; install -m 0644 /tmp/berth-agent.service /tmp/berth-panel.service /etc/systemd/system/; systemctl daemon-reload; systemctl restart berth-agent; sleep 1; systemctl restart berth-panel; sleep 2; systemctl is-active berth-agent berth-panel' </dev/null
  rm -rf /tmp/vmdeploy"
