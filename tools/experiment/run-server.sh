#!/usr/bin/env bash
# Runs the server with -out matched to a scheduler label, so its results
# don't get overwritten by the next run -- the server's -out prefix is
# shared by every session on that process (cmd/server/main.go), so a
# fresh server (fresh -out) is needed per run-client.sh invocation.
#
# Usage: ./tools/experiment/run-server.sh <roundrobin|rtt-aware|redundant>
# Ctrl+C after the matching run-client.sh finishes, then restart with the
# next scheduler's label before the next client run.
set -euo pipefail

label=${1:-}
case "$label" in
  roundrobin|rtt-aware|redundant) ;;
  *) echo "usage: $0 <roundrobin|rtt-aware|redundant>" >&2; exit 1 ;;
esac

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)

"$repo_root/bin/server" -listen 10.10.10.1:4433 -out "server-$label"
