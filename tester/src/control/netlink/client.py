# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Async wrapper around src.protocol.netlink.NetlinkOps.

pyroute2 is blocking-by-design (it speaks AF_NETLINK synchronously). We
don't want to block the event loop on a netlink round-trip, so every op
gets offloaded onto a **single-thread** executor. That matters:

- Single thread ⇒ netlink calls are naturally serialized; no races
  against other UE tun setups fighting for the same table id.
- Single thread ⇒ we never stampede the kernel with fork-like concurrency
  issues; pyroute2's internal lock stays trivially happy.

If we later find netlink throughput is a bottleneck (unlikely at 10k
UEs — each call is sub-millisecond), we can bump worker count; the API
stays the same.
"""

from __future__ import annotations

import asyncio
import logging
from concurrent.futures import ThreadPoolExecutor
from typing import Optional

from src.protocol.netlink import get_ops

log = logging.getLogger("tester.control.netlink")


class AsyncNetlink:
    """async/await facade over NetlinkOps. One singleton per process."""

    _instance: Optional["AsyncNetlink"] = None

    def __init__(self) -> None:
        # Single-thread executor — see module docstring.
        self._executor = ThreadPoolExecutor(max_workers=1,
                                             thread_name_prefix="netlink")
        # pyroute2 >= 0.9 uses asyncio internally for IPRoute(). If we
        # construct NetlinkOps on the main thread that's already running
        # an event loop, pyroute2's startup calls `loop.run_until_complete`
        # on the live loop and blows up. Constructing on the executor
        # thread means pyroute2 gets its own fresh loop there, isolated
        # from ours. The ops object then lives on that thread forever and
        # every call goes through run_in_executor anyway.
        self._ops = None  # set on first use, on executor thread

    @classmethod
    def get(cls) -> "AsyncNetlink":
        if cls._instance is None:
            cls._instance = cls()
        return cls._instance

    def _ensure_ops(self):
        if self._ops is None:
            self._ops = get_ops()
        return self._ops

    def _submit(self, fn_name: str, *args, **kwargs):
        loop = asyncio.get_running_loop()
        def _do():
            ops = self._ensure_ops()
            return getattr(ops, fn_name)(*args, **kwargs)
        return loop.run_in_executor(self._executor, _do)

    # ── Mirror NetlinkOps API, all async ────────────────────────────

    async def up_tun(self, tun_name: str, mtu: int = 1400) -> None:
        await self._submit("up_tun", tun_name, mtu)

    async def down_tun(self, tun_name: str) -> None:
        await self._submit("down_tun", tun_name)

    async def delete_link(self, tun_name: str) -> None:
        await self._submit("delete_link", tun_name)

    async def list_tun_interfaces(self, prefix: str = "tun-ue-"):
        return await self._submit("list_tun_interfaces", prefix)

    async def addr_add(self, tun_name: str, addr: str, prefix: int = 32) -> None:
        await self._submit("addr_add", tun_name, addr, prefix)

    async def addr_flush(self, tun_name: str) -> None:
        await self._submit("addr_flush", tun_name)

    async def route_add_default_dev(self, tun_name: str, table_id: int) -> None:
        await self._submit("route_add_default_dev", tun_name, table_id)

    async def route_flush_table(self, table_id: int) -> None:
        await self._submit("route_flush_table", table_id)

    async def rule_add_from_src(self, src_ip: str, table_id: int,
                                  priority: Optional[int] = None) -> None:
        await self._submit("rule_add_from_src", src_ip, table_id, priority)

    async def rule_del_from_src(self, src_ip: str,
                                  max_iterations: int = 16) -> int:
        return await self._submit("rule_del_from_src", src_ip, max_iterations)

    async def disable_ipv6(self, tun_name: str) -> None:
        # sysctl /proc write — not netlink, but stays on the same executor
        # so we never block the event loop for a file I/O.
        await self._submit("disable_ipv6", tun_name)
