# TC-STR-007: PDU Sessions 8 UEs

## Overview
This test scales PDU session establishment to 8 UEs. Each UE completes full registration followed by internet PDU session setup. At 8 UEs, the test exercises GTP-U tunnel management, PFCP session scaling, and IP pool allocation at a level that begins to reveal resource management issues.

## 3GPP Background
With 8 UEs each having a PDU session, the system manages: 8 MM contexts (AMF), 8 SM contexts (SMF), 8 PFCP sessions (UPF), 8 GTP-U tunnels (gNB-UPF), 8 TUN interfaces (tester), and 8 IP addresses from the pool. The N4 interface between SMF and UPF handles 8 PFCP Session Establishment Request/Response pairs.

The UPF's forwarding plane must maintain 8 sets of PDR/FAR rules. Each uplink PDR matches on a specific GTP-U TEID; each downlink PDR matches on a UE IP destination address. The FAR specifies the forwarding action (GTP-U encapsulation parameters for downlink, decapsulation for uplink).

**Network functions involved:** 8 UEs, gNB, AMF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 29.244 | 5 | PFCP session management |
| TS 29.281 | 4 | GTP-U tunneling |
| TS 23.501 | 5.6.1 | PDU sessions |

## Problem Statement
- What if the UPF's fast-path forwarding table has limited entries?
- What if 8 concurrent TUN interfaces cause routing table conflicts?
- What if the SMF's PFCP message queue backs up with 8 session creations?
- What if IP addresses are not contiguous, causing subnet routing issues?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For each UE (UE_1 through UE_8):
   a. Full registration and PDU session establishment.
   b. Record IP address.
3. All 8 UEs have active PDU sessions.

## Expected Behavior
- All 8 UEs register and establish PDU sessions with unique IPs.
- 8 GTP-U tunnels operational simultaneously.
- No IP address conflicts or TEID collisions.
- UPF forwarding plane handles 8 concurrent sessions.

## Pass/Fail Criteria
- **Pass:** All 8 UEs have active PDU sessions with valid, unique IPs.
- **Fail:** Any UE fails; duplicate IPs; tunnel creation failure.

## Key Concepts for Training

### UPF Forwarding Plane Scaling
The UPF processes packets in the fast path using PDR matching. For 8 UEs, the UPF must check each incoming packet against 8 uplink PDRs (matched by TEID) or 8 downlink PDRs (matched by destination IP). Efficient implementations use hash tables or TCAM for O(1) lookup. Linear search becomes a bottleneck at scale.

### TUN Interface Management
Each PDU session creates a TUN (network tunnel) interface on the tester host. With 8 interfaces (e.g., tun0-tun7), the host routing table must correctly route packets from each UE's IP through its specific TUN interface. Misconfigured routes cause cross-UE packet leakage.

### Resource Accounting
At 8 UEs: 16 TEIDs (8 uplink + 8 downlink), 8 PFCP sessions with ~32 rules (4 per session), 8 IP addresses, 8 TUN interfaces, 8 NAS security contexts, 8 GTP-U sockets or shared socket with TEID demultiplexing.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| TUN routing conflict | Packets reach wrong UE | Check per-TUN routing table entries |
| UPF PDR limit | 8th session rejected | Increase UPF max PDR count |
| PFCP queue backup | Slow session creation | Check SMF-UPF N4 performance |
| File descriptor limit | TUN creation fails | Increase ulimit -n |

## References
- 3GPP TS 29.244 V17.x -- Section 5 (PFCP)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-STR-006 (4 UEs), TC-STR-008 (16 UEs), TC-STR-014 (32 UEs PDU)

## Quiz Questions
1. With 8 UEs and PDU sessions, how many PDR rules does the UPF maintain (minimum)?
   *Answer: 16 -- 2 per UE (1 uplink PDR matching on GTP-U TEID for decapsulation, 1 downlink PDR matching on UE IP for encapsulation). Each PDR has an associated FAR defining the forwarding action.*

2. Why might the 8th TUN interface fail to create even though the first 7 succeeded?
   *Answer: Possible causes: file descriptor limit reached (ulimit -n), kernel TUN/TAP device limit, /dev/net/tun permission issues after many opens, or namespace conflicts in TUN device naming.*

3. How does the tester route packets from 8 different UE IPs through 8 different TUN interfaces?
   *Answer: Each TUN interface has a unique UE IP address assigned. The host routing table has per-host routes (e.g., 10.45.0.2/32 via tun0, 10.45.0.3/32 via tun1). Applications use SO_BINDTODEVICE or explicit interface binding to ensure packets go through the correct TUN.*
