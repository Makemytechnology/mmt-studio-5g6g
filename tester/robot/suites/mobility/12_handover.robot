# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    N2 Handover Test Suite
...              Inter-gNB handover via AMF (N2-based, TS 38.413 section 8.4)
...              Source gNB sends HandoverRequired, AMF mediates, target gNB accepts
...              Validates AMF handover signaling, UPF N4 path switch, data continuity
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        handover    n2    multi-gnb    mobility

*** Test Cases ***
# ===============================================================
# Basic N2 Handover Signaling
# ===============================================================
TC-HO-001 Basic N2 Handover
    [Documentation]    TC-HO-001: Basic N2 Handover — gNB-1 to gNB-2
    ...    Standard: TS 38.413 section 8.4.1-8.4.3 (N2 Handover Preparation/Execution)
    ...    Procedure:
    ...    1. gNB-1 (source) connects to AMF, NG Setup
    ...    2. gNB-2 (target) connects to AMF, NG Setup
    ...    3. Register UE via gNB-1, establish PDU session (PSI=1)
    ...    4. gNB-1 sends HandoverRequired to AMF (target=gNB-2)
    ...    5. AMF sends HandoverRequest to gNB-2
    ...    6. gNB-2 responds with HandoverRequestAcknowledge
    ...    7. AMF sends HandoverCommand to gNB-1
    ...    8. gNB-2 sends HandoverNotify to AMF (UE arrived)
    ...    9. AMF updates UPF (N4 path switch), releases source context
    ...    Verification: UE context moved to gNB-2, AMF sends UEContextRelease to gNB-1
    ...    Expected Result: Handover signaling completes successfully
    [Tags]    basic    signaling    priority-1
    Log    TC-HO-001: Basic N2 Handover

TC-HO-002 Handover With Active PDU Session
    [Documentation]    TC-HO-002: N2 Handover with active data session — data continuity
    ...    Standard: TS 38.413 section 8.4, TS 29.281 (GTP-U path switch)
    ...    Procedure:
    ...    1. Register UE on gNB-1, establish PDU (PSI=1, DNN=internet)
    ...    2. Start UDP traffic (1 Mbps bidirectional) through gNB-1
    ...    3. Initiate N2 handover to gNB-2
    ...    4. Verify GTP-U tunnel migrates to gNB-2
    ...    5. Resume/continue traffic through gNB-2
    ...    6. Verify throughput after handover
    ...    Verification: Traffic continues after handover, UPF path switched
    ...    Expected Result: Data throughput maintained post-handover
    [Tags]    data    pdu-session    priority-1
    Log    TC-HO-002: Handover with active PDU

TC-HO-003 Handover During VoNR Call
    [Documentation]    TC-HO-003: N2 Handover during active VoNR call — voice continuity
    ...    Standard: TS 38.413 section 8.4, TS 26.114 (voice continuity)
    ...    Procedure:
    ...    1. Register 2 UEs on gNB-1, establish IMS PDU sessions
    ...    2. Start bidirectional VoNR call (RTP audio)
    ...    3. Handover UE-A from gNB-1 to gNB-2 mid-call
    ...    4. Continue RTP after handover
    ...    5. Measure MOS before and after handover
    ...    Verification: Voice call survives handover, MOS acceptable
    ...    Expected Result: MOS >= 3.0 after handover
    [Tags]    vonr    voice    mid-call    priority-2
    Log    TC-HO-003: VoNR handover

TC-HO-004 Ping-Pong Handover
    [Documentation]    TC-HO-004: Ping-pong handover — gNB-1 to gNB-2 back to gNB-1
    ...    Standard: TS 38.413 section 8.4
    ...    Procedure:
    ...    1. Register UE on gNB-1, establish PDU session
    ...    2. Handover gNB-1 -> gNB-2 (first hop)
    ...    3. Verify UE context on gNB-2
    ...    4. Handover gNB-2 -> gNB-1 (return hop)
    ...    5. Verify UE context back on gNB-1
    ...    Parameters: 2 handover cycles
    ...    Verification: UE context correctly maintained through both hops
    ...    Expected Result: Both handovers succeed, PDU session preserved
    [Tags]    ping-pong    stability    priority-2
    Log    TC-HO-004: Ping-pong handover

TC-HO-005 Multi-UE Handover
    [Documentation]    TC-HO-005: Multiple UEs handover simultaneously
    ...    Standard: TS 38.413 section 8.4
    ...    Procedure:
    ...    1. Register 8 UEs on gNB-1, each with PDU session
    ...    2. Handover all 8 UEs to gNB-2 concurrently
    ...    3. Verify all UE contexts moved to gNB-2
    ...    Parameters: 8 UEs concurrent handover
    ...    Verification: All 8 UEs successfully handed over
    ...    Expected Result: All UEs on gNB-2 with active PDU sessions
    [Tags]    multi-ue    8-ue    concurrent    priority-2
    Log    TC-HO-005: Multi-UE handover

TC-HO-006 Handover With Multi-DNN Sessions
    [Documentation]    TC-HO-006: Handover UE with dual PDU sessions (internet + IMS)
    ...    Standard: TS 38.413 section 8.4, TS 23.501 section 5.6.1
    ...    Procedure:
    ...    1. Register UE on gNB-1 with PSI=1 (internet) + PSI=2 (ims)
    ...    2. Start data traffic on PSI=1 and VoNR on PSI=2
    ...    3. Handover to gNB-2 — both PDU sessions must migrate
    ...    4. Verify both tunnels active on gNB-2
    ...    5. Continue both data and voice traffic
    ...    Verification: Both PDU sessions survive handover
    ...    Expected Result: Data + voice continue on gNB-2
    [Tags]    multi-dnn    dual-pdu    priority-3
    Log    TC-HO-006: Multi-DNN handover

TC-HO-007 Multi-Hop Handover Sequence
    [Documentation]    TC-HO-007: UE ping-pongs N times between two configured gNBs
    ...    Standard: TS 38.413 section 8.4
    ...    Procedure:
    ...    1. Register UE (per sim_db gnb_name) on its source gNB
    ...    2. Establish PDU session (PSI=1, DNN=internet)
    ...    3. Repeat N times (default N=5):
    ...       a. Handover to peer gNB (incl. UplinkRANStatusTransfer + GTP-U End Marker)
    ...       b. Wait hop_gap_s seconds (default 1s)
    ...       c. Swap roles, handover back
    ...    4. Record per-hop latency (min/avg/max ms)
    ...    Parameters: imsi (default first UE), hops (default 5), hop_gap_s (default 1.0)
    ...    Verification: All N handovers succeed; PDU session preserved across hops
    ...    Expected Result: UE ends on the alternating peer; all hops OK
    [Tags]    multi-hop    sequence    mobility    priority-2
    Log    TC-HO-007: Multi-hop handover sequence

TC-HO-008 Handover Failure (Target Reject)
    [Documentation]    TC-HO-008: Target gNB rejects HandoverRequest with HandoverFailure
    ...    Standard: TS 38.413 section 8.4.2.3, section 9.2.3.5 (HandoverFailure),
    ...    section 8.4.1.3, section 9.2.3.4 (HandoverPreparationFailure)
    ...    Procedure:
    ...    1. Register UE on source gNB, establish PDU session
    ...    2. Set target_gnb.force_ho_failure = <cause>
    ...    3. Source sends HandoverRequired
    ...    4. AMF forwards as HandoverRequest to target
    ...    5. Target responds with HandoverFailure(cause)
    ...    6. AMF returns HandoverPreparationFailure to source
    ...    Parameters: imsi (default first UE), cause (default ho-failure-in-target-...)
    ...    Verification: Source observes prep failure cause, UE stays on source
    ...    Expected Result: Handover fails cleanly, no UE migration
    [Tags]    failure    target-reject    negative    priority-2
    Log    TC-HO-008: Handover failure (target rejects)

TC-HO-009 Handover Cancel
    [Documentation]    TC-HO-009: Source gNB cancels handover mid-preparation
    ...    Standard: TS 38.413 section 8.4.5 (HandoverCancel/Acknowledge)
    ...    Procedure:
    ...    1. Register UE on source gNB, establish PDU session
    ...    2. Source sends HandoverRequired
    ...    3. Before HandoverCommand arrives, source sends HandoverCancel
    ...    4. AMF responds with HandoverCancelAcknowledge
    ...    Parameters: imsi (default first UE), cancel_cause (default handover-cancelled)
    ...    Verification: HandoverCancelAck received, UE remains on source
    ...    Expected Result: Cancellation acknowledged, no UE migration
    [Tags]    cancel    abort    negative    priority-3
    Log    TC-HO-009: Handover cancel
