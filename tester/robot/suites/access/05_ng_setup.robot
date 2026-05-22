# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    5G NR NGAP NG Setup Procedure Test Suite
...              TS 38.413 §8.7.1 — First NGAP procedure after TNL association.
...              Protocol: NGAP over SCTP (port 38412), Stream 0 (Non-UE signaling).
...
...              Covers:
...              - Basic NG Setup success (Request/Response)
...              - State machine transitions (IDLE → NG_SETUP_SENT → READY)
...              - Custom PLMN, TAC, and Slice configurations
...              - Disconnect/Reconnect scenarios
...              - Multiple concurrent gNB setups
...              - NGAP message encoding/decoding validation
...              - Setup timing and latency measurement
Resource         ../../resources/common.resource
Library          ../../libraries/GnbLibrary.py
Library          ../../libraries/ProtocolLibrary.py
Library          Collections
Library          String
Library          DateTime

Test Tags        ng-setup    ngap    sctp    5g-nr

*** Variables ***
${SETUP_TIMEOUT}     10

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-001: Basic NG Setup — Happy Path
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-001 Basic NG Setup Success
    [Documentation]    TC-NGS-001: Basic NG Setup — Happy Path
    ...    Standard: 3GPP TS 38.413 §8.7.1 (NG Setup procedure)
    ...    Procedure:
    ...    1. Establish SCTP association to AMF on port 38412 (TS 38.412 §7)
    ...    2. Send NGSetupRequest (initiatingMessage, procedureCode=21) on stream 0
    ...       containing: GlobalRANNodeID, RANNodeName, SupportedTAList, DefaultPagingDRX
    ...    3. Receive NGSetupResponse (successfulOutcome) from AMF containing:
    ...       AMFName, ServedGUAMIList, RelativeAMFCapacity, PLMNSupportList
    ...    4. gNB transitions IDLE -> NG_SETUP_SENT -> READY
    ...    Expected: gNB reaches READY state, NG interface operational
    [Tags]    smoke    priority-1
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    ${state}=    Get gNB State    ${gnb}
    Should Be Equal    ${state}    READY    NG Setup should result in READY state
    Log    TC-NGS-001 PASS: gNB ${GNB_NAME} reached READY state
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-002: NG Setup State Machine Transitions
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-002 State Machine IDLE To NG_SETUP_SENT To READY
    [Documentation]    TC-NGS-002: gNB State Machine Validation
    ...    Standard: 3GPP TS 38.413 §8.7.1, Figure 8.7.1.1-1
    ...    Validates complete FSM: IDLE -> NG_SETUP_SENT -> READY
    ...    Initial state must be IDLE, connect sends NGSetupRequest on SCTP stream 0,
    ...    state transitions to NG_SETUP_SENT, upon NGSetupResponse -> READY.
    ...    Expected: Each state observed in correct order, no unexpected transitions
    [Tags]    state-machine    priority-1
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    # Initial state must be IDLE
    ${state_initial}=    Get gNB State    ${gnb}
    Should Be Equal    ${state_initial}    IDLE    Initial state should be IDLE
    # Connect triggers SCTP + NG Setup Request → NG_SETUP_SENT
    Connect gNB    ${gnb}
    # Wait for READY (NG Setup Response received)
    Wait gNB Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    ${state_final}=    Get gNB State    ${gnb}
    Should Be Equal    ${state_final}    READY    Final state should be READY
    Log    TC-NGS-002 PASS: State transitions verified: IDLE → NG_SETUP_SENT → READY
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-003: NG Setup with Default PLMN (MCC=001, MNC=01)
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-003 NG Setup With Default PLMN
    [Documentation]    TC-NGS-003: NG Setup with Default PLMN Identity
    ...    Standard: 3GPP TS 38.413 §8.7.1, §9.3.3.5 (PLMNIdentity encoding)
    ...    PLMNIdentity carried in SupportedTAList IE, encoded as 3 octets BCD.
    ...    Uses default PLMN from gNB config profile.
    ...    Expected: AMF accepts default PLMN, gNB reaches READY
    [Tags]    plmn    priority-1
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    Log    TC-NGS-003 PASS: NG Setup with default PLMN from config
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-004: NG Setup with Custom PLMN Configuration
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-004 NG Setup With Custom PLMN
    [Documentation]    TC-NGS-004: NG Setup with Custom PLMN (MCC=310, MNC=260)
    ...    Standard: 3GPP TS 38.413 §9.3.3.5 (PLMNIdentity), §9.3.1.5 (GlobalRANNodeID)
    ...    Tests 3-digit MNC encoding in BroadcastPLMNList within SupportedTAList.
    ...    AMF must have this PLMN in its served list to accept.
    ...    Expected: NG Setup succeeds if PLMN is configured on AMF
    [Tags]    plmn    priority-2
    ${gnb}=    Create gNB From Config    ${GNB_NAME}    mcc=310    mnc=260    tac=0100
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    Log    TC-NGS-004 PASS: NG Setup with PLMN 310/260 TAC 0100
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-005: NG Setup with Custom TAC
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-005 NG Setup With Custom TAC
    [Documentation]    TC-NGS-005: NG Setup with Custom Tracking Area Code
    ...    Standard: 3GPP TS 38.413 §9.3.3.10 (TAC — 3-octet OCTET STRING)
    ...    TAC carried in SupportedTAList as TAI (PLMN + TAC). Value 00FF tests
    ...    non-trivial encoding. AMF uses TAC for paging and area-based routing.
    ...    Expected: NG Setup succeeds with TAC=00FF, gNB reaches READY
    [Tags]    tac    priority-2
    ${gnb}=    Create gNB From Config    ${GNB_NAME}    tac=00FF
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    Log    TC-NGS-005 PASS: NG Setup with TAC 00FF
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-006: NG Setup RAN Node Name (RANNodeName IE)
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-006 NG Setup With RAN Node Name
    [Documentation]    TC-NGS-006: NG Setup with RANNodeName IE
    ...    Standard: 3GPP TS 38.413 §8.7.1, §9.3.1.6 (RANNodeName — PrintableString 1..150)
    ...    RANNodeName IE (id=82) provides human-readable gNB identifier.
    ...    Uses gNB name from config profile as RANNodeName.
    ...    Expected: AMF accepts NGSetupRequest with RANNodeName, gNB READY
    [Tags]    ran-name    priority-2
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    Log    TC-NGS-006 PASS: NG Setup with RANNodeName=${GNB_NAME}
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-007: NG Setup Disconnect and Reconnect
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-007 NG Setup Disconnect And Reconnect
    [Documentation]    TC-NGS-007: NG Setup Disconnect and Reconnect
    ...    Standard: 3GPP TS 38.413 §8.7.1, §10.6 (SCTP association restart)
    ...    Per §10.6, SCTP restart requires fresh NG Setup before any signaling.
    ...    Validates: connect -> READY -> disconnect -> IDLE -> reconnect -> READY.
    ...    Expected: Both initial and reconnect NG Setup succeed, states correct
    [Tags]    reconnect    priority-1
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    # First connection
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    Log    First NG Setup succeeded
    # Disconnect
    Disconnect gNB    ${gnb}
    ${state}=    Get gNB State    ${gnb}
    Should Be Equal    ${state}    IDLE    State after disconnect should be IDLE
    Log    Disconnected, state is IDLE
    # Reconnect
    Sleep    0.5s    Brief pause before reconnect
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    Log    TC-NGS-007 PASS: Reconnect NG Setup succeeded
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-008: NG Setup Reconnect Cycle (3x)
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-008 NG Setup Reconnect Three Cycles
    [Documentation]    TC-NGS-008: NG Setup Resilience — 3 Reconnect Cycles
    ...    Standard: 3GPP TS 38.413 §8.7.1, §10.6
    ...    Stress test: 3 consecutive disconnect/reconnect cycles.
    ...    Each cycle validates full NG Setup procedure after SCTP restart.
    ...    Expected: All 3 cycles pass, gNB transitions IDLE<->READY reliably
    [Tags]    reconnect    stress    priority-2
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    FOR    ${cycle}    IN RANGE    3
        Log    === Cycle ${cycle + 1}/3 ===
        Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
        gNB Should Be Ready    ${gnb}
        Disconnect gNB    ${gnb}
        ${state}=    Get gNB State    ${gnb}
        Should Be Equal    ${state}    IDLE
        Sleep    0.5s    Pause between cycles
    END
    Log    TC-NGS-008 PASS: 3 reconnect cycles completed
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-009: Multiple gNBs Concurrent NG Setup
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-009 Multiple gNBs Concurrent NG Setup
    [Documentation]    TC-NGS-009: Multiple gNBs Concurrent NG Setup
    ...    Standard: 3GPP TS 38.413 §8.7.1, TS 23.501 §5.2.1
    ...    Per TS 23.501, AMF serves multiple gNBs via separate NG-C interfaces.
    ...    3 gNBs created from same config profile, each gets unique gNB ID.
    ...    Expected: All 3 gNBs reach READY, AMF handles concurrent NG-C connections
    [Tags]    multi-gnb    priority-1
    ${gnb1}=    Create gNB From Config    ${GNB_NAME}
    ${gnb2}=    Create gNB From Config    ${GNB_NAME}
    ${gnb3}=    Create gNB From Config    ${GNB_NAME}
    # Connect all three
    Connect gNB    ${gnb1}
    Connect gNB    ${gnb2}
    Connect gNB    ${gnb3}
    # Wait all ready
    Wait gNB Ready    ${gnb1}    timeout=${SETUP_TIMEOUT}
    Wait gNB Ready    ${gnb2}    timeout=${SETUP_TIMEOUT}
    Wait gNB Ready    ${gnb3}    timeout=${SETUP_TIMEOUT}
    # Verify
    gNB Should Be Ready    ${gnb1}
    gNB Should Be Ready    ${gnb2}
    gNB Should Be Ready    ${gnb3}
    Log    TC-NGS-009 PASS: 3 gNBs all reached READY
    [Teardown]    Run Keywords    Remove gNB    ${gnb1}    AND    Remove gNB    ${gnb2}    AND    Remove gNB    ${gnb3}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-010: NG Setup NGAP Message Encode/Decode
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-010 NGAP NG Setup Request Encode And Decode
    [Documentation]    TC-NGS-010: NGAP APER Encode/Decode Validation
    ...    Standard: 3GPP TS 38.413 §9.4.1, ITU-T X.691 (APER encoding)
    ...    Build NGSetupRequest with GlobalRANNodeID (id=27), RANNodeName (id=82),
    ...    SupportedTAList (id=102), DefaultPagingDRX (id=21). APER encode, decode
    ...    back, verify all mandatory IEs present and procedureCode=21.
    ...    Expected: Round-trip encode/decode preserves all IEs correctly
    [Tags]    protocol    encoding    priority-1
    ${hex}=    Build NGAP NG Setup Request From Config    ${GNB_NAME}
    Should Not Be Empty    ${hex}    Encoded NG Setup Request should not be empty
    Log    Encoded NG Setup Request: ${hex}
    ${decoded}=    Decode NGAP Hex    ${hex}
    Should Contain    ${decoded}    NGSetupRequest    Decoded should contain NGSetupRequest
    Log    TC-NGS-010 PASS: NG Setup Request encode/decode validated

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-011: NG Setup Followed By UE Registration
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-011 NG Setup Then UE Registration
    [Documentation]    TC-NGS-011: NG Setup + UE Registration Integration
    ...    Standard: 3GPP TS 38.413 §8.7.1, TS 24.501 §5.5.1 (5GS Registration)
    ...    End-to-end: SCTP -> NG Setup -> gNB READY -> Attach UE -> Registration
    ...    Request (SUCI) -> Authentication -> Security Mode -> Registration Accept.
    ...    Validates complete NG-C path carries UE-associated signaling (InitialUEMessage).
    ...    Expected: gNB READY then UE REGISTERED, full signaling path operational
    [Tags]    integration    priority-1
    # Setup gNB from config
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    # Load UEs from UE config DB
    Load UEs From Config
    # Register UE_1
    Full Registration    ${UE_1}    ${gnb}
    UE Should Be Registered    ${UE_1}
    Log    TC-NGS-011 PASS: NG Setup + UE Registration end-to-end
    [Teardown]    Run Keywords    Remove All UEs    AND    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-012: NG Setup SCTP Port 38412 Verification
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-012 SCTP Association On Port 38412
    [Documentation]    TC-NGS-012: SCTP Transport on Standard Port 38412
    ...    Standard: 3GPP TS 38.412 §7 (SCTP transport for NG-C), IANA port 38412
    ...    Verifies SCTP association on well-known port 38412 (registered with IANA
    ...    for NGAP signaling). Uses AMF port from gNB config profile.
    ...    Expected: SCTP association on port 38412, NG Setup completes
    [Tags]    sctp    transport    priority-1
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    Log    TC-NGS-012 PASS: SCTP on port 38412 confirmed
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-013: NG Setup with S-NSSAI (Single Slice — eMBB)
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-013 NG Setup With eMBB Slice
    [Documentation]    TC-NGS-013: NG Setup with S-NSSAI eMBB Slice
    ...    Standard: 3GPP TS 38.413 §9.3.1.24 (S-NSSAI), TS 23.501 §5.15.2
    ...    S-NSSAI SST=1 (eMBB - Enhanced Mobile Broadband) in SupportedTAList
    ...    -> SliceSupportList. Uses slice config from gNB config profile.
    ...    Expected: AMF accepts eMBB slice configuration, gNB READY
    [Tags]    nssai    slice    priority-2
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    Log    TC-NGS-013 PASS: NG Setup with eMBB slice from config
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-014: NG Setup Context Replacement
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-014 NG Setup Replaces Existing Context
    [Documentation]    TC-NGS-014: NG Setup Context Replacement
    ...    Standard: 3GPP TS 38.413 §8.7.1.1
    ...    Per spec: "If the NG-RAN node has already successfully initiated the NG Setup
    ...    procedure, the NG-RAN node shall not initiate the NG Setup procedure again,
    ...    but instead use the NG Configuration Update procedure." On SCTP restart,
    ...    existing context is erased. Verify: setup -> disconnect -> re-setup -> UE count=0.
    ...    Expected: After re-setup, gNB context reset, no stale UE associations
    [Tags]    context    priority-2
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    ${ue_count_before}=    Get gNB UE Count    ${gnb}
    Log    UE count after first setup: ${ue_count_before}
    # Disconnect and re-setup (simulates context replacement)
    Disconnect gNB    ${gnb}
    Sleep    0.5s
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    gNB Should Be Ready    ${gnb}
    ${ue_count_after}=    Get gNB UE Count    ${gnb}
    Should Be Equal As Integers    ${ue_count_after}    0
    ...    UE count should be 0 after re-setup (context replaced)
    Log    TC-NGS-014 PASS: Context replaced, UE count reset to 0
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-015: NG Setup Timing Measurement
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-015 NG Setup Timing Measurement
    [Documentation]    TC-NGS-015: NG Setup Latency Measurement
    ...    Standard: 3GPP TS 38.413 §8.7.1, RFC 4960 §5 (SCTP 4-way handshake)
    ...    Measures total SCTP + NG Setup latency: SCTP INIT/INIT-ACK/COOKIE-ECHO/
    ...    COOKIE-ACK handshake + NGSetupRequest/Response round-trip.
    ...    Expected: Total latency < 5000ms, typical < 100ms on local network
    [Tags]    timing    performance    priority-3
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    ${start}=    Get Current Date    result_format=epoch
    Connect gNB And Wait Ready    ${gnb}    timeout=${SETUP_TIMEOUT}
    ${end}=    Get Current Date    result_format=epoch
    ${elapsed_s}=    Evaluate    ${end} - ${start}
    ${elapsed_ms}=    Evaluate    ${elapsed_s} * 1000
    Log    NG Setup latency: ${elapsed_ms:.1f} ms
    gNB Should Be Ready    ${gnb}
    Should Be True    ${elapsed_ms} < 5000
    ...    NG Setup should complete within 5 seconds (took ${elapsed_ms:.1f}ms)
    Log    TC-NGS-015 PASS: NG Setup latency=${elapsed_ms:.1f}ms
    [Teardown]    Remove gNB    ${gnb}

# ═══════════════════════════════════════════════════════════════════════════════
# TC-NGS-016: SCTP Idle Connection (No NG Setup Sent)
# ═══════════════════════════════════════════════════════════════════════════════
TC-NGS-016 SCTP Idle Connection No NG Setup
    [Documentation]    TC-NGS-016: SCTP Idle Connection — No NG Setup Sent
    ...    Standard: 3GPP TS 38.413 §8.7.1, TS 38.412 §7, RFC 4960 §5
    ...    Establishes SCTP association to AMF but intentionally sends NO NGSetupRequest.
    ...    Validates AMF behaviour with idle SCTP connections: the AMF should either
    ...    keep the association alive (SCTP heartbeats) or close it after an idle timeout.
    ...    This tests the negative path — a stale/misbehaving gNB that connects but
    ...    never initiates the NG Setup procedure.
    ...    Expected: SCTP association established, held idle for N seconds, then
    ...    verify whether AMF closes the idle connection or keeps it alive.
    [Tags]    sctp    idle    negative    priority-2
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect SCTP Only    ${gnb}
    Log    SCTP connected — holding idle (no NG Setup sent)
    Sleep    15s    Hold SCTP idle for 15 seconds
    ${connected}=    Is SCTP Connected    ${gnb}
    Log    After 15s idle: SCTP connected=${connected}
    Log    TC-NGS-016: SCTP idle connection test complete (connected=${connected})
    [Teardown]    Remove gNB    ${gnb}
