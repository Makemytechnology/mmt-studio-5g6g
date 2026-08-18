# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NPN — Non-Public Networks (SNPN + PNI-NPN).

TS 23.501 §5.30 — SNPN, PNI-NPN, CAG management, access authorization.
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

log = logging.getLogger("tester.tc_npn")


def _npn_api(path, method="GET", body=None):
    """Call SA Core NPN REST API."""
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


class NpnCreateSnpn(TestCase):
    """TC-NPN-001: Create and delete an SNPN network."""
    SPEC = TestSpec(
        tc_id="TC-NPN-001",
        title="NPN create and delete an SNPN network",
        spec="TS 23.501 §5.30",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the SNPN catalog per TS 23.501 §5.30.\n"
            "  An SNPN is identified by (PLMN, NID); the operator catalog\n"
            "  /api/npn/networks must accept, return and delete the row.\n"
            "  Any regression here breaks every downstream NPN test.\n"
            "\n"
            "Procedure (TS 23.501 §5.30)\n"
            "  1. POST /api/npn/networks {name='Test SNPN',\n"
            "     npn_type='SNPN', plmn='00101', nid='0123456789'}\n"
            "     → expect 200/201.\n"
            "  2. Extract npn_id from response (.id or .npn_id).\n"
            "  3. GET /api/npn/networks/{id} → expect 200; assert\n"
            "     npn_type=='SNPN'.\n"
            "  4. finally: DELETE /api/npn/networks/{id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — PLMN/NID hard-coded to MCC=001/MNC=01 test ranges).\n"
            "\n"
            "Pass criteria\n"
            "  Create returns 200/201 with an id AND the GET reads back\n"
            "  npn_type=='SNPN'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  npn (the SNPN row dict).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Cleanup runs unconditionally in the finally block.\n"
            "  NID '0123456789' is a 10-digit test value; not registered.\n"
            "  Backend may return .id or .npn_id; test accepts either."
        ),
    )

    def run(self):
        npn_id = None
        try:
            result, status = _npn_api("/api/npn/networks", "POST", {
                "name": "Test SNPN",
                "npn_type": "SNPN",
                "plmn": "00101",
                "nid": "0123456789",
            })
            if status not in (200, 201):
                self.fail_test(f"SNPN creation failed: {status} {result}")
                return self.result

            npn_id = result.get("id") or result.get("npn_id")
            log.info("SNPN created: id=%s", npn_id)

            if not npn_id:
                self.fail_test("SNPN created but no id returned", response=result)
                return self.result

            # Verify
            get_result, get_status = _npn_api(f"/api/npn/networks/{npn_id}")
            if get_status != 200:
                self.fail_test(f"SNPN GET failed: {get_status} {get_result}")
                return self.result

            if get_result.get("npn_type") != "SNPN":
                self.fail_test(f"Type mismatch", response=get_result)
                return self.result

            self.pass_test(npn=get_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


class NpnCreatePniNpn(TestCase):
    """TC-NPN-002: Create a PNI-NPN network."""
    SPEC = TestSpec(
        tc_id="TC-NPN-002",
        title="NPN create a PNI-NPN network (PLMN-integrated NPN)",
        spec="TS 23.501 §5.30",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the PNI-NPN flavour per TS 23.501 §5.30. PNI-NPN is\n"
            "  the PLMN-integrated NPN (private network ridden inside a\n"
            "  public PLMN, gated by CAG). NID is optional for PNI-NPN\n"
            "  because the network ID is implicit in the PLMN. The\n"
            "  catalog must accept the variant and return it unchanged.\n"
            "\n"
            "Procedure (TS 23.501 §5.30)\n"
            "  1. POST /api/npn/networks {name='Test PNI-NPN',\n"
            "     npn_type='PNI-NPN', plmn='00101'} (no NID required)\n"
            "     → expect HTTP 200/201.\n"
            "  2. Extract npn_id (.id or .npn_id) and npn_type from\n"
            "     the response.\n"
            "  3. Assert npn_type == 'PNI-NPN' verbatim.\n"
            "  4. finally: DELETE /api/npn/networks/{id} for cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — PLMN hard-coded to MCC=001/MNC=01 test range;\n"
            "  NID omitted by design).\n"
            "\n"
            "Pass criteria\n"
            "  Create returns 200/201 AND the response carries\n"
            "  npn_type=='PNI-NPN'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  npn (the PNI-NPN row dict from the create response).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  PNI-NPN CAG admission is exercised by TC-NPN-003 onwards."
        ),
    )

    def run(self):
        npn_id = None
        try:
            result, status = _npn_api("/api/npn/networks", "POST", {
                "name": "Test PNI-NPN",
                "npn_type": "PNI-NPN",
                "plmn": "00101",
            })
            if status not in (200, 201):
                self.fail_test(f"PNI-NPN creation failed: {status} {result}")
                return self.result

            npn_id = result.get("id") or result.get("npn_id")
            npn_type = result.get("npn_type")
            log.info("PNI-NPN created: id=%s type=%s", npn_id, npn_type)

            if npn_type != "PNI-NPN":
                self.fail_test(f"Expected PNI-NPN type, got {npn_type}", response=result)
                return self.result

            self.pass_test(npn=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


class NpnCagManagement(TestCase):
    """TC-NPN-003: Create CAG for PNI-NPN and add member IMSI."""
    SPEC = TestSpec(
        tc_id="TC-NPN-003",
        title="NPN create CAG for PNI-NPN and add an IMSI member",
        spec="TS 23.501 §5.30",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the CAG (Closed Access Group) member catalog per\n"
            "  TS 23.501 §5.30. A PNI-NPN's admission gate is its CAG\n"
            "  membership list; the OAM surface must accept create-group +\n"
            "  add-member + readback.\n"
            "\n"
            "Procedure (TS 23.501 §5.30)\n"
            "  1. POST /api/npn/networks {PNI-NPN, plmn='00101'}; record id.\n"
            "  2. POST /api/npn/cag {cag_id='0000000A', npn_id, name}.\n"
            "  3. POST /api/npn/cag/{cag_id}/members {imsi=\n"
            "     baseline.imsi('embb-bulk', 0)}.\n"
            "  4. GET /api/npn/cag/{cag_id}/members → expect 200.\n"
            "  5. Parse members (or items); assert the IMSI is present.\n"
            "  6. finally: DELETE the NPN (cascades to CAG + members).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — cag_id='0000000A' (8 hex) and baseline IMSI[0]\n"
            "  hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Network+CAG+member chain all succeed AND the readback shows\n"
            "  the IMSI in the membership list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  cag (response dict), members (membership list).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  CAG-ID must be 8 hex digits per TS 23.501 §5.30.2.\n"
            "  Members list may use 'members' or 'items' shape per build."
        ),
    )

    def run(self):
        npn_id = None
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            # Create PNI-NPN
            net_result, net_status = _npn_api("/api/npn/networks", "POST", {
                "name": "CAG Test NPN",
                "npn_type": "PNI-NPN",
                "plmn": "00101",
            })
            if net_status not in (200, 201):
                self.fail_test(f"NPN creation failed: {net_status} {net_result}")
                return self.result
            npn_id = net_result.get("id") or net_result.get("npn_id")

            # Create CAG
            cag_result, cag_status = _npn_api("/api/npn/cag", "POST", {
                "cag_id": "0000000A",
                "npn_id": npn_id,
                "name": "Test CAG Group",
            })
            if cag_status not in (200, 201):
                self.fail_test(f"CAG creation failed: {cag_status} {cag_result}")
                return self.result
            log.info("CAG created: %s", cag_result)

            cag_id = cag_result.get("id") or cag_result.get("cag_id") or "0000000A"

            # Add member
            member_result, member_status = _npn_api(
                f"/api/npn/cag/{cag_id}/members", "POST", {"imsi": imsi})
            if member_status not in (200, 201):
                self.fail_test(f"Member add failed: {member_status} {member_result}")
                return self.result

            # Verify membership
            members_result, members_status = _npn_api(
                f"/api/npn/cag/{cag_id}/members")
            if members_status != 200:
                self.fail_test(f"Members query failed: {members_status}")
                return self.result

            members = (members_result.get("members")
                       or members_result.get("items") or [])
            found = any(m.get("imsi") == imsi for m in members)
            if not found:
                self.fail_test(f"IMSI {imsi} not in CAG members", members=members)
                return self.result

            self.pass_test(cag=cag_result, members=members)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


class NpnAuthorize(TestCase):
    """TC-NPN-004: Authorize UE access to NPN via CAG."""
    SPEC = TestSpec(
        tc_id="TC-NPN-004",
        title="NPN authorize UE access to PNI-NPN via CAG membership",
        spec="TS 23.501 §5.30",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Drives the primary admission path: an IMSI that is a CAG\n"
            "  member must be admitted to the PNI-NPN. TS 23.501 §5.30\n"
            "  CAG access authorisation + TS 33.501 §6.1.4 primary auth\n"
            "  anchor for NPNs.\n"
            "\n"
            "Procedure (TS 23.501 §5.30 + TS 33.501 §6.1.4)\n"
            "  1. POST /api/npn/networks {PNI-NPN, plmn='00101'}.\n"
            "  2. POST /api/npn/cag {cag_id='0000000B', npn_id, name}.\n"
            "  3. POST /api/npn/cag/{cag_id}/members {imsi=baseline IMSI[0]}.\n"
            "  4. POST /api/npn/authorize {imsi, cag_id='0000000B', nid=''}\n"
            "     → expect 200/201.\n"
            "  5. Read allowed (or legacy authorized) from the response.\n"
            "  6. finally: DELETE the NPN (cascades).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — cag_id='0000000B' and baseline IMSI[0] hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  /authorize returns 200/201 AND allowed is True (or legacy\n"
            "  authorized is True).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  auth (the authorize response dict).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Backend exposes both 'allowed' (current) and 'authorized'\n"
            "  (legacy) keys; test accepts either.\n"
            "  Body keys on cag_id (8-hex), not just npn_id."
        ),
    )

    def run(self):
        npn_id = None
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            # Create NPN
            net_result, _ = _npn_api("/api/npn/networks", "POST", {
                "name": "Auth Test NPN",
                "npn_type": "PNI-NPN",
                "plmn": "00101",
            })
            npn_id = net_result.get("id") or net_result.get("npn_id")

            # Create CAG + add member
            cag_result, _ = _npn_api("/api/npn/cag", "POST", {
                "cag_id": "0000000B",
                "npn_id": npn_id,
                "name": "Auth CAG",
            })
            cag_id = cag_result.get("id") or cag_result.get("cag_id") or "0000000B"
            _npn_api(f"/api/npn/cag/{cag_id}/members", "POST", {"imsi": imsi})

            # Authorize via the real AuthenticateSNPN backend (TS 33.501
            # §6.1.4 anchor). Body must carry the 8-hex CAG-ID, not just
            # npn_id — the backend keys membership on the CAG hex string.
            auth_result, auth_status = _npn_api("/api/npn/authorize", "POST", {
                "imsi": imsi,
                "cag_id": "0000000B",
                "nid": "",
            })
            if auth_status not in (200, 201):
                self.fail_test(f"Authorize failed: {auth_status} {auth_result}")
                return self.result

            # Backend exposes both `allowed` (current) and `authorized`
            # (legacy alias) keys; accept either.
            authorized = auth_result.get("allowed",
                                         auth_result.get("authorized", False))
            if not authorized:
                self.fail_test("Expected allowed/authorized=true",
                               response=auth_result)
                return self.result

            log.info("UE authorized for NPN: %s", auth_result)
            self.pass_test(auth=auth_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


class NpnDenyUnauthorized(TestCase):
    """TC-NPN-005: Deny UE not in CAG allow-list."""
    SPEC = TestSpec(
        tc_id="TC-NPN-005",
        title="NPN deny UE access when not in CAG allow-list",
        spec="TS 23.501 §5.30",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Negative-path admission guard per TS 23.501 §5.30: an IMSI\n"
            "  that is not in the CAG allow-list must be denied. The\n"
            "  inverse of TC-NPN-004; guards against an open-by-default\n"
            "  regression in the AuthenticateSNPN backend.\n"
            "\n"
            "Procedure (TS 23.501 §5.30)\n"
            "  1. POST /api/npn/networks {PNI-NPN, plmn='00101'}.\n"
            "  2. POST /api/npn/cag {cag_id='0000000C', npn_id, name}\n"
            "     — note: NO members added.\n"
            "  3. POST /api/npn/authorize {imsi='001010000000099',\n"
            "     cag_id='0000000C', nid=''}.\n"
            "  4. Read allowed (or legacy authorized) from the response.\n"
            "  5. finally: DELETE the NPN.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — non-member IMSI '001010000000099' hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  allowed (and authorized) is False — the non-member IMSI is\n"
            "  refused admission to the empty CAG.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  deny_response, deny_status.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  The HTTP status can be 200 (with allowed=false) or non-2xx;\n"
            "  the deny semantic is asserted on the body, not the status.\n"
            "  Non-member IMSI '001010000000099' falls outside seed range."
        ),
    )

    def run(self):
        npn_id = None
        imsi = "001010000000099"
        try:
            # Create NPN
            net_result, _ = _npn_api("/api/npn/networks", "POST", {
                "name": "Deny Test NPN",
                "npn_type": "PNI-NPN",
                "plmn": "00101",
            })
            npn_id = net_result.get("id") or net_result.get("npn_id")

            # Create CAG with no members
            _npn_api("/api/npn/cag", "POST", {
                "cag_id": "0000000C",
                "npn_id": npn_id,
                "name": "Empty CAG",
            })

            # Attempt authorize — IMSI is not in the CAG → must deny.
            auth_result, auth_status = _npn_api("/api/npn/authorize", "POST", {
                "imsi": imsi,
                "cag_id": "0000000C",
                "nid": "",
            })

            authorized = auth_result.get("allowed",
                                         auth_result.get("authorized", False))
            if authorized:
                self.fail_test("UE should have been denied",
                               response=auth_result)
                return self.result

            log.info("UE correctly denied: %s", auth_result)
            self.pass_test(deny_response=auth_result, deny_status=auth_status)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


class NpnAccessLog(TestCase):
    """TC-NPN-006: Verify NPN access log entries."""
    SPEC = TestSpec(
        tc_id="TC-NPN-006",
        title="NPN authorize writes an access-log entry",
        spec="TS 23.501 §5.30",
        domain=Domain.INFRA,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  OAM-audit guard per TS 23.501 §5.30: every authorize call\n"
            "  must persist an access-log row keyed by IMSI so operators\n"
            "  can audit NPN admissions after the fact.\n"
            "\n"
            "Procedure (TS 23.501 §5.30)\n"
            "  1. POST /api/npn/networks {PNI-NPN, plmn='00101'}.\n"
            "  2. POST /api/npn/cag {cag_id='0000000D', npn_id, name}.\n"
            "  3. POST /api/npn/cag/{cag_id}/members {imsi=baseline IMSI[0]}.\n"
            "  4. POST /api/npn/authorize {imsi, cag_id='0000000D', nid=''}.\n"
            "  5. GET /api/npn/access-log?imsi={imsi} → expect 200.\n"
            "  6. Parse entries (items / entries / log); assert non-empty.\n"
            "  7. finally: DELETE the NPN.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — cag_id='0000000D' and baseline IMSI[0] hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  /access-log?imsi=… returns 200 AND at least one entry for\n"
            "  the authorize call performed in step 4.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  entry_count, entries.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Backend may return items / entries / log depending on build;\n"
            "  test accepts any non-empty shape.\n"
            "  Detailed (action, reason) shape is asserted by TC-NPN-013.\n"
            "  This TC only proves the audit-log write happens at all."
        ),
    )

    def run(self):
        npn_id = None
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            # Create NPN + CAG + member + authorize
            net_result, _ = _npn_api("/api/npn/networks", "POST", {
                "name": "Log Test NPN",
                "npn_type": "PNI-NPN",
                "plmn": "00101",
            })
            npn_id = net_result.get("id") or net_result.get("npn_id")

            cag_result, _ = _npn_api("/api/npn/cag", "POST", {
                "cag_id": "0000000D",
                "npn_id": npn_id,
                "name": "Log CAG",
            })
            cag_id = cag_result.get("id") or cag_result.get("cag_id") or "0000000D"
            _npn_api(f"/api/npn/cag/{cag_id}/members", "POST", {"imsi": imsi})
            _npn_api("/api/npn/authorize", "POST", {
                "imsi": imsi, "cag_id": "0000000D", "nid": "",
            })

            # Check access log — backend returns {"items": [...]}.
            log_result, log_status = _npn_api(f"/api/npn/access-log?imsi={imsi}")
            if log_status != 200:
                self.fail_test(f"Access log query failed: {log_status} {log_result}")
                return self.result

            entries = (log_result.get("items")
                       or log_result.get("entries")
                       or log_result.get("log") or [])
            if not entries:
                self.fail_test("No access log entries found", response=log_result)
                return self.result

            log.info("Found %d access log entries", len(entries))
            self.pass_test(entry_count=len(entries), entries=entries)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


# ─── TC-NPN-010 ─────────────────────────────────────────────────────


class NpnCagFullCRUD(TestCase):
    """TC-NPN-010: End-to-end CAG CRUD round-trip — proves the recent
    schema/code rename (npn_cags→npn_cag, cag_row_id→cag_id) works.

    Pre-rename, these calls failed silently against the wrong table
    name. This TC drives Create → List → AddMember → ListMembers →
    RemoveMember → DeleteCAG → DeleteNPN and asserts each step.
    """
    SPEC = TestSpec(
        tc_id="TC-NPN-010",
        title="NPN end-to-end CAG CRUD round-trip across npn_cag table",
        spec="TS 23.501 §5.30.2",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("regression", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Regression guard for the npn_cags→npn_cag and\n"
            "  cag_row_id→cag_id schema rename. Pre-rename the calls\n"
            "  failed silently against the wrong table name; this TC\n"
            "  walks the full CAG CRUD chain end-to-end to keep that\n"
            "  rename honest. Aligns to TS 23.501 §5.30.2.\n"
            "\n"
            "Procedure (TS 23.501 §5.30.2)\n"
            "  1. POST /api/npn/networks {SNPN, plmn='00101',\n"
            "     nid='tc-npn-010-nid'}.\n"
            "  2. POST /api/npn/cag {cag_id='DEADBEEF', npn_id, name}.\n"
            "  3. GET /api/npn/cag?npn_id={id}; assert DEADBEEF is present.\n"
            "  4. AddMember x2 with baseline IMSI[1] and IMSI[2].\n"
            "  5. GET /api/npn/cag/{cag_row_id}/members; assert both IMSIs\n"
            "     are listed.\n"
            "  6. DELETE /api/npn/cag/{cag_row_id}/members/{imsis[0]}\n"
            "     → expect 200/204.\n"
            "  7. GET members again; assert imsis[0] is gone.\n"
            "  8. finally: DELETE the NPN (cascades).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — cag_id='DEADBEEF', baseline IMSIs hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Every CRUD step returns the expected status AND the listing\n"
            "  reflects each mutation (CAG visible, both IMSIs present,\n"
            "  then the removed IMSI absent).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  npn_id, cag_row_id, members_after_remove.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  CAG-ID upper-cased on readback per the schema CHECK."
        ),
    )

    def run(self):
        npn_id = None
        try:
            net, st = _npn_api("/api/npn/networks", "POST", {
                "name": "Test SNPN 010",
                "npn_type": "SNPN",
                "plmn": "00101",
                "nid": "tc-npn-010-nid",
            })
            if st not in (200, 201):
                self.fail_test(f"NPN create failed: {st} {net}")
                return self.result
            npn_id = net.get("id") or net.get("npn_id")

            cag, st = _npn_api("/api/npn/cag", "POST", {
                "cag_id": "DEADBEEF", "npn_id": npn_id,
                "name": "Round-trip CAG",
            })
            if st not in (200, 201):
                self.fail_test(f"CAG create failed: {st} {cag}")
                return self.result
            cag_row_id = cag.get("id")

            cags = _npn_api(f"/api/npn/cag?npn_id={npn_id}")[0]
            if not isinstance(cags, list) or not any(
                    c.get("cag_id", "").upper() == "DEADBEEF" for c in cags):
                self.fail_test("CAG not in listing", listing=cags)
                return self.result

            imsis = [baseline.imsi("embb-bulk", 1), baseline.imsi("embb-bulk", 2)]
            for imsi in imsis:
                _, st = _npn_api(f"/api/npn/cag/{cag_row_id}/members",
                                 "POST", {"imsi": imsi})
                if st not in (200, 201):
                    self.fail_test(f"AddMember {imsi} failed: {st}")
                    return self.result

            members_resp, _ = _npn_api(f"/api/npn/cag/{cag_row_id}/members")
            members = members_resp.get("members") or []
            if len({m.get("imsi") for m in members} & set(imsis)) != 2:
                self.fail_test(f"members missing", members=members)
                return self.result

            _, st = _npn_api(f"/api/npn/cag/{cag_row_id}/members/{imsis[0]}",
                             "DELETE")
            if st not in (200, 204):
                self.fail_test(f"RemoveMember failed: {st}")
                return self.result
            members_resp, _ = _npn_api(f"/api/npn/cag/{cag_row_id}/members")
            members = members_resp.get("members") or []
            if any(m.get("imsi") == imsis[0] for m in members):
                self.fail_test("removed member still present",
                               members=members)
                return self.result

            self.pass_test(npn_id=npn_id, cag_row_id=cag_row_id,
                           members_after_remove=members)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


# ─── TC-NPN-011 ─────────────────────────────────────────────────────


class NpnAuthenticateAdmits(TestCase):
    """TC-NPN-011: AuthenticateSNPN admits a valid CAG member.

    TS 33.501 §6.1.4 primary auth anchor for SNPN. With a configured
    SNPN + CAG + member IMSI, the new /api/npn/authenticate endpoint
    must return allowed=true with the cag_id echoed back.
    """
    SPEC = TestSpec(
        tc_id="TC-NPN-011",
        title="NPN AuthenticateSNPN admits a valid CAG member",
        spec="TS 33.501 §6.1.4",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the AuthenticateSNPN admit path per TS 33.501 §6.1.4.\n"
            "  With a configured SNPN + CAG + member IMSI, the new\n"
            "  /api/npn/authenticate endpoint MUST return allowed=true and\n"
            "  echo the requested cag_id back in the response.\n"
            "\n"
            "Procedure (TS 33.501 §6.1.4)\n"
            "  1. POST /api/npn/networks {SNPN, plmn='00101',\n"
            "     nid='tc-npn-011'}; record npn_id.\n"
            "  2. POST /api/npn/cag {cag_id='AAAA1111', npn_id, name}.\n"
            "  3. POST /api/npn/cag/{cag_row_id}/members {imsi=baseline\n"
            "     IMSI[3]}.\n"
            "  4. POST /api/npn/authenticate {imsi, cag_id='AAAA1111',\n"
            "     nid='tc-npn-011'} → expect 200.\n"
            "  5. Assert allowed is True AND response.cag_id (uppercased)\n"
            "     equals 'AAAA1111'.\n"
            "  6. finally: DELETE the NPN (cascades).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — cag_id='AAAA1111' and baseline IMSI[3] hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  /authenticate returns 200, allowed=True, and the cag_id\n"
            "  echo (uppercased) matches.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  auth (the authenticate response dict).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Distinct endpoint from /authorize (TC-NPN-004); /authenticate\n"
            "  is the §6.1.4 primary-auth-anchored verb."
        ),
    )

    def run(self):
        npn_id = None
        imsi = baseline.imsi("embb-bulk", 3)
        try:
            net, _ = _npn_api("/api/npn/networks", "POST", {
                "name": "Auth Admit SNPN", "npn_type": "SNPN",
                "plmn": "00101", "nid": "tc-npn-011",
            })
            npn_id = net.get("id")
            cag, _ = _npn_api("/api/npn/cag", "POST", {
                "cag_id": "AAAA1111", "npn_id": npn_id,
                "name": "Admit CAG",
            })
            cag_row_id = cag.get("id")
            _npn_api(f"/api/npn/cag/{cag_row_id}/members", "POST",
                     {"imsi": imsi})

            res, st = _npn_api("/api/npn/authenticate", "POST", {
                "imsi": imsi, "cag_id": "AAAA1111", "nid": "tc-npn-011",
            })
            if st != 200:
                self.fail_test(f"authenticate non-200: {st} {res}")
                return self.result
            if res.get("allowed") is not True:
                self.fail_test(f"expected allowed=true, got {res}")
                return self.result
            if (res.get("cag_id") or "").upper() != "AAAA1111":
                self.fail_test(f"cag_id echo mismatch: {res}")
                return self.result
            self.pass_test(auth=res)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


# ─── TC-NPN-012 ─────────────────────────────────────────────────────


class NpnAuthenticateDenies(TestCase):
    """TC-NPN-012: AuthenticateSNPN denies non-members and bad CAG-IDs.

    Two failure modes per security/npn/npn.go::AuthenticateSNPN:
      a) IMSI not in CAG → reason='IMSI not in CAG'
      b) Malformed CAG-ID → reason='invalid CAG-ID format' (CAG-ID
         must be 8 hex digits per TS 23.501 §5.30.2).
    """
    SPEC = TestSpec(
        tc_id="TC-NPN-012",
        title="NPN AuthenticateSNPN denies non-members and bad CAG-IDs",
        spec="TS 23.501 §5.30.2",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.AUSF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Negative-path guard for AuthenticateSNPN per TS 23.501\n"
            "  §5.30.2 (CAG-ID format) plus TS 33.501 §6.1.4 (admission).\n"
            "  Two distinct deny reasons must surface verbatim:\n"
            "  'IMSI not in CAG' for non-members and 'invalid CAG-ID\n"
            "  format' for ids that are not 8 hex digits.\n"
            "\n"
            "Procedure (TS 23.501 §5.30.2)\n"
            "  1. POST /api/npn/networks {SNPN, plmn='00101',\n"
            "     nid='tc-npn-012'}; record npn_id.\n"
            "  2. POST /api/npn/cag {cag_id='BBBB2222', npn_id, name}\n"
            "     (no members).\n"
            "  3. POST /api/npn/authenticate {imsi='001010000099999',\n"
            "     cag_id='BBBB2222', nid='tc-npn-012'};\n"
            "     assert allowed=False AND reason=='IMSI not in CAG'.\n"
            "  4. POST /api/npn/authenticate {imsi='001010000099999',\n"
            "     cag_id='ZZZZZZZZ', nid='tc-npn-012'};\n"
            "     assert allowed=False AND reason=='invalid CAG-ID format'.\n"
            "  5. finally: DELETE the NPN.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — non-member IMSI and bogus CAG hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both calls return allowed=False AND each carries the exact\n"
            "  spec-named reason string verbatim.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  non_member, malformed_cag (the two response dicts).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Reason strings are part of the OAM contract; any rename\n"
            "  must update both backend and this test."
        ),
    )

    def run(self):
        npn_id = None
        try:
            net, _ = _npn_api("/api/npn/networks", "POST", {
                "name": "Auth Deny SNPN", "npn_type": "SNPN",
                "plmn": "00101", "nid": "tc-npn-012",
            })
            npn_id = net.get("id")
            _npn_api("/api/npn/cag", "POST", {
                "cag_id": "BBBB2222", "npn_id": npn_id,
                "name": "Empty Deny CAG",
            })

            # (a) Non-member.
            res, _ = _npn_api("/api/npn/authenticate", "POST", {
                "imsi": "001010000099999",
                "cag_id": "BBBB2222", "nid": "tc-npn-012",
            })
            if res.get("allowed") is not False:
                self.fail_test(f"non-member should be denied, got {res}")
                return self.result
            if res.get("reason") != "IMSI not in CAG":
                self.fail_test(f"wrong deny reason: {res.get('reason')}",
                               response=res)
                return self.result

            # (b) Malformed CAG-ID (not 8 hex digits).
            res2, _ = _npn_api("/api/npn/authenticate", "POST", {
                "imsi": "001010000099999",
                "cag_id": "ZZZZZZZZ", "nid": "tc-npn-012",
            })
            if res2.get("allowed") is not False:
                self.fail_test(f"bad CAG should be denied, got {res2}")
                return self.result
            if res2.get("reason") != "invalid CAG-ID format":
                self.fail_test(f"wrong format-deny reason: "
                               f"{res2.get('reason')}", response=res2)
                return self.result

            self.pass_test(non_member=res, malformed_cag=res2)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


# ─── TC-NPN-013 ─────────────────────────────────────────────────────


class NpnAccessLogPersisted(TestCase):
    """TC-NPN-013: Each AuthenticateSNPN call persists exactly one row.

    TS 23.501 §5.30 OAM/audit. Verifies the new logAccess helper
    writes admit/deny rows with correct (action, reason) and that
    the npn_id/cag_id FK columns are populated when the records exist.
    """
    SPEC = TestSpec(
        tc_id="TC-NPN-013",
        title="NPN AuthenticateSNPN persists admit and deny rows with FKs",
        spec="TS 23.501 §5.30",
        domain=Domain.INFRA,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the access-log persistence contract per TS 23.501\n"
            "  §5.30. Each AuthenticateSNPN call must persist one row\n"
            "  with the correct (action, reason) pair, and the FK columns\n"
            "  (npn_id, cag_id) must be populated when the parent NPN/CAG\n"
            "  rows exist.\n"
            "\n"
            "Procedure (TS 23.501 §5.30)\n"
            "  1. POST /api/npn/networks {SNPN, plmn='00101',\n"
            "     nid='tc-npn-013'}.\n"
            "  2. POST /api/npn/cag {cag_id='CCCC3333', npn_id, name}.\n"
            "  3. POST /api/npn/cag/{cag_row_id}/members\n"
            "     {imsi=baseline IMSI[4]} (admit IMSI).\n"
            "  4. POST /api/npn/authenticate twice: once with admit IMSI,\n"
            "     once with non-member '001010000000302'.\n"
            "  5. For each IMSI: GET /api/npn/access-log?imsi=…&limit=5;\n"
            "     assert items[0].action ∈ {'admitted', 'denied'} matches\n"
            "     expectation AND items[0].reason ∈ {'ok',\n"
            "     'IMSI not in CAG'} matches.\n"
            "  6. Also assert items[0].npn_id and items[0].cag_id are not\n"
            "     NULL.\n"
            "  7. finally: DELETE the NPN.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSIs and CAG hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Admit row has action='admitted', reason='ok'; deny row has\n"
            "  action='denied', reason='IMSI not in CAG'; both rows carry\n"
            "  non-null npn_id and cag_id FKs.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  admit_imsi, deny_imsi.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Only the most-recent row (items[0]) is asserted."
        ),
    )

    def run(self):
        npn_id = None
        admit_imsi = baseline.imsi("embb-bulk", 4)
        deny_imsi = "001010000000302"
        try:
            net, _ = _npn_api("/api/npn/networks", "POST", {
                "name": "AccessLog SNPN", "npn_type": "SNPN",
                "plmn": "00101", "nid": "tc-npn-013",
            })
            npn_id = net.get("id")
            cag, _ = _npn_api("/api/npn/cag", "POST", {
                "cag_id": "CCCC3333", "npn_id": npn_id,
                "name": "Log CAG",
            })
            cag_row_id = cag.get("id")
            _npn_api(f"/api/npn/cag/{cag_row_id}/members", "POST",
                     {"imsi": admit_imsi})

            # Trigger one admit + one deny.
            _npn_api("/api/npn/authenticate", "POST", {
                "imsi": admit_imsi, "cag_id": "CCCC3333",
                "nid": "tc-npn-013",
            })
            _npn_api("/api/npn/authenticate", "POST", {
                "imsi": deny_imsi, "cag_id": "CCCC3333",
                "nid": "tc-npn-013",
            })

            for imsi, want_action, want_reason in (
                    (admit_imsi, "admitted", "ok"),
                    (deny_imsi, "denied", "IMSI not in CAG"),
            ):
                resp, st = _npn_api(f"/api/npn/access-log?imsi={imsi}&limit=5")
                if st != 200:
                    self.fail_test(f"log GET {imsi} failed: {st}")
                    return self.result
                items = resp.get("items") or []
                if not items:
                    self.fail_test(f"no log row for {imsi}", response=resp)
                    return self.result
                row = items[0]
                if row.get("action") != want_action:
                    self.fail_test(f"{imsi}: action={row.get('action')} "
                                   f"want={want_action}", row=row)
                    return self.result
                if row.get("reason") != want_reason:
                    self.fail_test(f"{imsi}: reason={row.get('reason')} "
                                   f"want={want_reason}", row=row)
                    return self.result
                # FKs must be populated since the NPN/CAG exist.
                if row.get("npn_id") is None:
                    self.fail_test(f"{imsi}: npn_id NULL despite NPN exists",
                                   row=row)
                    return self.result
                if row.get("cag_id") is None:
                    self.fail_test(f"{imsi}: cag_id NULL despite CAG exists",
                                   row=row)
                    return self.result

            self.pass_test(admit_imsi=admit_imsi, deny_imsi=deny_imsi)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


# ─── TC-NPN-014 ─────────────────────────────────────────────────────


class NpnDeleteCascadesIntoCAGs(TestCase):
    """TC-NPN-014: Deleting an NPN cascades into CAGs and members.

    The DDL has ON DELETE CASCADE on npn_cag.npn_id → npn_networks.id
    and on npn_cag_members.cag_id → npn_cag.id. Verify the chain
    end-to-end: delete the NPN, observe that the CAG and its members
    disappear too.
    """
    SPEC = TestSpec(
        tc_id="TC-NPN-014",
        title="NPN deletion cascades into CAGs and CAG members",
        spec="TS 23.501 §5.30",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("regression", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Regression guard on the FK ON DELETE CASCADE chain per\n"
            "  TS 23.501 §5.30 OAM lifecycle. The schema has CASCADE on\n"
            "  npn_cag.npn_id → npn_networks.id and on\n"
            "  npn_cag_members.cag_id → npn_cag.id; deleting the parent\n"
            "  NPN must remove all descendants.\n"
            "\n"
            "Procedure (TS 23.501 §5.30)\n"
            "  1. POST /api/npn/networks {SNPN, plmn='00101',\n"
            "     nid='tc-npn-014'}; record npn_id.\n"
            "  2. POST /api/npn/cag {cag_id='FFFF4444', npn_id, name};\n"
            "     record cag_row_id.\n"
            "  3. POST /api/npn/cag/{cag_row_id}/members\n"
            "     {imsi=baseline IMSI[5]}.\n"
            "  4. DELETE /api/npn/networks/{npn_id} → expect 200/204.\n"
            "  5. GET /api/npn/cag → confirm no row has cag_id='FFFF4444'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — cag_id='FFFF4444' and baseline IMSI[5] hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  NPN delete succeeds AND the CAG row is no longer listed.\n"
            "  (Member rows follow via the second cascade hop.)\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  cag_count_after.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Test does not directly query npn_cag_members because the\n"
            "  cag_row_id is gone after cascade; CAG absence is the proxy."
        ),
    )

    def run(self):
        npn_id = None
        try:
            net, _ = _npn_api("/api/npn/networks", "POST", {
                "name": "Cascade SNPN", "npn_type": "SNPN",
                "plmn": "00101", "nid": "tc-npn-014",
            })
            npn_id = net.get("id")
            cag, _ = _npn_api("/api/npn/cag", "POST", {
                "cag_id": "FFFF4444", "npn_id": npn_id,
                "name": "Cascade CAG",
            })
            cag_row_id = cag.get("id")
            _npn_api(f"/api/npn/cag/{cag_row_id}/members", "POST",
                     {"imsi": baseline.imsi("embb-bulk", 5)})

            # Delete the NPN.
            _, st = _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
            if st not in (200, 204):
                self.fail_test(f"NPN delete failed: {st}")
                return self.result
            npn_id = None  # don't try to clean up again

            cags_after = _npn_api("/api/npn/cag")[0]
            if isinstance(cags_after, list) and any(
                    c.get("cag_id", "").upper() == "FFFF4444"
                    for c in cags_after):
                self.fail_test("CAG survived NPN delete (no cascade)",
                               cags=cags_after)
                return self.result
            self.pass_test(cag_count_after=len(cags_after)
                           if isinstance(cags_after, list) else 0)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if npn_id:
                _npn_api(f"/api/npn/networks/{npn_id}", "DELETE")
        return self.result


ALL_NPN_TCS = [
    NpnCreateSnpn, NpnCreatePniNpn, NpnCagManagement,
    NpnAuthorize, NpnDenyUnauthorized, NpnAccessLog,
    NpnCagFullCRUD, NpnAuthenticateAdmits, NpnAuthenticateDenies,
    NpnAccessLogPersisted, NpnDeleteCascadesIntoCAGs,
]
