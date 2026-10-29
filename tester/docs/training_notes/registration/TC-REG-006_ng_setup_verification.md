# TC-REG-006: NG Setup Verification

## Overview
This test validates the NG Setup procedure as a prerequisite to UE registration. It verifies that the gNB successfully establishes the NG-C interface with the AMF and reaches the READY state. Without a successful NG Setup, no UE-associated signaling (registration, PDU sessions, etc.) is possible over the NG interface.

## 3GPP Background
The NG Setup procedure (TS 38.413 Section 8.7.1) is the first NGAP procedure performed after an SCTP association is established between the gNB and the AMF. It is a non-UE-associated signaling procedure sent on SCTP stream 0.

The gNB sends an **NGSetupRequest** containing:
- **GlobalRANNodeID** (id=27): Uniquely identifies the gNB (PLMN + gNB ID)
- **RANNodeName** (id=82): Human-readable name (PrintableString, 1-150 chars)
- **SupportedTAList** (id=102): List of supported Tracking Areas, each containing TAC, BroadcastPLMNList with SliceSupportList (S-NSSAI)
- **DefaultPagingDRX** (id=21): Default paging DRX cycle

The AMF responds with **NGSetupResponse** containing:
- **AMFName**: Human-readable AMF identifier
- **ServedGUAMIList**: List of GUAMIs (PLMN + AMF Region/Set/Pointer) served by this AMF
- **RelativeAMFCapacity**: Load balancing weight (0-255)
- **PLMNSupportList**: PLMNs and slices supported by the AMF

The gNB FSM transitions: IDLE -> NG_SETUP_SENT -> READY.

**Network functions involved:** gNB, AMF
**Transport:** SCTP association on port 38412 (IANA registered for NGAP)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup procedure |
| TS 38.413 | 9.3.1.5 | GlobalRANNodeID IE |
| TS 38.413 | 9.3.3.5 | PLMNIdentity encoding (3 octets BCD) |
| TS 38.412 | 7 | SCTP transport for NG-C |
| TS 23.501 | 5.2.1 | AMF and gNB relationship |

## Problem Statement
- What if the SCTP association cannot be established (wrong AMF IP/port, firewall)?
- What if the AMF rejects the NG Setup because the PLMN is not in its served list?
- What if the gNB's TAC is not configured in the AMF?
- What if the gNB sends NG Setup on a non-zero SCTP stream (violating the spec)?
- What if the AMF is overloaded and does not respond, causing the gNB to timeout?

## Test Procedure (Step-by-Step)
1. Create gNB instance from configuration profile (gNB ID, TAC, PLMN, supported slices).
2. Establish SCTP association to AMF on IP:38412.
3. gNB sends NG Setup Request on SCTP stream 0 (non-UE-associated signaling).
4. AMF processes the request and sends NG Setup Response.
5. gNB FSM transitions to READY state.
6. Verify gNB state is READY.
7. Query gNB UE count (should be 0 -- no UEs registered yet).

## Expected Behavior
- SCTP four-way handshake completes (INIT/INIT-ACK/COOKIE-ECHO/COOKIE-ACK).
- NG Setup Request is sent on stream 0 with all mandatory IEs.
- NG Setup Response received from AMF with AMFName, GUAMIs, capacity.
- gNB FSM transitions: IDLE -> NG_SETUP_SENT -> READY.
- UE count is 0 (no UEs attached yet).

## Pass/Fail Criteria
- **Pass:** gNB reaches READY state; NG Setup Response received; UE count queryable.
- **Fail:** gNB does not reach READY state; SCTP connection fails; NG Setup Failure received.

## Key Concepts for Training

### NG-C Interface
The NG-C (Control Plane) interface connects the gNB to the AMF. It carries NGAP signaling over SCTP. The NG Setup is the handshake that initializes this interface -- both sides exchange capabilities (supported PLMNs, slices, TACs). After NG Setup, the interface is ready for UE-associated signaling (registration, handover, paging).

### SCTP for NGAP
Unlike LTE's S1-AP which also uses SCTP, 5G NGAP defines specific stream usage. Stream 0 is reserved for non-UE-associated signaling (NG Setup, Reset, Paging). UE-associated signaling is distributed across streams 1..N for parallelism. SCTP provides reliable, in-order delivery per stream, message boundaries, and multi-homing.

### gNB State Machine
The gNB FSM has three main states: IDLE (no connection), NG_SETUP_SENT (waiting for response), and READY (NG-C operational). Only in the READY state can the gNB process UE attach requests and forward NAS messages. A disconnect returns the gNB to IDLE, and reconnection requires a fresh NG Setup.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| SCTP connection refused | Timeout on connect | Verify AMF IP/port 38412, check firewall |
| NG Setup Failure | AMF rejects with cause | Check PLMN/TAC match AMF configuration |
| gNB stuck in NG_SETUP_SENT | No response from AMF | AMF may be down or overloaded |
| Wrong SCTP stream | AMF ignores the message | Ensure NG Setup is sent on stream 0 |
| PLMN encoding error | AMF doesn't recognize PLMN | Verify 3-octet BCD encoding of MCC/MNC |

## References
- 3GPP TS 38.413 V17.x -- Section 8.7.1 (NG Setup procedure)
- 3GPP TS 38.412 V17.x -- Section 7 (SCTP transport)
- 3GPP TS 23.501 V17.x -- Section 5.2.1 (Network function relationships)
- Related: TC-NGS-001 (basic NG Setup), TC-NGS-002 (state machine), TC-NGS-010 (encoding)

## Quiz Questions
1. What is the correct SCTP stream for sending the NG Setup Request, and why?
   *Answer: Stream 0. NG Setup is a non-UE-associated signaling procedure, and per TS 38.412, all non-UE-associated NGAP messages must be sent on SCTP stream 0.*

2. What information does the AMF provide in the NG Setup Response that is useful for subsequent operations?
   *Answer: The AMF provides its AMFName (for logging/identification), ServedGUAMIList (for UE routing to the correct AMF), RelativeAMFCapacity (for load balancing across AMFs), and PLMNSupportList (to confirm which PLMNs and slices the AMF supports).*

3. What happens if the gNB sends a Registration Request before completing NG Setup?
   *Answer: The gNB cannot send UE-associated signaling (like InitialUEMessage carrying the Registration Request) before NG Setup completes. The gNB FSM must be in READY state. If the gNB attempts to send signaling in IDLE state, the message will not be sent, and the UE registration will fail.*
