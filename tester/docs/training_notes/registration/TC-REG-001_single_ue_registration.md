# TC-REG-001: Single UE Registration

## Overview
This test validates the fundamental 5G NAS Initial Registration procedure for a single UE. It exercises the complete signaling chain from Registration Request through 5G-AKA authentication, NAS Security Mode Command, to Registration Accept. This is the most basic and critical test -- if a UE cannot register, no other 5G service is possible.

## 3GPP Background
Initial Registration is the first NAS procedure a UE performs after powering on and selecting a PLMN. The UE sends a Registration Request containing its identity (SUCI, derived from its permanent SUPI/IMSI) to the AMF via the gNB over the N1/N2 interfaces.

The AMF initiates authentication by requesting vectors from the AUSF/UDM. 5G-AKA (Authentication and Key Agreement) is the primary authentication method. The AMF sends an Authentication Request carrying RAND (random challenge) and AUTN (authentication token containing SQN, AMF field, and MAC). The UE uses its permanent key K and operator key OPc (via the Milenage algorithm set) to verify AUTN and compute RES*. The AMF compares RES* against XRES* to authenticate the UE.

After successful authentication, the AMF sends a Security Mode Command selecting ciphering (EEA) and integrity (EIA) algorithms. The UE activates NAS security and responds with Security Mode Complete. Finally, the AMF sends Registration Accept containing the 5G-GUTI, TAI list, and allowed NSSAI. The UE acknowledges with Registration Complete and transitions to the REGISTERED state.

**Network functions involved:** UE, gNB (NG-RAN), AMF, AUSF, UDM
**Interfaces:** Uu (UE-gNB), N2/NG-C (gNB-AMF), N12 (AMF-AUSF), N13 (AUSF-UDM)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration procedure |
| TS 33.501 | 6.1.3 | 5G-AKA authentication |
| TS 33.501 | 6.7.2 | NAS security algorithm selection |
| TS 38.413 | 8.7.1 | NG Setup procedure (prerequisite) |
| TS 23.502 | 4.2.2.2 | Registration procedure call flow |

## Problem Statement
- What if the AMF rejects the SUCI because the UDM has no subscription data?
- What if AUTN verification fails because the UE's K or OPc do not match the network?
- What if the SQN is out of sync between UE and network, triggering AUTS resynchronization?
- What if the AMF selects EIA0 (null integrity), which is forbidden for initial registration per TS 33.501?
- What if SCTP congestion delays the Authentication Request beyond the T3560 timer?

## Test Procedure (Step-by-Step)
1. Create a gNB instance from the configuration profile (gNB ID, TAC, PLMN, slices).
2. Establish SCTP association to the AMF on port 38412.
3. Send NG Setup Request; wait for NG Setup Response and gNB READY state.
4. Attach the UE to the gNB (assign RAN UE NGAP ID).
5. UE sends Registration Request with 5GS registration type = initial, carrying SUCI.
6. AMF sends Authentication Request with RAND and AUTN.
7. UE verifies AUTN, computes RES*, derives KAUSF -> KSEAF -> KAMF.
8. UE sends Authentication Response with RES*.
9. AMF sends Security Mode Command (selected EEA/EIA algorithms).
10. UE activates NAS security, sends Security Mode Complete.
11. AMF sends Registration Accept (5G-GUTI, TAI list, allowed NSSAI).
12. UE sends Registration Complete; FSM transitions to REGISTERED.
13. Verify the negotiated security algorithms (EEA/EIA) from the UE context.

## Expected Behavior
- NG Setup completes and gNB reaches READY state.
- Authentication Request is received within the timeout period.
- AUTN verification succeeds (MAC matches, SQN in range).
- Security Mode Command selects EIA > 0 (non-null integrity is mandatory).
- Registration Accept is received with a valid 5G-GUTI.
- UE FSM transitions: DEREGISTERED -> REGISTRATION_INITIATED -> AUTHENTICATED -> SECURITY_MODE -> REGISTERED.

## Pass/Fail Criteria
- **Pass:** UE reaches REGISTERED state; EIA algorithm is non-null (EIA > 0); NAS security keys (KNASint, KNASenc) are derived.
- **Fail:** UE does not reach REGISTERED state within 15 seconds; authentication fails; EIA0 selected; no security keys present.

## Key Concepts for Training

### 5G-AKA Key Hierarchy
The 5G authentication produces a chain of keys: from the permanent key K and the random challenge RAND, the Milenage algorithm generates CK and IK. These are combined with the serving network name (SN-name) to derive KAUSF, then KSEAF, then KAMF. From KAMF, NAS keys KNASint (for integrity) and KNASenc (for ciphering) are derived, along with KgNB for the access stratum.

### SUCI and SUPI
The SUPI (Subscription Permanent Identifier, typically an IMSI) is never sent in cleartext over the air. Instead, the UE computes a SUCI (Subscription Concealed Identifier) using the home network's public key (ECIES scheme). The UDM decrypts the SUCI to recover the SUPI for subscriber lookup.

### NAS Security Mode
The Security Mode Command procedure activates NAS security. The AMF selects the strongest algorithm supported by both the UE and the network. EIA0 (null integrity) is forbidden for initial registration. Common algorithms: EIA1 (Snow3G-128), EIA2 (AES-128-CMAC); EEA0 (null ciphering is allowed), EEA1 (Snow3G), EEA2 (AES-128-CTR).

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Wrong K/OPc in UDM | Authentication Reject (cause #3) | Verify subscriber credentials match between UE and UDM |
| SQN out of sync | Auth Failure cause=21 (Synch failure) + AUTS | UDM resyncs SQN from AUTS; usually auto-recovers |
| SCTP not connected | Timeout waiting for Auth Request | Check AMF IP/port, firewall rules, SCTP kernel support |
| EIA0 selected | Test fails on algorithm check | AMF policy must mandate EIA1 or EIA2 |
| gNB not READY | Registration never starts | Verify NG Setup succeeded before attaching UE |

## References
- 3GPP TS 24.501 V17.x -- Section 5.5.1 (Registration procedures)
- 3GPP TS 33.501 V17.x -- Section 6.1.3 (5G-AKA), Section 6.7 (NAS security)
- 3GPP TS 38.413 V17.x -- Section 8.7.1 (NG Setup)
- Related: TC-REG-002 (multi-UE), TC-REG-005 (algorithm verification), TC-AUTH-001 (auth focus)

## Quiz Questions
1. In the 5G-AKA key hierarchy, what is the correct derivation order from the permanent key K to the NAS integrity key?
   *Answer: K -> CK/IK (Milenage) -> KAUSF -> KSEAF -> KAMF -> KNASint*

2. Why is EIA0 (null integrity) not permitted during Initial Registration, even though EEA0 (null ciphering) may be acceptable?
   *Answer: Per TS 33.501, NAS integrity protection is mandatory to prevent man-in-the-middle attacks on registration signaling. Ciphering is optional as the information in registration messages may not be confidential, but tampering must be prevented.*

3. A UE sends a Registration Request but never receives an Authentication Request. What are the first three things you should check?
   *Answer: (1) SCTP association is established and gNB is in READY state; (2) The SUCI/IMSI is provisioned in the UDM with valid subscription data; (3) AMF logs for any NGAP decode errors or NAS message rejection.*
