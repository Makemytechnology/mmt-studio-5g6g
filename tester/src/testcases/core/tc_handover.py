# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: N2 (NGAP) Handover — inter-gNB mobility via AMF.

Covers TS 38.413 v19.2.0 NGAP procedures:
  §8.4.2 — Handover Preparation (HandoverRequired → AMF →
           HandoverRequest → HandoverRequestAcknowledge → HandoverCommand)
  §8.4.3 — Handover Resource Allocation (target-side bearer setup)
  §8.4.4 — Handover Notification (target → AMF: HandoverNotify;
           AMF → source: UEContextReleaseCommand)
  §8.4.5 — Handover Cancellation (source aborts mid-prep)
  §9.2.3 — Handover message families (Required / Request /
           RequestAcknowledge / Command / Notify / Cancel)

System-level reference: TS 23.502 v19.7.0 §4.9.2 (N2-based handover).
The source gNB sends HandoverRequired; AMF mediates; the target gNB
accepts; SMF/UPF perform the N4 path switch via §4.9.2 step 11.
"""

import time
import logging
import threading
import concurrent.futures

from src.testcases.base import TestCase, StopTest, _ensure_ip_on_interface
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.config import TRAFFIC_DURATION
from src.traffic.engine import TrafficEngine, derive_gateway
from src.observability.core_stats import collect_upf_stats, compute_upf_delta

log = logging.getLogger("tester.tc_handover")


class HandoverBase(TestCase):
    """Base class for handover tests — creates two gNBs."""
    _abstract = True

    def _get_gnb_for_ue(self, ue, gnb_map):
        """Get the gNB a UE should initially attach to (from gnb_name in UE config).

        Args:
            ue: UeStateMachine
            gnb_map: dict of gnb_name → GnbStateMachine
        Returns: GnbStateMachine or None if gnb_name not configured/found
        """
        gnb_name = getattr(ue.sim, 'gnb_name', '') or ''
        if gnb_name and gnb_name in gnb_map:
            return gnb_map[gnb_name]
        return None

    def _create_two_gnbs(self):
        """Create source (gNB-1) and target (gNB-2) from config profiles.

        Returns (gnb_1, gnb_2). Also sets self._gnb_map = {gnb_name: gnb}.
        """
        from src.statemachine.gnb_fsm import GnbStateMachine
        from src.protocol.gnb_config import gnb_cfg_list
        import src.config as _cfg

        profiles = gnb_cfg_list(_cfg.GNB_PROFILES_PATH)
        if len(profiles) < 2:
            self.fail_test("Need at least 2 gNB config profiles for handover tests. "
                           "Create a second gNB with a different IP/gNB-ID.")
            raise StopTest()

        gnbs = []
        self._gnb_map = {}
        for profile in profiles[:2]:
            # Validate — all config must come from GUI
            required = ["gnb_id", "gnb_name", "amf_ip", "mcc", "mnc", "tac"]
            missing = [f for f in required if not profile.get(f)]
            if missing:
                self.fail_test(f"gNB config '{profile.get('gnb_name', '?')}' missing: {missing}")
                raise StopTest()
            if not profile.get("slices"):
                self.fail_test(f"gNB config '{profile['gnb_name']}' missing slices.")
                raise StopTest()

            gnb_id_str = str(profile["gnb_id"])
            gnb_id = int(gnb_id_str, 16) if gnb_id_str.startswith("0x") else int(gnb_id_str)
            slices = profile["slices"]
            for s in slices:
                if isinstance(s.get("sd"), str) and s["sd"].startswith("0x"):
                    s["sd"] = int(s["sd"], 16)

            # Ensure IP alias if needed
            gnb_ip = profile.get("gnb_ip")
            iface = profile.get("interface")
            if gnb_ip and iface:
                _ensure_ip_on_interface(iface, gnb_ip)

            gtpu_mgr = None
            try:
                import src.app as _app
                if hasattr(_app, 'gtpu_manager'):
                    gtpu_mgr = _app.gtpu_manager
            except Exception:
                pass

            gnb = GnbStateMachine(
                amf_ip=profile["amf_ip"],
                amf_port=profile.get("amf_port", 38412),
                gnb_id=gnb_id,
                gnb_name=profile["gnb_name"],
                mcc=profile["mcc"],
                mnc=profile["mnc"],
                tac=profile["tac"],
                slices=slices,
                source_ip=gnb_ip,
                gtpu_manager=gtpu_mgr,
            )

            ok = gnb.connect()
            if not ok:
                self.fail_test(f"gNB '{gnb.gnb_name}' failed SCTP connect")
                raise StopTest()
            if not gnb.wait_for_state("READY", timeout=10):
                gnb.disconnect()
                self.fail_test(f"gNB '{gnb.gnb_name}' NG Setup failed (state={gnb.state})")
                raise StopTest()

            self.gnb_pool.append(gnb)
            gnbs.append(gnb)
            self._gnb_map[gnb.gnb_name] = gnb
            log.info("gNB '%s' ready (gnb_id=0x%X, ip=%s)", gnb.gnb_name, gnb.gnb_id, gnb.gnb_ip)

        # Validate: gNBs must have different gnb_ids for AMF to route HandoverRequest correctly
        if gnbs[0].gnb_id == gnbs[1].gnb_id:
            self.fail_test(
                f"Both gNBs have the same gnb_id (0x{gnbs[0].gnb_id:X}). "
                f"AMF cannot distinguish them for handover. "
                f"Configure different gNB IDs in gNB Config (e.g., 0x500000 and 0x500001).")
            raise StopTest()

        if gnbs[0].source_ip == gnbs[1].source_ip:
            log.warning("Both gNBs have the same source_ip (%s) — "
                        "configure different IPs for proper GTP-U routing",
                        gnbs[0].source_ip)

        return gnbs[0], gnbs[1]

    def _do_handover(self, ue, source_gnb, target_gnb, timeout=10):
        """Execute full N2 handover sequence. Returns True on success.

        Flow (TS 38.413 §8.4):
          1. source → AMF: HandoverRequired
          2. AMF → target: HandoverRequest
          3. target → AMF: HandoverRequestAcknowledge
          4. AMF → source: HandoverCommand
          5. source → AMF: UplinkRANStatusTransfer (PDCP COUNT)
             AMF → target: DownlinkRANStatusTransfer
          6. source → UPF: GTP-U End Marker (TS 29.281 §5.1)
          7. target → AMF: HandoverNotify
          8. AMF → source: UEContextReleaseCommand
        """
        ok = source_gnb.initiate_handover(ue, target_gnb)
        if not ok:
            return False

        if not source_gnb._ho_command_event.wait(timeout=timeout):
            log.warning("HandoverCommand not received within %ds", timeout)
            if not hasattr(target_gnb, '_ho_context') or target_gnb._ho_context is None:
                return False
        elif source_gnb._ho_prep_failure is not None:
            log.warning("HandoverPreparationFailure: cause=%s",
                        source_gnb._ho_prep_failure)
            return False

        # Status transfer + End Marker (in-spec post-HandoverCommand actions).
        try:
            source_gnb.send_uplink_ran_status_transfer(ue)
        except Exception as e:
            log.debug("UplinkRANStatusTransfer skipped: %s", e)
        try:
            source_gnb.send_end_markers(ue)
        except Exception as e:
            log.debug("End Marker send skipped: %s", e)

        time.sleep(0.5)

        ok = target_gnb.complete_handover(ue, source_gnb, timeout=timeout)
        if not ok:
            log.warning("Target gNB failed to complete handover")
            return False

        time.sleep(1)
        log.info("Handover complete: UE %s now on %s", ue.imsi, target_gnb.gnb_name)
        return True


class BasicHandover(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-001",
        title="Basic N2 handover — full signalling path, no traffic",
        spec="TS 38.413 §8.4.2 + §8.4.4 + TS 23.502 §4.9.2",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "foundational"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the N2 handover signalling path. If\n"
            "  every message in TS 38.413 §8.4.2 + §8.4.4 doesn't reach\n"
            "  its intended recipient and the UE context fails to migrate\n"
            "  without losing its PDU session, every downstream HO test\n"
            "  is dead-on-arrival.\n"
            "\n"
            "Procedure (TS 38.413 §8.4.2 prep + §8.4.4 notification,\n"
            "TS 23.502 §4.9.2 steps 1-12)\n"
            "  1. _create_two_gnbs() — load two gNB profiles, bring each\n"
            "     up via SCTP+NG Setup, build {gnb_name: gnb} map.\n"
            "  2. require_ue() — pull first UE from sim DB.\n"
            "  3. _get_gnb_for_ue(ue, _gnb_map) — use UE config's\n"
            "     gnb_name to pick initial source; the other gNB is the\n"
            "     HO target.\n"
            "  4. register_ue(ue, source_gnb) — full 5G-AKA via NGAP/NAS.\n"
            "  5. establish_pdu(ue, psi=1) — NAS PDU Session Estab on PSI=1.\n"
            "  6. _do_handover() drives source.initiate_handover() →\n"
            "     waits for _ho_command_event → UplinkRANStatusTransfer\n"
            "     → send_end_markers (TS 29.281 §5.1) → target\n"
            "     .complete_handover() (HandoverNotify).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool; gnb_name in\n"
            "         UE config picks the initial gNB).\n"
            "\n"
            "Pass criteria\n"
            "  _do_handover() returned True (HandoverCommand received,\n"
            "  HandoverNotify completed, target gNB became ue.gnb).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, source_gnb, target_gnb, pdu_sessions, ue_on_target.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — pretest resets sacore to baseline.\n"
            "  Requires ≥2 gNB profiles in gnb_profiles.yaml with\n"
            "  distinct gnb_id values; UE must have gnb_name set in its\n"
            "  config (otherwise fail_test before any HO is attempted).\n"
            "  Does NOT verify data-plane continuity — that's TC-HO-002."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        self.require_ue()

        ue = self.ue_pool[0]
        # UE must have gnb_name configured in UE Config
        initial_gnb = self._get_gnb_for_ue(ue, self._gnb_map)
        if not initial_gnb:
            self.fail_test(f"UE {ue.imsi}: gnb_name not configured in UE Config. "
                           f"Set gNB for this UE (available: {list(self._gnb_map.keys())})")
            return self.result
        # The other gNB is the handover target
        ho_target = target_gnb if initial_gnb.gnb_name == source_gnb.gnb_name else source_gnb
        source_gnb, target_gnb = initial_gnb, ho_target

        if not self.register_ue(ue, source_gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        # Verify PDU session is fully active before handover
        sess = ue.pdu_sessions.get(1, {})
        log.info("UE %s registered on %s — PDU: IP=%s TEID=0x%08X TUN=%s",
                 ue.imsi, source_gnb.gnb_name,
                 sess.get('ip', '?'), sess.get('local_teid', 0), sess.get('tun', '?'))

        ok = self._do_handover(ue, source_gnb, target_gnb)

        if ok:
            self.pass_test(
                ue=ue.imsi,
                source_gnb=source_gnb.gnb_name,
                target_gnb=target_gnb.gnb_name,
                pdu_sessions=list(ue.pdu_sessions.keys()),
                ue_on_target=ue.gnb.gnb_name if ue.gnb else "none",
            )
        else:
            self.fail_test(
                "N2 handover failed",
                ue=ue.imsi,
                source_gnb=source_gnb.gnb_name,
                target_gnb=target_gnb.gnb_name,
            )
        return self.result


class HandoverWithData(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-002",
        title="N2 handover with active UL data — traffic continuity",
        spec="TS 38.413 §8.4.2 + §8.4.4 + TS 23.502 §4.9.2 + TS 29.281 §5.1",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=180.0,
        description=(
            "Purpose\n"
            "  TC-HO-001 verifies signalling; this pins data-plane\n"
            "  continuity. TS 23.502 §4.9.2 step-11 N4 path switch must\n"
            "  re-bind the SMF's UE-side TEID to the target gNB, and\n"
            "  End Markers (TS 29.281 §5.1) on the source path flush\n"
            "  in-flight packets so the target doesn't drop them as\n"
            "  out-of-order.\n"
            "\n"
            "Procedure (TS 38.413 §8.4 + TS 23.502 §4.9.2 + TS 29.281 §5.1)\n"
            "  1. _create_two_gnbs(); require_ue(); pick initial source\n"
            "     gNB from UE config's gnb_name.\n"
            "  2. register_ue + establish_pdu on PSI=1 (DNN=internet).\n"
            "  3. collect_upf_stats() snapshot for delta.\n"
            "  4. Phase-1: TrafficEngine.create_session UDP UL 1M for\n"
            "     TRAFFIC_DURATION s through source; record kbps/jitter/\n"
            "     loss.\n"
            "  5. _do_handover(ue, source, target) — full §8.4 path;\n"
            "     record ho_ms = wall-clock duration.\n"
            "  6. 0.5s settle; Phase-2: identical UDP UL 1M via target.\n"
            "  7. compute_upf_delta() for before/after counters.\n"
            "\n"
            "Parameters (self.params)\n"
            "  TRAFFIC_DURATION env var (default 30 s per phase).\n"
            "\n"
            "Pass criteria\n"
            "  Handover succeeded (_do_handover returned True). Both\n"
            "  phases run unconditionally; failure only fires when the\n"
            "  HO itself fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, source_gnb, target_gnb, phase1_kbps, phase1_jitter_ms,\n"
            "  phase1_loss_pct, handover_ms, phase2_kbps, phase2_jitter_ms,\n"
            "  phase2_loss_pct, data_continuity (post_kbps > 0),\n"
            "  phase_duration_s, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Wall time ≈ 2×TRAFFIC_DURATION + HO time.\n"
            "  HOLLOW-PASS shape: pass_test is unconditional once HO\n"
            "  succeeds — phase2_kbps==0 is reported in data_continuity\n"
            "  flag but does NOT drive fail_test."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        self.require_ue()

        ue = self.ue_pool[0]
        initial_gnb = self._get_gnb_for_ue(ue, self._gnb_map)
        if not initial_gnb:
            self.fail_test(f"UE {ue.imsi}: gnb_name not configured")
            return self.result
        ho_target = target_gnb if initial_gnb.gnb_name == source_gnb.gnb_name else source_gnb
        source_gnb, target_gnb = initial_gnb, ho_target

        if not self.register_ue(ue, source_gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)
        phase_duration = TRAFFIC_DURATION

        sess = ue.pdu_sessions.get(1, {})
        log.info("UE %s on %s — IP=%s TEID=0x%08X TUN=%s",
                 ue.imsi, source_gnb.gnb_name,
                 sess.get('ip', '?'), sess.get('local_teid', 0), sess.get('tun', '?'))

        engine = TrafficEngine.get()
        upf_before = collect_upf_stats()

        # ── Phase 1: 60s traffic through source gNB ──
        log.info("Phase 1: %ds UDP 1Mbps through %s", phase_duration, source_gnb.gnb_name)
        pre_session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="1M", duration=phase_duration, direction="ul",
        )
        pre_session.start()
        pre_stats = pre_session.stop()
        pre_kbps = round(pre_stats.throughput_kbps, 1)
        pre_jitter = round(pre_stats.jitter_ms, 2)
        pre_loss = round(pre_stats.loss_pct, 2)
        log.info("Phase 1 done: %.1f kbps, jitter=%.1fms, loss=%.1f%%",
                 pre_kbps, pre_jitter, pre_loss)

        # ── Handover ──
        log.info("Handover: %s → %s", source_gnb.gnb_name, target_gnb.gnb_name)
        ho_start = time.time()
        ok = self._do_handover(ue, source_gnb, target_gnb)
        ho_ms = round((time.time() - ho_start) * 1000)
        if not ok:
            self.fail_test("Handover failed — cannot test data continuity",
                           pre_kbps=pre_kbps, handover_ms=ho_ms)
            return self.result
        log.info("Handover complete in %dms — UE now on %s", ho_ms, target_gnb.gnb_name)

        # ── Phase 2: 60s traffic through target gNB ──
        time.sleep(0.5)
        log.info("Phase 2: %ds UDP 1Mbps through %s", phase_duration, target_gnb.gnb_name)
        post_session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="1M", duration=phase_duration, direction="ul",
        )
        post_session.start()
        post_stats = post_session.stop()
        post_kbps = round(post_stats.throughput_kbps, 1)
        post_jitter = round(post_stats.jitter_ms, 2)
        post_loss = round(post_stats.loss_pct, 2)

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        log.info("Phase 2 done: %.1f kbps, jitter=%.1fms, loss=%.1f%%",
                 post_kbps, post_jitter, post_loss)
        log.info("Summary: pre=%.1fkbps → HO(%dms) → post=%.1fkbps",
                 pre_kbps, ho_ms, post_kbps)

        self.pass_test(
            ue=ue.imsi,
            source_gnb=source_gnb.gnb_name,
            target_gnb=target_gnb.gnb_name,
            phase1_kbps=pre_kbps, phase1_jitter_ms=pre_jitter, phase1_loss_pct=pre_loss,
            handover_ms=ho_ms,
            phase2_kbps=post_kbps, phase2_jitter_ms=post_jitter, phase2_loss_pct=post_loss,
            data_continuity=post_kbps > 0,
            phase_duration_s=phase_duration,
            upf_stats=upf_delta,
        )
        return self.result


class HandoverVoNR(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-003",
        title="N2 handover during active VoNR (IMS) call — voice continuity",
        spec="TS 38.413 §8.4.2 + §8.4.4 + TS 23.502 §4.9.2 + TS 23.228 §5.4",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.CSCF),
        severity=Severity.MAJOR,
        tags=("conformance", "ims"),
        setup=Setup.BASELINE,
        expected_duration_s=45.0,
        description=(
            "Purpose\n"
            "  Voice is the strictest continuity test: a 5xx-ms handover\n"
            "  shows as audible silence and the MOS score drops. Cell-\n"
            "  edge HO during an active VoNR call is the canonical field\n"
            "  failure operators see; this TC pins it under TS 23.228\n"
            "  §5.4 IMS PS-to-PS service-continuity expectations.\n"
            "\n"
            "Procedure (TS 38.413 §8.4 + TS 23.502 §4.9.2 + TS 23.228 §5.4)\n"
            "  1. _create_two_gnbs(); require_ue(); demand ≥2 UEs in pool.\n"
            "  2. For UE-A and UE-B: register on source_gnb, establish\n"
            "     PDU on PSI=2 (DNN=ims).\n"
            "  3. Pre-handover: ThreadPoolExecutor runs two\n"
            "     send_rtp_stream calls in parallel (A→B + B→A, audio\n"
            "     payload, port 20000/20001) for duration seconds.\n"
            "  4. Aggregate stats: pre_jitter = max(A.jitter, B.jitter);\n"
            "     pre_mos via ImsVoiceCallQuality._estimate_mos().\n"
            "  5. _do_handover(ue_a, source, target) — UE-B stays.\n"
            "  6. 1s settle; re-resolve tun for UE-A; repeat the RTP\n"
            "     measurement.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration — RTP measurement window in s (in-test constant 10).\n"
            "\n"
            "Pass criteria\n"
            "  Handover succeeded (_do_handover returned True). MOS bar\n"
            "  is computed and reported (post_mos, quality bucket) but\n"
            "  pass_test is unconditional once HO succeeds.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_a, ue_b, source_gnb, target_gnb, pre_mos, post_mos,\n"
            "  pre_jitter_ms, post_jitter_ms, quality, voice_continuity.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. UE pool must be ≥2. IMS DNN must be\n"
            "  provisioned. HOLLOW-PASS shape: voice_continuity (post_mos\n"
            "  ≥ 2.5) is recorded but does NOT drive fail_test — only the\n"
            "  HO outcome does."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs for VoNR handover test")
            return self.result

        ue_a = self.ue_pool[0]
        ue_b = self.ue_pool[1]
        duration = 10

        # Register both UEs on source gNB with IMS PDU
        for ue in (ue_a, ue_b):
            if not self.register_ue(ue, source_gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        from src.testcases.ims.tc_ims import _get_tun_for_ue, ImsVoiceCallQuality
        from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

        ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")

        # Pre-handover voice quality
        tun_a = _get_tun_for_ue(ue_a)
        tun_b = _get_tun_for_ue(ue_b)
        stats_pre_a, stats_pre_b = RtpStreamStats(), RtpStreamStats()
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            pool.submit(send_rtp_stream, ip_a, ip_b, 20000, duration, 20000, stats_pre_a, tun_a, "audio")
            f = pool.submit(send_rtp_stream, ip_b, ip_a, 20000, duration, 20001, stats_pre_b, tun_b, "audio")
            f.result()

        pre_jitter = max(stats_pre_a.jitter_ms, stats_pre_b.jitter_ms)
        pre_loss = max(stats_pre_a.loss_pct, stats_pre_b.loss_pct)
        pre_mos = ImsVoiceCallQuality._estimate_mos(max(pre_jitter * 4, 10), pre_jitter, pre_loss)
        log.info("Pre-handover VoNR: MOS=%.2f jitter=%.1fms loss=%.1f%%", pre_mos, pre_jitter, pre_loss)

        # Handover UE_A from source to target (UE_B stays on source)
        ok = self._do_handover(ue_a, source_gnb, target_gnb)
        if not ok:
            self.fail_test("VoNR handover failed")
            return self.result

        # Post-handover voice quality (UE_A now on target, UE_B still on source)
        time.sleep(1)
        tun_a_new = _get_tun_for_ue(ue_a)
        stats_post_a, stats_post_b = RtpStreamStats(), RtpStreamStats()
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            pool.submit(send_rtp_stream, ip_a, ip_b, 20000, duration, 20000, stats_post_a, tun_a_new, "audio")
            f = pool.submit(send_rtp_stream, ip_b, ip_a, 20000, duration, 20001, stats_post_b, tun_b, "audio")
            f.result()

        post_jitter = max(stats_post_a.jitter_ms, stats_post_b.jitter_ms)
        post_loss = max(stats_post_a.loss_pct, stats_post_b.loss_pct)
        post_mos = ImsVoiceCallQuality._estimate_mos(max(post_jitter * 4, 10), post_jitter, post_loss)
        log.info("Post-handover VoNR: MOS=%.2f jitter=%.1fms loss=%.1f%%", post_mos, post_jitter, post_loss)

        quality = ("Excellent" if post_mos >= 4.0 else "Good" if post_mos >= 3.5
                   else "Fair" if post_mos >= 3.0 else "Poor" if post_mos >= 2.5 else "Bad")

        self.pass_test(
            ue_a=ue_a.imsi, ue_b=ue_b.imsi,
            source_gnb=source_gnb.gnb_name, target_gnb=target_gnb.gnb_name,
            pre_mos=round(pre_mos, 2), post_mos=round(post_mos, 2),
            pre_jitter_ms=round(pre_jitter, 2), post_jitter_ms=round(post_jitter, 2),
            quality=quality, voice_continuity=post_mos >= 2.5,
        )
        return self.result


class PingPongHandover(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-004",
        title="Ping-pong N2 handover — source → target → source",
        spec="TS 38.413 §8.4.2 + §8.4.4",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("regression", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=45.0,
        description=(
            "Purpose\n"
            "  The return hop exercises a different code path than the\n"
            "  first hop: the AMF context just migrated to the target\n"
            "  must be migrated back, and the source gNB context must\n"
            "  be cleanly rebuilt. Surfaces UE-context cleanup leaks\n"
            "  that only manifest after at least one prior HO (orphan\n"
            "  AMF-UE-NGAP-ID entries, lingering N4 PFCP sessions).\n"
            "\n"
            "Procedure (TS 38.413 §8.4.2 + §8.4.4)\n"
            "  1. _create_two_gnbs(); require_ue().\n"
            "  2. register_ue(ue, source_gnb) + establish_pdu(ue, psi=1).\n"
            "  3. Hop 1: _do_handover(ue, source, target); record\n"
            "     hop1_gnb = ue.gnb.gnb_name.\n"
            "  4. time.sleep(1) settle.\n"
            "  5. Hop 2: _do_handover(ue, target, source) — return path;\n"
            "     record hop2_gnb = ue.gnb.gnb_name.\n"
            "  6. Both hop results aggregated.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  Both hops returned True from _do_handover (HandoverCommand\n"
            "  received + HandoverNotify completed both times).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, hop1_ok, hop1_gnb, hop2_ok, hop2_gnb, pdu_sessions.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The UE config's gnb_name is not consulted\n"
            "  here — the first gNB from _create_two_gnbs is used as\n"
            "  source regardless. Does NOT verify data-plane continuity\n"
            "  on either hop."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        self.require_ue()

        ue = self.ue_pool[0]
        if not self.register_ue(ue, source_gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        # Hop 1: source → target
        log.info("Hop 1: %s → %s", source_gnb.gnb_name, target_gnb.gnb_name)
        ok1 = self._do_handover(ue, source_gnb, target_gnb)
        hop1_gnb = ue.gnb.gnb_name if ue.gnb else "none"

        if not ok1:
            self.fail_test("Hop 1 failed", hop=1)
            return self.result

        time.sleep(1)

        # Hop 2: target → source (return)
        log.info("Hop 2: %s → %s", target_gnb.gnb_name, source_gnb.gnb_name)
        ok2 = self._do_handover(ue, target_gnb, source_gnb)
        hop2_gnb = ue.gnb.gnb_name if ue.gnb else "none"

        if ok1 and ok2:
            self.pass_test(
                ue=ue.imsi,
                hop1_ok=ok1, hop1_gnb=hop1_gnb,
                hop2_ok=ok2, hop2_gnb=hop2_gnb,
                pdu_sessions=list(ue.pdu_sessions.keys()),
            )
        else:
            self.fail_test(
                f"Ping-pong failed: hop1={'OK' if ok1 else 'FAIL'} hop2={'OK' if ok2 else 'FAIL'}",
                hop1_ok=ok1, hop2_ok=ok2,
            )
        return self.result


class MultiUeHandover(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-005",
        title="Multiple UEs sequentially handed over — AMF/SMF bookkeeping at scale",
        spec="TS 38.413 §8.4.2 + §8.4.4",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  AMF/SMF UE-context tables and PFCP session tables behave\n"
            "  differently at N=8 than at N=1: index collisions, lookup\n"
            "  contention, accidental quadratic loops. This is the\n"
            "  small-scale regression guard that hints at issues at\n"
            "  larger N before the full stress suite catches them.\n"
            "\n"
            "Procedure (TS 38.413 §8.4.2 + §8.4.4)\n"
            "  1. _create_two_gnbs(); require_ue().\n"
            "  2. ue_count = min(8, len(ue_pool)).\n"
            "  3. ThreadPoolExecutor(max_workers=ue_count) registers all\n"
            "     UEs on source_gnb in parallel: attach_ue → register →\n"
            "     wait REGISTERED → establish_pdu_session DNN=internet\n"
            "     PSI=1 → poll for ip (15s deadline). Any failure\n"
            "     immediately fail_test()'s.\n"
            "  4. Sequential loop: for each UE call\n"
            "     _do_handover(ue, source, target, timeout=15); collect\n"
            "     {imsi, ok, gnb} per UE.\n"
            "  5. Aggregate passed/failed counts.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — ue_count is computed (min(8, pool size)).\n"
            "\n"
            "Pass criteria\n"
            "  failed == 0 (every UE's _do_handover returned True).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_count, passed, failed, source_gnb, target_gnb, results.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. ≥2 gNB profiles; UE pool must be ≥ 2 (the\n"
            "  test caps at 8). Handovers run SEQUENTIALLY — concurrent\n"
            "  HO is out of scope here (AMF serialises per design)."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        self.require_ue()

        ue_count = min(8, len(self.ue_pool))
        ues = self.ue_pool[:ue_count]

        # Register all UEs on source gNB concurrently
        def _reg_one(ue):
            try:
                source_gnb.attach_ue(ue)
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

        log.info("Registering %d UEs on %s", ue_count, source_gnb.gnb_name)
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {pool.submit(_reg_one, ue): ue for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue, ok = f.result()
                if not ok:
                    self.fail_test(f"UE {ue.imsi} registration failed")
                    return self.result

        # Handover all UEs sequentially (each HO needs AMF coordination)
        ho_results = []
        for ue in ues:
            ok = self._do_handover(ue, source_gnb, target_gnb, timeout=15)
            ho_results.append({"imsi": ue.imsi, "ok": ok,
                               "gnb": ue.gnb.gnb_name if ue.gnb else "none"})
            log.info("HO UE %s: %s → %s", ue.imsi[-3:],
                     "OK" if ok else "FAIL",
                     ue.gnb.gnb_name if ue.gnb else "?")

        passed = sum(1 for r in ho_results if r["ok"])
        failed = ue_count - passed

        if failed == 0:
            self.pass_test(
                ue_count=ue_count, passed=passed, failed=failed,
                source_gnb=source_gnb.gnb_name, target_gnb=target_gnb.gnb_name,
                results=ho_results,
            )
        else:
            self.fail_test(
                f"{failed}/{ue_count} handovers failed",
                passed=passed, failed=failed, results=ho_results,
            )
        return self.result


class HandoverMultiDnn(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-006",
        title="N2 handover with multi-PDU UE (internet + IMS)",
        spec="TS 38.413 §8.4.2 + §8.4.4 + TS 23.502 §4.9.2",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  HandoverRequest carries a list of PDU Session Resources\n"
            "  (TS 38.413 §9.2.1.1). The target must allocate per-session\n"
            "  DRBs and the SMF must run TS 23.502 §4.9.2 step-11 path-\n"
            "  switch on EVERY session. Catches 'first session wins,\n"
            "  others get dropped' regressions where one PDU survives\n"
            "  the HO but a parallel PDU on a different DNN does not.\n"
            "\n"
            "Procedure (TS 38.413 §8.4 + §9.2.1.1 + TS 23.502 §4.9.2)\n"
            "  1. _create_two_gnbs(); require_ue().\n"
            "  2. register_ue(ue, source_gnb).\n"
            "  3. establish_pdu(ue, psi=1, dnn='internet').\n"
            "  4. establish_pdu(ue, psi=2, dnn='ims').\n"
            "  5. Pre: UDP UL 1M for 5s from internet IP → derive_gateway;\n"
            "     pre_ok = stats.throughput_kbps > 0.\n"
            "  6. _do_handover(ue, source, target).\n"
            "  7. 0.5s settle; same UDP UL 1M for 5s on internet IP;\n"
            "     post_kbps recorded.\n"
            "  8. both_survived = 1 in pdu_sessions AND 2 in pdu_sessions.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  Handover succeeded (_do_handover returned True). pass_test\n"
            "  is unconditional once HO succeeds — both_sessions_survived\n"
            "  and post_data_ok are reported but do NOT drive fail_test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, source_gnb, target_gnb, pre_data_ok, post_data_ok,\n"
            "  post_kbps, pdu_sessions_after, both_sessions_survived.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. IMS DNN must be provisioned. HOLLOW-PASS\n"
            "  shape: only HO success drives fail; PSI-2 silently dropped\n"
            "  by the target would show in both_sessions_survived=False\n"
            "  but the test would still PASS — operator must inspect."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        self.require_ue()

        ue = self.ue_pool[0]
        if not self.register_ue(ue, source_gnb):
            return self.result

        # Establish dual PDU sessions
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="ims"):
            return self.result

        inet_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        ims_ip = ue.pdu_sessions.get(2, {}).get("ip", "unknown")
        log.info("UE %s: internet=%s ims=%s on %s", ue.imsi, inet_ip, ims_ip, source_gnb.gnb_name)

        server = derive_gateway(inet_ip)
        engine = TrafficEngine.get()

        # Pre-handover: verify internet session works
        pre_session = engine.create_session(
            src_ip=inet_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="1M", duration=5, direction="ul",
        )
        pre_session.start()
        pre_stats = pre_session.stop()
        pre_ok = pre_stats.throughput_kbps > 0

        # Handover with both PDU sessions
        ok = self._do_handover(ue, source_gnb, target_gnb)
        if not ok:
            self.fail_test("Multi-DNN handover failed")
            return self.result

        # Post-handover: verify internet session still works
        time.sleep(0.5)
        post_session = engine.create_session(
            src_ip=inet_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="1M", duration=5, direction="ul",
        )
        post_session.start()
        post_stats = post_session.stop()
        post_ok = post_stats.throughput_kbps > 0
        post_kbps = round(post_stats.throughput_kbps, 1)

        pdu_sessions_after = list(ue.pdu_sessions.keys())
        both_survived = 1 in pdu_sessions_after and 2 in pdu_sessions_after

        self.pass_test(
            ue=ue.imsi,
            source_gnb=source_gnb.gnb_name, target_gnb=target_gnb.gnb_name,
            pre_data_ok=pre_ok, post_data_ok=post_ok,
            post_kbps=post_kbps,
            pdu_sessions_after=pdu_sessions_after,
            both_sessions_survived=both_survived,
        )
        return self.result


class MultiHopHandover(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-007",
        title="N-hop N2 handover — repeated migration between gNBs",
        spec="TS 38.413 §8.4.2 + §8.4.4",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("regression", "stress"),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        description=(
            "Purpose\n"
            "  Latency drift across many hops surfaces O(n) bookkeeping\n"
            "  (AMF UE-context linked-list scans, NGAP socket\n"
            "  fragmentation, gNB context-table rehashing). Each hop\n"
            "  should take roughly the same wall time; if hop_5 is 4×\n"
            "  hop_1, something is growing. Replays the known production-\n"
            "  capture pattern handover2.pcapng.\n"
            "\n"
            "Procedure (TS 38.413 §8.4.2 + §8.4.4)\n"
            "  1. _create_two_gnbs(); require_ue(params.get('imsi')).\n"
            "  2. hops = params.get('hops', 5); gap_s = 1.0.\n"
            "  3. _get_gnb_for_ue() picks initial source from UE config;\n"
            "     other gNB is the peer (a, b).\n"
            "  4. register_ue + establish_pdu psi=1.\n"
            "  5. Loop i in 1..hops: t0=time.time();\n"
            "     ok = _do_handover(ue, a, b, timeout=15);\n"
            "     ms = (time.time()-t0)*1000; append result; a,b = b,a;\n"
            "     time.sleep(gap_s) if not last hop. Any hop fail →\n"
            "     fail_test with results so far.\n"
            "  6. Compute avg/min/max ms across all hops.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi      — UE to drive (default: first in pool).\n"
            "  hops      — number of hops (default: 5).\n"
            "  hop_gap_s — pause between hops in s (default: 1.0).\n"
            "\n"
            "Pass criteria\n"
            "  Every hop's _do_handover returned True (no early exit\n"
            "  from the loop).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, source_gnb, target_gnb, hops, avg_ho_ms, max_ho_ms,\n"
            "  min_ho_ms, final_gnb, results (per-hop dict list).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Latency assertion is informational only —\n"
            "  no upper bound on avg_ho_ms / max_ho_ms is enforced."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        ue = self.require_ue(self.params.get("imsi"))
        hops = int(self.params.get("hops", 5))
        gap_s = float(self.params.get("hop_gap_s", 1.0))

        initial_gnb = self._get_gnb_for_ue(ue, self._gnb_map)
        if not initial_gnb:
            self.fail_test(f"UE {ue.imsi}: gnb_name not configured in UE Config. "
                           f"Set gNB for this UE (available: {list(self._gnb_map.keys())})")
            return self.result
        peer_gnb = target_gnb if initial_gnb.gnb_name == source_gnb.gnb_name else source_gnb
        source_gnb, target_gnb = initial_gnb, peer_gnb

        if not self.register_ue(ue, source_gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        sess = ue.pdu_sessions.get(1, {})
        log.info("UE %s registered on %s — PDU IP=%s, starting %d hops",
                 ue.imsi, source_gnb.gnb_name, sess.get('ip', '?'), hops)

        results = []
        a, b = source_gnb, target_gnb
        for i in range(1, hops + 1):
            log.info("Hop %d/%d: %s → %s", i, hops, a.gnb_name, b.gnb_name)
            t0 = time.time()
            ok = self._do_handover(ue, a, b, timeout=15)
            ms = round((time.time() - t0) * 1000)
            results.append({"hop": i, "src": a.gnb_name, "dst": b.gnb_name,
                            "ok": ok, "ms": ms})
            if not ok:
                self.fail_test(f"Hop {i} ({a.gnb_name} → {b.gnb_name}) failed",
                               ue=ue.imsi, hops_done=i - 1, results=results)
                return self.result
            a, b = b, a
            if i < hops:
                time.sleep(gap_s)

        avg_ms = round(sum(r["ms"] for r in results) / len(results))
        self.pass_test(
            ue=ue.imsi,
            source_gnb=source_gnb.gnb_name,
            target_gnb=target_gnb.gnb_name,
            hops=hops,
            avg_ho_ms=avg_ms,
            max_ho_ms=max(r["ms"] for r in results),
            min_ho_ms=min(r["ms"] for r in results),
            final_gnb=ue.gnb.gnb_name if ue.gnb else "none",
            results=results,
        )
        return self.result


class HandoverFailureTC(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-008",
        title="HandoverFailure path — target rejects, source recovers",
        spec="TS 38.413 §8.4.2.4 + §9.2.3.5 + §9.2.3.6",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Verify the negative-path bookkeeping (TS 38.413 §8.4.2.4\n"
            "  Unsuccessful Operation): when the target rejects with\n"
            "  HandoverFailure (§9.2.3.5), the AMF must surface\n"
            "  HandoverPreparationFailure (§9.2.3.6) to the source, AND\n"
            "  the source's UE context must remain intact (no premature\n"
            "  cleanup of N1/N2 state).\n"
            "\n"
            "Procedure (TS 38.413 §8.4.2.4 + §9.2.3.5 + §9.2.3.6)\n"
            "  1. _create_two_gnbs(); require_ue(params.get('imsi')).\n"
            "  2. cause = params.get('cause', 'ho-failure-in-target-5GC-\n"
            "     ngran-node-or-target-system').\n"
            "  3. _get_gnb_for_ue picks source from UE config gnb_name.\n"
            "  4. register_ue + establish_pdu psi=1.\n"
            "  5. target_gnb.force_ho_failure = cause (FSM inject).\n"
            "  6. try: _do_handover(ue, source, target, timeout=10)\n"
            "     finally: target_gnb.force_ho_failure = None.\n"
            "  7. Read source_gnb._ho_prep_failure and check ue.gnb is\n"
            "     still source.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi  — UE to drive (default: first in pool).\n"
            "  cause — NGAP cause string injected at target (default:\n"
            "          'ho-failure-in-target-5GC-ngran-node-or-target-\n"
            "          system').\n"
            "\n"
            "Pass criteria\n"
            "  not ho_ok AND _ho_prep_failure is non-None AND ue.gnb is\n"
            "  source_gnb (all three must hold).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, source_gnb, target_gnb, injected_cause, received_cause,\n"
            "  ue_remained_on_source.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Uses the gNB FSM force_ho_failure injection\n"
            "  hook — a test-only path, not real-RAN behaviour."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        ue = self.require_ue(self.params.get("imsi"))
        cause = self.params.get("cause", "ho-failure-in-target-5GC-ngran-node-or-target-system")

        initial_gnb = self._get_gnb_for_ue(ue, self._gnb_map)
        if not initial_gnb:
            self.fail_test(f"UE {ue.imsi}: gnb_name not configured")
            return self.result
        peer = target_gnb if initial_gnb.gnb_name == source_gnb.gnb_name else source_gnb
        source_gnb, target_gnb = initial_gnb, peer

        if not self.register_ue(ue, source_gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        target_gnb.force_ho_failure = cause
        try:
            ho_ok = self._do_handover(ue, source_gnb, target_gnb, timeout=10)
        finally:
            target_gnb.force_ho_failure = None

        prep_failure = source_gnb._ho_prep_failure
        ue_still_on_source = (ue.gnb is source_gnb)

        if (not ho_ok) and prep_failure is not None and ue_still_on_source:
            self.pass_test(
                ue=ue.imsi,
                source_gnb=source_gnb.gnb_name,
                target_gnb=target_gnb.gnb_name,
                injected_cause=cause,
                received_cause=str(prep_failure),
                ue_remained_on_source=True,
            )
        else:
            self.fail_test(
                "Failure path did not surface as expected",
                ho_ok=ho_ok,
                prep_failure=str(prep_failure),
                ue_still_on_source=ue_still_on_source,
            )
        return self.result


class HandoverCancelTC(HandoverBase):
    SPEC = TestSpec(
        tc_id="TC-HO-009",
        title="HandoverCancel — source aborts before HandoverCommand",
        spec="TS 38.413 §8.4.5 + §9.2.3.7 + §9.2.3.8",
        domain=Domain.HANDOVER,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  HandoverCancel (TS 38.413 §9.2.3.7) is the source-side\n"
            "  abort: the source decides mid-prep that the HO should not\n"
            "  proceed. AMF must respond with HandoverCancelAcknowledge\n"
            "  (§9.2.3.8) AND tear down any target-side context that was\n"
            "  already allocated. UE must remain on source.\n"
            "\n"
            "Procedure (TS 38.413 §8.4.5 + §9.2.3.7 + §9.2.3.8)\n"
            "  1. _create_two_gnbs(); require_ue(params.get('imsi')).\n"
            "  2. cancel_cause = params.get('cancel_cause',\n"
            "     'handover-cancelled').\n"
            "  3. _get_gnb_for_ue picks source from UE config gnb_name.\n"
            "  4. register_ue + establish_pdu psi=1.\n"
            "  5. source_gnb.initiate_handover(ue, target_gnb) — sends\n"
            "     HandoverRequired but DOES NOT wait for HandoverCommand.\n"
            "  6. time.sleep(0.2) — short window before HandoverCommand.\n"
            "  7. source_gnb.cancel_handover(ue, cause_value=cancel_cause,\n"
            "     timeout=5) → expects HandoverCancelAcknowledge.\n"
            "  8. Check ue.gnb is still source.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi         — UE to drive (default: first in pool).\n"
            "  cancel_cause — NGAP cause (default: 'handover-cancelled').\n"
            "\n"
            "Pass criteria\n"
            "  ack (from cancel_handover) is True AND ue.gnb is still\n"
            "  source_gnb. Both must hold.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, source_gnb, target_gnb, cancel_cause, cancel_ack,\n"
            "  ue_remained_on_source.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Timing-sensitive — the 200 ms window may\n"
            "  race: if HandoverCommand arrives before cancel_handover\n"
            "  sends, the test fails for the wrong reason."
        ),
    )

    def run(self):
        source_gnb, target_gnb = self._create_two_gnbs()
        ue = self.require_ue(self.params.get("imsi"))
        cancel_cause = self.params.get("cancel_cause", "handover-cancelled")

        initial_gnb = self._get_gnb_for_ue(ue, self._gnb_map)
        if not initial_gnb:
            self.fail_test(f"UE {ue.imsi}: gnb_name not configured")
            return self.result
        peer = target_gnb if initial_gnb.gnb_name == source_gnb.gnb_name else source_gnb
        source_gnb, target_gnb = initial_gnb, peer

        if not self.register_ue(ue, source_gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        if not source_gnb.initiate_handover(ue, target_gnb):
            self.fail_test("HandoverRequired send failed")
            return self.result

        # Don't wait for HandoverCommand — cancel mid-prep.
        time.sleep(0.2)
        ack = source_gnb.cancel_handover(ue, cause_value=cancel_cause, timeout=5)

        ue_still_on_source = (ue.gnb is source_gnb)
        if ack and ue_still_on_source:
            self.pass_test(
                ue=ue.imsi,
                source_gnb=source_gnb.gnb_name,
                target_gnb=target_gnb.gnb_name,
                cancel_cause=cancel_cause,
                cancel_ack=True,
                ue_remained_on_source=True,
            )
        else:
            self.fail_test(
                "HandoverCancel flow did not complete as expected",
                cancel_ack=ack,
                ue_still_on_source=ue_still_on_source,
            )
        return self.result


ALL_HANDOVER_TCS = [
    BasicHandover, HandoverWithData, HandoverVoNR,
    PingPongHandover, MultiUeHandover, HandoverMultiDnn,
    MultiHopHandover, HandoverFailureTC, HandoverCancelTC,
]
