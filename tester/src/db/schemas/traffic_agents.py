# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# traffic_agents — registry of tester-owned traffic engines running on
# DN / APN-side hosts. The tester's orchestrator drives these exclusively;
# sa_core is never in the traffic path.

TRAFFIC_AGENTS_DDL = [
    """
    CREATE TABLE IF NOT EXISTS traffic_agents (
      id         TEXT PRIMARY KEY,
      url        TEXT NOT NULL,
      dnn        TEXT DEFAULT '',
      dn_ip      TEXT DEFAULT '',
      token      TEXT DEFAULT '',
      is_default INTEGER DEFAULT 0,
      notes      TEXT DEFAULT ''
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_traffic_agents_dnn ON traffic_agents(dnn)",
]
