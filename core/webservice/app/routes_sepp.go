// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_sepp.go — REST surface for the SEPP operator policy
// (TS 29.573 + TS 33.501 §13.1).
//
// Wires `security/sepp_policy` to /api/sepp/*. The SEPP itself
// (`infra/roaming/sepp`) is a transparent N32-f reverse proxy with
// TLS termination; this surface manages the **policy state** the
// proxy consults: peer-PLMN allow-list, topology-hiding rules,
// admission gate, and the N32 audit log.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 29.573 §5.2  N32-c control plane (peer capability negotiation,
//                     TLS handshake) — peer SAN drives the allow-list.
//   - TS 29.573 §5.3  N32-f forwarding plane (HTTP reverse proxy with
//                     message filtering / topology hiding).
//   - TS 33.501 §13.1 5GC SBI security at the PLMN border (mutual TLS).
//   - TS 23.501 §5.36 Roaming architecture (this is the inter-PLMN
//                     reference point this surface guards).
//
// Deferred:
//
//   - TS 29.573 §5.3 PRINS (PLMN-internal Roaming INformation
//                    Stripping) is a TODO at the proxy site; the
//                    policy state lives here.
//
// Response shapes are `{ok: true, ...}` envelopes throughout.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	infraSEPP "github.com/mmt/mmt-studio-core/infra/roaming/sepp"
	"github.com/mmt/mmt-studio-core/security/sepp_policy"
)

func (s *Server) registerSEPPRoutes() {
	r := s.Router

	// ── Status / dashboard ────────────────────────────────────────
	r.Get("/api/sepp/status", func(w http.ResponseWriter, _ *http.Request) {
		// Compose the policy stats with the proxy's runtime status
		// (running / addr / TLS) so operators see both layers in one
		// fetch.
		st := sepp_policy.GetStats()
		st["proxy"] = infraSEPP.Status()
		jsonReply(w, map[string]any{"ok": true, "status": st})
	})

	r.Get("/api/sepp/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": sepp_policy.GetStats()})
	})

	// ── Peer-PLMN allow-list (TS 33.501 §13.1) ────────────────────
	r.Get("/api/sepp/peers", func(w http.ResponseWriter, rq *http.Request) {
		status := rq.URL.Query().Get("status")
		if status != "" && !sepp_policy.ValidStatus(status) {
			jsonError(w, "status must be one of active|inactive|blocked",
				http.StatusBadRequest)
			return
		}
		list, err := sepp_policy.ListPeers(status)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []sepp_policy.Peer{}
		}
		jsonReply(w, map[string]any{"ok": true, "peers": list})
	})

	r.Post("/api/sepp/peers", func(w http.ResponseWriter, rq *http.Request) {
		var d sepp_policy.Peer
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.PlmnID == "" {
			jsonError(w, "plmn_id required (TS 33.501 §13.1)",
				http.StatusBadRequest)
			return
		}
		if d.FQDN == "" {
			jsonError(w, "fqdn required", http.StatusBadRequest)
			return
		}
		if d.Status != "" && !sepp_policy.ValidStatus(d.Status) {
			jsonError(w, "status must be one of active|inactive|blocked",
				http.StatusBadRequest)
			return
		}
		row, err := sepp_policy.CreatePeer(d)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"ok": true, "peer": row})
	})

	r.Get("/api/sepp/peers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		p, err := sepp_policy.GetPeer(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if p == nil {
			jsonError(w, "peer not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "peer": p})
	})

	r.Patch("/api/sepp/peers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var fields map[string]interface{}
		if !decodeJSON(w, rq, &fields) {
			return
		}
		row, err := sepp_policy.UpdatePeer(id, fields)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if row == nil {
			jsonError(w, "peer not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "peer": row})
	})

	r.Delete("/api/sepp/peers/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		ok, err := sepp_policy.DeletePeer(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonError(w, "peer not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Topology-hiding rules (TS 29.573 §5.3.x) ──────────────────
	r.Get("/api/sepp/topology-hiding", func(w http.ResponseWriter, _ *http.Request) {
		list, err := sepp_policy.ListTopologyHiding()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []sepp_policy.TopologyHiding{}
		}
		jsonReply(w, map[string]any{"ok": true, "rules": list})
	})

	r.Post("/api/sepp/topology-hiding", func(w http.ResponseWriter, rq *http.Request) {
		var d sepp_policy.TopologyHiding
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.PeerID == 0 {
			jsonError(w, "peer_id required", http.StatusBadRequest)
			return
		}
		// Require the peer exists — FK CASCADE drops orphaned rules
		// at delete time, but we'd rather catch a typo at create time.
		if p, err := sepp_policy.GetPeer(d.PeerID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		} else if p == nil {
			jsonError(w, "peer not found", http.StatusBadRequest)
			return
		}
		row, err := sepp_policy.UpsertTopologyHiding(d)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "rule": row})
	})

	r.Get("/api/sepp/topology-hiding/{peer_id}", func(w http.ResponseWriter, rq *http.Request) {
		peerID, err := strconv.ParseInt(chi.URLParam(rq, "peer_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid peer_id", http.StatusBadRequest)
			return
		}
		row, err := sepp_policy.GetTopologyHiding(peerID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			jsonError(w, "no rule for this peer (default-policy applies)",
				http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "rule": row})
	})

	r.Delete("/api/sepp/topology-hiding/{peer_id}", func(w http.ResponseWriter, rq *http.Request) {
		peerID, err := strconv.ParseInt(chi.URLParam(rq, "peer_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid peer_id", http.StatusBadRequest)
			return
		}
		if err := sepp_policy.DeleteTopologyHiding(peerID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "peer_id": peerID})
	})

	// ── Admission gate ───────────────────────────────────────────
	r.Post("/api/sepp/check-access", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			PlmnID string `json:"plmn_id"`
			Path   string `json:"path"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.PlmnID == "" {
			jsonError(w, "plmn_id required", http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"access": sepp_policy.CheckPeerAccess(d.PlmnID, d.Path),
		})
	})

	// ── N32 audit log ────────────────────────────────────────────
	r.Get("/api/sepp/log", func(w http.ResponseWriter, rq *http.Request) {
		peer := rq.URL.Query().Get("peer")
		action := rq.URL.Query().Get("action")
		direction := rq.URL.Query().Get("direction")
		limit := 100
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := sepp_policy.ListN32Log(peer, action, direction, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "log": list})
	})

	// Synthetic raise (drills, operator-initiated tests).
	r.Post("/api/sepp/log", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			PeerPLMN   string `json:"peer_plmn"`
			Direction  string `json:"direction"`
			Path       string `json:"path"`
			Method     string `json:"method"`
			StatusCode int    `json:"status_code"`
			LatencyMs  int    `json:"latency_ms"`
			Action     string `json:"action"`
			Reason     string `json:"reason"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Path == "" {
			jsonError(w, "path required", http.StatusBadRequest)
			return
		}
		if d.Direction == "" {
			d.Direction = "inbound"
		}
		switch d.Direction {
		case "inbound", "outbound":
		default:
			jsonError(w, "direction must be inbound|outbound",
				http.StatusBadRequest)
			return
		}
		if d.Action == "" {
			d.Action = "forwarded"
		}
		switch d.Action {
		case "forwarded", "rejected", "rewritten":
		default:
			jsonError(w, "action must be forwarded|rejected|rewritten",
				http.StatusBadRequest)
			return
		}
		sepp_policy.LogN32(d.PeerPLMN, d.Direction, d.Path, d.Method,
			d.StatusCode, d.LatencyMs, d.Action, d.Reason)
		jsonReply(w, map[string]any{"ok": true})
	})
}
