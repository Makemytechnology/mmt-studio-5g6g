// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_roaming.go — REST surface for inter-PLMN roaming.
//
// Wires `infra/roaming` to /api/roaming/*. The package owns:
//
//   - Roaming agreements (HPLMN ↔ VPLMN, Home-Routed vs LBO).
//   - Active session tracker (roaming_sessions).
//   - Inbound-roaming admission lookup (DetectRoaming).
//   - Wholesale CDR ledger (roaming_cdrs) + TAP-style export stub.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 23.501 §5.6.3        SM-Roaming — HR vs LBO architecture.
//   - TS 23.501 §5.7.1.11     QoS aspects of home-routed roaming.
//   - TS 23.501 §5.17.4       Network sharing + 5GS/EPS interworking
//                             when an agreement carries SST/DNN
//                             restrictions.
//   - TS 32.240 / 32.298      Charging architecture + CDR fields.
//   - TS 29.510               Nnrf-disc bootstrap of partner-PLMN NF
//                             endpoints (TODO — operator hard-codes
//                             the URIs in an Agreement today).
//
// What this surface owns:
//
//   - GET    /api/roaming/stats                Dashboard counters.
//   - GET    /api/roaming/agreements           Agreement list.
//   - POST   /api/roaming/agreements           Upsert one agreement.
//   - DELETE /api/roaming/agreements/{plmn}    Remove an agreement.
//   - PATCH  /api/roaming/agreements/{plmn}/enabled  Toggle enabled.
//   - GET    /api/roaming/agreements/{plmn}    One agreement.
//   - GET    /api/roaming/sessions             Active sessions.
//   - GET    /api/roaming/sessions/{imsi}      Per-IMSI session log.
//   - POST   /api/roaming/sessions             Open a session row.
//   - POST   /api/roaming/sessions/{imsi}/release  Close active rows.
//   - GET    /api/roaming/detect/{imsi}        DetectRoaming probe.
//   - GET    /api/roaming/cdrs                 CDR list (newest first).
//   - GET    /api/roaming/cdrs/stats           Aggregate CDR stats.
//   - POST   /api/roaming/cdrs                 Insert a CDR row.
//   - POST   /api/roaming/cdrs/export          Mark all pending exported.
//   - POST   /api/roaming/export-tap           Alias for /cdrs/export
//                                              (older GUI clients).
package app

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/infra/roaming"
)

func (s *Server) registerRoamingRoutes() {
	r := s.Router

	// ── Stats / dashboard ─────────────────────────────────────────
	r.Get("/api/roaming/stats", func(w http.ResponseWriter, _ *http.Request) {
		st, err := roaming.GetStats()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cdrs, _ := roaming.GetCDRStats()
		out := map[string]any{
			"active_sessions":  st.ActiveSessions,
			"inbound_active":   st.InboundActive,
			"outbound_active":  st.OutboundActive,
			"total_sessions":   st.TotalSessions,
		}
		if cdrs != nil {
			out["unexported"] = cdrs.Unexported
			out["total_cdrs"] = cdrs.TotalCDRs
			out["total_bytes"] = cdrs.TotalBytes
		}
		jsonReply(w, out)
	})

	// ── Agreements (TS 23.501 §5.6.3) ─────────────────────────────
	r.Get("/api/roaming/agreements", func(w http.ResponseWriter, rq *http.Request) {
		enabledOnly := rq.URL.Query().Get("enabled") == "1"
		list, err := roaming.ListAgreements(enabledOnly)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []roaming.Agreement{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/roaming/agreements", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			PartnerPLMNID string `json:"partner_plmn_id"`
			PartnerName   string `json:"partner_name"`
			Direction     string `json:"direction"`
			RoamingMode   string `json:"roaming_mode"`
			MaxUEs        int    `json:"max_ues"`
			AllowedSST    string `json:"allowed_sst"`
			AllowedDNN    string `json:"allowed_dnn"`
			AUSFEndpoint  string `json:"ausf_endpoint"`
			UDMEndpoint   string `json:"udm_endpoint"`
			SMFEndpoint   string `json:"smf_endpoint"`
			SEPPEndpoint  string `json:"sepp_endpoint"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.PartnerPLMNID == "" {
			jsonError(w, "partner_plmn_id required", http.StatusBadRequest)
			return
		}
		// TS 23.501 §5.6.3 / §5.7.1.11 vocabulary.
		switch d.Direction {
		case "", "inbound", "outbound", "both":
		default:
			jsonError(w, "direction must be inbound|outbound|both",
				http.StatusBadRequest)
			return
		}
		if d.Direction == "" {
			d.Direction = "both"
		}
		switch d.RoamingMode {
		case "", "hr", "lbo", "both":
		default:
			jsonError(w, "roaming_mode must be hr|lbo|both",
				http.StatusBadRequest)
			return
		}
		if d.RoamingMode == "" {
			d.RoamingMode = "lbo"
		}
		if err := roaming.CreateAgreement(d.PartnerPLMNID, d.PartnerName,
			d.Direction, d.RoamingMode, d.MaxUEs,
			d.AllowedSST, d.AllowedDNN,
			d.AUSFEndpoint, d.UDMEndpoint, d.SMFEndpoint, d.SEPPEndpoint); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		ag, _ := roaming.GetAgreement(d.PartnerPLMNID)
		jsonReplyStatus(w, http.StatusCreated, ag)
	})

	r.Get("/api/roaming/agreements/{plmn}", func(w http.ResponseWriter, rq *http.Request) {
		ag, err := roaming.GetAgreement(chi.URLParam(rq, "plmn"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ag == nil {
			jsonError(w, "no agreement for plmn", http.StatusNotFound)
			return
		}
		jsonReply(w, ag)
	})

	r.Delete("/api/roaming/agreements/{plmn}", func(w http.ResponseWriter, rq *http.Request) {
		if err := roaming.DeleteAgreement(chi.URLParam(rq, "plmn")); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Patch("/api/roaming/agreements/{plmn}/enabled", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Enabled bool `json:"enabled"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := roaming.SetAgreementEnabled(chi.URLParam(rq, "plmn"), d.Enabled); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "enabled": d.Enabled})
	})

	// ── Sessions ─────────────────────────────────────────────────
	r.Get("/api/roaming/sessions", func(w http.ResponseWriter, rq *http.Request) {
		limit := 100
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		list, err := roaming.GetActiveSessions(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []roaming.Session{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/roaming/sessions/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		list, err := roaming.GetSessionsForIMSI(chi.URLParam(rq, "imsi"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []roaming.Session{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/roaming/sessions", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI          string `json:"imsi"`
			HomePLMNID    string `json:"home_plmn_id"`
			VisitedPLMNID string `json:"visited_plmn_id"`
			Direction     string `json:"direction"`
			RoamingMode   string `json:"roaming_mode"`
			PDUSessionID  *int   `json:"pdu_session_id,omitempty"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.HomePLMNID == "" || d.VisitedPLMNID == "" {
			jsonError(w, "imsi, home_plmn_id, visited_plmn_id required",
				http.StatusBadRequest)
			return
		}
		switch d.Direction {
		case "inbound", "outbound":
		default:
			jsonError(w, "direction must be inbound|outbound",
				http.StatusBadRequest)
			return
		}
		switch d.RoamingMode {
		case "hr", "lbo":
		default:
			jsonError(w, "roaming_mode must be hr|lbo",
				http.StatusBadRequest)
			return
		}
		if err := roaming.CreateSession(d.IMSI, d.HomePLMNID, d.VisitedPLMNID,
			d.Direction, d.RoamingMode, d.PDUSessionID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{"ok": true})
	})

	r.Post("/api/roaming/sessions/{imsi}/release", func(w http.ResponseWriter, rq *http.Request) {
		// Body is optional — bare POST releases all active rows for IMSI.
		var d struct {
			PDUSessionID *int `json:"pdu_session_id,omitempty"`
		}
		if rq.ContentLength > 0 {
			_ = json.NewDecoder(rq.Body).Decode(&d)
		}
		if err := roaming.ReleaseSession(chi.URLParam(rq, "imsi"), d.PDUSessionID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Roaming detection (admission probe) ──────────────────────
	r.Get("/api/roaming/detect/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		res := roaming.DetectRoaming(chi.URLParam(rq, "imsi"))
		if res == nil {
			jsonError(w, "imsi too short", http.StatusBadRequest)
			return
		}
		jsonReply(w, res)
	})

	// ── CDRs (TS 32.240 / 32.298) ─────────────────────────────────
	r.Get("/api/roaming/cdrs", func(w http.ResponseWriter, rq *http.Request) {
		limit := 100
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		list, err := roaming.ListCDRs(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []roaming.CDR{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/roaming/cdrs/stats", func(w http.ResponseWriter, _ *http.Request) {
		st, err := roaming.GetCDRStats()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, st)
	})

	// Older GUI / tester used /cdr-stats (with hyphen) — alias it.
	r.Get("/api/roaming/cdr-stats", func(w http.ResponseWriter, _ *http.Request) {
		st, err := roaming.GetCDRStats()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"total":      st.TotalCDRs,
			"unexported": st.Unexported,
		})
	})

	r.Post("/api/roaming/cdrs", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI          string  `json:"imsi"`
			HomePLMNID    string  `json:"home_plmn_id"`
			VisitedPLMNID string  `json:"visited_plmn_id"`
			Direction     string  `json:"direction"`
			RecordType    string  `json:"record_type"`
			DNN           *string `json:"dnn,omitempty"`
			SST           *int    `json:"sst,omitempty"`
			BytesUL       int64   `json:"bytes_ul"`
			BytesDL       int64   `json:"bytes_dl"`
			DurationSec   float64 `json:"duration_sec"`
			Cause         *string `json:"cause,omitempty"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.HomePLMNID == "" || d.VisitedPLMNID == "" {
			jsonError(w, "imsi, home_plmn_id, visited_plmn_id required",
				http.StatusBadRequest)
			return
		}
		if d.RecordType == "" {
			d.RecordType = "session"
		}
		id, err := roaming.CreateCDR(d.IMSI, d.HomePLMNID, d.VisitedPLMNID,
			d.Direction, d.RecordType, d.DNN, d.SST,
			d.BytesUL, d.BytesDL, d.DurationSec, d.Cause)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"id": id, "imsi": d.IMSI})
	})

	cdrExport := func(w http.ResponseWriter, _ *http.Request) {
		n, err := roaming.ExportPendingCDRs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "exported": n})
	}
	r.Post("/api/roaming/cdrs/export", cdrExport)
	// Older GUI / tester clients used /export-tap.
	r.Post("/api/roaming/export-tap", cdrExport)
}
