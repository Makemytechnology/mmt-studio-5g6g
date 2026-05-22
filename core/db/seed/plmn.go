// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/plmn.go — Home PLMN + default slices + APN + TAC seed data.
package seed

import "database/sql"

// SeedPLMN inserts the default Home PLMN with GUAMI, supported slices,
// APNs with IP pools, and a default tracking area. Only populates on a
// fresh DB — skips if a supported PLMN already exists.
func SeedPLMN(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM supported_plmns`).Scan(&count); err != nil {
		return nil // table may not exist yet
	}
	if count > 0 {
		return nil
	}

	// ── Home PLMN: 001/01 with GUAMI ─────────────────────────────────
	_, _ = db.Exec(`INSERT OR IGNORE INTO supported_plmns
		(plmn_id, mcc, mnc, name, plmn_type, amf_region_id, amf_set_id, amf_pointer, priority, enabled)
		VALUES ('001-01', '001', '01', 'MMT-CORE', 'home', 1, 1, 0, 1, 1)`)

	// ── Supported slices (TS 23.501 §5.15.2) ────────────────────────
	for _, s := range []struct{ sst int; sd string }{
		{1, "000001"}, // eMBB
		{2, "000001"}, // URLLC
		{3, "000001"}, // mIoT
	} {
		_, _ = db.Exec(`INSERT OR IGNORE INTO plmn_nssai (plmn_id, sst, sd) VALUES ('001-01', ?, ?)`, s.sst, s.sd)
	}

	// ── Default APNs ─────────────────────────────────────────────────
	// DNS defaults to Google Public DNS so EPCO (TS 24.008 §10.5.6.3
	// 000DH + 8021H IPCP with RFC 1877 DNS option) has something to
	// emit out of the box. Operator overrides via the webservice APN
	// form (webservice/templates/apn.html → /operations/apn).
	// P-CSCF is IMS-only — left NULL here and filled in by the operator
	// for the `ims` APN when they key in their IMS P-CSCF reachability.
	// /16 IP pools mirror what's already configured in upf.net_setup;
	// /16 sizes give per-UE allocation plenty of headroom.
	//
	// v2x's apn_config row is also seeded by db/schemas/v2x.go (one of
	// the V2X bootstrap inserts) with the same AMBR/DNS values. INSERT
	// OR IGNORE here means whichever runs first wins; the IPv4 pool is
	// attached below regardless.
	for _, a := range []struct {
		name, pdu, pool, dns1, dns2 string
		ssc, mtu                    int
	}{
		{"internet", "IPv4", "10.45.0.0/16", "8.8.8.8", "8.8.4.4", 1, 1500},
		{"ims", "IPv4", "10.46.0.0/16", "8.8.8.8", "8.8.4.4", 1, 1500},
		{"mcx", "IPv4", "10.47.0.0/16", "8.8.8.8", "8.8.4.4", 1, 1500},
		{"iot", "IPv4", "10.48.0.0/16", "8.8.8.8", "8.8.4.4", 1, 1500},
		{"v2x", "IPv4", "10.49.0.0/16", "8.8.8.8", "8.8.4.4", 1, 1500},
	} {
		_, _ = db.Exec(`INSERT OR IGNORE INTO apn_config
			(apn_name, ambr_dl_kbps, ambr_ul_kbps, pdu_session_type, ssc_mode,
			 dns_primary, dns_secondary, mtu)
			VALUES (?, 1000000, 1000000, ?, ?, ?, ?, ?)`,
			a.name, a.pdu, a.ssc, a.dns1, a.dns2, a.mtu)
		// Add IPv4 pool
		var apnID int64
		if db.QueryRow(`SELECT id FROM apn_config WHERE apn_name=?`, a.name).Scan(&apnID) == nil {
			_, _ = db.Exec(`INSERT OR IGNORE INTO apn_ip_pools (apn_id, cidr, ip_version) VALUES (?, ?, 4)`,
				apnID, a.pool)
		}
	}

	// ── Default tracking area ────────────────────────────────────────
	_, _ = db.Exec(`INSERT OR IGNORE INTO tracking_areas
		(tac, plmn_mcc, plmn_mnc, name, paging_priority, enabled)
		VALUES ('0001', '001', '01', 'Default', 5, 1)`)

	return nil
}
