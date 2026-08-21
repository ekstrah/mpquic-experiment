#!/usr/bin/env bash
# Client-side path setup for the flight-measured scenario (aerial mesh /
# private 5G / LEO satellite, Baltaci et al. 2026, arXiv 2604.27640) --
# aliases three path IPs onto this machine's interface and applies
# mesh-like / private-5G-like / satellite-like uplink shaping via
# netem.sh. Counterpart to netem-client.sh, which covers the older
# trace-emulated LTE/LEO scenario (Baltaci et al. 2023) instead -- run
# one or the other, not both, on a given interface.
# Edit IFACE/PATH0_IP/PATH1_IP/PATH2_IP below to match your machine, then:
#   ./tools/experiment/netem-client-flight.sh up      # alias IPs + apply shaping
#   ./tools/experiment/netem-client-flight.sh down    # remove shaping + aliases
# (self-elevates via sudo if not already root)
#
# After 'up', dial the client with:
#   ./bin/client -server <server-ip>:4433 -local <PATH0_IP>,<PATH1_IP>,<PATH2_IP> -continuous ...
set -euo pipefail

# Self-elevate: ip/tc need root. Lets callers skip typing sudo themselves.
# Resolved to an absolute path first -- sudo looks up a bare "$0" (e.g.
# when invoked as "bash netem-client-flight.sh") in PATH, not the current directory.
[ "$(id -u)" -eq 0 ] || exec sudo "$(cd "$(dirname "$0")" && pwd)/$(basename "$0")" "$@"

IFACE=enp131s0           # direct Ethernet link to the server, not WiFi
PREFIX=24
# Different last octets than netem-client.sh's 11/12 -- classid is
# derived from the IP's last octet (see netem.sh), so distinct octets
# keep the two scenarios' tc classes from colliding if ever run together.
PATH0_IP=10.10.10.21     # aerial-mesh-like path
PATH1_IP=10.10.10.22     # private-5G-like path
PATH2_IP=10.10.10.23     # satellite-like path

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
netem="$script_dir/netem.sh"

action=${1:-}
case "$action" in
  up)
    ip addr show dev "$IFACE" | grep -q "$PATH0_IP" || \
      ip addr add "$PATH0_IP/$PREFIX" dev "$IFACE" label "$IFACE:path0"
    ip addr show dev "$IFACE" | grep -q "$PATH1_IP" || \
      ip addr add "$PATH1_IP/$PREFIX" dev "$IFACE" label "$IFACE:path1"
    ip addr show dev "$IFACE" | grep -q "$PATH2_IP" || \
      ip addr add "$PATH2_IP/$PREFIX" dev "$IFACE" label "$IFACE:path2"
    "$netem" setup "$IFACE" "$PATH0_IP" src mesh uplink
    "$netem" setup "$IFACE" "$PATH1_IP" src p5g uplink
    "$netem" setup "$IFACE" "$PATH2_IP" src sat uplink
    echo "client paths ready -- dial with: -local $PATH0_IP,$PATH1_IP,$PATH2_IP"
    ;;
  down)
    "$netem" clear "$IFACE"
    ip addr del "$PATH0_IP/$PREFIX" dev "$IFACE" 2>/dev/null || true
    ip addr del "$PATH1_IP/$PREFIX" dev "$IFACE" 2>/dev/null || true
    ip addr del "$PATH2_IP/$PREFIX" dev "$IFACE" 2>/dev/null || true
    echo "client paths cleared"
    ;;
  *)
    echo "usage: $0 up|down" >&2
    exit 1
    ;;
esac
