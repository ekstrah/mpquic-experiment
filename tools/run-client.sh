#!/usr/bin/env bash
# Runs the client against the LTE/LEO emulation setup (netem-client.sh /
# netem-server.sh) using the paper's 10 Mbps CBR video-like burst config,
# with a chosen scheduler. Writes to client-<scheduler>-*.
#
# Usage: ./tools/run-client.sh <roundrobin|rtt-aware|redundant>
#
# Run once per scheduler to compare them in results-viewer.html, e.g.:
#   ./tools/run-client.sh roundrobin
#   ./tools/run-client.sh rtt-aware
#   ./tools/run-client.sh redundant
set -euo pipefail

scheduler=${1:-}
case "$scheduler" in
  roundrobin|rtt-aware|redundant) ;;
  *) echo "usage: $0 <roundrobin|rtt-aware|redundant>" >&2; exit 1 ;;
esac

SERVER=10.10.10.1:4433
LOCAL=10.10.10.11,10.10.10.12

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)

"$repo_root/bin/client" -server "$SERVER" -local "$LOCAL" -scheduler "$scheduler" -continuous \
  -burst-min-size 41667 -burst-max-size 41667 -burst-interval 33ms \
  -out "client-$scheduler"
