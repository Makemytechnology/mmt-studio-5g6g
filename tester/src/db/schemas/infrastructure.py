# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Infrastructure tables — multi-homing, AMF targets, GTP-U config

INFRA_DDL = [
    # Single-row infrastructure config
    """
    CREATE TABLE IF NOT EXISTS infra_config (
      id                INTEGER PRIMARY KEY CHECK (id = 1),
      multi_home_mode   TEXT DEFAULT 'single'
                        CHECK (multi_home_mode IN ('single','sctp-multihome','amf-pool','amf-failover')),
      sctp_heartbeat_s  INTEGER DEFAULT 30,
      sctp_max_retrans  INTEGER DEFAULT 5,
      gtpu_port         INTEGER DEFAULT 2152,
      qos_mode          TEXT DEFAULT 'strict' CHECK (qos_mode IN ('strict','permissive')),
      traffic_engine_url TEXT DEFAULT ''
    )
    """,

    # AMF target endpoints
    """
    CREATE TABLE IF NOT EXISTS amf_targets (
      id       INTEGER PRIMARY KEY AUTOINCREMENT,
      name     TEXT UNIQUE NOT NULL,
      ip       TEXT NOT NULL,
      port     INTEGER DEFAULT 38412,
      role     TEXT DEFAULT 'active' CHECK (role IN ('active','standby','backup')),
      weight   INTEGER DEFAULT 100
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_amf_role ON amf_targets(role)",

    # SCTP multi-home IP addresses (per gNB)
    """
    CREATE TABLE IF NOT EXISTS sctp_addresses (
      id        INTEGER PRIMARY KEY AUTOINCREMENT,
      gnb_name  TEXT NOT NULL,
      ip        TEXT NOT NULL,
      is_primary INTEGER DEFAULT 0,
      FOREIGN KEY (gnb_name) REFERENCES gnb_config(gnb_name) ON DELETE CASCADE,
      UNIQUE(gnb_name, ip)
    )
    """,

    # AMF pool assignments (which gNBs connect to which AMF)
    """
    CREATE TABLE IF NOT EXISTS amf_gnb_assignments (
      id        INTEGER PRIMARY KEY AUTOINCREMENT,
      amf_name  TEXT NOT NULL,
      gnb_name  TEXT NOT NULL,
      FOREIGN KEY (amf_name) REFERENCES amf_targets(name) ON DELETE CASCADE,
      FOREIGN KEY (gnb_name) REFERENCES gnb_config(gnb_name) ON DELETE CASCADE,
      UNIQUE(amf_name, gnb_name)
    )
    """,

    # Ensure default infra config row exists
    """
    INSERT OR IGNORE INTO infra_config (id) VALUES (1)
    """,
]
