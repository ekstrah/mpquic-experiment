# tools/experiment

Scripts to shape emulated links, run a client/server experiment over
them, and produce CSVs for `../viewing/results-viewer.html`.

## Prerequisites

- Two Ubuntu hosts (client + server) connected via a direct Ethernet
  link (`enp131s0` by default -- edit the `IFACE` variable near the top
  of each `netem-*.sh` if yours differs).
- `sudo` (the `netem-*.sh` scripts self-elevate).
- Built binaries from the repo root: `go build -o bin/client ./cmd/client`
  and `go build -o bin/server ./cmd/server`.

**"Client host" and "server host" below always mean the same two
physical machines throughout.** `netem-client*.sh up` IP-aliases the
path addresses onto whichever machine you run it on -- `bin/client`/
`run-client.sh` must then run on that *exact same* machine, or binding
to those addresses fails with `bind: cannot assign requested address`
(the alias simply doesn't exist anywhere else). Same pairing applies to
`netem-server*.sh` and the server host.

## 1. Apply link shaping (once per host, before running experiments)

Pick **one** scenario below -- don't run both on the same interface at
once. See `../../docs/link-characteristics.md` for what each link
models (and which paper it's from) and `../../docs/netem-emulation.md`
for how the shaping is actually applied under the hood.

### Trace-emulated: LTE + LEO (Baltaci et al. 2023)

```sh
# on the client host
./tools/experiment/netem-client.sh up
# on the server host
./tools/experiment/netem-server.sh up
```

Tear down when done: `netem-client.sh down` / `netem-server.sh down`.

### Flight-measured: aerial mesh + private 5G + LEO satellite (Baltaci et al. 2026)

```sh
# on the client host
./tools/experiment/netem-client-flight.sh up
# on the server host
./tools/experiment/netem-server-flight.sh up
```

Tear down when done: `netem-client-flight.sh down` / `netem-server-flight.sh down`.

Either `up` command prints the exact `-local`/`-listen` flags to use in
step 2.

## 2. Run the experiment, once per scheduler you want to compare

`run-server.sh` is shared by both scenarios (the server just listens on
one fixed address regardless of how many client paths exist). The
client script is **scenario-specific** -- it hardcodes the `-local` IPs
to match whichever `netem-client*.sh` you ran in step 1, so use the one
that matches:

| Step 1 scenario | Step 2 client script |
|---|---|
| `netem-client.sh` (LTE/LEO) | `run-client.sh` |
| `netem-client-flight.sh` (mesh/private 5G/satellite) | `run-client-flight.sh` |

Using the wrong pairing binds to IPs that were never aliased and fails
with `bind: cannot assign requested address`.

Each run needs a distinct `-out` prefix so results don't overwrite each
other -- these scripts handle that for you, labeled by scheduler name.

```sh
# on the server host -- restart before EACH scheduler run
./tools/experiment/run-server.sh roundrobin

# on the client host, once that server is up (pick the script matching step 1)
./tools/experiment/run-client.sh roundrobin
# or: ./tools/experiment/run-client-flight.sh roundrobin
```

Wait for the client script to finish, then Ctrl+C the server. Repeat for
the other schedulers (`rtt-aware`, `redundant`), restarting the server
each time. This always runs `-continuous` mode with the paper's 10Mbps
CBR video-like burst config (see the comment at the top of each script),
against whichever link scenario you applied in step 1.

## 3. View results

Each run writes `client-<scheduler>-*.csv` (on the client host) and
`server-<scheduler>-*.csv` (on the server host) to the current
directory. Copy them to your local machine, then open
`../viewing/results-viewer.html` in a browser (or serve it locally --
see that file's own in-page instructions) and drag the CSVs in.

## Scripts reference

| Script | Role |
|---|---|
| `netem.sh` | Low-level `tc`/netem primitive -- called by the wrapper scripts below, not usually invoked directly |
| `netem-client.sh` / `netem-server.sh` | Apply/remove trace-emulated LTE/LEO shaping |
| `netem-client-flight.sh` / `netem-server-flight.sh` | Apply/remove flight-measured aerial-mesh/private-5G/satellite shaping |
| `run-client.sh` | Run the client for a given scheduler against the LTE/LEO 2-path IPs |
| `run-client-flight.sh` | Run the client for a given scheduler against the mesh/private-5G/satellite 3-path IPs |
| `run-server.sh` | Run the server for a given scheduler label -- shared by both scenarios |
