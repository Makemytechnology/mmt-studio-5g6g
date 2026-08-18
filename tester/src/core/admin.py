# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Core Admin — manage SA Core state via REST API.

Flush UE contexts, clear sessions, reset IP pools, check NF status.
Provides soft-restart equivalent without SSH/sudo access.
"""

import logging

from src.core.api import core_api as _core_api

log = logging.getLogger("tester.core_admin")


# ── NF Status ──

def get_nf_status():
    """Get all NF running status (AMF, SMF, UPF, IMS, etc.)."""
    return _core_api("/api/admin/nf-status")


def get_sys_info():
    """Get core system info."""
    return _core_api("/api/admin/sys-info")


def get_db_stats():
    """Get core DB statistics (UE count, session count, etc.)."""
    return _core_api("/api/admin/db-stats")


def export_db():
    """Export full core DB as JSON."""
    return _core_api("/api/admin/export-db")


# ── Soft Restart (flush without process restart) ──

def flush_ue_contexts():
    """Flush all active UE contexts on AMF.

    Equivalent to a soft restart — clears all NGAP UE associations.
    UEs will need to re-register.
    """
    result = _core_api("/api/admin/flush-ue-contexts", "POST")
    if result:
        log.info("Flushed UE contexts: %s", result)
    return result


def clear_pdu_sessions():
    """Clear all PDU sessions on SMF/UPF."""
    result = _core_api("/api/admin/clear-pdu-sessions", "POST")
    if result:
        log.info("Cleared PDU sessions: %s", result)
    return result


def clear_ims_registrations():
    """Clear all IMS registrations on P-CSCF/S-CSCF."""
    result = _core_api("/api/admin/clear-ims-registrations", "POST")
    if result:
        log.info("Cleared IMS registrations: %s", result)
    return result


def reset_ip_pools():
    """Reset UE IP address pools on UPF."""
    result = _core_api("/api/admin/reset-ip-pools", "POST")
    if result:
        log.info("Reset IP pools: %s", result)
    return result


def flush_xfrm():
    """Flush IPsec XFRM state (if used for NAS security)."""
    result = _core_api("/api/admin/flush-xfrm", "POST")
    if result:
        log.info("Flushed XFRM: %s", result)
    return result


def soft_restart():
    """Soft restart — flush everything without restarting processes.

    Equivalent to restarting sa_core but without killing SCTP/NGAP server.
    Clears: UE contexts, PDU sessions, IMS registrations, IP pools.
    """
    log.info("Core soft restart — flushing all state")
    results = {}
    results["ue_contexts"] = flush_ue_contexts()
    results["pdu_sessions"] = clear_pdu_sessions()
    results["ims_registrations"] = clear_ims_registrations()
    results["ip_pools"] = reset_ip_pools()
    log.info("Core soft restart complete: %s",
             {k: "OK" if v else "FAIL" for k, v in results.items()})
    return results


def is_core_ready():
    """Check if core is running and all NFs are up."""
    status = get_nf_status()
    if not status:
        return False
    nfs = status.get("nfs", [])
    return all(nf.get("running") for nf in nfs)


# ── Hard reset (cornerstone test isolation primitive) ──

def _restart_satraffic_via_docker():
    """Restart the satraffic container via the docker socket.

    Why: satraffic uses `network_mode: service:sacore` to share sa_core's
    netns (so iperf3 client/server can reach UE IPs through sa_core's UPF).
    When sa_core exits + restarts (via /api/admin/remove-db-file),
    the netns is destroyed and recreated, but satraffic's process keeps
    running with stale socket file descriptors that no longer bind to
    anything reachable. We must restart satraffic to rebind.

    Uses Python stdlib (http.client over AF_UNIX) — no curl / docker CLI /
    docker SDK needed in the tester image. /var/run/docker.sock is mounted
    by docker-compose.yml.
    """
    import http.client, socket as _socket
    sock_path = "/var/run/docker.sock"
    if not _os_path_exists(sock_path):
        log.warning("Core: %s not mounted — cannot restart satraffic", sock_path)
        return False

    class _UDSConn(http.client.HTTPConnection):
        def connect(self):
            s = _socket.socket(_socket.AF_UNIX, _socket.SOCK_STREAM)
            s.settimeout(self.timeout)
            s.connect(sock_path)
            self.sock = s

    try:
        conn = _UDSConn("localhost", timeout=10)
        conn.request("POST", "/containers/satraffic/restart?t=2")
        resp = conn.getresponse()
        resp.read()  # drain
        conn.close()
        if resp.status in (204, 304):
            log.info("Core: satraffic restart issued (docker API: %d)", resp.status)
            return True
        log.warning("Core: satraffic restart via docker socket returned %d", resp.status)
    except Exception as e:
        log.warning("Core: satraffic restart raised: %s", e)
    return False


def _os_path_exists(p):
    import os
    return os.path.exists(p)


def _wait_satraffic_healthy(timeout_s: float = 30.0, poll_interval_s: float = 0.5):
    """Poll the satraffic agent on http://172.30.0.10:9100 until reachable.

    satraffic exposes a FastAPI on :9100 inside sa_core's netns; from
    the tester's netns we reach it via the bridge IP (172.30.0.10 maps
    to satraffic-in-sacore-netns because of network_mode: service:sacore).

    Uses urllib (stdlib) — no extra deps.
    """
    import time, urllib.request, urllib.error
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            req = urllib.request.Request("http://172.30.0.10:9100/", method="GET")
            with urllib.request.urlopen(req, timeout=2) as r:
                # 200 = root handler exists. Either way, reaching here
                # proves the agent is bound on :9100.
                _ = r.read()
                return True
        except urllib.error.HTTPError as he:
            # 404/405 from the agent's FastAPI also prove it's listening.
            if he.code in (404, 405):
                return True
        except Exception:
            pass
        time.sleep(poll_interval_s)
    return False


def _wait_for_fresh_boot(pre_boot_id: str, timeout_s: float = 60.0,
                         poll_interval_s: float = 2.0) -> bool:
    """Poll /api/admin/sys-info until we see a process with a different
    boot_id AND ready=true. Used after any endpoint that exits the
    process (restart, reset-to-baseline).

    boot_id is a per-process random hex (sa_core webservice/app/boot_id.go)
    that guarantees a different value across docker restart-policy
    restarts (which reuse the same container, so hostname is unchanged).

    The HTTP server comes up early (after route registration, before
    NF init) so boot_id flips ~2 s after exit; UPF DPDK EAL init alone
    can add 10-15 s before ready=true. Waiting on both prevents tests
    from racing the NF init.

    Returns True on success, False on timeout.
    """
    import time
    deadline = time.time() + timeout_s
    saw_boot_change = False
    last_announce = 0.0
    while time.time() < deadline:
        try:
            cur = _core_api("/api/admin/sys-info", quiet=True)
            if cur and cur.get("boot_id") and cur.get("boot_id") != pre_boot_id:
                if not saw_boot_change:
                    log.info("Core: fresh process detected (boot_id=%s); waiting for ready=true",
                             cur.get("boot_id"))
                    saw_boot_change = True
                if cur.get("ready") is True:
                    log.info("Core: sa_core ready (boot_id=%s, all NFs initialized)",
                             cur.get("boot_id"))
                    return True
        except Exception:
            pass
        now = time.time()
        if now - last_announce > 5.0:
            log.info("Core: waiting for sa_core (%s)",
                     "ready=false" if saw_boot_change else "no fresh boot_id yet")
            last_announce = now
        time.sleep(poll_interval_s)
    log.error("Core: sa_core did not become ready within %.1fs", timeout_s)
    return False


def restart_core(timeout_s: float = 60.0, poll_interval_s: float = 2.0) -> bool:
    """Restart sa_core in-place — process exits, docker brings it back,
    DB content is preserved.

    Use this from a test that mutates boot-only fields and needs to
    verify post-restart behavior:
      * network_config.amf_ip / sctp_port (NGAP listener bind)
      * supported_plmns AMF region/set/pointer (GUAMI in NG Setup Response)
      * DPDK / UPF dataplane config
      * Anything else read once in amf.Start / smf.Start / upf.Start

    Distinct from reset_to_baseline() which ALSO wipes the DB.

    Returns True when the new process is up + ready, False on timeout.
    """
    pre = get_sys_info()
    pre_boot = (pre or {}).get("boot_id")
    log.info("Core: requesting restart (DB preserved); pre boot_id=%s", pre_boot)

    # Fire-and-forget — response races the process exit.
    _core_api("/api/admin/restart", "POST")

    if not _wait_for_fresh_boot(pre_boot, timeout_s, poll_interval_s):
        return False

    # NGAP / PFCP listeners just rebound; netns is unchanged (process
    # restart inside the same container). satraffic shares sacore's
    # netns via `network_mode: service:sacore` so its bound sockets
    # are tied to the SAME netns instance — they survive the in-place
    # process restart. No satraffic restart needed.
    return True


def reset_to_baseline(timeout_s: float = 60.0, poll_interval_s: float = 2.0):
    """Hard-reset the core to a tester-owned baseline state.

    Full Inversion flow (tester is the single source of truth for runtime
    DB state — core's SeedAll runs only on virgin cold boots, never as
    part of test execution):

      1. POST /api/admin/restart — exits the sa_core process so every
         in-memory NF state (NGAP UE contexts, gNB associations, SMF
         PDU sessions, PFCP associations, IMS SIP state) dies cleanly.
         Docker `restart: unless-stopped` brings it back; DB file is
         PRESERVED so the cold-boot SeedAll path does NOT fire. We
         used to call /api/admin/remove-db-file here, which also
         deleted the DB file → wasted ~7-8 s on a SeedAll plant that
         the next step would immediately wipe.
      2. Poll /api/admin/sys-info for boot_id change AND ready=true.
      3. POST /api/admin/drop-db-data — wipe whatever rows the previous
         test left behind (file kept; reloads UDM/SMF/AMF caches).
      4. provisioner.sync_all() — push tester/config/baseline.yaml:
         PLMN, NSSAI catalog (with correct SDs per SST), per-PLMN NSSAI,
         TACs, APNs, 128 UEs (auth + AMBR + subscription tree).
      5. Restart satraffic — its socket FDs were bound to the old netns
         and need re-binding after sa_core's process restart.

    Returns True on success, False if any step fails.
    """
    import time
    log.info("Core: requesting reset-to-baseline (process will restart, DB preserved)")

    # Capture the current process boot_id before triggering reset.
    # boot_id is a per-process random hex (sa_core webservice/app/boot_id.go)
    # that GUARANTEES a different value across docker restart-policy
    # restarts (which reuse the same container, so hostname is unchanged).
    pre = get_sys_info()
    pre_boot = (pre or {}).get("boot_id")
    if pre_boot:
        log.debug("Core: pre-reset boot_id=%s", pre_boot)

    # Fire-and-forget — the response may or may not arrive cleanly because
    # the process is exiting. We don't need the body, only the side effect.
    # /api/admin/restart preserves the DB file → no cold-boot SeedAll →
    # step 3 below lands on whatever rows the previous test left, which
    # drop-db-data + sync_all will replace deterministically.
    _core_api("/api/admin/restart", "POST")

    if not _wait_for_fresh_boot(pre_boot, timeout_s, poll_interval_s):
        return False

    # ── Full Inversion: tester pushes its own configuration ──────────
    # Process restart flushed every in-memory NF state. The DB file
    # still carries whatever rows existed before reset (previous test's
    # state, or the initial seed on the very first run). Wipe and
    # replace with tester/config/baseline.yaml so PLMN/NSSAI/APN/UEs
    # match exactly what the test cases expect.
    drop_res = _core_api("/api/admin/drop-db-data", "POST")
    if not (drop_res and drop_res.get("ok")):
        log.error("Core: drop-db failed after restart: %s", drop_res)
        return False
    log.info("Core: drop-db OK — schema recreated; pushing tester baseline.yaml")

    try:
        from src.core.provisioner import sync_all as _sync_all
        summary = _sync_all()
        log.info("Core: tester baseline pushed — %s", summary)
    except Exception as e:
        log.error("Core: sync_all from tester baseline failed: %s", e)
        return False

    # sa_core's netns was recreated; satraffic's process kept running but
    # its iperf3-related socket bindings are stale. Restart it.
    if _restart_satraffic_via_docker():
        if _wait_satraffic_healthy(timeout_s=30.0):
            time.sleep(0.5)
            log.info("Core: reset complete — sa_core + satraffic both healthy")
            return True
        log.error("Core: satraffic did not become reachable after restart")
        return False
    # Best-effort fallback: even without restarting satraffic, sa_core
    # is up — but the test will likely fail iperf. Return True anyway so
    # the test runs and reports a real failure rather than a setup error.
    time.sleep(0.5)
    log.warning("Core: reset partially complete — sa_core back; satraffic NOT restarted")
    return True
