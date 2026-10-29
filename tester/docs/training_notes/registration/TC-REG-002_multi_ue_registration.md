# TC-REG-002: Multiple UE Concurrent Registration

## Overview
This test validates the AMF's ability to handle multiple UE registrations concurrently over the same SCTP/NGAP association. It verifies that the gNB can multiplex NGAP procedures for different UEs and that each UE completes authentication and registration independently. This is critical for real-world deployments where a single gNB serves hundreds of UEs simultaneously.

## 3GPP Background
In a 5G SA network, a single gNB maintains one SCTP association to each AMF. All UE-associated signaling (InitialUEMessage, DownlinkNASTransport, UplinkNASTransport) is multiplexed over this single NG-C connection. NGAP uses the AMF UE NGAP ID and RAN UE NGAP ID pair to distinguish UE contexts within the same association.

Each UE undergoes an independent NAS registration procedure: its own 5G-AKA authentication with unique RAND/AUTN vectors derived from its individual subscriber credentials (IMSI, K, OPc). The AMF must maintain separate NAS security contexts for each UE.

SCTP provides reliable, ordered delivery per stream. UE-associated signaling uses SCTP streams > 0, with round-robin or hash-based stream allocation. Non-UE signaling (NG Setup, Reset) uses stream 0.

**Network functions involved:** Multiple UEs, gNB, AMF, AUSF, UDM
**Key multiplexing identifiers:** RAN UE NGAP ID (gNB-assigned), AMF UE NGAP ID (AMF-assigned)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration procedure |
| TS 38.413 | 8.6.1 | InitialUEMessage (UE-associated signaling) |
| TS 38.413 | 8.2 | UE Context Management |
| TS 38.412 | 7 | SCTP transport and stream management |
| TS 23.501 | 5.2.2.1 | AMF serving multiple UEs |

## Problem Statement
- What if the AMF mixes up UE contexts when processing concurrent registrations?
- What if SCTP stream congestion causes one UE's signaling to block another?
- What if RAN UE NGAP ID allocation collides between concurrent UEs?
- What if the AUSF/UDM cannot handle concurrent authentication vector requests?
- What if one UE's authentication failure cascades and affects the other UE?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. Register UE_1: attach to gNB, send Registration Request, complete 5G-AKA, Security Mode, Registration Accept.
3. Register UE_2: attach to same gNB, send Registration Request, complete independent 5G-AKA, Security Mode, Registration Accept.
4. Verify UE_1 is in REGISTERED state.
5. Verify UE_2 is in REGISTERED state.
6. Confirm both UEs registered successfully with zero failures.

## Expected Behavior
- Both UEs receive independent Authentication Requests with different RAND/AUTN values.
- Each UE derives its own key set (KAMF, KNASint, KNASenc) from its own credentials.
- The AMF assigns unique AMF UE NGAP IDs to each UE.
- The gNB assigns unique RAN UE NGAP IDs to each UE.
- Both UEs reach REGISTERED state within the timeout period.
- NGAP procedures for different UEs do not interfere with each other.

## Pass/Fail Criteria
- **Pass:** Both UE_1 and UE_2 reach REGISTERED state; zero registration failures reported.
- **Fail:** Either UE fails to register; timeout exceeded; AMF context confusion detected.

## Key Concepts for Training

### NGAP UE Context Multiplexing
Each UE on a gNB is identified by a pair: (RAN UE NGAP ID, AMF UE NGAP ID). The gNB assigns the RAN UE NGAP ID when the UE attaches. The AMF assigns the AMF UE NGAP ID in its first downlink response. This pair uniquely identifies the UE context for all subsequent signaling on the NG-C interface.

### SCTP Multi-streaming
SCTP supports multiple streams within a single association. Each stream provides ordered delivery independently. NGAP distributes UE-associated signaling across streams to avoid head-of-line blocking. Stream 0 is reserved for non-UE signaling (NG Setup, Reset, Paging).

### Independent Security Contexts
Each UE has its own NAS security context: unique KAMF, KNASint, KNASenc, and NAS uplink/downlink COUNT values. The AMF must not reuse or confuse security contexts between UEs, as this would break NAS integrity verification.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| RAN UE NGAP ID collision | AMF rejects second UE | Ensure gNB allocates unique IDs |
| SCTP stream exhaustion | Signaling delay or timeout | Increase SCTP stream count in association setup |
| UDM overload | Slow auth vector delivery | Check UDM capacity and connection pool |
| First UE blocks second | Sequential instead of concurrent | Verify async/threaded processing in tester |
| AMF context table full | Registration Reject | Check AMF max UE configuration |

## References
- 3GPP TS 38.413 V17.x -- Section 8.6.1 (InitialUEMessage), Section 8.2 (UE Context Management)
- 3GPP TS 38.412 V17.x -- Section 7 (SCTP transport layer)
- 3GPP TS 24.501 V17.x -- Section 5.5.1.2 (Initial Registration)
- Related: TC-REG-001 (single UE), TC-STR-002..005 (scaling registration), TC-AUTH-004 (multi-UE auth)

## Quiz Questions
1. How does NGAP distinguish between signaling for different UEs on the same SCTP association?
   *Answer: Using the (RAN UE NGAP ID, AMF UE NGAP ID) pair. The RAN UE NGAP ID is assigned by the gNB, and the AMF UE NGAP ID is assigned by the AMF.*

2. Why is SCTP multi-streaming important for concurrent UE registrations?
   *Answer: Each stream provides independent ordered delivery. If one stream is congested or waiting for retransmission, other streams continue delivering messages. This prevents one UE's signaling delay from blocking another UE's registration.*

3. Can two UEs registered on the same gNB share the same KAMF? Why or why not?
   *Answer: No. Each UE has unique USIM credentials (K, OPc) and receives unique authentication vectors (RAND, AUTN). The derived KAMF is cryptographically unique per UE, ensuring independent NAS security contexts.*
