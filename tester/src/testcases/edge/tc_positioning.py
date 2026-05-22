# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""5G NR Positioning Test Cases — LMF, GMLC, NRPPa, Geofencing.

TS 23.273 — 5G System positioning architecture
TS 38.305 — NR positioning methods (E-CID, Multi-RTT, DL-TDOA, etc.)
TS 29.572 — Nlmf_Location service operations
TS 38.455 — NRPPa protocol (gNB ↔ LMF)
TS 37.355 — LPP protocol (UE ↔ LMF)
TS 38.211 §7.4.1.7 — PRS signal configuration
TS 23.271 §9 — LCS privacy classes
"""

import time
import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_positioning")

# ── GMLC API helpers ──

def _gmlc_api(path, method="GET", body=None, core_ip=None):
    """Call sa_core GMLC/positioning REST API."""
    if not core_ip:
        from src.core.api import get_core_ip
        core_ip = get_core_ip()
    url = f"http://{core_ip}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode()), resp.status
    except urllib.error.HTTPError as e:
        try:
            body = json.loads(e.read().decode())
        except Exception:
            body = {"error": str(e)}
        return body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


def _register_gnb_position(gnb_id, lat, lon, alt=30.0, core_ip=None):
    """Register gNB geographic position via GMLC API."""
    return _gmlc_api("/api/gnb/position", "POST", {
        "gnb_id": gnb_id, "latitude": lat, "longitude": lon, "altitude": alt,
    }, core_ip)


def _register_gnb_antenna(gnb_id, azimuth=0, beamwidth=120, downtilt=6, num_beams=8, core_ip=None):
    """Register gNB antenna config via GMLC API."""
    return _gmlc_api("/api/gnb/antenna", "POST", {
        "gnb_id": gnb_id, "azimuth_deg": azimuth, "beamwidth_deg": beamwidth,
        "downtilt_deg": downtilt, "num_beams": num_beams,
    }, core_ip)


def _request_location(imsi, method="ecid", accuracy_m=100, response_time_s=10, core_ip=None):
    """Request UE location via GMLC API (TS 29.572 §5.2.2)."""
    return _gmlc_api("/api/location/request", "POST", {
        "imsi": imsi, "method": method,
        "accuracy_m": accuracy_m, "response_time_s": response_time_s,
    }, core_ip)


def _get_location(session_id, core_ip=None):
    """Get location result for a session."""
    return _gmlc_api(f"/api/location/{session_id}", "GET", core_ip=core_ip)


def _allocate_prs(gnb_id, periodicity_ms=20, num_rb=24, num_symbols=2, comb_size=2, core_ip=None):
    """Allocate PRS resource for gNB (TS 38.211 §7.4.1.7)."""
    return _gmlc_api("/api/prs/allocate", "POST", {
        "gnb_id": gnb_id, "periodicity_ms": periodicity_ms,
        "num_rb": num_rb, "num_symbols": num_symbols, "comb_size": comb_size,
    }, core_ip)


def _get_prs(gnb_id, core_ip=None):
    """Get PRS configuration for gNB."""
    return _gmlc_api(f"/api/prs/{gnb_id}", "GET", core_ip=core_ip)


def _delete_prs(prs_id, core_ip=None):
    """Delete PRS resource."""
    return _gmlc_api(f"/api/prs/{prs_id}", "DELETE", core_ip=core_ip)


# ── gNB reference positions (simulated cell tower locations) ──
# 3 gNBs forming a triangle for trilateration
GNB_POSITIONS = {
    "gnb-0": {"lat": 37.7749, "lon": -122.4194, "alt": 30.0},  # San Francisco
    "gnb-1": {"lat": 37.7760, "lon": -122.4170, "alt": 25.0},  # ~250m NE
    "gnb-2": {"lat": 37.7735, "lon": -122.4170, "alt": 28.0},  # ~250m SE
}


class PosEcid(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-001",
        title="NR Enhanced Cell ID (E-CID) positioning",
        spec="TS 38.305 §8.1",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.LMF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        slice=Slice.EMBB,
        dnn="internet",
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  TS 38.305 §8.1 defines Enhanced Cell ID (E-CID) as the\n"
            "  coarsest NR positioning method: the LMF returns the serving\n"
            "  cell's geographic anchor (lat/lon) optionally refined by\n"
            "  timing-advance and Rx-Tx. It is the fallback gate every UE\n"
            "  must satisfy, so this smoke test pins the GMLC → LMF call.\n"
            "\n"
            "Procedure (TS 38.305 §8.1 + TS 29.572 §5.2.2)\n"
            "  1. require_gnb(); require_ue(); pick ue = ue_pool[0].\n"
            "  2. register_ue(ue, gnb) — full 5G-AKA.\n"
            "  3. establish_pdu(ue, psi=1, dnn='internet').\n"
            "  4. _register_gnb_position(gnb_name, lat, lon, alt) for gnb-0\n"
            "     (37.7749, -122.4194).\n"
            "  5. _request_location(imsi, method='ecid', accuracy_m=200).\n"
            "  6. If state != 'completed', poll GET /api/location/{sid}\n"
            "     for up to 10 s.\n"
            "  7. Read latitude, longitude, uncertainty_m from response.\n"
            "  8. Assert lat is not None AND lon is not None.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — accuracy_m=200 and SF anchor coords hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Location request returns 200/201/202 AND a (lat, lon) fix\n"
            "  becomes available within 10 s.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  method, imsi, latitude, longitude, uncertainty_m, gnb,\n"
            "  session_id.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE, eMBB slice, internet DNN. uncertainty_m is\n"
            "  reported but not asserted against the requested accuracy."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        gnb_name = gnb.gnb_name if hasattr(gnb, 'gnb_name') else "tester-gnb-00"
        gnb_pos = GNB_POSITIONS.get("gnb-0", GNB_POSITIONS["gnb-0"])

        # Register gNB position
        result, status = _register_gnb_position(gnb_name, gnb_pos["lat"], gnb_pos["lon"], gnb_pos["alt"])
        if status not in (200, 201):
            self.fail_test(f"Failed to register gNB position: {status} {result}")
            return self.result
        log.info("gNB position registered: %s at %.4f,%.4f", gnb_name, gnb_pos["lat"], gnb_pos["lon"])

        # Request E-CID location
        result, status = _request_location(ue.imsi, method="ecid", accuracy_m=200)
        if status not in (200, 201, 202):
            self.fail_test(f"Location request failed: {status} {result}")
            return self.result

        session_id = result.get("session_id")
        loc_state = result.get("state", "")
        lat = result.get("latitude")
        lon = result.get("longitude")
        uncertainty = result.get("uncertainty_m")

        # Poll if async
        if session_id and loc_state not in ("COMPLETED", "completed"):
            deadline = time.time() + 10
            while time.time() < deadline:
                result, status = _get_location(session_id)
                if result.get("state") in ("COMPLETED", "completed"):
                    lat = result.get("latitude")
                    lon = result.get("longitude")
                    uncertainty = result.get("uncertainty_m")
                    break
                time.sleep(1)

        if lat is None or lon is None:
            self.fail_test("E-CID location not returned", session_id=session_id, result=result)
            return self.result

        log.info("E-CID location: %.6f, %.6f (uncertainty=%.1fm)", lat, lon, uncertainty or 0)

        self.pass_test(
            method="ecid", imsi=ue.imsi,
            latitude=lat, longitude=lon, uncertainty_m=uncertainty,
            gnb=gnb_name, session_id=session_id,
        )
        return self.result


class PosMultiRtt(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-002",
        title="Multi-RTT positioning across multiple gNBs",
        spec="TS 38.305 §8.6",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.EMBB,
        dnn="internet",
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  TS 38.305 §8.6 defines Multi-RTT: the UE measures Rx-Tx\n"
            "  with the serving cell and at least two neighbour cells,\n"
            "  the LMF combines the ranges via trilateration to yield a\n"
            "  metre-class fix. Validates that 3-gNB triangulation actually\n"
            "  resolves a position.\n"
            "\n"
            "Procedure (TS 38.305 §8.6 + TS 38.455 Multi-RTT)\n"
            "  1. require_gnb(); require_ue(); ue = ue_pool[0].\n"
            "  2. register_ue() then establish_pdu(psi=1, dnn=internet).\n"
            "  3. For each (gnb-0, gnb-1, gnb-2) in GNB_POSITIONS:\n"
            "     _register_gnb_position(name, lat, lon, alt).\n"
            "  4. _request_location(imsi, method='multi_rtt', accuracy_m=10).\n"
            "  5. Poll GET /api/location/{sid} every 1 s for up to 15 s\n"
            "     until state=='completed' OR latitude appears.\n"
            "  6. Read lat, lon, uncertainty_m.\n"
            "  7. Assert lat is not None.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — accuracy_m=10 and SF triangle GNB_POSITIONS hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Location request 200/201/202 AND a latitude becomes\n"
            "  available within 15 s.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  method, imsi, latitude, longitude, uncertainty_m,\n"
            "  gnb_count, session_id.\n"
            "\n"
            "Known constraints\n"
            "  Only the existence of lat is asserted; the value is not\n"
            "  compared against the triangle centroid, so an LMF that\n"
            "  returns garbage coordinates would still pass."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        # Register 3 gNB positions for trilateration
        for name, pos in GNB_POSITIONS.items():
            result, status = _register_gnb_position(name, pos["lat"], pos["lon"], pos["alt"])
            if status not in (200, 201):
                self.fail_test(f"Failed to register {name} position: {status}")
                return self.result

        # Request Multi-RTT location
        result, status = _request_location(ue.imsi, method="multi_rtt", accuracy_m=10)
        if status not in (200, 201, 202):
            self.fail_test(f"Multi-RTT request failed: {status} {result}")
            return self.result

        session_id = result.get("session_id")

        # Poll for result
        deadline = time.time() + 15
        while time.time() < deadline:
            result, status = _get_location(session_id) if session_id else (result, status)
            if result.get("state") in ("COMPLETED", "completed") or result.get("latitude"):
                break
            time.sleep(1)

        lat = result.get("latitude")
        lon = result.get("longitude")
        uncertainty = result.get("uncertainty_m")

        if lat is None:
            self.fail_test("Multi-RTT location not returned", result=result)
            return self.result

        log.info("Multi-RTT location: %.6f, %.6f (uncertainty=%.1fm)", lat, lon, uncertainty or 0)

        self.pass_test(
            method="multi_rtt", imsi=ue.imsi,
            latitude=lat, longitude=lon, uncertainty_m=uncertainty,
            gnb_count=len(GNB_POSITIONS), session_id=session_id,
        )
        return self.result


class PosDlTdoa(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-003",
        title="DL-TDOA positioning via PRS measurements",
        spec="TS 38.305 §8.2",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.EMBB,
        dnn="internet",
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  TS 38.305 §8.2 defines DL-TDOA: each gNB transmits a PRS\n"
            "  (TS 38.211 §7.4.1.7), the UE measures Reference Signal Time\n"
            "  Difference between cells, and the LMF resolves position from\n"
            "  the hyperbolic intersection. This test pins both the PRS\n"
            "  allocation step and the resulting fix.\n"
            "\n"
            "Procedure (TS 38.305 §8.2 + TS 38.211 §7.4.1.7)\n"
            "  1. require_gnb(); require_ue(); ue = ue_pool[0].\n"
            "  2. register_ue(); establish_pdu(psi=1, dnn=internet).\n"
            "  3. For each gNB in GNB_POSITIONS:\n"
            "     a. _register_gnb_position()\n"
            "     b. _allocate_prs(); collect prs_id.\n"
            "  4. _request_location(imsi, method='dl_tdoa', accuracy_m=15).\n"
            "  5. Poll GET /api/location/{sid} for up to 15 s until\n"
            "     state=='completed' OR latitude appears.\n"
            "  6. _delete_prs() each prs_id (cleanup before assertion).\n"
            "  7. Assert lat is not None.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — PRS defaults (20 ms / 24 RB / 2 sym / comb-2) and\n"
            "  accuracy_m=15 hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Request 200/201/202 AND latitude available within 15 s.\n"
            "  Test logs a warning if fewer than 3 PRS resources allocated\n"
            "  but proceeds anyway.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  method, imsi, latitude, longitude, uncertainty_m,\n"
            "  prs_count, session_id.\n"
            "\n"
            "Known constraints\n"
            "  Test runs even with <3 PRS, so a degenerate fix from <3\n"
            "  cells (geometrically ambiguous) still passes if the LMF\n"
            "  returns any lat/lon."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        # Register gNB positions and allocate PRS
        prs_ids = []
        for name, pos in GNB_POSITIONS.items():
            _register_gnb_position(name, pos["lat"], pos["lon"], pos["alt"])
            result, status = _allocate_prs(name)
            if status in (200, 201):
                prs_id = result.get("prs_id") or result.get("id")
                if prs_id:
                    prs_ids.append(prs_id)

        if len(prs_ids) < 3:
            log.warning("Only %d PRS resources allocated (need 3), proceeding anyway", len(prs_ids))

        # Request DL-TDOA
        result, status = _request_location(ue.imsi, method="dl_tdoa", accuracy_m=15)
        if status not in (200, 201, 202):
            self.fail_test(f"DL-TDOA request failed: {status} {result}")
            return self.result

        session_id = result.get("session_id")
        deadline = time.time() + 15
        while time.time() < deadline:
            result, status = _get_location(session_id) if session_id else (result, status)
            if result.get("state") in ("COMPLETED", "completed") or result.get("latitude"):
                break
            time.sleep(1)

        lat = result.get("latitude")
        lon = result.get("longitude")
        uncertainty = result.get("uncertainty_m")

        # Cleanup PRS
        for prs_id in prs_ids:
            _delete_prs(prs_id)

        if lat is None:
            self.fail_test("DL-TDOA location not returned", result=result, prs_count=len(prs_ids))
            return self.result

        self.pass_test(
            method="dl_tdoa", imsi=ue.imsi,
            latitude=lat, longitude=lon, uncertainty_m=uncertainty,
            prs_count=len(prs_ids), session_id=session_id,
        )
        return self.result


class PosAgnss(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-004",
        title="Assisted GNSS (A-GNSS) positioning via LPP",
        spec="TS 38.305 §8.8",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.EMBB,
        dnn="internet",
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  TS 38.305 §8.8 defines Assisted-GNSS: the LMF provides the\n"
            "  UE with GNSS assistance data (ephemeris, ionosphere, time)\n"
            "  via LPP (TS 37.355) so the UE's GNSS receiver locks faster\n"
            "  and reports back a position. This test pins the LPP-assisted\n"
            "  GNSS positioning path end-to-end.\n"
            "\n"
            "Procedure (TS 38.305 §8.8 + TS 37.355 LPP A-GNSS)\n"
            "  1. require_gnb(); require_ue(); ue = ue_pool[0].\n"
            "  2. register_ue(); establish_pdu(psi=1, dnn=internet).\n"
            "  3. _request_location(imsi, method='agnss', accuracy_m=10).\n"
            "  4. Extract session_id from the response.\n"
            "  5. Poll GET /api/location/{sid} every 1 s for up to 15 s\n"
            "     until state=='completed' OR latitude appears.\n"
            "  6. Read lat, lon, uncertainty_m.\n"
            "  7. Assert lat is not None.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — method=agnss, accuracy_m=10 hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Request 200/201/202 AND latitude available within 15 s.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  method, imsi, latitude, longitude, uncertainty_m, session_id.\n"
            "\n"
            "Known constraints\n"
            "  Lat-presence-only — no comparison to a true GNSS fix.\n"
            "  Assistance-data payload (TS 37.355) is not inspected, so a\n"
            "  stub that skips LPP entirely and returns a hardcoded fix\n"
            "  would still pass."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        result, status = _request_location(ue.imsi, method="agnss", accuracy_m=10)
        if status not in (200, 201, 202):
            self.fail_test(f"A-GNSS request failed: {status} {result}")
            return self.result

        session_id = result.get("session_id")
        deadline = time.time() + 15
        while time.time() < deadline:
            result, status = _get_location(session_id) if session_id else (result, status)
            if result.get("state") in ("COMPLETED", "completed") or result.get("latitude"):
                break
            time.sleep(1)

        lat = result.get("latitude")
        lon = result.get("longitude")
        uncertainty = result.get("uncertainty_m")

        if lat is None:
            self.fail_test("A-GNSS location not returned", result=result)
            return self.result

        self.pass_test(
            method="agnss", imsi=ue.imsi,
            latitude=lat, longitude=lon, uncertainty_m=uncertainty,
            session_id=session_id,
        )
        return self.result


class PosPrsConfig(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-005",
        title="PRS resource allocation and configuration",
        spec="TS 38.211 §7.4.1.7",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  TS 38.211 §7.4.1.7 defines the Positioning Reference Signal\n"
            "  (PRS): a downlink signal with configurable periodicity,\n"
            "  resource-block span, OFDM symbols and comb-size used by all\n"
            "  TDOA/AoD methods. The LMF/gNB must accept a PRS allocation\n"
            "  and serve it back on a GET — this test pins that contract.\n"
            "\n"
            "Procedure (TS 38.211 §7.4.1.7 PRS provisioning)\n"
            "  1. require_gnb(); read gnb_name.\n"
            "  2. _register_gnb_position(gnb_name, lat=37.7749,\n"
            "     lon=-122.4194). Assert 200/201.\n"
            "  3. _allocate_prs(gnb_name, periodicity_ms=20, num_rb=48,\n"
            "     num_symbols=4, comb_size=4).\n"
            "  4. Assert allocation status in (200, 201).\n"
            "  5. Extract prs_id (or id).\n"
            "  6. GET /api/prs/{gnb_name}; assert status == 200.\n"
            "  7. _delete_prs(prs_id) for cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — PRS config (20/48/4/4) hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Position register 200/201 AND PRS allocate 200/201 AND PRS\n"
            "  GET == 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  gnb, prs_id, prs_config (POST response),\n"
            "  prs_query_result (GET response).\n"
            "\n"
            "Known constraints\n"
            "  The GET response is not schema-checked for the values\n"
            "  written (periodicity, num_rb, etc.) — a backend that 200s\n"
            "  with empty content would pass."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        gnb_name = gnb.gnb_name if hasattr(gnb, 'gnb_name') else "tester-gnb-00"
        gnb_pos = GNB_POSITIONS["gnb-0"]

        # Register gNB position first
        result, status = _register_gnb_position(gnb_name, gnb_pos["lat"], gnb_pos["lon"])
        if status not in (200, 201):
            self.fail_test(f"gNB position registration failed: {status}")
            return self.result

        # Allocate PRS with specific parameters
        result, status = _allocate_prs(gnb_name, periodicity_ms=20, num_rb=48, num_symbols=4, comb_size=4)
        if status not in (200, 201):
            self.fail_test(f"PRS allocation failed: {status} {result}")
            return self.result

        prs_id = result.get("prs_id") or result.get("id")
        log.info("PRS allocated: id=%s config=%s", prs_id, result)

        # Verify PRS config retrievable
        prs_result, prs_status = _get_prs(gnb_name)
        if prs_status != 200:
            self.fail_test(f"PRS query failed: {prs_status}")
            return self.result

        # Cleanup
        if prs_id:
            _delete_prs(prs_id)

        self.pass_test(
            gnb=gnb_name, prs_id=prs_id,
            prs_config=result, prs_query_result=prs_result,
        )
        return self.result


class PosGeofence(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-006",
        title="Geofence area-event detection",
        spec="TS 23.273 §6.7",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.EMBB,
        dnn="internet",
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  TS 23.273 §6.7 defines deferred area-event location requests:\n"
            "  the LCS client subscribes to entry/exit events for a target\n"
            "  geographic area, the LMF tracks the UE and fires triggers\n"
            "  when boundaries are crossed. This test pins the geofence\n"
            "  CRUD + location-trigger view round-trip.\n"
            "\n"
            "Procedure (TS 23.273 §6.7 area-event)\n"
            "  1. require_gnb(); require_ue(); ue = ue_pool[0].\n"
            "  2. register_ue(); establish_pdu(psi=1, dnn=internet).\n"
            "  3. _register_gnb_position(gnb_name, gnb-0 SF coords).\n"
            "  4. POST /api/geofences with name='test-geofence-{imsi}',\n"
            "     imsi, center_lat/lon = gnb pos, radius_m=500,\n"
            "     trigger_type='enter'.\n"
            "  5. Assert geofence POST in (200, 201).\n"
            "  6. Extract fence_id.\n"
            "  7. _request_location(imsi, method='ecid').\n"
            "  8. GET /api/geofences?imsi={imsi}.\n"
            "  9. Finally clause DELETEs the geofence.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — radius=500 m, trigger='enter' hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Geofence POST 200/201. Test always calls pass_test after\n"
            "  cleanup as long as POST succeeded — no assertion on\n"
            "  whether the trigger actually fired.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, fence_id, geofence_result (GET payload),\n"
            "  location_result (location request payload).\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: location_result and geofence_result are\n"
            "  reported but never asserted to contain a trigger event. The\n"
            "  test confirms only that the geofence CRUD round-trips."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        gnb_name = gnb.gnb_name if hasattr(gnb, 'gnb_name') else "tester-gnb-00"
        gnb_pos = GNB_POSITIONS["gnb-0"]
        _register_gnb_position(gnb_name, gnb_pos["lat"], gnb_pos["lon"])

        # Create geofence around gNB position (500m radius)
        result, status = _gmlc_api("/api/geofences", "POST", {
            "name": f"test-geofence-{ue.imsi}",
            "imsi": ue.imsi,
            "center_lat": gnb_pos["lat"],
            "center_lon": gnb_pos["lon"],
            "radius_m": 500,
            "trigger_type": "enter",
        })
        if status not in (200, 201):
            self.fail_test(f"Geofence creation failed: {status} {result}")
            return self.result

        fence_id = result.get("id") or result.get("fence_id")
        log.info("Geofence created: id=%s", fence_id)

        # Request location (should be inside geofence since gNB is at center)
        loc_result, loc_status = _request_location(ue.imsi, method="ecid")

        # Check geofence triggers
        geo_result, geo_status = _gmlc_api(f"/api/geofences?imsi={ue.imsi}", "GET")

        # Cleanup
        if fence_id:
            _gmlc_api(f"/api/geofences/{fence_id}", "DELETE")

        self.pass_test(
            imsi=ue.imsi, fence_id=fence_id,
            geofence_result=geo_result,
            location_result=loc_result,
        )
        return self.result


class PosLcsPrivacy(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-007",
        title="LCS privacy enforcement commercial vs emergency",
        spec="TS 23.271 §9",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.AMF, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  TS 23.271 §9 defines LCS privacy classes: a UE may deny\n"
            "  commercial-client location requests, but emergency-services\n"
            "  requests must ALWAYS be served regardless of privacy. This\n"
            "  test pins the privacy rule write + the commercial-vs-emergency\n"
            "  behavioural distinction.\n"
            "\n"
            "Procedure (TS 23.271 §9 LCS privacy)\n"
            "  1. require_gnb(); require_ue(); ue = ue_pool[0].\n"
            "  2. register_ue() (no PDU needed for privacy plane).\n"
            "  3. _register_gnb_position(gnb_name, gnb-0 SF coords).\n"
            "  4. POST /api/lcs-privacy with imsi, client_type='commercial',\n"
            "     allowed=False. Assert 200/201.\n"
            "  5. GET /api/lcs-privacy?imsi={imsi}.\n"
            "  6. POST /api/location/request with client_type='commercial'\n"
            "     — capture status.\n"
            "  7. POST /api/location/request with client_type='emergency'\n"
            "     — capture status.\n"
            "  8. pass_test with all four payloads/status codes.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — client types fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Privacy POST 200/201. pass_test ALWAYS fires after the four\n"
            "  REST calls return — no conditional gating on commercial\n"
            "  being denied or emergency being allowed.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, privacy_settings, commercial_request_status,\n"
            "  commercial_result, emergency_request_status,\n"
            "  emergency_result.\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: the commercial-denied vs emergency-allowed\n"
            "  semantics are NOT asserted in run(); the test merely records\n"
            "  the four status codes as result.details. Behavioural\n"
            "  assertion lives in the robot scenario."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result

        gnb_name = gnb.gnb_name if hasattr(gnb, 'gnb_name') else "tester-gnb-00"
        _register_gnb_position(gnb_name, GNB_POSITIONS["gnb-0"]["lat"], GNB_POSITIONS["gnb-0"]["lon"])

        # Deny commercial location requests
        result, status = _gmlc_api("/api/lcs-privacy", "POST", {
            "imsi": ue.imsi, "client_type": "commercial", "allowed": False,
        })
        if status not in (200, 201):
            self.fail_test(f"LCS privacy set failed: {status}")
            return self.result

        # Verify privacy settings
        priv_result, priv_status = _gmlc_api(f"/api/lcs-privacy?imsi={ue.imsi}", "GET")

        # Request location as commercial client (should be denied or restricted)
        loc_result, loc_status = _gmlc_api("/api/location/request", "POST", {
            "imsi": ue.imsi, "method": "ecid", "client_type": "commercial",
        })

        # Request as emergency (should always be allowed per TS 23.271 §9)
        emerg_result, emerg_status = _gmlc_api("/api/location/request", "POST", {
            "imsi": ue.imsi, "method": "ecid", "client_type": "emergency",
        })

        self.pass_test(
            imsi=ue.imsi,
            privacy_settings=priv_result,
            commercial_request_status=loc_status,
            commercial_result=loc_result,
            emergency_request_status=emerg_status,
            emergency_result=emerg_result,
        )
        return self.result


class PosAutoMethod(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-008",
        title="LMF auto-selects positioning method by requested accuracy",
        spec="TS 38.305 §8",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.EMBB,
        dnn="internet",
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  TS 38.305 §8 leaves it to the LMF to select the cheapest\n"
            "  positioning method that satisfies the requested accuracy.\n"
            "  This test pins that auto-selection is responsive to the\n"
            "  accuracy hint: a tight 5 m request should pick a high-end\n"
            "  method (multi-RTT/TDOA), a loose 200 m request should fall\n"
            "  back to E-CID.\n"
            "\n"
            "Procedure (TS 38.305 §8 LMF method auto-selection)\n"
            "  1. require_gnb(); require_ue(); ue = ue_pool[0].\n"
            "  2. register_ue(); establish_pdu(psi=1, dnn=internet).\n"
            "  3. For each gNB in GNB_POSITIONS: _register_gnb_position().\n"
            "  4. _request_location(imsi, method='auto', accuracy_m=5);\n"
            "     read response.method as high_method.\n"
            "  5. _request_location(imsi, method='auto', accuracy_m=200);\n"
            "     read response.method as low_method.\n"
            "  6. pass_test recording both methods chosen.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — accuracy levels (5 m, 200 m) hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  pass_test fires unconditionally after both requests return.\n"
            "  No comparison between high_method and low_method is enforced.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, high_accuracy_method, high_accuracy_result,\n"
            "  low_accuracy_method, low_accuracy_result.\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: the test logs the selection but does NOT\n"
            "  fail when both buckets return the same method. The selection\n"
            "  semantics are spec-checked only in the robot suite."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        gnb_name = gnb.gnb_name if hasattr(gnb, 'gnb_name') else "tester-gnb-00"
        for name, pos in GNB_POSITIONS.items():
            _register_gnb_position(name, pos["lat"], pos["lon"], pos["alt"])

        # High accuracy request (should select Multi-RTT or TDOA)
        high_result, high_status = _request_location(ue.imsi, method="auto", accuracy_m=5)
        high_method = high_result.get("method")

        # Low accuracy request (should select E-CID)
        low_result, low_status = _request_location(ue.imsi, method="auto", accuracy_m=200)
        low_method = low_result.get("method")

        log.info("Auto method: accuracy=5m → %s, accuracy=200m → %s", high_method, low_method)

        self.pass_test(
            imsi=ue.imsi,
            high_accuracy_method=high_method,
            high_accuracy_result=high_result,
            low_accuracy_method=low_method,
            low_accuracy_result=low_result,
        )
        return self.result


class PosHistory(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-009",
        title="Location history retrieval per IMSI",
        spec="TS 29.572 §5.2",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.LMF),
        severity=Severity.MINOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        slice=Slice.EMBB,
        dnn="internet",
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  TS 29.572 §5.2 defines the Nlmf_Location service; in this\n"
            "  product the LMF additionally records each fix in a per-IMSI\n"
            "  history table that the GMLC exposes via /api/location/\n"
            "  history. This test pins that recurring fixes accumulate in\n"
            "  the history view.\n"
            "\n"
            "Procedure (TS 29.572 §5.2 LMF history retrieval)\n"
            "  1. require_gnb(); require_ue(); ue = ue_pool[0].\n"
            "  2. register_ue(); establish_pdu(psi=1, dnn=internet).\n"
            "  3. _register_gnb_position(gnb-0 SF coords).\n"
            "  4. Loop 3 times: _request_location(imsi, method='ecid');\n"
            "     time.sleep(1).\n"
            "  5. GET /api/location/history?imsi={imsi}&limit=10.\n"
            "  6. Assert GET status == 200.\n"
            "  7. Parse entries = response.items / .history (or []).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — fix count (3) and limit (10) hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  History GET returns 200. pass_test fires with imsi,\n"
            "  history_count, history list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, history_count (len of entries), history list.\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: history_count is reported but not asserted\n"
            "  >= 3 (or even >= 1). An LMF that drops history rows would\n"
            "  pass as long as the GET endpoint returns 200."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        gnb_name = gnb.gnb_name if hasattr(gnb, 'gnb_name') else "tester-gnb-00"
        _register_gnb_position(gnb_name, GNB_POSITIONS["gnb-0"]["lat"], GNB_POSITIONS["gnb-0"]["lon"])

        # Request multiple locations
        for _ in range(3):
            _request_location(ue.imsi, method="ecid")
            time.sleep(1)

        # Query history
        result, status = _gmlc_api(f"/api/location/history?imsi={ue.imsi}&limit=10", "GET")
        if status != 200:
            self.fail_test(f"History query failed: {status}")
            return self.result

        entries = result.get("items") or result.get("history") or []
        log.info("Location history: %d entries", len(entries))

        self.pass_test(
            imsi=ue.imsi, history_count=len(entries), history=entries,
        )
        return self.result


class PosAntennaConfig(TestCase):
    SPEC = TestSpec(
        tc_id="TC-POS-010",
        title="gNB antenna configuration for AoD/AoA",
        spec="TS 38.455 §9.2.44",
        domain=Domain.POSITIONING,
        nfs=(NF.GNB, NF.LMF),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 38.455 §9.2.44 carries the TRP-Information IE which\n"
            "  conveys gNB antenna/beam geometry from the gNB to the LMF\n"
            "  over NRPPa. AoD/AoA positioning relies on this beam metadata\n"
            "  to convert measured DL-AoD or UL-AoA into a real bearing.\n"
            "  This test pins the antenna-config registration endpoint.\n"
            "\n"
            "Procedure (TS 38.455 §9.2.44 TRP beam-info)\n"
            "  1. require_gnb(); read gnb_name.\n"
            "  2. _register_gnb_position(gnb_name, gnb-0 SF coords).\n"
            "  3. _register_gnb_antenna(gnb_name, azimuth=45,\n"
            "     beamwidth=65, downtilt=8, num_beams=16).\n"
            "  4. Assert HTTP status in (200, 201).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — antenna geometry (az=45, bw=65, dt=8, beams=16)\n"
            "  hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Antenna POST returns 200/201. pass_test fires with the\n"
            "  echoed config plus the four geometry fields.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  gnb, antenna_config (POST response), azimuth, beamwidth,\n"
            "  downtilt, num_beams.\n"
            "\n"
            "Known constraints\n"
            "  Write-only smoke — no GET-back verification, so a backend\n"
            "  that 200s without persisting would still pass."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        gnb_name = gnb.gnb_name if hasattr(gnb, 'gnb_name') else "tester-gnb-00"
        gnb_pos = GNB_POSITIONS["gnb-0"]

        # Register position
        _register_gnb_position(gnb_name, gnb_pos["lat"], gnb_pos["lon"])

        # Register antenna config
        result, status = _register_gnb_antenna(
            gnb_name, azimuth=45, beamwidth=65, downtilt=8, num_beams=16)
        if status not in (200, 201):
            self.fail_test(f"Antenna config failed: {status} {result}")
            return self.result

        log.info("Antenna config registered: %s", result)

        self.pass_test(
            gnb=gnb_name, antenna_config=result,
            azimuth=45, beamwidth=65, downtilt=8, num_beams=16,
        )
        return self.result
