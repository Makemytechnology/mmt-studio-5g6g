# TC-PDU-003: Dual PDU Session Establishment (Internet + IMS)

## Overview
This test validates a single UE establishing two PDU sessions simultaneously -- one for internet access (DNN=internet, PSI=1) and one for IMS/VoNR (DNN=ims, PSI=2). This dual-PDU scenario is the standard configuration for a 5G smartphone making voice calls while browsing the internet. Each session has independent IP addresses, GTP-U tunnels, and QoS flows.

## 3GPP Background
Per TS 23.501 Section 5.6.1, a UE can maintain multiple PDU sessions simultaneously. Each PDU session is identified by a PSI (PDU Session Identity, values 1-15) and is associated with a specific DNN and S-NSSAI.

In a typical VoNR-capable UE:
- **PDU Session 1 (PSI=1, DNN=internet):** Provides general data connectivity. Default QoS flow with 5QI=9 (best effort). IP from internet pool.
- **PDU Session 2 (PSI=2, DNN=ims):** Provides IMS connectivity for SIP signaling and voice/video media. Default QoS flow with 5QI=5 (IMS signaling). IP from IMS pool. P-CSCF address provided via PCO.

Each PDU session creates independent N3 (GTP-U) tunnels between the gNB and potentially different UPFs. The SMF may select different UPFs for each DNN based on UPF capabilities and DN connectivity.

On the UE/tester side, each PDU session creates a separate TUN interface with its own IP address. Applications bind to the appropriate interface based on the service (SIP to IMS TUN, web browsing to internet TUN).

**Network functions involved:** UE, gNB, AMF, SMF(s), UPF(s)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.6.1 | Multiple PDU sessions per UE |
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 23.502 | 4.3.2 | PDU session establishment procedure |
| TS 29.281 | 4 | GTP-U per-session tunneling |
| TS 23.501 | 5.7.2.1 | 5QI=9 (internet) and 5QI=5 (IMS) |

## Problem Statement
- What if the AMF/SMF does not support multiple PDU sessions per UE?
- What if both sessions receive the same IP address (pool configuration error)?
- What if GTP-U TEIDs collide between the two sessions?
- What if the UE tries to use PSI=1 for both sessions?
- What if creating the second PDU session disrupts the first?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. Register UE via full NAS procedure.
3. Establish first PDU session: DNN=internet, PSI=1. Record IP_internet.
4. Establish second PDU session: DNN=ims, PSI=2. Record IP_ims.
5. Verify both sessions are active simultaneously.
6. Verify IP_internet != IP_ims (different pools, different addresses).
7. Both GTP-U tunnels operational independently.

## Expected Behavior
- First PDU session (internet) established with IP from internet pool.
- Second PDU session (IMS) established with IP from IMS pool.
- Different IP addresses allocated (from different pools).
- Independent GTP-U tunnels with unique TEIDs.
- Both sessions coexist without interfering with each other.

## Pass/Fail Criteria
- **Pass:** Both PDU sessions active; IPs are different; both GTP-U tunnels operational.
- **Fail:** Either session fails; same IP assigned to both; second session disrupts first.

## Key Concepts for Training

### PDU Session Identity (PSI)
The PSI is a 4-bit identifier (values 1-15) that uniquely identifies a PDU session within a UE's context. The UE assigns the PSI when requesting a new session. Each active PDU session must have a unique PSI. If a UE requests a session with an already-in-use PSI, the network may reject it or modify the existing session.

### Multiple GTP-U Tunnels Per UE
Each PDU session has its own N3 GTP-U tunnel with a unique TEID pair (uplink TEID at UPF, downlink TEID at gNB). The gNB must maintain a mapping: QFI -> PDU session -> GTP-U tunnel. Packets are classified by QFI in the GTP-U extension header and forwarded to the correct tunnel.

### QoS Isolation Between Sessions
The internet and IMS PDU sessions have different QoS requirements. Internet traffic (5QI=9) is best-effort and can tolerate higher latency. IMS signaling (5QI=5) requires low latency (PDB=100ms). The UPF applies independent QoS enforcement (QER) per PDU session, ensuring that heavy internet traffic does not starve IMS signaling.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Same IP for both sessions | Test fails on IP comparison | Check SMF IP pool configuration per DNN |
| PSI collision | Second session rejected | Ensure PSI=1 and PSI=2 are distinct |
| First session dropped | Internet session lost when IMS creates | Check AMF session management |
| TEID collision | Packets misrouted | Verify unique TEID allocation per session |
| TUN interface conflict | Only one TUN works | Check TUN device naming and routing |

## References
- 3GPP TS 23.501 V17.x -- Section 5.6.1 (Multiple PDU sessions)
- 3GPP TS 24.501 V17.x -- Section 6.4.1 (PDU Session Establishment)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-PDU-001 (internet), TC-PDU-002 (IMS), TC-IMS-002 (dual PDU), TC-IMS-006 (dual traffic)

## Quiz Questions
1. Why does a VoNR-capable UE need two separate PDU sessions instead of using a single session for both internet and IMS?
   *Answer: Internet and IMS require different DNNs (connecting to different data networks), different QoS profiles (5QI=9 vs 5QI=5), and potentially different UPFs. Separate PDU sessions ensure QoS isolation -- heavy internet downloads cannot impact SIP signaling latency. The IMS PDU session also provides P-CSCF discovery via PCO, which is DNN-specific.*

2. What is the maximum number of PDU sessions a single UE can maintain simultaneously?
   *Answer: Up to 15, since the PSI is a 4-bit identifier with values 1-15 (0 is reserved). In practice, most UEs maintain 2-3 sessions (internet, IMS, and possibly an enterprise VPN).*

3. How does the gNB determine which GTP-U tunnel to use for a downlink packet destined to a specific PDU session?
   *Answer: Each PDU session has a unique TEID assigned by the gNB during PDU Session Resource Setup. The UPF uses this TEID in the GTP-U header when sending downlink packets. The gNB uses the TEID to look up the PDU session context and forward the packet to the correct TUN interface/UE bearer.*
