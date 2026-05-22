// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_uas.go — REST surface for the Uncrewed Aerial Systems
// (UAS) service tier.
//
// Wires services/uas to /api/uas/* per the spec anchors that
// services/uas/uas.go cites in its package header:
//
//   - TS 22.125          UAS service requirements
//   - TS 23.256 §5.2.1   UAV registration (UAS NF state)
//   - TS 23.256 §5.2.3   UAV ↔ UAV-C C2 pairing
//   - TS 23.256 §5.2.4   Flight authorisation with USS/UTM
//   - TS 23.256 §5.2.5   Network Remote ID (Net-RID)
//   - TS 23.256 §5.2.6   UAV location reporting / tracking
//   - TS 23.256 §5.5     C2 communication (default 5QI=3)
//   - ASTM F3411-22a §4  Remote ID broadcast field semantics
//
// The local USS / UTM is a stand-in: AuthorizeFlight checks its own
// no-fly zones + per-UAV envelope. A real deployment forwards the
// request over UAE-USS / UAE-UTM (TS 22.125 §5.4) — the wire is
// deferred.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/services/uas"
)

func (s *Server) registerUASRoutes() {
	r := s.Router

	// ── Status / aggregate ───────────────────────────────────────
	r.Get("/api/uas/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, uas.GetUASStats())
	})

	// ── UAV registry (TS 23.256 §5.2.1) ──────────────────────────
	r.Get("/api/uas/registry", func(w http.ResponseWriter, _ *http.Request) {
		list, err := uas.ListUAVs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []uas.UAV{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/uas/registry/{uav_id}", func(w http.ResponseWriter, rq *http.Request) {
		uavID := chi.URLParam(rq, "uav_id")
		u, err := uas.GetUAVByUAVID(uavID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if u == nil {
			jsonError(w, "uav not found", http.StatusNotFound)
			return
		}
		jsonReply(w, u)
	})

	r.Post("/api/uas/registry", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI         string  `json:"imsi"`
			UAVID        string  `json:"uav_id"`
			SerialNumber string  `json:"serial_number"`
			Manufacturer string  `json:"manufacturer"`
			Model        string  `json:"model"`
			MaxSpeedMPS  float64 `json:"max_speed_mps"`
			MaxAltitudeM float64 `json:"max_altitude_m"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		id, err := uas.RegisterUAV(d.IMSI, d.UAVID, d.SerialNumber,
			d.Manufacturer, d.Model, d.MaxSpeedMPS, d.MaxAltitudeM)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Re-read so the response carries the canonical uav_id (the
		// helper auto-generates one when blank — TS 23.256 §5.2.5
		// CAA-Level UAV ID).
		got, _ := uas.GetUAV(id)
		uavID := d.UAVID
		if got != nil {
			uavID = got.UAVID
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok": true, "id": id, "uav_id": uavID,
		})
	})

	r.Delete("/api/uas/registry/{uav_id}", func(w http.ResponseWriter, rq *http.Request) {
		key := chi.URLParam(rq, "uav_id")
		// Accept either the numeric primary key or the string UAV ID
		// — operator-friendly. Numeric path wins if it parses cleanly
		// and a row with that id exists.
		if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
			if u, _ := uas.GetUAV(id); u != nil {
				if err := uas.DeleteUAV(id); err != nil {
					jsonError(w, err.Error(), http.StatusInternalServerError)
					return
				}
				jsonReply(w, map[string]any{"ok": true, "id": id, "uav_id": u.UAVID})
				return
			}
		}
		if err := uas.DeleteUAVByUAVID(key); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "uav_id": key})
	})

	// ── Flight authorisation (TS 23.256 §5.2.4) ──────────────────
	r.Post("/api/uas/authorize-flight", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			UAVID      string                 `json:"uav_id"`
			FlightPlan map[string]interface{} `json:"flight_plan"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.UAVID == "" {
			jsonError(w, "uav_id required", http.StatusBadRequest)
			return
		}
		res, err := uas.AuthorizeFlight(d.UAVID, d.FlightPlan)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, res)
	})

	r.Post("/api/uas/revoke-authorization", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			FlightID string `json:"flight_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.FlightID == "" {
			jsonError(w, "flight_id required", http.StatusBadRequest)
			return
		}
		if err := uas.RevokeAuthorization(d.FlightID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "flight_id": d.FlightID, "status": "revoked"})
	})

	r.Get("/api/uas/authorization/{uav_id}", func(w http.ResponseWriter, rq *http.Request) {
		uavID := chi.URLParam(rq, "uav_id")
		jsonReply(w, uas.CheckAuthorization(uavID))
	})

	// ── No-fly zones (TS 23.256 §5.2.4) ──────────────────────────
	r.Get("/api/uas/no-fly-zones", func(w http.ResponseWriter, _ *http.Request) {
		list, err := uas.ListNoFlyZones()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []uas.NoFlyZone{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/uas/no-fly-zones", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name    string   `json:"name"`
			LatMin  float64  `json:"lat_min"`
			LatMax  float64  `json:"lat_max"`
			LonMin  float64  `json:"lon_min"`
			LonMax  float64  `json:"lon_max"`
			AltMaxM *float64 `json:"alt_max_m"`
			Reason  string   `json:"reason"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		// Sanity: lat_min ≤ lat_max etc.; otherwise the box doesn't
		// describe a volume and checkNoFlyZones can never trigger.
		if d.LatMin > d.LatMax || d.LonMin > d.LonMax {
			jsonError(w, "lat_min/lon_min must be ≤ lat_max/lon_max",
				http.StatusBadRequest)
			return
		}
		id, err := uas.CreateNoFlyZone(d.Name, d.LatMin, d.LatMax,
			d.LonMin, d.LonMax, d.AltMaxM, d.Reason)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok": true, "id": id, "zone_id": id, "name": d.Name,
		})
	})

	r.Delete("/api/uas/no-fly-zones/{zone_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "zone_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid zone_id", http.StatusBadRequest)
			return
		}
		if err := uas.DeleteNoFlyZone(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Position / Net-RID (TS 23.256 §5.2.5 / §5.2.6) ───────────
	r.Post("/api/uas/position", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			UAVID      string  `json:"uav_id"`
			Latitude   float64 `json:"latitude"`
			Longitude  float64 `json:"longitude"`
			AltitudeM  float64 `json:"altitude_m"`
			HeadingDeg float64 `json:"heading_deg"`
			SpeedMPS   float64 `json:"speed_mps"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.UAVID == "" {
			jsonError(w, "uav_id required", http.StatusBadRequest)
			return
		}
		if err := uas.UpdatePosition(d.UAVID, d.Latitude, d.Longitude,
			d.AltitudeM, d.HeadingDeg, d.SpeedMPS); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "uav_id": d.UAVID})
	})

	r.Get("/api/uas/position/{uav_id}", func(w http.ResponseWriter, rq *http.Request) {
		uavID := chi.URLParam(rq, "uav_id")
		pos := uas.GetPosition(uavID)
		if pos == nil {
			jsonError(w, "no position data", http.StatusNotFound)
			return
		}
		jsonReply(w, pos)
	})

	r.Get("/api/uas/position/{uav_id}/history", func(w http.ResponseWriter, rq *http.Request) {
		uavID := chi.URLParam(rq, "uav_id")
		limit := 100
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		hist := uas.GetFlightHistory(uavID, limit)
		if hist == nil {
			hist = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"uav_id": uavID, "history": hist, "count": len(hist)})
	})

	r.Get("/api/uas/anomaly/{uav_id}", func(w http.ResponseWriter, rq *http.Request) {
		uavID := chi.URLParam(rq, "uav_id")
		jsonReply(w, uas.DetectAnomaly(uavID))
	})

	r.Get("/api/uas/remote-id/{uav_id}", func(w http.ResponseWriter, rq *http.Request) {
		uavID := chi.URLParam(rq, "uav_id")
		rid, err := uas.RemoteIDBroadcast(uavID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, rid)
	})

	// ── C2 sessions (TS 23.256 §5.2.3 + §5.5) ────────────────────
	r.Post("/api/uas/c2/establish", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			UAVID        string `json:"uav_id"`
			ControllerID string `json:"controller_id"`
			QoS5QI       int    `json:"qos_5qi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.UAVID == "" || d.ControllerID == "" {
			jsonError(w, "uav_id and controller_id required", http.StatusBadRequest)
			return
		}
		res, err := uas.EstablishC2(d.UAVID, d.ControllerID, d.QoS5QI)
		if err != nil {
			// 409 is the right shape for "already has active session"
			// per TS 23.256 §5.5 (one C2 session per UAV).
			jsonError(w, err.Error(), http.StatusConflict)
			return
		}
		// Re-shape so callers can read either {id} or {c2_id}.
		if sid, ok := res["c2_session_id"]; ok {
			res["id"] = sid
			res["c2_id"] = sid
		}
		jsonReplyStatus(w, http.StatusCreated, res)
	})

	r.Get("/api/uas/c2/status/{c2_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "c2_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid c2_id", http.StatusBadRequest)
			return
		}
		s, err := uas.GetC2Status(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s == nil {
			jsonError(w, "c2 session not found", http.StatusNotFound)
			return
		}
		jsonReply(w, s)
	})

	r.Delete("/api/uas/c2/{c2_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "c2_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid c2_id", http.StatusBadRequest)
			return
		}
		if err := uas.TerminateC2(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id, "status": "terminated"})
	})

	r.Post("/api/uas/c2/{c2_id}/failover", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "c2_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid c2_id", http.StatusBadRequest)
			return
		}
		res, err := uas.FailoverC2(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, res)
	})
}
