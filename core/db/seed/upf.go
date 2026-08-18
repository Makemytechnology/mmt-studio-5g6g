// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/upf.go — Default local UPF instance for turnkey setups.
package seed

import "database/sql"

// SeedUPF registers the default local UPF instance so the SMF can
// select it for PDU session establishment. Idempotent — skips if
// a UPF is already registered.
func SeedUPF(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM upf_instances`).Scan(&count); err != nil {
		return nil // table may not exist yet
	}
	if count > 0 {
		return nil
	}

	_, err := db.Exec(`INSERT OR IGNORE INTO upf_instances
		(upf_id, upf_ip, n3_ip, n6_ip, pfcp_port,
		 supported_dnns, supported_sst, max_sessions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"upf-local",     // upf_id
		"127.0.0.1",     // upf_ip (N4/PFCP control, loopback)
		"192.168.1.107", // n3_ip (GTP-U towards gNB)
		"192.168.1.107", // n6_ip (towards data network)
		8805,            // pfcp_port (TS 29.244 §6.1)
		"internet,ims",  // supported_dnns
		"1",             // supported_sst (eMBB)
		8192,            // max_sessions
	)
	return err
}
