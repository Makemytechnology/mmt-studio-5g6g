# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    CHF (Charging Function) Test Suite
...              TS 32.290 — Charging management; 5G system; Services, operations and procedures
...              TS 32.291 — 5G system; Charging service (Nchf_ConvergedCharging)
...              Covers: CDR generation, online/offline charging, tariff plans,
...              balance management, CDR export, per-service charging profiles, roaming CDRs
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        charging    chf    nchf

*** Test Cases ***
# ===============================================================
# CDR Generation
# TS 32.291 §6.1.2 — Charging Data Record creation
# TS 32.290 §6.2.1 — CDR triggering conditions
# ===============================================================
TC-CHF-001 CDR Generation On PDU Session Establish And Release
    [Documentation]    TC-CHF-001: Verify CDR created on PDU session lifecycle
    ...    Standard: TS 32.291 §6.1.2 (CDR creation), TS 32.290 §6.2.1 (triggers)
    ...    Procedure:
    ...    1. Register UE and establish PDU session (DNN=internet)
    ...    2. CHF receives Nchf_ConvergedCharging_Create at session start
    ...    3. Release PDU session
    ...    4. CHF receives Nchf_ConvergedCharging_Release at session end
    ...    5. Verify CDR contains: IMSI, DNN, start/end time, data volume
    ...    Verification:
    ...    - CDR record exists with correct IMSI and DNN
    ...    - CDR timestamps bracket the session duration
    ...    - Data volume counters (uplink + downlink) present
    ...    Expected Result: CDR generated with full session lifecycle data
    [Tags]    cdr    pdu-session    lifecycle    priority-1
    Log    TC-CHF-001: CDR Generation on PDU Session

TC-CHF-002 Online Charging Quota Management
    [Documentation]    TC-CHF-002: Online charging with quota allocation and tracking
    ...    Standard: TS 32.290 §5.3 (Converged Charging scenario), TS 32.290 §5.4.8 (Quota management)
    ...    Procedure:
    ...    1. Configure UE subscription with online charging enabled
    ...    2. Register UE and establish PDU session
    ...    3. CHF allocates initial quota (data volume or time)
    ...    4. UE consumes data traffic
    ...    5. CHF tracks usage, issues quota updates (Nchf_ConvergedCharging_Update)
    ...    6. Verify quota threshold triggers re-authorization
    ...    Verification:
    ...    - Initial quota allocated by CHF
    ...    - Usage tracked and reported
    ...    - Quota update triggered before exhaustion
    ...    Expected Result: Online charging quota lifecycle managed correctly
    [Tags]    online    quota    ocs    priority-1
    Log    TC-CHF-002: Online Charging Quota Management

TC-CHF-003 Offline Charging Post-Processing CDRs
    [Documentation]    TC-CHF-003: Offline charging CDR generation for post-processing
    ...    Standard: TS 32.290 §5.1 (Offline charging scenario), TS 32.290 §6.5 (Nchf_OfflineOnlyCharging service)
    ...    Procedure:
    ...    1. Configure UE subscription with offline charging
    ...    2. Register UE, establish PDU session, exchange traffic
    ...    3. Release PDU session
    ...    4. Verify CDR written to charging data store for post-processing
    ...    5. CDR includes: rating group, service-specific data, volume
    ...    Verification:
    ...    - CDR stored in offline charging data store
    ...    - CDR fields conform to TS 32.298 CDR format
    ...    - No real-time quota enforcement (offline mode)
    ...    Expected Result: Offline CDR available for billing post-processing
    [Tags]    offline    cdr    ofcs    priority-1
    Log    TC-CHF-003: Offline Charging CDRs

# ===============================================================
# Tariff & Balance
# TS 32.291 §6.1.6 — Charging Data Model (rating/balance types)
# TS 32.290 §5.3.2.3 — Session based charging (tariff time changes)
# ===============================================================
TC-CHF-010 Tariff Plan Application
    [Documentation]    TC-CHF-010: Verify correct tariff plan applied to charging session
    ...    Standard: TS 32.290 §5.3.2.3 (Session based charging — tariff time changes)
    ...    Procedure:
    ...    1. Configure tariff plan: rate per MB, time-of-day multiplier
    ...    2. Register UE and establish PDU session
    ...    3. UE sends data traffic (known volume)
    ...    4. Verify CHF applies configured tariff to calculate charge
    ...    5. CDR reflects tariff-based charge amount
    ...    Verification:
    ...    - Tariff plan loaded by CHF for the subscriber
    ...    - Charge amount matches expected (volume x rate x multiplier)
    ...    Expected Result: Tariff plan correctly applied to usage
    [Tags]    tariff    rating    priority-1
    Log    TC-CHF-010: Tariff Plan Application

TC-CHF-011 Balance Check And Deduction
    [Documentation]    TC-CHF-011: Verify balance check and deduction for online charging
    ...    Standard: TS 32.291 §6.1.6 (Data Model — balance/quota types)
    ...    Procedure:
    ...    1. Configure subscriber with known account balance
    ...    2. Register UE, establish PDU session (online charging)
    ...    3. CHF checks balance at session start (sufficient funds)
    ...    4. UE consumes data, CHF deducts from balance
    ...    5. Query balance via REST API — verify deduction matches usage
    ...    Verification:
    ...    - Balance checked before quota allocation
    ...    - Deduction amount equals consumed quota x tariff
    ...    - Balance query reflects updated amount
    ...    Expected Result: Balance correctly decremented by usage charge
    [Tags]    balance    deduction    online    priority-1
    Log    TC-CHF-011: Balance Check and Deduction

TC-CHF-012 CDR Export Via REST API
    [Documentation]    TC-CHF-012: Export CDRs via REST API endpoint
    ...    Standard: TS 32.297 §6.2 (CDR file transfer)
    ...    Procedure:
    ...    1. Generate CDRs via PDU session establish/release cycle
    ...    2. GET /api/charging/cdrs with time range filter
    ...    3. Verify CDR records returned in JSON format
    ...    4. Verify CDR fields: imsi, dnn, start_time, end_time, volume_ul, volume_dl
    ...    5. Verify filtering by IMSI and DNN works
    ...    Verification:
    ...    - REST endpoint returns CDR data
    ...    - Filtering parameters applied correctly
    ...    - CDR data matches session records
    ...    Expected Result: CDRs exportable and filterable via REST API
    [Tags]    api    cdr    export    priority-1
    Log    TC-CHF-012: CDR Export via REST API

# ===============================================================
# Per-Service Charging Profiles
# TS 32.291 §6.1.3.1 — Service-specific charging
# TS 32.290 §6.5 — Charging per service type
# ===============================================================
TC-CHF-020 Charging Profile Per Service Voice Data V2X
    [Documentation]    TC-CHF-020: Separate charging profiles for voice, data, and V2X
    ...    Standard: TS 32.291 §6.1.3.1 (Service-specific charging)
    ...    Procedure:
    ...    1. Configure charging profiles: voice (per-minute), data (per-MB), v2x (flat rate)
    ...    2. Register UE with all three service subscriptions
    ...    3. Establish PDU sessions: DNN=ims (voice), DNN=internet (data), DNN=v2x
    ...    4. Generate traffic on each session
    ...    5. Verify CDRs use correct rating group and tariff per service
    ...    Verification:
    ...    - Voice CDR: duration-based charging, rating group matches voice profile
    ...    - Data CDR: volume-based charging, rating group matches data profile
    ...    - V2X CDR: flat-rate charging, rating group matches v2x profile
    ...    Expected Result: Each service charged per its configured profile
    [Tags]    profile    per-service    voice    data    v2x    priority-1
    Log    TC-CHF-020: Per-Service Charging Profiles

TC-CHF-021 Roaming CDR Generation
    [Documentation]    TC-CHF-021: CDR generation for roaming subscriber
    ...    Standard: TS 32.291 §6.1.6.2.2.13 (RoamingQBCInformation), TS 32.291 §6.1.6.2.2.15 (RoamingChargingProfile)
    ...    Procedure:
    ...    1. Register UE with HPLMN != serving PLMN (roaming scenario)
    ...    2. Establish PDU session (home-routed or local breakout)
    ...    3. Exchange data traffic
    ...    4. Release PDU session
    ...    5. Verify CDR includes roaming indicators: VPLMN, HPLMN, roaming type
    ...    Verification:
    ...    - CDR contains visited PLMN and home PLMN identifiers
    ...    - Roaming charging rate applied (distinct from home rate)
    ...    - CDR flagged for inter-PLMN settlement
    ...    Expected Result: Roaming CDR with correct PLMN and roaming tariff
    [Tags]    roaming    cdr    inter-plmn    priority-2
    Log    TC-CHF-021: Roaming CDR Generation
