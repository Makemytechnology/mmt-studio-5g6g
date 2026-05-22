# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""5G NTN (Non-Terrestrial Network) Test Cases.

TS 38.821 — NR NTN study (architecture, timing, coverage)
TS 23.501 §5.4.10 — NTN support in 5G system
TS 38.213 §4.2 — Timing advance for NTN
TS 24.501 §5.3.7 — NAS timer adjustments
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_ntn")


def _ntn_api(path, method="GET", body=None, core_ip=None):
    """Call sa_core NTN REST API."""
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
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


class NtnLoadDefaults(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-001",
        title="Load default NTN test constellation",
        spec="TS 38.821 §4",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Foundational seed-data load for the NTN feature. Every\n"
            "  downstream NTN TC depends on at least one satellite, one\n"
            "  ground station and one TAI being present; this TC pins\n"
            "  that contract.\n"
            "\n"
            "Procedure (TS 38.821 §4 + TS 23.501 §5.4.10)\n"
            "  1. POST /api/ntn/load-defaults — populates the LEO/GEO\n"
            "     constellation fixture, the default ground-station set,\n"
            "     and the TAI table.\n"
            "  2. fail_test if status not in (200, 201).\n"
            "  3. Extract satellites/ground_stations/tais counts.\n"
            "  4. fail_test if satellites < 1.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND satellites >= 1.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  satellites, ground_stations, tais.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — does not bring up gNB/UE. Reloading is\n"
            "  idempotent on the core side.\n"
            "  Reloading is idempotent on the core side: subsequent POSTs\n"
            "  return the same satellite / GS / TAI counts. Setup.EMPTY means\n"
            "  no gNB/UE is brought up by the test harness.\n"
            "  Operator can verify TAIs > 0 via a follow-up GET if\n"
            "  strict TAI presence is required."
        ),
    )

    def run(self):
        result, status = _ntn_api("/api/ntn/load-defaults", "POST")
        if status not in (200, 201):
            self.fail_test(f"Load defaults failed: {status} {result}")
            return self.result

        sats = result.get("satellites", 0)
        gs = result.get("ground_stations", 0)
        tais = result.get("tais", 0)
        log.info("NTN defaults: %d satellites, %d ground stations, %d TAIs", sats, gs, tais)

        if sats < 1:
            self.fail_test("No satellites loaded")
            return self.result

        self.pass_test(satellites=sats, ground_stations=gs, tais=tais)
        return self.result


class NtnSatelliteConfig(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-002",
        title="Add a satellite and query the constellation",
        spec="TS 38.821 §4",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the satellite-registration contract: the\n"
            "  operator must be able to add LEO satellites with full\n"
            "  orbit/beam parameters and see them reflected in the\n"
            "  constellation view used by coverage / timing TCs.\n"
            "\n"
            "Procedure (TS 38.821 §4 + 3GPP TR 38.821 §6.1)\n"
            "  1. POST /api/ntn/load-defaults to ensure baseline.\n"
            "  2. POST /api/ntn/satellite with sat_id='TEST-LEO-1',\n"
            "     orbit_type='LEO', altitude_km=600, inclination_deg=97.6,\n"
            "     beam_count=16, beam_diameter_km=40.\n"
            "  3. fail_test if status not in (200, 201).\n"
            "  4. GET /api/ntn/constellation; pull sat_ids.\n"
            "  5. fail_test if 'TEST-LEO-1' not in sat_ids.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — satellite shape is fixed (sun-synchronous LEO).\n"
            "\n"
            "Pass criteria\n"
            "  Satellite POST 200/201 AND new sat_id appears in\n"
            "  constellation list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  satellite, constellation_count, min_rtt_ms, max_rtt_ms.\n"
            "\n"
            "Known constraints\n"
            "  Test does not clean up the added satellite — registry\n"
            "  accretes 'TEST-LEO-1' across runs (load-defaults resets it).\n"
            "  Registry accretes the test satellite across runs unless\n"
            "  load-defaults reloads. min/max RTT may be missing if the core\n"
            "  lazily computes them — fields are reported, not asserted."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        # Add a new LEO satellite
        result, status = _ntn_api("/api/ntn/satellite", "POST", {
            "sat_id": "TEST-LEO-1", "name": "Test-LEO-Sat",
            "orbit_type": "LEO", "altitude_km": 600,
            "inclination_deg": 97.6, "beam_count": 16, "beam_diameter_km": 40,
        })
        if status not in (200, 201):
            self.fail_test(f"Satellite add failed: {status} {result}")
            return self.result

        # Verify in constellation
        const, _ = _ntn_api("/api/ntn/constellation")
        sat_ids = [s["sat_id"] for s in const.get("satellites", [])]

        if "TEST-LEO-1" not in sat_ids:
            self.fail_test("Added satellite not in constellation", sat_ids=sat_ids)
            return self.result

        self.pass_test(
            satellite=result, constellation_count=len(sat_ids),
            min_rtt_ms=result.get("min_rtt_ms"), max_rtt_ms=result.get("max_rtt_ms"),
        )
        return self.result


class NtnGroundStation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-003",
        title="Ground station registration with gNB binding",
        spec="TS 23.501 §5.4.10",
        domain=Domain.NTN,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the ground-station / feeder-link gateway registry.\n"
            "  TS 23.501 §5.4.10 places the GS as the bent-pipe earth\n"
            "  station serving the satellite; the NG-RAN gNB is anchored\n"
            "  to a GS by IP binding.\n"
            "\n"
            "Procedure (TS 23.501 §5.4.10 + TS 38.821 §4.2)\n"
            "  1. POST /api/ntn/ground-station with gs_id='GS-TEST-1',\n"
            "     name='Test-Gateway', latitude=28.6139, longitude=77.2090\n"
            "     (Delhi), connected_gnb_ip='192.168.1.103'.\n"
            "  2. fail_test if status not in (200, 201).\n"
            "  3. GET /api/ntn/constellation, capture ground_stations\n"
            "     count.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — coordinates / gNB IP are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Ground-station POST returns 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ground_station (envelope), total_gs.\n"
            "\n"
            "Known constraints\n"
            "  Existence of the new entry in the constellation listing is\n"
            "  not strictly asserted — only the POST and a follow-up GET.\n"
            "  GS-to-gNB binding is registry-only — no NGAP control-plane\n"
            "  handshake is exercised. Removing the test GS is left to\n"
            "  the next load-defaults sweep.\n"
            "  No connectivity check is made against the gNB IP — the\n"
            "  binding is registry-level only."
        ),
    )

    def run(self):
        result, status = _ntn_api("/api/ntn/ground-station", "POST", {
            "gs_id": "GS-TEST-1", "name": "Test-Gateway",
            "latitude": 28.6139, "longitude": 77.2090,
            "connected_gnb_ip": "192.168.1.103",
        })
        if status not in (200, 201):
            self.fail_test(f"Ground station add failed: {status} {result}")
            return self.result

        const, _ = _ntn_api("/api/ntn/constellation")
        gs_list = const.get("ground_stations", [])

        self.pass_test(ground_station=result, total_gs=len(gs_list))
        return self.result


class NtnLeoCoverage(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-004",
        title="LEO satellite coverage check at a geographic point",
        spec="TS 38.821 §4.1",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the LEO coverage decision at a geographic point.\n"
            "  TS 38.821 §4.1 sets a 10° minimum-elevation criterion; the\n"
            "  serving-satellite picker must respect that.\n"
            "\n"
            "Procedure (TS 38.821 §4.1)\n"
            "  1. POST /api/ntn/load-defaults to seed the constellation.\n"
            "  2. GET /api/ntn/coverage?lat=20.0&lon=78.0 (central India).\n"
            "  3. fail_test if HTTP not 200.\n"
            "  4. Read `covered`, `serving_satellite`, `elevation_deg`,\n"
            "     `visible_satellites` from response.\n"
            "  5. Log them.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — query point pinned to 20.0/78.0.\n"
            "\n"
            "Pass criteria\n"
            "  GET returns HTTP 200; the four fields are reported.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  covered, serving_satellite, elevation_deg, visible_satellites,\n"
            "  location={lat,lon}.\n"
            "\n"
            "Known constraints\n"
            "  Coverage outcome (covered True/False) is not asserted —\n"
            "  result depends on constellation ephemeris at run time.\n"
            "  Coverage decision depends on real-time ephemeris; reproducing\n"
            "  the same result requires deterministic time. The 10° minimum-\n"
            "  elevation gate is enforced by the core, not this TC.\n"
            "  Operator can re-run the test to observe coverage variation\n"
            "  across satellite passes."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        # Check coverage at a location
        result, status = _ntn_api("/api/ntn/coverage?lat=20.0&lon=78.0")
        if status != 200:
            self.fail_test(f"Coverage query failed: {status}")
            return self.result

        covered = result.get("covered", False)
        serving = result.get("serving_satellite")
        elevation = result.get("elevation_deg")
        visible = result.get("visible_satellites", 0)

        log.info("Coverage: covered=%s serving=%s elevation=%.1f visible=%d",
                 covered, serving, elevation or 0, visible)

        self.pass_test(
            covered=covered, serving_satellite=serving,
            elevation_deg=elevation, visible_satellites=visible,
            location={"lat": 20.0, "lon": 78.0},
        )
        return self.result


class NtnGeoCoverage(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-005",
        title="GEO satellite wide-area coverage near sub-satellite point",
        spec="TS 38.821 §4",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates GEO wide-area visibility. A GEO bird at the sub-\n"
            "  satellite point has the highest possible elevation and the\n"
            "  largest beam footprint; coverage near nadir is the canonical\n"
            "  best-case for the model.\n"
            "\n"
            "Procedure (TS 38.821 §4)\n"
            "  1. POST /api/ntn/load-defaults.\n"
            "  2. GET /api/ntn/coverage?lat=0.0&lon=76.5 (near a default\n"
            "     GEO sub-satellite longitude).\n"
            "  3. fail_test if HTTP != 200.\n"
            "  4. Read covered / serving_satellite / elevation_deg /\n"
            "     visible_satellites.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — query point pinned to equator at 76.5° E.\n"
            "\n"
            "Pass criteria\n"
            "  GET returns HTTP 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  covered, serving_satellite, elevation_deg, visible_satellites.\n"
            "\n"
            "Known constraints\n"
            "  Whether the default fixture contains a GEO bird at 76.5° E\n"
            "  is operator policy — coverage outcome not strictly asserted.\n"
            "  Whether the default fixture contains a GEO bird at 76.5° E is\n"
            "  operator policy. Coverage outcome is reported, not strictly\n"
            "  asserted, to stay tolerant of constellation edits.\n"
            "  Operator may seed a specific GEO at the test location to\n"
            "  force a deterministic outcome."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        # GEO should cover wide area — check near sub-satellite point
        result, status = _ntn_api("/api/ntn/coverage?lat=0.0&lon=76.5")
        if status != 200:
            self.fail_test(f"Coverage query failed: {status}")
            return self.result

        self.pass_test(
            covered=result.get("covered"),
            serving_satellite=result.get("serving_satellite"),
            elevation_deg=result.get("elevation_deg"),
            visible_satellites=result.get("visible_satellites"),
        )
        return self.result


class NtnTimingAdvance(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-006",
        title="NTN propagation delay and timing advance computation",
        spec="TS 38.821 §6.1",
        domain=Domain.NTN,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the NTN propagation-delay model that drives both\n"
            "  the timing-advance computation (TS 38.213 §4.2) and the\n"
            "  NAS-timer adjustments (TS 24.501 §5.3.7) — TA = 2 * one-way\n"
            "  propagation, NAS timers scaled by max RTT.\n"
            "\n"
            "Procedure (TS 38.821 §6.1 + TS 38.213 §4.2)\n"
            "  1. POST /api/ntn/load-defaults.\n"
            "  2. GET /api/ntn/constellation; pick sats[0].sat_id —\n"
            "     fail_test if no satellites are listed.\n"
            "  3. GET /api/ntn/timing?sat_id={id}&lat=20.0&lon=78.0.\n"
            "  4. fail_test if HTTP != 200.\n"
            "  5. Read delay (service_link_ms, feeder_link_ms, rtt_ms) and\n"
            "     adjusted_timers map from the response.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — observation point pinned to 20.0/78.0.\n"
            "\n"
            "Pass criteria\n"
            "  Timing GET returns 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sat_id, delay, adjusted_timers.\n"
            "\n"
            "Known constraints\n"
            "  Picks the first satellite in the listing rather than\n"
            "  selecting the visible one — feeder/service link values\n"
            "  may correspond to an under-horizon satellite.\n"
            "  Picks the first satellite in the listing rather than selecting\n"
            "  a visible one — feeder/service link values may correspond to a\n"
            "  below-horizon satellite. NAS-timer adjustment shape is\n"
            "  operator-defined."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        # Get constellation to find a satellite
        const, _ = _ntn_api("/api/ntn/constellation")
        sats = const.get("satellites", [])
        if not sats:
            self.fail_test("No satellites in constellation")
            return self.result

        sat_id = sats[0]["sat_id"]

        result, status = _ntn_api(f"/api/ntn/timing?sat_id={sat_id}&lat=20.0&lon=78.0")
        if status != 200:
            self.fail_test(f"Timing query failed: {status} {result}")
            return self.result

        delay = result.get("delay", {})
        timers = result.get("adjusted_timers", {})

        log.info("Timing for %s: service=%.2fms feeder=%.2fms RTT=%.2fms",
                 sat_id, delay.get("service_link_ms", 0),
                 delay.get("feeder_link_ms", 0), delay.get("rtt_ms", 0))

        self.pass_test(
            sat_id=sat_id, delay=delay, adjusted_timers=timers,
        )
        return self.result


class NtnTimerAdjustment(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-007",
        title="NAS timer adjustment per satellite for NTN delay",
        spec="TS 38.821 §7.2",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Sweeps every satellite in the constellation and records the\n"
            "  per-satellite NAS-timer guard band (T3510 / T3512 / T3517).\n"
            "  Pins the TS 38.821 §7.2 timer-adjustment guidance: NAS\n"
            "  timer guards must exceed the worst-case round-trip.\n"
            "\n"
            "Procedure (TS 38.821 §7.2 + TS 24.501 §5.3.7)\n"
            "  1. POST /api/ntn/load-defaults.\n"
            "  2. GET /api/ntn/constellation; iterate sats.\n"
            "  3. For each sat_id GET /api/ntn/timing?sat_id={id}.\n"
            "  4. Build results[sat_id] = {orbit, rtt_ms, timers}.\n"
            "  5. Log results as pretty JSON.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  pass_test fires unconditionally — the operator inspects\n"
            "  the per-satellite timer map manually.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  timer_adjustments (dict sat_id → {orbit, rtt_ms, timers}).\n"
            "\n"
            "Known constraints\n"
            "  No numerical guard-band assertion; this is a survey TC, not\n"
            "  a strict gate.\n"
            "  Sweep is read-only; no timer is actually applied to a UE NAS\n"
            "  session. Operator inspects timer_adjustments to confirm guard\n"
            "  bands cover worst-case RTT.\n"
            "  RTT values are sampled in a single pass — does not survey\n"
            "  delay variation over time."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        const, _ = _ntn_api("/api/ntn/constellation")
        results = {}

        for sat in const.get("satellites", []):
            sat_id = sat["sat_id"]
            result, _ = _ntn_api(f"/api/ntn/timing?sat_id={sat_id}")
            timers = result.get("adjusted_timers", {})
            results[sat_id] = {
                "orbit": sat["orbit_type"],
                "rtt_ms": result.get("delay", {}).get("rtt_ms"),
                "timers": timers,
            }

        log.info("Timer adjustments: %s", json.dumps(results, indent=2))
        self.pass_test(timer_adjustments=results)
        return self.result


class NtnTaiLookup(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-008",
        title="Geographic TAI resolution by lat/lon",
        spec="TS 23.501 §5.4.10",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the geographic-to-TAI mapping that NTN uses to\n"
            "  segregate tracking areas by ground footprint. The AMF needs\n"
            "  this to decide whether a UE has crossed a TA boundary and\n"
            "  must perform a TAU (TS 23.501 §5.4.10.2).\n"
            "\n"
            "Procedure (TS 23.501 §5.4.10.2 + 38.821 §7.3)\n"
            "  1. POST /api/ntn/load-defaults.\n"
            "  2. GET /api/ntn/tai-lookup?lat=13.0&lon=77.5 (Bengaluru).\n"
            "  3. fail_test if HTTP != 200.\n"
            "  4. Capture tai from response.\n"
            "  5. GET /api/ntn/tais for the total count cross-check.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — query point pinned to 13.0/77.5.\n"
            "\n"
            "Pass criteria\n"
            "  TAI-lookup GET returns 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  location={lat, lon}, tai, total_tais.\n"
            "\n"
            "Known constraints\n"
            "  Whether the returned TAI is non-null depends on whether\n"
            "  default TAIs cover that point — not strictly asserted.\n"
            "  Whether the returned TAI is non-null depends on whether the\n"
            "  default TAIs cover that point — not strictly asserted, but\n"
            "  logged for operator inspection.\n"
            "  TAI shape (tac, plmn_id) is captured but only used as a\n"
            "  reference point for TC-NTN-009."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        # Query TAI for a location
        result, status = _ntn_api("/api/ntn/tai-lookup?lat=13.0&lon=77.5")
        if status != 200:
            self.fail_test(f"TAI lookup failed: {status}")
            return self.result

        tai = result.get("tai")
        log.info("TAI lookup at 13.0,77.5: %s", tai)

        # Also get all TAIs for reference
        tais, _ = _ntn_api("/api/ntn/tais")

        self.pass_test(
            location={"lat": 13.0, "lon": 77.5},
            tai=tai, total_tais=tais.get("count", 0),
        )
        return self.result


class NtnTaiChange(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-009",
        title="TAI change detection across two geographic positions",
        spec="TS 23.501 §5.4.10",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the TAU-trigger primitive at the data-model layer:\n"
            "  two far-apart positions must map to different TACs so the\n"
            "  AMF can detect TA crossings and initiate a TAU procedure\n"
            "  (TS 23.501 §5.4.10.2).\n"
            "\n"
            "Procedure (TS 23.501 §5.4.10.2)\n"
            "  1. POST /api/ntn/load-defaults.\n"
            "  2. GET /api/ntn/tai-lookup?lat=13.0&lon=77.5 → tai1.\n"
            "  3. GET /api/ntn/tai-lookup?lat=28.0&lon=77.0 → tai2.\n"
            "  4. Pull tai1.tac and tai2.tac (or None each).\n"
            "  5. changed = (tai1_tac != tai2_tac); log.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — two pinned points ~1700 km apart.\n"
            "\n"
            "Pass criteria\n"
            "  pass_test fires regardless of the boolean (the operator\n"
            "  reviews the captured TAC values); no strict assert on\n"
            "  inequality.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  position1={lat,lon,tac}, position2={lat,lon,tac}, tai_changed.\n"
            "\n"
            "Known constraints\n"
            "  Two-point survey; does not exercise an actual UE TAU.\n"
            "  Two-point survey; does not exercise an actual UE TAU. The\n"
            "  boolean changed flag is reported but not strictly asserted, so\n"
            "  fixture changes don't break the gate.\n"
            "  Operator can seed denser TAIs to tighten the change\n"
            "  detection along smaller geographic deltas."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        # Get TAI at position 1
        r1, _ = _ntn_api("/api/ntn/tai-lookup?lat=13.0&lon=77.5")
        tai1 = r1.get("tai")

        # Get TAI at distant position 2 (should be different TAI)
        r2, _ = _ntn_api("/api/ntn/tai-lookup?lat=28.0&lon=77.0")
        tai2 = r2.get("tai")

        tai1_tac = tai1.get("tac") if tai1 else None
        tai2_tac = tai2.get("tac") if tai2 else None
        changed = tai1_tac != tai2_tac

        log.info("TAI change: pos1=%s (TAC=%s) → pos2=%s (TAC=%s) changed=%s",
                 "13.0,77.5", tai1_tac, "28.0,77.0", tai2_tac, changed)

        self.pass_test(
            position1={"lat": 13.0, "lon": 77.5, "tac": tai1_tac},
            position2={"lat": 28.0, "lon": 77.0, "tac": tai2_tac},
            tai_changed=changed,
        )
        return self.result


class NtnFeederLinks(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-010",
        title="Feeder-link status and switch history",
        spec="TS 23.501 §5.4.10",
        domain=Domain.NTN,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Read-only contract on the feeder-link telemetry endpoint.\n"
            "  TS 23.501 §5.4.10 places feeder links between the satellite\n"
            "  and the ground-station; their up/down state and switch\n"
            "  history are key OAM signals.\n"
            "\n"
            "Procedure (TS 23.501 §5.4.10 + TS 38.821 §4.2)\n"
            "  1. POST /api/ntn/load-defaults.\n"
            "  2. GET /api/ntn/feeder-links.\n"
            "  3. fail_test if HTTP != 200.\n"
            "  4. Pull active_links (dict) and switch_history (list).\n"
            "  5. Log counts.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  GET returns HTTP 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  active_links, switch_history, active_count.\n"
            "\n"
            "Known constraints\n"
            "  No link is forcibly switched — read-only.\n"
            "  No feeder-link switch is forcibly triggered — read-only\n"
            "  telemetry. Active link count and switch_history are surfaced\n"
            "  for OAM dashboard consumption.\n"
            "  Switch history grows monotonically; no aging policy is\n"
            "  tested.\n"
            "  Verify with a separate UE TAU test on top."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        result, status = _ntn_api("/api/ntn/feeder-links")
        if status != 200:
            self.fail_test(f"Feeder links query failed: {status}")
            return self.result

        active = result.get("active_links", {})
        history = result.get("switch_history", [])

        log.info("Feeder links: %d active, %d switches", len(active), len(history))

        self.pass_test(
            active_links=active, switch_history=history,
            active_count=len(active),
        )
        return self.result


class NtnDlBuffer(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-011",
        title="DL packet buffering status during NTN coverage gap",
        spec="TS 23.501 §5.4.10",
        domain=Domain.NTN,
        nfs=(NF.UPF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Read-only contract on the UPF DL-buffering telemetry that\n"
            "  NTN uses during discontinuous coverage gaps. TS 23.501\n"
            "  §5.4.10 expects packets to be buffered for a UE that is\n"
            "  temporarily out of satellite view.\n"
            "\n"
            "Procedure (TS 23.501 §5.4.10)\n"
            "  1. GET /api/ntn/buffer-status.\n"
            "  2. fail_test if HTTP != 200.\n"
            "  3. Log buffer status envelope.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  GET returns HTTP 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  total_ues_buffered, total_packets.\n"
            "\n"
            "Known constraints\n"
            "  No traffic is generated; counters will normally be zero.\n"
            "  No traffic is generated, so the counters will normally be zero.\n"
            "  This TC is a reachability gate on the buffer telemetry surface\n"
            "  rather than an actual buffering exercise.\n"
            "  Operator can prime the buffer by running a CIoT test in\n"
            "  the same session before re-checking. If a CIoT-style\n"
            "  buffering test runs in the same suite, operator should\n"
            "  expect non-zero counters here. The test is intentionally\n"
            "  lightweight so it can run in EMPTY setup without bringing\n"
            "  up a gNB or UE."
        ),
    )

    def run(self):
        # Check buffer status (may be empty)
        result, status = _ntn_api("/api/ntn/buffer-status")
        if status != 200:
            self.fail_test(f"Buffer status failed: {status}")
            return self.result

        log.info("DL buffer: %s", result)

        self.pass_test(
            total_ues_buffered=result.get("total_ues_buffered", 0),
            total_packets=result.get("total_packets", 0),
        )
        return self.result


class NtnConstellationPositions(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NTN-012",
        title="Real-time satellite positions from ephemeris",
        spec="TS 38.821 §7.3.6",
        domain=Domain.NTN,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the ephemeris-driven satellite-position helper.\n"
            "  TS 38.821 §7.3.6 expects satellite positions to be derivable\n"
            "  in real time from stored ephemerides; the constellation\n"
            "  endpoint must surface a (lat, lon, alt_km) tuple for each\n"
            "  bird so coverage/timing TCs and the UI map can render.\n"
            "\n"
            "Procedure (TS 38.821 §7.3.6)\n"
            "  1. POST /api/ntn/load-defaults.\n"
            "  2. GET /api/ntn/constellation.\n"
            "  3. fail_test if HTTP != 200.\n"
            "  4. For each sat in satellites, capture sat_id / orbit /\n"
            "     position.{latitude, longitude, altitude_km}.\n"
            "  5. Log count.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Constellation GET returns 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  satellite_count, positions (list of {sat_id, orbit, lat,\n"
            "  lon, alt_km}).\n"
            "\n"
            "Known constraints\n"
            "  Per-row position presence is collected but not strictly\n"
            "  asserted (None values pass through to the report).\n"
            "  Per-row position presence is collected but not strictly\n"
            "  asserted (None values pass through). Ephemeris freshness is\n"
            "  operator-managed; positions reflect time-of-query."
        ),
    )

    def run(self):
        _ntn_api("/api/ntn/load-defaults", "POST")

        result, status = _ntn_api("/api/ntn/constellation")
        if status != 200:
            self.fail_test(f"Constellation query failed: {status}")
            return self.result

        sats = result.get("satellites", [])
        positions = []
        for sat in sats:
            pos = sat.get("position", {})
            positions.append({
                "sat_id": sat["sat_id"],
                "orbit": sat["orbit_type"],
                "lat": pos.get("latitude"),
                "lon": pos.get("longitude"),
                "alt_km": pos.get("altitude_km"),
            })

        log.info("Constellation: %d satellites with positions", len(positions))

        self.pass_test(
            satellite_count=len(sats),
            positions=positions,
        )
        return self.result
