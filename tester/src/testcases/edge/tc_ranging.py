# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Ranging & Sidelink Positioning.

TS 23.586 — Ranging-based services, sidelink positioning, privacy.
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

log = logging.getLogger("tester.tc_ranging")


def _ranging_api(path, method="GET", body=None):
    """Call SA Core Ranging REST API."""
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


class RangingInitiate(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RNG-001",
        title="Initiate RTT ranging session and verify distance",
        spec="TS 23.586 §6",
        domain=Domain.POSITIONING,
        nfs=(NF.LMF, NF.AF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.586 §6 defines ranging as a sidelink-positioning\n"
            "  service: a source UE measures distance/angle to a target\n"
            "  UE via PC5 RTT or AoA. This smoke pins the RTT method:\n"
            "  given two registered UEs, the LMF must return a non-null\n"
            "  distance_m.\n"
            "\n"
            "Procedure (TS 23.586 §6 ranging RTT)\n"
            "  1. POST /api/ranging/sessions with source_imsi/target_imsi\n"
            "     pulled from baseline.imsi('embb-bulk', 0/1) and\n"
            "     method='RTT'.\n"
            "  2. Assert HTTP 200/201.\n"
            "  3. Extract session_id (or id).\n"
            "  4. GET /api/ranging/sessions/{session_id}.\n"
            "  5. Assert GET status == 200.\n"
            "  6. Read distance = response.distance_m / .distance.\n"
            "  7. Assert distance is not None.\n"
            "  8. Finally clause DELETEs the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — IMSIs from baseline embb-bulk slot 0 and 1.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET 200 AND distance_m is not None.\n"
            "  pass_test fires with session payload and distance_m.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session, distance_m.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE seeds the embb-bulk SIMs. distance value is\n"
            "  asserted non-None but not bounded — a stub returning 0 or\n"
            "  a negative number would pass."
        ),
    )

    def run(self):
        session_id = None
        try:
            result, status = _ranging_api("/api/ranging/sessions", "POST", {
                "source_imsi": baseline.imsi("embb-bulk", 0),
                "target_imsi": baseline.imsi("embb-bulk", 1),
                "method": "RTT",
            })
            if status not in (200, 201):
                self.fail_test(f"Session creation failed: {status} {result}")
                return self.result

            session_id = result.get("id") or result.get("session_id")
            log.info("Ranging session created: id=%s", session_id)

            # GET session to verify result
            get_result, get_status = _ranging_api(
                f"/api/ranging/sessions/{session_id}")
            if get_status != 200:
                self.fail_test(f"Session GET failed: {get_status} {get_result}")
                return self.result

            distance = get_result.get("distance_m") or get_result.get("distance")
            if distance is None:
                self.fail_test("No distance in result", response=get_result)
                return self.result

            log.info("RTT distance: %s m", distance)
            self.pass_test(session=get_result, distance_m=distance)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if session_id:
                _ranging_api(f"/api/ranging/sessions/{session_id}", "DELETE")
        return self.result


class RangingAoA(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RNG-002",
        title="AoA ranging session returns azimuth and elevation",
        spec="TS 23.586 §6",
        domain=Domain.POSITIONING,
        nfs=(NF.LMF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.586 §6 includes Angle-of-Arrival as a ranging method:\n"
            "  unlike RTT which gives a scalar distance, AoA returns a\n"
            "  bearing (azimuth + elevation) from the anchor to the target.\n"
            "  This test pins that the AoA pipeline produces both angles.\n"
            "\n"
            "Procedure (TS 23.586 §6 ranging AoA)\n"
            "  1. POST /api/ranging/sessions with source/target IMSIs from\n"
            "     baseline embb-bulk[0]/[1] and method='AoA'.\n"
            "  2. Assert HTTP 200/201; extract session_id.\n"
            "  3. GET /api/ranging/sessions/{session_id}.\n"
            "  4. Assert GET status == 200.\n"
            "  5. Read azimuth = response.azimuth_deg / .azimuth.\n"
            "  6. Read elevation = response.elevation_deg / .elevation.\n"
            "  7. Assert both azimuth AND elevation are not None.\n"
            "  8. Finally clause DELETEs the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — IMSIs from baseline embb-bulk slot 0 and 1.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET 200 AND azimuth not None AND elevation\n"
            "  not None. pass_test fires with session, azimuth, elevation.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session, azimuth, elevation.\n"
            "\n"
            "Known constraints\n"
            "  Presence-only check on angles — ranges and units are not\n"
            "  validated. A stub returning azimuth=0/elevation=0 would pass."
        ),
    )

    def run(self):
        session_id = None
        try:
            result, status = _ranging_api("/api/ranging/sessions", "POST", {
                "source_imsi": baseline.imsi("embb-bulk", 0),
                "target_imsi": baseline.imsi("embb-bulk", 1),
                "method": "AoA",
            })
            if status not in (200, 201):
                self.fail_test(f"AoA session failed: {status} {result}")
                return self.result

            session_id = result.get("id") or result.get("session_id")

            # Verify azimuth and elevation present
            get_result, get_status = _ranging_api(
                f"/api/ranging/sessions/{session_id}")
            if get_status != 200:
                self.fail_test(f"Session GET failed: {get_status} {get_result}")
                return self.result

            azimuth = get_result.get("azimuth_deg") or get_result.get("azimuth")
            elevation = get_result.get("elevation_deg") or get_result.get("elevation")
            if azimuth is None or elevation is None:
                self.fail_test("Missing azimuth/elevation",
                               response=get_result)
                return self.result

            log.info("AoA: azimuth=%s elevation=%s", azimuth, elevation)
            self.pass_test(session=get_result, azimuth=azimuth,
                           elevation=elevation)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if session_id:
                _ranging_api(f"/api/ranging/sessions/{session_id}", "DELETE")
        return self.result


class RangingCancel(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RNG-003",
        title="Cancel an in-flight ranging session",
        spec="TS 23.586 §6",
        domain=Domain.POSITIONING,
        nfs=(NF.LMF, NF.AF),
        severity=Severity.MINOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.586 §6 requires that an in-flight ranging session can\n"
            "  be cancelled by the requester. This test pins the cancel\n"
            "  path: create then DELETE, expecting a clean shutdown\n"
            "  acknowledgement (200 or 204).\n"
            "\n"
            "Procedure (TS 23.586 §6 session cancel)\n"
            "  1. POST /api/ranging/sessions with source/target IMSIs and\n"
            "     method='RTT'.\n"
            "  2. Assert HTTP 200/201; extract session_id.\n"
            "  3. DELETE /api/ranging/sessions/{session_id}.\n"
            "  4. Assert DELETE status in (200, 204).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — IMSIs from baseline embb-bulk slot 0/1.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND DELETE in (200, 204). pass_test fires\n"
            "  with session_id and cancel response body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id, cancel (DELETE response body).\n"
            "\n"
            "Known constraints\n"
            "  No follow-up GET to confirm the session is gone — a stub\n"
            "  that 204s on DELETE without removing the row would pass."
        ),
    )

    def run(self):
        try:
            result, status = _ranging_api("/api/ranging/sessions", "POST", {
                "source_imsi": baseline.imsi("embb-bulk", 0),
                "target_imsi": baseline.imsi("embb-bulk", 1),
                "method": "RTT",
            })
            if status not in (200, 201):
                self.fail_test(f"Session creation failed: {status} {result}")
                return self.result

            session_id = result.get("id") or result.get("session_id")

            # Delete / cancel
            del_result, del_status = _ranging_api(
                f"/api/ranging/sessions/{session_id}", "DELETE")
            if del_status not in (200, 204):
                self.fail_test(f"Cancel failed: {del_status} {del_result}")
                return self.result

            log.info("Session %s cancelled", session_id)
            self.pass_test(session_id=session_id, cancel=del_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class RangingAddAnchor(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RNG-004",
        title="Add and delete a ranging anchor",
        spec="TS 23.586 §6",
        domain=Domain.POSITIONING,
        nfs=(NF.LMF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.586 §6 models ranging anchors as IMSI-tagged geographic\n"
            "  reference points the LMF uses for trilateration. This test\n"
            "  pins the anchor CRUD: POST writes anchor, GET reads it back,\n"
            "  DELETE removes it.\n"
            "\n"
            "Procedure (TS 23.586 §6 anchor provisioning)\n"
            "  1. POST /api/ranging/anchors with imsi=baseline.imsi(\n"
            "     'embb-bulk', 0), latitude=-33.8688, longitude=151.2093,\n"
            "     altitude=10.0 (Sydney).\n"
            "  2. Assert HTTP 200/201.\n"
            "  3. Extract anchor_id from response.\n"
            "  4. GET /api/ranging/anchors/{anchor_id}.\n"
            "  5. Assert GET status == 200.\n"
            "  6. Finally clause DELETEs the anchor.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — IMSI from baseline embb-bulk[0]; Sydney coords hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET 200. pass_test fires with the anchor\n"
            "  body from GET.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  anchor (GET response body).\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: the GET body is reported but not field-\n"
            "  checked against the written lat/lon. A stub returning a\n"
            "  hollow row on GET would pass."
        ),
    )

    def run(self):
        anchor_id = None
        try:
            result, status = _ranging_api("/api/ranging/anchors", "POST", {
                "imsi": baseline.imsi("embb-bulk", 0),
                "latitude": -33.8688,
                "longitude": 151.2093,
                "altitude": 10.0,
            })
            if status not in (200, 201):
                self.fail_test(f"Anchor add failed: {status} {result}")
                return self.result

            anchor_id = result.get("id") or result.get("anchor_id")
            log.info("Anchor created: id=%s", anchor_id)

            # Verify
            get_result, get_status = _ranging_api(
                f"/api/ranging/anchors/{anchor_id}")
            if get_status != 200:
                self.fail_test(f"Anchor GET failed: {get_status} {get_result}")
                return self.result

            self.pass_test(anchor=get_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if anchor_id:
                _ranging_api(f"/api/ranging/anchors/{anchor_id}", "DELETE")
        return self.result


class RangingPosition(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RNG-005",
        title="Compute trilateration position from 3 anchors",
        spec="TS 23.586 §6",
        domain=Domain.POSITIONING,
        nfs=(NF.LMF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  TS 23.586 §6 puts trilateration over multiple anchors at the\n"
            "  heart of ranging-based positioning. With three anchors at\n"
            "  known coordinates the LMF can resolve the target UE's\n"
            "  position to a (lat, lon). This test pins that the\n"
            "  trilateration solver runs and returns a fix.\n"
            "\n"
            "Procedure (TS 23.586 §6 trilateration)\n"
            "  1. Register 3 anchors via POST /api/ranging/anchors using\n"
            "     IMSIs baseline.imsi('embb-bulk', 2/3/4) at three Sydney\n"
            "     coordinates (-33.8688..-33.8700, 151.2080..151.2100).\n"
            "  2. Collect anchor_ids; assert len >= 3 (else fail).\n"
            "  3. POST /api/ranging/position with target_imsi=\n"
            "     baseline.imsi('embb-bulk', 0).\n"
            "  4. Assert position POST in (200, 201).\n"
            "  5. Read lat, lon, accuracy from response.\n"
            "  6. Assert lat is not None AND lon is not None.\n"
            "  7. Finally clause DELETEs each anchor.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — anchor coords (Sydney triangle) and IMSIs hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  >= 3 anchors created AND position POST 200/201 AND lat\n"
            "  not None AND lon not None.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  position (response body), latitude, longitude, accuracy.\n"
            "\n"
            "Known constraints\n"
            "  Lat/lon presence is asserted but values are NOT compared\n"
            "  against the expected triangle centroid — a stub returning\n"
            "  any non-null (lat, lon) pair would pass."
        ),
    )

    def run(self):
        anchor_ids = []
        anchors_data = [
            {"imsi": baseline.imsi("embb-bulk", 2), "latitude": -33.8688,
             "longitude": 151.2093, "altitude": 10.0},
            {"imsi": baseline.imsi("embb-bulk", 3), "latitude": -33.8700,
             "longitude": 151.2100, "altitude": 10.0},
            {"imsi": baseline.imsi("embb-bulk", 4), "latitude": -33.8695,
             "longitude": 151.2080, "altitude": 10.0},
        ]
        try:
            for a in anchors_data:
                r, s = _ranging_api("/api/ranging/anchors", "POST", a)
                if s in (200, 201):
                    aid = r.get("id") or r.get("anchor_id")
                    if aid:
                        anchor_ids.append(aid)

            if len(anchor_ids) < 3:
                self.fail_test(f"Only {len(anchor_ids)} anchors created, need 3")
                return self.result

            # Request position
            result, status = _ranging_api("/api/ranging/position", "POST", {
                "target_imsi": baseline.imsi("embb-bulk", 0),
            })
            if status not in (200, 201):
                self.fail_test(f"Position request failed: {status} {result}")
                return self.result

            lat = result.get("latitude")
            lon = result.get("longitude")
            accuracy = result.get("accuracy") or result.get("accuracy_m")
            if lat is None or lon is None:
                self.fail_test("Missing lat/lon in position", response=result)
                return self.result

            log.info("Position: lat=%s lon=%s accuracy=%s", lat, lon, accuracy)
            self.pass_test(position=result, latitude=lat, longitude=lon,
                           accuracy=accuracy)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for aid in anchor_ids:
                _ranging_api(f"/api/ranging/anchors/{aid}", "DELETE")
        return self.result


class RangingPrivacy(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RNG-006",
        title="Set and verify per-IMSI ranging privacy policy",
        spec="TS 23.586 §6",
        domain=Domain.POSITIONING,
        nfs=(NF.LMF, NF.AF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.586 §6 carries privacy requirements parallel to TS\n"
            "  23.271 §9 for LCS: a UE must be able to deny ranging\n"
            "  enquiries about itself. This test pins the metadata-layer\n"
            "  privacy policy: a `deny_all` rule must persist on a GET\n"
            "  read-back.\n"
            "\n"
            "Procedure (TS 23.586 §6 ranging privacy)\n"
            "  1. POST /api/ranging/privacy with imsi=baseline.imsi(\n"
            "     'embb-bulk', 0) and policy='deny_all'.\n"
            "  2. Assert HTTP 200/201.\n"
            "  3. GET /api/ranging/privacy/{imsi}.\n"
            "  4. Assert GET status == 200.\n"
            "  5. Read policy = response.policy.\n"
            "  6. Assert policy == 'deny_all'.\n"
            "  7. Finally clause RESETS to policy='allow_all' so\n"
            "     downstream tests aren't poisoned.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — IMSI from baseline embb-bulk[0]; policies hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET 200 AND policy field exactly equals\n"
            "  'deny_all'. pass_test fires with the GET privacy body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  privacy (GET response body).\n"
            "\n"
            "Known constraints\n"
            "  Strict string match on policy — no hollow-pass shape on\n"
            "  the policy field. Test resets to allow_all in finally to\n"
            "  keep test ordering safe."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            # Set deny_all
            result, status = _ranging_api("/api/ranging/privacy", "POST", {
                "imsi": imsi,
                "policy": "deny_all",
            })
            if status not in (200, 201):
                self.fail_test(f"Privacy set failed: {status} {result}")
                return self.result
            log.info("Privacy set to deny_all: %s", result)

            # Verify
            get_result, get_status = _ranging_api(
                f"/api/ranging/privacy/{imsi}")
            if get_status != 200:
                self.fail_test(f"Privacy GET failed: {get_status} {get_result}")
                return self.result

            policy = get_result.get("policy")
            if policy != "deny_all":
                self.fail_test(f"Expected deny_all, got {policy}",
                               response=get_result)
                return self.result

            self.pass_test(privacy=get_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            # Reset to allow_all
            _ranging_api("/api/ranging/privacy", "POST", {
                "imsi": imsi, "policy": "allow_all",
            })
        return self.result


ALL_RANGING_TCS = [
    RangingInitiate, RangingAoA, RangingCancel,
    RangingAddAnchor, RangingPosition, RangingPrivacy,
]
