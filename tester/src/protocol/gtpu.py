# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""GTP-U data plane — TUN interfaces + UDP:2152 tunnelling.  TS 29.281.

Architecture:
    App <-> TUN (tun-ue-IMSI) <-> GtpuManager <-> UDP:2152 <-> UPF
"""

import os
import struct
import socket
import fcntl
import logging
import queue
import threading
import subprocess

log = logging.getLogger("tester.gtpu")

# Linux TUN/TAP constants
TUNSETIFF = 0x400454ca
IFF_TUN = 0x0001
IFF_NO_PI = 0x1000

# GTP-U constants (TS 29.281)
GTPU_PORT = 2152
GTPU_FLAGS_BASIC = 0x30  # version=1, PT=1 (no extension headers)
GTPU_FLAGS_EXT = 0x34    # version=1, PT=1, E=1 (extension header present)
GTPU_MSG_TPDU = 0xFF     # T-PDU message type
GTPU_HDR_LEN = 8
# TS 38.415 §5.5.2.1: PDU Session Container extension header
GTPU_EXT_PDU_SESSION = 0x85  # Next Extension Header Type


def _classify_packet(ip_packet, qos_rules, default_qfi=1):
    """Classify an IP packet against QoS rules to determine QFI.

    Per TS 24.501 §9.11.4.13: UE matches outgoing packets against packet
    filters in QoS rules, ordered by precedence (lower = higher priority).
    The first matching rule's QFI is used. If no rule matches, use default QFI.

    Packet filter components (TS 24.501 §9.11.4.13):
      - match_all: matches any packet
      - protocol: IP protocol number (6=TCP, 17=UDP)
      - local_port / local_port_min..max: source port
      - remote_port / remote_port_min..max: destination port
      - remote_ip: destination IP
      - local_ip: source IP
    """
    if not qos_rules or len(ip_packet) < 20:
        return default_qfi

    # Parse IP header
    version = (ip_packet[0] >> 4) & 0x0F
    if version != 4:
        return default_qfi

    protocol = ip_packet[9]
    src_ip = f"{ip_packet[12]}.{ip_packet[13]}.{ip_packet[14]}.{ip_packet[15]}"
    dst_ip = f"{ip_packet[16]}.{ip_packet[17]}.{ip_packet[18]}.{ip_packet[19]}"

    # Parse ports for TCP/UDP
    src_port = dst_port = 0
    if protocol in (6, 17) and len(ip_packet) >= 24:  # TCP or UDP
        ihl = (ip_packet[0] & 0x0F) * 4
        if len(ip_packet) >= ihl + 4:
            src_port = (ip_packet[ihl] << 8) | ip_packet[ihl + 1]
            dst_port = (ip_packet[ihl + 2] << 8) | ip_packet[ihl + 3]

    # Match against ALL rules sorted by precedence (lower = higher priority)
    # Per TS 24.501 §5.4.4: packet must match a QoS rule to be sent
    sorted_rules = sorted(qos_rules, key=lambda r: r.get('precedence', 255))

    for rule in sorted_rules:
        for pf in rule.get('filters', []):
            content = pf.get('content', {})
            if _match_filter(content, protocol, src_ip, dst_ip, src_port, dst_port):
                return rule.get('qfi', default_qfi)

    # No rule matched — DROP per TS 24.501 §5.4.4
    # The default QoS rule (QFI=1, match-all) from PDU Session Establishment Accept
    # should always match. If we get here with rules present, something is wrong.
    if qos_rules:
        return -1  # DROP: no matching QoS rule
    return default_qfi  # no rules at all — use default (non-strict)


def _match_filter(content, protocol, src_ip, dst_ip, src_port, dst_port):
    """Match a single packet filter against packet fields."""
    if content.get('match_all'):
        return True

    # Protocol match
    if 'protocol' in content:
        if content['protocol'] != protocol:
            return False

    # Remote IP match
    if 'remote_ip' in content:
        if content['remote_ip'] not in ('0.0.0.0', dst_ip):
            return False

    # Local IP match
    if 'local_ip' in content:
        if content['local_ip'] not in ('0.0.0.0', src_ip):
            return False

    # Source port (local port for UL)
    if 'local_port' in content:
        if src_port != content['local_port']:
            return False
    if 'local_port_min' in content and 'local_port_max' in content:
        if not (content['local_port_min'] <= src_port <= content['local_port_max']):
            return False

    # Destination port (remote port for UL)
    if 'remote_port' in content:
        if dst_port != content['remote_port']:
            return False
    if 'remote_port_min' in content and 'remote_port_max' in content:
        if not (content['remote_port_min'] <= dst_port <= content['remote_port_max']):
            return False

    return True


def _build_gtpu_with_pdu_session_container(teid, payload, qfi, is_uplink=True):
    """Build GTP-U packet with PDU Session Container extension header.

    Per TS 38.415 §5.5.2.1: mandatory for 5G NR GTP-U.
    Per TS 29.281 §5.2: extension header chain format.

    GTP-U header (8 bytes):
      flags = 0x34 (V=1, PT=1, E=1 extension header flag)
      type = 0xFF (T-PDU)
      length = payload + extension header size
      TEID

    Extension header fields (4 bytes after base header):
      Sequence Number = 0x0000 (2 bytes, present when E/S/PN set)
      N-PDU Number = 0x00 (1 byte)
      Next Extension Header Type = 0x85 (PDU Session Container)

    PDU Session Container (4 bytes, TS 38.415 §5.5.3):
      Length = 1 (in 4-byte units = 4 bytes total)
      PDU Type (4 bits) + QFI (6 bits) + spare
      Next Extension Header Type = 0x00 (no more extensions)

    PDU Type: 0 = DL PDU SESSION INFORMATION, 1 = UL PDU SESSION INFORMATION
    """
    # PDU Session Container content
    pdu_type = 1 if is_uplink else 0  # UL=1, DL=0
    # Byte: [PDU Type (4 bits)][spare (2 bits)][QFI MSB (2 bits)]
    # Next byte: [QFI LSB (4 bits)][spare (4 bits)]
    # Simplified: type_qfi_byte = (pdu_type << 4) | ((qfi >> 2) & 0x03)
    # But per TS 38.415 §5.5.3.1/5.5.3.2 the layout is:
    #   Octet 1: PDU Type (4 bits) | QFI (6 bits) split across 2 bytes
    # Actually the format is:
    #   Octet 1: [PDU Type: 4 bits][QFI: 6 bits spread...] →
    #   Per TS 38.415 Figure 5.5.3.1-1 (DL) / 5.5.3.2-1 (UL):
    #     Octet 1: PDU Type (4 bits) | spare (4 bits) for DL
    #              PDU Type (4 bits) | DL Sending Time Stamp Repeated (1) | spare (1) | QFI (6 bits split)
    # Per TS 38.415 §5.5.3.2 Table 5.5.3.2-1 (UL PDU SESSION INFORMATION):
    #   Octet 1: PDU Type (4 bits) | spare (4 bits)
    #   Octet 2: spare (2 bits) | QFI (6 bits)
    byte1 = (pdu_type << 4)  # PDU Type + spare
    byte2 = (qfi & 0x3F)  # spare (2 bits = 0) | QFI (6 bits)

    pdu_session_ext = struct.pack('BBB',
        1,           # Extension header length in 4-byte units (=4 bytes)
        byte1,       # PDU Type + QFI high bits
        byte2,       # QFI low bits + spare
    ) + b'\x00'      # Next extension header type = 0 (no more)

    # Extension header fields (seq, npdu, next_ext_type)
    ext_fields = struct.pack('!HBB',
        0x0000,              # Sequence Number
        0x00,                # N-PDU Number
        GTPU_EXT_PDU_SESSION # Next Extension Header Type = 0x85
    )

    # Total payload length includes extension fields + PDU session container
    total_len = len(ext_fields) + len(pdu_session_ext) + len(payload)

    # GTP-U base header with E flag set
    gtpu_hdr = struct.pack('!BBHI',
        GTPU_FLAGS_EXT, GTPU_MSG_TPDU, total_len, teid)

    return gtpu_hdr + ext_fields + pdu_session_ext + payload


def _cleanup_stale_tun_interfaces():
    """Remove any leftover tun-ue-* interfaces from previous runs (netlink)."""
    try:
        from src.protocol.netlink import get_ops
        ops = get_ops()
        for iface in ops.list_tun_interfaces(prefix="tun-ue-"):
            log.info("Cleaning up stale interface %s", iface)
            ops.delete_link(iface)
    except Exception as e:
        log.debug("Stale TUN cleanup: %s", e)


class GtpuManager:
    """Manage GTP-U tunnels with TUN interfaces and UDP transport."""

    # Class-level singleton — lets any module reach the live manager without
    # reaching back through src.app (which would re-execute src.app on
    # re-import and double-run banner, DB init, test registry, etc.).
    _default = None

    @classmethod
    def get_default(cls):
        """Return the first GtpuManager constructed this process (or None)."""
        return cls._default

    def __init__(self, gnb_ip="0.0.0.0", udp_port=GTPU_PORT):
        self._tunnels = {}       # local_teid -> tunnel info dict
        self._tun_to_teid = {}   # tun_fd -> local_teid
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self.available = False
        self._udp_sock = None
        self._gnb_ip = gnb_ip
        self._udp_port = udp_port

        # Bounded pool of workers for tunnel setup. History of this
        # counter:
        #  - Pre-Phase-1:  no pool, every PSR Setup spawned a thread
        #                  that forked 10+ `ip`/`sysctl` subprocesses
        #                  → kernel SCTP recv buffer filled → ABORT.
        #  - Phase 1-2c:   single serialized worker — safe, but became
        #                  the PSR-Response-latency bottleneck at ~60-80
        #                  ms per UE × 55 UEs = ~4 s queue latency.
        #                  AMF's PSR-Response timer dropped the tail.
        #  - Now:          small pool (4). Each create_tunnel is mostly
        #                  netlink (~5 ms). The one subprocess we still
        #                  fork is the `ip route` fallback for the
        #                  pyroute2 EOPNOTSUPP bug — one per UE, not the
        #                  10+ of the legacy path. Four concurrent forks
        #                  is a non-issue; bringing down worst-case
        #                  latency from ~4 s to ~1 s should reclaim the
        #                  failures caused by AMF response-timeout.
        # Size is overridable via MMT_GTPU_SETUP_WORKERS env var so
        # operators can dial it up on bigger runs without a code change.
        self._setup_queue = queue.Queue()
        try:
            pool_size = max(1, int(os.environ.get("MMT_GTPU_SETUP_WORKERS", "4")))
        except ValueError:
            pool_size = 4
        self._setup_workers = []
        for i in range(pool_size):
            t = threading.Thread(
                target=self._tunnel_setup_worker, daemon=True,
                name=f"gtpu-setup-{i}")
            t.start()
            self._setup_workers.append(t)

        # Register as the process-wide default on first construction so
        # other modules can reach this instance without re-importing src.app.
        if GtpuManager._default is None:
            GtpuManager._default = self

        # Clean up stale interfaces
        _cleanup_stale_tun_interfaces()

        # Try to bind UDP socket
        try:
            self._udp_sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            self._udp_sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            self._udp_sock.bind((self._gnb_ip, self._udp_port))
            self._udp_sock.settimeout(1.0)
            self.available = True
            log.info("GTP-U UDP socket bound to %s:%d", self._gnb_ip, self._udp_port)
        except PermissionError:
            log.warning("GTP-U: cannot bind UDP port %d (no permission)", self._udp_port)
            self._udp_sock = None
            return
        except OSError as e:
            log.warning("GTP-U: cannot bind UDP port %d: %s", self._udp_port, e)
            self._udp_sock = None
            return

        # Test TUN creation privilege
        try:
            fd = os.open('/dev/net/tun', os.O_RDWR)
            os.close(fd)
        except PermissionError:
            log.warning("GTP-U: no CAP_NET_ADMIN — TUN creation disabled")
            self.available = False
            return
        except OSError as e:
            log.warning("GTP-U: /dev/net/tun not available: %s", e)
            self.available = False
            return

        # Start UDP receiver thread
        self._rx_thread = threading.Thread(
            target=self._udp_receiver, daemon=True, name="gtpu-udp-rx")
        self._rx_thread.start()

    # ─── TUN helpers ───

    def _create_tun(self, tun_name):
        """Create a TUN interface, return fd or raise."""
        fd = os.open('/dev/net/tun', os.O_RDWR)
        # struct ifreq: 16 bytes name + 2 bytes flags + padding
        ifr = struct.pack('16sH', tun_name.encode('utf-8'), IFF_TUN | IFF_NO_PI)
        fcntl.ioctl(fd, TUNSETIFF, ifr)
        return fd

    _rt_table_counter = 100

    def _configure_tun(self, tun_name, ue_ip):
        """Configure TUN with per-UE policy routing (netlink, no subprocess).

        When multiple UE TUNs exist on the same host, the kernel treats
        all UE IPs as 'local' and short-circuits UE-to-UE traffic without
        going through GTP-U. We use `ip rule from <ue_ip>` to force source-
        based routing through the per-UE TUN.

        Phase 1 of ARCHITECTURE.md: everything here goes through rtnetlink
        via pyroute2. Per-UE setup takes sub-millisecond instead of the
        ~100 ms we paid when forking 10 `ip`/`sysctl` calls — which was
        what starved the SCTP recv thread under load.
        """
        from src.protocol.netlink import get_ops
        ops = get_ops()

        GtpuManager._rt_table_counter += 1
        rt_table = GtpuManager._rt_table_counter

        # Pre-clean any stale `ip rule from <ue_ip>` left behind when the
        # previous run crashed and _destroy_tun never got to tidy up. Without
        # this the add below would EEXIST and UL iperf3 would later die with
        # "Cannot assign requested address". rule_del_from_src loops internally.
        ops.rule_del_from_src(ue_ip)

        # Defensive flush of the per-UE table — harmless if already empty.
        ops.route_flush_table(rt_table)

        # Order: disable IPv6 (skip NDP/link-local through GTP-U), set MTU +
        # state UP, add the UE address, add a default route in the per-UE
        # table, then install the policy rule that directs `from <ue_ip>` to
        # that table. Each call is sub-millisecond netlink — no forks.
        ops.disable_ipv6(tun_name)
        ops.up_tun(tun_name, mtu=1400)
        ops.addr_add(tun_name, ue_ip, prefix=32)
        ops.route_add_default_dev(tun_name, rt_table)
        ops.rule_add_from_src(ue_ip, rt_table, priority=rt_table)

        log.info("TUN %s configured: UE_IP=%s/32 MTU=1400 table=%d",
                 tun_name, ue_ip, rt_table)

    def _destroy_tun(self, tun_name, tun_fd, ue_ip=None):
        """Close fd, delete interface, clean up policy routing rules (netlink)."""
        if tun_fd >= 0:
            try:
                os.close(tun_fd)
            except OSError:
                pass
        from src.protocol.netlink import get_ops
        ops = get_ops()
        # Remove all policy routing rules for this UE IP (stacked duplicates).
        if ue_ip:
            ops.rule_del_from_src(ue_ip)
        ops.delete_link(tun_name)

    # ─── Serialized setup worker ───

    def submit_setup(self, fn, *args, **kwargs):
        """Queue a tunnel-setup callable to run on the single worker thread.

        Keeps every `ip`/`sysctl` subprocess fork off the SCTP recv thread
        AND prevents a storm of concurrent workers from starving the recv
        thread. Returns immediately; the callable runs asynchronously.
        """
        self._setup_queue.put((fn, args, kwargs))

    def cancel_pending_setups(self) -> int:
        """Drop every queued setup job. Returns count discarded.

        Called by GnbStateMachine.disconnect() so we don't burn CPU
        creating ~N TUNs whose AMF context is gone (and which we'd
        immediately tear down anyway). Already-running jobs finish.
        """
        dropped = 0
        while True:
            try:
                self._setup_queue.get_nowait()
                dropped += 1
            except queue.Empty:
                break
        if dropped:
            log.info("GTP-U: dropped %d pending tunnel-setup jobs after disconnect", dropped)
        return dropped

    def _tunnel_setup_worker(self):
        """Consume setup requests from the queue one at a time."""
        while not self._stop.is_set():
            try:
                item = self._setup_queue.get(timeout=1.0)
            except Exception:
                continue
            if item is None:
                break
            fn, args, kwargs = item
            try:
                fn(*args, **kwargs)
            except Exception as e:
                log.error("tunnel-setup worker: %s failed: %s",
                          getattr(fn, "__name__", str(fn)), e, exc_info=True)

    # ─── Tunnel lifecycle ───

    def create_tunnel(self, imsi, ue_ip, local_teid, remote_teid, upf_ip, qos_rules=None):
        """Create a GTP-U tunnel with TUN interface.

        Returns tun_name on success, None on failure.
        """
        if not self.available:
            log.warning("GTP-U manager unavailable (no root/CAP_NET_ADMIN or UDP bind failed) "
                        "— UE IP %s will NOT be on any interface; iperf3 -B %s will fail",
                        ue_ip, ue_ip)
            return None

        # Build TUN name: tun-ue-XXXXXX (last 6 digits of IMSI, fits 15 char limit)
        suffix = imsi[-6:] if len(imsi) >= 6 else imsi
        tun_name = f"tun-ue-{suffix}"

        # Destroy any existing tunnel for this IMSI (stale from previous session)
        with self._lock:
            stale = [t for t, info in self._tunnels.items() if info['imsi'] == imsi]
        for old_teid in stale:
            log.info("GTP-U: destroying stale tunnel for IMSI=%s TEID=0x%08X", imsi, old_teid)
            self.destroy_tunnel(old_teid)

        try:
            tun_fd = self._create_tun(tun_name)
            self._configure_tun(tun_name, ue_ip)
        except PermissionError:
            log.warning("GTP-U: no permission to create TUN %s", tun_name)
            return None
        except OSError as e:
            # TUN might still exist from a crash — try deleting and retrying
            log.debug("GTP-U: TUN %s busy, cleaning up and retrying", tun_name)
            self._destroy_tun(tun_name, -1, ue_ip)
            try:
                tun_fd = self._create_tun(tun_name)
                self._configure_tun(tun_name, ue_ip)
            except OSError as e2:
                log.warning("GTP-U: TUN creation failed for %s: %s", tun_name, e2)
                return None

        tunnel = {
            'imsi': imsi,
            'ue_ip': ue_ip,
            'local_teid': local_teid,
            'remote_teid': remote_teid,
            'upf_ip': upf_ip,
            'qos_rules': qos_rules or [],  # [{qfi, dqr, precedence, filters}]
            'default_qfi': 1,  # fallback if no filter matches
            'tun_name': tun_name,
            'tun_fd': tun_fd,
            'tun_reader': None,
            'stats': {
                'tx_packets': 0, 'rx_packets': 0,
                'tx_bytes': 0, 'rx_bytes': 0,
            },
        }

        with self._lock:
            self._tunnels[local_teid] = tunnel
            self._tun_to_teid[tun_fd] = local_teid

        # Start TUN reader thread
        reader = threading.Thread(
            target=self._tun_reader, args=(local_teid,),
            daemon=True, name=f"gtpu-tun-{suffix}")
        tunnel['tun_reader'] = reader
        reader.start()

        log.info("GTP-U tunnel created: IMSI=%s UE-IP=%s TEID=0x%08X->0x%08X UPF=%s TUN=%s",
                 imsi, ue_ip, local_teid, remote_teid, upf_ip, tun_name)
        return tun_name

    def destroy_tunnel(self, local_teid):
        """Remove a GTP-U tunnel and its TUN interface."""
        with self._lock:
            tunnel = self._tunnels.pop(local_teid, None)
            if tunnel:
                self._tun_to_teid.pop(tunnel['tun_fd'], None)

        if tunnel is None:
            return

        self._destroy_tun(tunnel['tun_name'], tunnel['tun_fd'], tunnel.get('ue_ip'))
        log.info("GTP-U tunnel destroyed: TEID=0x%08X TUN=%s", local_teid, tunnel['tun_name'])

    def shutdown(self):
        """Destroy all tunnels and close the UDP socket."""
        self._stop.set()
        with self._lock:
            teids = list(self._tunnels.keys())
        for teid in teids:
            self.destroy_tunnel(teid)
        if self._udp_sock:
            try:
                self._udp_sock.close()
            except OSError:
                pass
        log.info("GTP-U manager shut down")

    def update_qos_rules(self, local_teid, qos_rules):
        """Update QoS rules for a tunnel (called after PDU Session Modification)."""
        with self._lock:
            tunnel = self._tunnels.get(local_teid)
            if tunnel:
                tunnel['qos_rules'] = qos_rules
                # Find the default QFI (DQR=1)
                for rule in qos_rules:
                    if rule.get('dqr'):
                        tunnel['default_qfi'] = rule.get('qfi', 1)
                        break
                log.info("GTP-U QoS rules updated for TEID 0x%08X: %d rules",
                         local_teid, len(qos_rules))

    def get_tun_for_ip(self, ue_ip):
        """Get the TUN device name for a UE IP address."""
        with self._lock:
            for t in self._tunnels.values():
                if t['ue_ip'] == ue_ip:
                    return t['tun_name']
        return None

    def get_tunnels(self):
        """Return list of active tunnel info dicts for REST API."""
        with self._lock:
            result = []
            for teid, t in self._tunnels.items():
                result.append({
                    'imsi': t['imsi'],
                    'ue_ip': t['ue_ip'],
                    'local_teid': t['local_teid'],
                    'remote_teid': t['remote_teid'],
                    'upf_ip': t['upf_ip'],
                    'tun_name': t['tun_name'],
                    'stats': dict(t['stats']),
                })
            return result

    # ─── Data plane threads ───

    def _udp_receiver(self):
        """Receive GTP-U packets from UPF, strip header, write to TUN."""
        log.info("GTP-U UDP receiver started")
        while not self._stop.is_set():
            try:
                data, addr = self._udp_sock.recvfrom(65536)
            except socket.timeout:
                continue
            except OSError:
                if self._stop.is_set():
                    break
                continue

            if len(data) < GTPU_HDR_LEN:
                continue

            # Parse GTP-U header
            flags, msg_type, length, teid = struct.unpack('!BBHI', data[:GTPU_HDR_LEN])
            if msg_type != GTPU_MSG_TPDU:
                continue

            payload = data[GTPU_HDR_LEN:]

            with self._lock:
                tunnel = self._tunnels.get(teid)

            if tunnel is None:
                continue

            try:
                # GTP-U extension header handling (TS 29.281 §5.2)
                # flags byte: bit2=E(ext hdr), bit1=S(seq num), bit0=PN(N-PDU)
                offset = GTPU_HDR_LEN
                if flags & 0x07:
                    # Seq(2) + N-PDU(1) + Next-Ext-Type(1) = 4 bytes
                    if len(data) < offset + 4:
                        continue
                    next_ext = data[offset + 3]
                    offset += 4
                    # Walk extension header chain
                    while next_ext != 0 and offset < len(data):
                        ext_len = data[offset]  # length in 4-byte units including len+next
                        if ext_len == 0:
                            break
                        ext_size = ext_len * 4
                        if offset + ext_size > len(data):
                            break
                        next_ext = data[offset + ext_size - 1]
                        offset += ext_size
                actual_payload = data[offset:]
                if not actual_payload:
                    continue
                os.write(tunnel['tun_fd'], actual_payload)
                tunnel['stats']['rx_packets'] += 1
                tunnel['stats']['rx_bytes'] += len(actual_payload)
            except OSError as e:
                if actual_payload:
                    log.debug("GTP-U: TUN write error TEID 0x%08X: %s (payload %d bytes, first byte 0x%02X, flags 0x%02X)",
                              teid, e, len(actual_payload), actual_payload[0], flags)
                else:
                    log.debug("GTP-U: TUN write error TEID 0x%08X: %s", teid, e)

        log.info("GTP-U UDP receiver stopped")

    def _tun_reader(self, local_teid):
        """Read IP packets from TUN, wrap in GTP-U, send to UPF."""
        with self._lock:
            tunnel = self._tunnels.get(local_teid)
        if tunnel is None:
            return

        tun_fd = tunnel['tun_fd']
        remote_teid = tunnel['remote_teid']
        upf_ip = tunnel['upf_ip']
        tun_name = tunnel['tun_name']

        log.info("GTP-U TUN reader started for %s (TEID 0x%08X)", tun_name, local_teid)

        while not self._stop.is_set():
            try:
                # Use select with timeout to allow clean shutdown
                import select
                readable, _, _ = select.select([tun_fd], [], [], 1.0)
                if not readable:
                    continue
                ip_packet = os.read(tun_fd, 65536)
            except OSError:
                if self._stop.is_set():
                    break
                # fd was closed (tunnel destroyed)
                break

            if not ip_packet:
                continue

            # Drop non-IPv4 packets (PDU session type is IPv4)
            # IPv6 NDP/RS packets would be dropped by UPF anyway
            if ip_packet[0] >> 4 != 4:
                continue

            # Classify packet → QFI by matching against QoS rules (TS 24.501 §9.11.4.13)
            qfi = _classify_packet(ip_packet, tunnel.get('qos_rules', []),
                                   tunnel.get('default_qfi', 1))
            # Per TS 24.501 §5.4.4: drop packets that don't match any QoS rule
            if qfi < 0:
                tunnel['stats'].setdefault('dropped', 0)
                tunnel['stats']['dropped'] += 1
                continue
            # Build GTP-U with PDU Session Container (TS 38.415 §5.5.2.1)
            gtpu_pkt = _build_gtpu_with_pdu_session_container(
                remote_teid, ip_packet, qfi, is_uplink=True)
            try:
                self._udp_sock.sendto(gtpu_pkt, (upf_ip, GTPU_PORT))
                tunnel['stats']['tx_packets'] += 1
                tunnel['stats']['tx_bytes'] += len(ip_packet)
            except OSError as e:
                log.debug("GTP-U: UDP send error to %s: %s", upf_ip, e)

        log.info("GTP-U TUN reader stopped for %s", tun_name)
