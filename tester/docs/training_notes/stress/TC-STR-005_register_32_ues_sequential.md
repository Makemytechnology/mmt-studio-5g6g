# TC-STR-005: Register 32 UEs Sequential

## Overview
This test registers 32 UEs sequentially, using dynamically generated IMSIs. It is the threshold where small-scale testing transitions to medium-scale stress testing. At 32 UEs, SCTP buffering, AMF memory allocation patterns, and UDM query throughput become significant factors.

## 3GPP Background
With 32 UEs, the test uses dynamically generated IMSIs (001011234560001 through 001011234560032) rather than pre-configured UE identities. This tests the network's ability to handle a range of subscribers from the same PLMN.

Each registration generates approximately 7 NGAP messages, totaling ~224 NGAP messages over the single SCTP association. The AMF maintains 32 concurrent MM contexts consuming ~96-160 KB of memory (3-5 KB per context including cryptographic key material).

The UDM must have subscription data for all 32 IMSIs. This may require batch subscriber provisioning rather than manual configuration.

**Network functions involved:** 32 UEs (dynamic IMSI), gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 23.501 | 5.2 | AMF capacity |
| TS 33.501 | 6.1.3 | 5G-AKA (32 auth vectors) |
| TS 38.412 | 7 | SCTP scaling |

## Problem Statement
- What if the UDM doesn't have subscription data for dynamically generated IMSIs?
- What if the AMF's context lookup degrades to O(n) with 32 entries?
- What if SCTP throughput is insufficient for 224 messages in sequence?
- What if some IMSIs have never been authenticated, causing SQN resync storms?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For i from 1 to 32:
   a. Generate IMSI: 001011234560{i:03d}.
   b. Register UE using generated IMSI.
3. All 32 UEs registered.

## Expected Behavior
- All 32 UEs register successfully with dynamic IMSIs.
- Total time scales approximately linearly with UE count.
- No SCTP congestion, no auth failures, no context table issues.

## Pass/Fail Criteria
- **Pass:** All 32 UEs reach REGISTERED state.
- **Fail:** Any UE fails to register; excessive total time.

## Key Concepts for Training

### Dynamic IMSI Generation
Instead of using pre-configured UE objects, this test generates IMSIs programmatically. The IMSI format is: MCC(001) + MNC(01) + MSIN(1234560XXX). The UDM must have pre-provisioned subscriber records for this entire IMSI range. This approach enables scaling tests without maintaining large UE configuration files.

### Medium-Scale Context Management
At 32 UEs, the AMF's context management data structures become relevant. A simple linear list gives O(n) lookup -- acceptable at 32 but problematic at 1000+. Hash tables or B-trees give O(1) or O(log n) lookup. This test helps establish a performance baseline for scaling projections.

### Subscriber Provisioning at Scale
For 32+ UEs, manual subscriber provisioning is impractical. The UDM/HSS typically supports bulk provisioning via API, CSV import, or database scripts. All 32 subscribers need: IMSI, K (permanent key), OPc (derived operator key), SQN (initial sequence number), and subscription profile (allowed DNNs, slices, AMBR).

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Missing subscribers | Auth failure for some IMSIs | Bulk-provision all 32 IMSIs in UDM |
| Slow processing | Total time > 2 minutes | Profile AMF and UDM for bottlenecks |
| SCTP timeout | Registrations stall after ~20 UEs | Increase SCTP buffers and timeouts |
| SQN resync storm | Multiple AUTS triggered | Initialize SQN=0 for all new subscribers |

## References
- 3GPP TS 23.501 V17.x -- Section 5.2 (AMF scaling)
- 3GPP TS 33.501 V17.x -- Section 6.1.3 (5G-AKA)
- Related: TC-STR-004 (16 UEs), TC-STR-012 (64 UEs), TC-STR-013 (128 UEs)

## Quiz Questions
1. Why does this test use dynamically generated IMSIs instead of pre-configured UE objects?
   *Answer: To enable scaling to large UE counts without maintaining extensive configuration files. Dynamic generation also tests the network's ability to handle a range of subscribers and ensures no hardcoded UE dependencies.*

2. At 32 UEs, what is the approximate total NGAP message count and how does this stress the SCTP association?
   *Answer: Approximately 224 messages (7 per UE x 32 UEs). This tests SCTP throughput, buffer management, and ordered delivery. At typical message sizes of 100-500 bytes, the total data volume is modest, but the message count stresses SCTP's per-message overhead.*

3. What subscriber data must be pre-provisioned in the UDM for each of the 32 dynamic IMSIs?
   *Answer: Each IMSI needs: K (128-bit permanent key), OPc (128-bit derived operator key), SQN (initial sequence number, typically 0), subscription profile (allowed DNNs like "internet" and "ims"), S-NSSAI (allowed slices), and session AMBR values.*
