# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NTN Phase 2 (Regenerative payloads, store-and-forward, ISL).

TS 23.501 S5.40 -- Non-Terrestrial Network Phase 2 enhancements.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src import baseline

log = logging.getLogger("tester.tc_ntn_phase2")


def _ntn2_api(path, method="GET", body=None):
    """Call SA Core NTN Phase 2 REST API."""
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


def _create_regenerative(sat_id="SAT-REGEN-001"):
    """Helper: create a regenerative satellite config."""
    result, status = _ntn2_api("/api/ntn/phase2/regenerative", "POST", {
        "sat_id": sat_id,
        "onboard_nfs": "AMF,UPF",
        "processing_capacity": 100,
        "memory_mb": 4096,
    })
    return result, status


class NtnRegenerativeConfig(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN2-001",
        title="Create and delete a regenerative-payload satellite config",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.AMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundation smoke for the NTN Phase-2 regenerative payload\n"
            "  model (TS 38.821 §6.1, TS 23.501 NTN clauses). Phase 1 NTN\n"
            "  treats the satellite as a bent-pipe relay; Phase 2 lets the\n"
            "  satellite host live NFs (AMF, UPF) so user-plane can be served\n"
            "  during pass-gaps. This TC pins the CRUD surface that records\n"
            "  which NFs are onboard a given satellite ID — every later\n"
            "  capability / backhaul / S&F test depends on this config.\n"
            "\n"
            "Procedure (TS 38.821 §6.1 + TS 23.501 NTN)\n"
            "  1. _create_regenerative('SAT-REGEN-001') — POST\n"
            "     /api/ntn/phase2/regenerative {sat_id, onboard_nfs='AMF,UPF',\n"
            "     processing_capacity:100, memory_mb:4096}.\n"
            "  2. Assert HTTP 200/201 on create.\n"
            "  3. Record sat_id and the returned config envelope.\n"
            "  4. Teardown (finally): DELETE /api/ntn/phase2/regenerative/\n"
            "     {sat_id} so the registry is clean for the next run.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — sat_id is hard-coded 'SAT-REGEN-001' for this smoke.\n"
            "\n"
            "Pass criteria\n"
            "  Create POST returned 200/201 with a non-error body. (DELETE\n"
            "  in finally is best-effort, not asserted.)\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sat_id, config.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no UE, no radio. Exercises only the SA Core\n"
            "  NTN Phase-2 REST surface. Onboard NF capacity numbers are\n"
            "  recorded but not enforced by the simulator."
        ),
    )

    def run(self):
        sat_id = "SAT-REGEN-001"
        created = False
        try:
            result, status = _create_regenerative(sat_id)
            if status not in (200, 201):
                self.fail_test(f"Create regenerative config failed: {status} {result}")
                return self.result
            created = True

            log.info("Regenerative config created: sat_id=%s", sat_id)
            self.pass_test(sat_id=sat_id, config=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if created:
                _ntn2_api(f"/api/ntn/phase2/regenerative/{sat_id}", "DELETE")
        return self.result


class NtnStoreForward(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN2-002",
        title="Queue a store-and-forward message and verify queued",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.AMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the Phase-2 store-and-forward (S&F) message-queue path.\n"
            "  TS 38.821 §6.1 lists S&F as a core Phase-2 capability for LEO\n"
            "  constellations: when no satellite is currently in view of the\n"
            "  destination, the message is queued onboard and delivered on a\n"
            "  later pass. Without an observable queue, an operator cannot\n"
            "  size buffers or detect stuck messages.\n"
            "\n"
            "Procedure (TS 38.821 §6.1 S&F)\n"
            "  1. POST /api/ntn/phase2/store-forward {sat_id:'SAT-SF-001',\n"
            "     target:'GROUND-001', data_hex:'AABB', priority:1}.\n"
            "  2. Assert HTTP 200/201 on enqueue.\n"
            "  3. GET /api/ntn/phase2/store-forward; expect HTTP 200.\n"
            "  4. Read queue[] (fall back to messages[]).\n"
            "  5. Assert the queue is non-empty.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — sat_id, target and payload are fixed for this smoke.\n"
            "\n"
            "Pass criteria\n"
            "  Enqueue returned 200/201 AND queue GET returned 200 AND the\n"
            "  queue list contained at least one message.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  queue_length, queue.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No drain step is exercised here — that lives in\n"
            "  TC-NTN2-007 (SAF byte-counter ledger). The queue may retain\n"
            "  prior test traffic across runs unless the core resets it."
        ),
    )

    def run(self):
        try:
            result, status = _ntn2_api("/api/ntn/phase2/store-forward", "POST", {
                "sat_id": "SAT-SF-001",
                "target": "GROUND-001",
                "data_hex": "AABB",
                "priority": 1,
            })
            if status not in (200, 201):
                self.fail_test(f"Store-forward POST failed: {status} {result}")
                return self.result

            # Get queue
            q_result, q_status = _ntn2_api("/api/ntn/phase2/store-forward")
            if q_status != 200:
                self.fail_test(f"GET queue failed: {q_status} {q_result}")
                return self.result

            queue = q_result.get("queue", q_result.get("messages", []))
            if not queue:
                self.fail_test("Store-forward queue is empty after POST")
                return self.result

            log.info("Store-forward queued: %d messages", len(queue))
            self.pass_test(queue_length=len(queue), queue=queue)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NtnIslLink(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN2-003",
        title="Create and delete a persistent ISL link record",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the persistent (DB-backed) inter-satellite link record\n"
            "  used by Phase-2 routing. TS 38.821 §6.1 lets traffic travel\n"
            "  satellite-to-satellite via ISLs instead of returning to a\n"
            "  gateway between hops; the routing layer needs a stable ID\n"
            "  per pair so it can attach metrics and tear the link down on\n"
            "  end-of-life. This is the DB counterpart of the in-memory\n"
            "  mesh exercised in TC-NTN2-008.\n"
            "\n"
            "Procedure (TS 38.821 §6.1 ISL)\n"
            "  1. POST /api/ntn/phase2/isl {sat1_id:'SAT-ISL-A',\n"
            "     sat2_id:'SAT-ISL-B', bandwidth_mbps:1000, latency_ms:5.0}.\n"
            "  2. Assert HTTP 200/201 on create.\n"
            "  3. Extract isl_id from result['id'] (or result['isl_id']).\n"
            "  4. Teardown (finally): DELETE /api/ntn/phase2/isl/{isl_id}\n"
            "     if an isl_id was returned.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — satellite IDs and link metrics are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Create POST returned 200/201. (isl_id capture is best-effort\n"
            "  for cleanup; absence does not fail the test.)\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  isl_id, isl.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Bandwidth/latency are stored as link metadata,\n"
            "  not used to gate user-plane in the simulator. DELETE in\n"
            "  finally is best-effort; orphan rows are harmless."
        ),
    )

    def run(self):
        isl_id = None
        try:
            result, status = _ntn2_api("/api/ntn/phase2/isl", "POST", {
                "sat1_id": "SAT-ISL-A",
                "sat2_id": "SAT-ISL-B",
                "bandwidth_mbps": 1000,
                "latency_ms": 5.0,
            })
            if status not in (200, 201):
                self.fail_test(f"Create ISL failed: {status} {result}")
                return self.result

            isl_id = result.get("id") or result.get("isl_id")
            log.info("ISL link created: id=%s", isl_id)
            self.pass_test(isl_id=isl_id, isl=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if isl_id:
                _ntn2_api(f"/api/ntn/phase2/isl/{isl_id}", "DELETE")
        return self.result


class NtnCapabilities(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN2-004",
        title="Regenerative payload capability query reports onboard NFs",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.AMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the capability-discovery view for a regenerative-payload\n"
            "  satellite. TS 38.821 §6.1 requires that the network plane can\n"
            "  discover which NFs are hosted onboard a given satellite so it\n"
            "  can decide whether to route signalling or user-plane through\n"
            "  it during a pass. This TC verifies the GET path mirrors the\n"
            "  POST path created in TC-NTN2-001.\n"
            "\n"
            "Procedure (TS 38.821 §6.1)\n"
            "  1. _create_regenerative('SAT-CAP-001') — POST regenerative\n"
            "     config with onboard_nfs='AMF,UPF'.\n"
            "  2. Assert HTTP 200/201 on create.\n"
            "  3. GET /api/ntn/phase2/capabilities/{sat_id}; expect HTTP 200.\n"
            "  4. Read onboard_nfs from the capabilities envelope.\n"
            "  5. Assert that 'AMF' or 'UPF' appears in the onboard string.\n"
            "  6. Teardown (finally): DELETE the regenerative config.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — sat_id is hard-coded 'SAT-CAP-001'.\n"
            "\n"
            "Pass criteria\n"
            "  Create returned 200/201 AND capabilities GET returned 200\n"
            "  AND onboard_nfs included at least one of 'AMF' or 'UPF'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sat_id, capabilities.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Onboard NF list is checked as a substring match;\n"
            "  the test does not assert ordering or processing_capacity/\n"
            "  memory_mb echoed back."
        ),
    )

    def run(self):
        sat_id = "SAT-CAP-001"
        created = False
        try:
            result, status = _create_regenerative(sat_id)
            if status not in (200, 201):
                self.fail_test(f"Create regenerative config failed: {status} {result}")
                return self.result
            created = True

            cap_result, cap_status = _ntn2_api(
                f"/api/ntn/phase2/capabilities/{sat_id}")
            if cap_status != 200:
                self.fail_test(f"GET capabilities failed: {cap_status} {cap_result}")
                return self.result

            onboard = cap_result.get("onboard_nfs", "")
            if "AMF" not in onboard and "UPF" not in onboard:
                self.fail_test("Onboard NFs not listed in capabilities",
                               capabilities=cap_result)
                return self.result

            log.info("NTN capabilities: %s", cap_result)
            self.pass_test(sat_id=sat_id, capabilities=cap_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if created:
                _ntn2_api(f"/api/ntn/phase2/regenerative/{sat_id}", "DELETE")
        return self.result


class NtnPhase2Stats(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN2-005",
        title="Query NTN Phase 2 aggregate stats envelope",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.AMF, NF.UPF),
        severity=Severity.MINOR,
        tags=("smoke", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Read-only OAM contract test for the Phase-2 aggregate stats\n"
            "  envelope. TS 38.821 §6.1 / TS 23.501 NTN expect that the OAM\n"
            "  surface reports per-satellite and per-feature counters so the\n"
            "  operator can chart utilisation. This TC drives a small amount\n"
            "  of activity then ensures the stats endpoint is reachable —\n"
            "  schema/content is not parsed.\n"
            "\n"
            "Procedure (TS 38.821 §6.1 OAM)\n"
            "  1. _create_regenerative('SAT-STATS-001') — seed a config.\n"
            "  2. POST /api/ntn/phase2/store-forward with one S&F message\n"
            "     {sat_id, target:'GROUND-002', data_hex:'CCDD', priority:2}.\n"
            "  3. GET /api/ntn/phase2/stats; expect HTTP 200.\n"
            "  4. Capture the stats envelope as-is.\n"
            "  5. Teardown (finally): DELETE the regenerative config.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — all IDs and payloads are fixed for this contract test.\n"
            "\n"
            "Pass criteria\n"
            "  /api/ntn/phase2/stats GET returned HTTP 200. Body shape is\n"
            "  recorded but not asserted.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. MINOR severity — failure indicates an OAM\n"
            "  regression, not a service-plane outage. S&F message stays\n"
            "  in the queue (no drain) so subsequent runs see growing\n"
            "  counters."
        ),
    )

    def run(self):
        try:
            # Create some data first
            _create_regenerative("SAT-STATS-001")
            _ntn2_api("/api/ntn/phase2/store-forward", "POST", {
                "sat_id": "SAT-STATS-001",
                "target": "GROUND-002",
                "data_hex": "CCDD",
                "priority": 2,
            })

            # Query stats
            stats_result, stats_status = _ntn2_api("/api/ntn/phase2/stats")
            if stats_status != 200:
                self.fail_test(f"GET stats failed: {stats_status} {stats_result}")
                return self.result

            log.info("NTN Phase 2 stats: %s", stats_result)
            self.pass_test(stats=stats_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _ntn2_api("/api/ntn/phase2/regenerative/SAT-STATS-001", "DELETE")
        return self.result


class NtnPhase2Backhaul(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN2-006",
        title="Provision a satellite backhaul link, drive usage, deprovision",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.GNB, NF.AMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pin the satellite-backhaul ledger used by Phase-2 when a gNB\n"
            "  uses a LEO/GEO link as its transport instead of fibre. TS\n"
            "  38.821 §6.1 and TS 23.501 NTN clauses require that capacity\n"
            "  and current usage per (gNB, satellite) be observable so the\n"
            "  operator can detect over-subscription. This TC drives the\n"
            "  full lifecycle: provision → usage update → read-back.\n"
            "\n"
            "Procedure (TS 38.821 §6.1 backhaul)\n"
            "  1. POST /api/ntn/phase2/backhaul {gnb_id:'gNB-NTN-BH-001',\n"
            "     satellite_id:'SAT-BH-001', capacity_mbps:500.0}; expect\n"
            "     HTTP 200/201.\n"
            "  2. POST /api/ntn/phase2/backhaul/{gnb}/usage {mbps:100.0}.\n"
            "  3. GET /api/ntn/phase2/backhaul/stats; expect HTTP 200.\n"
            "  4. Assert stats['total_links'] >= 1.\n"
            "  5. Teardown (finally): DELETE /api/ntn/phase2/backhaul/{gnb}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — gnb_id, satellite_id and rates are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Provision returned 200/201 AND stats GET returned 200 AND\n"
            "  total_links was >= 1 (our link appeared in the ledger).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  gnb_id, satellite_id, stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Usage push is fire-and-forget — its HTTP code\n"
            "  is not asserted. The 100 Mbps update is recorded but no\n"
            "  shaping is applied to real traffic in the simulator."
        ),
    )

    def run(self):
        gnb = "gNB-NTN-BH-001"
        sat = "SAT-BH-001"
        provisioned = False
        try:
            result, status = _ntn2_api("/api/ntn/phase2/backhaul", "POST", {
                "gnb_id": gnb, "satellite_id": sat,
                "capacity_mbps": 500.0,
            })
            if status not in (200, 201):
                self.fail_test(f"Backhaul provision failed: {status} {result}")
                return self.result
            provisioned = True

            # Drive usage and read back stats
            _ntn2_api(f"/api/ntn/phase2/backhaul/{gnb}/usage", "POST",
                      {"mbps": 100.0})
            stats, st_st = _ntn2_api("/api/ntn/phase2/backhaul/stats")
            if st_st != 200:
                self.fail_test(f"Stats GET failed: {st_st} {stats}")
                return self.result
            if int(stats.get("total_links", 0)) < 1:
                self.fail_test("Backhaul not in stats", stats=stats)
                return self.result

            log.info("Backhaul stats after provision: %s", stats)
            self.pass_test(gnb_id=gnb, satellite_id=sat, stats=stats)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if provisioned:
                _ntn2_api(f"/api/ntn/phase2/backhaul/{gnb}", "DELETE")
        return self.result


class NtnPhase2SAF(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN2-007",
        title="Enqueue and drain SAF byte counters per IMSI",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.UPF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the per-IMSI byte ledger for store-and-forward (SAF).\n"
            "  Phase-2 NTN (TS 38.821 §6.1) must account for buffered user\n"
            "  bytes per subscriber so the network can bill/quota correctly\n"
            "  once the message is eventually delivered. This TC walks the\n"
            "  full enqueue → read counter → drain cycle and asserts the\n"
            "  counter actually moves between steps.\n"
            "\n"
            "Procedure (TS 38.821 §6.1 SAF accounting)\n"
            "  1. POST /api/ntn/phase2/saf/enqueue {imsi, bytes:1024};\n"
            "     expect HTTP 200/201.\n"
            "  2. GET /api/ntn/phase2/saf/{imsi}; expect HTTP 200 AND\n"
            "     queued_bytes >= 1024 (counter reflects the enqueue).\n"
            "  3. POST /api/ntn/phase2/saf/drain {imsi, bytes:1024};\n"
            "     expect HTTP 200.\n"
            "  4. Record the post-drain envelope.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — IMSI is baseline.imsi('embb-bulk', 0).\n"
            "\n"
            "Pass criteria\n"
            "  Enqueue returned 200/201 AND queued_bytes>=1024 after the\n"
            "  enqueue AND drain returned HTTP 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, after_drain.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No traffic actually transits a satellite — the\n"
            "  enqueue/drain is pure ledger arithmetic in SA Core. The\n"
            "  test does not assert queued_bytes==0 after drain, only that\n"
            "  the drain HTTP call succeeded."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            r1, s1 = _ntn2_api("/api/ntn/phase2/saf/enqueue", "POST",
                               {"imsi": imsi, "bytes": 1024})
            if s1 not in (200, 201):
                self.fail_test(f"SAF enqueue failed: {s1} {r1}")
                return self.result

            r2, s2 = _ntn2_api(f"/api/ntn/phase2/saf/{imsi}")
            if s2 != 200 or int(r2.get("queued_bytes", 0)) < 1024:
                self.fail_test("SAF queue did not reflect enqueue",
                               status=s2, body=r2)
                return self.result

            r3, s3 = _ntn2_api("/api/ntn/phase2/saf/drain", "POST",
                               {"imsi": imsi, "bytes": 1024})
            if s3 != 200:
                self.fail_test(f"SAF drain failed: {s3} {r3}")
                return self.result

            log.info("SAF after drain: %s", r3)
            self.pass_test(imsi=imsi, after_drain=r3)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NtnPhase2IslMesh(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN2-008",
        title="ISL adjacency mesh add, query neighbours, remove",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the live, in-memory ISLManager mesh used by the GUI and\n"
            "  by the Phase-2 routing layer to answer 'who can sat_a talk\n"
            "  to right now?'. TS 38.821 §6.1 expects a queryable neighbour\n"
            "  set per satellite for ISL routing; this is distinct from the\n"
            "  DB-backed pair table exercised in TC-NTN2-003.\n"
            "\n"
            "Procedure (TS 38.821 §6.1 ISL mesh)\n"
            "  1. POST /api/ntn/phase2/isl-mesh {from:'SAT-MESH-A',\n"
            "     to:'SAT-MESH-B', bw_mbps:250.0}; expect HTTP 200/201.\n"
            "  2. GET /api/ntn/phase2/isl-mesh/{sat_a}/neighbours; expect\n"
            "     HTTP 200.\n"
            "  3. Capture the neighbours envelope.\n"
            "  4. Teardown (finally): DELETE /api/ntn/phase2/isl-mesh?from=\n"
            "     {sat_a}&to={sat_b} to drop the adjacency.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — satellite IDs and bw are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Add POST returned 200/201 AND neighbours GET returned HTTP\n"
            "  200. Specific neighbour content is not asserted.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sat_a, sat_b, neighbours.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The mesh is in-process state; it resets on\n"
            "  SA Core restart. This TC is the GUI-facing counterpart of\n"
            "  the persistent DB pair test in TC-NTN2-003."
        ),
    )

    def run(self):
        sat_a = "SAT-MESH-A"
        sat_b = "SAT-MESH-B"
        added = False
        try:
            r, s = _ntn2_api("/api/ntn/phase2/isl-mesh", "POST", {
                "from": sat_a, "to": sat_b, "bw_mbps": 250.0,
            })
            if s not in (200, 201):
                self.fail_test(f"ISL-mesh add failed: {s} {r}")
                return self.result
            added = True

            n, ns = _ntn2_api(f"/api/ntn/phase2/isl-mesh/{sat_a}/neighbours")
            if ns != 200:
                self.fail_test(f"Neighbours GET failed: {ns} {n}")
                return self.result

            log.info("ISL-mesh neighbours of %s: %s", sat_a, n)
            self.pass_test(sat_a=sat_a, sat_b=sat_b, neighbours=n)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if added:
                _ntn2_api(f"/api/ntn/phase2/isl-mesh?from={sat_a}&to={sat_b}",
                          "DELETE")
        return self.result


ALL_NTN_PHASE2_TCS = [
    NtnRegenerativeConfig,
    NtnStoreForward,
    NtnIslLink,
    NtnCapabilities,
    NtnPhase2Stats,
    NtnPhase2Backhaul,
    NtnPhase2SAF,
    NtnPhase2IslMesh,
]
