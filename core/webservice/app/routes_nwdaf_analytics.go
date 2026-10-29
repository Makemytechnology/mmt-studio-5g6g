// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_nwdaf_analytics.go — REST surface for NWDAF analytics
// exposure (TS 23.288 §6.1).
//
// Wires `nf/nwdaf` to /api/nwdaf/*. The package owns analytics
// computation across the supported Analytics IDs (NF_LOAD, UE_MOBILITY,
// UE_COMMUNICATION, QOS_SUSTAINABILITY, ABNORMAL_BEHAVIOUR, PDU_SESSION,
// SLICE_LOAD), the periodic data-collection loop, and the
// subscription store. This surface drives `templates/nwdaf.html`.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 23.288 §6.1   Procedures for analytics exposure (umbrella).
//   - TS 23.288 §6.1.1 Analytics Subscribe / Unsubscribe.
//   - TS 23.288 §6.1.2 Analytics Request (one-shot).
//   - TS 23.288 §6.1.3 Contents of Analytics Exposure.
//   - TS 23.288 §6.2   Procedures for Data Collection (collectionLoop).
//
// Deferred (PDFs not local; TODO(spec:) prose only):
//
//   - TS 29.520 (Nnwdaf services Stage 3) — JSON-schema-faithful surface.
//
// All response shapes are `{ok: true, ...}` envelopes — matches
// `templates/nwdaf.html` (`d.ok && d.analytics.NF_LOAD …`).
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/nf/nwdaf"
	"github.com/mmt/mmt-studio-core/nf/nwdaf/analytics"
)

// parseMinConfidence extracts ?min_confidence= as a float in [0, 1].
// Anything outside that range or unparseable is ignored (returns 0
// = no filter), matching the existing behaviour where omitting the
// parameter means "show everything".
func parseMinConfidence(rq *http.Request) float64 {
	v := rq.URL.Query().Get("min_confidence")
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		return 0
	}
	return f
}

// supportedAnalyticsIDs is the ordered tuple the dashboard renders.
// Listed explicitly so the panel layout is deterministic across polls;
// `analytics.ValidAnalyticsIDs` (a map) loses ordering.
var supportedAnalyticsIDs = []string{
	analytics.AnalyticsNFLoad,
	analytics.AnalyticsUEMobility,
	analytics.AnalyticsUECommunication,
	analytics.AnalyticsQoSSustainability,
	analytics.AnalyticsAbnormalBehaviour,
	analytics.AnalyticsPDUSession,
	analytics.AnalyticsSliceLoad,
}

func (s *Server) registerNWDAFAnalyticsRoutes() {
	r := s.Router

	// ── Dashboard aggregator (panel) ──────────────────────────────
	// One round-trip computes every Analytics ID and packs them by
	// name; the panel reads a.NF_LOAD.result.load_level, a.UE_MOBILITY
	// .result.current_ues, etc. directly.
	r.Get("/api/nwdaf/analytics", func(w http.ResponseWriter, rq *http.Request) {
		window := 300
		if v := rq.URL.Query().Get("window_sec"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				window = n
			}
		}
		minConf := parseMinConfidence(rq)
		imsi := rq.URL.Query().Get("imsi")
		dnn := rq.URL.Query().Get("dnn")

		out := map[string]any{}
		filtered := 0
		for _, id := range supportedAnalyticsIDs {
			res := nwdaf.DefaultService.GetAnalytics(id, imsi, dnn, window)
			if minConf > 0 && res.Confidence < minConf {
				filtered++
				continue
			}
			out[id] = res
		}
		jsonReply(w, map[string]any{
			"ok":             true,
			"analytics":      out,
			"window_sec":     window,
			"min_confidence": minConf,
			"filtered_out":   filtered,
			"supported_ids":  supportedAnalyticsIDs,
		})
	})

	// ── Single Analytics ID (TS 23.288 §6.1.2 Analytics Request) ──
	r.Get("/api/nwdaf/analytics/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id := chi.URLParam(rq, "id")
		if !analytics.ValidAnalyticsIDs[id] {
			jsonError(w,
				"unknown analytics_id; valid: NF_LOAD|UE_MOBILITY|UE_COMMUNICATION|QOS_SUSTAINABILITY|ABNORMAL_BEHAVIOUR|PDU_SESSION|SLICE_LOAD",
				http.StatusBadRequest)
			return
		}
		window := 300
		if v := rq.URL.Query().Get("window_sec"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				window = n
			}
		}
		imsi := rq.URL.Query().Get("imsi")
		dnn := rq.URL.Query().Get("dnn")
		minConf := parseMinConfidence(rq)
		res := nwdaf.DefaultService.GetAnalytics(id, imsi, dnn, window)
		// TS 23.288 §6.1.3 — exposure may carry a confidence score
		// per prediction; the consumer can ask for results above a
		// threshold so the panel only renders high-confidence rows.
		if minConf > 0 && res.Confidence < minConf {
			jsonReply(w, map[string]any{
				"ok":             true,
				"result":         nil,
				"min_confidence": minConf,
				"actual":         res.Confidence,
				"filtered_out":   true,
			})
			return
		}
		jsonReply(w, map[string]any{"ok": true, "result": res})
	})

	// ── Subscriptions (TS 23.288 §6.1.1 Subscribe/Unsubscribe) ────
	r.Get("/api/nwdaf/subscriptions", func(w http.ResponseWriter, _ *http.Request) {
		list := nwdaf.DefaultService.ListSubscriptions()
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "subscriptions": list})
	})

	r.Post("/api/nwdaf/subscriptions", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			ConsumerNF  string `json:"consumer_nf"`
			AnalyticsID string `json:"analytics_id"`
			TargetIMSI  string `json:"target_imsi"`
			TargetDNN   string `json:"target_dnn"`
			TargetSST   string `json:"target_sst"`
			CallbackURL string `json:"callback_url"`
			IntervalSec int    `json:"interval_sec"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.ConsumerNF == "" {
			jsonError(w, "consumer_nf required", http.StatusBadRequest)
			return
		}
		if !analytics.ValidAnalyticsIDs[d.AnalyticsID] {
			jsonError(w,
				"unknown analytics_id; valid: NF_LOAD|UE_MOBILITY|UE_COMMUNICATION|QOS_SUSTAINABILITY|ABNORMAL_BEHAVIOUR|PDU_SESSION|SLICE_LOAD",
				http.StatusBadRequest)
			return
		}
		if d.IntervalSec <= 0 {
			d.IntervalSec = 60
		}
		subID := nwdaf.DefaultService.Subscribe(d.ConsumerNF, d.AnalyticsID,
			d.TargetIMSI, d.TargetDNN, d.TargetSST,
			d.CallbackURL, d.IntervalSec)
		if subID == "" {
			jsonError(w, "subscription create failed",
				http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "sub_id": subID})
	})

	r.Delete("/api/nwdaf/subscriptions/{sid}", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "sid")
		if sid == "" {
			jsonError(w, "sub_id required", http.StatusBadRequest)
			return
		}
		ok := nwdaf.DefaultService.Unsubscribe(sid)
		if !ok {
			jsonError(w, "subscription not found",
				http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "sub_id": sid})
	})

	// ── Historical results (TS 23.288 §6.1.3 contents over time) ──
	r.Get("/api/nwdaf/recent", func(w http.ResponseWriter, rq *http.Request) {
		id := rq.URL.Query().Get("analytics_id")
		if id != "" && !analytics.ValidAnalyticsIDs[id] {
			jsonError(w, "unknown analytics_id", http.StatusBadRequest)
			return
		}
		limit := 20
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list := nwdaf.DefaultService.GetRecentAnalytics(id, limit)
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "analytics_id": id,
			"recent": list})
	})

	// ── Service status ────────────────────────────────────────────
	r.Get("/api/nwdaf/status", func(w http.ResponseWriter, _ *http.Request) {
		st := nwdaf.DefaultService.Status()
		st["ok"] = true
		st["supported_ids"] = supportedAnalyticsIDs
		st["ingest"] = nwdaf.DefaultService.IngestStats()
		jsonReply(w, st)
	})

	// ── Data ingestion (TS 23.288 §6.2 Procedures for Data Collection)
	// External NFs / testers POST DataPoints; the next analytics
	// computation call sees them without waiting for a collection-loop
	// tick.
	r.Post("/api/nwdaf/data", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SourceNF    string  `json:"source_nf"`
			AnalyticsID string  `json:"analytics_id"`
			IMSI        string  `json:"imsi"`
			DNN         string  `json:"dnn"`
			DataJSON    string  `json:"data_json"`
			CollectedAt float64 `json:"collected_at"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		id, err := nwdaf.DefaultService.IngestDataPoint(analytics.DataPoint{
			SourceNF:    d.SourceNF,
			AnalyticsID: d.AnalyticsID,
			IMSI:        d.IMSI,
			DNN:         d.DNN,
			DataJSON:    d.DataJSON,
			CollectedAt: d.CollectedAt,
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Single subscription GET (TS 23.288 §6.1.1 read) ──────────
	r.Get("/api/nwdaf/subscriptions/{sid}", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "sid")
		row, err := nwdaf.DefaultService.GetSubscription(sid)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if row == nil {
			jsonError(w, "subscription not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "subscription": row})
	})

	// ── Subscription update (TS 23.288 §6.1.1 modify) ────────────
	// Sparse PATCH: target_imsi, target_dnn, target_sst, callback_url,
	// interval_sec, status. Unknown keys → 400.
	r.Patch("/api/nwdaf/subscriptions/{sid}", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "sid")
		var d map[string]any
		if !decodeJSON(w, rq, &d) {
			return
		}
		allowed := map[string]bool{
			"target_imsi": true, "target_dnn": true, "target_sst": true,
			"callback_url": true, "interval_sec": true, "status": true,
		}
		patch := map[string]any{}
		for k, v := range d {
			if allowed[k] {
				patch[k] = v
			}
		}
		if len(patch) == 0 {
			jsonError(w, "no allowed fields in patch (target_imsi|target_dnn|target_sst|callback_url|interval_sec|status)",
				http.StatusBadRequest)
			return
		}
		// `status` vocabulary check — schema doesn't CHECK this column
		// but the service-level lifecycle is {active, suspended,
		// cancelled}; reject anything else early.
		if v, ok := patch["status"].(string); ok {
			switch v {
			case "active", "suspended", "cancelled":
			default:
				jsonError(w, "status must be active|suspended|cancelled",
					http.StatusBadRequest)
				return
			}
		}
		ok, err := nwdaf.DefaultService.UpdateSubscription(sid, patch)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !ok {
			jsonError(w, "subscription not found", http.StatusNotFound)
			return
		}
		row, _ := nwdaf.DefaultService.GetSubscription(sid)
		jsonReply(w, map[string]any{"ok": true, "subscription": row})
	})
}
