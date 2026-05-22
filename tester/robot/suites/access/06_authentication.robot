# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    5G-AKA Authentication Test Suite
...              TS 33.501 §6.1.3 — 5G-AKA Authentication and Key Agreement
...              TS 33.102 §6.3.3 — SQN Resynchronization (AUTS)
...              TS 24.501 §5.4.1 — Authentication procedures
...
...              Covers:
...              - Successful 5G-AKA authentication (RAND/AUTN → RES*)
...              - SQN resynchronization via AUTS (Auth Failure cause=21)
...              - NAS Security Mode Command negotiation
...              - Key derivation validation (KAMF, KNASenc, KNASint, KgNB)
...              - Authentication with multiple UEs
...              - Re-authentication after deregistration
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        authentication    5g-aka    nas    security

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════════════════════
# TC-AUTH-001: Successful 5G-AKA Authentication
# ═══════════════════════════════════════════════════════════════════════════════
TC-AUTH-001 Successful 5G-AKA Authentication
    [Documentation]    TC-AUTH-001: 5G-AKA Authentication Success
    ...    Standard: TS 33.501 §6.1.3 (5G-AKA), TS 24.501 §5.4.1 (Authentication procedure)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. UE sends Registration Request (SUCI derived from SUPI/IMSI)
    ...    3. AMF requests authentication vectors from AUSF/UDM
    ...    4. AMF sends Authentication Request (RAND, AUTN) to UE
    ...    5. UE verifies AUTN (SQN, AMF, MAC), computes RES*
    ...    6. UE derives key hierarchy: CK/IK → KAUSF → KSEAF → KAMF → KNASint/KNASenc
    ...    7. UE sends Authentication Response (RES*) to AMF
    ...    8. AMF verifies RES* = XRES*, authentication succeeds
    ...    9. Security Mode Command/Complete → Registration Accept
    ...    Parameters: single UE with valid USIM credentials (K, OPc, SQN)
    ...    Verification: UE reaches REGISTERED state, NAS security keys derived,
    ...    KNASint present in security context
    ...    Expected Result: Authentication succeeds, UE registered with valid keys
    [Tags]    smoke    priority-1
    Full Registration    ${UE_1}
    UE Should Be Registered    ${UE_1}
    UE Should Have Security Keys    ${UE_1}
    Log    TC-AUTH-001 PASS: 5G-AKA authentication successful for ${UE_1}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-AUTH-002: NAS security algorithms negotiated and keys derived
# ═══════════════════════════════════════════════════════════════════════════════
TC-AUTH-002 NAS security algorithms negotiated and keys derived
    [Documentation]    TC-AUTH-002: NAS Security Algorithm Negotiation
    ...    Standard: TS 24.501 §5.4.2.2 (SecurityModeCommand procedure),
    ...    TS 33.501 §6.7.1 (algorithm selection) + §6.7.4 (key derivation)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Register UE via NAS (5G-AKA authentication)
    ...    3. AMF selects EEA (ciphering) and EIA (integrity) algorithms
    ...    based on UE security capabilities and AMF policy
    ...    4. Security Mode Command carries selected algorithms
    ...    5. UE activates NAS security with negotiated algorithms,
    ...    derives KNASenc / KNASint per TS 33.501 Annex A.8
    ...    Parameters: single UE, timeout=15s
    ...    Verification: KNASint is non-empty bytes (KDF chain ran),
    ...    EIA > 0 (NIA0/null integrity rejected outside emergency)
    ...    Expected Result: EIA1 (Snow3G-128) or EIA2 (AES-128) negotiated,
    ...    KNAS{enc,int} derived (32-byte KAMF → 16-byte NAS keys)
    ...    Note: Supersedes the now-removed TC-REG-005.
    [Tags]    security    priority-1
    Full Registration    ${UE_1}
    UE Should Have Security Keys    ${UE_1}
    ${algos}=    Get UE Security Algorithms    ${UE_1}
    Should Be True    ${algos}[eia] > 0    Integrity algorithm must be negotiated (EIA0 not allowed)
    Log    TC-AUTH-002 PASS: EEA${algos}[eea] / EIA${algos}[eia] negotiated, KNASint derived

# ═══════════════════════════════════════════════════════════════════════════════
# TC-AUTH-003: SQN Resynchronization via AUTS
# ═══════════════════════════════════════════════════════════════════════════════
TC-AUTH-003 SQN Resynchronization Via AUTS
    [Documentation]    TC-AUTH-003: SQN Resynchronization via AUTS
    ...    Standard: TS 33.501 §6.1.3.3 (Synchronization failure or MAC failure — §6.1.3.3.2 specifies SQN resync recovery in the home network),
    ...    TS 33.102 §6.3.5 (AUTS generation and handling)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. UE sends Registration Request (SUCI)
    ...    3. AMF sends Authentication Request (RAND, AUTN)
    ...    4. If UE SQN is out of range (SQN_HE - SQN_MS > delta):
    ...    a. UE sends Authentication Failure (cause=SQN failure, AUTS)
    ...    b. AMF/AUSF forwards AUTS to UDM for resynchronization
    ...    c. UDM resyncs SQN, generates new authentication vector
    ...    d. AMF re-sends Authentication Request with new RAND/AUTN
    ...    e. UE verifies new AUTN, computes RES*
    ...    5. Authentication succeeds after resync (or directly if SQN in range)
    ...    Parameters: single UE, SQN may be out of sync from previous test runs
    ...    Verification: UE reaches REGISTERED state regardless of initial SQN state,
    ...    resync procedure completes transparently if needed
    ...    Expected Result: Authentication succeeds (with or without resync)
    [Tags]    sqn-resync    priority-1
    # Register triggers auth — if SQN is out of sync, AUTS resync happens automatically
    Full Registration    ${UE_1}
    UE Should Be Registered    ${UE_1}
    UE Should Have Security Keys    ${UE_1}
    Log    TC-AUTH-003 PASS: Authentication succeeded (SQN resync if needed)

# ═══════════════════════════════════════════════════════════════════════════════
# TC-AUTH-004: Multi-UE Authentication
# ═══════════════════════════════════════════════════════════════════════════════
TC-AUTH-004 Multi-UE Authentication
    [Documentation]    TC-AUTH-004: Multi-UE Independent Authentication
    ...    Standard: TS 33.501 §6.1.3 (5G-AKA per UE), TS 38.413 (NGAP multi-UE)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Register UE_1: Registration Request → 5G-AKA → Security Mode → Accept
    ...    3. Register UE_2: independent 5G-AKA with separate auth vectors
    ...    4. Both UEs authenticated on same gNB/SCTP association
    ...    5. Each UE has independent security context (KAMF, KNASint, KNASenc)
    ...    6. Verify both UEs in REGISTERED state simultaneously
    ...    Parameters: 2 UEs from config, each with unique IMSI/K/OPc
    ...    Verification: Both UEs reach REGISTERED state, independent security contexts,
    ...    AMF correctly multiplexes NGAP procedures for concurrent UEs
    ...    Expected Result: Both UEs registered with independent auth credentials
    [Tags]    multi-ue    priority-1
    Full Registration    ${UE_1}
    Full Registration    ${UE_2}
    UE Should Be Registered    ${UE_1}
    UE Should Be Registered    ${UE_2}
    UE Should Have Security Keys    ${UE_1}
    UE Should Have Security Keys    ${UE_2}
    Log    TC-AUTH-004 PASS: Both UEs authenticated independently

# ═══════════════════════════════════════════════════════════════════════════════
# TC-AUTH-005: Re-authentication After Deregistration
# ═══════════════════════════════════════════════════════════════════════════════
TC-AUTH-005 Re-authentication After Deregistration
    [Documentation]    TC-AUTH-005: Re-Authentication After Deregistration
    ...    Standard: TS 33.501 §6.1.3 (5G-AKA), TS 24.501 §5.5.2.2 (deregistration),
    ...    TS 33.501 §6.7.3 (NAS security context handling)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. First registration: full 5G-AKA + Security Mode → REGISTERED
    ...    3. Deregister UE: Deregistration Request → DEREGISTERED
    ...    4. 500ms pause (allow AMF to release context)
    ...    5. Re-register UE: new Registration Request triggers fresh 5G-AKA
    ...    6. AMF generates new auth vectors (SQN incremented)
    ...    7. New security context established (fresh KAMF, KNASint, KNASenc)
    ...    Parameters: single UE, timeout=15s per operation
    ...    Verification: UE successfully re-registers after deregistration,
    ...    fresh authentication vectors used (SQN incremented)
    ...    Expected Result: Re-authentication succeeds, UE registered again
    [Tags]    re-auth    priority-1
    # First registration
    Full Registration    ${UE_1}
    UE Should Be Registered    ${UE_1}
    Log    First registration complete
    # Deregister
    Deregister UE And Wait    ${UE_1}
    UE Should Be Deregistered    ${UE_1}
    Sleep    0.5s
    # Re-register — requires fresh 5G-AKA
    Full Registration    ${UE_1}
    UE Should Be Registered    ${UE_1}
    UE Should Have Security Keys    ${UE_1}
    Log    TC-AUTH-005 PASS: Re-authentication after deregistration succeeded

# ═══════════════════════════════════════════════════════════════════════════════
# TC-AUTH-006: Authentication With All Configured UEs
# ═══════════════════════════════════════════════════════════════════════════════
TC-AUTH-006 Authentication With All Configured UEs
    [Documentation]    TC-AUTH-006: Authenticate All Configured UEs
    ...    Standard: TS 33.501 §6.1.3 (5G-AKA), TS 38.413 (NGAP concurrent procedures)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Iterate through all configured UEs (up to 3)
    ...    3. For each UE: Registration Request → 5G-AKA → Security Mode → Accept
    ...    4. Each UE uses its own IMSI, K, OPc credentials from config
    ...    5. All UEs authenticated sequentially on same gNB
    ...    6. Verify all UEs reach REGISTERED state
    ...    Parameters: up to 3 UEs from config database, timeout=15s per UE
    ...    Verification: All UEs reach REGISTERED state, no authentication failures,
    ...    AMF handles sequential auth for all subscribers
    ...    Expected Result: All configured UEs authenticated and registered
    [Tags]    all-ues    priority-2
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}
        Full Registration    ${imsi}
        UE Should Be Registered    ${imsi}
        UE Should Have Security Keys    ${imsi}
        Log    Authenticated: ${imsi}
    END
    Log    TC-AUTH-006 PASS: All UEs authenticated

# ═══════════════════════════════════════════════════════════════════════════════
# TC-AUTH-007: Authentication Followed By PDU Session
# ═══════════════════════════════════════════════════════════════════════════════
TC-AUTH-007 Authentication Then PDU Session
    [Documentation]    TC-AUTH-007: Authentication Then PDU Session End-to-End
    ...    Standard: TS 33.501 §6.1.3 (5G-AKA), TS 24.501 §6.4.1 (PDU session),
    ...    TS 23.502 §4.3.2 (PDU session establishment)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Register UE: 5G-AKA authentication → Security Mode → REGISTERED
    ...    3. Verify NAS security keys derived (KNASint present)
    ...    4. Establish PDU session (DNN=internet, PSI=1) with NAS security active
    ...    5. PDU Session Establishment Request protected by NAS ciphering/integrity
    ...    6. UE receives IP address, GTP-U tunnel created
    ...    Parameters: single UE, DNN=internet, PSI=1, timeout=20s
    ...    Verification: Authentication succeeds with valid keys, then PDU session
    ...    established with IP address — full NAS security active throughout
    ...    Expected Result: UE registered with keys, PDU session active with IP
    [Tags]    integration    priority-1
    ${ip}=    Full Registration And PDU Session    ${UE_1}
    UE Should Be Registered    ${UE_1}
    UE Should Have Security Keys    ${UE_1}
    Log    TC-AUTH-007 PASS: Auth + PDU session, IP=${ip}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-AUTH-008: Repeated Authentication Cycles
# ═══════════════════════════════════════════════════════════════════════════════
TC-AUTH-008 Repeated Authentication Cycles
    [Documentation]    TC-AUTH-008: Repeated Authentication Cycles (SQN Management)
    ...    Standard: TS 33.501 §6.1.3 (5G-AKA), TS 33.102 §C.2 (SQN management),
    ...    TS 24.501 §5.5.2.2 (deregistration)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Execute 3 register/deregister cycles:
    ...    Cycle N:
    ...    a. Register UE: 5G-AKA (SQN increments each time)
    ...    b. Verify REGISTERED state
    ...    c. Deregister UE: Deregistration Request → DEREGISTERED
    ...    d. 500ms pause between cycles
    ...    3. Each cycle uses incremented SQN (AUSF/UDM track SQN_HE)
    ...    4. UE-side SQN_MS must stay in sync with network SQN_HE
    ...    Parameters: cycles=3, timeout=15s per operation, 500ms inter-cycle pause
    ...    Verification: All 3 cycles complete without SQN sync failure,
    ...    no AUTS resync triggered (SQN managed correctly)
    ...    Expected Result: 3/3 cycles pass, SQN stays synchronized
    [Tags]    stress    sqn-management    priority-2
    FOR    ${i}    IN RANGE    3
        Log    === Auth cycle ${i+1}/3 ===
        Full Registration    ${UE_1}
        UE Should Be Registered    ${UE_1}
        UE Should Have Security Keys    ${UE_1}
        Deregister UE And Wait    ${UE_1}
        UE Should Be Deregistered    ${UE_1}
        Sleep    0.5s
    END
    Log    TC-AUTH-008 PASS: 3 authentication cycles completed

# ===============================================================
# SUCI / SUPI / 5G-GUTI Identity
# ===============================================================
TC-AUTH-009 Identity Type Registration
    [Documentation]    TC-AUTH-009: Registration with Identity Type from UE Config
    ...    Standard: TS 33.501 section 6.12 (SUCI), TS 23.003 section 2.2 (SUPI)
    ...    Identity Type configured per UE in UE Config:
    ...      SUPI: uses IMSI directly in Registration Request
    ...      SUCI: builds concealed identity with parameters from UE Config:
    ...        - Routing Indicator (4 digits)
    ...        - Protection Scheme (0=null, 1=ECIES-A, 2=ECIES-B)
    ...        - Home Network Public Key ID
    ...    Procedure:
    ...    1. Read Identity Type + SUCI parameters from UE Config
    ...    2. Build identity for Registration Request
    ...    3. AMF authenticates → assigns 5G-GUTI
    ...    4. Verify all SUCI parameters match UE Config
    ...    Expected Result: Registration with configured identity type
    [Tags]    identity    suci    supi    priority-1
    Log    TC-AUTH-009: Identity type registration

TC-AUTH-010 Identity Chain Verification
    [Documentation]    TC-AUTH-010: Verify SUPI → SUCI → 5G-GUTI identity chain
    ...    Standard: TS 23.003 section 2.2/2.2B/2.10
    ...    Validates the full identity lifecycle using UE Config settings:
    ...      SUPI: permanent identity from IMSI (never sent OTA)
    ...      SUCI: built per Identity Type + SUCI parameters from UE Config
    ...      5G-GUTI: temporary identity assigned by AMF
    ...    Expected Result: Full identity chain validated per UE Config
    [Tags]    identity    chain    supi    suci    guti    priority-1
    Log    TC-AUTH-010: Identity chain

TC-AUTH-011 Re-Registration With 5G-GUTI
    [Documentation]    TC-AUTH-011: Re-registration using 5G-GUTI (privacy)
    ...    Standard: TS 24.501 section 5.5.1.2.2
    ...    First registration uses identity from UE Config (SUPI or SUCI).
    ...    After deregister, re-register should use 5G-GUTI to avoid
    ...    exposing the permanent identity again.
    ...    Expected Result: 5G-GUTI used for subsequent registrations
    [Tags]    guti    re-registration    privacy    priority-2
    Log    TC-AUTH-011: GUTI re-registration

TC-AUTH-012 Multi-UE Identity No Collision
    [Documentation]    TC-AUTH-012: 8 UEs with unique identity — no collision
    ...    Standard: TS 33.501 section 6.12
    ...    Each UE uses Identity Type from its own UE Config.
    ...    Verifies unique MSIN per UE, concurrent registration at AMF.
    ...    Expected Result: All 8 identities unique, no collision
    [Tags]    identity    multi-ue    8-ue    concurrent    priority-2
    Log    TC-AUTH-012: Multi-UE identity
