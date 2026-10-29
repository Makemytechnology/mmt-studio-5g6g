# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Network Trace Test Suite
...              TS 32.421 — Subscriber and equipment trace
...              TS 32.422 — Trace control and configuration management
...              Covers: Trace session management, PCAP capture, trace export
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        trace    pcap    diagnostics

*** Test Cases ***
TC-TRC-001 Trace Session Start For UE
    [Documentation]    TC-TRC-001: Start trace session for specific UE
    ...    Standard: TS 32.422 §4.1 (trace session activation)
    ...    Procedure:
    ...    1. Create trace session targeting UE IMSI via REST API
    ...    2. UE performs registration
    ...    3. Verify trace records captured (NAS messages)
    ...    Expected Result: Trace session active, NAS PDUs recorded
    [Tags]    trace-session    start    priority-1
    Log    TC-TRC-001: Trace Session Start

TC-TRC-002 Trace Session Stop And Export
    [Documentation]    TC-TRC-002: Stop trace session and export PCAP
    ...    Procedure:
    ...    1. Active trace session from TC-TRC-001
    ...    2. Stop trace session via REST API
    ...    3. Export captured data
    ...    Expected Result: Trace data exported
    [Tags]    trace-session    stop    export    priority-1
    Log    TC-TRC-002: Trace Stop and Export

TC-TRC-003 Trace All UEs On Interface
    [Documentation]    TC-TRC-003: Trace all N2 (NGAP) messages
    ...    Standard: TS 32.422 §5.1 (interface-based trace)
    ...    Procedure:
    ...    1. Start N2 interface trace
    ...    2. Multiple UEs register and establish PDU sessions
    ...    3. Verify all NGAP messages captured
    ...    Expected Result: All N2 messages in trace
    [Tags]    interface-trace    n2    priority-2
    Log    TC-TRC-003: N2 Interface Trace

TC-TRC-010 Trace Record Contains Correct Fields
    [Documentation]    TC-TRC-010: Verify trace record structure
    ...    Standard: TS 32.423 §5.1 (trace record format)
    ...    Procedure: Capture trace, verify fields (timestamp, IMSI, message type, direction)
    ...    Expected Result: All mandatory fields present
    [Tags]    trace-record    validation    priority-2
    Log    TC-TRC-010: Trace Record Fields
