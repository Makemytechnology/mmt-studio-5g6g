// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_wifi_offload.go — REST surface for the operator-side
// non-3GPP (WLAN) access policy + admission probe.
//
// This is the operator's policy + admission view. The IKEv2 + EAP-5G
// + ESP datapath itself lives in nf/n3iwf/ and is exposed by
// routes_n3iwf.go. The two are complementary surfaces:
//
//   - routes_n3iwf.go      → live N3IWF NF state (sessions, IKE SAs,
//                            child SA stats, ESP↔GTP-U bridge).
//   - routes_wifi_offload.go (this file) → per-DNN access policy,
//                            attached-UE table, admission outcome
//                            audit log.
//
// Spec anchors (mirrored from access/wifi_offload/wifi_offload.go):
//
//   - TS 23.501 §4.2.7    Reference points (incl. N3 / Y1 / Y2 for
//                         non-3GPP access).
//   - TS 23.501 §4.2.8    Support of non-3GPP access — umbrella
//                         covering trusted + untrusted WLAN.
//   - TS 23.501 §4.2.8.5  TWIF (TODO — wireline / non-NAS UEs).
//   - TS 23.501 §5.10.2   Security Model for non-3GPP access.
//   - TS 23.501 §6.2.9    N3IWF (untrusted-WLAN gateway).
//   - TS 23.501 §6.2.9A   TNGF (trusted-WLAN gateway).
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/access/wifi_offload"
)

func (s *Server) registerWiFiOffloadRoutes() {
	r := s.Router

	// ── Stats / status (drives panel headline) ───────────────────
	r.Get("/api/wifi-offload/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, wifi_offload.GetStats())
	})

	// ── Per-DNN policy CRUD (TS 23.501 §4.2.8) ───────────────────
	r.Get("/api/wifi-offload/policies", func(w http.ResponseWriter, _ *http.Request) {
		list, err := wifi_offload.ListPolicies()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/wifi-offload/policies/{dnn}", func(w http.ResponseWriter, rq *http.Request) {
		dnn := chi.URLParam(rq, "dnn")
		pol, err := wifi_offload.GetPolicy(dnn)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pol == nil {
			jsonError(w, "policy not found", http.StatusNotFound)
			return
		}
		jsonReply(w, pol)
	})

	r.Post("/api/wifi-offload/policies", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			DNN          string `json:"dnn"`
			AccessType   string `json:"access_type"`
			OffloadPref  string `json:"offload_pref"`
			Enabled      *bool  `json:"enabled"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// SetPolicy enforces the §4.2.8 enums (access_type ∈
		// {untrusted, trusted, wireline}; offload_pref ∈ §6.2.9 set).
		// A blank request → 400 with the package error message.
		en := true
		if d.Enabled != nil {
			en = *d.Enabled
		}
		if err := wifi_offload.SetPolicy(d.DNN, d.AccessType, d.OffloadPref, en); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok":           true,
			"dnn":          d.DNN,
			"access_type":  d.AccessType,
			"offload_pref": d.OffloadPref,
			"enabled":      en,
		})
	})

	r.Delete("/api/wifi-offload/policies/{dnn}", func(w http.ResponseWriter, rq *http.Request) {
		dnn := chi.URLParam(rq, "dnn")
		if err := wifi_offload.DeletePolicy(dnn); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Per spec note: removing a policy lets the DNN fall back to
		// the §6.2.9 default (untrusted N3IWF, 5g_first).
		jsonReply(w, map[string]any{"ok": true, "dnn": dnn,
			"fallback": "untrusted/5g_first"})
	})

	// ── Admission probe (operator-callable; no state mutation) ───
	r.Post("/api/wifi-offload/admission", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI       string `json:"imsi"`
			DNN        string `json:"dnn"`
			AccessType string `json:"access_type"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.DNN == "" {
			jsonError(w, "imsi and dnn required", http.StatusBadRequest)
			return
		}
		// CheckOffload logs rejects to the audit table; allows are
		// quiet. The route returns the structured result either way.
		res := wifi_offload.CheckOffload(d.IMSI, d.DNN, d.AccessType)
		// 200 + allowed=false is the canonical "spec-shaped deny"
		// that matches existing N3IWF surface conventions.
		jsonReply(w, res)
	})

	// ── Attached UE table (in-flight) ────────────────────────────
	r.Get("/api/wifi-offload/attached", func(w http.ResponseWriter, _ *http.Request) {
		list, err := wifi_offload.ListAttachedUEs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, list)
	})

	r.Post("/api/wifi-offload/attached", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI       string `json:"imsi"`
			AccessType string `json:"access_type"`
			N3IWFID    string `json:"n3iwf_id"`
			InnerIP    string `json:"inner_ip"`
			OuterIP    string `json:"outer_ip"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		// Default to the safe path: untrusted via N3IWF.
		if d.AccessType == "" {
			d.AccessType = wifi_offload.AccessUntrusted
		}
		if err := wifi_offload.AttachUE(d.IMSI, d.AccessType, d.N3IWFID, d.InnerIP, d.OuterIP); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{
			"ok":          true,
			"imsi":        d.IMSI,
			"access_type": d.AccessType,
		})
	})

	r.Delete("/api/wifi-offload/attached", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		access := rq.URL.Query().Get("access_type")
		if imsi == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		if access == "" {
			access = wifi_offload.AccessUntrusted
		}
		if err := wifi_offload.DetachUE(imsi, access); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "access_type": access})
	})

	r.Get("/api/wifi-offload/attached/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		access := rq.URL.Query().Get("access_type")
		if access == "" {
			access = wifi_offload.AccessUntrusted
		}
		jsonReply(w, map[string]any{
			"imsi":        imsi,
			"access_type": access,
			"attached":    wifi_offload.IsAttached(imsi, access),
		})
	})

	// ── Audit log (rejected attempts, attaches, detaches) ────────
	r.Get("/api/wifi-offload/audit", func(w http.ResponseWriter, rq *http.Request) {
		limit := 100
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		list, err := wifi_offload.GetAuditLog(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"entries": list, "count": len(list)})
	})
}
