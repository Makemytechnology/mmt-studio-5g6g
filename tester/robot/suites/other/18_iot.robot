# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    5G IoT Test Suite — NB-IoT, RedCap, NIDD/SCEF, Ambient IoT
...              Covers: TS 24.301 (PSM, eDRX, CP CIoT), TS 38.306 (RedCap),
...              TS 23.682 (SCEF/NIDD), TS 22.369 (Ambient IoT)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        iot    nbiot    redcap    ambient

*** Test Cases ***
TC-IOT-001 NB-IoT PSM Configuration
    [Documentation]    TC-IOT-001: Power Saving Mode timer configuration
    ...    Standard: TS 24.301 §5.3.11 — T3324 (active), T3412-ext (TAU period)
    ...    Expected: PSM configured, UE enters sleeping state after T3324
    [Tags]    psm    nbiot    priority-1
    Run Test    iot_psm_config

TC-IOT-002 NB-IoT PSM State Transitions
    [Documentation]    TC-IOT-002: PSM active → sleeping → wake cycle
    ...    Standard: TS 24.301 §5.3.11 — PSM state machine
    ...    Expected: UE transitions through active/sleeping/active
    [Tags]    psm    state-machine    priority-1
    Run Test    iot_psm_states

TC-IOT-003 NB-IoT CP CIoT Uplink Data
    [Documentation]    TC-IOT-003: Control Plane CIoT uplink data transport
    ...    Standard: TS 23.401 §4.7.7 — CP optimization, TS 24.301 §8.3.25 (ESM DATA TRANSPORT)
    ...    Expected: UL data sent via NAS ESM Data Transport
    [Tags]    cp-ciot    uplink    priority-1
    Run Test    iot_cp_uplink

TC-IOT-004 NB-IoT CP CIoT Downlink Buffering
    [Documentation]    TC-IOT-004: DL data buffered during PSM, delivered on wake
    ...    Standard: TS 23.401 §4.7.7 — DL buffering while UE asleep
    ...    Expected: DL data queued, delivered when UE exits PSM
    [Tags]    cp-ciot    downlink    psm    priority-2
    Run Test    iot_cp_downlink

TC-IOT-005 NB-IoT Coverage Enhancement Levels
    [Documentation]    TC-IOT-005: CE level detection and reporting
    ...    Standard: TS 36.321 §7.1 — CE0/CE1/CE2 levels
    ...    Expected: CE level stats returned per level
    [Tags]    coverage    ce-level    priority-2
    Run Test    iot_coverage_levels

TC-IOT-006 eDRX Configuration
    [Documentation]    TC-IOT-006: Extended DRX cycle and paging window
    ...    Standard: TS 24.301 §5.3.12, TS 24.501 §5.3.14
    ...    Expected: eDRX configured with valid cycle and PTW
    [Tags]    edrx    power-saving    priority-2
    Run Test    iot_edrx_config

TC-IOT-007 NIDD Session Lifecycle
    [Documentation]    TC-IOT-007: NIDD session create/query/terminate
    ...    Standard: TS 23.682 §5.13 — SCEF Non-IP Data Delivery
    ...    Expected: Session created, queryable, terminable
    [Tags]    nidd    scef    priority-1
    Run Test    iot_nidd_session

TC-IOT-008 NIDD Downlink Data Delivery
    [Documentation]    TC-IOT-008: DL data via NIDD/SCEF T8 API
    ...    Standard: TS 29.122 — T8 API (App Server → SCEF → UE)
    ...    Expected: DL data queued/delivered via NIDD
    [Tags]    nidd    t8-api    downlink    priority-2
    Run Test    iot_nidd_downlink

TC-IOT-009 Ambient IoT Tag Registration
    [Documentation]    TC-IOT-009: Tag registration (Class A/B/C)
    ...    Standard: TS 22.369 — Ambient IoT tag management
    ...    Expected: Tags registered with class, group, type
    [Tags]    ambient    tag    priority-1
    Run Test    iot_tag_register

TC-IOT-010 Ambient IoT Reader Registration
    [Documentation]    TC-IOT-010: Reader registration with gNB binding
    ...    Standard: TS 22.369 — Reader infrastructure
    ...    Expected: Reader registered with location and gNB IP
    [Tags]    ambient    reader    priority-2
    Run Test    iot_reader_register

TC-IOT-011 Ambient IoT Inventory Scan
    [Documentation]    TC-IOT-011: Bulk tag scan/read via reader
    ...    Standard: TS 22.369 — Inventory operations
    ...    Expected: Scan event processed, tags discovered
    [Tags]    ambient    inventory    scan    priority-1
    Run Test    iot_inventory_scan

TC-IOT-012 Ambient IoT Tag Authentication
    [Documentation]    TC-IOT-012: Lightweight group-key authentication
    ...    Standard: TS 22.369 §5.2 — Group-based challenge-response
    ...    Expected: Valid tag authenticates, invalid rejected
    [Tags]    ambient    auth    security    priority-2
    Run Test    iot_tag_auth

TC-IOT-013 Ambient IoT Tag Positioning
    [Documentation]    TC-IOT-013: RSSI-based tag positioning via readers
    ...    Standard: TS 22.369 §5.2.2 — Ambient IoT positioning service requirements
    ...    Expected: Position estimated from multi-reader RSSI
    [Tags]    ambient    positioning    priority-2
    Run Test    iot_tag_positioning

TC-IOT-014 IoT Dashboard Stats
    [Documentation]    TC-IOT-014: Aggregate IoT dashboard statistics
    ...    Procedure: Query dashboard after creating devices/sessions
    ...    Expected: Stats reflect all IoT device types
    [Tags]    dashboard    stats    priority-3
    Run Test    iot_dashboard

TC-IOT-015 NB-IoT Rate Control
    [Documentation]    TC-IOT-015: APN and per-device rate limiting
    ...    Standard: TS 23.401 §4.7.7.2 — Rate control
    ...    Expected: Rate limits enforced on CP CIoT data
    [Tags]    rate-control    cp-ciot    priority-3
    Run Test    iot_rate_control
