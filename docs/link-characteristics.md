# Link characteristics reference

Two source papers, covering two different scenarios (see `netem.sh` for
how each is applied via `tc`/netem, and `TODO.md` → Link emulation):

- **Trace-emulated** (below): Baltaci et al. 2023 — LTE + LEO numbers
  come from real-world traces, but that paper's own MPTCP/MP-DCCP
  transport experiments ran on a testbed parameterized from those
  traces, not literal drone flights.
- **Flight-measured** (below): Baltaci et al. 2026 — aerial mesh /
  private 5G / LEO satellite numbers, validated against that paper's own
  real UAV flight experiments.

## Trace-emulated: LTE + LEO (Baltaci et al. 2023)

Source: Baltaci, Chavali, Kosek, Mohan, Schupke, Ott, "Multipath Transport
Analysis Over Cellular and LEO Access for Aerial Vehicles," IEEE Access,
2023 (DOI 10.1109/ACCESS.2023.3325702). All numbers below are measured
values from that paper's real-world traces, used to parameterize its
MoonGen (cellular) and OpenSAND (LEO) link emulators.

**Coverage caveat:** the paper only measured **LTE** and **Starlink LEO**.
It explicitly used LTE instead of 5G "due to unpredictable and insufficient
5G coverage in the air," and does not cover WiFi mesh at all. There are no
paper-sourced numbers for 5G/6G or WiFi mesh in this section — don't invent
any and attribute them to this paper. See the flight-measured section below
for private 5G, aerial mesh, and LEO satellite instead.

### LTE (cellular)

Collected from 90 real drone flights (up to 120m altitude) in an urban
environment with public network operators, using `QCSuper` to capture
LTE-layer info.

| Metric | Value |
|---|---|
| Uplink latency | avg ≈53ms, spikes as high as 2900ms (correlated with drone-in-air handovers) |
| Downlink latency | avg ≈45ms |
| Packet Error Rate (PER) | ≈0.006% (both directions) |
| Data rate | fluctuates ≈15-45Mbps (both directions, from drone flight dataset) |
| Mean handover (HO) duration | 20.01ms (std dev 195.13ms — heavy-tailed, most HOs short but some very long) |
| Mean HO frequency | 0.05Hz (variance 0.042Hz) |

Modeled in the paper's emulator with time-varying capacity/latency
(reflecting real link dynamics), not a constant.

### LEO (Starlink)

Collected from 48 hours of real measurements with a standard Starlink
terminal; dish on a rooftop in Garching, Germany, server in an AWS
Frankfurt data center — a representative regional-flight distance (<80km
between endpoints per the paper's RP scenario).

| Metric | Value |
|---|---|
| Latency | 12-38ms, fairly symmetric both directions |
| Packet Error Rate (PER) | ≈0.17% (~2 orders of magnitude higher than LTE) |
| Capacity | ≈62Mbps downlink, ≈18Mbps uplink |

Modeled in the paper's emulator as **constant capacity** (a OpenSAND v5
modulation-scheme limitation at the time), unlike LTE's time-varying
model — latency does still fluctuate within the 12-38ms range.

## Flight-measured: aerial mesh + private 5G + LEO satellite (Baltaci et al. 2026)

Source: Baltaci, Meer, Ozger, Cavdar, Schupke, "Multi-Connectivity for
UAVs: A Measurement Study of Integrating Cellular, Aerial Mesh, and LEO
Satellite Links," arXiv:2604.27640, 2026. Table I below is that paper's
"static ground measurements" baseline, taken prior to its flight
experiments; the paper's own real UAV flights (up to 30m altitude,
Airbus test site, Ottobrunn/Munich) then validate these against dynamic
conditions in Figures 3-6, showing e.g. the private 5G link's RTT has
"increased variability" in flight versus this static baseline.

| Metric | Aerial mesh | Private 5G | Satellite |
|---|---|---|---|
| Achievable data rate | >30 Mbit/s | ~5 Mbit/s | ~5 Mbit/s |
| Latency (RTT) | ~5 ms | ~30 ms | 150-200 ms |
| Initial connection time | Few seconds | Few seconds | Few minutes |
| Reconnection time | Few seconds | Few seconds | Few minutes |

**Coverage caveat:** no packet-loss/PER numbers are reported for any of
the three links (unlike the 2023 paper's LTE/LEO PER figures) — `netem.sh`
leaves loss at 0% for these three profiles rather than inventing a value.
Latency is reported as round-trip (RTT), not one-way, and only the
satellite figure gives a range (150-200ms) — `netem.sh` derives one-way
delay/jitter from these accordingly (see the `ponytail:` comment there).
