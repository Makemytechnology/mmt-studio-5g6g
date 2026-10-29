# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: TSN — Time-Sensitive Networking + Timing.

TS 23.501 §5.27 — 5GS bridge model, TSN stream mapping, clock sync.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_tsn")


def _tsn_api(path, method="GET", body=None):
    """Call SA Core TSN REST API."""
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode()), resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


class TsnCreateBridge(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TSN-001",
        title="Create, verify and delete a TSN bridge",
        spec="TS 23.501 §5.27",
        domain=Domain.QOS,
        nfs=(NF.SMF, NF.UPF, NF.PCF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.27 models the 5GS as a TSN bridge: the UE-side\n"
            "  DS-TT and the UPF-side NW-TT bookend a virtual L2 bridge\n"
            "  with a VLAN id on it. This smoke pins the bridge-CRUD\n"
            "  contract — write then read, with VLAN consistency.\n"
            "\n"
            "Procedure (TS 23.501 §5.27 5GS bridge provisioning)\n"
            "  1. Pre-cleanup DELETE /api/tsn/bridges/{bridge_id}.\n"
            "  2. POST /api/tsn/bridges with bridge_id='bridge-test-001',\n"
            "     ds_tt_port='port-ds-1', nw_tt_port='port-nw-1',\n"
            "     vlan_id=100.\n"
            "  3. Assert POST in (200, 201).\n"
            "  4. GET /api/tsn/bridges/{bridge_id}.\n"
            "  5. Assert GET status == 200.\n"
            "  6. Assert response.vlan_id == 100.\n"
            "  7. Finally clause DELETEs the bridge.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — bridge id, port names, vlan_id=100 hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET 200 AND vlan_id field == 100 on GET.\n"
            "  pass_test fires with the GET bridge body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  bridge (GET response body).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Strict vlan_id match — no hollow-pass shape\n"
            "  on the VLAN field."
        ),
    )

    def run(self):
        bridge_id = "bridge-test-001"
        try:
            _tsn_api(f"/api/tsn/bridges/{bridge_id}", "DELETE")  # pre-cleanup
            # Create bridge
            result, status = _tsn_api("/api/tsn/bridges", "POST", {
                "bridge_id": bridge_id,
                "name": "Test TSN Bridge",
                "ds_tt_port": "port-ds-1",
                "nw_tt_port": "port-nw-1",
                "vlan_id": 100,
            })
            if status not in (200, 201):
                self.fail_test(f"Bridge creation failed: {status} {result}")
                return self.result
            log.info("Bridge created: %s", result)

            # Verify
            get_result, get_status = _tsn_api(f"/api/tsn/bridges/{bridge_id}")
            if get_status != 200:
                self.fail_test(f"Bridge GET failed: {get_status} {get_result}")
                return self.result

            if get_result.get("vlan_id") != 100:
                self.fail_test(f"VLAN mismatch", response=get_result)
                return self.result

            self.pass_test(bridge=get_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _tsn_api(f"/api/tsn/bridges/{bridge_id}", "DELETE")
        return self.result


class TsnCreateStream(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TSN-002",
        title="Create a TSN stream and verify 5QI mapping",
        spec="TS 23.501 §5.27",
        domain=Domain.QOS,
        nfs=(NF.SMF, NF.UPF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.27 + TS 23.503 require the 5GS bridge to map\n"
            "  every TSN stream's IEEE 802.1Q traffic class onto a 5G QoS\n"
            "  identifier (5QI). Without this mapping the PCF cannot pick\n"
            "  the right QoS flow for the stream's frames. This test pins\n"
            "  the mapping is computed at stream creation.\n"
            "\n"
            "Procedure (TS 23.501 §5.27 TSN→5QI mapping)\n"
            "  1. Pre-cleanup DELETE /api/tsn/bridges/{bridge_id}.\n"
            "  2. POST /api/tsn/bridges (bridge-test-002, vlan_id=200).\n"
            "  3. POST /api/tsn/streams with bridge_id, stream_id=\n"
            "     'stream-test-002', traffic_class=1, interval_us=1000.\n"
            "  4. Assert stream POST in (200, 201).\n"
            "  5. Read mapped_5qi = response.mapped_5qi.\n"
            "  6. Assert mapped_5qi is not None.\n"
            "  7. Finally clause DELETEs the bridge.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — traffic_class=1, interval_us=1000 hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Stream POST 200/201 AND response carries a non-null\n"
            "  mapped_5qi field. pass_test fires with stream payload and\n"
            "  mapped_5qi value.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  stream (POST response body), mapped_5qi.\n"
            "\n"
            "Known constraints\n"
            "  mapped_5qi is asserted non-null but the actual value is not\n"
            "  cross-checked against the TS 23.501 §5.7.4 table — any\n"
            "  truthy integer passes."
        ),
    )

    def run(self):
        bridge_id = "bridge-test-002"
        stream_id = "stream-test-002"
        try:
            _tsn_api(f"/api/tsn/bridges/{bridge_id}", "DELETE")  # pre-cleanup
            # Create bridge
            _tsn_api("/api/tsn/bridges", "POST", {
                "bridge_id": bridge_id,
                "name": "Stream Test Bridge",
                "ds_tt_port": "port-ds-2",
                "nw_tt_port": "port-nw-2",
                "vlan_id": 200,
            })

            # Create stream
            result, status = _tsn_api("/api/tsn/streams", "POST", {
                "bridge_id": bridge_id,
                "stream_id": stream_id,
                "traffic_class": 1,
                "interval_us": 1000,
            })
            if status not in (200, 201):
                self.fail_test(f"Stream creation failed: {status} {result}")
                return self.result

            mapped_5qi = result.get("mapped_5qi")
            if mapped_5qi is None:
                self.fail_test("No mapped_5qi in stream response", response=result)
                return self.result

            log.info("Stream created: 5QI=%s", mapped_5qi)
            self.pass_test(stream=result, mapped_5qi=mapped_5qi)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _tsn_api(f"/api/tsn/bridges/{bridge_id}", "DELETE")
        return self.result


class TsnMap5qi(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TSN-003",
        title="Map TSN traffic class to 5QI",
        spec="TS 23.501 §5.27",
        domain=Domain.QOS,
        nfs=(NF.SMF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.27 leaves the actual TC→5QI lookup table to\n"
            "  operator policy. This product exposes that lookup as a\n"
            "  standalone endpoint /api/tsn/streams/map-5qi so PCF rules\n"
            "  can be tested without building a full stream. The test\n"
            "  pins that the lookup itself produces a 5QI for a given TC.\n"
            "\n"
            "Procedure (TS 23.501 §5.27 TC→5QI lookup)\n"
            "  1. POST /api/tsn/streams/map-5qi with traffic_class=1.\n"
            "  2. Assert HTTP 200/201.\n"
            "  3. Read qfi = response.mapped_5qi / response.qfi /\n"
            "     response['5qi'].\n"
            "  4. Assert qfi is not None.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — traffic_class=1 hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND response carries a non-null 5QI in any of\n"
            "  the three field names. pass_test fires with the full\n"
            "  mapping body and the resolved qfi.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  mapping (POST response body), qfi (resolved 5QI value).\n"
            "\n"
            "Known constraints\n"
            "  qfi is asserted non-null only. The TC→5QI value isn't\n"
            "  matched against a spec/table, so any backend that returns\n"
            "  some integer passes."
        ),
    )

    def run(self):
        try:
            result, status = _tsn_api("/api/tsn/streams/map-5qi", "POST", {
                "traffic_class": 1,
            })
            if status not in (200, 201):
                self.fail_test(f"5QI mapping failed: {status} {result}")
                return self.result

            qfi = result.get("mapped_5qi") or result.get("qfi") or result.get("5qi")
            if qfi is None:
                self.fail_test("No 5QI value in response", response=result)
                return self.result

            log.info("Traffic class 1 → 5QI %s", qfi)
            self.pass_test(mapping=result, qfi=qfi)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TsnClockDomain(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TSN-004",
        title="Create TSN clock domain and verify sync status",
        spec="TS 23.501 §5.27",
        domain=Domain.QOS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.27 requires the 5GS bridge to participate in\n"
            "  the TSN clock-distribution model (IEEE 802.1AS / gPTP).\n"
            "  A clock domain pins a grandmaster identity and a target\n"
            "  sync accuracy. This test pins the domain-CRUD + sync-status\n"
            "  read.\n"
            "\n"
            "Procedure (TS 23.501 §5.27 TSN clock domain)\n"
            "  1. Pre-cleanup DELETE /api/tsn/clock-domains/{domain_id}.\n"
            "  2. POST /api/tsn/clock-domains with domain_id=\n"
            "     'clock-domain-001', gm_identity='00:11:22:33:44:55:66:77',\n"
            "     sync_accuracy_ns=100.\n"
            "  3. Assert HTTP 200/201.\n"
            "  4. GET /api/tsn/clock-domains/{domain_id}/sync-status.\n"
            "  5. Assert GET status == 200.\n"
            "  6. Finally clause DELETEs the domain.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — domain_id, gm_identity and sync_accuracy_ns=100 hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND sync-status GET == 200. pass_test fires\n"
            "  with the POST domain body and sync_status body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  domain (POST response), sync_status (GET response).\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: sync_status payload is reported but its\n"
            "  fields (e.g. master_state, offset_ns) are not asserted.\n"
            "  A stub that 200s with empty content would pass."
        ),
    )

    def run(self):
        domain_id = "clock-domain-001"
        try:
            _tsn_api(f"/api/tsn/clock-domains/{domain_id}", "DELETE")  # pre-cleanup
            result, status = _tsn_api("/api/tsn/clock-domains", "POST", {
                "domain_id": domain_id,
                "gm_identity": "00:11:22:33:44:55:66:77",
                "sync_accuracy_ns": 100,
            })
            if status not in (200, 201):
                self.fail_test(f"Clock domain creation failed: {status} {result}")
                return self.result
            log.info("Clock domain created: %s", result)

            # Check sync status
            sync_result, sync_status = _tsn_api(
                f"/api/tsn/clock-domains/{domain_id}/sync-status")
            if sync_status != 200:
                self.fail_test(f"Sync status query failed: {sync_status} {sync_result}")
                return self.result

            self.pass_test(domain=result, sync_status=sync_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _tsn_api(f"/api/tsn/clock-domains/{domain_id}", "DELETE")
        return self.result


class TsnGateSchedule(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TSN-005",
        title="Configure gate schedule for a TSN stream",
        spec="TS 23.501 §5.27",
        domain=Domain.QOS,
        nfs=(NF.SMF, NF.UPF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.27 supports IEEE 802.1Qbv-style time-aware\n"
            "  shapers via gate schedules: per-stream open/close windows\n"
            "  measured in nanoseconds within a recurring cycle. This\n"
            "  test pins that a gate schedule can be installed on a TSN\n"
            "  stream.\n"
            "\n"
            "Procedure (TS 23.501 §5.27 IEEE 802.1Qbv gate schedule)\n"
            "  1. Pre-cleanup DELETE /api/tsn/bridges/{bridge_id}.\n"
            "  2. POST /api/tsn/bridges (bridge-test-005, vlan_id=500).\n"
            "  3. POST /api/tsn/streams (stream-test-005, tc=1,\n"
            "     interval_us=500).\n"
            "  4. POST /api/tsn/gate-schedules with stream_id,\n"
            "     gate_state='open', start_time_ns=0, duration_ns=250000,\n"
            "     cycle_time_ns=1000000.\n"
            "  5. Assert HTTP 200/201.\n"
            "  6. Finally clause DELETEs the bridge (cascades stream +\n"
            "     schedule).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — schedule (250 us open in a 1 ms cycle) hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Gate-schedule POST 200/201. pass_test fires with the\n"
            "  schedule body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  schedule (POST response body).\n"
            "\n"
            "Known constraints\n"
            "  Write-only — no GET-back read or 802.1Qbv enforcement check.\n"
            "  A backend that 200s on POST without programming any actual\n"
            "  gate would pass."
        ),
    )

    def run(self):
        bridge_id = "bridge-test-005"
        stream_id = "stream-test-005"
        try:
            _tsn_api(f"/api/tsn/bridges/{bridge_id}", "DELETE")  # pre-cleanup
            # Create bridge and stream
            _tsn_api("/api/tsn/bridges", "POST", {
                "bridge_id": bridge_id,
                "name": "Gate Schedule Bridge",
                "ds_tt_port": "port-ds-5",
                "nw_tt_port": "port-nw-5",
                "vlan_id": 500,
            })
            _tsn_api("/api/tsn/streams", "POST", {
                "bridge_id": bridge_id,
                "stream_id": stream_id,
                "traffic_class": 1,
                "interval_us": 500,
            })

            # Set gate schedule
            result, status = _tsn_api("/api/tsn/gate-schedules", "POST", {
                "stream_id": stream_id,
                "gate_state": "open",
                "start_time_ns": 0,
                "duration_ns": 250000,
                "cycle_time_ns": 1000000,
            })
            if status not in (200, 201):
                self.fail_test(f"Gate schedule failed: {status} {result}")
                return self.result

            log.info("Gate schedule set: %s", result)
            self.pass_test(schedule=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _tsn_api(f"/api/tsn/bridges/{bridge_id}", "DELETE")
        return self.result


ALL_TSN_TCS = [
    TsnCreateBridge, TsnCreateStream, TsnMap5qi,
    TsnClockDomain, TsnGateSchedule,
]
