# TC-STR-013: Register 128 UEs

## Overview
This is the largest-scale registration test, pushing to 128 UEs with dynamically generated IMSIs. It validates the upper bounds of AMF capacity, UDM throughput, SCTP stability, and gNB context management. At 128 UEs, the test approximates a busy small cell or a medium-density macro cell scenario.

## 3GPP Background
At 128 UEs, the system processes approximately 896 NGAP messages over a single SCTP association. The UDM handles 128 authentication vector requests. The AMF maintains 128 concurrent MM contexts consuming approximately 384-640 KB of memory.

This scale tests production-grade requirements. Per TS 23.501, a single AMF instance should support tens of thousands of UEs. At 128, we are testing a small fraction of production capacity, but it is sufficient to expose fundamental scaling issues in the implementation.

The SCTP association carries all signaling for 128 UEs. With default SCTP settings, the association has limited outbound streams (typically 10-50). At 128 UEs, stream sharing is significant, and SCTP flow control may become a factor.

**Network functions involved:** 128 UEs (dynamic IMSI), gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 23.501 | 5.2 | AMF capacity (tens of thousands) |
| TS 38.412 | 7 | SCTP under sustained load |
| TS 33.501 | 6.1.3 | 5G-AKA at maximum scale |

## Problem Statement
- What if the AMF has a hardcoded UE limit below 128?
- What if the SCTP association runs out of send buffer space?
- What if subscriber provisioning is incomplete (e.g., only 100 of 128 IMSIs exist)?
- What if the test takes too long due to cumulative processing delays?
- What if gNB RAN UE NGAP ID space (16-bit) is inefficiently allocated?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For i from 1 to 128:
   a. Generate IMSI: 001011234560{i:03d}.
   b. Register UE using generated IMSI.
3. All 128 UEs registered.

## Expected Behavior
- All 128 UEs register successfully.
- Total time scales linearly (approximately 128x single-UE time).
- SCTP association remains stable for the full test duration.
- No auth failures, context limits, or resource exhaustion.

## Pass/Fail Criteria
- **Pass:** All 128 UEs reach REGISTERED state.
- **Fail:** Any UE fails; timeout; SCTP association lost.

## Key Concepts for Training

### AMF Capacity Planning
Production AMFs are designed to handle 50,000-500,000 UEs per instance. At 128 UEs, we test ~0.05% of production capacity. However, this test catches fundamental design issues: O(n^2) algorithms that work at 10 UEs but fail at 100+, memory leaks that accumulate, and database bottlenecks.

### RAN UE NGAP ID Space
The RAN UE NGAP ID is typically a 32-bit unsigned integer (per TS 38.413 Section 9.3.3.2). With 128 UEs, only 128 IDs are consumed. But if the gNB uses sequential allocation and doesn't recycle IDs efficiently, rapid cycling could exhaust smaller ID spaces.

### SCTP Performance Under Load
At 896+ messages, SCTP's congestion control algorithm (similar to TCP's) may activate. If the network path has packet loss, SCTP reduces its congestion window, slowing message delivery. SCTP's multi-streaming helps: even if one stream is congested, others continue independently. Monitor SCTP statistics (retransmits, cwnd) during the test.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| AMF UE limit hit | Registrations fail after N UEs | Increase AMF max-subscribers config |
| SCTP send buffer full | Messages queued, timeouts | Increase SO_SNDBUF size |
| UDM overloaded | Auth slows for later UEs | Scale UDM database, add replicas |
| Test timeout | 128 UEs exceeds test timeout | Increase overall test timeout to 10+ minutes |
| Subscriber gap | IMSI 067 not provisioned | Verify continuous IMSI range in UDM |

## References
- 3GPP TS 23.501 V17.x -- Section 5.2 (AMF capacity)
- 3GPP TS 38.412 V17.x -- Section 7 (SCTP)
- Related: TC-STR-012 (64 UEs), TC-STR-005 (32 UEs), TC-STR-004 (16 UEs)

## Quiz Questions
1. At 128 UEs, approximately how many NGAP messages traverse the SCTP association?
   *Answer: Approximately 896 messages (7 per UE x 128 UEs), plus NG Setup and any error/retry messages. This represents sustained signaling load over several minutes.*

2. If registration of 128 UEs takes 15 minutes when it should take 5 minutes, what are the top three suspects?
   *Answer: (1) UDM database query latency (slow auth vector generation), (2) AMF O(n) or O(n^2) context lookup, (3) SCTP congestion control throttling due to buffer exhaustion or packet loss.*

3. The RAN UE NGAP ID is a 32-bit integer. Why is 128 UEs unlikely to cause ID exhaustion, and when might it become a concern?
   *Answer: 32 bits provide over 4 billion unique IDs. At 128 UEs, only 0.000003% of the space is used. ID exhaustion could occur if: IDs are not recycled after UE deregistration (leak), the implementation uses a smaller range artificially, or rapid cycling with slow recycling accumulates stale IDs.*
