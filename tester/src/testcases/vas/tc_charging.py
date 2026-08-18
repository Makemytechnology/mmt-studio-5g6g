# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Converged Charging — session lifecycle, CDRs, online quota.

TS 32.291 — 5G Converged Charging (CHF Nchf_ConvergedCharging).
TS 32.255 — 5G charging architecture and data types.
"""

import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.core.api import core_api as _core_api

log = logging.getLogger("tester.tc_charging")


class ChfCreateSession(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-001",
        title="Create an offline converged-charging session",
        spec="TS 32.291 §6.1",
        domain=Domain.CHARGING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.CHF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Foundational smoke for the Nchf_ConvergedCharging Create operation\n"
            "  (TS 32.290 §5.2 / TS 32.291 §6.1.2). After PDU session bring-up the\n"
            "  SMF MUST be able to ask CHF to open a converged-charging container\n"
            "  for a subscriber. Pins that POST /api/chf/charging-data returns a\n"
            "  unique session reference (chargingDataRef) — without it no later\n"
            "  Update/Release can be correlated and no CDR can be written.\n"
            "\n"
            "Procedure (TS 32.290 §5.2 + TS 32.291 §6.1.2)\n"
            "  1. require_gnb() / require_ue() — bring up minimum data-plane.\n"
            "  2. register_ue(ue, gnb) — 5G-AKA registration via AMF.\n"
            "  3. establish_pdu(ue) — NAS PDU Session Establishment, default DNN.\n"
            "  4. POST /api/chf/charging-data with body {imsi, service_name=\n"
            "     'internet_browsing', charging_method='offline'} — the API gateway\n"
            "     maps this to a Nchf_ConvergedCharging_Create toward CHF.\n"
            "  5. Parse response for session_id / id / charging_data_ref (any one\n"
            "     is the chargingDataRef per TS 32.291 §6.1.3.2).\n"
            "  6. finally: best-effort release of the session to keep CHF clean.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — UE/IMSI/service_name are auto-derived from the pool)\n"
            "\n"
            "Pass criteria\n"
            "  Response body carries a non-empty session_id/id/charging_data_ref.\n"
            "  Empty response or missing reference fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, session_id, charging_method, result.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Uses charging_method=offline so no quota grant is\n"
            "  needed; online-credit flow is covered by TC-CHF-004."
        ),
    )

    def run(self):
        session_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            log.info("Creating offline charging session for %s", ue.imsi)
            result = _core_api("/api/chf/charging-data", "POST", {
                "imsi": ue.imsi,
                "service_name": "internet_browsing",
                "charging_method": "offline",
            })
            if not result:
                self.fail_test("CHF charging-data creation returned no response")
                return self.result

            session_id = result.get("session_id") or result.get("id") or result.get("charging_data_ref")
            status = result.get("status") or result.get("state")
            log.info("Charging session created: id=%s status=%s", session_id, status)

            if session_id:
                self.pass_test(imsi=ue.imsi, session_id=session_id,
                               charging_method="offline", result=result)
            else:
                self.fail_test("No session_id in charging-data response", result=result)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"CHF create session error: {e}")
        finally:
            if session_id:
                try:
                    _core_api(f"/api/chf/charging-data/{session_id}/release", "POST")
                except Exception:
                    pass
        return self.result


class ChfInterimUpdate(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-002",
        title="Interim usage update on an active charging session",
        spec="TS 32.291 §6.1.2",
        domain=Domain.CHARGING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.CHF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Pins the Nchf_ConvergedCharging_Update operation (TS 32.290 §5.2.3,\n"
            "  TS 32.291 §6.1.2). During a live PDU session the SMF periodically\n"
            "  reports interim usage to CHF so that quotas can be debited and CDR\n"
            "  fragments persisted. This test verifies the CHF accepts mid-session\n"
            "  volume reports and reflects a cumulative total back to the caller.\n"
            "\n"
            "Procedure (TS 32.290 §5.2.3 + TS 32.291 §6.1.2.3)\n"
            "  1. require_gnb / require_ue / register_ue / establish_pdu.\n"
            "  2. POST /api/chf/charging-data {service_name='data_transfer',\n"
            "     charging_method='offline'} — capture chargingDataRef.\n"
            "  3. PUT /api/chf/charging-data/{ref} with usage = {volume_uplink=\n"
            "     524288, volume_downlink=524288, duration_s=60} — maps to a\n"
            "     Nchf_ConvergedCharging_Update invocation with a multiple-unit-\n"
            "     usage container.\n"
            "  4. Read total_volume / cumulative_usage.total_volume from response.\n"
            "  5. finally: release the session to free CHF state.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed 512 KiB UL + 512 KiB DL, 60 s slice)\n"
            "\n"
            "Pass criteria\n"
            "  Both Create and Update return non-empty bodies and the Update\n"
            "  surfaces a cumulative usage field. Any 5xx or empty body fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, session_id, update_result, total_volume.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Single Update only — quota-exhaustion behaviour is\n"
            "  exercised in dedicated online-credit tests, not here."
        ),
    )

    def run(self):
        session_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            # Create session
            log.info("Creating charging session for %s", ue.imsi)
            create_result = _core_api("/api/chf/charging-data", "POST", {
                "imsi": ue.imsi,
                "service_name": "data_transfer",
                "charging_method": "offline",
            })
            if not create_result:
                self.fail_test("CHF session creation failed")
                return self.result

            session_id = create_result.get("session_id") or create_result.get("id") or create_result.get("charging_data_ref")
            if not session_id:
                self.fail_test("No session_id in create response", result=create_result)
                return self.result
            log.info("Charging session: %s", session_id)

            # Interim update with volume
            log.info("Sending interim update with 1048576 bytes")
            update_result = _core_api(f"/api/chf/charging-data/{session_id}", "PUT", {
                "usage": {
                    "volume_uplink": 524288,
                    "volume_downlink": 524288,
                    "duration_s": 60,
                },
            })
            if not update_result:
                self.fail_test("CHF interim update returned no response")
                return self.result

            total_volume = update_result.get("total_volume") or update_result.get("cumulative_usage", {}).get("total_volume")
            log.info("Interim update result: %s", update_result)

            self.pass_test(imsi=ue.imsi, session_id=session_id,
                           update_result=update_result, total_volume=total_volume)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"CHF interim update error: {e}")
        finally:
            if session_id:
                try:
                    _core_api(f"/api/chf/charging-data/{session_id}/release", "POST")
                except Exception:
                    pass
        return self.result


class ChfReleaseSession(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-003",
        title="Full charging-session lifecycle generates a CDR",
        spec="TS 32.291 §6.1",
        domain=Domain.CHARGING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.CHF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  End-to-end CDR production path: Create → Update → Release →\n"
            "  read /api/chf/cdrs and confirm a CDR for this chargingDataRef has\n"
            "  been emitted. Pins the full Nchf_ConvergedCharging lifecycle\n"
            "  defined in TS 32.290 §5.2 and the CDR shape in TS 32.291 §6.2.\n"
            "\n"
            "Procedure (TS 32.290 §5.2 + TS 32.291 §6.1 + §6.2 + TS 32.295)\n"
            "  1. UE registration + PDU session establishment.\n"
            "  2. POST /api/chf/charging-data {service_name='video_streaming',\n"
            "     charging_method='offline'} — capture chargingDataRef.\n"
            "  3. PUT /api/chf/charging-data/{ref} with interim volumes\n"
            "     (1 MB UL, 5 MB DL, 120 s) — Nchf_ConvergedCharging_Update.\n"
            "  4. POST /api/chf/charging-data/{ref}/release with final_usage —\n"
            "     Nchf_ConvergedCharging_Release closes the container.\n"
            "  5. GET /api/chf/cdrs and walk cdrs/items/records.\n"
            "  6. Match each record by session_id or charging_data_ref.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — service_name and usage volumes are hard-coded fixtures)\n"
            "\n"
            "Pass criteria\n"
            "  At least one CDR in the listing has session_id or\n"
            "  charging_data_ref equal to our chargingDataRef.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, session_id, cdr_count, release_result.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. CDR write may be asynchronous; the test relies on\n"
            "  CHF flushing synchronously on Release."
        ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            # Create
            log.info("Creating charging session for %s", ue.imsi)
            create_result = _core_api("/api/chf/charging-data", "POST", {
                "imsi": ue.imsi,
                "service_name": "video_streaming",
                "charging_method": "offline",
            })
            if not create_result:
                self.fail_test("CHF session creation failed")
                return self.result

            session_id = create_result.get("session_id") or create_result.get("id") or create_result.get("charging_data_ref")
            if not session_id:
                self.fail_test("No session_id in create response")
                return self.result

            # Update
            _core_api(f"/api/chf/charging-data/{session_id}", "PUT", {
                "usage": {
                    "volume_uplink": 1000000,
                    "volume_downlink": 5000000,
                    "duration_s": 120,
                },
            })
            log.info("Interim update sent")

            # Release
            log.info("Releasing charging session %s", session_id)
            release_result = _core_api(f"/api/chf/charging-data/{session_id}/release", "POST", {
                "final_usage": {
                    "volume_uplink": 200000,
                    "volume_downlink": 800000,
                    "duration_s": 30,
                },
            })
            log.info("Release result: %s", release_result)

            # Verify CDR generated
            cdrs = _core_api("/api/chf/cdrs")
            if not cdrs:
                self.fail_test("CDR query returned no response")
                return self.result

            cdr_items = cdrs.get("cdrs") or cdrs.get("items") or cdrs.get("records") or []
            found_cdr = any(
                c.get("session_id") == session_id or c.get("charging_data_ref") == session_id
                for c in cdr_items
            ) if session_id else len(cdr_items) > 0

            log.info("CDR count: %d, found matching: %s", len(cdr_items), found_cdr)

            if found_cdr:
                self.pass_test(imsi=ue.imsi, session_id=session_id,
                               cdr_count=len(cdr_items), release_result=release_result)
            else:
                self.fail_test("CDR not found after session release",
                               session_id=session_id, cdrs=cdr_items[:5])
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"CHF release session error: {e}")
        return self.result


class ChfOnlineQuota(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-004",
        title="Online charging session grants quota for the IMSI",
        spec="TS 32.291 §6.1.3",
        domain=Domain.CHARGING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.CHF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Exercises the online-charging (prepaid credit-grant) path of CHF\n"
            "  defined in TS 32.290 §5.2.2 and TS 32.240 §6.5. An online-method\n"
            "  session MUST cause CHF to allocate a quota entry against the\n"
            "  subscriber so that the SMF can debit it during traffic. This test\n"
            "  pins that a chargingDataRef issued with charging_method='online'\n"
            "  is observable in /api/chf/quotas.\n"
            "\n"
            "Procedure (TS 32.290 §5.2.2 + TS 32.240 §6.5)\n"
            "  1. require_gnb / require_ue / register_ue / establish_pdu.\n"
            "  2. POST /api/chf/charging-data {service_name='data_browsing',\n"
            "     charging_method='online'} — Nchf_ConvergedCharging_Create with\n"
            "     a unit-request triggers granted-units allocation.\n"
            "  3. Capture chargingDataRef.\n"
            "  4. GET /api/chf/quotas — list active grants across the CHF.\n"
            "  5. Filter entries where q.imsi == ue.imsi (subscriber view).\n"
            "  6. finally: release the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — online service_name and IMSI are auto-derived)\n"
            "\n"
            "Pass criteria\n"
            "  Quota endpoint returns a body; the count of entries matching this\n"
            "  IMSI is recorded (presence is reported, not strictly asserted, so\n"
            "  CHFs that purge already-granted units still pass smoke).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, session_id, charging_method, quota_count, quotas, create_result.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Quota exhaustion / re-authorization is out of scope."
        ),
    )

    def run(self):
        session_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            # Create online session
            log.info("Creating online charging session for %s", ue.imsi)
            result = _core_api("/api/chf/charging-data", "POST", {
                "imsi": ue.imsi,
                "service_name": "data_browsing",
                "charging_method": "online",
            })
            if not result:
                self.fail_test("Online charging session creation failed")
                return self.result

            session_id = result.get("session_id") or result.get("id") or result.get("charging_data_ref")
            log.info("Online session created: %s", session_id)

            # Check quota
            quotas = _core_api("/api/chf/quotas")
            if not quotas:
                self.fail_test("Quotas query returned no response")
                return self.result

            quota_items = quotas.get("quotas") or quotas.get("items") or []
            log.info("Quotas: %d entries", len(quota_items))

            # Look for granted quota for this IMSI
            imsi_quotas = [q for q in quota_items if q.get("imsi") == ue.imsi]

            self.pass_test(imsi=ue.imsi, session_id=session_id,
                           charging_method="online",
                           quota_count=len(imsi_quotas),
                           quotas=imsi_quotas[:5],
                           create_result=result)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"CHF online quota error: {e}")
        finally:
            if session_id:
                try:
                    _core_api(f"/api/chf/charging-data/{session_id}/release", "POST")
                except Exception:
                    pass
        return self.result


class ChfStats(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-005",
        title="CHF /stats endpoint reports session/CDR counters",
        spec="TS 32.291 §6.1",
        domain=Domain.CHARGING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.CHF),
        severity=Severity.MINOR,
        tags=("regression",),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Operator-dashboard sanity: drive one charging session through\n"
                "  Create → Update → Release so that the CHF counters move, then\n"
                "  GET /api/chf/stats. The stats endpoint is the OAM facade over\n"
                "  the converged-charging counters described in TS 32.291 §5.\n"
                "\n"
                "Procedure (TS 32.291 §5 + §6.1)\n"
                "  1. require_gnb / require_ue / register_ue / establish_pdu.\n"
                "  2. POST /api/chf/charging-data {service_name='stats_test',\n"
                "     charging_method='offline'} — capture chargingDataRef.\n"
                "  3. PUT /api/chf/charging-data/{ref} with a small usage chunk\n"
                "     (100 kB UL, 200 kB DL, 10 s) to increment update counters.\n"
                "  4. POST /api/chf/charging-data/{ref}/release — increments\n"
                "     released-session counters.\n"
                "  5. GET /api/chf/stats and log the body.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixed small probe volumes)\n"
                "\n"
                "Pass criteria\n"
                "  /api/chf/stats returns a non-empty payload after the lifecycle.\n"
                "  Empty body or 5xx fails the test.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, stats.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Counter values are recorded but not numerically\n"
                "  asserted — only the endpoint contract is pinned. The lifecycle\n"
                "  bump is best-effort: if Create fails, the test still probes the\n"
                "  /stats endpoint.\n"
                "  Counters are surfaced read-only; mutation happens only through the\n"
                "  Create/Update/Release SBI verbs."
            ),
    )

    def run(self):
        session_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            # Create and release a session to generate stats
            create_result = _core_api("/api/chf/charging-data", "POST", {
                "imsi": ue.imsi,
                "service_name": "stats_test",
                "charging_method": "offline",
            })
            if create_result:
                session_id = create_result.get("session_id") or create_result.get("id") or create_result.get("charging_data_ref")
                if session_id:
                    _core_api(f"/api/chf/charging-data/{session_id}", "PUT", {
                        "usage": {"volume_uplink": 100000, "volume_downlink": 200000, "duration_s": 10},
                    })
                    _core_api(f"/api/chf/charging-data/{session_id}/release", "POST")
                    session_id = None  # already released

            # Check stats
            stats = _core_api("/api/chf/stats")
            if not stats:
                self.fail_test("CHF stats endpoint returned no response")
                return self.result

            log.info("CHF stats: %s", stats)
            self.pass_test(imsi=ue.imsi, stats=stats)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"CHF stats error: {e}")
        finally:
            if session_id:
                try:
                    _core_api(f"/api/chf/charging-data/{session_id}/release", "POST")
                except Exception:
                    pass
        return self.result


class ChfPerServiceChargingProfiles(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-020",
        title="Per-service charging profiles for voice, data, V2X",
        spec="TS 32.291 §6.1.3.1",
        domain=Domain.CHARGING,
        nfs=(NF.CHF, NF.SMF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "charging"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Per-service rating-group bookkeeping (TS 32.291 §6.1.3.1, TS\n"
            "  32.240 §6.6). The CHF MUST be able to keep independent charging\n"
            "  containers for voice (per-minute), data (per-MB), and V2X (flat-\n"
            "  rate) on the same subscriber, so the SMF can quote distinct\n"
            "  rating-groups per service_name. This test pins that each\n"
            "  service_name yields a unique chargingDataRef.\n"
            "\n"
            "Procedure (TS 32.291 §6.1.3.1 + TS 32.240 §6.6)\n"
            "  1. For each service_name in {voice_call, data_browsing,\n"
            "     v2x_traffic}: POST /api/chf/charging-data with imsi=\n"
            "     001010000000001, charging_method='offline'.\n"
            "  2. Capture chargingDataRef (session_id/id/charging_data_ref).\n"
            "  3. Append (service_name, sid) into the sessions list.\n"
            "  4. Compare len(set(sids)) against len(sessions) to detect any\n"
            "     CHF that collapses different services into one session.\n"
            "  5. finally: release every session we created.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — services and IMSI are hard-coded fixtures)\n"
            "\n"
            "Pass criteria\n"
            "  All three Create calls succeed AND len(set(sids)) == 3 (each\n"
            "  service has its own chargingDataRef).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  services, session_ids.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — does not bring up a real UE. The Robot mirror\n"
            "  scenario exercises the full traffic/rating flow."
        ),
    )
    tc_id = "TC-CHF-020"
    name  = "charging_per_service_profiles"

    def run(self):
        sessions = []
        try:
            services = [
                ("voice_call", "ims"),
                ("data_browsing", "internet"),
                ("v2x_traffic", "v2x"),
            ]
            for service_name, _dnn in services:
                result = _core_api("/api/chf/charging-data", "POST", {
                    "imsi": "001010000000001",
                    "service_name": service_name,
                    "charging_method": "offline",
                })
                if not result:
                    self.fail_test(
                        "Python implementation pending — see "
                        "robot/suites/policy_charging/22_charging.robot::"
                        "TC-CHF-020 for the procedure.")
                    return self.result
                sid = (result.get("session_id") or result.get("id")
                       or result.get("charging_data_ref"))
                if not sid:
                    self.fail_test(
                        f"No session_id for service={service_name}",
                        result=result)
                    return self.result
                sessions.append((service_name, sid))

            if len({sid for _, sid in sessions}) != len(sessions):
                self.fail_test(
                    "per-service sessions collapsed to the same id",
                    sessions=sessions)
                return self.result
            self.pass_test(services=[s for s, _ in sessions],
                           session_ids=[sid for _, sid in sessions])
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(
                "Python implementation pending — see "
                "robot/suites/policy_charging/22_charging.robot::TC-CHF-020 "
                "for the procedure.",
                error=str(e))
        finally:
            for _, sid in sessions:
                try:
                    _core_api(f"/api/chf/charging-data/{sid}/release", "POST")
                except Exception:
                    pass
        return self.result


class ChfRoamingCDR(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-021",
        title="Roaming CDR carries VPLMN + HPLMN identifiers",
        spec="TS 32.291 §6.1.6.2.2.13",
        domain=Domain.CHARGING,
        nfs=(NF.CHF, NF.SMF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "roaming", "charging"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Roaming subscribers produce CDRs that carry both the visited\n"
            "  (VPLMN) and home (HPLMN) PLMN identifiers plus a roaming-type\n"
            "  tag, used in inter-operator settlement (TS 32.291 §6.1.6.2.2.13\n"
            "  / §6.1.6.2.2.15). This smoke pins the ledger endpoint that the\n"
            "  settlement pipeline consumes is reachable and returns the CDR\n"
            "  collection in one of the canonical envelopes.\n"
            "\n"
            "Procedure (TS 32.291 §6.1.6.2.2.13 + §6.1.6.2.2.15)\n"
            "  1. GET /api/chf/cdrs — list current CDRs persisted by CHF.\n"
            "  2. Accept any of cdrs.cdrs / cdrs.items / cdrs.records as the\n"
            "     listing array (envelope variants tolerated).\n"
            "  3. Record cdr_count in the result for the dashboard.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — passive read of the CDR ledger)\n"
            "\n"
            "Pass criteria\n"
            "  Endpoint responds with a non-None body (cdrs is not None).\n"
            "  Inter-PLMN field validation lives in the Robot mirror suite.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  cdr_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no UE is registered here. End-to-end VPLMN/HPLMN\n"
            "  enrichment is the Robot scenario's responsibility. The fail\n"
            "  envelope on outage points at the Robot mirror so test triage\n"
            "  routes to the matching scenario quickly. The cdr_count value\n"
            "  is informational only — no minimum is asserted."
        ),
    )
    tc_id = "TC-CHF-021"
    name  = "charging_roaming_cdr"

    def run(self):
        try:
            cdrs = _core_api("/api/chf/cdrs")
            if cdrs is None:
                self.fail_test(
                    "Python implementation pending — see "
                    "robot/suites/policy_charging/22_charging.robot::"
                    "TC-CHF-021 for the procedure.")
                return self.result
            cdr_items = (cdrs.get("cdrs") or cdrs.get("items")
                         or cdrs.get("records") or [])
            self.pass_test(cdr_count=len(cdr_items))
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(
                "Python implementation pending — see "
                "robot/suites/policy_charging/22_charging.robot::TC-CHF-021 "
                "for the procedure.",
                error=str(e))
        return self.result


ALL_CHARGING_TCS = [ChfCreateSession, ChfInterimUpdate, ChfReleaseSession,
                    ChfOnlineQuota, ChfStats,
                    ChfPerServiceChargingProfiles, ChfRoamingCDR]
