# TC-STR-015: PDU Sessions 64 UEs

## Overview
This large-scale test establishes registration and PDU sessions for 64 UEs. It pushes the entire data-plane stack to its limits: 64 PFCP sessions, 64 GTP-U tunnels, 64 TUN interfaces, 64 IP addresses. This test reveals capacity limits in the UPF, SMF, and tester infrastructure that are not visible at smaller scales.

## 3GPP Background
At 64 UEs with PDU sessions, the UPF manages 128+ PDR rules (2 per UE), 128+ FAR rules, 64 QER rules, and potentially 64 URR rules for usage reporting. The total PFCP state is substantial. The gNB maintains 64 GTP-U tunnel endpoints, and the tester manages 64 TUN interfaces.

The N3 interface carries 64 multiplexed GTP-U tunnels. If each UE generates even modest traffic (1 Mbps), the aggregate is 64 Mbps. At full AMBR (e.g., 100 Mbps each), the theoretical aggregate is 6.4 Gbps, exceeding typical test environment link capacity.

IP pool sizing becomes critical: the SMF must have at least 64 available addresses. A /26 subnet (62 usable) is insufficient. A /25 (126 usable) or /24 (254 usable) is required.

**Network functions involved:** 64 UEs (dynamic IMSI), gNB, AMF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 29.244 | 5 | PFCP at scale |
| TS 29.281 | 4 | GTP-U multi-tunnel |
| TS 23.501 | 5.6.1 | PDU sessions |
| TS 23.501 | 5.7 | QoS at scale |

## Problem Statement
- What if the UPF cannot install 128+ PDR rules?
- What if 64 TUN interfaces exceed Linux kernel limits?
- What if the IP pool has fewer than 64 available addresses?
- What if total test time exceeds practical limits (>10 minutes)?
- What if the tester host runs out of file descriptors?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For i from 1 to 64:
   a. Generate IMSI, perform full registration.
   b. Establish PDU session, record IP.
3. All 64 UEs have active PDU sessions.

## Expected Behavior
- All 64 UEs have active PDU sessions with unique IPs.
- UPF handles 64 PFCP sessions without rejection.
- 64 TUN interfaces created and routed correctly.

## Pass/Fail Criteria
- **Pass:** All 64 UEs have active PDU sessions.
- **Fail:** Any UE fails; resource exhaustion; timeout.

## Key Concepts for Training

### System-Level Resource Limits at Scale
At 64 UEs: 64 TUN file descriptors + GTP-U sockets + SCTP + logging = potentially 100+ file descriptors. Default ulimit (1024) is sufficient. But kernel-level limits matter too: /proc/sys/net/ipv4/ip_forward must be enabled, net.core.rmem_max/wmem_max should be tuned, and the routing cache must handle 64+ entries.

### PFCP Session State Size
Each PFCP session on the UPF consumes memory for its rules. A minimal session (2 PDR + 2 FAR + 1 QER) is ~500 bytes to 2 KB depending on implementation. At 64 sessions: 32-128 KB. This is small, but the data-plane representation (forwarding rules in kernel/DPDK) may be much larger.

### IP Pool Planning for Production
This test demonstrates why IP pool planning matters. For a production deployment with 10,000+ UEs per SMF, the pool must be correspondingly large. Using /16 (65534 addresses) is common for production. Pool fragmentation (many allocated, few contiguous free) can also cause issues.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| IP pool too small | Rejects after ~62 UEs | Use /24 or larger pool |
| File descriptor limit | TUN creation fails | Increase ulimit -n to 4096 |
| UPF rule limit | PFCP rejected with "No Resources" | Increase UPF max-rules config |
| Kernel route limit | Route add fails | Check ip_route_max_size sysctl |
| Test timeout | >10 minutes total | Optimize per-UE setup time |

## References
- 3GPP TS 29.244 V17.x -- Section 5 (PFCP)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-STR-014 (32 UEs PDU), TC-STR-008 (16 UEs PDU), TC-STR-012 (64 UEs reg)

## Quiz Questions
1. What is the minimum IP pool subnet to safely accommodate 64 UEs?
   *Answer: /25 (128 addresses, 126 usable). A /26 only provides 62 usable addresses, which is insufficient for 64 UEs.*

2. At 64 UEs, approximately how many forwarding rules does the UPF maintain?
   *Answer: At minimum 128 PDRs + 128 FARs + 64 QERs = 320 rules. Including URRs and any additional QoS-specific rules, the total could be 400+.*

3. If the test fails at UE #63 with a "Resource Unavailable" PFCP error, what should you check?
   *Answer: (1) UPF max session/rule configuration limits, (2) UPF memory usage, (3) PFCP session table capacity, (4) Whether the UPF's data plane (DPDK buffer pool, flow table) has a hardcoded limit, (5) IP pool availability (addresses remaining).*
