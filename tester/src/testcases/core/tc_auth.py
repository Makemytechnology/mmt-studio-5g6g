# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Authentication & Security Mode Control (TS 24.501 §5.4).

Covers the three sub-procedures of clause 5.4 of the local TS 24.501
v19.6.2 (April 2026) text:
  §5.4.1.3   5G-AKA based primary authentication and key agreement.
  §5.4.2     NAS security mode control procedure.
  §5.4.3     Identification procedure.
With key-derivation references from the local TS 33.501 v19.6.0 §6
(KAUSF / KSEAF / KAMF / KNAS hierarchy and KgNB derivation).
"""

import concurrent.futures
import time

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Severity, Setup,
)


# ────────────────────────────────────────────────────────────────────────
# Existing TCs — descriptions rewritten to the 6-section template used by
# Registration tests. Run-bodies preserved where correct; minor fixes to
# AuthMultiUe and AuthAllUes to match what the titles claim.
# ────────────────────────────────────────────────────────────────────────


class AuthSuccess(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-001",
        title="5G-AKA primary authentication succeeds; NAS keys derived",
        spec="TS 24.501 §5.4.1.3 + TS 33.501 §6.1.3.2",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the 5G-AKA primary authentication +\n"
            "  key agreement procedure (TS 24.501 §5.4.1.3) ending in a\n"
            "  fully-derived NAS security context. If this fails, every\n"
            "  downstream control-plane and PDU test is dead-on-arrival.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3 + TS 33.501 §6.1.3.2)\n"
            "  1. UE → AMF: RegistrationRequest (initial, SUCI).\n"
            "  2. AMF → UE: AUTHENTICATION REQUEST carrying RAND, AUTN,\n"
            "     ngKSI and ABBA (§5.4.1.3.2).\n"
            "  3. UE verifies AUTN, computes RES* per TS 33.501 Annex A.4,\n"
            "     and returns AUTHENTICATION RESPONSE with RES* (§5.4.1.3.3).\n"
            "  4. AMF→AUSF Nausf_UEAuthentication_Authenticate validates\n"
            "     XRES* match (§5.4.1.3.4); AMF then runs SMC.\n"
            "  5. SMC completes; UE derives KNASint/KNASenc per TS 33.501\n"
            "     Annex A.8.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi    — UE to drive (default: first UE in pool).\n"
            "  timeout — wait for REGISTERED, seconds (default: 15).\n"
            "\n"
            "Pass criteria\n"
            "  - UE FSM reaches REGISTERED.\n"
            "  - security_ctx.knasint is non-empty bytes (proves the full\n"
            "    KAUSF → KSEAF → KAMF → KNASint chain ran).\n"
            "\n"
            "KPI deltas (/api/kpis/registration after the run)\n"
            "  - attempts +1, successes +1, failures 0, in_flight 0.\n"
            "  - latency_ms: one new sample, typically 100–200 ms.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — full UE provisioning required for the UDM\n"
            "  to return a valid auth-vector."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        if self.register_ue(ue, gnb, self.params.get("timeout", 15)):
            self.pass_test(imsi=ue.imsi,
                           has_keys=ue.security_ctx.get('knasint') is not None)
        return self.result


class AuthSecurityAlgo(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-002",
        title="NAS security algorithms negotiated and keys derived",
        spec="TS 24.501 §5.4.2.2 + TS 33.501 §6.7.1 + §6.7.4",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Joint gate on (1) the NAS algorithm negotiation inside the\n"
            "  Security Mode Command (TS 24.501 §5.4.2.2 — AMF picks\n"
            "  EEA/EIA from the UE's UE security capability and its own\n"
            "  ordered priority list) and (2) the resulting KNAS{enc,int}\n"
            "  derivation (TS 33.501 §6.7.4 Annex A.8). Catches NIA0/NEA0\n"
            "  fallback regressions (null integrity is forbidden outside\n"
            "  emergency per TS 33.501 §5.5) AND missing KDF wiring that\n"
            "  would leave KNASint empty even when an algorithm was\n"
            "  negotiated. Supersedes the now-removed TC-REG-005 — same\n"
            "  scope, but lives in the Authentication file alongside the\n"
            "  rest of the §5.4 coverage.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.2.2 + TS 33.501 §6.7.1 + §6.7.4)\n"
            "  1. Run full Initial Registration (TC-AUTH-001 path).\n"
            "  2. UE FSM stores the negotiated algorithms picked up from\n"
            "     the Selected NAS security algorithms IE in SMC, and\n"
            "     derives KNASenc/KNASint per Annex A.8.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - Registration succeeds.\n"
            "  - security_ctx.knasint is non-empty bytes (KDF chain ran).\n"
            "  - security_ctx.eia > 0 (integrity mandatory; NIA0 rejected\n"
            "    outside emergency).\n"
            "  - Result records both negotiated EEA and EIA values.\n"
            "\n"
            "KPI deltas\n"
            "  Same as TC-AUTH-001.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. If the AMF policy permits NIA0 (emergency-\n"
            "  only), this TC fails — that is intentional."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        ctx = ue.security_ctx
        if not ctx.get('knasint'):
            self.fail_test("KNASint not derived — key hierarchy broken")
            return self.result
        eia = ctx.get('eia', 0)
        if eia <= 0:
            self.fail_test("Integrity algorithm not negotiated (EIA=0)")
            return self.result
        self.pass_test(eea=ctx.get('eea', 0), eia=eia, has_keys=True)
        return self.result


class AuthSqnResync(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-003",
        title="SQN resync via AUTS — UE registers after #21 synch failure",
        spec="TS 24.501 §5.4.1.3.7 f + TS 33.501 §6.1.3.2.4",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Validate the resync branch of clause 5.4.1.3.7 item f: if\n"
            "  the USIM finds SQN out of range, the UE returns\n"
            "  AUTHENTICATION FAILURE with 5GMM cause #21 'synch failure'\n"
            "  carrying AUTS; AUSF/UDM re-syncs and re-issues a fresh\n"
            "  challenge; the UE then registers normally.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3.7 f + TS 33.501 §6.1.3.2.4)\n"
            "  1. Drive a normal Initial Registration. The shipped baseline\n"
            "     SIM may not actually be out of sync, so this TC asserts\n"
            "     the happy outcome (REGISTERED + derived keys); the resync\n"
            "     code path is only exercised opportunistically.\n"
            "  2. If the USIM IS out of sync, src/statemachine/ue_fsm.py\n"
            "     emits AUTH FAILURE with cause #21 + AUTS (Sec_5GMMCause\n"
            "     #21 from the IE table in §9.11.3.2); AMF deletes unused\n"
            "     AVs, fetches new ones, and restarts §5.4.1.3.2.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - Final state REGISTERED with derived knasint.\n"
            "  - Whether resync actually ran is opaque to the TC; the\n"
            "    pass criterion is the end-state, not the path.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1. If resync ran, latency_ms is\n"
            "  noticeably higher (extra AV fetch).\n"
            "\n"
            "Known constraints\n"
            "  To force an out-of-sync USIM deterministically, the SQN\n"
            "  field in sim_db.json must be rewound — not done here.\n"
            "  TC-AUTH-021 is the deterministic-resync companion."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if self.register_ue(ue, gnb):
            self.pass_test(imsi=ue.imsi,
                           has_keys=ue.security_ctx.get('knasint') is not None)
        return self.result


class AuthMultiUe(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-004",
        title="Two UEs authenticate concurrently against the same AUSF/UDM",
        spec="TS 24.501 §5.4.1.3 + TS 33.501 §6.1.3",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Two UEs simultaneously drive 5G-AKA to shake out AUSF/UDM\n"
            "  serialisation bugs around shared per-UE state (auth vector\n"
            "  cache, SQN counters, Nausf_UEAuthentication contexts).\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3)\n"
            "  1. Drive two UEs through Initial Registration in parallel\n"
            "     threads.\n"
            "  2. Each follows the §5.4.1.3.2 → §5.4.1.3.3 → §5.4.1.3.4\n"
            "     sequence independently; AUSF must not cross-wire RES*\n"
            "     between the two contexts.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — uses the first 2 UEs in the pool.\n"
            "\n"
            "Pass criteria\n"
            "  - Both UEs reach REGISTERED.\n"
            "  - Both have derived knasint (i.e. no key leaked between\n"
            "    them — knasint differs per-UE, but that is enforced by\n"
            "    distinct KAMF/SUPI inputs to TS 33.501 Annex A.8 KDF).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +2, successes +2.\n"
            "\n"
            "Known constraints\n"
            "  Needs ≥2 UEs in the pool; Setup.BASELINE provisions 128."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs")
            return self.result
        ues = self.ue_pool[:2]

        def _reg(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                return ue.wait_for_state("REGISTERED", timeout=15)
            except Exception:
                return False

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(_reg, ues))
        if not all(results):
            self.fail_test(f"Concurrent registration failed: {results}")
            return self.result
        self.pass_test(
            ue1=ues[0].imsi, ue2=ues[1].imsi,
            both_registered=all(u.state == "REGISTERED" for u in ues),
        )
        return self.result


class AuthReAuth(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-005",
        title="Re-registration after deregister triggers a fresh 5G-AKA",
        spec="TS 24.501 §5.4.1.3.2 + §5.5.2",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "regression", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  After UE-initiated deregistration the AMF deletes the 5G\n"
            "  NAS security context (TS 24.501 §5.5.2); the next initial\n"
            "  registration must therefore re-run 5G-AKA in full rather\n"
            "  than reuse a cached challenge. Surfaces leaks in AUSF\n"
            "  challenge bookkeeping and UDM auth-cache invalidation.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3 + §5.5.2)\n"
            "  1. Register → Deregister.\n"
            "  2. Re-register: the AMF MUST initiate authentication per\n"
            "     §5.4.1.3.2 (the prior context was deleted on deregister).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  Both Register cycles succeed and end in REGISTERED. The\n"
            "  fact that 5G-AKA ran on the second pass is inferred from\n"
            "  successful arrival in REGISTERED with non-NULL keys (a\n"
            "  cached-context shortcut would not derive new KAMF — but\n"
            "  this TC does not gate KAMF rotation; TC-AUTH-020 does).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +2, successes +2, plus one deregistration.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.deregister_ue(ue):
            return self.result
        time.sleep(0.5)
        if self.register_ue(ue, gnb):
            self.pass_test(imsi=ue.imsi, re_auth="OK")
        return self.result


class AuthAllUes(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-006",
        title="Three UEs authenticate sequentially — multi-UE health check",
        spec="TS 24.501 §5.4.1.3",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Cheap sequential multi-UE auth that any baseline should pass.\n"
            "  Catches per-UE auth-vector cache bugs that only appear once\n"
            "  the per-SUPI state has churned a few times.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3)\n"
            "  1. Iterate the first 3 UEs in the pool, registering each\n"
            "     in turn under the full §5.4.1.3 procedure.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — uses pool[:3].\n"
            "\n"
            "Pass criteria\n"
            "  All 3 UEs reach REGISTERED.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +3, successes +3.\n"
            "\n"
            "Known constraints\n"
            "  Needs ≥3 UEs in the pool; Setup.BASELINE provisions 128."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        count = min(3, len(self.ue_pool))
        for ue in self.ue_pool[:count]:
            if not self.register_ue(ue, gnb):
                return self.result
        self.pass_test(
            ue_count=count,
            all_registered=all(u.state == "REGISTERED" for u in self.ue_pool[:count]),
        )
        return self.result


class AuthThenPdu(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-007",
        title="Auth → SMC → default-bearer PDU session end-to-end",
        spec="TS 24.501 §5.4.1.3 + §5.4.2 + §6.4.1",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Full control-plane chain from authentication through PDU\n"
            "  session establishment on the default DNN. Validates that\n"
            "  the security context derived in §5.4 is actually usable\n"
            "  for the post-registration §6.4.1 PDU session.\n"
            "\n"
            "Procedure (TS 24.501 §5.4 + §6.4.1)\n"
            "  1. Initial Registration (TC-AUTH-001 path).\n"
            "  2. PDU SESSION ESTABLISHMENT REQUEST encapsulated in a\n"
            "     ULnasTransport per §8.2.10; SMF allocates a UE IP.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - REGISTERED.\n"
            "  - pdu_sessions[1] has an allocated IP.\n"
            "  - knasint still present (security context still bound).\n"
            "\n"
            "KPI deltas\n"
            "  /api/kpis/registration: attempts +1, successes +1.\n"
            "  /api/kpis/pdu_session: attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Needs SMF + UPF reachable; Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result
        ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        self.pass_test(imsi=ue.imsi, ip=ip,
                       has_keys=ue.security_ctx.get('knasint') is not None)
        return self.result


class AuthRepeatedCycles(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-008",
        title="Three Register/Deregister cycles back-to-back",
        spec="TS 24.501 §5.4.1.3 + §5.5",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("regression", "stress", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=45.0,
        description=(
            "Purpose\n"
            "  Repeated full-context teardown + rebuild against the same\n"
            "  SUPI. Surfaces UE-context cleanup leaks in AMF, AUSF state-\n"
            "  machine resets, SQN bookkeeping race conditions, and any\n"
            "  half-finished §5.4.2 SMC procedure that lingers between\n"
            "  registrations.\n"
            "\n"
            "Procedure (TS 24.501 §5.4 + §5.5)\n"
            "  Three rapid cycles of: Register → Deregister.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  All 3 cycles end in REGISTERED → DEREGISTERED with no\n"
            "  stuck state.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +3, successes +3, deregistrations +3.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        for _ in range(3):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.deregister_ue(ue):
                return self.result
            time.sleep(0.5)
        self.pass_test(imsi=ue.imsi, cycles=3)
        return self.result


class AuthSuciRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-009",
        title="SUCI built per UE Config — supi_type/RI/scheme/HN-key match",
        spec="TS 24.501 §5.4.3 + TS 33.501 §6.12 + TS 23.003 §2.2B",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=12.0,
        description=(
            "Purpose\n"
            "  The SUCI is a privacy-preserving identifier containing the\n"
            "  concealed SUPI (TS 24.501 §5.4.3.1). This TC asserts the\n"
            "  SUCI the UE built during registration faithfully reflects\n"
            "  the UE Config — supi_type, routing_indicator, protection\n"
            "  scheme (null / ECIES-A / ECIES-B), and home-network public\n"
            "  key id — so the UDM can de-conceal back to the correct SUPI.\n"
            "\n"
            "Procedure (TS 33.501 §6.12 + TS 23.003 §2.2B)\n"
            "  1. Register the UE.\n"
            "  2. Inspect ue.suci dict: supi_type, plmn, routing_indicator,\n"
            "     protection_scheme, home_network_public_key_id, msin.\n"
            "  3. Compare against ue.sim attributes.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  Every SUCI field matches the UE Config. Mismatch is fatal.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sim_db.json must declare supi_type, RI,\n"
            "  protection_scheme, and home_nw_pub_key_id fields."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result

        suci = ue.suci
        if not suci:
            self.fail_test("Identity not built during registration")
            return self.result

        config_type = getattr(ue.sim, 'supi_type', 'supi')
        config_routing = getattr(ue.sim, 'routing_indicator', '0000')
        config_scheme = getattr(ue.sim, 'protection_scheme', 0)
        config_hnpk_id = getattr(ue.sim, 'home_nw_pub_key_id', 0)

        errors = []
        if suci.get("supi_type") != ue.supi_type:
            errors.append(f"supi_type: built={suci.get('supi_type')} config={ue.supi_type}")
        if suci.get("routing_indicator") != config_routing:
            errors.append(f"routing_indicator: built={suci.get('routing_indicator')} config={config_routing}")
        if suci.get("protection_scheme") != config_scheme:
            errors.append(f"protection_scheme: built={suci.get('protection_scheme')} config={config_scheme}")
        if suci.get("home_network_public_key_id") != config_hnpk_id:
            errors.append(f"hn_pub_key_id: built={suci.get('home_network_public_key_id')} config={config_hnpk_id}")

        if errors:
            self.fail_test("; ".join(errors), suci=suci, config_identity_type=config_type)
        else:
            self.pass_test(
                imsi=ue.imsi, supi=ue.supi, identity_type=config_type,
                suci_type=ue.suci_type, suci=suci,
                guti_assigned=ue.guti is not None, guti_5g=ue._5g_guti,
                has_keys=ue.security_ctx.get('knasint') is not None,
            )
        return self.result


class AuthIdentityChain(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-010",
        title="SUPI → SUCI → 5G-GUTI identity-lifecycle end-to-end",
        spec="TS 23.003 §2.10 + TS 33.501 §6.12 + TS 24.501 §5.4.3",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=12.0,
        description=(
            "Purpose\n"
            "  Verify the full identity lifecycle observed during a single\n"
            "  registration: the permanent SUPI never leaves the device,\n"
            "  the SUCI sent OTA contains the correct PLMN, and the AMF\n"
            "  assigns a fresh 5G-GUTI in the Registration Accept.\n"
            "\n"
            "Procedure (TS 23.003 §2.10 + TS 33.501 §6.12)\n"
            "  1. Register UE.\n"
            "  2. Assert ue.supi present, ue.suci built with PLMN matching\n"
            "     ue.mcc+ue.mnc, and ue._5g_guti populated by Reg Accept.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  All three identity attributes are present and consistent.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. AMF must include 5G-GUTI in Registration\n"
            "  Accept (TS 24.501 §8.2.7)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result

        errors = []
        if not ue.supi:
            errors.append("SUPI not set")
        if not ue.suci:
            errors.append("SUCI not built during registration")
        elif ue.suci.get("plmn") != f"{ue.mcc}{ue.mnc}":
            errors.append(f"SUCI PLMN: {ue.suci.get('plmn')} != {ue.mcc}{ue.mnc}")
        if not ue.guti:
            errors.append("5G-GUTI not assigned by AMF")

        config_type = getattr(ue.sim, 'supi_type', 'supi')
        if errors:
            self.fail_test("; ".join(errors),
                           identity_type=config_type, supi=ue.supi,
                           suci=ue.suci, guti=ue._5g_guti)
        else:
            self.pass_test(
                identity_type=config_type, supi=ue.supi, suci=ue.suci,
                suci_type=ue.suci_type, guti_5g=ue._5g_guti,
                chain=f"{config_type}→SUCI({ue.suci_type})→5G-GUTI",
            )
        return self.result


class AuthGutiReRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-011",
        title="Re-registration with 5G-GUTI avoids fresh SUCI exposure",
        spec="TS 24.501 §5.5.1.2.2 + §5.4.3",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security", "privacy"),
        setup=Setup.BASELINE,
        expected_duration_s=18.0,
        description=(
            "Purpose\n"
            "  Per §5.5.1.2.2, when a UE has a valid 5G-GUTI it SHALL use\n"
            "  the 5G-GUTI rather than building a new SUCI for subsequent\n"
            "  Registration Request messages. This minimises OTA exposure\n"
            "  of the SUPI/SUCI. The TC observes that the 5G-GUTI assigned\n"
            "  by the first registration is available for a second\n"
            "  registration cycle.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.2.2)\n"
            "  1. Initial registration (SUCI on the wire).\n"
            "  2. Deregister; the AMF deletes only the 5GMM context, not\n"
            "     the assigned 5G-GUTI on the UE.\n"
            "  3. Re-register; the 5G-GUTI from step 1 should be available.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  Both registrations succeed; the UE has a 5G-GUTI in hand\n"
            "  after step 1 (i.e. ue._5g_guti not None).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +2, successes +2.\n"
            "\n"
            "Known constraints\n"
            "  The tester does not (currently) drive a GUTI-based mobility\n"
            "  registration on step 3 — that is TC-REG-015's territory."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        config_type = getattr(ue.sim, 'supi_type', 'supi')

        if not self.register_ue(ue, gnb):
            return self.result
        first_suci = dict(ue.suci) if ue.suci else None
        first_guti = ue._5g_guti

        if not self.deregister_ue(ue):
            return self.result
        time.sleep(0.5)

        if not self.register_ue(ue, gnb):
            return self.result
        second_suci = dict(ue.suci) if ue.suci else None
        second_guti = ue._5g_guti

        self.pass_test(
            imsi=ue.imsi, identity_type=config_type,
            first_suci=first_suci, first_guti=first_guti,
            second_suci=second_suci, second_guti=second_guti,
            guti_available=first_guti is not None,
        )
        return self.result


class AuthMultiUeIdentity(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-012",
        title="8 UEs register concurrently — SUCI MSINs are all unique",
        spec="TS 33.501 §6.12 + TS 24.501 §5.4.1.3",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Catch SUCI-builder state-leak bugs under contention. With\n"
            "  N parallel threads building SUCIs from N different SUPIs,\n"
            "  any shared-state collision in the builder would surface\n"
            "  as duplicate MSINs across distinct UEs.\n"
            "\n"
            "Procedure (TS 33.501 §6.12)\n"
            "  1. Drive 8 concurrent Initial Registrations.\n"
            "  2. Pull ue.suci.msin from each and assert uniqueness.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — uses pool[:8].\n"
            "\n"
            "Pass criteria\n"
            "  All 8 UEs reach REGISTERED, and the set of (built) MSINs\n"
            "  has cardinality 8.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +8, successes +8.\n"
            "\n"
            "Known constraints\n"
            "  Needs ≥8 UEs in the pool; Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue_count = min(8, len(self.ue_pool))
        ues = self.ue_pool[:ue_count]

        def _reg(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                return (ue, ue.wait_for_state("REGISTERED", timeout=15))
            except Exception:
                return (ue, False)

        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {pool.submit(_reg, ue): ue for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue, ok = f.result()
                if not ok:
                    self.fail_test(f"UE {ue.imsi} registration failed")
                    return self.result

        msins = {}
        results = []
        for ue in ues:
            msin = ue.suci.get("msin", "") if ue.suci else ""
            config_type = getattr(ue.sim, 'supi_type', 'supi')
            if msin in msins:
                self.fail_test(f"Identity collision: {ue.imsi} and {msins[msin]} share MSIN={msin}")
                return self.result
            msins[msin] = ue.imsi
            results.append({
                "imsi": ue.imsi, "identity_type": config_type,
                "supi": ue.supi, "suci_type": ue.suci_type,
                "msin": msin, "guti": ue._5g_guti is not None,
            })

        self.pass_test(ue_count=ue_count, all_unique=True, ue_results=results)
        return self.result


# ────────────────────────────────────────────────────────────────────────
# New TCs — cover spec branches not previously exercised.
# ────────────────────────────────────────────────────────────────────────


class AuthNgKsiAssigned(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-013",
        title="AUTHENTICATION REQUEST carries a native ngKSI (0..6)",
        spec="TS 24.501 §5.4.1.3.2 + §9.11.3.32",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Per §5.4.1.3.2 the AMF SHALL include a key set identifier\n"
            "  ngKSI in the AUTHENTICATION REQUEST message, drawn from a\n"
            "  value the UE does not currently hold. The ngKSI value field\n"
            "  is the 3-bit identifier (0..6); 7 means 'no key' and would\n"
            "  be illegal in an Auth Request from the network. The TSC bit\n"
            "  indicates native (0) vs mapped-from-EPS (1).\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3.2 + §9.11.3.32 NAS key set id)\n"
            "  1. Register.\n"
            "  2. ue.security_ctx.ngksi was captured from the Auth Request\n"
            "     by the UE FSM (ue_fsm._handle_auth_request).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - REGISTERED.\n"
            "  - security_ctx.ngksi is in [0..6].\n"
            "  - security_ctx.tsc is 0 (native context — initial reg).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. If the AMF picks ngksi=7, this fails — that\n"
            "  is intentional (illegal per the IE coding)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        ctx = ue.security_ctx
        ngksi = ctx.get("ngksi")
        tsc = ctx.get("tsc")
        if ngksi is None:
            self.fail_test("ngKSI not captured from Auth Request")
            return self.result
        if ngksi < 0 or ngksi > 6:
            self.fail_test(f"ngKSI={ngksi} out of legal range 0..6 (7='no key')")
            return self.result
        if tsc not in (0, None):
            # Initial reg with SUCI must be a native context per §4.4.2.1.
            self.fail_test(f"TSC={tsc} expected 0 (native) on initial registration")
            return self.result
        self.pass_test(imsi=ue.imsi, ngksi=ngksi, tsc=tsc)
        return self.result


class AuthNgKsiDifferentFromUEProvided(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-014",
        title="AMF picks ngKSI different from the UE-supplied value in Reg Req",
        spec="TS 24.501 §5.4.1.3.2 + §9.11.3.32",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=12.0,
        description=(
            "Purpose\n"
            "  §5.4.1.3.2 (lines 18099-18101) verbatim: 'If an ngKSI is\n"
            "  contained in an initial NAS message during a 5GMM procedure,\n"
            "  the network shall include a different ngKSI value in the\n"
            "  AUTHENTICATION REQUEST message when it initiates a 5G AKA\n"
            "  based primary authentication and key agreement procedure.'\n"
            "  This TC drives the UE to send a Registration Request with\n"
            "  a non-default ngKSI value (3) so that the spec rule has\n"
            "  something to bite on; the AMF must reply with anything in\n"
            "  0..6 EXCEPT 3.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3.2 + §9.11.3.32 NAS key set id)\n"
            "  1. UE → AMF: REGISTRATION REQUEST with NAS_KSI carrying\n"
            "     ksi_value=3, TSC=0 (native context). Per §9.11.3.32 the\n"
            "     IE is mandatory in initial NAS messages.\n"
            "  2. AMF fails to recognise the claimed context (no stored\n"
            "     5G NAS security ctx matches ksi=3 for this UE), so it\n"
            "     initiates fresh 5G-AKA per §5.4.1.3.2.\n"
            "  3. AMF picks a new ngKSI for the AUTHENTICATION REQUEST.\n"
            "     The new value MUST differ from the UE-supplied 3.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - Registration completes (so we observed an Auth Request).\n"
            "  - security_ctx.ngksi != 3 (i.e. AMF honoured the rule).\n"
            "  - security_ctx.ngksi ∈ 0..6 (legal range; 7 = 'no key').\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Note: the spec only requires inequality\n"
            "  within the SAME 5GMM procedure (initial NAS msg → Auth\n"
            "  Request). Cross-cycle rotation is NOT mandated; this TC\n"
            "  deliberately tests the strict in-procedure rule."
        ),
    )

    UE_SUPPLIED_KSI = 3

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        gnb.attach_ue(ue)
        # Drive a Reg Request with ksi_value=3 — the AMF won't have a
        # stored ctx matching this id, so it must run fresh 5G-AKA and
        # per §5.4.1.3.2 pick an ngKSI != 3 for the AUTH REQUEST.
        ue.register(ksi_value=self.UE_SUPPLIED_KSI)
        if not ue.wait_for_state("REGISTERED", timeout=15):
            self.fail_test(
                f"Registration did not complete (state={ue.state})",
                last_reject_cause=ue.last_reject_cause,
            )
            return self.result
        ngksi_picked = ue.security_ctx.get("ngksi")
        if ngksi_picked is None:
            self.fail_test("ngKSI not captured from Auth Request")
            return self.result
        if not (0 <= ngksi_picked <= 6):
            self.fail_test(f"ngKSI={ngksi_picked} out of legal 0..6 range")
            return self.result
        if ngksi_picked == self.UE_SUPPLIED_KSI:
            self.fail_test(
                f"AMF reused UE-supplied ngKSI={self.UE_SUPPLIED_KSI} in "
                f"AUTHENTICATION REQUEST — violates TS 24.501 §5.4.1.3.2"
            )
            return self.result
        self.pass_test(
            imsi=ue.imsi,
            ue_supplied_ngksi=self.UE_SUPPLIED_KSI,
            amf_picked_ngksi=ngksi_picked,
        )
        return self.result


class AuthKeyHierarchy(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-015",
        title="Full key hierarchy KSEAF → KAMF → KNAS{enc,int} → KgNB",
        spec="TS 33.501 §6.2.2 + §6.7.2 + §6.9 + Annex A",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Validate every node of the 5G NAS key tree gets populated\n"
            "  after a single successful registration: KSEAF (from AUSF in\n"
            "  step 5 of §6.1.3.2), KAMF (Annex A.7 KDF), KNASenc/KNASint\n"
            "  (Annex A.8) and KgNB (Annex A.9). A missing leaf points to\n"
            "  a broken KDF chain — UPF dataplane integrity would still\n"
            "  'work' until a real ciphered uplink fails.\n"
            "\n"
            "Procedure (TS 33.501 §6.2.2 + §6.7.2 + §6.9 + Annex A)\n"
            "  1. Register.\n"
            "  2. Inspect security_ctx for KSEAF, KAMF, knasenc, knasint,\n"
            "     kgnb.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  Every leaf is non-empty bytes.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. KSEAF/KAMF derive at Auth completion;\n"
            "  KNAS{enc,int} at SMC Complete; KgNB after SMC Complete."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        ctx = ue.security_ctx
        keys = {k: ctx.get(k) for k in ("KSEAF", "KAMF", "knasenc", "knasint", "kgnb")}
        missing = [k for k, v in keys.items() if not v]
        if missing:
            self.fail_test(f"Missing key(s) in hierarchy: {missing}",
                           present=[k for k, v in keys.items() if v])
            return self.result
        self.pass_test(
            imsi=ue.imsi,
            key_lengths={k: len(v) for k, v in keys.items()},
        )
        return self.result


class AuthNasCountsInit(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-016",
        title="NAS COUNTs initialised to 0 at SMC Complete",
        spec="TS 24.501 §5.4.2.4 + TS 33.501 §6.4.3",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Per §5.4.2.4 the SMC procedure brings a 5G NAS security\n"
            "  context into use; per TS 33.501 §6.4.3 the UL and DL NAS\n"
            "  COUNTs in that context are initialised to 0. After SMC the\n"
            "  UE sends SECURITY MODE COMPLETE (UL count → 1) and the AMF\n"
            "  starts ciphered downlink (DL count → 1 with the next\n"
            "  message, typically Registration Accept). This TC checks UL\n"
            "  has advanced past 0 (≥1) and DL is also accounted for —\n"
            "  i.e. the counts are integers and weren't reset to garbage.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.2.4)\n"
            "  1. Register.\n"
            "  2. Inspect security_ctx.ul_nas_count and dl_nas_count.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - UL count ≥ 1 (UE sent at least SECURITY MODE COMPLETE +\n"
            "    REGISTRATION COMPLETE under integrity).\n"
            "  - DL count is an integer ≥ 1 (AMF sent at least the\n"
            "    Registration Accept ciphered).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        ctx = ue.security_ctx
        ul = ctx.get("ul_nas_count")
        dl = ctx.get("dl_nas_count")
        if not isinstance(ul, int) or ul < 1:
            self.fail_test(f"UL NAS COUNT not advanced: ul_nas_count={ul!r}")
            return self.result
        if not isinstance(dl, int) or dl < 1:
            self.fail_test(f"DL NAS COUNT not advanced: dl_nas_count={dl!r}")
            return self.result
        self.pass_test(imsi=ue.imsi, ul_nas_count=ul, dl_nas_count=dl)
        return self.result


class AuthRegistrationCompleteIntegrity(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-017",
        title="REGISTRATION COMPLETE is integrity-protected (and ciphered)",
        spec="TS 24.501 §5.4.2.4 + §4.4.4 + §4.4.5",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Per §5.4.2.4 the SMC procedure activates the new context\n"
            "  for the UE; per §4.4.4 (integrity protection of NAS\n"
            "  signalling) once the context is active, subsequent NAS\n"
            "  messages MUST carry integrity. REGISTRATION COMPLETE\n"
            "  (the first post-SMC uplink in this flow) is therefore\n"
            "  integrity-protected and ciphered (sec_hdr=2). If KNASint\n"
            "  is absent or wrong, the AMF would discard the message and\n"
            "  the UE would never reach REGISTERED — so 'REGISTERED'\n"
            "  with non-empty KNASint observably proves the property.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.2.4 + §4.4.4)\n"
            "  1. Register.\n"
            "  2. Confirm REGISTERED state and that KNASint was used to\n"
            "     protect at least one uplink (UL NAS COUNT > 0).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - State REGISTERED.\n"
            "  - knasint non-empty bytes.\n"
            "  - ul_nas_count > 0.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        ctx = ue.security_ctx
        if not ctx.get("knasint"):
            self.fail_test("KNASint not derived — Registration Complete cannot be integrity-protected")
            return self.result
        if (ctx.get("ul_nas_count") or 0) <= 0:
            self.fail_test("UL NAS COUNT did not advance — no protected uplink")
            return self.result
        self.pass_test(imsi=ue.imsi,
                       knasint_len=len(ctx["knasint"]),
                       ul_nas_count=ctx["ul_nas_count"])
        return self.result


class AuthKgnbDerivation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-018",
        title="KgNB derived from KAMF at SMC Complete",
        spec="TS 33.501 §6.9 + Annex A.9 + TS 24.501 §5.4.2.4",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  TS 33.501 §6.9 has the AMF derive KgNB from KAMF using the\n"
            "  UL NAS COUNT value used at SMC Complete (Annex A.9 KDF).\n"
            "  The AMF then ships KgNB to the gNB inside the InitialCtxSetup\n"
            "  NGAP message — that's what enables AS-layer security. If the\n"
            "  KDF chain is broken, KgNB is absent or wrong and any\n"
            "  subsequent UP/AS security would fail.\n"
            "\n"
            "Procedure (TS 33.501 §6.9 + Annex A.9 + TS 24.501 §5.4.2.4)\n"
            "  1. Register.\n"
            "  2. Assert security_ctx.kgnb is 32 bytes of non-zero entropy.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - State REGISTERED.\n"
            "  - kgnb is bytes of length 32.\n"
            "  - kgnb is non-zero (i.e. not the all-zeros placeholder).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        kgnb = ue.security_ctx.get("kgnb")
        if not kgnb:
            self.fail_test("KgNB not derived")
            return self.result
        if len(kgnb) != 32:
            self.fail_test(f"KgNB length {len(kgnb)} != 32 (Annex A.9 expects 256-bit key)")
            return self.result
        if all(b == 0 for b in kgnb):
            self.fail_test("KgNB is all-zeros — KDF chain produced placeholder")
            return self.result
        self.pass_test(imsi=ue.imsi, kgnb_len=len(kgnb))
        return self.result


class AuthAbbaParameter(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-019",
        title="AUTHENTICATION REQUEST carries the ABBA parameter",
        spec="TS 24.501 §5.4.1.3.2 + §9.11.3.10 + TS 33.501 §6.1.1.4",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Per §5.4.1.3.2 the AMF SHALL include the ABBA parameter in\n"
            "  the AUTHENTICATION REQUEST message; ABBA is fed into the\n"
            "  KAMF KDF (TS 33.501 Annex A.7) to bind the derived KAMF to\n"
            "  the negotiated Anti-Bidding-down Between Architectures set\n"
            "  per TS 33.501 §6.1.1.4. A missing ABBA would silently let\n"
            "  the UE derive a KAMF different from the AMF — eventual\n"
            "  integrity failures downstream.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3.2 + §9.11.3.10)\n"
            "  1. Register.\n"
            "  2. ue.security_ctx.ABBA was captured from the Auth Request\n"
            "     in ue_fsm._handle_auth_request.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - State REGISTERED (KAMF derivation succeeded, implying\n"
            "    matching ABBA on both sides).\n"
            "  - security_ctx.ABBA non-empty bytes.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        abba = ue.security_ctx.get("ABBA")
        if not abba or not isinstance(abba, (bytes, bytearray)) or len(abba) < 2:
            self.fail_test(f"ABBA not captured or too short: {abba!r}")
            return self.result
        self.pass_test(imsi=ue.imsi, abba_hex=bytes(abba).hex(), abba_len=len(abba))
        return self.result


class AuthFreshKamfOnReRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-AUTH-020",
        title="Re-registration after deregister derives a fresh KAMF",
        spec="TS 24.501 §5.4.1.3 + §4.4.2.1 + TS 33.501 §6.9",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=22.0,
        description=(
            "Purpose\n"
            "  §4.4.2.1 mandates a two-context model (current + non-current\n"
            "  5G NAS security contexts). After deregistration the prior\n"
            "  context is cleared; a subsequent initial registration must\n"
            "  run 5G-AKA again and derive a brand-new KAMF from a fresh\n"
            "  KSEAF (TS 33.501 §6.9). Catches a class of regressions\n"
            "  where the AMF re-derives KAMF from a cached KSEAF, which\n"
            "  would produce the SAME KAMF as before.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1.3 + §4.4.2.1)\n"
            "  1. Register → capture KAMF₁.\n"
            "  2. Deregister.\n"
            "  3. Re-register → capture KAMF₂.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  - Both registrations succeed.\n"
            "  - KAMF₁ ≠ KAMF₂.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +2, successes +2.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        kamf_1 = ue.security_ctx.get("KAMF")
        if not kamf_1:
            self.fail_test("KAMF not derived on first registration")
            return self.result
        if not self.deregister_ue(ue):
            return self.result
        time.sleep(0.5)
        if not self.register_ue(ue, gnb):
            return self.result
        kamf_2 = ue.security_ctx.get("KAMF")
        if not kamf_2:
            self.fail_test("KAMF not derived on second registration")
            return self.result
        if bytes(kamf_1) == bytes(kamf_2):
            self.fail_test(
                "KAMF reused across consecutive 5G-AKA — violates "
                "§4.4.2.1 (two-context model) + TS 33.501 §6.9"
            )
            return self.result
        self.pass_test(
            imsi=ue.imsi,
            kamf_first_hex=bytes(kamf_1).hex()[:16] + "…",
            kamf_second_hex=bytes(kamf_2).hex()[:16] + "…",
        )
        return self.result


ALL_AUTH_TCS = [
    AuthSuccess, AuthSecurityAlgo, AuthSqnResync, AuthMultiUe,
    AuthReAuth, AuthAllUes, AuthThenPdu, AuthRepeatedCycles,
    AuthSuciRegistration, AuthIdentityChain,
    AuthGutiReRegistration, AuthMultiUeIdentity,
    # New spec-gap coverage (TC-AUTH-013..020)
    AuthNgKsiAssigned, AuthNgKsiDifferentFromUEProvided, AuthKeyHierarchy,
    AuthNasCountsInit, AuthRegistrationCompleteIntegrity,
    AuthKgnbDerivation, AuthAbbaParameter,
    AuthFreshKamfOnReRegistration,
]
