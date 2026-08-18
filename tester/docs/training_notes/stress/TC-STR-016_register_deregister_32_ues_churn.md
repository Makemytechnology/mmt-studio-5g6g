# TC-STR-016: Register/Deregister 32 UEs Churn

## Overview
This churn test registers all 32 UEs first, then deregisters all 32 in sequence. Unlike cycle tests where each UE registers and deregisters before the next, this test creates maximum concurrent context load (32 active UEs) before tearing everything down. It validates bulk context management, bulk context release, and post-churn cleanup.

## 3GPP Background
In production networks, "churn" refers to the pattern of many UEs registering and deregistering in a short period -- common during commute hours, stadium events, or disaster scenarios. The AMF must handle: (1) building up to peak capacity as UEs register, and (2) bulk teardown as UEs deregister.

During the registration phase, the AMF accumulates 32 concurrent MM contexts. During the deregistration phase, the AMF must process 32 Deregistration Requests, send 32 UE Context Release Commands to the gNB, and release 32 sets of NAS security contexts.

The UDM processes 32 authentication vector requests during registration and potentially 32 subscription event notifications during deregistration. The SMF may need to release PDU sessions if any were active.

**Network functions involved:** 32 UEs (dynamic IMSI), gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 24.501 | 5.5.2.2 | UE-initiated Deregistration |
| TS 38.413 | 8.3.1 | UE Context Release |
| TS 23.501 | 5.2 | AMF capacity management |

## Problem Statement
- What if the AMF cannot handle 32 simultaneous deregistration requests?
- What if UE Context Release for 32 UEs overwhelms the SCTP association?
- What if the gNB fails to free all 32 UE contexts, causing memory leak?
- What if post-churn state is not clean (stale contexts, dangling timers)?
- What if the UDM's SQN updates for 32 subscribers cause database lock contention?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. **Registration phase:** For i from 1 to 32:
   a. Generate IMSI: 001011234560{i:03d}.
   b. Register UE.
3. Log: 32 UEs registered.
4. **Deregistration phase:** For i from 1 to 32:
   a. Generate IMSI: 001011234560{i:03d}.
   b. Deregister UE.
5. All 32 UEs registered then deregistered.

## Expected Behavior
- All 32 UEs register successfully during the registration phase.
- All 32 UEs deregister successfully during the deregistration phase.
- After deregistration, gNB UE count returns to 0.
- AMF has no stale contexts after full churn cycle.
- UDM SQN values are consistent for all 32 subscribers.

## Pass/Fail Criteria
- **Pass:** All 32 registrations and all 32 deregistrations succeed.
- **Fail:** Any registration or deregistration fails; stale contexts remain.

## Key Concepts for Training

### Churn Pattern
Churn describes rapid UE turnover. Unlike steady-state (fixed set of registered UEs) or gradual growth (UEs accumulating), churn involves rapid ramp-up followed by rapid ramp-down. This pattern stresses: memory allocation/deallocation, ID pool management, timer creation/cancellation, and database write throughput. High churn can cause fragmentation if context memory is not properly managed.

### Bulk Context Release
When 32 UEs deregister, the AMF sends 32 Deregistration Accept messages (NAS) and 32 UE Context Release Commands (NGAP) to the gNB. The gNB must process 32 context releases and respond with 32 UE Context Release Complete messages. This generates a burst of ~64 NGAP messages in rapid succession, testing SCTP throughput for bursty traffic.

### Post-Churn Validation
After all UEs deregister, the system should return to a clean state equivalent to immediately after NG Setup. Key checks: gNB UE count = 0, no pending NAS timers, no PFCP sessions on UPF (if PDU sessions were active), all IPs returned to pool, all TEIDs freed. Any deviation indicates a resource leak.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Stale UE contexts | UE count > 0 after all deregistrations | Check UE Context Release processing |
| Dangling timers | Timer fires after context deleted | Verify timer cancellation on deregister |
| SCTP burst congestion | Deregistrations slow or fail | Increase SCTP send buffer for burst traffic |
| UDM write storm | Slow deregistration processing | Check UDM database write throughput |
| Memory not freed | AMF memory higher after churn than before | Memory leak -- profile AMF allocations |

## References
- 3GPP TS 24.501 V17.x -- Section 5.5.1, 5.5.2 (Registration/Deregistration)
- 3GPP TS 38.413 V17.x -- Section 8.3.1 (UE Context Release)
- Related: TC-STR-005 (32 UEs reg), TC-STR-009 (8 UEs cycles), TC-STR-001 (rapid cycles)

## Quiz Questions
1. What is the difference between a "churn test" (TC-STR-016) and a "cycle test" (TC-STR-009)?
   *Answer: In a churn test, all UEs register first (building to peak), then all deregister (rapid teardown). The AMF sees maximum concurrent contexts. In a cycle test, each UE cycles independently, so the AMF typically handles only 1 active context at a time. Churn stresses capacity; cycling stresses lifecycle management.*

2. After deregistering all 32 UEs, what system state should be validated to confirm clean teardown?
   *Answer: gNB UE count = 0, no pending NAS timers in AMF, no active PFCP sessions on UPF (if PDU sessions were active), all 32 RAN UE NGAP IDs returned to pool, all IP addresses returned to SMF pool, AMF memory usage returned to pre-registration baseline.*

3. Why might the deregistration phase take longer than expected for 32 UEs?
   *Answer: Each deregistration involves: NAS Deregistration Request/Accept exchange, NGAP UE Context Release Command/Complete, AMF internal context cleanup (timer cancellation, key deletion, PDU session notification to SMF). If the AMF processes these sequentially or if the gNB is slow to respond to UE Context Release, the total time compounds across 32 UEs.*
