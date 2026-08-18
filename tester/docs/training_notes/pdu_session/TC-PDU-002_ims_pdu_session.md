# TC-PDU-002: IMS PDU Session Establishment

## Overview
This test validates the establishment of an IMS-specific PDU session (DNN=ims). IMS PDU sessions are the foundation for VoNR (Voice over New Radio) and other multimedia services. Unlike internet PDU sessions, IMS sessions carry SIP signaling and provide P-CSCF discovery through Protocol Configuration Options (PCO). The default QoS flow uses 5QI=5 (IMS signaling).

## 3GPP Background
IMS (IP Multimedia Subsystem) access over 5G Core requires a dedicated PDU session with DNN=ims (TS 23.228 Section 5.2). This session differs from internet sessions in several ways:

1. **P-CSCF Discovery:** The PCO (Protocol Configuration Options) IE in the PDU Session Establishment Accept carries the P-CSCF (Proxy Call Session Control Function) address. The UE needs this address to send SIP REGISTER and other SIP messages.

2. **QoS:** The default QoS flow uses 5QI=5 (IMS signaling -- non-GBR, priority level 1, PDB=100ms). When a voice call is established, additional dedicated GBR bearers (5QI=1 for voice, 5QI=2 for video) are created dynamically via PCF/Rx interface.

3. **IP Address:** A separate IP address pool is used for IMS, distinct from the internet pool. This IP is used as the contact address in SIP signaling.

The SMF selects an IMS-specific UPF that connects to the IMS core network (P-CSCF, I-CSCF, S-CSCF) via the N6 interface.

**Network functions involved:** UE, gNB, AMF, SMF, UPF, P-CSCF
**Interfaces:** N1, N2, N4, N3 (GTP-U), N6 (UPF to IMS core)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 23.228 | 5.2 | IMS access via 5G Core |
| TS 24.229 | 5.1 | SIP procedures over IMS PDU |
| TS 23.501 | 5.7.2.1 | 5QI=5 (IMS signaling QoS) |
| TS 24.008 | 10.5.6.3 | PCO (Protocol Configuration Options) |

## Problem Statement
- What if the P-CSCF address is not provided in the PCO?
- What if the IMS DNN is not configured on the SMF?
- What if the IMS IP pool is exhausted?
- What if 5QI=5 is not configured, and the wrong QoS is assigned to IMS signaling?
- What if the UPF selected does not have connectivity to the IMS core?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. Register UE via full NAS procedure (5G-AKA, Security Mode, Registration Accept).
3. UE sends PDU Session Establishment Request with PSI=2, DNN=ims.
4. SMF selects IMS-specific UPF, allocates IMS IP from dedicated pool.
5. SMF provides P-CSCF address via PCO in the NAS message.
6. PDU Session Resource Setup creates GTP-U tunnel for IMS data plane.
7. PDU Session Establishment Accept received with IMS IP and P-CSCF.
8. Verify IMS PDU session is active with valid IP address.

## Expected Behavior
- SMF allocates an IMS IP address from the dedicated IMS pool.
- P-CSCF address is provided in the PCO to the UE.
- Default QoS flow with 5QI=5 (IMS signaling) is established.
- GTP-U tunnel is created for the IMS data plane.
- The UE is ready to send SIP REGISTER to the P-CSCF.

## Pass/Fail Criteria
- **Pass:** IMS PDU session active; valid UE IP address allocated; session context stored.
- **Fail:** PDU session establishment fails; no IP address; P-CSCF not provided.

## Key Concepts for Training

### IMS Architecture in 5G
The IMS is accessed through a dedicated PDU session (DNN=ims). The P-CSCF is the first contact point for SIP signaling -- it acts as a SIP proxy between the UE and the IMS core. The I-CSCF handles SIP routing between networks, and the S-CSCF is the central server that processes SIP registrations and call routing. In 5G, the PCF's Rx interface allows the IMS (via the AF/P-CSCF) to request dedicated QoS bearers for media.

### P-CSCF Discovery via PCO
The UE discovers the P-CSCF address through the Protocol Configuration Options (PCO) IE in the PDU Session Establishment Accept. The PCO is a TLV-encoded container that can carry DNS addresses, P-CSCF addresses (IPv4/IPv6), and other protocol parameters. The SMF populates the P-CSCF address from its DNN configuration.

### 5QI=5 for IMS Signaling
5QI=5 is the standardized QoS identifier for IMS signaling. Its characteristics: non-GBR (no guaranteed bit rate), priority level 1 (highest among non-GBR), PDB=100ms, PER=10^-6. This ensures SIP messages are delivered with low latency and high reliability, even under network congestion.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| No P-CSCF address | SIP REGISTER fails | Configure P-CSCF in SMF DNN profile |
| IMS DNN not configured | PDU Session Reject | Add DNN=ims to SMF configuration |
| Wrong 5QI assigned | IMS signaling delayed | Verify QoS profile for IMS DNN |
| IMS IP pool empty | PDU Session Reject | Expand IMS address pool |
| UPF no IMS connectivity | SIP messages not routed | Verify UPF N6 interface to IMS core |

## References
- 3GPP TS 23.228 V17.x -- Section 5.2 (IMS access via 5GC)
- 3GPP TS 24.229 V17.x -- Section 5.1 (SIP over IMS PDU)
- 3GPP TS 24.501 V17.x -- Section 6.4.1 (PDU Session Establishment)
- Related: TC-PDU-001 (internet PDU), TC-PDU-003 (multi-PDU), TC-IMS-001 (IMS PDU), TC-IMS-008 (SIP REGISTER)

## Quiz Questions
1. What is the difference between the internet PDU session and the IMS PDU session in terms of QoS and network connectivity?
   *Answer: The internet PDU session typically uses 5QI=9 (best effort, PDB=300ms) and connects to the public internet via the UPF's N6 interface. The IMS PDU session uses 5QI=5 (priority level 1, PDB=100ms) for SIP signaling and connects to the IMS core (P-CSCF) via the UPF's N6 interface. The IMS session also provides P-CSCF address via PCO.*

2. How does the UE discover the P-CSCF address for SIP registration?
   *Answer: The P-CSCF address is provided in the Protocol Configuration Options (PCO) IE within the PDU Session Establishment Accept message. The SMF populates this from its DNN configuration for the IMS DNN.*

3. After establishing an IMS PDU session, what additional QoS resources are needed before a VoNR voice call can begin?
   *Answer: A dedicated GBR QoS flow with 5QI=1 (conversational voice, PDB=100ms, GBR) must be established for the voice media. This is triggered by the PCF via the Rx interface when the P-CSCF processes the SIP INVITE. The default 5QI=5 flow only handles SIP signaling, not voice media.*
