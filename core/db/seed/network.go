// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/network.go — network_config singleton + TS 33.501 §5.3 security algorithms.
package seed

import "database/sql"

// SeedNetworkConfig writes the default AMF identity + NEA/NIA algorithm priorities.
// PLMN/TAC/slice catalogs are NOT seeded — operators set them from the GUI.
func SeedNetworkConfig(db *sql.DB) error {
	// Singleton row (INSERT OR IGNORE already applied in engine.EnsureSchema,
	// but explicit INSERT here mirrors Python behaviour and is harmless).
	//
	// `amf_ip` ships empty so the AMF NGAP startup falls straight to
	// transport_linux::pickPrimaryIPv4 (single-homed onto the actual
	// management interface). The legacy 192.168.1.107 default was a
	// developer's LAN IP — never local to the sacore Docker container,
	// so every fresh DB logged a "not on any local interface" WARN and
	// dropped to 0.0.0.0 bind. Operators who want to pin a specific
	// IP set it from the Network Config GUI.
	//
	// `sctp_port` defaults to 38412 (TS 38.412 §7 IANA registration);
	// operator-tunable from the same GUI.
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO network_config
         (id, amf_name, amf_ip, sctp_port, relative_amf_capacity, ims_vops_3gpp)
         VALUES (1, 'MMT-CORE', '', 38412, 128, 1)`); err != nil {
		return err
	}

	// Security algorithm priorities (TS 33.501 §5.3). Only populate on a
	// fresh DB to preserve any operator-side re-ordering.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM security_algorithms`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	algos := []struct {
		Type, Algorithm string
		Priority        int
	}{
		{"ciphering", "NEA0", 1}, {"ciphering", "NEA2", 2},
		{"ciphering", "NEA1", 3}, {"ciphering", "NEA3", 4},
		{"integrity", "NIA2", 1}, {"integrity", "NIA1", 2},
		{"integrity", "NIA3", 3}, {"integrity", "NIA0", 4},
	}
	for _, a := range algos {
		if _, err := db.Exec(
			`INSERT INTO security_algorithms (algo_type, algorithm, priority) VALUES (?,?,?)`,
			a.Type, a.Algorithm, a.Priority); err != nil {
			return err
		}
	}
	return nil
}
