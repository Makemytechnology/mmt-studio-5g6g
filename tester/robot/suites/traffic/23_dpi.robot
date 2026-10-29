# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    DPI (Deep Packet Inspection) Test Suite
...              TS 23.501 §5.8 — Application detection and control
...              TS 29.551 — Packet Flow Description (PFD) Management
...              TS 29.512 — Session Management Policy Control (Npcf_SMPolicyControl)
...              Covers: Application detection, PFD rules, traffic classification,
...              DNS cache, application-based QoS enforcement, DPI logging
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        dpi    inspection    application-detection

*** Test Cases ***
# ===============================================================
# Application Detection
# TS 23.501 §5.8.2 — Application detection information
# TS 29.551 §5.2 — PFD management procedures
# ===============================================================
TC-DPI-001 Application Detection HTTP DNS Video
    [Documentation]    TC-DPI-001: Detect application types from traffic patterns
    ...    Standard: TS 23.501 §5.8.2 (Application detection via PFD)
    ...    Procedure:
    ...    1. Register UE and establish PDU session (DNN=internet)
    ...    2. Configure PFD rules for HTTP, DNS, and video streaming apps
    ...    3. UE sends HTTP traffic (port 80/443) — verify detected as HTTP
    ...    4. UE sends DNS queries (port 53) — verify detected as DNS
    ...    5. UE sends video streaming traffic (known URL pattern) — verify detected
    ...    Verification:
    ...    - UPF DPI engine classifies HTTP traffic correctly
    ...    - DNS queries identified and logged
    ...    - Video streaming detected by URL/domain pattern match
    ...    Expected Result: All three application types detected accurately
    [Tags]    detection    http    dns    video    priority-1
    Log    TC-DPI-001: Application Detection (HTTP, DNS, Video)

TC-DPI-002 PFD Rules Configuration Via REST API
    [Documentation]    TC-DPI-002: Configure PFD (Packet Flow Description) rules via API
    ...    Standard: TS 29.551 §5.2.2 (Npcf_PFDManagement_Create)
    ...    Procedure:
    ...    1. POST /api/dpi/pfd-rules with PFD rule:
    ...       application_id, flow_descriptions (IP 5-tuple), domain_names, URLs
    ...    2. GET /api/dpi/pfd-rules — verify rule persisted
    ...    3. Verify UPF receives updated PFD set
    ...    4. DELETE /api/dpi/pfd-rules/{rule_id} — verify cleanup
    ...    Verification:
    ...    - PFD rule created and retrievable via API
    ...    - UPF applies new PFD for traffic classification
    ...    - Rule deletion removes detection for that application
    ...    Expected Result: PFD rules CRUD operations functional
    [Tags]    pfd    api    config    priority-1
    Log    TC-DPI-002: PFD Rules Configuration via REST API

TC-DPI-003 Traffic Classification By Application
    [Documentation]    TC-DPI-003: Classify traffic and report per-application statistics
    ...    Standard: TS 23.501 §5.8.2 (Application detection), §5.7.1 (QoS model)
    ...    Procedure:
    ...    1. Register UE and establish PDU session
    ...    2. Configure PFD rules for multiple applications
    ...    3. UE generates mixed traffic (web, video, messaging)
    ...    4. Query /api/dpi/stats for per-application traffic breakdown
    ...    5. Verify each flow classified to correct application ID
    ...    Verification:
    ...    - Traffic counters per application ID populated
    ...    - Volume (UL/DL) attributed to correct application
    ...    - Unmatched traffic classified as default/unknown
    ...    Expected Result: Per-application traffic classification accurate
    [Tags]    classification    stats    priority-1
    Log    TC-DPI-003: Traffic Classification by Application

# ===============================================================
# DNS Cache & QoS Enforcement
# TS 23.501 §5.8.2.3 — DNS-based application detection
# TS 23.501 §5.7.2.1 — QoS flow binding
# ===============================================================
TC-DPI-010 DPI DNS Cache Population
    [Documentation]    TC-DPI-010: DNS-based application detection with cache
    ...    Standard: TS 23.501 §5.8.2.3 (DNS-based detection)
    ...    Procedure:
    ...    1. Configure PFD rule with domain_name pattern (e.g., *.video.example.com)
    ...    2. UE sends DNS query for video.example.com
    ...    3. UPF DPI inspects DNS response, caches IP-to-domain mapping
    ...    4. Subsequent data traffic to resolved IP classified by cached domain
    ...    5. Verify cache TTL expiry and re-population on next DNS query
    ...    Verification:
    ...    - DNS response inspected and domain-to-IP cached
    ...    - Subsequent traffic to cached IP classified correctly
    ...    - Cache entry expires per DNS TTL
    ...    Expected Result: DNS cache enables domain-based classification for non-DNS traffic
    [Tags]    dns    cache    domain    priority-1
    Log    TC-DPI-010: DPI DNS Cache Population

TC-DPI-011 Application Based QoS Enforcement
    [Documentation]    TC-DPI-011: QoS enforcement per detected application
    ...    Standard: TS 23.501 §5.8.3 (Application-triggered QoS), §5.7.2.1 (QoS flows)
    ...    Procedure:
    ...    1. Configure PCC rule: video streaming app -> 5QI=4 (non-conversational video)
    ...    2. Configure PCC rule: web browsing -> 5QI=9 (default)
    ...    3. Register UE and establish PDU session
    ...    4. UE sends video streaming traffic — DPI detects and triggers dedicated QoS flow
    ...    5. Verify video traffic mapped to 5QI=4 QoS flow
    ...    6. Verify web traffic remains on default 5QI=9 flow
    ...    Verification:
    ...    - DPI detection triggers PCC rule evaluation
    ...    - Dedicated QoS flow created for video (5QI=4)
    ...    - Traffic separation: video on dedicated, web on default
    ...    Expected Result: Application-aware QoS enforcement active
    [Tags]    qos    enforcement    5qi    pcc    priority-1
    Log    TC-DPI-011: Application-Based QoS Enforcement

TC-DPI-012 DPI Detection Logging
    [Documentation]    TC-DPI-012: Verify DPI detections logged for audit and analytics
    ...    Standard: TS 23.501 §5.8.2 (Detection reporting)
    ...    Procedure:
    ...    1. Register UE and establish PDU session
    ...    2. Configure PFD rules with logging enabled
    ...    3. UE generates traffic matching configured applications
    ...    4. Query /api/dpi/detections for detection log entries
    ...    5. Verify log contains: timestamp, IMSI, application_id, volume, action
    ...    Verification:
    ...    - Each detection event logged with timestamp
    ...    - Log entries correlate to correct UE (IMSI) and application
    ...    - Log queryable via REST API with time range filter
    ...    Expected Result: DPI detections logged and queryable
    [Tags]    logging    audit    api    priority-2
    Log    TC-DPI-012: DPI Detection Logging
