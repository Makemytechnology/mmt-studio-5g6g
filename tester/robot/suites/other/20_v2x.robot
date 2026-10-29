# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    V2X (Vehicle-to-Everything) Test Suite
...              TS 23.287 — Architecture enhancements for V2X services
...              TS 24.587 — V2X services in 5GS (Stage 3)
...              TS 29.486 — V2X Policy Control (Npcf_V2XPolicy)
...              Covers: V2X subscription, authorization, DNN=v2x PDU session,
...              V2X QoS (5QI 3), PQI table provisioning, PC5 authorization IE
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        v2x    vehicle    pc5

*** Test Cases ***
# ===============================================================
# V2X Subscription & Authorization
# TS 23.287 §5.5 — V2X subscription in UDM
# TS 23.287 §6.2.2 — PCF-based Service Authorization
# ===============================================================
TC-V2X-001 V2X Vehicle UE Registration With Authorization
    [Documentation]    TC-V2X-001: Register V2X-authorized Vehicle UE
    ...    Standard: TS 23.287 §5.5 (V2X subscription), §6.2.2 (service authorization)
    ...    Procedure:
    ...    1. Configure UE with v2x_authorized=1, v2x_ue_type=vehicle in subscription
    ...    2. gNB NG Setup (standard)
    ...    3. UE Registration Request with V2X capability
    ...    4. AMF loads V2X subscription from UDM
    ...    5. Registration Accept includes NR V2X Services Authorized IE (§9.11.3.75)
    ...    Verification:
    ...    - Registration Accept contains V2X authorization (vehicle bit set)
    ...    - AMF log shows V2X subscription loaded
    ...    Expected Result: UE registered with V2X vehicle authorization
    [Tags]    subscription    authorization    vehicle    priority-1
    Log    TC-V2X-001: V2X Vehicle UE Registration

TC-V2X-002 V2X Pedestrian UE Registration With Authorization
    [Documentation]    TC-V2X-002: Register V2X-authorized Pedestrian UE
    ...    Standard: TS 23.287 §5.1 (Authorization and Provisioning for V2X communications — pedestrian UEs are authorized via the same per-PLMN policy mechanism as vehicular UEs)
    ...    Procedure:
    ...    1. Configure UE with v2x_authorized=1, v2x_ue_type=pedestrian
    ...    2. UE Registration Request
    ...    3. Registration Accept includes V2X authorization (pedestrian bit)
    ...    Expected Result: UE registered with V2X pedestrian authorization
    [Tags]    subscription    authorization    pedestrian    priority-2
    Log    TC-V2X-002: V2X Pedestrian UE Registration

TC-V2X-003 Non-V2X UE Registration No V2X IE
    [Documentation]    TC-V2X-003: Register UE without V2X subscription
    ...    Standard: TS 23.287 §6.2.2 (authorization only for V2X-subscribed UEs)
    ...    Procedure:
    ...    1. UE with v2x_authorized=0 (default)
    ...    2. UE Registration Request
    ...    3. Registration Accept does NOT contain V2X authorization IE
    ...    Expected Result: Normal registration, no V2X IEs
    [Tags]    subscription    negative    priority-1
    Log    TC-V2X-003: Non-V2X UE (no V2X IE in Accept)

# ===============================================================
# V2X PDU Session Establishment (Uu Mode)
# TS 23.287 §5.2.2.1 — V2X communication via unicast (Uu)
# DNN = v2x, 5QI = 3 (V2X messages)
# ===============================================================
TC-V2X-010 V2X PDU Session Establishment DNN=v2x
    [Documentation]    TC-V2X-010: Establish PDU session on DNN=v2x
    ...    Standard: TS 23.287 §5.2.2.1 (V2X over Uu reference point)
    ...    Procedure:
    ...    1. Register V2X-authorized UE (TC-V2X-001 precondition)
    ...    2. UE sends PDU Session Establishment Request (DNN=v2x)
    ...    3. SMF allocates IP from v2x pool, creates UPF session
    ...    4. PDU Session Accept with 5QI=3 (V2X signaling)
    ...    5. gNB confirms PDUSessionResourceSetup
    ...    Verification:
    ...    - PDU session established on DNN=v2x
    ...    - Allocated IP from v2x pool
    ...    - QoS flow with 5QI=3
    ...    Expected Result: V2X PDU session active
    [Tags]    pdu-session    dnn-v2x    5qi-3    priority-1
    Log    TC-V2X-010: V2X PDU Session (DNN=v2x, 5QI=3)

TC-V2X-011 V2X PDU Session With Internet Dual DNN
    [Documentation]    TC-V2X-011: V2X UE with dual PDU sessions (internet + v2x)
    ...    Standard: TS 23.501 §5.6.1 (multiple PDU sessions per UE)
    ...    Procedure:
    ...    1. Register V2X-authorized UE
    ...    2. Establish PSI=1 on DNN=internet (default data, 5QI=9)
    ...    3. Establish PSI=2 on DNN=v2x (V2X signaling, 5QI=3)
    ...    4. Verify both sessions active with independent GTP-U tunnels
    ...    Expected Result: Dual PDU sessions (internet + v2x) simultaneously
    [Tags]    pdu-session    dual-dnn    priority-1
    Log    TC-V2X-011: Dual DNN (internet + v2x)

TC-V2X-012 V2X PDU Session Non-Authorized UE Rejected
    [Documentation]    TC-V2X-012: Non-V2X UE attempts DNN=v2x — expect rejection
    ...    Standard: TS 23.287 §6.2.2 (authorization required for V2X)
    ...    Procedure:
    ...    1. Register non-V2X UE (v2x_authorized=0)
    ...    2. UE sends PDU Session Establishment Request (DNN=v2x)
    ...    3. SMF rejects: UE not authorized for V2X DNN
    ...    Expected Result: PDU Session Establishment Reject (cause #33 or #29)
    [Tags]    pdu-session    negative    authorization    priority-2
    Log    TC-V2X-012: Non-V2X UE rejected on DNN=v2x

# ===============================================================
# V2X QoS & PQI
# TS 23.287 §5.4 — QoS handling for V2X
# TS 23.287 Table 5.4.4-1 — Standardized PQI mapping
# ===============================================================
TC-V2X-020 V2X QoS Flow 5QI 3 Signaling
    [Documentation]    TC-V2X-020: V2X signaling QoS flow (5QI=3, GBR)
    ...    Standard: TS 23.501 §5.7.4 (5QI 3 — V2X messages)
    ...    Procedure:
    ...    1. V2X UE with active DNN=v2x PDU session
    ...    2. Verify default QoS flow uses 5QI=3
    ...    3. Send V2X signaling traffic (UDP, small packets, low latency)
    ...    4. Measure latency against 5QI=3 PDB (50ms)
    ...    Expected Result: V2X signaling delivered within 5QI=3 budget
    [Tags]    qos    5qi-3    gbr    signaling    priority-1
    Log    TC-V2X-020: V2X QoS 5QI=3

TC-V2X-021 V2X Data QoS Flow 5QI 80
    [Documentation]    TC-V2X-021: V2X data QoS flow (5QI=80, NonGBR)
    ...    Standard: TS 23.501 §5.7.4 (5QI 80 — low latency eMBB)
    ...    Procedure:
    ...    1. V2X UE with active DNN=v2x PDU session
    ...    2. Activate dedicated bearer with 5QI=80 for V2X data
    ...    3. Send V2X data traffic (sensor data, higher throughput)
    ...    4. Verify QoS isolation from default bearer
    ...    Expected Result: Dedicated V2X data bearer active
    [Tags]    qos    5qi-80    nonGBR    data    priority-2
    Log    TC-V2X-021: V2X Data QoS 5QI=80

# ===============================================================
# V2X PQI Table (PC5 QoS)
# TS 23.287 Table 5.4.4-1 — Standardized PQI mapping
# ===============================================================
TC-V2X-030 PQI Table Provisioning All Entries
    [Documentation]    TC-V2X-030: Verify PQI table has all standardized entries
    ...    Standard: TS 23.287 Table 5.4.4-1 (page 35)
    ...    Procedure:
    ...    1. Query /api/v2x/service-types REST endpoint
    ...    2. Verify 10 standardized PQI entries present:
    ...       PQI 21-23 (GBR), PQI 55-59 (NonGBR), PQI 90-91 (DelCritGBR)
    ...    3. Verify QoS characteristics per entry (delay, error rate, burst)
    ...    Expected Result: All 10 PQIs with correct parameters
    [Tags]    pqi    config    api    priority-1
    Log    TC-V2X-030: PQI Table Verification

TC-V2X-031 PQI 90 Collision Avoidance QoS
    [Documentation]    TC-V2X-031: PQI 90 — Cooperative Collision Avoidance
    ...    Standard: TS 23.287 Table 5.4.4-1 (PQI 90)
    ...    Parameters: DelCritGBR, priority=3, PDB=10ms, PER=10^-4, burst=2000B
    ...    Procedure: Verify PQI 90 entry exists with correct characteristics
    ...    Expected Result: PQI 90 configured for collision avoidance
    [Tags]    pqi    collision-avoidance    delay-critical    priority-2
    Log    TC-V2X-031: PQI 90 Collision Avoidance

TC-V2X-032 PQI 91 Emergency Trajectory QoS
    [Documentation]    TC-V2X-032: PQI 91 — Emergency Trajectory Alignment
    ...    Standard: TS 23.287 Table 5.4.4-1 (PQI 91)
    ...    Parameters: DelCritGBR, priority=2, PDB=3ms, PER=10^-5, burst=2000B
    ...    Note: Highest priority V2X service — 3ms latency requirement
    ...    Expected Result: PQI 91 configured for emergency trajectory
    [Tags]    pqi    emergency    delay-critical    priority-2
    Log    TC-V2X-032: PQI 91 Emergency Trajectory

# ===============================================================
# V2X Policy (PCF)
# TS 29.486 — Npcf_V2XPolicy
# TS 23.287 §6.2.2 — Service Authorization via PCF
# ===============================================================
TC-V2X-040 V2X Policy Association Create On Registration
    [Documentation]    TC-V2X-040: V2X Policy Association created during registration
    ...    Standard: TS 29.486 §5.4 (VAE_ApplicationRequirement Service — V2X policy parameters)
    ...    Procedure:
    ...    1. Register V2X-authorized UE
    ...    2. AMF selects V2X-capable PCF (TS 23.287 §6.2.3)
    ...    3. PCF creates V2X Policy Association
    ...    4. V2X policy parameters provisioned
    ...    Verification: PCF log shows V2X Policy Association created
    ...    Expected Result: V2X policy active for UE
    [Tags]    pcf    policy    npcf-v2x    priority-2
    Log    TC-V2X-040: V2X Policy Association Create

TC-V2X-041 V2X Policy Deleted On Deregistration
    [Documentation]    TC-V2X-041: V2X Policy Association deleted on deregistration
    ...    Standard: TS 29.486 §5.4 (VAE_ApplicationRequirement Service — subscription DELETE)
    ...    Procedure:
    ...    1. V2X UE registered with active V2X policy
    ...    2. UE sends Deregistration Request
    ...    3. PCF deletes V2X Policy Association
    ...    Expected Result: V2X policy cleaned up
    [Tags]    pcf    policy    deregistration    priority-2
    Log    TC-V2X-041: V2X Policy Deleted on Deregistration

# ===============================================================
# V2X Configuration API
# REST API verification
# ===============================================================
TC-V2X-050 V2X Config API Read
    [Documentation]    TC-V2X-050: Read V2X configuration via REST API
    ...    Procedure:
    ...    1. GET /api/v2x/config
    ...    2. Verify v2x_enabled, pc5_nr_enabled, ue_pc5_ambr_kbps present
    ...    Expected Result: V2X config returned with defaults
    [Tags]    api    config    priority-1
    Log    TC-V2X-050: V2X Config API

TC-V2X-051 V2X Config API Update
    [Documentation]    TC-V2X-051: Update V2X configuration via REST API
    ...    Procedure:
    ...    1. PUT /api/v2x/config with {"key": "ue_pc5_ambr_kbps", "value": "100000"}
    ...    2. GET /api/v2x/config and verify updated value
    ...    Expected Result: Config updated and persisted
    [Tags]    api    config    update    priority-2
    Log    TC-V2X-051: V2X Config Update

TC-V2X-052 V2X Service Types API
    [Documentation]    TC-V2X-052: List V2X service types (PQI table) via REST API
    ...    Procedure:
    ...    1. GET /api/v2x/service-types
    ...    2. Verify 10 PQI entries returned
    ...    3. Verify each entry has pqi, service_name, resource_type, packet_delay_ms
    ...    Expected Result: Full PQI table accessible via API
    [Tags]    api    pqi    service-types    priority-1
    Log    TC-V2X-052: V2X Service Types API

TC-V2X-053 V2X Subscribers API
    [Documentation]    TC-V2X-053: List V2X-authorized subscribers via REST API
    ...    Procedure:
    ...    1. GET /api/v2x/subscribers
    ...    2. Verify V2X-authorized UEs returned with v2x_ue_type and pc5_ambr
    ...    Expected Result: V2X subscriber list accurate
    [Tags]    api    subscribers    priority-2
    Log    TC-V2X-053: V2X Subscribers API

# ===============================================================
# V2X CM-IDLE / Service Request
# Session preservation during idle mode
# ===============================================================
TC-V2X-060 V2X Session Preserved During CM-IDLE
    [Documentation]    TC-V2X-060: V2X PDU session survives CM-IDLE transition
    ...    Standard: TS 23.502 §4.2.3.2 (Service Request reactivation)
    ...    Procedure:
    ...    1. V2X UE with active DNN=v2x PDU session
    ...    2. gNB releases UE context (CM-IDLE)
    ...    3. UE sends Service Request (CM-CONNECTED)
    ...    4. DRB re-established for V2X PDU session
    ...    5. V2X traffic resumes
    ...    Expected Result: V2X session + IP preserved across CM-IDLE
    [Tags]    idle-mode    service-request    session-preservation    priority-1
    Log    TC-V2X-060: V2X Session Preserved in CM-IDLE

TC-V2X-061 V2X Dual DNN Preserved During CM-IDLE
    [Documentation]    TC-V2X-061: Both internet + v2x sessions preserved during CM-IDLE
    ...    Procedure:
    ...    1. V2X UE with PSI=1 (internet) + PSI=2 (v2x)
    ...    2. gNB releases UE context
    ...    3. UE Service Request re-establishes both DRBs
    ...    4. Both sessions resume
    ...    Expected Result: Dual DNN preserved across idle
    [Tags]    idle-mode    dual-dnn    priority-2
    Log    TC-V2X-061: Dual DNN Preserved in CM-IDLE
