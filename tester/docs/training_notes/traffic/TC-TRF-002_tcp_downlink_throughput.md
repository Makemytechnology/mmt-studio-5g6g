# TC-TRF-002: TCP Downlink Throughput Test

## Overview
This test measures TCP downlink throughput from the core network to the UE through the GTP-U tunnel. Unlike uplink (TC-TRF-001), the traffic originates at the core and is encapsulated by the UPF for delivery to the UE. This validates the reverse data path: Core -> UPF -> GTP-U -> TUN -> UE application.

## 3GPP Background
Downlink traffic in 5G flows: Data Network -> UPF (N6) -> GTP-U encapsulation (N3) -> gNB -> UE. The UPF matches incoming packets from the DN against downlink PDRs (by destination UE IP). The matching FAR instructs the UPF to encapsulate the packet with GTP-U using the gNB's downlink TEID and forward it via UDP:2152.

The gNB receives the GTP-U packet, decapsulates it, and delivers it to the UE through the TUN interface. In a real network, the gNB maps to radio bearers; in the tester, it delivers to the TUN interface.

**Traffic path:** Core iperf3 -> UPF -> GTP-U encap -> UDP:2152 -> gNB -> TUN -> UE App

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 29.281 | 4 | GTP-U protocol |
| TS 23.501 | 5.7 | QoS model |
| TS 23.501 | 5.7.2.6 | Session-AMBR (DL) |

## Problem Statement
- What if the UPF's downlink PDR does not match the UE IP?
- What if the core iperf3 cannot route to the UE IP through the UPF?
- What if DL Session-AMBR limits throughput unexpectedly?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish internet PDU session.
2. Start iperf3 server on UE side (bound to UE TUN IP).
3. Trigger core-side iperf3 client via API to send to UE IP.
4. Traffic flows: Core -> UPF -> GTP-U -> TUN -> UE iperf3 server.
5. Collect throughput and UPF DL statistics.

## Expected Behavior
- Core iperf3 sends TCP stream to UE IP.
- UPF encapsulates in GTP-U and forwards to gNB.
- UE iperf3 server receives the stream.
- Throughput > 50 Mbps on local network.
- UPF dl_dropped = 0.

## Pass/Fail Criteria
- **Pass:** Downlink throughput > 0 Mbps; UPF DL bytes > 0; zero drops.
- **Fail:** No throughput; UPF cannot route to UE.

## Key Concepts for Training

### Downlink PDR Matching
The UPF's downlink PDR matches on the UE's IP address as the destination. When a packet from the DN arrives at the UPF, it checks all downlink PDRs for a matching destination IP. The matching PDR's FAR specifies: encapsulation type (GTP-U), outer header (gNB IP + downlink TEID), and forwarding action (forward to N3).

### Downlink Session-AMBR
The DL Session-AMBR limits total downlink throughput per PDU session. It is enforced by the UPF's QER. If the core sends faster than the DL AMBR, the UPF drops or shapes excess packets. UPF dl_dropped or dl_metered counters indicate enforcement.

### Core-Initiated Traffic
Unlike uplink (UE-initiated), downlink traffic is triggered from the core network. This requires the core iperf3 to know the UE's IP address and have a route through the UPF. The SA Core tester API provides endpoints to start iperf3 servers and clients for this purpose.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| No DL routing | Core can't reach UE IP | Verify UPF has downlink PDR for UE IP |
| GTP-U TEID wrong | Packets lost at gNB | Verify downlink TEID exchange |
| DL AMBR enforcement | Low throughput | Check subscription DL AMBR value |
| TUN not receiving | iperf3 server times out | Verify TUN interface is up, routing correct |

## References
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-TRF-001 (uplink), TC-TRF-003 (bidirectional), TC-TRF-009 (MBR DL)

## Quiz Questions
1. How does the UPF determine which GTP-U TEID to use for a downlink packet to a specific UE?
   *Answer: The UPF matches the destination IP against downlink PDRs. The matching PDR links to a FAR that specifies the gNB's downlink TEID (provided during PDU Session Resource Setup) and gNB transport address for GTP-U encapsulation.*

2. What is the difference between uplink and downlink GTP-U TEIDs?
   *Answer: The uplink TEID is allocated by the UPF (used by the gNB to send uplink packets to the UPF). The downlink TEID is allocated by the gNB (used by the UPF to send downlink packets to the gNB). Each direction has its own TEID.*

3. If the core sends 100 Mbps to the UE but the UE only receives 50 Mbps, what could cause this?
   *Answer: DL Session-AMBR or MBR enforcement at the UPF (subscription set to 50 Mbps), N3 link congestion, GTP-U overhead reducing effective throughput, or UPF processing limits.*
