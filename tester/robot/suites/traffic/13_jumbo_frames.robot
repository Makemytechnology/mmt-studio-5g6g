# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Jumbo Frame Test Suite
...              Validates UPF handling of large packets (MTU up to 9000 bytes)
...              Tests GTP-U tunnel with jumbo frames — no fragmentation expected
...              TS 29.281 (GTP-U), TS 23.501 section 5.8.2.11 (MTU handling)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        jumbo    mtu    data-plane    performance

*** Test Cases ***
# ===============================================================
# Jumbo Frame UDP Tests (increasing packet sizes)
# ===============================================================
TC-JMB-001 UDP 1500 Byte Packets (Standard MTU)
    [Documentation]    TC-JMB-001: UDP with standard 1500-byte packets (baseline)
    ...    Standard: TS 29.281 (GTP-U encapsulation)
    ...    Procedure:
    ...    1. Register UE, establish PDU session
    ...    2. UL: iperf3 UDP 10Mbps with --length 1500 (standard Ethernet MTU)
    ...    3. DL: core iperf3 client with same parameters
    ...    4. Both directions simultaneously
    ...    Parameters: MTU=1500, bandwidth=10M, duration=30s
    ...    Verification: No fragmentation, throughput matches target
    ...    Expected Result: Baseline throughput established
    [Tags]    1500-mtu    baseline    priority-1
    Log    TC-JMB-001: Standard 1500-byte packets

TC-JMB-002 UDP 4000 Byte Packets (Medium Jumbo)
    [Documentation]    TC-JMB-002: UDP with 4000-byte jumbo packets
    ...    Procedure: Same as TC-JMB-001 with --length 4000
    ...    Parameters: MTU=4000, bandwidth=10M, duration=30s
    ...    Verification: UPF handles 4K packets without fragmentation
    ...    Expected Result: Throughput maintained with larger packets
    [Tags]    4000-mtu    jumbo    priority-1
    Log    TC-JMB-002: 4000-byte jumbo packets

TC-JMB-003 UDP 8000 Byte Packets (Large Jumbo)
    [Documentation]    TC-JMB-003: UDP with 8000-byte jumbo packets
    ...    Procedure: Same as TC-JMB-001 with --length 8000
    ...    Parameters: MTU=8000, bandwidth=10M, duration=30s
    ...    Verification: UPF handles 8K packets
    ...    Expected Result: Throughput maintained, no drops from MTU issues
    [Tags]    8000-mtu    jumbo    priority-2
    Log    TC-JMB-003: 8000-byte jumbo packets

TC-JMB-004 UDP 8972 Byte Packets (Max Jumbo 9000 MTU)
    [Documentation]    TC-JMB-004: UDP with maximum jumbo frame size (9000 MTU - headers)
    ...    Standard: TS 29.281 (GTP-U overhead: 8-byte header + 20 IP + variable ext)
    ...    Procedure: Same as TC-JMB-001 with --length 8972
    ...    Parameters: MTU=8972 (9000 - 20 IP - 8 UDP), bandwidth=10M, duration=30s
    ...    Note: 9000-byte Ethernet frame = 8972-byte UDP payload + 20 IP + 8 UDP
    ...    Verification: Maximum jumbo frame through GTP-U tunnel
    ...    Expected Result: No fragmentation at max jumbo size
    [Tags]    9000-mtu    max-jumbo    priority-2
    Log    TC-JMB-004: Max jumbo 8972-byte packets

TC-JMB-005 Jumbo Frame Throughput Sweep
    [Documentation]    TC-JMB-005: Throughput comparison across packet sizes
    ...    Procedure:
    ...    1. Run UDP 10Mbps at 1500, 4000, 8000, 8972 byte packet sizes
    ...    2. Measure throughput, jitter, loss at each size
    ...    3. Compare results — larger packets should give better throughput
    ...    Parameters: sizes=[1500, 4000, 8000, 8972], bandwidth=10M, duration=15s each
    ...    Verification: All sizes pass, throughput improves with size
    ...    Expected Result: Comparative throughput report
    [Tags]    sweep    comparison    priority-1
    Log    TC-JMB-005: Jumbo frame throughput sweep

TC-JMB-006 Jumbo Frame Multi-UE 8 UEs
    [Documentation]    TC-JMB-006: 8 UEs with jumbo frame UDP traffic
    ...    Procedure:
    ...    1. Register 8 UEs, establish PDU sessions
    ...    2. Each UE: simultaneous UL+DL UDP 5Mbps with --length 8000
    ...    3. Measure per-UE throughput and aggregate
    ...    Parameters: 8 UEs, MTU=8000, bandwidth=5M, duration=15s
    ...    Verification: All 8 UEs handle jumbo frames concurrently
    ...    Expected Result: Aggregate ~40 Mbps UL + ~40 Mbps DL
    [Tags]    multi-ue    8-ue    jumbo    priority-2
    Log    TC-JMB-006: 8 UEs jumbo frame traffic
