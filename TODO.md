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
- [ ] Failure/degradation-aware scheduling: paths are a static list built
      once at session start (`cmd/client/main.go:386`) and schedulers only
      ever see RTT (`internal/scheduler/scheduler.go`) — there's no
      liveness signal, so a genuinely dead/disconnected path just sits in
      the list with a stale RTT reading instead of being explicitly routed
      around. Baltaci et al.'s paper evaluates exactly this spectrum
      (lowRTT-with-fallback, BLEST, redundant) — our `roundrobin`/
      `rtt-aware` degrade gracefully as RTT rises but don't react to an
      outright path failure or bottleneck the way the paper's schedulers
      do.
      **Depends on:** Implementation → Dynamic multipath (needs a way to
      signal a path is down/removed, not just slow).

### Continuous mode realism
- [ ] Burst sizes are uniform-random (`-burst-min-size`/`-burst-max-size`),
      not a modeled traffic shape (e.g. mostly-small with occasional large
      spikes, video-like).
- [ ] Burst content is synthetic random data, not real files/sensor input.
      Both of the above can be layered on the existing `BurstID`/
      `BurstBytes` wire framing without another protocol change.
- [ ] Bidirectional simultaneous traffic: Baltaci et al.'s RP scenario runs
      video (10 Mbps, AV -> pilot) and control (1 Mbps, pilot -> AV)
      *simultaneously in opposite directions*. This tool only sends bulk
      data one way (client -> server); the server never sends bulk data
      back (see Congestion control item above). Matching the paper's
      scenario for a real comparison needs either running two
      client/server pairs concurrently (one per direction, quick to try
      first) or extending the protocol for true simultaneous bidirectional
      streams (bigger change — only worth it if running two pairs proves
      insufficient, e.g. if they need to share path state).
      **Depends on:** nothing to try the two-pairs approach; the protocol
      extension depends on Implementation → Congestion control (server
      needs something to control once it sends bulk data).

## Investigation

### Network characteristics reference
- [x] Extract the paper's measured link characteristics into a markdown
      reference doc (e.g. `docs/link-characteristics.md`) to drive the tc
      configs below: LTE (uplink latency avg ~53ms w/ spikes to 2900ms,
      downlink avg ~45ms, PER ~0.006% both directions, data rate
      fluctuating ~15-45Mbps, mean HO duration 20.01ms/std 195.13ms, mean
      HO frequency 0.05Hz/variance 0.042Hz) and LEO/Starlink (latency
      12-38ms both directions, PER ~0.17%, capacity ~62Mbps down/18Mbps
      up, modeled as constant). **Caveat:** the paper only measured LTE
      and Starlink LEO — it explicitly used LTE instead of 5G because of
      "unpredictable and insufficient 5G coverage in the air," and doesn't
      cover WiFi mesh at all. 5G/WiFi-mesh characteristics would have to
      come from a different source (e.g. the "Multi-Connectivity for
      UAVs" aerial-mesh paper found earlier) or be reasonable estimates,
      not pulled from this paper — don't fabricate paper-attributed
      numbers for links it didn't test.
      **Depends on:** nothing.

### Link emulation / testbed
- [ ] Build the tc/netem shaping now that both machines are real Ubuntu
      hosts (not a single dev machine) — no proxy shim or Docker needed.
      Plan: IP-alias each path onto the existing NIC (`ip addr add
      <ip>/24 dev eth0 label eth0:pathN`, no multi-NIC hardware required),
      select paths via the client's existing `-local ip1,ip2` (source-IP
      based, `cmd/client/main.go:33`), then use `tc`/`netem` on each
      host's egress interface with an `htb` root qdisc classifying by
      source IP into one class per path, each with a child `netem`
      (delay/jitter/loss) and `htb` rate cap for bandwidth. Needed to
      exercise heterogeneous conditions on demand rather than depending on
      live links; unblocks Dynamic multipath, Shared bottleneck, and
      CC/scheduler metric mismatch below.
      **Depends on:** Network characteristics reference above (need the
      actual numbers before writing tc configs).

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
