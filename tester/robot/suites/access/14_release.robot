# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    UE Context Release / RLF / Inactivity Test Suite
...              Tests gNB-initiated UE context release scenarios
...              TS 38.413 section 8.3 (UE Context Management)
...              TS 38.413 section 8.1.3 (Error Indication)
...              TS 38.413 section 8.7.4 (RRC Inactive Transition Report)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        release    rlf    context    failure

*** Test Cases ***
# ===============================================================
# Radio Link Failure (RLF)
# ===============================================================
TC-REL-001 RLF UE Context Release
    [Documentation]    TC-REL-001: Radio Link Failure — gNB releases UE context
    ...    Standard: TS 38.413 section 8.3.2 (UE Context Release Request)
    ...    Cause: radioNetwork / radio-connection-with-ue-lost (RLF)
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. gNB sends UEContextReleaseRequest (cause=RLF) to AMF
    ...    3. AMF responds with UEContextReleaseCommand
    ...    4. gNB sends UEContextReleaseComplete
    ...    5. Verify UE context cleared, GTP-U tunnel destroyed
    ...    Expected Result: AMF releases UE, resources cleaned up
    [Tags]    rlf    radio-link-failure    priority-1
    Log    TC-REL-001: RLF release

TC-REL-002 User Inactivity Release
    [Documentation]    TC-REL-002: UE released due to user inactivity
    ...    Standard: TS 38.413 section 8.3.2
    ...    Cause: radioNetwork / user-inactivity
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. Wait (simulated inactivity period)
    ...    3. gNB sends UEContextReleaseRequest (cause=user-inactivity)
    ...    4. AMF responds with UEContextReleaseCommand
    ...    5. Verify clean release
    ...    Expected Result: AMF accepts inactivity release
    [Tags]    inactivity    idle    priority-1
    Log    TC-REL-002: Inactivity release

TC-REL-003 AN Release — Radio Resources Not Available
    [Documentation]    TC-REL-003: gNB releases UE — no radio resources
    ...    Standard: TS 38.413 section 8.3.2
    ...    Cause: radioNetwork / radio-resources-not-available
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. gNB sends UEContextReleaseRequest (cause=radio-resources-not-available)
    ...    3. Verify AMF releases and cleans up
    ...    Expected Result: Clean release, UE can re-register
    [Tags]    an-release    resources    priority-2
    Log    TC-REL-003: AN release (no resources)

TC-REL-004 Release Due to NGRAN Generated Reason
    [Documentation]    TC-REL-004: gNB-initiated release for NGRAN internal reason
    ...    Standard: TS 38.413 section 8.3.2
    ...    Cause: radioNetwork / release-due-to-ngran-generated-reason
    ...    Expected Result: AMF accepts release
    [Tags]    ngran-release    priority-2
    Log    TC-REL-004: NGRAN generated release

# ===============================================================
# RRC Inactive Transition
# ===============================================================
TC-REL-005 RRC Connected To Inactive Transition
    [Documentation]    TC-REL-005: UE transitions from RRC Connected to Inactive
    ...    Standard: TS 38.413 section 8.7.4 (RRC Inactive Transition Report)
    ...    Procedure:
    ...    1. Register UE, establish PDU session (RRC Connected)
    ...    2. gNB sends RRCInactiveTransitionReport (state=inactive)
    ...    3. Verify AMF accepts transition
    ...    4. gNB sends RRCInactiveTransitionReport (state=connected)
    ...    5. Verify UE back to connected
    ...    Expected Result: AMF handles RRC state transitions
    [Tags]    rrc-inactive    state-transition    priority-1
    Log    TC-REL-005: RRC inactive transition

TC-REL-006 RRC Inactive Then Release
    [Documentation]    TC-REL-006: UE goes RRC Inactive then released
    ...    Standard: TS 38.413 section 8.7.4 + 8.3.2
    ...    Cause: radioNetwork / ue-in-rrc-inactive-state-not-reachable
    ...    Procedure:
    ...    1. Register UE, PDU session
    ...    2. RRC Inactive transition report
    ...    3. UEContextReleaseRequest (cause=ue-in-rrc-inactive-state-not-reachable)
    ...    4. Verify release
    ...    Expected Result: UE released from inactive state
    [Tags]    rrc-inactive    release    priority-2
    Log    TC-REL-006: Inactive then release

# ===============================================================
# Error Indication
# ===============================================================
TC-REL-007 Error Indication With UE Context
    [Documentation]    TC-REL-007: gNB sends ErrorIndication for specific UE
    ...    Standard: TS 38.413 section 8.1.3 (Error Indication)
    ...    Cause: radioNetwork / failure-in-radio-interface-procedure
    ...    Procedure:
    ...    1. Register UE
    ...    2. gNB sends ErrorIndication with AMF-UE-NGAP-ID + RAN-UE-NGAP-ID
    ...    3. Verify AMF handles error (may release UE)
    ...    Expected Result: AMF processes error indication
    [Tags]    error    error-indication    priority-2
    Log    TC-REL-007: Error indication

TC-REL-008 Error Indication Without UE Context
    [Documentation]    TC-REL-008: gNB sends ErrorIndication — no UE association
    ...    Standard: TS 38.413 section 8.1.3
    ...    Cause: radioNetwork / unspecified
    ...    Procedure:
    ...    1. gNB connected to AMF (NG Setup done)
    ...    2. gNB sends ErrorIndication without UE IDs
    ...    3. Verify AMF handles gracefully
    ...    Expected Result: AMF logs error, no crash
    [Tags]    error    no-context    priority-3
    Log    TC-REL-008: Error indication (no UE)

# ===============================================================
# Release + Re-register
# ===============================================================
TC-REL-009 RLF Then Re-Registration
    [Documentation]    TC-REL-009: UE experiences RLF, then re-registers
    ...    Standard: TS 38.413 section 8.3.2 + TS 24.501 section 5.5.1.2
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. gNB triggers RLF release
    ...    3. AMF releases context
    ...    4. UE re-registers (new Initial UE Message)
    ...    5. Establish new PDU session
    ...    6. Verify traffic works on new session
    ...    Expected Result: UE recovers from RLF and resumes service
    [Tags]    rlf    re-register    recovery    priority-1
    Log    TC-REL-009: RLF + re-register

TC-REL-010 Multi-UE RLF Release
    [Documentation]    TC-REL-010: Multiple UEs experience RLF simultaneously
    ...    Procedure:
    ...    1. Register 8 UEs with PDU sessions
    ...    2. gNB sends RLF release for all 8 concurrently
    ...    3. Verify AMF releases all contexts
    ...    4. Verify all GTP-U tunnels destroyed
    ...    Parameters: 8 UEs concurrent RLF
    ...    Expected Result: All 8 UEs cleanly released
    [Tags]    multi-ue    rlf    concurrent    priority-2
    Log    TC-REL-010: Multi-UE RLF

# ===============================================================
# Inactivity Release + Traffic Resume
# ===============================================================
TC-REL-011 Inactivity Release Then UL Traffic
    [Documentation]    TC-REL-011: UE inactivity release → re-register → UL traffic
    ...    Standard: TS 38.413 section 8.3.2, TS 24.501 section 5.5.1.2
    ...    Cause: radioNetwork / user-inactivity
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. gNB triggers inactivity release (UEContextReleaseRequest)
    ...    3. AMF releases UE context
    ...    4. UE re-registers, new PDU session
    ...    5. Run 30s UDP UL traffic (1Mbps)
    ...    6. Measure throughput, jitter, loss
    ...    Verification: UE recovers and UL traffic works after inactivity release
    ...    Expected Result: Full UL throughput restored
    [Tags]    inactivity    ul-traffic    re-register    priority-1
    Log    TC-REL-011: Inactivity release + UL traffic

TC-REL-012 Inactivity Release Then DL Traffic
    [Documentation]    TC-REL-012: UE inactivity release → re-register → DL traffic
    ...    Standard: TS 38.413 section 8.3.2, TS 24.501 section 5.5.1.2
    ...    Cause: radioNetwork / user-inactivity
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. gNB triggers inactivity release
    ...    3. AMF releases UE context
    ...    4. UE re-registers, new PDU session
    ...    5. Core iperf3 client sends 30s UDP DL traffic (1Mbps) to UE
    ...    6. Measure throughput
    ...    Verification: UE recovers and DL traffic works after inactivity release
    ...    Expected Result: Full DL throughput restored
    [Tags]    inactivity    dl-traffic    re-register    priority-1
    Log    TC-REL-012: Inactivity release + DL traffic
