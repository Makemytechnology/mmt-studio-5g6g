# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Traffic Generation and QoS Validation Test Suite
...              TS 23.501 §5.7   — QoS model overview (5QI, QoS flows)
...              TS 23.501 §5.7.2.5 — Flow Bit Rates (MBR, GBR)
...              TS 23.501 §5.7.2.6 — Aggregate Bit Rates (Session-AMBR, UE-AMBR)
...              TS 23.501 §5.7.3.4 — Packet Delay Budget per 5QI
...              TS 23.501 §5.7.4   — Standardized 5QI to QoS characteristics
...              TS 29.281        — GTP-U user plane protocol
...              (sections verified against local TS 23.501 v19.07.00)
...
...              Each test case below documents:
...              - Standard: the 3GPP TS section the test exercises
...              - Procedure: the exact sequence the Python TestCase executes
...              - Pass gate: the actual assertion that decides PASS/FAIL
...              - Reported metrics: keys returned in result.details
...              - Notes: any known gap between description and implementation
...
...              Every test body delegates to `Run Python TestCase <tc_id>` which
...              resolves the registered Python TestCase from
...              src/testcases/traffic/tc_traffic.py (or src/testcases/core/
...              tc_pdu_session.py for TC-TRF-006) via TestCase.REGISTRY.
Resource         ../../resources/common.resource
Library          ../../libraries/TestCaseLibrary.py
Library          ../../libraries/GnbLibrary.py
Library          ../../libraries/UeLibrary.py
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        traffic    qos    data-plane    gtp-u

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# TCP Traffic — default 5QI=9 (non-GBR), no rate mandate per spec
# ═══════════════════════════════════════════════════════════════
TC-TRF-001 TCP Uplink Throughput
    [Documentation]    TC-TRF-001 — TCP uplink throughput via GTP-U
    ...    Standard: TS 29.281 (GTP-U user plane); TS 23.501 §5.7 (QoS model)
    ...    Procedure (matches src/testcases/traffic/tc_traffic.py::TcpUplink):
    ...    1. require_gnb() — auto-creates a gNB from config profile if none.
    ...    2. require_ue() — pulls first SIM from sim DB.
    ...    3. register_ue(ue, gnb) — 5G-AKA registration through NGAP/NAS.
    ...    4. establish_pdu(ue) — default DNN=internet, PSI=1, gets UE IP.
    ...    5. server = params.iperf_server OR derive_gateway(ue_ip).
    ...    6. TrafficEngine.create_session(src=ue_ip, dst=server, proto=tcp,
    ...       port=5201, duration=10s, direction=ul). start() / stop().
    ...    Pass gate: stats.throughput_kbps > 0 (any traffic at all).
    ...    Reported metrics: protocol, direction, ue_ip, server, tx_mbps,
    ...    retransmits, duration_s.
    ...    Note: TS 23.501 §5.7 default 5QI=9 is non-GBR — no minimum rate per
    ...    spec, so the strict gate is non-zero throughput. Local-network perf
    ...    targets (e.g. >50 Mbps) are informational only, not asserted.
    [Tags]    tcp    uplink    throughput    priority-1
    Run Python TestCase    TC-TRF-001

TC-TRF-002 TCP Downlink Throughput
    [Documentation]    TC-TRF-002 — TCP downlink throughput via GTP-U
    ...    Standard: TS 29.281 (GTP-U); TS 23.501 §5.7 (QoS model)
    ...    Procedure (matches TcpDownlink):
    ...    1–4 identical to TC-TRF-001 (register + PDU session).
    ...    5. TrafficEngine.create_session(src=ue_ip, dst=ue_ip, proto=tcp,
    ...       port=5202, duration=10s, direction=dl) — core-initiated stream
    ...       to the UE through the UPF / GTP-U tunnel.
    ...    Pass gate: stats.throughput_kbps > 0.
    ...    Reported metrics: protocol, direction, ue_ip, rx_mbps, duration_s.
    ...    Note: same non-GBR 5QI=9 rationale as TC-TRF-001 — non-zero is the
    ...    spec-correct gate; throughput numbers are diagnostic.
    [Tags]    tcp    downlink    throughput    priority-1
    Run Python TestCase    TC-TRF-002

TC-TRF-003 TCP Bidirectional Throughput
    [Documentation]    TC-TRF-003 — TCP simultaneous UL + DL through one tunnel
    ...    Standard: TS 29.281 (GTP-U)
    ...    Procedure (matches TcpBidirectional):
    ...    1–4 register UE + PDU session as TC-TRF-001.
    ...    5. TrafficEngine.run_bidir(ip_a=ue_ip, ip_b=server, proto=tcp,
    ...       ul_port=5201, dl_port=5202, duration=10s, udp=False).
    ...    Pass gate: ul.throughput_kbps > 0 AND dl.throughput_kbps > 0.
    ...    Reported metrics: protocol, direction=bidirectional, ue_ip,
    ...    ul_mbps, dl_mbps.
    [Tags]    tcp    bidirectional    throughput    priority-1
    Run Python TestCase    TC-TRF-003

# ═══════════════════════════════════════════════════════════════
# UDP Traffic — fixed target with 85% threshold
# ═══════════════════════════════════════════════════════════════
TC-TRF-004 UDP Uplink At 50 Mbps Target
    [Documentation]    TC-TRF-004 — UDP uplink, 50 Mbps target, 85% threshold
    ...    Standard: TS 29.281 (GTP-U); TS 23.501 §5.7 (QoS model)
    ...    Procedure (matches UdpUplink — DEFAULT_BANDWIDTH=50M):
    ...    1–4 register UE + PDU session.
    ...    5. TrafficEngine.create_session(proto=udp, bandwidth=50M,
    ...       duration=10s, direction=ul).
    ...    Pass gate: tx_mbps >= 0.85 * target_mbps (i.e. >= 42.5 Mbps).
    ...    Reported metrics: protocol=UDP, direction=uplink, ue_ip, server,
    ...    target_mbps=50, min_mbps=42.5, tx_mbps, jitter_ms, lost_packets,
    ...    total_packets, loss_pct.
    ...    Note: jitter/loss are reported as SUT health signals only.
    ...    Default 5QI=9 has no spec-mandated jitter/loss bound.
    [Tags]    udp    uplink    jitter    loss    priority-1
    Run Python TestCase    TC-TRF-004

TC-TRF-005 UDP Downlink At 50 Mbps Target
    [Documentation]    TC-TRF-005 — UDP downlink, 50 Mbps target, 85% threshold
    ...    Standard: TS 29.281; TS 23.501 §5.7
    ...    Procedure (matches UdpDownlink — DEFAULT_BANDWIDTH=50M):
    ...    Same as TC-TRF-004 but core-initiated UDP toward the UE
    ...    (direction=dl, port=5202).
    ...    Pass gate: rx_mbps >= 0.85 * target_mbps.
    ...    Reported metrics: protocol=UDP, direction=downlink, ue_ip,
    ...    target_mbps=50, min_mbps=42.5, rx_mbps, jitter_ms, lost_packets,
    ...    total_packets, loss_pct.
    [Tags]    udp    downlink    jitter    loss    priority-1
    Run Python TestCase    TC-TRF-005

TC-TRF-006 UDP Bidirectional At 50 Mbps Target
    [Documentation]    TC-TRF-006 — UDP simultaneous UL + DL, 50 Mbps each
    ...    Standard: TS 29.281; TS 23.501 §5.7
    ...    Implementation: src/testcases/core/tc_pdu_session.py::UdpBidirectional
    ...    (registered under this tc_id — TC-TRF-006 — there).
    ...    Procedure: register UE + PDU session, then run_bidir(proto=udp,
    ...    ul_port=5201, dl_port=5202, bandwidth=50M, duration=TRAFFIC_DURATION).
    ...    Pass gate: ul_mbps >= min_mbps AND dl_mbps >= min_mbps
    ...    (min_mbps = 85% of target).
    ...    Reported metrics: protocol=UDP, direction=bidirectional, ue_ip,
    ...    server, target_mbps, min_mbps, ul_mbps, dl_mbps, ul_jitter_ms,
    ...    dl_jitter_ms, ul_loss_pct, dl_loss_pct.
    ...    Note: this test runs longer than TC-001..005 because TRAFFIC_DURATION
    ...    defaults to ~30s and the bidir engine optionally holds the session
    ...    open for the full window when iperf3 bails early.
    [Tags]    udp    bidirectional    jitter    loss    priority-1
    Run Python TestCase    TC-TRF-006

# ═══════════════════════════════════════════════════════════════
# Latency
# ═══════════════════════════════════════════════════════════════
TC-TRF-007 ICMP RTT Through GTP-U
    [Documentation]    TC-TRF-007 — ICMP round-trip latency via GTP-U
    ...    Standard: TS 23.501 §5.7.3.4 (Packet Delay Budget per 5QI)
    ...    Implementation: src/testcases/traffic/tc_traffic.py::LatencyTest
    ...    (registered as tc_id TC-TRF-022 in the Python registry).
    ...    Procedure:
    ...    1–4 register UE + PDU session.
    ...    5. target = params.ping_target OR derive_gateway(ue_ip).
    ...    6. subprocess.run(["ping","-c","20","-I",ue_ip,"-W","2",target]).
    ...    7. Parse stdout for min/avg/max RTT line and packet-loss line.
    ...    Pass gate: ping returncode == 0.
    ...    Reported metrics: ue_ip, target, count=20, min_ms, avg_ms, max_ms,
    ...    loss_pct.
    ...    Note: the §5.7.3.4 PDB ceiling per 5QI is the spec mandate; the
    ...    Python TC does not currently assert avg_ms vs PDB — it only requires
    ...    a successful ping. Local-network sub-100ms is informational only.
    [Tags]    icmp    latency    rtt    ping    priority-1
    Run Python TestCase    TC-TRF-022

# ═══════════════════════════════════════════════════════════════
# QoS Rate Control — AMBR / MBR / GBR
# Known gap: these tests currently pass_test() unconditionally as long
# as iperf3 produced any throughput. The enforced/met flags are
# COMPUTED and REPORTED but NOT asserted. See per-TC "Known gap" note.
# ═══════════════════════════════════════════════════════════════
TC-TRF-008 Session AMBR Enforcement
    [Documentation]    TC-TRF-008 — Session-AMBR enforcement on uplink
    ...    Standard: TS 23.501 §5.7.2.6 (Aggregate Bit Rates — Session-AMBR
    ...    per PDU session, verified against local TS 23.501 v19.07.00).
    ...    Enforcement behaviour also referenced in §5.7.1.8 (AMBR/MFBR
    ...    enforcement and rate limitation).
    ...    Implementation: src/testcases/traffic/tc_traffic.py::AmbrEnforcement
    ...    (registered as tc_id TC-QOS-001 in the Python registry).
    ...    Procedure:
    ...    1. _set_ambr(imsi, dl_kbps=100000, ul_kbps=50000) — POSTs the AMBR
    ...       to SA Core /ue/subscription so SMF/PCF installs a UPF QER per
    ...       TS 29.244 §7.5.2.5.
    ...    2. register_ue + establish_pdu (PDU session inherits the AMBR).
    ...    3. upf_before = collect_upf_stats() (baseline UPF counters).
    ...    4. TrafficEngine.create_session(proto=tcp, bandwidth=2*ambr_ul_kbps,
    ...       duration=10s, direction=ul) — send 100 Mbps UL.
    ...    5. upf_after = collect_upf_stats(); upf_delta = compute_upf_delta().
    ...    6. upf_ul_kbps = upf_delta.io.ul_bytes * 8 / duration.
    ...    7. enforced = upf_ul_kbps <= ambr_ul_kbps * 1.2 (20% tolerance).
    ...    Pass gate (current): stats.throughput_kbps > 0 — iperf3 produced
    ...    ANY traffic. The `ambr_enforced` flag is REPORTED but NOT a gate.
    ...    Reported metrics: ambr_ul_kbps, ambr_dl_kbps, sent_kbps, sent_mbps,
    ...    upf_delivered_kbps, upf_delivered_mbps, upf_ul_dropped,
    ...    ambr_enforced, upf_stats, ue_ip, imsi, duration_s.
    ...    Known gap: with upf_delivered=0 (UPF stats not refreshed in time
    ...    or stats endpoint silently failing), enforced=(0 <= 1.2*N)=True is
    ...    a vacuous truth — the test reports "passed" without proving the
    ...    UPF QER actually rate-limited anything. Tracked separately.
    [Tags]    ambr    rate-limit    qos    priority-1
    Run Python TestCase    TC-QOS-001

TC-TRF-009 MBR Downlink Enforcement
    [Documentation]    TC-TRF-009 — MBR enforcement on downlink (per-flow rate limit)
    ...    Standard: TS 23.501 §5.7.2.5 (Flow Bit Rates — MBR per QoS flow,
    ...    verified against local TS 23.501 v19.07.00). Note: §5.7.2.4 is
    ...    "Notification control" in the local spec, NOT MBR.
    ...    Implementation: tc_traffic.py::MbrDownlinkTest (tc_id TC-QOS-002).
    ...    Procedure (same shape as TC-TRF-008 but DL):
    ...    1. _set_ambr(imsi, dl_kbps=50000, ul_kbps=1000000) — the "MBR" on
    ...       the DL flow is provisioned via the same subscription API.
    ...    2. register_ue + establish_pdu.
    ...    3. upf_before = collect_upf_stats().
    ...    4. TrafficEngine.create_session(proto=tcp, port=5202, duration=10s,
    ...       direction=dl) — core sends DL at line rate (no bandwidth arg).
    ...    5. upf_after; compute_upf_delta; upf_dl_kbps from io.dl_bytes.
    ...    6. enforced = upf_dl_kbps <= mbr_dl_kbps * 1.2.
    ...    Pass gate (current): stats.throughput_kbps > 0. `mbr_enforced`
    ...    is reported but NOT a gate.
    ...    Reported metrics: mbr_dl_kbps, core_sent_kbps, upf_delivered_kbps,
    ...    upf_delivered_mbps, upf_dl_dropped, mbr_enforced, upf_stats,
    ...    ue_ip, imsi, duration_s.
    ...    Known gap: same vacuous-true risk as TC-TRF-008 when upf_delivered=0.
    [Tags]    mbr    downlink    rate-limit    qos    priority-1
    Run Python TestCase    TC-QOS-002

TC-TRF-010 MBR Uplink Enforcement
    [Documentation]    TC-TRF-010 — MBR enforcement on uplink
    ...    Standard: TS 23.501 §5.7.2.5 (Flow Bit Rates — MBR per QoS flow).
    ...    Implementation: tc_traffic.py::MbrUplinkTest (tc_id TC-QOS-003).
    ...    Procedure: symmetric to TC-TRF-009 in the UL direction —
    ...    _set_ambr(dl=1000000, ul=50000), send 2*MBR UL via iperf3 TCP,
    ...    compute upf_ul_kbps from io.ul_bytes delta.
    ...    Pass gate (current): stats.throughput_kbps > 0.
    ...    Reported metrics: mbr_ul_kbps, sent_kbps, sent_mbps,
    ...    upf_delivered_kbps, upf_delivered_mbps, upf_ul_dropped,
    ...    mbr_enforced, upf_stats, ue_ip, imsi, duration_s.
    ...    Known gap: same vacuous-true risk as TC-TRF-008.
    [Tags]    mbr    uplink    rate-limit    qos    priority-1
    Run Python TestCase    TC-QOS-003

TC-TRF-011 GBR Flow Guarantee
    [Documentation]    TC-TRF-011 — GBR guaranteed minimum delivered rate
    ...    Standard: TS 23.501 §5.7.2.5 (Flow Bit Rates — GBR per QoS flow,
    ...    verified against local TS 23.501 v19.07.00). Note: §5.7.2.3 is
    ...    "RQA" (Reflective QoS Attribute) in the local spec, NOT GBR.
    ...    Implementation: tc_traffic.py::GbrFlowTest (tc_id TC-TRF-011).
    ...    Procedure:
    ...    1. register_ue + establish_pdu (default GBR=10000 kbps via params).
    ...    2. upf_before = collect_upf_stats().
    ...    3. TrafficEngine.create_session(proto=udp, bandwidth=10000K,
    ...       duration=10s, direction=ul) — send UDP exactly at GBR rate.
    ...    4. upf_after; compute_upf_delta; upf_ul_kbps from io.ul_bytes.
    ...    5. gbr_met = actual_kbps >= gbr_kbps * 0.9 AND loss_pct < 1.0.
    ...    Pass gate (current): stats.throughput_kbps > 0. `gbr_met` is
    ...    reported but NOT a pass/fail gate.
    ...    Reported metrics: gbr_kbps, sent_kbps, sent_mbps,
    ...    upf_delivered_kbps, upf_ul_dropped, jitter_ms, loss_pct, gbr_met,
    ...    upf_stats, ue_ip, imsi, duration_s.
    ...    Known gap: same shape as TC-TRF-008..010 — gbr_met is not asserted.
    [Tags]    gbr    guaranteed    qos    priority-1
    Run Python TestCase    TC-TRF-011

# ═══════════════════════════════════════════════════════════════
# Multi-UE and Stress
# ═══════════════════════════════════════════════════════════════
TC-TRF-012 Multi-UE Simultaneous Traffic
    [Documentation]    TC-TRF-012 — N UEs each running TCP uplink in turn
    ...    Standard: TS 29.281 (per-UE GTP-U tunnels); TS 23.501 §5.2.1
    ...    (AMF serves multiple gNBs).
    ...    Implementation: tc_traffic.py::MultiUeTraffic (tc_id TC-TRF-012).
    ...    Procedure:
    ...    1. require_gnb(); ue_count = min(params.ue_count or 2, len(ue_pool)).
    ...    2. Fail if fewer than 2 UEs available.
    ...    3. For each UE: register_ue + establish_pdu (PSI=1).
    ...    4. server = params.iperf_server OR derive_gateway(first_ue_ip).
    ...    5. For each UE sequentially: TrafficEngine.create_session(proto=tcp,
    ...       port=5201, duration=5s, direction=ul). Run, record tx_mbps.
    ...    Pass gate: every UE's stats.throughput_kbps > 0.
    ...    Reported metrics: ue_count, ue_results=[{imsi, ue_ip, status,
    ...    tx_mbps}], total_tx_mbps.
    ...    Note: tests are sequential not parallel — each UE's iperf3 runs to
    ...    completion before the next starts. "Simultaneous" in the doc
    ...    refers to all UEs being registered + having a live PDU session at
    ...    the same time, not concurrent iperf3.
    [Tags]    multi-ue    throughput    priority-2
    Run Python TestCase    TC-TRF-012

TC-TRF-013 Sustained Traffic Stability
    [Documentation]    TC-TRF-013 — 30-second TCP uplink stability run
    ...    Standard: TS 29.281 (GTP-U tunnel stability under sustained load)
    ...    Implementation: tc_traffic.py::SustainedTraffic (tc_id TC-TRF-013).
    ...    Procedure:
    ...    1–4 register UE + PDU session.
    ...    5. TrafficEngine.create_session(proto=tcp, port=5201, duration=30s
    ...       (default), direction=ul). Run for full 30 seconds.
    ...    6. Read sum_received from iperf3 raw JSON for rx_mbps.
    ...    Pass gate: stats.throughput_kbps > 0 over the full 30s window.
    ...    Reported metrics: protocol=TCP, direction=uplink, ue_ip, tx_mbps,
    ...    rx_mbps, duration_s, retransmits.
    ...    Note: catches tunnel/socket leaks and FSM stalls that a 10s smoke
    ...    test would miss. "0 retransmits" is a perf hint, not asserted.
    [Tags]    sustained    stability    stress    priority-2
    Run Python TestCase    TC-TRF-013
