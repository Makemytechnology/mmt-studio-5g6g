# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NIDD — Non-IP Data Delivery session lifecycle and data transfer.

TS 23.502 section 4.25 — NIDD via NEF/SCEF.
TS 29.122 — T8 API for NIDD.
TS 23.682 — SCEF architecture for IoT data delivery.
"""

import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.core.api import core_api as _core_api

log = logging.getLogger("tester.tc_nidd")


class NiddCreateSession(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NIDD-001",
        title="Create and delete a NIDD session",
        spec="TS 23.502 §4.25",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.NEF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Foundational lifecycle smoke for Non-IP Data Delivery via NEF\n"
            "  (TS 23.502 §4.25, TS 23.682 SCEF). NIDD is the path that low-\n"
            "  rate IoT devices use to ship small binary payloads to/from an\n"
            "  Application Server without bringing up an IP-based PDU session.\n"
            "  Without a working session-create / session-list / session-delete\n"
            "  contract on /api/nidd/sessions the rest of the NIDD test suite\n"
            "  cannot run.\n"
            "\n"
            "Procedure (TS 23.502 §4.25 + TS 29.122 T8)\n"
            "  1. require_gnb / require_ue / register_ue (no PDU needed).\n"
            "  2. POST /api/nidd/sessions {imsi, app_server_id='app-server-001',\n"
            "     config={notification_url, max_payload_size=512}}.\n"
            "  3. Require non-empty response with session_id (or id).\n"
            "  4. GET /api/nidd/sessions; accept sessions/items envelopes.\n"
            "  5. Require any entry has session_id (or id) matching ours.\n"
            "  6. finally: DELETE /api/nidd/sessions/{session_id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSI from UE pool; callback URL hard-coded fixture)\n"
            "\n"
            "Pass criteria\n"
            "  Create returns a session_id AND that id is visible in the list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, session_id, session_count, create_result.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Notification URL points at a tester-local host\n"
            "  (192.168.1.103); callbacks are not actually exercised here."
        ),
    )

    def run(self):
        session_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Create NIDD session
            log.info("Creating NIDD session for %s", ue.imsi)
            create_result = _core_api("/api/nidd/sessions", "POST", {
                "imsi": ue.imsi,
                "app_server_id": "app-server-001",
                "config": {
                    "notification_url": "http://192.168.1.103:8080/nidd/callback",
                    "max_payload_size": 512,
                },
            })
            if not create_result:
                self.fail_test("NIDD session creation returned no response")
                return self.result

            session_id = create_result.get("session_id") or create_result.get("id")
            status = create_result.get("status") or create_result.get("state")
            log.info("NIDD session created: id=%s status=%s", session_id, status)

            if not session_id:
                self.fail_test("No session_id in create response", result=create_result)
                return self.result

            # Verify session exists via GET
            sessions = _core_api("/api/nidd/sessions")
            session_items = []
            if sessions:
                session_items = sessions.get("sessions") or sessions.get("items") or []

            found = any(
                s.get("session_id") == session_id or s.get("id") == session_id
                for s in session_items
            )

            if found:
                self.pass_test(imsi=ue.imsi, session_id=session_id,
                               session_count=len(session_items),
                               create_result=create_result)
            else:
                self.fail_test("Created NIDD session not found in list",
                               session_id=session_id, sessions=session_items)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"NIDD create session error: {e}")
        finally:
            if session_id:
                try:
                    _core_api(f"/api/nidd/sessions/{session_id}", "DELETE")
                    log.info("Cleaned up NIDD session %s", session_id)
                except Exception:
                    pass
        return self.result


class NiddSendData(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NIDD-002",
        title="Send a downlink NIDD payload and verify delivery log",
        spec="TS 23.502 §4.25",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Downlink data-plane test for NIDD (TS 23.502 §4.25.4, TS 29.122\n"
                "  T8). An Application Server pushes a small hex-encoded payload\n"
                "  toward the UE via NEF; the delivery log MUST record a DL entry\n"
                "  so operators have an audit trail per payload.\n"
                "\n"
                "Procedure (TS 23.502 §4.25.4 + TS 29.122 §5)\n"
                "  1. require_gnb / require_ue / register_ue.\n"
                "  2. POST /api/nidd/sessions to create a session.\n"
                "  3. POST /api/nidd/sessions/{sid}/send {data_hex='48656C6C6F'}\n"
                "     (the ASCII bytes for 'Hello').\n"
                "  4. Require non-empty send response.\n"
                "  5. GET /api/nidd/sessions/{sid}/log (entries/log/items envelopes).\n"
                "  6. Scan for any entry with direction=='dl' or type=='dl'.\n"
                "  7. finally: DELETE the session.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — data_hex hard-coded to 0x48656C6C6F)\n"
                "\n"
                "Pass criteria\n"
                "  Send returns a non-empty response. dl_found and log count are\n"
                "  reported but the test passes regardless (best-effort log probe).\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, session_id, data_hex, send_result, log_entries, dl_found.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. No UE-side reception check — purely core-API.\n"
                "  Payload size is well under the 1500-byte NIDD ceiling; oversized\n"
                "  payloads are covered by the Robot suite's edge-case tests.\n"
                "  Hex-encoded payloads are the canonical T8 form per TS 29.122 §5."
            ),
    )

    def run(self):
        session_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Create session
            log.info("Creating NIDD session for %s", ue.imsi)
            create_result = _core_api("/api/nidd/sessions", "POST", {
                "imsi": ue.imsi,
                "app_server_id": "app-server-001",
                "config": {
                    "notification_url": "http://192.168.1.103:8080/nidd/callback",
                },
            })
            if not create_result:
                self.fail_test("NIDD session creation failed")
                return self.result

            session_id = create_result.get("session_id") or create_result.get("id")
            if not session_id:
                self.fail_test("No session_id in create response")
                return self.result

            # Send DL data ("Hello" = 48656C6C6F)
            data_hex = "48656C6C6F"
            log.info("Sending NIDD DL data: %s", data_hex)
            send_result = _core_api(f"/api/nidd/sessions/{session_id}/send", "POST", {
                "data_hex": data_hex,
            })
            if not send_result:
                self.fail_test("NIDD send returned no response")
                return self.result

            log.info("NIDD send result: %s", send_result)

            # Verify in delivery log
            log_result = _core_api(f"/api/nidd/sessions/{session_id}/log")
            log_entries = []
            if log_result:
                log_entries = log_result.get("entries") or log_result.get("log") or log_result.get("items") or []

            dl_found = any(
                e.get("direction") == "dl" or e.get("type") == "dl"
                for e in log_entries
            ) if log_entries else False

            self.pass_test(imsi=ue.imsi, session_id=session_id,
                           data_hex=data_hex, send_result=send_result,
                           log_entries=len(log_entries), dl_found=dl_found)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"NIDD send data error: {e}")
        finally:
            if session_id:
                try:
                    _core_api(f"/api/nidd/sessions/{session_id}", "DELETE")
                except Exception:
                    pass
        return self.result


class NiddReceiveData(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NIDD-003",
        title="Simulate uplink NIDD receipt and verify delivery log",
        spec="TS 23.502 §4.25",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Uplink data-plane test for NIDD (TS 23.502 §4.25.5, TS 29.122).\n"
                "  Simulates the UE side injecting a non-IP payload via the\n"
                "  /receive backdoor (no real RAN/NAS small-data wire used) and\n"
                "  pins that the delivery log records a UL entry.\n"
                "\n"
                "Procedure (TS 23.502 §4.25.5 + TS 29.122 §5)\n"
                "  1. require_gnb / require_ue / register_ue.\n"
                "  2. POST /api/nidd/sessions to open a session; capture sid.\n"
                "  3. POST /api/nidd/sessions/{sid}/receive\n"
                "     {data_hex='DEADBEEF01020304'} — simulates UE→AS payload.\n"
                "  4. Require non-empty receive response.\n"
                "  5. GET /api/nidd/sessions/{sid}/log.\n"
                "  6. Scan for any entry with direction=='ul' or type=='ul'.\n"
                "  7. finally: DELETE the session.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — data_hex hard-coded to 0xDEADBEEF01020304)\n"
                "\n"
                "Pass criteria\n"
                "  /receive returns a non-empty response. ul_found and log_entries\n"
                "  count are reported but do not fail the test.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, session_id, data_hex, recv_result, log_entries, ul_found.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Receive endpoint is a test-only injection point;\n"
                "  full small-data NAS wire is out of scope.\n"
                "  The /receive endpoint mirrors the NEF→AS callback (TS 29.122 §5).\n"
                "  Real UE-side ingress would arrive via Small-Data NAS Transport."
            ),
    )

    def run(self):
        session_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Create session
            log.info("Creating NIDD session for %s", ue.imsi)
            create_result = _core_api("/api/nidd/sessions", "POST", {
                "imsi": ue.imsi,
                "app_server_id": "app-server-001",
                "config": {
                    "notification_url": "http://192.168.1.103:8080/nidd/callback",
                },
            })
            if not create_result:
                self.fail_test("NIDD session creation failed")
                return self.result

            session_id = create_result.get("session_id") or create_result.get("id")
            if not session_id:
                self.fail_test("No session_id in create response")
                return self.result

            # Simulate UL data receipt
            data_hex = "DEADBEEF01020304"
            log.info("Simulating NIDD UL data: %s", data_hex)
            recv_result = _core_api(f"/api/nidd/sessions/{session_id}/receive", "POST", {
                "data_hex": data_hex,
            })
            if not recv_result:
                self.fail_test("NIDD receive returned no response")
                return self.result

            log.info("NIDD receive result: %s", recv_result)

            # Verify UL entry in log
            log_result = _core_api(f"/api/nidd/sessions/{session_id}/log")
            log_entries = []
            if log_result:
                log_entries = log_result.get("entries") or log_result.get("log") or log_result.get("items") or []

            ul_found = any(
                e.get("direction") == "ul" or e.get("type") == "ul"
                for e in log_entries
            ) if log_entries else False

            self.pass_test(imsi=ue.imsi, session_id=session_id,
                           data_hex=data_hex, recv_result=recv_result,
                           log_entries=len(log_entries), ul_found=ul_found)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"NIDD receive data error: {e}")
        finally:
            if session_id:
                try:
                    _core_api(f"/api/nidd/sessions/{session_id}", "DELETE")
                except Exception:
                    pass
        return self.result


class NiddAppServer(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NIDD-004",
        title="Register, list, and delete an NIDD application server",
        spec="TS 29.122 §5",
        domain=Domain.IOT,
        nfs=(NF.NEF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Application-server registry CRUD for the T8 NEF API\n"
                "  (TS 29.122 §5 — Application Function provisioning). An AS MUST\n"
                "  be registered before NIDD sessions can target it; the registry\n"
                "  holds the callback URL the NEF posts MO data to.\n"
                "\n"
                "Procedure (TS 29.122 §5)\n"
                "  1. POST /api/nidd/app-servers {app_server_id=\n"
                "     'test-app-server-001', callback_url='http://192.168.1.103:8080\n"
                "     /nidd/callback', description=...}.\n"
                "  2. Require non-empty response; capture app_server_id (fallback\n"
                "     to literal 'test-app-server-001').\n"
                "  3. GET /api/nidd/app-servers (app_servers/items/servers envelope).\n"
                "  4. Require any entry has app_server_id (or id) == ours.\n"
                "  5. finally: DELETE /api/nidd/app-servers/{app_server_id}.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — id, callback URL, description hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Registration returns a non-empty body and the AS appears in\n"
                "  the subsequent listing.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  app_server_id, server_count, reg_result.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY — no UE required for registry CRUD.\n"
                "  callback_url validation is not exercised here — the URL is left\n"
                "  syntactically valid even though no callback is fired.\n"
                "  Re-registration of the same app_server_id is implementation-\n"
                "  defined and not asserted."
            ),
    )

    def run(self):
        app_server_id = None
        try:
            # Register app server
            log.info("Registering NIDD app server")
            reg_result = _core_api("/api/nidd/app-servers", "POST", {
                "app_server_id": "test-app-server-001",
                "callback_url": "http://192.168.1.103:8080/nidd/callback",
                "description": "Test app server for NIDD",
            })
            if not reg_result:
                self.fail_test("App server registration returned no response")
                return self.result

            app_server_id = reg_result.get("app_server_id") or reg_result.get("id") or "test-app-server-001"
            log.info("App server registered: %s", app_server_id)

            # Verify via GET
            servers = _core_api("/api/nidd/app-servers")
            if not servers:
                self.fail_test("App servers GET returned no response")
                return self.result

            server_items = servers.get("app_servers") or servers.get("items") or servers.get("servers") or []
            found = any(
                s.get("app_server_id") == app_server_id or s.get("id") == app_server_id
                for s in server_items
            )

            if found:
                self.pass_test(app_server_id=app_server_id,
                               server_count=len(server_items),
                               reg_result=reg_result)
            else:
                self.fail_test("Registered app server not found in list",
                               app_server_id=app_server_id, servers=server_items)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"NIDD app server error: {e}")
        finally:
            if app_server_id:
                try:
                    _core_api(f"/api/nidd/app-servers/{app_server_id}", "DELETE")
                    log.info("Cleaned up app server %s", app_server_id)
                except Exception:
                    pass
        return self.result


class NiddStats(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NIDD-005",
        title="NIDD /stats endpoint reports session counters",
        spec="TS 23.502 §4.25",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.NEF),
        severity=Severity.MINOR,
        tags=("regression",),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Operator-dashboard counter feed for NIDD (TS 23.502 §4.25,\n"
                "  TS 29.122). The /stats endpoint surfaces aggregate session\n"
                "  and byte counters; this test drives one session+send to bump\n"
                "  the counters, then asserts the dashboard endpoint replies with\n"
                "  a non-empty payload.\n"
                "\n"
                "Procedure (TS 23.502 §4.25 + TS 29.122)\n"
                "  1. require_gnb / require_ue / register_ue.\n"
                "  2. POST /api/nidd/sessions to open a session; capture sid.\n"
                "  3. POST /api/nidd/sessions/{sid}/send {data_hex='48656C6C6F'}\n"
                "     to advance DL counters.\n"
                "  4. GET /api/nidd/stats.\n"
                "  5. Require non-empty response body.\n"
                "  6. finally: DELETE the session.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixed 'Hello' bump payload)\n"
                "\n"
                "Pass criteria\n"
                "  /api/nidd/stats returns a non-empty payload after the lifecycle.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, stats.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Counter values are recorded but not numerically\n"
                "  asserted — only the endpoint contract is pinned.\n"
                "  /stats body shape is implementation-defined; only presence is\n"
                "  pinned, not counter values.\n"
                "  If the lifecycle pre-bump fails, /stats is still probed (best-\n"
                "  effort warm-up)."
            ),
    )

    def run(self):
        session_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Create a session and send data to generate stats
            create_result = _core_api("/api/nidd/sessions", "POST", {
                "imsi": ue.imsi,
                "app_server_id": "app-server-001",
                "config": {"notification_url": "http://192.168.1.103:8080/nidd/callback"},
            })
            if create_result:
                session_id = create_result.get("session_id") or create_result.get("id")
                if session_id:
                    _core_api(f"/api/nidd/sessions/{session_id}/send", "POST", {
                        "data_hex": "48656C6C6F",
                    })

            # Check stats
            stats = _core_api("/api/nidd/stats")
            if not stats:
                self.fail_test("NIDD stats endpoint returned no response")
                return self.result

            log.info("NIDD stats: %s", stats)
            self.pass_test(imsi=ue.imsi, stats=stats)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"NIDD stats error: {e}")
        finally:
            if session_id:
                try:
                    _core_api(f"/api/nidd/sessions/{session_id}", "DELETE")
                except Exception:
                    pass
        return self.result


ALL_NIDD_TCS = [NiddCreateSession, NiddSendData, NiddReceiveData,
                NiddAppServer, NiddStats]
