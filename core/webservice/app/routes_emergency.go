// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_emergency.go — REST surface for 5GS Emergency Services.
//
// Wires `safety/emergency` to /api/emergency/*. The package owns:
//
//   - Singleton emergency-services configuration (emergency_config).
//   - Active emergency-PDU-session ledger (emergency_sessions).
//   - PDU-classification helper (Emergency Request type 3 / DNN=sos).
//   - QoS profile lookup (TS 23.501 §5.16.4.6 — 5QI / ARP).
//   - PSAP SIP routing helper (TS 23.167 §7.5).
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 22.101 §10           Emergency Calls (umbrella requirements).
//   - TS 22.101 §10.4         Emergency calls in IM CN subsystem.
//   - TS 22.101 §10.6         Location Availability for Emergency Calls.
//   - TS 23.501 §5.16.4       Emergency Services architecture (5GC).
//   - TS 23.501 §5.16.4.6     QoS for Emergency Services.
//   - TS 23.501 §5.16.4.8     IP Address Allocation for emergency PDUs.
//   - TS 23.501 §5.16.4.9     Handling of PDU Sessions for Emergency
//                             Services (Request type "Emergency Request" = 3).
//   - TS 23.167 §6.2.2        E-CSCF functional entity.
//   - TS 23.167 §7.1 / §7.5   IMS Emergency procedures + PSAP interworking.
//   - TS 24.501 §5.5.1.2.6    Initial Registration for Emergency services.
//   - TS 24.501 §5.5.1.2.6A   Initial Registration for emergency
//                             services when authentication is not performed.
//   - RFC 5031 §4.2           urn:service:sos[.sub-service].
//
// All response shapes match `templates/emergency.html`.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/safety/emergency"
)

func (s *Server) registerEmergencyRoutes() {
	r := s.Router

	// ── Stats / dashboard ─────────────────────────────────────────
	r.Get("/api/emergency", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, emergency.GetEmergencyStats())
	})

	r.Get("/api/emergency/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, emergency.GetEmergencyStats())
	})

	// ── Configuration (emergency_config singleton) ────────────────
	r.Get("/api/emergency/config", func(w http.ResponseWriter, _ *http.Request) {
		cfg, err := emergency.GetConfig()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg == nil {
			cfg = map[string]interface{}{}
		}
		jsonReply(w, cfg)
	})

	r.Post("/api/emergency/config", func(w http.ResponseWriter, rq *http.Request) {
		var fields map[string]interface{}
		if !decodeJSON(w, rq, &fields) {
			return
		}
		if err := emergency.UpdateConfig(fields); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg, _ := emergency.GetConfig()
		jsonReply(w, cfg)
	})

	// ── Active session ledger (TS 23.501 §5.16.4.9) ──────────────
	r.Get("/api/emergency/sessions", func(w http.ResponseWriter, _ *http.Request) {
		list, err := emergency.GetActiveEmergencySessions()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/emergency/sessions", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI         string `json:"imsi"`
			IMEI         string `json:"imei"`
			PDUSessionID int    `json:"pdu_session_id"`
			IPAddr       string `json:"ip_addr"`
			GnbIP        string `json:"gnb_ip"`
			TAC          string `json:"tac"`
			CellID       string `json:"cell_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// TS 23.501 §5.16.4 — IMEI is the only mandatory identifier
		// (unauthenticated emergency UEs may have no IMSI).
		if d.IMEI == "" && d.IMSI == "" {
			jsonError(w, "imei or imsi required", http.StatusBadRequest)
			return
		}
		id := emergency.CreateEmergencySession(d.IMSI, d.IMEI, d.PDUSessionID,
			d.IPAddr, d.GnbIP, d.TAC, d.CellID)
		if id == 0 {
			jsonError(w, "create session failed", http.StatusInternalServerError)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{"id": id})
	})

	r.Post("/api/emergency/sessions/{id}/release", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		emergency.ReleaseEmergencySession(id)
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Classification + QoS lookup ──────────────────────────────
	// PDU classifier — operator panels and external testers can ask
	// "is this PDU request an emergency one?" without re-implementing
	// the §5.16.4.9 logic (Request type=3 OR DNN=sos).
	r.Get("/api/emergency/classify", func(w http.ResponseWriter, rq *http.Request) {
		rt, _ := strconv.Atoi(rq.URL.Query().Get("request_type"))
		dnn := rq.URL.Query().Get("dnn")
		jsonReply(w, map[string]any{
			"is_emergency": emergency.IsEmergencyPDURequest(rt, dnn),
			"request_type": rt,
			"dnn":          dnn,
		})
	})

	r.Get("/api/emergency/qos", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, emergency.GetEmergencyQoS())
	})

	// ── E-CSCF helpers (TS 23.167 §7.5) ──────────────────────────
	// SIP URN check (RFC 5031 §4.2).
	r.Get("/api/emergency/check-urn", func(w http.ResponseWriter, rq *http.Request) {
		uri := rq.URL.Query().Get("request_uri")
		jsonReply(w, map[string]any{
			"request_uri":  uri,
			"is_emergency": emergency.CheckEmergencyURN(uri),
		})
	})

	// PSAP routing probe — does NOT actually dial the PSAP; returns
	// the operator-configured target so the panel can show a
	// reachability hint without holding open a UDP socket per click.
	r.Get("/api/emergency/psap", func(w http.ResponseWriter, _ *http.Request) {
		cfg, err := emergency.GetConfig()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg == nil {
			jsonError(w, "no config", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{
			"psap_sip_uri": cfg["psap_sip_uri"],
			"psap_ip":      cfg["psap_ip"],
			"psap_port":    cfg["psap_port"],
			"configured":   cfg["psap_sip_uri"] != "" || cfg["psap_ip"] != "",
		})
	})
}
