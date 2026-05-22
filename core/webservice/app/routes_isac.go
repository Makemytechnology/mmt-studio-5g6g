// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_isac.go — REST surface for Integrated Sensing and
// Communication (ISAC) over 5GS (TS 22.137).
//
// ISAC is a sensing service the operator runs on top of the radio:
// the same waveform that carries voice/data is reflected off objects
// in the environment, and a sensing-capable receiver recovers
// range / velocity / presence / shape / motion from those reflections.
// This is **not** edge computing and **not** positioning — it is its
// own service tier under sensing/.
//
// Routes:
//
//   /api/isac/sessions[/{id}][/(activate|cancel|complete|data[/latest])]
//                              — TS 22.137 §5.2.2 session FSM (created
//                                → active → completed | cancelled).
//   /api/isac/data             — convenience POST: body carries
//                                session_id (TS 22.137 §5.2.1 narrative
//                                — sensing receivers post measurements).
//   /api/isac/data/{session_id} — list / paginate.
//   /api/isac/consumers        — TS 22.137 §5.2.3 network-exposure peer
//                                registry (NEF-side third party).
//   /api/isac/subscribe        — consumer ↔ session subscription.
//   /api/isac/status           — aggregate counters.
package app

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/sensing/isac"
)

func (s *Server) registerISACRoutes() {
	r := s.Router

	// ── Sessions (TS 22.137 §5.2.2 FSM) ──────────────────────────
	r.Get("/api/isac/sessions", func(w http.ResponseWriter, rq *http.Request) {
		sessions, err := isac.ListSessions(
			rq.URL.Query().Get("sensing_type"),
			rq.URL.Query().Get("status"),
		)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sessions == nil {
			sessions = []isac.Session{}
		}
		jsonReply(w, sessions)
	})
	r.Get("/api/isac/sessions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		s, err := isac.GetSession(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, s)
	})
	r.Post("/api/isac/sessions", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			SensingType     string `json:"sensing_type"`
			TargetArea      string `json:"target_area"`
			Resolution      string `json:"resolution"`
			ReportIntervalS int    `json:"report_interval_s"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		s, err := isac.CreateSession(b.SensingType, b.TargetArea, b.Resolution, b.ReportIntervalS)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, s)
	})
	r.Post("/api/isac/sessions/{id}/activate", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		s, err := isac.ActivateSession(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, s)
	})
	r.Post("/api/isac/sessions/{id}/cancel", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		s, err := isac.CancelSession(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, s)
	})
	r.Post("/api/isac/sessions/{id}/complete", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		s, err := isac.CompleteSession(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, s)
	})
	r.Delete("/api/isac/sessions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := isac.DeleteSession(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Per-session data path (TS 22.137 §5.2.1) ─────────────────
	r.Post("/api/isac/sessions/{id}/data", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		var b struct {
			DetectedObjects string `json:"detected_objects"`
			Environmental   string `json:"environmental"`
			RawData         string `json:"raw_data"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		var detected, env, raw *string
		if b.DetectedObjects != "" {
			detected = &b.DetectedObjects
		}
		if b.Environmental != "" {
			env = &b.Environmental
		}
		if b.RawData != "" {
			raw = &b.RawData
		}
		dp, err := isac.ReportData(id, detected, env, raw)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, dp)
	})
	r.Get("/api/isac/sessions/{id}/data", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		list, err := isac.ListData(id, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []isac.DataPoint{}
		}
		jsonReply(w, list)
	})
	r.Get("/api/isac/sessions/{id}/data/latest", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		dp, err := isac.LatestData(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if dp == nil {
			jsonError(w, "no data", http.StatusNotFound)
			return
		}
		jsonReply(w, dp)
	})
	r.Get("/api/isac/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, isac.Status())
	})

	// Convenience POST: body carries `session_id`, kept distinct
	// from the per-session sub-route above so OAM scripts can post
	// sensing data without threading the URL.
	r.Post("/api/isac/data", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			SessionID       int64       `json:"session_id"`
			DetectedObjects interface{} `json:"detected_objects"`
			Environmental   interface{} `json:"environmental"`
			RawData         interface{} `json:"raw_data"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		var detected, env, raw *string
		if b.DetectedObjects != nil {
			s, _ := json.Marshal(b.DetectedObjects)
			ss := string(s)
			detected = &ss
		}
		if b.Environmental != nil {
			s, _ := json.Marshal(b.Environmental)
			ss := string(s)
			env = &ss
		}
		if b.RawData != nil {
			s, _ := json.Marshal(b.RawData)
			ss := string(s)
			raw = &ss
		}
		dp, err := isac.ReportData(b.SessionID, detected, env, raw)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, dp)
	})
	r.Get("/api/isac/data/{session_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "session_id"), 10, 64)
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		list, err := isac.ListData(id, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []isac.DataPoint{}
		}
		jsonReply(w, list)
	})

	// ── Consumers (TS 22.137 §5.2.3 network exposure) ────────────
	r.Get("/api/isac/consumers", func(w http.ResponseWriter, rq *http.Request) {
		list, err := isac.ListConsumers()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []isac.Consumer{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/isac/consumers", func(w http.ResponseWriter, rq *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(rq.Body).Decode(&body); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		name, _ := body["name"].(string)
		callback, _ := body["callback_url"].(string)
		c, err := isac.RegisterConsumer(name, callback)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, c)
	})
	r.Get("/api/isac/consumers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		c, err := isac.GetConsumer(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if c == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, c)
	})
	r.Delete("/api/isac/consumers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := isac.DeleteConsumer(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Subscriptions (consumer ↔ session) ───────────────────────
	r.Post("/api/isac/subscribe", func(w http.ResponseWriter, rq *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(rq.Body).Decode(&body); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		consumerID := int64(jsonNum(body["consumer_id"]))
		sessionID := int64(jsonNum(body["session_id"]))
		if consumerID == 0 || sessionID == 0 {
			jsonError(w, "consumer_id and session_id are required", http.StatusBadRequest)
			return
		}
		sub, err := isac.Subscribe(consumerID, sessionID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, sub)
	})
	r.Get("/api/isac/subscribe", func(w http.ResponseWriter, rq *http.Request) {
		consumerID, _ := strconv.ParseInt(rq.URL.Query().Get("consumer_id"), 10, 64)
		sessionID, _ := strconv.ParseInt(rq.URL.Query().Get("session_id"), 10, 64)
		list, err := isac.ListSubscriptions(consumerID, sessionID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []isac.Subscription{}
		}
		jsonReply(w, list)
	})
	r.Get("/api/isac/subscribe/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		sub, err := isac.GetSubscription(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sub == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, sub)
	})
	r.Delete("/api/isac/subscribe/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := isac.DeleteSubscription(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
}
