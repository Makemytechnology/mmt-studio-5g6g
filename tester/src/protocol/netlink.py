# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Netlink helpers — replaces every `ip`/`sysctl` subprocess we used to fork.

Phase 1 of ARCHITECTURE.md. Per-UE TUN provisioning used to cost ~100 ms
(fork + exec + `ip` tool parse for each of 10 commands); rtnetlink via
pyroute2 cuts that to sub-millisecond. Matters enormously at 10k UE
concurrency, where serialized forking starved the SCTP recv thread long
enough that the AMF ABORTed the association.

Public API:
    NetlinkOps.up_tun(tun_name, mtu=1400)
    NetlinkOps.down_tun(tun_name)
    NetlinkOps.delete_link(tun_name)
    NetlinkOps.addr_add(tun_name, addr, prefix=32)
    NetlinkOps.addr_flush(tun_name)
    NetlinkOps.route_flush_table(table_id)
    NetlinkOps.route_add_default_dev(tun_name, table_id)
    NetlinkOps.rule_add_from_src(src_ip, table_id, priority)
    NetlinkOps.rule_del_from_src(src_ip)            — best-effort, loops until None
    NetlinkOps.disable_ipv6(tun_name)               — kept as /proc/sys write
    NetlinkOps.list_tun_interfaces(prefix='tun-ue-')

All operations are thread-safe (pyroute2's IPRoute has an internal lock)
but we still keep an explicit lock for batch flows where partial-failure
visibility matters.
"""

from __future__ import annotations

import logging
import os
import socket
import struct
import subprocess
import threading
from typing import Iterable, List, Optional

# Raw netlink constants for RTM_NEWROUTE (bypass the pyroute2 filter
# chain for device-only default routes that pyroute2 0.9.6 keeps
# returning EOPNOTSUPP on).
_NETLINK_ROUTE   = 0
_RTM_NEWROUTE    = 24
_NLM_F_REQUEST   = 0x01
_NLM_F_ACK       = 0x04
_NLM_F_EXCL      = 0x200
_NLM_F_CREATE    = 0x400
_NLMSG_ERROR     = 2
_AF_INET         = 2
_RTPROT_BOOT     = 3
_RT_SCOPE_LINK   = 253
_RTN_UNICAST     = 1
_RTA_OIF         = 4
_RTA_TABLE       = 15


def _route_add_default_dev_raw(ifindex: int, table_id: int) -> None:
    """Send exactly the RTM_NEWROUTE bytes `ip route add default dev X
    table N` uses. Opens a short-lived AF_NETLINK/SOCK_RAW socket,
    sends the request, reads the ACK. Raises OSError on kernel reject.

    Why raw instead of pyroute2: pyroute2 0.9.6's filter chain injects
    defaults (type='unicast', proto='static') plus a strict_check on
    some kernels that reject the combination with EOPNOTSUPP. The
    wire-level netlink message we send here matches iproute2's
    `ip` command byte-for-byte — the kernel has no reason to reject it.
    """
    sock = socket.socket(socket.AF_NETLINK, socket.SOCK_RAW, _NETLINK_ROUTE)
    try:
        sock.bind((0, 0))  # pid=0 → kernel assigns
        # rtmsg — 12 bytes. Use the in-header 'table' byte if the id fits
        # in a byte, otherwise set it to RT_TABLE_UNSPEC(0) and attach
        # RTA_TABLE for the full 32-bit id.
        tbl_byte = table_id if table_id <= 255 else 0
        rtmsg = struct.pack(
            'BBBBBBBBI',
            _AF_INET,        # family
            0,               # dst_len (0 = default route)
            0,               # src_len
            0,               # tos
            tbl_byte,        # table (header)
            _RTPROT_BOOT,    # protocol
            _RT_SCOPE_LINK,  # scope
            _RTN_UNICAST,    # type
            0,               # flags
        )
        # RTA_OIF — 8 bytes
        rta_oif = struct.pack('=HHI', 8, _RTA_OIF, ifindex)
        # RTA_TABLE — 8 bytes. Always include; kernel accepts it even
        # when the header 'table' byte is also set. Makes the call work
        # uniformly for small AND large table ids.
        rta_table = struct.pack('=HHI', 8, _RTA_TABLE, table_id)
        payload = rtmsg + rta_oif + rta_table
        total_len = 16 + len(payload)
        hdr = struct.pack(
            '=IHHII',
            total_len,
            _RTM_NEWROUTE,
            _NLM_F_REQUEST | _NLM_F_CREATE | _NLM_F_EXCL | _NLM_F_ACK,
            1,  # seq
            0,  # pid → kernel sees our bound address as source
        )
        sock.send(hdr + payload)
        # ACK arrives as NLMSG_ERROR with errno=0 on success, negative
        # errno on failure. Kernel ACKs are always at least
        # sizeof(nlmsghdr)+sizeof(nlmsgerr) = 20 bytes.
        resp = sock.recv(4096)
        if len(resp) < 20:
            raise OSError(0, f"short netlink response ({len(resp)} bytes)")
        resp_type = struct.unpack('=H', resp[4:6])[0]
        if resp_type == _NLMSG_ERROR:
            neg_errno = struct.unpack('=i', resp[16:20])[0]
            errno_val = -neg_errno
            if errno_val != 0:
                raise OSError(errno_val, os.strerror(errno_val))
    finally:
        sock.close()

log = logging.getLogger("tester.netlink")

try:
    from pyroute2 import IPRoute
    from pyroute2.netlink.exceptions import NetlinkError
    _HAVE_PYROUTE2 = True
except Exception as e:  # pragma: no cover — only on broken installs
    _HAVE_PYROUTE2 = False
    _IMPORT_ERR = str(e)
    log.warning("pyroute2 not available (%s) — falling back to subprocess", e)


# Netlink errno constants we care about.
_EEXIST = 17
_ENOENT = 2
_ESRCH = 3
_ENODEV = 19


class NetlinkOps:
    """Thread-safe wrapper over pyroute2.IPRoute for the operations we need.

    One IPRoute socket is cheap (a single AF_NETLINK fd + small buffers),
    so we keep a module-level singleton rather than opening per call.
    Every operation returns None on success and logs a warning on failure,
    matching the old subprocess(check=False) behaviour — callers that cared
    about success already inspect the resulting system state.
    """

    _instance: Optional["NetlinkOps"] = None
    _instance_lock = threading.Lock()

    @classmethod
    def get(cls) -> "NetlinkOps":
        with cls._instance_lock:
            if cls._instance is None:
                cls._instance = cls()
            return cls._instance

    @classmethod
    def available(cls) -> bool:
        return _HAVE_PYROUTE2

    def __init__(self):
        if not _HAVE_PYROUTE2:
            raise RuntimeError(
                f"pyroute2 not available: {_IMPORT_ERR}. "
                "Run ./install.sh to install it into the venv.")
        # One IPRoute instance, shared. pyroute2 serializes netlink I/O on
        # an internal lock; we wrap it so our higher-level batches (ip del
        # loops, route flush + add) stay atomic from a logging perspective.
        self._ipr = IPRoute()
        self._op_lock = threading.Lock()

    # ── Link-level ────────────────────────────────────────────────────

    def _idx(self, ifname: str) -> Optional[int]:
        try:
            rows = self._ipr.link_lookup(ifname=ifname)
        except Exception as e:
            log.debug("link_lookup(%s) failed: %s", ifname, e)
            return None
        return rows[0] if rows else None

    def up_tun(self, tun_name: str, mtu: int = 1400) -> None:
        """Set MTU + state UP in a single netlink tx (replaces 2 subprocess calls)."""
        with self._op_lock:
            idx = self._idx(tun_name)
            if idx is None:
                log.warning("netlink up_tun: %s not found", tun_name)
                return
            try:
                self._ipr.link("set", index=idx, mtu=mtu, state="up")
            except NetlinkError as e:
                log.warning("netlink up_tun(%s): %s", tun_name, e)

    def down_tun(self, tun_name: str) -> None:
        with self._op_lock:
            idx = self._idx(tun_name)
            if idx is None:
                return
            try:
                self._ipr.link("set", index=idx, state="down")
            except NetlinkError as e:
                log.debug("netlink down_tun(%s): %s", tun_name, e)

    def delete_link(self, tun_name: str) -> None:
        with self._op_lock:
            idx = self._idx(tun_name)
            if idx is None:
                return
            try:
                self._ipr.link("del", index=idx)
            except NetlinkError as e:
                if e.code == _ENODEV:
                    return  # already gone
                log.warning("netlink delete_link(%s): %s", tun_name, e)

    def list_tun_interfaces(self, prefix: str = "tun-ue-") -> List[str]:
        """Return names of all links whose name starts with `prefix`."""
        names = []
        try:
            for link in self._ipr.get_links():
                n = link.get_attr("IFLA_IFNAME") or ""
                if n.startswith(prefix):
                    names.append(n)
        except Exception as e:
            log.debug("netlink list_tun_interfaces: %s", e)
        return names

    # ── Addresses ─────────────────────────────────────────────────────

    def addr_add(self, tun_name: str, addr: str, prefix: int = 32) -> None:
        with self._op_lock:
            idx = self._idx(tun_name)
            if idx is None:
                log.warning("netlink addr_add: %s not found", tun_name)
                return
            try:
                self._ipr.addr("add", index=idx, address=addr, prefixlen=prefix)
            except NetlinkError as e:
                if e.code == _EEXIST:
                    return  # already assigned
                log.warning("netlink addr_add %s/%d on %s: %s",
                            addr, prefix, tun_name, e)

    def addr_flush(self, tun_name: str) -> None:
        """Remove all addresses from a TUN — convenience for cleanup."""
        with self._op_lock:
            idx = self._idx(tun_name)
            if idx is None:
                return
            try:
                for addr in self._ipr.get_addr(index=idx):
                    ip = addr.get_attr("IFA_ADDRESS")
                    if ip:
                        try:
                            self._ipr.addr("del", index=idx, address=ip,
                                           prefixlen=addr["prefixlen"])
                        except NetlinkError:
                            pass
            except Exception as e:
                log.debug("netlink addr_flush(%s): %s", tun_name, e)

    # ── Routes ────────────────────────────────────────────────────────

    def route_add_default_dev(self, tun_name: str, table_id: int) -> None:
        """`ip route add default dev <tun> table <id>`.

        Fast path: raw RTM_NEWROUTE via AF_NETLINK, matching iproute2's
        wire bytes exactly. Sub-millisecond, no fork. On old or quirky
        kernels that somehow reject even the raw form, fall back to
        `ip route` subprocess (kept as belt-and-suspenders; unused on
        any kernel we've seen).

        Previously we tried pyroute2's high-level route() first, but its
        filter chain injects defaults that kernel 6.8 rejects with
        EOPNOTSUPP (errno 95). Every run fell through to the
        `ip route` subprocess fallback at ~50 ms per UE — the
        dominant term in tunnel-setup latency. The raw-netlink path
        below makes tunnel creation ~80× faster.
        """
        with self._op_lock:
            idx = self._idx(tun_name)
            if idx is None:
                raise RuntimeError(
                    f"route_add_default_dev: interface {tun_name} not found")
        # Raw netlink (outside op_lock — kernel serializes route adds
        # itself, and each socket open is independent state).
        try:
            _route_add_default_dev_raw(idx, table_id)
            return
        except OSError as e:
            if e.errno == _EEXIST:
                return
            log.warning("raw netlink route add failed (errno=%d %s) — "
                        "falling back to `ip route` for table=%d dev=%s",
                        e.errno, e.strerror, table_id, tun_name)
        # Paranoid last-resort subprocess fallback. Only fires if raw
        # netlink rejected us — should never happen on a Linux kernel
        # iproute2 supports.
        cmd = ["ip", "route", "add", "default", "dev", tun_name,
               "table", str(table_id)]
        rc = subprocess.run(cmd, capture_output=True, text=True)
        if rc.returncode == 0:
            return
        if "File exists" in (rc.stderr or ""):
            return
        raise RuntimeError(
            f"route_add_default_dev table={table_id} dev={tun_name} failed: "
            f"{(rc.stderr or rc.stdout or '').strip()}")

    def route_flush_table(self, table_id: int) -> None:
        with self._op_lock:
            try:
                for rt in self._ipr.get_routes(table=table_id):
                    try:
                        self._ipr.route("del", table=table_id,
                                        dst=rt.get_attr("RTA_DST") or "default",
                                        oif=rt.get_attr("RTA_OIF"))
                    except NetlinkError:
                        pass
            except Exception as e:
                log.debug("netlink route_flush_table(%d): %s", table_id, e)

    # ── Rules (policy routing) ────────────────────────────────────────

    def rule_add_from_src(self, src_ip: str, table_id: int,
                          priority: Optional[int] = None) -> None:
        """`ip rule add from <src> lookup <table> [priority <p>]`."""
        with self._op_lock:
            kwargs = {"src": src_ip, "table": table_id, "action": "FR_ACT_TO_TBL"}
            if priority is not None:
                kwargs["priority"] = priority
            try:
                self._ipr.rule("add", **kwargs)
            except NetlinkError as e:
                if e.code == _EEXIST:
                    return
                log.warning("netlink rule_add_from_src src=%s table=%d: %s",
                            src_ip, table_id, e)

    def rule_del_from_src(self, src_ip: str, max_iterations: int = 16) -> int:
        """Delete every rule with matching `from <src>`. Returns count removed.

        Loops because duplicate rules stack on `ip rule add`; a single
        delete removes only one instance. 16 is a generous upper bound.
        """
        removed = 0
        with self._op_lock:
            for _ in range(max_iterations):
                try:
                    # pyroute2 doesn't have a get_rules(src=) filter in every
                    # version, so we delete-by-attrs and catch ENOENT.
                    self._ipr.rule("del", src=src_ip, action="FR_ACT_TO_TBL")
                    removed += 1
                except NetlinkError as e:
                    if e.code in (_ENOENT, _ESRCH):
                        break
                    log.debug("netlink rule_del_from_src(%s): %s", src_ip, e)
                    break
                except Exception as e:
                    log.debug("netlink rule_del_from_src(%s): %s", src_ip, e)
                    break
        return removed

    # ── sysctl (no netlink path, use /proc) ───────────────────────────

    @staticmethod
    def disable_ipv6(tun_name: str) -> None:
        """Write /proc/sys/net/ipv6/conf/<tun>/disable_ipv6 = 1.

        Not netlink, but no subprocess fork either — direct file write is
        faster and cleaner than `sysctl -w ...`.
        """
        path = f"/proc/sys/net/ipv6/conf/{tun_name}/disable_ipv6"
        try:
            with open(path, "w") as f:
                f.write("1\n")
        except FileNotFoundError:
            pass  # interface may be already gone, or IPv6 disabled globally
        except Exception as e:
            log.debug("disable_ipv6(%s) failed: %s", tun_name, e)


# ── Fallback: if pyroute2 is missing we can still function via subprocess ──

class SubprocessFallbackOps:
    """Drop-in for NetlinkOps when pyroute2 isn't installed.

    Same method surface, same return semantics (None-on-success,
    log-and-swallow on error). Slow but keeps the system operational on
    hosts where pip install failed.
    """

    import subprocess as _sp

    def __init__(self):
        log.warning("Using subprocess fallback for netlink ops — pyroute2 is "
                    "strongly recommended. Run ./install.sh.")

    def _run(self, cmd: list, timeout: int = 5) -> int:
        try:
            r = self._sp.run(cmd, capture_output=True, timeout=timeout)
            if r.returncode != 0:
                msg = (r.stderr or r.stdout or b"").decode("utf-8", "replace").strip()
                log.debug("%s → rc=%d %s", " ".join(cmd), r.returncode, msg)
            return r.returncode
        except Exception as e:
            log.debug("%s → exception %s", " ".join(cmd), e)
            return -1

    def up_tun(self, tun_name, mtu=1400):
        self._run(["ip", "link", "set", "dev", tun_name, "mtu", str(mtu)])
        self._run(["ip", "link", "set", "dev", tun_name, "up"])

    def down_tun(self, tun_name):
        self._run(["ip", "link", "set", "dev", tun_name, "down"])

    def delete_link(self, tun_name):
        self._run(["ip", "link", "delete", tun_name])

    def list_tun_interfaces(self, prefix="tun-ue-"):
        try:
            r = self._sp.run(["ip", "-o", "link", "show"], capture_output=True,
                             timeout=5, text=True)
            names = []
            for line in r.stdout.splitlines():
                parts = line.split(":")
                if len(parts) >= 2:
                    name = parts[1].strip().split("@")[0]
                    if name.startswith(prefix):
                        names.append(name)
            return names
        except Exception:
            return []

    def addr_add(self, tun_name, addr, prefix=32):
        self._run(["ip", "addr", "add", f"{addr}/{prefix}", "dev", tun_name])

    def addr_flush(self, tun_name):
        self._run(["ip", "addr", "flush", "dev", tun_name])

    def route_add_default_dev(self, tun_name, table_id):
        self._run(["ip", "route", "add", "default", "dev", tun_name,
                   "table", str(table_id)])

    def route_flush_table(self, table_id):
        self._run(["ip", "route", "flush", "table", str(table_id)])

    def rule_add_from_src(self, src_ip, table_id, priority=None):
        cmd = ["ip", "rule", "add", "from", src_ip, "lookup", str(table_id)]
        if priority is not None:
            cmd += ["priority", str(priority)]
        self._run(cmd)

    def rule_del_from_src(self, src_ip, max_iterations=16):
        removed = 0
        for _ in range(max_iterations):
            rc = self._run(["ip", "rule", "del", "from", src_ip])
            if rc != 0:
                break
            removed += 1
        return removed

    @staticmethod
    def disable_ipv6(tun_name):
        NetlinkOps.disable_ipv6(tun_name)


def get_ops():
    """Return the best available ops provider for this host."""
    if _HAVE_PYROUTE2:
        return NetlinkOps.get()
    return SubprocessFallbackOps()
