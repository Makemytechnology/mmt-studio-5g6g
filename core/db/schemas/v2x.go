// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/v2x.go — V2X (Vehicle-to-Everything) DDL
// TS 23.287 §5.4.4 (PQI table) + §5.5 (V2X subscription).
package schemas

var V2XDDL = []string{
	`CREATE TABLE IF NOT EXISTS v2x_service_types (
        id              INTEGER PRIMARY KEY AUTOINCREMENT,
        service_name    TEXT NOT NULL UNIQUE,
        pqi             INTEGER NOT NULL,
        resource_type   TEXT NOT NULL CHECK (resource_type IN ('GBR','NonGBR','DelCritGBR')),
        priority_level  INTEGER NOT NULL,
        packet_delay_ms INTEGER NOT NULL,
        packet_error_rate TEXT NOT NULL,
        max_data_burst  INTEGER,
        avg_window_ms   INTEGER,
        fiveqi_uu       INTEGER,
        description     TEXT
    )`,
	`CREATE INDEX IF NOT EXISTS idx_v2x_svc_pqi ON v2x_service_types(pqi)`,

	`CREATE TABLE IF NOT EXISTS v2x_config (
        key     TEXT PRIMARY KEY,
        value   TEXT NOT NULL
    )`,

	// Per-UE V2X authorized PLMN list — the "authorized PLMNs" element
	// of the V2X policy container delivered to the UE in TS 23.287
	// §5.1.2 (V2X policy / parameter provisioning procedure).
	`CREATE TABLE IF NOT EXISTS v2x_authorized_plmns (
        id          INTEGER PRIMARY KEY AUTOINCREMENT,
        imsi        TEXT NOT NULL,
        plmn_id     TEXT NOT NULL,
        created_at  TEXT NOT NULL DEFAULT (datetime('now')),
        UNIQUE(imsi, plmn_id)
    )`,
	`CREATE INDEX IF NOT EXISTS idx_v2x_authplmns_imsi ON v2x_authorized_plmns(imsi)`,

	// Audit trail of V2X policy provisioning (TS 23.287 §5.1.2 + TS
	// 24.587 §5 — the PCF→UE policy push, wrapped in a UE Policy
	// Container per TS 24.501 §D.6.1). One row per ProvisionPolicy
	// call so the operator can correlate "did UE X get its V2X policy?"
	`CREATE TABLE IF NOT EXISTS v2x_policy_log (
        id              INTEGER PRIMARY KEY AUTOINCREMENT,
        imsi            TEXT NOT NULL,
        ue_type         TEXT,
        pc5_ambr_kbps   INTEGER,
        plmn_count      INTEGER NOT NULL DEFAULT 0,
        qos_count       INTEGER NOT NULL DEFAULT 0,
        freq_count      INTEGER NOT NULL DEFAULT 0,
        created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,
	`CREATE INDEX IF NOT EXISTS idx_v2x_policy_log_imsi ON v2x_policy_log(imsi)`,
}

// V2XAlterUE lists ALTER-TABLE column additions applied idempotently to the
// existing 'ue' table (TS 23.287 §5.2 V2X authorization + §5.5 V2X
// subscription).
var V2XAlterUE = []struct {
	Column string
	DDL    string
}{
	{"v2x_authorized", "v2x_authorized INTEGER NOT NULL DEFAULT 0"},
	{"v2x_ue_type", "v2x_ue_type TEXT CHECK (v2x_ue_type IN ('vehicle','pedestrian')) DEFAULT NULL"},
	{"v2x_pc5_ambr_kbps", "v2x_pc5_ambr_kbps INTEGER DEFAULT NULL"},
}

// V2XSeed is the seed-data INSERT list from the Python reference
// (TS 23.287 Table 5.4.4-1).
var V2XSeed = []string{
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('platooning_higher',21,'GBR',3,20,'1e-4',NULL,2000,3,'Platooning between UEs — higher degree of automation')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('sensor_sharing_higher',22,'GBR',4,50,'1e-2',NULL,2000,3,'Sensor sharing — higher degree of automation')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('info_sharing_driving',23,'GBR',3,100,'1e-4',NULL,2000,3,'Information sharing for automated driving')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('coop_lane_change_higher',55,'NonGBR',3,10,'1e-4',NULL,NULL,3,'Cooperative lane change — higher degree of automation')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('platooning_informative',56,'NonGBR',6,20,'1e-1',NULL,NULL,3,'Platooning informative exchange — low degree of automation')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('coop_lane_change_lower',57,'NonGBR',5,25,'1e-1',NULL,NULL,3,'Cooperative lane change — lower degree of automation')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('sensor_sharing_lower',58,'NonGBR',4,100,'1e-2',NULL,NULL,3,'Sensor information sharing — lower degree of automation')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('platooning_reporting',59,'NonGBR',6,500,'1e-1',NULL,NULL,3,'Platooning — reporting to an RSU')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('collision_avoidance',90,'DelCritGBR',3,10,'1e-4',2000,2000,3,'Cooperative collision avoidance — sensor sharing — video sharing')`,
	`INSERT OR IGNORE INTO v2x_service_types (service_name,pqi,resource_type,priority_level,packet_delay_ms,packet_error_rate,max_data_burst,avg_window_ms,fiveqi_uu,description) VALUES ('emergency_trajectory',91,'DelCritGBR',2,3,'1e-5',2000,2000,3,'Emergency trajectory alignment — sensor sharing — higher automation')`,
	`INSERT OR IGNORE INTO v2x_config (key,value) VALUES ('v2x_enabled','1')`,
	`INSERT OR IGNORE INTO v2x_config (key,value) VALUES ('pc5_nr_enabled','1')`,
	`INSERT OR IGNORE INTO v2x_config (key,value) VALUES ('pc5_lte_enabled','0')`,
	`INSERT OR IGNORE INTO v2x_config (key,value) VALUES ('ue_pc5_ambr_kbps','50000')`,
	`INSERT OR IGNORE INTO apn_config (apn_name,ambr_dl_kbps,ambr_ul_kbps,pdu_session_type,ssc_mode,dns_primary,dns_secondary,mtu) VALUES ('v2x',1000000,1000000,'IPv4',1,'8.8.8.8','8.8.4.4',1500)`,
	`INSERT OR IGNORE INTO services (name,fiveqi,resource_type,arp_priority,arp_pcap,arp_pvuln,gbr_ul_kbps,gbr_dl_kbps,mbr_ul_kbps,mbr_dl_kbps,flow_json,status) VALUES ('v2x_signaling',3,'GBR',1,1,0,50,50,100,100,'[]','ACTIVE')`,
	`INSERT OR IGNORE INTO services (name,fiveqi,resource_type,arp_priority,arp_pcap,arp_pvuln,flow_json,status) VALUES ('v2x_data',80,'NonGBR',5,0,1,'[]','ACTIVE')`,
}
