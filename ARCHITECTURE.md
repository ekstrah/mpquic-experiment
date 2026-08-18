# Architecture

Deep-dive reference for contributors. Read `README.md` first for the
high-level pitch and CLI usage; this document is the "how it actually
works" tour — wire format, lifecycle, concurrency, and the non-obvious bugs
already found and fixed, so they don't get reintroduced.

## Mental model

Two independent, single-purpose processes, `cmd/client` and `cmd/server`,
talk over **N independent QUIC connections** (one per network path) that
get correlated into one logical transfer by a shared session ID. There is
no real "multipath QUIC" at the transport layer — each path is a normal
`quic-go` connection with its own handshake, congestion control, and
retransmission state. Multipath only exists at the application layer, in
how `internal/scheduler` spreads chunks across those connections and how
`internal/transfer` correlates them back into one session on the receiving
end. See README's "Why not MPTCP" / "Why not `mp-quic`" sections for why
this design was chosen over the alternatives.

## Repository map

```
third_party/quic-go/    vendored quic-go@v0.61.0 + a small patch exposing
                         pluggable congestion control (see README's
                         "The quic-go patch" section for the exact diff)

internal/
  transfer/
    session.go           wire preambles: ControlHeader, PathHello, the
                          stream-kind discriminator byte, SessionID
    chunk.go              chunk framing (WriteChunk/ReadChunk), per-chunk
                          CRC32 checksum, Split(), Reassembler
  metrics/
    metrics.go             PathStats/RunResult, JSON+CSV writers, the
                          console Print(), RTT/delivery sample logs
  scheduler/
    scheduler.go          Scheduler interface + name registry
    roundrobin.go         one path per chunk, round-robin
    redundant.go          every path per chunk (max reliability, min efficiency)
    rtt_aware.go           lowest-known-RTT path, falls back to round-robin
  ccmodules/
    registry.go            congestion-control name registry (Factory type
                          must stay structurally assignable to quic-go's
                          patched quic.Config.CongestionControlFactory field
                          — see the comment in registry.go before changing it)
    cubic.go               wraps quic-go's built-in Cubic sender
    custom_template.go     fixedWindowSender — copy this to add a new algorithm
  tlsconfig/
    tlsconfig.go           self-signed cert generation (this is a private
                          experiment tool between hosts you control, not a
                          public service — see the package doc comment)

cmd/
  server/main.go          accepts connections, correlates sessions, verifies
                          + reassembles, writes results
  client/main.go          generates payload, dials paths, schedules chunks,
                          drains connections, writes results

tools/
  results-viewer.html     standalone (no build step) browser tool: drag in
                          the CSVs both binaries write, get charts. Detects
                          file type by CSV header content, not filename.
```

## Wire protocol

Every path is one QUIC connection with exactly one bidirectional stream.
All multi-byte integers are big-endian. There is no varint framing — every
field is fixed-width, so the parser never needs to backtrack.

**Every stream starts with a 1-byte discriminator**, then exactly one of
two preambles (`internal/transfer/session.go`):

```
kind=0 (ControlHeader) — sent once, only on path 0:
  [1B  kind=0]
  [16B SessionID]
  [8B  TotalSize]       uint64  — 0 means a continuous session (see below)
  [4B  ChunkSize]       uint32
  [4B  NumPaths]        uint32
  [2B  len(Scheduler)]  uint16
  [.. Scheduler]        ASCII, that many bytes

kind=1 (PathHello) — sent once, on every path except 0:
  [1B  kind=1]
  [16B SessionID]
  [4B  PathIndex]       uint32
```

Path 0 is special: it's the only path that describes the whole session
(total size, chunk size, path count, scheduler name). Every other path just
announces "I'm path N of session X" so the server can correlate the
separate QUIC connections without them sharing any transport-level state.

**After the preamble, zero or more `Chunk` frames** (`internal/transfer/chunk.go`),
with no further framing byte — the reader just keeps calling `ReadChunk`
until it hits `io.EOF`:

```
  [8B  Seq]         uint64  — chunk's position within its burst
  [8B  Offset]      uint64  — byte offset within its burst
  [8B  BurstID]     uint64  — which burst this chunk belongs to
  [8B  BurstBytes]  uint64  — that burst's total size
  [4B  Length]      uint32  — length of Data that follows (N)
  [4B  Checksum]    uint32  — CRC32-IEEE of Data, computed by WriteChunk
  [NB  Data]
```

There is deliberately **no whole-transfer hash anywhere in this protocol**.
Integrity is checked per chunk (see "Data integrity" below), which is what
makes the protocol size-agnostic — a chunk's checksum doesn't care how many
chunks came before it or how many are still to come.

`BurstID`/`BurstBytes` are what actually make continuous streaming
possible: they're repeated on every chunk of a burst (a few redundant bytes
per chunk, negligible) so the receiver learns a burst's total size from its
very first chunk, with **no second frame type needed** — the read loop
stays exactly `for { ReadChunk() }`; burst boundaries are inferred purely
from `BurstID` changing. A one-shot (`-size`) transfer is just a single
burst covering the whole session (`BurstID` 0, `Offset`/`Seq` session- and
burst-relative alike since there's only one burst). See "Data integrity"
below for how a burst's completion is judged, and README's "Continuous
mode" section for the CLI-level picture.

## Session correlation

One session = one client run = one `SessionID` = N independent QUIC
connections. The server's `sessionRegistry` (`cmd/server/main.go`) maps
`SessionID → *serverSession`, created lazily by whichever path connects
first (`getOrCreate`). Each accepted connection runs in its own
`handleConn` goroutine; regardless of arrival order, `sess.waitReady`
blocks each path's goroutine until path 0's `ControlHeader` has arrived and
initialized the session (`sess.start`).

`sess.start` branches on `ctrl.TotalSize`: nonzero constructs a
`Reassembler` sized to it (one-shot mode, unchanged); zero constructs a
`BurstTracker` instead and starts that session's `burstSweepLoop`
goroutine (continuous mode — see "Data integrity" below for what each one
actually does).

A `sync.WaitGroup` sized to `ctrl.NumPaths` tracks how many paths have
finished. For one-shot sessions, the last one to call `sess.pathDone`
triggers `sess.finalize()`, which does the integrity determination and
writes the three server-side result files. For continuous sessions, that
same trigger calls `sess.shutdownContinuous()` instead — there's no
"reassemble the whole thing and hash it" step, just a final force-flush of
whatever `BurstTracker` still has (see below). A continuous session can
also be force-flushed early, from the server process's own `SIGINT`
handler in `main()` (via `sessionRegistry.shutdownAll`) — the client may
still be streaming when the server operator hits Ctrl+C.

## Connection lifecycle (and the bug that lived here)

```
client                                              server
  |--- dial N QUIC connections (one per -local) ----->|
  |--- path 0: ControlHeader ------------------------>|
  |--- path 1..N-1: PathHello ------------------------>|
  |--- Chunk, Chunk, Chunk, ... (scheduler-assigned) -->|
  |--- stream.Close() [FIN, non-blocking] ------------>|
  |                                                     |  read loop sees
  |                                                     |  io.EOF (proof
  |                                                     |  everything up to
  |                                                     |  the FIN arrived,
  |                                                     |  in order)
  |<-------------------- 1-byte drain ack -------------|  (connection NOT
  |  (blocks on this read, up to drainTimeout)          |   closed yet)
  |--- conn.CloseWithError() -------------------------->|  (peer's close
  |                                                      |   tears this
  |                                                      |   connection down)
```

The drain-ack step (`drainPath`/`drainPaths` in `cmd/client/main.go`,
the `closeNow` flag in `cmd/server/main.go`'s `handleConn`) exists because
of a real bug found while validating this tool against real network runs:
`quic-go`'s `SendStream.Close()` only *schedules* the FIN — it does not
block until the data is actually sent, let alone acknowledged
(`third_party/quic-go/send_stream.go:552`). The original code called
`stream.Close()` and then, moments later, `conn.CloseWithError()` — which
sends `CONNECTION_CLOSE` immediately and abandons anything not yet
transmitted or acked (`third_party/quic-go/connection.go:2180`). Under real
network conditions (not idle loopback), this reliably dropped the last N
chunks, which looked like a hash-verification bug but was actually a
connection-teardown race.

**The rule this leaves for future code**: never call `CloseWithError` (or
let a process exit) immediately after a stream's last write, on *either*
side, without first getting some signal that the peer actually received
everything. The server intentionally does *not* close proactively after
sending its ack — that would race the ack write against its own close the
same way. It relies on the client's own close (which arrives once the
client has the ack) or, as a fallback if the client vanishes, the QUIC
idle timeout.

This same `drainPath` primitive is meant to be reused for mid-session path
retirement once dynamic multipath (adding/removing links while streaming)
gets implemented — draining is a per-path operation, not a
whole-session-end operation.

## Data integrity

Each `Chunk` carries its own CRC32 checksum (`Chunk.VerifyChecksum`,
`internal/transfer/chunk.go`), computed automatically in `WriteChunk` and
checked by the server right after `ReadChunk` (`cmd/server/main.go`). A
chunk that fails its checksum is logged and counted, but **not** handed to
whichever completion tracker is active for that session — under a
redundant scheduler, a good copy might still arrive on another path and
get counted instead.

**One-shot (`-size`) sessions** use `Reassembler` exactly as before: it's
sized to `ctrl.TotalSize` upfront, and a chunk's `Offset` (session-relative
in this mode, since there's only one burst) places it directly. The
server's authoritative `integrity_ok` is:

```go
ok := corrupted == 0 && s.reassembler.Complete()
```

i.e. *no chunk ever failed its checksum, and every expected chunk arrived
at least once*.

**Continuous sessions** (`ctrl.TotalSize == 0`) use `BurstTracker`
(`internal/transfer/burst.go`) instead, keyed by `Chunk.BurstID`. It
deliberately does **not** buffer a burst's actual bytes — this is a
reliability harness, not a data sink, so tracking bytes-received-so-far vs.
`Chunk.BurstBytes` (learned from that burst's very first chunk) is enough
to judge completion; there's nothing to reassemble into. A burst is
declared complete once its received bytes reach `BurstBytes`; one that
goes `burstTTL` (5s) without a new chunk is treated as abandoned (e.g. its
connection dropped mid-burst) and reported incomplete rather than tracked
forever. Every `burstSweepInterval` (1s), `serverSession.burstSweepLoop`
expires idle bursts and appends one `metrics.BurstSample` row per burst to
that session's `<out>-bursts.csv` — there's no single "the end" to wait for
in this mode the way `finalize()` waits for one-shot sessions, so results
are reported incrementally as the session runs instead of once at the
finish.

In both modes, the client-side result always reports `integrity_ok=n/a` (or,
in continuous mode, its own `-bursts.csv` rows track what it attempted to
send, not what arrived) — the client has no way to observe what the server
actually received.

This per-chunk-plus-per-burst design replaced an earlier one-shot-only
design (one SHA-256 over the whole payload, sent in the `ControlHeader`
*before* any data, checked once at the very end). That design required
knowing the total payload upfront and could only ever report one pass/fail
verdict for the entire transfer — incompatible with a session that has no
known total and no single end.

## Concurrency model

**Client** (`cmd/client/main.go`), per run:
- 1 goroutine per path sampling RTT every 10ms (`sampleRTT`) into a shared
  `metrics.SampleLog[RTTSample]` (internally mutex-protected).
- 1 goroutine per path draining a `chan transfer.Chunk` and writing chunks
  to that path's stream (`sendChunks`); the scheduler decides, per chunk,
  which queue(s) to push onto.
- `drainPaths` fans out one goroutine per path (closing + waiting for the
  ack), joined with a `sync.WaitGroup` before the run is considered done.

**Server** (`cmd/server/main.go`), per run:
- 1 goroutine per **accepted connection** (`handleConn`) — since each path
  is its own QUIC connection, this is naturally 1 goroutine per path, with
  no shared mutable state between them except through `serverSession`.
- Continuous sessions additionally get 1 goroutine per session
  (`burstSweepLoop`), ticking every `burstSweepInterval` to expire idle
  bursts and rewrite `-bursts.csv`, until `stopSweep` is closed (once,
  guarded by `stopSweepOne`) from whichever of `shutdownContinuous`'s two
  callers — `wg.Wait()` finishing or the process's `SIGINT` handler — gets
  there first.
- 1 process-wide goroutine in `main()` waiting on `SIGINT`/`SIGTERM`, which
  calls `sessionRegistry.shutdownAll()` before `os.Exit(0)`.
- Shared state within a session is protected by: `serverSession.mu`
  (control header / reassembler-or-burstTracker / progress printer setup,
  guarded by the `ready` channel so no path proceeds before path 0's header
  arrives), `serverSession.pathMu` (appending to `pathResults`),
  `Reassembler.mu` (one-shot mode: chunk placement, the same reassembler
  instance written to concurrently by every path's goroutine), and
  `BurstTracker.mu` (continuous mode: the same role, scoped per burst
  instead of per session).
- `sessionRegistry.mu` protects the `SessionID → *serverSession` map
  itself (session creation/lookup), separate from any individual session's
  own locking.

## Extending the system

Already documented in README (kept there since it's user-facing, not
internals): "Adding a congestion control module" and "Adding a scheduler".
Both are registry-based (`ccmodules.Register` / `scheduler.Register`
called from an `init()`), selected by name via `-cc` / `-scheduler` — no
changes needed anywhere else to add a new one.

## Testing

```sh
go test ./...                                   # unit tests (transfer, scheduler)
go build -o bin/server ./cmd/server && go build -o bin/client ./cmd/client
```

For an end-to-end smoke test (loopback, low-loss — good for catching
protocol/logic bugs, not representative of real-network timing issues like
the drain-ack bug above, which needs real network conditions to reproduce):

```sh
./bin/server -listen 127.0.0.1:14433 -out /tmp/s &
./bin/client -server 127.0.0.1:14433 -size 10485760 -out /tmp/c
# check: client stdout "total: ... integrity_ok=n/a"
#        server stdout "total: ... integrity_ok=true", 0 corrupted per path
```

And for continuous mode — this is also the easiest way to eyeball that
`BurstID`/`BurstBytes` round-trip correctly and that Ctrl+C mid-run still
leaves both `-bursts.csv` files valid (not truncated mid-row):

```sh
./bin/server -listen 127.0.0.1:14433 -out /tmp/s-cont &
./bin/client -server 127.0.0.1:14433 -continuous -duration 10s \
  -burst-min-size 1024 -burst-max-size 2097152 -burst-interval 500ms -out /tmp/c-cont
# check: /tmp/c-cont-bursts.csv and /tmp/s-cont-bursts.csv both have one row
#        per burst, complete=true, chunks_corrupted=0, matching burst_ids
```

`tools/results-viewer.html` is also useful during development: it has a
"Load sample data" button that synthesizes a full session (including RTT
and delivery timelines) without needing a real run, and — since it's a
single static file with no build step — doubles as a quick way to eyeball
whether a metrics/CSV format change round-trips correctly.
