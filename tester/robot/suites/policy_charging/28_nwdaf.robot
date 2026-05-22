# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    NWDAF (Network Data Analytics Function) Test Suite
...              TS 23.288 — Architecture enhancements for 5G System (NWDAF)
...              TS 29.520 — Nnwdaf services (Stage 3)
...              Covers: Data collection, anomaly detection, analytics subscription,
...              NF load analytics, UE mobility analytics, data export
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        nwdaf    analytics    ai-ml

*** Test Cases ***
# ===============================================================
# Data Collection & Event Exposure
# TS 23.288 §6.2 — Data collection framework
# TS 23.288 §6.2.2 — Event subscription for data collection
# ===============================================================
TC-NWDAF-001 Data Point Collection Registration And PDU Session Events
    [Documentation]    TC-NWDAF-001: NWDAF collects events from registration and PDU sessions
    ...    Standard: TS 23.288 §6.2.2.1 (NF event exposure for data collection)
    ...    Procedure:
    ...    1. NWDAF subscribes to AMF events (Namf_EventExposure):
    ...       - UE registration, deregistration, location report
    ...    2. NWDAF subscribes to SMF events (Nsmf_EventExposure):
    ...       - PDU session establishment, release, QoS change
    ...    3. UE performs registration (triggers AMF event)
    ...    4. UE establishes PDU session (triggers SMF event)
    ...    5. NWDAF receives and stores both event notifications
    ...    Verification:
    ...    - NWDAF event store contains registration event with SUPI, timestamp
    ...    - NWDAF event store contains PDU session event with DNN, UE IP, QoS
    ...    - Event timestamps monotonically increasing
    ...    Expected Result: NWDAF collects registration and session data points
    [Tags]    data-collection    event-exposure    amf    smf    priority-1
    Log    TC-NWDAF-001: Data Point Collection (Registration + PDU Session)

TC-NWDAF-002 Anomaly Detection Trigger
    [Documentation]    TC-NWDAF-002: NWDAF detects anomalous UE behavior
    ...    Standard: TS 23.288 §6.7.5 (Abnormal behaviour analytics)
    ...    Procedure:
    ...    1. NWDAF baseline model trained on normal UE activity patterns
    ...    2. Simulate anomalous UE behavior:
    ...       - Rapid repeated registration/deregistration cycles
    ...       - Unusual DNN access pattern
    ...    3. NWDAF anomaly detection evaluates incoming events
    ...    4. Anomaly score exceeds threshold
    ...    5. NWDAF generates analytics notification to subscribed NF (AMF/PCF)
    ...    Verification:
    ...    - Anomaly detected within detection window
    ...    - Analytics notification contains: SUPI, anomaly type, confidence score
    ...    - Subscribed NF receives notification
    ...    Expected Result: Anomalous UE behavior detected and reported
    [Tags]    anomaly    detection    abnormal-behaviour    priority-1
    Log    TC-NWDAF-002: Anomaly Detection Trigger

TC-NWDAF-003 Analytics Subscription Via API
    [Documentation]    TC-NWDAF-003: Subscribe to NWDAF analytics via REST API
    ...    Standard: TS 29.520 §5.2 (Nnwdaf_AnalyticsSubscription)
    ...    Procedure:
    ...    1. POST /api/nwdaf/subscriptions with:
    ...       - eventId (e.g., UE_MOBILITY, NF_LOAD, ABNORMAL_BEHAVIOUR)
    ...       - targetFilter (SUPI, S-NSSAI, DNN)
    ...       - notificationUri (callback URL)
    ...       - repetitionPeriod (reporting interval)
    ...    2. NWDAF validates and creates subscription
    ...    3. GET /api/nwdaf/subscriptions/{subscriptionId} confirms active
    ...    4. Trigger relevant event and verify notification delivered
    ...    Verification:
    ...    - Subscription created (201 Created) with subscriptionId
    ...    - Subscription listed in GET /api/nwdaf/subscriptions
    ...    - Analytics notification delivered to notificationUri
    ...    Expected Result: Analytics subscription active, notifications flowing
    [Tags]    api    subscription    nnwdaf    priority-1
    Log    TC-NWDAF-003: Analytics Subscription via API

# ===============================================================
# Analytics Output — NF Load & UE Mobility
# TS 23.288 §6.3 — NF load analytics
# TS 23.288 §6.7.1 — UE mobility analytics
# ===============================================================
TC-NWDAF-010 Load Analytics Per NF
    [Documentation]    TC-NWDAF-010: NWDAF produces load analytics per NF instance
    ...    Standard: TS 23.288 §6.3 (Load level information for NF)
    ...    Procedure:
    ...    1. NWDAF collects load metrics from NFs (AMF, SMF, UPF):
    ...       - CPU utilization, memory usage
    ...       - Active UE count, active session count
    ...       - Request throughput (registrations/sec, sessions/sec)
    ...    2. NWDAF computes load analytics (current + predicted)
    ...    3. GET /api/nwdaf/analytics?eventId=NF_LOAD&nfType=AMF
    ...    4. Response includes: nfInstanceId, nfType, nfLoadLevel (0-100),
    ...       nfCpuUsage, nfMemoryUsage, nfLoadTimeStamp
    ...    Verification:
    ...    - Load analytics available for each NF type
    ...    - Load level values within valid range (0-100)
    ...    - Timestamps current (within reporting interval)
    ...    Expected Result: Per-NF load analytics produced
    [Tags]    load    nf-load    capacity    priority-1
    Log    TC-NWDAF-010: Load Analytics per NF

TC-NWDAF-011 UE Mobility Analytics
    [Documentation]    TC-NWDAF-011: NWDAF produces UE mobility analytics
    ...    Standard: TS 23.288 §6.7.1 (UE mobility analytics)
    ...    Procedure:
    ...    1. NWDAF collects UE location events from AMF:
    ...       - Serving cell changes, TAI changes
    ...       - Registration area updates
    ...    2. UE performs multiple location changes (simulated mobility)
    ...    3. NWDAF computes mobility analytics:
    ...       - Trajectory prediction
    ...       - Expected UE location (spatial distribution)
    ...    4. GET /api/nwdaf/analytics?eventId=UE_MOBILITY&supi={supi}
    ...    5. Response includes: supi, locationInfoList, trajectoryInfoList
    ...    Verification:
    ...    - Mobility analytics contain observed location history
    ...    - Trajectory prediction covers requested time window
    ...    - Location probabilities sum to <= 1.0
    ...    Expected Result: UE mobility analytics with trajectory prediction
    [Tags]    mobility    ue-mobility    trajectory    priority-2
    Log    TC-NWDAF-011: UE Mobility Analytics

TC-NWDAF-012 Analytics Data Export
    [Documentation]    TC-NWDAF-012: Export collected analytics data in bulk
    ...    Standard: TS 23.288 §6.2.6 (Data collection and reporting)
    ...    Procedure:
    ...    1. NWDAF has accumulated analytics data (events + computed analytics)
    ...    2. GET /api/nwdaf/export with parameters:
    ...       - startTime, endTime (time window)
    ...       - eventId filter (optional)
    ...       - format (json)
    ...    3. Verify export contains all events within time window
    ...    4. Verify export data integrity (no missing or duplicate events)
    ...    Verification:
    ...    - Export response contains events matching filter criteria
    ...    - Event count matches expected count for time window
    ...    - Each event has valid schema (eventId, timestamp, payload)
    ...    - Export does not include events outside requested time window
    ...    Expected Result: Analytics data exported correctly
    [Tags]    export    data    bulk    priority-2
    Log    TC-NWDAF-012: Analytics Data Export
