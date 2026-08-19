# TODO

## Implementation

### Dynamic multipath (core research goal)
- [ ] Support adding/removing paths mid-session (needs multi-NIC hardware to
      exercise). `drainPath`/`drainPaths` (`cmd/client/main.go`) are already
      the right primitive to retire a path safely — reuse rather than
      reinvent.

### Congestion control
- [ ] Only the client selects congestion control per path — server has
      nothing to control since it doesn't send bulk data. Revisit if the
      protocol ever needs server->client bulk data.

### Schedulers
- [ ] No open items yet. Shared-bottleneck-aware scheduling (coupling
      send rate across paths that share a bottleneck) is gated on the
      Investigation item below — only build it if real measurements show
      correlated spikes.

### Link emulation / testbed
- [ ] No netns/tc-based link emulation yet (latency/loss/bandwidth
      impairment) — paths are real local NICs/IPs only for now.

### Continuous mode realism
- [ ] Burst sizes are uniform-random (`-burst-min-size`/`-burst-max-size`),
      not a modeled traffic shape (e.g. mostly-small with occasional large
      spikes, video-like).
- [ ] Burst content is synthetic random data, not real files/sensor input.
      Both of the above can be layered on the existing `BurstID`/
      `BurstBytes` wire framing without another protocol change.

### Scale
- [ ] One-shot (`-size`) transfers buffer the whole payload in server
      memory — see `ponytail:` comment in `internal/transfer/chunk.go`.
      Swap to temp file + `WriteAt` if transfer sizes need to exceed
      available RAM. `-continuous` mode doesn't have this problem.

## Investigation

- [ ] Check whether a shared bottleneck is actually occurring in the setup:
      look for RTT/loss spikes across paths correlating in time
      (simultaneous spikes on independent paths = signal of a shared queue
      somewhere, vs. independent spikes = no shared bottleneck). Drives
      whether the Schedulers item above is worth building.

## Done
- [x] Integrity bug (client force-closes QUIC conn before tail chunks
      flush/ack) — fixed via `drainPath`/`drainPaths` path-drain-ack
      primitive.
