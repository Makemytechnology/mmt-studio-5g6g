// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_chf.go — REST surface for the Charging Function (CHF).
//
// Wires `nf/chf` to /api/chf/*. The package owns charging-session
// lifecycle, CDR generation, online-charging quota grants, prepaid
// balance management, and the CSV export path.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 32.290 §6.2  — Nchf_ChargingData service operations
//                       (Create / Update / Release of charging data).
//   - TS 32.291 §6.1  — Online charging session lifecycle.
//   - TS 32.291 §6.1.3 — Multiple Unit Information / quota
//                       grant, report-usage, threshold reporting.
//   - TS 32.260       — IMS voice/video CDR fields.
//
// Before this surface landed, /api/chf/* was a 7-line stub block in
// routes_nsaas.go returning empty objects; the package's full
// machinery was unreachable from the panel and tester.
//
// All response shapes are `{ok: true, ...}` envelopes.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/nf/chf"
)

func (s *Server) registerCHFRoutes() {
	r := s.Router

	// ── Stats / dashboard ────────────────────────────────────────
	r.Get("/api/chf/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": chf.GetStats()})
	})

	// ── Charging-data (TS 32.290 §6.2) ───────────────────────────

	// List active + recent charging sessions.
	r.Get("/api/chf/charging-data", func(w http.ResponseWriter, rq *http.Request) {
		status := rq.URL.Query().Get("status")
		limit := 200
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		sessions, err := chf.ListChargingSessions(status, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sessions == nil {
			sessions = []map[string]any{}
		}
		jsonReply(w, map[string]any{
			"ok":       true,
			"sessions": sessions,
			"items":    sessions, // panel-side alias
			"count":    len(sessions),
		})
	})

	// Create session (Initial Charging Data Request).
	r.Post("/api/chf/charging-data", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI            string `json:"imsi"`
			PDUSessionID    int    `json:"pdu_session_id"`
			ServiceName     string `json:"service_name"`
			ChargingMethod  string `json:"charging_method"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		row, err := chf.CreateChargingSession(d.IMSI, d.ServiceName,
			d.ChargingMethod, d.PDUSessionID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := map[string]any{
			"ok":      true,
			"session": row,
			// Convenience top-level keys the tester pulls.
			"session_id": row["session_id"],
			"status":     row["status"],
		}
		jsonReply(w, out)
	})

	// Read one session.
	r.Get("/api/chf/charging-data/{session_id}", func(w http.ResponseWriter, rq *http.Request) {
		row, err := chf.GetChargingSession(chi.URLParam(rq, "session_id"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			jsonError(w, "session not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "session": row})
	})

	// Interim update (Update Charging Data Request, TS 32.291 §6.1).
	r.Put("/api/chf/charging-data/{session_id}", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "session_id")
		var d struct {
			Usage struct {
				VolumeUplink   int64 `json:"volume_uplink"`
				VolumeDownlink int64 `json:"volume_downlink"`
				DurationS      int   `json:"duration_s"`
			} `json:"usage"`
			UsedUnits int64 `json:"used_units"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		row, err := chf.UpdateChargingSession(sid,
			d.Usage.VolumeUplink, d.Usage.VolumeDownlink,
			d.Usage.DurationS, d.UsedUnits)
		if err != nil {
			// Distinguish "not found" from "bad input" for the panel.
			if err.Error() == "session_id required" {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{
			"ok":           true,
			"session":      row,
			"total_volume": asInt64(row["total_volume_ul"]) + asInt64(row["total_volume_dl"]),
		})
	})

	// Release (Termination Charging Data Request).
	r.Post("/api/chf/charging-data/{session_id}/release", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "session_id")
		var d struct {
			FinalUsage struct {
				VolumeUplink   int64 `json:"volume_uplink"`
				VolumeDownlink int64 `json:"volume_downlink"`
				DurationS      int   `json:"duration_s"`
			} `json:"final_usage"`
		}
		// Body is optional; ignore decode error on empty body so the
		// tester's no-body POST still flows through.
		if rq.ContentLength > 0 {
			if !decodeJSON(w, rq, &d) {
				return
			}
		}
		row, err := chf.ReleaseChargingSession(sid,
			d.FinalUsage.VolumeUplink, d.FinalUsage.VolumeDownlink,
			d.FinalUsage.DurationS)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "session": row})
	})

	// ── CDRs ─────────────────────────────────────────────────────
	r.Get("/api/chf/cdrs", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		cdrType := rq.URL.Query().Get("type")
		status := rq.URL.Query().Get("status")
		limit := 200
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		cdrs, err := chf.GetCDRs(imsi, cdrType, status, 0, 0, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cdrs == nil {
			cdrs = []chf.CDR{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "cdrs": cdrs, "count": len(cdrs),
		})
	})

	r.Post("/api/chf/cdrs/export", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI  string `json:"imsi"`
			Limit int    `json:"limit"`
		}
		if rq.ContentLength > 0 {
			if !decodeJSON(w, rq, &d) {
				return
			}
		}
		if d.Limit <= 0 {
			d.Limit = 1000
		}
		csv, err := chf.ExportCDRsCSV(d.IMSI, 0, 0, d.Limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Count rows = newline count - 1 (header line).
		rows := 0
		for _, c := range csv {
			if c == '\n' {
				rows++
			}
		}
		if rows > 0 {
			rows-- // strip header
		}
		jsonReply(w, map[string]any{
			"ok":       true,
			"exported": rows,
			"csv":      csv,
		})
	})

	// ── Quota grants (TS 32.291 §6.1.3.2) ────────────────────────
	r.Get("/api/chf/quotas", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		status := rq.URL.Query().Get("status")
		limit := 200
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		grants, err := chf.GetAllGrants(imsi, status, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if grants == nil {
			grants = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "quotas": grants, "count": len(grants),
		})
	})

	r.Post("/api/chf/quotas/grant", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI           string `json:"imsi"`
			Service        string `json:"service"`
			RequestedUnits int64  `json:"requested_units"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.Service == "" {
			jsonError(w, "imsi and service required",
				http.StatusBadRequest)
			return
		}
		grant := chf.GrantQuota(d.IMSI, d.Service, d.RequestedUnits)
		jsonReply(w, map[string]any{"ok": true, "grant": grant})
	})

	r.Post("/api/chf/quotas/report", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI      string `json:"imsi"`
			Service   string `json:"service"`
			UsedUnits int64  `json:"used_units"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.Service == "" {
			jsonError(w, "imsi and service required",
				http.StatusBadRequest)
			return
		}
		st := chf.ReportUsage(d.IMSI, d.Service, d.UsedUnits)
		jsonReply(w, map[string]any{"ok": true, "status": st})
	})

	r.Post("/api/chf/quotas/check", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI    string `json:"imsi"`
			Service string `json:"service"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.Service == "" {
			jsonError(w, "imsi and service required",
				http.StatusBadRequest)
			return
		}
		st := chf.CheckQuota(d.IMSI, d.Service)
		jsonReply(w, map[string]any{"ok": true, "status": st})
	})

	r.Post("/api/chf/quotas/revoke", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI    string `json:"imsi"`
			Service string `json:"service"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.Service == "" {
			jsonError(w, "imsi and service required",
				http.StatusBadRequest)
			return
		}
		n := chf.RevokeQuota(d.IMSI, d.Service)
		jsonReply(w, map[string]any{"ok": true, "revoked": n})
	})

	// ── Balances (prepaid) ───────────────────────────────────────
	r.Get("/api/chf/balances/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		balances := chf.GetAllBalances(imsi)
		if balances == nil {
			balances = []chf.Balance{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "imsi": imsi, "balances": balances,
		})
	})

	r.Post("/api/chf/balances/recharge", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string  `json:"imsi"`
			Amount      float64 `json:"amount"`
			BalanceType string  `json:"balance_type"`
			Reference   string  `json:"reference"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.Amount <= 0 {
			jsonError(w, "imsi and positive amount required",
				http.StatusBadRequest)
			return
		}
		if d.BalanceType == "" {
			d.BalanceType = "main"
		}
		newBal := chf.Recharge(d.IMSI, d.Amount, d.BalanceType, d.Reference)
		jsonReply(w, map[string]any{
			"ok":           true,
			"imsi":         d.IMSI,
			"new_balance":  newBal,
			"balance_type": d.BalanceType,
		})
	})
}

// asInt64 is a small coerce helper for the SQLite driver's eclectic
// numeric types (int64 / float64 / []byte / nil).
func asInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case []byte:
		n, _ := strconv.ParseInt(string(x), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}
