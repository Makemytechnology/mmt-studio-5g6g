// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_pws.go — REST surface for the Public Warning System.
//
// Wires `safety/pws` to /api/pws/*. The package owns the operator-
// facing CRUD + state machine for PWS alerts (draft → broadcasting
// → completed | cancelled) and the per-gNB delivery ledger; the
// AMF-side N2 (NGAP) fan-out lives in `nf/amf/pws/dispatch.go`.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 23.501 §4.4.1   PWS architecture (defers wire to TS 23.041).
//   - TS 23.501 §5.16.1  PWS functional description.
//   - TS 38.413 §8.9     NGAP Warning Message Transmission Procedures
//                        (Write-Replace / PWS Cancel / PWS Restart
//                        Indication / PWS Failure).
//
// All response shapes match `templates/pws.html`: every endpoint
// returns `{ok, ...}` with the body keyed by domain noun
// (`alerts`, `alert`, `stats`, `delivery_log`, `status`, `delivery`).
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/safety/pws"
)

func (s *Server) registerPWSRoutes() {
	r := s.Router

	// ── Stats / dashboard ─────────────────────────────────────────
	r.Get("/api/pws/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": pws.GetStats()})
	})
	r.Get("/api/pws/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": pws.GetStats()})
	})

	// ── Alerts CRUD + lifecycle (TS 23.501 §5.16.1) ──────────────
	r.Get("/api/pws/alerts", func(w http.ResponseWriter, rq *http.Request) {
		status := rq.URL.Query().Get("status")
		list, err := pws.ListAlerts(status)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Optional alert_type filter — done client-side; tester wants
		// the same shape regardless.
		atype := rq.URL.Query().Get("alert_type")
		if atype != "" {
			filtered := make([]map[string]interface{}, 0, len(list))
			for _, a := range list {
				if v, ok := a["alert_type"].(string); ok && v == atype {
					filtered = append(filtered, a)
				}
			}
			list = filtered
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "alerts": list})
	})

	r.Post("/api/pws/alerts", func(w http.ResponseWriter, rq *http.Request) {
		var cfg map[string]interface{}
		if !decodeJSON(w, rq, &cfg) {
			return
		}
		alert, err := pws.CreateAlert(cfg)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"ok": true, "alert": alert})
	})

	r.Get("/api/pws/alerts/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		a, err := pws.GetAlert(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if a == nil {
			jsonError(w, "alert not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "alert": a})
	})

	r.Delete("/api/pws/alerts/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := pws.DeleteAlert(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Post("/api/pws/alerts/{id}/broadcast", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		a, err := pws.BroadcastAlert(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "alert": a})
	})

	r.Post("/api/pws/alerts/{id}/cancel", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		a, err := pws.CancelAlert(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "alert": a})
	})

	r.Post("/api/pws/alerts/{id}/complete", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		a, err := pws.CompleteAlert(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "alert": a})
	})

	// ── Test-alert shortcut (TS 23.041 'test' alert_type) ────────
	// Operator panel wants a one-click "drill" — create + broadcast a
	// minimal alert in `test` mode without filling out the full form.
	r.Post("/api/pws/test-alert", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			MessageText string `json:"message_text"`
		}
		_ = decodeJSON(w, rq, &d)
		if d.MessageText == "" {
			d.MessageText = "This is a test alert. No action required."
		}
		alert, err := pws.CreateAlert(map[string]interface{}{
			"alert_type":   "test",
			"severity":     "minor",
			"urgency":      "expected",
			"category":     "test",
			"message_text": d.MessageText,
			"language":     "en",
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		idVal, _ := alert["id"].(int64)
		if idVal == 0 {
			if f, ok := alert["id"].(float64); ok {
				idVal = int64(f)
			}
		}
		broadcast, err := pws.BroadcastAlert(idVal)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "alert": broadcast})
	})

	// ── Delivery log + per-alert delivery status ─────────────────
	r.Post("/api/pws/alerts/{id}/delivery", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var d struct {
			GnbID  string `json:"gnb_id"`
			Status string `json:"status"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" {
			jsonError(w, "gnb_id required", http.StatusBadRequest)
			return
		}
		if err := pws.RecordDelivery(id, d.GnbID, d.Status); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{"ok": true})
	})

	r.Get("/api/pws/alerts/{id}/delivery-status", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		a, err := pws.GetAlert(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if a == nil {
			jsonError(w, "alert not found", http.StatusNotFound)
			return
		}
		deliveries, _ := pws.GetDeliveries(id)
		summary := map[string]int{
			"pending": 0, "delivered": 0,
			"failed": 0, "acknowledged": 0,
		}
		gnbs := map[string]bool{}
		for _, d := range deliveries {
			st, _ := d["status"].(string)
			summary[st]++
			if g, ok := d["gnb_id"].(string); ok {
				gnbs[g] = true
			}
		}
		jsonReply(w, map[string]any{
			"ok": true,
			"status": map[string]any{
				"alert_id":         id,
				"alert_status":     a["status"],
				"total_gnbs":       len(gnbs),
				"total_deliveries": len(deliveries),
				"delivery_summary": summary,
				"deliveries":       deliveries,
			},
		})
	})

	r.Get("/api/pws/delivery-log", func(w http.ResponseWriter, rq *http.Request) {
		// Aggregate the latest delivery rows across alerts. Take the
		// last 200 by descending id.
		limit := 200
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		all, err := pws.ListDeliveryLog(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if all == nil {
			all = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "delivery_log": all})
	})

	// ── CBS encoding preview (TS 23.041 placeholder) ─────────────
	r.Get("/api/pws/encode-preview", func(w http.ResponseWriter, rq *http.Request) {
		text := rq.URL.Query().Get("text")
		msgID, _ := strconv.Atoi(rq.URL.Query().Get("message_id"))
		ser, _ := strconv.Atoi(rq.URL.Query().Get("serial_number"))
		jsonReply(w, map[string]any{
			"ok":      true,
			"encoded": pws.EncodeCBSMessage(text, msgID, ser),
		})
	})
}
