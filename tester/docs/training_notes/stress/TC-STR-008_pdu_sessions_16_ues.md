# TC-STR-008: PDU Sessions 16 UEs

## Overview
This test scales PDU session establishment to 16 UEs on a single gNB. It validates the full data-plane stack at moderate scale: 16 registrations, 16 PDU sessions, 16 GTP-U tunnels, 16 IP allocations. This level typically reveals issues with resource pool management, PFCP session capacity, and system-level limits (file descriptors, TUN devices).

## 3GPP Background
At 16 UEs with PDU sessions, the system manages significant resources across all network functions. The SMF handles 16 Nsmf_PDUSession_CreateSMContext requests. The UPF creates 16 PFCP sessions with 32+ PDR/FAR rules. The gNB manages 16 GTP-U tunnel endpoints.

The N4 (PFCP) interface between SMF and UPF becomes a potential bottleneck: 16 PFCP Session Establishment exchanges, each involving multiple IEs (Create PDR, Create FAR, Create QER). If PFCP uses a single UDP socket, message serialization may slow session creation.

**Network functions involved:** 16 UEs, gNB, AMF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 29.244 | 5 | PFCP session scaling |
| TS 29.281 | 4 | GTP-U multi-tunnel |
| TS 23.501 | 5.6.1 | PDU sessions at scale |

## Problem Statement
- What if the kernel runs out of TUN devices at 16?
- What if the UPF's data plane cannot handle 16 concurrent forwarding rules?
- What if the SMF's IP pool is smaller than 16 addresses?
- What if system file descriptor limits are reached?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For each of 16 UEs (UE_1 through UE_16):
   a. Full registration and PDU session establishment.
   b. Record IP address.
3. All 16 UEs have active PDU sessions.

## Expected Behavior
- All 16 UEs register and receive unique IP addresses.
- 16 GTP-U tunnels operational simultaneously.
- UPF handles 16 PFCP sessions without degradation.
- No system resource limits hit.

## Pass/Fail Criteria
- **Pass:** All 16 UEs have active PDU sessions.
- **Fail:** Any UE fails; resource exhaustion detected.

## Key Concepts for Training

### System Resource Limits
At 16 UEs with GTP-U tunnels, the tester consumes: 16 TUN file descriptors, 16+ GTP-U sockets (or a shared socket), 16 routing table entries, memory for 16 UE contexts. Linux default ulimit is often 1024 file descriptors, which is sufficient. But if the tester also maintains SCTP connections and other resources, the total may approach limits.

### PFCP Scaling Characteristics
PFCP uses UDP (port 8805) between SMF and UPF. Each session establishment is a request/response pair. At 16 sessions, the critical path is the UPF's ability to install forwarding rules in its data plane. Hardware-accelerated UPFs (DPDK, SmartNIC) handle this quickly. Software UPFs may show latency at scale.

### IP Pool Sizing
For 16 UEs, the SMF needs at least 16 available addresses in the pool. A /28 subnet provides 14 usable addresses (insufficient). A /27 provides 30 (sufficient). Pool sizing must account for addresses in use, recently released addresses (grace period), and static allocations.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Pool too small | Sessions rejected after 14 UEs | Use /24 or larger pool |
| File descriptor limit | TUN creation fails | Increase ulimit -n to 4096+ |
| UPF data plane full | PFCP Establishment rejected | Increase UPF max sessions |
| Routing table overflow | Packets misrouted | Verify per-host routes for each TUN |

## References
- 3GPP TS 29.244 V17.x -- Section 5 (PFCP)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-STR-007 (8 UEs PDU), TC-STR-014 (32 UEs PDU), TC-STR-015 (64 UEs PDU)

## Quiz Questions
1. What is the minimum IP pool size (subnet mask) needed to serve 16 UEs?
   *Answer: A /27 subnet (32 addresses, 30 usable) is the minimum that safely accommodates 16 UEs. A /28 (16 addresses, 14 usable) is insufficient due to network and broadcast address reservation.*

2. How many PFCP Session Establishment Request messages does the SMF send to the UPF for 16 UEs?
   *Answer: 16 -- one per PDU session. Each request creates the PFCP session with PDR, FAR, QER, and URR rules for that UE's data path.*

3. What system-level limit is most likely to cause failure at 16 TUN interfaces, and how do you check it?
   *Answer: File descriptor limit (ulimit -n). Check with "ulimit -n" command. Each TUN interface consumes a file descriptor. The default is often 1024, which is sufficient for 16, but may be an issue if combined with other resource consumption. Also check /proc/sys/fs/file-max for the system-wide limit.*
