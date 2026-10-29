# TC-NGS-001: Basic NG Setup -- Happy Path

## Overview
This test validates the fundamental NG Setup procedure -- the first NGAP procedure after SCTP association establishment. It is the "happy path" test: everything is correctly configured, and the gNB should transition from IDLE to READY. Without successful NG Setup, no UE-associated signaling can occur over the NG interface.

## 3GPP Background
The NG Setup procedure (TS 38.413 Section 8.7.1) initializes the NG-C (control plane) interface between the gNB and AMF. It is an NGAP non-UE-associated signaling procedure sent on SCTP stream 0.

**NGSetupRequest (initiatingMessage, procedureCode=21)** contains:
- **GlobalRANNodeID** (id=27): PLMN Identity + gNB-ID (22-32 bits), uniquely identifying the gNB
- **RANNodeName** (id=82): Human-readable name (PrintableString 1..150 characters)
- **SupportedTAList** (id=102): List of TAIs (Tracking Area Identities) with associated BroadcastPLMNList and SliceSupportList (S-NSSAI values)
- **DefaultPagingDRX** (id=21): Default paging DRX cycle (v32, v64, v128, v256)

**NGSetupResponse (successfulOutcome)** contains:
- **AMFName**: Human-readable AMF identifier
- **ServedGUAMIList**: GUAMIs (PLMN + AMF Region ID + AMF Set ID + AMF Pointer) served by this AMF
- **RelativeAMFCapacity** (0-255): Weight for AMF load balancing
- **PLMNSupportList**: PLMNs supported by the AMF with slice support information

The NGAP messages use ASN.1 APER (Aligned Packed Encoding Rules) encoding per ITU-T X.691.

**Network functions involved:** gNB, AMF
**Transport:** SCTP on port 38412 (IANA registered)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup procedure |
| TS 38.413 | 9.4.1 | NG Setup message structure |
| TS 38.413 | 9.3.1.5 | GlobalRANNodeID IE |
| TS 38.413 | 9.3.3.5 | PLMNIdentity encoding |
| TS 38.412 | 7 | SCTP transport |

## Problem Statement
- What if SCTP association fails (wrong IP, port, firewall)?
- What if the PLMN in the NG Setup Request does not match any PLMN served by the AMF?
- What if the AMF is overloaded and cannot accept new gNBs?
- What if the APER encoding of the NG Setup Request is malformed?

## Test Procedure (Step-by-Step)
1. Create gNB instance from configuration profile (gNB name, gNB ID, PLMN, TAC, slices).
2. Establish SCTP association to AMF (IP address and port 38412 from config).
3. gNB sends NGSetupRequest on SCTP stream 0.
4. AMF validates the request and sends NGSetupResponse.
5. gNB receives response and transitions to READY state.
6. Verify gNB state is READY.
7. Teardown: remove gNB.

## Expected Behavior
- SCTP four-way handshake completes successfully.
- NGSetupRequest is accepted by the AMF.
- NGSetupResponse received with valid AMFName, GUAMIs, capacity.
- gNB FSM transitions: IDLE -> NG_SETUP_SENT -> READY.

## Pass/Fail Criteria
- **Pass:** gNB reaches READY state after NG Setup.
- **Fail:** gNB does not reach READY; NGSetupFailure received; SCTP connection fails.

## Key Concepts for Training

### NGAP Procedure Codes
NGAP procedures are identified by procedureCode in the NGAP PDU header. NG Setup uses procedureCode=21. Other key codes: InitialUEMessage=15, DownlinkNASTransport=4, UplinkNASTransport=46, UEContextRelease=41. The initiatingMessage/successfulOutcome/unsuccessfulOutcome wrapper indicates the message direction.

### APER Encoding
NGAP uses ASN.1 with APER (Aligned Packed Encoding Rules). APER is a compact binary encoding that preserves bit-level alignment. Each IE (Information Element) is encoded with its ID, criticality, and value. The PLMNIdentity is encoded as 3 octets in BCD format: for MCC=001/MNC=01, the encoding is 0x00F110 (digits swapped per 3GPP conventions).

### gNB FSM States
The gNB operates in three states: IDLE (no SCTP connection), NG_SETUP_SENT (NGSetupRequest sent, waiting for response), READY (NG Setup complete, operational). The READY state is the only state that permits UE-associated signaling. Any SCTP disconnection returns the gNB to IDLE.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| SCTP connection refused | Cannot establish association | Verify AMF IP, port 38412, SCTP kernel module |
| NGSetupFailure received | AMF rejects gNB | Check cause value: unknown PLMN, overloaded |
| Stuck in NG_SETUP_SENT | No response from AMF | AMF may be down or not processing NGAP |
| APER decode error | AMF drops the message | Verify ASN.1 encoding of all IEs |
| Wrong SCTP stream | AMF ignores message | Use stream 0 for non-UE signaling |

## References
- 3GPP TS 38.413 V17.x -- Section 8.7.1 (NG Setup)
- 3GPP TS 38.412 V17.x -- Section 7 (SCTP transport)
- ITU-T X.691 -- APER encoding rules
- Related: TC-NGS-002 (state machine), TC-NGS-010 (encoding), TC-NGS-017 (response mandatory IEs)

## Quiz Questions
1. What is the NGAP procedureCode for NG Setup, and which SCTP stream must it use?
   *Answer: procedureCode=21, SCTP stream 0. Stream 0 is reserved for non-UE-associated signaling procedures.*

2. What are the four mandatory IEs in the NGSetupRequest, and what purpose does each serve?
   *Answer: (1) GlobalRANNodeID -- uniquely identifies the gNB, (2) RANNodeName -- human-readable name for logging, (3) SupportedTAList -- tracking areas and slices the gNB supports, (4) DefaultPagingDRX -- paging cycle for UEs in idle mode.*

3. If the AMF sends an NGSetupFailure instead of NGSetupResponse, what information does it contain?
   *Answer: The NGSetupFailure contains a Cause IE indicating why the setup was rejected (e.g., "unknown PLMN", "AMF overloaded", "unspecified"). It may also contain a TimeToWait IE suggesting how long the gNB should wait before retrying.*
