// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Tactical Communication System (TCS) routes — Go port of
// webservice/routes/tcs_routes.py.
//
// All endpoints return mock/demo data matching the GUI template shape.
// When the core TCS module is ported, the stubs will be replaced with
// live calls.
//
// 3GPP References:
//   TS 23.501 sec 5.33  — Support for non-public networks (NPN)
//   TS 23.304        — Proximity-based services in 5G
//   TS 23.287        — Application layer support for V2X services
//   TS 33.501 sec 6.9   — Security for NPN / tactical deployments
package app

import (
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// RegisterTCSRoutes wires /api/tcs/* endpoints.
func (s *Server) RegisterTCSRoutes() {
	r := s.Router

	// ── Status ──────────────────────────────────────────────────────
	r.Get("/api/tcs/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"node_id":        "NIB-ALPHA-01",
			"mode":           "full_nib",
			"ip":             "10.45.0.1",
			"uptime_s":       int(time.Now().Unix()) % 86400,
			"services":       []string{"AMF", "SMF", "UPF", "AUSF", "UDR", "PCF", "IMS", "MCX"},
			"peer_count":     3,
			"ue_count":       12,
			"mesh_protocol":  "OLSR-v2",
			"fallback_state": "CONNECTED",
		})
	})

	// ── Peer management ─────────────────────────────────────────────
	r.Get("/api/tcs/peers", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "peers": []map[string]any{
			{"node_id": "NIB-BRAVO-02", "ip": "10.45.0.2", "mode": "full_nib",
				"link_status": "active", "rsrp": -78, "etx": 1.1, "last_seen": nowISO(),
				"mesh_hops": 1, "lat": 34.053, "lon": -118.243},
			{"node_id": "GNB-CHARLIE-03", "ip": "10.45.0.3", "mode": "gnb_only",
				"link_status": "active", "rsrp": -92, "etx": 1.8, "last_seen": nowISO(),
				"mesh_hops": 1, "lat": 34.058, "lon": -118.250},
			{"node_id": "NIB-DELTA-04", "ip": "10.45.0.4", "mode": "full_nib",
				"link_status": "degraded", "rsrp": -105, "etx": 3.2, "last_seen": nowISO(),
				"mesh_hops": 2, "lat": 34.060, "lon": -118.235},
		}})
	})

	r.Post("/api/tcs/peers", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			NodeID string `json:"node_id"`
			IP     string `json:"ip"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.NodeID == "" || d.IP == "" {
			jsonError(w, "node_id and ip required", http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{"ok": true, "node_id": d.NodeID, "status": "added"})
	})

	r.Delete("/api/tcs/peers/{node_id}", func(w http.ResponseWriter, rq *http.Request) {
		nodeID := chi.URLParam(rq, "node_id")
		jsonReply(w, map[string]any{"ok": true, "node_id": nodeID, "status": "removed"})
	})

	// ── UE Location Tracking ────────────────────────────────────────
	r.Get("/api/tcs/ue-locations", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "ues": []map[string]any{
			{"supi": "001010000000001", "serving_node_id": "NIB-ALPHA-01",
				"serving_node_ip": "10.45.0.1", "ims_contact": "sip:001010000000001@ims.local",
				"mcx_endpoint": "mcptt:user1@mcx.local", "status": "ATTACHED",
				"last_updated": nowISO(), "lat": 34.052, "lon": -118.244},
			{"supi": "001010000000002", "serving_node_id": "NIB-BRAVO-02",
				"serving_node_ip": "10.45.0.2", "ims_contact": "sip:001010000000002@ims.local",
				"mcx_endpoint": "mcptt:user2@mcx.local", "status": "ATTACHED",
				"last_updated": nowISO(), "lat": 34.054, "lon": -118.245},
			{"supi": "001010000000003", "serving_node_id": "GNB-CHARLIE-03",
				"serving_node_ip": "10.45.0.3", "ims_contact": nil,
				"mcx_endpoint": nil, "status": "DETACHED",
				"last_updated": nowISO(), "lat": 34.057, "lon": -118.249},
		}})
	})

	// ── Mesh Routing ────────────────────────────────────────────────
	r.Get("/api/tcs/mesh/topology", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"nodes": []map[string]any{
				{"node_id": "NIB-ALPHA-01", "ip": "10.45.0.1", "mode": "full_nib",
					"lat": 34.050, "lon": -118.245, "services": []string{"AMF", "SMF", "UPF", "IMS", "MCX"}, "ue_count": 5},
				{"node_id": "NIB-BRAVO-02", "ip": "10.45.0.2", "mode": "full_nib",
					"lat": 34.053, "lon": -118.243, "services": []string{"AMF", "SMF", "UPF", "IMS"}, "ue_count": 4},
				{"node_id": "GNB-CHARLIE-03", "ip": "10.45.0.3", "mode": "gnb_only",
					"lat": 34.058, "lon": -118.250, "services": []string{"gNB"}, "ue_count": 3},
				{"node_id": "NIB-DELTA-04", "ip": "10.45.0.4", "mode": "full_nib",
					"lat": 34.060, "lon": -118.235, "services": []string{"AMF", "SMF", "UPF"}, "ue_count": 2},
			},
			"links": []map[string]any{
				{"from": "NIB-ALPHA-01", "to": "NIB-BRAVO-02", "quality": "good", "rsrp": -78, "etx": 1.1},
				{"from": "NIB-ALPHA-01", "to": "GNB-CHARLIE-03", "quality": "good", "rsrp": -85, "etx": 1.3},
				{"from": "NIB-BRAVO-02", "to": "GNB-CHARLIE-03", "quality": "degraded", "rsrp": -95, "etx": 2.1},
				{"from": "NIB-BRAVO-02", "to": "NIB-DELTA-04", "quality": "poor", "rsrp": -108, "etx": 3.5},
				{"from": "GNB-CHARLIE-03", "to": "NIB-DELTA-04", "quality": "degraded", "rsrp": -100, "etx": 2.8},
			},
		})
	})

	r.Get("/api/tcs/mesh/routes", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "routes": []map[string]any{
			{"destination": "NIB-BRAVO-02", "next_hop": "NIB-BRAVO-02",
				"path": []string{"NIB-ALPHA-01", "NIB-BRAVO-02"}, "cost_etx": 1.1, "hops": 1, "updated_at": nowISO()},
			{"destination": "GNB-CHARLIE-03", "next_hop": "GNB-CHARLIE-03",
				"path": []string{"NIB-ALPHA-01", "GNB-CHARLIE-03"}, "cost_etx": 1.3, "hops": 1, "updated_at": nowISO()},
			{"destination": "NIB-DELTA-04", "next_hop": "NIB-BRAVO-02",
				"path": []string{"NIB-ALPHA-01", "NIB-BRAVO-02", "NIB-DELTA-04"}, "cost_etx": 4.6, "hops": 2, "updated_at": nowISO()},
		}})
	})

	r.Get("/api/tcs/mesh/route/{dest_node_id}", func(w http.ResponseWriter, rq *http.Request) {
		dest := chi.URLParam(rq, "dest_node_id")
		jsonReply(w, map[string]any{
			"source": "NIB-ALPHA-01", "destination": dest,
			"path": []string{"NIB-ALPHA-01", "NIB-BRAVO-02", dest},
			"cost_etx": 2.5, "hops": 2, "computed_at": nowISO(),
		})
	})

	// ── Sync Status ─────────────────────────────────────────────────
	r.Get("/api/tcs/sync/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"local_version": 1847,
			"peers": []map[string]any{
				{"node_id": "NIB-BRAVO-02", "version": 1845, "status": "behind", "behind_by": 2},
				{"node_id": "GNB-CHARLIE-03", "version": 1847, "status": "in_sync", "behind_by": 0},
				{"node_id": "NIB-DELTA-04", "version": 1830, "status": "behind", "behind_by": 17},
			},
			"pending_changesets": 3,
		})
	})

	r.Get("/api/tcs/sync/history", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "history": []map[string]any{
			{"version": 1847, "timestamp": nowISO(), "source_node": "NIB-ALPHA-01",
				"table": "subscribers", "operation": "UPDATE", "summary": "UE status change"},
			{"version": 1846, "timestamp": nowISO(), "source_node": "NIB-BRAVO-02",
				"table": "pdu_sessions", "operation": "INSERT", "summary": "New PDU session"},
			{"version": 1845, "timestamp": nowISO(), "source_node": "NIB-ALPHA-01",
				"table": "subscribers", "operation": "INSERT", "summary": "New subscriber provisioned"},
		}})
	})

	r.Post("/api/tcs/sync/trigger", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "message": "Sync triggered", "timestamp": nowISO()})
	})

	// ── Inter-NIB Services ──────────────────────────────────────────
	r.Get("/api/tcs/inter-nib/calls", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "calls": []map[string]any{
			{"call_id": "call-001", "caller": "001010000000001@NIB-ALPHA-01",
				"callee": "001010000000002@NIB-BRAVO-02", "type": "voice",
				"duration_s": 142, "codec": "AMR-WB", "status": "active"},
		}})
	})

	r.Get("/api/tcs/inter-nib/trunks", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "trunks": []map[string]any{
			{"peer_node": "NIB-BRAVO-02", "peer_sip_uri": "sip:10.45.0.2:5060",
				"status": "active", "calls_active": 1},
			{"peer_node": "NIB-DELTA-04", "peer_sip_uri": "sip:10.45.0.4:5060",
				"status": "standby", "calls_active": 0},
		}})
	})

	r.Get("/api/tcs/inter-nib/mcx-groups", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "groups": []map[string]any{
			{"group_id": "mcptt-grp-alpha", "members": map[string]int{"NIB-ALPHA-01": 3, "NIB-BRAVO-02": 2},
				"routing_mode": "distributed", "active_talker": "001010000000001"},
			{"group_id": "mcptt-grp-command", "members": map[string]int{"NIB-ALPHA-01": 2, "NIB-DELTA-04": 1},
				"routing_mode": "centralized", "active_talker": nil},
		}})
	})

	// ── Fallback Status ─────────────────────────────────────────────
	r.Get("/api/tcs/fallback/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"current_mode": "full_nib",
			"central_core": map[string]any{"status": "connected", "latency_ms": 12, "last_heartbeat": nowISO()},
			"state":        "CONNECTED",
			"state_history": []map[string]any{
				{"state": "CONNECTED", "entered_at": nowISO(), "reason": "Initial boot"},
			},
		})
	})

	r.Post("/api/tcs/fallback/trigger", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"ok": true, "state": "FALLBACK", "message": "Fallback mode activated",
			"timestamp": nowISO(),
		})
	})

	r.Post("/api/tcs/fallback/recover", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"ok": true, "state": "RECOVERY", "message": "Recovery initiated",
			"timestamp": nowISO(),
		})
	})

	// ── Configuration ───────────────────────────────────────────────
	r.Get("/api/tcs/config", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"node_id":       "NIB-ALPHA-01",
			"node_ip":       "10.45.0.1",
			"mode":          "full_nib",
			"mesh_protocol": "OLSR-v2",
			"v2x_pools":     map[string]int{"pool1_pct": 40, "pool2_pct": 35, "pool3_pct": 25},
			"security":      map[string]any{"hmac_key": "********", "encryption_enabled": true},
			"peers": []map[string]string{
				{"node_id": "NIB-BRAVO-02", "ip": "10.45.0.2"},
				{"node_id": "GNB-CHARLIE-03", "ip": "10.45.0.3"},
				{"node_id": "NIB-DELTA-04", "ip": "10.45.0.4"},
			},
		})
	})

	r.Post("/api/tcs/config", func(w http.ResponseWriter, rq *http.Request) {
		// Accept and acknowledge; TCS module not yet ported. Body is
		// drained so the client can see the 200 reply cleanly.
		_, _ = io.Copy(io.Discard, rq.Body)
		jsonReply(w, map[string]any{"ok": true, "message": "Configuration saved", "timestamp": nowISO()})
	})
}
