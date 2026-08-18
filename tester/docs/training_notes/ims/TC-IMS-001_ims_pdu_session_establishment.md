# TC-IMS-001: IMS PDU Session Establishment

## Overview
This test validates the establishment of an IMS PDU session (DNN=ims, PSI=2) for VoNR and multimedia services. The IMS PDU session provides the foundation for SIP signaling to the P-CSCF and subsequent voice/video media. It differs from internet PDU sessions in QoS (5QI=5 for IMS signaling), P-CSCF discovery via PCO, and connection to the IMS core.

## 3GPP Background
IMS access in 5G (TS 23.228 Section 5.2) requires a dedicated PDU session with DNN=ims. Key characteristics:
- **Default QoS:** 5QI=5 (IMS signaling, non-GBR, priority level 1, PDB=100ms, PER=10^-6)
- **P-CSCF discovery:** PCO in PDU Session Establishment Accept provides P-CSCF IPv4/IPv6 address
- **UPF connectivity:** The IMS UPF connects to the P-CSCF via N6
- **IP allocation:** Separate pool from internet DNN

After IMS PDU session establishment, the UE performs SIP REGISTER to the P-CSCF to register in the IMS core (S-CSCF). Once IMS-registered, the UE can make/receive VoNR calls.

**Network functions involved:** UE, gNB, AMF, SMF, UPF, P-CSCF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.228 | 5.2 | IMS access via 5GC |
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 23.501 | 5.7.2.1 | 5QI=5 (IMS signaling) |
| TS 24.229 | 5.1 | SIP procedures over IMS |

## Problem Statement
- What if the P-CSCF address is not provided in PCO?
- What if 5QI=5 is not assigned to the default IMS bearer?
- What if the IMS UPF does not have connectivity to the P-CSCF?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE via NAS (5G-AKA).
2. Send PDU Session Establishment Request: PSI=2, DNN=ims.
3. SMF selects IMS UPF, allocates IMS IP, provides P-CSCF via PCO.
4. GTP-U tunnel created for IMS data plane.
5. Verify IMS PDU session is active with valid IP address.

## Expected Behavior
- IMS IP allocated from dedicated pool.
- P-CSCF address available for SIP registration.
- Default QoS flow: 5QI=5 (IMS signaling).

## Pass/Fail Criteria
- **Pass:** IMS PDU session active with valid IP.
- **Fail:** Session establishment fails; no IP.

## Key Concepts for Training

### IMS Network Architecture
The IMS consists of: P-CSCF (Proxy - first contact point for UE SIP), I-CSCF (Interrogating - routes between networks), S-CSCF (Serving - handles registrations and call routing), MGCF (Media Gateway - interworks with PSTN), MRF (Media Resource - conferencing, announcements). All communicate via SIP.

### P-CSCF Discovery
The UE discovers the P-CSCF through PCO in the PDU Session Establishment Accept. Alternative methods: DHCP option 120 (SIP server list) or DNS NAPTR query. The PCO method is the primary mechanism in 5G.

### 5QI=5 for IMS Signaling
5QI=5 is specifically designed for IMS signaling (SIP messages). Priority level 1 ensures SIP messages are processed before best-effort internet traffic (5QI=9, priority 9). PDB=100ms ensures responsive call setup and tear-down.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| No P-CSCF | Cannot SIP REGISTER | Configure P-CSCF in SMF DNN profile |
| IMS DNN missing | PDU Session Reject | Add DNN=ims to SMF |
| Wrong 5QI | IMS signaling delayed | Verify QoS profile for IMS DNN |

## References
- 3GPP TS 23.228 V17.x -- Section 5.2 (IMS access via 5GC)
- Related: TC-IMS-002 (dual PDU), TC-IMS-008 (SIP REGISTER), TC-PDU-002 (IMS PDU)

## Quiz Questions
1. What QoS identifier (5QI) is used for the default IMS signaling bearer, and what are its key characteristics?
   *Answer: 5QI=5. Non-GBR, priority level 1 (highest for non-GBR), PDB=100ms, PER=10^-6.*

2. How does the UE discover the P-CSCF address in 5G?
   *Answer: Through the PCO (Protocol Configuration Options) IE in the PDU Session Establishment Accept message from the SMF.*

3. Why does IMS use a separate PDU session (DNN=ims) instead of sharing the internet session?
   *Answer: Different QoS (5QI=5 vs 5QI=9), separate UPF connectivity to IMS core, P-CSCF discovery via DNN-specific PCO, QoS isolation (SIP signaling must not be degraded by internet traffic), and separate IP addressing for IMS registration.*
