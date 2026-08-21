#!/usr/bin/env bash
# Runs the client against the flight-measured emulation setup
# (netem-client-flight.sh / netem-server-flight.sh -- aerial mesh /
# private 5G / LEO satellite) using the paper's 10 Mbps CBR video-like
# burst config, with a chosen scheduler. Writes to client-<scheduler>-*.
# Counterpart to run-client.sh, which targets the LTE/LEO scenario's
# 2-path IPs instead -- use whichever matches the netem-*.sh scenario
# you applied.
#
# Usage: ./tools/experiment/run-client-flight.sh <roundrobin|rtt-aware|redundant>
#
# Run once per scheduler to compare them in results-viewer.html, e.g.:
#   ./tools/experiment/run-client-flight.sh roundrobin
#   ./tools/experiment/run-client-flight.sh rtt-aware
#   ./tools/experiment/run-client-flight.sh redundant
set -euo pipefail

scheduler=${1:-}
case "$scheduler" in
  roundrobin|rtt-aware|redundant) ;;
  *) echo "usage: $0 <roundrobin|rtt-aware|redundant>" >&2; exit 1 ;;
esac

SERVER=10.10.10.1:4433
LOCAL=10.10.10.21,10.10.10.22,10.10.10.23

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)

"$repo_root/bin/client" -server "$SERVER" -local "$LOCAL" -scheduler "$scheduler" -continuous \
  -burst-min-size 41667 -burst-max-size 41667 -burst-interval 33ms \
  -out "client-$scheduler"
