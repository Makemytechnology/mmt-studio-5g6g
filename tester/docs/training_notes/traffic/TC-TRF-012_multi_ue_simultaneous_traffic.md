# TC-TRF-012: Multi-UE Simultaneous Traffic Test

## Overview
This test validates that multiple UEs (2+) can simultaneously carry TCP traffic through independent GTP-U tunnels. Each UE has its own PDU session, GTP-U tunnel, and UE IP. The test verifies that the UPF correctly forwards traffic for each UE independently.

## 3GPP Background
In production, a gNB serves hundreds of UEs simultaneously, each with independent data paths. The N3 link carries multiplexed GTP-U tunnels. The UPF demultiplexes by TEID (uplink) and destination IP (downlink).

Each UE's traffic is independently metered and policed (per-UE QER). One UE's traffic should not affect another UE's throughput (QoS isolation).

**Traffic path per UE:** UE_N -> TUN_N -> GTP-U (TEID_N) -> UPF -> Core

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7 | QoS model (per-UE) |
| TS 29.281 | 4 | GTP-U multi-tunnel |

## Problem Statement
- What if traffic from UE_1 leaks to UE_2's tunnel?
- What if one UE's throughput degrades when both are active?
- What if the UPF cannot demultiplex concurrent tunnels efficiently?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE_1 and UE_2, establish PDU sessions for both.
2. Start iperf3 server on SA Core.
3. Run iperf3 TCP sequentially for each UE (5s duration per UE).
4. Collect per-UE throughput results.

## Expected Behavior
- Both UEs achieve > 0 Mbps throughput.
- Each UE's traffic flows through its own GTP-U tunnel.
- No cross-UE traffic leakage.
- Aggregate throughput scales with UE count.

## Pass/Fail Criteria
- **Pass:** All UEs achieve throughput > 0; no failures.
- **Fail:** Any UE fails traffic test; cross-UE interference.

## Key Concepts for Training

### Per-UE Traffic Isolation
The UPF provides traffic isolation through independent PFCP sessions. Each UE's packets are matched by separate PDRs (different TEIDs/IPs) and processed by separate FARs and QERs. This ensures: (1) one UE's traffic cannot be received by another, (2) one UE's rate limit doesn't affect another, (3) one UE's failure doesn't impact another.

### Multi-UE Throughput Scaling
Ideal scaling: N UEs each achieving the same throughput as a single UE. In practice, sharing the N3 link and UPF processing limits aggregate throughput. If each UE gets 500 Mbps individually but only 400 Mbps with 2 UEs, the shared resources (link, CPU) are becoming bottlenecks.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Traffic leakage | Wrong UE receives data | Check TEID/IP mapping in UPF PDRs |
| Throughput degradation | UE_2 slower with UE_1 active | N3 link or UPF CPU bottleneck |
| Routing error | UE_2's traffic goes through UE_1's TUN | Check per-host routing and SO_BINDTODEVICE |

## References
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-TRF-001 (single UE), TC-STR-010 (4 UE traffic), TC-TRF-013 (sustained)

## Quiz Questions
1. How does the UPF distinguish uplink traffic from two different UEs?
   *Answer: By TEID in the GTP-U header. Each UE has a unique uplink TEID assigned by the UPF. The UPF matches the TEID against uplink PDRs to identify which UE/session the packet belongs to.*

2. If UE_1 and UE_2 each have 100 Mbps AMBR, what is the theoretical aggregate throughput on a 1 Gbps N3 link?
   *Answer: 200 Mbps (100 + 100). The N3 link has 800 Mbps headroom. AMBR is per-UE, and the aggregate depends on the link capacity and UPF processing.*

3. What is the most reliable way to ensure iperf3 traffic goes through the correct UE's GTP-U tunnel?
   *Answer: Use SO_BINDTODEVICE to bind the iperf3 socket to the specific UE's TUN interface. This ensures all traffic from that iperf3 instance exits through the correct TUN, regardless of routing table ambiguities.*
