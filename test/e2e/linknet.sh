#!/bin/sh
# panel-link e2e: a temporary NAT card on the fedora-1 VM, so the dev server and the VM
# (on the LAN by macvlan, which keeps host and guest apart) can reach each other.
#   linknet.sh up | down
set -u
case "${1:-}" in
up)
  incus config device get fedora-1 e2elink name >/dev/null 2>&1 || incus config device add fedora-1 e2elink nic network=incusbr0 name=eth9 >/dev/null
  sleep 3
  incus exec fedora-1 -- sh -s <<'EOS'
for i in $(seq 1 40); do
  echo 1 > /sys/bus/pci/rescan 2>/dev/null # a hot-added card may need a rescan to show up
  d=$(ip -o link | awk -F': ' '$2 ~ /^enp/ && $2 != "enp5s0" {print $2; exit}')
  [ -n "$d" ] && break
  sleep 1
done
ip link set "$d" up
ip addr add 10.42.231.249/24 dev "$d" 2>/dev/null
ip -4 -br addr show "$d"
EOS
  ;;
down)
  incus exec fedora-1 -- sh -c 'd=$(ip -o addr | awk "/10.42.231.249/ {print \$2; exit}"); [ -n "$d" ] && ip addr del 10.42.231.249/24 dev "$d"; true' </dev/null
  incus config device remove fedora-1 e2elink >/dev/null 2>&1
  true
  ;;
esac
