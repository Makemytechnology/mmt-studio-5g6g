# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Core sync state tracking

SYNC_DDL = [
    """
    CREATE TABLE IF NOT EXISTS core_sync (
      imsi        TEXT PRIMARY KEY,
      synced_at   REAL,
      core_status TEXT DEFAULT 'pending'
                  CHECK (core_status IN ('pending','provisioned','failed','deleted'))
    )
    """,
]
