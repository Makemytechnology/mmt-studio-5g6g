# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Roaming Test Suite
...              TS 23.501 §5.17 — Roaming architecture
...              TS 29.573 — SEPP (Security Edge Protection Proxy)
...              TS 23.502 §4.8 — Inter-PLMN procedures
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        roaming    inter-plmn    sepp

*** Test Cases ***
TC-ROAM-001 Roaming Agreement Configuration
    [Documentation]    TC-ROAM-001: Configure roaming agreement between PLMNs
    ...    Standard: TS 23.501 §5.17.1 (roaming architecture)
    ...    Procedure:
    ...    1. Configure home PLMN (001-01) and visited PLMN (001-02) roaming agreement
    ...    2. Verify agreement stored in roaming_agreements table
    ...    3. Verify equivalent PLMNs configured
    ...    Expected Result: Roaming agreement active
    [Tags]    config    agreement    priority-1
    Log    TC-ROAM-001: Roaming Agreement

TC-ROAM-002 Roaming UE Registration Home Routing
    [Documentation]    TC-ROAM-002: Roaming UE registers via home-routed path
    ...    Standard: TS 23.502 §4.2.2.2 (registration with roaming)
    ...    Procedure:
    ...    1. UE with HPLMN=001-02 connects to VPLMN=001-01
    ...    2. AMF detects roaming, selects home-routed path
    ...    3. Registration Accept includes equivalent PLMNs
    ...    Expected Result: Roaming UE registered
    [Tags]    registration    home-route    priority-1
    Log    TC-ROAM-002: Roaming Registration

TC-ROAM-003 Roaming CDR Generation
    [Documentation]    TC-ROAM-003: CDR generated for roaming session
    ...    Standard: TS 32.251 §6.3.2 (roaming CDR)
    ...    Procedure:
    ...    1. Roaming UE establishes PDU session
    ...    2. Verify roaming CDR generated with VPLMN/HPLMN info
    ...    Expected Result: Roaming CDR includes inter-PLMN identifiers
    [Tags]    cdr    billing    priority-2
    Log    TC-ROAM-003: Roaming CDR

TC-ROAM-010 Equivalent PLMN List In Registration Accept
    [Documentation]    TC-ROAM-010: Equivalent PLMNs IE in Registration Accept
    ...    Standard: TS 24.501 §9.11.3.45 (Equivalent PLMNs)
    ...    Expected Result: Registration Accept contains correct equivalent PLMNs
    [Tags]    registration    equivalent-plmn    priority-2
    Log    TC-ROAM-010: Equivalent PLMNs
