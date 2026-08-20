#!/usr/bin/env bash
# Server-side path setup: applies LTE-like / LEO-like downlink shaping,
# classified by destination IP -- the server listens on one address for
# every path (see -listen in cmd/server), so what varies per path is
# which client path IP it's replying to, not its own source (see the
# src/dst explanation at the top of netem.sh).
# Edit IFACE/SERVER_IP/PATH0_IP/PATH1_IP below to match, then:
#   ./tools/experiment/netem-server.sh up
#   ./tools/experiment/netem-server.sh down
# (self-elevates via sudo if not already root)
#
# After 'up', point the client at: -server <SERVER_IP>:4433
set -euo pipefail

# Self-elevate: ip/tc need root. Lets callers skip typing sudo themselves.
# Resolved to an absolute path first -- sudo looks up a bare "$0" (e.g.
# when invoked as "bash netem-server.sh") in PATH, not the current directory.
[ "$(id -u)" -eq 0 ] || exec sudo "$(cd "$(dirname "$0")" && pwd)/$(basename "$0")" "$@"

IFACE=enp131s0           # direct Ethernet link to the client, not WiFi
PREFIX=24
SERVER_IP=10.10.10.1     # this machine's address on the link
PATH0_IP=10.10.10.11     # must match the client's path0 alias (netem-client.sh)
PATH1_IP=10.10.10.12     # must match the client's path1 alias (netem-client.sh)

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
netem="$script_dir/netem.sh"

action=${1:-}
case "$action" in
  up)
    ip addr show dev "$IFACE" | grep -q "$SERVER_IP" || \
      ip addr add "$SERVER_IP/$PREFIX" dev "$IFACE"
    "$netem" setup "$IFACE" "$PATH0_IP" dst lte downlink
    "$netem" setup "$IFACE" "$PATH1_IP" dst leo downlink
    echo "server shaping ready -- listen with: -listen $SERVER_IP:4433"
    ;;
  down)
    "$netem" clear "$IFACE"
    ip addr del "$SERVER_IP/$PREFIX" dev "$IFACE" 2>/dev/null || true
    echo "server shaping cleared"
    ;;
  *)
    echo "usage: $0 up|down" >&2
    exit 1
    ;;
esac
