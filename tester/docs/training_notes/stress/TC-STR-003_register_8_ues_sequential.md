# TC-STR-003: Register 8 UEs Sequential

## Overview
This test scales UE registration to 8 UEs on a single gNB, doubling the load from TC-STR-002. It validates the AMF's context management, UDM's subscriber handling, and SCTP association capacity at moderate scale. Eight UEs is a typical small-cell deployment scenario.

## 3GPP Background
Scaling from 4 to 8 UEs introduces additional stress on network resources. The AMF must manage 8 simultaneous MM contexts with independent NAS security contexts. The UDM serves 8 different subscriber profiles. Each UE consumes SCTP stream resources for its NGAP signaling.

Per TS 38.412, the number of SCTP streams is negotiated during association setup (INIT/INIT-ACK). A typical configuration uses 2-10 outbound streams. With 8 UEs, the gNB may need to share streams across UEs or request more streams. UE-associated signaling is distributed across available streams (stream > 0) to minimize head-of-line blocking.

**Network functions involved:** 8 UEs, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 38.412 | 7 | SCTP streams for NGAP |
| TS 23.501 | 5.2 | AMF capacity and scaling |
| TS 38.413 | 8.6.1 | InitialUEMessage |
| TS 33.501 | 6.1.3 | 5G-AKA (per UE) |

## Problem Statement
- What if the SCTP stream count is insufficient for 8 concurrent UE contexts?
- What if the AMF's internal UE hash table has a collision at 8 entries?
- What if authentication for later UEs is slower due to UDM load?
- What if the gNB's context table only supports a limited number of entries?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. For each UE (UE_1 through UE_8), sequentially:
   a. Perform full registration (5G-AKA, Security Mode, Registration Accept).
   b. Log successful registration.
3. All 8 UEs registered.

## Expected Behavior
- All 8 UEs complete registration with independent authentication.
- gNB maintains 8 concurrent UE contexts.
- AMF maintains 8 concurrent MM contexts.
- No performance degradation compared to single-UE registration.

## Pass/Fail Criteria
- **Pass:** All 8 UEs reach REGISTERED state.
- **Fail:** Any UE fails to register.

## Key Concepts for Training

### SCTP Stream Allocation for NGAP
SCTP streams provide independent ordered delivery channels. The gNB typically negotiates N outbound streams (e.g., 5-10). UE-associated signaling is distributed across streams 1..N using round-robin or hash-based assignment. With 8 UEs and 5 streams, some streams carry signaling for 2 UEs. This is acceptable because SCTP guarantees ordering within a stream but allows independent delivery across streams.

### AMF Capacity Planning
The AMF's RelativeAMFCapacity (0-255) indicates its load capacity. At 8 UEs, even a minimally configured AMF should cope. However, each UE consumes memory for its MM context (~1-5 KB including security keys, state, timers). Planning for production scale (thousands of UEs) requires monitoring AMF resource usage during scaling tests like this one.

### Sequential Registration Timing
With 8 UEs registered sequentially, total test time is approximately 8 * (single UE registration time). If single registration takes ~2 seconds, 8 UEs should complete in ~16 seconds. Significantly longer times suggest AMF processing bottlenecks.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Stream exhaustion | Signaling delays for later UEs | Increase SCTP outbound stream count |
| Slow auth delivery | Total time >> 8x single UE | Check UDM performance under multi-subscriber load |
| Context table full | Later UEs rejected | Increase AMF max-subscribers config |
| Subscriber provisioning | Some UEs have wrong credentials | Verify all 8 IMSIs in UDM database |

## References
- 3GPP TS 38.412 V17.x -- Section 7 (SCTP multi-streaming)
- 3GPP TS 23.501 V17.x -- Section 5.2 (AMF capacity)
- Related: TC-STR-002 (4 UEs), TC-STR-004 (16 UEs), TC-STR-005 (32 UEs)

## Quiz Questions
1. If the SCTP association has 5 outbound streams (streams 1-5) and 8 UEs need signaling, how are UEs assigned to streams?
   *Answer: Using round-robin or hash-based distribution. For example: UE_1->stream 1, UE_2->stream 2, ..., UE_5->stream 5, UE_6->stream 1, UE_7->stream 2, UE_8->stream 3. Multiple UEs share streams, with ordering guaranteed within each stream.*

2. At 8 UEs, approximately how much memory does the AMF need for MM contexts (assuming ~3 KB per context)?
   *Answer: Approximately 24 KB (8 x 3 KB). This is trivial for modern systems, but the same formula at 100,000 UEs yields ~300 MB, which becomes significant.*

3. Why is sequential registration used instead of concurrent for this particular test?
   *Answer: Sequential registration isolates the test to purely multi-context management. If it fails, the issue is with handling N concurrent contexts, not with concurrent processing. This provides a cleaner baseline for debugging before testing concurrent registration.*
