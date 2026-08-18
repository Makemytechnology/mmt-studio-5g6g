# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    5QI / QoS Characteristics Conformance Suite
...              TS 23.501 §5.7.4   — Standardized 5QI to QoS characteristics
...              TS 23.501 §5.7.3.4 — Packet Delay Budget per 5QI
...              TS 23.501 §5.7.1.5 — Default QoS rule
...              TS 23.501 §5.7.2.5 — Flow Bit Rates (referenced by GBR rows)
...              (sections verified against local TS 23.501 v19.07.00)
...
...              Each test pins one row of Table 5.7.4-1 against the live
...              PCF + data plane. The local seed
...              (core/db/seed/baseline.yaml — qos_5qi_catalog) provisions
...              5QI rows 1, 2, 5, 9, 65, 66, 82 with priority / PDB / PELR
...              values cited from TS 23.501 v19.07.00 Table 5.7.4-1.
...              Add new TC-FQI-NNN entries below as the catalog grows;
...              implementations live in src/testcases/traffic/tc_fqi.py.
...
...              Every test body delegates to `Run Python TestCase <tc_id>`
...              which resolves the registered Python TestCase from
...              src/testcases/traffic/tc_fqi.py via TestCase.REGISTRY.
Resource         ../../resources/common.resource
Library          ../../libraries/TestCaseLibrary.py
Library          ../../libraries/GnbLibrary.py
Library          ../../libraries/UeLibrary.py
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        traffic    qos    5qi    catalog

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# Catalog conformance — PCF policy-preview asserts per DNN
# ═══════════════════════════════════════════════════════════════
TC-FQI-001 Default 5QI=9 on DNN=internet
    [Documentation]    TC-FQI-001 — default best-effort 5QI on DNN=internet
    ...    Standard: TS 23.501 §5.7.4 Table 5.7.4-1 row 5QI=9
    ...    (Non-GBR, Default Priority 90, PDB 300 ms, PER 10⁻⁶).
    ...    Implementation: tc_fqi.py::FiveQiDefaultInternet.
    ...    Procedure:
    ...    1. core_api GET /api/pcf/policy-preview?imsi=…&dnn=internet&sst=1.
    ...    2. Pull IsDefault rule from preview.rules[].
    ...    3. Assert FiveQI==9 AND ResourceType=='NonGBR'.
    ...    Pass gate: PCF default rule reports 5QI=9 / NonGBR.
    ...    Reported metrics: imsi, dnn, five_qi, resource_type, arp_priority,
    ...    service_name, default_qfi, expected_per_ts23501.
    ...    Note: ArpPriority is the ARP attribute (TS 23.501 §5.7.2.2), not
    ...    the spec's priority-level field — PCF emits ARP=9 for the default
    ...    service even though Table 5.7.4-1 priority-level for 5QI=9 is 90.
    [Tags]    5qi-9    non-gbr    catalog    priority-1
    Run Python TestCase    TC-FQI-001

TC-FQI-002 IMS Signalling 5QI=5 on DNN=ims
    [Documentation]    TC-FQI-002 — IMS signalling 5QI on DNN=ims
    ...    Standard: TS 23.501 §5.7.4 Table 5.7.4-1 row 5QI=5
    ...    (Non-GBR, Default Priority 10, PDB 100 ms, PER 10⁻⁶, IMS
    ...    Signalling).
    ...    Implementation: tc_fqi.py::FiveQiImsSignalling.
    ...    Procedure:
    ...    1. core_api GET /api/pcf/policy-preview?imsi=…&dnn=ims&sst=1.
    ...    2. Pull IsDefault rule; expect ServiceName=='ims_signalling'.
    ...    3. Assert FiveQI==5 AND ResourceType=='NonGBR'.
    ...    Pass gate: PCF default rule reports 5QI=5 / NonGBR.
    ...    Reported metrics: imsi, dnn, five_qi, resource_type, arp_priority,
    ...    service_name, expected_per_ts23501.
    [Tags]    5qi-5    ims    non-gbr    catalog    priority-1
    Run Python TestCase    TC-FQI-002

TC-FQI-003 Unbound DNN falls back to default_data 5QI=9
    [Documentation]    TC-FQI-003 — PCF fallback to default_data 5QI=9
    ...    Standard: TS 23.501 §5.7.4 (default 5QI fallback path) +
    ...    core PCF default_data short-circuit in pcf.go::CreatePolicy.
    ...    Implementation: tc_fqi.py::FiveQiFallbackUnboundDnn.
    ...    Procedure:
    ...    1. core_api GET /api/pcf/policy-preview?dnn=mcx&sst=1 — DNN
    ...       'mcx' has no service binding row in the baseline seed.
    ...    2. Pull IsDefault rule; expect ServiceName=='default_data'.
    ...    3. Assert FiveQI==9 AND ResourceType=='NonGBR'.
    ...    Pass gate: fallback rule reports default_data/9/NonGBR.
    ...    Reported metrics: imsi, dnn, five_qi, resource_type, service_name,
    ...    rule_count.
    ...    Note: re-point to a different unbound DNN if mcx ever gets a
    ...    service binding (5QI=65/66 MCX voice) added to the seed.
    [Tags]    fallback    catalog    priority-2
    Run Python TestCase    TC-FQI-003

TC-FQI-004 Per-DNN 5QI matrix conforms to TS 23.501 Table 5.7.4-1
    [Documentation]    TC-FQI-004 — cross-DNN 5QI conformance check
    ...    Standard: TS 23.501 §5.7.4 Table 5.7.4-1 (standardised 5QI
    ...    to QoS characteristics mapping; local v19.07.00).
    ...    Implementation: tc_fqi.py::FiveQiCatalogConformance.
    ...    Procedure:
    ...    1. expectations = {'internet': (9, 'NonGBR'),
    ...                       'ims':      (5, 'NonGBR')}
    ...       (mcx + iot covered by TC-FQI-003 fallback path).
    ...    2. For each (dnn, want_5qi, want_rt):
    ...       a. GET /api/pcf/policy-preview?dnn=<dnn>&sst=1.
    ...       b. Pull default rule.
    ...       c. Compare (got_5qi, got_rt) vs (want_5qi, want_rt).
    ...    Pass gate: every DNN row matches the expected (5QI, RT) pair.
    ...    Reported metrics: rows=[{dnn, got_5qi, want_5qi, got_rt,
    ...    want_rt, ok}], mismatches=subset where ok=False.
    [Tags]    catalog    matrix    priority-2
    Run Python TestCase    TC-FQI-004

# ═══════════════════════════════════════════════════════════════
# Per-5QI Packet Delay Budget envelopes (TS 23.501 §5.7.3.4)
# ═══════════════════════════════════════════════════════════════
TC-FQI-005 PDB envelope on 5QI=9 / DNN=internet
    [Documentation]    TC-FQI-005 — avg ICMP RTT < 300 ms on default flow
    ...    Standard: TS 23.501 §5.7.3.4 (Packet Delay Budget) +
    ...    Table 5.7.4-1 row 5QI=9 (PDB = 300 ms).
    ...    Implementation: tc_fqi.py::FiveQiPdbInternet.
    ...    Procedure:
    ...    1. require_gnb + require_ue + register_ue + establish_pdu
    ...       (DNN=internet, PSI=1, default 5QI=9).
    ...    2. target = params.ping_target OR derive_gateway(ue_ip).
    ...    3. ping -c 20 -I <ue_ip> -W 2 <target>.
    ...    4. Parse 'min/avg/max/mdev' line for avg_ms.
    ...    5. Assert avg_ms < pdb_ms (default 300).
    ...    Pass gate: ping rc==0 AND avg_ms < pdb_ms.
    ...    Reported metrics: ue_ip, target, count, min_ms, avg_ms, max_ms,
    ...    loss_pct, pdb_ms, five_qi, pdb_headroom_ms.
    [Tags]    5qi-9    pdb    icmp    priority-2
    Run Python TestCase    TC-FQI-005

TC-FQI-006 PDB envelope on 5QI=5 / DNN=ims
    [Documentation]    TC-FQI-006 — avg ICMP RTT < 100 ms on IMS flow
    ...    Standard: TS 23.501 §5.7.3.4 (Packet Delay Budget) +
    ...    Table 5.7.4-1 row 5QI=5 (PDB = 100 ms, IMS Signalling).
    ...    Implementation: tc_fqi.py::FiveQiPdbIms.
    ...    Procedure:
    ...    1. require_gnb + require_ue + register_ue + establish_pdu
    ...       (DNN=ims, PSI=2 to leave PSI=1/internet untouched).
    ...    2. target = params.ping_target OR derive_gateway(ue_ip).
    ...    3. ping -c 20 -I <ue_ip> -W 2 <target>.
    ...    4. Assert avg_ms < pdb_ms (default 100).
    ...    Pass gate: ping rc==0 AND avg_ms < pdb_ms.
    ...    Reported metrics: ue_ip, target, count, min_ms, avg_ms, max_ms,
    ...    loss_pct, pdb_ms, five_qi=5, dnn='ims', psi=2, pdb_headroom_ms.
    ...    Note: exercises only the bearer latency for the IMS DNN; real
    ...    IMS signalling delay lives in the TC-IMS-* suite.
    [Tags]    5qi-5    ims    pdb    icmp    priority-2
    Run Python TestCase    TC-FQI-006
