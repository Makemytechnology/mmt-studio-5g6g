// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/network.go — Network Configuration DDL
package schemas

var NetworkDDL = []string{
	// Single-row core network / AMF configuration. amf_ip default is
	// empty so the AMF auto-picks the management IPv4 (TS 38.412 §7
	// transport address — operator deployment choice). sctp_port
	// defaults to the IANA-registered NGAP port 38412 but is operator-
	// tunable for collisions / multi-tenant boxes.
	`CREATE TABLE IF NOT EXISTS network_config (
      id                    INTEGER PRIMARY KEY CHECK (id = 1),
      amf_name              TEXT NOT NULL DEFAULT 'MMT-CORE',
      amf_ip                TEXT NOT NULL DEFAULT '',
      sctp_port             INTEGER NOT NULL DEFAULT 38412,
      li_auth_token         TEXT NOT NULL DEFAULT '',
      li_x2_enabled         INTEGER NOT NULL DEFAULT 0,
      li_x3_enabled         INTEGER NOT NULL DEFAULT 0,
      li_mdf_poll_interval_ms INTEGER NOT NULL DEFAULT 1000,
      relative_amf_capacity INTEGER NOT NULL DEFAULT 255,
      ims_vops_3gpp         INTEGER NOT NULL DEFAULT 1,
      ims_vops_n3gpp        INTEGER NOT NULL DEFAULT 0,
      emc                   INTEGER NOT NULL DEFAULT 0,
      emf                   INTEGER NOT NULL DEFAULT 0,
      iwk_n26               INTEGER NOT NULL DEFAULT 0,
      mpsi                  INTEGER NOT NULL DEFAULT 0,
      emcn3                 INTEGER NOT NULL DEFAULT 0,
      mcsi                  INTEGER NOT NULL DEFAULT 0,
      restrict_ec           INTEGER NOT NULL DEFAULT 0,
      n3_data               INTEGER NOT NULL DEFAULT 0,
      cp_ciot               INTEGER NOT NULL DEFAULT 0,
      up_ciot               INTEGER NOT NULL DEFAULT 0,
      hc_cp_ciot            INTEGER NOT NULL DEFAULT 0,
      iphc_cp_ciot          INTEGER NOT NULL DEFAULT 0
    )`,

	// APN / DNN definitions (TS 23.501 §5.6.1)
	`CREATE TABLE IF NOT EXISTS apn_config (
      id               INTEGER PRIMARY KEY AUTOINCREMENT,
      apn_name         TEXT UNIQUE NOT NULL,
      ambr_dl_kbps     INTEGER NOT NULL DEFAULT 1000000,
      ambr_ul_kbps     INTEGER NOT NULL DEFAULT 1000000,
      pdu_session_type TEXT NOT NULL DEFAULT 'IPv4',
      ssc_mode         INTEGER NOT NULL DEFAULT 1,
      dns_primary      TEXT,
      dns_secondary    TEXT,
      pcscf_address    TEXT,
      mtu              INTEGER NOT NULL DEFAULT 1500
    )`,
	`CREATE INDEX IF NOT EXISTS idx_apn_name ON apn_config(apn_name)`,

	// IP pools per APN (IPv4 + IPv6)
	`CREATE TABLE IF NOT EXISTS apn_ip_pools (
      id         INTEGER PRIMARY KEY AUTOINCREMENT,
      apn_id     INTEGER NOT NULL,
      cidr       TEXT NOT NULL,
      ip_version INTEGER NOT NULL DEFAULT 4 CHECK (ip_version IN (4, 6)),
      FOREIGN KEY (apn_id) REFERENCES apn_config(id) ON DELETE CASCADE
    )`,
	`CREATE INDEX IF NOT EXISTS idx_apn_ip_pools_apn_id ON apn_ip_pools(apn_id)`,

	// Security algorithm priorities (TS 33.501 §5.3)
	`CREATE TABLE IF NOT EXISTS security_algorithms (
      id        INTEGER PRIMARY KEY AUTOINCREMENT,
      algo_type TEXT NOT NULL CHECK (algo_type IN ('ciphering', 'integrity')),
      algorithm TEXT NOT NULL,
      priority  INTEGER NOT NULL,
      UNIQUE(algo_type, algorithm)
    )`,
}
