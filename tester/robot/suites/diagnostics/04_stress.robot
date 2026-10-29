# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Stress and Batch Test Suite
...              Tests: Multi-UE registration, PDU sessions, traffic at scale
...              Covers: TS 23.501 §5.2 (AMF capacity), TS 38.413 (NGAP scaling)
...              Uses UE_1 through UE_128 from common.resource
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        stress    batch    multi-ue

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# Single UE Stress
# ═══════════════════════════════════════════════════════════════
TC-STR-001 Rapid Registration Deregistration 10 Cycles
    [Documentation]    TC-STR-001: 10 rapid attach/detach cycles on single UE
    ...    Standard: TS 23.502 §4.2.2 (Registration); §4.2.3 (Deregistration);
    ...    TS 23.501 §5.2 (AMF capacity / state churn).
    ...    Procedure:
    ...    1. Loop 10 times: Register UE And Wait ${UE_1} ${GNB} timeout=10
    ...       → Deregister UE And Wait ${UE_1} timeout=10.
    ...    Parameters: UE=${UE_1}; cycles=10; per-step timeout 10 s.
    ...    Verification: Each register completes (AMF allocates 5G-GUTI;
    ...    PDU-state idle); each deregister returns the UE to CM-IDLE.
    ...    No context leak — AMF UE count returns to baseline at the end.
    ...    Expected Result: 10 attach/detach cycles complete without
    ...    timeout, error, or residual UE context.
    [Tags]    rapid    single-ue    priority-1
    FOR    ${i}    IN RANGE    10
        Register UE And Wait    ${UE_1}    ${GNB}    timeout=10
        Deregister UE And Wait    ${UE_1}    timeout=10
    END
    Log    TC-STR-001 PASS: 10 rapid cycles completed

# ═══════════════════════════════════════════════════════════════
# Multi-UE Registration (scaling)
# ═══════════════════════════════════════════════════════════════
TC-STR-002 Register 4 UEs Sequential
    [Documentation]    TC-STR-002: Register 4 UEs sequential
    ...    Standard: TS 23.502 §4.2.2 (Registration); TS 23.501 §5.2
    ...    (AMF capacity scaling).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_4}: Full Registration ${imsi}.
    ...    Parameters: 4 distinct IMSIs from the seed pool.
    ...    Verification: Every UE reaches CM-CONNECTED + 5GMM-REGISTERED;
    ...    AMF holds 4 active contexts; no NGAP / NAS errors logged.
    ...    Expected Result: All 4 UEs register sequentially without
    ...    error or context collision.
    [Tags]    registration    4-ue    priority-1
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}
        Full Registration    ${imsi}
        Log    Registered: ${imsi}
    END
    Log    TC-STR-002 PASS: 4 UEs registered

TC-STR-003 Register 8 UEs Sequential
    [Documentation]    TC-STR-003: Register 8 UEs sequential
    ...    Standard: TS 23.502 §4.2.2 (Registration); TS 23.501 §5.2
    ...    (AMF capacity scaling).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_8}: Full Registration ${imsi}.
    ...    Parameters: 8 distinct IMSIs from the seed pool.
    ...    Verification: All 8 UEs become 5GMM-REGISTERED; AMF context
    ...    count is 8 at the end; no per-IMSI registration failure.
    ...    Expected Result: Sequential registration of 8 UEs completes
    ...    without back-pressure or NAS rejection.
    [Tags]    registration    8-ue    priority-1
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}    ${UE_5}    ${UE_6}    ${UE_7}    ${UE_8}
        Full Registration    ${imsi}
        Log    Registered: ${imsi}
    END
    Log    TC-STR-003 PASS: 8 UEs registered

TC-STR-004 Register 16 UEs Sequential
    [Documentation]    TC-STR-004: Register 16 UEs sequential
    ...    Standard: TS 23.502 §4.2.2 (Registration); TS 23.501 §5.2
    ...    (AMF capacity).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_16}: Full Registration ${imsi}.
    ...    Parameters: 16 distinct IMSIs.
    ...    Verification: 16 active 5GMM-REGISTERED contexts on the AMF;
    ...    every per-IMSI Full Registration returns success.
    ...    Expected Result: 16-UE sequential registration completes
    ...    without timeout or context collision.
    [Tags]    registration    16-ue    priority-2
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}    ${UE_5}    ${UE_6}    ${UE_7}    ${UE_8}    ${UE_9}    ${UE_10}    ${UE_11}    ${UE_12}    ${UE_13}    ${UE_14}    ${UE_15}    ${UE_16}
        Full Registration    ${imsi}
    END
    Log    TC-STR-004 PASS: 16 UEs registered

TC-STR-005 Register 32 UEs Sequential
    [Documentation]    TC-STR-005: Register 32 UEs sequential
    ...    Standard: TS 23.502 §4.2.2 (Registration); TS 23.501 §5.2
    ...    (AMF capacity); TS 38.413 (NGAP signalling scale).
    ...    Procedure:
    ...    1. For i in 1..32 build IMSI 001011234560<NNN> and run
    ...    Register UE And Wait ${imsi} ${GNB}.
    ...    Parameters: 32 generated IMSIs; default registration timeout.
    ...    Verification: All 32 registrations succeed; AMF accepts the
    ...    parallel context creation; no NGAP transaction collisions.
    ...    Expected Result: 32 UEs registered sequentially without
    ...    failure.
    [Tags]    registration    32-ue    priority-2
    FOR    ${i}    IN RANGE    1    33
        ${imsi}=    Set Variable    001011234560${{'%03d' % $i}}
        Register UE And Wait    ${imsi}    ${GNB}
    END
    Log    TC-STR-005 PASS: 32 UEs registered

# ═══════════════════════════════════════════════════════════════
# Multi-UE PDU Sessions
# ═══════════════════════════════════════════════════════════════
TC-STR-006 PDU Sessions 4 UEs
    [Documentation]    TC-STR-006: Register + PDU session for 4 UEs
    ...    Standard: TS 23.502 §4.3.2 (PDU Session Establishment);
    ...    TS 23.501 §5.7 (QoS).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_4}: Full Registration And PDU
    ...    Session ${imsi} → capture UE IP.
    ...    Parameters: 4 IMSIs; default DNN (internet); 5QI=9.
    ...    Verification: All 4 UEs have a registered PDU session; UPF
    ...    has 4 corresponding PFCP sessions; per-UE UE-IP is unique.
    ...    Expected Result: 4 UEs each carry one active PDU session.
    [Tags]    pdu-session    4-ue    priority-1
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}
        ${ip}=    Full Registration And PDU Session    ${imsi}
        Log    ${imsi} → IP: ${ip}
    END
    Log    TC-STR-006 PASS: 4 UEs with PDU sessions

TC-STR-007 PDU Sessions 8 UEs
    [Documentation]    TC-STR-007: Register + PDU session for 8 UEs
    ...    Standard: TS 23.502 §4.3.2 (PDU Session Establishment);
    ...    TS 23.501 §5.7 (QoS framework).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_8}: Full Registration And PDU
    ...    Session ${imsi} → capture UE IP.
    ...    Parameters: 8 IMSIs; default DNN; 5QI=9.
    ...    Verification: 8 concurrent PDU sessions; 8 PFCP sessions on
    ...    the UPF; unique per-UE IPs.
    ...    Expected Result: 8 UEs with active PDU sessions in parallel.
    [Tags]    pdu-session    8-ue    priority-1
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}    ${UE_5}    ${UE_6}    ${UE_7}    ${UE_8}
        ${ip}=    Full Registration And PDU Session    ${imsi}
        Log    ${imsi} → IP: ${ip}
    END
    Log    TC-STR-007 PASS: 8 UEs with PDU sessions

TC-STR-008 PDU Sessions 16 UEs
    [Documentation]    TC-STR-008: Register + PDU session for 16 UEs
    ...    Standard: TS 23.502 §4.3.2 (PDU Session Establishment);
    ...    TS 23.501 §5.7 (QoS framework); TS 29.244 (PFCP).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_16}: Full Registration And PDU
    ...    Session ${imsi}.
    ...    Parameters: 16 IMSIs; default DNN; 5QI=9.
    ...    Verification: 16 concurrent PDU sessions; 16 PFCP sessions;
    ...    UPF tunnel table holds 16 entries; UE-IP allocation has no
    ...    duplicates.
    ...    Expected Result: 16 active PDU sessions; UPF / SMF state
    ...    consistent.
    [Tags]    pdu-session    16-ue    priority-2
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}    ${UE_5}    ${UE_6}    ${UE_7}    ${UE_8}    ${UE_9}    ${UE_10}    ${UE_11}    ${UE_12}    ${UE_13}    ${UE_14}    ${UE_15}    ${UE_16}
        ${ip}=    Full Registration And PDU Session    ${imsi}
        Log    ${imsi} → ${ip}
    END
    Log    TC-STR-008 PASS: 16 UEs with PDU sessions

# ═══════════════════════════════════════════════════════════════
# Multi-UE Attach/Detach Cycles
# ═══════════════════════════════════════════════════════════════
TC-STR-009 Attach Detach 8 UEs 3 Cycles
    [Documentation]    TC-STR-009: 8 UEs each do 3 attach/detach cycles
    ...    Standard: TS 23.502 §4.2.2 / §4.2.3 (Reg / Dereg);
    ...    TS 23.501 §5.2 (AMF state churn under multi-UE load).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_8}, loop 3 times: Register UE
    ...    And Wait → Deregister UE And Wait.
    ...    Parameters: 8 UEs; 3 cycles each; per-step timeout 10 s.
    ...    Verification: All 24 attach/detach round-trips succeed; AMF
    ...    UE-context count returns to baseline (zero net UE context)
    ...    at the end of each UE's loop and at the suite end.
    ...    Expected Result: 8 × 3 = 24 cycles execute without timeout
    ...    or context leak.
    [Tags]    attach-detach    8-ue    cycles    priority-2
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}    ${UE_5}    ${UE_6}    ${UE_7}    ${UE_8}
        FOR    ${cycle}    IN RANGE    3
            Register UE And Wait    ${imsi}    ${GNB}    timeout=10
            Deregister UE And Wait    ${imsi}    timeout=10
        END
        Log    ${imsi}: 3 cycles complete
    END
    Log    TC-STR-009 PASS: 8 UEs × 3 cycles

# ═══════════════════════════════════════════════════════════════
# Multi-UE Traffic
# ═══════════════════════════════════════════════════════════════
TC-STR-010 Traffic 4 UEs Sequential
    [Documentation]    TC-STR-010: 4 UEs with PDU sessions
    ...    Standard: TS 23.502 §4.3.2 (PDU Session); TS 29.281 (GTP-U).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_4}: Full Registration And PDU
    ...    Session ${imsi}; record UE-IP.
    ...    Parameters: 4 IMSIs; default DNN.
    ...    Verification: All 4 PDU sessions establish; 4 distinct UE-IPs
    ...    allocated; UPF tunnel table has 4 entries.
    ...    Expected Result: 4 UEs with active PDU sessions ready for
    ...    downstream traffic.
    [Tags]    traffic    4-ue    priority-1
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}
        ${ip}=    Full Registration And PDU Session    ${imsi}
        Log    ${imsi}: PDU IP=${ip}
    END
    Log    TC-STR-010 PASS: 4 UEs with traffic

TC-STR-011 Traffic 8 UEs Sequential
    [Documentation]    TC-STR-011: 8 UEs with PDU sessions
    ...    Standard: TS 23.502 §4.3.2 (PDU Session); TS 29.281 (GTP-U).
    ...    Procedure:
    ...    1. For each IMSI in {UE_1..UE_8}: Full Registration And PDU
    ...    Session ${imsi}; record UE-IP.
    ...    Parameters: 8 IMSIs; default DNN.
    ...    Verification: All 8 PDU sessions up; 8 unique UE-IPs; 8
    ...    PFCP sessions on the UPF.
    ...    Expected Result: 8 UEs ready for parallel data traffic.
    [Tags]    traffic    8-ue    priority-2
    FOR    ${imsi}    IN    ${UE_1}    ${UE_2}    ${UE_3}    ${UE_4}    ${UE_5}    ${UE_6}    ${UE_7}    ${UE_8}
        ${ip}=    Full Registration And PDU Session    ${imsi}
        Log    ${imsi}: PDU IP=${ip}
    END
    Log    TC-STR-011 PASS: 8 UEs with traffic

# ═══════════════════════════════════════════════════════════════
# Large Scale (32/64/128 UEs)
# ═══════════════════════════════════════════════════════════════
TC-STR-012 Register 64 UEs
    [Documentation]    TC-STR-012: Register 64 UEs
    ...    Standard: TS 23.502 §4.2.2 (Registration); TS 23.501 §5.2
    ...    (AMF capacity at scale); TS 38.413 (NGAP transaction load).
    ...    Procedure:
    ...    1. For i in 1..64 build IMSI 001011234560<NNN> and call
    ...    Register UE And Wait ${imsi} ${GNB}.
    ...    Parameters: 64 generated IMSIs.
    ...    Verification: 64 active AMF UE contexts; no registration
    ...    rejection / timeout; NGAP signalling table holds 64 paired
    ...    UE-AMF-NGAP-IDs.
    ...    Expected Result: 64-UE sequential registration completes
    ...    under the AMF's scale target.
    [Tags]    registration    64-ue    large-scale    priority-3
    FOR    ${i}    IN RANGE    1    65
        ${imsi}=    Set Variable    001011234560${{'%03d' % $i}}
        Register UE And Wait    ${imsi}    ${GNB}
    END
    Log    TC-STR-012 PASS: 64 UEs registered

TC-STR-013 Register 128 UEs
    [Documentation]    TC-STR-013: Register all 128 UEs
    ...    Standard: TS 23.502 §4.2.2 (Registration); TS 23.501 §5.2
    ...    (AMF capacity at full pool); TS 38.413 (NGAP scaling).
    ...    Procedure:
    ...    1. For i in 1..128 build IMSI 001011234560<NNN> and call
    ...    Register UE And Wait ${imsi} ${GNB}.
    ...    Parameters: 128 generated IMSIs (full bundled pool).
    ...    Verification: 128 concurrent AMF UE contexts; no per-IMSI
    ...    registration failure; AMF UE-count metric reaches 128.
    ...    Expected Result: Full 128-UE pool registers without
    ...    rejection or AMF saturation.
    [Tags]    registration    128-ue    large-scale    priority-3
    FOR    ${i}    IN RANGE    1    129
        ${imsi}=    Set Variable    001011234560${{'%03d' % $i}}
        Register UE And Wait    ${imsi}    ${GNB}
    END
    Log    TC-STR-013 PASS: 128 UEs registered

TC-STR-014 PDU Sessions 32 UEs
    [Documentation]    TC-STR-014: Register + PDU for 32 UEs
    ...    Standard: TS 23.502 §4.3.2 (PDU Session); TS 23.501 §5.7
    ...    (QoS); TS 29.244 (PFCP at scale).
    ...    Procedure:
    ...    1. For i in 1..32 build IMSI; run Full Registration ${imsi};
    ...    then Full PDU Session ${imsi}; record UE-IP.
    ...    Parameters: 32 IMSIs; default DNN.
    ...    Verification: 32 PDU sessions established; 32 PFCP sessions
    ...    on UPF; 32 unique UE-IPs; UPF tunnel table has 32 entries.
    ...    Expected Result: 32-UE PDU bring-up scales without UPF
    ...    rejection or IP-pool collision.
    [Tags]    pdu-session    32-ue    large-scale    priority-3
    FOR    ${i}    IN RANGE    1    33
        ${imsi}=    Set Variable    001011234560${{'%03d' % $i}}
        Full Registration    ${imsi}
        ${ip}=    Full PDU Session    ${imsi}
        Log    ${imsi} → ${ip}
    END
    Log    TC-STR-014 PASS: 32 UEs with PDU sessions

TC-STR-015 PDU Sessions 64 UEs
    [Documentation]    TC-STR-015: Register + PDU for 64 UEs
    ...    Standard: TS 23.502 §4.3.2 (PDU Session); TS 23.501 §5.7;
    ...    TS 29.244 (PFCP scaling).
    ...    Procedure:
    ...    1. For i in 1..64 build IMSI; Full Registration ${imsi};
    ...    Full PDU Session ${imsi}; record UE-IP.
    ...    Parameters: 64 IMSIs; default DNN.
    ...    Verification: 64 active PDU sessions; 64 PFCP sessions on
    ...    UPF; UPF tunnel table holds 64 entries; IP-pool returns 64
    ...    distinct UE-IPs.
    ...    Expected Result: 64-UE bring-up succeeds without IP-pool
    ...    exhaustion or UPF stall.
    [Tags]    pdu-session    64-ue    large-scale    priority-3
    FOR    ${i}    IN RANGE    1    65
        ${imsi}=    Set Variable    001011234560${{'%03d' % $i}}
        Full Registration    ${imsi}
        ${ip}=    Full PDU Session    ${imsi}
    END
    Log    TC-STR-015 PASS: 64 UEs with PDU sessions

TC-STR-016 Register Deregister 32 UEs Churn
    [Documentation]    TC-STR-016: 32 UEs register then deregister — churn test
    ...    Standard: TS 23.502 §4.2.2 / §4.2.3 (Reg / Dereg);
    ...    TS 23.501 §5.2 (AMF state churn at scale).
    ...    Procedure:
    ...    1. For i in 1..32: Register UE And Wait ${imsi} ${GNB}.
    ...    2. For i in 1..32: Deregister UE And Wait ${imsi}.
    ...    Parameters: 32 generated IMSIs.
    ...    Verification: 32 registrations succeed; 32 deregistrations
    ...    return the AMF context count to baseline (no leak); NGAP
    ...    UE-Context-Release succeeds for each UE.
    ...    Expected Result: Full 32-UE churn cycle completes without
    ...    leaking AMF UE contexts.
    [Tags]    churn    32-ue    large-scale    priority-3
    # Register all 32
    FOR    ${i}    IN RANGE    1    33
        ${imsi}=    Set Variable    001011234560${{'%03d' % $i}}
        Register UE And Wait    ${imsi}    ${GNB}
    END
    Log    32 UEs registered
    # Deregister all 32
    FOR    ${i}    IN RANGE    1    33
        ${imsi}=    Set Variable    001011234560${{'%03d' % $i}}
        Deregister UE And Wait    ${imsi}
    END
    Log    TC-STR-016 PASS: 32 UEs registered then deregistered
