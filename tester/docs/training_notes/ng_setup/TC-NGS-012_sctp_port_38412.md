# TC-NGS-012: SCTP Transport on Standard Port 38412

## Overview
This test verifies that NGAP signaling uses the IANA-registered SCTP port 38412. Correct port usage is fundamental for interoperability between gNBs and AMFs from different vendors.

## 3GPP Background
Per TS 38.412 Section 7, the NG-C interface uses SCTP as the transport protocol. IANA has registered port 38412 for NGAP signaling (similar to port 36412 for S1AP in LTE). The gNB establishes an SCTP association to the AMF's well-known port 38412. The gNB uses an ephemeral local port.

SCTP provides: reliable delivery, message boundaries (unlike TCP's byte stream), multi-streaming (multiple independent ordered channels), and multi-homing (multiple IP addresses per endpoint for path redundancy). These properties make it superior to TCP for signaling transport.

**Network functions involved:** gNB, AMF
**Transport:** SCTP, UDP port 38412

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.412 | 7 | SCTP transport for NG-C |
| RFC 4960 | 5 | SCTP association management |
| IANA | Port 38412 | NGAP signaling |

## Problem Statement
- What if the AMF is configured on a non-standard port?
- What if a firewall blocks SCTP on port 38412?
- What if the SCTP kernel module is not loaded?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration (AMF port from config).
2. Connect SCTP to AMF on port 38412.
3. Perform NG Setup.
4. Verify gNB reaches READY state (confirms port 38412 works).
5. Teardown: remove gNB.

## Expected Behavior
- SCTP four-way handshake on port 38412 succeeds.
- NG Setup completes over this port.
- gNB reaches READY state.

## Pass/Fail Criteria
- **Pass:** gNB READY on port 38412.
- **Fail:** SCTP connection fails; wrong port used.

## Key Concepts for Training

### SCTP vs. TCP for Signaling
SCTP advantages for NGAP: (1) Message framing -- each NGAP message is a complete SCTP message, no need for length-delimited framing. (2) Multi-streaming -- UE signaling on different streams avoids head-of-line blocking. (3) Multi-homing -- automatic failover between AMF IP addresses. (4) 4-way handshake -- protection against SYN flood attacks.

### SCTP Kernel Support
Linux requires the sctp kernel module (modprobe sctp) and the lksctp-tools userspace library. SCTP is not enabled by default on all Linux distributions. Some cloud environments do not support SCTP (e.g., AWS VPC natively blocks SCTP). Docker/container networking may also restrict SCTP.

### Port 38412 in the 3GPP Port Range
3GPP registers specific ports for each interface: 38412 (NGAP/NG-C), 2152 (GTP-U/N3/N9), 8805 (PFCP/N4), 36412 (S1AP, LTE equivalent). These well-known ports simplify firewall configuration and interoperability.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Firewall blocks SCTP | Connection timeout | Open SCTP port 38412 in firewall |
| SCTP module not loaded | "Protocol not supported" | Run "modprobe sctp" |
| Wrong port configured | Connection refused | Verify AMF port is 38412 |
| Cloud SCTP restriction | Cannot create SCTP socket | Use SCTP-over-UDP or different environment |

## References
- 3GPP TS 38.412 V17.x -- Section 7 (SCTP transport)
- RFC 4960 -- SCTP specification
- Related: TC-NGS-001 (NG Setup), TC-NGS-007 (reconnect)

## Quiz Questions
1. What is the IANA-registered port for NGAP, and what transport protocol does it use?
   *Answer: Port 38412, SCTP protocol.*

2. Name three advantages of SCTP over TCP for 5G signaling transport.
   *Answer: (1) Message boundaries (no application-level framing needed), (2) Multi-streaming (independent ordered delivery per stream), (3) Multi-homing (automatic failover between IP addresses).*

3. What Linux commands verify that SCTP is available and that port 38412 is reachable?
   *Answer: "modprobe sctp" (load SCTP module), "cat /proc/net/sctp/assocs" (check active SCTP associations), "ss -S" (show SCTP sockets), "ncat --sctp AMF_IP 38412" or custom SCTP connect test.*
