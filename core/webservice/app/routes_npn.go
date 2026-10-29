// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_npn.go — REST surface for Non-Public Networks.
//
// Wires `security/npn` to /api/npn/*. The package owns NPN network
// CRUD, Closed Access Group (CAG) membership, the SNPN admission
// gate, and the per-IMSI access audit log. This surface drives
// `templates/npn.html`.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 23.501 §5.30   Non-Public Networks (umbrella).
//   - TS 23.501 §5.30.2 Closed Access Group — CAG-ID is a 32-bit
//                        identifier (8 hex digits).
//   - TS 23.501 §5.30.3 SNPN identification — (PLMN, NID) pair.
//   - TS 23.502 §4.2.2.2.3 SNPN registration — drives the admission
//                          gate that AuthenticateSNPN realises.
//   - TS 33.501 §6.1.4   SNPN authentication (the subscriber-side
//                        anchor for the admission decision).
//
// All response shapes match `templates/npn.html`: list endpoints
// return arrays / `{members: [...]}`; mutators echo the new row
// (CreateNPN echoes the full record so the panel doesn't need a
// follow-up GET).
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/security/npn"
)

// validNPNType gates the operator vocabulary at the route layer; the
// schema CHECK rejects bad values too but a clean 400 beats 500.
var validNPNType = map[string]struct{}{"SNPN": {}, "PNI-NPN": {}}

func (s *Server) registerNPNRoutes() {
	r := s.Router

	// ── Stats / dashboard ────────────────────────────────────────
	r.Get("/api/npn/stats", func(w http.ResponseWriter, _ *http.Request) {
		st := npn.GetNPNStatus()
		// Split count by type for the GUI cards.
		nets, _ := npn.ListNPNs()
		snpn, pni := 0, 0
		for _, n := range nets {
			switch v, _ := n["npn_type"].(string); v {
			case "SNPN":
				snpn++
			case "PNI-NPN":
				pni++
			}
		}
		st["snpn_count"] = snpn
		st["pni_npn_count"] = pni
		st["cag_groups"] = st["cag_count"]
		st["authorized_ues"] = st["member_count"]
		jsonReply(w, st)
	})

	// ── NPN networks (TS 23.501 §5.30) ───────────────────────────
	r.Get("/api/npn/networks", func(w http.ResponseWriter, _ *http.Request) {
		nets, err := npn.ListNPNs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if nets == nil {
			nets = []map[string]interface{}{}
		}
		jsonReply(w, nets)
	})

	r.Get("/api/npn/networks/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		n, err := npn.GetNPN(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n == nil {
			jsonError(w, "npn not found", http.StatusNotFound)
			return
		}
		jsonReply(w, n)
	})

	r.Post("/api/npn/networks", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name    string `json:"name"`
			NpnType string `json:"npn_type"`
			Plmn    string `json:"plmn"`
			Nid     string `json:"nid"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		if d.Plmn == "" {
			jsonError(w, "plmn required", http.StatusBadRequest)
			return
		}
		if d.NpnType == "" {
			d.NpnType = "PNI-NPN"
		}
		if _, ok := validNPNType[d.NpnType]; !ok {
			jsonError(w, "npn_type must be one of SNPN|PNI-NPN",
				http.StatusBadRequest)
			return
		}
		// SNPN requires a NID per TS 23.501 §5.30.3 (SNPN-id =
		// PLMN ID + NID). PNI-NPN doesn't carry a NID.
		if d.NpnType == "SNPN" && d.Nid == "" {
			jsonError(w, "nid required for SNPN networks (TS 23.501 §5.30.3)",
				http.StatusBadRequest)
			return
		}
		id, err := npn.CreateNPN(d.Name, d.NpnType, d.Plmn, d.Nid)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Echo the full record so callers can verify npn_type / nid
		// without an extra GET.
		if rec, gerr := npn.GetNPN(id); gerr == nil && rec != nil {
			jsonReply(w, rec)
			return
		}
		jsonReply(w, map[string]any{"id": id})
	})

	r.Delete("/api/npn/networks/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := npn.DeleteNPN(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── CAG (TS 23.501 §5.30.2 — 32-bit / 8-hex CAG-ID) ──────────
	r.Get("/api/npn/cag", func(w http.ResponseWriter, rq *http.Request) {
		var npnID int64
		if v := rq.URL.Query().Get("npn_id"); v != "" {
			npnID, _ = strconv.ParseInt(v, 10, 64)
		}
		cags, err := npn.ListCAGs(npnID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cags == nil {
			cags = []map[string]interface{}{}
		}
		jsonReply(w, cags)
	})

	r.Post("/api/npn/cag", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			CagID       string `json:"cag_id"`
			NpnID       int64  `json:"npn_id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// 8-hex format check happens inside CreateCAG (ValidateCAGID),
		// but we surface it as 400 here too — lets the route signal the
		// vocabulary contract without parsing an error string.
		if !npn.ValidateCAGID(d.CagID) {
			jsonError(w, "cag_id must be 8 hex digits (TS 23.501 §5.30.2)",
				http.StatusBadRequest)
			return
		}
		if d.NpnID == 0 {
			jsonError(w, "npn_id required", http.StatusBadRequest)
			return
		}
		if d.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		id, err := npn.CreateCAG(d.CagID, d.NpnID, d.Name, d.Description)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"id": id, "cag_id": d.CagID})
	})

	r.Delete("/api/npn/cag/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := npn.DeleteCAG(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── CAG membership ───────────────────────────────────────────
	r.Get("/api/npn/cag/{id}/members", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		members, err := npn.ListMembers(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if members == nil {
			members = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"members": members})
	})

	r.Post("/api/npn/cag/{id}/members", func(w http.ResponseWriter, rq *http.Request) {
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
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		mid, err := npn.AddMember(id, d.IMSI)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "member_id": mid,
			"cag_row_id": id, "imsi": d.IMSI})
	})

	r.Delete("/api/npn/cag/{id}/members/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		imsi := chi.URLParam(rq, "imsi")
		if err := npn.RemoveMember(id, imsi); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": imsi})
	})

	// ── SNPN admission gate (TS 33.501 §6.1.4) ───────────────────
	// Body {imsi, cag_id, nid}; response carries the same shape the
	// AMF consumes in the registration path (allowed/reason/cag_id/nid).
	// Each call writes a row to npn_access_log.
	r.Post("/api/npn/authenticate", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI  string `json:"imsi"`
			CagID string `json:"cag_id"`
			Nid   string `json:"nid"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		jsonReply(w, npn.AuthenticateSNPN(d.IMSI, d.CagID, d.Nid))
	})

	// Legacy alias for the GUI's older /api/npn/authorize path.
	r.Post("/api/npn/authorize", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI  string `json:"imsi"`
			CagID string `json:"cag_id"`
			Nid   string `json:"nid"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		res := npn.AuthenticateSNPN(d.IMSI, d.CagID, d.Nid)
		// Older panel expected `authorized` rather than `allowed` —
		// alias both so the new + legacy clients share one route.
		res["authorized"] = res["allowed"]
		jsonReply(w, res)
	})

	// ── Access audit log (per-IMSI admission decisions) ──────────
	r.Get("/api/npn/access-log", func(w http.ResponseWriter, rq *http.Request) {
		limit := 100
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		imsiFilter := rq.URL.Query().Get("imsi")
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		query := `SELECT id, imsi, npn_id, cag_id, action, reason, created_at
				  FROM npn_access_log`
		args := []any{}
		if imsiFilter != "" {
			query += ` WHERE imsi=?`
			args = append(args, imsiFilter)
		}
		query += ` ORDER BY id DESC LIMIT ?`
		args = append(args, limit)
		rr, err := db.Query(query, args...)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rr.Close()
		out := []map[string]any{}
		for rr.Next() {
			var id int64
			var imsi, action string
			var npnID, cagID *int64
			var reason, createdAt *string
			if err := rr.Scan(&id, &imsi, &npnID, &cagID, &action, &reason, &createdAt); err != nil {
				continue
			}
			out = append(out, map[string]any{
				"id":         id,
				"imsi":       imsi,
				"npn_id":     npnID,
				"cag_id":     cagID,
				"action":     action,
				"reason":     reason,
				"created_at": createdAt,
			})
		}
		jsonReply(w, map[string]any{"items": out})
	})
}
