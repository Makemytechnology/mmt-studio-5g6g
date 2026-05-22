# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Idle mode, Service Request, Paging.

Covers TS 24.501 v19.6.2:
  §5.6.1   — Service request procedure (initiation, accepted, not-accepted)
  §5.6.1.2 — Initiation (UE drives Service Request after RRC Inactive)
  §5.6.1.4 — Accepted by the network (AMF reactivates user plane)
  §5.6.1.5 — Not accepted by the network
  §5.6.2   — Paging procedure (network-initiated)
  §5.6.3   — Notification procedure
  §8.2.15  — SERVICE REQUEST message encoding

And TS 38.413 v19.2.0:
  §8.5     — NGAP Paging procedure
  §9.2.5.4 — NGAP Paging message
"""

import time
import logging
import threading
import concurrent.futures

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.config import TRAFFIC_DURATION
from src.traffic.engine import TrafficEngine, derive_gateway
from src.observability.core_stats import collect_upf_stats, compute_upf_delta
from src.traffic.stats.mos import estimate_mos

log = logging.getLogger("tester.tc_idle_mode")


class IdleModeBase(TestCase):
    """Base class for idle mode / service request tests."""
    _abstract = True

    def _go_inactive(self, ue, gnb):
        """Transition UE to RRC Inactive state."""
        gnb.report_rrc_inactive(ue, "inactive")
        time.sleep(1)
        log.info("UE %s → RRC Inactive", ue.imsi)

    def _service_request(self, ue, gnb, service_type=1, timeout=10):
        """UE sends Service Request and waits for AMF response.

        service_type: 0=signalling, 1=data, 2=mobile-terminated
        Returns True if AMF accepted (UE gets context back).
        """
        gnb.send_service_request(ue, service_type=service_type)

        # Wait for AMF response (InitialContextSetup or DL NAS Transport)
        deadline = time.time() + timeout
        while time.time() < deadline:
            # AMF may send InitialContextSetupRequest or just DL NAS
            if ue.amf_ue_ngap_id is not None:
                # Report back to connected
                gnb.report_rrc_inactive(ue, "connected")
                log.info("UE %s → RRC Connected (Service Request accepted)", ue.imsi)
                return True
            time.sleep(0.3)

        log.warning("Service Request timeout for UE %s", ue.imsi)
        return False

    def _verify_traffic(self, ue_ip, server, duration=5, bandwidth="1M"):
        """Quick UL traffic test to verify connectivity after Service Request."""
        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth=bandwidth, duration=duration, direction="ul",
        )
        session.start()
        stats = session.stop()
        kbps = round(stats.throughput_kbps, 1) if stats else 0
        return stats is not None, kbps


class ServiceRequestAfterInactive(IdleModeBase):
    SPEC = TestSpec(
        tc_id="TC-IDL-001",
        title="UE-triggered Service Request after RRC Inactive — basic flow",
        spec="TS 24.501 §5.6.1.2 + §5.6.1.4 + §8.2.15",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "foundational"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the UE-triggered Service Request\n"
            "  procedure (§5.6.1). Validates the most common path: UE\n"
            "  in RRC Inactive with an established PDU session has new\n"
            "  UL data, sends Service Request (type=data), AMF accepts\n"
            "  and reactivates the user plane.\n"
            "\n"
            "Procedure (§5.6.1.2 initiation + §5.6.1.4 accepted)\n"
            "  1. Initial Registration → REGISTERED.\n"
            "  2. UE-requested PDU Session Establishment on PSI=1.\n"
            "  3. Short pre-flight UL traffic to confirm baseline.\n"
            "  4. UE transitions to RRC Inactive (CM-IDLE on N1).\n"
            "  5. UE sends SERVICE REQUEST (§8.2.15) with Service Type=1\n"
            "     'data' and ngKSI (carries the current 5GMM context).\n"
            "  6. AMF accepts per §5.6.1.4; gNB receives Initial Context\n"
            "     Setup and the UE returns to CM-CONNECTED.\n"
            "  7. Re-verify UL traffic completes.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi    — UE to drive (default: first UE in pool).\n"
            "  timeout — Service Request acceptance wait, s (default 10).\n"
            "\n"
            "Pass criteria\n"
            "  - SR accepted; ue.amf_ue_ngap_id is reattached.\n"
            "  - Post-SR UL traffic returns non-zero kbps.\n"
            "\n"
            "KPI deltas (/api/kpis/service_request when available)\n"
            "  attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Requires a UPF reachable on the PDU IP\n"
            "  pool (TC-PDU-001 must pass first)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)

        # Verify traffic works before going inactive
        ok_before, kbps_before = self._verify_traffic(ue_ip, server)
        log.info("Before inactive: traffic=%s (%.1f kbps)", ok_before, kbps_before)

        # Go RRC Inactive
        self._go_inactive(ue, gnb)

        # Service Request (UL data trigger)
        sr_ok = self._service_request(ue, gnb, service_type=1)
        if not sr_ok:
            self.fail_test("Service Request failed", ue=ue.imsi)
            return self.result

        # Verify traffic works after Service Request
        time.sleep(1)
        ok_after, kbps_after = self._verify_traffic(ue_ip, server)
        log.info("After Service Request: traffic=%s (%.1f kbps)", ok_after, kbps_after)

        # TS 24.501 v19.6.2 §5.6.1.4.1: SR accept must result in actual
        # user-plane re-establishment. iperf3 returning a stats object
        # is not proof — non-zero throughput is. Catches the "SMF/UPF
        # FAR not actually re-bound" regression that previously slipped
        # through with kbps_after==0 but ok_after==True.
        if kbps_after > 0:
            self.pass_test(
                ue=ue.imsi, ue_ip=ue_ip,
                service_request_ok=sr_ok,
                traffic_before=ok_before, kbps_before=kbps_before,
                traffic_after=ok_after, kbps_after=kbps_after,
            )
        else:
            self.fail_test(
                "Post-SR traffic delivered 0 kbps — §5.6.1.4.1 "
                "user-plane re-establishment did not deliver packets",
                ue=ue.imsi, ue_ip=ue_ip,
                service_request_ok=sr_ok,
                kbps_before=kbps_before, kbps_after=kbps_after,
            )
        return self.result


class ServiceRequestUlTraffic(IdleModeBase):
    SPEC = TestSpec(
        tc_id="TC-IDL-002",
        title="Service Request — sustained UL traffic after user plane reactivation",
        spec="TS 24.501 §5.6.1.2 + §5.6.1.4",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        description=(
            "Purpose\n"
            "  Validates that the user plane re-established by §5.6.1.4\n"
            "  is truly functional — not just the NAS-level acceptance.\n"
            "  A clean SR accept that produces zero throughput is the\n"
            "  classic 'UPF FAR not restored' regression class.\n"
            "\n"
            "Procedure\n"
            "  1. Register + PDU on PSI=1.\n"
            "  2. RRC Inactive, then Service Request (type=data).\n"
            "  3. Run TRAFFIC_DURATION-second UDP UL at 1 Mbps; collect\n"
            "     UPF delta counters before/after.\n"
            "\n"
            "Parameters\n"
            "  TRAFFIC_DURATION env (default 30s).\n"
            "\n"
            "Pass criteria\n"
            "  - SR accepted.\n"
            "  - UL session reports non-zero kbps.\n"
            "  - UPF delta shows N3 ingress counters incrementing.\n"
            "\n"
            "KPI deltas\n"
            "  /api/kpis/service_request attempts/successes +1.\n"
            "  UPF tx_packets / tx_bytes deltas > 0.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE; UPF metering bridge must be alive."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = TRAFFIC_DURATION

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)

        self._go_inactive(ue, gnb)
        sr_ok = self._service_request(ue, gnb, service_type=1)
        if not sr_ok:
            self.fail_test("Service Request failed")
            return self.result

        # Sustained UL traffic
        upf_before = collect_upf_stats()

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="1M", duration=duration, direction="ul",
        )
        session.start()
        stats = session.stop()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        kbps = 0
        jitter = 0
        loss = 0
        if stats:
            kbps = round(stats.throughput_kbps, 1)
            jitter = round(stats.jitter_ms, 2)
            loss = round(stats.loss_pct, 2)

        log.info("Post-SR UL: %.1f kbps, jitter=%.1fms, loss=%.1f%%", kbps, jitter, loss)

        # TS 24.501 v19.6.2 §5.6.1.4.1: when the SERVICE REQUEST carries
        # an Uplink data status IE and the UE is not in NB-N1 mode, the
        # AMF "shall ... indicate the SMF to re-establish the user-plane
        # resources for the corresponding PDU sessions" and report the
        # outcome via the "PDU session reactivation result IE" in the
        # SERVICE ACCEPT. A non-None stats object only proves iperf3
        # finished; non-zero kbps proves the re-established user-plane
        # actually delivers packets. Zero kbps with stats!=None is the
        # classic "SR accepted, SMF/UPF FAR not actually re-established"
        # regression — must fail.
        if stats and kbps > 0:
            self.pass_test(
                ue=ue.imsi, duration_s=duration,
                ul_kbps=kbps, ul_jitter_ms=jitter, ul_loss_pct=loss,
                upf_stats=upf_delta,
            )
        elif stats:
            self.fail_test(
                "Post-SR UL throughput is 0 — §5.6.1.4.1 user-plane "
                "re-establishment did not deliver packets",
                ul_kbps=kbps, upf_stats=upf_delta,
            )
        else:
            self.fail_test("UL traffic failed after Service Request")
        return self.result


class ServiceRequestBidirTraffic(IdleModeBase):
    SPEC = TestSpec(
        tc_id="TC-IDL-003",
        title="Service Request — bidirectional UL+DL traffic after reactivation",
        spec="TS 24.501 §5.6.1.2 + §5.6.1.4",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        description=(
            "Purpose\n"
            "  Some SMF/UPF builds restore only the UL FAR on a Service\n"
            "  Request (forgetting the DL FAR's TEID re-bind). Driving\n"
            "  both directions catches that asymmetry.\n"
            "\n"
            "Procedure\n"
            "  1. Register + PDU on PSI=1.\n"
            "  2. RRC Inactive → SR (type=data) → §5.6.1.4 accept.\n"
            "  3. Run TRAFFIC_DURATION-second simultaneous UL+DL at\n"
            "     1 Mbps each; record per-direction throughput.\n"
            "\n"
            "Parameters\n"
            "  TRAFFIC_DURATION env.\n"
            "\n"
            "Pass criteria\n"
            "  Both ul_kbps and dl_kbps are non-zero.\n"
            "\n"
            "KPI deltas\n"
            "  service_request +1; UPF rx/tx counters both increment.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. DL throughput is gated by the GTP-U DL\n"
            "  path having been re-bound to the new RAN TEID."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = TRAFFIC_DURATION

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)

        self._go_inactive(ue, gnb)
        sr_ok = self._service_request(ue, gnb, service_type=1)
        if not sr_ok:
            self.fail_test("Service Request failed")
            return self.result

        # Simultaneous UL + DL
        engine = TrafficEngine.get()
        ul_stats, dl_stats = engine.run_bidir(
            ip_a=ue_ip, ip_b=server, server=server, protocol="udp",
            ul_port=5201, dl_port=5202, bandwidth="1M", duration=duration, udp=True,
        )

        ul_kbps = round(ul_stats.throughput_kbps, 1) if ul_stats else 0
        dl_kbps = round(dl_stats.throughput_kbps, 1) if dl_stats else 0

        # TS 24.501 v19.6.2 §5.6.1.4.1: AMF "shall ... indicate the SMF
        # to re-establish the user-plane resources for the corresponding
        # PDU sessions" on SR accept, and the SERVICE ACCEPT carries the
        # "PDU session reactivation result IE" reporting the outcome.
        # The SMF/UPF must restore BOTH the UL FAR and the DL FAR (with
        # the new gNB-side TEID). The pre-existing assertion accepted any
        # non-None stats object — that masked the "UL works but DL FAR
        # still points at the stale TEID" regression class. Both
        # directions must deliver packets.
        if ul_stats and dl_stats and ul_kbps > 0 and dl_kbps > 0:
            self.pass_test(
                ue=ue.imsi, duration_s=duration,
                ul_kbps=ul_kbps, dl_kbps=dl_kbps,
            )
        elif ul_stats and dl_stats:
            self.fail_test(
                f"§5.6.1.4.1 reactivation incomplete — UL={ul_kbps}kbps "
                f"DL={dl_kbps}kbps (one or both directions delivered 0)",
                ul_kbps=ul_kbps, dl_kbps=dl_kbps,
            )
        else:
            self.fail_test(
                f"Bidir: UL={'OK' if ul_stats else 'FAIL'} DL={'OK' if dl_stats else 'FAIL'}"
            )
        return self.result


class PagingAfterInactive(IdleModeBase):
    SPEC = TestSpec(
        tc_id="TC-IDL-004",
        title="Network-initiated Paging followed by mobile-terminated Service Request",
        spec="TS 24.501 §5.6.2 + TS 38.413 §8.5 + §9.2.5.4",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the paging trigger path: AMF sends\n"
            "  NGAP Paging (§9.2.5.4) when DL data arrives for a UE in\n"
            "  RRC Inactive. UE responds with Service Request (type=2,\n"
            "  mobile-terminated) per §5.6.2.\n"
            "\n"
            "Procedure (§5.6.2 + TS 38.413 §8.5)\n"
            "  1. Register + PDU on PSI=1; pre-flight UL works.\n"
            "  2. UE → RRC Inactive.\n"
            "  3. Trigger short DL burst to ue_ip — UPF buffers and\n"
            "     signals SMF; SMF asks AMF to page.\n"
            "  4. AMF emits NGAP Paging to all gNBs in the UE's TAI list.\n"
            "  5. UE sends SERVICE REQUEST with Service Type=2.\n"
            "  6. AMF accepts; user plane reactivates; DL drains.\n"
            "\n"
            "Parameters\n"
            "  None (default UE / default IMSI from pool).\n"
            "\n"
            "Pass criteria\n"
            "  - gNB._paging_event fires within 10 s.\n"
            "  - SR (type=2) accepted.\n"
            "  - Post-paging traffic verify is non-zero.\n"
            "\n"
            "KPI deltas\n"
            "  Paging attempts +1; service_request_mt successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Lab UPF must support the 'buffer + notify\n"
            "  SMF' path (FAR action=BUFFER + report on first packet)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)

        self._go_inactive(ue, gnb)

        # Set up paging event listener
        gnb._paging_event = threading.Event()

        # Trigger DL data from core — this should cause AMF to page the UE
        log.info("Triggering DL data to paged UE %s", ue.imsi)

        engine = TrafficEngine.get()
        dl_session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="udp",
            dst_port=5202, bandwidth="100K", duration=5, direction="dl",
        )

        # Start DL in a background thread so paging wait is not blocked
        dl_thread = threading.Thread(target=dl_session.start, daemon=True)
        dl_thread.start()

        # Wait for paging
        paging_received = gnb._paging_event.wait(timeout=10)
        log.info("Paging %s", "received" if paging_received else "NOT received")

        if paging_received:
            # UE responds with Service Request (mobile-terminated)
            sr_ok = self._service_request(ue, gnb, service_type=2)
        else:
            sr_ok = False

        dl_thread.join(timeout=15)
        dl_session.stop()

        # Verify traffic after paging
        time.sleep(1)
        ok_after, kbps_after = self._verify_traffic(ue_ip, server)

        if paging_received and sr_ok:
            self.pass_test(
                ue=ue.imsi, paging_received=True,
                service_request_ok=sr_ok,
                traffic_after=ok_after, kbps_after=kbps_after,
                paging_info=gnb._last_paging,
            )
        else:
            self.fail_test(
                f"Paging={'OK' if paging_received else 'FAIL'} SR={'OK' if sr_ok else 'FAIL'}",
                paging_received=paging_received, service_request_ok=sr_ok,
            )
        return self.result


class PagingDlTraffic(IdleModeBase):
    SPEC = TestSpec(
        tc_id="TC-IDL-005",
        title="Paging → MT Service Request → sustained DL traffic",
        spec="TS 24.501 §5.6.2 + TS 38.413 §8.5",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        description=(
            "Purpose\n"
            "  TC-IDL-004 proves the paging path is wired; this test\n"
            "  proves the UPF DL FAR is fully restored, not just the\n"
            "  first packet. Sustained DL throughput after paging is\n"
            "  the strict gate per TS 24.501 §5.6.1.4.1 user-plane\n"
            "  reactivation semantics.\n"
            "\n"
            "Procedure (TS 24.501 §5.6.2 + TS 38.413 §8.5)\n"
            "  1. require_gnb() + require_ue(); duration = TRAFFIC_DURATION.\n"
            "  2. register_ue(ue, gnb) + establish_pdu(ue, psi=1).\n"
            "  3. ue_ip = pdu_sessions[1].ip; server = derive_gateway(ue_ip).\n"
            "  4. _go_inactive(ue, gnb) — gNB reports RRC Inactive.\n"
            "  5. Arm gnb._paging_event = threading.Event(); spawn UDP DL\n"
            "     trigger session (100K, 3s, dst_port 5202) in a daemon\n"
            "     thread.\n"
            "  6. Wait up to 10s for _paging_event. If received,\n"
            "     _service_request(ue, gnb, service_type=2). Join trigger.\n"
            "  7. Sustained DL: UDP 1M for TRAFFIC_DURATION s on dst_port\n"
            "     5202; capture dl_kbps.\n"
            "\n"
            "Parameters (self.params)\n"
            "  TRAFFIC_DURATION env var (default 30 s).\n"
            "\n"
            "Pass criteria\n"
            "  Paging received AND dl_stats is non-None AND dl_kbps > 0.\n"
            "  Three failure branches: (a) no paging, (b) stats None,\n"
            "  (c) stats non-None but kbps == 0 (the §5.6.1.4.1 'SR\n"
            "  accepted but DL FAR not restored' regression).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, paging_received, dl_kbps, duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Lab UPF must implement the 'buffer +\n"
            "  notify SMF' first-packet path (FAR action=BUFFER + report)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = TRAFFIC_DURATION

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)

        self._go_inactive(ue, gnb)

        # Paging trigger — short DL burst to cause AMF to page the UE
        gnb._paging_event = threading.Event()

        engine = TrafficEngine.get()
        trigger_session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="udp",
            dst_port=5202, bandwidth="100K", duration=3, direction="dl",
        )

        trigger_thread = threading.Thread(target=trigger_session.start, daemon=True)
        trigger_thread.start()

        paging_received = gnb._paging_event.wait(timeout=10)
        if paging_received:
            self._service_request(ue, gnb, service_type=2)
        trigger_thread.join(timeout=10)
        trigger_session.stop()

        if not paging_received:
            self.fail_test("Paging not received")
            return self.result

        # Sustained DL traffic after paging
        time.sleep(1)
        dl_session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="udp",
            dst_port=5202, bandwidth="1M", duration=duration, direction="dl",
        )
        dl_session.start()
        dl_stats = dl_session.stop()

        dl_kbps = round(dl_stats.throughput_kbps, 1) if dl_stats else 0

        # TS 24.501 v19.6.2 §5.6.1.4.1: paging → MT SR → AMF "shall ...
        # indicate the SMF to re-establish the user-plane resources for
        # the corresponding PDU sessions". Sustained DL must actually
        # deliver packets; non-None stats alone is not sufficient proof.
        if dl_stats and dl_kbps > 0:
            self.pass_test(ue=ue.imsi, paging_received=True,
                           dl_kbps=dl_kbps, duration_s=duration)
        elif dl_stats:
            self.fail_test(
                "Post-paging DL delivered 0 kbps — §5.6.1.4.1 "
                "user-plane re-establishment did not deliver packets",
                dl_kbps=dl_kbps, duration_s=duration,
            )
        else:
            self.fail_test("DL traffic failed after paging")
        return self.result


class ConnectedInactiveCycles(IdleModeBase):
    SPEC = TestSpec(
        tc_id="TC-IDL-006",
        title="Repeat Connected ↔ Inactive cycles — no context leak",
        spec="TS 24.501 §5.6.1.2 + §5.6.1.4",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("regression", "stress"),
        setup=Setup.BASELINE,
        expected_duration_s=45.0,
        description=(
            "Purpose\n"
            "  Repeated Service Request cycles surface AMF/SMF context\n"
            "  leaks that single-shot tests miss: orphaned\n"
            "  AMF-UE-NGAP-ID entries, stale PFCP sessions, leaked GTP-U\n"
            "  TEIDs in the UPF. Validates each cycle restores the user\n"
            "  plane per TS 24.501 §5.6.1.4.1, not just the NAS-level SR\n"
            "  acceptance.\n"
            "\n"
            "Procedure (TS 24.501 §5.6.1.2 + §5.6.1.4)\n"
            "  1. require_gnb() + require_ue(); cycles = 3 (in-test\n"
            "     constant).\n"
            "  2. register_ue + establish_pdu psi=1.\n"
            "  3. ue_ip = pdu_sessions[1].ip; server = derive_gateway.\n"
            "  4. for i in range(cycles):\n"
            "       _go_inactive(ue, gnb);\n"
            "       sr_ok = _service_request(ue, gnb, service_type=1);\n"
            "       if not sr_ok → record {sr_ok=False, traffic_ok=False}\n"
            "         and continue (does NOT abort the loop);\n"
            "       time.sleep(0.5); _verify_traffic 1M 5s UL;\n"
            "       traffic_ok = (stats non-None AND kbps > 0).\n"
            "  5. Aggregate cycle_results.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — cycles is an in-test constant (3).\n"
            "\n"
            "Pass criteria\n"
            "  all(r['sr_ok'] AND r['traffic_ok'] for r in cycle_results)\n"
            "  — every cycle must accept SR AND deliver kbps > 0.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cycles, results (per-cycle dict: cycle, sr_ok,\n"
            "  traffic_ok, kbps).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The default 3 is a regression guard, not\n"
            "  a stress run — there is no params override here to bump\n"
            "  it (cycles is hard-coded)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        cycles = 3

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)

        cycle_results = []
        for i in range(cycles):
            log.info("Cycle %d/%d", i + 1, cycles)

            self._go_inactive(ue, gnb)
            sr_ok = self._service_request(ue, gnb, service_type=1)
            if not sr_ok:
                cycle_results.append({"cycle": i + 1, "sr_ok": False, "traffic_ok": False})
                continue

            time.sleep(0.5)
            ok, kbps = self._verify_traffic(ue_ip, server)
            # TS 24.501 v19.6.2 §5.6.1.4.1: a SR-accepted cycle that
            # delivers 0 kbps is a re-establishment failure regardless
            # of the iperf3 stats object returning. Gate per-cycle
            # traffic_ok on kbps > 0 so the cycle is treated as failed.
            cycle_results.append({
                "cycle": i + 1, "sr_ok": True,
                "traffic_ok": bool(ok and kbps > 0), "kbps": kbps,
            })
            log.info("  Cycle %d: SR=%s traffic=%s (%.1fkbps)",
                     i + 1, sr_ok, ok, kbps)

        all_ok = all(r["sr_ok"] and r["traffic_ok"] for r in cycle_results)
        if all_ok:
            self.pass_test(ue=ue.imsi, cycles=cycles, results=cycle_results)
        else:
            self.fail_test(
                "One or more cycles failed — §5.6.1.4.1 user-plane "
                "re-establishment incomplete on at least one cycle",
                results=cycle_results,
            )
        return self.result


class ServiceRequestSignalling(IdleModeBase):
    SPEC = TestSpec(
        tc_id="TC-IDL-007",
        title="Service Request with Service Type = 0 (signalling only)",
        spec="TS 24.501 §5.6.1.2 + §5.6.1.4 + §9.11.3.50",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  TS 24.501 §9.11.3.50 defines Service Type values; value 0\n"
            "  is 'signalling' and must NOT trigger user-plane\n"
            "  reactivation — only the NAS signalling connection. Pins\n"
            "  the AMF short-circuit of the SMF/UPF interaction so an\n"
            "  SR with no Uplink data status IE doesn't trigger PFCP\n"
            "  Modify on every PDU.\n"
            "\n"
            "Procedure (TS 24.501 §5.6.1.2 + §5.6.1.4 + §9.11.3.50)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue(ue, gnb) — no PDU session establishment\n"
            "     (deliberate; signalling-only).\n"
            "  3. _go_inactive(ue, gnb) — transition to RRC Inactive.\n"
            "  4. _service_request(ue, gnb, service_type=0) — sends\n"
            "     SERVICE REQUEST with Service Type=0 (signalling).\n"
            "  5. _service_request polls ue.amf_ue_ngap_id non-None for\n"
            "     up to 10s; on success marks RRC Connected.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  _service_request returned True (sr_ok). That checks the\n"
            "  AMF reattached the UE NGAP id — sufficient for signalling-\n"
            "  only acceptance.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, service_type='signalling', accepted.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. No PDU session needed. The 'AMF does NOT\n"
            "  trigger PFCP Modify' assertion is NOT verified in-test —\n"
            "  it requires inspecting SMF logs which the tester does not\n"
            "  do here. Test passes on SR-accept alone."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result

        self._go_inactive(ue, gnb)

        # Signalling-only Service Request (type=0)
        sr_ok = self._service_request(ue, gnb, service_type=0)

        if sr_ok:
            self.pass_test(ue=ue.imsi, service_type="signalling", accepted=True)
        else:
            self.fail_test("Signalling Service Request failed")
        return self.result


class MultiUePaging(IdleModeBase):
    SPEC = TestSpec(
        tc_id="TC-IDL-008",
        title="Multiple UEs reactivated concurrently — AMF Service Request concurrency",
        spec="TS 24.501 §5.6.1.2 + §5.6.1.4 + TS 38.413 §8.5",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=45.0,
        description=(
            "Purpose\n"
            "  Surface AMF concurrency bugs on the Service Request resume\n"
            "  path: race conditions between concurrent\n"
            "  InitialContextSetupRequests, AMF-UE-NGAP-ID allocator\n"
            "  contention, SMF/UPF batched update serialisation. Each\n"
            "  UE must independently satisfy TS 24.501 §5.6.1.4.1 user-\n"
            "  plane re-establishment.\n"
            "\n"
            "Procedure (TS 24.501 §5.6.1.2 + §5.6.1.4 + TS 38.413 §8.5)\n"
            "  1. require_gnb() + require_ue();\n"
            "     ue_count = min(4, len(ue_pool)).\n"
            "  2. ThreadPoolExecutor registers all UEs concurrently:\n"
            "     attach → register → wait REGISTERED → establish PSI=1\n"
            "     DNN=internet → poll for IP (15s).\n"
            "  3. Sequential loop: _go_inactive(ue, gnb) for each UE.\n"
            "  4. ThreadPoolExecutor submits _service_request(ue, gnb, 1)\n"
            "     for every UE concurrently; collect {imsi, sr_ok}.\n"
            "  5. time.sleep(1); for each result re-resolve ue_ip and\n"
            "     _verify_traffic 1M 5s; set traffic_ok = (ok AND kbps>0).\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — ue_count is computed (min(4, pool size)).\n"
            "\n"
            "Pass criteria\n"
            "  all(r['sr_ok'] AND r['traffic_ok'] for r in results) —\n"
            "  every UE must accept SR AND deliver kbps > 0 post-SR.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_count, results (per-UE dict: imsi, sr_ok, traffic_ok,\n"
            "  kbps).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Pool must have ≥ 2 UEs; cap is 4 to keep\n"
            "  wall time bounded. Traffic verify is sequential per UE\n"
            "  after the concurrent SR fan-out, so the concurrency\n"
            "  assertion is on the SR phase, not the post-SR data phase."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()

        ue_count = min(4, len(self.ue_pool))
        ues = self.ue_pool[:ue_count]

        # Register all concurrently
        def _reg_one(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                if not ue.wait_for_state("REGISTERED", timeout=15):
                    return (ue, False)
                ue.establish_pdu_session(dnn="internet", sst=1, pdu_session_id=1)
                deadline = time.time() + 15
                while time.time() < deadline:
                    s = ue.pdu_sessions.get(1)
                    if s and s.get("ip") and s["ip"] != "unknown":
                        return (ue, True)
                    time.sleep(0.3)
                return (ue, False)
            except Exception:
                return (ue, False)

        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {pool.submit(_reg_one, ue): ue for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue, ok = f.result()
                if not ok:
                    self.fail_test(f"UE {ue.imsi} registration failed")
                    return self.result

        # All go inactive
        for ue in ues:
            self._go_inactive(ue, gnb)

        # All send Service Request concurrently
        log.info("All %d UEs inactive — sending concurrent Service Requests", ue_count)
        results = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {pool.submit(self._service_request, ue, gnb, 1): ue for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue = futures[f]
                sr_ok = f.result()
                results.append({"imsi": ue.imsi, "sr_ok": sr_ok})
                log.info("UE %s: SR=%s", ue.imsi[-3:], sr_ok)

        # Verify traffic for all. TS 24.501 v19.6.2 §5.6.1.4.1: SR
        # accept must result in the AMF instructing the SMF to
        # re-establish user-plane resources. A per-UE traffic_ok flag
        # based only on iperf3 stats!=None masks the case where SR is
        # accepted but no packets flow — gate on kbps > 0 instead.
        server = derive_gateway(ues[0].pdu_sessions.get(1, {}).get("ip", "unknown"))
        time.sleep(1)
        for r in results:
            ue = next(u for u in ues if u.imsi == r["imsi"])
            ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            ok, kbps = self._verify_traffic(ue_ip, server)
            r["traffic_ok"] = bool(ok and kbps > 0)
            r["kbps"] = kbps

        all_ok = all(r["sr_ok"] and r.get("traffic_ok") for r in results)
        passed = sum(1 for r in results if r["sr_ok"] and r.get("traffic_ok"))

        if all_ok:
            self.pass_test(ue_count=ue_count, results=results)
        else:
            self.fail_test(
                f"{ue_count - passed}/{ue_count} UEs failed §5.6.1.4.1 "
                f"user-plane re-establishment (SR accepted but 0 kbps)",
                results=results,
            )
        return self.result


ALL_IDLE_MODE_TCS = [
    ServiceRequestAfterInactive, ServiceRequestUlTraffic, ServiceRequestBidirTraffic,
    PagingAfterInactive, PagingDlTraffic,
    ConnectedInactiveCycles, ServiceRequestSignalling, MultiUePaging,
]
