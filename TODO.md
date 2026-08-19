# TODO

Each item lists **Depends on** (must land first) where applicable. Items
with no dependency line can be picked up any time.

## Implementation

### Dynamic multipath (core research goal)
- [ ] Support adding/removing paths mid-session (needs multi-NIC hardware to
      exercise, or emulated NICs). `drainPath`/`drainPaths`
      (`cmd/client/main.go`) are already the right primitive to retire a
      path safely — reuse rather than reinvent.
      **Depends on:** Investigation → Link emulation / testbed (unless real
      multi-NIC hardware is available instead).

### Congestion control
- [ ] Implement a second real CC algorithm with a different feedback loop
      than `cubic` (e.g. delay-based, like Vegas/Copa-style). `fixed`
      (`internal/ccmodules/custom_template.go`) is a constant-window
      template, not a real algorithm — currently `cubic` is the only
      working CC, so there's nothing to actually mismatch yet.
      **Depends on:** nothing — unblocks the metric-mismatch investigation
      below.
- [ ] Only the client selects congestion control per path — server has
      nothing to control since it doesn't send bulk data. Revisit if the
      protocol ever needs server->client bulk data.

### Schedulers
- [ ] Shared-bottleneck-aware scheduling (couple send rate across paths
      that share a bottleneck).
      **Depends on:** Investigation → Shared bottleneck (only build if
      confirmed).
- [ ] CC-agnostic scheduling metric: add a delivery-rate/throughput field
      to `PathInfo` (`internal/scheduler/scheduler.go`) so scheduling
      doesn't rely on raw RTT, which is skewed by which CC algorithm is
      driving each path (loss-based CCs inflate RTT by design; delay-based
      CCs keep it artificially low).
      **Depends on:** Investigation → CC/scheduler metric mismatch (only
      build if confirmed).

### Continuous mode realism
- [ ] Burst sizes are uniform-random (`-burst-min-size`/`-burst-max-size`),
      not a modeled traffic shape (e.g. mostly-small with occasional large
      spikes, video-like).
- [ ] Burst content is synthetic random data, not real files/sensor input.
      Both of the above can be layered on the existing `BurstID`/
      `BurstBytes` wire framing without another protocol change.

## Investigation

### Link emulation / testbed
- [ ] No netns/tc-based link emulation yet (latency/loss/bandwidth
      impairment) — paths are real local NICs/IPs only for now. Needed to
      exercise heterogeneous conditions on demand rather than depending on
      multi-NIC hardware and live links; unblocks Dynamic multipath,
      Shared bottleneck, and CC/scheduler metric mismatch below.
      **Depends on:** nothing.

### Shared bottleneck
- [ ] Check whether a shared bottleneck is actually occurring in the setup:
      look for RTT/loss spikes across paths correlating in time
      (simultaneous spikes on independent paths = signal of a shared queue
      somewhere, vs. independent spikes = no shared bottleneck).
      **Depends on:** Link emulation (for controlled, repeatable
      conditions — could also be attempted on real links, but harder to
      trigger on demand).

### CC/scheduler metric mismatch
- [ ] Check whether running different CC algorithms per path (e.g.
      loss-based on one path, delay-based on another) actually causes the
      scheduler to misjudge path quality via distorted RTT readings.
      **Depends on:** Implementation → Congestion control (need a second
      real CC algorithm to mismatch against `cubic`); Link emulation
      recommended for controlled conditions.

## Consideration

### Scale
- [ ] One-shot (`-size`) transfers buffer the whole payload in server
      memory — see `ponytail:` comment in `internal/transfer/chunk.go`.
      Swap to temp file + `WriteAt` if transfer sizes need to exceed
      available RAM. `-continuous` mode doesn't have this problem.

## Done
- [x] Integrity bug (client force-closes QUIC conn before tail chunks
      flush/ack) — fixed via `drainPath`/`drainPaths` path-drain-ack
      primitive.
