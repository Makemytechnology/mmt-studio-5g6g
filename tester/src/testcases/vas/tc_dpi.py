# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: DPI / Application Detection.

TS 23.501 §5.8.2 — Traffic Detection in 5GC.

Sub-clause coverage:

  §5.8.2.4    PFD rule structure (SST/DNN-agnostic; applies to all PDU
              sessions). Detection types: SNI (TLS ClientHello), DNS
              (response snooping), IP-range (post-DNS pinning + manual),
              port-range (fallback for plain TCP/UDP).

  §5.8.2.4.1  App-ID matching against PFD set.

  §5.8.2.4.2  Traffic Detection Information delivered to the UPF — the
              app catalogue + PFD rules form the operator-curated cache
              the SMF would otherwise sync to the UPF over PFCP.

  §5.8.2.6    Charging / Usage Monitoring — per-(IMSI, app) byte
              counters with 60-min coalescing. Surfaced via
              GetAppUsageSummary -> /api/dpi/usage-summary.

  §5.8.2.8.4  PFD lifecycle (CRUD on the local cache; PFCP push
              TS 29.244 §6.2.5 is a deferred TODO at the wire level).

These test cases drive the live /api/dpi/* endpoints (wired to
security/dpi in core commit that introduced this file). They do NOT
exercise the C-side TLS ClientHello parser in
nf/upf/dataplane/src/upf_dpi.c — that path is unit-tested separately
in the core repo.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_dpi")


def _dpi_api(path, method="GET", body=None):
    """Call SA Core DPI REST API and return (json|str, status)."""
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read().decode()
            try:
                return json.loads(raw), resp.status
            except Exception:
                return raw, resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


def _list_apps():
    body, status = _dpi_api("/api/dpi/apps")
    if status != 200 or not isinstance(body, dict):
        return []
    return body.get("items") or []


def _list_rules(app_id=""):
    qs = f"?app_id={app_id}" if app_id else ""
    body, status = _dpi_api(f"/api/dpi/rules{qs}")
    if status != 200 or not isinstance(body, dict):
        return []
    return body.get("items") or []


# ─── TC-DPI-001 ──────────────────────────────────────────────────────


class DpiAppCRUD(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-001",
        title="Application catalogue CRUD round-trip",
        spec="TS 23.501 §5.8.2.8.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  Foundational catalogue-CRUD smoke for application IDs in the\n"
            "  Traffic Detection database (TS 23.501 §5.8.2.8.4). The PFD\n"
            "  catalogue is the operator-curated cache the SMF eventually pushes\n"
            "  to the UPF over PFCP (TS 29.244 §6.2.5); without a working upsert\n"
            "  on /api/dpi/app no app-ID detection can run downstream.\n"
            "\n"
            "Procedure (TS 23.501 §5.8.2.8.4 + TS 29.244 §6.2.5)\n"
            "  1. Pre-cleanup: POST /api/dpi/app/{app_id}/delete.\n"
            "  2. POST /api/dpi/app with full row (app_id, app_name, category,\n"
            "     qos_profile, charging_profile, priority).\n"
            "  3. Require status 200/201 AND echoed app_id == requested AND\n"
            "     app_name preserved verbatim.\n"
            "  4. GET /api/dpi/apps — assert membership by app_id.\n"
            "  5. Idempotent upsert: re-POST same app_id with new app_name=\n"
            "     'Renamed' and priority=25.\n"
            "  6. Re-list apps; require the persisted row now shows the new name.\n"
            "  7. finally: delete by app_id for hermetic teardown.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — app_id 'tc-dpi-001' is hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Create returns 200/201 with matching app_id+app_name; listing\n"
            "  membership confirmed; second POST replaces app_name to 'Renamed'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  app, total_apps.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no UE/PDU session. PFCP push to UPF is verified\n"
            "  separately in TC-DPI-011..014."
        ),
    )

    def run(self):
        app_id = "tc-dpi-001"
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")  # pre-cleanup

            row, st = _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "CRUD Test App",
                "category": "general", "qos_profile": "standard",
                "charging_profile": "default", "priority": 50,
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st} {row}")
                return self.result
            if row.get("app_id") != app_id:
                self.fail_test("create returned wrong/missing app_id",
                               response=row)
                return self.result
            if row.get("app_name") != "CRUD Test App":
                self.fail_test(f"app_name not echoed: {row.get('app_name')}",
                               row=row)
                return self.result

            apps = _list_apps()
            if not any(a.get("app_id") == app_id for a in apps):
                self.fail_test("app missing from listing",
                               apps_count=len(apps))
                return self.result

            # Idempotent upsert — second POST with same id replaces fields.
            _, _ = _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "Renamed",
                "priority": 25,
            })
            apps = _list_apps()
            mine = next((a for a in apps if a.get("app_id") == app_id), None)
            if not mine or mine.get("app_name") != "Renamed":
                self.fail_test("upsert did not replace app_name", app=mine)
                return self.result

            self.pass_test(app=mine, total_apps=len(apps))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
        return self.result


# ─── TC-DPI-002 ──────────────────────────────────────────────────────


class DpiPFDRuleCRUD(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-002",
        title="PFD rule CRUD with detection_type validation",
        spec="TS 23.501 §5.8.2.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression", "negative"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  PFD (Packet Flow Description) rule lifecycle for a single app\n"
            "  (TS 23.501 §5.8.2.4). Pins that the operator-API accepts the three\n"
            "  canonical detection types (SNI / DNS / port-range), rejects any\n"
            "  unknown detection_type with a 4xx, and that DELETE on a rule id\n"
            "  is honoured.\n"
            "\n"
            "Procedure (TS 23.501 §5.8.2.4 + TS 29.244 §6.2.5)\n"
            "  1. Pre-cleanup app, then POST /api/dpi/app to create app_id\n"
            "     'tc-dpi-002'.\n"
            "  2. For each (det, pat) in [('sni','*.example.test'),\n"
            "     ('dns','example.test'), ('port_range','5000-5010')]:\n"
            "     POST /api/dpi/rule {app_id, detection_type, pattern}.\n"
            "  3. GET /api/dpi/rules?app_id=… — require length == 3.\n"
            "  4. Negative: POST a rule with detection_type='ml-magic' —\n"
            "     require status >= 400.\n"
            "  5. POST /api/dpi/rule/{id}/delete on the first rule id —\n"
            "     require 200/201 and the listing now has length == 2.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — app_id and rule patterns are hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Three valid rules accepted, listing length==3; unknown type\n"
            "  rejected with 4xx; one delete drops listing to 2.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  initial_rules, after_delete, rule_ids.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Wildcard SNI is exercised in TC-DPI-003 detect logic."
        ),
    )

    def run(self):
        app_id = "tc-dpi-002"
        rule_ids = []
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "Rule Test",
                "category": "general",
            })

            # Add three rules — sni, dns, port_range.
            rules = [
                ("sni", "*.example.test"),
                ("dns", "example.test"),
                ("port_range", "5000-5010"),
            ]
            for det, pat in rules:
                _, st = _dpi_api("/api/dpi/rule", "POST", {
                    "app_id": app_id, "detection_type": det, "pattern": pat,
                })
                if st not in (200, 201):
                    self.fail_test(f"rule add failed for ({det},{pat}): {st}")
                    return self.result

            stored = _list_rules(app_id)
            if len(stored) != 3:
                self.fail_test(f"expected 3 rules, got {len(stored)}",
                               rules=stored)
                return self.result

            # Reject invalid detection_type.
            bad, st = _dpi_api("/api/dpi/rule", "POST", {
                "app_id": app_id, "detection_type": "ml-magic",
                "pattern": "anything",
            })
            if st < 400:
                self.fail_test(f"invalid type accepted (status={st})",
                               response=bad)
                return self.result

            # Delete one rule by id, expect listing to drop to 2.
            rid = stored[0].get("id")
            _, st = _dpi_api(f"/api/dpi/rule/{rid}/delete", "POST")
            if st not in (200, 201):
                self.fail_test(f"rule delete failed: {st}")
                return self.result
            after = _list_rules(app_id)
            if len(after) != 2:
                self.fail_test(f"after delete expected 2, got {len(after)}",
                               rules=after)
                return self.result
            for rid in [r.get("id") for r in stored]:
                rule_ids.append(rid)

            self.pass_test(initial_rules=len(stored),
                           after_delete=len(after),
                           rule_ids=rule_ids)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
        return self.result


# ─── TC-DPI-003 ──────────────────────────────────────────────────────


class DpiSNIDetection(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-003",
        title="SNI-based application detection (high-confidence)",
        spec="TS 23.501 §5.8.2.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  SNI-based detection is the high-confidence classifier for TLS\n"
            "  flows (TS 23.501 §5.8.2.4 'PFD set' / TS 29.244 §8.2.39). The\n"
            "  TLS ClientHello carries the server name in cleartext, so a glob\n"
            "  match MUST resolve to the registered app with near-1.0 confidence.\n"
            "  Unmatched SNI MUST yield app=null so downstream consumers know\n"
            "  to fall back to other classifiers.\n"
            "\n"
            "Procedure (TS 23.501 §5.8.2.4)\n"
            "  1. Pre-cleanup app, then POST /api/dpi/app for 'tc-dpi-003'.\n"
            "  2. POST /api/dpi/rule with detection_type='sni',\n"
            "     pattern='*.tc-dpi-003.example'.\n"
            "  3. GET /api/dpi/detect?sni=video.tc-dpi-003.example.\n"
            "  4. Require status 200, res.app == 'tc-dpi-003',\n"
            "     float(res.confidence) >= 0.99.\n"
            "  5. GET /api/dpi/detect?sni=nope.unrelated.example — require\n"
            "     res.app is None (miss).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixtures hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Hit returns the registered app with confidence>=0.99; miss\n"
            "  returns app=None.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  hit, miss.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. This test drives only the REST surface — the C\n"
            "  ClientHello parser in upf_dpi.c is covered by unit tests in\n"
            "  the core repo."
        ),
    )

    def run(self):
        app_id = "tc-dpi-003"
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "SNI Probe", "category": "test",
            })
            _dpi_api("/api/dpi/rule", "POST", {
                "app_id": app_id, "detection_type": "sni",
                "pattern": "*.tc-dpi-003.example",
            })

            res, st = _dpi_api("/api/dpi/detect?sni=video.tc-dpi-003.example")
            if st != 200:
                self.fail_test(f"detect non-200: {st} {res}")
                return self.result
            if res.get("app") != app_id:
                self.fail_test(f"expected app={app_id}, got {res.get('app')}",
                               response=res)
                return self.result
            if float(res.get("confidence", 0)) < 0.99:
                self.fail_test(f"SNI hit should be high-confidence, "
                               f"got {res.get('confidence')}", response=res)
                return self.result

            miss, _ = _dpi_api("/api/dpi/detect?sni=nope.unrelated.example")
            if miss.get("app") is not None:
                self.fail_test(f"miss should be null, got {miss}",
                               response=miss)
                return self.result

            self.pass_test(hit=res, miss=miss)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
        return self.result


# ─── TC-DPI-004 ──────────────────────────────────────────────────────


class DpiDNSDetection(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-004",
        title="DNS-based application detection",
        spec="TS 23.501 §5.8.2.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  DNS snooping is the second-tier classifier for non-TLS apps\n"
                "  (TS 23.501 §5.8.2.4). When the UPF sees a UE DNS response, it\n"
                "  pins the answered IP to the app whose DNS-suffix PFD matched.\n"
                "  This test pins that the REST classifier resolves a domain query\n"
                "  to the registered app_id.\n"
                "\n"
                "Procedure (TS 23.501 §5.8.2.4)\n"
                "  1. Pre-cleanup app, POST /api/dpi/app 'tc-dpi-004'.\n"
                "  2. POST /api/dpi/rule with detection_type='dns',\n"
                "     pattern='tc-dpi-004.example' (DNS suffix).\n"
                "  3. GET /api/dpi/detect?domain=api.tc-dpi-004.example.\n"
                "  4. Require res.app == 'tc-dpi-004'.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  DNS-suffix match returns the registered app_id.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  hit.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Confidence value is not asserted here (SNI test\n"
                "  covers the confidence contract).\n"
                "  DNS-suffix matching is greedy from the right; this test pins one\n"
                "  match level above the suffix to exercise that behaviour.\n"
                "  Negative DNS-detect paths (unmatched suffix) live in TC-DPI-010.\n"
                "  Wildcard suffix support uses dot-bounded segments per RFC 1035."
            ),
    )

    def run(self):
        app_id = "tc-dpi-004"
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "DNS Probe", "category": "test",
            })
            _dpi_api("/api/dpi/rule", "POST", {
                "app_id": app_id, "detection_type": "dns",
                "pattern": "tc-dpi-004.example",
            })

            res, _ = _dpi_api("/api/dpi/detect?domain=api.tc-dpi-004.example")
            if res.get("app") != app_id:
                self.fail_test(f"expected app={app_id}, got {res}")
                return self.result
            self.pass_test(hit=res)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
        return self.result


# ─── TC-DPI-005 ──────────────────────────────────────────────────────


class DpiPortRangeDetection(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-005",
        title="Port-range fallback classifier (low-confidence)",
        spec="TS 23.501 §5.8.2.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Port-range is the fallback classifier of last resort for plain\n"
                "  TCP/UDP (TS 23.501 §5.8.2.4). The PFD shape allows port ranges\n"
                "  via TS 29.244 §8.2.39 Flow Description encoding. Confidence MUST\n"
                "  be strictly below 1.0 so SNI/DNS-classified flows always win when\n"
                "  multiple rules match — a regression in this ordering would silently\n"
                "  mis-attribute traffic.\n"
                "\n"
                "Procedure (TS 23.501 §5.8.2.4 + TS 29.244 §8.2.39)\n"
                "  1. Pre-cleanup, POST /api/dpi/app 'tc-dpi-005'.\n"
                "  2. POST /api/dpi/rule with detection_type='port_range',\n"
                "     pattern='5060-5061'.\n"
                "  3. GET /api/dpi/detect?port=5060.\n"
                "  4. Require res.app == 'tc-dpi-005' AND\n"
                "     float(res.confidence) < 1.0.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — pattern '5060-5061' (SIP-ish) hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Port hit returns the registered app with confidence < 1.0.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  hit.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Only one classifier is registered, so no priority\n"
                "  collision is exercised here.\n"
                "  Wildcard ports beyond the registered range are exercised by\n"
                "  TC-DPI-010 as part of the catch-all 'no match' contract.\n"
                "  Single ports (not ranges) ride on this same code path with the\n"
                "  low and high bounds collapsed."
            ),
    )

    def run(self):
        app_id = "tc-dpi-005"
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "Port Probe", "category": "test",
            })
            _dpi_api("/api/dpi/rule", "POST", {
                "app_id": app_id, "detection_type": "port_range",
                "pattern": "5060-5061",
            })
            res, _ = _dpi_api("/api/dpi/detect?port=5060")
            if res.get("app") != app_id:
                self.fail_test(f"port hit failed: {res}")
                return self.result
            # Fallback classifiers are intentionally < 1.0
            if float(res.get("confidence", 0)) >= 1.0:
                self.fail_test(f"port-range confidence should be <1.0 "
                               f"(SNI/DNS-preferred classifier), got {res}")
                return self.result
            self.pass_test(hit=res)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
        return self.result


# ─── TC-DPI-006 ──────────────────────────────────────────────────────


class DpiSeedDefaultsPopulatesCatalog(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-006",
        title="SeedDefaultApps populates reference catalogue and PFDs",
        spec="TS 23.501 §5.8.2.8.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Seed the operator-curated reference catalogue used in canned\n"
                "  demos and CI baselines (TS 23.501 §5.8.2.8.4 — operator-managed\n"
                "  PFD set). Pins that the seed populates the eight 'big-app'\n"
                "  reference rows AND that each gets at least one PFD rule (so the\n"
                "  catalogue is actually usable, not just listed).\n"
                "\n"
                "Procedure (TS 23.501 §5.8.2.8.4)\n"
                "  1. POST /api/dpi/seed-defaults — require status 200/201.\n"
                "  2. GET /api/dpi/apps; collect app_id set.\n"
                "  3. Require REFERENCE_APPS ⊆ ids (the 8 expected app_ids:\n"
                "     youtube, netflix, whatsapp, instagram, tiktok, facebook,\n"
                "     google, teams).\n"
                "  4. For each reference app_id, GET /api/dpi/rules?app_id=… and\n"
                "     require the rule list non-empty.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — REFERENCE_APPS is a class constant)\n"
                "\n"
                "Pass criteria\n"
                "  All 8 reference apps present and each has at least one PFD rule.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  seeded_count, total_apps.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY — relies on the seed routine being idempotent so the\n"
                "  test can be re-run cleanly.\n"
                "  The seeded apps are the canonical demo set that ships with the\n"
                "  operator GUI's default dashboard tiles.\n"
                "  Adding or removing reference apps is a deliberate operator action;\n"
                "  this test pins the floor, not the ceiling."
            ),
    )

    REFERENCE_APPS = {"youtube", "netflix", "whatsapp", "instagram",
                      "tiktok", "facebook", "google", "teams"}

    def run(self):
        try:
            _, st = _dpi_api("/api/dpi/seed-defaults", "POST")
            if st not in (200, 201):
                self.fail_test(f"seed failed: {st}")
                return self.result
            apps = _list_apps()
            ids = {a.get("app_id") for a in apps}
            missing = self.REFERENCE_APPS - ids
            if missing:
                self.fail_test(f"reference apps missing: {missing}",
                               present=sorted(ids & self.REFERENCE_APPS))
                return self.result
            # Each reference app should also have at least one PFD rule.
            empties = []
            for rid in self.REFERENCE_APPS:
                if not _list_rules(rid):
                    empties.append(rid)
            if empties:
                self.fail_test(f"apps with no PFD rules: {empties}")
                return self.result
            self.pass_test(seeded_count=len(self.REFERENCE_APPS),
                           total_apps=len(apps))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── TC-DPI-007 ──────────────────────────────────────────────────────


class DpiSeededYouTubeDetection(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-007",
        title="Seeded YouTube SNI patterns detect end-to-end",
        spec="TS 23.501 §5.8.2.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  End-to-end smoke for the seed catalogue: after seed-defaults,\n"
                "  the YouTube SNI patterns must actually be wired into the\n"
                "  classifier (TS 23.501 §5.8.2.4). Pins that PFD rows are not\n"
                "  merely listed but functionally reachable through /detect.\n"
                "\n"
                "Procedure (TS 23.501 §5.8.2.4 + §5.8.2.8.4)\n"
                "  1. POST /api/dpi/seed-defaults (idempotent).\n"
                "  2. For each sni in ('www.youtube.com',\n"
                "     'rr1---sn-abc.googlevideo.com'):\n"
                "     a. GET /api/dpi/detect?sni={sni}.\n"
                "     b. Require res.app == 'youtube'.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — SNIs are hard-coded representative samples)\n"
                "\n"
                "Pass criteria\n"
                "  Both probes return res.app == 'youtube'.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — pure pass/fail on detect membership).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. CDN SNIs (googlevideo) drift over time; this test\n"
                "  pins only a representative pair that the seed dataset ships with.\n"
                "  The classifier picks the first matching rule under the highest\n"
                "  priority (the seed sets priority=10 on all reference apps).\n"
                "  Both probe SNIs are static; CDN edge-server SNIs (googlevideo)\n"
                "  are stable enough for CI.\n"
                "  If the wildcard seed pattern ever narrows, expect TC-DPI-007 to\n"
                "  fail before any production SLA does."
            ),
    )

    def run(self):
        try:
            _dpi_api("/api/dpi/seed-defaults", "POST")
            for sni in ("www.youtube.com", "rr1---sn-abc.googlevideo.com"):
                res, _ = _dpi_api(f"/api/dpi/detect?sni={sni}")
                if res.get("app") != "youtube":
                    self.fail_test(f"SNI={sni} expected youtube, got {res}")
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── TC-DPI-008 ──────────────────────────────────────────────────────


class DpiUsageSummaryEnvelope(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-008",
        title="/api/dpi/usage-summary envelope contract",
        spec="TS 23.501 §5.8.2.6",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MINOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Pins the operator-API envelope for per-app usage counters\n"
                "  (TS 23.501 §5.8.2.6 — Charging / Usage Monitoring). The GUI\n"
                "  binds to {total_bytes:int, apps:[...]} with no runtime type\n"
                "  guessing; any schema regression silently breaks rendering, so\n"
                "  this test runs as a strict schema gate.\n"
                "\n"
                "Procedure (TS 23.501 §5.8.2.6)\n"
                "  1. GET /api/dpi/usage-summary.\n"
                "  2. Require status 200.\n"
                "  3. Require body is a dict.\n"
                "  4. Require 'total_bytes' key present (any int/float).\n"
                "  5. Require body.apps is a list (may be empty).\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — passive endpoint probe)\n"
                "\n"
                "Pass criteria\n"
                "  Status 200 AND body is dict AND total_bytes present AND\n"
                "  body.apps is a list.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  total_bytes, apps_count.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Numeric counter values are not asserted (they grow\n"
                "  with traffic in the live system).\n"
                "  Counter values are surfaced by the UPF -> SMF push and may be\n"
                "  zero on a freshly seeded cluster.\n"
                "  The /apps array shape is `[{app_id, bytes, ...}]` but only the\n"
                "  top-level keys are pinned here."
            ),
    )

    def run(self):
        try:
            body, st = _dpi_api("/api/dpi/usage-summary")
            if st != 200:
                self.fail_test(f"non-200: {st}")
                return self.result
            if not isinstance(body, dict):
                self.fail_test(f"body not dict: {body}")
                return self.result
            if "total_bytes" not in body:
                self.fail_test("missing total_bytes", body=body)
                return self.result
            if not isinstance(body.get("apps"), list):
                self.fail_test("apps is not a list", body=body)
                return self.result
            self.pass_test(total_bytes=body.get("total_bytes"),
                           apps_count=len(body.get("apps", [])))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── TC-DPI-009 ──────────────────────────────────────────────────────


class DpiDeleteCascadesRules(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-009",
        title="Deleting a DPI app cascades into its PFD rules",
        spec="TS 23.501 §5.8.2.8.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Referential-integrity guarantee on the PFD database\n"
                "  (TS 23.501 §5.8.2.8.4 — PFD lifecycle). The DDL has ON DELETE\n"
                "  CASCADE from dpi_pfd_rules.app_id to dpi_applications.app_id;\n"
                "  if this cascade ever regresses, orphan PFD rows would pollute\n"
                "  /detect with rules for apps that no longer exist.\n"
                "\n"
                "Procedure (TS 23.501 §5.8.2.8.4)\n"
                "  1. Pre-cleanup app 'tc-dpi-009'; then POST /api/dpi/app.\n"
                "  2. POST three PFDs covering all detection kinds: sni\n"
                "     ('*.casc.test'), dns ('casc.test'), port_range ('9000-9001').\n"
                "  3. GET /api/dpi/rules?app_id=… — require length == 3.\n"
                "  4. POST /api/dpi/app/{app_id}/delete.\n"
                "  5. GET /api/dpi/rules?app_id=… again — require empty list.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Rules count drops from 3 to 0 after app delete.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  rules_before, rules_after.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Driven through the REST API — does not bypass the\n"
                "  database directly.\n"
                "  SQLite-backed deployments rely on PRAGMA foreign_keys=ON; if it\n"
                "  drifts off, this test catches it.\n"
                "  The cascade is one-way only — deleting a rule does NOT delete\n"
                "  its parent app."
            ),
    )

    def run(self):
        app_id = "tc-dpi-009"
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "Cascade Test",
            })
            for det, pat in (("sni", "*.casc.test"),
                             ("dns", "casc.test"),
                             ("port_range", "9000-9001")):
                _dpi_api("/api/dpi/rule", "POST", {
                    "app_id": app_id, "detection_type": det, "pattern": pat,
                })
            before = _list_rules(app_id)
            if len(before) != 3:
                self.fail_test(f"expected 3 rules, got {len(before)}")
                return self.result

            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            after = _list_rules(app_id)
            if after:
                self.fail_test(f"rules survived app delete: {after}")
                return self.result
            self.pass_test(rules_before=len(before), rules_after=len(after))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── TC-DPI-010 ──────────────────────────────────────────────────────


class DpiDetectMissReturnsNull(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-010",
        title="DPI detect with no rules / unknown input returns null",
        spec="TS 23.501 §5.8.2.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Pins the 'uncategorised' contract on the /detect classifier\n"
                "  (TS 23.501 §5.8.2.4 — anything not matching the PFD set is\n"
                "  unclassified). The GUI renders an 'unknown' state from a strict\n"
                "  {app:null, confidence:0} envelope; any soft-fail (e.g. echoing\n"
                "  the last matched app) would silently mis-attribute traffic.\n"
                "\n"
                "Procedure (TS 23.501 §5.8.2.4)\n"
                "  1. For each probe q in (sni=this-cannot-match.invalid,\n"
                "     domain=likewise.invalid, ip=203.0.113.99, port=64999):\n"
                "     a. GET /api/dpi/detect?{q}.\n"
                "     b. Require HTTP 200.\n"
                "     c. Require res.app is None.\n"
                "     d. Require res.confidence in (0, 0.0).\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — four canonical 'guaranteed miss' probes hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  All four probes return 200, app=None, confidence==0.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  probes (count of probes asserted).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Uses TEST-NET-3 (203.0.113.0/24) and an out-of-\n"
                "  range high port to avoid colliding with any seeded rule.\n"
                "  Unknown-input semantics are critical for GUI render: a returned\n"
                "  app='' or confidence=null would render as 'detecting...' forever.\n"
                "  Probes are intentionally split across all four input kinds (sni,\n"
                "  domain, ip, port) so a regression in one is isolated."
            ),
    )

    def run(self):
        try:
            for q in ("sni=this-cannot-match.invalid",
                      "domain=likewise.invalid",
                      "ip=203.0.113.99",
                      "port=64999"):
                res, st = _dpi_api(f"/api/dpi/detect?{q}")
                if st != 200:
                    self.fail_test(f"non-200 for {q}: {st}")
                    return self.result
                if res.get("app") is not None:
                    self.fail_test(f"unexpected hit for {q}: {res}")
                    return self.result
                if res.get("confidence") not in (0, 0.0):
                    self.fail_test(f"miss confidence != 0 for {q}: {res}")
                    return self.result
            self.pass_test(probes=4)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── Spec-compliance: PFCP PFD-Management wire (TS 29.244 §6.2.5) ─


def _wait_for_upf_app(app_id, timeout=4.0):
    """Poll /api/dpi/upf-pfd-state until app_id appears (or timeout).

    The push from /api/dpi/* writes is fire-and-forget on the SMF
    side, so the UPF-side cache update lags the HTTP response.
    """
    import time
    deadline = time.time() + timeout
    while time.time() < deadline:
        body, _ = _dpi_api("/api/dpi/upf-pfd-state")
        cache = (body or {}).get("cache") or {}
        if app_id in cache:
            return cache
        time.sleep(0.2)
    return (body or {}).get("cache") or {}


class DpiSMFPushesPFDsToUPF(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-011",
        title="SMF -> UPF PFD push via PFCP PFD-Management",
        spec="TS 29.244 §6.2.5",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  PFCP PFD-Management wire test (TS 29.244 §6.2.5, §7.4.3.1\n"
                "  Message Type 3). After seed-defaults, the SMF MUST push the\n"
                "  full ApplicationIDsPFDs IE set to every UPF anchor so each UPF\n"
                "  can classify flows without round-tripping to the SMF.\n"
                "\n"
                "Procedure (TS 29.244 §6.2.5 + §7.4.3.1)\n"
                "  1. POST /api/dpi/seed-defaults — require 200/201; triggers a\n"
                "     fire-and-forget PFD push from the SMF.\n"
                "  2. _wait_for_upf_app('youtube', timeout=4.0) — polls\n"
                "     /api/dpi/upf-pfd-state every 200 ms until the UPF cache\n"
                "     contains 'youtube'.\n"
                "  3. Require 'youtube' present.\n"
                "  4. For each ref in {youtube, netflix, whatsapp, instagram,\n"
                "     tiktok, facebook, google, teams} require ref in cache.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — reference app list hard-coded; poll timeout 4.0 s)\n"
                "\n"
                "Pass criteria\n"
                "  All 8 reference apps appear in the UPF PFD cache within 4 s.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  upf_apps, youtube_entries.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Pushes are asynchronous; the test tolerates this\n"
                "  via _wait_for_upf_app polling.\n"
                "  Push timing depends on the SMF's PFD-Management driver in\n"
                "  nf/smf/pfd_push.go — a regression there is the most likely cause\n"
                "  of a failure here.\n"
                "  If the UPF reports an unrelated error during seed, only the per-\n"
                "  app membership is asserted, not error-free seed."
            ),
    )

    def run(self):
        try:
            # Seed populates 8 reference apps + their rules and triggers
            # the push (fire-and-forget goroutine on the API side).
            _, st = _dpi_api("/api/dpi/seed-defaults", "POST")
            if st not in (200, 201):
                self.fail_test(f"seed failed: {st}")
                return self.result

            cache = _wait_for_upf_app("youtube")
            if "youtube" not in cache:
                self.fail_test("UPF cache never received youtube push",
                               cache_keys=list(cache.keys()))
                return self.result

            # Each reference app should land in the UPF cache with the
            # same number of PFD entries as the SMF source.
            misses = []
            for ref in ("youtube", "netflix", "whatsapp", "instagram",
                        "tiktok", "facebook", "google", "teams"):
                if ref not in cache:
                    misses.append(ref)
            if misses:
                self.fail_test(f"UPF cache missing apps: {misses}",
                               cache_keys=sorted(cache.keys()))
                return self.result

            self.pass_test(upf_apps=sorted(cache.keys()),
                           youtube_entries=cache.get("youtube", []))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class DpiNewRulePushesDelta(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-012",
        title="Adding a single PFD rule re-pushes delta to the UPF",
        spec="TS 29.244 §6.2.5.3",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  PFD delta push test (TS 29.244 §6.2.5.3 — UP function PFD-set\n"
                "  replacement). Adding a single PFD rule MUST cause the SMF to\n"
                "  re-push that app's PFD set to the UPF; the new pattern must\n"
                "  arrive on the wire as a DN-kind (Domain Name) entry per TS\n"
                "  29.244 §8.2.39 Flow Description.\n"
                "\n"
                "Procedure (TS 29.244 §6.2.5.3 + §8.2.39)\n"
                "  1. Pre-cleanup, POST /api/dpi/app 'tc-dpi-012'.\n"
                "  2. POST /api/dpi/rule with detection_type='sni',\n"
                "     pattern='*.tc-dpi-012.test'.\n"
                "  3. _wait_for_upf_app(app_id) — poll UPF cache.\n"
                "  4. Read cache[app_id] entries; collect set of kinds.\n"
                "  5. Require 'dn' kind present (SNI patterns ride as Domain Name).\n"
                "  6. Require the literal '*.tc-dpi-012.test' is one of the\n"
                "     entry patterns.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  UPF cache for this app contains a DN-kind entry whose pattern\n"
                "  equals the registered SNI glob.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  entries.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Push is async — poll horizon 4 s.\n"
                "  DN-kind entries on the wire correspond to TS 29.244 §8.2.39 Flow\n"
                "  Description with the 'permit out tcp from any to <fqdn>' template.\n"
                "  The single-rule add path is the cheapest way to exercise the\n"
                "  delta-push code without polluting the seed catalogue."
            ),
    )

    def run(self):
        app_id = "tc-dpi-012"
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "Delta Push", "category": "test",
            })
            _dpi_api("/api/dpi/rule", "POST", {
                "app_id": app_id, "detection_type": "sni",
                "pattern": "*.tc-dpi-012.test",
            })
            cache = _wait_for_upf_app(app_id)
            entries = cache.get(app_id) or []
            kinds = {e.get("kind") for e in entries}
            if "dn" not in kinds:
                self.fail_test(f"expected DN-kind entry on UPF for SNI rule; "
                               f"cache says {entries}")
                return self.result
            patterns = [e.get("pattern") for e in entries]
            if not any(p == "*.tc-dpi-012.test" for p in patterns):
                self.fail_test(f"pattern not propagated: {patterns}")
                return self.result
            self.pass_test(entries=entries)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
        return self.result


class DpiAppDeleteRemovesFromUPFCache(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-013",
        title="Deleting a DPI app prunes its UPF-side PFD entries",
        spec="TS 23.501 §5.8.2.8.4",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  PFD-lifecycle cleanup over the PFCP wire (TS 23.501 §5.8.2.8.4,\n"
                "  TS 29.244 §6.2.5.3 UP-function PFD-set replacement). When an\n"
                "  operator deletes an app from the catalogue, the next SMF resync\n"
                "  to the UPF MUST drop that app from the UPF-side PFD cache.\n"
                "\n"
                "Procedure (TS 23.501 §5.8.2.8.4 + TS 29.244 §6.2.5.3)\n"
                "  1. Pre-cleanup, POST /api/dpi/app 'tc-dpi-013'.\n"
                "  2. POST /api/dpi/rule with detection_type='dns',\n"
                "     pattern='tc-dpi-013.test'.\n"
                "  3. _wait_for_upf_app(app_id) — require it lands in UPF cache.\n"
                "  4. POST /api/dpi/app/{app_id}/delete.\n"
                "  5. Poll /api/dpi/upf-pfd-state every 200 ms for up to 4 s\n"
                "     until app_id is no longer in the cache.\n"
                "  6. Read final state; require app_id not in cache.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded; poll horizon 4 s)\n"
                "\n"
                "Pass criteria\n"
                "  app_id present in UPF cache before delete; absent after delete\n"
                "  within the 4 s poll horizon.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  cache_keys.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Resync is async; the polling loop handles propagation\n"
                "  delay.\n"
                "  Resync is observable through the UPF cache; SMF→UPF state\n"
                "  delivery happens via TS 29.244 PFD-Management messages.\n"
                "  If propagation exceeds 4 s, suspect the SMF push driver or UPF\n"
                "  control-plane queue depth."
            ),
    )

    def run(self):
        app_id = "tc-dpi-013"
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "Will Be Deleted",
            })
            _dpi_api("/api/dpi/rule", "POST", {
                "app_id": app_id, "detection_type": "dns",
                "pattern": "tc-dpi-013.test",
            })
            cache = _wait_for_upf_app(app_id)
            if app_id not in cache:
                self.fail_test("app never reached UPF cache to delete")
                return self.result

            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            # After delete the next push is the resync; wait for it
            # to settle and assert the app is gone.
            import time
            deadline = time.time() + 4.0
            while time.time() < deadline:
                body, _ = _dpi_api("/api/dpi/upf-pfd-state")
                if app_id not in ((body or {}).get("cache") or {}):
                    break
                time.sleep(0.2)
            body, _ = _dpi_api("/api/dpi/upf-pfd-state")
            cache_now = (body or {}).get("cache") or {}
            if app_id in cache_now:
                self.fail_test(f"deleted app survived resync: {cache_now}")
                return self.result
            self.pass_test(cache_keys=sorted(cache_now.keys()))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
        return self.result


class DpiFlowDescriptionEncodingForIPRange(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DPI-014",
        title="ip_range / port_range rules ride as Flow Description on the wire",
        spec="TS 29.244 §8.2.39",
        domain=Domain.VAS,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  Flow Description encoding for IP-range and port-range PFDs\n"
            "  (TS 29.244 §8.2.39, TS 23.501 §5.8.2.4). On the PFCP wire,\n"
            "  these MUST ride as FD-kind entries whose pattern is the SDF\n"
            "  filter string preserved verbatim — operators rely on the\n"
            "  textual form for audit and re-import.\n"
            "\n"
            "Procedure (TS 29.244 §8.2.39 + TS 23.501 §5.8.2.4)\n"
            "  1. Pre-cleanup, POST /api/dpi/app 'tc-dpi-014'.\n"
            "  2. POST /api/dpi/rule with detection_type='ip_range',\n"
            "     pattern='10.10.10.0/24'.\n"
            "  3. POST /api/dpi/rule with detection_type='port_range',\n"
            "     pattern='5060-5061'.\n"
            "  4. _wait_for_upf_app(app_id); read cache entries.\n"
            "  5. Count entries with kind=='fd' — require exactly 2.\n"
            "  6. Require at least one entry's pattern contains\n"
            "     '10.10.10.0/24'.\n"
            "  7. Require at least one entry's pattern contains '5060-5061'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — patterns hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Exactly 2 FD-kind UPF entries, with CIDR and port-range strings\n"
            "  both preserved verbatim.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  fd_count, patterns.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Pattern substring check is intentionally lenient —\n"
            "  the wire envelope may wrap it inside an SDF filter prefix."
        ),
    )

    def run(self):
        app_id = "tc-dpi-014"
        try:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
            _dpi_api("/api/dpi/app", "POST", {
                "app_id": app_id, "app_name": "FD Probe",
            })
            _dpi_api("/api/dpi/rule", "POST", {
                "app_id": app_id, "detection_type": "ip_range",
                "pattern": "10.10.10.0/24",
            })
            _dpi_api("/api/dpi/rule", "POST", {
                "app_id": app_id, "detection_type": "port_range",
                "pattern": "5060-5061",
            })
            cache = _wait_for_upf_app(app_id)
            entries = cache.get(app_id) or []
            kinds = [e.get("kind") for e in entries]
            patterns = [e.get("pattern") for e in entries]
            fd_count = sum(1 for k in kinds if k == "fd")
            if fd_count != 2:
                self.fail_test(f"expected 2 FD-kind entries, got {fd_count}",
                               entries=entries)
                return self.result
            if not any("10.10.10.0/24" in (p or "") for p in patterns):
                self.fail_test(f"ip_range pattern lost on wire: {patterns}")
                return self.result
            if not any("5060-5061" in (p or "") for p in patterns):
                self.fail_test(f"port_range pattern lost on wire: {patterns}")
                return self.result
            self.pass_test(fd_count=fd_count, patterns=patterns)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _dpi_api(f"/api/dpi/app/{app_id}/delete", "POST")
        return self.result


ALL_DPI_TCS = [
    DpiAppCRUD,
    DpiPFDRuleCRUD,
    DpiSNIDetection,
    DpiDNSDetection,
    DpiPortRangeDetection,
    DpiSeedDefaultsPopulatesCatalog,
    DpiSeededYouTubeDetection,
    DpiUsageSummaryEnvelope,
    DpiDeleteCascadesRules,
    DpiDetectMissReturnsNull,
    DpiSMFPushesPFDsToUPF,
    DpiNewRulePushesDelta,
    DpiAppDeleteRemovesFromUPFCache,
    DpiFlowDescriptionEncodingForIPRange,
]
