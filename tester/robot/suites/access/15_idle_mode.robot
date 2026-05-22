# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Idle Mode / Service Request / Paging Test Suite
...              Tests UE transitions between RRC Connected, Inactive, and Idle states
...              NAS Service Request (TS 24.501 section 8.2.15)
...              NGAP Paging (TS 38.413 section 8.5)
...              UE-triggered (UL data) and network-triggered (DL paging) transitions
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        idle-mode    service-request    paging    rrc-state

*** Test Cases ***
# ===============================================================
# UE-Initiated Service Request (UL data trigger)
# ===============================================================
TC-IDL-001 Service Request After RRC Inactive
    [Documentation]    TC-IDL-001: UE sends Service Request after going RRC Inactive
    ...    Standard: TS 24.501 section 8.2.15, TS 38.413 section 8.7.4
    ...    Procedure:
    ...    1. Register UE, establish PDU session (RRC Connected)
    ...    2. gNB reports RRC Inactive transition to AMF
    ...    3. UE sends NAS Service Request (type=data) via InitialUEMessage
    ...    4. AMF responds (InitialContextSetup or DL NAS Transport)
    ...    5. UE back to RRC Connected
    ...    6. Verify traffic works after service request
    ...    Expected Result: UE resumes data after Service Request
    [Tags]    service-request    data    ul-trigger    priority-1
    Log    TC-IDL-001: Service Request after inactive

TC-IDL-002 Service Request Then UL Traffic
    [Documentation]    TC-IDL-002: Service Request followed by sustained UL traffic
    ...    Procedure:
    ...    1. Register + PDU + go RRC Inactive
    ...    2. Service Request (type=data)
    ...    3. Run 30s UDP UL traffic through restored session
    ...    4. Measure throughput, jitter, loss
    ...    Expected Result: Full UL throughput restored after Service Request
    [Tags]    service-request    ul-traffic    priority-1
    Log    TC-IDL-002: Service Request + UL traffic

TC-IDL-003 Service Request Then Bidirectional Traffic
    [Documentation]    TC-IDL-003: Service Request followed by simultaneous UL+DL traffic
    ...    Procedure:
    ...    1. Register + PDU + go RRC Inactive
    ...    2. Service Request (type=data)
    ...    3. Run 30s simultaneous UL+DL UDP traffic
    ...    Expected Result: Full bidirectional throughput after Service Request
    [Tags]    service-request    bidir    priority-1
    Log    TC-IDL-003: Service Request + bidir traffic

# ===============================================================
# Network-Initiated Paging (DL data trigger)
# ===============================================================
TC-IDL-004 Paging After RRC Inactive
    [Documentation]    TC-IDL-004: AMF pages UE in RRC Inactive state
    ...    Standard: TS 38.413 section 8.5.2, TS 24.501 section 8.2.15
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. gNB reports RRC Inactive
    ...    3. Trigger DL data on core (AMF initiates paging)
    ...    4. gNB receives NGAP Paging message
    ...    5. UE sends Service Request (type=mobile-terminated)
    ...    6. AMF delivers DL data
    ...    Expected Result: Paging triggers UE wake-up, DL data delivered
    [Tags]    paging    dl-trigger    network-initiated    priority-1
    Log    TC-IDL-004: Paging after inactive

TC-IDL-005 Paging Then DL Traffic
    [Documentation]    TC-IDL-005: Paging followed by sustained DL traffic
    ...    Procedure:
    ...    1. Register + PDU + go RRC Inactive
    ...    2. Core initiates DL traffic (triggers paging)
    ...    3. gNB receives Paging, UE sends Service Request
    ...    4. Run 30s DL traffic through restored session
    ...    Expected Result: Full DL throughput after paging + Service Request
    [Tags]    paging    dl-traffic    priority-2
    Log    TC-IDL-005: Paging + DL traffic

# ===============================================================
# Multiple Transitions
# ===============================================================
TC-IDL-006 Connected Inactive Connected Cycles
    [Documentation]    TC-IDL-006: Multiple RRC Connected ↔ Inactive cycles
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. Cycle 3 times: go inactive → Service Request → verify traffic
    ...    3. Measure throughput after each wake-up
    ...    Expected Result: All 3 cycles succeed, throughput stable
    [Tags]    cycles    stability    priority-2
    Log    TC-IDL-006: Connected-Inactive cycles

TC-IDL-007 Service Request Signalling Type
    [Documentation]    TC-IDL-007: Service Request with type=signalling (not data)
    ...    Standard: TS 24.501 section 8.2.15 (ServiceType=0)
    ...    Procedure:
    ...    1. Register UE, go RRC Inactive
    ...    2. Service Request (type=signalling)
    ...    3. Verify UE context restored without user plane
    ...    Expected Result: Signalling-only Service Request accepted
    [Tags]    service-request    signalling    priority-2
    Log    TC-IDL-007: Signalling Service Request

TC-IDL-008 Multi-UE Paging
    [Documentation]    TC-IDL-008: Multiple UEs paged simultaneously
    ...    Procedure:
    ...    1. Register 4 UEs, establish PDU sessions
    ...    2. All go RRC Inactive
    ...    3. Trigger DL data for all 4 (paging storm)
    ...    4. All UEs send Service Request
    ...    5. Verify all resume traffic
    ...    Expected Result: All 4 UEs paged and resume service
    [Tags]    multi-ue    paging    4-ue    priority-2
    Log    TC-IDL-008: Multi-UE paging
