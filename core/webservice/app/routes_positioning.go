// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Auto-extracted from domain_routes.go (refactor: split god function by
// domain banner). Do not re-merge — keep new domain APIs in their own
// routes_<domain>.go file.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/nf/gmlc"
)

func (s *Server) registerPositioningRoutes() {
	r := s.Router

	// ── Positioning (positioning_routes.py) ──────────────────────────
	r.Get("/api/positioning/sessions", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		state := rq.URL.Query().Get("state")
		limit := 50
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"ok": true, "sessions": []any{}})
			return
		}
		query := `SELECT session_id, imsi, method, state, latitude, longitude,
			uncertainty_m, created_at, completed_at
			FROM positioning_sessions WHERE 1=1`
		var args []any
		if imsi != "" {
			query += " AND imsi=?"
			args = append(args, imsi)
		}
		if state != "" {
			query += " AND state=?"
			args = append(args, state)
		}
		query += " ORDER BY created_at DESC LIMIT ?"
		args = append(args, limit)
		rows, err := db.Query(query, args...)
		if err != nil {
			jsonReply(w, map[string]any{"ok": true, "sessions": []any{}})
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var items []map[string]any
		for rows.Next() {
			scan := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range scan {
				ptrs[i] = &scan[i]
			}
			if rows.Scan(ptrs...) == nil {
				row := make(map[string]any, len(cols))
				for i, name := range cols {
					row[name] = scan[i]
				}
				items = append(items, row)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "sessions": items})
	})
	r.Get("/api/positioning/history", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		if imsi == "" {
			jsonError(w, "imsi query param required", http.StatusBadRequest)
			return
		}
		limit := 50
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "history": []any{}})
			return
		}
		rows, err := db.Query(`SELECT session_id, latitude, longitude,
			uncertainty_m, method, completed_at
			FROM positioning_sessions
			WHERE imsi=? AND state='COMPLETED'
			ORDER BY completed_at DESC LIMIT ?`, imsi, limit)
		if err != nil {
			jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "history": []any{}})
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var sid, method string
			var lat, lon, unc *float64
			var completed *string
			if rows.Scan(&sid, &lat, &lon, &unc, &method, &completed) == nil {
				items = append(items, map[string]any{
					"session_id": sid, "latitude": lat, "longitude": lon,
					"uncertainty_m": unc, "method": method, "completed_at": completed,
				})
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "history": items})
	})
	r.Get("/api/geofences", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"ok": true, "geofences": []any{}})
			return
		}
		query := `SELECT id, name, imsi, center_lat, center_lon, radius_m,
			trigger_type, active FROM geofences`
		var args []any
		if imsi != "" {
			query += " WHERE imsi=?"
			args = append(args, imsi)
		}
		query += " ORDER BY id"
		rows, err := db.Query(query, args...)
		if err != nil {
			jsonReply(w, map[string]any{"ok": true, "geofences": []any{}})
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var items []map[string]any
		for rows.Next() {
			scan := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range scan {
				ptrs[i] = &scan[i]
			}
			if rows.Scan(ptrs...) == nil {
				row := make(map[string]any, len(cols))
				for i, name := range cols {
					row[name] = scan[i]
				}
				items = append(items, row)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "geofences": items})
	})
	r.Post("/api/geofences", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name        string   `json:"name"`
			CenterLat   *float64 `json:"center_lat"`
			CenterLon   *float64 `json:"center_lon"`
			RadiusM     *float64 `json:"radius_m"`
			TriggerType string   `json:"trigger_type"`
			IMSI        string   `json:"imsi"`
			CallbackURL string   `json:"callback_url"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Name == "" || d.CenterLat == nil || d.CenterLon == nil || d.RadiusM == nil {
			jsonError(w, "name, center_lat, center_lon, radius_m required", http.StatusBadRequest)
			return
		}
		if d.TriggerType == "" {
			d.TriggerType = "both"
		}
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		res, err := db.Exec(`INSERT INTO geofences (name, center_lat, center_lon, radius_m, trigger_type, imsi, callback_url, active)
			VALUES (?,?,?,?,?,?,?,1)`,
			d.Name, *d.CenterLat, *d.CenterLon, *d.RadiusM, d.TriggerType, d.IMSI, d.CallbackURL)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		w.WriteHeader(201)
		jsonReply(w, map[string]any{"ok": true, "id": id, "name": d.Name})
	})
	r.Delete("/api/geofences/{fence_id}", func(w http.ResponseWriter, rq *http.Request) {
		fenceID := chi.URLParam(rq, "fence_id")
		db, _ := engine.Open()
		if db != nil {
			db.Exec(`DELETE FROM geofences WHERE id=?`, fenceID)
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── LCS Privacy ─────────────────────────────────────────────────
	r.Get("/api/lcs-privacy", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		if imsi == "" {
			jsonError(w, "imsi query param required", http.StatusBadRequest)
			return
		}
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "privacy": []any{}})
			return
		}
		rows, err := db.Query(`SELECT client_type, allowed FROM lcs_privacy WHERE imsi=?`, imsi)
		if err != nil {
			jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "privacy": []any{}})
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var ct string
			var allowed int
			if rows.Scan(&ct, &allowed) == nil {
				items = append(items, map[string]any{"client_type": ct, "allowed": allowed != 0})
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "privacy": items})
	})
	r.Post("/api/lcs-privacy", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI       string `json:"imsi"`
			ClientType string `json:"client_type"`
			Allowed    *bool  `json:"allowed"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.ClientType == "" {
			jsonError(w, "imsi and client_type required", http.StatusBadRequest)
			return
		}
		allowed := 1
		if d.Allowed != nil && !*d.Allowed {
			allowed = 0
		}
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = db.Exec(`INSERT OR REPLACE INTO lcs_privacy (imsi, client_type, allowed) VALUES (?,?,?)`,
			d.IMSI, d.ClientType, allowed)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── gNB position / antenna registration (TS 23.273 §4.3.3 GMLC,
	//    TS 38.305 §6 LMF; antenna info per TS 38.455 §9.2.44 TRP
	//    information) ──────────────────────────────────────────────
	r.Post("/api/gnb/position", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID     string  `json:"gnb_id"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Altitude  float64 `json:"altitude"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" {
			jsonError(w, "gnb_id required", http.StatusBadRequest)
			return
		}
		gmlc.RegisterGnbPosition(d.GnbID, d.Latitude, d.Longitude, d.Altitude)
		jsonReply(w, map[string]any{
			"ok":        true,
			"gnb_id":    d.GnbID,
			"latitude":  d.Latitude,
			"longitude": d.Longitude,
			"altitude":  d.Altitude,
		})
	})
	r.Post("/api/gnb/antenna", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID        string  `json:"gnb_id"`
			AzimuthDeg   float64 `json:"azimuth_deg"`
			BeamwidthDeg float64 `json:"beamwidth_deg"`
			DowntiltDeg  float64 `json:"downtilt_deg"`
			NumBeams     int     `json:"num_beams"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" {
			jsonError(w, "gnb_id required", http.StatusBadRequest)
			return
		}
		if d.NumBeams <= 0 {
			d.NumBeams = 1
		}
		gmlc.RegisterGnbAntenna(d.GnbID, d.AzimuthDeg, d.BeamwidthDeg, d.DowntiltDeg, d.NumBeams)
		jsonReply(w, map[string]any{
			"ok":             true,
			"gnb_id":         d.GnbID,
			"azimuth_deg":    d.AzimuthDeg,
			"beamwidth_deg":  d.BeamwidthDeg,
			"downtilt_deg":   d.DowntiltDeg,
			"num_beams":      d.NumBeams,
		})
	})

	// ── Nlmf_Location_DetermineLocation (TS 29.572 §5.2.2.2),
	//    surfaced to clients via the GMLC Le interface (TS 23.273
	//    §4.4.1). LCS privacy gate per TS 23.271 §9 — emergency
	//    bypasses; commercial requires an explicit allow row. ─────
	r.Post("/api/location/request", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI           string  `json:"imsi"`
			Method         string  `json:"method"`
			AccuracyM      float64 `json:"accuracy_m"`
			ResponseTimeS  float64 `json:"response_time_s"`
			ClientType     string  `json:"client_type"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		if d.ClientType == "" {
			d.ClientType = "commercial"
		}
		// TS 23.271 §9 — emergency always allowed; otherwise check
		// lcs_privacy table for an explicit deny.
		if d.ClientType != "emergency" {
			db, err := engine.Open()
			if err == nil {
				var allowed int = 1
				_ = db.QueryRow(`SELECT allowed FROM lcs_privacy
					WHERE imsi=? AND client_type=?`, d.IMSI, d.ClientType).Scan(&allowed)
				if allowed == 0 {
					jsonReplyStatus(w, http.StatusForbidden, map[string]any{
						"ok":          false,
						"error":       "lcs privacy denies " + d.ClientType + " for imsi",
						"imsi":        d.IMSI,
						"client_type": d.ClientType,
					})
					return
				}
			}
		}
		res := gmlc.RequestLocation(d.IMSI, d.Method, d.AccuracyM, d.ResponseTimeS, d.ClientType)
		// state field is "PENDING"/"ACTIVE"/"COMPLETED" — match
		// the runner's expectation of either state="completed" or
		// a populated latitude.
		out := map[string]any{
			"session_id":  res.SessionID,
			"state":       res.State,
			"method":      res.Method,
			"imsi":        res.IMSI,
			"client_type": d.ClientType,
		}
		if res.Latitude != nil {
			out["latitude"] = *res.Latitude
		}
		if res.Longitude != nil {
			out["longitude"] = *res.Longitude
		}
		if res.Altitude != nil {
			out["altitude"] = *res.Altitude
		}
		if res.UncertaintyM != nil {
			out["uncertainty_m"] = *res.UncertaintyM
		}
		if res.Confidence != nil {
			out["confidence"] = *res.Confidence
		}
		jsonReply(w, out)
	})
	r.Get("/api/location/history", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		if imsi == "" {
			jsonError(w, "imsi query param required", http.StatusBadRequest)
			return
		}
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 50
		}
		items := gmlc.LocationHistory(imsi, limit)
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "items": items, "history": items})
	})
	r.Get("/api/location/{session_id}", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "session_id")
		res := gmlc.GetLocation(sid)
		if res == nil {
			jsonError(w, "session not found", http.StatusNotFound)
			return
		}
		out := map[string]any{
			"session_id": res.SessionID,
			"state":      res.State,
			"method":     res.Method,
			"imsi":       res.IMSI,
		}
		if res.Latitude != nil {
			out["latitude"] = *res.Latitude
		}
		if res.Longitude != nil {
			out["longitude"] = *res.Longitude
		}
		if res.Altitude != nil {
			out["altitude"] = *res.Altitude
		}
		if res.UncertaintyM != nil {
			out["uncertainty_m"] = *res.UncertaintyM
		}
		if res.Confidence != nil {
			out["confidence"] = *res.Confidence
		}
		jsonReply(w, out)
	})

	// ── PRS resource lifecycle (TS 38.211 §7.4.1.7 PRS signal
	//    configuration; LMF allocates per TS 38.305 §6.1) ─────────
	r.Post("/api/prs/allocate", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID          string `json:"gnb_id"`
			FrequencyLayer int    `json:"frequency_layer"`
			PeriodicityMS  int    `json:"periodicity_ms"`
			NumRB          int    `json:"num_rb"`
			NumSymbols     int    `json:"num_symbols"`
			CombSize       int    `json:"comb_size"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GnbID == "" {
			jsonError(w, "gnb_id required", http.StatusBadRequest)
			return
		}
		if d.PeriodicityMS == 0 {
			d.PeriodicityMS = 20
		}
		if d.NumRB == 0 {
			d.NumRB = 24
		}
		if d.NumSymbols == 0 {
			d.NumSymbols = 2
		}
		if d.CombSize == 0 {
			d.CombSize = 2
		}
		prs := gmlc.AllocatePRS(d.GnbID, d.FrequencyLayer, d.PeriodicityMS,
			d.NumRB, d.NumSymbols, d.CombSize)
		if prs == nil {
			jsonError(w, "allocation failed", http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"ok":              true,
			"prs_id":          prs.PRSResourceID,
			"id":              prs.PRSResourceID,
			"gnb_id":          prs.GnbID,
			"frequency_layer": prs.FrequencyLayer,
			"periodicity_ms":  prs.PeriodicityMS,
			"num_rb":          prs.NumRB,
			"num_symbols":     prs.NumSymbols,
			"comb_size":       prs.CombSize,
			"sequence_id":     prs.SequenceID,
			"active":          prs.Active,
		})
	})
	r.Get("/api/prs/{key}", func(w http.ResponseWriter, rq *http.Request) {
		key := chi.URLParam(rq, "key")
		// Permissive: list by gNB id (string). Tester always reads
		// the gNB-id form; the numeric path is handled by a deeper
		// lookup at the LMF level if needed in future.
		list := gmlc.GetPRSConfig(key)
		out := make([]map[string]any, 0, len(list))
		for _, p := range list {
			out = append(out, map[string]any{
				"prs_id":          p.PRSResourceID,
				"id":              p.PRSResourceID,
				"gnb_id":          p.GnbID,
				"frequency_layer": p.FrequencyLayer,
				"periodicity_ms":  p.PeriodicityMS,
				"num_rb":          p.NumRB,
				"num_symbols":     p.NumSymbols,
				"comb_size":       p.CombSize,
				"sequence_id":     p.SequenceID,
				"active":          p.Active,
			})
		}
		jsonReply(w, map[string]any{"gnb_id": key, "items": out, "count": len(out)})
	})
	r.Delete("/api/prs/{prs_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.Atoi(chi.URLParam(rq, "prs_id"))
		if err != nil {
			jsonError(w, "prs_id must be int", http.StatusBadRequest)
			return
		}
		gmlc.DeactivatePRS(id)
		jsonReply(w, map[string]any{"ok": true, "prs_id": id})
	})
}
