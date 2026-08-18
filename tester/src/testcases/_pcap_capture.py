# Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
"""Per-test packet capture for the satester runner.

Architecture
------------
Each test case in TestRunner.run_test is wrapped with a PcapCapture
that spawns `tcpdump` as a subprocess for the test's lifetime. The
subprocess writes a pcap to ACTIVE_PCAP_PATH while the test runs --
that path is the source for the live-streaming HTTP endpoint
(/api/tests/active/pcap.stream). When the test ends, the active pcap
is moved to RUNS_DIR/<run_id>.pcap as the permanent artefact.

Why in-process tcpdump (vs the old docker-sidecar capture)
----------------------------------------------------------
The previous design ran a separate privileged netshoot container that
joined sacore's netns, and a Windows-side watcher polled /api/tests
to decide when to open Wireshark. Three races killed it:

  1. Container spin-up (~1-2 s) plus Wireshark startup (~2 s) was
     SLOWER than a typical 510 ms test, so the capture wasn't ready
     when the test fired its SCTP packets.
  2. Status transitions (RUNNING -> PASS) raced the poll loop -- the
     watcher often saw PASS before observing RUNNING.
  3. Sacore restart from pretest_mode=full tore down the netns the
     capture container was joined to, killing tcpdump mid-test.

Capturing inside the satester container instead means the test code
itself controls start/stop with millisecond precision and a tcpdump
that's already running when the SCTP exchange begins. All three
races disappear.

Requirements
------------
  * The satester image must have tcpdump installed (added in
    tester/build/Dockerfile alongside libsctp-dev / iperf3 / etc).
  * The satester container must have CAP_NET_RAW + CAP_NET_ADMIN
    (already granted in orchestrate/docker-compose.yml's satester
    `cap_add:` block for SCTP and TUN setup).
  * The interface to capture is `eth0` inside the container's netns
    -- that's the mmtnet bridge port through which all SCTP/NGAP/
    HTTP between satester and sacore flows.

Pcap format
-----------
tcpdump's default output is classic pcap (LINK_TYPE_EN10MB, little-
endian magic 0xa1b2c3d4). Wireshark auto-detects via magic, so the
.pcap extension is honest. `-U` (packet-buffered) is critical for the
streaming endpoint: without it tcpdump fills a 4 MiB stdout buffer
before flushing, so the live stream would lag by up to thousands of
packets.

NOT captured by this design
---------------------------
  * Loopback traffic inside sacore (PFCP at UDP/8805 between the
    in-process SMF and UPF) is invisible from satester's netns.
  * Traffic between sacore and a remote-only peer (split-host
    role=tester deploys without a colocated sacore) would need a
    capture target override; for now we capture satester's eth0
    only because that's where the runner lives.
"""
import logging
import os
import shutil
import subprocess
import threading
import time

log = logging.getLogger("tester.pcap")

# Where tcpdump writes the LIVE pcap during a test run. Fixed path
# so the HTTP streaming endpoint (/api/tests/active/pcap.stream)
# knows where to tail without coordination beyond "the file exists
# iff a test is running". /tmp because it's tmpfs in most Docker
# setups -- writes don't go to disk, just RAM, so tcpdump can flush
# packet-by-packet without I/O latency on the hot path.
ACTIVE_PCAP_PATH = "/tmp/mmt-active.pcap"

# Final resting place for each completed test's pcap. Lives alongside
# the JSON report (data/test_results/report_*.json) so an operator
# downloading a report can find its packet capture without guessing.
RUNS_DIR = os.path.abspath(
    os.path.join(os.path.dirname(__file__), "..", "..", "data", "test_results")
)

# Default capture interface. eth0 is the mmtnet-side veth in the
# satester container -- that's where SCTP/NGAP to sacore, all UE/gNB
# control plane, and HTTP between satester and sacore all flow.
# NOT `any`: libpcap silently drops SCTP on the LINUX_SLL2 DLT that
# `-i any` selects (this bit run_studio.sh's wireshark launcher too;
# its comment calls out the same bug).
DEFAULT_IFACE = "eth0"


def _slugify(name: str) -> str:
    """Make `name` filesystem-safe so it survives as part of a pcap
    filename. We only sanitise the bare minimum -- test names are
    already mostly snake_case identifiers, so we just strip path
    separators and shell-meta characters."""
    bad = '<>:"/\\|?*\0 \t'
    return "".join("_" if c in bad else c for c in name)


class PcapCapture:
    """Wraps `tcpdump` as a subprocess for one test's lifetime.

    Usage:
        cap = PcapCapture(run_id="20260520_081530_tc_ngs_001")
        cap.start()
        try:
            run_the_test()
        finally:
            cap.stop()
        # cap.final_path now points at the saved pcap.
    """

    def __init__(self, run_id, iface=DEFAULT_IFACE):
        self.run_id = run_id
        self.iface = iface
        self.active_path = ACTIVE_PCAP_PATH
        self.final_path = os.path.join(RUNS_DIR, f"{_slugify(run_id)}.pcap")
        self._proc = None
        self._lock = threading.Lock()

    def start(self):
        """Spawn tcpdump writing to ACTIVE_PCAP_PATH. Blocks briefly
        (up to ~500 ms) until the pcap global header is on disk --
        otherwise a streaming-endpoint consumer that hits the file
        immediately would get EOF before any packet record exists."""
        with self._lock:
            if self._proc is not None:
                log.warning("PcapCapture: start() called twice; ignoring")
                return

            # Remove any pcap left by a previous test so the streaming
            # endpoint can use file-exists as the "test is active"
            # signal without false positives.
            try:
                os.unlink(self.active_path)
            except FileNotFoundError:
                pass

            # -U: packet-buffered (flush per packet). Without this
            #     tcpdump fills a 4 MiB stdout buffer before flushing
            #     and the streaming endpoint stalls behind the buffer.
            # -i: capture interface; eth0 is mmtnet-side in satester.
            # -w: write to the active path (consumed by the streamer).
            # snaplen default 262144 is fine -- captures full packets.
            #
            # stdout/stderr go to DEVNULL: tcpdump's startup banner
            # ("listening on eth0, link-type EN10MB ...") would
            # otherwise spam satester's web log every test.
            try:
                self._proc = subprocess.Popen(
                    ["tcpdump", "-i", self.iface, "-U", "-w", self.active_path],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
            except FileNotFoundError:
                log.warning(
                    "PcapCapture: tcpdump not in PATH; "
                    "skipping capture for run_id=%s", self.run_id,
                )
                self._proc = None
                return
            except Exception as e:
                log.warning("PcapCapture: failed to start tcpdump: %s", e)
                self._proc = None
                return

            # Wait for tcpdump to write the 24-byte pcap global header
            # so a streaming consumer doesn't see an empty file. 500 ms
            # is plenty (tcpdump opens the file before its first read).
            deadline = time.time() + 0.5
            while time.time() < deadline:
                try:
                    if os.path.getsize(self.active_path) >= 24:
                        return
                except FileNotFoundError:
                    pass
                time.sleep(0.02)
            log.debug(
                "PcapCapture: pcap header not yet on disk after 500 ms "
                "(run_id=%s); proceeding anyway", self.run_id,
            )

    def stop(self):
        """SIGTERM tcpdump, wait briefly for it to flush, then move
        the active pcap to its final per-run path. Idempotent --
        calling stop() twice is a no-op."""
        with self._lock:
            if self._proc is None:
                return
            try:
                self._proc.terminate()
                try:
                    self._proc.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    # tcpdump usually exits in <100 ms on SIGTERM;
                    # if not, force-kill so we don't leak processes.
                    self._proc.kill()
                    self._proc.wait(timeout=1)
            except Exception as e:
                log.warning("PcapCapture: error stopping tcpdump: %s", e)
            self._proc = None

        # Move active -> final. `os.replace` is atomic on POSIX so a
        # streaming consumer that's mid-read either sees the whole
        # active file or its move-target; not a torn read.
        try:
            os.makedirs(RUNS_DIR, exist_ok=True)
            os.replace(self.active_path, self.final_path)
        except FileNotFoundError:
            # tcpdump never created the file (e.g. start() failed).
            # Nothing to move; final_path simply won't exist.
            pass
        except Exception as e:
            log.warning(
                "PcapCapture: failed to move %s -> %s: %s",
                self.active_path, self.final_path, e,
            )

    def __enter__(self):
        self.start()
        return self

    def __exit__(self, *exc_info):
        self.stop()


def is_active():
    """Used by the streaming endpoint to decide whether to keep
    holding the connection open after the file stops growing."""
    try:
        return os.path.exists(ACTIVE_PCAP_PATH)
    except Exception:
        return False
