# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    MEC (Multi-access Edge Computing) Test Suite
...              ETSI MEC 003 — MEC Framework and Reference Architecture
...              ETSI MEC 011 — MEC Platform Application Enablement (Mp1)
...              TS 23.548 — 5GS Edge Computing enhancements
...              Covers: Edge app registration, ULCL traffic steering,
...              AF-influenced routing, edge DNS, application discovery
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        mec    edge    edge-computing

*** Test Cases ***
# ===============================================================
# Edge Application Lifecycle
# ETSI MEC 003 §6.2 — MEC application lifecycle
# ETSI MEC 011 §6.2.2 — Application registration (Mp1)
# ===============================================================
TC-MEC-001 Edge Application Registration
    [Documentation]    TC-MEC-001: Register edge application on MEC platform
    ...    Standard: ETSI MEC 011 §6.2.2 (AppInfo registration via Mp1)
    ...    Procedure:
    ...    1. POST /api/mec/applications with app descriptor:
    ...       - appName, appProvider, appDId (descriptor ID)
    ...       - appServiceRequired, appServiceOptional
    ...       - trafficRuleDescriptors, dnsRuleDescriptors
    ...    2. MEC platform validates app descriptor
    ...    3. Application instantiated on edge node
    ...    4. Application state transitions to ACTIVE
    ...    Verification:
    ...    - API returns 201 Created with appInstanceId
    ...    - Application listed in GET /api/mec/applications
    ...    - Application state is ACTIVE
    ...    - Traffic rules installed for application
    ...    Expected Result: Edge application registered and active
    [Tags]    app-registration    lifecycle    mp1    priority-1
    Log    TC-MEC-001: Edge Application Registration

TC-MEC-002 ULCL Traffic Steering
    [Documentation]    TC-MEC-002: UPF ULCL steers traffic to local edge application
    ...    Standard: TS 23.548 §6.3.3 (ULCL/BP for local routing)
    ...    Procedure:
    ...    1. Edge application registered and active (TC-MEC-001 precondition)
    ...    2. UE registered with PDU session (DNN=internet)
    ...    3. SMF configures ULCL at branching point UPF
    ...    4. Traffic matching edge app filter steered to local PSA (UPF)
    ...    5. Non-matching traffic routed to central data network
    ...    Verification:
    ...    - ULCL rule installed in UPF with correct traffic filter
    ...    - Matching traffic reaches local edge application
    ...    - Non-matching traffic reaches central DN as normal
    ...    - UE IP address unchanged (session continuity)
    ...    Expected Result: ULCL steers matching traffic to edge, rest to DN
    [Tags]    ulcl    traffic-steering    upf    priority-1
    Log    TC-MEC-002: ULCL Traffic Steering

TC-MEC-003 AF-Influenced Traffic Routing
    [Documentation]    TC-MEC-003: AF influences traffic routing via NEF/PCF
    ...    Standard: TS 23.548 §6.2 (AF influence on traffic routing)
    ...    Procedure:
    ...    1. AF (Application Function) sends traffic influence request:
    ...       - Target DNN, S-NSSAI
    ...       - Application identifier (traffic filter)
    ...       - Requested DNAI (Data Network Access Identifier)
    ...    2. NEF validates and forwards to PCF
    ...    3. PCF generates PCC rule update for SMF
    ...    4. SMF reconfigures UPF PDR/FAR for local routing
    ...    5. Subsequent UE traffic matching filter routed to edge DNAI
    ...    Verification:
    ...    - AF request accepted (201 or 200)
    ...    - PCF PCC rule updated with routing indication
    ...    - SMF installs new PDR/FAR in UPF
    ...    - Traffic routed to requested DNAI
    ...    Expected Result: AF-influenced routing active for target traffic
    [Tags]    af-influence    nef    pcf    routing    priority-1
    Log    TC-MEC-003: AF-Influenced Traffic Routing

# ===============================================================
# Edge DNS & Application Discovery
# ETSI MEC 011 §7.2.5 — DNS rules
# ETSI MEC 011 §8.2.6 — MEC service discovery (Mp1)
# ===============================================================
TC-MEC-010 Edge DNS Resolution
    [Documentation]    TC-MEC-010: DNS queries resolved to local edge application IP
    ...    Standard: ETSI MEC 011 §7.2.5 (DNS rule management)
    ...    Procedure:
    ...    1. Edge application registered with DNS rule:
    ...       - domainName: "app.edge.local"
    ...       - ipAddress: local edge application IP
    ...       - ttl: 300
    ...    2. UE sends DNS query for "app.edge.local"
    ...    3. Local DNS at edge resolves to edge application IP
    ...    4. UE connects to local edge instance (low latency)
    ...    Verification:
    ...    - DNS query returns edge application IP (not central)
    ...    - TTL matches configured value
    ...    - Latency to resolved IP significantly lower than central
    ...    Expected Result: DNS resolves to local edge application
    [Tags]    dns    resolution    local-routing    priority-1
    Log    TC-MEC-010: Edge DNS Resolution

TC-MEC-011 Edge Application Discovery Via API
    [Documentation]    TC-MEC-011: Discover available edge applications via MEC API
    ...    Standard: ETSI MEC 011 §8.2.6 (Service discovery via Mp1)
    ...    Procedure:
    ...    1. Multiple edge applications registered on MEC platform
    ...    2. GET /api/mec/services with optional filters:
    ...       - ser_category_id (service category)
    ...       - scope_of_locality (MEC_HOST / MEC_SYSTEM)
    ...    3. Verify discovery response lists available services
    ...    4. Each service entry includes: serInstanceId, serName,
    ...       transportInfo, serializer, scopeOfLocality, state
    ...    Verification:
    ...    - All active edge services returned
    ...    - Filter by category returns correct subset
    ...    - Service endpoint (transportInfo) reachable
    ...    Expected Result: Edge services discoverable via API
    [Tags]    api    discovery    service-catalog    priority-2
    Log    TC-MEC-011: Edge Application Discovery
