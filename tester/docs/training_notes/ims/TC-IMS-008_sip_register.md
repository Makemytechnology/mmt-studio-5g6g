# TC-IMS-008: SIP REGISTER to P-CSCF

## Overview
This test validates the SIP REGISTER procedure through the IMS PDU session. The UE sends a SIP REGISTER to the P-CSCF (discovered via PCO) to register in the IMS core. IMS registration is required before the UE can make or receive VoNR calls.

## 3GPP Background
SIP REGISTER (TS 24.229 Section 5.1) is the IMS registration procedure. The UE sends REGISTER to the P-CSCF, which proxies it through the I-CSCF to the S-CSCF. The S-CSCF authenticates the UE (IMS-AKA or SIP Digest) and stores the UE's contact binding (UE IP:port for receiving SIP messages).

**SIP REGISTER message:**
```
REGISTER sip:ims.domain.com SIP/2.0
Via: SIP/2.0/UDP ue_ims_ip:5060
From: <sip:imsi@ims.domain.com>
To: <sip:imsi@ims.domain.com>
Contact: <sip:imsi@ue_ims_ip:5060>
Authorization: [IMS-AKA or Digest credentials]
Expires: 3600
```

The P-CSCF routes the REGISTER to the I-CSCF (via DNS), which selects an S-CSCF. The S-CSCF validates credentials and responds with 200 OK if successful.

**Network functions involved:** UE (SIP client), P-CSCF, I-CSCF, S-CSCF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.229 | 5.1 | IMS registration procedure |
| TS 23.228 | 5.2 | IMS access via 5GC |
| TS 33.203 | * | IMS security (IMS-AKA) |

## Problem Statement
- What if the P-CSCF address is unreachable from the IMS UE IP?
- What if IMS-AKA authentication fails at the S-CSCF?
- What if the SIP REGISTER times out (no 200 OK)?
- What if the UE IP in the Contact header is not routable?

## Test Procedure (Step-by-Step)
1. Register UE via NAS, establish IMS PDU session.
2. Extract P-CSCF address from PCO.
3. Create SIP client bound to UE IMS IP.
4. Send SIP REGISTER to P-CSCF (port 5060).
5. Wait for 200 OK response.

## Expected Behavior
- SIP REGISTER sent to P-CSCF through IMS GTP-U tunnel.
- P-CSCF proxies to I-CSCF/S-CSCF.
- 200 OK received (UE registered in IMS).

## Pass/Fail Criteria
- **Pass:** SIP status 200 OK received.
- **Fail:** Timeout; 401 (unauthorized); 403 (forbidden); 404 (not found).

## Key Concepts for Training

### IMS Registration Flow
1. UE -> P-CSCF: REGISTER
2. P-CSCF -> I-CSCF: REGISTER (with path header)
3. I-CSCF -> S-CSCF: REGISTER (S-CSCF selected via DNS)
4. S-CSCF: authenticates UE, stores contact binding
5. S-CSCF -> I-CSCF -> P-CSCF -> UE: 200 OK (or 401 for auth challenge)

### IMS-AKA Authentication
IMS-AKA (TS 33.203) is similar to 5G-AKA but for the IMS layer. The S-CSCF challenges the UE with 401 Unauthorized containing a nonce (derived from RAND/AUTN). The UE computes a response using its ISIM credentials. The second REGISTER includes Authorization header with the response.

### SIP vs NAS Registration
SIP registration (IMS layer) is separate from NAS registration (5G layer). NAS registration authenticates the UE to the 5G core (AMF/AUSF). SIP registration authenticates the UE to the IMS core (S-CSCF). Both are required for VoNR -- NAS registration for data connectivity, SIP registration for voice service.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| P-CSCF unreachable | SIP REGISTER timeout | Check UPF N6 connectivity to P-CSCF |
| Auth failure | 401/403 response | Check IMS credentials (ISIM/K) |
| Wrong Contact | P-CSCF can't reach UE | Verify UE IMS IP in Contact header |
| DNS failure | I-CSCF can't find S-CSCF | Check IMS DNS configuration |

## References
- 3GPP TS 24.229 V17.x -- Section 5.1 (IMS registration)
- 3GPP TS 33.203 V17.x -- IMS security
- Related: TC-IMS-001 (IMS PDU), TC-IMS-009 (SIP INVITE), TC-IMS-011 (call quality)

## Quiz Questions
1. What is the SIP REGISTER message, and what does a 200 OK response mean?
   *Answer: SIP REGISTER is the IMS registration request that binds the UE's SIP URI to its current IP:port. A 200 OK means the S-CSCF accepted the registration -- the UE is now reachable for incoming calls and can initiate outgoing calls.*

2. Why are both NAS registration and SIP registration required for VoNR?
   *Answer: NAS registration (5G-AKA) provides data connectivity (PDU session with IP). SIP registration (IMS-AKA) provides voice service (registers the UE in the IMS so it can make/receive calls). Without NAS registration, there is no IP connectivity. Without SIP registration, the IMS does not know the UE exists.*

3. What role does the P-CSCF play in SIP REGISTER?
   *Answer: The P-CSCF acts as a SIP proxy -- it receives the REGISTER from the UE, adds Via/Path headers, and forwards it to the I-CSCF. It does not authenticate the UE (that is the S-CSCF's job). The P-CSCF also applies IPsec/TLS security on the UE-P-CSCF link.*
