# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Shared asyncio helpers for the control plane.

Phase 2 of ARCHITECTURE.md. A single event loop owns SCTP recv, NGAP
dispatch, UE actor mailboxes, and netlink work. This module hosts the
cross-cutting utilities — graceful shutdown, safe task spawning, a
loop accessor that works whether we're in the asyncio event thread or
in a thread that drove `asyncio.run(main())`.
"""

from __future__ import annotations

import asyncio
import logging
import signal
from typing import Awaitable, Callable, Optional

log = logging.getLogger("tester.control.loop")


# ── Task spawning with uncaught-exception logging ──────────────────────

def spawn_task(coro: Awaitable, *, name: Optional[str] = None) -> asyncio.Task:
    """asyncio.create_task with a done-callback that logs exceptions.

    Fire-and-forget tasks that raise silently are one of the fastest ways
    to make an async program un-debuggable. Everything we spawn goes
    through here so crashes surface.
    """
    task = asyncio.create_task(coro, name=name)
    task.add_done_callback(_log_task_exception)
    return task


def _log_task_exception(task: asyncio.Task) -> None:
    if task.cancelled():
        return
    exc = task.exception()
    if exc is None:
        return
    log.error("task %s raised: %r", task.get_name(), exc, exc_info=exc)


# ── Graceful shutdown ──────────────────────────────────────────────────

class Shutdown:
    """Process-wide shutdown signal.

    Any long-running coroutine that wants to participate in clean
    shutdown should `await shutdown.wait()` in its idle branch.
    """

    def __init__(self) -> None:
        self._event = asyncio.Event()

    def request(self, reason: str = "") -> None:
        if not self._event.is_set():
            if reason:
                log.info("shutdown requested: %s", reason)
            else:
                log.info("shutdown requested")
        self._event.set()

    def requested(self) -> bool:
        return self._event.is_set()

    async def wait(self) -> None:
        await self._event.wait()


async def install_signal_handlers(shutdown: Shutdown) -> None:
    """Wire SIGINT/SIGTERM to shutdown.request().

    Call from the main coroutine after the loop is running. Safe to skip
    (tests that run inside pytest etc. don't need it).
    """
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(sig, shutdown.request, sig.name)
        except NotImplementedError:
            # Windows / restricted env — ignore, the tester only runs on
            # Linux but tests may run anywhere.
            pass


# ── Bridging sync callers into the async loop (rare — keep this small) ──

def run_coroutine_blocking(loop: asyncio.AbstractEventLoop,
                            coro: Awaitable,
                            timeout: Optional[float] = None):
    """Submit a coroutine onto an existing loop from a non-async thread.

    Only for the narrow case where the legacy threaded testcase code
    needs to drive an async actor once. New code should stay inside the
    loop and `await`.
    """
    fut = asyncio.run_coroutine_threadsafe(coro, loop)
    return fut.result(timeout=timeout)
