# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""
SA Tester Centralized Logging
==============================
Provides:
- Hierarchical loggers under the ``tester`` root
- Console handler with color-coded level prefixes
- Rotating file handler (/var/log/sa_tester/sa_tester.log)
- In-memory ring buffer for the web-based log viewer

Usage in any module::

    from src.tester_logger import get_logger
    logger = get_logger("statemachine")   # -> tester.statemachine
    logger.info("gNB connected to AMF %s:%d", ip, port)
"""

import json
import logging
import logging.handlers
import os
import threading
from collections import deque
from dataclasses import dataclass, asdict
from typing import List, Optional

# ============================================================
#  RING BUFFER HANDLER (for web UI)
# ============================================================

@dataclass
class LogEntry:
    """A single log entry stored in the ring buffer."""
    timestamp: float
    level: str
    logger_name: str
    module: str
    message: str
    seq: int = 0

    def to_dict(self) -> dict:
        return asdict(self)


class RingBufferHandler(logging.Handler):
    """
    A logging handler that stores records in a thread-safe ring buffer.
    The web UI polls this buffer via the /api/logs endpoint.
    """

    _instance: Optional["RingBufferHandler"] = None

    def __init__(self, capacity: int = 5000):
        super().__init__()
        self._buffer: deque = deque(maxlen=capacity)
        self._seq = 0
        self._lock = threading.Lock()

    def emit(self, record: logging.LogRecord):
        try:
            msg = record.getMessage()
            with self._lock:
                self._seq += 1
                entry = LogEntry(
                    timestamp=record.created,
                    level=record.levelname,
                    logger_name=record.name,
                    module=record.module,
                    message=msg,
                    seq=self._seq,
                )
                self._buffer.append(entry)
        except Exception:
            self.handleError(record)

    def get_entries(self, after_seq: int = 0, level: str = "",
                    logger_filter: str = "", search: str = "",
                    last_n: int = 200) -> List[dict]:
        """Return log entries matching the given filters."""
        with self._lock:
            entries = list(self._buffer)

        if after_seq:
            entries = [e for e in entries if e.seq > after_seq]

        if level:
            level_up = level.upper()
            level_num = getattr(logging, level_up, 0)
            entries = [e for e in entries
                       if getattr(logging, e.level, 0) >= level_num]

        if logger_filter:
            entries = [e for e in entries
                       if logger_filter in e.logger_name]

        if search:
            search_low = search.lower()
            entries = [e for e in entries
                       if search_low in e.message.lower()]

        # Return last N entries
        if len(entries) > last_n:
            entries = entries[-last_n:]

        return [e.to_dict() for e in entries]

    def get_logger_names(self) -> List[str]:
        """Return distinct logger names seen in the buffer."""
        with self._lock:
            return sorted(set(e.logger_name for e in self._buffer))

    @property
    def current_seq(self) -> int:
        with self._lock:
            return self._seq

    def clear(self):
        with self._lock:
            self._buffer.clear()

    @classmethod
    def get_instance(cls) -> "RingBufferHandler":
        """Get the singleton ring buffer handler."""
        if cls._instance is None:
            cls._instance = cls(capacity=5000)
        return cls._instance


# ============================================================
#  COLORED CONSOLE FORMATTER
# ============================================================

class ColoredFormatter(logging.Formatter):
    """Console formatter with level-colored prefixes."""

    COLORS = {
        "DEBUG":    "\033[36m",     # cyan
        "INFO":     "\033[32m",     # green
        "WARNING":  "\033[33m",     # yellow
        "ERROR":    "\033[31m",     # red
        "CRITICAL": "\033[1;31m",   # bold red
    }
    RESET = "\033[0m"

    def format(self, record):
        color = self.COLORS.get(record.levelname, "")
        # Short logger name: tester.statemachine -> statemachine
        short_name = record.name
        if short_name.startswith("tester."):
            short_name = short_name[7:]
        record.short_name = short_name
        record.color = color
        record.reset = self.RESET
        return super().format(record)


# ============================================================
#  SETUP
# ============================================================

_initialized = False
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))


def setup_logging(log_dir: str = "", level: str = "DEBUG",
                  console: bool = True, file: bool = True):
    """
    Initialize the SA Tester logging system. Call once from app.py.

    Args:
        log_dir: Directory for log files. Defaults to /var/log/sa_tester/.
        level: Root log level (DEBUG, INFO, WARNING, ERROR).
        console: Enable colored console output.
        file: Enable rotating file handler.
    """
    global _initialized
    if _initialized:
        return
    _initialized = True

    root_level = getattr(logging, level.upper(), logging.DEBUG)

    # Root tester logger
    tester_root = logging.getLogger("tester")
    tester_root.setLevel(root_level)
    tester_root.propagate = False  # Don't propagate to root logger

    # --- Ring buffer handler (always active) ---
    ring = RingBufferHandler.get_instance()
    ring.setLevel(logging.DEBUG)
    ring.setFormatter(logging.Formatter("%(message)s"))
    tester_root.addHandler(ring)

    # --- Console handler ---
    if console:
        console_handler = logging.StreamHandler()
        console_handler.setLevel(root_level)
        console_handler.setFormatter(ColoredFormatter(
            "%(asctime)s.%(msecs)03.0f %(color)s[%(levelname)-7s]%(reset)s %(name)s: %(message)s",
            datefmt="%H:%M:%S",
        ))
        tester_root.addHandler(console_handler)

    # --- Rotating file handler ---
    if file:
        if not log_dir:
            log_dir = "/var/log/sa_tester"
        os.makedirs(log_dir, exist_ok=True)
        log_path = os.path.join(log_dir, "sa_tester.log")

        file_handler = logging.handlers.RotatingFileHandler(
            log_path, maxBytes=5 * 1024 * 1024, backupCount=5,
            encoding="utf-8",
        )
        file_handler.setLevel(logging.DEBUG)
        # Millisecond-precision timestamps (same as the console handler
        # above). Needed for correlating events across threads where two
        # log lines can land in the same second — tunnel-setup worker
        # finishes, PSR Response send-worker fires, both at 18:08:38.x.
        file_handler.setFormatter(logging.Formatter(
            "%(asctime)s.%(msecs)03.0f [%(levelname)-7s] %(name)s "
            "(%(module)s:%(lineno)d): %(message)s",
            datefmt="%Y-%m-%d %H:%M:%S",
        ))
        tester_root.addHandler(file_handler)

    # Also capture Flask/Werkzeug and sa_tester logs into our ring buffer
    for name in ("werkzeug", "sa_tester"):
        ext_logger = logging.getLogger(name)
        ext_logger.addHandler(ring)


def get_logger(name: str) -> logging.Logger:
    """
    Get a named SA Tester logger.

    Args:
        name: Short module name (e.g., "statemachine", "protocol").
              Will be prefixed with "tester." automatically.

    Returns:
        logging.Logger instance.

    Example::
        logger = get_logger("statemachine")
        logger.info("gNB state: %s", state)
    """
    if not name.startswith("tester."):
        name = "tester." + name
    return logging.getLogger(name)


def set_level(logger_name: str, level: str):
    """Change log level for a specific logger at runtime."""
    if not logger_name.startswith("tester."):
        logger_name = "tester." + logger_name
    lg = logging.getLogger(logger_name)
    lg.setLevel(getattr(logging, level.upper(), logging.DEBUG))


def get_all_loggers() -> List[dict]:
    """Return info about all active tester.* loggers."""
    result = []
    manager = logging.Logger.manager
    for name, logger in sorted(manager.loggerDict.items()):
        if name.startswith("tester.") and isinstance(logger, logging.Logger):
            result.append({
                "name": name,
                "level": logging.getLevelName(logger.level),
                "effective_level": logging.getLevelName(logger.getEffectiveLevel()),
            })
    return result


def set_all_levels(level: str):
    """Set the same log level on the root tester logger and all child loggers."""
    level_up = level.upper()
    root = logging.getLogger("tester")
    root.setLevel(getattr(logging, level_up, logging.DEBUG))
    for info in get_all_loggers():
        set_level(info["name"], level_up)


# ---------- Persistence ----------

_LEVELS_PATH = os.path.join(PROJECT_ROOT, "data", "log_levels.json")


def save_levels(path: str = ""):
    """Persist current logger levels to JSON so they survive restarts."""
    path = path or _LEVELS_PATH
    levels = {}
    for info in get_all_loggers():
        levels[info["name"]] = info["effective_level"]
    root = logging.getLogger("tester")
    levels["__root__"] = logging.getLevelName(root.level)
    try:
        os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            json.dump(levels, f, indent=2)
    except Exception as exc:
        logging.getLogger("tester.logger").warning("Could not save log levels: %s", exc)


def load_levels(path: str = ""):
    """Restore logger levels from JSON on startup."""
    path = path or _LEVELS_PATH
    if not os.path.exists(path):
        return
    try:
        with open(path, "r", encoding="utf-8") as f:
            levels = json.load(f)
    except Exception:
        return
    root_level = levels.pop("__root__", "")
    if root_level:
        root = logging.getLogger("tester")
        root.setLevel(getattr(logging, root_level, logging.DEBUG))
    for name, level in levels.items():
        if name.startswith("tester."):
            set_level(name, level)
