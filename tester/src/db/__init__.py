# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/db/ — Database layer (mirrors sa_core/db/)
#
# Structure:
#   engine.py     — get_db(), ensure_schema() (like sa_core/db/engine.py)
#   schemas/      — DDL per domain (like sa_core/db/schemas/)
#   crud/         — CRUD per domain (like sa_core/db/crud/)
#   runs.py       — test run lifecycle
#   reports.py    — HTML/JSON/JUnit report generation
#   analysis.py   — trends, regressions, flaky detection

from src.db.engine import get_db, ensure_schema, DB_FILE
from src.db.crud import (
    ue_list, ue_get, ue_add, ue_update, ue_delete, ue_count,
    gnb_list, gnb_get, gnb_add,
    result_save, result_list, result_get, result_stats,
    sync_mark, sync_status, sync_pending,
    migrate_from_json,
)
