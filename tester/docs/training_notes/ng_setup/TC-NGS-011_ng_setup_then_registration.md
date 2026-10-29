# TC-NGS-011: NG Setup + UE Registration Integration

## Overview
This end-to-end integration test validates the complete signaling path from SCTP connection through NG Setup to UE registration. It confirms that after NG Setup completes, the gNB can successfully carry UE-associated signaling. This bridges the gap between NG Setup tests (infrastructure) and registration tests (UE service).

## 3GPP Background
The complete signaling chain for a UE to register involves two phases:

**Phase 1 - NG Setup (non-UE-associated):**
SCTP association -> NGSetupRequest (stream 0) -> NGSetupResponse -> gNB READY

**Phase 2 - UE Registration (UE-associated):**
UE attach -> InitialUEMessage (stream > 0, carries Registration Request with SUCI) -> DownlinkNASTransport (Auth Request) -> UplinkNASTransport (Auth Response) -> DownlinkNASTransport (Security Mode Command) -> UplinkNASTransport (Security Mode Complete) -> DownlinkNASTransport (Registration Accept) -> UplinkNASTransport (Registration Complete) -> UE REGISTERED

The transition from Phase 1 to Phase 2 is the first InitialUEMessage, which is the first UE-associated NGAP procedure. The AMF allocates an AMF UE NGAP ID and begins tracking the UE context.

**Network functions involved:** gNB, AMF, AUSF, UDM, UE

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup |
| TS 38.413 | 8.6.1 | InitialUEMessage |
| TS 24.501 | 5.5.1 | Registration procedure |
| TS 33.501 | 6.1.3 | 5G-AKA |

## Problem Statement
- What if NG Setup succeeds but InitialUEMessage is rejected?
- What if the SCTP stream assignment for UE signaling is wrong (stream 0)?
- What if the AMF context from NG Setup is not properly linked for UE processing?
- What if the UE configuration doesn't match the gNB's PLMN?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration.
2. Connect SCTP, perform NG Setup, verify READY.
3. Load UE configurations from config database.
4. Register UE_1 on the gNB.
5. Verify UE_1 is in REGISTERED state.
6. Teardown: remove all UEs and gNB.

## Expected Behavior
- NG Setup completes, gNB READY.
- UE_1 successfully registers through the established NG interface.
- Full signaling path (SCTP -> NGAP -> NAS) is operational.

## Pass/Fail Criteria
- **Pass:** gNB READY and UE REGISTERED.
- **Fail:** Either NG Setup or registration fails.

## Key Concepts for Training

### End-to-End Signaling Path
The complete signaling path from UE to core: UE -> Uu (radio) -> gNB -> N2/SCTP (NGAP) -> AMF -> N12 (AUSF) -> N13 (UDM). Each interface must be operational. This test verifies the N2 interface specifically, from SCTP association through NGAP procedures.

### InitialUEMessage as the Bridge
The InitialUEMessage (procedureCode=15) is the first UE-associated message on the NG interface. It carries: the NAS PDU (Registration Request), establishment cause (e.g., mo-Signalling), and the RAN UE NGAP ID. The AMF responds with DownlinkNASTransport (procedureCode=4), which is the first message that carries an AMF UE NGAP ID.

### Integration Test Value
Integration tests are more valuable than unit tests for catching interface mismatches. NG Setup might work perfectly, and registration might work in isolation, but together they might fail due to: context not passed from NG Setup handler to UE handler, PLMN mismatch between gNB and UE config, or stream assignment errors.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| InitialUEMessage rejected | AMF returns ErrorIndication | Check UE PLMN matches AMF served PLMNs |
| Wrong SCTP stream | AMF ignores UE message | Use stream > 0 for UE signaling |
| UE config mismatch | Auth failure | Verify UE IMSI provisioned in UDM |
| gNB context missing | AMF can't find gNB | Verify NG Setup completed successfully |

## References
- 3GPP TS 38.413 V17.x -- Section 8.6.1 (InitialUEMessage), Section 8.7.1 (NG Setup)
- 3GPP TS 24.501 V17.x -- Section 5.5.1 (Registration)
- Related: TC-NGS-001 (NG Setup), TC-REG-001 (registration), TC-NGS-002 (NG Setup state machine)

## Quiz Questions
1. What is the first UE-associated NGAP message after NG Setup, and what does it carry?
   *Answer: InitialUEMessage (procedureCode=15). It carries the NAS PDU (Registration Request), establishment cause, and RAN UE NGAP ID.*

2. Which SCTP stream should InitialUEMessage use, and why?
   *Answer: A stream > 0 (typically streams 1..N, round-robin). Stream 0 is reserved for non-UE signaling. Using stream 0 for UE messages violates the spec and may cause the AMF to drop the message.*

3. If NG Setup succeeds but the first UE registration fails with "Unknown PLMN," what is the likely cause?
   *Answer: The UE's SUCI contains a home PLMN that is not served by the AMF. Even though the gNB's PLMN was accepted in NG Setup, the UE's subscription may be under a different PLMN. The AMF rejects UEs from PLMNs it does not serve.*
