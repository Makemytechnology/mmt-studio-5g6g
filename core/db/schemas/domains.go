// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// Code-generated from 35 Python schema files. DO NOT EDIT by hand.
package schemas

func init() {
	Register("eas", EasDDL)
	Register("isac", IsacDDL)
	Register("ranging", RangingDDL)
	Register("tsn", TsnDDL)
	Register("resilience", ResilienceDDL)
	Register("roaming", RoamingDDL)
	Register("iot", IotDDL)
	Register("musim", MusimDDL)
	Register("n26", N26DDL)
	Register("nsacf", NsacfDDL)
	Register("nwdaf", NwdafDDL)
	Register("exposure", ExposureDDL)
	Register("ursp", UrspDDL)
	Register("smsf", SmsfDDL)
	Register("ai", AiDDL)
	Register("trace", TraceDDL)
	Register("dr", DrDDL)
	Register("emergency", EmergencyDDL)
	Register("iops", IopsDDL)
	Register("mbs", MbsDDL)
	Register("pws", PwsDDL)
	Register("racs", RacsDDL)
	Register("dpi", DpiDDL)
	Register("li", LiDDL)
	Register("npn", NpnDDL)
	Register("ran_sharing", RanSharingDDL)
	Register("esim", EsimDDL)
	Register("mcx", McxDDL)
	Register("nsaas", NsaasDDL)
	Register("pin", PinDDL)
	Register("prose", ProseDDL)
	Register("seal", SealDDL)
	Register("ss", SsDDL)
	Register("uas", UasDDL)
	Register("ussd", UssdDDL)
}

var EasDDL = []string{
	`CREATE TABLE IF NOT EXISTS eas_registry (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      app_id            TEXT NOT NULL,
      name              TEXT,
      endpoint_url      TEXT NOT NULL,
      dnai              TEXT,
      latitude          REAL,
      longitude         REAL,
      supported_dnns    TEXT,
      supported_slices  TEXT,
      capacity          INTEGER NOT NULL DEFAULT 100,
      active_connections INTEGER NOT NULL DEFAULT 0,
      status            TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active','inactive','maintenance')),
      created_at        TEXT NOT NULL DEFAULT (datetime('now')),
      updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS eas_dnai_map (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      dnai          TEXT UNIQUE NOT NULL,
      description   TEXT,
      location_hint TEXT,
      upf_instance  TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS eas_discovery_log (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi          TEXT,
      app_id        TEXT,
      criteria_json TEXT,
      results_count INTEGER NOT NULL DEFAULT 0,
      selected_eas_id INTEGER,
      created_at    TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS eas_dns_entries (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      fqdn        TEXT UNIQUE NOT NULL,
      eas_id      INTEGER NOT NULL,
      created_at  TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (eas_id) REFERENCES eas_registry(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_eas_dns_entries_eas_id ON eas_dns_entries(eas_id)`,
	`CREATE INDEX IF NOT EXISTS idx_eas_discovery_log_selected_eas_id ON eas_discovery_log(selected_eas_id)`,

}

var IsacDDL = []string{
	`CREATE TABLE IF NOT EXISTS isac_sessions (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      sensing_type      TEXT NOT NULL
                        CHECK (sensing_type IN (
                            'presence_detection', 'object_tracking',
                            'environment_monitoring', 'gesture_recognition',
                            'intrusion_detection')),
      target_area       TEXT,
      resolution        TEXT,
      report_interval_s INTEGER NOT NULL DEFAULT 1,
      status            TEXT NOT NULL DEFAULT 'created'
                        CHECK (status IN ('created', 'active', 'completed', 'cancelled')),
      created_at        TEXT NOT NULL DEFAULT (datetime('now')),
      completed_at      TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS isac_data (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      session_id        INTEGER NOT NULL,
      timestamp         TEXT NOT NULL DEFAULT (datetime('now')),
      detected_objects  TEXT,
      environmental     TEXT,
      raw_data          TEXT,
      FOREIGN KEY (session_id) REFERENCES isac_sessions(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS isac_consumers (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      name              TEXT NOT NULL,
      callback_url      TEXT,
      api_key           TEXT UNIQUE,
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS isac_subscriptions (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      consumer_id       INTEGER NOT NULL,
      session_id        INTEGER NOT NULL,
      active            INTEGER NOT NULL DEFAULT 1,
      created_at        TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (consumer_id) REFERENCES isac_consumers(id) ON DELETE CASCADE,
      FOREIGN KEY (session_id) REFERENCES isac_sessions(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_isac_data_session ON isac_data(session_id)`,

	`CREATE INDEX IF NOT EXISTS idx_isac_data_ts ON isac_data(timestamp)`,

	`CREATE INDEX IF NOT EXISTS idx_isac_sub_consumer ON isac_subscriptions(consumer_id)`,

	`CREATE INDEX IF NOT EXISTS idx_isac_sub_session ON isac_subscriptions(session_id)`,

}

var RangingDDL = []string{
	`CREATE TABLE IF NOT EXISTS ranging_sessions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      source_imsi     TEXT NOT NULL,
      target_imsi     TEXT NOT NULL,
      method          TEXT NOT NULL
                      CHECK (method IN ('RTT','AoA','multi_RTT')),
      status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','completed','cancelled','timeout')),
      distance_m      REAL,
      azimuth_deg     REAL,
      elevation_deg   REAL,
      accuracy_m      REAL,
      created_at      TEXT NOT NULL,
      completed_at    TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS ranging_anchors (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT UNIQUE NOT NULL,
      latitude        REAL NOT NULL,
      longitude       REAL NOT NULL,
      altitude        REAL NOT NULL DEFAULT 0.0,
      anchor_type     TEXT NOT NULL DEFAULT 'ue'
                      CHECK (anchor_type IN ('gnb','ue','fixed')),
      active          INTEGER NOT NULL DEFAULT 1,
      created_at      TEXT NOT NULL
    )`,

	`CREATE TABLE IF NOT EXISTS ranging_privacy (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT UNIQUE NOT NULL,
      policy          TEXT NOT NULL DEFAULT 'allow_all'
                      CHECK (policy IN ('allow_all','deny_all','contacts_only')),
      allowed_contacts TEXT,
      updated_at      TEXT NOT NULL
    )`,

	`CREATE TABLE IF NOT EXISTS ranging_results_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      session_id      INTEGER NOT NULL,
      measurement_type TEXT NOT NULL,
      value           REAL NOT NULL,
      unit            TEXT NOT NULL,
      timestamp       TEXT NOT NULL,
      FOREIGN KEY (session_id) REFERENCES ranging_sessions(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_ranging_sess_source ON ranging_sessions(source_imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_ranging_sess_target ON ranging_sessions(target_imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_ranging_sess_status ON ranging_sessions(status)`,

	`CREATE INDEX IF NOT EXISTS idx_ranging_anchor_active ON ranging_anchors(active)`,

	`CREATE INDEX IF NOT EXISTS idx_ranging_log_sess ON ranging_results_log(session_id)`,

	`CREATE INDEX IF NOT EXISTS idx_ranging_log_ts ON ranging_results_log(timestamp)`,

}

var TsnDDL = []string{
	`CREATE TABLE IF NOT EXISTS tsn_bridges (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      bridge_id   TEXT UNIQUE NOT NULL,
      name        TEXT NOT NULL DEFAULT '',
      ds_tt_port  TEXT NOT NULL DEFAULT '',
      nw_tt_port  TEXT NOT NULL DEFAULT '',
      vlan_id     INTEGER,
      status      TEXT NOT NULL DEFAULT 'inactive'
                  CHECK (status IN ('active','inactive','error')),
      created_at  TEXT NOT NULL
    )`,

	`CREATE TABLE IF NOT EXISTS tsn_streams (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      bridge_id       INTEGER NOT NULL,
      stream_id       TEXT UNIQUE NOT NULL,
      traffic_class   INTEGER NOT NULL DEFAULT 0,
      priority        INTEGER NOT NULL DEFAULT 0,
      max_frame_size  INTEGER NOT NULL DEFAULT 1522,
      interval_us     INTEGER NOT NULL DEFAULT 1000,
      mapped_5qi      INTEGER,
      pdb_ms          REAL,
      created_at      TEXT NOT NULL,
      FOREIGN KEY (bridge_id) REFERENCES tsn_bridges(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS tsn_clock_domains (
      id                   INTEGER PRIMARY KEY AUTOINCREMENT,
      domain_id            TEXT UNIQUE NOT NULL,
      gm_identity          TEXT NOT NULL DEFAULT '',
      sync_accuracy_ns     INTEGER NOT NULL DEFAULT 0,
      holdover_capability_s INTEGER NOT NULL DEFAULT 0,
      status               TEXT NOT NULL DEFAULT 'freerun'
                           CHECK (status IN ('synced','freerun','holdover')),
      last_sync_at         TEXT,
      created_at           TEXT NOT NULL
    )`,

	`CREATE TABLE IF NOT EXISTS tsn_gate_schedules (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      stream_id     INTEGER NOT NULL,
      gate_state    TEXT NOT NULL DEFAULT 'open'
                    CHECK (gate_state IN ('open','closed')),
      start_time_ns BIGINT NOT NULL DEFAULT 0,
      duration_ns   BIGINT NOT NULL DEFAULT 0,
      cycle_time_ns BIGINT NOT NULL DEFAULT 0,
      FOREIGN KEY (stream_id) REFERENCES tsn_streams(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_tsn_stream_bridge ON tsn_streams(bridge_id)`,

	`CREATE INDEX IF NOT EXISTS idx_tsn_gate_stream ON tsn_gate_schedules(stream_id)`,

}

var ResilienceDDL = []string{
	`CREATE TABLE IF NOT EXISTS resilience_nf_instances (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      nf_type           TEXT NOT NULL,
      instance_id       TEXT NOT NULL UNIQUE,
      endpoint          TEXT NOT NULL,
      priority          INTEGER NOT NULL DEFAULT 0,
      role              TEXT NOT NULL DEFAULT 'standby'
                        CHECK (role IN ('active', 'standby')),
      health            TEXT NOT NULL DEFAULT 'healthy'
                        CHECK (health IN ('healthy', 'degraded', 'failed')),
      last_heartbeat_at TEXT,
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS resilience_sites (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      name              TEXT NOT NULL UNIQUE,
      location          TEXT,
      role              TEXT NOT NULL DEFAULT 'standby'
                        CHECK (role IN ('active', 'standby', 'dr_site')),
      status            TEXT NOT NULL DEFAULT 'online'
                        CHECK (status IN ('online', 'offline', 'failover')),
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS resilience_failover_log (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      nf_type           TEXT NOT NULL,
      from_instance     TEXT,
      to_instance       TEXT,
      reason            TEXT,
      site              TEXT,
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS resilience_state_snapshots (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      nf_type           TEXT NOT NULL,
      state_data        TEXT NOT NULL,
      snapshot_at       TEXT NOT NULL DEFAULT (datetime('now')),
      replicated_to     TEXT
    )`,

	`CREATE INDEX IF NOT EXISTS idx_res_nf_type ON resilience_nf_instances(nf_type)`,

	`CREATE INDEX IF NOT EXISTS idx_res_failover_nf ON resilience_failover_log(nf_type)`,

	`CREATE INDEX IF NOT EXISTS idx_res_snap_nf ON resilience_state_snapshots(nf_type)`,

}

var RoamingDDL = []string{
	`CREATE TABLE IF NOT EXISTS roaming_agreements (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      partner_plmn_id TEXT NOT NULL,
      partner_name    TEXT NOT NULL DEFAULT '',
      direction       TEXT NOT NULL DEFAULT 'both'
                      CHECK (direction IN ('inbound','outbound','both')),
      roaming_mode    TEXT NOT NULL DEFAULT 'lbo'
                      CHECK (roaming_mode IN ('hr','lbo','both')),
      max_ues         INTEGER NOT NULL DEFAULT 0,
      allowed_sst     TEXT NOT NULL DEFAULT '',
      allowed_dnn     TEXT NOT NULL DEFAULT '',
      ausf_endpoint   TEXT NOT NULL DEFAULT '',
      udm_endpoint    TEXT NOT NULL DEFAULT '',
      smf_endpoint    TEXT NOT NULL DEFAULT '',
      sepp_endpoint   TEXT NOT NULL DEFAULT '',
      enabled         INTEGER NOT NULL DEFAULT 1,
      UNIQUE(partner_plmn_id)
    )`,

	`CREATE TABLE IF NOT EXISTS roaming_sessions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      home_plmn_id    TEXT NOT NULL,
      visited_plmn_id TEXT NOT NULL,
      direction       TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
      roaming_mode    TEXT NOT NULL CHECK (roaming_mode IN ('hr','lbo')),
      pdu_session_id  INTEGER,
      start_time      TEXT NOT NULL DEFAULT (datetime('now')),
      end_time        TEXT,
      status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','released','failed'))
    )`,

	`CREATE TABLE IF NOT EXISTS roaming_cdrs (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      home_plmn_id    TEXT NOT NULL,
      visited_plmn_id TEXT NOT NULL,
      direction       TEXT NOT NULL,
      record_type     TEXT NOT NULL DEFAULT 'session'
                      CHECK (record_type IN ('session','event')),
      dnn             TEXT,
      sst             INTEGER,
      start_time      TEXT NOT NULL DEFAULT (datetime('now')),
      end_time        TEXT,
      bytes_ul        INTEGER NOT NULL DEFAULT 0,
      bytes_dl        INTEGER NOT NULL DEFAULT 0,
      duration_sec    REAL NOT NULL DEFAULT 0,
      cause           TEXT,
      exported        INTEGER NOT NULL DEFAULT 0
    )`,

	`CREATE INDEX IF NOT EXISTS idx_roam_sess_imsi ON roaming_sessions(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_roam_sess_status ON roaming_sessions(status)`,

	`CREATE INDEX IF NOT EXISTS idx_roam_cdr_imsi ON roaming_cdrs(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_roam_cdr_exported ON roaming_cdrs(exported)`,

}

var IotDDL = []string{
	`CREATE TABLE IF NOT EXISTS iot_edrx_config (
      imsi            TEXT PRIMARY KEY,
      device_type     TEXT NOT NULL DEFAULT 'nbiot'
                      CHECK (device_type IN ('nbiot','ltem','redcap')),
      edrx_cycle_sec  REAL NOT NULL DEFAULT 40.96,
      ptw_sec         REAL NOT NULL DEFAULT 2.56,
      enabled         INTEGER NOT NULL DEFAULT 1,
      FOREIGN KEY (imsi) REFERENCES ue(imsi) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS iot_psm_state (
      imsi            TEXT PRIMARY KEY,
      psm_enabled     INTEGER NOT NULL DEFAULT 1,
      t3324_sec       INTEGER NOT NULL DEFAULT 10,
      t3412_ext_sec   INTEGER NOT NULL DEFAULT 86400,
      psm_state       TEXT NOT NULL DEFAULT 'active'
                      CHECK (psm_state IN ('active','sleeping','unreachable')),
      sleep_start     TEXT,
      next_wakeup     TEXT,
      FOREIGN KEY (imsi) REFERENCES ue(imsi) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS iot_nbiot_capabilities (
      imsi              TEXT PRIMARY KEY,
      multi_tone        INTEGER NOT NULL DEFAULT 0,
      ce_level          INTEGER NOT NULL DEFAULT 0,
      cp_ciot_supported INTEGER NOT NULL DEFAULT 1,
      up_ciot_supported INTEGER NOT NULL DEFAULT 0,
      data_over_nas     INTEGER NOT NULL DEFAULT 1,
      FOREIGN KEY (imsi) REFERENCES ue(imsi) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS iot_cp_data (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      direction       TEXT NOT NULL CHECK (direction IN ('UL','DL')),
      data_payload    BLOB NOT NULL,
      apn             TEXT,
      delivered       INTEGER NOT NULL DEFAULT 0,
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      delivered_at    TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS iot_nidd_sessions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      session_id      TEXT UNIQUE NOT NULL,
      apn             TEXT NOT NULL,
      app_server_url  TEXT NOT NULL,
      status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','suspended','terminated')),
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (imsi) REFERENCES ue(imsi) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS iot_rate_control (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      apn             TEXT NOT NULL,
      imsi            TEXT,
      max_ul_rate     INTEGER NOT NULL DEFAULT 100,
      max_dl_rate     INTEGER NOT NULL DEFAULT 100,
      time_window_sec INTEGER NOT NULL DEFAULT 3600,
      current_ul      INTEGER NOT NULL DEFAULT 0,
      current_dl      INTEGER NOT NULL DEFAULT 0,
      window_start    TEXT,
      UNIQUE(apn, imsi)
    )`,

	`CREATE TABLE IF NOT EXISTS iot_tags (
      tag_id          TEXT PRIMARY KEY,
      tag_class       TEXT NOT NULL DEFAULT 'A'
                      CHECK (tag_class IN ('A','B','C')),
      tag_type        TEXT NOT NULL DEFAULT 'asset',
      group_id        TEXT,
      owner           TEXT,
      data_payload    TEXT,
      last_seen_at    TEXT,
      last_reader_id  TEXT,
      latitude        REAL,
      longitude       REAL,
      registered_at   TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS iot_readers (
      reader_id       TEXT PRIMARY KEY,
      gnb_ip          TEXT,
      latitude        REAL,
      longitude       REAL,
      capabilities    TEXT,
      status          TEXT NOT NULL DEFAULT 'active',
      last_heartbeat  TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS iot_inventory_events (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      reader_id       TEXT NOT NULL,
      event_type      TEXT NOT NULL,
      tags_found      INTEGER NOT NULL DEFAULT 0,
      result_json     TEXT,
      timestamp       TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS nidd_data_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      session_id      INTEGER NOT NULL,
      direction       TEXT NOT NULL CHECK (direction IN ('UL','DL')),
      data_hex        TEXT NOT NULL,
      data_length     INTEGER NOT NULL DEFAULT 0,
      status          TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','delivered','buffered','failed','expired')),
      created_at      TEXT NOT NULL,
      delivered_at    TEXT,
      FOREIGN KEY (session_id) REFERENCES iot_nidd_sessions(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS nidd_app_servers (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      app_server_id   TEXT UNIQUE NOT NULL,
      name            TEXT NOT NULL DEFAULT '',
      callback_url    TEXT NOT NULL DEFAULT '',
      auth_token      TEXT NOT NULL DEFAULT '',
      created_at      TEXT NOT NULL
    )`,

	`CREATE INDEX IF NOT EXISTS idx_iot_cp_imsi ON iot_cp_data(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_iot_cp_pending ON iot_cp_data(delivered, direction)`,

	`CREATE INDEX IF NOT EXISTS idx_nidd_imsi ON iot_nidd_sessions(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_iot_tag_group ON iot_tags(group_id)`,

	`CREATE INDEX IF NOT EXISTS idx_iot_inv_ts ON iot_inventory_events(timestamp)`,

	`CREATE INDEX IF NOT EXISTS idx_nidd_log_sess ON nidd_data_log(session_id)`,

	`CREATE INDEX IF NOT EXISTS idx_nidd_log_status ON nidd_data_log(status)`,

	`CREATE INDEX IF NOT EXISTS idx_iot_inv_reader ON iot_inventory_events(reader_id)`,

}

var MusimDDL = []string{
	`CREATE TABLE IF NOT EXISTS musim_groups (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      device_id   TEXT UNIQUE NOT NULL,
      description TEXT,
      active_imsi TEXT,
      created_at  TEXT NOT NULL DEFAULT (datetime('now')),
      updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS musim_group_members (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      group_id    INTEGER NOT NULL,
      imsi        TEXT UNIQUE NOT NULL,
      priority    INTEGER NOT NULL DEFAULT 0,
      usim_index  INTEGER,
      joined_at   TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (group_id) REFERENCES musim_groups(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS musim_capabilities (
      id                      INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi                    TEXT UNIQUE NOT NULL,
      musim_supported         INTEGER NOT NULL DEFAULT 0,
      max_usim_count          INTEGER NOT NULL DEFAULT 2,
      min_paging_interval_ms  INTEGER NOT NULL DEFAULT 1280,
      negotiated_at           TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS musim_paging_log (
      id           INTEGER PRIMARY KEY AUTOINCREMENT,
      device_id    TEXT NOT NULL,
      source_imsi  TEXT,
      target_imsi  TEXT NOT NULL,
      reason       TEXT,
      outcome      TEXT NOT NULL DEFAULT 'delivered'
                   CHECK (outcome IN ('delivered','switched','timeout','rejected')),
      created_at   TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_musim_grp_device ON musim_groups(device_id)`,

	`CREATE INDEX IF NOT EXISTS idx_musim_mbr_grp ON musim_group_members(group_id)`,

	`CREATE INDEX IF NOT EXISTS idx_musim_mbr_imsi ON musim_group_members(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_musim_cap_imsi ON musim_capabilities(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_musim_pg_dev ON musim_paging_log(device_id)`,

	`CREATE INDEX IF NOT EXISTS idx_musim_pg_ts ON musim_paging_log(created_at)`,

}

var N26DDL = []string{
	`CREATE TABLE IF NOT EXISTS n26_handover_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      source_rat      TEXT NOT NULL CHECK (source_rat IN ('4G', '5G')),
      target_rat      TEXT NOT NULL CHECK (target_rat IN ('4G', '5G')),
      source_ue_id    INTEGER,
      target_ue_id    INTEGER,
      timestamp       TEXT NOT NULL DEFAULT (datetime('now')),
      status          TEXT NOT NULL DEFAULT 'initiated'
                      CHECK (status IN ('initiated', 'completed', 'failed'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_n26_ho_imsi ON n26_handover_log(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_n26_ho_ts ON n26_handover_log(timestamp)`,

}

var NsacfDDL = []string{
	`CREATE TABLE IF NOT EXISTS nsacf_slice_limits (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      sst                 INTEGER NOT NULL,
      sd                  TEXT NOT NULL DEFAULT '000000',
      max_ues             INTEGER NOT NULL DEFAULT 1000,
      reserved_ues        INTEGER NOT NULL DEFAULT 0,
      priority_threshold  INTEGER NOT NULL DEFAULT 0,
      preemption_enabled  INTEGER NOT NULL DEFAULT 0,
      UNIQUE(sst, sd)
    )`,

	`CREATE TABLE IF NOT EXISTS nsacf_admissions (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi                TEXT NOT NULL,
      sst                 INTEGER NOT NULL,
      sd                  TEXT NOT NULL DEFAULT '000000',
      priority            INTEGER NOT NULL DEFAULT 0,
      admitted_at         TEXT NOT NULL DEFAULT (datetime('now')),
      UNIQUE(imsi, sst, sd)
    )`,

	`CREATE TABLE IF NOT EXISTS nsacf_admission_log (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi                TEXT NOT NULL,
      sst                 INTEGER NOT NULL,
      sd                  TEXT NOT NULL DEFAULT '000000',
      action              TEXT NOT NULL
                          CHECK (action IN ('admitted','denied','released','preempted')),
      reason              TEXT,
      created_at          TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS nsacf_ue_slice_mbr (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi                TEXT NOT NULL,
      sst                 INTEGER NOT NULL,
      sd                  TEXT NOT NULL DEFAULT '000000',
      mbr_dl_kbps         INTEGER NOT NULL DEFAULT 0,
      mbr_ul_kbps         INTEGER NOT NULL DEFAULT 0,
      current_dl_kbps     INTEGER NOT NULL DEFAULT 0,
      current_ul_kbps     INTEGER NOT NULL DEFAULT 0,
      UNIQUE(imsi, sst, sd)
    )`,

	`CREATE INDEX IF NOT EXISTS idx_nsacf_adm_slice ON nsacf_admissions(sst, sd)`,

	`CREATE INDEX IF NOT EXISTS idx_nsacf_adm_imsi ON nsacf_admissions(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_nsacf_log_slice ON nsacf_admission_log(sst, sd)`,

	`CREATE INDEX IF NOT EXISTS idx_nsacf_log_imsi ON nsacf_admission_log(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_nsacf_mbr_imsi ON nsacf_ue_slice_mbr(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_nsacf_mbr_slice ON nsacf_ue_slice_mbr(sst, sd)`,

}

var NwdafDDL = []string{
	`CREATE TABLE IF NOT EXISTS nwdaf_data_points (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      source_nf       TEXT NOT NULL,
      analytics_id    TEXT NOT NULL,
      imsi            TEXT,
      dnn             TEXT,
      sst             TEXT,
      data_json       TEXT NOT NULL,
      collected_at    TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS nwdaf_analytics (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      analytics_id    TEXT NOT NULL,
      target_period   TEXT NOT NULL,
      scope_json      TEXT,
      result_json     TEXT NOT NULL,
      confidence      REAL,
      computed_at     TEXT NOT NULL DEFAULT (datetime('now')),
      valid_until     TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS nwdaf_subscriptions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      sub_id          TEXT UNIQUE NOT NULL,
      consumer_nf     TEXT NOT NULL,
      analytics_id    TEXT NOT NULL,
      target_imsi     TEXT,
      target_dnn      TEXT,
      target_sst      TEXT,
      callback_url    TEXT,
      interval_sec    INTEGER NOT NULL DEFAULT 60,
      status          TEXT NOT NULL DEFAULT 'active',
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      last_notified   TEXT
    )`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_dp_ts ON nwdaf_data_points(collected_at)`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_dp_aid ON nwdaf_data_points(analytics_id)`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_dp_imsi ON nwdaf_data_points(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_an_aid ON nwdaf_analytics(analytics_id)`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_sub_aid ON nwdaf_subscriptions(analytics_id)`,

}

var ExposureDDL = []string{
	`CREATE TABLE IF NOT EXISTS nwdaf_exposure_consumers (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      name              TEXT NOT NULL UNIQUE,
      callback_url      TEXT,
      api_key           TEXT UNIQUE,
      allowed_analytics TEXT,
      active            INTEGER NOT NULL DEFAULT 1,
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	// target_type follows TS 23.288 §6.2.2.2 / TS 29.520 §5.3.2
	// targetOfAnalyticsReporting: an analytics subscription scope is
	// either a UE (imsi), a slice (S-NSSAI), an NF instance (nf), an
	// NF set (nf_set), an area of interest (area), or network-wide.
	`CREATE TABLE IF NOT EXISTS nwdaf_exposure_subscriptions (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      consumer_id       INTEGER NOT NULL REFERENCES nwdaf_exposure_consumers(id) ON DELETE CASCADE,
      analytics_type    TEXT NOT NULL,
      target_type       TEXT NOT NULL CHECK(target_type IN ('imsi','slice','network','nf','nf_set','area')),
      target_id         TEXT,
      interval_s        INTEGER NOT NULL DEFAULT 60,
      callback_url      TEXT,
      active            INTEGER NOT NULL DEFAULT 1,
      last_notified_at  TEXT,
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS nwdaf_exposure_log (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      consumer_id       INTEGER,
      analytics_type    TEXT,
      query_type        TEXT NOT NULL CHECK(query_type IN ('subscription','one_shot')),
      response_code     INTEGER,
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_exp_cons_key ON nwdaf_exposure_consumers(api_key)`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_exp_sub_cid ON nwdaf_exposure_subscriptions(consumer_id)`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_exp_sub_act ON nwdaf_exposure_subscriptions(active)`,

	// TS 23.288 §6.2.9 — User consent for UE-targeted analytics
	// exposure. The NEF must gate exposure to AFs on consent recorded
	// per (consumer, SUPI). The default-policy row in
	// nwdaf_consent_policy controls behaviour when no per-UE row
	// exists ('opt_in' = default-deny, 'opt_out' = default-allow).
	`CREATE TABLE IF NOT EXISTS nwdaf_user_consent (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      consumer_id   INTEGER NOT NULL REFERENCES nwdaf_exposure_consumers(id) ON DELETE CASCADE,
      supi          TEXT NOT NULL,
      allow         INTEGER NOT NULL DEFAULT 1,
      reason        TEXT NOT NULL DEFAULT '',
      recorded_at   TEXT NOT NULL DEFAULT (datetime('now')),
      UNIQUE(consumer_id, supi)
    )`,
	`CREATE INDEX IF NOT EXISTS idx_nwdaf_consent_supi ON nwdaf_user_consent(supi)`,

	`CREATE TABLE IF NOT EXISTS nwdaf_consent_policy (
      id          INTEGER PRIMARY KEY,
      mode        TEXT NOT NULL DEFAULT 'opt_in'
                  CHECK(mode IN ('opt_in','opt_out')),
      updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_exp_log_ts ON nwdaf_exposure_log(created_at)`,

	`CREATE INDEX IF NOT EXISTS idx_nwdaf_exposure_log_consumer_id ON nwdaf_exposure_log(consumer_id)`,

}

var UrspDDL = []string{
	`CREATE TABLE IF NOT EXISTS ursp_rules (
        id          INTEGER PRIMARY KEY AUTOINCREMENT,
        imsi        TEXT,
        precedence  INTEGER NOT NULL,
        description TEXT,
        enabled     INTEGER NOT NULL DEFAULT 1,
        created_at  TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS ursp_traffic_descriptors (
        id          INTEGER PRIMARY KEY AUTOINCREMENT,
        rule_id     INTEGER NOT NULL,
        match_type  TEXT NOT NULL CHECK (match_type IN (
                        'app_id', 'ip_3tuple', 'dnn', 'fqdn',
                        'conn_cap', 'domain')),
        match_value TEXT NOT NULL,
        FOREIGN KEY (rule_id) REFERENCES ursp_rules(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS ursp_route_descriptors (
        id               INTEGER PRIMARY KEY AUTOINCREMENT,
        rule_id          INTEGER NOT NULL,
        precedence       INTEGER NOT NULL DEFAULT 0,
        sst              INTEGER,
        sd               TEXT,
        dnn              TEXT,
        pdu_session_type TEXT CHECK (pdu_session_type IN (
                            'IPv4', 'IPv6', 'IPv4v6', 'Unstructured')),
        access_type      TEXT NOT NULL DEFAULT 'any'
                            CHECK (access_type IN ('3GPP', 'non-3GPP', 'any')),
        FOREIGN KEY (rule_id) REFERENCES ursp_rules(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_ursp_rules_imsi ON ursp_rules(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_ursp_rules_prec ON ursp_rules(precedence)`,

	`CREATE INDEX IF NOT EXISTS idx_ursp_td_rule ON ursp_traffic_descriptors(rule_id)`,

	`CREATE INDEX IF NOT EXISTS idx_ursp_rd_rule ON ursp_route_descriptors(rule_id)`,

}

var SmsfDDL = []string{
	`CREATE TABLE IF NOT EXISTS sms_messages (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      sender_imsi       TEXT NOT NULL,
      sender_msisdn     TEXT,
      recipient_msisdn  TEXT NOT NULL,
      direction         TEXT NOT NULL CHECK (direction IN ('MO','MT')),
      tp_da             TEXT,
      tp_oa             TEXT,
      tp_ud             TEXT,
      encoding          TEXT NOT NULL DEFAULT 'gsm7',
      status            TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','delivered','failed','expired')),
      segments          INTEGER NOT NULL DEFAULT 1,
      reference         INTEGER,
      created_at        TEXT NOT NULL DEFAULT (datetime('now')),
      delivered_at      TEXT
    )`,

	`CREATE INDEX IF NOT EXISTS idx_sms_sender
      ON sms_messages(sender_imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_sms_recipient
      ON sms_messages(recipient_msisdn)`,

	`CREATE INDEX IF NOT EXISTS idx_sms_status
      ON sms_messages(status)`,

	`CREATE TABLE IF NOT EXISTS sms_routing (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      msisdn_pattern  TEXT NOT NULL,
      route_type      TEXT NOT NULL CHECK (route_type IN ('local','smsc','forward')),
      destination     TEXT,
      priority        INTEGER NOT NULL DEFAULT 0
    )`,

	`CREATE INDEX IF NOT EXISTS idx_sms_routing_pattern
      ON sms_routing(msisdn_pattern)`,

}

var AiDDL = []string{
	`CREATE TABLE IF NOT EXISTS ai_config (
      id                INTEGER PRIMARY KEY CHECK (id = 1),
      active_provider   TEXT NOT NULL DEFAULT 'local',
      local_endpoint    TEXT NOT NULL DEFAULT 'http://localhost:11434',
      local_model       TEXT NOT NULL DEFAULT 'llama3.2',
      anthropic_api_key  TEXT,
      anthropic_model    TEXT NOT NULL DEFAULT 'claude-sonnet-4-20250514',
      openai_api_key    TEXT,
      openai_model      TEXT NOT NULL DEFAULT 'gpt-4o',
      gemini_api_key    TEXT,
      gemini_model      TEXT NOT NULL DEFAULT 'gemini-2.5-flash',
      custom_endpoint   TEXT,
      custom_api_key    TEXT,
      custom_model      TEXT,
      max_tokens        INTEGER NOT NULL DEFAULT 4096,
      temperature       REAL NOT NULL DEFAULT 0.3,
      system_prompt     TEXT NOT NULL DEFAULT 'You are a 5G/4G SA Core network expert assistant. You help operators troubleshoot issues, analyze logs, explain anomalies, and optimize network configuration. You have deep knowledge of 3GPP specifications (TS 23.501, TS 24.501, TS 33.501, TS 38.413, etc.) and the SA Core architecture.',
      rag_enabled       INTEGER NOT NULL DEFAULT 0,
      vectorstore_path  TEXT NOT NULL DEFAULT 'vectorstore.db'
    )`,

	`CREATE TABLE IF NOT EXISTS ai_conversations (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      session_id      TEXT NOT NULL,
      role            TEXT NOT NULL CHECK (role IN ('system','user','assistant')),
      content         TEXT NOT NULL,
      provider        TEXT,
      model           TEXT,
      tokens_used     INTEGER,
      latency_ms      INTEGER,
      timestamp       TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_ai_conv_session ON ai_conversations(session_id)`,

	`CREATE INDEX IF NOT EXISTS idx_ai_conv_ts ON ai_conversations(timestamp)`,

}

var TraceDDL = []string{
	`CREATE TABLE IF NOT EXISTS trace_sessions (
      trace_ref       TEXT PRIMARY KEY,
      imsi            TEXT,
      gnb_ip          TEXT,
      depth           TEXT NOT NULL DEFAULT 'medium'
                      CHECK (depth IN ('minimum','medium','maximum')),
      interfaces      TEXT NOT NULL DEFAULT 'N1,N2',
      duration_sec    INTEGER NOT NULL DEFAULT 600,
      status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','completed','stopped')),
      started_at      TEXT NOT NULL DEFAULT (datetime('now')),
      stopped_at      TEXT,
      record_count    INTEGER NOT NULL DEFAULT 0
    )`,

	`CREATE TABLE IF NOT EXISTS trace_records (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      trace_ref       TEXT NOT NULL,
      timestamp       TEXT NOT NULL DEFAULT (datetime('now')),
      interface       TEXT NOT NULL,
      direction       TEXT NOT NULL,
      msg_type        TEXT NOT NULL,
      msg_code        INTEGER,
      imsi            TEXT,
      summary         TEXT,
      hex_dump        TEXT,
      latency_us      INTEGER,
      FOREIGN KEY (trace_ref) REFERENCES trace_sessions(trace_ref) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_trace_rec_ref ON trace_records(trace_ref)`,

	`CREATE INDEX IF NOT EXISTS idx_trace_rec_ts ON trace_records(timestamp)`,

	// trace_correlation — operator bridge from one transport identifier
	// (IMSI, AMF-UE-NGAP-ID, SEID, OTEL trace_id, SBI 3gpp-Sbi-
	// Correlation-Info) to all the others tied to the same UE call.
	// Lets the panel pivot from an NGAP capture to its SBI fan-out and
	// PFCP control-plane events without a join graph in the GUI.
	`CREATE TABLE IF NOT EXISTS trace_correlation (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      call_id         TEXT NOT NULL UNIQUE,
      imsi            TEXT,
      amf_ue_ngap_id  INTEGER,
      ran_ue_ngap_id  INTEGER,
      gnb_id          TEXT,
      pdu_session_id  INTEGER,
      seid_up         INTEGER,
      seid_cp         INTEGER,
      teid_dl         INTEGER,
      teid_ul         INTEGER,
      otel_trace_id   TEXT,
      ngap_trace_ref  TEXT,
      sbi_corr_id     TEXT,
      started_at      TEXT NOT NULL DEFAULT (datetime('now')),
      updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_trace_corr_imsi ON trace_correlation(imsi)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_corr_amf  ON trace_correlation(amf_ue_ngap_id)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_corr_seid ON trace_correlation(seid_up)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_corr_otel ON trace_correlation(otel_trace_id)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_corr_sbi  ON trace_correlation(sbi_corr_id)`,
}

var DrDDL = []string{
	`CREATE TABLE IF NOT EXISTS disaster_declarations (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      name            TEXT NOT NULL,
      reason          TEXT NOT NULL DEFAULT '',
      affected_areas  TEXT NOT NULL DEFAULT '',
      status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','ended')),
      declared_at     TEXT NOT NULL DEFAULT (datetime('now')),
      ended_at        TEXT NOT NULL DEFAULT '',
      declared_by     TEXT NOT NULL DEFAULT ''
    )`,

	`CREATE TABLE IF NOT EXISTS disaster_roaming_ues (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      declaration_id  INTEGER NOT NULL
                      REFERENCES disaster_declarations(id) ON DELETE CASCADE,
      imsi            TEXT NOT NULL,
      hplmn           TEXT NOT NULL DEFAULT '',
      connected_at    TEXT NOT NULL DEFAULT (datetime('now')),
      services_used   TEXT NOT NULL DEFAULT '',
      UNIQUE(declaration_id, imsi)
    )`,

	`CREATE TABLE IF NOT EXISTS disaster_roaming_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      hplmn           TEXT NOT NULL DEFAULT '',
      action          TEXT NOT NULL CHECK (action IN ('admitted','denied','released')),
      reason          TEXT NOT NULL DEFAULT '',
      created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_disaster_status ON disaster_declarations(status)`,

	`CREATE INDEX IF NOT EXISTS idx_dr_ues_decl ON disaster_roaming_ues(declaration_id)`,

	`CREATE INDEX IF NOT EXISTS idx_dr_log_ts ON disaster_roaming_log(created_at)`,

}

var EmergencyDDL = []string{
	`CREATE TABLE IF NOT EXISTS emergency_config (
      id                INTEGER PRIMARY KEY CHECK (id = 1),
      enabled           INTEGER NOT NULL DEFAULT 1,
      auth_required     INTEGER NOT NULL DEFAULT 0,
      emergency_dnn     TEXT NOT NULL DEFAULT 'sos',
      ip_pool_v4        TEXT NOT NULL DEFAULT '10.99.0.0/24',
      ip_pool_v6        TEXT NOT NULL DEFAULT '',
      psap_sip_uri      TEXT NOT NULL DEFAULT '',
      psap_ip           TEXT NOT NULL DEFAULT '',
      psap_port         INTEGER NOT NULL DEFAULT 5060,
      emergency_qfi     INTEGER NOT NULL DEFAULT 5,
      voice_qfi         INTEGER NOT NULL DEFAULT 1,
      arp_priority      INTEGER NOT NULL DEFAULT 1,
      max_sessions      INTEGER NOT NULL DEFAULT 100
    )`,

	`CREATE TABLE IF NOT EXISTS emergency_sessions (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi              TEXT,
      imei              TEXT,
      pdu_session_id    INTEGER,
      ip_addr           TEXT,
      gnb_ip            TEXT,
      tac               TEXT,
      cell_id           TEXT,
      start_time        TEXT NOT NULL DEFAULT (datetime('now')),
      end_time          TEXT,
      status            TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active','released','failed')),
      called_number     TEXT
    )`,

	`CREATE INDEX IF NOT EXISTS idx_emerg_sess_status ON emergency_sessions(status)`,

}

var IopsDDL = []string{
	`CREATE TABLE IF NOT EXISTS iops_config (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      gnb_id              TEXT NOT NULL UNIQUE,
      iops_enabled        INTEGER NOT NULL DEFAULT 1,
      local_auth_enabled  INTEGER NOT NULL DEFAULT 1,
      max_local_ues       INTEGER NOT NULL DEFAULT 100,
      local_ip_pool       TEXT NOT NULL DEFAULT '10.99.0.0/24',
      local_services_json TEXT,
      created_at          TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS iops_events (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      gnb_id      TEXT NOT NULL,
      event_type  TEXT NOT NULL
                  CHECK (event_type IN (
                      'backhaul_lost', 'iops_activated',
                      'restoring', 'restored', 'failed')),
      reason      TEXT,
      created_at  TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS iops_cached_credentials (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      gnb_id          TEXT NOT NULL,
      imsi            TEXT NOT NULL,
      rand_hex        TEXT NOT NULL,
      autn_hex        TEXT NOT NULL,
      xres_star_hex   TEXT NOT NULL,
      kseaf_hex       TEXT NOT NULL,
      expires_at      TEXT NOT NULL,
      UNIQUE (gnb_id, imsi)
    )`,

	`CREATE TABLE IF NOT EXISTS iops_local_sessions (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      gnb_id        TEXT NOT NULL,
      imsi          TEXT NOT NULL,
      service_type  TEXT NOT NULL
                    CHECK (service_type IN ('voice', 'data', 'ptt', 'emergency')),
      ip_address    TEXT,
      status        TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'released')),
      created_at    TEXT NOT NULL DEFAULT (datetime('now')),
      released_at   TEXT
    )`,

	`CREATE INDEX IF NOT EXISTS idx_iops_events_gnb ON iops_events(gnb_id)`,

	`CREATE INDEX IF NOT EXISTS idx_iops_creds_gnb ON iops_cached_credentials(gnb_id)`,

	`CREATE INDEX IF NOT EXISTS idx_iops_sessions_gnb ON iops_local_sessions(gnb_id)`,

	`CREATE INDEX IF NOT EXISTS idx_iops_sessions_status ON iops_local_sessions(status)`,

}

var MbsDDL = []string{
	`CREATE TABLE IF NOT EXISTS mbs_areas (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      name            TEXT NOT NULL UNIQUE,
      tracking_areas  TEXT NOT NULL,
      description     TEXT,
      created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS mbs_sessions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      tmgi            TEXT NOT NULL UNIQUE,
      name            TEXT,
      session_type    TEXT NOT NULL DEFAULT 'multicast'
                      CHECK (session_type IN ('multicast', 'broadcast')),
      status          TEXT NOT NULL DEFAULT 'created'
                      CHECK (status IN ('created', 'activated', 'deactivated')),
      qos_5qi         INTEGER NOT NULL DEFAULT 9,
      area_id         INTEGER,
      max_bitrate_kbps INTEGER,
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      activated_at    TEXT,
      FOREIGN KEY (area_id) REFERENCES mbs_areas(id) ON DELETE SET NULL
    )`,

	`CREATE TABLE IF NOT EXISTS mbs_members (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      session_id      INTEGER NOT NULL,
      imsi            TEXT NOT NULL,
      joined_at       TEXT NOT NULL DEFAULT (datetime('now')),
      left_at         TEXT,
      UNIQUE (session_id, imsi),
      FOREIGN KEY (session_id) REFERENCES mbs_sessions(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS mbs_content_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      session_id      INTEGER NOT NULL,
      content_type    TEXT NOT NULL,
      content_size    INTEGER NOT NULL DEFAULT 0,
      scheduled_at    TEXT,
      delivered_at    TEXT,
      recipients_count INTEGER NOT NULL DEFAULT 0,
      status          TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'delivering', 'delivered', 'failed')),
      FOREIGN KEY (session_id) REFERENCES mbs_sessions(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_mbs_members_session ON mbs_members(session_id)`,

	`CREATE INDEX IF NOT EXISTS idx_mbs_members_imsi ON mbs_members(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_mbs_content_session ON mbs_content_log(session_id)`,

	`CREATE INDEX IF NOT EXISTS idx_mbs_sessions_area_id ON mbs_sessions(area_id)`,

}

var PwsDDL = []string{
	`CREATE TABLE IF NOT EXISTS pws_alerts (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      message_id          INTEGER NOT NULL,
      serial_number       INTEGER NOT NULL,
      alert_type          TEXT NOT NULL DEFAULT 'cmas'
                          CHECK (alert_type IN ('etws', 'cmas', 'eu_alert', 'test')),
      severity            TEXT NOT NULL DEFAULT 'unknown'
                          CHECK (severity IN ('extreme', 'severe', 'moderate', 'minor', 'unknown')),
      urgency             TEXT NOT NULL DEFAULT 'unknown'
                          CHECK (urgency IN ('immediate', 'expected', 'future', 'past', 'unknown')),
      category            TEXT NOT NULL DEFAULT 'safety',
      message_text        TEXT NOT NULL DEFAULT '',
      language            TEXT NOT NULL DEFAULT 'en',
      repetition_period_s INTEGER NOT NULL DEFAULT 60,
      number_of_broadcasts INTEGER NOT NULL DEFAULT 10,
      target_areas        TEXT,
      status              TEXT NOT NULL DEFAULT 'draft'
                          CHECK (status IN ('draft', 'broadcasting', 'completed', 'cancelled')),
      created_at          TEXT NOT NULL DEFAULT (datetime('now')),
      broadcast_at        TEXT,
      completed_at        TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS pws_delivery_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      alert_id        INTEGER NOT NULL,
      gnb_id          TEXT NOT NULL,
      status          TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'delivered', 'failed', 'acknowledged')),
      delivered_at    TEXT,
      ack_at          TEXT,
      FOREIGN KEY (alert_id) REFERENCES pws_alerts(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_pws_alerts_status ON pws_alerts(status)`,

	`CREATE INDEX IF NOT EXISTS idx_pws_alerts_type ON pws_alerts(alert_type)`,

	`CREATE INDEX IF NOT EXISTS idx_pws_delivery_alert ON pws_delivery_log(alert_id)`,

	`CREATE INDEX IF NOT EXISTS idx_pws_delivery_gnb ON pws_delivery_log(gnb_id)`,

}

var RacsDDL = []string{
	`CREATE TABLE IF NOT EXISTS racs_config (
      id                INTEGER PRIMARY KEY CHECK (id = 1),
      restriction_level TEXT NOT NULL DEFAULT 'normal'
                        CHECK (restriction_level IN (
                            'normal','restricted','emergency_only','full_lockdown')),
      reason            TEXT NOT NULL DEFAULT '',
      affected_areas    TEXT NOT NULL DEFAULT '',
      activated_at      TEXT NOT NULL DEFAULT '',
      activated_by      TEXT NOT NULL DEFAULT ''
    )`,

	`CREATE TABLE IF NOT EXISTS racs_barring_config (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      access_category   INTEGER NOT NULL UNIQUE,
      barring_factor    REAL NOT NULL DEFAULT 1.0,
      barring_time_s    INTEGER NOT NULL DEFAULT 320,
      enabled           INTEGER NOT NULL DEFAULT 0
    )`,

	`CREATE TABLE IF NOT EXISTS racs_access_log (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi              TEXT NOT NULL,
      access_category   INTEGER NOT NULL,
      restriction_level TEXT NOT NULL,
      decision          TEXT NOT NULL CHECK (decision IN ('allowed','barred')),
      reason            TEXT NOT NULL DEFAULT '',
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_racs_log_imsi ON racs_access_log(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_racs_log_ts ON racs_access_log(created_at)`,

}

var DpiDDL = []string{
	`CREATE TABLE IF NOT EXISTS dpi_applications (
      app_id          TEXT PRIMARY KEY,
      app_name        TEXT NOT NULL,
      category        TEXT NOT NULL DEFAULT 'general',
      qos_profile     TEXT,
      charging_profile TEXT,
      priority        INTEGER NOT NULL DEFAULT 100,
      enabled         INTEGER NOT NULL DEFAULT 1
    )`,

	`CREATE TABLE IF NOT EXISTS dpi_pfd_rules (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      app_id          TEXT NOT NULL,
      detection_type  TEXT NOT NULL
                      CHECK (detection_type IN ('sni','dns','ip_range','host','port_range')),
      pattern         TEXT NOT NULL,
      FOREIGN KEY (app_id) REFERENCES dpi_applications(app_id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS dpi_detection_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      app_id          TEXT NOT NULL,
      pdu_session_id  INTEGER,
      bytes_ul        INTEGER NOT NULL DEFAULT 0,
      bytes_dl        INTEGER NOT NULL DEFAULT 0,
      first_seen      TEXT NOT NULL DEFAULT (datetime('now')),
      last_seen       TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS dpi_dns_cache (
      domain          TEXT NOT NULL,
      resolved_ip     TEXT NOT NULL,
      app_id          TEXT,
      cached_at       TEXT NOT NULL DEFAULT (datetime('now')),
      ttl_sec         INTEGER NOT NULL DEFAULT 300,
      PRIMARY KEY (domain, resolved_ip)
    )`,

	`CREATE INDEX IF NOT EXISTS idx_dpi_pfd_app ON dpi_pfd_rules(app_id)`,

	`CREATE INDEX IF NOT EXISTS idx_dpi_det_imsi ON dpi_detection_log(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_dpi_det_app ON dpi_detection_log(app_id)`,

}

var LiDDL = []string{
	`CREATE TABLE IF NOT EXISTS li_warrants (
      warrant_id      TEXT PRIMARY KEY,
      authority       TEXT NOT NULL,
      case_reference  TEXT NOT NULL,
      target_imsi     TEXT NOT NULL,
      target_msisdn   TEXT,
      scope           TEXT NOT NULL DEFAULT 'iri'
                      CHECK (scope IN ('iri','cc','iri+cc')),
      start_time      TEXT NOT NULL DEFAULT (datetime('now')),
      end_time        TEXT NOT NULL,
      status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','expired','revoked')),
      mdf_endpoint    TEXT,
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      created_by      TEXT NOT NULL
    )`,

	`CREATE TABLE IF NOT EXISTS li_iri_events (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      warrant_id      TEXT NOT NULL,
      event_type      TEXT NOT NULL,
      target_imsi     TEXT NOT NULL,
      event_data      TEXT NOT NULL,
      timestamp       TEXT NOT NULL DEFAULT (datetime('now')),
      delivered       INTEGER NOT NULL DEFAULT 0,
      FOREIGN KEY (warrant_id) REFERENCES li_warrants(warrant_id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS li_cc_sessions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      warrant_id      TEXT NOT NULL,
      target_imsi     TEXT NOT NULL,
      session_type    TEXT NOT NULL DEFAULT 'data',
      pdu_session_id  INTEGER,
      call_id         TEXT,
      status          TEXT NOT NULL DEFAULT 'active',
      started_at      TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (warrant_id) REFERENCES li_warrants(warrant_id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS li_audit_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      action          TEXT NOT NULL,
      warrant_id      TEXT,
      operator        TEXT NOT NULL,
      detail          TEXT,
      timestamp       TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_li_cc_sessions_warrant_id ON li_cc_sessions(warrant_id)`,

	`CREATE INDEX IF NOT EXISTS idx_li_warrant_imsi ON li_warrants(target_imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_li_iri_warrant ON li_iri_events(warrant_id)`,

	`CREATE INDEX IF NOT EXISTS idx_li_iri_ts ON li_iri_events(timestamp)`,

	`CREATE INDEX IF NOT EXISTS idx_li_audit_ts ON li_audit_log(timestamp)`,

}

var NpnDDL = []string{
	`CREATE TABLE IF NOT EXISTS npn_networks (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      name        TEXT NOT NULL,
      npn_type    TEXT NOT NULL CHECK (npn_type IN ('SNPN','PNI-NPN')),
      plmn        TEXT NOT NULL,
      nid         TEXT,
      description TEXT,
      status      TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive')),
      config_json TEXT,
      created_at  TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS npn_cag (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      cag_id      TEXT NOT NULL UNIQUE,
      npn_id      INTEGER NOT NULL,
      name        TEXT NOT NULL,
      description TEXT,
      created_at  TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (npn_id) REFERENCES npn_networks(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS npn_cag_members (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      cag_id      INTEGER NOT NULL,
      imsi        TEXT NOT NULL,
      authorized  INTEGER NOT NULL DEFAULT 1,
      added_at    TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (cag_id) REFERENCES npn_cag(id) ON DELETE CASCADE,
      UNIQUE (cag_id, imsi)
    )`,

	`CREATE TABLE IF NOT EXISTS npn_access_log (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi        TEXT NOT NULL,
      npn_id      INTEGER,
      cag_id      INTEGER,
      action      TEXT NOT NULL CHECK (action IN ('admitted','denied','removed')),
      reason      TEXT,
      created_at  TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_npn_type ON npn_networks(npn_type)`,

	`CREATE INDEX IF NOT EXISTS idx_npn_status ON npn_networks(status)`,

	`CREATE INDEX IF NOT EXISTS idx_npn_cag_npn ON npn_cag(npn_id)`,

	`CREATE INDEX IF NOT EXISTS idx_npn_cag_members_cag_id ON npn_cag_members(cag_id)`,

	`CREATE INDEX IF NOT EXISTS idx_npn_cag_mbr_imsi ON npn_cag_members(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_npn_alog_imsi ON npn_access_log(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_npn_alog_ts ON npn_access_log(created_at)`,

}

var RanSharingDDL = []string{
	`CREATE TABLE IF NOT EXISTS ran_sharing_agreements (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      name                TEXT NOT NULL,
      sharing_type        TEXT NOT NULL
                          CHECK (sharing_type IN ('MORAN', 'MOCN')),
      participating_plmns TEXT NOT NULL,
      capacity_split_json TEXT,
      priority_rules_json TEXT,
      status              TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('active', 'inactive', 'pending')),
      created_at          TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS ran_sharing_gnb_map (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      agreement_id        INTEGER NOT NULL,
      gnb_id              TEXT NOT NULL,
      allocated_capacity_pct INTEGER NOT NULL DEFAULT 0,
      UNIQUE (agreement_id, gnb_id),
      FOREIGN KEY (agreement_id) REFERENCES ran_sharing_agreements(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS ran_sharing_usage_log (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      agreement_id        INTEGER,
      plmn                TEXT NOT NULL,
      gnb_id              TEXT NOT NULL,
      ue_count            INTEGER NOT NULL DEFAULT 0,
      throughput_mbps      REAL NOT NULL DEFAULT 0.0,
      timestamp           TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (agreement_id) REFERENCES ran_sharing_agreements(id) ON DELETE SET NULL
    )`,

	`CREATE INDEX IF NOT EXISTS idx_rs_gnb_agreement ON ran_sharing_gnb_map(agreement_id)`,

	`CREATE INDEX IF NOT EXISTS idx_rs_usage_agreement ON ran_sharing_usage_log(agreement_id)`,

	`CREATE INDEX IF NOT EXISTS idx_rs_usage_ts ON ran_sharing_usage_log(timestamp)`,

}

var EsimDDL = []string{
	`CREATE TABLE IF NOT EXISTS esim_profiles (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      iccid             TEXT UNIQUE NOT NULL,
      imsi              TEXT NOT NULL,
      eid               TEXT,
      profile_state     TEXT NOT NULL DEFAULT 'available'
                        CHECK (profile_state IN (
                            'available','reserved','downloaded',
                            'installed','enabled','disabled','deleted'
                        )),
      activation_code   TEXT UNIQUE,
      matching_id       TEXT UNIQUE,
      smdp_address      TEXT,
      profile_name      TEXT NOT NULL DEFAULT 'SA Core',
      profile_type      TEXT NOT NULL DEFAULT 'operational'
                        CHECK (profile_type IN ('test','operational','provisioning')),
      profile_class     TEXT NOT NULL DEFAULT 'operational'
                        CHECK (profile_class IN ('test','provisioning','operational')),
      profile_blob      BLOB,
      created_at        TEXT NOT NULL DEFAULT (datetime('now')),
      reserved_at       TEXT,
      downloaded_at     TEXT,
      installed_at      TEXT,
      FOREIGN KEY (imsi) REFERENCES ue(imsi) ON UPDATE CASCADE ON DELETE RESTRICT
    )`,

	`CREATE TABLE IF NOT EXISTS esim_euicc (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      eid               TEXT UNIQUE NOT NULL,
      device_info       TEXT,
      lpa_version       TEXT,
      euicc_info        TEXT,
      current_iccid     TEXT,
      last_contact      TEXT,
      registered_at     TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (current_iccid) REFERENCES esim_profiles(iccid)
        ON UPDATE CASCADE ON DELETE SET NULL
    )`,

	`CREATE TABLE IF NOT EXISTS esim_notifications (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      iccid             TEXT NOT NULL,
      eid               TEXT,
      seq_number        INTEGER NOT NULL DEFAULT 0,
      event_type        TEXT NOT NULL
                        CHECK (event_type IN (
                            'install','enable','disable','delete','download'
                        )),
      result_code       INTEGER NOT NULL DEFAULT 0,
      timestamp         TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS esim_iccid_counter (
      id                INTEGER PRIMARY KEY CHECK (id = 1),
      issuer_id         TEXT NOT NULL DEFAULT '8901',
      next_sequence     INTEGER NOT NULL DEFAULT 1
    )`,

	`CREATE INDEX IF NOT EXISTS idx_esim_prof_imsi ON esim_profiles(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_esim_prof_eid ON esim_profiles(eid)`,

	`CREATE INDEX IF NOT EXISTS idx_esim_prof_state ON esim_profiles(profile_state)`,

	`CREATE INDEX IF NOT EXISTS idx_esim_prof_ac ON esim_profiles(activation_code)`,

	`CREATE INDEX IF NOT EXISTS idx_esim_euicc_eid ON esim_euicc(eid)`,

	`CREATE INDEX IF NOT EXISTS idx_esim_notif_iccid ON esim_notifications(iccid)`,

	`CREATE INDEX IF NOT EXISTS idx_esim_euicc_current_iccid ON esim_euicc(current_iccid)`,

}

var McxDDL = []string{

	`CREATE TABLE IF NOT EXISTS mcx_user_profiles (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      ue_id       INTEGER NOT NULL,
      mcptt_id    TEXT UNIQUE NOT NULL,
      display_name TEXT NOT NULL,
      priority    INTEGER NOT NULL DEFAULT 5,
      org         TEXT,
      role        TEXT DEFAULT 'user',
      enabled     INTEGER NOT NULL DEFAULT 1,
      created_at  TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (ue_id) REFERENCES ue(id) ON UPDATE CASCADE ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS mcx_groups (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      name          TEXT UNIQUE NOT NULL,
      group_type    TEXT NOT NULL DEFAULT 'normal' CHECK (group_type IN ('normal','broadcast','emergency')),
      max_members   INTEGER NOT NULL DEFAULT 50,
      encryption_key TEXT,
      priority      INTEGER NOT NULL DEFAULT 5,
      enabled       INTEGER NOT NULL DEFAULT 1,
      created_at    TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS mcx_group_members (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      group_id    INTEGER NOT NULL,
      mcptt_id    TEXT NOT NULL,
      role        TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin','dispatcher','member')),
      joined_at   TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (group_id) REFERENCES mcx_groups(id) ON UPDATE CASCADE ON DELETE CASCADE,
      FOREIGN KEY (mcptt_id) REFERENCES mcx_user_profiles(mcptt_id) ON UPDATE CASCADE ON DELETE CASCADE,
      UNIQUE (group_id, mcptt_id)
    )`,

	`CREATE TABLE IF NOT EXISTS mcx_active_calls (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      call_id       TEXT UNIQUE NOT NULL,
      call_type     TEXT NOT NULL CHECK (call_type IN ('group','private','emergency','broadcast')),
      originator    TEXT NOT NULL,
      group_id      INTEGER,
      participants  TEXT NOT NULL DEFAULT '[]',
      state         TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','active','ended')),
      priority      INTEGER NOT NULL DEFAULT 5,
      floor_holder  TEXT,
      rtp_port_a    INTEGER,
      rtp_port_b    INTEGER,
      started_at    TEXT NOT NULL DEFAULT (datetime('now')),
      ended_at      TEXT,
      FOREIGN KEY (group_id) REFERENCES mcx_groups(id) ON UPDATE CASCADE ON DELETE SET NULL
    )`,

	`CREATE TABLE IF NOT EXISTS mcx_messages (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      message_id  TEXT UNIQUE NOT NULL,
      sender      TEXT NOT NULL,
      recipient   TEXT,
      group_id    INTEGER,
      msg_type    TEXT NOT NULL DEFAULT 'sds' CHECK (msg_type IN ('sds','file')),
      content     TEXT,
      file_name   TEXT,
      file_size   INTEGER,
      file_path   TEXT,
      delivered   INTEGER NOT NULL DEFAULT 0,
      created_at  TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (group_id) REFERENCES mcx_groups(id) ON UPDATE CASCADE ON DELETE SET NULL
    )`,

	`CREATE TABLE IF NOT EXISTS mcx_floor_history (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      call_id     TEXT NOT NULL,
      mcptt_id    TEXT NOT NULL,
      event       TEXT NOT NULL CHECK (event IN ('request','granted','denied','release','revoked','preempted')),
      priority    INTEGER,
      timestamp   TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (call_id) REFERENCES mcx_active_calls(call_id) ON UPDATE CASCADE ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_user_ue ON mcx_user_profiles(ue_id)`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_user_mcptt ON mcx_user_profiles(mcptt_id)`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_gm_group ON mcx_group_members(group_id)`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_gm_user ON mcx_group_members(mcptt_id)`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_call_state ON mcx_active_calls(state)`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_call_group ON mcx_active_calls(group_id)`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_msg_sender ON mcx_messages(sender)`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_msg_group ON mcx_messages(group_id)`,

	`CREATE INDEX IF NOT EXISTS idx_mcx_floor_call ON mcx_floor_history(call_id)`,

	`CREATE INDEX IF NOT EXISTS idx_nsacf_log_action ON nsacf_admission_log(action)`,

}

var NsaasDDL = []string{
	`CREATE TABLE IF NOT EXISTS nsaas_tenants (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      name          TEXT UNIQUE NOT NULL,
      contact_email TEXT,
      api_key       TEXT,
      created_at    TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS nsaas_templates (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      name          TEXT NOT NULL,
      description   TEXT,
      sst           INTEGER NOT NULL,
      sd            TEXT,
      default_dnn   TEXT,
      qos_profile   TEXT,
      sla_defaults  TEXT,
      created_at    TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS nsaas_slices (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      tenant_id         INTEGER NOT NULL,
      template_id       INTEGER NOT NULL,
      name              TEXT,
      sst               INTEGER NOT NULL,
      sd                TEXT,
      status            TEXT NOT NULL DEFAULT 'preparing'
                        CHECK (status IN (
                          'preparing','provisioned','active',
                          'modifying','decommissioning','decommissioned'
                        )),
      config_json       TEXT,
      nssai_catalog_id  INTEGER,
      created_at        TEXT NOT NULL DEFAULT (datetime('now')),
      activated_at      TEXT,
      decommissioned_at TEXT,
      FOREIGN KEY (tenant_id)   REFERENCES nsaas_tenants(id)   ON DELETE CASCADE,
      FOREIGN KEY (template_id) REFERENCES nsaas_templates(id) ON DELETE RESTRICT
    )`,

	`CREATE TABLE IF NOT EXISTS nsaas_sla (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      slice_id      INTEGER NOT NULL,
      metric        TEXT NOT NULL,
      target_value  REAL NOT NULL,
      current_value REAL,
      compliant     INTEGER NOT NULL DEFAULT 1,
      checked_at    TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (slice_id) REFERENCES nsaas_slices(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_nsaas_slices_tenant_id ON nsaas_slices(tenant_id)`,
	`CREATE INDEX IF NOT EXISTS idx_nsaas_slices_template_id ON nsaas_slices(template_id)`,
	`CREATE INDEX IF NOT EXISTS idx_nsaas_sla_slice_id ON nsaas_sla(slice_id)`,

}

var PinDDL = []string{
	`CREATE TABLE IF NOT EXISTS pin_networks (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      owner_imsi    TEXT NOT NULL,
      name          TEXT NOT NULL,
      description   TEXT,
      gateway_imsi  TEXT,
      status        TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','inactive')),
      config_json   TEXT,
      created_at    TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS pin_elements (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      pin_id        INTEGER NOT NULL,
      element_id    TEXT NOT NULL,
      element_type  TEXT NOT NULL
                    CHECK (element_type IN ('sensor','actuator','gateway','wearable')),
      protocol      TEXT NOT NULL
                    CHECK (protocol IN ('BLE','Zigbee','WiFi','Thread','NFC')),
      name          TEXT,
      status        TEXT NOT NULL DEFAULT 'disconnected'
                    CHECK (status IN ('connected','disconnected','sleeping')),
      last_seen_at  TEXT,
      created_at    TEXT NOT NULL DEFAULT (datetime('now')),
      UNIQUE (pin_id, element_id),
      FOREIGN KEY (pin_id) REFERENCES pin_networks(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS pin_data_log (
      id            INTEGER PRIMARY KEY AUTOINCREMENT,
      pin_id        INTEGER NOT NULL,
      element_id    TEXT NOT NULL,
      direction     TEXT NOT NULL
                    CHECK (direction IN ('UL','DL')),
      data_hex      TEXT,
      data_size     INTEGER NOT NULL DEFAULT 0,
      timestamp     TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (pin_id) REFERENCES pin_networks(id) ON DELETE CASCADE
    )`,

	`CREATE INDEX IF NOT EXISTS idx_pin_elements_pin_id ON pin_elements(pin_id)`,
	`CREATE INDEX IF NOT EXISTS idx_pin_data_log_pin_id ON pin_data_log(pin_id)`,

}

var ProseDDL = []string{
	`CREATE TABLE IF NOT EXISTS prose_apps (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      app_id          TEXT UNIQUE NOT NULL,
      name            TEXT NOT NULL,
      prose_app_code  TEXT NOT NULL,
      validity_hours  INTEGER NOT NULL DEFAULT 24,
      created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS prose_discovery_filters (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi        TEXT NOT NULL,
      app_id      TEXT NOT NULL,
      filter_type TEXT NOT NULL CHECK (filter_type IN ('open','restricted','model_a','model_b')),
      filter_data TEXT,
      created_at  TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (app_id) REFERENCES prose_apps(app_id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS prose_ue_config (
      id                    INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi                  TEXT UNIQUE NOT NULL,
      authorized            INTEGER NOT NULL DEFAULT 0,
      discovery_enabled     INTEGER NOT NULL DEFAULT 1,
      communication_enabled INTEGER NOT NULL DEFAULT 1,
      relay_capable         INTEGER NOT NULL DEFAULT 0,
      relay_enabled         INTEGER NOT NULL DEFAULT 0,
      updated_at            TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS prose_sessions (
      id           INTEGER PRIMARY KEY AUTOINCREMENT,
      session_type TEXT NOT NULL CHECK (session_type IN ('unicast','groupcast','broadcast','relay')),
      source_imsi  TEXT NOT NULL,
      target_imsi  TEXT,
      group_id     TEXT,
      relay_imsi   TEXT,
      service      TEXT,
      status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','released','failed')),
      created_at   TEXT NOT NULL DEFAULT (datetime('now')),
      released_at  TEXT
    )`,

	`CREATE INDEX IF NOT EXISTS idx_prose_discovery_filters_app_id ON prose_discovery_filters(app_id)`,

}

var SealDDL = []string{
	`CREATE TABLE IF NOT EXISTS seal_groups (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      name        TEXT UNIQUE NOT NULL,
      description TEXT,
      app_id      TEXT,
      config_json TEXT,
      created_at  TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS seal_group_members (
      id         INTEGER PRIMARY KEY AUTOINCREMENT,
      group_id   INTEGER NOT NULL,
      imsi       TEXT NOT NULL,
      role       TEXT NOT NULL DEFAULT 'member'
                 CHECK (role IN ('admin', 'member', 'viewer')),
      joined_at  TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (group_id) REFERENCES seal_groups(id) ON DELETE CASCADE,
      UNIQUE (group_id, imsi)
    )`,

	`CREATE TABLE IF NOT EXISTS seal_location_subs (
      id               INTEGER PRIMARY KEY AUTOINCREMENT,
      target_type      TEXT NOT NULL CHECK (target_type IN ('imsi', 'group')),
      target_id        TEXT NOT NULL,
      callback_url     TEXT NOT NULL,
      interval_s       INTEGER NOT NULL DEFAULT 60,
      active           INTEGER NOT NULL DEFAULT 1,
      last_notified_at TEXT,
      created_at       TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS seal_configs (
      id           INTEGER PRIMARY KEY AUTOINCREMENT,
      target_type  TEXT NOT NULL CHECK (target_type IN ('imsi', 'group', 'app')),
      target_id    TEXT NOT NULL,
      config_key   TEXT NOT NULL,
      config_value TEXT,
      updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
      UNIQUE (target_type, target_id, config_key)
    )`,

	`CREATE TABLE IF NOT EXISTS seal_val_users (
      id           INTEGER PRIMARY KEY AUTOINCREMENT,
      val_user_id  TEXT UNIQUE NOT NULL,
      imsi         TEXT NOT NULL,
      app_id       TEXT,
      created_at   TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_seal_gm_group ON seal_group_members(group_id)`,

	`CREATE INDEX IF NOT EXISTS idx_seal_gm_imsi  ON seal_group_members(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_seal_loc_active ON seal_location_subs(active)`,

	`CREATE INDEX IF NOT EXISTS idx_seal_cfg_target ON seal_configs(target_type, target_id)`,

	`CREATE INDEX IF NOT EXISTS idx_seal_val_imsi ON seal_val_users(imsi)`,

}

var SsDDL = []string{
	`CREATE TABLE IF NOT EXISTS supplementary_services (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi              TEXT NOT NULL,
      service_type      TEXT NOT NULL,
      active            INTEGER NOT NULL DEFAULT 0,
      forwarding_number TEXT,
      no_reply_timer    INTEGER DEFAULT 20,
      barring_password  TEXT,
      config_json       TEXT,
      updated_at        TEXT,
      UNIQUE(imsi, service_type)
    )`,

	`CREATE INDEX IF NOT EXISTS idx_ss_imsi ON supplementary_services(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_ss_type ON supplementary_services(service_type)`,

}

var UasDDL = []string{
	`CREATE TABLE IF NOT EXISTS uas_registry (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT,
      uav_id          TEXT UNIQUE NOT NULL,
      serial_number   TEXT,
      manufacturer    TEXT,
      model           TEXT,
      max_speed_mps   REAL DEFAULT 20.0,
      max_altitude_m  REAL DEFAULT 120.0,
      status          TEXT NOT NULL DEFAULT 'registered'
                      CHECK (status IN ('registered','active','grounded','deregistered')),
      created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS uas_flight_auth (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      uav_id          TEXT NOT NULL,
      flight_id       TEXT UNIQUE NOT NULL,
      flight_plan_json TEXT,
      authorized      INTEGER NOT NULL DEFAULT 0,
      restrictions    TEXT,
      status          TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','authorized','active','completed','revoked')),
      authorized_at   TEXT,
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (uav_id) REFERENCES uas_registry(uav_id) ON DELETE CASCADE
    )`,
	`CREATE INDEX IF NOT EXISTS idx_uas_flight_auth_uav_id ON uas_flight_auth(uav_id)`,

	`CREATE TABLE IF NOT EXISTS uas_positions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      uav_id          TEXT NOT NULL,
      latitude        REAL NOT NULL,
      longitude       REAL NOT NULL,
      altitude_m      REAL NOT NULL DEFAULT 0.0,
      heading_deg     REAL DEFAULT 0.0,
      speed_mps       REAL DEFAULT 0.0,
      timestamp       TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS uas_no_fly_zones (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      name            TEXT NOT NULL,
      lat_min         REAL NOT NULL,
      lat_max         REAL NOT NULL,
      lon_min         REAL NOT NULL,
      lon_max         REAL NOT NULL,
      alt_max_m       REAL DEFAULT 0.0,
      reason          TEXT,
      active          INTEGER NOT NULL DEFAULT 1,
      created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS uas_c2_sessions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      uav_id          TEXT NOT NULL,
      controller_id   TEXT NOT NULL,
      qos_5qi         INTEGER NOT NULL DEFAULT 3,
      status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','terminated','failed')),
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      terminated_at   TEXT
    )`,

	`CREATE INDEX IF NOT EXISTS idx_uas_positions_uav_id ON uas_positions(uav_id)`,
	`CREATE INDEX IF NOT EXISTS idx_uas_c2_sessions_uav_id ON uas_c2_sessions(uav_id)`,

}

var UssdDDL = []string{
	`CREATE TABLE IF NOT EXISTS ussd_menus (
      id             INTEGER PRIMARY KEY AUTOINCREMENT,
      code           TEXT UNIQUE,
      parent_id      INTEGER,
      title          TEXT NOT NULL,
      action_type    TEXT CHECK (action_type IN (
                         'menu','balance_check','data_usage','topup',
                         'show_msisdn','custom_text','forward'
                     )),
      action_data    TEXT,
      display_order  INTEGER NOT NULL DEFAULT 0,
      FOREIGN KEY (parent_id) REFERENCES ussd_menus(id)
        ON UPDATE CASCADE ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS ussd_sessions (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi            TEXT NOT NULL,
      code            TEXT NOT NULL,
      state           TEXT NOT NULL DEFAULT 'active'
                      CHECK (state IN ('active','completed','timeout','error')),
      current_menu_id INTEGER,
      session_data    TEXT,
      created_at      TEXT NOT NULL,
      ended_at        TEXT,
      FOREIGN KEY (current_menu_id) REFERENCES ussd_menus(id)
        ON UPDATE CASCADE ON DELETE SET NULL
    )`,

	`CREATE INDEX IF NOT EXISTS idx_ussd_menu_parent ON ussd_menus(parent_id)`,

	`CREATE INDEX IF NOT EXISTS idx_ussd_menu_code ON ussd_menus(code)`,

	`CREATE INDEX IF NOT EXISTS idx_ussd_sess_imsi ON ussd_sessions(imsi)`,

	`CREATE INDEX IF NOT EXISTS idx_ussd_sess_state ON ussd_sessions(state)`,

	`CREATE INDEX IF NOT EXISTS idx_ussd_sessions_current_menu_id ON ussd_sessions(current_menu_id)`,
}
