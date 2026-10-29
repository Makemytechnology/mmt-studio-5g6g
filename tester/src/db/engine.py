# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Database engine — connection management (mirrors sa_core/db/engine.py)

import os
import sqlite3
from sqlite3 import Connection

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
DB_DIR = os.path.join(PROJECT_ROOT, "data")
DB_FILE = os.path.join(DB_DIR, "sa_tester.db")


def get_db() -> Connection:
    """Get SQLite connection with foreign keys enabled."""
    os.makedirs(DB_DIR, exist_ok=True)
    conn = sqlite3.connect(DB_FILE, check_same_thread=False)
    conn.execute("PRAGMA foreign_keys = ON;")
    conn.row_factory = sqlite3.Row
    return conn


def ensure_schema():
    """Create all tables if they don't exist."""
    from src.db.schemas import ALL_DDL
    with get_db() as conn:
        cur = conn.cursor()
        for ddl in ALL_DDL:
            cur.execute(ddl)
        conn.commit()
