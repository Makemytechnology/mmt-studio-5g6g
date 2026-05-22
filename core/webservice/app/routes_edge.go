// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_edge.go — REST surface for Edge Computing (TS 23.501 §5.13
// + TS 23.548 / TS 23.558 EDGEAPP architecture).
//
// EAS (Edge Application Server) — TS 23.548 §6.2 discovery and §6.8
// DNAI mapping; TS 23.558 §8.4 EDGEAPP EES surface. Routes:
//
//   /api/eas/registry       — TS 23.558 §8.4.3 EAS registration CRUD
//   /api/eas/discover       — TS 23.548 §6.2.2 EAS discovery
//   /api/eas/dnai           — TS 23.548 §6.8 DNAI mapping CRUD
//   /api/eas/dns            — TS 23.548 §6.2.3.2.2 EASDF FQDN→EAS
//   /api/eas/discovery-log  — operator-side discovery audit
//
// All routes are operator-facing (no auth gate today — EAS does not
// carry the same legal sensitivity as LI). The wire-format for the
// EDGEAPP APIs (Eees_EASRegistration_*, Eees_EASDiscovery_*) lives in
// TS 29.558; the local product exposes the data shape over plain
// JSON to match the OAM panel and the tester. A future deployment can
// add a TS 29.558-conformant translator without touching the data
// path.

package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/mmt/mmt-studio-core/edge/eas"
	"github.com/mmt/mmt-studio-core/edge/tsn"
)

func (s *Server) registerEdgeRoutes() {
	r := s.Router

	// ── EAS registry CRUD (TS 23.558 §8.4.3 Eees_EASRegistration_*) ──
	r.Get("/api/eas/registry", func(w http.ResponseWriter, rq *http.Request) {
		list, err := eas.ListEAS()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []eas.EAS{}
		}
		jsonReply(w, list)
	})
	r.Get("/api/eas/registry/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		row, err := eas.GetEAS(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, row)
	})
	r.Post("/api/eas/registry", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			AppID            string   `json:"app_id"`
			Name             string   `json:"name"`
			EndpointURL      string   `json:"endpoint_url"`
			DNAI             string   `json:"dnai"`
			Latitude         *float64 `json:"latitude"`
			Longitude        *float64 `json:"longitude"`
			SupportedDNNs    string   `json:"supported_dnns"`
			SupportedSlices  string   `json:"supported_slices"`
			Capacity         int      `json:"capacity"`
			Status           string   `json:"status"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		var name, dnai, dnns, slices *string
		if b.Name != "" {
			name = &b.Name
		}
		if b.DNAI != "" {
			dnai = &b.DNAI
		}
		if b.SupportedDNNs != "" {
			dnns = &b.SupportedDNNs
		}
		if b.SupportedSlices != "" {
			slices = &b.SupportedSlices
		}
		if b.Capacity == 0 {
			b.Capacity = 100
		}
		id, err := eas.CreateEAS(b.AppID, b.EndpointURL, name, dnai,
			b.Latitude, b.Longitude, dnns, slices, b.Capacity, b.Status)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		row, _ := eas.GetEAS(id)
		jsonReply(w, row)
	})
	r.Put("/api/eas/registry/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		var fields map[string]any
		if err := json.NewDecoder(rq.Body).Decode(&fields); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := eas.UpdateEAS(id, fields); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		row, _ := eas.GetEAS(id)
		jsonReply(w, row)
	})
	r.Delete("/api/eas/registry/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := eas.DeleteEAS(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── EAS discovery (TS 23.548 §6.2.2 Distributed-Anchor model) ──
	r.Post("/api/eas/discover", func(w http.ResponseWriter, rq *http.Request) {
		var c eas.DiscoveryCriteria
		if err := json.NewDecoder(rq.Body).Decode(&c); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		results, err := eas.Discover(c)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if results == nil {
			results = []eas.EAS{}
		}
		selected := any(nil)
		if len(results) > 0 {
			selected = results[0]
		}
		jsonReply(w, map[string]any{
			"results":  results,
			"selected": selected,
			"count":    len(results),
		})
	})

	// ── DNAI map (TS 23.548 §6.8) ──────────────────────────────────
	r.Get("/api/eas/dnai", func(w http.ResponseWriter, rq *http.Request) {
		list, err := eas.ListDNAI()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []eas.DNAIMapping{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/eas/dnai", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			DNAI         string `json:"dnai"`
			Description  string `json:"description"`
			LocationHint string `json:"location_hint"`
			UPFInstance  string `json:"upf_instance"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		var desc, loc, upf *string
		if b.Description != "" {
			desc = &b.Description
		}
		if b.LocationHint != "" {
			loc = &b.LocationHint
		}
		if b.UPFInstance != "" {
			upf = &b.UPFInstance
		}
		id, err := eas.CreateDNAI(b.DNAI, desc, loc, upf)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"id": id, "dnai": b.DNAI, "ok": true})
	})
	r.Delete("/api/eas/dnai/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := eas.DeleteDNAI(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── EASDF FQDN→EAS resolution (TS 23.548 §6.2.3.2.2) ──────────
	r.Get("/api/eas/dns", func(w http.ResponseWriter, rq *http.Request) {
		list, err := eas.ListDNSEntries()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []eas.DNSEntry{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/eas/dns", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			FQDN  string `json:"fqdn"`
			EASID int64  `json:"eas_id"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		id, err := eas.RegisterDNSEntry(b.FQDN, b.EASID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"id": id, "fqdn": b.FQDN, "eas_id": b.EASID, "ok": true})
	})
	r.Delete("/api/eas/dns/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := eas.DeleteDNSEntry(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Post("/api/eas/dns/resolve", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			FQDN string `json:"fqdn"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		ans, err := eas.ResolveDNS(b.FQDN)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if ans == nil {
			jsonError(w, "fqdn not registered", http.StatusNotFound)
			return
		}
		jsonReply(w, ans)
	})

	// ── Discovery audit ────────────────────────────────────────────
	r.Get("/api/eas/discovery-log", func(w http.ResponseWriter, rq *http.Request) {
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100
		}
		entries, err := eas.ListDiscoveryLog(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if entries == nil {
			entries = []eas.DiscoveryLog{}
		}
		jsonReply(w, map[string]any{"entries": entries, "count": len(entries)})
	})

	// ── Status (drives panel headline) ─────────────────────────────
	r.Get("/api/eas/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, eas.Status())
	})

	// ISAC routes moved to routes_isac.go (TS 22.137 — sensing is
	// architecturally distinct from edge computing).
	// Ranging routes moved to routes_ranging.go (TS 23.586 — sidelink
	// positioning is architecturally distinct from edge computing).

	// ────────────────────────────────────────────────────────────────
	// TSN (TS 23.501 §5.27 + IEEE 802.1Q TSN bridge model)
	// ────────────────────────────────────────────────────────────────
	r.Get("/api/tsn/stats", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsn.Status())
	})
	r.Get("/api/tsn/bridges", func(w http.ResponseWriter, rq *http.Request) {
		list, err := tsn.ListBridges()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []tsn.Bridge{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/tsn/bridges", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			BridgeID string `json:"bridge_id"`
			Name     string `json:"name"`
			DSTTPort string `json:"dstt_port"`
			NWTTPort string `json:"nwtt_port"`
			VLANID   *int   `json:"vlan_id"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		id, err := tsn.CreateBridge(b.BridgeID, b.Name, b.DSTTPort, b.NWTTPort, b.VLANID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"id": id, "ok": true})
	})
	// {id} is permissive: numeric → row PK; non-numeric → bridge_id string.
	r.Get("/api/tsn/bridges/{id}", func(w http.ResponseWriter, rq *http.Request) {
		raw := chi.URLParam(rq, "id")
		var b *tsn.Bridge
		var err error
		if n, perr := strconv.ParseInt(raw, 10, 64); perr == nil {
			b, err = tsn.GetBridge(n)
		} else {
			b, err = tsn.GetBridgeByBridgeID(raw)
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if b == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, b)
	})
	r.Delete("/api/tsn/bridges/{id}", func(w http.ResponseWriter, rq *http.Request) {
		raw := chi.URLParam(rq, "id")
		var err error
		if n, perr := strconv.ParseInt(raw, 10, 64); perr == nil {
			err = tsn.DeleteBridge(n)
		} else {
			err = tsn.DeleteBridgeByBridgeID(raw)
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	// TS 23.501 §5.27.2 traffic-class → 5QI mapping. Operator policy.
	r.Post("/api/tsn/streams/map-5qi", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			TrafficClass int `json:"traffic_class"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"traffic_class": b.TrafficClass,
			"mapped_5qi":    tsn.Map5QI(b.TrafficClass),
		})
	})
	r.Get("/api/tsn/streams", func(w http.ResponseWriter, rq *http.Request) {
		bridgeID, _ := strconv.ParseInt(rq.URL.Query().Get("bridge_id"), 10, 64)
		list, err := tsn.ListStreams(bridgeID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []tsn.Stream{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/tsn/streams", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			BridgeID     int64    `json:"bridge_id"`
			BridgeIDStr  string   `json:"bridge_id_str"`
			StreamID     string   `json:"stream_id"`
			TrafficClass int      `json:"traffic_class"`
			Priority     int      `json:"priority"`
			MaxFrameSize int      `json:"max_frame_size"`
			IntervalUS   int      `json:"interval_us"`
			Mapped5QI    *int     `json:"mapped_5qi"`
			PDBMS        *float64 `json:"pdb_ms"`
		}
		// Allow `bridge_id` as either int (PK) or string (operator id).
		// Permissive decoding: read raw map, then dispatch by type.
		var raw map[string]any
		bodyBytes, _ := io.ReadAll(rq.Body)
		_ = json.Unmarshal(bodyBytes, &raw)
		_ = json.Unmarshal(bodyBytes, &b)
		if v, ok := raw["bridge_id"].(string); ok {
			brow, err := tsn.GetBridgeByBridgeID(v)
			if err != nil {
				jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if brow == nil {
				jsonError(w, "bridge_id not found", http.StatusBadRequest)
				return
			}
			b.BridgeID = brow.ID
		}
		// Auto-derive 5QI from traffic_class if not supplied.
		if b.Mapped5QI == nil {
			q := tsn.Map5QI(b.TrafficClass)
			b.Mapped5QI = &q
		}
		id, err := tsn.CreateStream(b.BridgeID, b.StreamID, b.TrafficClass, b.Priority,
			b.MaxFrameSize, b.IntervalUS, b.Mapped5QI, b.PDBMS)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"id":         id,
			"ok":         true,
			"mapped_5qi": *b.Mapped5QI,
			"stream_id":  b.StreamID,
		})
	})
	r.Delete("/api/tsn/streams/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := tsn.DeleteStream(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/tsn/clock-domains", func(w http.ResponseWriter, rq *http.Request) {
		list, err := tsn.ListClockDomains()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []tsn.ClockDomain{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/tsn/clock-domains", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			DomainID         string `json:"domain_id"`
			GMIdentity       string `json:"gm_identity"`
			SyncAccuracyNS   int    `json:"sync_accuracy_ns"`
			HoldoverCapS     int    `json:"holdover_cap_s"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		id, err := tsn.CreateClockDomain(b.DomainID, b.GMIdentity, b.SyncAccuracyNS, b.HoldoverCapS)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"id": id, "ok": true})
	})
	r.Delete("/api/tsn/clock-domains/{id}", func(w http.ResponseWriter, rq *http.Request) {
		raw := chi.URLParam(rq, "id")
		var err error
		if n, perr := strconv.ParseInt(raw, 10, 64); perr == nil {
			err = tsn.DeleteClockDomain(n)
		} else {
			err = tsn.DeleteClockDomainByDomainID(raw)
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/tsn/clock-domains/{id}/sync-status", func(w http.ResponseWriter, rq *http.Request) {
		raw := chi.URLParam(rq, "id")
		var c *tsn.ClockDomain
		var err error
		if n, perr := strconv.ParseInt(raw, 10, 64); perr == nil {
			c, err = tsn.GetClockDomain(n)
		} else {
			c, err = tsn.GetClockDomainByDomainID(raw)
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if c == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{
			"domain_id":     c.DomainID,
			"status":        c.Status,
			"synced":        c.Status == "synced",
			"last_sync_at":  c.LastSyncAt,
		})
	})
	r.Get("/api/tsn/gate-schedules", func(w http.ResponseWriter, rq *http.Request) {
		streamID, _ := strconv.ParseInt(rq.URL.Query().Get("stream_id"), 10, 64)
		list, err := tsn.ListGateSchedules(streamID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []tsn.GateSchedule{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/tsn/gate-schedules", func(w http.ResponseWriter, rq *http.Request) {
		// stream_id may be int (PK) or string (operator id). Decode
		// permissively then dispatch.
		bodyBytes, _ := io.ReadAll(rq.Body)
		var raw map[string]any
		_ = json.Unmarshal(bodyBytes, &raw)

		gateState, _ := raw["gate_state"].(string)
		startTimeNS := int64(jsonNum(raw["start_time_ns"]))
		durationNS := int64(jsonNum(raw["duration_ns"]))
		cycleTimeNS := int64(jsonNum(raw["cycle_time_ns"]))
		var streamPK int64
		switch v := raw["stream_id"].(type) {
		case float64:
			streamPK = int64(v)
		case string:
			s, err := tsn.GetStreamByStreamID(v)
			if err != nil {
				jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if s == nil {
				jsonError(w, "stream_id not found", http.StatusBadRequest)
				return
			}
			streamPK = s.ID
		default:
			jsonError(w, "stream_id required", http.StatusBadRequest)
			return
		}
		id, err := tsn.CreateGateSchedule(streamPK, gateState,
			startTimeNS, durationNS, cycleTimeNS)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"id": id, "ok": true, "stream_id": streamPK})
	})
	r.Delete("/api/tsn/gate-schedules/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := tsn.DeleteGateSchedule(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/tsn/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsn.Status())
	})
}

// jsonNum coerces a JSON-decoded interface{} (which is float64 for
// numbers via map[string]any) into a float64 with 0 fallback.
func jsonNum(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
