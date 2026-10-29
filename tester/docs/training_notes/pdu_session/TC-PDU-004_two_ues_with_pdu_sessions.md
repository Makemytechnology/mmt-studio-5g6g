# TC-PDU-004: Two-UE PDU Session Establishment

## Overview
This test validates that two independent UEs can each establish PDU sessions on the same gNB and receive distinct IP addresses. It verifies the network's ability to manage per-UE GTP-U tunnels, IP address allocation, and session contexts. This is essential for any multi-user deployment where the gNB serves multiple subscribers simultaneously.

## 3GPP Background
In a production 5G network, a single gNB serves hundreds or thousands of UEs, each with independent PDU sessions. The core network (SMF/UPF) must allocate unique resources for each UE:

- **Unique UE IP address:** Each UE gets a distinct IP from the SMF's address pool.
- **Unique GTP-U TEIDs:** Each UE's PDU session has unique uplink and downlink TEIDs on the N3 interface.
- **Independent PFCP sessions:** The SMF creates separate PFCP sessions on the UPF for each UE, with independent PDR/FAR/QER rules.
- **Independent NAS contexts:** Each UE has its own NAS security context (KAMF, KNASint, KNASenc) and PDU session context.

The NGAP UE context (RAN UE NGAP ID + AMF UE NGAP ID) ensures the gNB can multiplex signaling for both UEs over the same SCTP association.

**Network functions involved:** Two UEs, gNB, AMF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 38.413 | 8.2 | UE Context Management (multi-UE) |
| TS 29.281 | 4 | GTP-U per-UE tunnels |
| TS 23.501 | 5.6.1 | PDU sessions |
| TS 23.502 | 4.3.2 | PDU session procedure |

## Problem Statement
- What if the SMF allocates the same IP to both UEs (pool management bug)?
- What if GTP-U TEID allocation is not unique across UEs?
- What if the UPF's PFCP session table cannot handle concurrent sessions?
- What if the gNB confuses UE contexts when forwarding PDU session signaling?
- What if the second UE's registration disrupts the first UE's active PDU session?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. Register UE_1 via NAS, establish PDU session (DNN=internet, PSI=1). Record IP_1.
3. Register UE_2 via NAS (independent 5G-AKA on same gNB), establish PDU session (DNN=internet, PSI=1). Record IP_2.
4. Verify both UEs have active PDU sessions.
5. Verify IP_1 != IP_2.
6. Both GTP-U tunnels are operational independently.

## Expected Behavior
- UE_1 and UE_2 each receive unique IP addresses from the internet pool.
- Each UE has independent GTP-U tunnels with distinct TEIDs.
- The gNB maintains separate UE contexts (different RAN UE NGAP IDs).
- Neither UE's session establishment affects the other.
- Both UEs can independently use their PDU sessions.

## Pass/Fail Criteria
- **Pass:** Both UEs have active PDU sessions; IP_1 != IP_2; both sessions functional.
- **Fail:** Either UE fails PDU session; same IP assigned; one session disrupts the other.

## Key Concepts for Training

### Per-UE Resource Isolation
In 5G, each UE has completely independent resources: NAS security context, PDU session context, GTP-U tunnel, UE IP address, and PFCP session rules on the UPF. This isolation ensures that one UE's traffic, failures, or security compromise cannot affect another UE. The gNB tracks each UE via the (RAN UE NGAP ID, AMF UE NGAP ID) pair.

### IP Address Pool Management
The SMF maintains address pools per DNN. Each pool has a finite range (e.g., 10.45.0.0/16 for internet). Addresses are allocated on PDU session creation and released on session deletion. The SMF must track allocated addresses to prevent duplicates. In high-density deployments, pool exhaustion is a real concern.

### GTP-U Forwarding Per UE
The gNB maintains a forwarding table mapping each UE's IP/QFI to the corresponding GTP-U tunnel (TEID + UPF transport address). Uplink packets from UE_1 are encapsulated with UE_1's uplink TEID; packets from UE_2 use UE_2's TEID. The UPF uses the TEID to identify which UE the packet belongs to and applies the correct PDR/QER rules.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Duplicate IP allocation | IP_1 == IP_2, test fails | Check SMF address pool allocation logic |
| TEID collision | Packets misrouted between UEs | Verify unique TEID generation per session |
| Context confusion | Wrong UE receives response | Check RAN UE NGAP ID assignment logic |
| Second UE disrupts first | First UE's session drops | Verify session independence in AMF/SMF |
| Pool exhaustion | Second UE's PDU rejected | Release stale sessions, expand pool |

## References
- 3GPP TS 24.501 V17.x -- Section 6.4.1 (PDU Session Establishment)
- 3GPP TS 38.413 V17.x -- Section 8.2 (UE Context Management)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-PDU-001 (single UE internet), TC-REG-002 (multi-UE registration), TC-STR-006 (4 UE PDU)

## Quiz Questions
1. Both UE_1 and UE_2 request PDU sessions with PSI=1 on the same gNB. Is this valid?
   *Answer: Yes. The PSI is unique per UE, not globally. UE_1's PSI=1 and UE_2's PSI=1 are completely independent because they belong to different UE contexts (different RAN UE NGAP IDs, different NAS contexts, different PFCP sessions).*

2. How does the UPF distinguish between uplink packets from UE_1 and UE_2 when both arrive on the N3 interface?
   *Answer: Each UE's uplink packets are encapsulated with a unique GTP-U TEID. The UPF matches the TEID in the GTP-U header against its PDR (Packet Detection Rule) table to identify which UE and PDU session the packet belongs to, then applies the corresponding forwarding and QoS rules.*

3. If UE_1 has IP 10.45.0.2 and UE_2 has IP 10.45.0.3, and a downlink packet arrives at the UPF for 10.45.0.3, how does the UPF route it?
   *Answer: The UPF matches the destination IP 10.45.0.3 against its downlink PDR rules. The matching PDR identifies UE_2's PFCP session, and the associated FAR (Forwarding Action Rule) specifies the GTP-U tunnel parameters (gNB transport address and downlink TEID) for UE_2. The UPF encapsulates the packet with UE_2's TEID and sends it to the gNB.*
