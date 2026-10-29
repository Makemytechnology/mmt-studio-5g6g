# TC-PDU-001: Internet PDU Session Establishment

## Overview
This test validates the establishment of a PDU session for internet data connectivity. It exercises the complete procedure from PDU Session Establishment Request through IP address allocation and GTP-U tunnel creation. A PDU session is the fundamental data connectivity path in 5G -- without it, a registered UE cannot exchange user-plane traffic.

## 3GPP Background
A PDU session (TS 24.501 Section 6.4.1, TS 23.502 Section 4.3.2) provides a data path between the UE and the Data Network (DN). For internet access, the DNN (Data Network Name) is typically "internet."

The procedure involves:
1. **UE -> AMF:** PDU Session Establishment Request (NAS), carrying PSI (PDU Session Identity), requested DNN, S-NSSAI, PDU session type (IPv4/IPv6/IPv4v6).
2. **AMF -> SMF:** Nsmf_PDUSession_CreateSMContext (selects SMF based on DNN/S-NSSAI).
3. **SMF:** Allocates UE IP address from the DN pool, selects UPF, creates PFCP session rules (PDR, FAR, QER, URR) on the UPF via N4.
4. **SMF -> AMF -> gNB:** PDU Session Resource Setup Request (NGAP) with GTP-U tunnel information (UPF TEID, UPF transport address), QoS flow descriptions (QFI, 5QI).
5. **gNB:** Creates downlink GTP-U tunnel to UPF, creates TUN interface for the UE, sends PDU Session Resource Setup Response with gNB-side TEID.
6. **AMF -> UE:** PDU Session Establishment Accept (NAS) with assigned UE IP address, QoS rules, and session parameters.

**Default QoS:** For internet DNN, the default QoS flow typically uses 5QI=9 (non-GBR, best effort, PDB=300ms).

**Network functions involved:** UE, gNB, AMF, SMF, UPF
**Interfaces:** N1 (UE-AMF NAS), N2 (gNB-AMF NGAP), N4 (SMF-UPF PFCP), N3 (gNB-UPF GTP-U), N6 (UPF-DN)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 6.4.1 | PDU Session Establishment Request/Accept |
| TS 23.502 | 4.3.2 | PDU session establishment procedure |
| TS 23.501 | 5.6.1 | PDU session concept |
| TS 29.281 | 4 | GTP-U protocol (UDP port 2152) |
| TS 23.501 | 5.7.2.1 | 5QI to QoS characteristics mapping |

## Problem Statement
- What if the SMF cannot allocate a UE IP address (address pool exhausted)?
- What if the UPF does not respond to PFCP session establishment?
- What if the GTP-U tunnel TEID is not unique, causing packet routing errors?
- What if the DNN "internet" is not configured on the SMF?
- What if the UE requests IPv6 but only IPv4 is available?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. Register UE via full NAS procedure (5G-AKA, Security Mode, Registration Accept).
3. UE sends PDU Session Establishment Request with PSI=1, DNN=internet.
4. SMF selects UPF, allocates UE IP address from the internet pool.
5. SMF creates PFCP session on UPF with PDR/FAR rules for UL/DL forwarding.
6. AMF sends PDU Session Resource Setup Request to gNB (GTP-U TEID, QFI=1).
7. gNB creates GTP-U tunnel, allocates TUN interface with UE IP address.
8. PDU Session Establishment Accept received with UE IP address.
9. Verify PDU session is active and UE has a valid (non-"unknown") IP address.

## Expected Behavior
- SMF allocates a routable UE IP address from the internet DNN pool.
- GTP-U tunnel is established between gNB and UPF (UDP port 2152).
- TUN interface is created on the tester with the allocated UE IP.
- PDU Session Establishment Accept carries the UE IP address.
- Default QoS flow (QFI=1, 5QI=9) is established.

## Pass/Fail Criteria
- **Pass:** PDU session is active; UE IP address is valid (not "unknown"); GTP-U tunnel TEID assigned.
- **Fail:** No IP address allocated; PDU session establishment times out; GTP-U tunnel not created.

## Key Concepts for Training

### GTP-U Tunneling
GTP-U (GPRS Tunnelling Protocol - User Plane) encapsulates user data in GTP-U headers (TEID field for tunnel identification) over UDP port 2152. Each PDU session has at least one GTP-U tunnel between the gNB (N3 interface) and the UPF. The TEID (Tunnel Endpoint Identifier) is a 32-bit value that uniquely identifies the tunnel endpoint. Uplink packets from the UE are encapsulated in GTP-U by the gNB and sent to the UPF; downlink packets from the DN are encapsulated by the UPF and sent to the gNB.

### DNN and IP Address Allocation
The DNN (Data Network Name) is the 5G equivalent of the LTE APN. It identifies the data network the UE wants to connect to. The SMF maintains IP address pools per DNN. For "internet" DNN, addresses are typically from private ranges (10.x.x.x) that are NATed at the UPF. The UE receives its IP address in the PDU Session Establishment Accept message.

### QoS Flows and 5QI
Each PDU session contains one or more QoS flows, identified by QFI (QoS Flow Identifier). Each flow maps to a 5QI (5G QoS Identifier) that defines characteristics like priority, packet delay budget, and packet error rate. The default internet flow uses 5QI=9 (non-GBR, best effort).

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| IP pool exhausted | PDU Session Reject (cause #26) | Expand SMF IP pool or release stale sessions |
| DNN not configured | PDU Session Reject (cause #27) | Add "internet" DNN to SMF configuration |
| UPF unreachable | PFCP timeout at SMF | Check N4 connectivity between SMF and UPF |
| GTP-U tunnel fail | No TUN interface created | Verify kernel TUN module loaded, permissions |
| Stale GTP-U tunnel | Packet routing to wrong UE | Destroy old tunnels before creating new ones |

## References
- 3GPP TS 24.501 V17.x -- Section 6.4.1 (PDU Session Establishment)
- 3GPP TS 23.502 V17.x -- Section 4.3.2 (PDU session procedure)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U protocol)
- Related: TC-PDU-002 (IMS PDU), TC-PDU-003 (multi-PDU), TC-TRF-001 (TCP traffic)

## Quiz Questions
1. What protocol and port number does GTP-U use for user plane data transport between the gNB and UPF?
   *Answer: GTP-U uses UDP on port 2152. User data is encapsulated with a GTP-U header containing a TEID that identifies the tunnel endpoint.*

2. What is the role of the SMF in PDU session establishment, and which interfaces does it use?
   *Answer: The SMF selects the UPF, allocates the UE IP address, and creates PFCP session rules. It uses the N4 interface (PFCP protocol) to communicate with the UPF and receives requests from the AMF via the Nsmf service-based interface.*

3. What happens to user plane traffic if the GTP-U TEID assigned to two different UEs is accidentally the same?
   *Answer: The UPF would be unable to distinguish between the two UEs' traffic. Downlink packets could be routed to the wrong gNB/UE, and uplink packets from both UEs would appear as the same tunnel. This would cause data corruption, session failures, and potential privacy violations.*
