# mpquic-experiment

Client/server platform for testing reliability of data transfer over
**multiple network paths using QUIC**, with **pluggable congestion control**
per path. Go, runs on Linux/Ubuntu (and Windows, for local dev).

> New to this codebase? This README is the quick-start / user-facing
> reference. For wire-format byte layouts, the connection lifecycle,
> concurrency model, and the non-obvious bugs already found and fixed here,
> see [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Contents

- [How it works](#how-it-works)
  - [Why not MPTCP?](#why-not-mptcp)
  - [Why not `mp-quic` (the academic multipath QUIC fork)?](#why-not-mp-quic-the-academic-multipath-quic-fork)
- [The `quic-go` patch](#the-quic-go-patch)
- [Layout](#layout)
- [Build](#build)
- [Run](#run)
  - [Flags](#flags)
  - [Continuous mode](#continuous-mode)
    - [Constant-bitrate (video-like) streaming](#constant-bitrate-video-like-streaming)
- [Adding a congestion control module](#adding-a-congestion-control-module)
- [Adding a scheduler](#adding-a-scheduler)
- [Known limitations / next steps](#known-limitations--next-steps)

## How it works

There is no off-the-shelf Go library that gives you both real IETF
multipath QUIC (single connection, multiple paths) *and* a maintained,
pluggable-congestion-control QUIC stack (see "Why not `mp-quic`" below). So
multipath here is **application-level connection bonding**: the client opens
one independent QUIC connection per configured local path (source IP/NIC) to
the server, and a pluggable **scheduler** decides which path each chunk of
the payload goes out on. Independent connections mean a stall on one path
can't Head-of-Line-block another path — see "Why not MPTCP" below.

```
client                                          server
  ├─ path 0 (QUIC conn, local IP #1) ──────┐
  ├─ path 1 (QUIC conn, local IP #2) ──────┼──▶ one UDP listener,
  └─ path N (QUIC conn, local IP #N) ──────┘    N correlated connections
```

1. Client generates `-size` bytes of random data.
2. It opens one QUIC connection per `-local` address, each with its own
   congestion control module (`-cc`).
3. Path 0's stream carries a `ControlHeader` (session ID, total size, chunk
   size, scheduler name). Every other path's stream carries a `PathHello`
   (session ID, path index) so the server can correlate multiple connections
   into one logical transfer.
4. The chosen `-scheduler` assigns each chunk to one or more paths
   (`redundant` assigns every chunk to every path). Each chunk carries its
   own CRC32 checksum, verified independently as it arrives — there's no
   single whole-transfer hash, so a corrupted chunk doesn't invalidate
   anything else, and verification never waits for a "last" chunk.
5. The server reassembles chunks by byte offset (idempotent — duplicate
   writes from a redundant scheduler are ignored, and a corrupted chunk is
   discarded rather than reassembled, in case a good copy still arrives on
   another path). **This per-chunk checksum plus full-delivery check is the
   reliability check.**
6. Once a path's stream cleanly closes, the client waits for the server to
   confirm delivery (a small drain ack) before tearing down that path's
   connection, so in-flight tail data can't be lost to a premature close.
7. Both sides write a results record (per-path + aggregate: bytes, chunks,
   chunks corrupted, duration, throughput, integrity) to stdout, JSON, and
   CSV.

### Why not MPTCP?

A related study (Baltaci et al., IEEE Access 2023, 10.1109/ACCESS.2023.3325702)
bonding cellular + LEO satellite links found MPTCP suffers excessive
retransmissions from Head-of-Line blocking: one shared, in-order byte stream
across paths means a stall on one path stalls delivery on the other. Running
each path as an independent QUIC connection avoids that coupling entirely.

### Why not `mp-quic` (the academic multipath QUIC fork)?

`qdeconinck/mp-quic` and derivatives implement real single-connection
multipath, but are forked from a 2018-era, pre-RFC9000 QUIC draft and are
effectively unmaintained (one candidate fork even has unresolved `<<<<<<<`
git conflict markers committed into `go.mod`). Mainline `quic-go` is RFC 9000
and actively maintained, but only supports connection *migration* (one
active path at a time, `path_manager.go`), not concurrent multipath, and its
congestion control lives in `internal/congestion` — unusable from outside
the module. This project vendors mainline `quic-go` and patches just enough
to expose pluggable congestion control (see below), then does multipath at
the application layer instead.

## The `quic-go` patch

`third_party/quic-go` is a vendored copy of `quic-go@v0.61.0` with one small,
mechanical diff (referenced via `replace` in `go.mod`, so re-vendoring a
newer release later is a re-apply of the same edits):

1. `internal/congestion` → `congestion` (public package, visibility move
   only — no algorithm logic changed).
2. `congestion/types.go` adds type aliases (`ByteCount`, `PacketNumber`,
   `Time`, `RTTStats`, `ConnectionStats`) so external code implementing the
   `SendAlgorithm` interface never has to import `internal/protocol` or
   `internal/utils` directly.
3. `ackhandler.NewSentPacketHandler` takes a `CongestionControlFactory`
   parameter (defaults to Cubic if nil); `connection.go`'s `MigratedPath`
   reuses the same factory instead of hardcoding Cubic.
4. `quic.Config` gained a `CongestionControlFactory` field, threaded through
   both connection-setup call sites in `connection.go`.

## Layout

```
third_party/quic-go/          vendored + patched quic-go
internal/
  ccmodules/                  pluggable congestion control: registry.go, cubic.go, custom_template.go
  scheduler/                  pluggable path scheduler: roundrobin.go, redundant.go, rtt_aware.go
  transfer/                   wire protocol: session.go (control/hello preambles), chunk.go (split/reassemble/checksum)
  metrics/                    per-path + aggregate results: JSON/CSV/console output
  tlsconfig/                  self-signed TLS setup (QUIC requires TLS 1.3)
cmd/
  server/main.go
  client/main.go
```

## Build

```sh
go build -o bin/server ./cmd/server
go build -o bin/client ./cmd/client
```

## Run

Server:

```sh
./bin/server -listen :4433 -out server-results
```

Client, striping across two local interfaces (e.g. two NICs, or two IPs on
one NIC — Windows/Linux both treat all of `127.0.0.0/8` as loopback, so
`127.0.0.1`/`127.0.0.2` work for a same-machine dry run):

```sh
./bin/client -server <server-ip>:4433 \
  -local 192.168.1.10,10.0.0.5 \
  -size 10485760 \
  -scheduler roundrobin \
  -cc cubic,fixed \
  -out client-results
```

### Flags

**`cmd/server`**

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `:4433` | UDP address to listen on |
| `-out` | `server-results` | output file prefix (`-results.json` / `-results.csv`) |
| `-progress-interval` | `1s` | console progress print interval |

**`cmd/client`**

| Flag | Default | Meaning |
|---|---|---|
| `-server` | *(required)* | server address, `host:port` |
| `-local` | *(one path, OS-chosen address)* | comma-separated local source IPs, one per path |
| `-size` | `10485760` | total payload size in bytes (randomly generated) |
| `-chunk-size` | `32768` | chunk size in bytes |
| `-scheduler` | `roundrobin` | `roundrobin`, `redundant`, or `rtt-aware` |
| `-cc` | `cubic` | congestion control per path: one name for all paths, or a comma-separated list matching the number of paths (`cubic`, `fixed`) |
| `-out` | `client-results` | output file prefix |
| `-progress-interval` | `1s` | console progress print interval |
| `-continuous` | `false` | stream continuously as variable-size bursts instead of one fixed `-size` transfer (`-size` is ignored when set) |
| `-duration` | `0` | `-continuous` only: how long to keep streaming (`0` = until Ctrl+C) |
| `-burst-min-size` / `-burst-max-size` | `4096` / `1048576` | `-continuous` only: each burst's size is drawn uniformly at random from this range, in bytes |
| `-burst-interval` | `1s` | `-continuous` only: time between bursts |

The **server-side `integrity_ok`** in the results is the authoritative
reliability check: `true` iff every chunk it received passed its CRC32
checksum and every expected chunk arrived at least once (`chunks_corrupted`
in the CSV/JSON breaks out how many failed the checksum, per path). The
client-side result reports `integrity_ok=n/a` since it has no way to know
whether the server's checks passed.

### Continuous mode

`-continuous` is for simulating a link that never has one clean "transfer
done" moment -- closer to a real telemetry stream than a one-shot benchmark:
most traffic is small, punctuated by occasional large bursts (e.g. a drone
sending routine sensor readings, then an image). Each burst is sized
uniformly at random in `[-burst-min-size, -burst-max-size]`, sent one
`-burst-interval` apart, and verified independently (per-chunk CRC32 plus
full-byte-count delivery, same integrity model as one-shot mode, just
scoped to one burst instead of the whole session -- see
[`ARCHITECTURE.md`](ARCHITECTURE.md) for how).

```sh
./bin/server -listen :4433 -out server-results
./bin/client -server <server-ip>:4433 -continuous \
  -duration 60s -burst-min-size 4096 -burst-max-size 1048576 -burst-interval 1s \
  -out client-results
```

#### Constant-bitrate (video-like) streaming

Setting `-burst-min-size` equal to `-burst-max-size` removes the random
sizing (`sendBurst` only adds jitter when `max > min`) and gives a fixed
size sent at a fixed interval -- structurally the same shape as one video
frame sent every frame period, i.e. a constant-bitrate (CBR) stream. This
is the traffic model to reach for when comparing against work that used
CBR video/RTP-style traffic rather than bursty traffic -- e.g. Baltaci et
al. (IEEE Access 2023, 10.1109/ACCESS.2023.3325702, see "Why not MPTCP"
above) generated their MP-DCCP video traffic as a constant-bitrate flow
specifically to avoid confounding adaptive-bitrate behavior with the
transport layer's own congestion control, the same reasoning that applies
here.

Compute burst size from a target bitrate and frame rate:

```
bytes per frame = (bitrate in bits/sec) / 8 / (frames per second)
```

e.g. 10 Mbps at 30fps: `10_000_000 / 8 / 30 ≈ 41667` bytes every `1/30 s ≈ 33ms`:

```sh
./bin/client -server <server-ip>:4433 -continuous \
  -duration 60s -burst-min-size 41667 -burst-max-size 41667 -burst-interval 33ms \
  -out client-results
```

Verified on loopback: 61 "frames" over ~2s, every one exactly 41667 bytes,
landing ~33ms apart in `-bursts.csv`'s `start_ms` column.

Results are reported incrementally rather than once at the end (a
continuous session may run indefinitely): both sides append one row per
completed (or timed-out-incomplete) burst to `<out>-bursts.csv` as the
session runs. `-results.json`/`-results.csv` are one-shot-only and aren't
written in this mode. Ctrl+C (or `-duration` elapsing) stops the client
gracefully -- in-flight bursts finish, then paths drain and close the same
way one-shot mode's do. The server handles Ctrl+C the same way: whatever
bursts it's currently tracking get flushed to `-bursts.csv` before it exits.

## Adding a congestion control module

Copy `internal/ccmodules/custom_template.go`, rename `fixedWindowSender`,
implement the real logic in `OnPacketAcked` / `OnCongestionEvent` /
`GetCongestionWindow` (see `congestion.SendAlgorithmWithDebugInfos` in
`third_party/quic-go/congestion/interface.go` for the full method set), and
change the registered name in its `init()`. Select it with `-cc=<name>`.

## Adding a scheduler

Implement `scheduler.Scheduler` (`Assign(chunkSeq uint64, paths []PathInfo) []int`)
in a new file under `internal/scheduler/`, following `roundrobin.go` as a
template, and register it in `init()`. Select it with `-scheduler=<name>`.

## Known limitations / next steps

- Paths are real local NICs/IPs only for now — no netns/tc-based link
  emulation (latency/loss/bandwidth impairment) yet, and no dynamic
  add/remove of paths mid-session (needs multi-NIC hardware to exercise;
  `drainPath`/`drainPaths` in `cmd/client/main.go` are already the right
  primitive to retire a path safely once this is picked up).
- The server buffers the whole payload in memory for **one-shot** (`-size`)
  transfers (see the `ponytail:` comment in `internal/transfer/chunk.go` —
  swap to a temp file + `WriteAt` for transfer sizes beyond available RAM).
  `-continuous` mode doesn't have this problem — it never buffers a burst's
  bytes, only tracks arrival counts (`internal/transfer/burst.go`).
- `-continuous` mode's burst sizes are drawn from a uniform
  `[-burst-min-size, -burst-max-size]` range, not a modeled traffic shape
  (e.g. mostly-small with occasional large spikes); burst content is
  synthetic random data, not real files/sensor input. Both can be layered
  on top of the existing `BurstID`/`BurstBytes` wire framing later without
  another protocol change.
- Only the client selects congestion control per path; the server doesn't
  send bulk data in this protocol, so it has nothing to control.
