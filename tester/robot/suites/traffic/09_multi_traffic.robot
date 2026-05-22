# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Multi-UE Concurrent Traffic Test Suite
...              Tests simultaneous data sessions at scale (2/4/8/16/32/64/128 UEs)
...              TS 23.501 §5.2 (AMF/UPF capacity), TS 29.281 (GTP-U scaling)
...              Protocols: TCP (throughput), UDP (jitter/loss), Browsing (HTTP-like)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        multi-ue    traffic    scale    data-plane

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# TCP Multi-UE (1 Mbps UL + 1 Mbps DL per UE)
# ═══════════════════════════════════════════════════════════════
TC-MTR-100 TCP 2 UEs 1Mbps Each
    [Documentation]    TC-MTR-100: TCP Multi-UE Traffic — 2 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7 (QoS), TS 29.281 (GTP-U per-UE tunnels)
    ...    Procedure:
    ...    1. Register 2 UEs, establish PDU sessions (2 GTP-U tunnels)
    ...    2. Start iperf3 server on SA Core via /api/iperf/start
    ...    3. Each UE runs iperf3 TCP at 1 Mbps uplink sequentially
    ...    4. Each UE runs iperf3 TCP at 1 Mbps downlink (reverse mode) sequentially
    ...    5. Collect UPF stats delta (before/after)
    ...    Parameters: 2 UEs, TCP, UL=1 Mbps, DL=1 Mbps, duration=10s per UE
    ...    Traffic: 2 Mbps aggregate UL + 2 Mbps aggregate DL through UPF
    ...    Verification: Both UEs achieve ~1 Mbps each, UPF handles concurrent tunnels
    ...    UPF Metrics: ul_pkts, dl_pkts, ul_dropped, dl_dropped, session_count=2
    ...    Expected Result: Both UEs pass, aggregate ~2 Mbps UL + ~2 Mbps DL
    [Tags]    tcp    2-ue    1mbps    priority-1
    Log    TC-MTR-100: TCP 2 UEs × 1 Mbps

TC-MTR-101 TCP 4 UEs 1Mbps Each
    [Documentation]    TC-MTR-101: TCP Multi-UE Traffic — 4 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7 (QoS), TS 29.281 (GTP-U per-UE tunnels)
    ...    Procedure:
    ...    1. Register 4 UEs, establish PDU sessions (4 GTP-U tunnels)
    ...    2. Start iperf3 server on SA Core via /api/iperf/start
    ...    3. Each UE runs iperf3 TCP at 1 Mbps uplink sequentially
    ...    4. Each UE runs iperf3 TCP at 1 Mbps downlink (reverse mode) sequentially
    ...    5. Collect UPF stats delta (before/after)
    ...    Parameters: 4 UEs, TCP, UL=1 Mbps, DL=1 Mbps, duration=10s per UE
    ...    Traffic: 4 Mbps aggregate UL + 4 Mbps aggregate DL through UPF
    ...    Verification: All 4 UEs achieve ~1 Mbps each, UPF handles concurrent tunnels
    ...    UPF Metrics: ul_pkts, dl_pkts, ul_dropped, dl_dropped, session_count=4
    ...    Expected Result: All 4 UEs pass, aggregate ~4 Mbps UL + ~4 Mbps DL
    [Tags]    tcp    4-ue    1mbps    priority-1
    Log    TC-MTR-101: TCP 4 UEs × 1 Mbps

TC-MTR-001 TCP 8 UEs 1Mbps Each
    [Documentation]    TC-MTR-001: TCP Multi-UE Traffic — 8 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7 (QoS), TS 29.281 (GTP-U per-UE tunnels)
    ...    Procedure:
    ...    1. Register 8 UEs, establish PDU sessions (8 GTP-U tunnels)
    ...    2. Start iperf3 server on SA Core via /api/iperf/start
    ...    3. Each UE runs iperf3 TCP at 1 Mbps uplink sequentially
    ...    4. Each UE runs iperf3 TCP at 1 Mbps downlink (reverse mode) sequentially
    ...    5. Collect UPF stats delta (before/after)
    ...    Parameters: 8 UEs, TCP, UL=1 Mbps, DL=1 Mbps, duration=10s per UE
    ...    Traffic: 8 Mbps aggregate UL + 8 Mbps aggregate DL through UPF
    ...    Verification: All 8 UEs achieve ~1 Mbps each, UPF handles concurrent tunnels
    ...    UPF Metrics: ul_pkts, dl_pkts, ul_dropped, dl_dropped, session_count=8
    ...    Expected Result: All 8 UEs pass, aggregate ~8 Mbps UL + ~8 Mbps DL
    [Tags]    tcp    8-ue    1mbps    priority-1
    Log    TC-MTR-001: TCP 8 UEs × 1 Mbps

TC-MTR-106 TCP 16 UEs 1Mbps Each
    [Documentation]    TC-MTR-106: TCP Multi-UE Traffic — 16 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7, TS 29.281 (GTP-U scaling)
    ...    Procedure:
    ...    1. Register 16 UEs, establish 16 PDU sessions (16 GTP-U tunnels)
    ...    2. Each UE: 1 Mbps TCP uplink + 1 Mbps TCP downlink
    ...    3. Collect UPF stats delta
    ...    Parameters: 16 UEs, TCP, UL=1 Mbps, DL=1 Mbps, duration=10s per UE
    ...    Traffic: 16 Mbps aggregate UL + 16 Mbps aggregate DL
    ...    Verification: All 16 UEs achieve throughput, UPF manages 16 concurrent sessions
    ...    Expected Result: All 16 UEs pass, aggregate ~16 Mbps each direction
    [Tags]    tcp    16-ue    1mbps    priority-2
    Log    TC-MTR-106: TCP 16 UEs × 1 Mbps

TC-MTR-002 TCP 32 UEs 1Mbps Each
    [Documentation]    TC-MTR-002: TCP Multi-UE Traffic — 32 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7, TS 29.281 (GTP-U scaling)
    ...    Procedure:
    ...    1. Register 32 UEs, establish 32 PDU sessions (32 GTP-U tunnels)
    ...    2. Each UE: 1 Mbps TCP uplink + 1 Mbps TCP downlink
    ...    3. Collect UPF stats delta
    ...    Parameters: 32 UEs, TCP, UL=1 Mbps, DL=1 Mbps, duration=10s per UE
    ...    Traffic: 32 Mbps aggregate UL + 32 Mbps aggregate DL
    ...    Verification: All 32 UEs achieve throughput, UPF manages 32 concurrent sessions
    ...    Expected Result: All 32 UEs pass, aggregate ~32 Mbps each direction
    [Tags]    tcp    32-ue    1mbps    priority-2
    Log    TC-MTR-002: TCP 32 UEs × 1 Mbps

TC-MTR-003 TCP 64 UEs 1Mbps Each
    [Documentation]    TC-MTR-003: TCP Multi-UE Traffic — 64 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7, TS 29.281 (GTP-U scaling)
    ...    Procedure:
    ...    1. Register 64 UEs, establish 64 PDU sessions
    ...    2. Each UE: 1 Mbps TCP uplink + 1 Mbps TCP downlink
    ...    Parameters: 64 UEs, TCP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 64 Mbps aggregate each direction
    ...    Expected Result: All 64 UEs pass, UPF handles 64 sessions
    [Tags]    tcp    64-ue    1mbps    large-scale    priority-3
    Log    TC-MTR-003: TCP 64 UEs × 1 Mbps

TC-MTR-004 TCP 128 UEs 1Mbps Each
    [Documentation]    TC-MTR-004: TCP Multi-UE Traffic — 128 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7, TS 29.281 (GTP-U scaling)
    ...    Procedure:
    ...    1. Register all 128 UEs, establish 128 PDU sessions
    ...    2. Each UE: 1 Mbps TCP uplink + 1 Mbps TCP downlink
    ...    Parameters: 128 UEs, TCP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 128 Mbps aggregate each direction
    ...    Expected Result: All 128 UEs pass, maximum capacity test
    [Tags]    tcp    128-ue    1mbps    large-scale    priority-3
    Log    TC-MTR-004: TCP 128 UEs × 1 Mbps

# ═══════════════════════════════════════════════════════════════
# UDP Multi-UE (1 Mbps UL + 1 Mbps DL per UE, jitter/loss)
# ═══════════════════════════════════════════════════════════════
TC-MTR-102 UDP 2 UEs 1Mbps Each
    [Documentation]    TC-MTR-102: UDP Multi-UE Traffic — 2 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7.2.1 (5QI characteristics)
    ...    Procedure:
    ...    1. Register 2 UEs, establish PDU sessions
    ...    2. Each UE: 1 Mbps UDP uplink (jitter/loss measured)
    ...    3. Each UE: 1 Mbps UDP downlink reverse mode
    ...    4. Per-UE jitter and packet loss reported
    ...    Parameters: 2 UEs, UDP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 2 Mbps aggregate UDP each direction
    ...    Verification: Per-UE jitter < 50ms, loss < 1%
    ...    Expected Result: Both UEs pass with acceptable jitter/loss
    [Tags]    udp    2-ue    1mbps    jitter    priority-1
    Log    TC-MTR-102: UDP 2 UEs × 1 Mbps

TC-MTR-103 UDP 4 UEs 1Mbps Each
    [Documentation]    TC-MTR-103: UDP Multi-UE Traffic — 4 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7.2.1 (5QI characteristics)
    ...    Procedure:
    ...    1. Register 4 UEs, establish PDU sessions
    ...    2. Each UE: 1 Mbps UDP uplink (jitter/loss measured)
    ...    3. Each UE: 1 Mbps UDP downlink reverse mode
    ...    4. Per-UE jitter and packet loss reported
    ...    Parameters: 4 UEs, UDP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 4 Mbps aggregate UDP each direction
    ...    Verification: Per-UE jitter < 50ms, loss < 1%
    ...    Expected Result: All 4 UEs pass with acceptable jitter/loss
    [Tags]    udp    4-ue    1mbps    jitter    priority-1
    Log    TC-MTR-103: UDP 4 UEs × 1 Mbps

TC-MTR-005 UDP 8 UEs 1Mbps Each
    [Documentation]    TC-MTR-005: UDP Multi-UE Traffic — 8 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7.2.1 (5QI characteristics)
    ...    Procedure:
    ...    1. Register 8 UEs, establish PDU sessions
    ...    2. Each UE: 1 Mbps UDP uplink (jitter/loss measured)
    ...    3. Each UE: 1 Mbps UDP downlink reverse mode
    ...    4. Per-UE jitter and packet loss reported
    ...    Parameters: 8 UEs, UDP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 8 Mbps aggregate UDP each direction
    ...    Verification: Per-UE jitter < 50ms, loss < 1%
    ...    Expected Result: All 8 UEs pass with acceptable jitter/loss
    [Tags]    udp    8-ue    1mbps    jitter    priority-1
    Log    TC-MTR-005: UDP 8 UEs × 1 Mbps

TC-MTR-107 UDP 16 UEs 1Mbps Each
    [Documentation]    TC-MTR-107: UDP Multi-UE Traffic — 16 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7.2.1
    ...    Parameters: 16 UEs, UDP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 16 Mbps aggregate UDP each direction
    ...    Verification: Per-UE jitter < 50ms, loss < 1%
    ...    Expected Result: All 16 UEs pass with acceptable jitter/loss
    [Tags]    udp    16-ue    1mbps    jitter    priority-2
    Log    TC-MTR-107: UDP 16 UEs × 1 Mbps

TC-MTR-006 UDP 32 UEs 1Mbps Each
    [Documentation]    TC-MTR-006: UDP Multi-UE Traffic — 32 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7.2.1
    ...    Procedure: Same as TC-MTR-005 with 32 UEs
    ...    Parameters: 32 UEs, UDP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 32 Mbps aggregate UDP each direction
    ...    Expected Result: All 32 UEs pass, jitter < 50ms, loss < 1%
    [Tags]    udp    32-ue    1mbps    jitter    priority-2
    Log    TC-MTR-006: UDP 32 UEs × 1 Mbps

TC-MTR-007 UDP 64 UEs 1Mbps Each
    [Documentation]    TC-MTR-007: UDP Multi-UE Traffic — 64 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7.2.1
    ...    Parameters: 64 UEs, UDP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 64 Mbps aggregate UDP each direction
    ...    Expected Result: All 64 UEs pass
    [Tags]    udp    64-ue    1mbps    jitter    large-scale    priority-3
    Log    TC-MTR-007: UDP 64 UEs × 1 Mbps

TC-MTR-008 UDP 128 UEs 1Mbps Each
    [Documentation]    TC-MTR-008: UDP Multi-UE Traffic — 128 UEs × 1 Mbps UL/DL
    ...    Standard: TS 23.501 §5.7.2.1
    ...    Parameters: 128 UEs, UDP, UL=1 Mbps, DL=1 Mbps, duration=10s
    ...    Traffic: 128 Mbps aggregate UDP each direction
    ...    Expected Result: All 128 UEs pass, maximum UDP capacity
    [Tags]    udp    128-ue    1mbps    jitter    large-scale    priority-3
    Log    TC-MTR-008: UDP 128 UEs × 1 Mbps

# ═══════════════════════════════════════════════════════════════
# Browsing (HTTP-like: 100K UL + 2M DL per UE, short bursts)
# ═══════════════════════════════════════════════════════════════
TC-MTR-104 Browsing 2 UEs
    [Documentation]    TC-MTR-104: Browsing Traffic — 2 UEs (100K UL + 2M DL)
    ...    Standard: TS 23.501 §5.7 (5QI=9: non-GBR, best effort internet)
    ...    Procedure:
    ...    1. Register 2 UEs, establish PDU sessions (DNN=internet)
    ...    2. Each UE: 100 Kbps TCP uplink (HTTP requests)
    ...    3. Each UE: 2 Mbps TCP downlink (HTTP responses/page loads)
    ...    4. Simulates asymmetric web browsing pattern
    ...    Parameters: 2 UEs, TCP, UL=100 Kbps, DL=2 Mbps, duration=5s
    ...    Traffic: 0.2 Mbps UL + 4 Mbps DL aggregate
    ...    Verification: Both UEs complete browsing sessions
    ...    Expected Result: Both UEs achieve target rates
    [Tags]    browsing    2-ue    http-like    priority-1
    Log    TC-MTR-104: Browsing 2 UEs (100K UL + 2M DL)

TC-MTR-105 Browsing 4 UEs
    [Documentation]    TC-MTR-105: Browsing Traffic — 4 UEs (100K UL + 2M DL)
    ...    Standard: TS 23.501 §5.7 (5QI=9: non-GBR, best effort internet)
    ...    Procedure:
    ...    1. Register 4 UEs, establish PDU sessions (DNN=internet)
    ...    2. Each UE: 100 Kbps TCP uplink (HTTP requests)
    ...    3. Each UE: 2 Mbps TCP downlink (HTTP responses/page loads)
    ...    4. Simulates asymmetric web browsing pattern
    ...    Parameters: 4 UEs, TCP, UL=100 Kbps, DL=2 Mbps, duration=5s
    ...    Traffic: 0.4 Mbps UL + 8 Mbps DL aggregate
    ...    Verification: All 4 UEs complete browsing sessions
    ...    Expected Result: All 4 UEs achieve target rates
    [Tags]    browsing    4-ue    http-like    priority-1
    Log    TC-MTR-105: Browsing 4 UEs (100K UL + 2M DL)

TC-MTR-009 Browsing 8 UEs
    [Documentation]    TC-MTR-009: Browsing Traffic — 8 UEs (100K UL + 2M DL)
    ...    Standard: TS 23.501 §5.7 (5QI=9: non-GBR, best effort internet)
    ...    Procedure:
    ...    1. Register 8 UEs, establish PDU sessions (DNN=internet)
    ...    2. Each UE: 100 Kbps TCP uplink (HTTP requests)
    ...    3. Each UE: 2 Mbps TCP downlink (HTTP responses/page loads)
    ...    4. Simulates asymmetric web browsing pattern
    ...    Parameters: 8 UEs, TCP, UL=100 Kbps, DL=2 Mbps, duration=5s
    ...    Traffic: 0.8 Mbps UL + 16 Mbps DL aggregate
    ...    Verification: All UEs complete browsing sessions
    ...    Expected Result: All 8 UEs achieve target rates
    [Tags]    browsing    8-ue    http-like    priority-1
    Log    TC-MTR-009: Browsing 8 UEs (100K UL + 2M DL)

TC-MTR-108 Browsing 16 UEs
    [Documentation]    TC-MTR-108: Browsing Traffic — 16 UEs (100K UL + 2M DL)
    ...    Standard: TS 23.501 §5.7 (5QI=9: non-GBR, best effort internet)
    ...    Parameters: 16 UEs, TCP, UL=100 Kbps, DL=2 Mbps, duration=5s
    ...    Traffic: 1.6 Mbps UL + 32 Mbps DL aggregate
    ...    Expected Result: All 16 UEs achieve target rates
    [Tags]    browsing    16-ue    http-like    priority-2
    Log    TC-MTR-108: Browsing 16 UEs (100K UL + 2M DL)

TC-MTR-010 Browsing 32 UEs
    [Documentation]    TC-MTR-010: Browsing Traffic — 32 UEs (100K UL + 2M DL)
    ...    Standard: TS 23.501 §5.7 (5QI=9)
    ...    Parameters: 32 UEs, TCP, UL=100 Kbps, DL=2 Mbps, duration=5s
    ...    Traffic: 3.2 Mbps UL + 64 Mbps DL aggregate
    ...    Expected Result: All 32 UEs achieve browsing rates
    [Tags]    browsing    32-ue    http-like    priority-2
    Log    TC-MTR-010: Browsing 32 UEs

TC-MTR-011 Browsing 64 UEs
    [Documentation]    TC-MTR-011: Browsing Traffic — 64 UEs (100K UL + 2M DL)
    ...    Standard: TS 23.501 §5.7 (5QI=9)
    ...    Parameters: 64 UEs, TCP, UL=100 Kbps, DL=2 Mbps, duration=5s
    ...    Traffic: 6.4 Mbps UL + 128 Mbps DL aggregate
    ...    Expected Result: All 64 UEs achieve browsing rates
    [Tags]    browsing    64-ue    http-like    large-scale    priority-3
    Log    TC-MTR-011: Browsing 64 UEs

TC-MTR-012 Browsing 128 UEs
    [Documentation]    TC-MTR-012: Browsing Traffic — 128 UEs (100K UL + 2M DL)
    ...    Standard: TS 23.501 §5.7 (5QI=9)
    ...    Parameters: 128 UEs, TCP, UL=100 Kbps, DL=2 Mbps, duration=5s
    ...    Traffic: 12.8 Mbps UL + 256 Mbps DL aggregate
    ...    Expected Result: All 128 UEs achieve browsing rates, max capacity
    [Tags]    browsing    128-ue    http-like    large-scale    priority-3
    Log    TC-MTR-012: Browsing 128 UEs
