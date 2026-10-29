# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Supplementary Services — CFU, BAOC, CLIP/CLIR, bulk operations.

Service-by-service spec mapping (every TC's pass/fail asserts
correspond to one of these clauses):

  TS 24.604 §4.5.1 / §4.5.2 — Communication Diversion (CDIV):
                              CFU / CFB / CFNRy / CFNRc activation,
                              deactivation, registration, interrogation.
  TS 24.611 §4.5.1          — Communication Barring (CB) activation /
                              deactivation (BAOC, BAOIC, BAIC, ...).
                              ACR (anonymous communication rejection)
                              also lives in TS 24.611.
  TS 24.615 §4.5.2 / §4.5.4 — Communication Waiting (CW) activation,
                              deactivation, interrogation. NOTE: CW
                              moved out of TS 24.611 in Rel-10+.
  TS 24.607 §4.5            — Originating Identification (OIP/OIR).
  TS 24.608 §4.5            — Terminating Identification (TIP/TIR).
  TS 22.030 §6.5.2          — UE-side MMI procedure forms (only
                              relevant when the test path drives the
                              UE keypad layer, not the REST API).

The `*30#` / `*21#` etc. service codes the inline comments mention
are TS 22.030 Annex B Table B.1 entries — each one is anchored in
the local PDF.
"""

import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.core.api import core_api as _core_api

log = logging.getLogger("tester.tc_supplementary")


class SsCallForwarding(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SS-001",
        title="Activate / interrogate / deactivate Call Forwarding Unconditional",
        spec="TS 24.604 §4.5.1",
        domain=Domain.VAS,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Communication Diversion / CFU full state machine (TS 24.604\n"
            "  §4.5.1 — Activation, §4.5.2 — Deactivation, §4.5.7 —\n"
            "  Interrogation). The MMI shorthand for unconditional forwarding\n"
            "  is *21*<number># (TS 22.030 Annex B). The test pins all four\n"
            "  state-machine edges: activate → interrogate-active → deactivate\n"
            "  → interrogate-inactive.\n"
            "\n"
            "Procedure (TS 24.604 §4.5.1 + §4.5.2 + §4.5.7)\n"
            "  1. require_gnb / require_ue / register_ue.\n"
            "  2. POST /api/supplementary/activate {imsi, service_type='CFU',\n"
            "     forwarding_number='+1234567890'}.\n"
            "  3. GET /api/supplementary/interrogate?imsi=…&service_type=CFU.\n"
            "  4. Require active flag truthy.\n"
            "  5. POST /api/supplementary/deactivate {imsi, service_type='CFU'}.\n"
            "  6. GET /interrogate again — require active flag falsy.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — forwarding number hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Interrogation shows active after activate, then inactive after\n"
            "  deactivate.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, service, forwarding_number, activated, deactivated.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Only the unconditional variant (CFU) is exercised;\n"
            "  CFB/CFNRy/CFNRc share the SBI but are not driven here."
        ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Activate CFU
            log.info("Activating CFU for %s", ue.imsi)
            activate_result = _core_api("/api/supplementary/activate", "POST", {
                "imsi": ue.imsi,
                "service_type": "CFU",
                "forwarding_number": "+1234567890",
            })
            if not activate_result:
                self.fail_test("CFU activation returned no response")
                return self.result
            log.info("CFU activated: %s", activate_result)

            # Interrogate to verify active
            interrogate_result = _core_api(
                f"/api/supplementary/interrogate?imsi={ue.imsi}&service_type=CFU")
            if not interrogate_result:
                self.fail_test("CFU interrogation returned no response")
                return self.result

            active = interrogate_result.get("active") or interrogate_result.get("status") == "active"
            if not active:
                self.fail_test("CFU not active after activation", interrogate=interrogate_result)
                return self.result
            log.info("CFU interrogation confirms active")

            # Deactivate
            deactivate_result = _core_api("/api/supplementary/deactivate", "POST", {
                "imsi": ue.imsi,
                "service_type": "CFU",
            })
            log.info("CFU deactivated: %s", deactivate_result)

            # Verify inactive
            interrogate_after = _core_api(
                f"/api/supplementary/interrogate?imsi={ue.imsi}&service_type=CFU")
            inactive = not (interrogate_after or {}).get("active", False)

            if inactive:
                self.pass_test(imsi=ue.imsi, service="CFU",
                               forwarding_number="+1234567890",
                               activated=True, deactivated=True)
            else:
                self.fail_test("CFU still active after deactivation",
                               interrogate=interrogate_after)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Call forwarding error: {e}")
        return self.result


class SsCallBarring(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SS-002",
        title="Activate / interrogate / deactivate Barring of All Outgoing Calls",
        spec="TS 24.611 §4.5.1",
        domain=Domain.VAS,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Communication Barring — Barring of All Outgoing Calls (TS 24.611\n"
                "  §4.5.1 Activation, §4.5.2 Deactivation, §4.5.7 Interrogation).\n"
                "  The MMI shorthand for BAOC is *33*<PW># (TS 22.030 Annex B\n"
                "  Table B.1). Pins the four state-machine edges in the same\n"
                "  fashion as TC-SS-001 but for the barring family.\n"
                "\n"
                "Procedure (TS 24.611 §4.5.1 + §4.5.2 + §4.5.7)\n"
                "  1. require_gnb / require_ue / register_ue.\n"
                "  2. POST /api/supplementary/activate {imsi, service_type='BAOC'}.\n"
                "  3. GET /api/supplementary/interrogate?imsi=…&service_type=BAOC.\n"
                "  4. Require active flag truthy.\n"
                "  5. POST /api/supplementary/deactivate {imsi, service_type='BAOC'}.\n"
                "  6. GET /interrogate again — require active flag falsy.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — no barring password is passed at the REST surface)\n"
                "\n"
                "Pass criteria\n"
                "  Interrogation shows active after activate; inactive after\n"
                "  deactivate.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, service, activated, deactivated.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. BAOIC / BAIC / BIC-Roam variants share the\n"
                "  SBI but are not driven here.\n"
                "  Test path uses the supplementary-API only; the matching IMS S-CSCF\n"
                "  ISC interface is exercised by the Robot mirror.\n"
                "  Re-activation after a successful activate is a no-op (not asserted)."
            ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Activate BAOC
            log.info("Activating BAOC for %s", ue.imsi)
            activate_result = _core_api("/api/supplementary/activate", "POST", {
                "imsi": ue.imsi,
                "service_type": "BAOC",
            })
            if not activate_result:
                self.fail_test("BAOC activation returned no response")
                return self.result

            # Interrogate
            interrogate_result = _core_api(
                f"/api/supplementary/interrogate?imsi={ue.imsi}&service_type=BAOC")
            if not interrogate_result:
                self.fail_test("BAOC interrogation returned no response")
                return self.result

            active = interrogate_result.get("active") or interrogate_result.get("status") == "active"
            if not active:
                self.fail_test("BAOC not active after activation", interrogate=interrogate_result)
                return self.result
            log.info("BAOC confirmed active")

            # Deactivate
            deactivate_result = _core_api("/api/supplementary/deactivate", "POST", {
                "imsi": ue.imsi,
                "service_type": "BAOC",
            })
            log.info("BAOC deactivated: %s", deactivate_result)

            # Verify inactive
            interrogate_after = _core_api(
                f"/api/supplementary/interrogate?imsi={ue.imsi}&service_type=BAOC")
            inactive = not (interrogate_after or {}).get("active", False)

            if inactive:
                self.pass_test(imsi=ue.imsi, service="BAOC",
                               activated=True, deactivated=True)
            else:
                self.fail_test("BAOC still active after deactivation",
                               interrogate=interrogate_after)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Call barring error: {e}")
        return self.result


class SsClipClir(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SS-003",
        title="Activate CLIP and CLIR together, verify both active",
        spec="TS 24.607 §4.5",
        domain=Domain.VAS,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Originating Identification supplementary services (TS 24.607\n"
            "  §4.5 — OIP/OIR). CLIP forwards the calling-line ID; CLIR\n"
            "  suppresses it on a per-call basis. They are logically opposing\n"
            "  flags but can be active simultaneously — CLIR overrides CLIP at\n"
            "  call setup. This test pins that both flags can be ON in the\n"
            "  services registry at once.\n"
            "\n"
            "Procedure (TS 24.607 §4.5 + TS 22.030 Annex B)\n"
            "  1. require_gnb / require_ue / register_ue.\n"
            "  2. POST /api/supplementary/activate {imsi, service_type='CLIP'}.\n"
            "  3. POST /api/supplementary/activate {imsi, service_type='CLIR'}.\n"
            "  4. GET /api/supplementary/services?imsi={ue.imsi}.\n"
            "  5. Walk services/items envelope; collect service_type for\n"
            "     entries with active or status=='active'.\n"
            "  6. Require both 'CLIP' and 'CLIR' appear in active_types.\n"
            "  7. Cleanup: deactivate CLIP and CLIR.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed activation pair)\n"
            "\n"
            "Pass criteria\n"
            "  CLIP and CLIR are both observable as active in the listing.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, clip_active, clir_active, all_active.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. CLIR-Override at call setup is out of scope —\n"
            "  the test only pins the registry view."
        ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Activate CLIP
            log.info("Activating CLIP for %s", ue.imsi)
            clip_result = _core_api("/api/supplementary/activate", "POST", {
                "imsi": ue.imsi,
                "service_type": "CLIP",
            })
            if not clip_result:
                self.fail_test("CLIP activation returned no response")
                return self.result

            # Activate CLIR
            log.info("Activating CLIR for %s", ue.imsi)
            clir_result = _core_api("/api/supplementary/activate", "POST", {
                "imsi": ue.imsi,
                "service_type": "CLIR",
            })
            if not clir_result:
                self.fail_test("CLIR activation returned no response")
                return self.result

            # List all services for IMSI
            services = _core_api(f"/api/supplementary/services?imsi={ue.imsi}")
            if not services:
                self.fail_test("Services list returned no response")
                return self.result

            items = services.get("services") or services.get("items") or []
            active_types = [s.get("service_type") or s.get("type") for s in items
                            if s.get("active") or s.get("status") == "active"]

            clip_active = "CLIP" in active_types
            clir_active = "CLIR" in active_types

            log.info("Active services: %s", active_types)

            if clip_active and clir_active:
                self.pass_test(imsi=ue.imsi, clip_active=True, clir_active=True,
                               all_active=active_types)
            else:
                self.fail_test("CLIP and/or CLIR not both active",
                               clip_active=clip_active, clir_active=clir_active,
                               services=items)

            # Clean up
            for svc in ["CLIP", "CLIR"]:
                _core_api("/api/supplementary/deactivate", "POST", {
                    "imsi": ue.imsi, "service_type": svc,
                })
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"CLIP/CLIR error: {e}")
        return self.result


class SsBulkSet(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SS-004",
        title="Bulk-set multiple supplementary services in one operator call",
        spec="TS 24.604 §4.5.1",
        domain=Domain.VAS,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Bulk operator-API provisioning of supplementary services\n"
            "  (TS 24.604 §4.5.1 for CDIV plus TS 24.607 §4.5 for CLIP/CLIR).\n"
            "  Operators MUST be able to set multiple services in one call\n"
            "  during bulk subscriber onboarding without N round-trips.\n"
            "\n"
            "Procedure (TS 24.604 §4.5.1 + TS 24.607 §4.5)\n"
            "  1. require_gnb / require_ue / register_ue.\n"
            "  2. POST /api/supplementary/bulk {imsi, services=[\n"
            "     {service_type='CFU', forwarding_number='+9876543210'},\n"
            "     {service_type='CLIP'}, {service_type='CLIR'}]}.\n"
            "  3. Require non-empty bulk_result.\n"
            "  4. GET /api/supplementary/services?imsi={ue.imsi}.\n"
            "  5. Collect active_types set from active or status=='active'\n"
            "     entries.\n"
            "  6. Require {'CFU','CLIP','CLIR'} ⊆ active_types.\n"
            "  7. Cleanup: deactivate CFU, CLIP, CLIR.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — service set hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  All three requested services appear as active after the bulk\n"
            "  POST.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, bulk_result, active_services.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Partial-success / error-array reporting is not\n"
            "  exercised — the test asserts all-or-nothing only."
        ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Bulk set
            log.info("Bulk-setting services for %s", ue.imsi)
            bulk_result = _core_api("/api/supplementary/bulk", "POST", {
                "imsi": ue.imsi,
                "services": [
                    {"service_type": "CFU", "forwarding_number": "+9876543210"},
                    {"service_type": "CLIP"},
                    {"service_type": "CLIR"},
                ],
            })
            if not bulk_result:
                self.fail_test("Bulk set returned no response")
                return self.result

            log.info("Bulk set result: %s", bulk_result)

            # Verify all services active
            services = _core_api(f"/api/supplementary/services?imsi={ue.imsi}")
            if not services:
                self.fail_test("Services list returned no response after bulk set")
                return self.result

            items = services.get("services") or services.get("items") or []
            active_types = set(
                s.get("service_type") or s.get("type") for s in items
                if s.get("active") or s.get("status") == "active"
            )

            expected = {"CFU", "CLIP", "CLIR"}
            missing = expected - active_types

            if not missing:
                self.pass_test(imsi=ue.imsi, bulk_result=bulk_result,
                               active_services=sorted(active_types))
            else:
                self.fail_test(f"Missing services after bulk set: {missing}",
                               active=sorted(active_types), expected=sorted(expected))

            # Clean up
            for svc in ["CFU", "CLIP", "CLIR"]:
                _core_api("/api/supplementary/deactivate", "POST", {
                    "imsi": ue.imsi, "service_type": svc,
                })
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Bulk set error: {e}")
        return self.result


ALL_SUPPLEMENTARY_TCS = [SsCallForwarding, SsCallBarring, SsClipClir, SsBulkSet]
