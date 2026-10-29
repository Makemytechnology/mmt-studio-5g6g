# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: PDU Session establishment / throughput.

PDU-domain TCs (TC-PDU-*) cover the UE-requested PDU Session
Establishment procedure of the local TS 24.501 v19.6.2 §6.4.1.
Traffic-domain TCs (TC-TRF-*) further below run UDP throughput on
top of an established session — they live in this file for historical
reasons but their SPEC.domain is TRAFFIC, not PDU_SESSION.
"""

import concurrent.futures
import time

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.config import TRAFFIC_DURATION
from src.traffic.engine import TrafficEngine, derive_gateway, bw_to_mbps
from src.observability.core_stats import collect_upf_stats, compute_upf_delta
from src.traffic.stats.mos import estimate_mos


class PduSessionEstablishment(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PDU-001",
        title="UE-requested PDU session establishment on default DNN (internet, SST=1)",
        spec="TS 24.501 §6.4.1.2 + §6.4.1.3",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "foundational"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the UE-requested PDU Session\n"
            "  Establishment procedure (TS 24.501 §6.4.1). If this\n"
            "  doesn't pass, every traffic, QoS, multi-DNN and slicing\n"
            "  test downstream is dead-on-arrival.\n"
            "\n"
            "Procedure (TS 24.501 §6.4.1.2 + §6.4.1.3)\n"
            "  1. Initial Registration → REGISTERED.\n"
            "  2. UE allocates a PSI and PTI per §6.4.1.2 (must not\n"
            "     duplicate an existing session/transaction).\n"
            "  3. UE sends PDU SESSION ESTABLISHMENT REQUEST wrapped in\n"
            "     UL NAS TRANSPORT (§8.2.10) carrying DNN=internet,\n"
            "     S-NSSAI SST=1, PDU type=IPv4, Integrity-Prot Max Data\n"
            "     Rate IE = 0xFFFF.\n"
            "  4. SMF accepts → PDU SESSION ESTABLISHMENT ACCEPT with a\n"
            "     UE IP (per §6.4.1.3).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi    — UE to drive (default: first UE in pool).\n"
            "  dnn     — DNN string (default: 'internet').\n"
            "  timeout — wait for PDU IP, seconds (default: 20).\n"
            "\n"
            "Pass criteria\n"
            "  ue.pdu_sessions[1].ip is non-empty.\n"
            "\n"
            "KPI deltas (/api/kpis/pdu_session)\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The lab UPF must serve the 'internet'\n"
            "  DNN; SMF must have a static or dynamic IP pool."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        dnn = self.params.get("dnn", "internet")
        timeout = self.params.get("timeout", 20)

        if not self.register_ue(ue, gnb, timeout):
            return self.result

        if self.establish_pdu(ue, psi=1, dnn=dnn, timeout=timeout):
            session = ue.pdu_sessions[1]
            self.pass_test(imsi=ue.imsi, dnn=dnn, ip=session.get("ip"))
        return self.result


class ImsPduSessionTest(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PDU-002",
        title="PDU session establishment on the IMS DNN (DNN=ims, PSI=2)",
        spec="TS 24.501 §6.4.1.2 + §6.4.1.3 + TS 23.501 §5.6.1",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "ims"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Pre-req for VoNR / ViNR / IMS-signalling suites: a PDU\n"
            "  session on the IMS DNN with a separately-allocated PSI.\n"
            "  Per TS 23.501 §5.6.1 the SMF picks a UPF specifically for\n"
            "  IMS traffic; the established UE IP becomes the source for\n"
            "  SIP registration.\n"
            "\n"
            "Procedure (TS 24.501 §6.4.1.2 + §6.4.1.3)\n"
            "  1. Initial Registration → REGISTERED.\n"
            "  2. UE-requested PDU Session Establishment with DNN=ims,\n"
            "     PSI=2 (any value not currently in use; the convention\n"
            "     is 2 to separate from the default-DNN PSI=1).\n"
            "  3. Wait for ACCEPT with UE IP on the IMS DNN.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None (uses first UE in pool, DNN='ims', PSI=2).\n"
            "\n"
            "Pass criteria\n"
            "  ue.pdu_sessions[2].ip is non-empty.\n"
            "\n"
            "KPI deltas\n"
            "  /api/kpis/pdu_session: attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Lab UPF/UDM must have the 'ims' DNN\n"
            "  provisioned. If not, the SMF emits PDU SESSION\n"
            "  ESTABLISHMENT REJECT with cause #27 'Missing or unknown\n"
            "  DNN' per §6.4.1.4."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        if self.establish_pdu(ue, psi=2, dnn="ims"):
            session = ue.pdu_sessions[2]
            self.pass_test(imsi=ue.imsi, dnn="ims", ip=session.get("ip"))
        return self.result


class MultiPduSessionTest(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PDU-003",
        title="Single UE establishes two concurrent PDU sessions (internet + IMS)",
        spec="TS 24.501 §6.4.1.2",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Per §6.4.1.2 the UE 'shall allocate a PDU session ID\n"
            "  which is not currently being used by another PDU session\n"
            "  over either 3GPP access or non-3GPP access.' This TC pins\n"
            "  the AMF/SMF/UPF multi-session plumbing: two simultaneous\n"
            "  PDU sessions with distinct DNNs from the same UE land on\n"
            "  distinct GTP-U tunnels and distinct UE IPs.\n"
            "\n"
            "Procedure (TS 24.501 §6.4.1.2)\n"
            "  1. Register UE.\n"
            "  2. PSI=1 establishment on DNN=internet.\n"
            "  3. PSI=2 establishment on DNN=ims.\n"
            "  4. Both sessions must end up with allocated UE IPs.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  ue.pdu_sessions[1].ip and ue.pdu_sessions[2].ip are both\n"
            "  non-empty.\n"
            "\n"
            "KPI deltas\n"
            "  /api/kpis/pdu_session: attempts +2, successes +2.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Same UPF/SMF DNN provisioning as TC-PDU-001\n"
            "  and TC-PDU-002."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        if not self.register_ue(ue, gnb):
            return self.result
        ok_inet = self.establish_pdu(ue, psi=1, dnn="internet")
        ok_ims = self.establish_pdu(ue, psi=2, dnn="ims")
        if ok_inet and ok_ims:
            self.pass_test(
                internet_ip=ue.pdu_sessions.get(1, {}).get("ip"),
                ims_ip=ue.pdu_sessions.get(2, {}).get("ip"),
            )
        return self.result


class TwoUePduTest(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PDU-004",
        title="Two UEs on the same gNB each establish a PDU session",
        spec="TS 24.501 §6.4.1.2 + TS 23.501 §5.8.2.4",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Two distinct UEs anchored on the same gNB each establish\n"
            "  a PDU session. Surfaces SMF UE-IP-pool allocation races\n"
            "  and UPF per-UE GTP-U tunnel isolation — both UEs must\n"
            "  get distinct IPs and neither must disturb the other's\n"
            "  session state.\n"
            "\n"
            "Procedure (TS 24.501 §6.4.1.2 + TS 23.501 §5.8.2.4)\n"
            "  1. Register UE-1 and UE-2 on the same gNB.\n"
            "  2. Each establishes PSI=1 on DNN=internet.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — uses pool[:2].\n"
            "\n"
            "Pass criteria\n"
            "  Both UE-1 and UE-2 hold a non-empty PSI=1 IP.\n"
            "\n"
            "KPI deltas\n"
            "  /api/kpis/pdu_session: attempts +2, successes +2.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Needs ≥ 2 UEs in the pool."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs")
            return self.result
        ue1, ue2 = self.ue_pool[0], self.ue_pool[1]
        for ue in (ue1, ue2):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result
        ip1 = ue1.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip2 = ue2.pdu_sessions.get(1, {}).get("ip", "unknown")
        if ip1 == ip2:
            self.fail_test(
                f"Two UEs received the SAME IP {ip1} — SMF allocator "
                f"violated TS 23.501 §5.8.2.4 (per-UE IP pool uniqueness)"
            )
            return self.result
        self.pass_test(ue1_ip=ip1, ue2_ip=ip2, distinct_ips=True)
        return self.result


# ────────────────────────────────────────────────────────────────────────────
# New spec-aligned coverage (TC-PDU-005..008)
# ────────────────────────────────────────────────────────────────────────────


class PduCustomPsi(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PDU-005",
        title="PDU session with non-default PSI (10) succeeds",
        spec="TS 24.501 §6.4.1.2 + §9.11.4.11 (PDU session identity)",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Per §6.4.1.2 the UE 'shall allocate a PDU session ID\n"
            "  which is not currently being used by another PDU session'.\n"
            "  §9.11.4.11 makes PSI a 4-bit value with reserved coding\n"
            "  for 0 ('No PDU session identity assigned') and 15 (TWIF\n"
            "  on behalf of N5CW devices). Any 1..14 must work; this TC\n"
            "  picks PSI=10 to catch SMF/UPF implementations that hard-\n"
            "  code PSI=1/2 in their per-UE bookkeeping.\n"
            "\n"
            "Procedure (TS 24.501 §6.4.1.2 + §9.11.4.11)\n"
            "  1. Register UE.\n"
            "  2. UE-requested PDU Session Establishment with PSI=10,\n"
            "     DNN=internet, S-NSSAI SST=1.\n"
            "  3. SMF must accept and allocate a UE IP.\n"
            "\n"
            "Parameters (self.params)\n"
            "  psi — override (default: 10).\n"
            "  dnn — override (default: 'internet').\n"
            "\n"
            "Pass criteria\n"
            "  ue.pdu_sessions[psi].ip is non-empty.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. PSI must be in 1..14; 0 and 15 are\n"
            "  reserved per §9.11.4.11."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        psi = self.params.get("psi", 10)
        dnn = self.params.get("dnn", "internet")
        if not (1 <= psi <= 14):
            self.fail_test(f"PSI={psi} out of legal range 1..14 "
                           f"(0 and 15 reserved per §9.11.4.11)")
            return self.result
        if not self.register_ue(ue, gnb):
            return self.result
        if self.establish_pdu(ue, psi=psi, dnn=dnn):
            session = ue.pdu_sessions[psi]
            self.pass_test(imsi=ue.imsi, psi=psi, dnn=dnn,
                           ip=session.get("ip"))
        return self.result


class PduCustomSnssaiSd(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PDU-006",
        title="PDU session with S-NSSAI carrying SST + SD (4-octet value)",
        spec="TS 24.501 §6.4.1.2 + §9.11.2.8 (S-NSSAI)",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        slice=Slice.URLLC,
        tags=("conformance", "slicing"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  §9.11.2.8 Table 9.11.2.8.1 — S-NSSAI value: 1 byte SST\n"
            "  with optional 3-byte SD (Slice Differentiator). This TC\n"
            "  drives a session with an explicit SD ≠ 0 / 0xFFFFFF, so\n"
            "  the SMF must:\n"
            "  (a) decode the 4-byte S-NSSAI value field,\n"
            "  (b) look up the slice in its NSSAI catalog using BOTH\n"
            "      SST and SD,\n"
            "  (c) allocate a UE IP from the slice-appropriate pool.\n"
            "  Catches SMF implementations that ignore SD and silently\n"
            "  fall back to SST-only matching.\n"
            "\n"
            "Procedure (TS 24.501 §6.4.1.2 + §9.11.2.8)\n"
            "  1. Register UE.\n"
            "  2. Establish PDU with S-NSSAI = (SST=2, SD=0x111111) on\n"
            "     PSI=1, DNN=internet (eMBB/URLLC vary by deployment).\n"
            "\n"
            "Parameters (self.params)\n"
            "  sst — slice service type (default: 2 = URLLC).\n"
            "  sd  — slice differentiator (default: 0x111111).\n"
            "  dnn — DNN string (default: 'internet').\n"
            "\n"
            "Pass criteria\n"
            "  ue.pdu_sessions[1].ip is non-empty.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The lab's NSSAI catalog must include the\n"
            "  (SST,SD) pair driven here. If not, SMF rejects with cause\n"
            "  #62 'No network slices available' per §5.5.1.2.7."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        sst = self.params.get("sst", 2)
        sd = self.params.get("sd", 0x111111)
        dnn = self.params.get("dnn", "internet")
        if not self.register_ue(ue, gnb):
            return self.result
        if self.establish_pdu(ue, psi=1, dnn=dnn, sst=sst, sd=sd):
            session = ue.pdu_sessions[1]
            self.pass_test(
                imsi=ue.imsi, dnn=dnn,
                snssai={"sst": sst, "sd_hex": f"0x{sd:06X}"},
                ip=session.get("ip"),
            )
        return self.result


class PduConcurrentMultiUe(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PDU-007",
        title="Five UEs concurrently establish PDU sessions on the same gNB",
        spec="TS 24.501 §6.4.1.2 + TS 23.501 §5.8.2.4",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Stress version of TC-PDU-004. Five UEs in parallel\n"
            "  threads each register and establish a PDU session.\n"
            "  Catches:\n"
            "  - SMF UE-IP allocator races (two UEs handed the same IP).\n"
            "  - UPF per-UE GTP-U tunnel table contention.\n"
            "  - AMF NGAP InitialContextSetup ↔ DL NAS Transport\n"
            "    serialisation issues under burst load.\n"
            "\n"
            "Procedure (TS 24.501 §6.4.1.2)\n"
            "  1. Spawn N threads; each thread: attach UE → register →\n"
            "     establish PDU session on PSI=1, DNN=internet.\n"
            "  2. Collect each UE's allocated IP.\n"
            "\n"
            "Parameters (self.params)\n"
            "  count — UEs to drive (default: 5; capped by pool size).\n"
            "\n"
            "Pass criteria\n"
            "  - Every UE reaches REGISTERED.\n"
            "  - Every UE has a non-empty PSI=1 IP.\n"
            "  - All N IPs are unique (no allocator collision).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +N, successes +N (registration AND PDU).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Needs ≥ count UEs in the pool."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        count = min(self.params.get("count", 5), len(self.ue_pool))
        if count < 2:
            self.fail_test(f"Need at least 2 UEs, have {count}")
            return self.result
        ues = self.ue_pool[:count]

        def _setup(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                if not ue.wait_for_state("REGISTERED", timeout=20):
                    return (ue, False, None)
                ue.establish_pdu_session()
                deadline = time.time() + 20
                while time.time() < deadline:
                    sess = ue.pdu_sessions.get(1, {})
                    if sess.get("ip"):
                        return (ue, True, sess.get("ip"))
                    time.sleep(0.2)
                return (ue, False, None)
            except Exception:
                return (ue, False, None)

        with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:
            results = list(pool.map(_setup, ues))

        details = [{"imsi": u.imsi, "ok": ok, "ip": ip} for u, ok, ip in results]
        ips = [r["ip"] for r in details if r["ip"]]
        failed = [r for r in details if not r["ok"]]
        if failed:
            self.fail_test(f"{len(failed)}/{count} UEs failed",
                           ue_results=details)
            return self.result
        if len(set(ips)) != len(ips):
            self.fail_test(
                f"SMF handed duplicate IPs across UEs: {sorted(ips)}",
                ue_results=details,
            )
            return self.result
        self.pass_test(ue_count=count, all_unique_ips=True,
                       ue_results=details)
        return self.result


class PduReregisterReestablish(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PDU-008",
        title="Re-register + re-establish PDU session on same PSI",
        spec="TS 24.501 §6.4.1.2 + §5.5.2.1 + §5.5.2.2.3",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  Per §5.5.2.1 + §5.5.2.2.3, de-registration locally\n"
            "  releases all PDU sessions and the SMF marks them deleted.\n"
            "  A fresh registration must then be able to reuse the same\n"
            "  PSI (1) because the prior session is gone from the SMF's\n"
            "  per-UE table. Catches SMF leaks that would surface as\n"
            "  cause #43 'PTI already in use' or #44 'PDU session ID\n"
            "  already in use' on the second establishment.\n"
            "\n"
            "Procedure (TS 24.501 §6.4.1.2 + §5.5.2.1 + §5.5.2.2.3)\n"
            "  1. Register + establish PDU on PSI=1 → IP_1.\n"
            "  2. Deregister (switch-off). SMF must release PSI=1.\n"
            "  3. Re-register + re-establish PDU on PSI=1 → IP_2.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  - First PDU session reaches IP allocation.\n"
            "  - Second registration + second PDU session both succeed.\n"
            "  - The second IP is non-empty (may equal or differ from\n"
            "    the first depending on SMF pool policy).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +2, successes +2 (per procedure).\n"
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
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result
        ip_1 = ue.pdu_sessions.get(1, {}).get("ip")
        if not self.deregister_ue(ue):
            return self.result
        time.sleep(0.5)
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result
        ip_2 = ue.pdu_sessions.get(1, {}).get("ip")
        self.pass_test(
            imsi=ue.imsi,
            first_ip=ip_1, second_ip=ip_2,
            ip_reused=(ip_1 == ip_2),
        )
        return self.result


class UdpBidirectional(TestCase):
    """UDP bidirectional throughput with jitter and packet loss."""
    SPEC = TestSpec(
        tc_id="TC-TRF-006",
        title="UDP bidir at 50 Mbps target: both UL and DL reach ≥ 85% of target via GTP-U",
        spec="TS 29.281 §4",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Verifies full-duplex UDP through one GTP-U tunnel. UL and DL share\n"
            "  the UPF PDR/FAR/QER and TEID; running both simultaneously surfaces\n"
            "  half-duplex regressions and TUN ring-buffer contention that single-\n"
            "  direction tests miss.\n"
            "\n"
            "Procedure (TS 29.281 §5 GTP-U)\n"
            "  1. require_gnb() + require_ue() (auto-create from config if needed).\n"
            "  2. register_ue + establish_pdu (DNN=internet, PSI=1).\n"
            "  3. server = params.iperf_server OR derive_gateway(ue_ip).\n"
            "  4. TrafficEngine.run_bidir(ip_a=ue_ip, ip_b=server, proto=udp,\n"
            "     ul_port=port, dl_port=port+1, bandwidth=50M, duration=\n"
            "     TRAFFIC_DURATION, udp=True). One iperf3 server + client per\n"
            "     direction; both streams traverse the same UPF PDR/FAR/TEID.\n"
            "\n"
            "Parameters (self.params)\n"
            "  bandwidth — per-direction target rate (default: 50M).\n"
            "  duration  — seconds (default: TRAFFIC_DURATION env, typically 30).\n"
            "  port      — UL UDP port; DL uses port+1 (default UL: 5201).\n"
            "  min_ratio — per-direction pass threshold (default: 0.85).\n"
            "\n"
            "Pass criteria\n"
            "  ul_mbps >= 0.85 * target AND dl_mbps >= 0.85 * target. Either\n"
            "  side below threshold fails and the fail message identifies which.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  protocol=UDP, direction=bidirectional, ue_ip, server, target_mbps,\n"
            "  min_mbps, ul_mbps, dl_mbps, ul_jitter_ms, dl_jitter_ms,\n"
            "  ul_loss_pct, dl_loss_pct.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Default duration is longer than TC-TRF-001..005 to\n"
            "  surface sustained-load issues; tagged 'slow'. expected_duration_s=90.\n"
            "  When iperf3 bails early on long debug windows the engine optionally\n"
            "  holds the session open for the remainder so the operator can poke\n"
            "  the UPF / tcpdump without SCTP/NG/PDU being torn down."
        ),
        requires_dataplane=True,
    )

    # Default target bandwidth when no `bandwidth` param is supplied.
    # Subclasses override for fixed-rate variants.
    DEFAULT_BANDWIDTH = "50M"
    # Actual/target ratio below which a flow is considered failing.
    MIN_THROUGHPUT_RATIO = 0.85

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        port = self.params.get("port", 5201)
        duration = self.params.get("duration", TRAFFIC_DURATION)
        bandwidth = self.params.get("bandwidth", self.DEFAULT_BANDWIDTH)
        target_mbps = bw_to_mbps(bandwidth)
        min_ratio = float(self.params.get("min_ratio", self.MIN_THROUGHPUT_RATIO))
        min_mbps = round(target_mbps * min_ratio, 2)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        ul_port = port
        dl_port = port + 1  # separate port to avoid TIME_WAIT conflict

        import time as _t
        t_start = _t.time()
        try:
            engine = TrafficEngine.get()
            ul_stats, dl_stats = engine.run_bidir(
                ip_a=ue_ip, ip_b=server, server=server,
                protocol="udp", ul_port=ul_port, dl_port=dl_port,
                bandwidth=bandwidth, duration=duration, udp=True)

            ul_mbps = round(ul_stats.throughput_kbps / 1000, 2) if ul_stats else 0.0
            dl_mbps = round(dl_stats.throughput_kbps / 1000, 2) if dl_stats else 0.0
            ul_jitter = round(ul_stats.jitter_ms, 3) if ul_stats else 0.0
            dl_jitter = round(dl_stats.jitter_ms, 3) if dl_stats else 0.0
            ul_loss = round(ul_stats.loss_pct, 2) if ul_stats else 0.0
            dl_loss = round(dl_stats.loss_pct, 2) if dl_stats else 0.0

            ul_ok = ul_mbps >= min_mbps
            dl_ok = dl_mbps >= min_mbps

            metrics = dict(
                protocol="UDP", direction="bidirectional",
                ue_ip=ue_ip, server=server,
                target_mbps=target_mbps, min_mbps=min_mbps,
                ul_mbps=ul_mbps, dl_mbps=dl_mbps,
                ul_jitter_ms=ul_jitter, dl_jitter_ms=dl_jitter,
                ul_loss_pct=ul_loss, dl_loss_pct=dl_loss,
            )

            # If iperf3 bailed early (e.g. Go-UPF dropped the TCP control
            # handshake, so iperf3 exits within ~2 min on TCP-SYN retry)
            # and we're running a long debug window, hold the session open
            # for the remainder of `duration` so the operator can tcpdump,
            # poke the UPF, etc., without SCTP/NG/PDU being torn down
            # immediately. Only kicks in when duration > 5 min, so normal
            # automation still fails fast.
            #
            # Poll gNB state every 5s during the hold so we exit cleanly
            # if SCTP drops (AMF UE-inactivity timeout, etc.) instead of
            # sleeping blindly on top of a dead connection.
            elapsed = _t.time() - t_start
            if not (ul_ok and dl_ok) and duration > 300 and elapsed < duration:
                remaining = duration - elapsed
                import logging as _log
                _tclog = _log.getLogger("tester.testcases")
                _tclog.info(
                    "TC-TRF-006: iperf3 returned after %.1fs (duration=%ds); "
                    "holding session open for %.0fs — polling gNB state to "
                    "exit early if SCTP drops. Hit Stop-All to abort.",
                    elapsed, duration, remaining)
                hold_deadline = t_start + duration
                HEALTHY = ("READY", "CONNECTED")
                while _t.time() < hold_deadline:
                    gnb_state = getattr(gnb, "state", None)
                    if gnb_state not in HEALTHY:
                        _tclog.warning(
                            "TC-TRF-006: gNB state=%r during hold — SCTP "
                            "likely dropped (AMF side closed? heartbeat "
                            "timeout?); aborting hold after %.0fs elapsed",
                            gnb_state, _t.time() - t_start)
                        break
                    _t.sleep(5)

            if ul_ok and dl_ok:
                self.pass_test(**metrics)
            else:
                reason = (
                    f"UL={ul_mbps:.1f}/{target_mbps:.0f} Mbps "
                    f"({'OK' if ul_ok else 'LOW'}), "
                    f"DL={dl_mbps:.1f}/{target_mbps:.0f} Mbps "
                    f"({'OK' if dl_ok else 'LOW'}); "
                    f"threshold={min_mbps:.1f} Mbps "
                    f"({int(min_ratio*100)}% of target)"
                )
                self.fail_test(reason, **metrics)
        except Exception as e:
            self.result.status = "ERROR"
            self.result.error = str(e)
        return self.result


def _bidir_desc(mbps: int) -> str:
    pct = int(UdpBidirectional.MIN_THROUGHPUT_RATIO * 100)
    return (
        "Purpose\n"
        f"  Fixed-rate variant of TC-TRF-006 at {mbps} Mbps per direction.\n"
        f"  Pins the user plane's ability to sustain a {mbps} Mbps UDP\n"
        f"  bidir load through one GTP-U tunnel (TS 29.281 §5) without\n"
        "  half-duplex regressions or UPF PDR/FAR/QER throttling, and\n"
        "  exposes TUN ring-buffer contention that single-direction or\n"
        f"  lower-rate tests miss. Pass threshold is {pct}% of target\n"
        "  per direction.\n"
        "\n"
        "Procedure (inherited from UdpBidirectional.run; TS 29.281 §5)\n"
        "  1. require_gnb() + require_ue() (auto-create from config if\n"
        "     pool empty).\n"
        "  2. register_ue + establish_pdu (DNN=internet, PSI=1).\n"
        "  3. server = params.iperf_server OR derive_gateway(ue_ip).\n"
        "  4. ul_port = params.port (default 5201); dl_port = port+1\n"
        "     (separate to avoid TIME_WAIT collisions).\n"
        f"  5. bandwidth = '{mbps}M' (DEFAULT_BANDWIDTH overridden on the\n"
        "     subclass; params.bandwidth wins if supplied).\n"
        "  6. TrafficEngine.run_bidir UDP both directions for\n"
        "     duration=params.duration (default TRAFFIC_DURATION).\n"
        f"  7. Compare ul_mbps and dl_mbps against min_mbps = {mbps} *\n"
        "     params.min_ratio (default 0.85).\n"
        "  8. If both directions failed AND duration > 300, holds the\n"
        "     session open polling gNB state for the remainder so the\n"
        "     operator can debug; normal runs fail fast.\n"
        "\n"
        "Parameters (self.params)\n"
        f"  bandwidth — per-direction target (default: '{mbps}M').\n"
        "  duration  — seconds (default: TRAFFIC_DURATION env).\n"
        "  port      — UL UDP port (default: 5201; DL = port+1).\n"
        f"  min_ratio — per-direction pass threshold (default: 0.85,\n"
        f"              i.e. {pct}% of target).\n"
        "\n"
        "Pass criteria\n"
        f"  ul_mbps >= min_mbps AND dl_mbps >= min_mbps where min_mbps =\n"
        f"  {mbps} * min_ratio. Either side below threshold fails and the\n"
        "  fail message identifies which (UL LOW vs DL LOW).\n"
        "\n"
        "KPI deltas / Reported metrics\n"
        "  protocol='UDP', direction='bidirectional', ue_ip, server,\n"
        "  target_mbps, min_mbps, ul_mbps, dl_mbps, ul_jitter_ms,\n"
        "  dl_jitter_ms, ul_loss_pct, dl_loss_pct.\n"
        "\n"
        "Known constraints\n"
        f"  Setup.BASELINE. Tagged 'scale' / 'slow' — at {mbps} Mbps the\n"
        "  test stresses the iperf3 + DPDK + UPF data path and can\n"
        "  exceed expected_duration_s on a slow host. Errors during\n"
        "  run_bidir set result.status='ERROR' (not FAIL)."
    )


class UdpBidirectional100M(UdpBidirectional):
    SPEC = TestSpec(
        tc_id="TC-TRF-007",
        title="UDP bidirectional throughput at 100 Mbps",
        spec="TS 23.501 §5.7",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=_bidir_desc(100),
        requires_dataplane=True,
    )
    DEFAULT_BANDWIDTH = "100M"


class UdpBidirectional250M(UdpBidirectional):
    SPEC = TestSpec(
        tc_id="TC-TRF-008",
        title="UDP bidirectional throughput at 250 Mbps",
        spec="TS 23.501 §5.7",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=_bidir_desc(250),
        requires_dataplane=True,
    )
    DEFAULT_BANDWIDTH = "250M"


class UdpBidirectional500M(UdpBidirectional):
    SPEC = TestSpec(
        tc_id="TC-TRF-009",
        title="UDP bidirectional throughput at 500 Mbps",
        spec="TS 23.501 §5.7",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=_bidir_desc(500),
        requires_dataplane=True,
    )
    DEFAULT_BANDWIDTH = "500M"


class UdpBidirectional1G(UdpBidirectional):
    SPEC = TestSpec(
        tc_id="TC-TRF-010",
        title="UDP bidirectional throughput at 1 Gbps",
        spec="TS 23.501 §5.7",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=120.0,
        description=_bidir_desc(1000),
        requires_dataplane=True,
    )
    DEFAULT_BANDWIDTH = "1000M"
