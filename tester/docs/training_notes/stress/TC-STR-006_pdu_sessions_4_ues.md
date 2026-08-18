# TC-STR-006: PDU Sessions 4 UEs

## Overview
This test validates the registration and PDU session establishment for 4 UEs. Beyond simple registration, each UE establishes a data-plane PDU session, receiving an IP address and GTP-U tunnel. This tests the SMF's session management, UPF's PFCP capacity, and IP address pool allocation under multi-UE conditions.

## 3GPP Background
Each PDU session involves coordination between the AMF, SMF, and UPF. The SMF creates PFCP session rules (PDR, FAR, QER, URR) on the UPF for each UE. At 4 UEs, the UPF maintains 4 independent PFCP sessions with distinct forwarding rules and GTP-U tunnels.

Resource allocation per UE:
- 1 UE IP address from the internet pool
- 1 uplink TEID (gNB -> UPF) and 1 downlink TEID (UPF -> gNB)
- 1 PFCP session with at minimum 2 PDRs (UL/DL), 2 FARs, and QER/URR rules
- 1 TUN interface on the tester

Total resources for 4 UEs: 4 IP addresses, 8 TEIDs, 4 PFCP sessions, 4 TUN interfaces.

**Network functions involved:** 4 UEs, gNB, AMF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 23.502 | 4.3.2 | PDU session procedure |
| TS 29.244 | 5 | PFCP session management |
| TS 29.281 | 4 | GTP-U per-UE tunnels |
| TS 23.501 | 5.6.1 | PDU sessions |

## Problem Statement
- What if the SMF's IP pool cannot allocate 4 unique addresses?
- What if the UPF's PFCP session table is limited?
- What if GTP-U TEID allocation produces duplicates across 4 UEs?
- What if TUN interface creation fails after the first 2-3 UEs?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For each UE (UE_1 through UE_4):
   a. Perform full registration and PDU session establishment.
   b. Record allocated IP address.
3. All 4 UEs have active PDU sessions with unique IPs.

## Expected Behavior
- All 4 UEs register and establish PDU sessions.
- Each UE receives a unique IP address.
- 4 independent GTP-U tunnels are operational.
- UPF maintains 4 PFCP sessions with correct forwarding rules.

## Pass/Fail Criteria
- **Pass:** All 4 UEs have active PDU sessions with valid IPs.
- **Fail:** Any UE fails registration or PDU session; IP duplication.

## Key Concepts for Training

### PFCP Session Management
The PFCP (Packet Forwarding Control Protocol, TS 29.244) is used between the SMF and UPF on the N4 interface. For each PDU session, the SMF creates a PFCP session containing: PDRs (Packet Detection Rules) that match incoming packets, FARs (Forwarding Action Rules) that define where to send them, QERs (QoS Enforcement Rules) for rate limiting, and URRs (Usage Reporting Rules) for billing.

### IP Address Pool Exhaustion
The SMF maintains IP address pools per DNN. A typical pool might be a /24 (254 addresses) or /16 (65534 addresses). At 4 UEs, exhaustion is unlikely, but the allocation mechanism is exercised: allocate on session create, release on session delete. Persistent address leaks compound over time.

### GTP-U Tunnel Scaling
Each PDU session creates at least one GTP-U tunnel (N3 interface). The gNB and UPF each allocate a TEID for their endpoint. With 4 UEs, there are 4 tunnels. The gNB must route uplink packets from each UE to the correct UPF using the correct TEID, and the UPF must route downlink packets to the correct gNB TEID.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| IP pool exhaustion | 4th UE gets no IP | Expand pool or release stale sessions |
| PFCP session failure | SMF can't create session on UPF | Check N4 connectivity and UPF capacity |
| TUN interface limit | OS rejects 4th TUN device | Check /dev/net/tun permissions and limits |
| TEID collision | Packets misrouted | Verify TEID uniqueness per session |

## References
- 3GPP TS 29.244 V17.x -- Section 5 (PFCP)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-STR-007 (8 UEs), TC-STR-008 (16 UEs), TC-PDU-004 (2 UEs PDU)

## Quiz Questions
1. For 4 UEs with PDU sessions, how many PFCP sessions exist on the UPF?
   *Answer: 4 -- one per PDU session. Each PFCP session contains the forwarding rules (PDR/FAR/QER/URR) specific to that UE's data path.*

2. What happens if the SMF's IP pool for the "internet" DNN only has 3 available addresses when 4 UEs request sessions?
   *Answer: The 4th UE's PDU Session Establishment Request is rejected with cause #26 (insufficient resources). The UE receives a PDU Session Establishment Reject message and must retry later.*

3. How many GTP-U TEIDs are in use with 4 UEs, and who allocates them?
   *Answer: 8 TEIDs total -- 2 per UE (1 uplink TEID allocated by the UPF, 1 downlink TEID allocated by the gNB). The UPF's uplink TEID is sent to the gNB so it knows where to forward encapsulated uplink packets. The gNB's downlink TEID is sent to the UPF for the reverse direction.*
