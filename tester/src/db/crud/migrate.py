# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# JSON → SQLite migration (mirrors sa_core/db/migrate/)

import json
import os
import logging
from src.db.crud.ue import ue_get, ue_add, ue_count
from src.db.crud.gnb import gnb_get, gnb_add, gnb_list

log = logging.getLogger("tester.db")


def migrate_from_json(sim_json_path: str = None, gnb_json_path: str = None):
    """Import existing JSON config files into SQLite DB."""
    if sim_json_path and os.path.exists(sim_json_path):
        with open(sim_json_path, 'r') as f:
            ues = json.load(f)
        count = 0
        for ue in ues:
            if not ue_get(ue["imsi"]):
                try:
                    ue_add(ue)
                    count += 1
                except Exception as e:
                    log.warning("Failed to migrate UE %s: %s", ue.get("imsi"), e)
        log.info("Migrated %d/%d UEs from %s", count, len(ues), sim_json_path)

    if gnb_json_path and os.path.exists(gnb_json_path):
        with open(gnb_json_path, 'r') as f:
            gnbs = json.load(f)
        count = 0
        for g in gnbs:
            if not gnb_get(g.get("gnb_name", "")):
                try:
                    gnb_add(g)
                    count += 1
                except Exception as e:
                    log.warning("Failed to migrate gNB %s: %s", g.get("gnb_name"), e)
        log.info("Migrated %d/%d gNBs from %s", count, len(gnbs), gnb_json_path)
