# TC-STR-010: Traffic 4 UEs Sequential

## Overview
This test validates 4 UEs each with active PDU sessions capable of carrying traffic. Each UE completes registration and PDU session establishment, creating the full data-plane path (GTP-U tunnel, TUN interface, IP address). This is the baseline multi-UE traffic readiness test.

## 3GPP Background
For each of the 4 UEs, the complete data path is established: UE application -> TUN interface -> GTP-U encapsulation (gNB side) -> UDP port 2152 -> UPF -> Data Network. The UPF applies per-UE QoS rules (QER) and forwarding rules (PDR/FAR).

In a production network, traffic from multiple UEs shares the same N3 GTP-U transport link between gNB and UPF but is separated by unique TEIDs. The UPF must correctly demultiplex incoming GTP-U packets by TEID and apply the correct QoS policies.

The default QoS flow (5QI=9, QFI=1) provides best-effort service for internet traffic. Each UE's traffic is independently metered and policed by the UPF according to its subscription AMBR.

**Network functions involved:** 4 UEs, gNB, AMF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7 | QoS model |
| TS 29.281 | 4 | GTP-U user plane |
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 23.501 | 5.7.2.6 | Session-AMBR |

## Problem Statement
- What if GTP-U tunnel forwarding works for one UE but fails when 4 are active?
- What if the UPF's QoS enforcement applies the wrong AMBR to a UE?
- What if traffic from one UE leaks to another UE's TUN interface?
- What if the UPF cannot handle 4 concurrent forwarding sessions?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For each UE (UE_1 through UE_4):
   a. Full registration and PDU session establishment.
   b. Record PDU session IP address.
3. All 4 UEs have active PDU sessions ready for traffic.

## Expected Behavior
- All 4 UEs have active PDU sessions with valid, unique IPs.
- GTP-U tunnels are established and functional for all 4 UEs.
- Each UE's TUN interface is correctly configured for traffic.
- UPF has 4 active PFCP sessions with correct forwarding rules.

## Pass/Fail Criteria
- **Pass:** All 4 UEs have active PDU sessions with valid IPs.
- **Fail:** Any UE fails to establish PDU session.

## Key Concepts for Training

### Traffic Readiness vs. Active Traffic
This test validates traffic readiness -- the data path is established but no active traffic is generated. Traffic readiness means: TUN interface exists, GTP-U tunnel is up, UPF has forwarding rules, IP routing is configured. Separate traffic tests (TC-TRF-*) generate actual data flows over these established paths.

### Per-UE QoS in the UPF
The UPF enforces QoS independently per UE via QER (QoS Enforcement Rules). Each UE's session has: Session-AMBR (aggregate max bit rate), per-flow MBR (if applicable), and priority level. The UPF must not allow one UE's traffic to impact another UE's guaranteed service.

### Data Path Verification
To verify the data path is operational, the tester can: (1) ping the UPF gateway from the UE IP, (2) run a small iperf3 test, or (3) send a DNS query through the tunnel. Any of these confirms the full chain: TUN -> GTP-U -> UPF -> DN.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| TUN not created | No network interface for UE | Check kernel TUN module, permissions |
| GTP-U tunnel inactive | Ping through tunnel fails | Verify TEID exchange was successful |
| UPF rules missing | Packets dropped at UPF | Check PFCP session establishment |
| Routing misconfigured | Traffic goes to wrong TUN | Verify per-host routing table entries |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7 (QoS model)
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-STR-011 (8 UEs traffic), TC-STR-006 (4 UEs PDU), TC-TRF-012 (multi-UE traffic)

## Quiz Questions
1. What is the difference between "traffic readiness" and "active traffic" testing?
   *Answer: Traffic readiness verifies the data path is established (TUN, GTP-U, UPF rules, IP). Active traffic testing generates real data flows (iperf3, ping) over the established path. A system can be traffic-ready but fail active traffic due to routing errors, firewall rules, or UPF forwarding bugs.*

2. With 4 UEs, how does the UPF know which QoS rules (AMBR) to apply to each UE's traffic?
   *Answer: The SMF creates separate PFCP sessions for each UE, each containing a QER with that UE's subscription AMBR. The UPF matches incoming packets to the correct PFCP session via PDR (uplink: by TEID, downlink: by UE IP) and applies the associated QER.*

3. If UE_1 and UE_2 both have active PDU sessions but only UE_1 can ping the UPF gateway, what should you check?
   *Answer: (1) UE_2's TUN interface exists and has the correct IP, (2) host routing table has a route for UE_2's IP via its TUN, (3) UE_2's GTP-U TEID is correctly installed in the UPF's PDR, (4) UPF's FAR for UE_2 points to the correct gNB TEID/address, (5) Check for UE_2-specific firewall rules.*
