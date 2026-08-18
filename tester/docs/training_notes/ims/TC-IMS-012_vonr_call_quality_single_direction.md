# TC-IMS-012: VoNR Single-Direction Voice Quality

## Overview
This test measures VoNR voice quality in a single direction (UE_A -> UE_B only). By isolating one direction, it identifies asymmetric quality issues -- for example, if UL jitter is worse than DL. Single-direction testing helps pinpoint which path segment contributes to quality degradation.

## 3GPP Background
In a VoNR call, each direction may have different quality due to: asymmetric network paths, different UPF processing for UL vs DL, different GTP-U tunnel characteristics, or different TUN interface performance. Single-direction testing isolates these factors.

The sender transmits AMR-WB RTP at 50 packets/second (20ms frames). The receiver measures: jitter, packet loss, and arrival timing to estimate MOS.

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 26.114 | 10 | Voice quality |
| ITU-T G.107 | * | E-model |
| TS 23.501 | 5.7.2.1 | 5QI=1 |

## Problem Statement
- What if UL quality is significantly worse than DL?
- What if one direction has high jitter but the other doesn't?
- What if packet loss is direction-dependent?

## Test Procedure (Step-by-Step)
1. Register UE_1, establish IMS PDU session.
2. Generate single-direction RTP (UE_A -> server) at AMR-WB rate.
3. Measure jitter, loss, estimate MOS.

## Expected Behavior
- Single-direction MOS >= 3.5.
- Jitter < 50ms; loss < 1%.
- Quality metrics for isolated direction.

## Pass/Fail Criteria
- **Pass:** MOS >= 3.5; acceptable jitter and loss.
- **Fail:** MOS < 3.5; high jitter or loss.

## Key Concepts for Training

### Directional Quality Analysis
By comparing single-direction results (UL only vs DL only vs bidirectional), engineers can identify: (1) If UL is worse: gNB->UPF path issue (GTP-U encapsulation, UPF UL processing). (2) If DL is worse: UPF->gNB path issue (GTP-U encapsulation by UPF, TUN receive processing). (3) If bidirectional is worse than both individual: cross-direction interference.

### One-Way Metrics vs Round-Trip
Single-direction testing provides true one-way metrics (not RTT/2 estimates). One-way jitter and loss are directly measurable. This is more accurate than bidirectional metrics for diagnosing specific path segments.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| UL worse than DL | Higher UL jitter | Check GTP-U encap processing |
| DL worse than UL | Higher DL loss | Check UPF DL scheduling |
| Both good, bidir bad | Interference | Check shared resource contention |

## References
- ITU-T G.107 -- E-model
- Related: TC-IMS-011 (bidirectional), TC-IMS-003 (voice traffic), TC-IMS-005 (latency)

## Quiz Questions
1. Why is single-direction testing useful in addition to bidirectional testing?
   *Answer: It isolates quality issues to a specific direction. If bidirectional MOS is low but single-direction UL MOS is high, the issue is in the DL path. This narrows troubleshooting to the DL components (UPF DL processing, DL GTP-U path, TUN receive).*

2. If UL MOS is 4.2 and DL MOS is 3.0, where should you investigate?
   *Answer: The DL path: UPF DL packet processing, DL GTP-U encapsulation (UPF -> gNB), TUN receive buffer/processing on the gNB side, DL QoS scheduling at the UPF.*

3. How many RTP packets per second does AMR-WB generate in a single direction?
   *Answer: 50 packets/second (one 20ms voice frame per packet, 1000ms/20ms = 50).*
