# How `tools/experiment/netem.sh` mocks link characteristics

Companion to [`link-characteristics.md`](link-characteristics.md) (the
measured numbers, for both the trace-emulated LTE/LEO scenario and the
flight-measured aerial-mesh/private-5G/satellite scenario) -- this
explains the mechanism `tools/experiment/netem.sh` uses to actually apply
them via Linux `tc`/`netem`, real kernel-level packet shaping, not a
simulation layered on top. That "real shaping" point holds equally for
both scenarios -- what differs between them is only where their source
numbers came from, not how `netem.sh` applies either one.

## The three-step tc tree

Each path gets its own shaping "lane" built from three `tc` primitives,
chained together:

```
tc qdisc add ... root handle 1: htb default 999
```
A root **classifier** (`htb`) on the interface -- a traffic cop that
routes packets into different lanes based on rules. By default (`999`),
everything goes into an unshaped lane.

```
tc class add ... classid 1:<id> htb rate <rate> ceil <rate>
```
One **lane** per path, capped at that profile's bandwidth. This mocks
**capacity** -- packets queue and drop once traffic exceeds it, same as a
real bottleneck link.

```
tc qdisc add ... parent 1:<id> handle <id>: netem delay <avg> <jitter> loss <pct>
```
A **netem** qdisc attached inside that lane -- mocks **latency and packet
loss**: `delay` holds every packet for `avg` ± `jitter` before releasing
it, `loss` randomly drops a matching fraction of packets.

```
tc filter add ... u32 match ip <src|dst> <ip> flowid 1:<id>
```
The **sorting rule**: inspects each packet's source or destination IP and
routes it into the matching lane. Without this, all traffic sits in the
default unshaped lane and none of the shaping above applies.

**Net effect:** path IPs sharing one physical link each get an
independent bandwidth cap, delay, and loss rate -- genuinely different,
statistically-matched network conditions on the same wire.

## Why `src` vs `dst` differs by host

- **Client**: each path dials out from a distinct `-local` source IP
  (`cmd/client/main.go:33`), so egress traffic genuinely originates from
  a different IP per path -- classify by **`src`**.
- **Server**: it listens on one wildcard address for every path
  (`-listen` in `cmd/server`), so its egress traffic all shares one
  source IP -- what varies per path is which client IP it's replying to.
  Classify by **`dst`**.

## Where the numbers come from

`tools/experiment/netem.sh` hardcodes one function per statistic
(`lte_delay`/`leo_delay`/`mesh_delay`/`p5g_delay`/`sat_delay`, matching
`_loss`/`_rate` functions), pulled straight from `link-characteristics.md`.
`lte_delay`/`leo_rate` take a direction argument because LTE latency and
LEO capacity are asymmetric (uplink != downlink); the others don't, either
because the source paper reports them as symmetric (`leo_delay`/`lte_loss`)
or because the flight-measured paper's Table I doesn't split by direction
at all (`mesh`/`p5g`/`sat`).

| Profile | Direction | Delay | Loss | Rate |
|---|---|---|---|---|
| LTE | uplink | 53ms +-20ms | 0.006% | 30mbit |
| LTE | downlink | 45ms +-18ms | 0.006% | 30mbit |
| LEO | uplink | 25ms +-13ms | 0.17% | 18mbit |
| LEO | downlink | 25ms +-13ms | 0.17% | 62mbit |
| mesh | either | 2.5ms +-0ms | 0% | 30mbit |
| p5g | either | 15ms +-0ms | 0% | 5mbit |
| sat | either | 87.5ms +-12.5ms | 0% | 5mbit |

mesh/p5g/sat delay is half of Table I's reported RTT (one-way, to match
this script's existing "sum of both hosts' one-way delay = modeled RTT"
convention); jitter and loss are 0 wherever Table I doesn't report a
range or a loss figure, rather than inventing one -- see the `ponytail:`
comment in `netem.sh` for the exact reasoning per profile.

## Usage

```sh
sudo ./tools/experiment/netem.sh setup <iface> <match-ip> <src|dst> <lte|leo|mesh|p5g|sat> <uplink|downlink>
sudo ./tools/experiment/netem.sh clear <iface>
```

In practice, use the wrapper scripts instead of calling this directly:
- Trace-emulated LTE/LEO: `tools/experiment/netem-client.sh up`/`down`
  (aliases path IPs + applies `src`-based uplink shaping) and
  `tools/experiment/netem-server.sh up`/`down` (`dst`-based downlink
  shaping).
- Flight-measured mesh/private-5G/satellite: same pattern, via
  `tools/experiment/netem-client-flight.sh` and
  `tools/experiment/netem-server-flight.sh`.

Run one scenario's pair of scripts or the other, not both at once, on a
given interface. See `TODO.md` -> Link emulation / testbed for the
current setup (a direct Ethernet link between two Ubuntu hosts).

## Known limitation

`ponytail:` static delay/loss/rate approximate the paper's **time-varying**
links -- real LTE data rate fluctuates continuously and latency has rare
2900ms handover spikes, neither modeled here. These numbers are fixed for
the whole run. Two upgrade paths, cheapest first:

- Netem's built-in `distribution` parameter (e.g. `delay 45ms 18ms
  distribution pareto`) gives a heavier-tailed jitter shape, closer to
  occasional real spikes, with no new code.
- A companion script periodically running `tc qdisc change ... netem
  delay <new-value>` on a timer, for true time-varying/handover-event
  behavior (e.g. matching the paper's mean HO frequency of 0.05Hz).
