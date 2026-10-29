# TC-TRF-003: TCP Bidirectional Throughput Test

## Overview
This test measures simultaneous TCP throughput in both uplink and downlink directions through the GTP-U tunnel. Using iperf3's --bidir flag, it generates full-duplex traffic, testing the data path's ability to handle concurrent UL and DL streams without interference.

## 3GPP Background
In production 5G, UEs simultaneously upload and download data (e.g., video call: upload camera, download remote video). The GTP-U tunnel supports bidirectional traffic through independent UL and DL TEIDs. The UPF applies independent UL and DL QoS enforcement (separate QER rules for each direction).

The UL Session-AMBR and DL Session-AMBR are independently configured in the UE's subscription. The UPF enforces them separately. Bidirectional throughput should be approximately the sum of individual UL and DL throughput if the N3 link has sufficient capacity.

**Traffic paths (simultaneous):**
- UL: UE -> TUN -> GTP-U -> UPF -> Core
- DL: Core -> UPF -> GTP-U -> TUN -> UE

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 29.281 | 4 | GTP-U (bidirectional) |
| TS 23.501 | 5.7.2.6 | Session-AMBR (UL/DL independent) |

## Problem Statement
- What if UL traffic starves DL or vice versa?
- What if the GTP-U socket cannot handle bidirectional load?
- What if QoS enforcement incorrectly combines UL+DL rates?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish internet PDU session.
2. Start iperf3 server on SA Core.
3. Run iperf3 client with --bidir flag (simultaneous UL + DL).
4. Measure TX (uplink) and RX (downlink) throughput.
5. Collect UPF stats for both directions.

## Expected Behavior
- Both UL and DL streams achieve > 50 Mbps on local network.
- No significant throughput asymmetry (unless AMBR differs per direction).
- UPF shows both ul_bytes and dl_bytes > 0.
- Zero retransmits, zero drops.

## Pass/Fail Criteria
- **Pass:** Both TX and RX > 0 Mbps; stable throughput.
- **Fail:** One direction fails; severe asymmetry; drops.

## Key Concepts for Training

### Full-Duplex GTP-U
GTP-U supports full-duplex operation: UL packets use the UL TEID (gNB -> UPF) while DL packets use the DL TEID (UPF -> gNB) simultaneously. The GTP-U UDP socket handles both directions. The UPF processes UL and DL independently through different PDR/FAR chains.

### Independent UL/DL AMBR
The Session-AMBR has separate UL and DL values (e.g., UL=50 Mbps, DL=100 Mbps for typical mobile subscription). The UPF enforces these independently. Bidirectional testing validates that: (1) both directions work simultaneously, (2) AMBR enforcement is direction-specific, (3) no cross-direction interference.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| UL starves DL | DL throughput drops during bidir | Check socket buffer allocation |
| Socket contention | Both directions slow | Increase GTP-U socket buffers |
| AMBR cross-contamination | Combined rate limited | Verify independent UL/DL QER |

## References
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-TRF-001 (UL only), TC-TRF-002 (DL only), TC-TRF-006 (UDP bidir)

## Quiz Questions
1. In bidirectional traffic, do UL and DL share the same GTP-U TEID?
   *Answer: No. UL uses the uplink TEID (allocated by UPF) and DL uses the downlink TEID (allocated by gNB). They are independent identifiers for independent directions.*

2. If UL AMBR = 50 Mbps and DL AMBR = 100 Mbps, what is the maximum bidirectional aggregate throughput?
   *Answer: 150 Mbps (50 UL + 100 DL). AMBR is enforced independently per direction. The UPF does not combine them.*

3. Why might bidirectional throughput be less than the sum of individual UL and DL throughput?
   *Answer: Shared resources: the N3 link bandwidth is shared between directions, the GTP-U socket CPU is shared, SCTP signaling may compete with data, and the TUN interface processing is sequential. On a 1 Gbps link, 500 Mbps UL + 500 Mbps DL = 1 Gbps, saturating the link.*
