#!/bin/sh
# Phase 1 acceptance (方案 §1.3): panel + agent idle memory ≤ 100 MB.
# Counts anonymous + kernel memory of both services' cgroups (page cache is reclaimable
# and not "used" by Berth). Exits 1 when over budget.
set -eu
HOST=${BERTH_DEV_HOST:-root@192.0.2.10}
LIMIT_MB=${LIMIT_MB:-100}
ssh "$HOST" 'for s in berth-panel berth-agent; do
  d=/sys/fs/cgroup/system.slice/$s.service
  anon=$(awk "/^anon /{print \$2}" $d/memory.stat)
  kern=$(awk "/^kernel /{print \$2}" $d/memory.stat)
  echo "$s $((anon/1048576)) $((kern/1048576)) $(( $(cat $d/memory.current)/1048576 ))"
done' | awk -v limit="$LIMIT_MB" '
  { printf "%-12s anon=%dMB kernel=%dMB current=%dMB\n", $1, $2, $3, $4; t += $2 + $3 }
  END { printf "total (anon+kernel) = %dMB, budget %dMB: %s\n", t, limit, (t <= limit ? "OK" : "OVER"); exit (t > limit) }'
