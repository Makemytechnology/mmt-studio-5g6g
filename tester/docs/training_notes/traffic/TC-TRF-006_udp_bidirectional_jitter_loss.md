# TC-TRF-006: UDP Bidirectional Throughput with QoS Metrics

## Overview
This test measures simultaneous UDP uplink and downlink throughput with jitter and packet loss. Bidirectional UDP is the closest approximation to a real-time voice or video call, where both directions carry media simultaneously. It validates that the GTP-U tunnel handles full-duplex UDP without QoS degradation.

## 3GPP Background
Real-time bidirectional applications (VoNR calls, video conferencing) send UDP in both directions simultaneously. Each direction uses independent GTP-U TEIDs and QER rules. The 5QI framework ensures QoS isolation: a GBR voice flow (5QI=1) should not be impacted by non-GBR internet traffic.

This test uses 5QI=9 (default bearer) for both directions at 50 Mbps each. The UPF independently enforces UL and DL Session-AMBR.

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7 | QoS model |
| TS 29.281 | 4 | GTP-U bidirectional |

## Problem Statement
- What if bidirectional UDP causes higher jitter than unidirectional?
- What if one direction's loss increases when both are active?
- What if the GTP-U socket cannot handle full-duplex UDP at 50 Mbps each?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish internet PDU session.
2. Start iperf3 server on SA Core.
3. Run iperf3 UDP client with --bidir at 50 Mbps.
4. Measure jitter and loss for both TX (UL) and RX (DL).

## Expected Behavior
- Both UL and DL: jitter < 50ms, loss < 1%.
- Both directions achieve > 0 Mbps throughput.
- No cross-direction interference.

## Pass/Fail Criteria
- **Pass:** Jitter < 50ms and loss < 1% in both directions.
- **Fail:** Either direction exceeds jitter/loss thresholds.

## Key Concepts for Training

### Full-Duplex QoS Isolation
The UPF processes UL and DL independently: separate PDRs, FARs, and QERs per direction. This ensures that UL congestion does not affect DL quality and vice versa. Bidirectional testing validates this isolation -- if DL jitter increases only when UL is active, the isolation is incomplete.

### Bidirectional Jitter Budget
For a VoNR call (5QI=1, PDB=100ms), one-way delay must be < 100ms. Bidirectional jitter contributions from both directions must stay within this budget. If UL jitter = 30ms and DL jitter = 30ms, total round-trip jitter is acceptable. But if either exceeds 50ms, one-way delay may violate the PDB.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Cross-direction jitter | DL jitter increases with UL active | Check GTP-U socket multiplexing |
| Increased loss | Loss > 1% in bidir but not unidir | Increase socket buffers for both directions |
| Throughput reduction | 50% of unidirectional rate | N3 link bandwidth saturated |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7 (QoS)
- Related: TC-TRF-004 (UL UDP), TC-TRF-005 (DL UDP), TC-TRF-003 (TCP bidir)

## Quiz Questions
1. In bidirectional UDP at 50 Mbps each direction, what is the aggregate demand on the N3 link?
   *Answer: 100 Mbps (50 UL + 50 DL). Plus GTP-U overhead (~3-4%), so approximately 104 Mbps total on the wire.*

2. If UL jitter is 20ms in unidirectional test but 45ms in bidirectional, what does this suggest?
   *Answer: Cross-direction interference. The DL traffic is competing with UL for shared resources (GTP-U socket, CPU, network interface). The QoS isolation between directions is incomplete.*

3. For a VoNR call (5QI=1), what is the maximum acceptable one-way delay, and how does bidirectional jitter affect it?
   *Answer: PDB=100ms for 5QI=1, meaning one-way delay must be < 100ms. If bidirectional jitter is 30ms in each direction, the total round-trip delay variation is up to 60ms. The actual one-way delay = propagation + processing + jitter. Jitter must be small enough that one-way delay stays below PDB.*
