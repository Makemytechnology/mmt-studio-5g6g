# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Network Slicing Test Suite
...              TS 23.501 section 5.15 (Network Slicing)
...              TS 24.501 section 5.5.1 (Registration with NSSAI)
...              S-NSSAI = SST (Slice/Service Type) + SD (Slice Differentiator)
...              SST=1: eMBB, SST=2: URLLC, SST=3: MIoT
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        slicing    nssai    s-nssai

*** Test Cases ***
# ===============================================================
# Single Slice Registration + PDU
# ===============================================================
TC-SLC-001 eMBB Slice Registration And PDU Session
    [Documentation]    TC-SLC-001: Register UE on eMBB slice (SST=1) + PDU session
    ...    Standard: TS 23.501 section 5.15.2, TS 24.501 section 5.5.1.2
    ...    Procedure:
    ...    1. gNB NG Setup with SST=1/SD=0x010203
    ...    2. UE Registration Request with Requested NSSAI = SST=1/SD=0x010203
    ...    3. AMF returns Allowed NSSAI in Registration Accept
    ...    4. PDU Session Establishment on SST=1 (DNN=internet)
    ...    5. Verify traffic on eMBB slice
    ...    Parameters: SST=1, SD=0x010203, DNN=internet
    ...    Expected Result: Registration + PDU + traffic on eMBB slice
    [Tags]    embb    sst-1    priority-1
    Log    TC-SLC-001: eMBB slice

TC-SLC-002 URLLC Slice Registration And PDU Session
    [Documentation]    TC-SLC-002: Register UE on URLLC slice (SST=2)
    ...    Standard: TS 23.501 section 5.15.2 (SST=2 URLLC characteristics)
    ...    Procedure:
    ...    1. gNB NG Setup with SST=2 slice support
    ...    2. UE Registration with Requested NSSAI = SST=2
    ...    3. PDU Session Establishment on SST=2
    ...    4. Verify low-latency traffic characteristics
    ...    Parameters: SST=2, DNN=internet
    ...    Expected Result: URLLC slice active
    [Tags]    urllc    sst-2    priority-2
    Log    TC-SLC-002: URLLC slice

TC-SLC-003 MIoT Slice Registration And PDU Session
    [Documentation]    TC-SLC-003: Register UE on MIoT slice (SST=3)
    ...    Standard: TS 23.501 section 5.15.2 (SST=3 MIoT)
    ...    Parameters: SST=3, DNN=internet
    ...    Expected Result: MIoT slice active
    [Tags]    miot    sst-3    priority-3
    Log    TC-SLC-003: MIoT slice

# ===============================================================
# Multi-Slice: Multiple PDU Sessions On Different Slices
# ===============================================================
TC-SLC-004 Dual Slice PDU Sessions
    [Documentation]    TC-SLC-004: UE with PDU sessions on two different slices
    ...    Standard: TS 23.501 section 5.15.4 (UE associated to multiple slices)
    ...    Procedure:
    ...    1. gNB NG Setup supporting SST=1 + SST=2
    ...    2. UE Registration with Requested NSSAI = [SST=1, SST=2]
    ...    3. PDU Session 1 on SST=1/SD=0x010203 (DNN=internet, eMBB)
    ...    4. PDU Session 2 on SST=2 (DNN=internet, URLLC)
    ...    5. Verify both sessions active with different slice IDs
    ...    Expected Result: Both PDU sessions on different slices
    [Tags]    multi-slice    dual    sst-1    sst-2    priority-1
    Log    TC-SLC-004: Dual slice PDU

TC-SLC-005 Dual Slice Simultaneous Traffic
    [Documentation]    TC-SLC-005: Simultaneous traffic on two different slices
    ...    Standard: TS 23.501 section 5.15.4
    ...    Procedure:
    ...    1. Establish dual-slice PDU sessions (SST=1 + SST=2)
    ...    2. Simultaneous UDP UL+DL traffic on both sessions
    ...    3. Measure per-slice throughput, jitter, loss
    ...    4. Verify QoS isolation — URLLC should have lower jitter
    ...    Parameters: SST=1 at 5Mbps, SST=2 at 1Mbps, duration=30s
    ...    Expected Result: Both slices carry traffic independently
    [Tags]    multi-slice    traffic    qos-isolation    priority-1
    Log    TC-SLC-005: Dual slice traffic

TC-SLC-006 Triple Slice PDU Sessions
    [Documentation]    TC-SLC-006: UE with 3 slices (eMBB + URLLC + IMS)
    ...    Standard: TS 23.501 section 5.15.4
    ...    Procedure:
    ...    1. gNB supports SST=1, SST=2, SST=1/SD=IMS
    ...    2. UE requests all 3 slices
    ...    3. PDU Session 1: SST=1 DNN=internet (data)
    ...    4. PDU Session 2: SST=2 DNN=internet (URLLC)
    ...    5. PDU Session 3: SST=1 DNN=ims (voice)
    ...    6. Verify all 3 active
    ...    Expected Result: 3 PDU sessions on 3 different slices
    [Tags]    multi-slice    triple    priority-2
    Log    TC-SLC-006: Triple slice

# ===============================================================
# Slice Rejection / Error Handling
# ===============================================================
TC-SLC-007 Unsupported Slice Rejection
    [Documentation]    TC-SLC-007: UE requests unsupported slice — AMF rejects
    ...    Standard: TS 24.501 section 5.5.1.2.4 (NSSAI not supported)
    ...    Procedure:
    ...    1. gNB supports SST=1 only
    ...    2. UE requests SST=99 (unsupported)
    ...    3. AMF rejects slice in Allowed NSSAI or rejects registration
    ...    Expected Result: AMF rejects unsupported slice gracefully
    [Tags]    rejection    unsupported    negative    priority-2
    Log    TC-SLC-007: Unsupported slice rejection

TC-SLC-008 Partial Slice Acceptance
    [Documentation]    TC-SLC-008: UE requests 2 slices, AMF accepts only 1
    ...    Standard: TS 24.501 section 5.5.1.2.4
    ...    Procedure:
    ...    1. gNB supports SST=1
    ...    2. UE requests [SST=1, SST=99]
    ...    3. AMF returns Allowed NSSAI = [SST=1] only
    ...    4. PDU Session on SST=1 succeeds
    ...    Expected Result: Partial acceptance, allowed slice works
    [Tags]    partial    acceptance    priority-2
    Log    TC-SLC-008: Partial slice acceptance

# ===============================================================
# Multi-UE Slicing
# ===============================================================
TC-SLC-009 Multi-UE Same Slice
    [Documentation]    TC-SLC-009: 8 UEs on same eMBB slice with traffic
    ...    Standard: TS 23.501 section 5.15.5 (slice capacity)
    ...    Procedure:
    ...    1. Register 8 UEs on SST=1
    ...    2. PDU sessions for all
    ...    3. Simultaneous UL+DL traffic per UE
    ...    Parameters: 8 UEs, SST=1, 1Mbps each
    ...    Expected Result: All 8 UEs share slice capacity
    [Tags]    multi-ue    same-slice    8-ue    priority-1
    Log    TC-SLC-009: Multi-UE same slice

TC-SLC-010 Multi-UE Different Slices
    [Documentation]    TC-SLC-010: UEs distributed across different slices
    ...    Standard: TS 23.501 section 5.15.5
    ...    Procedure:
    ...    1. Register 4 UEs on SST=1 and 4 UEs on SST=2
    ...    2. PDU sessions on respective slices
    ...    3. Simultaneous traffic — verify slice isolation
    ...    Parameters: 4 UEs on SST=1, 4 UEs on SST=2, 1Mbps each
    ...    Expected Result: Slice isolation maintained under load
    [Tags]    multi-ue    different-slices    isolation    priority-2
    Log    TC-SLC-010: Multi-UE different slices
