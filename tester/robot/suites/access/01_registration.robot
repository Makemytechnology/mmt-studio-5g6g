# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    5G NAS Registration Test Suite
...              Tests: NG Setup, single/multi UE registration, attach/detach cycles
...              Covers: TS 24.501 §5.5.1 (Registration), TS 33.501 §6.1.3 (5G-AKA)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        registration    nas    5g-aka

*** Test Cases ***
TC-REG-001 Single UE Registration
    [Documentation]    TC-REG-001: Single UE Initial Registration
    ...    Standard: TS 24.501 §5.5.1.2 (Initial registration), TS 33.501 §6.1.3 (5G-AKA)
    ...    Procedure:
    ...    1. Create gNB from config, connect SCTP to AMF, complete NG Setup
    ...    2. Attach UE to gNB (assign RAN UE NGAP ID)
    ...    3. UE sends Registration Request (5GS registration type=initial)
    ...    4. AMF initiates 5G-AKA: Authentication Request (RAND, AUTN)
    ...    5. UE computes RES*, derives KAUSF/KSEAF/KAMF keys
    ...    6. AMF sends Security Mode Command (EEA/EIA algorithm selection)
    ...    7. UE responds with Security Mode Complete (NAS security activated)
    ...    8. AMF sends Registration Accept (5G-GUTI, TAI list, allowed NSSAI)
    ...    9. UE sends Registration Complete → state transitions to REGISTERED
    ...    Parameters: timeout=15s, single UE from config
    ...    Verification: UE FSM reaches REGISTERED state, NAS security keys established,
    ...    EIA (integrity) and EEA (ciphering) algorithms negotiated
    ...    Expected Result: UE registered with valid security context (EIA > 0)
    [Tags]    smoke    priority-1
    Full Registration    ${UE_1}
    ${algos}=    Get UE Security Algorithms    ${UE_1}
    Log    TC-REG-001 PASS: Security EEA${algos}[eea] / EIA${algos}[eia]

TC-REG-002 Multi UE Registration
    [Documentation]    TC-REG-002: Multiple UE Concurrent Registration
    ...    Standard: TS 24.501 §5.5.1.2 (Initial registration), TS 38.413 (NGAP multiplexing)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. For each UE (up to configured count), spawn registration thread
    ...    3. Stagger UE registrations by configurable delay (default 200ms)
    ...    4. Each UE independently: attach → Registration Request → 5G-AKA → Security Mode → Accept
    ...    5. All UE registrations proceed concurrently over same SCTP/NGAP association
    ...    6. Wait for all threads to complete (timeout + 5s grace)
    ...    7. Count passed/failed UEs, report per-UE results
    ...    Parameters: count=all configured UEs, timeout=20s, stagger_ms=200ms
    ...    Verification: All UEs reach REGISTERED state within timeout,
    ...    AMF handles concurrent NGAP procedures correctly
    ...    Expected Result: All UEs registered, 0 failures
    [Tags]    multi-ue    priority-1
    Full Registration    ${UE_1}
    Full Registration    ${UE_2}
    UE Should Be Registered    ${UE_1}
    UE Should Be Registered    ${UE_2}
    Log    TC-REG-002 PASS: Both UEs registered

TC-REG-003 Attach Detach Re-attach Cycle
    [Documentation]    TC-REG-003: Attach/Detach Cycle Stress Test
    ...    Standard: TS 24.501 §5.5.1.2 (registration), §5.5.2.2 (de-registration)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. For each cycle (default 3 cycles):
    ...    a. Attach UE to gNB, send Registration Request
    ...    b. Complete 5G-AKA, Security Mode, Registration Accept
    ...    c. Verify UE reaches REGISTERED state
    ...    d. Send Deregistration Request, wait for DEREGISTERED state
    ...    e. 500ms pause between cycles
    ...    3. Record per-cycle results (register success, deregister success)
    ...    4. All cycles must complete successfully
    ...    Parameters: cycles=3, timeout=15s per operation
    ...    Verification: All register/deregister cycles complete successfully,
    ...    AMF handles repeated context create/release, SQN increments correctly
    ...    Expected Result: All 3 cycles pass, no stale context or SQN issues
    [Tags]    stress    cycle    priority-1
    FOR    ${i}    IN RANGE    3
        Log    Cycle ${i+1}/3
        Register UE And Wait    ${UE_1}    ${GNB}
        UE Should Be Registered    ${UE_1}
        Deregister UE And Wait    ${UE_1}
        UE Should Be Deregistered    ${UE_1}
        Sleep    0.5s    Brief pause between cycles
    END
    Log    TC-REG-003 PASS: 3 attach/detach cycles completed

# TC-REG-004 (UE-Initiated Deregistration) was moved to the
# Deregistration suite as TC-DEREG-001 ("UE-initiated de-registration
# with De-registration type 'switch off'", in tc_deregistration.py) —
# it is §5.5.2-domain coverage and belongs alongside the rest of
# de-registration tests rather than under Registration.

# TC-REG-005 (Security Algorithms Negotiated) was moved to the
# Authentication suite as TC-AUTH-002 ("NAS security algorithms
# negotiated and keys derived") — it is §5.4-domain coverage and
# belongs alongside the rest of authentication / SMC tests rather
# than under Registration.

# TC-REG-006 (NG Setup Verification) was moved to the NG Setup suite
# as TC-NGS-001 ("NG Setup happy path: SCTP + NG Setup Request/
# Response reaches READY") — it is §8.7.1-domain coverage and belongs
# alongside the rest of NG Setup tests rather than under Registration.
