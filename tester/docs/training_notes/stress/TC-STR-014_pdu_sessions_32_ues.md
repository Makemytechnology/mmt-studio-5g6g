# TC-STR-014: PDU Sessions 32 UEs

## Overview
This test scales registration and PDU session establishment to 32 UEs using dynamically generated IMSIs. Each UE gets a full data-plane path (GTP-U tunnel, IP address, PFCP session). At 32 UEs, the UPF's forwarding table contains 32 PFCP sessions with 64+ PDR rules, and the tester manages 32 TUN interfaces.

## 3GPP Background
With 32 PDU sessions, the system manages significant data-plane resources. The UPF's fast-path forwarding engine must efficiently classify packets across 32 sessions. Uplink: 32 TEID-based PDRs for GTP-U decapsulation. Downlink: 32 IP-based PDRs for GTP-U encapsulation. The SMF-UPF N4 interface processes 32 PFCP Session Establishment exchanges.

The IP address pool must accommodate 32 addresses. A /26 subnet (64 addresses, 62 usable) is the minimum safe size. The tester host must manage 32 TUN interfaces with 32 host routes and proper interface binding.

**Network functions involved:** 32 UEs (dynamic IMSI), gNB, AMF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 29.244 | 5 | PFCP scaling |
| TS 29.281 | 4 | GTP-U multi-tunnel |
| TS 23.501 | 5.6.1 | PDU sessions at scale |

## Problem Statement
- What if the UPF's PDR table has a practical limit below 64 entries?
- What if 32 TUN interfaces cause kernel routing table issues?
- What if the IP pool for 32 UEs is undersized?
- What if PFCP session creation becomes a bottleneck at 32 sessions?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For i from 1 to 32:
   a. Generate IMSI, perform full registration.
   b. Establish PDU session, record IP.
3. All 32 UEs have active PDU sessions.

## Expected Behavior
- All 32 UEs register and establish PDU sessions with unique IPs.
- UPF handles 32 concurrent PFCP sessions.
- 32 TUN interfaces and GTP-U tunnels operational.

## Pass/Fail Criteria
- **Pass:** All 32 UEs have active PDU sessions.
- **Fail:** Any UE fails; IP exhaustion; PFCP failure.

## Key Concepts for Training

### UPF Forwarding Table Design
At 32 sessions (64+ PDRs), the UPF's forwarding table design matters. Options: (1) Linear scan -- O(n) per packet, too slow at scale. (2) Hash table on TEID/IP -- O(1) per packet, optimal. (3) Ternary CAM (TCAM) -- hardware-based, fastest but limited entries. Most software UPFs use hash tables, which scale well to thousands of sessions.

### Host Network Stack at Scale
The tester host manages 32 TUN interfaces. The kernel routing table has 32 per-host routes. Applications must use SO_BINDTODEVICE or source-based routing to direct traffic through the correct TUN. At 32 interfaces, the standard Linux network stack handles this efficiently, but routing table lookup time increases slightly.

### PFCP Session Establishment Throughput
Each PFCP session establishment involves: Create PDR (UL/DL), Create FAR (UL/DL), Create QER, optionally Create URR. A typical message is ~200-500 bytes. At 32 sessions, the SMF sends ~32 PFCP requests, totaling ~6-16 KB. This is not a bandwidth bottleneck but may be latency-sensitive if the UPF takes time to install rules in its data plane.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| IP pool exhausted | Sessions rejected after ~28 UEs | Use /24 pool (254 addresses) |
| TUN interface limit | Creation fails after ~30 | Check kernel TUN device limit |
| UPF PDR limit | PFCP rejected | Increase UPF max-pdrs config |
| PFCP timeout | SMF logs N4 timeout | Check N4 path latency and UPF health |

## References
- 3GPP TS 29.244 V17.x -- Section 5 (PFCP)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-STR-008 (16 UEs PDU), TC-STR-015 (64 UEs PDU), TC-STR-005 (32 UEs reg only)

## Quiz Questions
1. What is the minimum IP pool subnet size to safely accommodate 32 UEs?
   *Answer: /26 (64 addresses, 62 usable). A /27 (32 addresses, 30 usable) might work if no addresses are reserved, but leaves no margin. /26 provides comfortable headroom.*

2. At 32 PDU sessions, how many PDR rules does the UPF maintain?
   *Answer: At minimum 64 (2 per session: 1 uplink PDR matching on TEID, 1 downlink PDR matching on UE IP). In practice, additional PDRs may exist for specific QoS flows or tunneled traffic.*

3. Why might PFCP session establishment become a bottleneck at 32 sessions even though message sizes are small?
   *Answer: Each PFCP session establishment requires the UPF to install forwarding rules in its data plane. If the UPF uses kernel-based forwarding (iptables, nftables), each rule modification may trigger a kernel lock and table rebuild. Hardware UPFs may need to program TCAM entries. The latency per rule installation, not message size, is the bottleneck.*
