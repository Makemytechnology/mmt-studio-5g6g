# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: UAS (Uncrewed Aerial Systems).

TS 23.256 -- Support of Uncrewed Aerial Systems (UAS) connectivity,
identification and tracking.
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

log = logging.getLogger("tester.tc_uas")


def _uas_api(path, method="GET", body=None):
    """Call SA Core UAS REST API."""
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


def _register_uav(imsi=None, uav_id_hint="UAV-TEST-001"):
    if imsi is None:
        # UAS registry stores the IMSI as a foreign key — it doesn't need
        # an NGAP/UPF cellular flow. Use the first baseline UE so the
        # foreign key is real, not a fabricated string.
        imsi = baseline.imsi("embb-bulk", 0)
    """Helper: register a UAV and return (result, status, uav_id_string).

    The route response carries both `id` (integer) and `uav_id` (string).
    Downstream calls expect a string in JSON bodies — TS 23.256 §5.2.5
    CAA-Level UAV ID is the canonical identifier — so we prefer the
    string `uav_id` field even when an integer `id` is present.
    """
    result, status = _uas_api("/api/uas/registry", "POST", {
        "imsi": imsi,
        "uav_id": uav_id_hint,
        "serial_number": "SN-TEST-00001",
        "manufacturer": "TestMfg",
        "model": "TestModel-X1",
        "max_speed_mps": 25.0,
        "max_altitude_m": 120.0,
    })
    uav_id = result.get("uav_id") or uav_id_hint
    return result, status, uav_id


class UasRegisterUav(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-001",
        title="Register and delete a UAV",
        spec="TS 23.256 §5.2",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the UAS registry (UDM-anchored). TS\n"
            "  23.256 §5.2 mandates UAV registration before any USS-side\n"
            "  authorisation; this TC pins the CRUD round-trip.\n"
            "\n"
            "Procedure (TS 23.256 §5.2 + TS 22.125)\n"
            "  1. _register_uav(uav_id_hint='UAV-{tc_id}') — POSTs\n"
            "     /api/uas/registry with imsi=baseline.imsi('embb-bulk',\n"
            "     0), uav_id, serial='SN-TEST-00001', manufacturer,\n"
            "     model, max_speed_mps=25.0, max_altitude_m=120.0.\n"
            "  2. fail_test if status not in (200, 201).\n"
            "  3. finally: DELETE /api/uas/registry/{uav_id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — envelope (25 m/s, 120 m AGL) is pinned to align\n"
            "  with sub-250-g UAV class rules.\n"
            "\n"
            "Pass criteria\n"
            "  Registry POST returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  uav_id, uav (full registry envelope).\n"
            "\n"
            "Known constraints\n"
            "  No NGAP/NAS attach for the UAV — imsi is a foreign key into\n"
            "  UDM, not an active subscriber session.\n"
            "  No NGAP/NAS attach for the UAV — imsi is a foreign key into\n"
            "  UDM, not an active subscriber session. Registry rows are\n"
            "  reaped on finally.\n"
            "  Operator may re-run with a different baseline imsi if the\n"
            "  first is held by another test."
        ),
    )

    def run(self):
        uav_id = None
        try:
            result, status, uav_id = _register_uav(uav_id_hint=f"UAV-{self.tc_id}")
            if status not in (200, 201):
                self.fail_test(f"Register UAV failed: {status} {result}")
                return self.result

            log.info("UAV registered: id=%s", uav_id)
            self.pass_test(uav_id=uav_id, uav=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if uav_id:
                _uas_api(f"/api/uas/registry/{uav_id}", "DELETE")
        return self.result


class UasAuthorizeFlight(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-002",
        title="Authorise a flight plan for a registered UAV",
        spec="TS 23.256 §5.2.4",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the USS-style flight-plan approval interface. TS 23.256\n"
            "  §5.2.4 makes the UAS NF authorise a flight against the UAV's\n"
            "  envelope and known NFZs before the UAV can take off.\n"
            "\n"
            "Procedure (TS 23.256 §5.2.4)\n"
            "  1. _register_uav() to obtain uav_id.\n"
            "  2. POST /api/uas/authorize-flight with:\n"
            "       uav_id,\n"
            "       flight_plan.waypoints=[(37.7749, -122.4194, 50),\n"
            "                              (37.7750, -122.4180, 60)],\n"
            "       flight_plan.max_altitude=100.\n"
            "  3. fail_test if status not in (200, 201).\n"
            "  4. Capture authorized flag (authorized or status fallback).\n"
            "  5. finally: DELETE UAV from registry.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — waypoints inside the well-known SF demo area.\n"
            "\n"
            "Pass criteria\n"
            "  Authorize POST returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  uav_id, authorization (full envelope).\n"
            "\n"
            "Known constraints\n"
            "  No NFZ intersection check is forced — TC-UAS-007 covers the\n"
            "  deny path.\n"
            "  No NFZ intersection check is forced — TC-UAS-007 covers the\n"
            "  deny path. The authorized flag is reported but not strictly\n"
            "  asserted to be True."
        ),
    )

    def run(self):
        uav_id = None
        try:
            result, status, uav_id = _register_uav(uav_id_hint=f"UAV-{self.tc_id}")
            if status not in (200, 201):
                self.fail_test(f"Register UAV failed: {status} {result}")
                return self.result

            auth_result, auth_status = _uas_api(
                "/api/uas/authorize-flight", "POST", {
                    "uav_id": uav_id,
                    "flight_plan": {
                        "waypoints": [
                            {"lat": 37.7749, "lon": -122.4194, "alt": 50},
                            {"lat": 37.7750, "lon": -122.4180, "alt": 60},
                        ],
                        "max_altitude": 100,
                    },
                })
            if auth_status not in (200, 201):
                self.fail_test(f"Authorize flight failed: {auth_status} {auth_result}")
                return self.result

            authorized = auth_result.get("authorized", auth_result.get("status"))
            log.info("Flight authorization: %s", authorized)
            self.pass_test(uav_id=uav_id, authorization=auth_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if uav_id:
                _uas_api(f"/api/uas/registry/{uav_id}", "DELETE")
        return self.result


class UasUpdatePosition(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-003",
        title="Update a UAV position and verify readback",
        spec="TS 23.256 §5.2.5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the UAV tracking endpoint. TS 23.256 §5.2.5 makes\n"
            "  the UAS NF responsible for collecting position reports from\n"
            "  the UAV and feeding them to the USS and the Remote-ID\n"
            "  broadcast path.\n"
            "\n"
            "Procedure (TS 23.256 §5.2.5)\n"
            "  1. _register_uav() — obtain uav_id.\n"
            "  2. POST /api/uas/position with uav_id, latitude=37.7749,\n"
            "     longitude=-122.4194, altitude_m=80.0, heading_deg=270.0,\n"
            "     speed_mps=12.5.\n"
            "  3. fail_test if status not in (200, 201).\n"
            "  4. GET /api/uas/position/{uav_id}.\n"
            "  5. fail_test if HTTP != 200.\n"
            "  6. fail_test if latitude or longitude missing from response.\n"
            "  7. finally: DELETE UAV.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — position values pinned.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET 200 AND latitude/longitude present.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  uav_id, position (the full readback envelope).\n"
            "\n"
            "Known constraints\n"
            "  Values are not asserted to round-trip exactly (sub-meter\n"
            "  precision differences tolerated).\n"
            "  Values are not asserted to round-trip exactly (sub-meter\n"
            "  precision differences tolerated). The remote-id broadcast\n"
            "  is exercised in TC-UAS-006."
        ),
    )

    def run(self):
        uav_id = None
        try:
            result, status, uav_id = _register_uav(uav_id_hint=f"UAV-{self.tc_id}")
            if status not in (200, 201):
                self.fail_test(f"Register UAV failed: {status} {result}")
                return self.result

            # Update position
            pos_result, pos_status = _uas_api("/api/uas/position", "POST", {
                "uav_id": uav_id,
                "latitude": 37.7749,
                "longitude": -122.4194,
                "altitude_m": 80.0,
                "heading_deg": 270.0,
                "speed_mps": 12.5,
            })
            if pos_status not in (200, 201):
                self.fail_test(f"Update position failed: {pos_status} {pos_result}")
                return self.result

            # Read back
            get_result, get_status = _uas_api(f"/api/uas/position/{uav_id}")
            if get_status != 200:
                self.fail_test(f"GET position failed: {get_status} {get_result}")
                return self.result

            lat = get_result.get("latitude")
            lon = get_result.get("longitude")
            log.info("UAV position: lat=%s lon=%s", lat, lon)

            if lat is None or lon is None:
                self.fail_test("Position fields missing", position=get_result)
                return self.result

            self.pass_test(uav_id=uav_id, position=get_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if uav_id:
                _uas_api(f"/api/uas/registry/{uav_id}", "DELETE")
        return self.result


class UasNoFlyZone(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-004",
        title="Create and delete a no-fly zone (NFZ)",
        spec="TS 23.256 §5.2.4",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the no-fly-zone registry CRUD. TS 23.256 §5.2.4\n"
            "  positions the NFZ list as an input to flight-plan auth;\n"
            "  this is the editor primitive for the operator/regulator.\n"
            "\n"
            "Procedure (TS 23.256 §5.2.4)\n"
            "  1. POST /api/uas/no-fly-zones with name='test-nfz-airport',\n"
            "     bounding box lat_min=37.610..lat_max=37.630,\n"
            "     lon_min=-122.400..lon_max=-122.370,\n"
            "     alt_max_m=150.0, reason='Airport proximity'.\n"
            "  2. fail_test if status not in (200, 201).\n"
            "  3. Capture zone_id (id or zone_id).\n"
            "  4. finally: DELETE the NFZ.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — SFO-shaped bounding box.\n"
            "\n"
            "Pass criteria\n"
            "  NFZ POST returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  zone_id, zone (full envelope).\n"
            "\n"
            "Known constraints\n"
            "  Enforcement is exercised separately (TC-UAS-007); this TC\n"
            "  is pure registry CRUD.\n"
            "  Enforcement is exercised separately (TC-UAS-007); this TC is\n"
            "  pure registry CRUD. Bounding box geometry / altitude shape\n"
            "  validation is the core's responsibility.\n"
            "  NFZ-vs-flight-plan intersection is the core's job — only\n"
            "  the registry round-trip is the gate here."
        ),
    )

    def run(self):
        zone_id = None
        try:
            result, status = _uas_api("/api/uas/no-fly-zones", "POST", {
                "name": "test-nfz-airport",
                "lat_min": 37.610,
                "lat_max": 37.630,
                "lon_min": -122.400,
                "lon_max": -122.370,
                "alt_max_m": 150.0,
                "reason": "Airport proximity",
            })
            if status not in (200, 201):
                self.fail_test(f"Create no-fly zone failed: {status} {result}")
                return self.result

            zone_id = result.get("id") or result.get("zone_id")
            log.info("No-fly zone created: id=%s", zone_id)
            self.pass_test(zone_id=zone_id, zone=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if zone_id:
                _uas_api(f"/api/uas/no-fly-zones/{zone_id}", "DELETE")
        return self.result


class UasC2Session(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-005",
        title="Establish, query and tear down a C2 session",
        spec="TS 23.256 §5.5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates the UAV Command-and-Control (C2) session lifecycle\n"
            "  primitive. TS 23.256 §5.5 places the C2 link as a special\n"
            "  PDU session with elevated QoS (low PDB / low loss) between\n"
            "  controller and UAV, anchored at the SMF.\n"
            "\n"
            "Procedure (TS 23.256 §5.5)\n"
            "  1. _register_uav() — obtain uav_id.\n"
            "  2. POST /api/uas/c2/establish with uav_id,\n"
            "     controller_id='CTRL-001', qos_5qi=3 (V2X messages QoS).\n"
            "  3. fail_test if establish not in (200, 201).\n"
            "  4. Capture c2_id (id or c2_id).\n"
            "  5. GET /api/uas/c2/status/{c2_id}.\n"
            "  6. fail_test if HTTP != 200.\n"
            "  7. finally: DELETE C2 session AND DELETE UAV.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — controller_id and 5QI pinned.\n"
            "\n"
            "Pass criteria\n"
            "  Establish 200/201 AND status GET 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  c2_id, c2_session, status (the status envelope).\n"
            "\n"
            "Known constraints\n"
            "  No actual data-plane is wired up — the SMF anchor and any\n"
            "  GTP-U binding are not exercised.\n"
            "  No actual data-plane is wired up — the SMF anchor and any\n"
            "  GTP-U binding are not exercised. C2 session row is reaped on\n"
            "  finally."
        ),
    )

    def run(self):
        uav_id = None
        c2_id = None
        try:
            result, status, uav_id = _register_uav(uav_id_hint=f"UAV-{self.tc_id}")
            if status not in (200, 201):
                self.fail_test(f"Register UAV failed: {status} {result}")
                return self.result

            # Establish C2
            c2_result, c2_status = _uas_api("/api/uas/c2/establish", "POST", {
                "uav_id": uav_id,
                "controller_id": "CTRL-001",
                "qos_5qi": 3,
            })
            if c2_status not in (200, 201):
                self.fail_test(f"C2 establish failed: {c2_status} {c2_result}")
                return self.result

            c2_id = c2_result.get("id") or c2_result.get("c2_id")
            log.info("C2 session established: id=%s", c2_id)

            # Query status
            status_result, st_status = _uas_api(f"/api/uas/c2/status/{c2_id}")
            if st_status != 200:
                self.fail_test(f"C2 status query failed: {st_status}")
                return self.result

            self.pass_test(c2_id=c2_id, c2_session=c2_result, status=status_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if c2_id:
                _uas_api(f"/api/uas/c2/{c2_id}", "DELETE")
            if uav_id:
                _uas_api(f"/api/uas/registry/{uav_id}", "DELETE")
        return self.result


class UasRemoteId(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-006",
        title="Remote-ID broadcast payload meets ASTM F3411 fields",
        spec="TS 23.256 §5.2.5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the Remote-ID broadcast payload against the ASTM F3411\n"
            "  mandatory field set (uav_id, operator/serial, position).\n"
            "  TS 23.256 §5.2.5 routes UAV position out through this\n"
            "  endpoint for Remote-ID broadcasters.\n"
            "\n"
            "Procedure (TS 23.256 §5.2.5 + ASTM F3411-22)\n"
            "  1. _register_uav() — obtain uav_id.\n"
            "  2. POST /api/uas/position with lat=37.7749, lon=-122.4194,\n"
            "     altitude_m=50.0, heading=90°, speed=5 m/s.\n"
            "  3. GET /api/uas/remote-id/{uav_id}.\n"
            "  4. fail_test if HTTP != 200.\n"
            "  5. For each required field in (uav_id, serial_number,\n"
            "     latitude, longitude): fail_test if missing.\n"
            "  6. finally: DELETE UAV.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pinned waypoint.\n"
            "\n"
            "Pass criteria\n"
            "  GET 200 AND all four mandatory ASTM fields are present.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  uav_id, remote_id (the full payload).\n"
            "\n"
            "Known constraints\n"
            "  Field presence only — values are not cross-checked against\n"
            "  the registry/position inputs.\n"
            "  Field presence only — values are not cross-checked against\n"
            "  the registry/position inputs. ASTM F3411 optional fields are\n"
            "  not asserted."
        ),
    )

    def run(self):
        uav_id = None
        try:
            result, status, uav_id = _register_uav(uav_id_hint=f"UAV-{self.tc_id}")
            if status not in (200, 201):
                self.fail_test(f"Register UAV failed: {status} {result}")
                return self.result

            # Update position so remote-id has location data
            _uas_api("/api/uas/position", "POST", {
                "uav_id": uav_id,
                "latitude": 37.7749,
                "longitude": -122.4194,
                "altitude_m": 50.0,
                "heading_deg": 90.0,
                "speed_mps": 5.0,
            })

            # Query remote ID
            rid_result, rid_status = _uas_api(f"/api/uas/remote-id/{uav_id}")
            if rid_status != 200:
                self.fail_test(f"Remote ID query failed: {rid_status} {rid_result}")
                return self.result

            # Verify ASTM F3411 expected fields
            for field in ("uav_id", "serial_number", "latitude", "longitude"):
                if field not in rid_result:
                    self.fail_test(f"Remote ID missing field: {field}",
                                   remote_id=rid_result)
                    return self.result

            log.info("Remote ID verified for UAV %s", uav_id)
            self.pass_test(uav_id=uav_id, remote_id=rid_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if uav_id:
                _uas_api(f"/api/uas/registry/{uav_id}", "DELETE")
        return self.result


class UasNoFlyZoneViolation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-007",
        title="Flight plan crossing an NFZ is denied with violations",
        spec="TS 23.256 §5.2.4",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative-path enforcement gate for NFZ honouring. TS 23.256\n"
            "  §5.2.4 mandates that the USS/UAS NF reject a flight plan\n"
            "  intersecting a no-fly zone, and that the response surface\n"
            "  the offending violations to the operator.\n"
            "\n"
            "Procedure (TS 23.256 §5.2.4)\n"
            "  1. POST /api/uas/no-fly-zones carving out a small SF NFZ.\n"
            "  2. fail_test if NFZ POST not in (200, 201); capture zone_id.\n"
            "  3. _register_uav() to obtain uav_id.\n"
            "  4. POST /api/uas/authorize-flight with two waypoints, the\n"
            "     second deliberately inside the NFZ (37.7500/-122.4500).\n"
            "  5. fail_test if HTTP != 200.\n"
            "  6. fail_test if response.authorized is truthy.\n"
            "  7. fail_test if violations field is empty / missing.\n"
            "  8. finally: DELETE NFZ AND DELETE UAV.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — NFZ box and inside waypoint pinned.\n"
            "\n"
            "Pass criteria\n"
            "  authorize-flight returns HTTP 200, authorized=false, and\n"
            "  violations is non-empty.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  uav_id, zone_id, violations.\n"
            "\n"
            "Known constraints\n"
            "  Intersection check is whatever the core implements; this\n"
            "  TC trusts that and only checks the response shape."
        ),
    )

    def run(self):
        zone_id = None
        uav_id = None
        try:
            # Carve out a small NFZ.
            z, zs = _uas_api("/api/uas/no-fly-zones", "POST", {
                "name": "tc_uas_007_nfz",
                "lat_min": 37.7000, "lat_max": 37.8000,
                "lon_min": -122.5000, "lon_max": -122.4000,
                "alt_max_m": 200.0, "reason": "Test NFZ",
            })
            if zs not in (200, 201):
                self.fail_test(f"NFZ create failed: {zs} {z}")
                return self.result
            zone_id = z.get("id") or z.get("zone_id")

            _, rs, uav_id = _register_uav(uav_id_hint=f"UAV-{self.tc_id}")
            if rs not in (200, 201):
                self.fail_test(f"Register UAV failed: {rs}")
                return self.result

            # Flight plan with a waypoint inside the NFZ.
            res, ts = _uas_api("/api/uas/authorize-flight", "POST", {
                "uav_id": uav_id,
                "flight_plan": {
                    "waypoints": [
                        {"lat": 37.5000, "lon": -122.6000, "alt": 50},
                        {"lat": 37.7500, "lon": -122.4500, "alt": 50},  # inside NFZ
                    ],
                },
            })
            if ts != 200:
                self.fail_test(f"authorize-flight HTTP failed: {ts} {res}")
                return self.result
            if res.get("authorized"):
                self.fail_test("flight authorized despite NFZ crossing", body=res)
                return self.result
            if not res.get("violations"):
                self.fail_test("no violations field in deny response", body=res)
                return self.result

            self.pass_test(uav_id=uav_id, zone_id=zone_id, violations=res.get("violations"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if zone_id:
                _uas_api(f"/api/uas/no-fly-zones/{zone_id}", "DELETE")
            if uav_id:
                _uas_api(f"/api/uas/registry/{uav_id}", "DELETE")
        return self.result


class UasC2SingleActive(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-008",
        title="Duplicate C2 establish on same UAV returns 409",
        spec="TS 23.256 §5.5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Invariant: exactly one active C2 session per UAV. Without\n"
            "  this, two controllers could simultaneously issue conflicting\n"
            "  commands. TS 23.256 §5.5 implies single-active C2 binding\n"
            "  per UAV ID.\n"
            "\n"
            "Procedure (TS 23.256 §5.5)\n"
            "  1. _register_uav() — obtain uav_id.\n"
            "  2. POST /api/uas/c2/establish (controller='CTRL-DUP', 5QI=3).\n"
            "  3. fail_test if first establish not in (200, 201).\n"
            "  4. Capture c2_id.\n"
            "  5. POST /api/uas/c2/establish again with controller=\n"
            "     'CTRL-DUP-2'.\n"
            "  6. fail_test if status != 409.\n"
            "  7. GET /api/uas/c2/status/{c2_id} — fail_test if status\n"
            "     != 'active' or HTTP != 200.\n"
            "  8. finally: DELETE C2 and UAV.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Second establish returns 409, first stays active.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  uav_id, c2_id.\n"
            "\n"
            "Known constraints\n"
            "  Negative tag — relies on the core implementing the conflict\n"
            "  signal. No timeout-based recovery is tested."
        ),
    )

    def run(self):
        uav_id = None
        c2_id = None
        try:
            _, rs, uav_id = _register_uav(uav_id_hint=f"UAV-{self.tc_id}")
            if rs not in (200, 201):
                self.fail_test(f"Register UAV failed: {rs}")
                return self.result

            r1, s1 = _uas_api("/api/uas/c2/establish", "POST", {
                "uav_id": uav_id, "controller_id": "CTRL-DUP", "qos_5qi": 3,
            })
            if s1 not in (200, 201):
                self.fail_test(f"first establish failed: {s1} {r1}")
                return self.result
            c2_id = r1.get("id") or r1.get("c2_id") or r1.get("c2_session_id")

            # Second establish on same UAV must conflict (409).
            r2, s2 = _uas_api("/api/uas/c2/establish", "POST", {
                "uav_id": uav_id, "controller_id": "CTRL-DUP-2", "qos_5qi": 3,
            })
            if s2 != 409:
                self.fail_test(f"expected 409 on duplicate establish, got {s2} {r2}")
                return self.result

            # First session still active.
            st, sts = _uas_api(f"/api/uas/c2/status/{c2_id}")
            if sts != 200 or st.get("status") != "active":
                self.fail_test(f"first session not active: {sts} {st}")
                return self.result

            self.pass_test(uav_id=uav_id, c2_id=c2_id)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if c2_id:
                _uas_api(f"/api/uas/c2/{c2_id}", "DELETE")
            if uav_id:
                _uas_api(f"/api/uas/registry/{uav_id}", "DELETE")
        return self.result


class UasAnomalyDetect(TestCase):
    SPEC = TestSpec(
        tc_id="TC-UAS-009",
        title="Anomaly detection flags altitude/speed envelope breaches",
        spec="TS 23.256 §5.2.6",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates envelope-breach detection. TS 23.256 §5.2.6 places\n"
            "  anomaly detection (envelope-out-of-bounds, NFZ entry, route\n"
            "  deviation) inside the UAS NF; here we drive a clean speed +\n"
            "  altitude double-breach and expect both flags in details.\n"
            "\n"
            "Procedure (TS 23.256 §5.2.6)\n"
            "  1. _register_uav() — envelope is 25 m/s, 120 m.\n"
            "  2. POST /api/uas/position with altitude_m=200.0 (>120) and\n"
            "     speed_mps=40.0 (>25). Status ignored.\n"
            "  3. POST /api/uas/authorize-flight with one waypoint so an\n"
            "     active flight exists (§5.2.6 detection requires it).\n"
            "  4. GET /api/uas/anomaly/{uav_id}.\n"
            "  5. fail_test if HTTP != 200.\n"
            "  6. fail_test if anomaly is not truthy.\n"
            "  7. Join details strings; fail_test if 'altitude' or 'speed'\n"
            "     keywords are missing.\n"
            "  8. finally: DELETE UAV.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — breach values pinned to clearly exceed envelope.\n"
            "\n"
            "Pass criteria\n"
            "  anomaly=True AND details mention both 'altitude' and 'speed'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  uav_id, details (list of breach descriptions).\n"
            "\n"
            "Known constraints\n"
            "  Keyword-based string matching on details — implementation\n"
            "  must include those English words."
        ),
    )

    def run(self):
        uav_id = None
        try:
            _, rs, uav_id = _register_uav(uav_id_hint=f"UAV-{self.tc_id}")  # max 25 m/s, 120 m
            if rs not in (200, 201):
                self.fail_test(f"Register UAV failed: {rs}")
                return self.result

            # Ship a position that exceeds the registered envelope.
            _uas_api("/api/uas/position", "POST", {
                "uav_id": uav_id,
                "latitude": 37.7749, "longitude": -122.4194,
                "altitude_m": 200.0,        # > 120 m
                "heading_deg": 0.0,
                "speed_mps": 40.0,          # > 25 m/s
            })

            # Also need an active flight for §5.2.6 detection.
            _uas_api("/api/uas/authorize-flight", "POST", {
                "uav_id": uav_id,
                "flight_plan": {"waypoints": [
                    {"lat": 37.7749, "lon": -122.4194, "alt": 50},
                ]},
            })

            res, status = _uas_api(f"/api/uas/anomaly/{uav_id}")
            if status != 200:
                self.fail_test(f"anomaly fetch failed: {status} {res}")
                return self.result
            if not res.get("anomaly"):
                self.fail_test("expected anomaly=true with breach", body=res)
                return self.result
            details = res.get("details") or []
            joined = " ".join(str(x).lower() for x in details)
            if "altitude" not in joined or "speed" not in joined:
                self.fail_test("expected altitude+speed in details",
                               details=details)
                return self.result

            self.pass_test(uav_id=uav_id, details=details)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if uav_id:
                _uas_api(f"/api/uas/registry/{uav_id}", "DELETE")
        return self.result


ALL_UAS_TCS = [
    UasRegisterUav,
    UasAuthorizeFlight,
    UasUpdatePosition,
    UasNoFlyZone,
    UasC2Session,
    UasRemoteId,
    UasNoFlyZoneViolation,
    UasC2SingleActive,
    UasAnomalyDetect,
]
