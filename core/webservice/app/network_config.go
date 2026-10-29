// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Network configuration helpers — Go port of webservice/network_config.py.
//
// The Python reference reads the network_config + security_algorithms +
// apn_config + supported_plmns tables and shapes them into the "config"
// dict that NGAP NG-Setup and AMF context initialization consume. In the
// Go port these are already wired through db/crud and nf/amf/ctx; this
// file adds the PLMN BCD encode/decode helpers and the full config loader
// that the /api/network-config endpoint exposes.
package app

import (
	"net/http"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// loadNetworkConfig reads the network_config singleton + security_algorithms
// and shapes them into the JSON the frontend expects.
func loadNetworkConfig() (map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	// Ensure row exists
	_, _ = db.Exec(`INSERT OR IGNORE INTO network_config (id) VALUES (1)`)

	// Read network_config — close rows before next query (SQLite single-conn)
	cfg := map[string]any{}
	rows, err := db.Query(`SELECT * FROM network_config WHERE id=1`)
	if err != nil {
		return nil, err
	}
	cols, _ := rows.Columns()
	if rows.Next() {
		scan := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range scan {
			ptrs[i] = &scan[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return nil, err
		}
		for i, name := range cols {
			cfg[name] = scan[i]
		}
	}
	rows.Close()

	// Net features are individual columns — group them for the frontend
	netFeat := map[string]any{}
	for _, f := range []string{
		"ims_vops_3gpp", "ims_vops_n3gpp", "emc", "emf", "iwk_n26", "mpsi",
		"emcn3", "mcsi", "restrict_ec", "n3_data", "cp_ciot", "up_ciot",
		"hc_cp_ciot", "iphc_cp_ciot",
	} {
		if v, ok := cfg[f]; ok {
			netFeat[f] = v
			delete(cfg, f)
		}
	}
	cfg["net_feat"] = netFeat

	// Security algorithms — safe to query now, previous rows closed
	algoRows, err := db.Query(`SELECT algo_type, algorithm, priority
		FROM security_algorithms ORDER BY algo_type, priority`)
	if err == nil {
		ciph := []map[string]any{}
		integ := []map[string]any{}
		for algoRows.Next() {
			var atype, algo string
			var prio int
			if err := algoRows.Scan(&atype, &algo, &prio); err == nil {
				entry := map[string]any{"algorithm": algo, "priority": prio}
				if atype == "ciphering" {
					ciph = append(ciph, entry)
				} else {
					integ = append(integ, entry)
				}
			}
		}
		algoRows.Close()
		cfg["security_algorithms"] = map[string]any{
			"ciphering": ciph,
			"integrity": integ,
		}
	}

	return cfg, nil
}

// saveNetworkConfig writes the network_config singleton + security_algorithms.
func saveNetworkConfig(patch map[string]any) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO network_config (id) VALUES (1)`); err != nil {
		return err
	}

	// Update core fields. sctp_port is operator-tunable per TS 38.412 §7
	// (default 38412 IANA-registered but not mandated).
	validFields := map[string]bool{
		"amf_name": true, "amf_ip": true, "sctp_port": true,
		"relative_amf_capacity": true,
		// LI subsystem knobs (TS 33.127 §6.2/§6.3/§6.4 + §5.2). All
		// DB-driven so operators rotate the auth token and toggle
		// X2/X3 deliverers without a restart.
		"li_auth_token":           true,
		"li_x2_enabled":           true,
		"li_x3_enabled":           true,
		"li_mdf_poll_interval_ms": true,
	}
	for k, v := range patch {
		if validFields[k] {
			if _, err := db.Exec("UPDATE network_config SET "+k+"=? WHERE id=1", v); err != nil {
				return err
			}
		}
	}

	// Update net_feat fields
	if nf, ok := patch["net_feat"].(map[string]any); ok {
		featFields := map[string]bool{
			"ims_vops_3gpp": true, "ims_vops_n3gpp": true, "emc": true, "emf": true,
			"iwk_n26": true, "mpsi": true, "emcn3": true, "mcsi": true,
			"restrict_ec": true, "n3_data": true, "cp_ciot": true, "up_ciot": true,
			"hc_cp_ciot": true, "iphc_cp_ciot": true,
		}
		for k, v := range nf {
			if featFields[k] {
				if _, err := db.Exec("UPDATE network_config SET "+k+"=? WHERE id=1", v); err != nil {
					return err
				}
			}
		}
	}

	// Update security algorithms
	if sa, ok := patch["security_algorithms"].(map[string]any); ok {
		for _, atype := range []string{"ciphering", "integrity"} {
			if algos, ok := sa[atype]; ok {
				algoList, _ := algos.([]any)
				if _, err := db.Exec(`DELETE FROM security_algorithms WHERE algo_type=?`, atype); err != nil {
					return err
				}
				for _, a := range algoList {
					if m, ok := a.(map[string]any); ok {
						algo, _ := m["algorithm"].(string)
						prio, _ := m["priority"].(float64)
						if algo != "" {
							if _, err := db.Exec(`INSERT OR REPLACE INTO security_algorithms (algo_type, algorithm, priority) VALUES (?,?,?)`,
								atype, algo, int(prio)); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// RegisterNetworkConfigRoutes adds the full network config reader.
// The provisioning_route.go already has a basic /api/network-config;
// this extends it with the full shaped response matching the Python
// load_config() output.
func (s *Server) RegisterNetworkConfigRoutes() {
	s.Router.Get("/api/network-config/full", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Core config
		var cfg map[string]any
		row := db.QueryRow("SELECT * FROM network_config WHERE id=1")
		cols := []string{"id", "amf_name", "amf_ip", "sctp_port", "relative_amf_capacity"}
		scan := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range scan {
			ptrs[i] = &scan[i]
		}
		if err := row.Scan(ptrs...); err == nil {
			cfg = map[string]any{}
			for i, name := range cols {
				cfg[name] = scan[i]
			}
		}

		// Security algorithms — close before next query
		rows, err := db.Query(`SELECT algo_type, algorithm, priority
            FROM security_algorithms ORDER BY algo_type, priority`)
		if err == nil {
			var algos []map[string]any
			for rows.Next() {
				var atype, algo string
				var prio int
				if err := rows.Scan(&atype, &algo, &prio); err == nil {
					algos = append(algos, map[string]any{
						"algo_type": atype, "algorithm": algo, "priority": prio,
					})
				}
			}
			rows.Close()
			if cfg == nil {
				cfg = map[string]any{}
			}
			cfg["security_algorithms"] = algos
		}

		// APNs
		apnRows, err := db.Query(`SELECT apn_name, pdu_session_type, ambr_dl_kbps,
            ambr_ul_kbps, dns_primary, dns_secondary, mtu FROM apn_config ORDER BY apn_name`)
		if err == nil {
			var apns []map[string]any
			for apnRows.Next() {
				var name, ptype string
				var dl, ul, mtu int64
				var dns1, dns2 *string
				if err := apnRows.Scan(&name, &ptype, &dl, &ul, &dns1, &dns2, &mtu); err == nil {
					apns = append(apns, map[string]any{
						"apn_name": name, "pdu_session_type": ptype,
						"ambr_dl_kbps": dl, "ambr_ul_kbps": ul,
						"dns_primary": dns1, "dns_secondary": dns2, "mtu": mtu,
					})
				}
			}
			apnRows.Close()
			cfg["apn_config"] = apns
		}

		jsonReply(w, cfg)
	})
}

// EncodePLMN converts MCC/MNC to 3-byte BCD PLMN ID (TS 24.008 §10.5.1.13).
func EncodePLMN(mcc, mnc string) []byte {
	// MCC is always 3 digits, MNC is 2 or 3.
	// BCD layout: [MCC2 MCC1] [MNC3 MCC3] [MNC2 MNC1]
	// where MNC3 = 0xF when MNC is 2 digits.
	m1 := digit(mcc, 0)
	m2 := digit(mcc, 1)
	m3 := digit(mcc, 2)
	n1 := digit(mnc, 0)
	n2 := digit(mnc, 1)
	n3 := byte(0x0F) // filler for 2-digit MNC
	if len(mnc) >= 3 {
		n3 = digit(mnc, 2)
	}
	return []byte{
		(m2 << 4) | m1,
		(n3 << 4) | m3,
		(n2 << 4) | n1,
	}
}

// DecodePLMN reverses the BCD encoding.
func DecodePLMN(b []byte) (mcc, mnc string) {
	if len(b) < 3 {
		return "", ""
	}
	m1 := b[0] & 0x0F
	m2 := (b[0] >> 4) & 0x0F
	m3 := b[1] & 0x0F
	n3 := (b[1] >> 4) & 0x0F
	n1 := b[2] & 0x0F
	n2 := (b[2] >> 4) & 0x0F
	mcc = string([]byte{'0' + m1, '0' + m2, '0' + m3})
	if n3 == 0x0F {
		mnc = string([]byte{'0' + n1, '0' + n2})
	} else {
		mnc = string([]byte{'0' + n1, '0' + n2, '0' + n3})
	}
	return
}

func digit(s string, i int) byte {
	if i < len(s) {
		return s[i] - '0'
	}
	return 0
}
