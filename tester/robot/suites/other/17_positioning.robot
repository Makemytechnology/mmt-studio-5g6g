# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    5G NR Positioning Test Suite
...              Tests: LMF location services, positioning methods, geofencing
...              Covers: TS 23.273 (5G positioning), TS 38.305 (NR methods),
...              TS 29.572 (Nlmf_Location), TS 38.455 (NRPPa)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        positioning    lmf    location

*** Test Cases ***
TC-POS-001 E-CID Positioning
    [Documentation]    TC-POS-001: NR Enhanced Cell ID positioning
    ...    Standard: TS 38.305 §8.1 — E-CID uses serving cell + timing advance
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. Register gNB position via GMLC API
    ...    3. Request E-CID location via GMLC API
    ...    4. Verify location result with uncertainty
    ...    Expected: Location returned with uncertainty < 200m
    [Tags]    smoke    ecid    priority-1
    Run Test    pos_ecid

TC-POS-002 Multi-RTT Positioning
    [Documentation]    TC-POS-002: Multi-Round Trip Time positioning
    ...    Standard: TS 38.305 §8.6 — RTT from >= 3 gNBs
    ...    Procedure:
    ...    1. Register UE, establish PDU session on 3 gNBs
    ...    2. Register 3 gNB positions via GMLC API
    ...    3. Request Multi-RTT location
    ...    4. Verify trilateration-based position
    ...    Expected: Location with uncertainty < 10m
    [Tags]    multi-rtt    priority-2
    Run Test    pos_multi_rtt

TC-POS-003 DL-TDOA Positioning
    [Documentation]    TC-POS-003: Downlink Time Difference of Arrival
    ...    Standard: TS 38.305 §8.2 — DL-TDOA via PRS measurements
    ...    Procedure:
    ...    1. Register UE, allocate PRS resources on 3+ gNBs
    ...    2. Request DL-TDOA location
    ...    3. Verify RSTD-based hyperbolic trilateration
    ...    Expected: Location with uncertainty < 15m
    [Tags]    dl-tdoa    prs    priority-2
    Run Test    pos_dl_tdoa

TC-POS-004 A-GNSS Positioning
    [Documentation]    TC-POS-004: Assisted GNSS positioning
    ...    Standard: TS 38.305 §8.8 — GNSS via LPP assistance data
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. Request A-GNSS location via GMLC API
    ...    3. Verify GNSS fix coordinates
    ...    Expected: Location returned (simulated GNSS fix)
    [Tags]    agnss    gnss    priority-2
    Run Test    pos_agnss

TC-POS-005 PRS Resource Allocation
    [Documentation]    TC-POS-005: PRS resource allocation and configuration
    ...    Standard: TS 38.211 §7.4.1.7 — PRS signal configuration
    ...    Procedure:
    ...    1. Register gNB position
    ...    2. Allocate PRS resource with specific parameters
    ...    3. Verify PRS config (periodicity, num_rb, comb_size)
    ...    4. Deallocate PRS resource
    ...    Expected: PRS allocated and retrievable via API
    [Tags]    prs    config    priority-1
    Run Test    pos_prs_config

TC-POS-006 Geofence Enter/Leave
    [Documentation]    TC-POS-006: Geofence area event detection
    ...    Standard: TS 23.273 §6.7 — Deferred location with area events
    ...    Procedure:
    ...    1. Register UE, set gNB position
    ...    2. Create geofence zone around gNB
    ...    3. Request UE location (inside zone)
    ...    4. Verify geofence trigger (enter event)
    ...    Expected: Geofence enter event detected
    [Tags]    geofence    area-event    priority-2
    Run Test    pos_geofence

TC-POS-007 LCS Privacy Check
    [Documentation]    TC-POS-007: LCS privacy enforcement
    ...    Standard: TS 23.271 §9 — LCS privacy classes
    ...    Procedure:
    ...    1. Set LCS privacy to deny commercial requests
    ...    2. Request commercial location → should be denied
    ...    3. Request emergency location → should be allowed
    ...    Expected: Privacy enforced per client type
    [Tags]    privacy    lcs    priority-2
    Run Test    pos_lcs_privacy

TC-POS-008 Auto Method Selection
    [Documentation]    TC-POS-008: Automatic positioning method selection
    ...    Standard: TS 38.305 §8 — Method selection based on QoS
    ...    Procedure:
    ...    1. Register UE with gNB position
    ...    2. Request location with method=auto, accuracy=5m
    ...    3. Verify LMF selected appropriate method
    ...    4. Request with accuracy=100m → should use E-CID
    ...    Expected: Method auto-selected based on QoS requirements
    [Tags]    auto    method-selection    priority-2
    Run Test    pos_auto_method

TC-POS-009 Location History
    [Documentation]    TC-POS-009: Location history retrieval
    ...    Standard: TS 29.572 §5.2 — Location history
    ...    Procedure:
    ...    1. Request multiple locations for same UE
    ...    2. Query location history via API
    ...    3. Verify chronological ordering
    ...    Expected: History contains all previous locations
    [Tags]    history    priority-3
    Run Test    pos_history

TC-POS-010 gNB Antenna Configuration
    [Documentation]    TC-POS-010: gNB antenna registration for AoD/AoA
    ...    Standard: TS 38.455 §9.2.44 — TRP beam information
    ...    Procedure:
    ...    1. Register gNB with antenna config (azimuth, beamwidth, downtilt)
    ...    2. Verify config stored via API
    ...    3. Request DL-AoD location using antenna info
    ...    Expected: Antenna config persisted, AoD method uses beam info
    [Tags]    antenna    aod    aoa    priority-3
    Run Test    pos_antenna_config
