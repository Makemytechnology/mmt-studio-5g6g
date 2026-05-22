# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    5G NTN (Non-Terrestrial Network) Test Suite
...              Tests: Satellite constellation, coverage, timing, feeder links, TAI
...              Covers: TS 38.821 (NTN study), TS 23.501 §5.4.10 (NTN arch),
...              TS 38.213 §4.2 (timing advance), TS 24.501 §5.3.7 (timers)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        ntn    satellite    non-terrestrial

*** Test Cases ***
TC-NTN-001 Load Default Constellation
    [Documentation]    TC-NTN-001: Load test constellation (2 LEO + 1 GEO)
    ...    Procedure: POST /api/ntn/load-defaults, verify satellites + ground stations
    ...    Expected: 3 satellites, 1 ground station, 5 TAIs loaded
    [Tags]    constellation    setup    priority-1
    Run Test    ntn_load_defaults

TC-NTN-002 Satellite Configuration
    [Documentation]    TC-NTN-002: Add/query satellites in constellation
    ...    Standard: TS 38.821 §4 — NTN system architecture
    ...    Expected: Satellite added with correct orbit parameters
    [Tags]    satellite    config    priority-1
    Run Test    ntn_satellite_config

TC-NTN-003 Ground Station Registration
    [Documentation]    TC-NTN-003: Add ground station with gNB binding
    ...    Standard: TS 23.501 §5.4.10 — NR satellite access support (gateway/feeder-link architecture is RAN-side, deferred to TS 38.300)
    ...    Expected: Ground station registered with coordinates and gNB IP
    [Tags]    ground-station    config    priority-2
    Run Test    ntn_ground_station

TC-NTN-004 LEO Coverage Check
    [Documentation]    TC-NTN-004: Coverage query for LEO satellite
    ...    Standard: TS 38.821 §4.1 — Min elevation angle 10 deg
    ...    Expected: Coverage status with elevation and slant range
    [Tags]    coverage    leo    priority-1
    Run Test    ntn_leo_coverage

TC-NTN-005 GEO Coverage Check
    [Documentation]    TC-NTN-005: GEO satellite coverage (always visible)
    ...    Standard: TS 38.821 §4 — GEO at 35786 km
    ...    Expected: Always covered within footprint, RTT ~270ms
    [Tags]    coverage    geo    priority-2
    Run Test    ntn_geo_coverage

TC-NTN-006 Propagation Delay and Timing Advance
    [Documentation]    TC-NTN-006: Compute delay and TA for NTN
    ...    Standard: TS 38.821 §6.1, TS 38.213 §4.2
    ...    Expected: Service link + feeder link delay, TA in microseconds
    [Tags]    timing    propagation    ta    priority-1
    Run Test    ntn_timing_advance

TC-NTN-007 NAS Timer Adjustment
    [Documentation]    TC-NTN-007: Adjust NAS timers for NTN delay
    ...    Standard: TS 38.821 §7.2, TS 24.501 §5.3.7
    ...    Expected: T3510/T3511/T3517 increased by 4*RTT guard
    [Tags]    timers    nas    priority-2
    Run Test    ntn_timer_adjustment

TC-NTN-008 Geographic TAI Lookup
    [Documentation]    TC-NTN-008: TAI resolution by geographic position
    ...    Standard: TS 23.501 §5.4.10 — NR satellite TA segregation (cells of each NR satellite RAT Type need to be deployed in distinct TAs)
    ...    Expected: Correct TAI found for UE location
    [Tags]    tai    geographic    priority-1
    Run Test    ntn_tai_lookup

TC-NTN-009 TAI Change Detection
    [Documentation]    TC-NTN-009: Detect TAI change when UE moves
    ...    Standard: TS 23.501 §5.4.10 — TA segregation drives TAU trigger when serving cell crosses an NTN TA boundary
    ...    Expected: TAI change detected, TAU would be triggered
    [Tags]    tai    tau    mobility    priority-2
    Run Test    ntn_tai_change

TC-NTN-010 Feeder Link Status
    [Documentation]    TC-NTN-010: Query active feeder links and history
    ...    Standard: TS 23.501 §5.4.10 — NR satellite access support (feeder-link switching mechanics are RAN-side per the §5.4.10 editor's note pointing to TS 38.300)
    ...    Expected: Active links and switch history returned
    [Tags]    feeder-link    priority-2
    Run Test    ntn_feeder_links

TC-NTN-011 DL Buffer During Coverage Gap
    [Documentation]    TC-NTN-011: DL packet buffering for discontinuous coverage
    ...    Standard: TS 23.501 §5.4.10 — NR satellite access support (discontinuous-coverage buffering itself is a RAN/UE behaviour outside §5.4.10's normative scope)
    ...    Expected: Buffer status reflects pending DL packets
    [Tags]    buffer    coverage-gap    priority-2
    Run Test    ntn_dl_buffer

TC-NTN-012 Constellation Positions
    [Documentation]    TC-NTN-012: Get real-time satellite positions
    ...    Standard: TS 38.821 §7.3.6 — Ephemeris Data for NTN
    ...    Expected: Positions computed for all satellites at current time
    [Tags]    ephemeris    position    priority-3
    Run Test    ntn_constellation_positions
