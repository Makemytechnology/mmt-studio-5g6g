// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_mbs.go — REST surface for 5G Multicast/Broadcast Services.
//
// Wires `safety/mbs` to /api/mbs/*. The package owns MBS sessions
// (multicast/broadcast, lifecycle), service areas (TAI scoping),
// member management, content delivery (immediate + scheduled), and
// the audit log.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 23.247 §4.1   5G MBS architecture (umbrella).
//   - TS 23.247 §4.2   MBS reference points (N6mb, MBSF, MBSU, MB-UPF).
//   - TS 23.247 §7     MBS Session Procedures (Create / Activate /
//                      Deactivate / Release lifecycle).
//   - TS 23.247 §7.2   MBS service-area handling (TAI-list scoping).
//   - TS 22.146 / 22.246  MBMS / MBS service requirements.
//
// Response shapes match `templates/mbs.html`: every endpoint returns
// `{ok, ...}` envelopes keyed by domain noun (`sessions`, `session`,
// `areas`, `area`, `members`, `content_log`, `delivery`, `stats`).
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/safety/mbs"
)

func (s *Server) registerMBSRoutes() {
	r := s.Router

	// ── Stats / dashboard ─────────────────────────────────────────
	r.Get("/api/mbs/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": mbs.GetStats()})
	})
	r.Get("/api/mbs/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": mbs.GetStats()})
	})

	// ── Sessions (TS 23.247 §7) ───────────────────────────────────
	r.Get("/api/mbs/sessions", func(w http.ResponseWriter, rq *http.Request) {
		sessionType := rq.URL.Query().Get("session_type")
		status := rq.URL.Query().Get("status")
		list, err := mbs.ListSessions(sessionType, status)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "sessions": list})
	})

	r.Post("/api/mbs/sessions", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TMGI           string `json:"tmgi"`
			Name           string `json:"name"`
			SessionType    string `json:"session_type"`
			QoS5QI         int    `json:"qos_5qi"`
			AreaID         *int64 `json:"area_id,omitempty"`
			MaxBitrateKbps int    `json:"max_bitrate_kbps"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		sess, err := mbs.CreateSession(d.TMGI, d.Name, d.SessionType,
			d.QoS5QI, d.AreaID, d.MaxBitrateKbps)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"ok": true, "session": sess})
	})

	r.Get("/api/mbs/sessions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		sess, err := mbs.GetSession(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sess == nil {
			jsonError(w, "session not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "session": sess})
	})

	r.Delete("/api/mbs/sessions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := mbs.DeleteSession(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Post("/api/mbs/sessions/{id}/activate", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		sess, err := mbs.ActivateSession(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "session": sess})
	})

	r.Post("/api/mbs/sessions/{id}/deactivate", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		sess, err := mbs.DeactivateSession(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "session": sess})
	})

	// ── Members (multicast) ──────────────────────────────────────
	r.Get("/api/mbs/sessions/{id}/members", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		list, err := mbs.ListMembers(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "members": list})
	})

	r.Post("/api/mbs/sessions/{id}/join", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var d struct {
			IMSI string `json:"imsi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := mbs.JoinSession(id, d.IMSI); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Post("/api/mbs/sessions/{id}/leave", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var d struct {
			IMSI string `json:"imsi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := mbs.LeaveSession(id, d.IMSI); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Service Areas (TS 23.247 §7.2) ───────────────────────────
	r.Get("/api/mbs/areas", func(w http.ResponseWriter, _ *http.Request) {
		list, err := mbs.ListAreas()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "areas": list})
	})

	r.Post("/api/mbs/areas", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name           string `json:"name"`
			TrackingAreas  string `json:"tracking_areas"`
			Description    string `json:"description"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		area, err := mbs.CreateArea(d.Name, d.TrackingAreas, d.Description)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"ok": true, "area": area})
	})

	r.Delete("/api/mbs/areas/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := mbs.DeleteArea(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── TAI list management on an existing area (TS 23.247 §7.2) ─
	// Lets the operator add or drop TAIs without recreating the
	// area (which would invalidate any session referencing it).
	r.Post("/api/mbs/areas/{id}/tais", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var d struct {
			Append []string `json:"append"`
			Remove []string `json:"remove"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if len(d.Append) == 0 && len(d.Remove) == 0 {
			jsonError(w, "append or remove required",
				http.StatusBadRequest)
			return
		}
		var row map[string]interface{}
		if len(d.Remove) > 0 {
			row, err = mbs.RemoveTAIs(id, d.Remove)
			if err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if len(d.Append) > 0 {
			row, err = mbs.AppendTAIs(id, d.Append)
			if err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		jsonReply(w, map[string]any{"ok": true, "area": row})
	})

	// ── Content delivery (TS 23.247 §7) ──────────────────────────
	r.Post("/api/mbs/sessions/{id}/send", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var d struct {
			ContentType string `json:"content_type"`
			ContentData string `json:"content_data"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// content_size = len(content_data); the wire payload stays
		// off the audit log (we only record metadata).
		row, err := mbs.SendContent(id, d.ContentType, len(d.ContentData))
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "delivery": row})
	})

	r.Post("/api/mbs/sessions/{id}/schedule", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var d struct {
			ContentType string `json:"content_type"`
			ContentData string `json:"content_data"`
			DeliverAt   string `json:"deliver_at"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		row, err := mbs.ScheduleContent(id, d.ContentType,
			len(d.ContentData), d.DeliverAt)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "delivery": row})
	})

	r.Get("/api/mbs/content-log", func(w http.ResponseWriter, rq *http.Request) {
		limit := 200
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := mbs.ListContentLog(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "content_log": list})
	})
}
