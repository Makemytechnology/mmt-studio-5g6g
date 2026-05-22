// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_ntn.go — REST surface for the operator-side
// Non-Terrestrial Network (NTN) state.
//
// Wires access/ntn/ to /api/ntn/* per the spec anchors that
// access/ntn/ntn.go and access/ntn/phase2.go cite in their headers:
//
//   - TS 22.261 §6.3.2.3   Service requirements for satellite access
//   - TS 23.501 §5.4.10    NR satellite access RAT-type gating
//   - TS 23.501 §5.4.11    Integrating NR satellite access into 5GS
//   - TS 23.501 §5.4.11.4  UE location verification
//   - TS 23.501 §5.4.11.7  Tracking Area handling for NR satellite
//                          access (geographic TAI mapping)
//   - TS 23.501 §5.4.11.9  N2 / connection management for
//                          regenerative satellite payload
//   - TS 23.501 §5.4.13    Discontinuous network coverage
//                          (LEO pass gaps, DL buffering)
//   - TS 23.501 §5.4.14    UE-Satellite-UE communication (ISL)
//   - TS 23.501 §5.43      5G Satellite Backhaul (Phase 2)
//   - TS 38.300 §16.14     NR support for non-terrestrial networks
//   - TS 38.821 §4.1       Transparent / regenerative payload
//   - TS 38.821 §5.1 / §5.2 NG-RAN reference scenarios
//   - TS 38.821 §6.2.5     Feeder link switch
//   - TS 38.821 §6.3       UL timing advance / propagation delay
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/access/ntn"
)

func (s *Server) registerNTNRoutes() {
	r := s.Router

	// ── Constellation (satellites + ground stations) ─────────────
	r.Get("/api/ntn/constellation", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"satellites":      ntn.DefaultConstellation.GetAllSatellites(),
			"ground_stations": ntn.DefaultConstellation.GetAllGroundStations(),
		})
	})

	r.Get("/api/ntn/satellites", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ntn.DefaultConstellation.GetAllSatellites())
	})

	addSat := func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SatID          string  `json:"sat_id"`
			Name           string  `json:"name"`
			OrbitType      string  `json:"orbit_type"`
			AltitudeKM     float64 `json:"altitude_km"`
			InclinationDeg float64 `json:"inclination_deg"`
			LongitudeDeg   float64 `json:"longitude_deg"`
			BeamCount      int     `json:"beam_count"`
			BeamDiameterKM float64 `json:"beam_diameter_km"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.SatID == "" {
			jsonError(w, "sat_id required", http.StatusBadRequest)
			return
		}
		// TS 38.821 §4.1 / §5.1 — orbit_type ∈ {LEO, MEO, GEO, HAPS}.
		switch d.OrbitType {
		case "LEO", "MEO", "GEO", "HAPS":
		default:
			jsonError(w, "orbit_type must be LEO|MEO|GEO|HAPS",
				http.StatusBadRequest)
			return
		}
		sat := ntn.NewSatelliteConfig(d.SatID, d.Name, d.OrbitType,
			d.AltitudeKM, d.InclinationDeg, d.LongitudeDeg,
			d.BeamCount, d.BeamDiameterKM)
		ntn.DefaultConstellation.AddSatellite(sat)
		jsonReplyStatus(w, http.StatusCreated, sat)
	}
	// Alias paths — `/satellite` (singular) is what the existing
	// tester suite uses; keep `/satellites` for new clients.
	r.Post("/api/ntn/satellite", addSat)
	r.Post("/api/ntn/satellites", addSat)

	r.Get("/api/ntn/satellites/{sat_id}", func(w http.ResponseWriter, rq *http.Request) {
		sat := ntn.DefaultConstellation.GetSatellite(chi.URLParam(rq, "sat_id"))
		if sat == nil {
			jsonError(w, "satellite not found", http.StatusNotFound)
			return
		}
		jsonReply(w, sat)
	})

	r.Delete("/api/ntn/satellites/{sat_id}", func(w http.ResponseWriter, rq *http.Request) {
		ntn.DefaultConstellation.RemoveSatellite(chi.URLParam(rq, "sat_id"))
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Get("/api/ntn/ground-stations", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ntn.DefaultConstellation.GetAllGroundStations())
	})

	addGS := func(w http.ResponseWriter, rq *http.Request) {
		var d ntn.GroundStationConfig
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.GSID == "" {
			jsonError(w, "gs_id required", http.StatusBadRequest)
			return
		}
		ntn.DefaultConstellation.AddGroundStation(&d)
		jsonReplyStatus(w, http.StatusCreated, d)
	}
	// Alias — `/ground-station` (singular) is what the tester uses.
	r.Post("/api/ntn/ground-station", addGS)
	r.Post("/api/ntn/ground-stations", addGS)

	// `/api/ntn/load-defaults` loads constellation + TAI defaults so
	// the GUI lights up the panels without manual provisioning.
	r.Post("/api/ntn/load-defaults", func(w http.ResponseWriter, _ *http.Request) {
		ntn.DefaultConstellation.LoadDefaults()
		ntn.DefaultTAIMgr.LoadDefaults("001", "01")
		jsonReply(w, map[string]any{
			"ok":              true,
			"satellites":      len(ntn.DefaultConstellation.GetAllSatellites()),
			"ground_stations": len(ntn.DefaultConstellation.GetAllGroundStations()),
			"tais":            len(ntn.DefaultTAIMgr.GetTAIList()),
		})
	})

	// ── Coverage (TS 23.501 §5.4.13 + §5.4.11.4) ─────────────────
	r.Get("/api/ntn/coverage", func(w http.ResponseWriter, rq *http.Request) {
		lat, _ := strconv.ParseFloat(rq.URL.Query().Get("lat"), 64)
		lon, _ := strconv.ParseFloat(rq.URL.Query().Get("lon"), 64)
		minElev := 10.0
		if me := rq.URL.Query().Get("min_elev"); me != "" {
			if v, err := strconv.ParseFloat(me, 64); err == nil {
				minElev = v
			}
		}
		jsonReply(w, ntn.DefaultCoverageMgr.CheckCoverage(
			ntn.DefaultConstellation, lat, lon, minElev))
	})

	r.Get("/api/ntn/buffer-status", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		jsonReply(w, ntn.DefaultCoverageMgr.GetBufferStatus(imsi))
	})

	r.Post("/api/ntn/buffer", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI string      `json:"imsi"`
			Data interface{} `json:"data"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		ntn.DefaultCoverageMgr.BufferDLPacket(d.IMSI, d.Data)
		jsonReplyStatus(w, http.StatusCreated, map[string]any{"ok": true})
	})

	r.Post("/api/ntn/buffer/{imsi}/flush", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		flushed := ntn.DefaultCoverageMgr.FlushDLBuffer(imsi)
		jsonReply(w, map[string]any{"imsi": imsi, "flushed": len(flushed),
			"entries": flushed})
	})

	// ── Feeder links (TS 38.821 §6.2.5) ──────────────────────────
	// Operator GUI panel expects {active_links, switch_history} in
	// one shot; keep the underlying helpers granular.
	r.Get("/api/ntn/feeder-links", func(w http.ResponseWriter, rq *http.Request) {
		limit := 50
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		active := ntn.DefaultFeederLinkMgr.GetAllActiveLinks()
		if active == nil {
			active = map[string]map[string]interface{}{}
		}
		history := ntn.DefaultFeederLinkMgr.GetSwitchHistory(limit)
		if history == nil {
			history = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{
			"active_links":   active,
			"switch_history": history,
		})
	})

	r.Post("/api/ntn/feeder-links", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SatID string `json:"sat_id"`
			GSID  string `json:"gs_id"`
			GnbIP string `json:"gnb_ip"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.SatID == "" || d.GSID == "" {
			jsonError(w, "sat_id and gs_id required", http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			ntn.DefaultFeederLinkMgr.RegisterFeederLink(d.SatID, d.GSID, d.GnbIP))
	})

	r.Post("/api/ntn/feeder-links/switch", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SatID    string `json:"sat_id"`
			NewGSID  string `json:"new_gs_id"`
			NewGnbIP string `json:"new_gnb_ip"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.SatID == "" || d.NewGSID == "" {
			jsonError(w, "sat_id and new_gs_id required",
				http.StatusBadRequest)
			return
		}
		jsonReply(w, ntn.DefaultFeederLinkMgr.InitiateSwitch(
			d.SatID, d.NewGSID, d.NewGnbIP))
	})

	r.Get("/api/ntn/feeder-links/history", func(w http.ResponseWriter, rq *http.Request) {
		limit := 50
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		jsonReply(w, ntn.DefaultFeederLinkMgr.GetSwitchHistory(limit))
	})

	// ── Geographic TAI (TS 23.501 §5.4.11.7) ─────────────────────
	r.Get("/api/ntn/tais", func(w http.ResponseWriter, _ *http.Request) {
		list := ntn.DefaultTAIMgr.GetTAIList()
		jsonReply(w, map[string]any{
			"tais":  list,
			"count": len(list),
		})
	})

	r.Post("/api/ntn/tais/load-defaults", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			MCC string `json:"mcc"`
			MNC string `json:"mnc"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.MCC == "" {
			d.MCC = "001"
		}
		if d.MNC == "" {
			d.MNC = "01"
		}
		ntn.DefaultTAIMgr.LoadDefaults(d.MCC, d.MNC)
		jsonReply(w, map[string]any{"ok": true,
			"tais": len(ntn.DefaultTAIMgr.GetTAIList())})
	})

	// `/tais/lookup` and `/tai-lookup` both wrap the lookup result
	// in `{tai: ...}` so the GUI can branch on tai!=null.
	taiLookup := func(w http.ResponseWriter, rq *http.Request) {
		lat, _ := strconv.ParseFloat(rq.URL.Query().Get("lat"), 64)
		lon, _ := strconv.ParseFloat(rq.URL.Query().Get("lon"), 64)
		t := ntn.DefaultTAIMgr.GetTAIForLocation(lat, lon)
		jsonReply(w, map[string]any{"tai": t,
			"location": map[string]float64{"lat": lat, "lon": lon}})
	}
	r.Get("/api/ntn/tais/lookup", taiLookup)
	r.Get("/api/ntn/tai-lookup", taiLookup)

	// ── Propagation delay + NAS timer guard (TS 38.821 §6.3) ─────
	// `/api/ntn/timing` is the GUI/tester surface — wraps the raw
	// propagation delay map in {delay, adjusted_timers}. The lower-
	// level POST `/api/ntn/propagation` returns the raw delay map
	// for clients that already integrate the timer logic.
	r.Get("/api/ntn/timing", func(w http.ResponseWriter, rq *http.Request) {
		satID := rq.URL.Query().Get("sat_id")
		sat := ntn.DefaultConstellation.GetSatellite(satID)
		if sat == nil {
			jsonError(w, "satellite not found", http.StatusNotFound)
			return
		}
		var lat, lon *float64
		if v := rq.URL.Query().Get("lat"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				lat = &f
			}
		}
		if v := rq.URL.Query().Get("lon"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				lon = &f
			}
		}
		jsonReply(w, map[string]any{
			"sat_id":          satID,
			"delay":           ntn.ComputePropagationDelay(sat, lat, lon),
			"adjusted_timers": ntn.GetAdjustedNASTimers(sat),
		})
	})

	r.Post("/api/ntn/propagation", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SatID string  `json:"sat_id"`
			UELat float64 `json:"ue_lat"`
			UELon float64 `json:"ue_lon"`
			HasUE bool    `json:"has_ue_loc"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		sat := ntn.DefaultConstellation.GetSatellite(d.SatID)
		if sat == nil {
			jsonError(w, "satellite not found", http.StatusNotFound)
			return
		}
		var lat, lon *float64
		if d.HasUE {
			lat, lon = &d.UELat, &d.UELon
		}
		jsonReply(w, ntn.ComputePropagationDelay(sat, lat, lon))
	})

	r.Get("/api/ntn/satellites/{sat_id}/nas-timers", func(w http.ResponseWriter, rq *http.Request) {
		sat := ntn.DefaultConstellation.GetSatellite(chi.URLParam(rq, "sat_id"))
		if sat == nil {
			jsonError(w, "satellite not found", http.StatusNotFound)
			return
		}
		jsonReply(w, ntn.GetAdjustedNASTimers(sat))
	})

	// ─────────────────────────────────────────────────────────────
	// NTN Phase 2 — DB-backed regenerative payload, store-and-
	// forward queue, ISL pair table, and aggregate capabilities
	// (TS 23.501 §5.4.11.9, §5.4.13, §5.4.14, §5.43;
	//  TS 38.821 §5.2 regenerative payload).
	// ─────────────────────────────────────────────────────────────

	// Regenerative payload — `sat_id` may come from the body (POST)
	// or the path (GET/DELETE). Body form is what the existing
	// tester uses; path form is the resource-style alias.
	r.Post("/api/ntn/phase2/regenerative", func(w http.ResponseWriter, rq *http.Request) {
		var cfg map[string]interface{}
		if !decodeJSON(w, rq, &cfg) {
			return
		}
		satID, _ := cfg["sat_id"].(string)
		if satID == "" {
			jsonError(w, "sat_id required", http.StatusBadRequest)
			return
		}
		out, err := ntn.RegenerativePayload(satID, cfg)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, out)
	})

	r.Post("/api/ntn/phase2/regenerative/{sat_id}", func(w http.ResponseWriter, rq *http.Request) {
		var cfg map[string]interface{}
		if !decodeJSON(w, rq, &cfg) {
			return
		}
		out, err := ntn.RegenerativePayload(chi.URLParam(rq, "sat_id"), cfg)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, out)
	})

	r.Get("/api/ntn/phase2/regenerative", func(w http.ResponseWriter, _ *http.Request) {
		list, err := ntn.ListRegenerativeConfigs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/ntn/phase2/regenerative/{sat_id}", func(w http.ResponseWriter, rq *http.Request) {
		out, err := ntn.GetRegenerativeConfig(chi.URLParam(rq, "sat_id"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			jsonError(w, "no regenerative config for satellite",
				http.StatusNotFound)
			return
		}
		jsonReply(w, out)
	})

	r.Delete("/api/ntn/phase2/regenerative/{sat_id}", func(w http.ResponseWriter, rq *http.Request) {
		if err := ntn.DeleteRegenerativeConfig(chi.URLParam(rq, "sat_id")); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// Aggregate satellite capabilities (regenerative + ISL view).
	// Flatten `onboard_nfs` to top-level so the GUI / tester don't
	// have to traverse `regenerative.onboard_nfs`.
	r.Get("/api/ntn/phase2/capabilities/{sat_id}", func(w http.ResponseWriter, rq *http.Request) {
		satID := chi.URLParam(rq, "sat_id")
		caps := ntn.GetSatCapabilities(satID)
		if regen, ok := caps["regenerative"].(map[string]interface{}); ok && regen != nil {
			if v, ok := regen["onboard_nfs"]; ok {
				caps["onboard_nfs"] = v
			}
			if v, ok := regen["processing_capacity"]; ok {
				caps["processing_capacity"] = v
			}
			if v, ok := regen["memory_mb"]; ok {
				caps["memory_mb"] = v
			}
		}
		jsonReply(w, caps)
	})

	// Store-and-forward queue.
	r.Post("/api/ntn/phase2/store-forward", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SatID    string `json:"sat_id"`
			Target   string `json:"target"`
			DataHex  string `json:"data_hex"`
			Priority int    `json:"priority"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		out, err := ntn.StoreAndForward(d.SatID, d.DataHex, d.Target)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, out)
	})

	r.Get("/api/ntn/phase2/store-forward", func(w http.ResponseWriter, rq *http.Request) {
		// If a sat_id is supplied filter to that satellite; else the
		// caller wants the whole queue across all satellites.
		satID := rq.URL.Query().Get("sat_id")
		var queue []map[string]interface{}
		var err error
		if satID != "" {
			queue, err = ntn.GetStoreForwardQueue(satID)
		} else {
			queue, err = ntn.ListStoreForwardQueued()
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if queue == nil {
			queue = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"queue": queue, "count": len(queue)})
	})

	r.Get("/api/ntn/phase2/store-forward/{sat_id}", func(w http.ResponseWriter, rq *http.Request) {
		out, err := ntn.GetStoreForwardQueue(chi.URLParam(rq, "sat_id"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"queue": out, "count": len(out)})
	})

	// ISL pair table — DB-backed; TS 23.501 §5.4.14.
	r.Get("/api/ntn/phase2/isl", func(w http.ResponseWriter, _ *http.Request) {
		list, err := ntn.ListISLLinks()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/ntn/phase2/isl", func(w http.ResponseWriter, rq *http.Request) {
		var cfg map[string]interface{}
		if !decodeJSON(w, rq, &cfg) {
			return
		}
		sat1, _ := cfg["sat1_id"].(string)
		sat2, _ := cfg["sat2_id"].(string)
		if sat1 == "" || sat2 == "" {
			jsonError(w, "sat1_id and sat2_id required",
				http.StatusBadRequest)
			return
		}
		out, err := ntn.InterSatLink(sat1, sat2, cfg)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, out)
	})

	r.Delete("/api/ntn/phase2/isl/{link_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "link_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid link_id", http.StatusBadRequest)
			return
		}
		if err := ntn.DeleteISLLink(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// Phase-1 aliases for the DB-backed surface — kept for the
	// older GUI panels that don't carry the `phase2/` prefix.
	r.Get("/api/ntn/regenerative", func(w http.ResponseWriter, _ *http.Request) {
		list, err := ntn.ListRegenerativeConfigs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})
	r.Get("/api/ntn/isl", func(w http.ResponseWriter, _ *http.Request) {
		list, err := ntn.ListISLLinks()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})
	r.Get("/api/ntn/store-forward/{sat_id}", func(w http.ResponseWriter, rq *http.Request) {
		out, err := ntn.GetStoreForwardQueue(chi.URLParam(rq, "sat_id"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []map[string]interface{}{}
		}
		jsonReply(w, out)
	})

	// ─────────────────────────────────────────────────────────────
	// NTN Phase 2 — operator-side in-memory state for backhaul
	// (TS 23.501 §5.43), per-IMSI SAF byte counters, and a
	// pre-DB-backed ISL adjacency mesh. These are distinct from
	// the DB-backed surfaces above and are addressed under
	// `/api/ntn/phase2/{backhaul,saf,isl-mesh}` to avoid clash.
	// ─────────────────────────────────────────────────────────────

	// Backhaul (TS 23.501 §5.43)
	r.Get("/api/ntn/phase2/backhaul", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ntn.DefaultBackhaulMgr.All())
	})

	r.Post("/api/ntn/phase2/backhaul", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			GnbID        string  `json:"gnb_id"`
			SatelliteID  string  `json:"satellite_id"`
			CapacityMbps float64 `json:"capacity_mbps"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := ntn.DefaultBackhaulMgr.Provision(d.GnbID, d.SatelliteID, d.CapacityMbps); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, ntn.DefaultBackhaulMgr.Get(d.GnbID))
	})

	r.Delete("/api/ntn/phase2/backhaul/{gnb_id}", func(w http.ResponseWriter, rq *http.Request) {
		gnbID := chi.URLParam(rq, "gnb_id")
		if !ntn.DefaultBackhaulMgr.Deprovision(gnbID) {
			jsonError(w, "gnb backhaul not provisioned", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Post("/api/ntn/phase2/backhaul/{gnb_id}/usage", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Mbps float64 `json:"mbps"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if !ntn.DefaultBackhaulMgr.UpdateUsage(chi.URLParam(rq, "gnb_id"), d.Mbps) {
			jsonError(w, "gnb backhaul not provisioned", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Get("/api/ntn/phase2/backhaul/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ntn.DefaultBackhaulMgr.Stats())
	})

	// Phase-2 SAF (per-IMSI buffered bytes; TODO Store-and-Forward)
	r.Post("/api/ntn/phase2/saf/enqueue", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI  string `json:"imsi"`
			Bytes int64  `json:"bytes"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := ntn.DefaultSAFMgr.Enqueue(d.IMSI, d.Bytes); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			ntn.DefaultSAFMgr.QueueFor(d.IMSI))
	})

	r.Post("/api/ntn/phase2/saf/drain", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI  string `json:"imsi"`
			Bytes int64  `json:"bytes"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := ntn.DefaultSAFMgr.Drain(d.IMSI, d.Bytes); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, ntn.DefaultSAFMgr.QueueFor(d.IMSI))
	})

	r.Get("/api/ntn/phase2/saf/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		q := ntn.DefaultSAFMgr.QueueFor(chi.URLParam(rq, "imsi"))
		if q == nil {
			jsonError(w, "no saf queue for imsi", http.StatusNotFound)
			return
		}
		jsonReply(w, q)
	})

	r.Get("/api/ntn/phase2/saf", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ntn.DefaultSAFMgr.AllQueues())
	})

	r.Get("/api/ntn/phase2/saf/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ntn.DefaultSAFMgr.Stats())
	})

	// Phase-2 ISL mesh (in-memory operator-visible adjacency;
	// distinct from the DB-backed pair table at /phase2/isl).
	r.Get("/api/ntn/phase2/isl-mesh", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ntn.DefaultISLMgr.All())
	})

	r.Post("/api/ntn/phase2/isl-mesh", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			From   string  `json:"from"`
			To     string  `json:"to"`
			BWMbps float64 `json:"bw_mbps"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := ntn.DefaultISLMgr.AddLink(d.From, d.To, d.BWMbps); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{"ok": true})
	})

	r.Delete("/api/ntn/phase2/isl-mesh", func(w http.ResponseWriter, rq *http.Request) {
		from := rq.URL.Query().Get("from")
		to := rq.URL.Query().Get("to")
		if from == "" || to == "" {
			jsonError(w, "from and to required", http.StatusBadRequest)
			return
		}
		if !ntn.DefaultISLMgr.RemoveLink(from, to) {
			jsonError(w, "isl link not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Get("/api/ntn/phase2/isl-mesh/{from}/neighbours", func(w http.ResponseWriter, rq *http.Request) {
		from := chi.URLParam(rq, "from")
		jsonReply(w, map[string]any{
			"sat":        from,
			"neighbours": ntn.DefaultISLMgr.Neighbours(from),
		})
	})

	r.Get("/api/ntn/phase2/isl-mesh/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ntn.DefaultISLMgr.Stats())
	})

	// Aggregate Phase-2 stats (DB + in-memory).
	r.Get("/api/ntn/phase2/stats", func(w http.ResponseWriter, _ *http.Request) {
		dbStats, _ := ntn.GetPhase2Stats()
		if dbStats == nil {
			dbStats = map[string]interface{}{}
		}
		dbStats["backhaul"] = ntn.DefaultBackhaulMgr.Stats()
		dbStats["saf"] = ntn.DefaultSAFMgr.Stats()
		dbStats["isl_mesh"] = ntn.DefaultISLMgr.Stats()
		jsonReply(w, dbStats)
	})

	// Aggregate Phase-1 stats (alias).
	r.Get("/api/ntn/stats", func(w http.ResponseWriter, _ *http.Request) {
		st, err := ntn.GetPhase2Stats()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		st["satellites"] = len(ntn.DefaultConstellation.GetAllSatellites())
		st["ground_stations"] = len(ntn.DefaultConstellation.GetAllGroundStations())
		st["tais"] = len(ntn.DefaultTAIMgr.GetTAIList())
		st["active_feeder_links"] = len(ntn.DefaultFeederLinkMgr.GetAllActiveLinks())
		jsonReply(w, st)
	})
}
