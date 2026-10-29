# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Cluster configuration — read from config/cluster.json

import os
import json
import logging

log = logging.getLogger("tester.cluster")

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CLUSTER_CONFIG_PATH = os.path.join(PROJECT_ROOT, "config", "cluster.json")

DEFAULT_CONFIG = {
    "mode": "standalone",           # standalone | cluster
    "node_id": "tester-01",
    "role": "standalone",           # standalone | controller | worker
    "controller_url": "",           # http://controller:5001
    "db_engine": "sqlite",          # sqlite | postgresql
    "db_sqlite": {
        "path": "data/sa_tester.db"
    },
    "db_postgresql": {
        "host": "localhost",
        "port": 5432,
        "database": "sa_tester",
        "user": "satester",
        "password": ""
    },
    "worker": {
        "gnb_start": 0,            # first gNB index for this worker
        "gnb_count": 10000,         # number of gNBs this worker handles
        "ues_per_gnb_active": 1000,
        "ues_per_gnb_idle": 10000,
        "report_interval_s": 5,     # metrics push interval
    },
    "nodes": []                     # list of {id, ip, port, gnb_start, gnb_count, status}
}


def load_config() -> dict:
    """Load cluster config from config/cluster.json or return defaults."""
    if os.path.exists(CLUSTER_CONFIG_PATH):
        try:
            with open(CLUSTER_CONFIG_PATH, 'r') as f:
                cfg = json.load(f)
            # Merge with defaults
            merged = dict(DEFAULT_CONFIG)
            merged.update(cfg)
            return merged
        except Exception as e:
            log.warning("Failed to load cluster config: %s", e)
    return dict(DEFAULT_CONFIG)


def save_config(cfg: dict):
    """Save cluster config to config/cluster.json."""
    os.makedirs(os.path.dirname(CLUSTER_CONFIG_PATH), exist_ok=True)
    with open(CLUSTER_CONFIG_PATH, 'w') as f:
        json.dump(cfg, f, indent=2)
    log.info("Cluster config saved to %s", CLUSTER_CONFIG_PATH)


def is_standalone() -> bool:
    return load_config().get("mode") == "standalone"


def is_controller() -> bool:
    return load_config().get("role") == "controller"


def is_worker() -> bool:
    return load_config().get("role") == "worker"
