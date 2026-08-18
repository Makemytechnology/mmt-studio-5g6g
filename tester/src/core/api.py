# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Shared REST API client for SA Core webservice.

Used by provisioner, admin, iperf management, and UPF stats collection.
All core communication goes through this module.
"""

import json
import logging
import urllib.request

log = logging.getLogger("tester.core_api")


def get_core_ip():
    """Get SA Core IP from gNB config (AMF IP = core IP)."""
    try:
        import src.config as _cfg
        return _cfg.AMF_IP
    except Exception:
        return "127.0.0.1"


def core_api_url():
    """Get SA Core API base URL."""
    return f"http://{get_core_ip()}:5000"


def core_api(path, method="GET", body=None, timeout=10, quiet=False):
    """Call SA Core REST API. Returns parsed JSON or None.

    `quiet=True` suppresses the DEBUG-level "failed" log on connection
    errors. Used by callers that already know the core is mid-restart
    and don't want to flood the log with expected ConnectionRefused
    noise (see reset_to_baseline's poll loop).
    """
    url = f"{core_api_url()}{path}"
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read())
    except Exception as e:
        if not quiet:
            log.debug("Core API %s %s failed: %s", method, path, e)
        return None
