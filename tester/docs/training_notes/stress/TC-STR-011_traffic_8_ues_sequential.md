# TC-STR-011: Traffic 8 UEs Sequential

## Overview
This test scales traffic-ready PDU session establishment to 8 UEs. Each UE completes registration and PDU session setup, creating a full data-plane path. At 8 UEs, the test pushes TUN interface management, GTP-U tunnel capacity, and UPF forwarding table size further than TC-STR-010.

## 3GPP Background
With 8 UEs and PDU sessions, the gNB-UPF data plane carries 8 multiplexed GTP-U tunnels over the N3 interface. The shared UDP transport (port 2152) must handle traffic for all 8 tunnels. The UPF's packet processing pipeline performs TEID-based demultiplexing for uplink and IP-based classification for downlink.

At 8 UEs, aggregate throughput considerations emerge. If each UE has a 100 Mbps Session-AMBR, the theoretical aggregate is 800 Mbps. The N3 transport link and UPF processing capacity must support this aggregate.

**Network functions involved:** 8 UEs, gNB, AMF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7 | QoS model |
| TS 29.281 | 4 | GTP-U multi-tunnel |
| TS 23.501 | 5.7.2.6 | Session-AMBR |
| TS 29.244 | 5 | PFCP session scaling |

## Problem Statement
- What if aggregate traffic from 8 UEs exceeds the N3 link capacity?
- What if TUN interface naming collisions occur at 8 devices?
- What if the UPF's packet classifier degrades with 8 PDR entries?
- What if the host routing table becomes inconsistent with 8 per-host routes?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For each UE (UE_1 through UE_8):
   a. Full registration and PDU session establishment.
   b. Record PDU session IP address.
3. All 8 UEs have active PDU sessions.

## Expected Behavior
- All 8 UEs have active PDU sessions with unique IPs.
- 8 GTP-U tunnels and 8 TUN interfaces operational.
- UPF forwarding table contains 8 sessions with correct rules.

## Pass/Fail Criteria
- **Pass:** All 8 UEs have active PDU sessions.
- **Fail:** Any UE fails to establish PDU session.

## Key Concepts for Training

### Aggregate Throughput Capacity
When multiple UEs share the same N3 transport link, their aggregate throughput is bounded by the link capacity. For 8 UEs each with 100 Mbps AMBR on a 1 Gbps N3 link, the aggregate (800 Mbps) approaches the link limit. QoS scheduling at the gNB and UPF determines how bandwidth is shared under contention.

### GTP-U Multiplexing Efficiency
Multiple GTP-U tunnels share a single UDP socket (port 2152). The GTP-U header (8+ bytes) plus UDP/IP headers (28 bytes) add per-packet overhead. With 8 UEs generating small packets (e.g., VoIP), the overhead ratio is higher than with fewer UEs generating large packets. This affects effective throughput.

### Forwarding Table Efficiency
The UPF's PDR table is the primary forwarding structure. At 8 entries, linear search is still fast. But the test establishes a baseline: if 8 PDRs work, how does performance scale to 80, 800, or 8000? This scaling characterization is essential for production planning.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| TUN naming conflict | Interface creation fails | Use unique names (ue1_tun, ue2_tun, etc.) |
| Routing table chaos | Traffic misrouted between UEs | Verify per-host routes with "ip route show" |
| UPF throughput limit | Aggregate traffic drops packets | Check UPF hardware/software capacity |
| GTP-U socket buffer | Packets dropped at socket | Increase SO_RCVBUF on GTP-U socket |

## References
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- 3GPP TS 23.501 V17.x -- Section 5.7 (QoS)
- Related: TC-STR-010 (4 UEs), TC-TRF-012 (multi-UE traffic), TC-STR-014 (32 UEs PDU)

## Quiz Questions
1. With 8 UEs each having 100 Mbps Session-AMBR, what is the theoretical aggregate throughput demand on the N3 link?
   *Answer: 800 Mbps. If the N3 link is 1 Gbps Ethernet, this leaves only 200 Mbps headroom. In practice, GTP-U/UDP/IP header overhead further reduces available capacity.*

2. How does the UPF handle multiple GTP-U tunnels arriving on the same UDP port 2152?
   *Answer: The UPF reads the TEID field from the GTP-U header in each received packet. The TEID is matched against the uplink PDR rules to identify which UE/session the packet belongs to. Each TEID maps to a specific PFCP session with its own QoS and forwarding rules.*

3. If 7 of 8 UEs can successfully ping through their GTP-U tunnels, what debugging steps would you take for the failing 8th UE?
   *Answer: (1) Verify TUN interface exists (ip link show), (2) Check UE IP assignment (ip addr), (3) Verify routing table entry for that UE's IP, (4) Check GTP-U TEID was received from AMF, (5) Verify PFCP session was created on UPF for this UE, (6) Capture packets on TUN and GTP-U socket to isolate where packets are lost.*
