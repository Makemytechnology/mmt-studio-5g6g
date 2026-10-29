# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Traffic profiles, groups, group memberships, and PDU-session flow snapshots.
#
# A "profile" is a declarative QoS class (DNN + 5QI + protocol + rates + SLA).
# A "group" binds a profile to a set of UEs with a run pattern.
# A "pdu_session_flow" is the per-UE runtime snapshot of (dnn, 5qi) → tun/ip.

TRAFFIC_PROFILES_DDL = [
    """
    CREATE TABLE IF NOT EXISTS traffic_profiles (
      id                 TEXT PRIMARY KEY,
      dnn                TEXT DEFAULT '',
      five_qi            INTEGER DEFAULT 9,
      protocol           TEXT DEFAULT 'udp'
                         CHECK (protocol IN ('udp','tcp','rtp-audio','rtp-video','icmp')),
      direction          TEXT DEFAULT 'ul'
                         CHECK (direction IN ('ul','dl','bidir')),
      bandwidth          TEXT DEFAULT '',
      duration           INTEGER DEFAULT 10,
      length             INTEGER DEFAULT 0,
      codec              TEXT DEFAULT '',
      dscp               INTEGER DEFAULT -1,
      sla_min_kbps       INTEGER DEFAULT 0,
      sla_max_jitter_ms  REAL    DEFAULT 0,
      sla_max_loss_pct   REAL    DEFAULT 0,
      sla_max_latency_ms REAL    DEFAULT 0,
      notes              TEXT    DEFAULT ''
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_profile_dnn_5qi ON traffic_profiles(dnn, five_qi)",

    """
    CREATE TABLE IF NOT EXISTS traffic_groups (
      id                 TEXT PRIMARY KEY,
      profile_id         TEXT NOT NULL,
      pattern            TEXT DEFAULT 'concurrent'
                         CHECK (pattern IN ('concurrent','ramp','staggered','periodic','poisson')),
      ramp_s             INTEGER DEFAULT 0,
      period_s           REAL    DEFAULT 0,
      concurrency_limit  INTEGER DEFAULT 0,
      notes              TEXT    DEFAULT '',
      FOREIGN KEY (profile_id) REFERENCES traffic_profiles(id) ON DELETE CASCADE
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_group_profile ON traffic_groups(profile_id)",

    """
    CREATE TABLE IF NOT EXISTS traffic_group_members (
      group_id  TEXT NOT NULL,
      ue_imsi   TEXT NOT NULL,
      PRIMARY KEY (group_id, ue_imsi),
      FOREIGN KEY (group_id) REFERENCES traffic_groups(id) ON DELETE CASCADE
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_group_members_imsi ON traffic_group_members(ue_imsi)",

    # Runtime snapshot: which (DNN, 5QI) flows does each UE currently have?
    # Populated by the UE FSM when a PDU session is established / modified.
    """
    CREATE TABLE IF NOT EXISTS pdu_session_flows (
      imsi        TEXT NOT NULL,
      dnn         TEXT NOT NULL,
      five_qi     INTEGER NOT NULL,
      qfi         INTEGER DEFAULT 0,
      tun_device  TEXT DEFAULT '',
      ue_ip       TEXT DEFAULT '',
      dn_ip       TEXT DEFAULT '',
      updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      PRIMARY KEY (imsi, dnn, five_qi)
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_flows_dnn_5qi ON pdu_session_flows(dnn, five_qi)",
    "CREATE INDEX IF NOT EXISTS idx_flows_imsi    ON pdu_session_flows(imsi)",
]
