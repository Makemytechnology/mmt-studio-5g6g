# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# UE and gNB configuration tables

UE_DDL = [
    """
    CREATE TABLE IF NOT EXISTS ue (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi              TEXT UNIQUE NOT NULL,
      msisdn            TEXT,
      k                 TEXT NOT NULL,
      opc               TEXT NOT NULL,
      op_type           TEXT NOT NULL DEFAULT 'OPC' CHECK (op_type IN ('OP','OPC')),
      sqn               INTEGER DEFAULT 0,
      amf               TEXT DEFAULT '8000',
      gnb_name          TEXT NOT NULL,
      identity_type     TEXT DEFAULT 'supi' CHECK (identity_type IN ('supi','suci')),
      routing_indicator TEXT DEFAULT '0000',
      protection_scheme INTEGER DEFAULT 0 CHECK (protection_scheme IN (0,1,2)),
      hn_pub_key_id     INTEGER DEFAULT 0,
      hn_pub_key        TEXT
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_ue_imsi ON ue(imsi)",
    "CREATE INDEX IF NOT EXISTS idx_ue_gnb ON ue(gnb_name)",
]

GNB_DDL = [
    """
    CREATE TABLE IF NOT EXISTS gnb_config (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      gnb_name    TEXT UNIQUE NOT NULL,
      gnb_id      TEXT NOT NULL,
      gnb_ip      TEXT,
      interface   TEXT,
      amf_ip      TEXT NOT NULL,
      amf_port    INTEGER DEFAULT 38412,
      mcc         TEXT NOT NULL,
      mnc         TEXT NOT NULL,
      tac         TEXT NOT NULL,
      paging_drx  TEXT DEFAULT 'v128',
      slices_json TEXT NOT NULL DEFAULT '[{"sst":1,"sd":"0x010203"}]'
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_gnb_name ON gnb_config(gnb_name)",
]
