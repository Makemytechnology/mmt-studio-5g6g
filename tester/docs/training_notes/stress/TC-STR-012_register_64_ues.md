# TC-STR-012: Register 64 UEs

## Overview
This large-scale test registers 64 UEs sequentially using dynamically generated IMSIs. It validates AMF capacity, UDM throughput, SCTP association stability, and gNB context management under significant load. This is a production-representative scale for small cell deployments.

## 3GPP Background
At 64 UEs, the system processes approximately 448 NGAP messages (7 per UE) over a single SCTP association. The UDM handles 64 authentication vector requests and 64 SQN updates. The AMF maintains 64 concurrent MM contexts consuming approximately 192-320 KB of memory.

This scale tests the AMF's internal data structures for efficiency. A hash-table-based context lookup gives O(1) per operation. A poorly implemented linear scan gives O(n) per registration, yielding O(n^2) total for N registrations -- at 64 UEs, this means ~4096 operations vs. ~64 for O(1).

SCTP heartbeats and association management must remain stable throughout the ~2-5 minute test duration. SCTP path MTU discovery, congestion control, and retransmission timers affect sustained signaling throughput.

**Network functions involved:** 64 UEs (dynamic IMSI), gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 23.501 | 5.2 | AMF capacity |
| TS 33.501 | 6.1.3 | 5G-AKA at scale |
| TS 38.412 | 7 | SCTP for sustained signaling |

## Problem Statement
- What if AMF context lookup degrades non-linearly at 64 entries?
- What if the UDM database becomes a bottleneck with 64 queries?
- What if SCTP congestion control kicks in, throttling signaling?
- What if memory allocation for 64 contexts causes fragmentation?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For i from 1 to 64:
   a. Generate IMSI: 001011234560{i:03d}.
   b. Register UE using generated IMSI.
3. All 64 UEs registered.

## Expected Behavior
- All 64 UEs register successfully.
- Total time scales linearly (approximately 64x single-UE time).
- SCTP association remains stable throughout.
- No SQN resync or authentication failures.

## Pass/Fail Criteria
- **Pass:** All 64 UEs reach REGISTERED state.
- **Fail:** Any UE fails; severe timing degradation.

## Key Concepts for Training

### Large-Scale Context Management
At 64 UEs, the AMF's context management must be efficient. Key operations: (1) Lookup by SUPI -- used when AMF receives NAS message from known UE, (2) Lookup by 5G-GUTI -- used for subsequent registrations, (3) Lookup by NGAP ID pair -- used for all NGAP messages. All three lookups must be O(1) or O(log n) for scalable performance.

### UDM Database Throughput
The UDM stores subscriber data (K, OPc, SQN, subscription profile) in a database. At 64 sequential requests, the critical metric is per-query latency. If each auth vector query takes 5ms, 64 queries take 320ms. If it takes 50ms (due to disk I/O), 64 queries take 3.2 seconds. Database indexing on IMSI is essential.

### SCTP Association Health
During a 2-5 minute test with 448+ messages, the SCTP association must remain healthy. SCTP heartbeats (every 30s by default) verify path liveness. If the peer fails to respond to heartbeats, the association is aborted. Network latency spikes during heavy signaling could interfere with heartbeat timing.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Non-linear timing | 64 UEs take 10x longer than 8 UEs | Profile AMF for O(n^2) patterns |
| UDM timeout | Auth fails for later UEs | Check UDM database connection pool size |
| SCTP abort | Association lost mid-test | Increase SCTP heartbeat interval |
| Memory pressure | AMF crashes near 64 UEs | Monitor AMF memory, increase allocation |
| Missing subscribers | Auth failure for some IMSIs | Verify all 64 IMSIs provisioned in UDM |

## References
- 3GPP TS 23.501 V17.x -- Section 5.2 (AMF capacity)
- 3GPP TS 33.501 V17.x -- Section 6.1.3 (5G-AKA)
- Related: TC-STR-005 (32 UEs), TC-STR-013 (128 UEs), TC-STR-004 (16 UEs)

## Quiz Questions
1. At 64 UEs registered sequentially, what is the expected total NGAP message count?
   *Answer: Approximately 448 messages (7 per UE x 64 UEs). Including NG Setup and other non-UE signaling, the total is slightly higher.*

2. How much memory does the AMF consume for 64 concurrent UE contexts?
   *Answer: Approximately 192-320 KB (3-5 KB per context x 64). This includes NAS security keys (32 bytes each for KNASint, KNASenc, KAMF), state machine data, timer structures, and PDU session references.*

3. If registering 32 UEs takes 60 seconds and 64 UEs takes 180 seconds (instead of expected 120 seconds), what scaling issue is this?
   *Answer: Super-linear (worse than O(n)) scaling. The 50% extra time suggests an O(n log n) or O(n^2) component, likely in context lookup or UDM queries. Common cause: linear scan of existing contexts during each new registration.*
