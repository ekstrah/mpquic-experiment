#!/usr/bin/env bash
# Client-side path setup: aliases two path IPs onto this machine's
# interface and applies LTE-like / LEO-like uplink shaping via netem.sh.
# Edit IFACE/PATH0_IP/PATH1_IP below to match your machine, then:
#   sudo ./tools/netem-client.sh up      # alias IPs + apply shaping
#   sudo ./tools/netem-client.sh down    # remove shaping + aliases
#
# After 'up', dial the client with:
#   ./bin/client -server <server-ip>:4433 -local <PATH0_IP>,<PATH1_IP> -continuous ...
set -euo pipefail

IFACE=enp131s0           # direct Ethernet link to the server, not WiFi
PREFIX=24
PATH0_IP=10.10.10.11     # LTE-like path
PATH1_IP=10.10.10.12     # LEO-like path

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
netem="$script_dir/netem.sh"

action=${1:-}
case "$action" in
  up)
    ip addr show dev "$IFACE" | grep -q "$PATH0_IP" || \
      ip addr add "$PATH0_IP/$PREFIX" dev "$IFACE" label "$IFACE:path0"
    ip addr show dev "$IFACE" | grep -q "$PATH1_IP" || \
      ip addr add "$PATH1_IP/$PREFIX" dev "$IFACE" label "$IFACE:path1"
    "$netem" setup "$IFACE" "$PATH0_IP" src lte uplink
    "$netem" setup "$IFACE" "$PATH1_IP" src leo uplink
    echo "client paths ready -- dial with: -local $PATH0_IP,$PATH1_IP"
    ;;
  down)
    "$netem" clear "$IFACE"
    ip addr del "$PATH0_IP/$PREFIX" dev "$IFACE" 2>/dev/null || true
    ip addr del "$PATH1_IP/$PREFIX" dev "$IFACE" 2>/dev/null || true
    echo "client paths cleared"
    ;;
  *)
    echo "usage: $0 up|down" >&2
    exit 1
    ;;
esac
