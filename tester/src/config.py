# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""SA Tester configuration.

Single source of truth:
  - gNB config (AMF IP/port, gNB params): config/gnb_profiles.json
  - UE roster (128 baseline UEs):         config/baseline.yaml
    → loaded at startup via src.baseline.sim_entries(); K/OPc derived
      on the fly from IMSI + kdf_version. GUI-added UEs (add/clone/
      delete via /api/sim-db) mutate an in-memory copy of that list
      and are session-scoped — they don't persist across restarts.
"""

import os
import json

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

# ── Config file paths ──
SIM_DB_PATH = os.environ.get("SIM_DB_PATH", "")
GNB_PROFILES_PATH = os.path.join(PROJECT_ROOT, "config", "gnb_profiles.json")

# ── AMF target (derived from first gNB profile — single source of truth) ──
def _load_amf_defaults():
    try:
        with open(GNB_PROFILES_PATH, "r") as f:
            profiles = json.load(f)
        if profiles:
            return profiles[0].get("amf_ip", "127.0.0.1"), profiles[0].get("amf_port", 38412)
    except (FileNotFoundError, json.JSONDecodeError, IndexError, KeyError):
        pass
    return "127.0.0.1", 38412

AMF_IP, AMF_PORT = _load_amf_defaults()

# ── Web UI ──
TESTER_WEB_PORT = 5001

# ── gNB defaults ──
GNB_DEFAULTS = {
    "mcc": "001",
    "mnc": "01",
    "tac": "0001",
    "gnb_id_base": 0x500000,
    "gnb_name_prefix": "tester-gnb",
    "paging_drx": "v128",
    "slices": [{"sst": 1, "sd": 0x010203}],
}

# ── UE defaults ──
UE_DEFAULTS = {
    "ue_sec_cap": bytes([0xF0, 0x70, 0xF0, 0x70]),
    "requested_nssai": [{"sst": 1, "sd": 0x010203}],
}

# ── Test runner ──
TEST_TIMEOUT_SEC = 30


def _load_traffic_duration():
    """Read TRAFFIC_DURATION from common.resource (single source of truth).

    Parses ${TRAFFIC_DURATION} from robot/resources/common.resource.
    Falls back to 60 if file not found or parse error.
    """
    try:
        resource_path = os.path.join(PROJECT_ROOT, "robot", "resources", "common.resource")
        with open(resource_path, "r") as f:
            for line in f:
                if "${TRAFFIC_DURATION}" in line:
                    parts = line.strip().split()
                    if len(parts) >= 2:
                        return int(parts[-1])
    except Exception:
        pass
    return 60


TRAFFIC_DURATION = _load_traffic_duration()
