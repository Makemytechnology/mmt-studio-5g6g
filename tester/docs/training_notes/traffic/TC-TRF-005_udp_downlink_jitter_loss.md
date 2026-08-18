# TC-TRF-005: UDP Downlink Throughput with QoS Metrics

## Overview
This test measures UDP downlink throughput with jitter and packet loss from the core to the UE through the GTP-U tunnel. It validates the downlink data path's QoS characteristics, essential for streaming video and other downlink-heavy applications.

## 3GPP Background
Downlink UDP traffic is initiated from the core network iperf3 client, traverses the UPF (which encapsulates in GTP-U), and arrives at the UE's TUN interface. The UPF applies DL QoS enforcement (DL Session-AMBR, per-flow MBR) via QER rules.

For downlink, jitter and packet loss are particularly important because most consumer data is downlink-dominant (streaming, downloads). The UPF's QER for the DL direction may have different AMBR values than UL.

**Traffic path:** Core iperf3 -> UPF -> GTP-U encap -> UDP:2152 -> TUN -> UE iperf3 server

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7.2.1 | 5QI characteristics |
| TS 29.281 | 4 | GTP-U downlink |
| TS 23.501 | 5.7.2.6 | Session-AMBR (DL) |

## Problem Statement
- What if DL AMBR is lower than the iperf3 send rate, causing drops?
- What if UPF DL packet scheduling introduces jitter?
- What if the GTP-U socket receive buffer overflows on the gNB side?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish internet PDU session.
2. Start iperf3 UDP server on UE side (bound to UE TUN IP).
3. Trigger core iperf3 client to send 50 Mbps UDP to UE IP.
4. Collect jitter, loss, throughput at UE-side iperf3.
5. Collect UPF DL stats (dl_pkts, dl_bytes, dl_dropped).

## Expected Behavior
- Jitter < 50ms; packet loss < 1%.
- DL throughput close to 50 Mbps target.
- UPF dl_dropped = 0.

## Pass/Fail Criteria
- **Pass:** Jitter < 50ms; loss < 1%; UPF DL drops = 0.
- **Fail:** High jitter; excessive loss; UPF drops.

## Key Concepts for Training

### Downlink QoS Enforcement
The UPF enforces DL QoS through QER rules configured by the SMF. The DL Session-AMBR limits aggregate downlink rate. If the core sends above the AMBR, the UPF drops excess packets (dl_dropped increases). The UPF may also apply priority scheduling between GBR and non-GBR flows.

### Jitter Sources in Downlink
Downlink jitter can come from: (1) UPF processing variation (packet queuing, QoS scheduling), (2) GTP-U encapsulation overhead (variable packet sizes), (3) Network path congestion (N3 link), (4) gNB-side TUN interface scheduling. Understanding jitter sources helps target optimization.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| High DL drops | dl_dropped > 0 | Increase DL AMBR or reduce send rate |
| Jitter spikes | Periodic jitter > 100ms | Check GTP-U socket buffer, OS scheduling |
| Loss > 1% | Packets not reaching UE | Check GTP-U tunnel, TUN receive buffer |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7 (QoS)
- Related: TC-TRF-004 (UL UDP), TC-TRF-006 (bidir UDP), TC-TRF-002 (DL TCP)

## Quiz Questions
1. What UPF counter indicates DL AMBR enforcement?
   *Answer: dl_dropped (packets dropped by QER) and dl_metered (packets metered/shaped). Non-zero values indicate the DL send rate exceeded the AMBR.*

2. For a video streaming service using 5QI=9, what is the maximum acceptable jitter?
   *Answer: The PDB for 5QI=9 is 300ms. Jitter should be well below this (typically < 50ms) to maintain smooth playback. Video players use jitter buffers (200-500ms) to absorb variation, but excessive jitter causes buffering pauses.*

3. Why might DL UDP jitter be higher than UL UDP jitter?
   *Answer: DL traffic at the UPF competes with other UEs' traffic for scheduling. The UPF processes packets from the DN (potentially bursty), applies QoS rules, and encapsulates in GTP-U. UL traffic from a single UE is more predictable since it comes from the tester's iperf3 at a controlled rate.*
