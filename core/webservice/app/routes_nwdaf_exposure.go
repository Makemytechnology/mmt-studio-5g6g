// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_nwdaf_exposure.go — REST surface for NWDAF Analytics
// Exposure to AFs / 3rd-parties via NEF (TS 23.288 §6.1.1.2 +
// §6.1.2.2; TS 29.522 §4.4 northbound shape).
//
// Wires `nf/nwdaf/exposure` to /api/nwdaf/exposure/*. The package
// owns consumer registration (with API keys), per-consumer
// subscriptions, the audit log, and the periodic notifier. This
// surface drives `templates/nwdaf_exposure.html`.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 23.288 §6.1.1.2 — Analytics subscribe / unsubscribe by AFs
//                          via NEF (subscribe / notify).
//   - TS 23.288 §6.1.2.2 — Analytics request by AFs via NEF
//                          (one-shot, query_type='one_shot').
//   - TS 23.288 §6.1.3   — Contents of Analytics Exposure (the
//                          notification + one-shot payload shape).
//   - TS 29.522 §4.4     — NEF northbound APIs (Nnef_AnalyticsExposure
//                          shape).
//
// Deferred (PDFs not local; TODO(spec:) prose only):
//
//   - TS 29.522 §5 JSON schemas — surface keeps the same fields; not
//                                  bit-exact OpenAPI yet.
//   - TS 23.288 §6.2.9 user consent gate — only per-consumer
//                                          allow-list is enforced.
//
// All response shapes are `{ok: true, ...}` envelopes — matches
// `templates/nwdaf_exposure.html` (`d.ok && d.consumers`, `d.ok &&
// d.subscriptions`, `d.ok && d.types`, `d.ok && d.log`).
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/nf/nwdaf"
	"github.com/mmt/mmt-studio-core/nf/nwdaf/analytics"
	"github.com/mmt/mmt-studio-core/nf/nwdaf/exposure"
)

func (s *Server) registerNWDAFExposureRoutes() {
	r := s.Router

	// ── Stats / dashboard ────────────────────────────────────────
	r.Get("/api/nwdaf/exposure/stats", func(w http.ResponseWriter, _ *http.Request) {
		st, err := exposure.GetStats()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		st["ok"] = true
		jsonReply(w, st)
	})

	// ── Supported exposure types (Stage-3 query-string vocabulary) ──
	r.Get("/api/nwdaf/exposure/types", func(w http.ResponseWriter, _ *http.Request) {
		types := exposure.ListExposureTypes()
		jsonReply(w, map[string]any{"ok": true, "types": types})
	})

	// ── Consumers ─────────────────────────────────────────────────
	r.Get("/api/nwdaf/exposure/consumers", func(w http.ResponseWriter, _ *http.Request) {
		list, err := exposure.ListConsumers()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "consumers": list})
	})

	r.Post("/api/nwdaf/exposure/consumers", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name             string   `json:"name"`
			CallbackURL      string   `json:"callback_url"`
			APIKey           string   `json:"api_key"`
			AllowedAnalytics []string `json:"allowed_analytics"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		// Validate allowed_analytics against the canonical IDs so
		// CheckAnalyticsPermission can never reject a known-good type.
		// Stage-3 callers may send either the lowercase Stage-3 query
		// name ("nf_load", from TS 29.520 §5.3.2 EventID enums) or the
		// uppercase internal Analytics ID ("NF_LOAD", TS 23.288 §6.1).
		// Normalise through exposure.ExposureTypes before validating
		// so both spellings round-trip.
		for i, a := range d.AllowedAnalytics {
			if a == "" {
				continue
			}
			canon := a
			if mapped, ok := exposure.ExposureTypes[a]; ok {
				canon = mapped
			}
			if !analytics.ValidAnalyticsIDs[canon] {
				jsonError(w,
					"unknown analytics_id in allowed_analytics: "+a,
					http.StatusBadRequest)
				return
			}
			d.AllowedAnalytics[i] = canon
		}
		// Auto-mint an API key if the caller didn't supply one.
		if d.APIKey == "" {
			d.APIKey = exposure.GenerateAPIKey()
		}
		id, err := exposure.CreateConsumer(d.Name, d.CallbackURL,
			d.APIKey, d.AllowedAnalytics)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id, "api_key": d.APIKey})
	})

	r.Delete("/api/nwdaf/exposure/consumers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := exposure.DeleteConsumer(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Subscriptions (TS 23.288 §6.1.1.2) ────────────────────────
	r.Get("/api/nwdaf/exposure/subscriptions", func(w http.ResponseWriter, _ *http.Request) {
		list, err := exposure.ListSubscriptions()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "subscriptions": list})
	})

	r.Post("/api/nwdaf/exposure/subscriptions", func(w http.ResponseWriter, rq *http.Request) {
		// Panel sends target_imsi / target_slice as separate fields;
		// the package stores them as (target_type, target_id). Accept
		// both shapes so the panel JS works today and direct API
		// callers can use the canonical (target_type, target_id) form.
		var d struct {
			ConsumerID    int64  `json:"consumer_id"`
			AnalyticsType string `json:"analytics_type"`
			TargetType    string `json:"target_type"`
			TargetID      string `json:"target_id"`
			TargetIMSI    string `json:"target_imsi"`
			TargetSlice   string `json:"target_slice"`
			IntervalS     int    `json:"interval_s"`
			CallbackURL   string `json:"callback_url"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.ConsumerID == 0 {
			jsonError(w, "consumer_id required", http.StatusBadRequest)
			return
		}
		if d.AnalyticsType == "" {
			jsonError(w, "analytics_type required", http.StatusBadRequest)
			return
		}
		// Resolve panel-style fields to (target_type, target_id).
		if d.TargetType == "" {
			switch {
			case d.TargetIMSI != "":
				d.TargetType, d.TargetID = "imsi", d.TargetIMSI
			case d.TargetSlice != "":
				d.TargetType, d.TargetID = "slice", d.TargetSlice
			default:
				d.TargetType = "network"
			}
		}
		// target_type follows TS 23.288 §6.2.2.2
		// targetOfAnalyticsReporting (UE / slice / NF / NF set /
		// area / network-wide). Schema CHECK matches.
		switch d.TargetType {
		case "imsi", "slice", "network", "nf", "nf_set", "area":
		default:
			jsonError(w,
				"target_type must be one of imsi|slice|network|nf|nf_set|area",
				http.StatusBadRequest)
			return
		}
		if d.IntervalS <= 0 {
			d.IntervalS = 60
		}
		id, err := exposure.CreateSubscription(d.ConsumerID,
			d.AnalyticsType, d.TargetType, d.TargetID,
			d.IntervalS, d.CallbackURL)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	r.Delete("/api/nwdaf/exposure/subscriptions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := exposure.DeleteSubscription(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── One-shot query (TS 23.288 §6.1.2.2 by AFs via NEF) ────────
	// Panel: GET /api/nwdaf/exposure/analytics/{type}?imsi=&slice=
	// Maps the external Stage-3 type name (ue_mobility, nf_load, …)
	// to the internal Analytics ID, computes via nwdaf.DefaultService,
	// and audit-logs as query_type='one_shot'.
	r.Get("/api/nwdaf/exposure/analytics/{type}", func(w http.ResponseWriter, rq *http.Request) {
		extType := chi.URLParam(rq, "type")
		internalID, ok := exposure.ExposureTypes[extType]
		if !ok {
			jsonError(w,
				"unknown exposure type; see /api/nwdaf/exposure/types",
				http.StatusBadRequest)
			exposure.LogQuery(nil, extType, "one_shot",
				http.StatusBadRequest)
			return
		}

		// API-key gate (optional but enforced when present).
		var consumerID *int64
		if key := rq.Header.Get("X-API-Key"); key != "" {
			c, err := exposure.ValidateAPIKey(key)
			if err != nil {
				jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if c == nil {
				jsonError(w, "invalid API key", http.StatusUnauthorized)
				exposure.LogQuery(nil, extType, "one_shot",
					http.StatusUnauthorized)
				return
			}
			// The allow-list is keyed by internal Analytics ID
			// (validated at consumer-create time), so check against
			// `internalID` rather than the external Stage-3 alias.
			if !exposure.CheckAnalyticsPermission(c, internalID) {
				jsonError(w, "consumer not authorised for this analytics type",
					http.StatusForbidden)
				if v, ok := c["id"].(int64); ok {
					consumerID = &v
				}
				exposure.LogQuery(consumerID, extType, "one_shot",
					http.StatusForbidden)
				return
			}
			if v, ok := c["id"].(int64); ok {
				consumerID = &v
			}
		}

		imsi := rq.URL.Query().Get("imsi")
		slice := rq.URL.Query().Get("slice") // surfaced as DNN scope today;
		// the SLICE_LOAD analytic uses targetDNN as its slice key —
		// upstream §6.3 is keyed by S-NSSAI, so map slice -> dnn slot.

		// TS 23.288 §6.2.9 user-consent gate. When the query targets a
		// specific UE (imsi != "") and a consumer is identified, the
		// NEF must check consent before exposing UE-scoped analytics.
		// The gate only fires for UE-scoped queries — slice/network
		// scopes don't carry a consent dimension.
		if imsi != "" && consumerID != nil {
			ok, reason := exposure.ConsentAllowed(*consumerID, imsi)
			if !ok {
				jsonError(w, "user consent denied: "+reason,
					http.StatusForbidden)
				exposure.LogQuery(consumerID, extType, "one_shot",
					http.StatusForbidden)
				return
			}
		}

		window := 300
		if v := rq.URL.Query().Get("window_sec"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				window = n
			}
		}
		res := nwdaf.DefaultService.GetAnalytics(internalID, imsi, slice, window)
		exposure.LogQuery(consumerID, extType, "one_shot", http.StatusOK)

		jsonReply(w, map[string]any{
			"ok":             true,
			"exposure_type":  extType,
			"analytics_id":   internalID,
			"result":         res,
		})
	})

	// ── Audit log (with filters per TS 23.288 ops triage) ────────
	// Filters: ?consumer_id=&type=&query_type=&since=&limit=
	r.Get("/api/nwdaf/exposure/log", func(w http.ResponseWriter, rq *http.Request) {
		filter := exposure.LogFilter{
			AnalyticsType: rq.URL.Query().Get("type"),
			QueryType:     rq.URL.Query().Get("query_type"),
			Since:         rq.URL.Query().Get("since"),
		}
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				filter.Limit = n
			}
		}
		if v := rq.URL.Query().Get("consumer_id"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				jsonError(w, "consumer_id must be integer",
					http.StatusBadRequest)
				return
			}
			filter.ConsumerID = &n
		}
		if filter.QueryType != "" &&
			filter.QueryType != "subscription" &&
			filter.QueryType != "one_shot" {
			jsonError(w, "query_type must be subscription|one_shot",
				http.StatusBadRequest)
			return
		}
		list, err := exposure.GetLogFiltered(filter)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "log": list, "count": len(list),
		})
	})

	s.registerNWDAFExposureHardeningRoutes()
}

// registerNWDAFExposureHardeningRoutes wires the consumer / sub
// PATCH paths, the API-key rotate button, the dry-run permission
// probe, and the TS 23.288 §6.2.9 user-consent surface. Split out so
// the main register function stays browsable.
func (s *Server) registerNWDAFExposureHardeningRoutes() {
	r := s.Router

	// ── Single consumer GET ──────────────────────────────────────
	r.Get("/api/nwdaf/exposure/consumers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		row, err := exposure.GetConsumer(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			jsonError(w, "consumer not found", http.StatusNotFound)
			return
		}
		exposure.MarshalConsumerAllowed(row)
		jsonReply(w, map[string]any{"ok": true, "consumer": row})
	})

	// ── Consumer PATCH (rename, callback, allow-list, active flag) ─
	r.Patch("/api/nwdaf/exposure/consumers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var d map[string]any
		if !decodeJSON(w, rq, &d) {
			return
		}
		// Validate any analytics IDs in the patch — accept Stage-3
		// lowercase aliases per TS 29.520 §5.3.2 alongside the
		// uppercase canonical TS 23.288 §6.1 names.
		if v, ok := d["allowed_analytics"]; ok {
			ids := []string{}
			switch t := v.(type) {
			case []any:
				for _, x := range t {
					if s, ok := x.(string); ok {
						ids = append(ids, s)
					}
				}
			}
			normalised := make([]string, 0, len(ids))
			for _, a := range ids {
				if a == "" {
					continue
				}
				canon := a
				if mapped, ok := exposure.ExposureTypes[a]; ok {
					canon = mapped
				}
				if !analytics.ValidAnalyticsIDs[canon] {
					jsonError(w,
						"unknown analytics_id in allowed_analytics: "+a,
						http.StatusBadRequest)
					return
				}
				normalised = append(normalised, canon)
			}
			d["allowed_analytics"] = normalised
		}
		row, err := exposure.UpdateConsumer(id, d)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if row == nil {
			jsonError(w, "consumer not found", http.StatusNotFound)
			return
		}
		exposure.MarshalConsumerAllowed(row)
		jsonReply(w, map[string]any{"ok": true, "consumer": row})
	})

	// ── API-key rotation ─────────────────────────────────────────
	r.Post("/api/nwdaf/exposure/consumers/{id}/rotate-key", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		newKey, row, err := exposure.RotateAPIKey(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if newKey == "" {
			jsonError(w, "consumer not found", http.StatusNotFound)
			return
		}
		exposure.MarshalConsumerAllowed(row)
		// Audit-log the rotation so the trail records the swap.
		exposure.LogQuery(&id, "api_key", "subscription",
			http.StatusOK)
		jsonReply(w, map[string]any{
			"ok":       true,
			"id":       id,
			"api_key":  newKey,
			"consumer": row,
		})
	})

	// ── Subscription GET / PATCH ─────────────────────────────────
	r.Get("/api/nwdaf/exposure/subscriptions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		row, err := exposure.GetSubscription(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			jsonError(w, "subscription not found",
				http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "subscription": row})
	})

	r.Patch("/api/nwdaf/exposure/subscriptions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var d map[string]any
		if !decodeJSON(w, rq, &d) {
			return
		}
		row, err := exposure.UpdateSubscription(id, d)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if row == nil {
			jsonError(w, "subscription not found",
				http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "subscription": row})
	})

	// ── Permission probe (dry-run access check) ──────────────────
	r.Post("/api/nwdaf/exposure/check-permission", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			APIKey       string `json:"api_key"`
			ExposureType string `json:"exposure_type"`
			SUPI         string `json:"supi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		out := exposure.CheckPermission(d.APIKey, d.ExposureType, d.SUPI)
		out["ok"] = true
		jsonReply(w, out)
	})

	// ── User consent (TS 23.288 §6.2.9) ──────────────────────────
	r.Get("/api/nwdaf/exposure/consent/policy", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"ok":   true,
			"mode": exposure.GetConsentMode(),
		})
	})

	r.Post("/api/nwdaf/exposure/consent/policy", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Mode string `json:"mode"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := exposure.SetConsentMode(d.Mode); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "mode": d.Mode})
	})

	r.Get("/api/nwdaf/exposure/consent", func(w http.ResponseWriter, rq *http.Request) {
		var consumerID int64
		if v := rq.URL.Query().Get("consumer_id"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				jsonError(w, "consumer_id must be integer",
					http.StatusBadRequest)
				return
			}
			consumerID = n
		}
		limit := 200
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		rows, err := exposure.ListConsent(consumerID, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		jsonReply(w, map[string]any{
			"ok":      true,
			"consent": rows,
			"count":   len(rows),
			"mode":    exposure.GetConsentMode(),
		})
	})

	r.Post("/api/nwdaf/exposure/consent", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			ConsumerID int64  `json:"consumer_id"`
			SUPI       string `json:"supi"`
			Allow      bool   `json:"allow"`
			Reason     string `json:"reason"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.ConsumerID == 0 || d.SUPI == "" {
			jsonError(w, "consumer_id and supi required",
				http.StatusBadRequest)
			return
		}
		if err := exposure.SetConsent(d.ConsumerID, d.SUPI,
			d.Allow, d.Reason); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":          true,
			"consumer_id": d.ConsumerID,
			"supi":        d.SUPI,
			"allow":       d.Allow,
		})
	})
}
