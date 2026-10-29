#!/usr/bin/env python3
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""
satester_db_cli.py
------------------
CLI for SA Tester DB (mirrors sa_core's sacore_db_cli.py).

Commands:
  python3 -m src.db.satester_db_cli list-ue
  python3 -m src.db.satester_db_cli list-ue --gnb tester-gnb-00
  python3 -m src.db.satester_db_cli get-ue --imsi 001011234560001
  python3 -m src.db.satester_db_cli list-gnb
  python3 -m src.db.satester_db_cli list-runs
  python3 -m src.db.satester_db_cli stats
  python3 -m src.db.satester_db_cli migrate
"""

import argparse
import json
import sys
import os

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..")))

from src.db.schema import ensure_schema
from src.db import crud


def _print_json(obj):
    print(json.dumps(obj, indent=2, sort_keys=True, ensure_ascii=False, default=str))


def main():
    ensure_schema()

    parser = argparse.ArgumentParser(description="SA Tester DB CLI")
    sub = parser.add_subparsers(dest="command")

    # list-ue
    p = sub.add_parser("list-ue", help="List UEs")
    p.add_argument("--gnb", help="Filter by gnb_name")

    # get-ue
    p = sub.add_parser("get-ue", help="Get UE by IMSI")
    p.add_argument("--imsi", required=True)

    # list-gnb
    sub.add_parser("list-gnb", help="List gNB configs")

    # list-runs
    p = sub.add_parser("list-runs", help="List test runs")
    p.add_argument("--limit", type=int, default=10)

    # stats
    sub.add_parser("stats", help="DB statistics")

    # migrate
    sub.add_parser("migrate", help="Migrate JSON → SQLite")

    args = parser.parse_args()

    if args.command == "list-ue":
        ues = crud.ue_list(args.gnb)
        print(f"{len(ues)} UE(s)")
        _print_json(ues)
    elif args.command == "get-ue":
        ue = crud.ue_get(args.imsi)
        if ue:
            _print_json(ue)
        else:
            print(f"IMSI {args.imsi} not found")
    elif args.command == "list-gnb":
        gnbs = crud.gnb_list()
        print(f"{len(gnbs)} gNB(s)")
        _print_json(gnbs)
    elif args.command == "list-runs":
        from src.db.runs import list_runs
        runs = list_runs(args.limit)
        for r in runs:
            print(f"  {r['id']:10s} {r['run_type']:12s} {r['status']:10s} "
                  f"P={r.get('passed', 0)} F={r.get('failed', 0)} T={r.get('total', 0)}")
    elif args.command == "stats":
        stats = crud.result_stats()
        print(f"UEs:        {crud.ue_count()}")
        print(f"gNBs:       {len(crud.gnb_list())}")
        print(f"Results:    {stats.get('total', 0)} (P={stats.get('passed', 0)} F={stats.get('failed', 0)})")
        print(f"Pending:    {len(crud.sync_pending())}")
    elif args.command == "migrate":
        from src.config import GNB_PROFILES_PATH
        crud.migrate_from_json(None, GNB_PROFILES_PATH)
        print(f"Migrated: {crud.ue_count()} UEs, {len(crud.gnb_list())} gNBs")
    else:
        parser.print_help()


if __name__ == "__main__":
    main()
