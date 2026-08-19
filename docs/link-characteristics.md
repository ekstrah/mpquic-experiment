# Link characteristics reference

Source: Baltaci, Chavali, Kosek, Mohan, Schupke, Ott, "Multipath Transport
Analysis Over Cellular and LEO Access for Aerial Vehicles," IEEE Access,
2023 (DOI 10.1109/ACCESS.2023.3325702). All numbers below are measured
values from that paper's real-world traces, used to parameterize its
MoonGen (cellular) and OpenSAND (LEO) link emulators. Intended to drive
this project's own `tc`/`netem` configs (see `TODO.md` → Link emulation).

**Coverage caveat:** the paper only measured **LTE** and **Starlink LEO**.
It explicitly used LTE instead of 5G "due to unpredictable and insufficient
5G coverage in the air," and does not cover WiFi mesh at all. There are no
paper-sourced numbers for 5G/6G or WiFi mesh below — don't invent any and
attribute them to this paper. If those link types are needed later, source
them separately (e.g. the "Multi-Connectivity for UAVs" aerial-mesh
measurement paper found during research) or clearly label them as
estimates, not measurements.

## LTE (cellular)

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

## LEO (Starlink)

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

## Not covered by this paper

- **5G/6G**: no measured numbers. Paper explicitly notes the emulator
  "can be extended to 5G by modeling the appropriate link parameters" —
  i.e. this was future work, not something they measured.
- **WiFi mesh**: not mentioned anywhere in the paper.
