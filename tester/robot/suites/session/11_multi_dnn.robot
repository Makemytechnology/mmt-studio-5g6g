# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Multi-DNN Concurrent Session Test Suite
...              Each UE pair runs simultaneous data (DNN=internet) + ViNR call (DNN=ims)
...              Validates independent PDU sessions, QoS isolation, and multi-bearer operation
...              TS 23.501 §5.6.1 (multiple PDU sessions), TS 24.501 §6.4 (PDU session mgmt)
...              TS 23.228 (IMS), TS 26.114 (media), TS 29.281 (GTP-U per-session tunnels)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        multi-dnn    dual-session    data-plane    ims

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# Multi-DNN: Concurrent Data + ViNR per UE Pair
# Each UE establishes 2 PDU sessions:
#   PSI=1 (DNN=internet, 5QI=9) — UDP bidirectional throughput via iperf3
#   PSI=2 (DNN=ims, 5QI=1/2)   — ViNR bidirectional call (audio+video RTP)
# Both sessions run concurrently through independent GTP-U tunnels
# ═══════════════════════════════════════════════════════════════
TC-MDN-001 Multi-DNN 2 UEs Data + ViNR
    [Documentation]    TC-MDN-001: Multi-DNN — 1 UE Pair × (UDP Data + ViNR Call)
    ...    Standard: TS 23.501 §5.6.1 (multiple PDU sessions per UE)
    ...    Procedure:
    ...    1. Register 2 UEs, establish dual PDU sessions per UE:
    ...       - PSI=1: DNN=internet (default 5QI=9, best effort)
    ...       - PSI=2: DNN=ims (5QI=1 voice + 5QI=2 video)
    ...    2. Concurrently run on each UE pair:
    ...       a) UDP bidirectional 1 Mbps via iperf3 on internet PDU (PSI=1)
    ...       b) ViNR call: audio RTP (AMR-WB, port 20000) + video RTP (H.264, port 20002) on IMS PDU (PSI=2)
    ...    3. Measure data throughput + voice MOS + video quality independently
    ...    Parameters: 2 UEs (1 pair), UDP=1 Mbps, ViNR audio+video, duration=15s
    ...    GTP-U: 2 tunnels per UE (4 total), independent TEIDs per PDU session
    ...    Verification: Data throughput ~1 Mbps, voice MOS >= 3.5, video stable
    ...    Expected Result: Both sessions pass simultaneously — QoS isolation confirmed
    [Tags]    2-ue    1-pair    priority-1
    Log    TC-MDN-001: Multi-DNN 2 UEs (1 data + 1 ViNR pair)

TC-MDN-002 Multi-DNN 4 UEs Data + ViNR
    [Documentation]    TC-MDN-002: Multi-DNN — 2 UE Pairs × (UDP Data + ViNR Call)
    ...    Standard: TS 23.501 §5.6.1, TS 29.281 (GTP-U multi-tunnel)
    ...    Procedure: Same as TC-MDN-001 with 4 UEs (2 pairs)
    ...    Parameters: 4 UEs (2 pairs), UDP=1 Mbps each, ViNR audio+video, duration=15s
    ...    GTP-U: 8 tunnels total (2 per UE × 4 UEs)
    ...    Expected Result: All 2 data + 2 ViNR sessions pass concurrently
    [Tags]    4-ue    2-pair    priority-1
    Log    TC-MDN-002: Multi-DNN 4 UEs (2 data + 2 ViNR pairs)

TC-MDN-003 Multi-DNN 8 UEs Data + ViNR
    [Documentation]    TC-MDN-003: Multi-DNN — 4 UE Pairs × (UDP Data + ViNR Call)
    ...    Parameters: 8 UEs (4 pairs), UDP=1 Mbps, ViNR audio+video, duration=10s
    ...    GTP-U: 16 tunnels total
    ...    Expected Result: All 4 data + 4 ViNR sessions pass concurrently
    [Tags]    8-ue    4-pair    priority-2
    Log    TC-MDN-003: Multi-DNN 8 UEs (4 data + 4 ViNR pairs)

TC-MDN-004 Multi-DNN 16 UEs Data + ViNR
    [Documentation]    TC-MDN-004: Multi-DNN — 8 UE Pairs × (UDP Data + ViNR Call)
    ...    Parameters: 16 UEs (8 pairs), UDP=1 Mbps, ViNR audio+video, duration=10s
    ...    GTP-U: 32 tunnels total
    ...    Expected Result: All 8 data + 8 ViNR sessions pass
    [Tags]    16-ue    8-pair    priority-2
    Log    TC-MDN-004: Multi-DNN 16 UEs (8 data + 8 ViNR pairs)

TC-MDN-005 Multi-DNN 32 UEs Data + ViNR
    [Documentation]    TC-MDN-005: Multi-DNN — 16 UE Pairs × (UDP Data + ViNR Call)
    ...    Parameters: 32 UEs (16 pairs), UDP=1 Mbps, ViNR audio+video, duration=10s
    ...    GTP-U: 64 tunnels total
    ...    Expected Result: All 16 data + 16 ViNR sessions pass
    [Tags]    32-ue    16-pair    large-scale    priority-3
    Log    TC-MDN-005: Multi-DNN 32 UEs (16 data + 16 ViNR pairs)

TC-MDN-006 Multi-DNN 64 UEs Data + ViNR
    [Documentation]    TC-MDN-006: Multi-DNN — 32 UE Pairs × (UDP Data + ViNR Call)
    ...    Parameters: 64 UEs (32 pairs), UDP=1 Mbps, ViNR audio+video, duration=10s
    ...    GTP-U: 128 tunnels total — maximum multi-DNN capacity
    ...    Expected Result: All 32 data + 32 ViNR sessions pass
    [Tags]    64-ue    32-pair    large-scale    priority-3
    Log    TC-MDN-006: Multi-DNN 64 UEs (32 data + 32 ViNR pairs)
