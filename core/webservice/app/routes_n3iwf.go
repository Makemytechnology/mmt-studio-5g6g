// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Auto-extracted from domain_routes.go (refactor: split god function by
// domain banner). Do not re-merge — keep new domain APIs in their own
// routes_<domain>.go file.
package app

import (
	"net/http"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/ctx"
)

func (s *Server) registerN3IWFRoutes() {
	r := s.Router

	// ── N3IWF (n3iwf_api.py) ────────────────────────────────────────
	r.Get("/api/n3iwf/config", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"enabled": 0})
			return
		}
		row := db.QueryRow(`SELECT * FROM n3iwf_config WHERE id=1`)
		// n3iwf_config may not exist — return safe default
		var id, enabled int64
		var n3iwfIP, innerIPPool, ipsecEnc, ipsecInt, dhGroup string
		var ikePort, ikeNatPort int64
		var supportedDNNs, supportedNSSAI *string
		err = row.Scan(&id, &enabled, &n3iwfIP, &ikePort, &ikeNatPort,
			&innerIPPool, &ipsecEnc, &ipsecInt, &dhGroup, &supportedDNNs, &supportedNSSAI)
		if err != nil {
			jsonReply(w, map[string]any{"enabled": 0})
			return
		}
		jsonReply(w, map[string]any{
			"id": id, "enabled": enabled, "n3iwf_ip": n3iwfIP,
			"ike_port": ikePort, "ike_nat_port": ikeNatPort,
			"inner_ip_pool": innerIPPool, "ipsec_enc_algo": ipsecEnc,
			"ipsec_int_algo": ipsecInt, "dh_group": dhGroup,
			"supported_dnns": supportedDNNs, "supported_nssai": supportedNSSAI,
		})
	})
	r.Post("/api/n3iwf/config", func(w http.ResponseWriter, rq *http.Request) {
		var d map[string]any
		if !decodeJSON(w, rq, &d) {
			return
		}
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fields := []string{"enabled", "n3iwf_ip", "ike_port", "ike_nat_port",
			"inner_ip_pool", "ipsec_enc_algo", "ipsec_int_algo",
			"dh_group", "supported_dnns", "supported_nssai"}
		for _, f := range fields {
			if v, ok := d[f]; ok {
				db.Exec("UPDATE n3iwf_config SET "+f+"=? WHERE id=1", v)
			}
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/n3iwf/status", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		enabled := false
		n3iwfIP := "0.0.0.0"
		ikePort := 500
		if err == nil {
			var en int64
			var ip string
			var port int64
			if db.QueryRow(`SELECT enabled, n3iwf_ip, ike_port FROM n3iwf_config WHERE id=1`).Scan(&en, &ip, &port) == nil {
				enabled = en != 0
				n3iwfIP = ip
				ikePort = int(port)
			}
		}

		// Pull live UE state from the process-wide manager. The IKE
		// handler writes to ctx.Default; this is a snapshot read so
		// the lock is held only briefly.
		ueList := ctx.Default.All()
		ues := make([]map[string]any, 0, len(ueList))
		var registered, pduActive, pduSessions int
		for _, u := range ueList {
			ues = append(ues, map[string]any{
				"ue_id":          u.UEID,
				"state":          string(u.State),
				"ue_addr":        u.UEAddrIP,
				"ue_port":        u.UEAddrPort,
				"spi_i":          u.HexSPIi(),
				"imsi":           u.IMSI,
				"supi":           u.SUPI,
				"inner_ip":       u.InnerIP,
				"child_sa_count": len(u.ChildSAs),
				"created_at":     u.CreatedAt.UnixMilli(),
				"last_activity":  u.LastActivity.UnixMilli(),
			})
			switch u.State {
			case ctx.StateRegistered:
				registered++
			case ctx.StatePDUActive:
				registered++
				pduActive++
			}
			// Per-UE PDU sessions = non-signalling ChildSAs.
			for _, csa := range u.ChildSAs {
				if !csa.Signalling {
					pduSessions++
				}
			}
		}

		jsonReply(w, map[string]any{
			"enabled":  enabled,
			"n3iwf_ip": n3iwfIP,
			"ike_port": ikePort,
			"stats": map[string]int{
				"total_ues":           len(ueList),
				"registered_ues":      registered,
				"active_pdu_sessions": pduSessions,
				"pdu_active_ues":      pduActive,
			},
			"ues":       ues,
			"timestamp": float64(time.Now().UnixMilli()) / 1000.0,
		})
	})
}
