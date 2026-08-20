#!/usr/bin/env bash
# tc/netem link emulation for LTE-like and LEO-like paths, parameterized
# from docs/link-characteristics.md (Baltaci et al. 2023 measurements).
# Linux only (iproute2 tc). Run on BOTH client and server hosts -- each
# host only shapes its own egress, so e.g. LTE's uplink delay goes on the
# client's egress and LTE's downlink delay goes on the server's egress;
# the sum of both hosts' one-way delay is the modeled RTT contribution.
#
# Usage (self-elevates via sudo if not already root):
#   ./tools/experiment/netem.sh setup <iface> <match-ip> <src|dst> <lte|leo> <uplink|downlink>
#   ./tools/experiment/netem.sh clear <iface>
#
# On the CLIENT: match-ip is one of the -local source IPs the client
# dials from, matched as "src" -- each path's egress traffic genuinely
# originates from a distinct IP there.
#   ./tools/experiment/netem.sh setup eth0 10.0.0.11 src lte uplink
#
# On the SERVER: the server listens on one wildcard address for every
# path (see -listen in cmd/server), so its egress traffic all shares one
# source IP -- what varies per path is which client IP it's replying to.
# Match-ip is the client's path IP, matched as "dst":
#   ./tools/experiment/netem.sh setup eth0 10.0.0.11 dst lte downlink
#
# Either way, match-ip must be aliased onto the client's interface first
# if it isn't already a real address, e.g.:
#   sudo ip addr add 10.0.0.11/24 dev eth0 label eth0:path0
#
# ponytail: static delay/loss/rate approximate the paper's time-varying
# links (real LTE data rate fluctuates, latency has rare 2900ms handover
# spikes not modeled here). Add periodic spike/rate-change injection
# (a timer loop re-running `tc qdisc change`) if handover behavior
# specifically needs testing.
set -euo pipefail

# Self-elevate: ip/tc need root. Lets callers skip typing sudo themselves.
# Resolved to an absolute path first -- sudo looks up a bare "$0" (e.g.
# when invoked as "bash netem.sh") in PATH, not the current directory.
[ "$(id -u)" -eq 0 ] || exec sudo "$(cd "$(dirname "$0")" && pwd)/$(basename "$0")" "$@"

lte_delay() { # $1=direction -> "avg jitter" (ms), from measured latency
  case "$1" in
    uplink)   echo "53ms 20ms" ;;
    downlink) echo "45ms 18ms" ;;
  esac
}
leo_delay() { echo "25ms 13ms"; } # symmetric per paper (12-38ms range)

lte_loss="0.006%"
leo_loss="0.17%"

lte_rate() { echo "30mbit"; } # midpoint of measured ~15-45Mbps fluctuation
leo_rate() { # $1=direction -> fixed asymmetric capacity per paper
  case "$1" in
    uplink)   echo "18mbit" ;;
    downlink) echo "62mbit" ;;
  esac
}

usage() {
  echo "usage: $0 setup <iface> <match-ip> <src|dst> <lte|leo> <uplink|downlink>" >&2
  echo "       $0 clear <iface>" >&2
  exit 1
}

action=${1:-}
iface=${2:-}
[ -n "$action" ] && [ -n "$iface" ] || usage

if [ "$action" = clear ]; then
  tc qdisc del dev "$iface" root 2>/dev/null || true
  echo "cleared tc config on $iface"
  exit 0
fi

[ "$action" = setup ] || usage
match_ip=${3:-}
match_dir=${4:-}
profile=${5:-}
direction=${6:-}
[ -n "$match_ip" ] && [ -n "$match_dir" ] && [ -n "$profile" ] && [ -n "$direction" ] || usage

case "$match_dir" in src|dst) ;; *) usage ;; esac
case "$direction" in uplink|downlink) ;; *) usage ;; esac

# class id derived from the match IP's last octet -- deterministic, no
# state file needed to track which class belongs to which path.
classid=$(echo "$match_ip" | awk -F. '{print $NF}')
case "$classid" in ''|*[!0-9]*) echo "match-ip must be an IPv4 address" >&2; exit 1 ;; esac

case "$profile" in
  lte) delay=$(lte_delay "$direction"); loss=$lte_loss; rate=$(lte_rate) ;;
  leo) delay=$(leo_delay); loss=$leo_loss; rate=$(leo_rate "$direction") ;;
  *) echo "unknown profile: $profile (want lte|leo)" >&2; exit 1 ;;
esac

# root htb qdisc, created once per interface; safe to call setup
# repeatedly for additional paths on the same iface.
tc qdisc show dev "$iface" | grep -q "htb 1:" || \
  tc qdisc add dev "$iface" root handle 1: htb default 999

tc class add dev "$iface" parent 1: classid "1:$classid" htb rate "$rate" ceil "$rate"
# shellcheck disable=SC2086  # $delay is an intentional two-word netem arg (avg jitter)
tc qdisc add dev "$iface" parent "1:$classid" handle "$classid:" netem delay $delay loss "$loss"
tc filter add dev "$iface" protocol ip parent 1: prio 1 u32 match ip "$match_dir" "$match_ip" flowid "1:$classid"

echo "applied $profile/$direction shaping on $iface for $match_dir=$match_ip (class 1:$classid): delay=$delay loss=$loss rate=$rate"
