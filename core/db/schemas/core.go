// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/core.go — Core UE Subscription Schema
//
// 3GPP References:
//   TS 23.501 §5.7, §5.9, §5.15 — Subscription data model
//   TS 29.503 §5.2.2 — UDM data sets
//   TS 29.505 §5.2 — UDR data store
//   TS 33.501 §6.1 — Authentication data
package schemas

// CoreDDL defines the UE, auth, subscribed NSSAI, slice/DNN, service binding,
// and IMS subscriber tables. Port of db/schemas/core.py.
var CoreDDL = []string{

	// nssai_catalog — available slices (TS 23.501 §5.15.2)
	`CREATE TABLE IF NOT EXISTS nssai_catalog (
      id    INTEGER PRIMARY KEY AUTOINCREMENT,
      sst   INTEGER NOT NULL,
      sd    TEXT,
      name  TEXT,
      UNIQUE(sst, sd)
    )`,

	// services — QoS profile catalog (TS 23.501 §5.7.4 Table 5.7.4-1)
	`CREATE TABLE IF NOT EXISTS services (
      id             INTEGER PRIMARY KEY AUTOINCREMENT,
      name           TEXT UNIQUE NOT NULL,
      fiveqi         INTEGER NOT NULL,
      resource_type  TEXT NOT NULL CHECK (resource_type IN ('GBR','NonGBR')),
      arp_priority   INTEGER NOT NULL,
      arp_pcap       INTEGER NOT NULL,
      arp_pvuln      INTEGER NOT NULL,
      gbr_ul_kbps    INTEGER,
      gbr_dl_kbps    INTEGER,
      mbr_ul_kbps    INTEGER,
      mbr_dl_kbps    INTEGER,
      flow_json      TEXT NOT NULL DEFAULT '[]',
      charging_profile TEXT,
      status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','INACTIVE','INACTIVE (event gated)','PENDING','REMOVED')),
      FOREIGN KEY (charging_profile) REFERENCES charging_profiles(name) ON UPDATE CASCADE ON DELETE SET NULL
    )`,

	`CREATE TRIGGER IF NOT EXISTS trg_services_gbr_guard
    BEFORE INSERT ON services
    FOR EACH ROW
    WHEN NEW.resource_type = 'GBR' AND (NEW.gbr_ul_kbps IS NULL OR NEW.gbr_dl_kbps IS NULL)
    BEGIN
      SELECT RAISE(ABORT, 'GBR service requires gbr_ul_kbps and gbr_dl_kbps');
    END`,

	`CREATE TRIGGER IF NOT EXISTS trg_services_gbr_guard_upd
    BEFORE UPDATE OF resource_type, gbr_ul_kbps, gbr_dl_kbps ON services
    FOR EACH ROW
    WHEN NEW.resource_type = 'GBR' AND (NEW.gbr_ul_kbps IS NULL OR NEW.gbr_dl_kbps IS NULL)
    BEGIN
      SELECT RAISE(ABORT, 'GBR service requires gbr_ul_kbps and gbr_dl_kbps');
    END`,

	// ue — UE identity (TS 23.501 §5.9 / TS 29.503 §5.2.2.1)
	`CREATE TABLE IF NOT EXISTS ue (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      imsi              TEXT UNIQUE NOT NULL,
      msisdn            TEXT,
      enabled           INTEGER NOT NULL DEFAULT 1,
      imeisv            TEXT,
      subscriber_status INTEGER NOT NULL DEFAULT 0,
      ambr_dl_kbps      INTEGER NOT NULL DEFAULT 1000000,
      ambr_ul_kbps      INTEGER NOT NULL DEFAULT 1000000
    )`,
	`CREATE INDEX IF NOT EXISTS idx_ue_imsi ON ue(imsi)`,

	// ue_auth_data — UE authentication (TS 33.501 §6.1)
	`CREATE TABLE IF NOT EXISTS ue_auth_data (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      ue_id           INTEGER NOT NULL UNIQUE,
      op_type         TEXT NOT NULL CHECK (op_type IN ('OP','OPC')),
      op              TEXT NOT NULL,
      k               TEXT NOT NULL,
      sqn             INTEGER DEFAULT 0,
      amf             TEXT DEFAULT '8000',
      suci_profile    TEXT CHECK (suci_profile IN ('A','B') OR suci_profile IS NULL),
      hn_private_key  TEXT,
      FOREIGN KEY (ue_id) REFERENCES ue(id) ON UPDATE CASCADE ON DELETE CASCADE
    )`,

	// ue_subscribed_nssai — Subscribed S-NSSAIs (TS 23.501 §5.15.2)
	`CREATE TABLE IF NOT EXISTS ue_subscribed_nssai (
      id         INTEGER PRIMARY KEY AUTOINCREMENT,
      ue_id      INTEGER NOT NULL,
      nssai_id   INTEGER NOT NULL,
      is_default INTEGER NOT NULL DEFAULT 0,
      FOREIGN KEY (ue_id) REFERENCES ue(id) ON UPDATE CASCADE ON DELETE CASCADE,
      FOREIGN KEY (nssai_id) REFERENCES nssai_catalog(id) ON UPDATE CASCADE ON DELETE CASCADE,
      UNIQUE(ue_id, nssai_id)
    )`,
	`CREATE INDEX IF NOT EXISTS idx_ue_sub_nssai ON ue_subscribed_nssai(ue_id)`,

	// ue_slice_dnn — Per-UE DNN authorization per subscribed slice
	// (TS 29.503 §6.1.6.2.6 DnnInfo within SnssaiInfo §6.1.6.2.7)
	`CREATE TABLE IF NOT EXISTS ue_slice_dnn (
      id                    INTEGER PRIMARY KEY AUTOINCREMENT,
      subscribed_nssai_id   INTEGER NOT NULL,
      dnn                   TEXT NOT NULL,
      is_default            INTEGER NOT NULL DEFAULT 0,
      FOREIGN KEY (subscribed_nssai_id) REFERENCES ue_subscribed_nssai(id) ON UPDATE CASCADE ON DELETE CASCADE,
      FOREIGN KEY (dnn) REFERENCES apn_config(apn_name) ON UPDATE CASCADE ON DELETE RESTRICT,
      UNIQUE(subscribed_nssai_id, dnn)
    )`,
	`CREATE INDEX IF NOT EXISTS idx_ue_slice_dnn ON ue_slice_dnn(subscribed_nssai_id)`,

	// service_bindings — Per-UE QoS flow bindings (TS 23.501 §5.7.2)
	`CREATE TABLE IF NOT EXISTS service_bindings (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      slice_dnn_id    INTEGER NOT NULL,
      service_name    TEXT NOT NULL,
      is_default      INTEGER NOT NULL DEFAULT 0,
      FOREIGN KEY (slice_dnn_id) REFERENCES ue_slice_dnn(id) ON UPDATE CASCADE ON DELETE CASCADE,
      FOREIGN KEY (service_name) REFERENCES services(name) ON UPDATE CASCADE ON DELETE RESTRICT,
      UNIQUE(slice_dnn_id, service_name)
    )`,
	`CREATE INDEX IF NOT EXISTS idx_svc_bind ON service_bindings(slice_dnn_id)`,

	// ims_subscribers — IMS Subscribers (TS 23.228 §4.3)
	`CREATE TABLE IF NOT EXISTS ims_subscribers (
      id                 INTEGER PRIMARY KEY AUTOINCREMENT,
      ue_id              INTEGER NOT NULL UNIQUE,
      impi               TEXT NOT NULL,
      impu               TEXT NOT NULL,
      service_profile_id INTEGER,
      FOREIGN KEY (ue_id) REFERENCES ue(id) ON UPDATE CASCADE ON DELETE CASCADE,
      FOREIGN KEY (service_profile_id) REFERENCES ims_service_profiles(id) ON DELETE SET NULL
    )`,
	`CREATE INDEX IF NOT EXISTS idx_ims_subscribers_service_profile_id ON ims_subscribers(service_profile_id)`,
	`CREATE INDEX IF NOT EXISTS idx_ue_subscribed_nssai_nssai_id ON ue_subscribed_nssai(nssai_id)`,
}
