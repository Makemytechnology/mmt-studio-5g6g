# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Emergency Services Test Suite
...              TS 23.501 §5.16 — Emergency services in 5GS
...              TS 23.167 — IMS emergency sessions
...              TS 24.501 §5.5.1.2 — Initial registration (emergency branches at §5.5.1.2.6 / §5.5.1.2.6A)
...              Covers: Emergency PDU session, ECSCF routing, emergency registration,
...              location reporting, unauthenticated emergency, deregistration cleanup
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        emergency    e911    public-safety

*** Test Cases ***
# ===============================================================
# Emergency PDU Session
# TS 23.501 §5.16.4 — Emergency PDU session establishment
# TS 24.501 §6.4.1 — PDU session with request_type=emergency
# ===============================================================
TC-EMG-001 Emergency PDU Session Establishment Request Type 3
    [Documentation]    TC-EMG-001: Establish emergency PDU session (request_type=3)
    ...    Standard: TS 24.501 §6.4.1.2 (request_type=emergency), TS 23.501 §5.16.4
    ...    Procedure:
    ...    1. Register UE (standard registration)
    ...    2. UE sends PDU Session Establishment Request with request_type=3 (emergency)
    ...    3. AMF identifies emergency request, selects emergency SMF
    ...    4. SMF allocates IP from emergency pool, creates UPF session
    ...    5. PDU Session Accept with emergency indication
    ...    6. Verify DNN=sos or emergency-specific DNN used
    ...    Verification:
    ...    - PDU session established with emergency type
    ...    - IP allocated from emergency address pool
    ...    - Session flagged as emergency in AMF/SMF state
    ...    Expected Result: Emergency PDU session active with DNN=sos
    [Tags]    pdu-session    request-type-3    sos    priority-1
    Log    TC-EMG-001: Emergency PDU Session (request_type=3)

TC-EMG-002 Emergency Call Routing Via ECSCF
    [Documentation]    TC-EMG-002: Emergency call routed through E-CSCF to PSAP
    ...    Standard: TS 23.167 §7.3 (E-CSCF routing), TS 23.501 §5.16.5
    ...    Procedure:
    ...    1. UE with active emergency PDU session (TC-EMG-001 precondition)
    ...    2. UE initiates SIP INVITE to emergency number (e.g., urn:service:sos)
    ...    3. P-CSCF identifies emergency dialog, routes to E-CSCF
    ...    4. E-CSCF determines PSAP based on UE location
    ...    5. INVITE forwarded to PSAP (or simulated endpoint)
    ...    6. Verify emergency call setup with location information
    ...    Verification:
    ...    - SIP INVITE routed via E-CSCF (not standard I-CSCF/S-CSCF path)
    ...    - Location information (cell ID, TAI) included in SIP headers
    ...    - Emergency dialog established end-to-end
    ...    Expected Result: Emergency call routed to PSAP via E-CSCF
    [Tags]    sip    ecscf    psap    routing    priority-1
    Log    TC-EMG-002: Emergency Call Routing (E-CSCF)

TC-EMG-003 Emergency Registration Limited Service
    [Documentation]    TC-EMG-003: Emergency registration for limited-service UE
    ...    Standard: TS 24.501 §5.5.1.2.6 (Initial registration for emergency services not accepted by the network — limited-service UE rejection path)
    ...    Procedure:
    ...    1. UE without valid subscription attempts emergency registration
    ...    2. UE sends Registration Request with 5GS registration type = emergency
    ...    3. AMF accepts emergency registration (limited service state)
    ...    4. UE granted emergency services only (no normal data/voice)
    ...    5. Verify UE context shows emergency-only registration
    ...    Verification:
    ...    - Registration Accept with emergency-only indication
    ...    - UE restricted to emergency services (no DNN=internet)
    ...    - AMF state shows RM-REGISTERED with emergency flag
    ...    Expected Result: Limited-service emergency registration accepted
    [Tags]    registration    limited-service    priority-1
    Log    TC-EMG-003: Emergency Registration (Limited Service)

# ===============================================================
# Location & Unauthenticated Emergency
# TS 23.501 §5.16.2 — Location for emergency
# TS 23.501 §5.16.3 — Unauthenticated emergency
# ===============================================================
TC-EMG-010 Location Reporting During Emergency
    [Documentation]    TC-EMG-010: Location reporting active during emergency session
    ...    Standard: TS 23.501 §5.16.2 (Location services for emergency)
    ...    Procedure:
    ...    1. UE with active emergency PDU session
    ...    2. AMF activates location reporting for emergency UE
    ...    3. gNB reports UE location (cell ID, TAI, coordinates if available)
    ...    4. Location forwarded to E-CSCF/PSAP via SIP Geolocation header
    ...    5. Verify periodic location updates during emergency session
    ...    Verification:
    ...    - Location reporting activated upon emergency session start
    ...    - Cell ID and TAI reported to AMF
    ...    - Location available in emergency SIP signaling
    ...    Expected Result: Continuous location reporting during emergency
    [Tags]    location    reporting    geolocation    priority-1
    Log    TC-EMG-010: Location Reporting During Emergency

TC-EMG-011 Emergency Session Without Authentication
    [Documentation]    TC-EMG-011: Emergency services granted without 5G-AKA
    ...    Standard: TS 23.501 §5.16.3 (Unauthenticated emergency)
    ...    Procedure:
    ...    1. UE with no USIM or invalid credentials
    ...    2. UE sends Registration Request (emergency type)
    ...    3. AMF skips authentication (5G-AKA not required for emergency)
    ...    4. AMF assigns temporary 5G-GUTI for emergency UE
    ...    5. UE establishes emergency PDU session (request_type=3)
    ...    6. Verify emergency call possible without authentication
    ...    Verification:
    ...    - No Authentication Request sent by AMF
    ...    - Temporary identity assigned
    ...    - Emergency PDU session and call functional
    ...    Expected Result: Unauthenticated emergency access granted
    [Tags]    unauthenticated    no-usim    priority-1
    Log    TC-EMG-011: Emergency Without Authentication

TC-EMG-012 Emergency Deregistration Cleanup
    [Documentation]    TC-EMG-012: Cleanup of emergency state on deregistration
    ...    Standard: TS 24.501 §5.5.2.2.1 (Deregistration for emergency)
    ...    Procedure:
    ...    1. UE with active emergency registration and emergency PDU session
    ...    2. Emergency call completes (BYE exchange)
    ...    3. UE sends Deregistration Request (or network-initiated)
    ...    4. AMF releases emergency PDU session
    ...    5. SMF frees emergency IP back to pool
    ...    6. AMF removes emergency UE context
    ...    Verification:
    ...    - Emergency PDU session released cleanly
    ...    - Emergency IP returned to pool
    ...    - UE context removed from AMF (no stale emergency state)
    ...    - Location reporting deactivated
    ...    Expected Result: All emergency resources cleaned up
    [Tags]    deregistration    cleanup    priority-1
    Log    TC-EMG-012: Emergency Deregistration Cleanup
